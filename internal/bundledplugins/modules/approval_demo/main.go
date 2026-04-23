//go:build wasip1

package main

import (
	"encoding/json"
	"strings"

	"github.com/foobarto/stado/internal/bundledplugins/sdk"
)

func main() {}

//go:wasmimport stado stado_ui_approve
func stadoUIApprove(titlePtr, titleLen, bodyPtr, bodyLen uint32) int32

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 { return sdk.Alloc(size) }

//go:wasmexport stado_free
func stadoFree(ptr int32, size int32) { sdk.Free(ptr, size) }

//go:wasmexport stado_tool_approval_demo
func stadoToolApprovalDemo(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	var args struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if raw := sdk.Bytes(argsPtr, argsLen); len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return writePayload(resultPtr, resultCap, "invalid approval_demo args: "+err.Error(), true)
		}
	}
	if strings.TrimSpace(args.Title) == "" {
		args.Title = "Plugin approval demo"
	}
	if strings.TrimSpace(args.Body) == "" {
		args.Body = "Allow the demo plugin to continue?"
	}

	titleBytes := []byte(args.Title)
	bodyBytes := []byte(args.Body)
	titlePtr := sdk.Alloc(int32(len(titleBytes)))
	defer sdk.Free(titlePtr, int32(len(titleBytes)))
	bodyPtr := sdk.Alloc(int32(len(bodyBytes)))
	defer sdk.Free(bodyPtr, int32(len(bodyBytes)))
	sdk.Write(titlePtr, titleBytes)
	sdk.Write(bodyPtr, bodyBytes)

	switch stadoUIApprove(uint32(titlePtr), uint32(len(titleBytes)), uint32(bodyPtr), uint32(len(bodyBytes))) {
	case 1:
		return writePayload(resultPtr, resultCap, "approved", false)
	case 0:
		return writePayload(resultPtr, resultCap, "denied", false)
	default:
		return writePayload(resultPtr, resultCap, "approval UI unavailable", true)
	}
}

func writePayload(resultPtr, resultCap int32, text string, isError bool) int32 {
	if resultCap <= 0 {
		if isError {
			return -1
		}
		return 0
	}
	payload := []byte(text)
	if int32(len(payload)) > resultCap {
		payload = payload[:resultCap]
	}
	written := sdk.Write(resultPtr, payload)
	if isError {
		return -written
	}
	return written
}
