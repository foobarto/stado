package telemetry

// Cross-process span linking for session forks (PLAN §9.4/9.5).
//
// When `stado session fork` runs, the fork event creates a
// `stado.session.fork` span under the parent process's trace. The
// child session's worktree is then typically opened in a fresh stado
// process (TUI, `stado run`, ACP, headless), and that second process
// would otherwise start a brand-new trace — Jaeger would render two
// disconnected trees where there should be one.
//
// We bridge the two by writing the parent's W3C traceparent to a
// file in the child's worktree. When the child process boots, it
// reads the file and wraps its root context with the parent span
// reference, so every span the child creates lands in the same trace
// tree as the fork.
//
// File format: W3C traceparent ASCII line, e.g.
//
//     00-<32-hex-trace-id>-<16-hex-span-id>-01
//
// One line, terminated with LF. Matches what `trace.SpanContext`'s
// standard TextMapPropagator emits, so any OTel-aware tool can read
// the same file.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/foobarto/stado/internal/workdirpath"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// TraceparentFile is the filename stado uses within a worktree to
// persist the parent span context across processes. Sits next to
// `.stado-pid`, written during fork, read during OpenSession.
const TraceparentFile = ".stado-span-context"

// propagator is the standard W3C TraceContext propagator. Shared so
// write + read agree on wire format.
var propagator = propagation.TraceContext{}

const maxTraceparentBytes = 256

// WriteCurrentTraceparent persists the span context currently on ctx
// to `<dir>/<TraceparentFile>`. Silent no-op when ctx carries no
// recording span (fresh Background() context, telemetry disabled, or
// the span already ended without a remote export — none of those
// produce a meaningful parent for a child trace).
//
// Best-effort IO: worktree-level filesystems sometimes lack write
// permission (e.g. read-only mounts). Errors are returned so callers
// can log + continue rather than treating this as fatal — a missing
// span link degrades observability but doesn't break functionality.
func WriteCurrentTraceparent(ctx context.Context, dir string) error {
	if dir == "" {
		return errors.New("traceparent: dir required")
	}
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		// Nothing to persist — telemetry disabled or span has no
		// real identity. Writing a malformed placeholder would later
		// poison the child's trace, so bail.
		return nil
	}

	carrier := propagation.MapCarrier{}
	propagator.Inject(ctx, carrier)
	tp := carrier["traceparent"]
	if tp == "" {
		return errors.New("traceparent: propagator returned empty traceparent")
	}

	root, err := workdirpath.OpenRootUnderUserConfig(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return writeTraceparentFile(root, TraceparentFile, []byte(tp+"\n"), 0o600)
}

func writeTraceparentFile(root *os.Root, name string, data []byte, perm os.FileMode) error {
	if info, err := root.Lstat(name); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("traceparent file is a symlink: %s", name)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("traceparent file is not regular: %s", name)
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	tmp := name + "." + uuid.NewString() + ".tmp"
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	keepTmp := false
	defer func() {
		if !keepTmp {
			_ = root.Remove(tmp)
		}
	}()
	n, err := f.Write(data)
	if err != nil {
		_ = f.Close()
		return err
	}
	if n != len(data) {
		_ = f.Close()
		return io.ErrShortWrite
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := root.Rename(tmp, name); err != nil {
		return err
	}
	keepTmp = true
	return nil
}

// LoadParentTraceparent looks for `<dir>/<TraceparentFile>` and, if
// present, returns ctx wrapped with the recovered parent span
// reference. Returns (ctx, false) when the file is missing (no
// linking attempted) so callers don't need to special-case the
// normal standalone-stado boot path.
//
// A malformed traceparent file is treated like a missing one — we
// log via slog once if requested by the caller, but don't fail the
// boot. Observability is decorative.
func LoadParentTraceparent(ctx context.Context, dir string) (context.Context, bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	if dir == "" {
		return ctx, false
	}
	root, err := workdirpath.OpenRootUnderUserConfig(dir)
	if err != nil {
		return ctx, false
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(TraceparentFile)
	if err != nil {
		return ctx, false
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return ctx, false
	}
	if info.Size() <= 0 || info.Size() > maxTraceparentBytes {
		return ctx, false
	}
	f, err := root.Open(TraceparentFile)
	if err != nil {
		return ctx, false
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxTraceparentBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxTraceparentBytes {
		return ctx, false
	}
	tp := strings.TrimSpace(string(data))
	if tp == "" {
		return ctx, false
	}
	carrier := propagation.MapCarrier{"traceparent": tp}
	out := propagator.Extract(ctx, carrier)
	if sc := trace.SpanContextFromContext(out); !sc.IsValid() {
		return ctx, false
	}
	return out, true
}

// FormatTraceparent renders a SpanContext as the W3C traceparent
// wire form. Exposed for tests and tooling that want to verify the
// on-disk shape without constructing a Propagator by hand.
func FormatTraceparent(sc trace.SpanContext) string {
	if !sc.IsValid() {
		return ""
	}
	tid := sc.TraceID().String()
	sid := sc.SpanID().String()
	flags := "00"
	if sc.IsSampled() {
		flags = "01"
	}
	return fmt.Sprintf("00-%s-%s-%s", tid, sid, flags)
}
