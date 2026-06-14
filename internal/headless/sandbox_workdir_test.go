package headless

import "testing"

// Codex #5: the headless default sandbox policy must be pinned to the sidecar
// worktree (which `land` later applies), never the user's real checkout — else
// a headless shell.exec writes straight into the operator's repo, bypassing the
// sidecar/audit isolation. sandboxPolicyWorkdir encodes that precedence.
func TestSandboxPolicyWorkdir(t *testing.T) {
	cases := []struct {
		name     string
		realCWD  string
		sidecar  string
		wantPath string
	}{
		{"sidecar wins over real checkout", "/home/u/repo", "/home/u/.stado/wt/abc", "/home/u/.stado/wt/abc"},
		{"no sidecar falls back to workdir", "/home/u/repo", "", "/home/u/repo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sandboxPolicyWorkdir(tc.realCWD, tc.sidecar); got != tc.wantPath {
				t.Errorf("sandboxPolicyWorkdir(%q, %q) = %q, want %q", tc.realCWD, tc.sidecar, got, tc.wantPath)
			}
		})
	}
}
