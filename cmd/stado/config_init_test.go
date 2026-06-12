package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

// TestConfigInitTemplate_DocumentsHooksSidebarFooter: the init template
// must document the [tui.sidebar].sections / [tui.footer].segments knobs
// and the modern lifecycle-hooks config (deny/mutate/fail_closed), not the
// stale "notification-only, cannot block or modify a turn" text that only
// covered post_turn. Reproduces C2 (P2): before the fix the template never
// mentioned sidebar.sections/footer.segments and its hooks docs were stale.
func TestConfigInitTemplate_DocumentsHooksSidebarFooter(t *testing.T) {
	tmpl := defaultConfigTemplate
	for _, want := range []string{
		"sidebar",
		"sections",
		"footer",
		"segments",
		"hooks.lifecycle",
		"fail_closed",
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("config init template missing %q", want)
		}
	}
	// The lifecycle hooks can deny/mutate — the template must say so,
	// and must NOT keep claiming hooks are notification-only.
	lowered := strings.ToLower(tmpl)
	if !strings.Contains(lowered, "deny") || !strings.Contains(lowered, "mutate") {
		t.Errorf("config init template hooks docs don't mention deny/mutate lifecycle hooks")
	}
	if strings.Contains(tmpl, "cannot block or modify a turn") {
		t.Errorf("config init template still carries stale 'cannot block or modify a turn' hooks docs")
	}
}

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
