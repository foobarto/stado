package runtime

// Conversation persistence — writes each user/assistant/tool turn to
// `<worktree>/.stado/conversation.jsonl` so the TUI can pick up where
// it left off when the user re-attaches. Newline-delimited JSON per
// message: one line = one agent.Message with its Content blocks
// encoded verbatim.
//
// Why JSONL under .stado/ rather than in the git sidecar: the trace
// ref records tool-call audit (who called what, what the result
// hashed to) but strips the content — by design, to avoid rewriting
// history when the same bytes get re-generated. The conversation is
// different: it IS the bytes the agent loop sees, and losing it on
// restart defeats attach's point. .stado/ stays in the worktree so
// it rides along with fork + materialise but doesn't conflict with
// user files in the repo.
//
// Append-only during a live session: messages and compaction markers are
// appended, never rewritten, matching DESIGN's conversation-history
// invariant. Fresh child sessions may be seeded with a new log, but an
// established non-empty log refuses replacement.

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/foobarto/stado/internal/compact"
	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/google/uuid"
)

// ConversationFile is the per-worktree conversation log path
// (relative to the worktree root). Readable by `stado session show`
// for debugging + by runtime.OpenSession when resuming.
const ConversationFile = ".stado/conversation.jsonl"

const (
	maxConversationLogBytes    int64 = 64 << 20
	maxConversationRecordBytes int64 = 8 << 20
)

var conversationAppendMu sync.Mutex

// ConversationCompaction is an append-only log event recording that
// subsequent resumes should use a compacted conversation view. The raw
// messages before this line remain in conversation.jsonl; LoadConversation
// folds the view to Summary while raw exports can still replay the full log.
type ConversationCompaction struct {
	Type       string `json:"type"`
	Summary    string `json:"summary"`
	FromTurn   int    `json:"from_turn"`
	ToTurn     int    `json:"to_turn"`
	TurnsTotal int    `json:"turns_total"`
	By         string `json:"by,omitempty"`
	At         string `json:"at,omitempty"`
	TreeSHA    string `json:"tree_sha,omitempty"`
	TraceSHA   string `json:"trace_sha,omitempty"`
	RawLogSHA  string `json:"raw_log_sha,omitempty"`
}

// AppendMessage appends a single agent.Message to the conversation
// log under worktree. Creates the `.stado` dir + file on first call.
// Best-effort atomicity: O_APPEND|O_CREATE with a fresh fd per call.
// In-process appenders serialize so a crash-shortened tail can be framed off
// before a later complete record is appended.
func AppendMessage(worktree string, msg agent.Message) error {
	return appendConversationRecord(worktree, msg)
}

// AppendMessagesFrom appends msgs[start:] to the conversation log and
// returns the next persisted view offset. If appending fails part-way
// through, the returned offset accounts for the messages already written
// so callers can avoid duplicating them on a later retry.
func AppendMessagesFrom(worktree string, msgs []agent.Message, start int) (int, error) {
	if start < 0 || start > len(msgs) {
		start = 0
	}
	for i := start; i < len(msgs); i++ {
		if err := AppendMessage(worktree, msgs[i]); err != nil {
			return i, err
		}
	}
	return len(msgs), nil
}

// AppendCompaction appends a compaction event without rewriting prior
// conversation lines. On resume, LoadConversation applies the event to
// produce the compacted prompt view; raw JSONL exports still include the
// complete pre-compaction trail.
func AppendCompaction(worktree string, ev ConversationCompaction) error {
	ev.Type = "compaction"
	if ev.At == "" {
		ev.At = time.Now().UTC().Format(time.RFC3339)
	}
	return appendConversationRecord(worktree, ev)
}

func appendConversationRecord(worktree string, v any) error {
	conversationAppendMu.Lock()
	defer conversationAppendMu.Unlock()

	root, name, err := conversationRoot(worktree, true)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	path := filepath.Join(worktree, ConversationFile)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // preserve HTML/tags in message bodies verbatim
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("conversation: encode: %w", err)
	}
	if int64(buf.Len()) > maxConversationRecordBytes {
		return fmt.Errorf("conversation: record exceeds %d bytes: %s", maxConversationRecordBytes, path)
	}
	needsSeparator, err := conversationNeedsSeparator(root, name, path)
	if err != nil {
		return err
	}
	payload := buf.Bytes()
	if needsSeparator {
		// Preserve a crash-shortened tail append-only, but terminate its frame so
		// the next complete JSON record remains independently recoverable.
		payload = append([]byte{'\n'}, payload...)
	}
	recordBytes := int64(len(payload))
	f, err := openConversationAppendFile(root, name, recordBytes, path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if n, err := f.Write(payload); err != nil {
		return fmt.Errorf("conversation: append %s: %w", path, err)
	} else if n != len(payload) {
		return fmt.Errorf("conversation: append %s: %w", path, io.ErrShortWrite)
	}
	return nil
}

func conversationNeedsSeparator(root *os.Root, name, displayPath string) (bool, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("conversation: inspect append framing %s: %w", displayPath, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("conversation log is a symlink: %s", displayPath)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("conversation log is not a regular file: %s", displayPath)
	}
	f, err := root.Open(name)
	if err != nil {
		return false, fmt.Errorf("conversation: inspect append framing %s: %w", displayPath, err)
	}
	defer func() { _ = f.Close() }()
	openedInfo, err := f.Stat()
	if err != nil {
		return false, fmt.Errorf("conversation: inspect append framing %s: %w", displayPath, err)
	}
	if !os.SameFile(info, openedInfo) {
		return false, fmt.Errorf("conversation log changed while opening: %s", displayPath)
	}
	if openedInfo.Size() == 0 {
		return false, nil
	}
	if _, err := f.Seek(-1, io.SeekEnd); err != nil {
		return false, fmt.Errorf("conversation: inspect append framing %s: %w", displayPath, err)
	}
	var last [1]byte
	if _, err := io.ReadFull(f, last[:]); err != nil {
		return false, fmt.Errorf("conversation: inspect append framing %s: %w", displayPath, err)
	}
	return last[0] != '\n', nil
}

func openConversationAppendFile(root *os.Root, name string, appendBytes int64, displayPath string) (*os.File, error) {
	if appendBytes > maxConversationLogBytes {
		return nil, fmt.Errorf("conversation log exceeds %d bytes: %s", maxConversationLogBytes, displayPath)
	}
	for range 2 {
		info, err := root.Lstat(name)
		switch {
		case errors.Is(err, os.ErrNotExist):
			f, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_APPEND|os.O_WRONLY, 0o600)
			if errors.Is(err, os.ErrExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("conversation: open %s: %w", displayPath, err)
			}
			return f, nil
		case err != nil:
			return nil, fmt.Errorf("conversation: stat %s: %w", displayPath, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("conversation log is a symlink: %s", displayPath)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("conversation log is not a regular file: %s", displayPath)
		}
		if info.Size()+appendBytes > maxConversationLogBytes {
			return nil, fmt.Errorf("conversation log exceeds %d bytes: %s", maxConversationLogBytes, displayPath)
		}
		f, err := root.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("conversation: open %s: %w", displayPath, err)
		}
		openedInfo, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("conversation: stat %s: %w", displayPath, err)
		}
		if !openedInfo.Mode().IsRegular() {
			_ = f.Close()
			return nil, fmt.Errorf("conversation log is not a regular file: %s", displayPath)
		}
		if !os.SameFile(info, openedInfo) {
			_ = f.Close()
			return nil, fmt.Errorf("conversation log changed while opening: %s", displayPath)
		}
		if openedInfo.Size()+appendBytes > maxConversationLogBytes {
			_ = f.Close()
			return nil, fmt.Errorf("conversation log exceeds %d bytes: %s", maxConversationLogBytes, displayPath)
		}
		return f, nil
	}
	return nil, fmt.Errorf("conversation log changed while opening: %s", displayPath)
}

// LoadConversation reads every message from worktree's conversation
// log and returns them in order. Missing file → (nil, nil) so
// callers can treat absence as "fresh session" without a special
// case. Partial files (e.g. killed mid-write) are read up to the
// last valid line; an error on a trailing line is silently ignored
// because losing one message is better than refusing to boot.
func LoadConversation(worktree string) ([]agent.Message, error) {
	if worktree == "" {
		return nil, nil
	}
	root, name, err := conversationRoot(worktree, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	path := filepath.Join(worktree, ConversationFile)
	f, err := root.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("conversation: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	return decodeMessages(f)
}

// RawConversationLog returns the raw append-only JSONL bytes. It opens the log
// through the worktree root so raw export paths stay confined to `.stado`.
func RawConversationLog(worktree string) ([]byte, error) {
	if worktree == "" {
		return nil, errors.New("conversation: worktree required")
	}
	root, name, err := conversationRoot(worktree, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	path := filepath.Join(worktree, ConversationFile)
	data, err := workdirpath.NewRootResolver(root).ReadFileLimited(name, maxConversationLogBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("conversation: read %s: %w", path, err)
	}
	return data, nil
}

// ConversationToolInvocationCount returns the number of tool calls in the
// append-only conversation evidence. Unlike LoadConversation's replay view,
// this count never decreases when a compaction marker replaces old messages
// with a summary, so it can seed stable trajectory invocation ordinals after
// compaction or process restart.
func ConversationToolInvocationCount(worktree string) (int, error) {
	raw, err := RawConversationLog(worktree)
	if err != nil {
		return 0, err
	}
	_, invocations, err := decodeConversation(bytes.NewReader(raw))
	return invocations, err
}

// WriteConversation replaces the on-disk conversation log atomically
// with the given message slice only when the log is absent or empty.
// Live sessions must use AppendMessage / AppendCompaction so the raw
// log remains append-only. This helper exists for seeding fresh child
// sessions and tests, not for rewriting established history.
//
// The write is tmp+rename so a crash mid-write can't leave a
// truncated conversation. A missing `.stado/` dir is created on the
// fly — symmetric with AppendMessage.
func WriteConversation(worktree string, msgs []agent.Message) error {
	root, name, err := conversationRoot(worktree, true)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	final := filepath.Join(worktree, ConversationFile)
	if info, err := root.Stat(name); err == nil && info.Size() > 0 {
		return fmt.Errorf("conversation: refusing to replace non-empty append-only log %s", final)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("conversation: stat %s: %w", final, err)
	}
	tmp := "." + name + "." + uuid.NewString() + ".tmp"
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("conversation: open tmp: %w", err)
	}
	keepTmp := false
	defer func() {
		if !keepTmp {
			_ = root.Remove(tmp)
		}
	}()
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	for i, m := range msgs {
		if err := enc.Encode(m); err != nil {
			_ = f.Close()
			return fmt.Errorf("conversation: encode msg %d: %w", i, err)
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("conversation: sync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := root.Rename(tmp, name); err != nil {
		return err
	}
	keepTmp = true
	return nil
}

// ConversationLogSHA returns a sha256 digest of the raw append-only JSONL
// bytes. Missing conversation logs hash as the empty byte sequence so a
// compaction marker can still bind "nothing before this event" precisely.
func ConversationLogSHA(worktree string) (string, error) {
	data, err := RawConversationLog(worktree)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func conversationRoot(worktree string, createDir bool) (*os.Root, string, error) {
	if worktree == "" {
		return nil, "", errors.New("conversation: worktree required")
	}
	workRoot, err := workdirpath.NewUserConfigResolver().OpenRoot(worktree)
	if err != nil {
		return nil, "", fmt.Errorf("conversation: open worktree %s: %w", worktree, err)
	}
	defer func() { _ = workRoot.Close() }()
	if createDir {
		if err := workdirpath.NewRootResolver(workRoot).MkdirAll(".stado", 0o700); err != nil {
			return nil, "", fmt.Errorf("conversation: mkdir %s: %w", filepath.Join(worktree, ".stado"), err)
		}
	}
	root, err := workRoot.OpenRoot(".stado")
	if err != nil {
		return nil, "", fmt.Errorf("conversation: open %s: %w", filepath.Join(worktree, ".stado"), err)
	}
	return root, filepath.Base(ConversationFile), nil
}

func decodeMessages(r io.Reader) ([]agent.Message, error) {
	msgs, _, err := decodeConversation(r)
	return msgs, err
}

func decodeConversation(r io.Reader) ([]agent.Message, int, error) {
	var msgs []agent.Message
	toolInvocations := 0
	appendMessages := func(messages ...agent.Message) {
		for _, message := range messages {
			for _, block := range message.Content {
				if block.ToolUse != nil {
					toolInvocations++
				}
			}
		}
		msgs = append(msgs, messages...)
	}
	deliveryIDs := make(map[string]struct{})
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), int(maxConversationRecordBytes))
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var probe struct {
			Type   string `json:"type"`
			Schema string `json:"schema"`
		}
		if err := json.Unmarshal(line, &probe); err == nil {
			switch probe.Type {
			case "compaction":
				var ev ConversationCompaction
				if err := json.Unmarshal(line, &ev); err != nil {
					break
				}
				if ev.Summary != "" {
					msgs = compact.ReplaceMessages(ev.Summary)
				}
				continue
			case "message":
				if probe.Schema == conversationDeliverySchemaV1 {
					delivery, err := strictDecodeConversationDelivery(line)
					if err != nil {
						return msgs, toolInvocations, err
					}
					if _, duplicate := deliveryIDs[delivery.DeliveryID]; duplicate {
						return msgs, toolInvocations, errors.New("conversation delivery: duplicate delivery ID evidence")
					}
					deliveryIDs[delivery.DeliveryID] = struct{}{}
					appendMessages(delivery.Messages...)
					continue
				}
				if probe.Schema != "" {
					// A future structured receiver record is not an ordinary empty
					// message. Ignore it without normalizing its evidence shape.
					continue
				}
				var wrapped struct {
					Message agent.Message `json:"message"`
				}
				if err := json.Unmarshal(line, &wrapped); err != nil {
					break
				}
				appendMessages(wrapped.Message)
				continue
			}
		}
		var m agent.Message
		if err := json.Unmarshal(line, &m); err != nil {
			// A crash-shortened tail may have been framed off by a later append.
			// Ignore only this invalid JSON frame and continue; recognized
			// structured records above still fail closed on schema corruption.
			continue
		}
		if m.Role == "" && len(m.Content) == 0 {
			// Unknown future event type. Preserve forward compatibility by
			// ignoring it rather than replaying an empty message.
			continue
		}
		appendMessages(m)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return msgs, toolInvocations, err
	}
	return msgs, toolInvocations, nil
}
