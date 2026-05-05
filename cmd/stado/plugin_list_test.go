package main

import (
	"bytes"
	"strings"
	"testing"
)

// TestPluginList_ShowsBundled: with no installed plugins, the list
// still shows bundled ones with the ✓ bundled status.
func TestPluginList_ShowsBundled(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	var stdout bytes.Buffer
	pluginListCmd.SetOut(&stdout)
	pluginListCmd.SetErr(&stdout)
	defer func() {
		pluginListCmd.SetOut(nil)
		pluginListCmd.SetErr(nil)
	}()

	if err := pluginListCmd.RunE(pluginListCmd, nil); err != nil {
		t.Fatalf("pluginListCmd.RunE: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "bundled") {
		t.Errorf("output should mention 'bundled' status; got:\n%s", out)
	}
	// The auto-compact module is always registered (init() in
	// internal/bundledplugins/auto_compact.go).
	if !strings.Contains(out, "auto-compact") {
		t.Errorf("output should list 'auto-compact' bundled module; got:\n%s", out)
	}
}

// TestPluginList_BundledHasDashFingerprint: bundled rows render the
// fingerprint column as "-" rather than empty/truncated noise.
func TestPluginList_BundledHasDashFingerprint(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	var stdout bytes.Buffer
	pluginListCmd.SetOut(&stdout)
	pluginListCmd.SetErr(&stdout)
	defer func() {
		pluginListCmd.SetOut(nil)
		pluginListCmd.SetErr(nil)
	}()

	if err := pluginListCmd.RunE(pluginListCmd, nil); err != nil {
		t.Fatalf("pluginListCmd.RunE: %v", err)
	}
	out := stdout.String()
	for _, line := range strings.Split(out, "\n") {
		// Only inspect data rows: those carry the "✓ bundled" status
		// marker. The summary line ("N plugins (M bundled; ...)")
		// also contains the word "bundled" but isn't a row.
		if !strings.Contains(line, "✓ bundled") {
			continue
		}
		// Bundled rows should show "-" in the fingerprint position.
		// The tabwriter pads with spaces, so look for "- " in the line
		// where bundled appears. (The exact column alignment depends
		// on the longest row's fingerprint; "-" is the shortest, so
		// "- " is a reliable substring.)
		if !strings.Contains(line, "- ") && !strings.Contains(line, "\t-\t") {
			t.Errorf("bundled row should show '-' fingerprint; got: %q", line)
		}
	}
}
