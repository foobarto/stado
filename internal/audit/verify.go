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

	// MutationChain lists every hook-mutation provenance link found on this
	// ref (a commit carrying Mutated-By-Hook + Original-Result-SHA paired
	// with its original-result parent). Each link's Broken flag marks a
	// linkage anomaly. This is a DISTINCT anomaly class from signature
	// validity: a broken link does NOT increment Invalid and does NOT set
	// InvalidAt — the signatures over both commits can be perfectly valid
	// while the content linkage between them was tampered. Callers report
	// BrokenLinks() separately and exit non-zero on it without claiming the
	// signature chain failed. Absence of the provenance trailers on a commit
	// means "not a mutation commit" (legacy-safe), never a broken link.
	MutationChain []MutationLink
}

// BrokenLinks returns the number of mutation links that failed validation.
// Zero for legacy / pre-fix chains (no provenance trailers) and for a chain
// whose every mutation link is intact.
func (r WalkResult) BrokenLinks() int {
	n := 0
	for _, l := range r.MutationChain {
		if l.Broken {
			n++
		}
	}
	return n
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

		// Hook-mutation provenance linkage (NON-fatal, distinct from
		// signature validity). Mutated-By-Hook is the DEFINITIVE marker of a
		// mutation commit (set only on the second of the two-commit pair, and
		// keyed on alone by the /tree badge + `session logs` detection too).
		// We deliberately do NOT also require Original-Result-SHA: when the
		// original tool result was empty its SHA is legitimately "" (sha256Of
		// ""==""), so requiring the trailer would silently skip validating an
		// empty-origin mutation — recorded on the commit, but never
		// cross-checked. validateMutationLink handles the empty case (an empty
		// Original-Result-SHA matches the empty parent Result-SHA) and still
		// breaks on a non-empty mismatch, so tamper detection is unaffected.
		// Absence of Mutated-By-Hook = "not a mutation commit" (legacy/pre-fix
		// commits land here, never as a broken link). validateMutationLink is
		// NON-fatal: it records a Broken link with a reason rather than
		// erroring, so the walk continues; a broken link never touches
		// Invalid/InvalidAt.
		if _, trailers := parseMessage(commit.Message); trailers[trailerMutatedByHook] != "" {
			res.MutationChain = append(res.MutationChain,
				validateMutationLink(w.Src, cur, commit, trailers))
		}

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
			// Codex #138: VerifyV2 checks v2 (author/committer/
			// timestamps bound) first, then falls back to v1 so
			// pre-fix audit history still verifies. A v1 signature
			// on a commit whose author or timestamps were tampered
			// will still validate via the v1 fallback — that's a
			// pre-existing limitation of the v1 scheme, not a v2
			// regression. New commits (v2) catch the tamper.
			if err := VerifyV2(w.Pub, commit.TreeHash.String(), parents, commit.Message,
				SignedIdentity{
					AuthorName:     commit.Author.Name,
					AuthorEmail:    commit.Author.Email,
					AuthorUnix:     commit.Author.When.Unix(),
					CommitterName:  commit.Committer.Name,
					CommitterEmail: commit.Committer.Email,
					CommitterUnix:  commit.Committer.When.Unix(),
				}); err != nil {
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
