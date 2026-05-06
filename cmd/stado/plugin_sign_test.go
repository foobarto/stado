package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

// TestPluginSign_KeyEnv_Hex: --key-env reads the seed from an env
// var encoded as hex (64 chars).
func TestPluginSign_KeyEnv_Hex(t *testing.T) {
	dir := t.TempDir()
	manifestPath, _ := writeUnsignedTestPlugin(t, dir, "demo", "0.1.0")

	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	seed := priv.Seed()
	t.Setenv("STADO_TEST_SIGNING_KEY", hex.EncodeToString(seed))
	pluginSignKeyPath = ""
	pluginSignKeyEnv = "STADO_TEST_SIGNING_KEY"
	pluginSignQuiet = true
	defer func() {
		pluginSignKeyEnv = ""
		pluginSignQuiet = false
	}()

	if err := pluginSignCmd.RunE(pluginSignCmd, []string{manifestPath}); err != nil {
		t.Fatalf("sign with --key-env hex: %v", err)
	}
	sigPath := filepath.Join(dir, "plugin.manifest.sig")
	if _, err := os.Stat(sigPath); err != nil {
		t.Errorf("expected sig file written; got: %v", err)
	}
}

// TestPluginSign_KeyEnv_Base64: --key-env accepts base64-encoded seed.
func TestPluginSign_KeyEnv_Base64(t *testing.T) {
	dir := t.TempDir()
	manifestPath, _ := writeUnsignedTestPlugin(t, dir, "demo", "0.1.0")

	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	seed := priv.Seed()
	t.Setenv("STADO_TEST_SIGNING_KEY", base64.StdEncoding.EncodeToString(seed))
	pluginSignKeyPath = ""
	pluginSignKeyEnv = "STADO_TEST_SIGNING_KEY"
	pluginSignQuiet = true
	defer func() {
		pluginSignKeyEnv = ""
		pluginSignQuiet = false
	}()

	if err := pluginSignCmd.RunE(pluginSignCmd, []string{manifestPath}); err != nil {
		t.Fatalf("sign with --key-env base64: %v", err)
	}
}

// TestPluginSign_KeyEnv_MissingVar: --key-env pointing at an unset
// env var is a clear error.
func TestPluginSign_KeyEnv_MissingVar(t *testing.T) {
	dir := t.TempDir()
	manifestPath, _ := writeUnsignedTestPlugin(t, dir, "demo", "0.1.0")

	pluginSignKeyPath = ""
	pluginSignKeyEnv = "STADO_TEST_SIGNING_KEY_UNSET_FOR_THIS_TEST"
	defer func() { pluginSignKeyEnv = "" }()

	err := pluginSignCmd.RunE(pluginSignCmd, []string{manifestPath})
	if err == nil {
		t.Fatal("expected error when env var is unset")
	}
	if !strings.Contains(err.Error(), "empty or unset") {
		t.Errorf("error should mention `empty or unset`; got: %v", err)
	}
}

// TestPluginSign_BothKeyAndKeyEnv: passing both --key and --key-env
// is rejected.
func TestPluginSign_BothKeyAndKeyEnv(t *testing.T) {
	dir := t.TempDir()
	manifestPath, _ := writeUnsignedTestPlugin(t, dir, "demo", "0.1.0")

	seedPath := filepath.Join(dir, "key.seed")
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	if err := os.WriteFile(seedPath, priv.Seed(), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STADO_TEST_SIGNING_KEY", hex.EncodeToString(priv.Seed()))
	pluginSignKeyPath = seedPath
	pluginSignKeyEnv = "STADO_TEST_SIGNING_KEY"
	defer func() {
		pluginSignKeyPath = ""
		pluginSignKeyEnv = ""
	}()

	err := pluginSignCmd.RunE(pluginSignCmd, []string{manifestPath})
	if err == nil {
		t.Fatal("expected error when both --key and --key-env are passed")
	}
	if !strings.Contains(err.Error(), "exactly one") {
		t.Errorf("error should mention `exactly one`; got: %v", err)
	}
}

// TestPluginSign_NoKeyAtAll: no --key and no --key-env is an error.
func TestPluginSign_NoKeyAtAll(t *testing.T) {
	dir := t.TempDir()
	manifestPath, _ := writeUnsignedTestPlugin(t, dir, "demo", "0.1.0")

	pluginSignKeyPath = ""
	pluginSignKeyEnv = ""

	err := pluginSignCmd.RunE(pluginSignCmd, []string{manifestPath})
	if err == nil {
		t.Fatal("expected error when neither --key nor --key-env is set")
	}
	if !strings.Contains(err.Error(), "--key-env") {
		t.Errorf("error should mention --key-env as an option; got: %v", err)
	}
}

// writeUnsignedTestPlugin sets up a minimal manifest + wasm dir for
// sign tests. Returns the manifest path + the wasm path.
func writeUnsignedTestPlugin(t *testing.T, dir, name, version string) (manifestPath, wasmPath string) {
	t.Helper()
	wasmPath = filepath.Join(dir, "plugin.wasm")
	if err := os.WriteFile(wasmPath, []byte("test-wasm-bytes-"+name), 0o644); err != nil {
		t.Fatal(err)
	}
	mf := plugins.Manifest{
		Name:    name,
		Version: version,
		Author:  "ci-test",
	}
	manifestPath = filepath.Join(dir, "plugin.manifest.json")
	body, _ := json.MarshalIndent(&mf, "", "  ")
	if err := os.WriteFile(manifestPath, body, 0o644); err != nil {
		t.Fatal(err)
	}
	return manifestPath, wasmPath
}

func TestPluginGenKeyRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	decoy := filepath.Join(dir, "decoy.seed")
	if err := os.WriteFile(decoy, []byte("do not replace"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "plugin.seed")
	if err := os.Symlink("decoy.seed", path); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if err := pluginGenKeyCmd.RunE(pluginGenKeyCmd, []string{path}); err == nil {
		t.Fatal("plugin gen-key should reject symlinked seed path")
	}
	data, err := os.ReadFile(decoy)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "do not replace" {
		t.Fatalf("symlink target modified: %q", data)
	}
}

func TestPluginSignRejectsManifestSymlink(t *testing.T) {
	dir := t.TempDir()
	manifest := []byte(renderManifest("demo"))
	decoy := filepath.Join(dir, "decoy.manifest.json")
	if err := os.WriteFile(decoy, manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(dir, "plugin.manifest.json")
	if err := os.Symlink("decoy.manifest.json", manifestPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), []byte("wasm"), 0o644); err != nil {
		t.Fatal(err)
	}
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	seedPath := filepath.Join(dir, "plugin.seed")
	if err := os.WriteFile(seedPath, seed, 0o600); err != nil {
		t.Fatal(err)
	}

	oldKeyPath, oldWasm := pluginSignKeyPath, pluginSignWasm
	pluginSignKeyPath, pluginSignWasm = seedPath, ""
	defer func() {
		pluginSignKeyPath, pluginSignWasm = oldKeyPath, oldWasm
	}()

	if err := pluginSignCmd.RunE(pluginSignCmd, []string{manifestPath}); err == nil {
		t.Fatal("plugin sign should reject symlinked manifest path")
	} else if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
	data, err := os.ReadFile(decoy)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(manifest) {
		t.Fatalf("symlink target modified: %q", data)
	}
}

func TestPluginSignRejectsKeySymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.json"), []byte(renderManifest("demo")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), []byte("wasm"), 0o644); err != nil {
		t.Fatal(err)
	}
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	target := filepath.Join(dir, "target.seed")
	if err := os.WriteFile(target, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "plugin.seed")
	if err := os.Symlink("target.seed", keyPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	oldKeyPath, oldWasm := pluginSignKeyPath, pluginSignWasm
	pluginSignKeyPath, pluginSignWasm = keyPath, ""
	defer func() {
		pluginSignKeyPath, pluginSignWasm = oldKeyPath, oldWasm
	}()

	err := pluginSignCmd.RunE(pluginSignCmd, []string{filepath.Join(dir, "plugin.manifest.json")})
	if err == nil {
		t.Fatal("plugin sign should reject symlinked key path")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
}

func TestPluginSignRejectsWASMSymlink(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.json"), []byte(renderManifest("demo")), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "target.wasm")
	if err := os.WriteFile(target, []byte("wasm"), 0o644); err != nil {
		t.Fatal(err)
	}
	wasmPath := filepath.Join(dir, "plugin.wasm")
	if err := os.Symlink("target.wasm", wasmPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	seedPath := filepath.Join(dir, "plugin.seed")
	if err := os.WriteFile(seedPath, seed, 0o600); err != nil {
		t.Fatal(err)
	}

	oldKeyPath, oldWasm := pluginSignKeyPath, pluginSignWasm
	pluginSignKeyPath, pluginSignWasm = seedPath, ""
	defer func() {
		pluginSignKeyPath, pluginSignWasm = oldKeyPath, oldWasm
	}()

	err := pluginSignCmd.RunE(pluginSignCmd, []string{filepath.Join(dir, "plugin.manifest.json")})
	if err == nil {
		t.Fatal("plugin sign should reject symlinked wasm path")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
}

func TestPluginSignRejectsOversizedManifest(t *testing.T) {
	dir := t.TempDir()
	manifestPath := filepath.Join(dir, "plugin.manifest.json")
	if err := os.WriteFile(manifestPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(manifestPath, maxPluginSignManifestBytes+1); err != nil {
		t.Fatal(err)
	}
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	seedPath := filepath.Join(dir, "plugin.seed")
	if err := os.WriteFile(seedPath, seed, 0o600); err != nil {
		t.Fatal(err)
	}

	oldKeyPath, oldWasm := pluginSignKeyPath, pluginSignWasm
	pluginSignKeyPath, pluginSignWasm = seedPath, ""
	defer func() {
		pluginSignKeyPath, pluginSignWasm = oldKeyPath, oldWasm
	}()

	err := pluginSignCmd.RunE(pluginSignCmd, []string{manifestPath})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("plugin sign error = %v, want size rejection", err)
	}
}

func TestPluginSignRejectsOversizedWASM(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.json"), []byte(renderManifest("demo")), 0o644); err != nil {
		t.Fatal(err)
	}
	wasmPath := filepath.Join(dir, "plugin.wasm")
	if err := os.WriteFile(wasmPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(wasmPath, maxPluginSignWASMBytes+1); err != nil {
		t.Fatal(err)
	}
	seed := make([]byte, ed25519.SeedSize)
	for i := range seed {
		seed[i] = byte(i + 1)
	}
	seedPath := filepath.Join(dir, "plugin.seed")
	if err := os.WriteFile(seedPath, seed, 0o600); err != nil {
		t.Fatal(err)
	}

	oldKeyPath, oldWasm := pluginSignKeyPath, pluginSignWasm
	pluginSignKeyPath, pluginSignWasm = seedPath, ""
	defer func() {
		pluginSignKeyPath, pluginSignWasm = oldKeyPath, oldWasm
	}()

	err := pluginSignCmd.RunE(pluginSignCmd, []string{filepath.Join(dir, "plugin.manifest.json")})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("plugin sign error = %v, want size rejection", err)
	}
}

func TestPluginDigestRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.wasm")
	if err := os.WriteFile(target, []byte("wasm"), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "plugin.wasm")
	if err := os.Symlink("target.wasm", path); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	err := pluginDigestCmd.RunE(pluginDigestCmd, []string{path})
	if err == nil {
		t.Fatal("plugin digest should reject symlinked input")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
}

func TestPluginDigestRejectsOversizedWASM(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugin.wasm")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, maxPluginSignWASMBytes+1); err != nil {
		t.Fatal(err)
	}

	err := pluginDigestCmd.RunE(pluginDigestCmd, []string{path})
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("plugin digest error = %v, want size rejection", err)
	}
}
