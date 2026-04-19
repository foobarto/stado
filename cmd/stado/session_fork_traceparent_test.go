package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	otel "go.opentelemetry.io/otel"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/telemetry"
)

// TestForkWritesTraceparent_ChildLoadsIt exercises the Phase 9.4/9.5
// cross-process span link end-to-end:
//   1. Install an SDK tracer so the fork span gets a real trace/span id.
//   2. Run createSessionAt inside a parent span.
//   3. Assert the child worktree contains a .stado-span-context whose
//      traceparent names the fork span's IDs.
//   4. runtime.RootContext(childWorktree) → a context whose attached
//      span reference matches the fork span's trace id.
func TestForkWritesTraceparent_ChildLoadsIt(t *testing.T) {
	// Isolate XDG paths.
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	cwd := filepath.Join(root, "work")
	_ = os.MkdirAll(cwd, 0o755)
	restore := chdir(t, cwd)
	defer restore()

	// Swap in an SDK tracer for the duration of this test. The fork
	// code uses otel.Tracer(...), which routes through the global
	// provider; restore the old one on cleanup.
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	old := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(old) })

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	_ = os.MkdirAll(cfg.WorktreeDir(), 0o755)

	sc, err := openSidecar(cfg)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), "p-traceparent", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	writeWorkFile(t, parent.WorktreePath, "seed.txt", "x")
	_ = commitAndTag(t, parent, 1)

	// Perform the fork. createSessionAt opens + ends its own
	// `stado.session.fork` span; the span's context is what lands in
	// `.stado-span-context` under the child's worktree.
	child, err := createSessionAt(cfg, parent.ID, plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}

	tpFile := filepath.Join(child.WorktreePath, telemetry.TraceparentFile)
	raw, err := os.ReadFile(tpFile)
	if err != nil {
		t.Fatalf("expected %s to exist after fork: %v", telemetry.TraceparentFile, err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(raw)), "00-") {
		t.Errorf("file contents not a traceparent: %q", raw)
	}

	// Find the recorded fork span and compare trace IDs.
	spans := sr.Ended()
	var forkSpan sdktrace.ReadOnlySpan
	for _, s := range spans {
		if s.Name() == telemetry.SpanSessionFork {
			forkSpan = s
			break
		}
	}
	if forkSpan == nil {
		t.Fatal("fork span not recorded")
	}
	forkSC := forkSpan.SpanContext()

	// Child-side: RootContext reads the file and returns a context
	// whose span reference matches the fork span's trace/span IDs.
	ctx := runtime.RootContext(child.WorktreePath)
	childSC := trace.SpanContextFromContext(ctx)
	if !childSC.IsValid() {
		t.Fatal("RootContext returned context without a span reference")
	}
	if childSC.TraceID() != forkSC.TraceID() {
		t.Errorf("trace id mismatch: child %v vs fork %v", childSC.TraceID(), forkSC.TraceID())
	}
	if childSC.SpanID() != forkSC.SpanID() {
		t.Errorf("span id mismatch: child %v vs fork %v", childSC.SpanID(), forkSC.SpanID())
	}
}

// TestRootContext_NoFile_ReturnsBackground: the non-forked common
// case. No .stado-span-context in cwd → RootContext behaves like
// context.Background() (no span reference attached).
func TestRootContext_NoFile_ReturnsBackground(t *testing.T) {
	tmp := t.TempDir()
	ctx := runtime.RootContext(tmp)
	if ctx == nil {
		t.Fatal("RootContext returned nil")
	}
	if trace.SpanContextFromContext(ctx).IsValid() {
		t.Error("non-forked cwd should have no span reference")
	}
	// Ensure the returned context is usable as a parent for a new
	// cancel — catches the edge case where a helper returns a
	// context with a done-channel pre-closed.
	_, cancel := context.WithCancel(ctx)
	cancel()
}
