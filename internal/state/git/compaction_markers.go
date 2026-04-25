package git

import (
	"fmt"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
)

// CompactionMarker is one compaction event surfaced on the session's
// tree ref. `stado session show` uses this to render the compaction
// timeline per PLAN §11.3.6. Extracted by walking the tree ref and
// parsing the trailers that `CompactionMeta.formatMessage` emits.
type CompactionMarker struct {
	CommitHash plumbing.Hash // the tree-ref commit carrying the compaction
	Title      string        // subject line after "Compaction: "
	FromTurn   int
	ToTurn     int
	TurnsTotal int
	At         string // RFC3339 from Compaction-At trailer (raw)
	By         string // Compaction-By trailer; may be empty
	RawLogSHA  string // digest of conversation.jsonl before compaction; may be empty for old markers
}

// ListCompactions walks the tree ref's first-parent chain from HEAD
// and returns every commit whose subject starts with "Compaction: "
// in newest-first order. Other commits (tool calls, seed commits)
// are skipped. Returns an empty slice + nil error when the session
// has no compactions (common).
func (s *Sidecar) ListCompactions(sessionID string) ([]CompactionMarker, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return nil, err
	}
	head, err := s.ResolveRef(TreeRef(sessionID))
	if err != nil {
		return nil, nil
	}
	var out []CompactionMarker
	cur := head
	for !cur.IsZero() {
		commit, err := s.repo.CommitObject(cur)
		if err != nil {
			return out, err
		}
		if m, ok := parseCompactionCommit(commit.Message); ok {
			m.CommitHash = cur
			out = append(out, m)
		}
		if len(commit.ParentHashes) == 0 {
			break
		}
		cur = commit.ParentHashes[0]
	}
	return out, nil
}

// parseCompactionCommit extracts a CompactionMarker from a
// Compaction-tagged commit message. Returns (_, false) when the
// subject doesn't start with "Compaction: " — the caller skips
// non-compaction commits.
func parseCompactionCommit(msg string) (CompactionMarker, bool) {
	if !strings.HasPrefix(msg, "Compaction: ") {
		return CompactionMarker{}, false
	}
	m := CompactionMarker{}
	// Subject: everything after "Compaction: " up to the first newline.
	subjectEnd := strings.Index(msg, "\n")
	if subjectEnd < 0 {
		subjectEnd = len(msg)
	}
	m.Title = strings.TrimSpace(strings.TrimPrefix(msg[:subjectEnd], "Compaction: "))

	// Trailers — line-by-line after the body.
	for _, line := range strings.Split(msg, "\n") {
		if strings.HasPrefix(line, "Compaction-From-Turn: ") {
			_, _ = fmt.Sscanf(line, "Compaction-From-Turn: %d", &m.FromTurn)
		}
		if strings.HasPrefix(line, "Compaction-To-Turn: ") {
			_, _ = fmt.Sscanf(line, "Compaction-To-Turn: %d", &m.ToTurn)
		}
		if strings.HasPrefix(line, "Compaction-Turns-Total: ") {
			_, _ = fmt.Sscanf(line, "Compaction-Turns-Total: %d", &m.TurnsTotal)
		}
		if v := trailerValue(line, "Compaction-At: "); v != "" {
			m.At = v
		}
		if v := trailerValue(line, "Compaction-By: "); v != "" {
			m.By = v
		}
		if v := trailerValue(line, "Compaction-Raw-Log-SHA: "); v != "" {
			m.RawLogSHA = v
		}
	}
	return m, true
}

// trailerValue returns the text after `key ` on a trailer line, or ""
// if the line doesn't carry that trailer.
func trailerValue(line, key string) string {
	if !strings.HasPrefix(line, key) {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(line, key))
}
