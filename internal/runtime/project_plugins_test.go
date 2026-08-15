package runtime

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

// TestResolveInstalledPluginDir_FindsProjectLocal guards EP-0035: a plugin
// installed under the project-local .stado/plugins/ must be discoverable, not
// just plugins under the global state dir. Before the fix, the resolver (and
// the agent-loop tool registry) hard-coded the global state dir, so project
// plugins never loaded.
func TestResolveInstalledPluginDir_FindsProjectLocal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))

	proj := filepath.Join(root, "proj")
	pluginsRoot := filepath.Join(proj, ".stado", "plugins")
	if err := os.MkdirAll(pluginsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	wasm := []byte("project-plugin")
	digest := sha256.Sum256(wasm)
	manifest := plugins.Manifest{Name: "foo", Version: "0.1.0", WASMSHA256: hex.EncodeToString(digest[:])}
	source := filepath.Join(root, "foo-source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := plugins.NewLocalInstallRecord(source, manifest)
	if err != nil {
		t.Fatal(err)
	}
	installDir := filepath.Join(pluginsRoot, record.StoreKey)
	if err := os.MkdirAll(installDir, 0o700); err != nil {
		t.Fatal(err)
	}
	canonical, _ := manifest.Canonical()
	for name, data := range map[string][]byte{"plugin.manifest.json": canonical, "plugin.manifest.sig": []byte("sig"), "plugin.wasm": wasm} {
		if err := os.WriteFile(filepath.Join(installDir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := plugins.WriteInstallRecord(installDir, record, manifest); err != nil {
		t.Fatal(err)
	}

	orig, _ := os.Getwd()
	defer func() { _ = os.Chdir(orig) }()
	if err := os.Chdir(proj); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ProjectPluginsDir() == "" {
		t.Fatal("precondition: project .stado/plugins should be discovered by config.Load")
	}

	pkg, err := plugins.ResolveInstalledPackage(cfg.AllPluginDirs(), "foo")
	if err != nil {
		t.Fatalf("project-local plugin foo should resolve (EP-0035): %v", err)
	}
	if !strings.Contains(pkg.Dir, filepath.Join(".stado", "plugins")) {
		t.Errorf("expected the project-local dir, got %q", pkg.Dir)
	}
}
