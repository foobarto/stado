package main

// `stado install` / `stado uninstall` — copy the stado binary into
// a user-writeable PATH directory (or remove it from one). Useful
// for first-run setup ("I built stado in ~/Dokumenty/stado, how do
// I put it on $PATH?") without forcing the user to remember the
// idiomatic XDG-bin location.
//
// Target-dir priority — first writeable + on-PATH wins:
//   1. $XDG_BIN_HOME    (proposed XDG spec)
//   2. $HOME/.local/bin (de facto XDG for executables)
//   3. $HOME/bin        (older Unix convention)
//
// On-PATH membership is checked against the live $PATH split. A
// directory that's writeable but NOT on $PATH is skipped with an
// informative warning so the operator knows to amend their shell
// config.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

const installedBinaryName = "stado"

var (
	installPrefixFlag string
	installForceFlag  bool
)

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "Copy the running stado binary into a user-writeable PATH directory",
	Long: "Locate a user-writeable directory on $PATH (preferring XDG-bin\n" +
		"conventions: $XDG_BIN_HOME, then ~/.local/bin, then ~/bin),\n" +
		"and copy the running stado binary there as `stado`. Idempotent:\n" +
		"if the target file already exists and is the same binary, no\n" +
		"copy happens.\n\n" +
		"Override the target with --prefix <dir> if you want a specific\n" +
		"location (e.g. /usr/local/bin, but you'll likely need sudo for\n" +
		"system dirs — `stado install` doesn't try to escalate).",
	RunE: runInstall,
}

var uninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Remove an installed `stado` binary from user-writeable PATH directories",
	Long: "Walk the same XDG-bin candidate directories `stado install`\n" +
		"would have used, and remove any `stado` binary found there.\n" +
		"Doesn't touch the running binary itself if that's a different\n" +
		"path (e.g. you ran `./stado uninstall` from a build dir).",
	RunE: runUninstall,
}

func init() {
	installCmd.Flags().StringVar(&installPrefixFlag, "prefix", "",
		"Override the target directory (skip the XDG-bin auto-detection)")
	installCmd.Flags().BoolVar(&installForceFlag, "force", false,
		"Overwrite an existing different-content stado at the target")
	rootCmd.AddCommand(installCmd)
	rootCmd.AddCommand(uninstallCmd)
}

// xdgBinCandidates returns the user-writeable bin directories, in
// priority order. Empty entries (when env vars are unset) are
// skipped so the slice contains only resolvable paths. Callers
// further filter by writeability and on-PATH membership.
func xdgBinCandidates() []string {
	var out []string
	if v := os.Getenv("XDG_BIN_HOME"); v != "" {
		out = append(out, v)
	}
	if home, err := os.UserHomeDir(); err == nil {
		out = append(out, filepath.Join(home, ".local", "bin"))
		out = append(out, filepath.Join(home, "bin"))
	}
	return out
}

// pathSet returns the set of directories on $PATH, normalised to
// absolute paths so later membership checks are robust against
// trailing slashes / relative entries.
func pathSet() map[string]struct{} {
	out := map[string]struct{}{}
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		out[filepath.Clean(abs)] = struct{}{}
	}
	return out
}

// pickInstallTarget selects a destination directory. With --prefix,
// uses that verbatim (after creating it if missing). Otherwise
// walks xdgBinCandidates and picks the first one that exists OR
// can be created and is on $PATH. A directory not on $PATH is
// returned anyway with a stderr advisory — the operator can amend
// their shell config.
func pickInstallTarget() (dir string, onPath bool, err error) {
	if installPrefixFlag != "" {
		abs, e := filepath.Abs(installPrefixFlag)
		if e != nil {
			return "", false, fmt.Errorf("--prefix: %w", e)
		}
		if e := os.MkdirAll(abs, 0o755); e != nil {
			return "", false, fmt.Errorf("--prefix: mkdir %s: %w", abs, e)
		}
		_, on := pathSet()[filepath.Clean(abs)]
		return abs, on, nil
	}

	candidates := xdgBinCandidates()
	pathDirs := pathSet()

	// First pass: existing + on-PATH.
	for _, c := range candidates {
		abs, e := filepath.Abs(c)
		if e != nil {
			continue
		}
		if info, e := os.Stat(abs); e == nil && info.IsDir() {
			if _, on := pathDirs[filepath.Clean(abs)]; on {
				return abs, true, nil
			}
		}
	}
	// Second pass: existing, not on PATH (still valid; advisory).
	for _, c := range candidates {
		abs, e := filepath.Abs(c)
		if e != nil {
			continue
		}
		if info, e := os.Stat(abs); e == nil && info.IsDir() {
			return abs, false, nil
		}
	}
	// Third pass: create the highest-priority candidate.
	if len(candidates) > 0 {
		abs, _ := filepath.Abs(candidates[0])
		if e := os.MkdirAll(abs, 0o755); e != nil {
			return "", false, fmt.Errorf("create %s: %w", abs, e)
		}
		_, on := pathDirs[filepath.Clean(abs)]
		return abs, on, nil
	}
	return "", false, errors.New("no XDG-bin candidates available (HOME unset?); pass --prefix <dir>")
}

func runInstall(cmd *cobra.Command, _ []string) error {
	src, err := os.Executable()
	if err != nil {
		return fmt.Errorf("install: locate self: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(src); err == nil {
		src = resolved
	}

	dir, onPath, err := pickInstallTarget()
	if err != nil {
		return err
	}
	dst := filepath.Join(dir, installedBinaryName)

	if src == dst {
		fmt.Fprintf(os.Stdout, "stado install: already running from %s — nothing to do\n", dst)
		return nil
	}

	// Idempotency: if dst exists and content matches src, no-op.
	srcHash, err := hashFile(src)
	if err != nil {
		return fmt.Errorf("install: hash source: %w", err)
	}
	if dstInfo, err := os.Stat(dst); err == nil && !dstInfo.IsDir() {
		dstHash, hashErr := hashFile(dst)
		if hashErr == nil && dstHash == srcHash {
			fmt.Fprintf(os.Stdout, "stado install: %s already up-to-date (sha256 matches)\n", dst)
			emitPathAdvisory(dir, onPath)
			return nil
		}
		if !installForceFlag {
			return fmt.Errorf("install: %s exists and differs from current binary; pass --force to overwrite", dst)
		}
	}

	if err := copyFileMode(src, dst, 0o755); err != nil {
		return fmt.Errorf("install: copy %s → %s: %w", src, dst, err)
	}
	fmt.Fprintf(os.Stdout, "stado install: copied %s → %s\n", src, dst)
	emitPathAdvisory(dir, onPath)
	return nil
}

// emitPathAdvisory prints a stderr note when the install target
// isn't on $PATH so the operator knows their shell won't pick up
// the new binary without a config change.
func emitPathAdvisory(dir string, onPath bool) {
	if onPath {
		return
	}
	fmt.Fprintf(os.Stderr,
		"stado install: WARNING — %s is not on $PATH. Add it to your shell config:\n"+
			"  export PATH=\"%s:$PATH\"\n",
		dir, dir)
}

func runUninstall(cmd *cobra.Command, _ []string) error {
	self, _ := os.Executable()
	if self != "" {
		if resolved, err := filepath.EvalSymlinks(self); err == nil {
			self = resolved
		}
	}

	candidates := xdgBinCandidates()
	if installPrefixFlag != "" {
		// Allow --prefix on uninstall too for symmetry.
		if abs, err := filepath.Abs(installPrefixFlag); err == nil {
			candidates = []string{abs}
		}
	}

	removed := 0
	for _, c := range candidates {
		abs, err := filepath.Abs(c)
		if err != nil {
			continue
		}
		dst := filepath.Join(abs, installedBinaryName)
		info, err := os.Stat(dst)
		if err != nil {
			continue
		}
		if info.IsDir() {
			continue // shouldn't happen but be defensive
		}
		// Don't remove the binary we're currently running from —
		// that's almost certainly not what the user meant. Tell
		// them what to do instead.
		if dst == self {
			fmt.Fprintf(os.Stderr,
				"stado uninstall: skipping %s — that's the binary we're running from. Run `stado uninstall` from a different copy or delete it manually.\n",
				dst)
			continue
		}
		if err := os.Remove(dst); err != nil {
			fmt.Fprintf(os.Stderr, "stado uninstall: could not remove %s: %v\n", dst, err)
			continue
		}
		fmt.Fprintf(os.Stdout, "stado uninstall: removed %s\n", dst)
		removed++
	}
	if removed == 0 {
		fmt.Fprintln(os.Stdout, "stado uninstall: nothing to do (no installed copy found in XDG-bin candidates)")
	}
	return nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied path
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyFileMode copies src to dst with the given mode. Writes to a
// tmp file in the same directory then renames into place — atomic
// against partial copies, and on Linux/macOS preserves the
// running-binary-can't-be-overwritten property: if the user is
// installing over their own running stado, the rename swaps the
// inode rather than truncating the in-use file.
func copyFileMode(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src) //nolint:gosec // operator-supplied
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".stado-install-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, dst); err != nil {
		return fmt.Errorf("rename %s → %s: %w", tmpPath, dst, err)
	}
	cleanup = false
	return nil
}

// _ silences unused-import detection on uncommon paths;
// strings is used elsewhere in cmd/stado for general purposes.
var _ = strings.TrimSpace
