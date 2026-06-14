package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
)

// EP-0039 §G: fetchLatestTag was a no-op stub; parseReleaseTagName is the pure
// core of its real implementation. plugin remove was absent entirely.

func TestParseReleaseTagName(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want    string
		wantErr bool
	}{
		{"github valid", `{"tag_name":"v1.2.3","name":"rel"}`, "v1.2.3", false},
		{"gitlab valid", `{"tag_name":"v0.4.0","released_at":"x"}`, "v0.4.0", false},
		{"missing tag_name", `{"name":"rel"}`, "", true},
		{"empty tag_name", `{"tag_name":""}`, "", true},
		{"garbage", `not json`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseReleaseTagName([]byte(tc.body), "test")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got tag %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("tag = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFetchLatestTag_UnsupportedHostErrors(t *testing.T) {
	id, err := plugins.ParseIdentity("example.com/owner/repo@v1.0.0")
	if err != nil {
		t.Skipf("identity parse: %v", err)
	}
	if _, err := fetchLatestTag(id); err == nil {
		t.Error("expected an error for an unsupported host, got nil (would silently no-op)")
	}
}

// TestPluginRemove_RemovesVersionsAndLock installs two fake version dirs + a
// lock entry, then `plugin remove` deletes both dirs and drops the lock entry.
func TestPluginRemove_RemovesVersionsAndLock(t *testing.T) {
	// Route StateDir at a temp dir via XDG so AllPluginDirs()/StateDir resolve
	// there; mirror how other cmd/stado tests stage config state.
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "cfg"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	// Run outside any project so pluginLockPath uses the global StateDir.
	t.Chdir(tmp)

	cfg := mustLoadConfigForRemoveTest(t)
	pluginsDir := filepath.Join(cfg.StateDir(), "plugins")
	for _, v := range []string{"acme-1.0.0", "acme-1.1.0"} {
		d := filepath.Join(pluginsDir, v)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "plugin.wasm"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A lock entry for the same plugin (identity repo == "acme").
	lockPath := pluginLockPath(cfg)
	_ = os.MkdirAll(filepath.Dir(lockPath), 0o755)
	lock := plugins.NewLock()
	lock.Add(plugins.LockEntry{Identity: "github.com/acmeorg/acme@v1.1.0", WASMSHA256: "deadbeef"})
	lock.Add(plugins.LockEntry{Identity: "github.com/other/keepme@v2.0.0", WASMSHA256: "feedface"})
	if err := lock.Write(lockPath); err != nil {
		t.Fatal(err)
	}

	pluginRemoveCmd.SetOut(os.Stderr)
	if err := pluginRemoveCmd.RunE(pluginRemoveCmd, []string{"acme"}); err != nil {
		t.Fatalf("plugin remove: %v", err)
	}

	for _, v := range []string{"acme-1.0.0", "acme-1.1.0"} {
		if _, err := os.Stat(filepath.Join(pluginsDir, v)); !os.IsNotExist(err) {
			t.Errorf("%s still present after remove", v)
		}
	}
	got, err := plugins.ReadLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Get("github.com/acmeorg/acme@v1.1.0"); ok {
		t.Error("lock entry for removed plugin survived")
	}
	if _, ok := got.Get("github.com/other/keepme@v2.0.0"); !ok {
		t.Error("unrelated lock entry was dropped")
	}
}

func TestPluginRemove_RejectsUnsafeName(t *testing.T) {
	for _, bad := range []string{"..", "a/b", "x*", "../etc"} {
		if err := pluginRemoveCmd.RunE(pluginRemoveCmd, []string{bad}); err == nil {
			t.Errorf("remove %q should be rejected as unsafe", bad)
		}
	}
}

func TestPluginRemove_NotInstalledErrors(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "cfg"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	t.Chdir(tmp)
	pluginRemoveCmd.SetOut(os.Stderr)
	if err := pluginRemoveCmd.RunE(pluginRemoveCmd, []string{"nope"}); err == nil {
		t.Error("remove of a not-installed plugin should error")
	}
}

func mustLoadConfigForRemoveTest(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}
