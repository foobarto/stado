package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	stadogit "github.com/foobarto/stado/internal/state/git"
)

func TestPlanSubagentAdoptionAllowsDisjointChanges(t *testing.T) {
	cfg, parent, _ := forkPluginEnv(t)
	writeAndCommitTree(t, parent, "seed", map[string]string{
		"base.txt": "base",
	})
	child, err := ForkSession(cfg, parent)
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	forkTree, err := child.CurrentTree()
	if err != nil {
		t.Fatalf("child.CurrentTree: %v", err)
	}
	writeAndCommitTree(t, child, "child", map[string]string{
		"child.txt": "child",
	})
	writeAndCommitTree(t, parent, "parent", map[string]string{
		"parent.txt": "parent",
	})

	plan, err := PlanSubagentAdoption(parent, child, forkTree)
	if err != nil {
		t.Fatalf("PlanSubagentAdoption: %v", err)
	}
	if !plan.CanAdopt {
		t.Fatalf("CanAdopt = false, conflicts = %#v", plan.Conflicts)
	}
	if want := []string{"child.txt"}; !reflect.DeepEqual(plan.ChangedFiles, want) {
		t.Fatalf("ChangedFiles = %#v, want %#v", plan.ChangedFiles, want)
	}
	if want := []string{"parent.txt"}; !reflect.DeepEqual(plan.ParentChangedFiles, want) {
		t.Fatalf("ParentChangedFiles = %#v, want %#v", plan.ParentChangedFiles, want)
	}
	if plan.ForkTree == "" || plan.ParentTree == "" || plan.ChildTree == "" {
		t.Fatalf("plan missing tree identities: %+v", plan)
	}
}

func TestAdoptSubagentChangesAppliesDisjointChanges(t *testing.T) {
	cfg, parent, _ := forkPluginEnv(t)
	writeAndCommitTree(t, parent, "seed", map[string]string{
		"base.txt": "base",
	})
	child, err := ForkSession(cfg, parent)
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	forkTree, err := child.CurrentTree()
	if err != nil {
		t.Fatalf("child.CurrentTree: %v", err)
	}
	writeAndCommitTree(t, child, "child", map[string]string{
		"child.txt": "child",
	})
	writeAndCommitTree(t, parent, "parent", map[string]string{
		"parent.txt": "parent",
	})

	plan, err := AdoptSubagentChanges(parent, child, forkTree, "test-agent", "test-model")
	if err != nil {
		t.Fatalf("AdoptSubagentChanges: %v", err)
	}
	if !plan.Applied || !plan.CanAdopt {
		t.Fatalf("plan = %+v, want applied adoptable plan", plan)
	}
	if want := []string{"child.txt"}; !reflect.DeepEqual(plan.AdoptedFiles, want) {
		t.Fatalf("AdoptedFiles = %#v, want %#v", plan.AdoptedFiles, want)
	}
	if got := readWorktreeFile(t, parent, "child.txt"); got != "child" {
		t.Fatalf("parent child.txt = %q, want child", got)
	}
	if got := readWorktreeFile(t, parent, "parent.txt"); got != "parent" {
		t.Fatalf("parent parent.txt = %q, want parent", got)
	}
	if plan.AdoptedTree == "" {
		t.Fatalf("AdoptedTree missing: %+v", plan)
	}
	if head, err := parent.TraceHead(); err != nil || head.IsZero() {
		t.Fatalf("parent trace head missing after adoption: %s %v", head, err)
	}
}

func TestAdoptSubagentChangesDeletesChildDeletedFiles(t *testing.T) {
	cfg, parent, _ := forkPluginEnv(t)
	writeAndCommitTree(t, parent, "seed", map[string]string{
		"gone.txt": "base",
	})
	child, err := ForkSession(cfg, parent)
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	forkTree, err := child.CurrentTree()
	if err != nil {
		t.Fatalf("child.CurrentTree: %v", err)
	}
	if err := os.Remove(filepath.Join(child.WorktreePath, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	commitCurrentTree(t, child, "delete")

	plan, err := AdoptSubagentChanges(parent, child, forkTree, "test-agent", "test-model")
	if err != nil {
		t.Fatalf("AdoptSubagentChanges: %v", err)
	}
	if !plan.Applied {
		t.Fatalf("plan = %+v, want applied", plan)
	}
	if _, err := os.Stat(filepath.Join(parent.WorktreePath, "gone.txt")); !os.IsNotExist(err) {
		t.Fatalf("gone.txt still exists or unexpected stat error: %v", err)
	}
}

func TestAdoptSubagentChangesBlocksConflicts(t *testing.T) {
	cfg, parent, _ := forkPluginEnv(t)
	writeAndCommitTree(t, parent, "seed", map[string]string{
		"same.txt": "base",
	})
	child, err := ForkSession(cfg, parent)
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	forkTree, err := child.CurrentTree()
	if err != nil {
		t.Fatalf("child.CurrentTree: %v", err)
	}
	writeAndCommitTree(t, child, "child", map[string]string{
		"same.txt": "child",
	})
	writeAndCommitTree(t, parent, "parent", map[string]string{
		"same.txt": "parent",
	})

	plan, err := AdoptSubagentChanges(parent, child, forkTree, "test-agent", "test-model")
	if !errors.Is(err, ErrSubagentAdoptionConflict) {
		t.Fatalf("error = %v, want ErrSubagentAdoptionConflict", err)
	}
	if plan.CanAdopt || plan.Applied {
		t.Fatalf("plan = %+v, want blocked unapplied plan", plan)
	}
	if got := readWorktreeFile(t, parent, "same.txt"); got != "parent" {
		t.Fatalf("parent same.txt = %q, want parent", got)
	}
}

func TestPlanSubagentAdoptionBlocksConflictingChanges(t *testing.T) {
	cfg, parent, _ := forkPluginEnv(t)
	writeAndCommitTree(t, parent, "seed", map[string]string{
		"same.txt": "base",
	})
	child, err := ForkSession(cfg, parent)
	if err != nil {
		t.Fatalf("ForkSession: %v", err)
	}
	forkTree, err := child.CurrentTree()
	if err != nil {
		t.Fatalf("child.CurrentTree: %v", err)
	}
	writeAndCommitTree(t, child, "child", map[string]string{
		"same.txt": "child",
	})
	writeAndCommitTree(t, parent, "parent", map[string]string{
		"same.txt": "parent",
	})

	plan, err := PlanSubagentAdoption(parent, child, forkTree)
	if err != nil {
		t.Fatalf("PlanSubagentAdoption: %v", err)
	}
	if plan.CanAdopt {
		t.Fatalf("CanAdopt = true, want conflict: %+v", plan)
	}
	if want := []string{"same.txt"}; !reflect.DeepEqual(plan.Conflicts, want) {
		t.Fatalf("Conflicts = %#v, want %#v", plan.Conflicts, want)
	}
}

func TestCopyChildChangeRejectsSymlinkEscape(t *testing.T) {
	parent := t.TempDir()
	child := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(child, "link.txt")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	err := copyChildChange(parent, child, "link.txt")
	if err == nil {
		t.Fatal("copyChildChange should reject symlink escapes")
	}
	if _, statErr := os.Lstat(filepath.Join(parent, "link.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("unsafe link adopted, stat err = %v", statErr)
	}
}

func TestCopyChildChangeRejectsUnsafeSymlinkWithoutRemovingParent(t *testing.T) {
	parent := t.TempDir()
	child := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, "link.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(child, "link.txt")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	err := copyChildChange(parent, child, "link.txt")
	if err == nil || !strings.Contains(err.Error(), "unsafe symlink") {
		t.Fatalf("copyChildChange error = %v, want unsafe symlink rejection", err)
	}
	data, readErr := os.ReadFile(filepath.Join(parent, "link.txt"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "keep" {
		t.Fatalf("parent file modified after rejected symlink: %q", data)
	}
}

func TestCopyChildChangeRejectsOversizedRegularFileWithoutRemovingParent(t *testing.T) {
	parent := t.TempDir()
	child := t.TempDir()
	if err := os.WriteFile(filepath.Join(parent, "huge.bin"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(child, "huge.bin")
	if err := os.WriteFile(childPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(childPath, maxSubagentAdoptionFileBytes+1); err != nil {
		t.Fatal(err)
	}

	err := copyChildChange(parent, child, "huge.bin")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("copyChildChange error = %v, want size rejection", err)
	}
	data, readErr := os.ReadFile(filepath.Join(parent, "huge.bin"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "keep" {
		t.Fatalf("parent file modified after oversized child: %q", data)
	}
}

func TestCopyChildChangeRejectsParentSymlinkSwap(t *testing.T) {
	parent := t.TempDir()
	child := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(child, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "dir", "file.txt"), []byte("child"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(parent, "dir")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	err := copyChildChange(parent, child, filepath.Join("dir", "file.txt"))
	if err == nil {
		t.Fatal("copyChildChange should reject parent symlink escapes")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "file.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("outside write occurred, stat err = %v", statErr)
	}
}

func TestCopyChildChangeReplacesParentFinalSymlink(t *testing.T) {
	parent := t.TempDir()
	child := t.TempDir()
	if err := os.WriteFile(filepath.Join(child, "link.txt"), []byte("child"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "target.txt")
	if err := os.WriteFile(target, []byte("target"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(parent, "link.txt")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if err := copyChildChange(parent, child, "link.txt"); err != nil {
		t.Fatalf("copyChildChange: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "target" {
		t.Fatalf("parent symlink target modified: %q", data)
	}
	info, err := os.Lstat(filepath.Join(parent, "link.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("parent final symlink was not replaced")
	}
	linkData, err := os.ReadFile(filepath.Join(parent, "link.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(linkData); got != "child" {
		t.Fatalf("parent link.txt = %q, want child", got)
	}
}

func TestCopyChildChangeNormalizesFileModes(t *testing.T) {
	parent := t.TempDir()
	child := t.TempDir()
	if err := os.WriteFile(filepath.Join(child, "regular.txt"), []byte("regular"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(child, "regular.txt"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "script.sh"), []byte("#!/bin/sh\n"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(child, "script.sh"), 0o777); err != nil {
		t.Fatal(err)
	}

	if err := copyChildChange(parent, child, "regular.txt"); err != nil {
		t.Fatalf("copy regular: %v", err)
	}
	if err := copyChildChange(parent, child, "script.sh"); err != nil {
		t.Fatalf("copy executable: %v", err)
	}
	assertPerm := func(path string, want os.FileMode) {
		t.Helper()
		info, err := os.Stat(filepath.Join(parent, path))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode = %04o, want %04o", path, got, want)
		}
	}
	assertPerm("regular.txt", 0o644)
	assertPerm("script.sh", 0o755)
}

func commitCurrentTree(t *testing.T, sess *stadogit.Session, summary string) {
	t.Helper()
	tree, err := sess.BuildTreeFromDir(sess.WorktreePath)
	if err != nil {
		t.Fatalf("BuildTreeFromDir: %v", err)
	}
	if _, err := sess.CommitToTree(tree, stadogit.CommitMeta{Tool: "write", Summary: summary}); err != nil {
		t.Fatalf("CommitToTree: %v", err)
	}
}

func writeAndCommitTree(t *testing.T, sess *stadogit.Session, summary string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		full := filepath.Join(sess.WorktreePath, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commitCurrentTree(t, sess, summary)
}

func readWorktreeFile(t *testing.T, sess *stadogit.Session, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(sess.WorktreePath, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
