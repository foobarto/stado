package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

func installGCVersion(t *testing.T, priv ed25519.PrivateKey, pub ed25519.PublicKey, name, version, source, pluginsRoot string) plugins.InstalledPackage {
	t.Helper()
	generated := buildTestPluginWithCaps(t, priv, pub, name, version, nil)
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{"plugin.wasm", "plugin.manifest.json", "plugin.manifest.sig"} {
		data, err := os.ReadFile(filepath.Join(generated, filename))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(source, filename), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := pluginInstallCmd.RunE(pluginInstallCmd, []string{source}); err != nil {
		t.Fatalf("install %s: %v", version, err)
	}
	mf, _, err := plugins.LoadFromDir(generated)
	if err != nil {
		t.Fatal(err)
	}
	packages, err := plugins.ListInstalledPackages(pluginsRoot)
	if err != nil {
		t.Fatal(err)
	}
	record, err := plugins.NewLocalInstallRecord(source, *mf)
	if err != nil {
		t.Fatal(err)
	}
	for _, pkg := range packages {
		if pkg.Record.StoreKey == record.StoreKey {
			return pkg
		}
	}
	t.Fatalf("installed store row %s not found", record.StoreKey)
	return plugins.InstalledPackage{}
}

// TestPluginGC_DryRunListsCandidates: with three versions installed
// (0.1.0, 0.2.0, 0.3.0), --keep=1 (default) and no --apply lists the
// two older versions but doesn't delete them.
func TestPluginGC_DryRunListsCandidates(t *testing.T) {
	cfg := isolatedHome(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pluginInstallSigner = hex.EncodeToString(pub)
	defer func() { pluginInstallSigner = "" }()

	pluginsDir := filepath.Join(cfg.StateDir(), "plugins")
	source := filepath.Join(t.TempDir(), "demo")
	var installed []plugins.InstalledPackage
	for _, v := range []string{"0.1.0", "0.2.0", "0.3.0"} {
		installed = append(installed, installGCVersion(t, priv, pub, "demo", v, source, pluginsDir))
	}
	for _, pkg := range installed {
		if _, err := os.Stat(pkg.Dir); err != nil {
			t.Fatalf("expected %s installed: %v", pkg.Record.StoreKey, err)
		}
	}

	pluginGCKeep = 1
	pluginGCApply = false
	if err := pluginGCCmd.RunE(pluginGCCmd, nil); err != nil {
		t.Fatalf("gc dry-run: %v", err)
	}

	// All three still present after dry-run.
	for _, pkg := range installed {
		if _, err := os.Stat(pkg.Dir); err != nil {
			t.Errorf("dry-run deleted %s: %v", pkg.Record.StoreKey, err)
		}
	}
}

// TestPluginGC_ApplyDeletesOlder: --apply removes the older versions
// and leaves the newest.
func TestPluginGC_ApplyDeletesOlder(t *testing.T) {
	cfg := isolatedHome(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pluginInstallSigner = hex.EncodeToString(pub)
	defer func() { pluginInstallSigner = "" }()

	pluginsDir := filepath.Join(cfg.StateDir(), "plugins")
	source := filepath.Join(t.TempDir(), "demo")
	installed := make(map[string]plugins.InstalledPackage)
	for _, v := range []string{"0.1.0", "0.2.0", "0.3.0"} {
		installed[v] = installGCVersion(t, priv, pub, "demo", v, source, pluginsDir)
	}
	lockPath := pluginLockPath(cfg, false)
	lock := plugins.NewLock()
	for version, pkg := range installed {
		lock.Add(plugins.LockEntry{StoreKey: pkg.Record.StoreKey, Identity: "github.com/acme/demo@v" + version})
	}
	if err := lock.Write(lockPath); err != nil {
		t.Fatal(err)
	}

	pluginGCKeep = 1
	pluginGCApply = true
	defer func() { pluginGCApply = false }()
	if err := pluginGCCmd.RunE(pluginGCCmd, nil); err != nil {
		t.Fatalf("gc apply: %v", err)
	}

	// 0.3.0 kept.
	if _, err := os.Stat(installed["0.3.0"].Dir); err != nil {
		t.Errorf("apply deleted newest demo-0.3.0: %v", err)
	}
	// 0.1.0 + 0.2.0 gone.
	for _, want := range []string{"demo-0.1.0", "demo-0.2.0"} {
		pkg := installed[strings.TrimPrefix(want, "demo-")]
		if _, err := os.Stat(pkg.Dir); !os.IsNotExist(err) {
			t.Errorf("apply should have removed %s, got %v", want, err)
		}
		if err := plugins.CheckInstallReceipt(cfg.StateDir(), pluginsDir, pkg.Record); err == nil {
			t.Errorf("apply retained admission receipt for %s", want)
		}
	}
	if err := plugins.CheckInstallReceipt(cfg.StateDir(), pluginsDir, installed["0.3.0"].Record); err != nil {
		t.Fatalf("newest admission receipt was removed: %v", err)
	}
	after, err := plugins.ReadLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Entries) != len(installed) {
		t.Fatalf("gc deleted immutable continuity history: %+v", after.Entries)
	}
}

// TestPluginGC_KeepN: --keep=2 keeps the two newest, drops the rest.
func TestPluginGC_KeepN(t *testing.T) {
	cfg := isolatedHome(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pluginInstallSigner = hex.EncodeToString(pub)
	defer func() { pluginInstallSigner = "" }()

	pluginsDir := filepath.Join(cfg.StateDir(), "plugins")
	source := filepath.Join(t.TempDir(), "demo")
	installed := make(map[string]plugins.InstalledPackage)
	for _, v := range []string{"0.1.0", "0.2.0", "0.3.0", "0.4.0"} {
		installed[v] = installGCVersion(t, priv, pub, "demo", v, source, pluginsDir)
	}

	pluginGCKeep = 2
	pluginGCApply = true
	defer func() {
		pluginGCApply = false
		pluginGCKeep = 1
	}()
	if err := pluginGCCmd.RunE(pluginGCCmd, nil); err != nil {
		t.Fatalf("gc apply: %v", err)
	}

	for _, want := range []string{"demo-0.3.0", "demo-0.4.0"} {
		if _, err := os.Stat(installed[strings.TrimPrefix(want, "demo-")].Dir); err != nil {
			t.Errorf("apply deleted %s; should be kept: %v", want, err)
		}
	}
	for _, want := range []string{"demo-0.1.0", "demo-0.2.0"} {
		if _, err := os.Stat(installed[strings.TrimPrefix(want, "demo-")].Dir); !os.IsNotExist(err) {
			t.Errorf("apply should have removed %s, got %v", want, err)
		}
	}
}

func TestPluginGC_PreservesPinnedActiveVersion(t *testing.T) {
	cfg := isolatedHome(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pluginInstallSigner = hex.EncodeToString(pub)
	defer func() { pluginInstallSigner = "" }()
	pluginsDir := filepath.Join(cfg.StateDir(), "plugins")
	source := filepath.Join(t.TempDir(), "demo")
	installed := make(map[string]plugins.InstalledPackage)
	for _, version := range []string{"0.1.0", "0.2.0", "0.3.0"} {
		installed[version] = installGCVersion(t, priv, pub, "demo", version, source, pluginsDir)
	}
	if err := plugins.WriteActivePackageMarker(pluginsDir, installed["0.1.0"]); err != nil {
		t.Fatal(err)
	}

	pluginGCKeep = 1
	pluginGCApply = true
	t.Cleanup(func() { pluginGCApply = false })
	if err := pluginGCCmd.RunE(pluginGCCmd, nil); err != nil {
		t.Fatal(err)
	}
	for _, kept := range []string{"demo-0.1.0", "demo-0.3.0"} {
		if _, err := os.Stat(installed[strings.TrimPrefix(kept, "demo-")].Dir); err != nil {
			t.Fatalf("pinned/newest version %s was deleted: %v", kept, err)
		}
	}
	if _, err := os.Stat(installed["0.2.0"].Dir); !os.IsNotExist(err) {
		t.Fatalf("unpinned middle version survived: %v", err)
	}
}

// TestPluginGC_PerSignerGroups: plugins from DIFFERENT signers
// stay in separate groups; gc only sweeps within each group.
// Setup: signer A ships alpha-0.1.0 + alpha-0.2.0; signer B ships
// beta-0.1.0 alone. With --keep=1, alpha-0.1.0 goes (older within
// its group), beta-0.1.0 stays (the only version in its group).
func TestPluginGC_PerSignerGroups(t *testing.T) {
	cfg := isolatedHome(t)
	pubA, privA, _ := ed25519.GenerateKey(rand.Reader)
	pubB, privB, _ := ed25519.GenerateKey(rand.Reader)

	pluginsDir := filepath.Join(cfg.StateDir(), "plugins")
	installed := make(map[string]plugins.InstalledPackage)
	install := func(pub ed25519.PublicKey, priv ed25519.PrivateKey, name, v, source string) {
		t.Helper()
		pluginInstallSigner = hex.EncodeToString(pub)
		installed[name+"-"+v] = installGCVersion(t, priv, pub, name, v, source, pluginsDir)
	}
	defer func() { pluginInstallSigner = "" }()

	alphaSource := filepath.Join(t.TempDir(), "alpha")
	betaSource := filepath.Join(t.TempDir(), "beta")
	install(pubA, privA, "alpha", "0.1.0", alphaSource)
	install(pubA, privA, "alpha", "0.2.0", alphaSource)
	install(pubB, privB, "beta", "0.1.0", betaSource)

	pluginGCKeep = 1
	pluginGCApply = true
	defer func() { pluginGCApply = false }()
	if err := pluginGCCmd.RunE(pluginGCCmd, nil); err != nil {
		t.Fatalf("gc apply: %v", err)
	}

	// alpha-0.2.0 (kept within its group) and beta-0.1.0 (only
	// version in its group) survive.
	for _, want := range []string{"alpha-0.2.0", "beta-0.1.0"} {
		if _, err := os.Stat(installed[want].Dir); err != nil {
			t.Errorf("kept-version %s missing: %v", want, err)
		}
	}
	// alpha-0.1.0 sweeps as the older version in alpha's group.
	if _, err := os.Stat(installed["alpha-0.1.0"].Dir); !os.IsNotExist(err) {
		t.Errorf("apply should have removed alpha-0.1.0, got %v", err)
	}
}

// TestPluginGC_KeepZeroRejected covers the input-validation guard.
func TestPluginGC_KeepZeroRejected(t *testing.T) {
	_ = isolatedHome(t)
	pluginGCKeep = 0
	defer func() { pluginGCKeep = 1 }()
	err := pluginGCCmd.RunE(pluginGCCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--keep must be >= 1") {
		t.Fatalf("expected --keep=0 to be rejected, got %v", err)
	}
}
