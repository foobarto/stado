package runtime

import (
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
	tree, err := sess.BuildTreeFromDir(sess.WorktreePath)
	if err != nil {
		t.Fatalf("BuildTreeFromDir: %v", err)
	}
	if _, err := sess.CommitToTree(tree, stadogit.CommitMeta{Tool: "write", Summary: summary}); err != nil {
		t.Fatalf("CommitToTree: %v", err)
	}
}
