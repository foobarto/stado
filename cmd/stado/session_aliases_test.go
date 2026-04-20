package main

import (
	"slices"
	"testing"
)

// TestSessionAliases_ShortcutsAttached: every shell-style alias we
// advertise in the CHANGELOG must be listed on the corresponding
// cobra command. Guards against someone inadvertently removing an
// alias while refactoring the Use/Short fields.
func TestSessionAliases_ShortcutsAttached(t *testing.T) {
	cases := []struct {
		name    string
		cmd     *struct {
			aliases []string
			use     string
		}
		alias string
	}{}
	_ = cases // unused — below is the real check

	// Direct inspection — cobra stores aliases as a string slice on
	// Command. We reach in rather than walk help text to stay stable
	// across cobra's template changes.
	if !slices.Contains(sessionListCmd.Aliases, "ls") {
		t.Errorf("session list is missing 'ls' alias: %v", sessionListCmd.Aliases)
	}
	if !slices.Contains(sessionDeleteCmd.Aliases, "rm") {
		t.Errorf("session delete is missing 'rm' alias: %v", sessionDeleteCmd.Aliases)
	}
	if !slices.Contains(sessionExportCmd.Aliases, "cat") {
		t.Errorf("session export is missing 'cat' alias: %v", sessionExportCmd.Aliases)
	}
}
