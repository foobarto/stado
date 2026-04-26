package git

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const maxEncodedCommitBytes int64 = 1 << 20

// readEncodedObject drains an EncodedObject's Reader and returns its
// bytes. Used to recover the canonical git bytes after Encode so the
// SSH signer can sign exactly what git signs.
func readEncodedObject(obj plumbing.EncodedObject) ([]byte, error) {
	if obj.Size() > maxEncodedCommitBytes {
		return nil, fmt.Errorf("encoded object exceeds %d bytes", maxEncodedCommitBytes)
	}
	r, err := obj.Reader()
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	data, err := io.ReadAll(io.LimitReader(r, maxEncodedCommitBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxEncodedCommitBytes {
		return nil, fmt.Errorf("encoded object exceeds %d bytes", maxEncodedCommitBytes)
	}
	return data, nil
}

// CommitToTrace writes an empty-tree commit (metadata only) on trace ref.
// Every tool call — query or mutation, success or failure — produces one.
func (s *Session) CommitToTrace(meta CommitMeta) (plumbing.Hash, error) {
	emptyTree, err := s.writeEmptyTree()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return s.commitOnRef(TraceRef(s.ID), emptyTree, meta)
}

// CommitToTree writes a commit against tree ref with the given tree hash.
// Callers should check whether the tree actually changed (BuildTreeFromDir
// + comparison to TreeHead) before calling — per policy a tree commit is only
// made for mutating or exec-with-diff tool calls.
func (s *Session) CommitToTree(treeHash plumbing.Hash, meta CommitMeta) (plumbing.Hash, error) {
	return s.commitOnRef(TreeRef(s.ID), treeHash, meta)
}

// CommitCompaction records a user-accepted /compact event on both
// refs. Per DESIGN §"Compaction":
//
//   - `tree` ref gets a new commit whose tree hash equals its parent's
//     (filesystem is unchanged — compaction is a conversation-scope
//     event). `git checkout refs/sessions/<id>/tree~1 -- …` therefore
//     restores the pre-compaction state exactly.
//   - `trace` ref gets a parallel empty-tree commit with the same
//     summary body, so tooling that walks either ref sees the event.
//
// Both commits share the same subject + summary; trailers reference
// the collapsed turn range for downstream audit / UI rendering by
// `stado session show`.
func (s *Session) CommitCompaction(meta CompactionMeta) (treeSHA, traceSHA plumbing.Hash, err error) {
	// Resolve the current tree-ref head so we can reuse its tree hash.
	// A pure chat session may not have a tree ref yet; in that case we
	// still write an empty-tree marker so the accepted compaction is
	// auditable instead of silently applying only to the JSONL view.
	treeHead, err := s.Sidecar.resolveRef(TreeRef(s.ID))
	var treeHash plumbing.Hash
	if err == nil {
		headCommit, err := object.GetCommit(s.Sidecar.repo.Storer, treeHead)
		if err != nil {
			return plumbing.ZeroHash, plumbing.ZeroHash, fmt.Errorf("compaction: resolve tree head: %w", err)
		}
		treeHash = headCommit.TreeHash
	} else if errors.Is(err, plumbing.ErrReferenceNotFound) {
		treeHash, err = s.writeEmptyTree()
		if err != nil {
			return plumbing.ZeroHash, plumbing.ZeroHash, fmt.Errorf("compaction: empty tree: %w", err)
		}
	} else {
		return plumbing.ZeroHash, plumbing.ZeroHash, fmt.Errorf("compaction: resolve tree ref: %w", err)
	}

	// Synthetic CommitMeta lets us reuse commitOnRef (signing, OnCommit
	// hook, encoded-object plumbing) without a parallel pathway.
	// formatMessage returns a single string — we inject it by setting
	// Summary to the pre-formatted payload and nulling the title/tool
	// fields that would otherwise be prepended.
	now := time.Now()
	payload := meta.formatMessage(now)

	treeSHA, err = s.commitOnRef(TreeRef(s.ID), treeHash, preformattedMeta(payload))
	if err != nil {
		return plumbing.ZeroHash, plumbing.ZeroHash, fmt.Errorf("compaction: tree commit: %w", err)
	}

	emptyTree, err := s.writeEmptyTree()
	if err != nil {
		return treeSHA, plumbing.ZeroHash, fmt.Errorf("compaction: empty tree: %w", err)
	}
	traceSHA, err = s.commitOnRef(TraceRef(s.ID), emptyTree, preformattedMeta(payload))
	if err != nil {
		return treeSHA, plumbing.ZeroHash, fmt.Errorf("compaction: trace commit: %w", err)
	}
	return treeSHA, traceSHA, nil
}

// commitOnRef creates a commit with parent = current ref tip (if any) and
// updates the ref to the new commit. When Session.Signer is non-nil, the
// signature trailer is appended to the commit message before encoding so
// the message text itself carries the tamper-evident proof.
func (s *Session) commitOnRef(ref plumbing.ReferenceName, tree plumbing.Hash, meta CommitMeta) (plumbing.Hash, error) {
	var parents []plumbing.Hash
	head, err := s.Sidecar.resolveRef(ref)
	if err == nil {
		parents = append(parents, head)
	}

	msg := meta.formatMessage()
	if s.Signer != nil {
		parentStrs := make([]string, len(parents))
		for i, p := range parents {
			parentStrs[i] = p.String()
		}
		sigValue := s.Signer.Sign(tree.String(), parentStrs, msg)
		if sigValue != "" {
			msg = appendSignatureTrailer(msg, sigValue)
		}
	}

	now := time.Now()
	sig := s.signature(now)
	commit := &object.Commit{
		Author:    sig,
		Committer: sig,
		Message:   msg,
		TreeHash:  tree,
	}
	for _, p := range parents {
		commit.ParentHashes = append(commit.ParentHashes, p)
	}

	// SSHSIG gpgsig header (Phase 5 — git-tooling interop). When the
	// Signer also implements SSHCommitSigner, encode the commit once
	// with an empty signature to get the canonical bytes git signs,
	// feed those through SignSSH, and re-encode with the PGPSignature
	// field set. `git log --show-signature` then recognises the
	// signature and (with gpg.ssh.allowedSignersFile configured)
	// verifies it against the signer's pubkey. The existing Signature:
	// trailer path stays untouched for stado-native audit verification.
	if sshSigner, ok := s.Signer.(SSHCommitSigner); ok {
		probe := s.Sidecar.repo.Storer.NewEncodedObject()
		if err := commit.Encode(probe); err != nil {
			return plumbing.ZeroHash, fmt.Errorf("encode commit (sshsig probe): %w", err)
		}
		canonicalBytes, err := readEncodedObject(probe)
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("read canonical bytes: %w", err)
		}
		sshsig, err := sshSigner.SignSSH(canonicalBytes)
		if err != nil {
			return plumbing.ZeroHash, fmt.Errorf("sshsig: %w", err)
		}
		if sshsig != "" {
			commit.PGPSignature = sshsig
		}
	}

	obj := s.Sidecar.repo.Storer.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("encode commit: %w", err)
	}
	hash, err := s.Sidecar.repo.Storer.SetEncodedObject(obj)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("store commit: %w", err)
	}

	if err := s.Sidecar.setRef(ref, hash); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("update ref: %w", err)
	}
	if s.OnCommit != nil {
		s.OnCommit(CommitEvent{
			Ref:  string(ref),
			Hash: hash.String(),
			Meta: meta,
		})
	}
	return hash, nil
}

// appendSignatureTrailer adds `Signature: ed25519:<base64>` as the last line
// of body, stripping any preexisting trailer first. Kept here (not in audit/)
// to avoid a state/git → audit import cycle.
func appendSignatureTrailer(body, sigValue string) string {
	const trailer = "Signature: "
	// Strip existing Signature line(s).
	lines := strings.Split(body, "\n")
	filtered := lines[:0]
	for _, line := range lines {
		if strings.HasPrefix(line, trailer) {
			continue
		}
		filtered = append(filtered, line)
	}
	body = strings.Join(filtered, "\n")
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	return body + trailer + sigValue + "\n"
}

// writeEmptyTree creates (or returns the cached hash of) the empty tree object.
// git guarantees it's 4b825dc642cb6eb9a060e54bf8d69288fbee4904.
func (s *Session) writeEmptyTree() (plumbing.Hash, error) {
	tree := &object.Tree{}
	obj := s.Sidecar.repo.Storer.NewEncodedObject()
	if err := tree.Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return s.Sidecar.repo.Storer.SetEncodedObject(obj)
}

// TreeEntry describes one node while building a tree. Internal helper exposed
// only for tests.
type treeEntry struct {
	name string
	hash plumbing.Hash
	mode filemode.FileMode
}

// entriesToTree sorts entries by name (git requirement) and writes the tree
// object, returning its hash.
func (s *Session) entriesToTree(entries []treeEntry) (plumbing.Hash, error) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	tree := &object.Tree{}
	for _, e := range entries {
		tree.Entries = append(tree.Entries, object.TreeEntry{
			Name: e.name,
			Mode: e.mode,
			Hash: e.hash,
		})
	}
	obj := s.Sidecar.repo.Storer.NewEncodedObject()
	if err := tree.Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return s.Sidecar.repo.Storer.SetEncodedObject(obj)
}

// HashString is a small convenience — returns the hex of a plumbing hash or
// "-" for the zero hash.
func HashString(h plumbing.Hash) string {
	if h.IsZero() {
		return "-"
	}
	return hex.EncodeToString(h[:])
}
