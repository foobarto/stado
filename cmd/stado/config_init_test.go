package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

func TestConfigInit_WritesPrivateFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))

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
