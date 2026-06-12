package lspfind

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/foobarto/stado/internal/hooks"
)

// writeGoFile drops a .go source file under root so serverFor(".go") routes
// to (the faked) gopls and readLSPDocumentText finds a body to didOpen.
func writeGoFile(t *testing.T, root, name string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte("package x\n\nfunc F() {}\n"), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return path
}

// firePostTool runs the hook at the post_tool point the way the executor
// would, returning the aggregate result so a test can assert it stayed a
// Continue (observe-only).
func firePostTool(t *testing.T, h hooks.HookScript, tool, class, args, errStr string) hooks.HookResult {
	t.Helper()
	runner := hooks.NewLifecycleRunner(h)
	p := hooks.PostTool(0, tool, class, args, "ok", errStr)
	res, _ := runner.Fire(context.Background(), hooks.PointPostTool, p)
	return res
}

// TestDiagnosticsHook_CollectsOnMutatingEdit: a successful mutating edit on
// a .go file makes the hook pull diagnostics from the (faked) server and
// store them under the file's workdir-relative path.
func TestDiagnosticsHook_CollectsOnMutatingEdit(t *testing.T) {
	bin := buildFakeLSP(t)
	t.Setenv("FAKELSP_DIAG", "1")
	withFakeLaunch(t, bin)

	root := t.TempDir()
	writeGoFile(t, root, "a.go")

	m := NewLSPClientManager(context.Background())
	t.Cleanup(m.CloseAll)
	store := NewDiagnosticsStore()
	hook := NewDiagnosticsHook(m, store, root)

	res := firePostTool(t, hook, "edit", "mutating", `{"path":"a.go"}`, "")
	if res.Decision != hooks.DecisionContinue {
		t.Fatalf("post-edit hook must be observe-only (Continue), got decision %v", res.Decision)
	}

	sum := store.Summarize(0)
	if sum.Errors != 1 {
		t.Fatalf("expected 1 error diagnostic stored, got %d (warnings=%d, total=%d)", sum.Errors, sum.Warnings, sum.Total)
	}
	if len(sum.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(sum.Entries))
	}
	if got := sum.Entries[0].RelPath; got != "a.go" {
		t.Fatalf("diagnostic relpath = %q, want a.go", got)
	}
	if sum.Entries[0].Line != 1 {
		t.Fatalf("diagnostic line = %d, want 1 (1-indexed)", sum.Entries[0].Line)
	}
}

// TestDiagnosticsHook_SkipsNonMutating: a non-mutating tool (read) or a
// failed edit must NOT touch the store — diagnostics are only meaningful
// after a successful file mutation.
func TestDiagnosticsHook_SkipsNonMutating(t *testing.T) {
	bin := buildFakeLSP(t)
	t.Setenv("FAKELSP_DIAG", "1")
	withFakeLaunch(t, bin)

	root := t.TempDir()
	writeGoFile(t, root, "a.go")

	m := NewLSPClientManager(context.Background())
	t.Cleanup(m.CloseAll)
	store := NewDiagnosticsStore()
	hook := NewDiagnosticsHook(m, store, root)

	// Non-mutating read: skipped.
	firePostTool(t, hook, "read", "non-mutating", `{"path":"a.go"}`, "")
	if got := store.Summarize(0).Total; got != 0 {
		t.Fatalf("non-mutating tool produced %d diagnostics, want 0", got)
	}

	// Failed mutating edit: skipped (the file didn't change).
	firePostTool(t, hook, "edit", "mutating", `{"path":"a.go"}`, "edit failed: no match")
	if got := store.Summarize(0).Total; got != 0 {
		t.Fatalf("failed edit produced %d diagnostics, want 0", got)
	}
}

// TestDiagnosticsHook_NonLSPFileIsNoOp: editing a file with no configured
// language server (.txt) is a clean no-op — no error, no store mutation.
func TestDiagnosticsHook_NonLSPFileIsNoOp(t *testing.T) {
	bin := buildFakeLSP(t)
	t.Setenv("FAKELSP_DIAG", "1")
	withFakeLaunch(t, bin)

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hi\n"), 0o600); err != nil {
		t.Fatalf("write notes.txt: %v", err)
	}

	m := NewLSPClientManager(context.Background())
	t.Cleanup(m.CloseAll)
	store := NewDiagnosticsStore()
	hook := NewDiagnosticsHook(m, store, root)

	res := firePostTool(t, hook, "write", "mutating", `{"path":"notes.txt"}`, "")
	if res.Decision != hooks.DecisionContinue {
		t.Fatalf("non-LSP edit must Continue, got %v", res.Decision)
	}
	if got := store.Summarize(0).Total; got != 0 {
		t.Fatalf("non-LSP file produced %d diagnostics, want 0", got)
	}
}
