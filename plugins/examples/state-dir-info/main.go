// state-dir-info — minimal plugin demonstrating the EP-0029
// `cfg:state_dir` capability. Calls `stado_cfg_state_dir` once,
// returns the resolved path as JSON.
//
// Authoring lineage: this is the canonical example for the
// `cfg:*` capability vocabulary. Plugins that need to compose
// paths under stado's state dir (operator-tooling for installed
// plugins, the memory store, the worktree dir, etc.) start here.
//
// Capabilities (declared in plugin.manifest.json):
//   cfg:state_dir
//
// No other capabilities — this plugin doesn't read or write
// anything. It's purely a "what is your state-dir, stado?" tool.
package main

import (
	"encoding/json"
	"sync"
	"unsafe"
)

func main() {}

//go:wasmimport stado stado_log
func stadoLog(levelPtr, levelLen, msgPtr, msgLen uint32)

//go:wasmimport stado stado_cfg_state_dir
func stadoCfgStateDir(bufPtr, bufCap uint32) int32

func logInfo(msg string) {
	level := []byte("info")
	m := []byte(msg)
	stadoLog(
		uint32(uintptr(unsafe.Pointer(&level[0]))), uint32(len(level)),
		uint32(uintptr(unsafe.Pointer(&m[0]))), uint32(len(m)),
	)
}

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

type result struct {
	StateDir string `json:"state_dir"`
	Note     string `json:"note,omitempty"`
}

//go:wasmexport stado_tool_state_dir
func stadoToolStateDir(_, _, resultPtr, resultCap int32) int32 {
	logInfo("state-dir-info invoked")

	// 4 KiB matches maxPluginRuntimeCfgValueBytes on the host side.
	// State-dir paths are well under 1 KiB in practice; this is just
	// safety margin. If the host returns -1 we surface that as a note.
	const cfgBufCap = 4096
	cfgBuf := make([]byte, cfgBufCap)
	n := stadoCfgStateDir(
		uint32(uintptr(unsafe.Pointer(&cfgBuf[0]))), uint32(cfgBufCap),
	)
	if n < 0 {
		return writeJSON(resultPtr, resultCap, result{
			Note: "stado_cfg_state_dir returned -1 — value exceeds buffer or capability not granted (declare cfg:state_dir in your manifest)",
		})
	}
	if n == 0 {
		return writeJSON(resultPtr, resultCap, result{
			Note: "stado_cfg_state_dir returned 0 bytes — host caller did not populate host.StateDir (uncommon; usually means a non-standard invocation path)",
		})
	}
	return writeJSON(resultPtr, resultCap, result{
		StateDir: string(cfgBuf[:n]),
	})
}

func writeJSON(resultPtr, resultCap int32, v any) int32 {
	payload, err := json.Marshal(v)
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
