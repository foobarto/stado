package audit_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/audit"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

func sha256Trailer(s string) string {
	if len(s) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// buildMutationChain creates a signed two-commit mutation provenance chain on
// the trace ref: an original-result commit (blob-backed) then a mutation
// commit parenting it (blob-backed, carrying Original-Result-SHA +
// Mutated-By-Hook). Returns the session, the public key, and both commit
// hashes. Mirrors what the executor emits at runtime, but constructed via the
// producer API directly so the linkage validator is tested in isolation.
func buildMutationChain(t *testing.T) (*stadogit.Session, ed25519.PublicKey, plumbing.Hash, plumbing.Hash) {
	t.Helper()
	sc, err := stadogit.OpenOrInitSidecar(filepath.Join(t.TempDir(), "sc.git"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := stadogit.CreateSession(sc, t.TempDir(), "sess-mut", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sess.Signer = audit.NewSigner(priv)

	const original = "raw secret value"
	const mutated = "raw [REDACTED] value"
	origSHA := sha256Trailer(original)
	mutSHA := sha256Trailer(mutated)

	// Original-result commit (no mutation trailers).
	origHash, _, err := sess.CommitToTraceBlob(stadogit.CommitMeta{
		Tool: "read", ShortArg: "x", Summary: "non-mutating [ok]",
		ResultSHA: origSHA, Agent: "test", Turn: 1,
	}, []byte(original))
	if err != nil {
		t.Fatalf("original commit: %v", err)
	}
	// Mutation commit (parents the original via commitOnRef chaining).
	mutHash, _, err := sess.CommitToTraceBlob(stadogit.CommitMeta{
		Tool: "read", ShortArg: "x", Summary: "non-mutating [ok]",
		ResultSHA: mutSHA, OriginalResultSHA: origSHA, MutatedByHook: "redact",
		Agent: "test", Turn: 1,
	}, []byte(mutated))
	if err != nil {
		t.Fatalf("mutation commit: %v", err)
	}
	return sess, pub, origHash, mutHash
}

// TestVerify_ValidMutationChain: a well-formed mutation chain verifies (all
// signatures valid) AND the link is reported with no broken anomalies.
func TestVerify_ValidMutationChain(t *testing.T) {
	sess, pub, origHash, mutHash := buildMutationChain(t)

	head, err := sess.TraceHead()
	if err != nil {
		t.Fatal(err)
	}
	w := audit.NewWalker(sess.Sidecar.Repo().Storer, pub)
	res, err := w.Verify("trace", head)
	if err != nil {
		t.Fatal(err)
	}

	// Signatures all valid.
	if res.Invalid != 0 || res.Unsigned != 0 {
		t.Errorf("clean chain: Invalid=%d Unsigned=%d, want 0/0", res.Invalid, res.Unsigned)
	}
	// Exactly one mutation link, intact.
	if len(res.MutationChain) != 1 {
		t.Fatalf("want 1 mutation link, got %d", len(res.MutationChain))
	}
	link := res.MutationChain[0]
	if link.Broken {
		t.Errorf("valid chain link should not be broken: %s", link.BrokenReason)
	}
	if res.BrokenLinks() != 0 {
		t.Errorf("BrokenLinks = %d, want 0", res.BrokenLinks())
	}
	if link.Commit != mutHash {
		t.Errorf("link.Commit = %s, want mutation %s", link.Commit, mutHash)
	}
	if link.Parent != origHash {
		t.Errorf("link.Parent = %s, want original %s", link.Parent, origHash)
	}
	if link.ByHook != "redact" {
		t.Errorf("link.ByHook = %q, want redact", link.ByHook)
	}
	if link.Tool != "read" {
		t.Errorf("link.Tool = %q, want read", link.Tool)
	}
	if !link.BlobBacked {
		t.Error("link should be blob-backed (the (c) check ran against real bytes)")
	}
}

// TestVerify_TamperedMutationChain: breaking the Original-Result-SHA link is
// reported as a BROKEN link anomaly while the signature chain STILL verifies —
// proving the broken-link anomaly class is distinct from signature validity.
//
// The tamper here rewrites the ORIGINAL commit's recorded result blob (and its
// Result-SHA trailer) AND re-signs it as a valid signature, then leaves the
// mutation commit's Original-Result-SHA pointing at the now-stale digest. Both
// commits verify (valid signatures), but the content linkage is broken.
func TestVerify_TamperedMutationChain(t *testing.T) {
	sc, err := stadogit.OpenOrInitSidecar(filepath.Join(t.TempDir(), "sc.git"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := stadogit.CreateSession(sc, t.TempDir(), "sess-tamper", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := audit.NewSigner(priv)
	sess.Signer = signer

	const original = "raw secret value"
	const mutated = "raw [REDACTED] value"
	origSHA := sha256Trailer(original)
	mutSHA := sha256Trailer(mutated)

	// Original-result commit, recording a DIFFERENT Result-SHA than what the
	// mutation commit's Original-Result-SHA will point to — the broken link.
	// We sign it validly so the signature chain stays intact; only the
	// content linkage is inconsistent. This models an attacker who rewrote
	// the original-result entry after the fact and re-signed it.
	const tamperedOriginal = "raw INNOCUOUS value"
	tamperedOrigSHA := sha256Trailer(tamperedOriginal)
	if _, _, err := sess.CommitToTraceBlob(stadogit.CommitMeta{
		Tool: "read", ShortArg: "x", Summary: "non-mutating [ok]",
		ResultSHA: tamperedOrigSHA, Agent: "test", Turn: 1,
	}, []byte(tamperedOriginal)); err != nil {
		t.Fatalf("tampered original commit: %v", err)
	}
	// Mutation commit still claims the REAL original's SHA (origSHA) — which
	// no longer matches the parent's Result-SHA (tamperedOrigSHA).
	if _, _, err := sess.CommitToTraceBlob(stadogit.CommitMeta{
		Tool: "read", ShortArg: "x", Summary: "non-mutating [ok]",
		ResultSHA: mutSHA, OriginalResultSHA: origSHA, MutatedByHook: "redact",
		Agent: "test", Turn: 1,
	}, []byte(mutated)); err != nil {
		t.Fatalf("mutation commit: %v", err)
	}

	head, err := sess.TraceHead()
	if err != nil {
		t.Fatal(err)
	}
	w := audit.NewWalker(sess.Sidecar.Repo().Storer, pub)
	res, err := w.Verify("trace", head)
	if err != nil {
		t.Fatal(err)
	}

	// Signatures STILL verify — the anomaly is NOT a signature failure.
	if res.Invalid != 0 {
		t.Errorf("signature chain should still verify on a content-linkage tamper, Invalid=%d", res.Invalid)
	}
	if res.Unsigned != 0 {
		t.Errorf("Unsigned=%d, want 0", res.Unsigned)
	}
	if res.InvalidAt != plumbing.ZeroHash {
		t.Errorf("InvalidAt should be zero for a broken-link-only tamper, got %s", res.InvalidAt)
	}

	// But the broken link IS reported.
	if res.BrokenLinks() != 1 {
		t.Fatalf("BrokenLinks = %d, want 1", res.BrokenLinks())
	}
	var broken audit.MutationLink
	for _, l := range res.MutationChain {
		if l.Broken {
			broken = l
		}
	}
	if !strings.Contains(broken.BrokenReason, "Original-Result-SHA") {
		t.Errorf("BrokenReason should cite the SHA mismatch, got %q", broken.BrokenReason)
	}
	if broken.OriginalSHA != origSHA {
		t.Errorf("broken link OriginalSHA = %q, want %q", broken.OriginalSHA, origSHA)
	}
}

// TestVerify_LegacyChainNoProvenance is the STAGE 8 backward-compat
// regression: a pre-fix-style chain — empty-tree trace commits with NO
// provenance trailers, exactly what v0.62/v0.63 wrote — verifies OK with ZERO
// MutationChain entries and ZERO broken links. Proves the fix is non-breaking:
// absence of the trailers is "not a mutation commit", never a broken link.
func TestVerify_LegacyChainNoProvenance(t *testing.T) {
	sc, err := stadogit.OpenOrInitSidecar(filepath.Join(t.TempDir(), "sc.git"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := stadogit.CreateSession(sc, t.TempDir(), "sess-legacy", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sess.Signer = audit.NewSigner(priv)

	// Pre-fix shape: empty-tree trace commits via CommitToTrace, plain
	// tool-call metadata, no Original-Result-SHA / Mutated-By-Hook.
	for i := 0; i < 4; i++ {
		if _, err := sess.CommitToTrace(stadogit.CommitMeta{
			Tool: "grep", ShortArg: "foo", Summary: "non-mutating [ok]",
			ResultSHA: sha256Trailer("result"), Turn: i,
		}); err != nil {
			t.Fatalf("CommitToTrace: %v", err)
		}
	}

	head, err := sess.TraceHead()
	if err != nil {
		t.Fatal(err)
	}
	w := audit.NewWalker(sess.Sidecar.Repo().Storer, pub)
	res, err := w.Verify("trace", head)
	if err != nil {
		t.Fatal(err)
	}

	if res.TotalCommits != 4 || res.Signed != 4 || res.Invalid != 0 || res.Unsigned != 0 {
		t.Errorf("legacy clean walk: %+v", res)
	}
	if len(res.MutationChain) != 0 {
		t.Errorf("legacy chain must surface NO mutation links, got %d", len(res.MutationChain))
	}
	if res.BrokenLinks() != 0 {
		t.Errorf("legacy chain BrokenLinks = %d, want 0", res.BrokenLinks())
	}
}
