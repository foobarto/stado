package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
			"memory:propose",
			"memory:read",
			"memory:write",
			"cfg:state_dir",
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
	if !h.MemoryPropose || !h.MemoryRead || !h.MemoryWrite {
		t.Errorf("memory caps not parsed: propose=%v read=%v write=%v", h.MemoryPropose, h.MemoryRead, h.MemoryWrite)
	}
	if !h.CfgStateDir {
		t.Error("CfgStateDir should be enabled by `cfg:state_dir`")
	}
	if !h.NeedsMemoryBridge() {
		t.Error("NeedsMemoryBridge should be true when memory caps are declared")
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

func TestRealPathForWritePreservesFinalSymlink(t *testing.T) {
	allowed := t.TempDir()
	target := filepath.Join(allowed, "target.txt")
	if err := os.WriteFile(target, []byte("target"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(allowed, "link.txt")
	if err := os.Symlink("target.txt", link); err != nil {
		t.Skipf("symlink not supported in this environment: %v", err)
	}

	resolved, err := realPathForWrite("", link)
	if err != nil {
		t.Fatalf("realPathForWrite: %v", err)
	}
	if resolved != link {
		t.Fatalf("realPathForWrite = %q, want final symlink path %q", resolved, link)
	}
}

func TestReadAllowedFileRejectsSymlinkSwapEscape(t *testing.T) {
	tmp := t.TempDir()
	allowed := filepath.Join(tmp, "allowed")
	forbidden := filepath.Join(tmp, "forbidden")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(forbidden, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(forbidden, "secret.txt"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(forbidden, filepath.Join(allowed, "swapped")); err != nil {
		t.Skipf("symlink not supported in this environment: %v", err)
	}

	_, err := readAllowedFile(filepath.Join(allowed, "swapped", "secret.txt"), []string{allowed}, maxPluginRuntimeFSFileBytes)
	if err == nil {
		t.Fatal("readAllowedFile should reject symlink escape under allowed root")
	}
}

func TestReadAllowedFileRejectsOversizedFile(t *testing.T) {
	allowed := t.TempDir()
	path := filepath.Join(allowed, "large.bin")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, maxPluginRuntimeFSFileBytes+1); err != nil {
		t.Fatal(err)
	}

	_, err := readAllowedFile(path, []string{allowed}, maxPluginRuntimeFSFileBytes)
	if err == nil {
		t.Fatal("readAllowedFile should reject oversized files")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want size limit", err)
	}
}

func TestReadAllowedFileRejectsBufferLimit(t *testing.T) {
	allowed := t.TempDir()
	path := filepath.Join(allowed, "small.txt")
	if err := os.WriteFile(path, []byte("ab"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := readAllowedFile(path, []string{allowed}, 1)
	if err == nil {
		t.Fatal("readAllowedFile should reject reads larger than the caller buffer limit")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want size limit", err)
	}
}

func TestWriteAllowedFileRejectsSymlinkSwapEscape(t *testing.T) {
	tmp := t.TempDir()
	allowed := filepath.Join(tmp, "allowed")
	forbidden := filepath.Join(tmp, "forbidden")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(forbidden, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(forbidden, filepath.Join(allowed, "swapped")); err != nil {
		t.Skipf("symlink not supported in this environment: %v", err)
	}

	err := writeAllowedFile(filepath.Join(allowed, "swapped", "out.txt"), []string{allowed}, []byte("pwned"), 0o644)
	if err == nil {
		t.Fatal("writeAllowedFile should reject symlink escape under allowed root")
	}
	if _, statErr := os.Stat(filepath.Join(forbidden, "out.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("outside write occurred, stat err = %v", statErr)
	}
}

func TestWriteAllowedFileRejectsInAllowedFinalSymlink(t *testing.T) {
	allowed := t.TempDir()
	target := filepath.Join(allowed, "target.txt")
	if err := os.WriteFile(target, []byte("target"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target.txt", filepath.Join(allowed, "link.txt")); err != nil {
		t.Skipf("symlink not supported in this environment: %v", err)
	}

	err := writeAllowedFile(filepath.Join(allowed, "link.txt"), []string{allowed}, []byte("pwned"), 0o644)
	if err == nil {
		t.Fatal("writeAllowedFile should reject final symlink under allowed root")
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "target" {
		t.Fatalf("symlink target modified: %q", data)
	}
}

func TestWriteAllowedFileRejectsOversizedData(t *testing.T) {
	allowed := t.TempDir()
	path := filepath.Join(allowed, "large.bin")
	data := make([]byte, maxPluginRuntimeFSFileBytes+1)

	err := writeAllowedFile(path, []string{allowed}, data, 0o644)
	if err == nil {
		t.Fatal("writeAllowedFile should reject oversized payloads")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("error = %v, want size limit", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("oversized write created file, stat err = %v", statErr)
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
