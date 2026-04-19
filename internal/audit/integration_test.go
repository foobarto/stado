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
