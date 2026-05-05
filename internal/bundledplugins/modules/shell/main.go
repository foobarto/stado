//go:build wasip1

// Package main is the stado `shell` bundled plugin — shell.exec.
// Calls stado_exec (EP-0038 §B Tier 1) rather than the legacy stado_exec_bash.
package main

import (
	"encoding/json"

	"github.com/foobarto/stado/internal/bundledplugins/sdk"
)

func main() {}

//go:wasmimport stado stado_exec
func stadoExec(reqPtr, reqLen, resPtr, resCap uint32) int32

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 { return sdk.Alloc(size) }

//go:wasmexport stado_free
func stadoFree(ptr int32, size int32) { sdk.Free(ptr, size) }

// stado_tool_exec — shell.exec tool.
//
//go:wasmexport stado_tool_exec
func stadoToolExec(argsPtr, argsLen, resPtr, resCap int32) int32 {
	args := sdk.Bytes(argsPtr, argsLen)
	var req struct {
		Command   string `json:"command"`
		TimeoutMs int    `json:"timeout_ms"`
	}
	if err := json.Unmarshal(args, &req); err != nil || req.Command == "" {
		b, _ := json.Marshal(map[string]string{"error": "command is required"})
		return writeResult(resPtr, resCap, b)
	}

	execReq, _ := json.Marshal(map[string]any{
		"argv":       []string{"/bin/sh", "-c", req.Command},
		"timeout_ms": req.TimeoutMs,
	})
	reqPtr := sdk.Alloc(int32(len(execReq)))
	sdk.Write(reqPtr, execReq)
	defer sdk.Free(reqPtr, int32(len(execReq)))

	const bufSize = 1 << 20 // 1 MiB
	resBuf := sdk.Alloc(bufSize)
	defer sdk.Free(resBuf, bufSize)

	n := stadoExec(uint32(reqPtr), uint32(len(execReq)), uint32(resBuf), bufSize)
	if n < 0 {
		b, _ := json.Marshal(map[string]string{"error": "exec failed"})
		return writeResult(resPtr, resCap, b)
	}
	// Extract stdout from exec result JSON and return as raw text,
	// matching the native bash tool's output format.
	var execResult struct {
		Stdout   string `json:"stdout"`
		ExitCode int    `json:"exit_code"`
		Error    string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(sdk.Bytes(resBuf, n), &execResult); err != nil {
		return writeResult(resPtr, resCap, sdk.Bytes(resBuf, n)) // fallback: pass through
	}
	if execResult.Error != "" {
		b, _ := json.Marshal(map[string]string{"error": execResult.Error})
		return writeResult(resPtr, resCap, b)
	}
	return writeResult(resPtr, resCap, []byte(execResult.Stdout))
}

func writeResult(resPtr, resCap int32, data []byte) int32 {
	if int32(len(data)) > resCap {
		return -1
	}
	return sdk.Write(resPtr, data)
}
