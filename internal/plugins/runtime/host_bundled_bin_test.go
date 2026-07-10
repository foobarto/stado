package runtime

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAuthorizeBundledExecPathAddsOnlyExactPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rg")
	if err := os.WriteFile(path, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	host := &Host{ExecProc: true, ExecProcGlobs: []string{"rg"}}
	got, err := authorizeBundledExecPath(host, path)
	if err != nil {
		t.Fatal(err)
	}
	if !host.procAllowed(got) {
		t.Fatalf("trusted bundled path %q was not authorized: %v", got, host.ExecProcGlobs)
	}
	if host.procAllowed(filepath.Join(dir, "other", "rg")) {
		t.Fatal("bundled authorization widened to another path with the same basename")
	}
}
