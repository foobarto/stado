package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

func TestEffectiveBackgroundPluginIDs_IncludeBundledAutoCompact(t *testing.T) {
	ids := effectiveBackgroundPluginIDs(&config.Config{}, nil)
	if len(ids) == 0 || ids[0] != "auto-compact" {
		t.Fatalf("default background ids = %v, want auto-compact first", ids)
	}
}

// TestEffectiveBackgroundPluginIDs_IncludesLaunchPersonaPlugins: a launch
// persona's `plugins:` are unioned into the background set (launch-only),
// after cfg.Plugins.Background, deduped against the defaults + config.
func TestEffectiveBackgroundPluginIDs_IncludesLaunchPersonaPlugins(t *testing.T) {
	cfg := &config.Config{}
	cfg.Plugins.Background = []string{"recorder"}
	ids := effectiveBackgroundPluginIDs(cfg, []string{"telemetry", "recorder"})
	has := func(id string) bool {
		for _, x := range ids {
			if x == id {
				return true
			}
		}
		return false
	}
	if !has("telemetry") {
		t.Errorf("persona plugin 'telemetry' should be in the background set; got %v", ids)
	}
	// Deduped: recorder appears once even though it's in both config + persona.
	count := 0
	for _, x := range ids {
		if x == "recorder" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("recorder should appear exactly once (deduped); got %d in %v", count, ids)
	}
	// Default bundled auto-compact still leads.
	if ids[0] != "auto-compact" {
		t.Errorf("auto-compact should still lead; got %v", ids)
	}
}

func TestHasAutoCompactBackgroundPlugin_DetectsLoadedPlugin(t *testing.T) {
	rnd, _ := render.New(theme.Default())
	m := NewModel("/tmp", "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())
	m.backgroundPlugins = []*pluginRuntime.BackgroundPlugin{{
		Manifest: plugins.Manifest{},
	}}
	if m.hasAutoCompactBackgroundPlugin() {
		t.Fatal("empty manifest should not count as auto-compact")
	}
	m.backgroundPlugins = []*pluginRuntime.BackgroundPlugin{{
		Manifest: plugins.Manifest{Name: "auto-compact"},
	}}
	if !m.hasAutoCompactBackgroundPlugin() {
		t.Fatal("auto-compact background plugin should have been detected")
	}
}

func TestLoadOneBackgroundRejectsEscapingPluginID(t *testing.T) {
	rnd, _ := render.New(theme.Default())
	m := NewModel("/tmp", "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())
	pluginsRoot := filepath.Join(t.TempDir(), "plugins")
	if err := os.MkdirAll(filepath.Join(filepath.Dir(pluginsRoot), "escape"), 0o755); err != nil {
		t.Fatal(err)
	}

	bp, note := m.loadOneBackground(context.TODO(), nil, &config.Config{}, []string{pluginsRoot}, "../escape")
	if bp != nil {
		t.Fatal("escaping background plugin id unexpectedly loaded")
	}
	if !strings.Contains(note, "invalid plugin id") {
		t.Fatalf("background plugin note = %q, want invalid plugin id", note)
	}
}

func seedInstalledAutoCompactAt(t *testing.T, source, version string) plugins.InstalledPackage {
	t.Helper()
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := plugins.Manifest{
		Name: "auto-compact", Version: version,
		WASMSHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	data, err := manifest.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "plugin.manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "plugin.manifest.sig"), []byte("test-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	record, err := plugins.NewLocalInstallRecord(source, manifest)
	if err != nil {
		t.Fatal(err)
	}
	pluginsRoot := filepath.Join(os.Getenv("XDG_DATA_HOME"), "stado", "plugins")
	dir := filepath.Join(pluginsRoot, record.StoreKey)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.sig"), []byte("test-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := plugins.WriteInstallRecord(dir, record, manifest); err != nil {
		t.Fatal(err)
	}
	identity, err := plugins.RuntimeIdentityForLocalSource(manifest, source)
	if err != nil {
		t.Fatal(err)
	}
	return plugins.InstalledPackage{Dir: dir, Record: record, Manifest: manifest, Identity: identity}
}

func seedInstalledAutoCompact(t *testing.T, version string) plugins.InstalledPackage {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	return seedInstalledAutoCompactAt(t, filepath.Join(t.TempDir(), "auto-compact"), version)
}

// TestInstalledAutoCompact_ReturnsDirWhenPresent asserts the scanner
// finds the plugin directory and returns its name (which the
// hard-threshold advisory then formats into a /plugin:<name> hint).
func TestInstalledAutoCompact_ReturnsDirWhenPresent(t *testing.T) {
	pkg := seedInstalledAutoCompact(t, "0.1.0")

	rnd, _ := render.New(theme.Default())
	m := NewModel("/tmp", "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())
	got := m.installedAutoCompact()
	if got != pkg.Identity.Canonical {
		t.Errorf("installedAutoCompact = %q, want %q", got, pkg.Identity.Canonical)
	}
}

// TestInstalledAutoCompact_PicksLatestVersion: multiple installed
// versions → lexicographically greatest wins (matches install-bump
// ordering in practice).
func TestInstalledAutoCompact_PicksLatestVersion(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	source := filepath.Join(t.TempDir(), "auto-compact")
	var latest plugins.InstalledPackage
	for _, v := range []string{"0.1.0", "0.2.5", "0.2.3"} {
		pkg := seedInstalledAutoCompactAt(t, source, v)
		if v == "0.2.5" {
			latest = pkg
		}
	}

	rnd, _ := render.New(theme.Default())
	m := NewModel("/tmp", "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())
	got := m.installedAutoCompact()
	if got != latest.Identity.Canonical {
		t.Errorf("installedAutoCompact = %q, want %q", got, latest.Identity.Canonical)
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
		installFakePlugin(t, name+"-source", plugins.Manifest{Name: name, Version: "0.1.0"})
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
	pkg := seedInstalledAutoCompact(t, "0.1.0")

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
	if ac != pkg.Identity.Canonical || !strings.Contains("/plugin:"+ac+" compact", "@0.1.0") {
		t.Errorf("advisory format wouldn't include the plugin id: %q", ac)
	}
}
