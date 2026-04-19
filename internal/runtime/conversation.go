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
// Append-only: the file never gets rewritten, matching DESIGN's
// append-only invariant for the conversation. Truncation happens
// only on explicit user action (e.g. starting a new session).

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/foobarto/stado/pkg/agent"
)

// ConversationFile is the per-worktree conversation log path
// (relative to the worktree root). Readable by `stado session show`
// for debugging + by runtime.OpenSession when resuming.
const ConversationFile = ".stado/conversation.jsonl"

// AppendMessage appends a single agent.Message to the conversation
// log under worktree. Creates the `.stado` dir + file on first call.
// Best-effort atomicity: O_APPEND|O_CREATE with a fresh fd per call.
// Concurrent appenders serialise at the OS-level append guarantee.
func AppendMessage(worktree string, msg agent.Message) error {
	if worktree == "" {
		return errors.New("conversation: worktree required")
	}
	dir := filepath.Join(worktree, ".stado")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("conversation: mkdir %s: %w", dir, err)
	}
	path := filepath.Join(worktree, ConversationFile)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("conversation: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false) // preserve HTML/tags in message bodies verbatim
	if err := enc.Encode(msg); err != nil {
		return fmt.Errorf("conversation: encode: %w", err)
	}
	return nil
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
	path := filepath.Join(worktree, ConversationFile)
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("conversation: open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	return decodeMessages(f)
}

func decodeMessages(r io.Reader) ([]agent.Message, error) {
	var msgs []agent.Message
	scanner := bufio.NewScanner(r)
	// Allow up to 1 MiB per message — generous for typical assistant
	// replies; DESIGN §"Tool-output curation" caps individual tool
	// outputs well below this, and user messages don't approach it.
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var m agent.Message
		if err := json.Unmarshal(line, &m); err != nil {
			// Partial tail → stop accumulating but keep what we had.
			break
		}
		msgs = append(msgs, m)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return msgs, err
	}
	return msgs, nil
}
