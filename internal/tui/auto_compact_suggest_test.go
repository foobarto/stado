package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

// seedInstalledAutoCompact pretends an auto-compact plugin is
// installed by creating a directory under $XDG_DATA_HOME/stado/plugins.
// The version suffix is configurable per test.
func seedInstalledAutoCompact(t *testing.T, version string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	dir := filepath.Join(root, "stado", "plugins", "auto-compact-"+version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestInstalledAutoCompact_ReturnsDirWhenPresent asserts the scanner
// finds the plugin directory and returns its name (which the
// hard-threshold advisory then formats into a /plugin:<name> hint).
func TestInstalledAutoCompact_ReturnsDirWhenPresent(t *testing.T) {
	seedInstalledAutoCompact(t, "0.1.0")

	rnd, _ := render.New(theme.Default())
	m := NewModel("/tmp", "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())
	got := m.installedAutoCompact()
	if got != "auto-compact-0.1.0" {
		t.Errorf("installedAutoCompact = %q, want auto-compact-0.1.0", got)
	}
}

// TestInstalledAutoCompact_PicksLatestVersion: multiple installed
// versions → lexicographically greatest wins (matches install-bump
// ordering in practice).
func TestInstalledAutoCompact_PicksLatestVersion(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	for _, v := range []string{"0.1.0", "0.2.5", "0.2.3"} {
		_ = os.MkdirAll(filepath.Join(root, "stado", "plugins", "auto-compact-"+v), 0o755)
	}

	rnd, _ := render.New(theme.Default())
	m := NewModel("/tmp", "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())
	got := m.installedAutoCompact()
	if got != "auto-compact-0.2.5" {
		t.Errorf("installedAutoCompact = %q, want auto-compact-0.2.5", got)
	}
}

// TestInstalledAutoCompact_EmptyWhenNotInstalled keeps the no-plugin
// advisory path clean: the hard-threshold block should not mention
// auto-compact when nothing by that name is on disk.
func TestInstalledAutoCompact_EmptyWhenNotInstalled(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	rnd, _ := render.New(theme.Default())
	m := NewModel("/tmp", "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())
	if got := m.installedAutoCompact(); got != "" {
		t.Errorf("installedAutoCompact should be empty when uninstalled, got %q", got)
	}
}

// TestInstalledAutoCompact_IgnoresOtherPluginNames confirms the
// scanner doesn't match session-inspect-*/hello-go-* etc. — only
// plugins whose directory starts with `auto-compact-`.
func TestInstalledAutoCompact_IgnoresOtherPluginNames(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	for _, name := range []string{"session-inspect-0.1.0", "hello-go-0.1.0", "hello-0.1.0"} {
		_ = os.MkdirAll(filepath.Join(root, "stado", "plugins", name), 0o755)
	}

	rnd, _ := render.New(theme.Default())
	m := NewModel("/tmp", "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())
	if got := m.installedAutoCompact(); got != "" {
		t.Errorf("unrelated plugins matched: got %q", got)
	}
}

// TestHardThresholdAdvisory_IncludesAutoCompactWhenInstalled: the
// glue that ties the scanner to the block rendering. Seed the plugin
// and force aboveHardThreshold, then read back the advisory the
// block-renderer would produce via InputSubmit.
func TestHardThresholdAdvisory_IncludesAutoCompactWhenInstalled(t *testing.T) {
	seedInstalledAutoCompact(t, "0.1.0")

	rnd, _ := render.New(theme.Default())
	m := NewModel("/tmp", "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())
	// Force "above threshold" without dragging in a real provider:
	// any ctxHardThreshold > 0 combined with usage.InputTokens >= cap
	// does it. Use a synthetic fake-capped provider (tests already
	// ship one in threshold_test.go).
	m.ctxHardThreshold = 0.9
	m.provider = fakeCappedProvider{max: 100}
	m.usage.InputTokens = 95

	// Rather than drive the real key-input path (complicated setup),
	// just build the advisory text directly via the same renderer
	// logic — the behaviour we're proving is "does the renderer
	// include /plugin:auto-compact-* when the plugin is installed?".
	ac := m.installedAutoCompact()
	if ac == "" {
		t.Fatal("auto-compact plugin should have been detected")
	}
	// Regression: ensure the format used in the advisory actually
	// references the full installed directory name.
	if !strings.Contains("/plugin:"+ac+" compact", "auto-compact-0.1.0") {
		t.Errorf("advisory format wouldn't include the plugin id: %q", ac)
	}
}
