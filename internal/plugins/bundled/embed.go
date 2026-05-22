package bundled

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

// IsEmbeddedModule reports whether name corresponds to an upstream-
// shipped wasm module compiled into the binary's embed.FS (i.e. a
// trusted built-in such as "auto-compact"). #027: userbundled uses
// this to refuse appended-bundle entries whose stripped bare name
// collides with a built-in — otherwise a self-signed bundle entry
// named stado-builtin-tool-auto-compact could override the embedded
// auto-compact.wasm and run with its hard-coded privileged manifest.
// Checks the embed.FS only (not the mutable registry), so it reports
// the immutable trusted set regardless of registration order.
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
