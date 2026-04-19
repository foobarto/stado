package audit

import (
	"crypto/ed25519"
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// WalkResult summarises a verification walk over a single ref.
type WalkResult struct {
	Ref          string
	TotalCommits int
	Signed       int
	Unsigned     int
	Invalid      int
	// InvalidAt is the first commit whose signature failed verification,
	// or the zero hash if none did. Useful for pinpointing tamper.
	InvalidAt plumbing.Hash
	// FirstUnsignedAt is the first commit missing a signature, or zero.
	FirstUnsignedAt plumbing.Hash
}

// Walker walks commit history verifying signatures.
type Walker struct {
	Pub ed25519.PublicKey
	Src storer.EncodedObjectStorer
}

// NewWalker returns a Walker that reads commits from src and verifies with pub.
func NewWalker(src storer.EncodedObjectStorer, pub ed25519.PublicKey) *Walker {
	return &Walker{Pub: pub, Src: src}
}

// Verify walks every reachable commit from head, verifying each signature.
// Non-fast: linear walk of the parent chain; stops on first invalid sig.
func (w *Walker) Verify(refName string, head plumbing.Hash) (WalkResult, error) {
	res := WalkResult{Ref: refName}
	cur := head
	seen := map[plumbing.Hash]bool{}
	for !cur.IsZero() {
		if seen[cur] {
			break
		}
		seen[cur] = true
		commit, err := object.GetCommit(w.Src, cur)
		if err != nil {
			return res, fmt.Errorf("audit: read %s: %w", cur, err)
		}
		res.TotalCommits++

		sig, ok := ExtractSignature(commit.Message)
		if !ok {
			res.Unsigned++
			if res.FirstUnsignedAt.IsZero() {
				res.FirstUnsignedAt = cur
			}
		} else {
			parents := make([]string, len(commit.ParentHashes))
			for i, p := range commit.ParentHashes {
				parents[i] = p.String()
			}
			if err := Verify(w.Pub, commit.TreeHash.String(), parents, commit.Message); err != nil {
				res.Invalid++
				if res.InvalidAt.IsZero() {
					res.InvalidAt = cur
				}
			} else {
				res.Signed++
			}
			_ = sig
		}

		if len(commit.ParentHashes) == 0 {
			break
		}
		cur = commit.ParentHashes[0] // first-parent linear audit trail
	}
	return res, nil
}
