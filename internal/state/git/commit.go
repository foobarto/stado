package git

import (
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// readEncodedObject drains an EncodedObject's Reader and returns its
// bytes. Used to recover the canonical git bytes after Encode so the
// SSH signer can sign exactly what git signs.
func readEncodedObject(obj plumbing.EncodedObject) ([]byte, error) {
	r, err := obj.Reader()
	if err != nil {
		return nil, err
	}
	defer func() { _ = r.Close() }()
	return io.ReadAll(r)
}

var _ = strings.Builder{} // keep strings import usage visible after Phase 5 edit

// CommitMeta is the structured per-tool-call metadata we record in every
// commit message (both tree and trace refs). Machine-parseable trailers so
// `stado audit export` / SIEM ingestion can reconstruct the call.
//
// See PLAN.md §2.5 for the commit-message format.
type CommitMeta struct {
	Tool        string
	ShortArg    string // small summary used in the title line
	Summary     string // human one-liner (also the title)
	ArgsSHA     string
	ResultSHA   string
	TokensIn    int
	TokensOut   int
	CacheHit    bool
	CostUSD     float64
	Model       string
	DurationMs  int64
	Agent       string
	Turn        int
	Error       string
}

// formatMessage renders a CommitMeta into the structured commit message.
// First line: `<tool>(<short-arg>): <summary>`. Blank line. Trailer block.
func (c CommitMeta) formatMessage() string {
	var b strings.Builder
	title := fmt.Sprintf("%s", c.Tool)
	if c.ShortArg != "" {
		title += "(" + c.ShortArg + ")"
	}
	if c.Summary != "" {
		title += ": " + c.Summary
	}
	b.WriteString(title)
	b.WriteString("\n\n")

	trailers := []struct{ k, v string }{
		{"Tool", c.Tool},
		{"Args-SHA", c.ArgsSHA},
		{"Result-SHA", c.ResultSHA},
		{"Tokens-In", fmt.Sprintf("%d", c.TokensIn)},
		{"Tokens-Out", fmt.Sprintf("%d", c.TokensOut)},
		{"Cache-Hit", boolStr(c.CacheHit)},
		{"Cost-USD", fmt.Sprintf("%.4f", c.CostUSD)},
		{"Model", c.Model},
		{"Duration-Ms", fmt.Sprintf("%d", c.DurationMs)},
		{"Agent", c.Agent},
		{"Turn", fmt.Sprintf("%d", c.Turn)},
	}
	if c.Error != "" {
		trailers = append(trailers, struct{ k, v string }{"Error", c.Error})
	}
	for _, t := range trailers {
		if t.v == "" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", t.k, t.v)
	}
	return b.String()
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

// boolStr prints true/false rather than "1"/"0" to match PLAN.md's trailer.
func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
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
