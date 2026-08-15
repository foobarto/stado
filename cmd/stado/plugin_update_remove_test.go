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

func TestCommitIdentityIsNeverImplicitlyUpdatedToTag(t *testing.T) {
	id, err := plugins.ParseIdentity("github.com/acme/plugin@0123456789abcdef0123456789abcdef01234567")
	if err != nil {
		t.Fatal(err)
	}
	if allowsImplicitPluginUpdate(id) {
		t.Fatal("full commit pin unexpectedly eligible for implicit latest-tag update")
	}
}

func TestSelectLatestPackageReleaseDoesNotCrossMonorepoPackages(t *testing.T) {
	id, err := plugins.ParseIdentity("github.com/foobarto/stado-plugins/supervise@v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	releases := []packageRelease{
		{TagName: "browser/v9.0.0"},
		{TagName: "supervise/v9.0.0-rc.1", Prerelease: true},
		{TagName: "supervise/v8.0.0", Draft: true},
		{TagName: "supervise/v1.2.0"},
		{TagName: "supervise/v1.1.0"},
		{TagName: "v8.0.0", Assets: releaseAssets("browser-")},
	}
	got, err := selectLatestPackageRelease(id, releases)
	if err != nil {
		t.Fatal(err)
	}
	if got != "v1.2.0" {
		t.Fatalf("latest supervise release = %q, want v1.2.0", got)
	}
}

func TestSelectLatestPackageReleaseAcceptsExactPrefixedAssetSet(t *testing.T) {
	id, err := plugins.ParseIdentity("github.com/foobarto/stado-plugins/research/reader@v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	releases := []packageRelease{
		{TagName: "v1.4.0", Assets: releaseAssets("research-reader-")},
		{TagName: "v2.0.0", Assets: releaseAssets("research-writer-")},
	}
	got, err := selectLatestPackageRelease(id, releases)
	if err != nil {
		t.Fatal(err)
	}
	if got != "v1.4.0" {
		t.Fatalf("latest reader release = %q, want v1.4.0", got)
	}
}

func TestSelectLatestPackageReleaseRejectsAmbiguousFlatAssets(t *testing.T) {
	id, err := plugins.ParseIdentity("github.com/foobarto/stado-plugins/supervise@v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	release := packageRelease{TagName: "v2.0.0"}
	for _, name := range []string{"plugin.wasm", "plugin.manifest.json", "plugin.manifest.sig"} {
		release.Assets = append(release.Assets, struct {
			Name string `json:"name"`
		}{Name: name})
	}
	if _, err := selectLatestPackageRelease(id, []packageRelease{release}); err == nil {
		t.Fatal("flat monorepo assets unexpectedly selected for a subpackage")
	}
}

func releaseAssets(prefix string) []struct {
	Name string `json:"name"`
} {
	var assets []struct {
		Name string `json:"name"`
	}
	for _, suffix := range []string{"plugin.wasm", "plugin.manifest.json", "plugin.manifest.sig"} {
		assets = append(assets, struct {
			Name string `json:"name"`
		}{Name: prefix + suffix})
	}
	return assets
}

// TestPluginRemoveRevokesReceiptsAndRetainsContinuityHistory installs two fake
// version dirs, then verifies removal revokes live admission before deleting
// bytes while preserving immutable source-continuity history.
func TestPluginRemoveRevokesReceiptsAndRetainsContinuityHistory(t *testing.T) {
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
	source := filepath.Join(tmp, "acme-source")
	var installed []plugins.InstalledPackage
	for _, version := range []string{"1.0.0", "1.1.0"} {
		wasm := []byte("wasm-" + version)
		digest := sha256.Sum256(wasm)
		mf := plugins.Manifest{Name: "acme", Version: version, WASMSHA256: hex.EncodeToString(digest[:])}
		installed = append(installed, writeTestLocalInstalledPackage(t, pluginsDir, source, mf, "sig", wasm))
	}
	for _, pkg := range installed {
		if err := plugins.WriteInstallReceipt(cfg.StateDir(), pluginsDir, pkg.Record); err != nil {
			t.Fatal(err)
		}
	}
	// A lock entry for the same plugin (identity repo == "acme").
	lockPath := pluginLockPath(cfg, false)
	_ = os.MkdirAll(filepath.Dir(lockPath), 0o755)
	lock := plugins.NewLock()
	lock.Add(plugins.LockEntry{StoreKey: installed[0].Record.StoreKey, Identity: "github.com/acmeorg/acme@v1.1.0", WASMSHA256: "deadbeef"})
	lock.Add(plugins.LockEntry{StoreKey: "remote-" + strings.Repeat("f", 64), Identity: "github.com/other/keepme@v2.0.0", WASMSHA256: "feedface"})
	if err := lock.Write(lockPath); err != nil {
		t.Fatal(err)
	}

	pluginRemoveCmd.SetOut(os.Stderr)
	if err := pluginRemoveCmd.RunE(pluginRemoveCmd, []string{installed[0].Record.StoreKey}); err != nil {
		t.Fatalf("plugin remove: %v", err)
	}

	for _, pkg := range installed {
		if _, err := os.Stat(pkg.Dir); !os.IsNotExist(err) {
			t.Errorf("%s still present after remove", pkg.Dir)
		}
		if err := plugins.CheckInstallReceipt(cfg.StateDir(), pluginsDir, pkg.Record); err == nil {
			t.Errorf("receipt for %s survived remove", pkg.Record.StoreKey)
		}
	}
	got, err := plugins.ReadLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Get("github.com/acmeorg/acme@v1.1.0"); !ok {
		t.Error("immutable continuity row was deleted")
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
