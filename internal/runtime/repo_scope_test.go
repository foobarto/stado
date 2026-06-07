package runtime

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestListRepoWorktreeSessionIDsScopesToRepo is the regression guard for the
// B1 usability bug: `session list` and the TUI session manager augmented their
// listing with the GLOBAL worktree dir and never filtered by the worktree's
// user-repo pin, so every project's sessions leaked into every repo's listing
// (2032 cross-project sessions on the operator's box). The fix filters by the
// pin; this test pins worktrees to three different repos (one via a symlinked
// path form, to also cover the /home -> /var/home ostree split) plus one
// unpinned, and asserts only the current repo's are returned.
func TestListRepoWorktreeSessionIDsScopesToRepo(t *testing.T) {
	worktreeRoot := t.TempDir()
	repoA := t.TempDir()
	repoB := t.TempDir()

	// A symlink that points at repoA — a worktree pinned through it must
	// still be recognised as belonging to repoA (canonicalisation).
	linkA := filepath.Join(t.TempDir(), "link-to-a")
	if err := os.Symlink(repoA, linkA); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	mkPinned := func(id, pin string) {
		dir := filepath.Join(worktreeRoot, id)
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if pin != "" {
			if err := WriteUserRepoPin(dir, pin); err != nil {
				t.Fatal(err)
			}
		}
	}

	mkPinned("sess-a1", repoA) // current repo, direct
	mkPinned("sess-a2", linkA) // current repo, via symlinked path
	mkPinned("sess-b1", repoB) // other repo — must be excluded
	mkPinned("sess-none", "")  // unpinned — included (unknown provenance)

	// repoA sees its own (incl. the symlink form) + the unpinned one, never
	// the worktree pinned to repoB.
	got, err := ListRepoWorktreeSessionIDs(worktreeRoot, repoA)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"sess-a1", "sess-a2", "sess-none"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListRepoWorktreeSessionIDs(_, repoA) = %v, want %v", got, want)
	}

	// repoB sees its own + the unpinned one, never repoA's worktrees.
	gotB, err := ListRepoWorktreeSessionIDs(worktreeRoot, repoB)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotB, []string{"sess-b1", "sess-none"}) {
		t.Fatalf("ListRepoWorktreeSessionIDs(_, repoB) = %v, want [sess-b1 sess-none]", gotB)
	}
}

func TestSameUserRepoSymlinkTolerant(t *testing.T) {
	repo := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if !SameUserRepo(repo, link) {
		t.Errorf("SameUserRepo(%q, %q) = false, want true (symlink should canonicalise equal)", repo, link)
	}
	if SameUserRepo(repo, t.TempDir()) {
		t.Error("SameUserRepo of two distinct repos = true, want false")
	}
	// Empty paths never match (an unset pin must not collapse into a match).
	if SameUserRepo("", "") {
		t.Error("SameUserRepo(\"\", \"\") = true, want false")
	}
	if SameUserRepo("", repo) {
		t.Error("SameUserRepo(\"\", repo) = true, want false")
	}
}
