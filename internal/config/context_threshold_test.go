package config

import (
	"os"
	"path/filepath"
	"testing"
)

// R9: an explicit `0` in config.toml disables the gate and must survive Load
// (not be coerced back to the default), per docs/features/context.md.
func TestContextThreshold_ZeroDisablesNotDefaulted(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	path := filepath.Join(tmp, "stado", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[context]\nsoft_threshold = 0.0\nhard_threshold = 0.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Context.SoftThreshold != 0 {
		t.Errorf("explicit soft_threshold=0 should stay 0 (disabled), got %v", cfg.Context.SoftThreshold)
	}
	if cfg.Context.HardThreshold != 0 {
		t.Errorf("explicit hard_threshold=0 should stay 0 (disabled), got %v", cfg.Context.HardThreshold)
	}
}

// When the keys are absent, the defaults still apply.
func TestContextThreshold_UnsetGetsDefault(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	path := filepath.Join(tmp, "stado", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[defaults]\nprovider = \"anthropic\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Context.SoftThreshold != 0.70 {
		t.Errorf("unset soft_threshold should default to 0.70, got %v", cfg.Context.SoftThreshold)
	}
	if cfg.Context.HardThreshold != 0.90 {
		t.Errorf("unset hard_threshold should default to 0.90, got %v", cfg.Context.HardThreshold)
	}
}
