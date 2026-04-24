package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// Provider + Model are intentionally empty on a fresh config —
	// stado probes local runners at provider-build time rather than
	// assuming a specific hosted provider as a default.
	if cfg.Defaults.Provider != "" {
		t.Errorf("Defaults.Provider = %q, want empty (probe-at-build)", cfg.Defaults.Provider)
	}
	if cfg.Defaults.Model != "" {
		t.Errorf("Defaults.Model = %q, want empty", cfg.Defaults.Model)
	}
	if cfg.Approvals.Mode != "prompt" {
		t.Errorf("Approvals.Mode = %q, want %q", cfg.Approvals.Mode, "prompt")
	}
	if cfg.Agent.SystemPromptPath == "" {
		t.Fatal("Agent.SystemPromptPath should default to a config-dir template")
	}
	if cfg.Agent.SystemPromptTemplate == "" {
		t.Fatal("Agent.SystemPromptTemplate should be loaded")
	}
	if _, err := os.Stat(cfg.Agent.SystemPromptPath); err != nil {
		t.Fatalf("default system prompt template not created: %v", err)
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

func TestLoadCustomSystemPromptPath(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	customPath := filepath.Join(cfgHome, "custom-system.md")
	if err := os.WriteFile(customPath, []byte("model={{ .Model }} project={{ .ProjectInstructions }}"), 0o600); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(cfgHome, "stado")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("agent.system_prompt_path = "+quoteTOML(customPath)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Agent.SystemPromptPath != customPath {
		t.Fatalf("SystemPromptPath = %q, want %q", cfg.Agent.SystemPromptPath, customPath)
	}
	if !strings.Contains(cfg.Agent.SystemPromptTemplate, "{{ .Model }}") {
		t.Fatalf("custom template not loaded: %q", cfg.Agent.SystemPromptTemplate)
	}
}

func TestLoadRejectsInvalidSystemPromptTemplate(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	configDir := filepath.Join(cfgHome, "stado")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "system-prompt.md"), []byte("{{ .Missing }}"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "validate [agent].system_prompt_path") {
		t.Fatalf("expected template validation error, got %v", err)
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

func quoteTOML(s string) string {
	return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"`
}
