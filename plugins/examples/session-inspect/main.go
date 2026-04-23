// session-inspect — demo plugin exercising the Phase 7.1b
// session/LLM capabilities. Declares session:read + session:fork +
// llm:invoke, then a single `inspect` tool that pulls the current
// session's shape into a JSON report for the caller.
//
// Not a production plugin — the point is to prove the host imports
// work end-to-end from a signed wasm module. The auto-compaction
// plugin this scaffold is intended to enable would add an
// `llm:invoke` call to summarise the history and a `session:fork`
// with the summary as seed.
//
// Build target: GOOS=wasip1 GOARCH=wasm -buildmode=c-shared (same
// reactor pattern as plugins/examples/hello-go/). ~120 lines of
// plugin logic.
package main

import (
	"encoding/json"
	"sync"
	"unsafe"
)

func main() {}

// ---- host imports ------------------------------------------------------

//go:wasmimport stado stado_log
func stadoLog(levelPtr, levelLen, msgPtr, msgLen uint32)

//go:wasmimport stado stado_session_read
func stadoSessionRead(fieldPtr, fieldLen, bufPtr, bufCap uint32) int32

// ---- log helper --------------------------------------------------------

func logInfo(msg string) {
	level := []byte("info")
	msgBytes := []byte(msg)
	stadoLog(
		uint32(uintptr(unsafe.Pointer(&level[0]))), uint32(len(level)),
		uint32(uintptr(unsafe.Pointer(&msgBytes[0]))), uint32(len(msgBytes)),
	)
}

// ---- allocator ---------------------------------------------------------

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

// ---- session read helper ----------------------------------------------

// readField calls stado_session_read for the named field. Returns the
// raw bytes or nil if the host denied / errored. Buffer size of 64 KiB
// is generous enough for even large history dumps from reasonable
// sessions; oversized would return -1 which we surface as nil.
func readField(name string) []byte {
	fieldBytes := []byte(name)
	buf := make([]byte, 64*1024)
	n := stadoSessionRead(
		uint32(uintptr(unsafe.Pointer(&fieldBytes[0]))), uint32(len(fieldBytes)),
		uint32(uintptr(unsafe.Pointer(&buf[0]))), uint32(len(buf)),
	)
	if n < 0 {
		return nil
	}
	return buf[:n]
}

// ---- tool: inspect ----------------------------------------------------

// report is the JSON shape the inspect tool returns. Intentionally
// compact so the caller can pretty-print it elsewhere.
type report struct {
	SessionID    string          `json:"session_id"`
	MessageCount string          `json:"message_count"`
	TokenCount   string          `json:"token_count"`
	LastTurnRef  string          `json:"last_turn_ref"`
	HistoryBytes int             `json:"history_bytes"` // len of history payload
	HistoryHead  json.RawMessage `json:"history_head,omitempty"`
}

//go:wasmexport stado_tool_inspect
func stadoToolInspect(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	_ = argsPtr
	_ = argsLen

	logInfo("inspect: reading session state")

	hist := readField("history")
	r := report{
		SessionID:    string(readField("session_id")),
		MessageCount: string(readField("message_count")),
		TokenCount:   string(readField("token_count")),
		LastTurnRef:  string(readField("last_turn_ref")),
		HistoryBytes: len(hist),
	}
	// Echo the first 512 bytes of history as a sanity excerpt — any
	// more would overflow typical tool-call result budgets.
	if len(hist) > 0 {
		head := hist
		if len(head) > 512 {
			head = head[:512]
		}
		r.HistoryHead = json.RawMessage(head)
	}

	payload, err := json.Marshal(r)
	if err != nil {
		return -1
	}
	if int32(len(payload)) > resultCap {
		return -1
	}
	dst := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(resultPtr))), int(resultCap))
	copy(dst, payload)
	return int32(len(payload))
}
