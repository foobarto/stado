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
	// For user-bundled plugins the wasm bytes are stored directly on
	// the registry entry. Consult the registry before falling through
	// to the embed.FS (which only contains upstream-shipped modules).
	registryMu.Lock()
	infos := buildList(registry)
	registryMu.Unlock()
	for _, info := range infos {
		if info.Name == toolName && info.WasmSource != nil {
			return info.WasmSource, nil
		}
	}
	return wasmFS.ReadFile("wasm/" + toolName + ".wasm")
}

func MustWasm(toolName string) []byte {
	data, err := Wasm(toolName)
	if err != nil {
		panic(fmt.Sprintf("bundledplugins: missing wasm for %s: %v", toolName, err))
	}
	return data
}
