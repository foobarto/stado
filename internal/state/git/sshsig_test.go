package git

import (
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/foobarto/stado/internal/audit"
)

// TestCommit_PGPSignatureIsSSHSIGWhenSignerImplements asserts:
//   - a Session with an audit.Signer (which implements SSHCommitSigner)
//     writes a well-formed SSHSIG PEM into commit.PGPSignature
//   - the embedded pubkey round-trips back to the signer's public half
//   - VerifySSH succeeds against the canonical bytes git would sign
//     (i.e. the commit object encoded with PGPSignature cleared)
func TestCommit_PGPSignatureIsSSHSIGWhenSignerImplements(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer := audit.NewSigner(priv)

	sc := tempSidecar(t, t.TempDir())
	sess, err := CreateSession(sc, filepath.Join(sc.Path, "..", "wt"), "s-sshsig", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	sess.Signer = signer

	h, err := sess.CommitToTrace(CommitMeta{Tool: "grep", Summary: "first"})
	if err != nil {
		t.Fatalf("CommitToTrace: %v", err)
	}

	c, err := object.GetCommit(sc.repo.Storer, h)
	if err != nil {
		t.Fatal(err)
	}
	if c.PGPSignature == "" {
		t.Fatal("expected PGPSignature to be set by SSHCommitSigner path")
	}
	if !strings.HasPrefix(c.PGPSignature, "-----BEGIN SSH SIGNATURE-----") {
		t.Errorf("PGPSignature missing SSHSIG PEM header: %q", c.PGPSignature[:64])
	}

	// Round-trip: pubkey embedded in the SSHSIG blob matches the signer.
	recovered, err := audit.Ed25519PubFromSSHSIG(c.PGPSignature)
	if err != nil {
		t.Fatalf("Ed25519PubFromSSHSIG: %v", err)
	}
	if string(recovered) != string(pub) {
		t.Error("SSHSIG pubkey does not match signer's pubkey")
	}

	// Verify the signature against the canonical bytes git would sign —
	// encode with PGPSignature cleared to reproduce the pre-sign form.
	noSig := *c
	noSig.PGPSignature = ""
	probe := sc.repo.Storer.NewEncodedObject()
	if err := noSig.Encode(probe); err != nil {
		t.Fatal(err)
	}
	canonical, err := readEncodedObject(probe)
	if err != nil {
		t.Fatal(err)
	}
	if err := audit.VerifySSH(pub, audit.GitNamespace, audit.HashSHA512, canonical, c.PGPSignature); err != nil {
		t.Errorf("VerifySSH on canonical bytes failed: %v", err)
	}
}

// TestCommit_NoPGPSignatureWhenNoSigner — the gpgsig path is opt-in via
// the Signer field; Sessions without a Signer produce plain commits.
func TestCommit_NoPGPSignatureWhenNoSigner(t *testing.T) {
	sc := tempSidecar(t, t.TempDir())
	sess, err := CreateSession(sc, filepath.Join(sc.Path, "..", "wt"), "s-nosig", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	// sess.Signer stays nil.
	h, err := sess.CommitToTrace(CommitMeta{Tool: "read", Summary: "no sig"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := object.GetCommit(sc.repo.Storer, h)
	if err != nil {
		t.Fatal(err)
	}
	if c.PGPSignature != "" {
		t.Errorf("expected no PGPSignature without a Signer, got %q", c.PGPSignature)
	}
}
