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

// parseMessage splits a commit message into (title, trailer-map). The title
// is everything up to the first blank line; trailers are "Key: Value" lines
// after the blank line.
func parseMessage(msg string) (title string, trailers map[string]string) {
	lines := strings.Split(msg, "\n")
	trailers = map[string]string{}
	var titleDone bool
	for _, line := range lines {
		if !titleDone {
			if line == "" {
				titleDone = true
				continue
			}
			if title == "" {
				title = line
			}
			continue
		}
		if idx := strings.Index(line, ":"); idx > 0 {
			k := strings.TrimSpace(line[:idx])
			v := strings.TrimSpace(line[idx+1:])
			if k != "" && k != "Signature" {
				trailers[k] = v
			}
		}
	}
	return title, trailers
}
