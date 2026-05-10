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

//go:wasmimport stado stado_fs_tool_edit
func stadoFSToolEdit(argsPtr, argsLen, resultPtr, resultCap uint32) int32

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

type editArgs struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

//go:wasmexport stado_tool_edit
func stadoToolEdit(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	var args editArgs
	if raw := wasmBytes(argsPtr, argsLen); len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return writeError(resultPtr, resultCap, err.Error())
		}
	}
	body := "Path: " + defaultLabel(args.Path, "(unspecified)") +
		"\nOld: " + preview(args.Old) +
		"\nNew: " + preview(args.New)
	ok, errMsg := requestApproval("Allow file edit?", body)
	if errMsg != "" {
		return writeError(resultPtr, resultCap, errMsg)
	}
	if !ok {
		return writeError(resultPtr, resultCap, "operation denied by user")
	}
	return stadoFSToolEdit(uint32(argsPtr), uint32(argsLen), uint32(resultPtr), uint32(resultCap))
}

func requestApproval(title, body string) (bool, string) {
	titleBytes := []byte(title)
	bodyBytes := []byte(body)
	switch stadoUIApprove(bytesPtr(titleBytes), uint32(len(titleBytes)), bytesPtr(bodyBytes), uint32(len(bodyBytes))) {
	case 1:
		return true, ""
	case 0:
		return false, ""
	default:
		return false, "approval UI unavailable"
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

func writeError(resultPtr, resultCap int32, msg string) int32 {
	n := writeResult(resultPtr, resultCap, msg)
	if n <= 0 {
		return -1
	}
	return -n
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

func preview(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if s == "" {
		return "(empty)"
	}
	r := []rune(s)
	if len(r) > 120 {
		return string(r[:120]) + "..."
	}
	return s
}

func defaultLabel(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}
