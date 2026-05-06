//go:build wasip1

// session_search — bundled wasm plugin offering session.search.
//
// Calls stado_session_read("history") to fetch the JSON conversation
// log, then runs a substring or regex search over message text.
// Returns matches with role, message index, and a context snippet.
//
// Capability: session:read (existing). No new host imports.
package main

import (
	"encoding/json"
	"sync"
	"unsafe"

	"github.com/foobarto/stado/internal/bundledplugins/modules/session_search/searchcore"
	"github.com/foobarto/stado/internal/bundledplugins/sdk"
)

func main() {}

//go:wasmimport stado stado_session_read
func stadoSessionRead(fieldPtr, fieldLen, bufPtr, bufCap uint32) int32

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
func stadoFree(ptr int32, _ int32) {
	pinned.Delete(uintptr(ptr))
}

// readHistory pulls the full conversation log via session:read. The
// host returns a JSON array of {role, text} objects. We grow the
// buffer once if the first read truncated.
func readHistory() ([]byte, bool) {
	field := []byte("history")
	fp := uint32(uintptr(unsafe.Pointer(&field[0])))
	fl := uint32(len(field))

	// Start with a generous buffer (1 MB). If the host returns -1
	// because of truncation, grow once to 8 MB and retry.
	for _, cap := range []int32{1 << 20, 8 << 20} {
		buf := make([]byte, cap)
		bp := uint32(uintptr(unsafe.Pointer(&buf[0])))
		n := stadoSessionRead(fp, fl, bp, uint32(cap))
		if n >= 0 {
			return buf[:n], true
		}
	}
	return nil, false
}

//go:wasmexport stado_tool_session_search
func stadoToolSessionSearch(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	var args searchcore.Args
	if raw := sdk.Bytes(argsPtr, argsLen); len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return writeErr(resultPtr, resultCap, "invalid args: "+err.Error())
		}
	}
	if args.Query == "" {
		return writeErr(resultPtr, resultCap, "query is required")
	}
	hist, ok := readHistory()
	if !ok {
		return writeErr(resultPtr, resultCap, "session:read denied or truncation; declare session:read in manifest")
	}
	var messages []searchcore.HistoryMessage
	if err := json.Unmarshal(hist, &messages); err != nil {
		return writeErr(resultPtr, resultCap, "history parse: "+err.Error())
	}
	result, err := searchcore.Run(messages, args)
	if err != nil {
		return writeErr(resultPtr, resultCap, err.Error())
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return writeErr(resultPtr, resultCap, "marshal: "+err.Error())
	}
	if int32(len(payload)) > resultCap {
		// Truncate by trimming matches until it fits — better than
		// returning -1 with no data.
		for len(result.Matches) > 0 && int32(len(payload)) > resultCap {
			result.Matches = result.Matches[:len(result.Matches)-1]
			payload, _ = json.Marshal(result)
		}
		if int32(len(payload)) > resultCap {
			return writeErr(resultPtr, resultCap, "result_cap too small")
		}
	}
	return sdk.Write(resultPtr, payload)
}

func writeErr(resultPtr, resultCap int32, msg string) int32 {
	payload := []byte(`{"error":` + jsonString(msg) + `}`)
	if int32(len(payload)) > resultCap {
		payload = payload[:resultCap]
	}
	return -sdk.Write(resultPtr, payload)
}

// jsonString quotes s as a JSON string literal (handles \", \\, \n, \r, \t).
func jsonString(s string) string {
	out := []byte{'"'}
	for _, r := range s {
		switch r {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\n':
			out = append(out, '\\', 'n')
		case '\r':
			out = append(out, '\\', 'r')
		case '\t':
			out = append(out, '\\', 't')
		default:
			if r < 0x20 {
				continue
			}
			out = append(out, []byte(string(r))...)
		}
	}
	out = append(out, '"')
	return string(out)
}
