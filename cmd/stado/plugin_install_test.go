package main

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
)

// buildTestPlugin writes a minimal plugin dir (wasm + manifest + sig)
// signed by priv, returning (src-dir, sha256-of-wasm).
func buildTestPlugin(t *testing.T, priv ed25519.PrivateKey, pub ed25519.PublicKey, name, version string) string {
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
	return dir
}

// isolatedHome sets XDG paths to a temp dir for this test — required so
// the install writes to the test state dir, not the user's real one.
func isolatedHome(t *testing.T) *config.Config {
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

// TestPluginInstall_FailsWithoutTrust covers the safety default: no
// pinned signer + no --signer → install refuses.
func TestPluginInstall_FailsWithoutTrust(t *testing.T) {
	_ = isolatedHome(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	src := buildTestPlugin(t, priv, pub, "demo", "1.0.0")

	pluginInstallSigner = ""
	defer func() { pluginInstallSigner = "" }()

	err := pluginInstallCmd.RunE(pluginInstallCmd, []string{src})
	if err == nil {
		t.Fatal("expected install to fail without a trusted signer")
	}
	if !strings.Contains(err.Error(), "not pinned") {
		t.Errorf("expected 'not pinned' error, got %v", err)
	}
}

// TestPluginInstall_WithSignerTOFU installs after inline-pinning the
// signer via --signer.
func TestPluginInstall_WithSignerTOFU(t *testing.T) {
	cfg := isolatedHome(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	src := buildTestPlugin(t, priv, pub, "demo", "1.0.0")

	pluginInstallSigner = hex.EncodeToString(pub)
	defer func() { pluginInstallSigner = "" }()

	if err := pluginInstallCmd.RunE(pluginInstallCmd, []string{src}); err != nil {
		t.Fatalf("install: %v", err)
	}
	dst := filepath.Join(cfg.StateDir(), "plugins", "demo-1.0.0")
	if _, err := os.Stat(filepath.Join(dst, "plugin.wasm")); err != nil {
		t.Errorf("install did not copy wasm: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "plugin.manifest.json")); err != nil {
		t.Errorf("install did not copy manifest: %v", err)
	}
}

// TestPluginInstall_SignerMismatchRejected: provide a --signer whose
// fingerprint doesn't match the manifest's author_pubkey_fpr.
func TestPluginInstall_SignerMismatchRejected(t *testing.T) {
	_ = isolatedHome(t)
	pub1, priv1, _ := ed25519.GenerateKey(rand.Reader)
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	src := buildTestPlugin(t, priv1, pub1, "demo", "1.0.0")

	// Pin a different key as the signer.
	pluginInstallSigner = hex.EncodeToString(pub2)
	defer func() { pluginInstallSigner = "" }()

	err := pluginInstallCmd.RunE(pluginInstallCmd, []string{src})
	if err == nil {
		t.Fatal("expected mismatch error")
	}
	if !strings.Contains(err.Error(), "does not match manifest") {
		t.Errorf("error should call out manifest mismatch: %v", err)
	}
}

// TestPluginInstall_Idempotent: re-installing the same version is a
// no-op advisory, not an error.
func TestPluginInstall_Idempotent(t *testing.T) {
	_ = isolatedHome(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	src := buildTestPlugin(t, priv, pub, "demo", "1.0.0")
	pluginInstallSigner = hex.EncodeToString(pub)
	defer func() { pluginInstallSigner = "" }()

	if err := pluginInstallCmd.RunE(pluginInstallCmd, []string{src}); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if err := pluginInstallCmd.RunE(pluginInstallCmd, []string{src}); err != nil {
		t.Errorf("re-install should be no-op, got %v", err)
	}
}
