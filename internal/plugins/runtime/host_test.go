package runtime

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

func TestNewHost_ParsesCapabilities(t *testing.T) {
	m := plugins.Manifest{
		Name: "demo",
		Capabilities: []string{
			"fs:read:/etc",
			"fs:read:/home/user/projects",
			"fs:write:/tmp/work",
			"net:api.github.com",
			"net:deny",   // skipped — plugin-level "deny" isn't a useful allow entry
			"net:allow",  // skipped — too permissive for plugins
			"malformed",  // no colon → skipped
		},
	}
	h := NewHost(m, "/tmp", nil)
	if h.Manifest.Name != "demo" {
		t.Errorf("manifest name: %q", h.Manifest.Name)
	}
	if len(h.FSRead) != 2 || h.FSRead[0] != "/etc" || h.FSRead[1] != "/home/user/projects" {
		t.Errorf("FSRead: %v", h.FSRead)
	}
	if len(h.FSWrite) != 1 || h.FSWrite[0] != "/tmp/work" {
		t.Errorf("FSWrite: %v", h.FSWrite)
	}
	if len(h.NetHost) != 1 || h.NetHost[0] != "api.github.com" {
		t.Errorf("NetHost: %v", h.NetHost)
	}
	if h.Logger == nil {
		t.Error("Logger should default to slog.Default")
	}
}

func TestPathAllowed_PrefixAndExact(t *testing.T) {
	allow := []string{"/home/user/projects", "/tmp"}
	cases := []struct {
		path string
		want bool
	}{
		{"/home/user/projects", true},           // exact
		{"/home/user/projects/x.go", true},      // under tree
		{"/home/user/projects-other", false},    // prefix but not subtree
		{"/tmp", true},
		{"/tmp/work/file.txt", true},
		{"/etc/passwd", false},
		{"/", false},
	}
	for _, c := range cases {
		if got := pathAllowed(c.path, allow); got != c.want {
			t.Errorf("pathAllowed(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestPathAllowed_EmptyAllowList(t *testing.T) {
	if pathAllowed("/anything", nil) {
		t.Error("empty allow-list should deny everything")
	}
}

func TestResolveAbs_RelativeJoinedWithWorkdir(t *testing.T) {
	got := resolveAbs("/home/user", "./subdir/file.txt")
	want := filepath.Clean("/home/user/subdir/file.txt")
	if got != want {
		t.Errorf("resolveAbs = %q, want %q", got, want)
	}
}

func TestResolveAbs_AbsoluteKept(t *testing.T) {
	got := resolveAbs("/home/user", "/etc/passwd")
	if got != "/etc/passwd" {
		t.Errorf("absolute path mangled: %q", got)
	}
}

func TestResolveAbs_CleansTraversal(t *testing.T) {
	// Canonical traversal-bypass attempt: a plugin with "fs:read:/allowed"
	// tries to read "/allowed/../etc/passwd". filepath.Clean normalises
	// it to "/etc/passwd", which pathAllowed then rejects against
	// ["/allowed"]. Without Clean the raw prefix match could be
	// tricked.
	got := resolveAbs("", "/allowed/../etc/passwd")
	if got != "/etc/passwd" {
		t.Errorf("traversal not cleaned: got %q want /etc/passwd", got)
	}
	if pathAllowed(got, []string{"/allowed"}) {
		t.Error("cleaned path should NOT be allow-listed")
	}
}

// TestInstallHostImports_Smoke: registering the stado module succeeds
// even without a plugin wasm to consume it. Catches signature drift
// in wazero's HostModuleBuilder API.
func TestInstallHostImports_Smoke(t *testing.T) {
	ctx := context.Background()
	r, err := New(ctx)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer func() { _ = r.Close(ctx) }()

	host := NewHost(plugins.Manifest{Name: "smoke"}, "/tmp", nil)
	if err := InstallHostImports(ctx, r, host); err != nil {
		t.Fatalf("InstallHostImports: %v", err)
	}
}

// TestInstallHostImports_TwiceFails covers the runtime invariant that
// two modules can't export the same name. Real plugins get an isolated
// Runtime per plugin (see the 7.1c caller) so this test also guards
// that architectural choice.
func TestInstallHostImports_TwiceFails(t *testing.T) {
	ctx := context.Background()
	r, _ := New(ctx)
	defer func() { _ = r.Close(ctx) }()

	host := NewHost(plugins.Manifest{Name: "a"}, "/tmp", nil)
	if err := InstallHostImports(ctx, r, host); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if err := InstallHostImports(ctx, r, host); err == nil {
		t.Fatal("second install should fail with name collision")
	}
}
