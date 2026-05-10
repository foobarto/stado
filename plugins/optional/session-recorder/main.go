// session-recorder — second validating plugin for Phase 7.1b. Shows
// that the session/LLM capability set isn't shaped only for
// auto-compaction: different plugin, same ABI, different capability
// mix.
//
// Combines:
//
//   session:read       → read token_count, message_count, session_id, last_turn_ref
//   fs:read:.stado     → read the prior recording file
//   fs:write:.stado    → write the appended recording file
//   (no llm:invoke, no session:fork)
//
// Both invocation modes supported:
//
//   one-shot tool: `/plugin:session-recorder-0.1.0 snapshot` captures
//                  the current session state as a single line.
//
//   background:    add the plugin ID to [plugins].background and the
//                  tick fires on every turn boundary, emitting one
//                  JSONL line per turn into
//                  `<worktree>/.stado/session-recordings.jsonl`.
//
// The recording file is an append-only JSONL log — one object per
// line — suitable for `jq -s '.[]'` or stream processing.
package main

import (
	"encoding/json"
	"strconv"
	"sync"
	"time"
	"unsafe"
)

func main() {}

// ---- host imports -----------------------------------------------------

//go:wasmimport stado stado_log
func stadoLog(levelPtr, levelLen, msgPtr, msgLen uint32)

//go:wasmimport stado stado_session_read
func stadoSessionRead(fieldPtr, fieldLen, bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_fs_read
func stadoFSRead(pathPtr, pathLen, bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_fs_write
func stadoFSWrite(pathPtr, pathLen, bufPtr, bufLen uint32) int32

// ---- helpers ----------------------------------------------------------

func logAt(level, msg string) {
	lb := []byte(level)
	mb := []byte(msg)
	stadoLog(
		uint32(uintptr(unsafe.Pointer(&lb[0]))), uint32(len(lb)),
		uint32(uintptr(unsafe.Pointer(&mb[0]))), uint32(len(mb)),
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

func readField(name string) []byte {
	nb := []byte(name)
	buf := make([]byte, 64*1024)
	n := stadoSessionRead(
		uint32(uintptr(unsafe.Pointer(&nb[0]))), uint32(len(nb)),
		uint32(uintptr(unsafe.Pointer(&buf[0]))), uint32(len(buf)),
	)
	if n < 0 {
		return nil
	}
	return buf[:n]
}

// readFile calls stado_fs_read. Returns bytes on success, nil when the
// file doesn't exist (first-run case) or the host denies the read.
// Callers treat nil as "start fresh".
func readFile(path string) []byte {
	pb := []byte(path)
	buf := make([]byte, 256*1024)
	n := stadoFSRead(
		uint32(uintptr(unsafe.Pointer(&pb[0]))), uint32(len(pb)),
		uint32(uintptr(unsafe.Pointer(&buf[0]))), uint32(len(buf)),
	)
	if n < 0 {
		return nil
	}
	return buf[:n]
}

// writeFile calls stado_fs_write. Returns true on success. Host
// truncates existing content, so callers must supply the full desired
// payload (we read-prior + append + write-whole).
func writeFile(path string, data []byte) bool {
	pb := []byte(path)
	var dataPtr, dataLen uint32
	if len(data) > 0 {
		dataPtr = uint32(uintptr(unsafe.Pointer(&data[0])))
		dataLen = uint32(len(data))
	}
	n := stadoFSWrite(
		uint32(uintptr(unsafe.Pointer(&pb[0]))), uint32(len(pb)),
		dataPtr, dataLen,
	)
	return n >= 0
}

// ---- recording shape --------------------------------------------------

const recordingPath = ".stado/session-recordings.jsonl"

type recording struct {
	Timestamp   string `json:"ts"`
	Kind        string `json:"kind"` // "tick" | "snapshot"
	SessionID   string `json:"session_id,omitempty"`
	TokenCount  int    `json:"tokens"`
	MessageCnt  int    `json:"messages"`
	LastTurnRef string `json:"last_turn_ref,omitempty"`
	Note        string `json:"note,omitempty"`
}

// captureRecording pulls the current session state into a recording
// struct. Never errors — missing fields become zero values so the
// JSONL line still writes and the operator can see what was missing.
func captureRecording(kind, note string) recording {
	tok, _ := strconv.Atoi(string(readField("token_count")))
	msg, _ := strconv.Atoi(string(readField("message_count")))
	return recording{
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		Kind:        kind,
		SessionID:   string(readField("session_id")),
		TokenCount:  tok,
		MessageCnt:  msg,
		LastTurnRef: string(readField("last_turn_ref")),
		Note:        note,
	}
}

// appendRecording serialises r into one JSONL line and appends it to
// the on-disk log. Since stado_fs_write truncates, the pattern is
// read-prior → append → write-whole. Returns "" on success or a
// short error message the caller can surface in its JSON result.
func appendRecording(r recording) string {
	line, err := json.Marshal(r)
	if err != nil {
		return "marshal failed"
	}
	line = append(line, '\n')
	prior := readFile(recordingPath) // nil on first run — that's fine
	combined := append(prior, line...)
	if !writeFile(recordingPath, combined) {
		return "fs:write denied or failed"
	}
	return ""
}

// ---- tool: snapshot ---------------------------------------------------

type snapshotArgs struct {
	Note string `json:"note"`
}

type snapshotResult struct {
	Status    string `json:"status"`          // "ok" | "error"
	Reason    string `json:"reason,omitempty"`
	Path      string `json:"path,omitempty"`
	Recorded  int    `json:"recorded_bytes,omitempty"`
}

//go:wasmexport stado_tool_snapshot
func stadoToolSnapshot(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	var a snapshotArgs
	if argsLen > 0 {
		args := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(argsPtr))), int(argsLen))
		_ = json.Unmarshal(args, &a) // ignore bad JSON; note is optional
	}

	r := captureRecording("snapshot", a.Note)
	logAt("info", "session-recorder: snapshot tokens="+strconv.Itoa(r.TokenCount)+
		" messages="+strconv.Itoa(r.MessageCnt))

	if reason := appendRecording(r); reason != "" {
		return writeSnapshotResult(resultPtr, resultCap, snapshotResult{
			Status: "error", Reason: reason,
		})
	}
	line, _ := json.Marshal(r)
	return writeSnapshotResult(resultPtr, resultCap, snapshotResult{
		Status:   "ok",
		Path:     recordingPath,
		Recorded: len(line) + 1, // +newline
	})
}

// stado_plugin_tick — background-lifecycle entry point. Fires once per
// turn boundary when the plugin ID is listed in [plugins].background.
// Silently appends one "tick" recording per turn. Unlike the one-shot
// snapshot tool, the tick can't return structured results to a caller —
// errors just land in the host's stderr via stado_log.
//
// Returns 0 (continue) unconditionally: a recorder that stops
// recording on one failed write would leave silent gaps in the log.
// Persistent failures are a signal for the operator to check the
// stderr log, not a reason to unregister.
//
//go:wasmexport stado_plugin_tick
func stadoPluginTick() int32 {
	r := captureRecording("tick", "")
	if reason := appendRecording(r); reason != "" {
		logAt("warn", "session-recorder: tick append failed: "+reason)
		return 0
	}
	return 0
}

// writeSnapshotResult serialises r into the host's result buffer.
// Matches the auto-compact plugin's writeResult shape so the two
// examples look consistent.
func writeSnapshotResult(resultPtr, resultCap int32, r snapshotResult) int32 {
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
