package git

import (
	"errors"
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// resultBlobName is the single tree entry under a blob-backed trace commit
// that holds the recorded tool-result bytes. Audit/`/tree` recover the
// original (and the mutated) Content by reading this blob from the trace
// commit's tree. Kept short + constant so the audit-verify linkage check and
// the tree-badge renderer can look it up without a layout negotiation.
const resultBlobName = "result"

// maxTraceResultBlobBytes caps how large a tool-result Content we'll store as
// a git blob inside a trace commit's tree (hook-mutation provenance, WAVE 2a).
//
// This is DELIBERATELY far smaller than the 256 MiB tree-snapshot cap
// (maxTreeBlobBytes): a trace commit records a single tool-result *envelope*,
// not a filesystem snapshot, and a tamper-evident audit log should not be a
// vector for a hostile/runaway tool result to bloat the sidecar repo. 4 MiB
// comfortably holds any realistic tool result (a `read` of a large file, a
// long `bash` capture) while bounding worst-case per-commit growth. On
// overflow we fall back to an empty-tree (SHA-only) commit and the caller
// notes the skip in meta.Error — mirroring the snapshot-skip contract in
// internal/tools/executor.go (the mutation's digest stays in the signed
// chain; only the recoverable bytes are dropped).
const maxTraceResultBlobBytes int64 = 4 << 20 // 4 MiB

// MaxTraceResultBlobBytes exposes the trace-result blob cap so callers (the
// executor) can pre-detect an overflow and annotate the commit's Error trailer
// consistently with the SHA-only fallback CommitToTraceBlob applies.
func MaxTraceResultBlobBytes() int64 { return maxTraceResultBlobBytes }

// CommitToTraceBlob writes a trace commit whose tree holds the given tool
// result Content as a single `result` blob, so the bytes are recoverable from
// the signed audit chain (not just their SHA). It is the blob-backed
// counterpart of CommitToTrace, used for the original + mutated commits of a
// post_tool mutation (hook-mutation provenance, WAVE 2a).
//
// Size cap + SHA-only fallback: when content exceeds maxTraceResultBlobBytes,
// the bytes are NOT stored — the commit falls back to an empty tree (as
// CommitToTrace) and (skipped=true) is returned so the caller can note the
// drop in meta.Error. The commit, its Result-SHA, and the provenance trailers
// still land in the signed chain; only the recoverable bytes are omitted.
//
// content nil/empty is valid (a tool that returned no Content): an empty blob
// is written so the layout is uniform and ReadTraceResultBlob returns "".
func (s *Session) CommitToTraceBlob(meta CommitMeta, content []byte) (hash plumbing.Hash, skipped bool, err error) {
	if int64(len(content)) > maxTraceResultBlobBytes {
		// Overflow: fall back to SHA-only (empty-tree). The digest +
		// provenance trailers still bind into the signed commit.
		h, cerr := s.CommitToTrace(meta)
		return h, true, cerr
	}
	tree, err := s.writeResultTree(content)
	if err != nil {
		return plumbing.ZeroHash, false, err
	}
	h, err := s.commitOnRef(TraceRef(s.ID), tree, meta)
	return h, false, err
}

// writeResultTree stores content as a `result` blob and returns a tree that
// holds exactly that one entry. Reuses entriesToTree (the same sorted-tree
// writer the snapshot path uses) so the object shape is identical to a normal
// git tree — `git cat-file`, audit export, and the tree-badge renderer all see
// a regular blob.
func (s *Session) writeResultTree(content []byte) (plumbing.Hash, error) {
	blobHash, err := s.writeResultBlob(content)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return s.entriesToTree([]treeEntry{{
		name: resultBlobName,
		hash: blobHash,
		mode: filemode.Regular,
	}})
}

// writeResultBlob writes content as a git blob object and returns its hash.
// Mirrors writeBlobReader (treebuild.go) but for an in-memory byte slice — the
// trace-result path already has the bytes in hand and never touches the
// filesystem.
func (s *Session) writeResultBlob(content []byte) (plumbing.Hash, error) {
	obj := s.Sidecar.repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	obj.SetSize(int64(len(content)))
	w, err := obj.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := w.Write(content); err != nil {
		_ = w.Close()
		return plumbing.ZeroHash, err
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	return s.Sidecar.repo.Storer.SetEncodedObject(obj)
}

// ReadTraceResultBlob recovers the `result` blob recorded under a blob-backed
// trace commit, returning its bytes. Returns (nil, false, nil) when the commit
// is an ordinary empty-tree (SHA-only) trace commit with no `result` entry —
// the 99% case and the overflow-fallback case — so callers can distinguish
// "no recoverable bytes here" from a real read error.
//
// Used by audit-verify's blob-backed linkage check (the parent's recorded
// bytes must hash to the child's Original-Result-SHA) and by the WAVE 2b
// /tree badge / session-logs render.
func (s *Session) ReadTraceResultBlob(commit plumbing.Hash) (content []byte, present bool, err error) {
	c, err := object.GetCommit(s.Sidecar.repo.Storer, commit)
	if err != nil {
		return nil, false, fmt.Errorf("read trace commit %s: %w", commit, err)
	}
	tree, err := object.GetTree(s.Sidecar.repo.Storer, c.TreeHash)
	if err != nil {
		return nil, false, fmt.Errorf("read trace tree %s: %w", c.TreeHash, err)
	}
	entry, err := tree.FindEntry(resultBlobName)
	if errors.Is(err, object.ErrEntryNotFound) || errors.Is(err, object.ErrDirectoryNotFound) {
		// No `result` entry → empty-tree / SHA-only commit. Not an error.
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("find result entry in %s: %w", c.TreeHash, err)
	}
	data, err := s.readBlobLimited(entry.Hash, maxTraceResultBlobBytes)
	if err != nil {
		return nil, false, fmt.Errorf("read result blob %s: %w", entry.Hash, err)
	}
	return data, true, nil
}
