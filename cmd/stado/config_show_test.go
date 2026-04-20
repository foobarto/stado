package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

// TestConfigShow_HumanOutputHasKeySections: human mode renders
// every important section.
func TestConfigShow_HumanOutputHasKeySections(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

	cfg, _ := config.Load()
	var buf bytes.Buffer
	if err := renderConfigHuman(&buf, cfg); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"config file",
		"state dir",
		"[defaults]",
		"provider",
		"[approvals]",
		"mode",
		"[agent]",
		"[context]",
		"soft_threshold",
		"hard_threshold",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("config show output missing %q", want)
		}
	}
}

// TestConfigShow_NonExistentFileHint: when config.toml doesn't
// exist the output calls that out explicitly so users don't think
// their config file is being read.
func TestConfigShow_NonExistentFileHint(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))

	cfg, _ := config.Load()
	if _, err := os.Stat(cfg.ConfigPath); !os.IsNotExist(err) {
		t.Fatalf("pre-condition: config.toml should not exist, got %v", err)
	}
	var buf bytes.Buffer
	_ = renderConfigHuman(&buf, cfg)
	if !strings.Contains(buf.String(), "does not exist") {
		t.Errorf("missing 'does not exist' hint: %q", buf.String())
	}
}

// TestConfigShow_EnvOverridesAppear: STADO_DEFAULTS_PROVIDER is one
// of the many env keys the config loader honours. Setting it should
// show up in the rendered output.
func TestConfigShow_EnvOverridesAppear(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("STADO_DEFAULTS_PROVIDER", "ollama")

	cfg, _ := config.Load()
	var buf bytes.Buffer
	_ = renderConfigHuman(&buf, cfg)
	if !strings.Contains(buf.String(), "ollama") {
		t.Errorf("env override not reflected: %q", buf.String())
	}
}

// TestConfigShow_JSONShapeIsValid: --json output parses as JSON.
func TestConfigShow_JSONShapeIsValid(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))

	cfg, _ := config.Load()
	// Marshal cfg directly — this is what the --json code path does.
	enc, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(enc, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"Defaults", "Approvals", "Context", "Agent"} {
		if _, ok := parsed[key]; !ok {
			t.Errorf("JSON output missing key %q", key)
		}
	}
}
