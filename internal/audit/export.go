package audit

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// Record is one exported commit — shaped for SIEM ingestion (JSON lines).
//
// Fields mirror the commit-message trailers stado writes; parsing is
// best-effort so unexpected trailers don't break export.
type Record struct {
	Commit    string            `json:"commit"`
	Ref       string            `json:"ref"`
	Parents   []string          `json:"parents,omitempty"`
	Tree      string            `json:"tree"`
	Timestamp time.Time         `json:"timestamp"`
	Author    string            `json:"author"`
	Email     string            `json:"email"`
	Title     string            `json:"title"`
	Trailers  map[string]string `json:"trailers,omitempty"`
	Signed    bool              `json:"signed"`
}

// ExportJSONL walks head → root and writes one JSON record per commit to w.
// Order: newest first (head); caller can reverse for time-ascending.
func ExportJSONL(w io.Writer, src storer.EncodedObjectStorer, refName string, head plumbing.Hash) error {
	bw := bufio.NewWriter(w)
	defer bw.Flush()

	enc := json.NewEncoder(bw)
	cur := head
	seen := map[plumbing.Hash]bool{}
	for !cur.IsZero() {
		if seen[cur] {
			break
		}
		seen[cur] = true
		c, err := object.GetCommit(src, cur)
		if err != nil {
			return err
		}
		rec := toRecord(refName, cur, c)
		if err := enc.Encode(rec); err != nil {
			return err
		}
		if len(c.ParentHashes) == 0 {
			break
		}
		cur = c.ParentHashes[0]
	}
	return nil
}

func toRecord(refName string, hash plumbing.Hash, c *object.Commit) Record {
	title, trailers := parseMessage(c.Message)
	rec := Record{
		Commit:    hash.String(),
		Ref:       refName,
		Tree:      c.TreeHash.String(),
		Author:    c.Author.Name,
		Email:     c.Author.Email,
		Timestamp: c.Author.When.UTC(),
		Title:     title,
		Trailers:  trailers,
	}
	for _, p := range c.ParentHashes {
		rec.Parents = append(rec.Parents, p.String())
	}
	_, rec.Signed = ExtractSignature(c.Message)
	return rec
}

// ParseMessage is the exported entry point for the canonical
// commit-message parser. Returns the title (first non-empty line)
// and the trailer block (LAST contiguous run of well-formed,
// unindented `Key: Value` lines per [isTrailerLine]).
//
// Exported in v0.56.0 (Codex G6/L P1) so the duplicate impls in
// cmd/stado/stats.go + internal/runtime/sessionstats can route through
// this canonical parser instead of carrying their own copies of the
// pre-#51 `TrimSpace(line[:idx])` shape — exactly the bug Codex #143
// round 2 hardened [parseMessage] against. The duplicates kept the
// pre-fix behavior and were a silent re-introduction of the
// trailer-injection vector for any consumer reached via the cmd-side
// or sessionstats path. See [parseMessage] for the parsing algorithm.
func ParseMessage(msg string) (title string, trailers map[string]string) {
	return parseMessage(msg)
}

// parseMessage extracts the title (first non-empty line) and the
// trailer block from a stado commit message. The trailer block is the
// LAST contiguous run of well-formed trailer lines per [isTrailerLine]
// (unindented `Key: Value` with key matching `[A-Za-z][-_A-Za-z0-9]*`)
// — git-trailer convention, formalized here as defense layer 2 against
// summary-injection (Codex #143 round 2).
//
// Pre-fix this parser treated every `K: V` line after the first blank
// as a trailer AND `strings.TrimSpace`-ed the key, so an indented
// compaction-summary line `  Tool: bash` parsed as a real `Tool`
// trailer — overwriting the real value under last-write-wins. The
// CompactionMeta.formatMessage two-space indent (defense layer 1) did
// nothing because TrimSpace flattened the indent. This parser is now
// defense layer 2 — only unindented lines in the final contiguous
// block count.
//
// Signature trailers are dropped from the returned map — audit
// signatures aren't useful to consumers that read trailers for
// display / aggregation. [ExtractSignature] is the right entry point
// when callers want the signature itself.
func parseMessage(msg string) (title string, trailers map[string]string) {
	lines := strings.Split(msg, "\n")
	trailers = map[string]string{}

	// Title is the first non-empty line.
	for _, line := range lines {
		if line != "" {
			title = line
			break
		}
	}

	// Walk backward from the end to find the trailer block — a
	// contiguous run of well-formed trailer lines, optionally with
	// trailing blank lines after. Stops at the first line that
	// isn't a trailer (indented summary lines, body text, blanks
	// inside the body, etc.).
	end := len(lines)
	for end > 0 && lines[end-1] == "" {
		end--
	}
	start := end
	for start > 0 && isTrailerLine(lines[start-1]) {
		start--
	}
	for _, line := range lines[start:end] {
		if line == "" {
			continue
		}
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		k := line[:idx] // no TrimSpace — unindented is required by isTrailerLine
		v := strings.TrimSpace(line[idx+1:])
		if k == "" || k == "Signature" {
			continue
		}
		trailers[k] = v
	}
	return title, trailers
}

// isTrailerLine reports whether a line is a well-formed trailer:
// starts in column 0 (no leading whitespace), key matches
// `[A-Za-z][-_A-Za-z0-9]*`, and a colon follows. Indented lines are
// rejected so a compaction-summary line that happens to look like
// `Key: Value` (Codex #143) doesn't get promoted to a real trailer.
func isTrailerLine(line string) bool {
	if line == "" || line[0] == ' ' || line[0] == '\t' {
		return false
	}
	idx := strings.Index(line, ":")
	if idx <= 0 {
		return false
	}
	// First char must be a letter; rest must match alnum/-/_.
	for i := 0; i < idx; i++ {
		c := line[i]
		ok := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_'
		if !ok {
			return false
		}
	}
	first := line[0]
	return (first >= 'A' && first <= 'Z') || (first >= 'a' && first <= 'z')
}
