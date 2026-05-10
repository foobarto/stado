package main

import (
	"encoding/json"
	"strings"
	"sync"
	"unsafe"
)

func main() {}

//go:wasmimport stado stado_ui_approve
func stadoUIApprove(titlePtr, titleLen, bodyPtr, bodyLen uint32) int32

var pinned sync.Map

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 {
	if size <= 0 {
		return 0
	}
	buf := make([]byte, size)
	ptr := uintptr(unsafe.Pointer(&buf[0]))
	pinned.Store(ptr, buf)
	return int32(ptr)
}

//go:wasmexport stado_free
func stadoFree(ptr int32, size int32) {
	pinned.Delete(uintptr(ptr))
	_ = size
}

//go:wasmexport stado_tool_approval_demo
func stadoToolApprovalDemo(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	var args struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if raw := wasmBytes(argsPtr, argsLen); len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return writeError(resultPtr, resultCap, "invalid approval_demo args: "+err.Error())
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
	switch stadoUIApprove(bytesPtr(titleBytes), uint32(len(titleBytes)), bytesPtr(bodyBytes), uint32(len(bodyBytes))) {
	case 1:
		return writeResult(resultPtr, resultCap, "approved")
	case 0:
		return writeResult(resultPtr, resultCap, "denied")
	default:
		return writeError(resultPtr, resultCap, "approval UI unavailable")
	}
}

func wasmBytes(ptr, size int32) []byte {
	if ptr == 0 || size <= 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), int(size))
}

func bytesPtr(b []byte) uint32 {
	if len(b) == 0 {
		return 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0])))
}

func writeResult(resultPtr, resultCap int32, msg string) int32 {
	if resultCap <= 0 {
		return 0
	}
	dst := wasmBytes(resultPtr, resultCap)
	if len(dst) == 0 {
		return 0
	}
	payload := []byte(msg)
	if len(payload) > len(dst) {
		payload = payload[:len(dst)]
	}
	copy(dst, payload)
	return int32(len(payload))
}

func writeError(resultPtr, resultCap int32, msg string) int32 {
	n := writeResult(resultPtr, resultCap, msg)
	if n <= 0 {
		return -1
	}
	return -n
}
