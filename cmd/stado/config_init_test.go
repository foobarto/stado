package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

func TestConfigInit_WritesPrivateFile(t *testing.T) {
	setConfigInitEnv(t)

	configInitForce = true
	defer func() { configInitForce = false }()

	if err := configInitCmd.RunE(configInitCmd, nil); err != nil {
		t.Fatalf("config init: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(cfg.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %#o, want 0600", got)
	}
}

func TestConfigInitRejectsSymlink(t *testing.T) {
	root := setConfigInitEnv(t)
	configDir := filepath.Join(root, "config", "stado")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(configDir, "decoy.toml")
	if err := os.WriteFile(decoy, []byte("[defaults]\nprovider = \"old\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.Symlink("decoy.toml", configPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	configInitForce = true
	defer func() { configInitForce = false }()

	if err := configInitCmd.RunE(configInitCmd, nil); err == nil {
		t.Fatal("config init should reject symlinked config path")
	}
	data, err := os.ReadFile(decoy)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "[defaults]\nprovider = \"old\"\n" {
		t.Fatalf("symlink target modified: %q", data)
	}
}

func TestConfigInitRejectsExistingWithoutForce(t *testing.T) {
	root := setConfigInitEnv(t)
	configDir := filepath.Join(root, "config", "stado")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("[defaults]\nprovider = \"old\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	configInitForce = false

	if err := configInitCmd.RunE(configInitCmd, nil); err == nil {
		t.Fatal("config init should require --force for existing config")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "[defaults]\nprovider = \"old\"\n" {
		t.Fatalf("existing config modified: %q", data)
	}
}

func setConfigInitEnv(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	return root
}
