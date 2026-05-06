package rg

// bundled-binary machinery for ripgrep.
//
// Release builds run `go run hack/fetch-binaries.go` before `go build`
// so the per-OS/arch blobs below are populated. Dev builds without
// that step keep the placeholder empty files — the extractor treats
// an empty bundled slice as "not bundled" and falls back to PATH.
//
// Per-platform embeds live in bundled_<goos>_<goarch>.go (build tags
// select the single blob relevant to the current target). The
// manifest.json sidecar pins the sha256 the extractor verifies
// against; embedded here as `bundledSHA256` via its matching per-
// platform file.

import (
	"os"
	"path/filepath"

	"github.com/foobarto/stado/internal/tools/binext"
)

// cacheDir returns $XDG_CACHE_HOME/stado/bin or ~/.cache/stado/bin.
func cacheDir() string {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, "stado", "bin")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "stado", "bin")
}

// bundledBinary returns the path to the extracted ripgrep binary for
// this GOOS/GOARCH, or ("", binext.ErrNotBundled) when this build has
// no blob for the current platform.
//
// bundledBytes and bundledSHA256 come from the platform-specific
// build-tagged file. Absent files (e.g. in dev builds) leave these as
// nil/empty so ErrNotBundled fires.
func bundledBinary() (string, error) {
	name := "rg"
	if isWindows() {
		name = "rg.exe"
	}
	return binext.Extract(cacheDir(), name, bundledBytes, bundledSHA256)
}

// BundledPath returns the filesystem path to the embedded ripgrep binary,
// extracting it to the cache dir on first call. Used by stado_bundled_bin.
func BundledPath() (string, error) { return bundledBinary() }
