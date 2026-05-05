//go:build wasip1

// Package main is the stado `fs` bundled plugin — read, write, edit, glob, grep.
// Implements tool logic in wasm against the Tier 1 stado_fs_* host imports.
// EP-0038 §C.
package main

import (
	"encoding/json"

	"github.com/foobarto/stado/internal/bundledplugins/sdk"
)

func main() {}

// ── host imports ───────────────────────────────────────────────────────────

//go:wasmimport stado stado_fs_read
func stadoFSRead(pathPtr, pathLen, bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_fs_read_partial
func stadoFSReadPartial(pathPtr, pathLen, offsetHi, offsetLo, lengthHi, lengthLo, bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_fs_tool_write
func stadoFSToolWrite(argsPtr, argsLen, resPtr, resCap uint32) int32

//go:wasmimport stado stado_fs_tool_edit
func stadoFSToolEdit(argsPtr, argsLen, resPtr, resCap uint32) int32

//go:wasmimport stado stado_fs_tool_glob
func stadoFSToolGlob(argsPtr, argsLen, resPtr, resCap uint32) int32

//go:wasmimport stado stado_fs_tool_grep
func stadoFSToolGrep(argsPtr, argsLen, resPtr, resCap uint32) int32

// fs.ls uses stado_exec to call /bin/ls (no native equivalent in fs/ tools).
//
//go:wasmimport stado stado_exec
func stadoExec(reqPtr, reqLen, resPtr, resCap uint32) int32

// ── ABI exports ────────────────────────────────────────────────────────────

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 { return sdk.Alloc(size) }

//go:wasmexport stado_free
func stadoFree(ptr int32, size int32) { sdk.Free(ptr, size) }

// stado_tool_read — fs.read with optional offset/length for partial reads.
//
//go:wasmexport stado_tool_read
func stadoToolRead(argsPtr, argsLen, resPtr, resCap int32) int32 {
	args := sdk.Bytes(argsPtr, argsLen)
	var req struct {
		Path   string `json:"path"`
		Offset int64  `json:"offset"`
		Length int64  `json:"length"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return writeError(resPtr, resCap, "invalid args: "+err.Error())
	}

	const defaultBufSize = 16 << 20 // 16 MiB
	bufCap := int32(defaultBufSize)
	buf := sdk.Alloc(bufCap)
	defer sdk.Free(buf, bufCap)

	pathBytes := []byte(req.Path)
	pathPtr := sdk.Alloc(int32(len(pathBytes)))
	sdk.Write(pathPtr, pathBytes)
	defer sdk.Free(pathPtr, int32(len(pathBytes)))

	var n int32
	if req.Offset > 0 || req.Length > 0 {
		length := req.Length
		if length <= 0 {
			length = defaultBufSize
		}
		n = stadoFSReadPartial(
			uint32(pathPtr), uint32(len(pathBytes)),
			uint32(req.Offset>>32), uint32(req.Offset),
			uint32(length>>32), uint32(length),
			uint32(buf), uint32(bufCap),
		)
	} else {
		n = stadoFSRead(uint32(pathPtr), uint32(len(pathBytes)), uint32(buf), uint32(bufCap))
	}
	if n < 0 {
		return writeError(resPtr, resCap, "read failed")
	}
	// Return raw content matching native ReadTool format.
	return writeResult(resPtr, resCap, sdk.Bytes(buf, n))
}

// stado_tool_write — delegate to existing native write host import.
//
//go:wasmexport stado_tool_write
func stadoToolWrite(argsPtr, argsLen, resPtr, resCap int32) int32 {
	return stadoFSToolWrite(uint32(argsPtr), uint32(argsLen), uint32(resPtr), uint32(resCap))
}

// stado_tool_edit — delegate to existing native edit host import.
//
//go:wasmexport stado_tool_edit
func stadoToolEdit(argsPtr, argsLen, resPtr, resCap int32) int32 {
	return stadoFSToolEdit(uint32(argsPtr), uint32(argsLen), uint32(resPtr), uint32(resCap))
}

// stado_tool_glob — delegate to existing native glob host import.
//
//go:wasmexport stado_tool_glob
func stadoToolGlob(argsPtr, argsLen, resPtr, resCap int32) int32 {
	return stadoFSToolGlob(uint32(argsPtr), uint32(argsLen), uint32(resPtr), uint32(resCap))
}

// stado_tool_grep — delegate to existing native grep host import.
//
//go:wasmexport stado_tool_grep
func stadoToolGrep(argsPtr, argsLen, resPtr, resCap int32) int32 {
	return stadoFSToolGrep(uint32(argsPtr), uint32(argsLen), uint32(resPtr), uint32(resCap))
}

// stado_tool_ls — fs.ls. Calls `ls -l --time-style=long-iso` via stado_exec.
// Returns structured directory entries that fs.glob doesn't provide.
//
//go:wasmexport stado_tool_ls
func stadoToolLs(argsPtr, argsLen, resPtr, resCap int32) int32 {
	args := sdk.Bytes(argsPtr, argsLen)
	var req struct {
		Path   string `json:"path"`
		Hidden bool   `json:"hidden"`
	}
	json.Unmarshal(args, &req)
	if req.Path == "" {
		req.Path = "."
	}
	argv := []string{"/bin/ls", "-l", "--time-style=long-iso"}
	if req.Hidden {
		argv = []string{"/bin/ls", "-la", "--time-style=long-iso"}
	}
	argv = append(argv, req.Path)
	execReq, _ := json.Marshal(map[string]any{"argv": argv, "timeout_ms": 10000})
	reqPtr := sdk.Alloc(int32(len(execReq)))
	defer sdk.Free(reqPtr, int32(len(execReq)))
	sdk.Write(reqPtr, execReq)

	const cap = 1 << 20
	resBuf := sdk.Alloc(cap)
	defer sdk.Free(resBuf, cap)
	n := stadoExec(uint32(reqPtr), uint32(len(execReq)), uint32(resBuf), cap)
	if n < 0 {
		return writeError(resPtr, resCap, "exec failed")
	}
	// stado_exec returns {stdout, exit_code} — pass through stdout as raw text
	// matching the existing ls plugin's output format.
	var ex struct {
		Stdout string `json:"stdout"`
	}
	if err := json.Unmarshal(sdk.Bytes(resBuf, n), &ex); err != nil {
		return writeResult(resPtr, resCap, sdk.Bytes(resBuf, n))
	}
	return writeResult(resPtr, resCap, []byte(ex.Stdout))
}

// ── helpers ────────────────────────────────────────────────────────────────

func writeError(resPtr, resCap int32, msg string) int32 {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return writeResult(resPtr, resCap, b)
}

func writeResult(resPtr, resCap int32, data []byte) int32 {
	if int32(len(data)) > resCap {
		return -1
	}
	return sdk.Write(resPtr, data)
}
