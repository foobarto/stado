package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/workdirpath"
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

// TestPluginInstall_Autoload: --autoload flag persists the plugin's
// tools into [tools].autoload in config.toml so they're loaded into
// future sessions without a separate `tool autoload` call.
func TestPluginInstall_Autoload(t *testing.T) {
	cfg := isolatedHome(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	src := buildTestPluginWithTools(t, priv, pub, "auto", "1.0.0",
		[]plugins.ToolDef{
			{Name: "auto_lookup", Description: "lookup"},
			{Name: "auto_search", Description: "search"},
		})

	pluginInstallSigner = hex.EncodeToString(pub)
	pluginInstallAutoload = true
	defer func() {
		pluginInstallSigner = ""
		pluginInstallAutoload = false
	}()

	if err := pluginInstallCmd.RunE(pluginInstallCmd, []string{src}); err != nil {
		t.Fatalf("install: %v", err)
	}

	// Re-load config and verify autoload entries landed.
	reloaded, err := config.Load()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	got := reloaded.Tools.Autoload
	wantHas := map[string]bool{"auto_lookup": false, "auto_search": false}
	for _, name := range got {
		if _, ok := wantHas[name]; ok {
			wantHas[name] = true
		}
	}
	for name, found := range wantHas {
		if !found {
			t.Errorf("autoload missing %q after install --autoload (got %v)", name, got)
		}
	}
	_ = cfg // already verified via reloaded
}

// buildTestPluginWithTools is buildTestPlugin + tools in the manifest.
// Used by --autoload tests.
func buildTestPluginWithTools(t *testing.T, priv ed25519.PrivateKey, pub ed25519.PublicKey, name, version string, tools []plugins.ToolDef) string {
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
		Tools:           tools,
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

func TestPluginInstall_NormalizesInstalledPermissions(t *testing.T) {
	cfg := isolatedHome(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	src := buildTestPlugin(t, priv, pub, "demo", "1.0.0")
	if err := os.Chmod(filepath.Join(src, "plugin.wasm"), 0o777); err != nil {
		t.Fatal(err)
	}
	extraDir := filepath.Join(src, "bin")
	if err := os.Mkdir(extraDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extraDir, "helper.sh"), []byte("#!/bin/sh\n"), 0o777); err != nil {
		t.Fatal(err)
	}

	pluginInstallSigner = hex.EncodeToString(pub)
	defer func() { pluginInstallSigner = "" }()

	if err := pluginInstallCmd.RunE(pluginInstallCmd, []string{src}); err != nil {
		t.Fatalf("install: %v", err)
	}
	dst := filepath.Join(cfg.StateDir(), "plugins", "demo-1.0.0")
	assertPerm := func(path string, want os.FileMode) {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode = %04o, want %04o", path, got, want)
		}
	}
	assertPerm(dst, 0o700)
	assertPerm(filepath.Join(dst, "plugin.wasm"), 0o700)
	assertPerm(filepath.Join(dst, "plugin.manifest.json"), 0o600)
	assertPerm(filepath.Join(dst, "bin"), 0o700)
	assertPerm(filepath.Join(dst, "bin", "helper.sh"), 0o700)
}

func TestPluginInstallRejectsOversizedAuxiliaryFile(t *testing.T) {
	cfg := isolatedHome(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	src := buildTestPlugin(t, priv, pub, "demo", "1.0.0")
	extraPath := filepath.Join(src, "extra.bin")
	if err := os.WriteFile(extraPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(extraPath, maxPluginInstallFileBytes+1); err != nil {
		t.Fatal(err)
	}

	pluginInstallSigner = hex.EncodeToString(pub)
	defer func() { pluginInstallSigner = "" }()

	err := pluginInstallCmd.RunE(pluginInstallCmd, []string{src})
	if err == nil {
		t.Fatal("expected oversized auxiliary plugin file to be rejected")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected size rejection, got %v", err)
	}
	dst := filepath.Join(cfg.StateDir(), "plugins", "demo-1.0.0")
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("failed install left destination behind, stat err = %v", statErr)
	}
}

func TestCopyPluginFileRejectsOversizedFile(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()
	extraPath := filepath.Join(src, "extra.bin")
	if err := os.WriteFile(extraPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(extraPath, maxPluginInstallFileBytes+1); err != nil {
		t.Fatal(err)
	}
	srcRoot, err := workdirpath.OpenRootNoSymlink(src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srcRoot.Close() }()
	dstRoot, err := workdirpath.OpenRootNoSymlink(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dstRoot.Close() }()

	err = copyPluginFile(srcRoot, dstRoot, "extra.bin", 0o600)
	if err == nil {
		t.Fatal("expected oversized plugin file to be rejected")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected size rejection, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dst, "extra.bin")); !os.IsNotExist(statErr) {
		t.Fatalf("copy left destination file behind, stat err = %v", statErr)
	}
}

func TestCopyDirRejectsSourceSymlinkEscape(t *testing.T) {
	src := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(src, "escape.txt")); err != nil {
		t.Fatal(err)
	}

	err := copyDir(src, filepath.Join(t.TempDir(), "installed"))
	if err == nil {
		t.Fatal("expected symlink source entry to be rejected")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
}

func TestCopyDirRejectsDestinationSymlink(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "plugin.wasm"), []byte("wasm"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	dst := filepath.Join(t.TempDir(), "installed")
	if err := os.Symlink(outside, dst); err != nil {
		t.Fatal(err)
	}

	err := copyDir(src, dst)
	if err == nil {
		t.Fatal("expected destination symlink to be rejected")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
}

func TestCopyDirRejectsDestinationParentSymlink(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "plugin.wasm"), []byte("wasm"), 0o644); err != nil {
		t.Fatal(err)
	}
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "plugins")
	if err := os.Symlink("outside", link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	err := copyDir(src, filepath.Join(link, "installed"))
	if err == nil {
		t.Fatal("expected destination parent symlink to be rejected")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(outside, "installed")); !os.IsNotExist(statErr) {
		t.Fatalf("symlink target was modified, stat err = %v", statErr)
	}
}

func TestCopyDirRejectsTooManyEntries(t *testing.T) {
	src := t.TempDir()
	for i := 0; i <= maxPluginInstallEntries; i++ {
		if err := os.WriteFile(filepath.Join(src, fmt.Sprintf("%04d.txt", i)), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dst := filepath.Join(t.TempDir(), "installed")

	err := copyDir(src, dst)
	if err == nil {
		t.Fatal("expected oversized plugin package entry count to be rejected")
	}
	if !strings.Contains(err.Error(), "more than") {
		t.Fatalf("expected entry count rejection, got %v", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("failed copy left destination behind, stat err = %v", statErr)
	}
}

func TestCopyDirRejectsTooDeepPackage(t *testing.T) {
	src := t.TempDir()
	rel := "."
	for i := 0; i <= maxPluginInstallDepth; i++ {
		name := fmt.Sprintf("d%02d", i)
		rel = filepath.Join(rel, name)
		if err := os.Mkdir(filepath.Join(src, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	dst := filepath.Join(t.TempDir(), "installed")

	err := copyDir(src, dst)
	if err == nil {
		t.Fatal("expected deeply nested plugin package to be rejected")
	}
	if !strings.Contains(err.Error(), "nesting") {
		t.Fatalf("expected depth rejection, got %v", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("failed copy left destination behind, stat err = %v", statErr)
	}
}

func TestVerifyInstalledPluginCopyRejectsWASMSwap(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	src := buildTestPlugin(t, priv, pub, "demo", "1.0.0")
	m, sig, err := plugins.LoadFromDir(src)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "installed")
	if err := copyDir(src, dst); err != nil {
		t.Fatalf("copy: %v", err)
	}
	if err := verifyInstalledPluginCopy(dst, m, sig); err != nil {
		t.Fatalf("initial verify: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dst, "plugin.wasm"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = verifyInstalledPluginCopy(dst, m, sig)
	if err == nil {
		t.Fatal("expected tampered installed wasm to be rejected")
	}
	if !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected digest mismatch, got %v", err)
	}
}

// TestPluginInstall_SignerMismatchRejected: provide a --signer whose
// fingerprint doesn't match the manifest's author_pubkey_fpr.
func TestPluginInstall_SignerMismatchRejected(t *testing.T) {
	cfg := isolatedHome(t)
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
	store, loadErr := plugins.NewTrustStore(cfg.StateDir()).Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(store) != 0 {
		t.Fatalf("mismatched signer should not be pinned, got %+v", store)
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

	prevOut := pluginInstallCmd.OutOrStdout()
	prevErr := pluginInstallCmd.ErrOrStderr()
	defer func() {
		pluginInstallCmd.SetOut(prevOut)
		pluginInstallCmd.SetErr(prevErr)
	}()
	var out, errBuf bytes.Buffer
	pluginInstallCmd.SetOut(&out)
	pluginInstallCmd.SetErr(&errBuf)

	if err := pluginInstallCmd.RunE(pluginInstallCmd, []string{src}); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if !strings.Contains(out.String(), "installed demo v1.0.0") {
		t.Fatalf("first install stdout missing success line: %q", out.String())
	}
	out.Reset()
	errBuf.Reset()
	if err := pluginInstallCmd.RunE(pluginInstallCmd, []string{src}); err != nil {
		t.Errorf("re-install should be no-op, got %v", err)
	}
	if !strings.Contains(out.String(), "skipped: demo v1.0.0 already installed at") {
		t.Fatalf("reinstall stdout missing skip line: %q", out.String())
	}
	if strings.Contains(errBuf.String(), "already installed") {
		t.Fatalf("reinstall should not warn on stderr, got %q", errBuf.String())
	}
}

func TestPluginInstall_SignerDoesNotResetRollbackState(t *testing.T) {
	_ = isolatedHome(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pluginInstallSigner = hex.EncodeToString(pub)
	defer func() { pluginInstallSigner = "" }()

	srcHigh := buildTestPlugin(t, priv, pub, "demo", "2.0.0")
	if err := pluginInstallCmd.RunE(pluginInstallCmd, []string{srcHigh}); err != nil {
		t.Fatalf("install high version: %v", err)
	}

	srcLow := buildTestPlugin(t, priv, pub, "demo", "1.0.0")
	err := pluginInstallCmd.RunE(pluginInstallCmd, []string{srcLow})
	if err == nil || !strings.Contains(err.Error(), "rollback") {
		t.Fatalf("install low version with --signer = %v, want rollback rejection", err)
	}
}
