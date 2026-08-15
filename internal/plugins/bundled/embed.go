package bundled

import (
	"embed"
	"fmt"
)

// wasmFS contains embedded wasm for bundled tool wrappers plus bundled
// background plugins that ship as part of the core binary.
//
// The wasm are built from source (EP-0042 Part B), not committed —
// `make wasm`, the goreleaser before-hook, or `go generate ./...` produce
// them. A plain `go build`/`go install` from a tree without them fails the
// embed; build via `git clone && make` (go install is not supported for this
// reason). See docs/eps/0042-binaries-out-of-source-tree.md.
//
//go:generate bash ../../../plugins/bundled/build.sh
//go:embed wasm/*.wasm
var wasmFS embed.FS

const (
	ManifestNamePrefix = "stado-builtin-tool"
	Author             = "stado"
)

func Wasm(toolName string) ([]byte, error) {
	return wasmFS.ReadFile("wasm/" + toolName + ".wasm")
}

// IsEmbeddedModule reports whether name corresponds to an upstream-
// shipped wasm module compiled into the binary's immutable embed.FS.
func IsEmbeddedModule(name string) bool {
	if name == "" {
		return false
	}
	f, err := wasmFS.Open("wasm/" + name + ".wasm")
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func MustWasm(toolName string) []byte {
	data, err := Wasm(toolName)
	if err != nil {
		panic(fmt.Sprintf("bundled: missing wasm for %s: %v", toolName, err))
	}
	return data
}
