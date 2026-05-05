//go:build wasip1

// Package main is the stado `rg` bundled plugin — rg.search.
// Uses stado_bundled_bin + stado_exec. EP-0038 §C.
package main

import (
	"encoding/json"
	"strings"

	"github.com/foobarto/stado/internal/bundledplugins/sdk"
)

func main() {}

//go:wasmimport stado stado_bundled_bin
func stadoBundledBin(namePtr, nameLen, bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_exec
func stadoExec(reqPtr, reqLen, resPtr, resCap uint32) int32

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 { return sdk.Alloc(size) }

//go:wasmexport stado_free
func stadoFree(ptr int32, size int32) { sdk.Free(ptr, size) }

//go:wasmexport stado_tool_search
func stadoToolSearch(argsPtr, argsLen, resPtr, resCap int32) int32 {
	args := sdk.Bytes(argsPtr, argsLen)
	var req struct {
		Pattern string   `json:"pattern"`
		Path    string   `json:"path"`
		Flags   []string `json:"flags"`
	}
	if err := json.Unmarshal(args, &req); err != nil || req.Pattern == "" {
		return writeError(resPtr, resCap, "pattern is required")
	}

	// Resolve ripgrep binary path.
	name := []byte("ripgrep")
	namePtr := sdk.Alloc(int32(len(name)))
	sdk.Write(namePtr, name)
	defer sdk.Free(namePtr, int32(len(name)))

	const pathBufSize = 512
	pathBuf := sdk.Alloc(pathBufSize)
	defer sdk.Free(pathBuf, pathBufSize)

	n := stadoBundledBin(uint32(namePtr), uint32(len(name)), uint32(pathBuf), pathBufSize)
	if n < 0 {
		return writeError(resPtr, resCap, "ripgrep binary not available — install ripgrep or use a release build")
	}
	rgPath := string(sdk.Bytes(pathBuf, n))

	argv := []string{rgPath, "--json"}
	argv = append(argv, req.Flags...)
	argv = append(argv, req.Pattern)
	if req.Path != "" {
		argv = append(argv, req.Path)
	}

	execReq, _ := json.Marshal(map[string]any{"argv": argv})
	reqPtr := sdk.Alloc(int32(len(execReq)))
	sdk.Write(reqPtr, execReq)
	defer sdk.Free(reqPtr, int32(len(execReq)))

	const resBufSize = 8 << 20 // 8 MiB
	resBuf := sdk.Alloc(resBufSize)
	defer sdk.Free(resBuf, resBufSize)

	n = stadoExec(uint32(reqPtr), uint32(len(execReq)), uint32(resBuf), resBufSize)
	if n < 0 {
		return writeError(resPtr, resCap, "exec failed")
	}

	// Parse exec result and extract stdout.
	var execResult struct {
		Stdout   string `json:"stdout"`
		ExitCode int    `json:"exit_code"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(sdk.Bytes(resBuf, n), &execResult); err != nil {
		return writeError(resPtr, resCap, "parse exec result: "+err.Error())
	}
	if execResult.Error != "" {
		return writeError(resPtr, resCap, execResult.Error)
	}

	type result struct {
		Output   string `json:"output"`
		ExitCode int    `json:"exit_code"`
	}
	b, _ := json.Marshal(result{
		Output:   strings.TrimRight(execResult.Stdout, "\n"),
		ExitCode: execResult.ExitCode,
	})
	return writeResult(resPtr, resCap, b)
}

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
