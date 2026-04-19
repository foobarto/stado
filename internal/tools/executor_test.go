package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/pkg/tool"
)

// ---- fixture helpers ----

type stubHost struct {
	NullHost
	workdir string
}

func (h stubHost) Workdir() string { return h.workdir }

// A tool whose class is set via an inner Class field; used to drive policy.
type stubTool struct {
	name   string
	class  tool.Class
	effect func(worktree string) (tool.Result, error)
}

func (s stubTool) Name() string         { return s.name }
func (s stubTool) Description() string  { return "stub" }
func (s stubTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (s stubTool) Class() tool.Class    { return s.class }
func (s stubTool) Run(ctx context.Context, _ json.RawMessage, h tool.Host) (tool.Result, error) {
	return s.effect(h.Workdir())
}

// newSessionAndRegistry builds a fresh sidecar + session + registry for a test.
func newExecutorFixture(t *testing.T) (*Executor, *stadogit.Session, string) {
	t.Helper()
	root := t.TempDir()
	sc, err := stadogit.OpenOrInitSidecar(filepath.Join(root, "sc.git"), t.TempDir())
	if err != nil {
		t.Fatalf("sidecar: %v", err)
	}
	sess, err := stadogit.CreateSession(sc, filepath.Join(root, "wt"), "s-exec", plumbing.ZeroHash)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	reg := NewRegistry()
	exec := &Executor{
		Registry: reg,
		Session:  sess,
		Agent:    "test-agent",
		Model:    "test-model",
	}
	return exec, sess, sess.WorktreePath
}

// ---- tests ----

func TestExecutor_NonMutating_OnlyTraceCommit(t *testing.T) {
	ex, sess, wt := newExecutorFixture(t)
	ex.Registry.Register(stubTool{
		name:  "stubread",
		class: tool.ClassNonMutating,
		effect: func(string) (tool.Result, error) {
			return tool.Result{Content: "hello"}, nil
		},
	})

	_, err := ex.Run(context.Background(), "stubread", json.RawMessage(`{"path":"foo"}`), stubHost{workdir: wt})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	trace, err := sess.TraceHead()
	if err != nil || trace.IsZero() {
		t.Errorf("trace ref should be set: %v head=%s", err, trace)
	}
	tree, _ := sess.TreeHead()
	if !tree.IsZero() {
		t.Errorf("tree ref should NOT be set for non-mutating tool, got %s", tree)
	}
}

func TestExecutor_Mutating_CommitsBothRefs(t *testing.T) {
	ex, sess, wt := newExecutorFixture(t)
	ex.Registry.Register(stubTool{
		name:  "stubwrite",
		class: tool.ClassMutating,
		effect: func(workdir string) (tool.Result, error) {
			return tool.Result{Content: "ok"}, os.WriteFile(filepath.Join(workdir, "new.txt"), []byte("data"), 0o644)
		},
	})

	_, err := ex.Run(context.Background(), "stubwrite", json.RawMessage(`{"path":"new.txt"}`), stubHost{workdir: wt})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	trace, _ := sess.TraceHead()
	tree, _ := sess.TreeHead()
	if trace.IsZero() {
		t.Error("trace ref missing")
	}
	if tree.IsZero() {
		t.Error("tree ref missing for mutating tool")
	}
}

func TestExecutor_Exec_NoDiff_OnlyTrace(t *testing.T) {
	ex, sess, wt := newExecutorFixture(t)
	// Seed a baseline tree by committing once.
	if err := os.WriteFile(filepath.Join(wt, "seed"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedTree, err := sess.BuildTreeFromDir(wt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.CommitToTree(seedTree, stadogit.CommitMeta{Tool: "seed"}); err != nil {
		t.Fatal(err)
	}
	treeHeadBefore, _ := sess.TreeHead()

	ex.Registry.Register(stubTool{
		name:  "stubbash",
		class: tool.ClassExec,
		effect: func(string) (tool.Result, error) {
			return tool.Result{Content: "no-op"}, nil // doesn't touch the worktree
		},
	})

	_, err = ex.Run(context.Background(), "stubbash", json.RawMessage(`{"command":"true"}`), stubHost{workdir: wt})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	tree, _ := sess.TreeHead()
	if tree != treeHeadBefore {
		t.Errorf("tree ref should be unchanged for no-op exec; before=%s after=%s", treeHeadBefore, tree)
	}
	trace, _ := sess.TraceHead()
	if trace.IsZero() {
		t.Error("trace ref missing")
	}
}

func TestExecutor_Exec_WithDiff_Commits(t *testing.T) {
	ex, sess, wt := newExecutorFixture(t)
	if err := os.WriteFile(filepath.Join(wt, "seed"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	seed, _ := sess.BuildTreeFromDir(wt)
	sess.CommitToTree(seed, stadogit.CommitMeta{Tool: "seed"})
	before, _ := sess.TreeHead()

	ex.Registry.Register(stubTool{
		name:  "stubmake",
		class: tool.ClassExec,
		effect: func(workdir string) (tool.Result, error) {
			return tool.Result{Content: "built"}, os.WriteFile(filepath.Join(workdir, "artifact"), []byte("bin"), 0o644)
		},
	})

	_, err := ex.Run(context.Background(), "stubmake", json.RawMessage(`{"command":"make"}`), stubHost{workdir: wt})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	after, _ := sess.TreeHead()
	if after == before {
		t.Error("tree ref should advance on exec-with-diff")
	}
}

func TestExecutor_ErrorPathStillWritesTrace(t *testing.T) {
	ex, sess, wt := newExecutorFixture(t)
	ex.Registry.Register(stubTool{
		name:  "stubfail",
		class: tool.ClassExec,
		effect: func(string) (tool.Result, error) {
			return tool.Result{}, errors.New("boom")
		},
	})

	_, err := ex.Run(context.Background(), "stubfail", json.RawMessage(`{}`), stubHost{workdir: wt})
	if err == nil {
		t.Error("expected propagated error")
	}
	trace, _ := sess.TraceHead()
	if trace.IsZero() {
		t.Error("trace ref missing on error path")
	}

	// The trailer should include Error: boom.
	c, _ := object.GetCommit(sess.Sidecar.Repo().Storer, trace)
	if !strings.Contains(c.Message, "Error: boom") {
		t.Errorf("trace commit missing Error trailer: %q", c.Message)
	}
}

func TestExecutor_UnknownToolReturnsError(t *testing.T) {
	ex, _, _ := newExecutorFixture(t)
	_, err := ex.Run(context.Background(), "nope", nil, stubHost{})
	if err == nil {
		t.Error("expected error for unknown tool")
	}
}
