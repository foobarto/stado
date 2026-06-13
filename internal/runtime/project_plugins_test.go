package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
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
	if err := os.MkdirAll(filepath.Join(proj, ".stado", "plugins", "foo-0.1.0"), 0o755); err != nil {
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

	dir, ok := ResolveInstalledPluginDir(cfg, "foo")
	if !ok {
		t.Fatal("project-local plugin foo should resolve (EP-0035)")
	}
	if !strings.Contains(dir, filepath.Join(".stado", "plugins")) {
		t.Errorf("expected the project-local dir, got %q", dir)
	}
}
