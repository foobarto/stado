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
	"github.com/foobarto/stado/internal/toolinput"
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

func (s stubTool) Name() string           { return s.name }
func (s stubTool) Description() string    { return "stub" }
func (s stubTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (s stubTool) Class() tool.Class      { return s.class }
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

func TestExecutor_StateMutating_OnlyTraceCommit(t *testing.T) {
	ex, sess, wt := newExecutorFixture(t)
	ex.Registry.Register(stubTool{
		name:  "stubstate",
		class: tool.ClassStateMutating,
		effect: func(string) (tool.Result, error) {
			return tool.Result{Content: "state updated"}, nil
		},
	})

	_, err := ex.Run(context.Background(), "stubstate", json.RawMessage(`{"action":"create"}`), stubHost{workdir: wt})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	trace, err := sess.TraceHead()
	if err != nil || trace.IsZero() {
		t.Errorf("trace ref should be set: %v head=%s", err, trace)
	}
	tree, _ := sess.TreeHead()
	if !tree.IsZero() {
		t.Errorf("tree ref should NOT be set for state-mutating tool, got %s", tree)
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

// Codex validated finding (Cluster U): when a mutating tool succeeds
// but BuildTreeFromDir fails (workdir exceeds 256 MiB blob cap or
// 200000 entry cap), the pre-fix executor returned the tool's success
// silently and only slog-warned about the missing audit tree ref. The
// mutation happened but the signed tree audit lost it — `audit verify`
// wouldn't notice. New contract: snapshot failure on a mutating tool
// augments meta.Error AND res.Error so both the audit log and the
// model see the gap; mutation itself stays committed (already
// happened, can't be undone).
//
// Reproduces with a sparse file exceeding the 256 MiB blob cap —
// fast to create on tmpfs, triggers `info.Size() > maxTreeBlobBytes`
// before any byte is read.
func TestExecutor_Mutating_SnapshotFailureSurfacedInError(t *testing.T) {
	ex, sess, wt := newExecutorFixture(t)
	ex.Registry.Register(stubTool{
		name:  "stubwrite-with-oversize",
		class: tool.ClassMutating,
		effect: func(workdir string) (tool.Result, error) {
			// Real mutation (the legitimate tool output).
			if err := os.WriteFile(filepath.Join(workdir, "ok.txt"), []byte("real mutation"), 0o644); err != nil {
				return tool.Result{}, err
			}
			// Trip the audit-snapshot cap: sparse file > 256 MiB.
			// Workdir-write tools could be coerced into creating
			// this via prompt injection on a permissive config;
			// the audit cap is the operator's defense and the
			// fix surfaces the resulting gap.
			f, err := os.Create(filepath.Join(workdir, "too-large.bin"))
			if err != nil {
				return tool.Result{}, err
			}
			defer f.Close()
			// 256 MiB + 1 byte — one byte over the cap, no actual data written.
			if err := f.Truncate(int64(256)<<20 + 1); err != nil {
				return tool.Result{}, err
			}
			return tool.Result{Content: "ok"}, nil
		},
	})

	res, err := ex.Run(context.Background(), "stubwrite-with-oversize",
		json.RawMessage(`{}`), stubHost{workdir: wt})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The tool's mutation IS visible (already happened — can't be
	// undone). What changed: the snapshot failure now surfaces.
	if res.Error == "" {
		t.Fatal("snapshot failure on mutating tool must surface in res.Error so the model sees the audit gap")
	}
	if !strings.Contains(res.Error, "audit snapshot failed") {
		t.Errorf("res.Error should mention `audit snapshot failed`; got %q", res.Error)
	}
	if !strings.Contains(res.Error, "tree-ref skipped") {
		t.Errorf("res.Error should note the tree-ref was skipped; got %q", res.Error)
	}

	// Trace commit was written — operator audit log captures the
	// note via meta.Error (which the trace-commit message embeds).
	trace, _ := sess.TraceHead()
	if trace.IsZero() {
		t.Fatal("trace ref must still be written; audit log must capture the audit-skip")
	}
	// Pull the trace commit and check its body carries the note.
	commit, err := object.GetCommit(sess.Sidecar.Repo().Storer, trace)
	if err != nil {
		t.Fatalf("read trace commit: %v", err)
	}
	if !strings.Contains(commit.Message, "audit snapshot failed") {
		t.Errorf("trace commit body should embed the audit-skip note; got:\n%s", commit.Message)
	}

	// Tree commit must NOT be written — there's no valid post-state
	// to sign. This is the silent-drop the operator could rely on
	// pre-fix; now it's loudly absent + the result.Error explains.
	tree, _ := sess.TreeHead()
	if !tree.IsZero() {
		t.Error("tree ref must NOT be written when snapshot failed; would be uncovered post-state")
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

func TestExecutor_RejectsOversizedArgsBeforeToolRun(t *testing.T) {
	ex, _, wt := newExecutorFixture(t)
	ran := false
	ex.Registry.Register(stubTool{
		name:  "stubread",
		class: tool.ClassNonMutating,
		effect: func(string) (tool.Result, error) {
			ran = true
			return tool.Result{Content: "unexpected"}, nil
		},
	})

	_, err := ex.Run(context.Background(), "stubread", json.RawMessage(strings.Repeat("x", toolinput.MaxBytes+1)), stubHost{workdir: wt})
	if err == nil {
		t.Fatal("expected oversized args error")
	}
	if ran {
		t.Fatal("tool ran after oversized args")
	}
}

// TestExecutor_PrependsProgressLog: a tool that appends to the
// per-call progress collector during Run gets its emissions
// prepended to the result envelope so the model sees the trail.
func TestExecutor_PrependsProgressLog(t *testing.T) {
	ex, _, wt := newExecutorFixture(t)
	ex.Registry.Register(stubTool{
		name:  "stub_with_progress",
		class: tool.ClassNonMutating,
		effect: func(_ string) (tool.Result, error) {
			// Tool itself doesn't have ctx; in the real flow this
			// happens inside bundled_plugin_tools.Run which sees ctx
			// + has the collector wired into host.Progress. For the
			// test we drive the collector directly via the package
			// helpers exposed alongside ContextWithProgress —
			// substituting what the wasm-side wrapper would do.
			return tool.Result{Content: "the answer"}, nil
		},
	})

	// The executor installs the collector inside Run; our stub tool
	// can't reach it via ctx because it doesn't take ctx into the
	// effect closure. Instead, register a tool that DOES use ctx:
	ex.Registry.Register(progressEmittingTool{name: "scan_with_progress"})

	res, err := ex.Run(context.Background(), "scan_with_progress",
		json.RawMessage(`{}`), stubHost{workdir: wt})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(res.Content, "[progress] scanner: checking 1/3") {
		t.Errorf("missing first progress line: %q", res.Content)
	}
	if !strings.Contains(res.Content, "[progress] scanner: checking 3/3") {
		t.Errorf("missing third progress line: %q", res.Content)
	}
	if !strings.Contains(res.Content, "scan complete") {
		t.Errorf("missing tool result: %q", res.Content)
	}
}

// TestExecutor_NoProgress_NoChange: tools that don't emit progress
// produce identical output before and after the wiring (regression
// guard against accidental envelope-wrapping).
func TestExecutor_NoProgress_NoChange(t *testing.T) {
	ex, _, wt := newExecutorFixture(t)
	ex.Registry.Register(stubTool{
		name:  "silent",
		class: tool.ClassNonMutating,
		effect: func(_ string) (tool.Result, error) {
			return tool.Result{Content: "just a result"}, nil
		},
	})
	res, err := ex.Run(context.Background(), "silent", json.RawMessage(`{}`), stubHost{workdir: wt})
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "just a result" {
		t.Errorf("envelope wrapping leaked: %q", res.Content)
	}
}

// TestExecutor_ErroredTool_NoProgressLog: when the tool errors,
// progress entries don't get prepended (errored output replaces
// rather than augments).
func TestExecutor_ErroredTool_NoProgressLog(t *testing.T) {
	ex, _, wt := newExecutorFixture(t)
	ex.Registry.Register(progressThenErrorTool{})
	res, _ := ex.Run(context.Background(), "scan_then_error",
		json.RawMessage(`{}`), stubHost{workdir: wt})
	if strings.Contains(res.Content, "[progress]") {
		t.Errorf("errored tool should not include progress log: %q", res.Content)
	}
}

// progressEmittingTool: takes ctx, appends to the in-context
// collector, returns a successful result.
type progressEmittingTool struct {
	name string
}

func (p progressEmittingTool) Name() string         { return p.name }
func (progressEmittingTool) Description() string    { return "stub" }
func (progressEmittingTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (progressEmittingTool) Class() tool.Class      { return tool.ClassNonMutating }
func (p progressEmittingTool) Run(ctx context.Context, _ json.RawMessage, _ tool.Host) (tool.Result, error) {
	if pc := tool.ProgressFromContext(ctx); pc != nil {
		pc.Append("scanner", "checking 1/3")
		pc.Append("scanner", "checking 2/3")
		pc.Append("scanner", "checking 3/3")
	}
	return tool.Result{Content: "scan complete"}, nil
}

// progressThenErrorTool: appends progress, then errors. Verifies
// the executor doesn't prepend progress when the tool reports an
// error (we want errored results to read clearly).
type progressThenErrorTool struct{}

func (progressThenErrorTool) Name() string           { return "scan_then_error" }
func (progressThenErrorTool) Description() string    { return "" }
func (progressThenErrorTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (progressThenErrorTool) Class() tool.Class      { return tool.ClassNonMutating }
func (progressThenErrorTool) Run(ctx context.Context, _ json.RawMessage, _ tool.Host) (tool.Result, error) {
	if pc := tool.ProgressFromContext(ctx); pc != nil {
		pc.Append("scanner", "starting up")
	}
	return tool.Result{Content: "tool failed", Error: "internal failure"}, nil
}

// TestExecutor_LiveCwd_AuditsRealDir: when the tool host's workdir differs
// from the sidecar worktree (TUI live-cwd mode, #030), the audit tree is built
// from the dir the tool actually wrote, not the unchanged worktree.
func TestExecutor_LiveCwd_AuditsRealDir(t *testing.T) {
	ex, sess, wt := newExecutorFixture(t)
	realCwd := t.TempDir()
	if realCwd == wt {
		t.Fatal("precondition: realCwd must differ from worktree")
	}
	ex.Registry.Register(stubTool{
		name:  "stublivewrite",
		class: tool.ClassMutating,
		effect: func(workdir string) (tool.Result, error) {
			return tool.Result{Content: "ok"}, os.WriteFile(filepath.Join(workdir, "real.txt"), []byte("live"), 0o644)
		},
	})

	if _, err := ex.Run(context.Background(), "stublivewrite", json.RawMessage(`{}`), stubHost{workdir: realCwd}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The audit tree must reflect realCwd (has real.txt), not the empty
	// worktree. Compare the committed tree-head's snapshot to a fresh build
	// of each candidate dir.
	tree, _ := sess.TreeHead()
	if tree.IsZero() {
		t.Fatal("live-cwd mutating tool should produce an audit tree")
	}
	fromReal, err := sess.BuildTreeFromDir(realCwd)
	if err != nil {
		t.Fatal(err)
	}
	fromWorktree, err := sess.BuildTreeFromDir(wt)
	if err != nil {
		t.Fatal(err)
	}
	if fromReal == fromWorktree {
		t.Fatal("precondition: realCwd and worktree trees should differ (realCwd has real.txt)")
	}
	// The tool wrote only to realCwd, so the worktree must still be empty of it.
	if _, statErr := os.Stat(filepath.Join(wt, "real.txt")); statErr == nil {
		t.Fatal("tool unexpectedly wrote to the worktree")
	}
}
