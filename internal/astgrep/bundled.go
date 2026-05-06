package astgrep

// bundled-binary machinery for ast-grep. Mirrors internal/tools/rg's
// pattern — see that package for the full rationale.
//
// Release builds run `go run hack/fetch-binaries.go` before `go build`
// to populate bundledBytes. Dev builds leave it empty → binext returns
// ErrNotBundled → ResolveBinary falls back to PATH (today's behaviour).

import (
	"os"
	"path/filepath"

	"github.com/foobarto/stado/internal/tools/binext"
)

func cacheDir() string {
	if xdg := os.Getenv("XDG_CACHE_HOME"); xdg != "" {
		return filepath.Join(xdg, "stado", "bin")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache", "stado", "bin")
}

func bundledBinary() (string, error) {
	name := "ast-grep"
	if isWindows() {
		name = "ast-grep.exe"
	}
	return binext.Extract(cacheDir(), name, bundledBytes, bundledSHA256)
}

// BundledPath returns the filesystem path to the embedded ast-grep binary,
// extracting it to the cache dir on first call. Used by stado_bundled_bin.
func BundledPath() (string, error) { return bundledBinary() }
