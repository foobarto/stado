//go:build !stado_embed_binaries

// Default stub for the bundled ripgrep blob. Populated for a specific
// GOOS/GOARCH by a release build that ran `go run hack/fetch-binaries.go`,
// which writes a matching `bundled_<goos>_<goarch>.go` tagged
// `//go:build stado_embed_binaries && <goos> && <goarch>` that declares
// the same two vars with `//go:embed` directives. Release builds pass
// `-tags stado_embed_binaries`; dev builds omit it and compile this
// stub instead (empty bytes → PATH fallback).

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
