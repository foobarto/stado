//go:build wasip1

// web — bundled stado plugin for HTTP fetching.
//
// Tools:
//   web__fetch (display: web.fetch) — HTTP GET, returns response body
//
// Capabilities:
//   net:http_request[:<host>]
//
// EP-no-internal-tools Step 2: rewritten to use the stado_http_request
// primitive (was a thin shim over the deleted stado_http_get delegate).
// Same shape as the webfetch plugin; merged into a single module would
// be cleaner but webfetch and web are registered separately today.
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"unsafe"

	"github.com/foobarto/stado/internal/bundledplugins/sdk"
)

func main() {}

//go:wasmimport stado stado_http_request
func stadoHTTPRequest(argsPtr, argsLen, resPtr, resCap uint32) int32

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 { return sdk.Alloc(size) }

//go:wasmexport stado_free
func stadoFree(ptr int32, size int32) { sdk.Free(ptr, size) }

type fetchArgs struct {
	URL string `json:"url"`
}

type httpRequestArgs struct {
	Method string `json:"method"`
	URL    string `json:"url"`
}

type httpResponse struct {
	Status        int               `json:"status"`
	Headers       map[string]string `json:"headers"`
	BodyB64       string            `json:"body_b64"`
	BodyTruncated bool              `json:"body_truncated"`
}

const maxResponseBodyDisplay = 64 * 1024

//go:wasmexport stado_tool_fetch
func stadoToolFetch(argsPtr, argsLen, resPtr, resCap int32) int32 {
	args := sdk.Bytes(argsPtr, argsLen)
	var p fetchArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return writeError(resPtr, resCap, "web.fetch: bad args: "+err.Error())
	}
	if p.URL == "" {
		return writeError(resPtr, resCap, "web.fetch: url required")
	}

	reqArgs, err := json.Marshal(httpRequestArgs{Method: "GET", URL: p.URL})
	if err != nil {
		return writeError(resPtr, resCap, "web.fetch: marshal: "+err.Error())
	}

	respBuf := make([]byte, 4*1024*1024+512)
	respPtr := uint32(uintptr(unsafe.Pointer(&respBuf[0])))
	n := stadoHTTPRequest(
		uint32(uintptr(unsafe.Pointer(&reqArgs[0]))),
		uint32(len(reqArgs)),
		respPtr,
		uint32(len(respBuf)),
	)
	if n < 0 {
		return writeError(resPtr, resCap, "web.fetch: stado_http_request returned -1")
	}
	resp := respBuf[:n]

	var r httpResponse
	if err := json.Unmarshal(resp, &r); err != nil {
		return writeError(resPtr, resCap, "web.fetch: "+string(resp))
	}
	if r.Status >= 400 {
		return writeError(resPtr, resCap, fmt.Sprintf("HTTP %d", r.Status))
	}
	body, err := base64.StdEncoding.DecodeString(r.BodyB64)
	if err != nil {
		return writeError(resPtr, resCap, "web.fetch: body decode: "+err.Error())
	}
	if len(body) > maxResponseBodyDisplay {
		body = append(body[:maxResponseBodyDisplay], []byte(fmt.Sprintf(
			"\n[truncated: response body exceeded %d display bytes]", maxResponseBodyDisplay))...)
	}
	if r.BodyTruncated {
		body = append(body, []byte("\n[upstream truncated: response exceeded 4 MiB]")...)
	}
	return sdk.Write(resPtr, body)
}

func writeError(resultPtr, resultCap int32, msg string) int32 {
	if int32(len(msg)) > resultCap {
		msg = msg[:resultCap]
	}
	return sdk.Write(resultPtr, []byte(msg))
}
