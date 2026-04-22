package builtinplugins

import (
	"embed"
	"fmt"
)

// wasmFS contains one embedded wasm module per bundled built-in tool.
// Each module is a thin wrapper around exactly one public host import.
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
		panic(fmt.Sprintf("builtinplugins: missing wasm for %s: %v", toolName, err))
	}
	return data
}
