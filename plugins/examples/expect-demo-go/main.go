// expect-demo-go — minimal example of the stado_terminal_expect
// primitive. One tool, no args: spawns a shell that prints a prompt,
// waits for input, echoes a marker, then exits. The demo drives that
// session via expect → write → expect → close, returning a summary of
// what each step saw. Designed to be ~80 LOC of demo on top of the
// usual wasm boilerplate.
//
// Plugin authors copy this shape when they need to script a real
// interactive program (REPL, ssh prompt, msfconsole). The bundled
// `shell.expect` tool exposes the same primitive without writing a
// plugin; this demo exists so authors can see the end-to-end glue.
package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"unsafe"
)

func main() {}

//go:wasmimport stado stado_terminal_open
func stadoTerminalOpen(argsPtr, argsLen, resPtr, resCap uint32) int64

//go:wasmimport stado stado_terminal_attach
func stadoTerminalAttach(argsPtr, argsLen, resPtr, resCap uint32) int32

//go:wasmimport stado stado_terminal_write
func stadoTerminalWrite(idLo, idHi, bufPtr, bufLen, errPtr, errCap uint32) int32

//go:wasmimport stado stado_terminal_expect
func stadoTerminalExpect(idLo, idHi, argsPtr, argsLen, resPtr, resCap uint32) int32

//go:wasmimport stado stado_terminal_close
func stadoTerminalClose(argsPtr, argsLen, resPtr, resCap uint32) int32

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

const scratchCap = 1 << 20 // 1 MiB result buffer; expect.before can grow

type expectResult struct {
	Matched      bool   `json:"matched"`
	PatternIndex int    `json:"pattern_index,omitempty"`
	Before       string `json:"before"` // base64
	Match        string `json:"match,omitempty"`
	Timeout      bool   `json:"timeout,omitempty"`
	EOF          bool   `json:"eof,omitempty"`
	ExitCode     int    `json:"exit_code,omitempty"`
}

type stepReport struct {
	Step    string `json:"step"`
	Matched bool   `json:"matched"`
	Match   string `json:"match,omitempty"` // decoded UTF-8 view
	Note    string `json:"note,omitempty"`
}

//go:wasmexport stado_tool_expect_demo
func stadoToolExpectDemo(_, _, resPtr, resCap int32) int32 {
	const cmd = `printf 'DEMO> '; read -r line; echo got=$line; exit 0`

	id, err := openSession([]string{"/bin/bash", "-c", cmd})
	if err != nil {
		return writeErr(resPtr, resCap, "spawn: "+err.Error())
	}
	defer closeSession(id)

	if err := attachSession(id); err != nil {
		return writeErr(resPtr, resCap, "attach: "+err.Error())
	}

	steps := []stepReport{}

	prompt, err := expectPattern(id, []string{"DEMO> "}, false, 2000)
	if err != nil {
		return writeErr(resPtr, resCap, "expect prompt: "+err.Error())
	}
	steps = append(steps, reportStep("expect prompt", prompt))

	if _, err := writeSession(id, "hello\n"); err != nil {
		return writeErr(resPtr, resCap, "write: "+err.Error())
	}

	echo, err := expectPattern(id, []string{`got=hello`}, false, 2000)
	if err != nil {
		return writeErr(resPtr, resCap, "expect echo: "+err.Error())
	}
	steps = append(steps, reportStep("expect echo", echo))

	tail, err := expectPattern(id, []string{"never"}, false, 500)
	if err != nil {
		return writeErr(resPtr, resCap, "expect EOF: "+err.Error())
	}
	steps = append(steps, reportStep("expect after exit", tail))

	out, _ := json.Marshal(map[string]any{
		"summary": "spawn → expect prompt → write 'hello' → expect echo → wait for EOF",
		"steps":   steps,
	})
	return writeRaw(resPtr, resCap, out)
}

func reportStep(name string, r expectResult) stepReport {
	rep := stepReport{Step: name, Matched: r.Matched}
	switch {
	case r.Matched:
		raw, _ := base64.StdEncoding.DecodeString(r.Match)
		rep.Match = string(raw)
	case r.Timeout:
		rep.Note = "timeout"
	case r.EOF:
		rep.Note = "eof exit_code=" + itoa(r.ExitCode)
	}
	return rep
}

func openSession(argv []string) (uint64, error) {
	args, _ := json.Marshal(map[string]any{"argv": argv})
	scratch := make([]byte, 4096)
	rc := stadoTerminalOpen(ptr(args), uint32(len(args)), ptr(scratch), uint32(len(scratch)))
	if rc <= 0 {
		return 0, hostErr(scratch, int32(-rc))
	}
	return uint64(rc), nil
}

func attachSession(id uint64) error {
	args, _ := json.Marshal(map[string]any{"id": id})
	scratch := make([]byte, 4096)
	rc := stadoTerminalAttach(ptr(args), uint32(len(args)), ptr(scratch), uint32(len(scratch)))
	if rc < 0 {
		return hostErr(scratch, -rc)
	}
	return nil
}

func writeSession(id uint64, text string) (int32, error) {
	data := []byte(text)
	scratch := make([]byte, 1024)
	idLo := uint32(id & 0xFFFFFFFF)
	idHi := uint32(id >> 32)
	rc := stadoTerminalWrite(idLo, idHi, ptr(data), uint32(len(data)), ptr(scratch), uint32(len(scratch)))
	if rc < 0 {
		return 0, hostErr(scratch, -rc)
	}
	return rc, nil
}

func expectPattern(id uint64, patterns []string, regex bool, timeoutMs int) (expectResult, error) {
	args, _ := json.Marshal(map[string]any{
		"patterns":   patterns,
		"regex":      regex,
		"timeout_ms": timeoutMs,
	})
	scratch := make([]byte, scratchCap)
	idLo := uint32(id & 0xFFFFFFFF)
	idHi := uint32(id >> 32)
	rc := stadoTerminalExpect(idLo, idHi, ptr(args), uint32(len(args)), ptr(scratch), uint32(len(scratch)))
	if rc < 0 {
		return expectResult{}, hostErr(scratch, -rc)
	}
	var res expectResult
	if err := json.Unmarshal(scratch[:rc], &res); err != nil {
		return expectResult{}, err
	}
	return res, nil
}

func closeSession(id uint64) {
	args, _ := json.Marshal(map[string]any{"id": id})
	scratch := make([]byte, 256)
	stadoTerminalClose(ptr(args), uint32(len(args)), ptr(scratch), uint32(len(scratch)))
}

func ptr(b []byte) uint32 {
	if len(b) == 0 {
		return 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0])))
}

func hostErr(scratch []byte, n int32) error {
	if n <= 0 || int(n) > len(scratch) {
		return errString("host error (no diagnostic)")
	}
	return errString(string(scratch[:n]))
}

type errString string

func (e errString) Error() string { return string(e) }

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func writeRaw(resPtr, resCap int32, data []byte) int32 {
	if int32(len(data)) > resCap {
		return -1
	}
	dst := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(resPtr))), int(resCap))
	copy(dst, data)
	return int32(len(data))
}

func writeErr(resPtr, resCap int32, msg string) int32 {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return writeRaw(resPtr, resCap, b)
}

// silence unused-import warning if helpers shrink.
var _ = strings.Contains
