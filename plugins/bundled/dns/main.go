//go:build wasip1

// dns — bundled stado plugin for DNS lookups.
// EP-0038 §C.
//
// Tools:
//   dns_resolve — A/AAAA/TXT/MX/NS/PTR record lookup
//
// Capabilities:
//   dns:resolve[:<glob>]
package main

import "github.com/foobarto/stado/internal/bundledplugins/sdk"

func main() {}

//go:wasmimport stado stado_dns_resolve
func stadoDNSResolve(reqPtr, reqLen, resPtr, resCap uint32) int32

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 { return sdk.Alloc(size) }

//go:wasmexport stado_free
func stadoFree(ptr int32, size int32) { sdk.Free(ptr, size) }

//go:wasmexport stado_tool_resolve
func stadoToolResolve(argsPtr, argsLen, resPtr, resCap int32) int32 {
	return stadoDNSResolve(uint32(argsPtr), uint32(argsLen), uint32(resPtr), uint32(resCap))
}
