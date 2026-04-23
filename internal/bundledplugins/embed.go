package bundledplugins

import (
	"embed"
	"fmt"
)

// wasmFS contains embedded wasm for bundled tool wrappers plus bundled
// background plugins that ship as part of the core binary.
//
//go:embed wasm/*.wasm
var wasmFS embed.FS

const (
	ManifestNamePrefix = "stado-builtin-tool"
	Author             = "stado"
)

func Wasm(toolName string) ([]byte, error) {
	return wasmFS.ReadFile("wasm/" + toolName + ".wasm")
}

func MustWasm(toolName string) []byte {
	data, err := Wasm(toolName)
	if err != nil {
		panic(fmt.Sprintf("bundledplugins: missing wasm for %s: %v", toolName, err))
	}
	return data
}
