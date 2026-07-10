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
