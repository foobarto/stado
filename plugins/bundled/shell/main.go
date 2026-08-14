//go:build wasip1

// shell — bundled stado plugin: one-shot exec + interactive PTY sessions.
//
// One-shot tools (over stado_exec):
//
//	shell_exec      — run a shell command, return stdout
//	shell_bash      — run via /bin/bash
//	shell_sh        — run via /bin/sh
//	shell_zsh       — run via /usr/bin/zsh
//
// PTY session tools (over stado_pty_*):
//
//	shell_spawn     — open a PTY session, returns id
//	shell_list      — list active sessions
//	shell_read      — read output (mode: auto|stream|screen)
//	shell_write     — write to a session's stdin
//	shell_resize    — resize PTY (cols, rows)
//	shell_signal    — send POSIX signal
//	shell_destroy   — kill + free the session
//
// EP-0038 §C.
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/foobarto/stado/internal/plugins/bundled/sdk"
	"github.com/foobarto/stado/pkg/tool"
)

func main() {}

// ── host imports ───────────────────────────────────────────────────────────

//go:wasmimport stado stado_exec
func stadoExec(reqPtr, reqLen, resPtr, resCap uint32) int32

//go:wasmimport stado stado_pty_create
func stadoPTYCreate(argsPtr, argsLen, resPtr, resCap uint32) int64

//go:wasmimport stado stado_pty_list
func stadoPTYList(bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_pty_write
func stadoPTYWrite(idLo, idHi, bufPtr, bufLen, errPtr, errCap uint32) int32

// stado_pty_read takes (idLo, idHi, maxBytes, timeoutMs, bufPtr, bufCap).
//
//go:wasmimport stado stado_pty_read
func stadoPTYRead(idLo, idHi, maxBytes, timeoutMs, bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_pty_signal
func stadoPTYSignal(argsPtr, argsLen, resPtr, resCap uint32) int32

//go:wasmimport stado stado_pty_resize
func stadoPTYResize(argsPtr, argsLen, resPtr, resCap uint32) int32

//go:wasmimport stado stado_pty_destroy
func stadoPTYDestroy(argsPtr, argsLen, resPtr, resCap uint32) int32

//go:wasmimport stado stado_pty_snapshot
func stadoPTYSnapshot(argsPtr, argsLen, resPtr, resCap uint32) int32

// stado_pty_expect takes (idLo, idHi, argsPtr, argsLen, resPtr, resCap).
//
//go:wasmimport stado stado_pty_expect
func stadoPTYExpect(idLo, idHi, argsPtr, argsLen, resPtr, resCap uint32) int32

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
		errLen := -n
		if errLen > cap {
			errLen = cap
		}
		message := string(sdk.Bytes(resBuf, errLen))
		var envelope tool.ErrorEnvelopeV1
		if json.Unmarshal([]byte(message), &envelope) == nil &&
			envelope.Schema == tool.ErrorEnvelopeSchemaV1 && envelope.Message != "" {
			return "", &execErr{msg: envelope.Message, kind: envelope.Kind, exitCode: envelope.ExitCode}
		}
		return "", &execErr{msg: message, kind: tool.FailureLaunch}
	}
	var er struct {
		Stdout   string `json:"stdout"`
		ExitCode int    `json:"exit_code"`
		Error    string `json:"error,omitempty"`
	}
	if err := json.Unmarshal(sdk.Bytes(resBuf, n), &er); err != nil {
		return "", &execErr{msg: "exec: invalid host response: " + err.Error(), kind: tool.FailureLaunch}
	}
	if er.Error != "" {
		return "", &execErr{msg: er.Error, kind: tool.FailureLaunch}
	}
	if er.ExitCode != 0 {
		msg := fmt.Sprintf("command exited with code %d", er.ExitCode)
		if er.Stdout != "" {
			msg += "\n" + er.Stdout
		}
		code := er.ExitCode
		return "", &execErr{msg: msg, kind: tool.FailureExit, exitCode: &code}
	}
	return er.Stdout, nil
}

type execErr struct {
	msg      string
	kind     tool.FailureKind
	exitCode *int
}

func (e *execErr) Error() string { return e.msg }

func (e *execErr) envelope(maxBytes int32) string {
	marshal := func(message string) []byte {
		payload, _ := json.Marshal(tool.ErrorEnvelopeV1{
			Schema: tool.ErrorEnvelopeSchemaV1, Kind: e.kind,
			Message: message, ExitCode: e.exitCode,
		})
		return payload
	}
	if payload := marshal(e.msg); int32(len(payload)) <= maxBytes {
		return string(payload)
	}

	const suffix = "\n[output truncated to preserve error metadata]"
	runes := []rune(e.msg)
	low, high := 0, len(runes)
	best := marshal(suffix)
	for low <= high {
		mid := low + (high-low)/2
		candidate := marshal(string(runes[:mid]) + suffix)
		if int32(len(candidate)) <= maxBytes {
			best = candidate
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return string(best)
}

func execTool(resPtr, resCap int32, argv []string, command string, timeoutMs int) int32 {
	if command != "" {
		argv = append(argv, "-c", command)
	}
	out, err := runOneShot(argv, "", timeoutMs)
	if err != nil {
		if structured, ok := err.(*execErr); ok {
			return writeErr(resPtr, resCap, structured.envelope(resCap))
		}
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
	id := stadoPTYCreate(uint32(argsPtr), uint32(argsLen), uint32(errBuf), errCap)
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
	n := stadoPTYList(uint32(buf), cap)
	if n < 0 {
		return writeErr(resPtr, resCap, "list failed")
	}
	return writeRaw(resPtr, resCap, sdk.Bytes(buf, n))
}

// (EP-0043 D6: shell_attach / shell_detach removed — read/write work by id,
// no attach gate.)

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
	n := stadoPTYWrite(idLo, idHi, uint32(dataPtr), uint32(len(data)), uint32(errBuf), errCap)
	if n < 0 {
		return writeErr(resPtr, resCap, "write: "+string(sdk.Bytes(errBuf, -n)))
	}
	out, _ := json.Marshal(map[string]int{"n": int(n)})
	return writeRaw(resPtr, resCap, out)
}

// shell_read — get output from a PTY session. EP-0043 folds the old
// shell_screenshot in via a `mode`:
//
//	mode "auto" (default) — the host returns the rendered screen when a
//	  full-screen program is active (vim/htop/less/installer — it switched
//	  to the alternate screen buffer), else the raw incremental stream.
//	mode "stream"  — force the raw incremental byte stream.
//	mode "screen"  — force the rendered vt100 screen.
//
// Response carries a `kind` discriminator:
//
//	{"kind":"stream", "data_b64": <b64>, "n": N}
//	{"kind":"screen", "text": ..., "cols": C, "rows": R, "cursor": {...}, "title": ..., "svg"?: ...}
//
//go:wasmexport stado_tool_read
func stadoToolRead(argsPtr, argsLen, resPtr, resCap int32) int32 {
	var req struct {
		ID        uint64  `json:"id"`
		Mode      string  `json:"mode"`
		MaxBytes  int     `json:"max_bytes"`
		TimeoutMs int     `json:"timeout_ms"`
		WithSVG   bool    `json:"with_svg"`
		SVGCellW  float64 `json:"svg_cell_w"`
		SVGCellH  float64 `json:"svg_cell_h"`
		SVGFontPx int     `json:"svg_font_px"`
	}
	if err := json.Unmarshal(sdk.Bytes(argsPtr, argsLen), &req); err != nil || req.ID == 0 {
		return writeErr(resPtr, resCap, "id is required")
	}
	mode := req.Mode
	if mode == "" {
		mode = "auto"
	}

	// screen | auto: ask the host for the rendered screen. For auto the host
	// returns a {"kind":"stream"} marker (no grid render) when no full-screen
	// program is active, in which case we fall through to the raw stream.
	if mode == "screen" || mode == "auto" {
		screenJSON, isStream, emsg := readScreen(req.ID, mode, req.WithSVG, req.SVGCellW, req.SVGCellH, req.SVGFontPx)
		if emsg != "" {
			return writeErr(resPtr, resCap, "read: "+emsg)
		}
		if !isStream {
			return writeRaw(resPtr, resCap, screenJSON)
		}
		// auto + not full-screen → raw stream below.
	}

	// stream: raw incremental bytes since the last read.
	if req.MaxBytes <= 0 || req.MaxBytes > 1<<20 {
		req.MaxBytes = 64 * 1024
	}
	bufPtr := sdk.Alloc(int32(req.MaxBytes))
	defer sdk.Free(bufPtr, int32(req.MaxBytes))

	idLo := uint32(req.ID & 0xFFFFFFFF)
	idHi := uint32(req.ID >> 32)
	n := stadoPTYRead(idLo, idHi, uint32(req.MaxBytes), uint32(req.TimeoutMs), uint32(bufPtr), uint32(req.MaxBytes))
	if n == -1 {
		// EOF sentinel (host contract): session closed + ring empty, and
		// the host writes NO error string for -1. Surface a clean stream
		// EOF — not a 1-byte garbage error from the zeroed scratch buffer.
		out, _ := json.Marshal(map[string]any{"kind": "stream", "data_b64": "", "n": 0, "eof": true})
		return writeRaw(resPtr, resCap, out)
	}
	if n < 0 {
		// Negative (≤ -2) = -byte_count of an error string at bufPtr.
		errLen := -n
		if errLen > 0 && errLen <= int32(req.MaxBytes) {
			return writeErr(resPtr, resCap, "read: "+string(sdk.Bytes(bufPtr, errLen)))
		}
		return writeErr(resPtr, resCap, "read failed")
	}
	out, _ := json.Marshal(map[string]any{
		"kind":     "stream",
		"data_b64": base64.StdEncoding.EncodeToString(sdk.Bytes(bufPtr, n)),
		"n":        int(n),
	})
	return writeRaw(resPtr, resCap, out)
}

// readScreen calls the snapshot host import. Returns the rendered-screen
// JSON (kind:"screen") to forward verbatim; isStream=true when mode=="auto"
// and the host reported no full-screen program (caller reads the raw stream
// instead). The result is copied out before the scratch buffer is freed.
func readScreen(id uint64, mode string, withSVG bool, cw, ch float64, fontPx int) (out []byte, isStream bool, errMsg string) {
	args, _ := json.Marshal(map[string]any{
		"id":          id,
		"mode":        mode,
		"with_svg":    withSVG,
		"svg_cell_w":  cw,
		"svg_cell_h":  ch,
		"svg_font_px": fontPx,
	})
	argPtr := sdk.Alloc(int32(len(args)))
	defer sdk.Free(argPtr, int32(len(args)))
	sdk.Write(argPtr, args)

	const cap = 256 * 1024
	buf := sdk.Alloc(cap)
	defer sdk.Free(buf, cap)
	n := stadoPTYSnapshot(uint32(argPtr), uint32(len(args)), uint32(buf), cap)
	if n < 0 {
		return nil, false, string(sdk.Bytes(buf, -n))
	}
	raw := sdk.Bytes(buf, n)
	var disc struct {
		Kind string `json:"kind"`
	}
	_ = json.Unmarshal(raw, &disc)
	if disc.Kind == "stream" {
		return nil, true, ""
	}
	cp := make([]byte, len(raw))
	copy(cp, raw)
	return cp, false, ""
}

//go:wasmexport stado_tool_signal
func stadoToolSignal(argsPtr, argsLen, resPtr, resCap int32) int32 {
	return passthroughTerminal(argsPtr, argsLen, resPtr, resCap, stadoPTYSignal, "signal")
}

//go:wasmexport stado_tool_resize
func stadoToolResize(argsPtr, argsLen, resPtr, resCap int32) int32 {
	return passthroughTerminal(argsPtr, argsLen, resPtr, resCap, stadoPTYResize, "resize")
}

//go:wasmexport stado_tool_destroy
func stadoToolDestroy(argsPtr, argsLen, resPtr, resCap int32) int32 {
	return passthroughTerminal(argsPtr, argsLen, resPtr, resCap, stadoPTYDestroy, "destroy")
}

// shell_read_until — block until a configured pattern matches against the
// session's post-output byte stream, the timeout elapses, or the
// process exits. args: {"id": uint64, "patterns": ["foo", "bar"],
//
//	"regex"?: false, "timeout_ms"?: 30000}
//
// Returns the host-supplied JSON discriminator unchanged:
//
//	match  : {"matched": true, "pattern_index": N, "before": <b64>, "match": <b64>}
//	timeout: {"matched": false, "timeout": true, "before": <b64>}
//	eof    : {"matched": false, "eof": true, "before": <b64>, "exit_code": N}
//
// `before` and `match` are base64 because PTY output routinely
// includes ANSI escapes and other non-UTF8 sequences that JSON
// strings can't carry losslessly. No attach required — access is by
// session id (EP-0043 D6).
//
//go:wasmexport stado_tool_read_until
func stadoToolReadUntil(argsPtr, argsLen, resPtr, resCap int32) int32 {
	var req struct {
		ID        uint64   `json:"id"`
		Patterns  []string `json:"patterns"`
		Regex     bool     `json:"regex"`
		TimeoutMs int      `json:"timeout_ms"`
	}
	if err := json.Unmarshal(sdk.Bytes(argsPtr, argsLen), &req); err != nil || req.ID == 0 {
		return writeErr(resPtr, resCap, "id is required")
	}
	if len(req.Patterns) == 0 {
		return writeErr(resPtr, resCap, "patterns is required")
	}

	// Repackage args for the host import — id rides on the i32 pair
	// (matching the read/write convention for sessions whose i64 id
	// can't sit alongside a payload pointer in i32 stack slots), so
	// the JSON only carries patterns / regex / timeout_ms.
	hostArgs, _ := json.Marshal(map[string]any{
		"patterns":   req.Patterns,
		"regex":      req.Regex,
		"timeout_ms": req.TimeoutMs,
	})
	argPtr := sdk.Alloc(int32(len(hostArgs)))
	defer sdk.Free(argPtr, int32(len(hostArgs)))
	sdk.Write(argPtr, hostArgs)

	const cap = 1 << 20 // 1 MiB — Expect can return arbitrarily large `before` slabs
	buf := sdk.Alloc(cap)
	defer sdk.Free(buf, cap)

	idLo := uint32(req.ID & 0xFFFFFFFF)
	idHi := uint32(req.ID >> 32)
	n := stadoPTYExpect(idLo, idHi, uint32(argPtr), uint32(len(hostArgs)), uint32(buf), cap)
	if n < 0 {
		// Negative = -byte_count of the host's error string at buf.
		errLen := -n
		if errLen > 0 && errLen <= cap {
			return writeErr(resPtr, resCap, "read_until: "+string(sdk.Bytes(buf, errLen)))
		}
		return writeErr(resPtr, resCap, "read_until failed")
	}
	return writeRaw(resPtr, resCap, sdk.Bytes(buf, n))
}

// (EP-0043: shell_screenshot is folded into shell_read mode:"screen"/"auto";
// the rendered-screen path lives in readScreen above, which calls the same
// stado_pty_snapshot host import.)

// ── helpers ────────────────────────────────────────────────────────────────

func writeErr(resPtr, resCap int32, msg string) int32 {
	b := []byte(msg)
	if int32(len(b)) > resCap {
		b = b[:resCap]
	}
	if len(b) == 0 {
		return -1
	}
	n := sdk.Write(resPtr, b)
	if n <= 0 {
		return -1
	}
	return -n
}

func writeRaw(resPtr, resCap int32, data []byte) int32 {
	if int32(len(data)) > resCap {
		return -1
	}
	return sdk.Write(resPtr, data)
}
