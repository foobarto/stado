package main

import (
	"strings"
	"testing"
)

// TestToolMutate_RejectsUnknownLiteral reproduces P2.17: `tool enable
// bogus.tool` accepted an unknown literal and persisted it to
// [tools].enabled with rc=0, while `tool info`/`tool run` reject unknown
// names. A non-glob literal that names no known tool is now rejected; globs
// (and known tools, incl. currently-disabled ones) still pass.
func TestToolMutate_RejectsUnknownLiteral(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	toolMutateDryRun = true
	defer func() { toolMutateDryRun = false }()

	if err := runToolMutate("enable", "enabled", "disabled", []string{"bogus.tool"}); err == nil ||
		!strings.Contains(err.Error(), "unknown") {
		t.Errorf("unknown literal should be rejected, got: %v", err)
	}
	// A known canonical tool name passes validation.
	if err := runToolMutate("enable", "enabled", "disabled", []string{"fs.read"}); err != nil {
		t.Errorf("known tool 'fs.read' should be accepted, got: %v", err)
	}
	// A glob is allowed even when it currently matches nothing.
	if err := runToolMutate("enable", "enabled", "disabled", []string{"nomatch*"}); err != nil {
		t.Errorf("glob should be accepted, got: %v", err)
	}
}
