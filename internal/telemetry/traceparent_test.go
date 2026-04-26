package telemetry

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// newTestTracerProvider returns an in-memory tracer provider backed
// by the caller's recorder — isolated from the global provider so
// tests run in parallel without stepping on each other.
func newTestTracerProvider(sr *tracetest.SpanRecorder) *sdktrace.TracerProvider {
	return sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
}

// TestFormatTraceparent_Shape asserts the W3C wire form we emit
// matches the `00-<32 hex>-<16 hex>-<2 hex>` layout verifiers expect.
// A malformed traceparent would poison every downstream collector.
func TestFormatTraceparent_Shape(t *testing.T) {
	// Construct a valid SpanContext by hand — no SDK needed.
	tid, _ := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	sid, _ := trace.SpanIDFromHex("1112131415161718")
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
	})

	got := FormatTraceparent(sc)
	want := "00-0102030405060708090a0b0c0d0e0f10-1112131415161718-01"
	if got != want {
		t.Errorf("FormatTraceparent = %q, want %q", got, want)
	}

	// Sampled=false flips the last byte.
	scUnsampled := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: tid, SpanID: sid,
	})
	if got := FormatTraceparent(scUnsampled); !strings.HasSuffix(got, "-00") {
		t.Errorf("unsampled traceparent should end -00, got %q", got)
	}

	// Invalid context returns "" — callers key off empty-string to
	// skip writing the file.
	if got := FormatTraceparent(trace.SpanContext{}); got != "" {
		t.Errorf("invalid span context produced non-empty traceparent: %q", got)
	}
}

// TestWriteCurrentTraceparent_NoSpan_IsNoOp asserts that calling
// WriteCurrentTraceparent with a bare Background() context silently
// skips the write — telemetry-disabled runs shouldn't leave broken
// placeholder files in the child worktree.
func TestWriteCurrentTraceparent_NoSpan_IsNoOp(t *testing.T) {
	dir := t.TempDir()
	if err := WriteCurrentTraceparent(context.Background(), dir); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, TraceparentFile)); !os.IsNotExist(err) {
		t.Errorf("file should not exist after no-op write, stat err = %v", err)
	}
}

// TestRoundTrip_WriteThenLoad exercises the full round-trip: create
// a recording span, write its traceparent, read it back in a fresh
// context, and assert the child-context's span reference matches the
// original trace/span IDs.
func TestRoundTrip_WriteThenLoad(t *testing.T) {
	dir := t.TempDir()

	// Build a context with a valid recording span via the SDK's
	// in-memory tracer — gives us real trace/span IDs without needing
	// an exporter.
	sr := tracetest.NewSpanRecorder()
	tp := newTestTracerProvider(sr)
	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "fork")
	defer span.End()

	if err := WriteCurrentTraceparent(ctx, dir); err != nil {
		t.Fatalf("WriteCurrentTraceparent: %v", err)
	}

	// Read the raw file to verify format.
	raw, err := os.ReadFile(filepath.Join(dir, TraceparentFile))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	tpLine := strings.TrimSpace(string(raw))
	if !regexp.MustCompile(`^00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}$`).MatchString(tpLine) {
		t.Errorf("on-disk traceparent malformed: %q", tpLine)
	}

	// Load into a fresh context; assert the span reference matches
	// the original's trace/span IDs.
	child, ok := LoadParentTraceparent(context.Background(), dir)
	if !ok {
		t.Fatal("LoadParentTraceparent returned false for an existing file")
	}
	childSC := trace.SpanContextFromContext(child)
	origSC := span.SpanContext()
	if childSC.TraceID() != origSC.TraceID() {
		t.Errorf("trace id mismatch: %v vs %v", childSC.TraceID(), origSC.TraceID())
	}
	if childSC.SpanID() != origSC.SpanID() {
		t.Errorf("span id mismatch: %v vs %v", childSC.SpanID(), origSC.SpanID())
	}
	if childSC.IsSampled() != origSC.IsSampled() {
		t.Errorf("sampling flag mismatch: %v vs %v", childSC.IsSampled(), origSC.IsSampled())
	}
}

func TestWriteCurrentTraceparentRejectsSymlinkEscape(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "traceparent")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, TraceparentFile)); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	sr := tracetest.NewSpanRecorder()
	tp := newTestTracerProvider(sr)
	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "fork")
	defer span.End()

	if err := WriteCurrentTraceparent(ctx, dir); err == nil {
		t.Fatal("WriteCurrentTraceparent succeeded through a symlink escape")
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "outside\n" {
		t.Fatalf("outside traceparent was modified: %q", got)
	}
}

func TestWriteCurrentTraceparentRejectsInRootSymlink(t *testing.T) {
	dir := t.TempDir()
	decoy := filepath.Join(dir, "decoy-traceparent")
	if err := os.WriteFile(decoy, []byte("do not replace\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("decoy-traceparent", filepath.Join(dir, TraceparentFile)); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	sr := tracetest.NewSpanRecorder()
	tp := newTestTracerProvider(sr)
	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "fork")
	defer span.End()

	if err := WriteCurrentTraceparent(ctx, dir); err == nil {
		t.Fatal("WriteCurrentTraceparent succeeded through an in-root symlink")
	}
	got, err := os.ReadFile(decoy)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "do not replace\n" {
		t.Fatalf("in-root traceparent target was modified: %q", got)
	}
}

// TestLoadParentTraceparent_Missing_IsNoOp: the common no-fork case.
// No file → return the base context unchanged, signal false.
func TestLoadParentTraceparent_Missing_IsNoOp(t *testing.T) {
	ctx, ok := LoadParentTraceparent(context.Background(), t.TempDir())
	if ok {
		t.Error("missing file should return (ctx, false)")
	}
	if trace.SpanContextFromContext(ctx).IsValid() {
		t.Error("returned context should have no span reference")
	}
}

// TestLoadParentTraceparent_Malformed_IsNoOp: a garbage file shouldn't
// crash boot. Observability is decorative; malformed is equivalent
// to missing.
func TestLoadParentTraceparent_Malformed_IsNoOp(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, TraceparentFile), []byte("not a traceparent"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, ok := LoadParentTraceparent(context.Background(), dir)
	if ok {
		t.Error("malformed file should return (ctx, false)")
	}
}

// TestLoadParentTraceparent_EmptyDir_IsNoOp: guard against the ""-cwd
// edge case (should never happen, but we should fail soft rather than
// crash the caller).
func TestLoadParentTraceparent_EmptyDir_IsNoOp(t *testing.T) {
	_, ok := LoadParentTraceparent(context.Background(), "")
	if ok {
		t.Error("empty dir should return (ctx, false)")
	}
}

func TestLoadParentTraceparent_SymlinkIgnored(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real-traceparent")
	if err := os.WriteFile(target, []byte("00-0102030405060708090a0b0c0d0e0f10-1112131415161718-01\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, TraceparentFile)); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadParentTraceparent(context.Background(), dir); ok {
		t.Fatal("symlinked traceparent file should be ignored")
	}
}

func TestLoadParentTraceparent_OversizedIgnored(t *testing.T) {
	dir := t.TempDir()
	oversized := strings.Repeat("x", maxTraceparentBytes+1)
	if err := os.WriteFile(filepath.Join(dir, TraceparentFile), []byte(oversized), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := LoadParentTraceparent(context.Background(), dir); ok {
		t.Fatal("oversized traceparent file should be ignored")
	}
}
