// Default stub for the bundled ripgrep blob. Populated for a specific
// GOOS/GOARCH by a release build that ran `go run hack/fetch-binaries.go`
// + replaced this file with a `//go:embed bundled/rg-<os>-<arch>` one.
//
// The build-tag-less version shipped in source keeps everything working
// on dev machines: bundledBytes is empty → binext.ErrNotBundled →
// ripgrep.ResolveBinary falls back to PATH (today's behaviour).

package rg

import "runtime"

// bundledBytes is the raw ripgrep binary for the current platform, or
// nil when this build didn't include one.
var bundledBytes []byte

// bundledSHA256 is the expected hex sha256 of bundledBytes, used by
// binext.Extract to verify the blob on first use. Empty when there's
// no bundled binary.
var bundledSHA256 string

func isWindows() bool { return runtime.GOOS == "windows" }
