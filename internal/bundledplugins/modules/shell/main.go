//go:build wasip1

// shell — bundled stado plugin: one-shot exec + interactive PTY sessions.
//
// One-shot tools (over stado_exec):
//   shell_exec      — run a shell command, return stdout
//   shell_bash      — run via /bin/bash
//   shell_sh        — run via /bin/sh
//   shell_zsh       — run via /usr/bin/zsh
//
// PTY session tools (over stado_terminal_*):
//   shell_spawn     — open a PTY session, returns id
//   shell_list      — list active sessions
//   shell_attach    — claim a session for read/write
//   shell_detach    — release attachment
//   shell_read      — read buffered output from a session
//   shell_write     — write to a session's stdin
//   shell_resize    — resize PTY (cols, rows)
//   shell_signal    — send POSIX signal
//   shell_destroy   — kill + free the session
//
// EP-0038 §C.
package main

import (
	"encoding/base64"
	"encoding/json"

	"github.com/foobarto/stado/internal/bundledplugins/sdk"
)

func main() {}

// ── host imports ───────────────────────────────────────────────────────────

//go:wasmimport stado stado_exec
func stadoExec(reqPtr, reqLen, resPtr, resCap uint32) int32

//go:wasmimport stado stado_terminal_open
func stadoTerminalOpen(argsPtr, argsLen, resPtr, resCap uint32) int64

//go:wasmimport stado stado_terminal_list
func stadoTerminalList(bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_terminal_attach
func stadoTerminalAttach(argsPtr, argsLen, resPtr, resCap uint32) int32

//go:wasmimport stado stado_terminal_detach
func stadoTerminalDetach(argsPtr, argsLen, resPtr, resCap uint32) int32

//go:wasmimport stado stado_terminal_write
func stadoTerminalWrite(idLo, idHi, bufPtr, bufLen, errPtr, errCap uint32) int32

// stado_terminal_read takes (idLo, idHi, maxBytes, timeoutMs, bufPtr, bufCap).
//
//go:wasmimport stado stado_terminal_read
func stadoTerminalRead(idLo, idHi, maxBytes, timeoutMs, bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_terminal_signal
func stadoTerminalSignal(argsPtr, argsLen, resPtr, resCap uint32) int32

//go:wasmimport stado stado_terminal_resize
func stadoTerminalResize(argsPtr, argsLen, resPtr, resCap uint32) int32

//go:wasmimport stado stado_terminal_close
func stadoTerminalClose(argsPtr, argsLen, resPtr, resCap uint32) int32

// ── ABI ────────────────────────────────────────────────────────────────────

//go:wasmexport stado_alloc
func stadoAlloc(size int32) int32 { return sdk.Alloc(size) }

//go:wasmexport stado_free
func stadoFree(ptr int32, size int32) { sdk.Free(ptr, size) }

// ── one-shot exec ──────────────────────────────────────────────────────────

func runOneShot(argv []string, stdin string, timeoutMs int) (string, error) {
	req, _ := json.Marshal(map[string]any{
		"argv":       argv,
		"stdin":      stdin,
		"timeout_ms": timeoutMs,
	})
	reqPtr := sdk.Alloc(int32(len(req)))
	defer sdk.Free(reqPtr, int32(len(req)))
	sdk.Write(reqPtr, req)

	const cap = 1 << 20
	resBuf := sdk.Alloc(cap)
	defer sdk.Free(resBuf, cap)
	n := stadoExec(uint32(reqPtr), uint32(len(req)), uint32(resBuf), cap)
	if n < 0 {
		return "", &execErr{msg: "exec failed"}
	}
	var er struct {
		Stdout string `json:"stdout"`
		Error  string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(sdk.Bytes(resBuf, n), &er); err != nil {
		return string(sdk.Bytes(resBuf, n)), nil
	}
	if er.Error != "" {
		return "", &execErr{msg: er.Error}
	}
	return er.Stdout, nil
}

type execErr struct{ msg string }

func (e *execErr) Error() string { return e.msg }

func execTool(resPtr, resCap int32, argv []string, command string, timeoutMs int) int32 {
	if command != "" {
		argv = append(argv, "-c", command)
	}
	out, err := runOneShot(argv, "", timeoutMs)
	if err != nil {
		return writeErr(resPtr, resCap, err.Error())
	}
	return writeRaw(resPtr, resCap, []byte(out))
}

//go:wasmexport stado_tool_exec
func stadoToolExec(argsPtr, argsLen, resPtr, resCap int32) int32 {
	args := sdk.Bytes(argsPtr, argsLen)
	var req struct {
		Command   string `json:"command"`
		TimeoutMs int    `json:"timeout_ms"`
	}
	json.Unmarshal(args, &req)
	if req.Command == "" {
		return writeErr(resPtr, resCap, "command is required")
	}
	return execTool(resPtr, resCap, []string{"/bin/sh"}, req.Command, req.TimeoutMs)
}

//go:wasmexport stado_tool_bash
func stadoToolBash(argsPtr, argsLen, resPtr, resCap int32) int32 {
	var req struct {
		Command   string `json:"command"`
		TimeoutMs int    `json:"timeout_ms"`
	}
	json.Unmarshal(sdk.Bytes(argsPtr, argsLen), &req)
	if req.Command == "" {
		return writeErr(resPtr, resCap, "command is required")
	}
	return execTool(resPtr, resCap, []string{"/bin/bash"}, req.Command, req.TimeoutMs)
}

//go:wasmexport stado_tool_sh
func stadoToolSh(argsPtr, argsLen, resPtr, resCap int32) int32 {
	var req struct {
		Command   string `json:"command"`
		TimeoutMs int    `json:"timeout_ms"`
	}
	json.Unmarshal(sdk.Bytes(argsPtr, argsLen), &req)
	if req.Command == "" {
		return writeErr(resPtr, resCap, "command is required")
	}
	return execTool(resPtr, resCap, []string{"/bin/sh"}, req.Command, req.TimeoutMs)
}

//go:wasmexport stado_tool_zsh
func stadoToolZsh(argsPtr, argsLen, resPtr, resCap int32) int32 {
	var req struct {
		Command   string `json:"command"`
		TimeoutMs int    `json:"timeout_ms"`
	}
	json.Unmarshal(sdk.Bytes(argsPtr, argsLen), &req)
	if req.Command == "" {
		return writeErr(resPtr, resCap, "command is required")
	}
	return execTool(resPtr, resCap, []string{"/usr/bin/zsh"}, req.Command, req.TimeoutMs)
}

// ── PTY session tools ─────────────────────────────────────────────────────

// shell_spawn — open a PTY session.
//
//go:wasmexport stado_tool_spawn
func stadoToolSpawn(argsPtr, argsLen, resPtr, resCap int32) int32 {
	const errCap = 4096
	errBuf := sdk.Alloc(errCap)
	defer sdk.Free(errBuf, errCap)
	id := stadoTerminalOpen(uint32(argsPtr), uint32(argsLen), uint32(errBuf), errCap)
	if id <= 0 {
		// Negative return = -byte_count of error string at errBuf.
		errLen := -id
		if errLen > 0 && errLen <= errCap {
			return writeErr(resPtr, resCap, string(sdk.Bytes(errBuf, int32(errLen))))
		}
		return writeErr(resPtr, resCap, "spawn failed")
	}
	out, _ := json.Marshal(map[string]any{"id": id})
	return writeRaw(resPtr, resCap, out)
}

//go:wasmexport stado_tool_list
func stadoToolList(argsPtr, argsLen, resPtr, resCap int32) int32 {
	const cap = 64 * 1024
	buf := sdk.Alloc(cap)
	defer sdk.Free(buf, cap)
	n := stadoTerminalList(uint32(buf), cap)
	if n < 0 {
		return writeErr(resPtr, resCap, "list failed")
	}
	return writeRaw(resPtr, resCap, sdk.Bytes(buf, n))
}

//go:wasmexport stado_tool_attach
func stadoToolAttach(argsPtr, argsLen, resPtr, resCap int32) int32 {
	return passthroughTerminal(argsPtr, argsLen, resPtr, resCap, stadoTerminalAttach, "attach")
}

//go:wasmexport stado_tool_detach
func stadoToolDetach(argsPtr, argsLen, resPtr, resCap int32) int32 {
	return passthroughTerminal(argsPtr, argsLen, resPtr, resCap, stadoTerminalDetach, "detach")
}

func passthroughTerminal(
	argsPtr, argsLen, resPtr, resCap int32,
	fn func(uint32, uint32, uint32, uint32) int32,
	op string,
) int32 {
	const errCap = 4096
	errBuf := sdk.Alloc(errCap)
	defer sdk.Free(errBuf, errCap)
	rc := fn(uint32(argsPtr), uint32(argsLen), uint32(errBuf), errCap)
	if rc < 0 {
		return writeErr(resPtr, resCap, op+": "+string(sdk.Bytes(errBuf, -rc)))
	}
	out, _ := json.Marshal(map[string]bool{"ok": true})
	return writeRaw(resPtr, resCap, out)
}

//go:wasmexport stado_tool_write
func stadoToolWrite(argsPtr, argsLen, resPtr, resCap int32) int32 {
	var req struct {
		ID      uint64 `json:"id"`
		Data    string `json:"data"`     // UTF-8
		DataB64 string `json:"data_b64"` // raw bytes
	}
	if err := json.Unmarshal(sdk.Bytes(argsPtr, argsLen), &req); err != nil || req.ID == 0 {
		return writeErr(resPtr, resCap, "id and data/data_b64 are required")
	}
	var data []byte
	if req.DataB64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(req.DataB64)
		if err != nil {
			return writeErr(resPtr, resCap, "invalid base64: "+err.Error())
		}
		data = decoded
	} else {
		data = []byte(req.Data)
	}
	if len(data) == 0 {
		return writeErr(resPtr, resCap, "empty data")
	}
	dataPtr := sdk.Alloc(int32(len(data)))
	defer sdk.Free(dataPtr, int32(len(data)))
	sdk.Write(dataPtr, data)

	const errCap = 4096
	errBuf := sdk.Alloc(errCap)
	defer sdk.Free(errBuf, errCap)

	idLo := uint32(req.ID & 0xFFFFFFFF)
	idHi := uint32(req.ID >> 32)
	n := stadoTerminalWrite(idLo, idHi, uint32(dataPtr), uint32(len(data)), uint32(errBuf), errCap)
	if n < 0 {
		return writeErr(resPtr, resCap, "write: "+string(sdk.Bytes(errBuf, -n)))
	}
	out, _ := json.Marshal(map[string]int{"n": int(n)})
	return writeRaw(resPtr, resCap, out)
}

//go:wasmexport stado_tool_read
func stadoToolRead(argsPtr, argsLen, resPtr, resCap int32) int32 {
	var req struct {
		ID        uint64 `json:"id"`
		MaxBytes  int    `json:"max_bytes"`
		TimeoutMs int    `json:"timeout_ms"`
	}
	if err := json.Unmarshal(sdk.Bytes(argsPtr, argsLen), &req); err != nil || req.ID == 0 {
		return writeErr(resPtr, resCap, "id is required")
	}
	if req.MaxBytes <= 0 || req.MaxBytes > 1<<20 {
		req.MaxBytes = 64 * 1024
	}
	bufPtr := sdk.Alloc(int32(req.MaxBytes))
	defer sdk.Free(bufPtr, int32(req.MaxBytes))

	idLo := uint32(req.ID & 0xFFFFFFFF)
	idHi := uint32(req.ID >> 32)
	n := stadoTerminalRead(idLo, idHi, uint32(req.MaxBytes), uint32(req.TimeoutMs), uint32(bufPtr), uint32(req.MaxBytes))
	if n < 0 {
		// Negative return = -byte_count of error string at bufPtr.
		errLen := -n
		if errLen > 0 && errLen <= int32(req.MaxBytes) {
			return writeErr(resPtr, resCap, "read: "+string(sdk.Bytes(bufPtr, errLen)))
		}
		return writeErr(resPtr, resCap, "read failed")
	}
	out, _ := json.Marshal(map[string]any{
		"data_b64": base64.StdEncoding.EncodeToString(sdk.Bytes(bufPtr, n)),
		"n":        int(n),
	})
	return writeRaw(resPtr, resCap, out)
}

//go:wasmexport stado_tool_signal
func stadoToolSignal(argsPtr, argsLen, resPtr, resCap int32) int32 {
	return passthroughTerminal(argsPtr, argsLen, resPtr, resCap, stadoTerminalSignal, "signal")
}

//go:wasmexport stado_tool_resize
func stadoToolResize(argsPtr, argsLen, resPtr, resCap int32) int32 {
	return passthroughTerminal(argsPtr, argsLen, resPtr, resCap, stadoTerminalResize, "resize")
}

//go:wasmexport stado_tool_destroy
func stadoToolDestroy(argsPtr, argsLen, resPtr, resCap int32) int32 {
	return passthroughTerminal(argsPtr, argsLen, resPtr, resCap, stadoTerminalClose, "destroy")
}

// ── helpers ────────────────────────────────────────────────────────────────

func writeErr(resPtr, resCap int32, msg string) int32 {
	b, _ := json.Marshal(map[string]string{"error": msg})
	if int32(len(b)) > resCap {
		return -1
	}
	return sdk.Write(resPtr, b)
}

func writeRaw(resPtr, resCap int32, data []byte) int32 {
	if int32(len(data)) > resCap {
		return -1
	}
	return sdk.Write(resPtr, data)
}
