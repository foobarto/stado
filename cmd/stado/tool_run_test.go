package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

// TestToolRun_ResolvesByCanonicalForm: `tool run fs.read` finds the
// bundled fs.read tool by canonical-dotted form. Note: fs.read is
// registered under bare name "read" (legacy native), so canonical
// lookup goes through the metadata fallback.
func TestToolRun_ResolvesByCanonicalForm(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	tmp := t.TempDir()
	target := filepath.Join(tmp, "hello.txt")
	if err := os.WriteFile(target, []byte("hello world"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err = runToolByName(t.Context(),
		"fs.read",
		`{"path":"`+target+`"}`,
		toolRunOptions{
			Cfg:     cfg,
			Workdir: tmp,
			Stdout:  &stdout,
			Stderr:  &stderr,
		},
	)
	if err != nil {
		t.Fatalf("runToolByName: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "hello world") {
		t.Errorf("expected 'hello world' in stdout; got: %q", stdout.String())
	}
}

// TestToolRun_ResolvesByBareForm: bare native name "read" works too
// (legacy fs native registers with bare name).
func TestToolRun_ResolvesByBareForm(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	tmp := t.TempDir()
	target := filepath.Join(tmp, "hello.txt")
	if err := os.WriteFile(target, []byte("hi"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err = runToolByName(t.Context(),
		"read",
		`{"path":"`+target+`"}`,
		toolRunOptions{
			Cfg:     cfg,
			Workdir: tmp,
			Stdout:  &stdout,
			Stderr:  &stderr,
		},
	)
	if err != nil {
		t.Fatalf("runToolByName: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "hi") {
		t.Errorf("expected 'hi' in stdout; got: %q", stdout.String())
	}
}

// TestToolRun_ToolNotFound reports a clear error message that
// references stado tool list.
func TestToolRun_ToolNotFound(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err = runToolByName(t.Context(),
		"nope.foo",
		`{}`,
		toolRunOptions{Cfg: cfg, Stdout: &stdout, Stderr: &stderr},
	)
	if err == nil {
		t.Fatal("expected error for unknown tool; got nil")
	}
	if !strings.Contains(err.Error(), "stado tool list") {
		t.Errorf("error message should reference 'stado tool list'; got: %q", err.Error())
	}
}

// TestToolRun_DisabledRefuses: a tool listed in cfg.Tools.Disabled
// is refused unless --force is passed.
func TestToolRun_DisabledRefuses(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.Tools.Disabled = []string{"read"}

	var stdout, stderr bytes.Buffer
	err = runToolByName(t.Context(), "read", `{}`,
		toolRunOptions{Cfg: cfg, Stdout: &stdout, Stderr: &stderr})
	if err == nil {
		t.Fatal("expected error for disabled tool; got nil")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("error should mention 'disabled'; got: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should hint at --force; got: %q", err.Error())
	}
}

// TestToolRun_DisabledForceOverrides: --force runs the tool anyway.
func TestToolRun_DisabledForceOverrides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.Tools.Disabled = []string{"read"}

	tmp := t.TempDir()
	target := filepath.Join(tmp, "x.txt")
	if err := os.WriteFile(target, []byte("forced"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	var stdout, stderr bytes.Buffer
	err = runToolByName(t.Context(), "read",
		`{"path":"`+target+`"}`,
		toolRunOptions{Cfg: cfg, Workdir: tmp, Force: true, Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		t.Fatalf("with --force, expected success; got: %v\nstderr: %s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "forced") {
		t.Errorf("expected 'forced' in stdout; got: %q", stdout.String())
	}
}

// TestToolRun_DispatchesInstalledPlugin: an installed plugin's
// declared tool is invocable via stado tool run <toolname>. Verifies
// that the dispatcher gets past the bundled-only branch and reaches
// the installed-plugin path. Uses buildTestPluginWithCaps from
// plugin_test_helpers_test.go, which produces a stub plugin whose
// ToolDef.Name is "anything".
func TestToolRun_DispatchesInstalledPlugin(t *testing.T) {
	cfg := isolatedHome(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pluginInstallSigner = hex.EncodeToString(pub)
	defer func() { pluginInstallSigner = "" }()

	src := buildTestPluginWithCaps(t, priv, pub, "rundemo", "v0.1.0", []string{"fs:read:."})
	if err := pluginInstallCmd.RunE(pluginInstallCmd, []string{src}); err != nil {
		t.Fatalf("plugin install: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(filepath.Join(cfg.StateDir(), "plugins", "rundemo-v0.1.0"))
	}()

	tmp := t.TempDir()
	var stdout, stderr bytes.Buffer

	// The fixture plugin's declared tool name is "anything" (see
	// buildTestPluginWithCaps). The wasm payload is a stub blob, so
	// invocation is expected to fail at the wasm-runtime layer — but
	// the assertion here is "we got past the not-yet-supported
	// branch", i.e. dispatch reached the installed-plugin path.
	err := runToolByName(t.Context(), "anything", `{}`,
		toolRunOptions{Cfg: cfg, Workdir: tmp, Stdout: &stdout, Stderr: &stderr})
	if err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "not yet supported") || strings.Contains(errMsg, "not a bundled tool") {
			t.Fatalf("tool run still hits the not-yet-supported branch: %v", err)
		}
		// Otherwise: dispatch reached but invocation failed —
		// acceptable for this test (which is verifying the wiring,
		// not the test fixture's wasm correctness).
		t.Logf("tool run reached installed dispatch; invocation error: %v\nstderr: %s", err, stderr.String())
	}
}

// TestToolRun_DisabledByGlob: glob in [tools].disabled also refuses.
func TestToolRun_DisabledByGlob(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.Tools.Disabled = []string{"fs.*"}

	var stdout, stderr bytes.Buffer
	err = runToolByName(t.Context(), "fs.read", `{}`,
		toolRunOptions{Cfg: cfg, Stdout: &stdout, Stderr: &stderr})
	if err == nil {
		t.Fatal("expected error for tool matching disabled glob; got nil")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Errorf("error should mention 'disabled'; got: %q", err.Error())
	}
}
