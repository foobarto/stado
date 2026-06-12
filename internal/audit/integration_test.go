package audit_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/foobarto/stado/internal/audit"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

// End-to-end: sign every commit via Session.Signer, then audit.Walker over
// the ref verifies all of them. Tampering with any signed commit's message
// is detected.
func TestE2E_SignAndVerifyRefWalk(t *testing.T) {
	sc, err := stadogit.OpenOrInitSidecar(filepath.Join(t.TempDir(), "sc.git"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	wt := t.TempDir()
	sess, err := stadogit.CreateSession(sc, wt, "sess-e2e", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sess.Signer = audit.NewSigner(priv)

	for i := 0; i < 3; i++ {
		if _, err := sess.CommitToTrace(stadogit.CommitMeta{
			Tool: "grep", ShortArg: "foo", Summary: "search", Turn: i,
		}); err != nil {
			t.Fatalf("CommitToTrace: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(sess.WorktreePath, "a"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tree, _ := sess.BuildTreeFromDir(sess.WorktreePath)
	if _, err := sess.CommitToTree(tree, stadogit.CommitMeta{Tool: "write", ShortArg: "a"}); err != nil {
		t.Fatalf("CommitToTree: %v", err)
	}

	head, err := sess.TraceHead()
	if err != nil {
		t.Fatal(err)
	}
	w := audit.NewWalker(sc.Repo().Storer, pub)
	res, err := w.Verify("trace", head)
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalCommits != 3 || res.Signed != 3 || res.Invalid != 0 || res.Unsigned != 0 {
		t.Errorf("clean walk: %+v", res)
	}

	// Tamper: rewrite a commit with a mutated Tool trailer.
	commit, err := object.GetCommit(sc.Repo().Storer, head)
	if err != nil {
		t.Fatal(err)
	}
	commit.Message = strings.Replace(commit.Message, "Tool: grep", "Tool: evil", 1)
	obj := sc.Repo().Storer.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		t.Fatal(err)
	}
	tamperedHead, err := sc.Repo().Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatal(err)
	}

	res2, err := w.Verify("trace", tamperedHead)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Invalid == 0 {
		t.Errorf("tampered walk should flag at least one invalid, got %+v", res2)
	}
}

// TestE2E_LegacyV1ReportedDistinctly: after the 2026-06-12 clean-break, a
// pre-v2 (legacy v1) signature no longer verifies, but the walk must classify
// it distinctly from tamper — LegacyV1, not Invalid — so an operator with old
// history sees "legacy, re-sign to verify" rather than a tampered chain.
func TestE2E_LegacyV1ReportedDistinctly(t *testing.T) {
	sc, err := stadogit.OpenOrInitSidecar(filepath.Join(t.TempDir(), "sc.git"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := stadogit.CreateSession(sc, t.TempDir(), "sess-legacy", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := audit.NewSigner(priv)
	sess.Signer = signer
	for i := 0; i < 3; i++ {
		if _, err := sess.CommitToTrace(stadogit.CommitMeta{Tool: "grep", Summary: "s", Turn: i}); err != nil {
			t.Fatalf("CommitToTrace: %v", err)
		}
	}
	head, err := sess.TraceHead()
	if err != nil {
		t.Fatal(err)
	}

	// Downgrade the HEAD commit's signature to legacy v1 (re-sign over the v1
	// CanonicalBytes payload), preserving its tree/parents/identity.
	commit, err := object.GetCommit(sc.Repo().Storer, head)
	if err != nil {
		t.Fatal(err)
	}
	parents := make([]string, len(commit.ParentHashes))
	for i, p := range commit.ParentHashes {
		parents[i] = p.String()
	}
	v1sig := signer.Sign(commit.TreeHash.String(), parents, commit.Message)
	commit.Message = audit.AppendTrailer(commit.Message, v1sig)
	obj := sc.Repo().Storer.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		t.Fatal(err)
	}
	legacyHead, err := sc.Repo().Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatal(err)
	}

	res, err := audit.NewWalker(sc.Repo().Storer, pub).Verify("trace", legacyHead)
	if err != nil {
		t.Fatal(err)
	}
	if res.LegacyV1 != 1 {
		t.Errorf("LegacyV1 = %d, want 1: %+v", res.LegacyV1, res)
	}
	if res.Invalid != 0 {
		t.Errorf("a legacy v1 sig must NOT count as Invalid (tamper): %+v", res)
	}
	if res.Signed != 2 {
		t.Errorf("Signed = %d, want 2 (the v2 parents): %+v", res.Signed, res)
	}
	if res.FirstLegacyV1At != legacyHead {
		t.Errorf("FirstLegacyV1At = %s, want %s", res.FirstLegacyV1At, legacyHead)
	}
}

// TestE2E_MultipleSignatureTrailersInvalid: a commit carrying two Signature
// trailers (trailer-injection) must walk as Invalid (tamper signal), not as
// the benign Unsigned class. (codex review on #139.)
func TestE2E_MultipleSignatureTrailersInvalid(t *testing.T) {
	sc, err := stadogit.OpenOrInitSidecar(filepath.Join(t.TempDir(), "sc.git"), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := stadogit.CreateSession(sc, t.TempDir(), "sess-dup", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := audit.NewSigner(priv)
	sess.Signer = signer
	if _, err := sess.CommitToTrace(stadogit.CommitMeta{Tool: "grep", Summary: "s"}); err != nil {
		t.Fatalf("CommitToTrace: %v", err)
	}
	head, err := sess.TraceHead()
	if err != nil {
		t.Fatal(err)
	}

	commit, err := object.GetCommit(sc.Repo().Storer, head)
	if err != nil {
		t.Fatal(err)
	}
	// Inject a SECOND Signature trailer (signer.Sign returns the ed25519: form).
	second := signer.Sign(commit.TreeHash.String(), nil, "anything")
	commit.Message = strings.TrimRight(commit.Message, "\n") + "\nSignature: " + second + "\n"
	obj := sc.Repo().Storer.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		t.Fatal(err)
	}
	dupHead, err := sc.Repo().Storer.SetEncodedObject(obj)
	if err != nil {
		t.Fatal(err)
	}

	res, err := audit.NewWalker(sc.Repo().Storer, pub).Verify("trace", dupHead)
	if err != nil {
		t.Fatal(err)
	}
	if res.Invalid != 1 {
		t.Errorf("duplicate-trailer commit: Invalid = %d, want 1: %+v", res.Invalid, res)
	}
	if res.Unsigned != 0 {
		t.Errorf("duplicate-trailer commit must NOT be classified Unsigned: %+v", res)
	}
}
