package git

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestListWorktreeSessionIDsStreamsAndSortsValidDirs(t *testing.T) {
	root := t.TempDir()
	valid := []string{"session-z", "session-a"}
	for _, id := range valid {
		if err := os.Mkdir(filepath.Join(root, id), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "session-file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ListWorktreeSessionIDs(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"session-a", "session-z"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListWorktreeSessionIDs = %v, want %v", got, want)
	}
}

func TestListWorktreeSessionIDsRejectsTooManyEntries(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"session-a", "session-b", "session-c"} {
		if err := os.Mkdir(filepath.Join(root, id), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	_, err := listWorktreeSessionIDsLimited(root, 2)
	if err == nil {
		t.Fatal("listWorktreeSessionIDsLimited succeeded with too many entries")
	}
	if !strings.Contains(err.Error(), "more than") {
		t.Fatalf("listWorktreeSessionIDsLimited error = %v, want entry cap", err)
	}
}

func TestListWorktreeSessionIDsSkipsSymlinkDirs(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "session-target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "session-link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	got, err := ListWorktreeSessionIDs(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "session-target" {
		t.Fatalf("ListWorktreeSessionIDs = %v, want only real target directory", got)
	}
}
