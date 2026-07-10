package runtime

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

func captureRegistryDiagnostics(t *testing.T, fn func()) string {
	t.Helper()
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = write
	t.Cleanup(func() { os.Stderr = old })
	fn()
	if err := write.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(read)
	if err != nil {
		t.Fatal(err)
	}
	if err := read.Close(); err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestSubagentQuietDiagnosticsPropagateToChildRegistryBuild(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Disabled = []string{"not-a-real-tool\x1b]52;c;payload\a"}
	runner := SubagentRunner{Config: cfg, QuietRegistryDiagnostics: true}
	out := captureRegistryDiagnostics(t, func() {
		if _, err := runner.buildExecutor(nil, "test-subagent"); err != nil {
			t.Fatal(err)
		}
	})
	if out != "" {
		t.Fatalf("TUI subagent registry diagnostics leaked to stderr: %q", out)
	}
}

func TestRegistryDiagnosticsAreSanitizedAndSuppressible(t *testing.T) {
	out := captureRegistryDiagnostics(t, func() {
		emitRegistryDiagnostic("plugin %s failed\n", "bad\x1b]52;c;payload\a")
	})
	if strings.ContainsAny(out, "\x1b\a") || !strings.Contains(out, "plugin bad]52;c;payload failed") {
		t.Fatalf("diagnostic was not safely sanitized: %q", out)
	}

	out = captureRegistryDiagnostics(t, func() {
		withRegistryDiagnosticsSuppressed(func() {
			emitRegistryDiagnostic("must not escape\n")
		})
	})
	if out != "" {
		t.Fatalf("suppressed registry diagnostic leaked to stderr: %q", out)
	}
}
