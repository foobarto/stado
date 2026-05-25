package tool

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Codex 2026-05-25 deep-dive 3-way convergent: my agent + gemini +
// codex all flagged that the four CheckWritePath implementations
// (acpwrap.DefaultHost, acp.acpHost, daemon.daemonToolHost,
// subagent.ScopedWriteHost) walk path segments WITHOUT symlink
// resolution AND with case-SENSITIVE compare. This factored helper
// fixes both.
//
// Cases covered:
// 1. Plain .git/HEAD — must reject (baseline)
// 2. Symlink-into-.git — must reject (was bypass pre-fix)
// 3. Mixed-case .GIT/config — must reject on macOS/Windows (was
//    bypass on case-insensitive FS pre-fix); rejection is uniform
//    everywhere because EqualFold is platform-independent.
// 4. Plain source file — must pass through (baseline)
// 5. Path containing .gitignore (NOT .git) — must pass
// 6. Absolute path outside workdir resolving to .git — must reject

func TestDefaultGitWritePathGuard_RejectsPlainDotGit(t *testing.T) {
	workdir := t.TempDir()
	err := DefaultGitWritePathGuard(workdir, ".git/HEAD")
	if err == nil {
		t.Fatal("expected refusal for .git/HEAD; got nil")
	}
	if !errors.Is(err, ErrGitMetadataWrite) {
		t.Errorf("expected ErrGitMetadataWrite sentinel; got %v", err)
	}
}

// Critical regression: symlink-into-.git bypass. Pre-fix all 4 impls
// did lexical segment walk; `foo` as a symlink to `.git` looked like
// the legit segment "foo" and passed. After fix EvalSymlinks resolves
// before the segment check.
func TestDefaultGitWritePathGuard_RejectsSymlinkIntoGit(t *testing.T) {
	workdir := t.TempDir()
	gitDir := filepath.Join(workdir, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Create a symlink `foo` in workdir pointing AT the .git dir.
	if err := os.Symlink(gitDir, filepath.Join(workdir, "foo")); err != nil {
		t.Skipf("symlink unsupported on this platform: %v", err)
	}
	// fs.write foo/config — pre-fix: passes; post-fix: rejects.
	err := DefaultGitWritePathGuard(workdir, "foo/config")
	if err == nil {
		t.Fatal("expected refusal for symlink-into-.git path; got nil — symlink bypass")
	}
	if !errors.Is(err, ErrGitMetadataWrite) {
		t.Errorf("expected ErrGitMetadataWrite sentinel; got %v", err)
	}
}

// macOS HFS+/APFS and Windows NTFS treat .GIT and .git as the same
// directory. Operators on those platforms get a case-insensitive
// bypass pre-fix.
func TestDefaultGitWritePathGuard_RejectsMixedCaseDotGit(t *testing.T) {
	workdir := t.TempDir()
	for _, badPath := range []string{".GIT/HEAD", ".Git/HEAD", "src/.GIT/config"} {
		t.Run(badPath, func(t *testing.T) {
			err := DefaultGitWritePathGuard(workdir, badPath)
			if err == nil {
				t.Errorf("expected refusal for %q; got nil — case-insensitive bypass", badPath)
			}
			if !errors.Is(err, ErrGitMetadataWrite) {
				t.Errorf("expected ErrGitMetadataWrite sentinel; got %v", err)
			}
		})
	}
}

// Plain source paths must NOT be rejected.
func TestDefaultGitWritePathGuard_AllowsLegitimatePaths(t *testing.T) {
	workdir := t.TempDir()
	for _, okPath := range []string{
		"main.go",
		"src/main.go",
		".gitignore",        // .gitignore is a file, not a .git segment
		"src/.gitattributes", // .gitattributes likewise
		"docs/README.md",
		filepath.Join(workdir, "abs", "path.txt"),
	} {
		t.Run(okPath, func(t *testing.T) {
			if err := DefaultGitWritePathGuard(workdir, okPath); err != nil {
				t.Errorf("expected pass for %q; got refusal: %v", okPath, err)
			}
		})
	}
}

// `..` segments are normalized by filepath.Clean before the segment
// walk, so `src/../.git/HEAD` resolves to `.git/HEAD` and is
// rejected. (If Clean didn't normalize, lexical bypass.)
func TestDefaultGitWritePathGuard_RejectsDotDotTraversalIntoGit(t *testing.T) {
	workdir := t.TempDir()
	err := DefaultGitWritePathGuard(workdir, "src/../.git/HEAD")
	if err == nil {
		t.Fatal("expected refusal for `..` traversal into .git; got nil")
	}
}

// Empty workdir + relative path: the helper should still resolve via
// process cwd (filepath.Join + Clean) and apply the segment check.
// Hosts without a workdir anchor (rare but legal — e.g. early test
// stubs) still get the defense.
func TestDefaultGitWritePathGuard_EmptyWorkdir(t *testing.T) {
	// Use a relative .git path; no workdir means the join is no-op
	// but Clean still produces the segment.
	err := DefaultGitWritePathGuard("", ".git/HEAD")
	if err == nil {
		t.Fatal("expected refusal for .git/HEAD with empty workdir; got nil")
	}
}

// Skip-stable for runtime platforms where /tmp differs.
func TestDefaultGitWritePathGuard_AbsolutePathTargetingGit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("path-handling on Windows differs; skipped")
	}
	workdir := t.TempDir()
	abs := filepath.Join(workdir, ".git", "config")
	// Absolute path containing .git anywhere in the segments — reject.
	if err := DefaultGitWritePathGuard(workdir, abs); err == nil {
		t.Errorf("expected refusal for abs path containing .git segment; got nil")
	}
}

// Sanity: the symlink-best-effort fallback returns the input
// unchanged when the path can't be resolved at all.
func TestEvalSymlinksBestEffort_NoExistingPrefix(t *testing.T) {
	got := evalSymlinksBestEffort("/this/path/should/not/exist/anywhere/foo.txt")
	// At minimum the returned path should contain the trailing
	// components so the segment check still sees the literal
	// .git if present.
	if !strings.Contains(got, "foo.txt") {
		t.Errorf("evalSymlinksBestEffort dropped the path tail; got %q", got)
	}
}
