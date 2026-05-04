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
// Concurrent appenders serialise at the OS-level append guarantee.
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
	recordBytes := int64(buf.Len())
	if recordBytes > maxConversationRecordBytes {
		return fmt.Errorf("conversation: record exceeds %d bytes: %s", maxConversationRecordBytes, path)
	}

	f, err := openConversationAppendFile(root, name, recordBytes, path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	if n, err := f.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("conversation: append %s: %w", path, err)
	} else if n != buf.Len() {
		return fmt.Errorf("conversation: append %s: %w", path, io.ErrShortWrite)
	}
	return nil
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
	data, err := workdirpath.ReadRootRegularFileLimited(root, name, maxConversationLogBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("conversation: read %s: %w", path, err)
	}
	return data, nil
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
	workRoot, err := workdirpath.OpenRootUnderUserConfig(worktree)
	if err != nil {
		return nil, "", fmt.Errorf("conversation: open worktree %s: %w", worktree, err)
	}
	defer func() { _ = workRoot.Close() }()
	if createDir {
		if err := workdirpath.MkdirAllRootNoSymlink(workRoot, ".stado", 0o700); err != nil {
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
	var msgs []agent.Message
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), int(maxConversationRecordBytes))
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var probe struct {
			Type string `json:"type"`
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
				var wrapped struct {
					Message agent.Message `json:"message"`
				}
				if err := json.Unmarshal(line, &wrapped); err != nil {
					break
				}
				msgs = append(msgs, wrapped.Message)
				continue
			}
		}
		var m agent.Message
		if err := json.Unmarshal(line, &m); err != nil {
			// Partial tail → stop accumulating but keep what we had.
			break
		}
		if m.Role == "" && len(m.Content) == 0 {
			// Unknown future event type. Preserve forward compatibility by
			// ignoring it rather than replaying an empty message.
			continue
		}
		msgs = append(msgs, m)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return msgs, err
	}
	return msgs, nil
}
