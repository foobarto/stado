package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Defaults.Provider != "anthropic" {
		t.Errorf("Defaults.Provider = %q, want %q", cfg.Defaults.Provider, "anthropic")
	}
	if cfg.Defaults.Model != "claude-sonnet-4-5" {
		t.Errorf("Defaults.Model = %q, want %q", cfg.Defaults.Model, "claude-sonnet-4-5")
	}
	if cfg.Approvals.Mode != "prompt" {
		t.Errorf("Approvals.Mode = %q, want %q", cfg.Approvals.Mode, "prompt")
	}
	if !cfg.Context.Enabled {
		t.Error("Context.Enabled should be true by default")
	}
}

func TestEnvOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("STADO_DEFAULTS_PROVIDER", "openai")
	t.Setenv("STADODEFAULTS_MODEL", "gpt-4o")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Defaults.Provider != "openai" {
		t.Errorf("Defaults.Provider = %q, want %q", cfg.Defaults.Provider, "openai")
	}
}

func TestConfigPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	expected := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "stado", "config.toml")
	if cfg.ConfigPath != expected {
		t.Errorf("ConfigPath = %q, want %q", cfg.ConfigPath, expected)
	}
}

func TestStateDir(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	expected := filepath.Join(os.Getenv("XDG_DATA_HOME"), "stado")
	if cfg.StateDir() != expected {
		t.Errorf("StateDir() = %q, want %q", cfg.StateDir(), expected)
	}
}
