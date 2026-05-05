//go:build wasip1

// web — bundled stado plugin for HTTP fetching and search.
// EP-0038 §C.
//
// Tools:
//   web_fetch  — HTTP GET/POST, returns markdown-converted body
//
// Capabilities:
//   net:http_request[:<host>]
package main

import (
	"github.com/foobarto/stado/internal/bundledplugins/sdk"
)

func main() {}

//go:wasmimport stado stado_http_get
func stadoHTTPGet(argsPtr, argsLen, resPtr, resCap uint32) int32

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 { return sdk.Alloc(size) }

//go:wasmexport stado_free
func stadoFree(ptr int32, size int32) { sdk.Free(ptr, size) }

// stado_tool_fetch — web.fetch tool. Delegates to the existing webfetch
// native host import (stado_http_get).
//
//go:wasmexport stado_tool_fetch
func stadoToolFetch(argsPtr, argsLen, resPtr, resCap int32) int32 {
	return stadoHTTPGet(uint32(argsPtr), uint32(argsLen), uint32(resPtr), uint32(resCap))
}
