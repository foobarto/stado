// Default stub for the bundled ast-grep blob. Release builds replace
// this file with one that `//go:embed`s the actual binary; dev builds
// keep the empty bytes and fall back to PATH.
//
// See internal/tools/rg/bundled_stub.go for the full pattern rationale.

package astgrep

import "runtime"

var bundledBytes []byte
var bundledSHA256 string

func isWindows() bool { return runtime.GOOS == "windows" }
