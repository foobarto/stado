package acpwrap

import (
	"testing"

	"github.com/foobarto/stado/pkg/tool"
)

// #050: DefaultHost must implement tool.WritePathGuard so the fs
// WriteTool/EditTool consult it before writing.
func TestDefaultHost_ImplementsWritePathGuard(t *testing.T) {
	var _ tool.WritePathGuard = DefaultHost{}
}

// #050: writes that resolve into a .git directory are refused, even
// when the host is rooted at the real checkout. This is the
// highest-impact write a prompt-injected wrapped agent can attempt.
func TestDefaultHost_CheckWritePath_BlocksGit(t *testing.T) {
	h := NewDefaultHost("/repo", nil)
	cases := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"relative .git config", ".git/config", true},
		{"relative .git hook", ".git/hooks/pre-commit", true},
		{"absolute .git path", "/repo/.git/config", true},
		{"traversal into .git", "subdir/../.git/config", true},
		{"normal source file", "internal/foo.go", false},
		{"relative file at root", "README.md", false},
		{"file merely named git", "gitignore-helper.go", false},
		{"dir named gitfoo", "gitfoo/x.txt", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := h.CheckWritePath(tc.path)
			if tc.wantErr && err == nil {
				t.Errorf("CheckWritePath(%q) = nil, want error", tc.path)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("CheckWritePath(%q) = %v, want nil", tc.path, err)
			}
		})
	}
}

// Guard must work with an empty workdir (relative paths can't be
// joined, but a literal .git segment is still caught).
func TestDefaultHost_CheckWritePath_EmptyWorkdir(t *testing.T) {
	h := NewDefaultHost("", nil)
	if err := h.CheckWritePath(".git/config"); err == nil {
		t.Error("expected .git write to be blocked even with empty workdir")
	}
	if err := h.CheckWritePath("file.txt"); err != nil {
		t.Errorf("normal file should be allowed with empty workdir, got %v", err)
	}
}
