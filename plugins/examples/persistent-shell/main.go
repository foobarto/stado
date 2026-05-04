// persistent-shell — wraps stado's host-side PTY registry
// (internal/plugins/runtime/pty) as nine plugin tools: create, list,
// attach, detach, write, read, signal, resize, destroy.
//
// Why this exists: every other tool a wasm plugin can drive is
// stateless request→response. Real shell work — driving an `ssh`
// session, watching a `nc -lvnp` listener, running `msfconsole` step
// by step — needs interactive stdin/stdout *and* needs the process
// to keep running between tool calls. Putting the PTY registry
// host-side (one per stado runtime) gives the plugin per-session ids
// to drive across calls without itself owning any process state.
//
// Mental model:
//
//   id = shell.create({argv: ["/bin/bash"]})       // detached, output buffering
//   shell.attach({id: id})                          // claim it
//   shell.write({id: id, data: "id\n"})             // bytes in
//   bytes = shell.read({id: id, timeout_ms: 500})   // bytes out (incl. ring replay)
//   shell.detach({id: id})                          // release; PTY keeps running
//   shell.destroy({id: id})                         // SIGTERM → grace → SIGKILL
//
// Single-attach-at-a-time per session. attach({force: true}) steals
// the lock from a previous attacher (the recovery path for "subagent
// crashed without detaching"). signal/resize/destroy don't require
// attach — Ctrl-C and TUI repaint are out-of-band.
package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"unsafe"
)

func main() {}

//go:wasmimport stado stado_log
func stadoLog(levelPtr, levelLen, msgPtr, msgLen uint32)

//go:wasmimport stado stado_pty_create
func stadoPtyCreate(argsPtr, argsLen, resPtr, resCap uint32) int64

//go:wasmimport stado stado_pty_list
func stadoPtyList(bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_pty_attach
func stadoPtyAttach(argsPtr, argsLen, resPtr, resCap uint32) int32

//go:wasmimport stado stado_pty_detach
func stadoPtyDetach(argsPtr, argsLen, resPtr, resCap uint32) int32

//go:wasmimport stado stado_pty_write
func stadoPtyWrite(idLo, idHi, bufPtr, bufLen, errPtr, errCap uint32) int32

//go:wasmimport stado stado_pty_read
func stadoPtyRead(idLo, idHi, maxBytes, timeoutMs, bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_pty_signal
func stadoPtySignal(argsPtr, argsLen, resPtr, resCap uint32) int32

//go:wasmimport stado stado_pty_resize
func stadoPtyResize(argsPtr, argsLen, resPtr, resCap uint32) int32

//go:wasmimport stado stado_pty_destroy
func stadoPtyDestroy(argsPtr, argsLen, resPtr, resCap uint32) int32

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
func stadoFree(ptr int32, _ int32) {
	pinned.Delete(uintptr(ptr))
}

const (
	hostBufCap   = 1 << 20 // 1 MiB scratch for host call results
	readBufCap   = 1 << 18 // 256 KiB per read call
	stdinMaxSize = 1 << 16 // 64 KiB per write call
)

type errResult struct {
	Error string `json:"error"`
}

// ---------- shell.create ----------

type createArgs struct {
	Argv        []string `json:"argv,omitempty"`
	Cmd         string   `json:"cmd,omitempty"`
	Env         []string `json:"env,omitempty"`
	Cwd         string   `json:"cwd,omitempty"`
	Cols        uint16   `json:"cols,omitempty"`
	Rows        uint16   `json:"rows,omitempty"`
	BufferBytes int      `json:"buffer_bytes,omitempty"`
}

type createResult struct {
	ID uint64 `json:"id"`
}

//go:wasmexport stado_tool_shell_create
func stadoToolShellCreate(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	var a createArgs
	if err := decodeArgs(argsPtr, argsLen, &a); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: err.Error()})
	}
	payload, _ := json.Marshal(a)
	scratch := make([]byte, hostBufCap)
	pp, pl := ptrLen(payload)
	rc := stadoPtyCreate(
		pp, pl,
		uint32(uintptr(unsafe.Pointer(&scratch[0]))), uint32(hostBufCap),
	)
	if rc <= 0 {
		// Negative byte count: error string in scratch.
		n := -rc
		return writeJSON(resultPtr, resultCap, errResult{Error: hostErrorString(scratch, int32(n))})
	}
	logInfo("shell.create id=" + uint64ToStr(uint64(rc)))
	return writeJSON(resultPtr, resultCap, createResult{ID: uint64(rc)})
}

// ---------- shell.list ----------

//go:wasmexport stado_tool_shell_list
func stadoToolShellList(_, _, resultPtr, resultCap int32) int32 {
	scratch := make([]byte, hostBufCap)
	rc := stadoPtyList(
		uint32(uintptr(unsafe.Pointer(&scratch[0]))), uint32(hostBufCap),
	)
	if rc < 0 {
		return writeJSON(resultPtr, resultCap, errResult{Error: hostErrorString(scratch, -rc)})
	}
	// Pass through the host JSON unchanged — it's already a
	// SessionInfo array.
	return copyBytes(resultPtr, resultCap, scratch[:rc])
}

// ---------- shell.attach ----------

type attachArgs struct {
	ID    uint64 `json:"id"`
	Force bool   `json:"force,omitempty"`
}

type okResult struct {
	OK bool `json:"ok"`
}

//go:wasmexport stado_tool_shell_attach
func stadoToolShellAttach(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	var a attachArgs
	if err := decodeArgs(argsPtr, argsLen, &a); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: err.Error()})
	}
	return runHostJSONOp(stadoPtyAttach, a, resultPtr, resultCap)
}

// ---------- shell.detach ----------

type detachArgs struct {
	ID uint64 `json:"id"`
}

//go:wasmexport stado_tool_shell_detach
func stadoToolShellDetach(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	var a detachArgs
	if err := decodeArgs(argsPtr, argsLen, &a); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: err.Error()})
	}
	return runHostJSONOp(stadoPtyDetach, a, resultPtr, resultCap)
}

// ---------- shell.write ----------
//
// Input: id + base64-encoded data. Base64 is non-negotiable for JSON
// transport — raw binary doesn't survive the JSON Schema gate, and
// agents will sometimes stuff non-printable bytes (Ctrl-C, TUI escape
// sequences) into the stream.

type writeArgs struct {
	ID       uint64 `json:"id"`
	Data     string `json:"data,omitempty"`         // plain UTF-8 (newlines preserved)
	DataB64  string `json:"data_b64,omitempty"`     // for raw bytes
}

type writeResult struct {
	N int32 `json:"n"`
}

//go:wasmexport stado_tool_shell_write
func stadoToolShellWrite(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	var a writeArgs
	if err := decodeArgs(argsPtr, argsLen, &a); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: err.Error()})
	}
	var data []byte
	switch {
	case a.DataB64 != "":
		raw, err := base64.StdEncoding.DecodeString(a.DataB64)
		if err != nil {
			return writeJSON(resultPtr, resultCap, errResult{Error: "data_b64 invalid: " + err.Error()})
		}
		data = raw
	case a.Data != "":
		data = []byte(a.Data)
	default:
		return writeJSON(resultPtr, resultCap, errResult{Error: "shell.write: data or data_b64 required"})
	}
	if len(data) > stdinMaxSize {
		return writeJSON(resultPtr, resultCap, errResult{Error: "shell.write: data too large"})
	}
	scratch := make([]byte, 4096)
	idLo, idHi := splitID(a.ID)
	dp, dl := ptrLen(data)
	rc := stadoPtyWrite(
		idLo, idHi,
		dp, dl,
		uint32(uintptr(unsafe.Pointer(&scratch[0]))), uint32(len(scratch)),
	)
	if rc < 0 {
		return writeJSON(resultPtr, resultCap, errResult{Error: hostErrorString(scratch, -rc)})
	}
	return writeJSON(resultPtr, resultCap, writeResult{N: rc})
}

// ---------- shell.read ----------

type readArgs struct {
	ID        uint64 `json:"id"`
	MaxBytes  uint32 `json:"max_bytes,omitempty"`
	TimeoutMs uint32 `json:"timeout_ms,omitempty"`
}

type readResult struct {
	Data    string `json:"data,omitempty"`
	DataB64 string `json:"data_b64"`
	N       int32  `json:"n"`
	EOF     bool   `json:"eof,omitempty"`
}

//go:wasmexport stado_tool_shell_read
func stadoToolShellRead(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	var a readArgs
	if err := decodeArgs(argsPtr, argsLen, &a); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: err.Error()})
	}
	maxBytes := a.MaxBytes
	if maxBytes == 0 || maxBytes > readBufCap {
		maxBytes = readBufCap
	}
	scratch := make([]byte, readBufCap)
	idLo, idHi := splitID(a.ID)
	rc := stadoPtyRead(
		idLo, idHi,
		maxBytes, a.TimeoutMs,
		uint32(uintptr(unsafe.Pointer(&scratch[0]))), uint32(readBufCap),
	)
	if rc == -1 {
		// EOF — session closed and ring drained.
		return writeJSON(resultPtr, resultCap, readResult{EOF: true, N: 0})
	}
	if rc < 0 {
		return writeJSON(resultPtr, resultCap, errResult{Error: hostErrorString(scratch, -rc)})
	}
	if rc == 0 {
		return writeJSON(resultPtr, resultCap, readResult{N: 0})
	}
	got := scratch[:rc]
	res := readResult{N: rc, DataB64: base64.StdEncoding.EncodeToString(got)}
	if isPlainText(got) {
		res.Data = string(got)
	}
	return writeJSON(resultPtr, resultCap, res)
}

// ---------- shell.signal ----------

type signalArgs struct {
	ID  uint64 `json:"id"`
	Sig int    `json:"sig"`
}

//go:wasmexport stado_tool_shell_signal
func stadoToolShellSignal(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	var a signalArgs
	if err := decodeArgs(argsPtr, argsLen, &a); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: err.Error()})
	}
	if a.Sig == 0 {
		a.Sig = 15 // SIGTERM
	}
	return runHostJSONOp(stadoPtySignal, a, resultPtr, resultCap)
}

// ---------- shell.resize ----------

type resizeArgs struct {
	ID   uint64 `json:"id"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

//go:wasmexport stado_tool_shell_resize
func stadoToolShellResize(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	var a resizeArgs
	if err := decodeArgs(argsPtr, argsLen, &a); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: err.Error()})
	}
	return runHostJSONOp(stadoPtyResize, a, resultPtr, resultCap)
}

// ---------- shell.destroy ----------

type destroyArgs struct {
	ID uint64 `json:"id"`
}

//go:wasmexport stado_tool_shell_destroy
func stadoToolShellDestroy(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	var a destroyArgs
	if err := decodeArgs(argsPtr, argsLen, &a); err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: err.Error()})
	}
	return runHostJSONOp(stadoPtyDestroy, a, resultPtr, resultCap)
}

// ---------- helpers ----------

func decodeArgs(ptr, length int32, dst any) error {
	if length <= 0 {
		return nil
	}
	args := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), int(length))
	return json.Unmarshal(args, dst)
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

func copyBytes(resultPtr, resultCap int32, payload []byte) int32 {
	if int32(len(payload)) > resultCap {
		return -1
	}
	dst := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(resultPtr))), int(resultCap))
	copy(dst, payload)
	return int32(len(payload))
}

func ptrLen(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

// runHostJSONOp dispatches a JSON-args / int32-result host import
// where 0 = success and -length = error-string-in-result.
func runHostJSONOp(call func(uint32, uint32, uint32, uint32) int32, payload any, resultPtr, resultCap int32) int32 {
	body, err := json.Marshal(payload)
	if err != nil {
		return writeJSON(resultPtr, resultCap, errResult{Error: err.Error()})
	}
	scratch := make([]byte, 4096)
	bp, bl := ptrLen(body)
	rc := call(bp, bl,
		uint32(uintptr(unsafe.Pointer(&scratch[0]))), uint32(len(scratch)),
	)
	if rc < 0 {
		return writeJSON(resultPtr, resultCap, errResult{Error: hostErrorString(scratch, -rc)})
	}
	return writeJSON(resultPtr, resultCap, okResult{OK: true})
}

func hostErrorString(scratch []byte, n int32) string {
	if n <= 0 || int(n) > len(scratch) {
		return "host import returned error with no diagnostic"
	}
	return string(scratch[:n])
}

// splitID packs a uint64 into (lo, hi) for the wasm32 host-import ABI
// (each parameter is i32; we fold the 64-bit id over two slots).
func splitID(id uint64) (uint32, uint32) {
	return uint32(id & 0xFFFFFFFF), uint32(id >> 32)
}

func isPlainText(b []byte) bool {
	for _, c := range b {
		if c == '\n' || c == '\r' || c == '\t' || (c >= 0x20 && c < 0x7f) {
			continue
		}
		// UTF-8 lead bytes — leave decoding to the caller; treat as
		// plain so they get a usable .data field.
		if c >= 0x80 {
			continue
		}
		return false
	}
	return true
}

func uint64ToStr(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// silence unused-import warning when no string-helpers used.
var _ = strings.Contains
