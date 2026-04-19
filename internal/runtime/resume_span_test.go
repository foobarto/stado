package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	otel "go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/foobarto/stado/internal/config"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/telemetry"
)

// TestResume_EmitsSpanLinkedToFork: when OpenSession resumes a
// session whose worktree contains a `.stado-span-context` file,
// it opens a `stado.session.resume` span under that context.
// The span's trace ID must match whatever traceparent the file
// carries — that's what makes Jaeger render fork → resume → turns
// as one tree.
func TestResume_EmitsSpanLinkedToFork(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	repo := filepath.Join(root, "repo")
	_ = os.MkdirAll(filepath.Join(repo, ".git"), 0o755)

	// Install an SDK tracer so spans get real IDs.
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	oldProv := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(oldProv) })

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}

	// Create a session that will be the resume target.
	sess, err := OpenSession(cfg, repo)
	if err != nil {
		t.Fatal(err)
	}
	// Give it a tree ref so the resume path qualifies.
	emptyTree, _ := sess.BuildTreeFromDir(sess.WorktreePath)
	if _, err := sess.CommitToTree(emptyTree, stadogit.CommitMeta{Tool: "write", Summary: "seed"}); err != nil {
		t.Fatal(err)
	}

	// Write a synthetic parent traceparent into the worktree — this
	// simulates what `stado session fork` would have written.
	ctx, forkSpan := otel.Tracer("test-fork").Start(context.Background(), "synthetic-fork")
	if err := telemetry.WriteCurrentTraceparent(ctx, sess.WorktreePath); err != nil {
		t.Fatalf("write parent traceparent: %v", err)
	}
	forkSpan.End()
	forkTraceID := forkSpan.SpanContext().TraceID()

	// Resume: re-open with cwd = worktree path. Produces a
	// stado.session.resume span — find it in the recorder.
	if _, err := OpenSession(cfg, sess.WorktreePath); err != nil {
		t.Fatalf("resume: %v", err)
	}

	var resumeSpan sdktrace.ReadOnlySpan
	for _, s := range sr.Ended() {
		if s.Name() == telemetry.SpanSessionResume {
			resumeSpan = s
			break
		}
	}
	if resumeSpan == nil {
		t.Fatal("stado.session.resume span not emitted")
	}
	if resumeSpan.SpanContext().TraceID() != forkTraceID {
		t.Errorf("resume span trace id = %v, want %v (fork)", resumeSpan.SpanContext().TraceID(), forkTraceID)
	}
}

// TestResume_NoSpan_WhenNoTraceparent: the no-ancestry case — a
// normal resume without any fork history upstream shouldn't emit a
// span (nothing to link to).
func TestResume_NoSpan_WhenNoTraceparent(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	repo := filepath.Join(root, "repo")
	_ = os.MkdirAll(filepath.Join(repo, ".git"), 0o755)

	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	oldProv := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(oldProv) })

	cfg, _ := config.Load()
	sess, err := OpenSession(cfg, repo)
	if err != nil {
		t.Fatal(err)
	}
	emptyTree, _ := sess.BuildTreeFromDir(sess.WorktreePath)
	if _, err := sess.CommitToTree(emptyTree, stadogit.CommitMeta{Tool: "write", Summary: "seed"}); err != nil {
		t.Fatal(err)
	}
	// Ensure no traceparent file exists.
	_ = os.Remove(filepath.Join(sess.WorktreePath, telemetry.TraceparentFile))

	// Resume.
	if _, err := OpenSession(cfg, sess.WorktreePath); err != nil {
		t.Fatal(err)
	}

	for _, s := range sr.Ended() {
		if s.Name() == telemetry.SpanSessionResume {
			t.Error("resume span should not fire when no traceparent file is present")
		}
	}
}
