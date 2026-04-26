// Package binext is a shared helper for tools that bundle a third-party
// binary (ripgrep, ast-grep). It handles the first-use extract:
//
//  1. If no bundled bytes exist, return ErrNotBundled so the caller
//     can fall back to a PATH lookup.
//  2. Otherwise extract the bytes to $XDG_CACHE_HOME/stado/bin/<name>-<sha>,
//     verify the sha256 against the declared digest, chmod +x, and
//     return the absolute path.
//
// Second-and-later invocations short-circuit on the existing file when
// its sha256 matches — no re-extraction, no re-verify.
package binext

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// ErrNotBundled is returned by Extract when the bundled byte slice is
// empty — i.e. this build didn't ship a bundled binary for the current
// OS/arch. Callers typically log + fall back to `exec.LookPath(name)`.
var ErrNotBundled = errors.New("binext: no bundled binary for this build")

// Extract writes bundled bytes to cacheDir/<name>-<sha12>[.exe] the
// first time it's called, verifies the digest, and returns the path.
// Subsequent calls with the same (name, bundled, digest) re-use the
// cached file without rewriting.
//
// cacheDir is typically $XDG_CACHE_HOME/stado/bin. name is
// e.g. "rg"; expectedSHA is the hex sha256 of bundled; bundled is the
// raw bytes (go:embed'd by the caller).
//
// On Windows callers should pass `name = "rg.exe"`.
func Extract(cacheDir, name string, bundled []byte, expectedSHA string) (string, error) {
	if len(bundled) == 0 {
		return "", ErrNotBundled
	}
	if err := validateToolName(name); err != nil {
		return "", err
	}
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", fmt.Errorf("binext: cache dir: %w", err)
	}
	root, err := os.OpenRoot(cacheDir)
	if err != nil {
		return "", fmt.Errorf("binext: open cache dir: %w", err)
	}
	defer func() { _ = root.Close() }()

	actualSHA := hashBytes(bundled)
	if expectedSHA != "" && actualSHA != expectedSHA {
		return "", fmt.Errorf("binext: %s digest mismatch — bundled=%s expected=%s",
			name, actualSHA, expectedSHA)
	}

	// Path includes a 12-char sha prefix so a mismatch causes a
	// brand-new file rather than overwriting a divergent cached copy.
	suffix := actualSHA[:12]
	fileName := name + "-" + suffix
	path := filepath.Join(cacheDir, fileName)

	if ok, err := cacheHit(root, fileName, bundled); err != nil {
		return "", err
	} else if ok {
		return path, nil
	}

	// Write atomically: tmp + rename so a concurrent call doesn't see
	// a half-written file.
	tmpName := "." + fileName + "." + uuid.NewString() + ".tmp"
	tmp, err := root.OpenFile(tmpName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return "", fmt.Errorf("binext: create %s: %w", tmpName, err)
	}
	keepTmp := false
	defer func() {
		if !keepTmp {
			_ = root.Remove(tmpName)
		}
	}()
	n, err := tmp.Write(bundled)
	if err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("binext: write %s: %w", tmpName, err)
	}
	if n != len(bundled) {
		_ = tmp.Close()
		return "", fmt.Errorf("binext: write %s: %w", tmpName, io.ErrShortWrite)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("binext: sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("binext: close %s: %w", tmpName, err)
	}
	if err := root.Rename(tmpName, fileName); err != nil {
		return "", fmt.Errorf("binext: rename %s: %w", path, err)
	}
	keepTmp = true
	return path, nil
}

// cacheHit reports whether path already exists + its sha256 matches
// bundled. A different sha is NOT a hit (we'd rewrite).
func cacheHit(root *os.Root, name string, bundled []byte) (bool, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, nil
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("binext: cache entry is not a regular file: %s", name)
	}
	f, err := root.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return hex.EncodeToString(h.Sum(nil)) == hashBytes(bundled), nil
}

func validateToolName(name string) error {
	if strings.TrimSpace(name) == "" || strings.Contains(name, "\x00") || strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("binext: invalid tool name %q", name)
	}
	if filepath.Base(name) != name || !filepath.IsLocal(name) {
		return fmt.Errorf("binext: invalid tool name %q", name)
	}
	return nil
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
