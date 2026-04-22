package runtime

import (
	"context"
	"os"
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
			"fs:read:.",
			"fs:write:/tmp/work",
			"net:api.github.com",
			"net:http_get",
			"exec:shallow_bash",
			"exec:search",
			"exec:ast_grep",
			"lsp:query",
			"net:deny",  // skipped — plugin-level "deny" isn't a useful allow entry
			"net:allow", // skipped — too permissive for plugins
			"malformed", // no colon → skipped
		},
	}
	h := NewHost(m, "/tmp", nil)
	if h.Manifest.Name != "demo" {
		t.Errorf("manifest name: %q", h.Manifest.Name)
	}
	if len(h.FSRead) != 3 || h.FSRead[0] != "/etc" || h.FSRead[1] != "/home/user/projects" || h.FSRead[2] != "/tmp" {
		t.Errorf("FSRead: %v", h.FSRead)
	}
	if len(h.FSWrite) != 1 || h.FSWrite[0] != "/tmp/work" {
		t.Errorf("FSWrite: %v", h.FSWrite)
	}
	if len(h.NetHost) != 1 || h.NetHost[0] != "api.github.com" {
		t.Errorf("NetHost: %v", h.NetHost)
	}
	if !h.NetHTTPGet {
		t.Error("NetHTTPGet should be enabled")
	}
	if !h.ExecBash || !h.ExecSearch || !h.ExecASTGrep {
		t.Errorf("exec caps not parsed: bash=%v search=%v ast=%v", h.ExecBash, h.ExecSearch, h.ExecASTGrep)
	}
	if !h.LSPQuery {
		t.Error("LSPQuery should be enabled")
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
		{"/home/user/projects", true},        // exact
		{"/home/user/projects/x.go", true},   // under tree
		{"/home/user/projects-other", false}, // prefix but not subtree
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

// TestRealPath_SymlinkEscape: a symlink inside an allowed directory
// pointing outside must resolve to the real target; the capability
// check then rejects it. This closes the fs sandbox escape where
// os.ReadFile follows symlinks before the prefix check.
func TestRealPath_SymlinkEscape(t *testing.T) {
	tmp := t.TempDir()
	allowed := filepath.Join(tmp, "allowed")
	forbidden := filepath.Join(tmp, "forbidden")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(forbidden, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a file outside the allowed tree.
	secret := filepath.Join(forbidden, "secret.txt")
	if err := os.WriteFile(secret, []byte("top secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create a symlink inside allowed pointing outside.
	link := filepath.Join(allowed, "link_to_secret")
	if err := os.Symlink(secret, link); err != nil {
		t.Fatal(err)
	}

	// realPath must resolve to the actual target, not stay inside allowed.
	resolved, err := realPath("", link)
	if err != nil {
		t.Fatalf("realPath failed: %v", err)
	}
	if resolved != secret {
		t.Errorf("realPath resolved to %q, want %q", resolved, secret)
	}

	// After resolution, the capability check rejects it.
	h := NewHost(plugins.Manifest{Name: "test"}, "", nil)
	h.FSRead = []string{allowed}
	if h.allowRead(resolved) {
		t.Error("resolved symlink target outside allowed should be denied")
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
