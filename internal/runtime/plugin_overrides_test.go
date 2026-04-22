package runtime

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/pkg/tool"
)

func isolatedRuntimeConfig(t *testing.T) *config.Config {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func installOverridePlugin(t *testing.T, cfg *config.Config, priv ed25519.PrivateKey, pub ed25519.PublicKey, pluginID string, def plugins.ToolDef) {
	t.Helper()
	dir := filepath.Join(cfg.StateDir(), "plugins", pluginID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	wasm := []byte("pretend-wasm-blob-" + pluginID)
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), wasm, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(wasm)
	m := &plugins.Manifest{
		Name:            "override",
		Version:         "1.0.0",
		Author:          "test-author",
		AuthorPubkeyFpr: plugins.Fingerprint(pub),
		WASMSHA256:      hex.EncodeToString(sum[:]),
		Tools:           []plugins.ToolDef{def},
		TimestampUTC:    time.Now().UTC().Format(time.RFC3339),
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
	ts := plugins.NewTrustStore(cfg.StateDir())
	if _, err := ts.Trust(hex.EncodeToString(pub), "test-author"); err != nil {
		t.Fatal(err)
	}
}

func TestApplyToolOverrides_ReplacesBundledTool(t *testing.T) {
	cfg := isolatedRuntimeConfig(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	installOverridePlugin(t, cfg, priv, pub, "corp-read-1.0.0", plugins.ToolDef{
		Name:        "read",
		Description: "policy-wrapped read",
		Class:       "Exec",
		Schema:      `{"type":"object","properties":{"path":{"type":"string"}}}`,
	})
	cfg.Tools.Overrides = map[string]string{"read": "corp-read-1.0.0"}

	reg := BuildDefaultRegistry()
	if err := ApplyToolOverrides(reg, cfg); err != nil {
		t.Fatalf("ApplyToolOverrides: %v", err)
	}
	got, ok := reg.Get("read")
	if !ok {
		t.Fatal("read tool missing after override")
	}
	if got.Description() != "policy-wrapped read" {
		t.Fatalf("description = %q", got.Description())
	}
	if reg.ClassOf("read") != tool.ClassExec {
		t.Fatalf("ClassOf(read) = %v, want %v", reg.ClassOf("read"), tool.ClassExec)
	}
}

func TestApplyToolOverrides_RejectsUnknownTarget(t *testing.T) {
	cfg := isolatedRuntimeConfig(t)
	cfg.Tools.Overrides = map[string]string{"nope": "corp-nope-1.0.0"}
	reg := BuildDefaultRegistry()
	if err := ApplyToolOverrides(reg, cfg); err == nil {
		t.Fatal("expected unknown target error")
	}
}

func TestApplyToolOverrides_RejectsSessionAwareOverrides(t *testing.T) {
	cfg := isolatedRuntimeConfig(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	installOverridePlugin(t, cfg, priv, pub, "corp-read-1.0.0", plugins.ToolDef{
		Name:        "read",
		Description: "session-aware read",
		Class:       "NonMutating",
		Schema:      `{"type":"object"}`,
	})
	cfg.Tools.Overrides = map[string]string{"read": "corp-read-1.0.0"}
	cfg.Plugins.RekorURL = ""
	cfg.Plugins.CRLURL = ""

	dir := filepath.Join(cfg.StateDir(), "plugins", "corp-read-1.0.0")
	mf, _, err := plugins.LoadFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	mf.Capabilities = []string{"session:read"}
	canonical, err := mf.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	sig, err := mf.Sign(priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.json"), canonical, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.sig"), []byte(sig), 0o644); err != nil {
		t.Fatal(err)
	}

	reg := BuildDefaultRegistry()
	err = ApplyToolOverrides(reg, cfg)
	if err == nil || !strings.Contains(err.Error(), "session/llm capabilities") {
		t.Fatalf("ApplyToolOverrides error = %v, want session/llm rejection", err)
	}
}
