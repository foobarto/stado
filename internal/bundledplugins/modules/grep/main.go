//go:build wasip1

package main

import "github.com/foobarto/stado/internal/bundledplugins/sdk"

func main() {}

//go:wasmimport stado stado_fs_tool_grep
func stadoFSToolGrep(argsPtr, argsLen, resultPtr, resultCap uint32) int32

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 { return sdk.Alloc(size) }

//go:wasmexport stado_free
func stadoFree(ptr int32, size int32) { sdk.Free(ptr, size) }

//go:wasmexport stado_tool_grep
func stadoToolGrep(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	return stadoFSToolGrep(uint32(argsPtr), uint32(argsLen), uint32(resultPtr), uint32(resultCap))
}
