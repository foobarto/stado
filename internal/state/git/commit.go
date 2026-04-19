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
	// Plugin identifies the plugin that initiated this action, for
	// trace commits made on behalf of plugin-triggered LLM invocations,
	// forks, or tool calls. Empty for actions the core agent loop ran
	// directly. Surfaces as a `Plugin:` trailer so `git log` + `stado
	// audit export` can attribute every commit correctly per DESIGN
	// §"Plugin extension points for context management" invariant 3.
	Plugin string

	// preformatted lets callers (e.g. CommitCompaction) pass an
	// already-rendered message through commitOnRef without going
	// through the tool-call-oriented trailer layout below. Empty →
	// formatMessage builds the standard CommitMeta form.
	preformatted string
}

// formatMessage renders a CommitMeta into the structured commit message.
// First line: `<tool>(<short-arg>): <summary>`. Blank line. Trailer block.
// When preformatted is non-empty, it's returned as-is — the caller has
// already produced the final message (compaction, future custom events).
func (c CommitMeta) formatMessage() string {
	if c.preformatted != "" {
		return c.preformatted
	}
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
	if c.Plugin != "" {
		trailers = append(trailers, struct{ k, v string }{"Plugin", c.Plugin})
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

// CompactionMeta is the payload for a user-accepted /compact event.
// Kept separate from CommitMeta because compaction commits carry
// summary-prose metadata rather than tool-call telemetry — different
// audience (humans reading `git log`), different trailers.
type CompactionMeta struct {
	Title       string // short single-line title for the commit subject
	Summary     string // full summary body
	FromTurn    int    // first turn included in the compaction (0 = session start)
	ToTurn      int    // last turn included
	TurnsTotal  int    // number of turns collapsed (for audit)
	ByAuthor    string // who/what ran the compaction (usually the session's bot identity)
}

// formatCompactionMessage renders CompactionMeta into the structured
// commit message format shared across tree + trace refs. First line is
// the subject; body is the summary; trailers pin the turn range and
// audit timestamp.
func (c CompactionMeta) formatMessage(ts time.Time) string {
	var b strings.Builder
	title := c.Title
	if title == "" {
		title = fmt.Sprintf("compaction: turns %d..%d", c.FromTurn, c.ToTurn)
	}
	b.WriteString("Compaction: ")
	b.WriteString(title)
	b.WriteString("\n\n")
	if c.Summary != "" {
		b.WriteString(strings.TrimSpace(c.Summary))
		b.WriteString("\n\n")
	}
	trailers := []struct{ k, v string }{
		{"Compaction-From-Turn", fmt.Sprintf("%d", c.FromTurn)},
		{"Compaction-To-Turn", fmt.Sprintf("%d", c.ToTurn)},
		{"Compaction-Turns-Total", fmt.Sprintf("%d", c.TurnsTotal)},
		{"Compaction-At", ts.UTC().Format(time.RFC3339)},
	}
	if c.ByAuthor != "" {
		trailers = append(trailers, struct{ k, v string }{"Compaction-By", c.ByAuthor})
	}
	for _, t := range trailers {
		if t.v == "" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", t.k, t.v)
	}
	return b.String()
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
	// Compaction on an empty session is a no-op — nothing to compact.
	treeHead, err := s.Sidecar.resolveRef(TreeRef(s.ID))
	if err != nil {
		return plumbing.ZeroHash, plumbing.ZeroHash, fmt.Errorf("compaction: no tree ref yet (session has no commits): %w", err)
	}
	headCommit, err := object.GetCommit(s.Sidecar.repo.Storer, treeHead)
	if err != nil {
		return plumbing.ZeroHash, plumbing.ZeroHash, fmt.Errorf("compaction: resolve tree head: %w", err)
	}

	// Synthetic CommitMeta lets us reuse commitOnRef (signing, OnCommit
	// hook, encoded-object plumbing) without a parallel pathway.
	// formatMessage returns a single string — we inject it by setting
	// Summary to the pre-formatted payload and nulling the title/tool
	// fields that would otherwise be prepended.
	now := time.Now()
	payload := meta.formatMessage(now)

	treeSHA, err = s.commitOnRef(TreeRef(s.ID), headCommit.TreeHash, preformattedMeta(payload))
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

// preformattedMeta wraps a fully-rendered commit message as a
// CommitMeta that passes it through unchanged. Used when the caller
// has its own formatter (e.g. CompactionMeta.formatMessage) and
// doesn't want CommitMeta's tool-call-specific layout.
func preformattedMeta(msg string) CommitMeta {
	return CommitMeta{preformatted: msg}
}

// CompactionMarker is one compaction event surfaced on the session's
// tree ref. `stado session show` uses this to render the compaction
// timeline per PLAN §11.3.6. Extracted by walking the tree ref and
// parsing the trailers that `CompactionMeta.formatMessage` emits.
type CompactionMarker struct {
	CommitHash  plumbing.Hash // the tree-ref commit carrying the compaction
	Title       string        // subject line after "Compaction: "
	FromTurn    int
	ToTurn      int
	TurnsTotal  int
	At          string // RFC3339 from Compaction-At trailer (raw)
	By          string // Compaction-By trailer; may be empty
}

// ListCompactions walks the tree ref's first-parent chain from HEAD
// and returns every commit whose subject starts with "Compaction: "
// in newest-first order. Other commits (tool calls, seed commits)
// are skipped. Returns an empty slice + nil error when the session
// has no compactions (common).
func (s *Sidecar) ListCompactions(sessionID string) ([]CompactionMarker, error) {
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
