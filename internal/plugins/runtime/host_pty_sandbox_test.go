package runtime

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/plugins/runtime/pty"
	"github.com/foobarto/stado/internal/sandbox"
)

// nativeSandboxAvailable reports whether a real confinement runner exists here.
func nativeSandboxAvailable(t *testing.T) {
	t.Helper()
	if n := sandbox.Detect().Name(); n == "none" || n == "windows-passthrough" {
		t.Skipf("no native sandbox runner (%s)", n)
	}
}

// TestSandboxPTYSpawnOpts_WrapsWhenHostPolicySet (#100): on a confining surface
// (host.DefaultSandboxPolicy set) the PTY spawn command is rewritten to run
// under the sandbox runner, while the original command is preserved inside the
// wrapped argv.
func TestSandboxPTYSpawnOpts_WrapsWhenHostPolicySet(t *testing.T) {
	nativeSandboxAvailable(t)
	wd := t.TempDir()
	host := &Host{ExecPTY: true, Workdir: wd, DefaultSandboxPolicy: NewDefaultSandboxPolicy(wd)}

	out, err := sandboxPTYSpawnOpts(host, pty.SpawnOpts{Argv: []string{"/bin/echo", "hi"}})
	if err != nil {
		t.Fatalf("sandboxPTYSpawnOpts: %v", err)
	}
	if out.PreparedCmd == nil {
		t.Fatal("expected a sandbox-wrapped PreparedCmd")
	}
	// The wrapper runs the detected runner binary (bwrap on Linux, sandbox-exec
	// on macOS) — assert against the actual runner, not a hardcoded name (Codex).
	if runner := sandbox.Detect().Name(); !strings.Contains(out.PreparedCmd.Path, runner) {
		t.Errorf("PreparedCmd should run the %q runner; Path=%q", runner, out.PreparedCmd.Path)
	}
	joined := strings.Join(out.PreparedCmd.Args, " ")
	if !strings.Contains(joined, "/bin/echo") || !strings.Contains(joined, "hi") {
		t.Errorf("wrapped command dropped the original argv: %v", out.PreparedCmd.Args)
	}
	// Argv stays the human-readable original (List display, not the wrapper).
	if len(out.Argv) != 2 || out.Argv[0] != "/bin/echo" {
		t.Errorf("Argv should stay the original for display; got %v", out.Argv)
	}
}

// TestSandboxPTYSpawnOpts_NoWrapWithoutHostPolicy (#100): run/tui/resume leave
// DefaultSandboxPolicy nil (operator's own FS, by design) — the PTY spawn opts
// must pass through unchanged.
func TestSandboxPTYSpawnOpts_NoWrapWithoutHostPolicy(t *testing.T) {
	host := &Host{ExecPTY: true, Workdir: t.TempDir()} // no DefaultSandboxPolicy
	in := pty.SpawnOpts{Argv: []string{"/bin/echo", "hi"}}
	out, err := sandboxPTYSpawnOpts(host, in)
	if err != nil {
		t.Fatalf("sandboxPTYSpawnOpts: %v", err)
	}
	if len(out.Argv) != 2 || out.Argv[0] != "/bin/echo" || out.Argv[1] != "hi" {
		t.Fatalf("unsandboxed surface must pass argv through unchanged; got %v", out.Argv)
	}
}

// TestPTYSandbox_BwrapShellRuns (#100): end-to-end — a sandboxed PTY actually
// runs a shell under bwrap and produces output. Skips when bwrap can't run a
// trivial command here (e.g. user namespaces restricted in CI).
func TestPTYSandbox_BwrapShellRuns(t *testing.T) {
	if sandbox.Detect().Name() != "bwrap" {
		t.Skipf("bwrap-specific E2E; runner is %q", sandbox.Detect().Name())
	}
	// Probe: can bwrap actually run here?
	probe := exec.Command("bwrap", "--ro-bind", "/", "/", "--", "/bin/true")
	if err := probe.Run(); err != nil {
		t.Skipf("bwrap cannot run in this environment: %v", err)
	}

	wd := t.TempDir()
	host := &Host{ExecPTY: true, Workdir: wd, DefaultSandboxPolicy: NewDefaultSandboxPolicy(wd)}
	m := pty.NewManager()
	defer m.CloseAll()
	host.PTYManager = m

	opts, err := sandboxPTYSpawnOpts(host, pty.SpawnOpts{
		Cmd: "echo SANDBOXED-OK", Cols: 80, Rows: 24,
	})
	if err != nil {
		t.Fatalf("wrap: %v", err)
	}
	id, err := m.Spawn(opts)
	if err != nil {
		t.Skipf("sandboxed PTY spawn unavailable here: %v", err)
	}
	defer m.Destroy(id)

	deadline := time.Now().Add(3 * time.Second)
	var acc string
	for time.Now().Before(deadline) {
		data, rerr := m.Read(id, 4096, 200*time.Millisecond)
		acc += string(data)
		if strings.Contains(acc, "SANDBOXED-OK") {
			return // success: the shell ran inside the sandbox
		}
		if rerr != nil { // EOF
			break
		}
	}
	t.Fatalf("sandboxed PTY did not produce expected output; got %q", acc)
}
