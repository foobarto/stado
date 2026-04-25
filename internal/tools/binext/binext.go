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
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return "", fmt.Errorf("binext: cache dir: %w", err)
	}

	actualSHA := hashBytes(bundled)
	if expectedSHA != "" && actualSHA != expectedSHA {
		return "", fmt.Errorf("binext: %s digest mismatch — bundled=%s expected=%s",
			name, actualSHA, expectedSHA)
	}

	// Path includes a 12-char sha prefix so a mismatch causes a
	// brand-new file rather than overwriting a divergent cached copy.
	suffix := actualSHA[:12]
	path := filepath.Join(cacheDir, name+"-"+suffix)

	if ok, err := cacheHit(path, bundled); err != nil {
		return "", err
	} else if ok {
		return path, nil
	}

	// Write atomically: tmp + rename so a concurrent call doesn't see
	// a half-written file.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, bundled, 0o700); err != nil { // #nosec G306 -- cached tool extract must be executable.
		return "", fmt.Errorf("binext: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("binext: rename %s: %w", path, err)
	}
	return path, nil
}

// cacheHit reports whether path already exists + its sha256 matches
// bundled. A different sha is NOT a hit (we'd rewrite).
func cacheHit(path string, bundled []byte) (bool, error) {
	f, err := os.Open(path) // #nosec G304 -- cache path is derived from bundled tool name and digest.
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

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
