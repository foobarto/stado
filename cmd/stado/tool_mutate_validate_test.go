package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

// TestValidateKnownToolArgs reproduces P2.17: unknown literal tool names were
// accepted and persisted to a [tools].<list>. validateKnownToolArgs rejects a
// non-glob literal that names no known tool; globs and known tools (incl.
// currently-disabled ones) pass.
func TestValidateKnownToolArgs(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	if err := validateKnownToolArgs("enable", []string{"bogus.tool"}); err == nil ||
		!strings.Contains(err.Error(), "unknown") {
		t.Errorf("unknown literal should be rejected, got: %v", err)
	}
	if err := validateKnownToolArgs("enable", []string{"fs.read"}); err != nil {
		t.Errorf("known tool 'fs.read' should pass, got: %v", err)
	}
	if err := validateKnownToolArgs("enable", []string{"nomatch*"}); err != nil {
		t.Errorf("glob should pass, got: %v", err)
	}
}

// TestToolDisable_ValidatesBeforeAutoloadRemoval is the Codex P2 regression:
// `tool disable` removes the inverse [tools].autoload entry before delegating
// to runToolMutate, so validating inside runToolMutate left the config
// partially mutated when one of several args was invalid. Validation now runs
// in the command BEFORE any write — disabling a known-autoloaded tool plus an
// unknown literal must error AND leave autoload untouched.
func TestToolDisable_ValidatesBeforeAutoloadRemoval(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	cwd := t.TempDir()
	restore := chdir(t, cwd)
	defer restore()

	path := filepath.Join(cwd, ".stado", "config.toml")
	if err := config.WriteToolsListAdd(path, "autoload", []string{"fs.read"}); err != nil {
		t.Fatalf("seed autoload: %v", err)
	}

	err := toolDisableCmd.RunE(toolDisableCmd, []string{"fs.read", "bogus.tool"})
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("invalid disable should error with unknown tool, got: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	found := false
	for _, a := range cfg.Tools.Autoload {
		if a == "fs.read" {
			found = true
		}
	}
	if !found {
		t.Errorf("partial mutation: fs.read was removed from autoload despite the invalid command; autoload=%v", cfg.Tools.Autoload)
	}
}
