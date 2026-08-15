package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
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

	wasm := []byte("scope-test")
	digest := sha256.Sum256(wasm)
	manifest := plugins.Manifest{Name: "display", Version: "1.0.0", WASMSHA256: hex.EncodeToString(digest[:])}
	projectPkg := writeTestLocalInstalledPackage(t, cfg.ProjectPluginsDir(), filepath.Join(root, "project-source"), manifest, "sig", wasm)
	globalPkg := writeTestLocalInstalledPackage(t, filepath.Join(cfg.StateDir(), "plugins"), filepath.Join(root, "global-source"), manifest, "sig", wasm)
	locations, err := listInstalledPluginLocations(cfg)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]string{}
	for _, location := range locations {
		seen[location.ID] = location.Scope
	}
	if seen[projectPkg.Record.StoreKey] != "project" || seen[globalPkg.Record.StoreKey] != "global" {
		t.Fatalf("installed locations = %#v, want project and global scopes", seen)
	}

	targets := pluginLockTargets(cfg)
	if len(targets) != 2 || !targets[0].Local || targets[1].Local {
		t.Fatalf("lock targets = %#v, want project then global", targets)
	}
}

func TestScopedSelectorAndRemoveDisambiguateIdenticalStoreRows(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(filepath.Join(project, ".stado"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	wasm := []byte("identical-package")
	digest := sha256.Sum256(wasm)
	manifest := plugins.Manifest{Name: "same", Version: "1.0.0", WASMSHA256: hex.EncodeToString(digest[:])}
	source := filepath.Join(root, "one-source")
	projectPkg := writeTestLocalInstalledPackage(t, cfg.ProjectPluginsDir(), source, manifest, "sig", wasm)
	globalRoot := filepath.Join(cfg.StateDir(), "plugins")
	globalPkg := writeTestLocalInstalledPackage(t, globalRoot, source, manifest, "sig", wasm)
	if projectPkg.Record.StoreKey != globalPkg.Record.StoreKey {
		t.Fatal("identical package/source did not produce identical store key")
	}
	for rootDir, pkg := range map[string]plugins.InstalledPackage{cfg.ProjectPluginsDir(): projectPkg, globalRoot: globalPkg} {
		if err := plugins.WriteInstallReceipt(cfg.StateDir(), rootDir, pkg.Record); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := resolveManagedInstalledPackage(cfg, projectPkg.Record.StoreKey); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("unscoped duplicate error = %v", err)
	}
	for scope, wantDir := range map[string]string{"project": projectPkg.Dir, "global": globalPkg.Dir} {
		pkg, selectedRoot, err := resolveManagedInstalledPackage(cfg, scope+":"+projectPkg.Record.StoreKey)
		if err != nil || pkg.Dir != wantDir || selectedRoot.Scope != scope {
			t.Fatalf("%s resolution pkg=%+v root=%+v err=%v", scope, pkg, selectedRoot, err)
		}
	}
	pluginRemoveCmd.SetOut(os.Stderr)
	if err := pluginRemoveCmd.RunE(pluginRemoveCmd, []string{"global:" + globalPkg.Record.StoreKey}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(globalPkg.Dir); !os.IsNotExist(err) {
		t.Fatalf("scoped global remove left global row: %v", err)
	}
	if _, err := os.Stat(projectPkg.Dir); err != nil {
		t.Fatalf("scoped global remove deleted project row: %v", err)
	}
	if err := plugins.CheckInstallReceipt(cfg.StateDir(), globalRoot, globalPkg.Record); err == nil {
		t.Fatal("scoped global remove retained global receipt")
	}
	if err := plugins.CheckInstallReceipt(cfg.StateDir(), cfg.ProjectPluginsDir(), projectPkg.Record); err != nil {
		t.Fatalf("scoped global remove revoked project receipt: %v", err)
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
	installDir := filepath.Join(pluginsDir, "local-"+strings.Repeat("0", 64))
	if err := os.MkdirAll(installDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(pluginsDir, "active")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	pkg := plugins.InstalledPackage{Dir: installDir, Record: plugins.InstallRecord{StoreKey: filepath.Base(installDir)}, Identity: plugins.RuntimeIdentity{Namespace: "local://test"}}
	if err := plugins.WriteActivePackageMarker(pluginsDir, pkg); err == nil {
		t.Fatal("active marker write followed a symlink")
	}
	entries, _ := os.ReadDir(outside)
	if len(entries) != 0 {
		t.Fatalf("outside marker was written: %v", entries)
	}
}
