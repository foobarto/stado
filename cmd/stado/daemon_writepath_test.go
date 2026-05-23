package main

import (
	"strings"
	"testing"
)

// Codex K P0 regression: pre-fix daemonToolHost did NOT implement
// pkg/tool.WritePathGuard, so an `fs.write` call routed through the
// stado daemon's host (any `stado tool run fs__write ...` or RPC
// fs.write from a client) bypassed the .git-write guard that PR #050
// + acpwrap had in place. Daemon clients are autonomous + untrusted
// in the same sense as ACP-wrapped agents — they must not be able to
// corrupt the worktree's git metadata via the daemon's auto-approve
// posture. After fix daemonToolHost.CheckWritePath enforces the same
// defense as the acp host (internal/acp/host.go) and acpwrap's
// DefaultHost. Test mirrors TestACPHost_CheckWritePath_RefusesGitMetadata.
func TestDaemonToolHost_CheckWritePath_RefusesGitMetadata(t *testing.T) {
	h := &daemonToolHost{workdir: "/work"}
	cases := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"absolute .git/HEAD", "/work/.git/HEAD", true},
		{"relative .git/config", ".git/config", true},
		{"nested .git in subdir", "src/.git/objects/foo", true},
		{"path resolved through ..", "src/../.git/HEAD", true},
		{"plain source file ok", "main.go", false},
		{"absolute non-git ok", "/work/src/main.go", false},
		{"file named .gitignore (no .git seg) ok", "src/.gitignore", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := h.CheckWritePath(c.path)
			if (err != nil) != c.wantErr {
				t.Errorf("CheckWritePath(%q) err = %v, wantErr = %v", c.path, err, c.wantErr)
			}
			if c.wantErr && err != nil && !strings.Contains(err.Error(), ".git") {
				t.Errorf("error should mention .git; got %q", err.Error())
			}
		})
	}
}
