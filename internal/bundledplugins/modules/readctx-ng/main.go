//go:build wasip1

// Package main is the stado `readctx` bundled plugin — readctx.read.
// Delegates to the existing native host import for backward compat. EP-0038 §C.
package main

import "github.com/foobarto/stado/internal/bundledplugins/sdk"

func main() {}

//go:wasmimport stado stado_fs_tool_read_with_context
func stadoFSToolReadWithContext(argsPtr, argsLen, resPtr, resCap uint32) int32

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 { return sdk.Alloc(size) }

//go:wasmexport stado_free
func stadoFree(ptr int32, size int32) { sdk.Free(ptr, size) }

//go:wasmexport stado_tool_read
func stadoToolRead(argsPtr, argsLen, resPtr, resCap int32) int32 {
	return stadoFSToolReadWithContext(uint32(argsPtr), uint32(argsLen), uint32(resPtr), uint32(resCap))
}
