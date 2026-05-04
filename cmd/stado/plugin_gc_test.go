package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPluginGC_DryRunListsCandidates: with three versions installed
// (0.1.0, 0.2.0, 0.3.0), --keep=1 (default) and no --apply lists the
// two older versions but doesn't delete them.
func TestPluginGC_DryRunListsCandidates(t *testing.T) {
	cfg := isolatedHome(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pluginInstallSigner = hex.EncodeToString(pub)
	defer func() { pluginInstallSigner = "" }()

	for _, v := range []string{"0.1.0", "0.2.0", "0.3.0"} {
		src := buildTestPluginWithCaps(t, priv, pub, "demo", v, nil)
		if err := pluginInstallCmd.RunE(pluginInstallCmd, []string{src}); err != nil {
			t.Fatalf("install %s: %v", v, err)
		}
	}

	pluginsDir := filepath.Join(cfg.StateDir(), "plugins")
	for _, want := range []string{"demo-0.1.0", "demo-0.2.0", "demo-0.3.0"} {
		if _, err := os.Stat(filepath.Join(pluginsDir, want)); err != nil {
			t.Fatalf("expected %s installed: %v", want, err)
		}
	}

	pluginGCKeep = 1
	pluginGCApply = false
	if err := pluginGCCmd.RunE(pluginGCCmd, nil); err != nil {
		t.Fatalf("gc dry-run: %v", err)
	}

	// All three still present after dry-run.
	for _, want := range []string{"demo-0.1.0", "demo-0.2.0", "demo-0.3.0"} {
		if _, err := os.Stat(filepath.Join(pluginsDir, want)); err != nil {
			t.Errorf("dry-run deleted %s: %v", want, err)
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

	for _, v := range []string{"0.1.0", "0.2.0", "0.3.0"} {
		src := buildTestPluginWithCaps(t, priv, pub, "demo", v, nil)
		if err := pluginInstallCmd.RunE(pluginInstallCmd, []string{src}); err != nil {
			t.Fatalf("install %s: %v", v, err)
		}
	}

	pluginsDir := filepath.Join(cfg.StateDir(), "plugins")

	pluginGCKeep = 1
	pluginGCApply = true
	defer func() { pluginGCApply = false }()
	if err := pluginGCCmd.RunE(pluginGCCmd, nil); err != nil {
		t.Fatalf("gc apply: %v", err)
	}

	// 0.3.0 kept.
	if _, err := os.Stat(filepath.Join(pluginsDir, "demo-0.3.0")); err != nil {
		t.Errorf("apply deleted newest demo-0.3.0: %v", err)
	}
	// 0.1.0 + 0.2.0 gone.
	for _, want := range []string{"demo-0.1.0", "demo-0.2.0"} {
		if _, err := os.Stat(filepath.Join(pluginsDir, want)); !os.IsNotExist(err) {
			t.Errorf("apply should have removed %s, got %v", want, err)
		}
	}
}

// TestPluginGC_KeepN: --keep=2 keeps the two newest, drops the rest.
func TestPluginGC_KeepN(t *testing.T) {
	cfg := isolatedHome(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	pluginInstallSigner = hex.EncodeToString(pub)
	defer func() { pluginInstallSigner = "" }()

	for _, v := range []string{"0.1.0", "0.2.0", "0.3.0", "0.4.0"} {
		src := buildTestPluginWithCaps(t, priv, pub, "demo", v, nil)
		if err := pluginInstallCmd.RunE(pluginInstallCmd, []string{src}); err != nil {
			t.Fatalf("install %s: %v", v, err)
		}
	}

	pluginsDir := filepath.Join(cfg.StateDir(), "plugins")

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
		if _, err := os.Stat(filepath.Join(pluginsDir, want)); err != nil {
			t.Errorf("apply deleted %s; should be kept: %v", want, err)
		}
	}
	for _, want := range []string{"demo-0.1.0", "demo-0.2.0"} {
		if _, err := os.Stat(filepath.Join(pluginsDir, want)); !os.IsNotExist(err) {
			t.Errorf("apply should have removed %s, got %v", want, err)
		}
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

	install := func(pub ed25519.PublicKey, priv ed25519.PrivateKey, name, v string) {
		t.Helper()
		pluginInstallSigner = hex.EncodeToString(pub)
		src := buildTestPluginWithCaps(t, priv, pub, name, v, nil)
		if err := pluginInstallCmd.RunE(pluginInstallCmd, []string{src}); err != nil {
			t.Fatalf("install %s-%s: %v", name, v, err)
		}
	}
	defer func() { pluginInstallSigner = "" }()

	install(pubA, privA, "alpha", "0.1.0")
	install(pubA, privA, "alpha", "0.2.0")
	install(pubB, privB, "beta", "0.1.0")

	pluginsDir := filepath.Join(cfg.StateDir(), "plugins")

	pluginGCKeep = 1
	pluginGCApply = true
	defer func() { pluginGCApply = false }()
	if err := pluginGCCmd.RunE(pluginGCCmd, nil); err != nil {
		t.Fatalf("gc apply: %v", err)
	}

	// alpha-0.2.0 (kept within its group) and beta-0.1.0 (only
	// version in its group) survive.
	for _, want := range []string{"alpha-0.2.0", "beta-0.1.0"} {
		if _, err := os.Stat(filepath.Join(pluginsDir, want)); err != nil {
			t.Errorf("kept-version %s missing: %v", want, err)
		}
	}
	// alpha-0.1.0 sweeps as the older version in alpha's group.
	if _, err := os.Stat(filepath.Join(pluginsDir, "alpha-0.1.0")); !os.IsNotExist(err) {
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
