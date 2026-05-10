//go:build wasip1

package main

import "github.com/foobarto/stado/internal/plugins/bundled/sdk"

func main() {}

//go:wasmimport stado stado_lsp_find_definition
func stadoLSPFindDefinition(argsPtr, argsLen, resultPtr, resultCap uint32) int32

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 { return sdk.Alloc(size) }

//go:wasmexport stado_free
func stadoFree(ptr int32, size int32) { sdk.Free(ptr, size) }

// Wire-form lsp__definition → exported as stado_tool_definition.
//
//go:wasmexport stado_tool_definition
func stadoToolDefinition(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	return stadoLSPFindDefinition(uint32(argsPtr), uint32(argsLen), uint32(resultPtr), uint32(resultCap))
}
