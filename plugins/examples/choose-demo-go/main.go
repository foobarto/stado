package main

import (
	"encoding/json"
	"strings"
	"sync"
	"unsafe"
)

func main() {}

//go:wasmimport stado stado_ui_choose
func stadoUIChoose(reqPtr, reqLen, respPtr, respCap uint32) int32

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

type chooseOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type chooseRequest struct {
	Prompt  string         `json:"prompt"`
	Options []chooseOption `json:"options"`
	Multi   bool           `json:"multi"`
	Default []string       `json:"default,omitempty"`
}

type chooseResponse struct {
	Selected  []string `json:"selected"`
	Cancelled bool     `json:"cancelled"`
}

//go:wasmexport stado_tool_choose_demo
func stadoToolChooseDemo(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	var args struct {
		Prompt  string         `json:"prompt"`
		Options []chooseOption `json:"options"`
		Multi   bool           `json:"multi"`
		Default []string       `json:"default"`
	}
	if raw := wasmBytes(argsPtr, argsLen); len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return writeError(resultPtr, resultCap, "invalid choose_demo args: "+err.Error())
		}
	}
	if strings.TrimSpace(args.Prompt) == "" {
		args.Prompt = "Pick an option (choose_demo manual smoke):"
	}
	if len(args.Options) == 0 {
		args.Options = []chooseOption{
			{ID: "alpha", Label: "Alpha"},
			{ID: "bravo", Label: "Bravo"},
			{ID: "charlie", Label: "Charlie"},
		}
	}

	req, err := json.Marshal(chooseRequest{
		Prompt:  args.Prompt,
		Options: args.Options,
		Multi:   args.Multi,
		Default: args.Default,
	})
	if err != nil {
		return writeError(resultPtr, resultCap, "choose_demo: marshal request: "+err.Error())
	}

	// stado_ui_choose writes its response (or a -bytes error message)
	// back into the caller's result buffer. Borrow the buffer directly;
	// the host respects resultCap and the wasmexport contract. The
	// request buffer needs to outlive the call, so allocate via the
	// pinned alloc map first.
	reqPtr := stadoAlloc(int32(len(req)))
	defer stadoFree(reqPtr, int32(len(req)))
	dst := wasmBytes(reqPtr, int32(len(req)))
	copy(dst, req)

	n := stadoUIChoose(uint32(reqPtr), uint32(len(req)), uint32(resultPtr), uint32(resultCap))
	if n < 0 {
		// Host already wrote the -n-byte error message into resultPtr.
		return n
	}
	if n == 0 {
		return writeError(resultPtr, resultCap, "choose_demo: empty response from host")
	}

	respJSON := wasmBytes(resultPtr, n)
	var resp chooseResponse
	if err := json.Unmarshal(respJSON, &resp); err != nil {
		return writeError(resultPtr, resultCap, "choose_demo: parse response: "+err.Error())
	}
	if resp.Cancelled {
		return writeResult(resultPtr, resultCap, "cancelled")
	}
	return writeResult(resultPtr, resultCap, "selected: "+strings.Join(resp.Selected, ","))
}

func wasmBytes(ptr, size int32) []byte {
	if ptr == 0 || size <= 0 {
		return nil
	}
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), int(size))
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
