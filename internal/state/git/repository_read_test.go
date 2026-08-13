package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
)

func TestRepositoryEvidenceReadsAndSearchesImmutableTree(t *testing.T) {
	sc := tempSidecar(t, t.TempDir())
	worktree := t.TempDir()
	sess, err := CreateSession(sc, worktree, "repository-evidence", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(worktree, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "README.md"), []byte("supervised work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "internal", "worker.go"), []byte("package internal\n\nfunc Worker() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("README.md", filepath.Join(worktree, "readme-link")); err != nil {
		t.Fatal(err)
	}
	tree, err := sess.BuildTreeFromDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	head, err := sess.CommitToTree(tree, CommitMeta{Tool: "test", Summary: "snapshot"})
	if err != nil {
		t.Fatal(err)
	}

	data, err := sess.ReadFileAtHead(head, "internal/worker.go", 1024)
	if err != nil || !strings.Contains(string(data), "func Worker") {
		t.Fatalf("repository read data=%q err=%v", data, err)
	}
	if _, err := sess.ReadFileAtHead(head, "../outside", 1024); err == nil {
		t.Fatal("repository traversal read succeeded")
	}
	if _, err := sess.ReadFileAtHead(head, "readme-link", 1024); err == nil {
		t.Fatal("repository symlink read succeeded")
	}
	files, more, err := sess.ListFilesAtHead(head, "internal", 10)
	if err != nil || more || len(files) != 1 || files[0] != "internal/worker.go" {
		t.Fatalf("repository files=%v more=%t err=%v", files, more, err)
	}
	matches, partial, err := sess.SearchFilesAtHead(head, "internal", "worker", 10, 100, 1<<20)
	if err != nil || partial || len(matches) != 2 || matches[0].Line != 0 || matches[1].Line != 3 {
		t.Fatalf("repository matches=%+v partial=%t err=%v", matches, partial, err)
	}
}

func TestRepositorySearchReportsScanCeiling(t *testing.T) {
	sc := tempSidecar(t, t.TempDir())
	worktree := t.TempDir()
	sess, err := CreateSession(sc, worktree, "repository-search-limit", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree, "large.txt"), []byte(strings.Repeat("x", 4096)), 0o644); err != nil {
		t.Fatal(err)
	}
	tree, err := sess.BuildTreeFromDir(worktree)
	if err != nil {
		t.Fatal(err)
	}
	head, err := sess.CommitToTree(tree, CommitMeta{Tool: "test", Summary: "snapshot"})
	if err != nil {
		t.Fatal(err)
	}
	matches, partial, err := sess.SearchFilesAtHead(head, "", "missing", 10, 100, 128)
	if err != nil || len(matches) != 0 || !partial {
		t.Fatalf("limited search matches=%+v partial=%t err=%v", matches, partial, err)
	}
}
