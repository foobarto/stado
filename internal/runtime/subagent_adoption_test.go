package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
