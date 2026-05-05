package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/pkg/tool"
)

// TestPluginRunToolHost_Surface ensures the minimal tool.Host
// shape `--with-tool-host` plumbs into host.ToolHost satisfies the
// interface and returns the expected default behaviours.
func TestPluginRunToolHost_Surface(t *testing.T) {
	wd := t.TempDir()
	runner := sandbox.NoneRunner{}
	h := newPluginRunToolHost(wd, runner, false)

	if got := h.Workdir(); got != wd {
		t.Errorf("Workdir() = %q, want %q", got, wd)
	}
	rh, ok := h.(interface{ Runner() sandbox.Runner })
	if !ok {
		t.Fatalf("pluginRunToolHost should expose Runner() for bash duck-typing")
	}
	if got := rh.Runner().Name(); got != "none" {
		t.Errorf("Runner().Name() = %q, want %q", got, "none")
	}
	dec, err := h.Approve(context.Background(), tool.ApprovalRequest{
		Tool:    "any",
		Command: "anything",
	})
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if dec != tool.DecisionAllow {
		t.Errorf("Approve() = %v, want DecisionAllow", dec)
	}
	info, ok := h.PriorRead(tool.ReadKey{Path: "/tmp/x"})
	if ok {
		t.Errorf("PriorRead ok=true on fresh host, info=%+v", info)
	}
	// RecordRead must be a no-op (no panic).
	h.RecordRead(tool.ReadKey{Path: "/tmp/x"}, tool.PriorReadInfo{Turn: 1})
}

// TestPluginRun_WithToolHost_ExecBashGate_NoSandbox exercises the
// EP-0028 D1 guarantee in its v0.27.0 form: when no native sandbox
// is available (sandbox.Detect → NoneRunner), exec:bash plugins are
// refused with an install hint. EP-0005 forbids substituting the
// operator's CLI invocation for a real syscall filter, so the gate
// fires BEFORE the wasm runtime is constructed.
//
// On hosts WITH a native sandbox (bwrap on Linux dev hosts,
// sandbox-exec on macOS) the gate doesn't fire — verifying the
// happy path needs a real wasm plugin built, which is out of scope
// for a unit test. Skipped there.
func TestPluginRun_WithToolHost_ExecBashGate_NoSandbox(t *testing.T) {
	if sandbox.Detect().Name() != "none" {
		t.Skipf("skipping: native sandbox is available (%s); test exercises the no-sandbox refusal branch", sandbox.Detect().Name())
	}

	cfg := isolatedHome(t)
	// EP-0028 D1: refusal is opt-in via [sandbox] refuse_no_runner = true.
	// Write that into the isolated XDG_CONFIG_HOME so config.Load() inside
	// pluginRunCmd.RunE picks it up.
	cfgDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "stado")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(cfgDir, "config.toml"),
		[]byte("[sandbox]\nrefuse_no_runner = true\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	src := buildTestPluginWithCaps(t, priv, pub, "needs-bash", "0.1.0", []string{"exec:bash"})

	pluginInstallSigner = hex.EncodeToString(pub)
	defer func() { pluginInstallSigner = "" }()
	if err := pluginInstallCmd.RunE(pluginInstallCmd, []string{src}); err != nil {
		t.Fatalf("plugin install: %v", err)
	}
	defer func() {
		_ = os.RemoveAll(filepath.Join(cfg.StateDir(), "plugins", "needs-bash-0.1.0"))
	}()

	pluginRunWithToolHost = true
	defer func() { pluginRunWithToolHost = false }()

	err := pluginRunCmd.RunE(pluginRunCmd, []string{"needs-bash-0.1.0", "anything"})
	if err == nil {
		t.Fatal("expected --with-tool-host to refuse exec:bash on no-sandbox host with refuse_no_runner=true")
	}
	if !strings.Contains(err.Error(), "no native sandbox") || !strings.Contains(err.Error(), "bubblewrap") {
		t.Errorf("expected refusal to mention no-native-sandbox + bubblewrap install hint, got %v", err)
	}
}

// buildTestPluginWithCaps mirrors buildTestPlugin in plugin_install_test.go
// but allows declaring extra capabilities (and a stub tool that
// matches the capability so the manifest is internally consistent).
func buildTestPluginWithCaps(t *testing.T, priv ed25519.PrivateKey, pub ed25519.PublicKey, name, version string, caps []string) string {
	t.Helper()
	dir := t.TempDir()

	wasm := []byte("pretend-wasm-blob-" + name)
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), wasm, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(wasm)
	m := &plugins.Manifest{
		Name:            name,
		Version:         version,
		Author:          "test-author",
		AuthorPubkeyFpr: plugins.Fingerprint(pub),
		WASMSHA256:      hex.EncodeToString(sum[:]),
		Capabilities:    caps,
		Tools: []plugins.ToolDef{{
			Name:        "anything",
			Description: "test stub",
			Schema:      `{"type":"object"}`,
		}},
		TimestampUTC: time.Now().UTC().Format(time.RFC3339),
	}
	canonical, err := m.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	sig, err := m.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.json"), canonical, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.sig"), []byte(sig), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
