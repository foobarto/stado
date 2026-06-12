package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// maxAuditResultBlobBytes bounds how many bytes the audit walker will read
// from a recorded `result` blob when cross-checking a mutation link. Matches
// the producer-side cap (state/git.MaxTraceResultBlobBytes) with headroom; a
// blob larger than this can't be a legitimate trace result, so refusing to
// read it is the safe choice (the SHA-equality check (a) still stands).
const maxAuditResultBlobBytes int64 = 8 << 20 // 8 MiB

// Trailer keys for hook-mutation provenance (must match
// internal/state/git/commit_meta.go's CommitMeta.formatMessage). Centralised
// here so the audit-verify linkage validator and downstream consumers parse
// the same names.
const (
	trailerResultSHA         = "Result-SHA"
	trailerOriginalResultSHA = "Original-Result-SHA"
	trailerMutatedByHook     = "Mutated-By-Hook"
	trailerTool              = "Tool"
)

// resultBlobName is the single tree entry under a blob-backed trace commit
// that holds the recorded tool-result bytes. Kept in lockstep with
// internal/state/git's resultBlobName (the producer side) — this is a
// cross-package layout contract, intentionally duplicated to avoid an
// audit → state/git import edge.
const resultBlobName = "result"

// MutationLink is one validated original→mutated provenance edge in the trace
// chain: a commit carrying Mutated-By-Hook + Original-Result-SHA, paired with
// its first parent (the original-result commit). A link is reported whether or
// not it validates; Broken marks a tamper/inconsistency anomaly.
//
// IMPORTANT: a broken link is a DISTINCT anomaly class from a bad signature.
// It never sets WalkResult.Invalid — the signatures over both commits can be
// perfectly valid while the *content linkage* between them was tampered (e.g.
// someone rewrote the original commit's Result-SHA after the fact). Verify
// surfaces it separately so `audit verify` exits non-zero on a broken link
// without claiming the signature chain itself failed.
type MutationLink struct {
	// Commit is the mutation commit (the one carrying the provenance trailers).
	Commit plumbing.Hash
	// Parent is its first parent — the original-result commit.
	Parent plumbing.Hash
	// Tool is the mutating call's tool name (from the Tool trailer).
	Tool string
	// ByHook is the attributed mutating hook (Mutated-By-Hook trailer).
	ByHook string
	// OriginalSHA is the mutation commit's Original-Result-SHA trailer (the
	// pre-mutation result digest).
	OriginalSHA string
	// MutatedSHA is the mutation commit's Result-SHA trailer (the canonical,
	// model-facing digest).
	MutatedSHA string
	// BlobBacked is true when the parent (original) commit stored its result
	// bytes as a `result` blob (the (c) check ran against real bytes).
	BlobBacked bool
	// Broken marks a failed linkage validation. BrokenReason explains which
	// invariant was violated.
	Broken       bool
	BrokenReason string
}

// validateMutationLink checks a candidate mutation commit (already known to
// carry Mutated-By-Hook + Original-Result-SHA) against its first parent and
// returns the link record. NON-fatal: a violation sets Broken + BrokenReason
// rather than erroring, so the walk continues and one tampered link doesn't
// hide later ones.
func validateMutationLink(src storer.EncodedObjectStorer, commit plumbing.Hash, c *object.Commit, mutTrailers map[string]string) MutationLink {
	link := MutationLink{
		Commit:      commit,
		Tool:        mutTrailers[trailerTool],
		ByHook:      mutTrailers[trailerMutatedByHook],
		OriginalSHA: mutTrailers[trailerOriginalResultSHA],
		MutatedSHA:  mutTrailers[trailerResultSHA],
	}

	// (b) Mutated-By-Hook must be non-empty.
	if link.ByHook == "" {
		link.Broken = true
		link.BrokenReason = "Mutated-By-Hook trailer is empty"
		return link
	}

	// A mutation commit must have a first parent (the original-result commit).
	if len(c.ParentHashes) == 0 {
		link.Broken = true
		link.BrokenReason = "mutation commit has no parent (original-result commit missing)"
		return link
	}
	link.Parent = c.ParentHashes[0]

	parent, err := object.GetCommit(src, link.Parent)
	if err != nil {
		link.Broken = true
		link.BrokenReason = fmt.Sprintf("cannot read parent commit %s: %v", link.Parent, err)
		return link
	}
	_, parentTrailers := parseMessage(parent.Message)

	// (a) The first parent's Result-SHA must equal this commit's
	// Original-Result-SHA — the core linkage invariant.
	parentResultSHA := parentTrailers[trailerResultSHA]
	if parentResultSHA != link.OriginalSHA {
		link.Broken = true
		link.BrokenReason = fmt.Sprintf("parent Result-SHA %q != Original-Result-SHA %q",
			parentResultSHA, link.OriginalSHA)
		return link
	}

	// (c) If the original-result commit is blob-backed, the recovered bytes
	// must hash to Original-Result-SHA. A blob present but mismatching is a
	// broken link; absence of a blob (SHA-only / overflow fallback) is NOT a
	// break — there's nothing to cross-check, the SHA-equality of (a) stands.
	blob, present, berr := readResultBlob(src, parent)
	if berr != nil {
		link.Broken = true
		link.BrokenReason = fmt.Sprintf("cannot read parent result blob: %v", berr)
		return link
	}
	if present {
		link.BlobBacked = true
		if got := resultSHA(blob); got != link.OriginalSHA {
			link.Broken = true
			link.BrokenReason = fmt.Sprintf("parent result blob hashes to %q, Original-Result-SHA is %q",
				got, link.OriginalSHA)
			return link
		}
	}

	return link
}

// readResultBlob recovers the `result` blob under a commit's tree, mirroring
// state/git.Session.ReadTraceResultBlob but operating on a raw object storer
// (the audit walker has no Session). Returns (nil, false, nil) for a commit
// with no `result` entry (empty-tree / SHA-only).
func readResultBlob(src storer.EncodedObjectStorer, c *object.Commit) ([]byte, bool, error) {
	tree, err := object.GetTree(src, c.TreeHash)
	if err != nil {
		return nil, false, fmt.Errorf("read tree %s: %w", c.TreeHash, err)
	}
	entry, err := tree.FindEntry(resultBlobName)
	if errors.Is(err, object.ErrEntryNotFound) || errors.Is(err, object.ErrDirectoryNotFound) {
		// No `result` entry → empty-tree / SHA-only commit. Not an error.
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("find result entry in %s: %w", c.TreeHash, err)
	}
	blob, err := object.GetBlob(src, entry.Hash)
	if err != nil {
		return nil, false, fmt.Errorf("read blob %s: %w", entry.Hash, err)
	}
	if blob.Size > maxAuditResultBlobBytes {
		return nil, false, fmt.Errorf("result blob %s exceeds %d bytes", entry.Hash, maxAuditResultBlobBytes)
	}
	r, err := blob.Reader()
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = r.Close() }()
	data, err := io.ReadAll(io.LimitReader(r, maxAuditResultBlobBytes+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > maxAuditResultBlobBytes {
		return nil, false, fmt.Errorf("result blob %s exceeds %d bytes", entry.Hash, maxAuditResultBlobBytes)
	}
	return data, true, nil
}

// resultSHA mirrors the executor's sha256Of: `sha256:<hex>` of the bytes, or
// "" for empty input (so an empty original result matches the producer's empty
// Result-SHA).
func resultSHA(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
