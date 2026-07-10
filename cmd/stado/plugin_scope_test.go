package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

func TestPluginScopeIncludesProjectAndGlobalInstalls(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	project := filepath.Join(root, "project")
	projectStado := filepath.Join(project, ".stado")
	if err := os.MkdirAll(projectStado, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}

	projectID := "local-1.0.0"
	globalID := "global-2.0.0"
	if err := os.MkdirAll(filepath.Join(cfg.ProjectPluginsDir(), projectID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cfg.StateDir(), "plugins", globalID), 0o700); err != nil {
		t.Fatal(err)
	}
	locations, err := listInstalledPluginLocations(cfg)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, location := range locations {
		seen[location.ID] = location.Scope
	}
	if seen[projectID] != "project" || seen[globalID] != "global" {
		t.Fatalf("installed locations = %#v, want project and global scopes", seen)
	}

	targets := pluginLockTargets(cfg)
	if len(targets) != 2 || !targets[0].Local || targets[1].Local {
		t.Fatalf("lock targets = %#v, want project then global", targets)
	}
}

func TestWithPluginInstallScopeRestoresPreviousValue(t *testing.T) {
	pluginInstallLocal = false
	t.Cleanup(func() { pluginInstallLocal = false })
	if err := withPluginInstallScope(true, func() error {
		if !pluginInstallLocal {
			t.Fatal("project-local update did not select local install scope")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if pluginInstallLocal {
		t.Fatal("plugin install scope leaked after update")
	}
}

func TestWritePluginActiveMarkerRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	pluginsDir := filepath.Join(root, "plugins")
	installDir := filepath.Join(pluginsDir, "demo-1.0.0")
	if err := os.MkdirAll(installDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(pluginsDir, "active")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := writePluginActiveMarker(installDir, "demo", "1.0.0"); err == nil {
		t.Fatal("active marker write followed a symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "demo")); !os.IsNotExist(err) {
		t.Fatalf("outside marker was written: %v", err)
	}
}

func TestWritePluginActiveMarkerRejectsTraversalSegments(t *testing.T) {
	installDir := filepath.Join(t.TempDir(), "plugins", "demo-1.0.0")
	if err := os.MkdirAll(installDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, version string }{
		{name: "..", version: "1.0.0"},
		{name: "demo", version: "../outside"},
	} {
		if err := writePluginActiveMarker(installDir, tc.name, tc.version); err == nil {
			t.Fatalf("writePluginActiveMarker(%q, %q) accepted traversal", tc.name, tc.version)
		}
	}
}
