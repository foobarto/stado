package runtime

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

func installOverridePlugin(t *testing.T, cfg *config.Config, priv ed25519.PrivateKey, pub ed25519.PublicKey, pluginID string, def plugins.ToolDef) plugins.InstalledPackage {
	t.Helper()
	if def.Capabilities == nil {
		def.Capabilities = plugins.CapabilitySubset()
	}
	wasm := []byte("pretend-wasm-blob-" + pluginID)
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
	source := filepath.Join(t.TempDir(), pluginID+"-source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	record, err := plugins.NewLocalInstallRecord(source, *m)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(cfg.StateDir(), "plugins", record.StoreKey)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), wasm, 0o644); err != nil {
		t.Fatal(err)
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
	if err := plugins.WriteInstallRecord(dir, record, *m); err != nil {
		t.Fatal(err)
	}
	if err := plugins.WriteInstallReceipt(cfg.StateDir(), filepath.Dir(dir), record); err != nil {
		t.Fatal(err)
	}
	ts := plugins.NewTrustStore(cfg.StateDir())
	if _, err := ts.TrustVerifiedPackage(hex.EncodeToString(pub), "test-author", record.Namespace, m, sig); err != nil {
		t.Fatal(err)
	}
	identity, err := plugins.RuntimeIdentityForInstalledDir(dir, *m)
	if err != nil {
		t.Fatal(err)
	}
	return plugins.InstalledPackage{Dir: dir, Record: record, Manifest: *m, Signature: sig, Identity: identity}
}

func TestApplyToolOverrides_ReplacesBundledTool(t *testing.T) {
	cfg := isolatedRuntimeConfig(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pkg := installOverridePlugin(t, cfg, priv, pub, "corp-read-1.0.0", plugins.ToolDef{
		Name:        "fs__read",
		Description: "policy-wrapped read",
		Class:       "Exec",
		Schema:      `{"type":"object","properties":{"path":{"type":"string"}}}`,
	})
	cfg.Tools.Overrides = map[string]string{"fs__read": pkg.Record.StoreKey}

	reg := BuildDefaultRegistry(nil)
	if err := ApplyToolOverrides(reg, cfg); err != nil {
		t.Fatalf("ApplyToolOverrides: %v", err)
	}
	got, ok := reg.Get("fs__read")
	if !ok {
		t.Fatal("read tool missing after override")
	}
	if got.Description() != "policy-wrapped read" {
		t.Fatalf("description = %q", got.Description())
	}
	if reg.ClassOf("fs__read") != tool.ClassExec {
		t.Fatalf("ClassOf(read) = %v, want %v", reg.ClassOf("fs__read"), tool.ClassExec)
	}
}

func TestApplyToolOverrides_RejectsUnknownTarget(t *testing.T) {
	cfg := isolatedRuntimeConfig(t)
	cfg.Tools.Overrides = map[string]string{"nope": "corp-nope-1.0.0"}
	reg := BuildDefaultRegistry(nil)
	if err := ApplyToolOverrides(reg, cfg); err == nil {
		t.Fatal("expected unknown target error")
	}
}

func TestApplyToolOverrides_RejectsEscapingPluginID(t *testing.T) {
	cfg := isolatedRuntimeConfig(t)
	cfg.Tools.Overrides = map[string]string{"fs__read": "../corp-read-1.0.0"}
	reg := BuildDefaultRegistry(nil)
	err := ApplyToolOverrides(reg, cfg)
	if err == nil {
		t.Fatal("escaping plugin selector unexpectedly resolved")
	}
}

func TestApplyToolOverrides_RejectsSessionAwareOverrides(t *testing.T) {
	cfg := isolatedRuntimeConfig(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pkg := installOverridePlugin(t, cfg, priv, pub, "corp-read-1.0.0", plugins.ToolDef{
		Name:        "fs__read",
		Description: "session-aware read",
		Class:       "NonMutating",
		Schema:      `{"type":"object"}`,
	})
	cfg.Tools.Overrides = map[string]string{"fs__read": pkg.Record.StoreKey}
	cfg.Plugins.RekorURL = ""
	cfg.Plugins.CRLURL = ""

	dir := pkg.Dir
	mf, _, err := plugins.LoadFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	mf.Capabilities = []string{"session:read"}
	mf.Tools[0].Capabilities = plugins.CapabilitySubset("session:read")
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
	updatedRecord, err := plugins.NewLocalInstallRecord(pkg.Record.CanonicalSource, *mf)
	if err != nil {
		t.Fatal(err)
	}
	updatedDir := filepath.Join(filepath.Dir(dir), updatedRecord.StoreKey)
	if err := os.Rename(dir, updatedDir); err != nil {
		t.Fatal(err)
	}
	dir = updatedDir
	if err := plugins.WriteInstallRecord(dir, updatedRecord, *mf); err != nil {
		t.Fatal(err)
	}
	if err := plugins.RemoveInstallReceipt(cfg.StateDir(), filepath.Dir(dir), pkg.Record.StoreKey); err != nil {
		t.Fatal(err)
	}
	if err := plugins.WriteInstallReceipt(cfg.StateDir(), filepath.Dir(dir), updatedRecord); err != nil {
		t.Fatal(err)
	}
	cfg.Tools.Overrides["fs__read"] = updatedRecord.StoreKey

	reg := BuildDefaultRegistry(nil)
	err = ApplyToolOverrides(reg, cfg)
	if err == nil || !strings.Contains(err.Error(), "session/provider capabilities") {
		t.Fatalf("ApplyToolOverrides error = %v, want session/provider rejection", err)
	}
}

func TestApplyToolOverrides_ReadCapabilityPromotesClass(t *testing.T) {
	cfg := isolatedRuntimeConfig(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pkg := installOverridePlugin(t, cfg, priv, pub, "corp-read-1.0.0", plugins.ToolDef{
		Name:        "fs__read",
		Description: "read via plugin",
		Class:       "NonMutating",
		Schema:      `{"type":"object"}`,
	})
	cfg.Tools.Overrides = map[string]string{"fs__read": pkg.Record.StoreKey}

	dir := pkg.Dir
	mf, _, err := plugins.LoadFromDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	mf.Capabilities = []string{"fs:read:/work", "session:read"}
	mf.Tools[0].Capabilities = plugins.CapabilitySubset("fs:read:/work")
	mf.Tools = append(mf.Tools, plugins.ToolDef{
		Name:         "session__sibling",
		Description:  "session-aware sibling",
		Class:        "Exec",
		Schema:       `{"type":"object"}`,
		Capabilities: plugins.CapabilitySubset("session:read"),
	})
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
	updatedRecord, err := plugins.NewLocalInstallRecord(pkg.Record.CanonicalSource, *mf)
	if err != nil {
		t.Fatal(err)
	}
	updatedDir := filepath.Join(filepath.Dir(dir), updatedRecord.StoreKey)
	if err := os.Rename(dir, updatedDir); err != nil {
		t.Fatal(err)
	}
	dir = updatedDir
	if err := plugins.WriteInstallRecord(dir, updatedRecord, *mf); err != nil {
		t.Fatal(err)
	}
	if err := plugins.RemoveInstallReceipt(cfg.StateDir(), filepath.Dir(dir), pkg.Record.StoreKey); err != nil {
		t.Fatal(err)
	}
	if err := plugins.WriteInstallReceipt(cfg.StateDir(), filepath.Dir(dir), updatedRecord); err != nil {
		t.Fatal(err)
	}
	cfg.Tools.Overrides["fs__read"] = updatedRecord.StoreKey

	reg := BuildDefaultRegistry(nil)
	if err := ApplyToolOverrides(reg, cfg); err != nil {
		t.Fatalf("ApplyToolOverrides: %v", err)
	}
	if got := reg.ClassOf("fs__read"); got != tool.ClassExec {
		t.Fatalf("ClassOf(read) = %v, want %v", got, tool.ClassExec)
	}
}

// VerifyInstalledPlugin is the exported runtime entry for plugin
// verification (TUI override / installed-plugin path). It funnels through
// verifyPluginOverride, which (a) refuses nil arguments and (b) hard-denies
// revoked fingerprints before any I/O. SECURITY.md's "no escape hatch"
// guarantee for the deny-list depends on this — these tests pin both.

func TestVerifyInstalledPlugin_nilManifest(t *testing.T) {
	err := VerifyInstalledPlugin(context.Background(), &config.Config{}, "/nonexistent", nil, "")
	if err == nil {
		t.Fatal("expected error for nil manifest, got nil (would have panicked without the guard)")
	}
	if !strings.Contains(err.Error(), "verify: nil manifest") {
		t.Errorf("expected 'verify: nil manifest', got %v", err)
	}
}

func TestVerifyInstalledPlugin_nilConfig(t *testing.T) {
	mf := &plugins.Manifest{AuthorPubkeyFpr: "deadbeefdeadbeef"}
	err := VerifyInstalledPlugin(context.Background(), nil, "/nonexistent", mf, "")
	if err == nil {
		t.Fatal("expected error for nil config, got nil (would have panicked on cfg.StateDir())")
	}
	if !strings.Contains(err.Error(), "verify: nil config") {
		t.Errorf("expected 'verify: nil config', got %v", err)
	}
}

// The revoked-fingerprint deny runs BEFORE wasm-digest verification and
// BEFORE the trust-store lookup — the "no escape hatch" guarantee.
// Passing a non-existent pluginDir proves no I/O happens: if the deny ran
// AFTER wasm-digest, we'd see a file-read error instead of RevokedError.
func TestVerifyInstalledPlugin_revokedFingerprintRejectedBeforeAnyIO(t *testing.T) {
	const revoked = "6c48b56f20c9c344" // browser-demo.seed
	err := VerifyInstalledPlugin(
		context.Background(),
		&config.Config{},
		"/nonexistent-plugin-dir-no-wasm-no-manifest",
		&plugins.Manifest{AuthorPubkeyFpr: revoked},
		"",
	)
	if err == nil {
		t.Fatal("expected revoked error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "revoked") {
		t.Errorf("expected revoked error, got %v", err)
	}
	if !strings.Contains(msg, "browser-demo.seed") {
		t.Errorf("error should name the leaked seed: %v", err)
	}
	// If the deny had run after wasm-digest, the error would mention the
	// missing wasm file. Assert no I/O leaked through.
	if strings.Contains(msg, "no such file") || strings.Contains(msg, "open ") {
		t.Errorf("revoked deny should fire BEFORE any filesystem I/O, but error suggests I/O happened: %v", err)
	}
}

// Fingerprint-consistency check: a tampered trusted_keys.json shouldn't
// be able to substitute an arbitrary pubkey under a pinned fingerprint
// (the asymmetry between this path and (*TrustStore).VerifyManifest that
// Copilot caught on PR #40). Pre-populate the trust store with a
// deliberately-mismatched entry and assert the override verifier refuses.
func TestVerifyInstalledPlugin_trustStoreFingerprintMismatch(t *testing.T) {
	cfg := isolatedRuntimeConfig(t)
	ts := plugins.NewTrustStore(cfg.StateDir())
	const pinnedFpr = "aaaaaaaaaaaaaaaa"
	// Generate a fresh ed25519 keypair so the test isn't tied to any
	// session-specific key. Its real fingerprint is overwhelmingly unlikely
	// to collide with pinnedFpr (2^-64); guard explicitly anyway.
	realPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	if plugins.Fingerprint(realPub) == pinnedFpr {
		t.Fatal("astronomical collision — re-run the test")
	}
	if err := ts.Save(map[string]plugins.TrustEntry{
		pinnedFpr: {Fingerprint: pinnedFpr, Pubkey: hex.EncodeToString(realPub)},
	}); err != nil {
		t.Fatalf("seed tampered trust store: %v", err)
	}
	// Provide a real wasm file matching the manifest's SHA so the
	// wasm-digest check passes and execution reaches the trust-store
	// fingerprint-consistency check we're testing.
	wasmContent := []byte("test-wasm-content")
	wasmSum := sha256.Sum256(wasmContent)
	manifest := plugins.Manifest{Name: "mismatch", Version: "1.0.0", AuthorPubkeyFpr: pinnedFpr, WASMSHA256: hex.EncodeToString(wasmSum[:])}
	source := filepath.Join(t.TempDir(), "mismatch-source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := plugins.NewLocalInstallRecord(source, manifest)
	if err != nil {
		t.Fatal(err)
	}
	pluginDir := filepath.Join(t.TempDir(), record.StoreKey)
	if err := os.MkdirAll(pluginDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.wasm"), wasmContent, 0o644); err != nil {
		t.Fatalf("write plugin.wasm: %v", err)
	}
	if err := plugins.WriteInstallRecord(pluginDir, record, manifest); err != nil {
		t.Fatal(err)
	}
	if err := plugins.WriteInstallReceipt(cfg.StateDir(), filepath.Dir(pluginDir), record); err != nil {
		t.Fatal(err)
	}
	err = VerifyInstalledPlugin(
		context.Background(),
		cfg,
		pluginDir,
		&manifest,
		"",
	)
	if err == nil {
		t.Fatal("expected fingerprint-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Errorf("expected 'fingerprint mismatch', got %v", err)
	}
}
