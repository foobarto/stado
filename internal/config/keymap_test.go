package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadKeymap verifies the [keymap] table parses: a schema name plus a
// [keymap.bindings] sub-table of action-name -> comma-separated keys.
func TestLoadKeymap(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	configDir := filepath.Join(cfgHome, "stado")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[keymap]\n" +
		"schema = \"vscode\"\n" +
		"[keymap.bindings]\n" +
		"sidebar_toggle = \"ctrl+y\"\n" +
		"input_line_home = \"home,ctrl+a\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Keymap.Schema != "vscode" {
		t.Errorf("Keymap.Schema = %q, want vscode", cfg.Keymap.Schema)
	}
	if got := cfg.Keymap.Bindings["sidebar_toggle"]; got != "ctrl+y" {
		t.Errorf("Keymap.Bindings[sidebar_toggle] = %q, want ctrl+y", got)
	}
	if got := cfg.Keymap.Bindings["input_line_home"]; got != "home,ctrl+a" {
		t.Errorf("Keymap.Bindings[input_line_home] = %q, want \"home,ctrl+a\"", got)
	}
}

// TestLoadKeymapDefault verifies an absent [keymap] table leaves an empty
// schema (treated as the emacs default downstream) and a nil bindings map.
func TestLoadKeymapDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Keymap.Schema != "" {
		t.Errorf("Keymap.Schema = %q, want empty default", cfg.Keymap.Schema)
	}
	if len(cfg.Keymap.Bindings) != 0 {
		t.Errorf("Keymap.Bindings = %v, want empty default", cfg.Keymap.Bindings)
	}
}
