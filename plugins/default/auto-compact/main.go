// auto-compact — Phase 7.1b validating plugin. Exercises all four
// session/LLM host imports end-to-end:
//
//	session:observe    → poll background-plugin lifecycle events
//	session:read       → read token_count, history, last_turn_ref
//	llm:invoke:30000   → ask the active model to summarise the history
//	session:fork       → fork at the last turn with the summary as seed
//
// Invocation pattern (matches DESIGN §"Plugin extension points for
// context management" — fork-as-recovery, not in-place rewrite):
//
//	/plugin:auto-compact-<version> compact '{"threshold_tokens": 10000}'
//	→ {"status":"skipped","tokens":4200,"threshold":10000}     (under threshold)
//	→ {"status":"compacted","child":"<uuid>","tokens_used":N}  (forked)
//
// The user sees the fork notification in the TUI and can attach to
// the new session via `stado session attach <child>`.
package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"unsafe"
)

func main() {}

// ---- host imports -----------------------------------------------------

//go:wasmimport stado stado_log
func stadoLog(levelPtr, levelLen, msgPtr, msgLen uint32)

//go:wasmimport stado stado_session_read
func stadoSessionRead(fieldPtr, fieldLen, bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_session_next_event
func stadoSessionNextEvent(bufPtr, bufCap uint32) int32

//go:wasmimport stado stado_session_fork
func stadoSessionFork(atPtr, atLen, seedPtr, seedLen, outPtr, outCap uint32) int32

//go:wasmimport stado stado_llm_invoke
func stadoLLMInvoke(promptPtr, promptLen, outPtr, outCap uint32) int32

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

// readField calls stado_session_read. Returns bytes on success,
// nil on host error (capability denied, no session, unknown field).
func readField(name string) []byte {
	nb := []byte(name)
	buf := make([]byte, 256*1024) // enough for most history payloads
	n := stadoSessionRead(
		uint32(uintptr(unsafe.Pointer(&nb[0]))), uint32(len(nb)),
		uint32(uintptr(unsafe.Pointer(&buf[0]))), uint32(len(buf)),
	)
	if n < 0 {
		return nil
	}
	return buf[:n]
}

// invokeLLM calls stado_llm_invoke with prompt. Returns reply bytes
// or nil when the host denied / budget exceeded / provider errored.
func invokeLLM(prompt string) []byte {
	pb := []byte(prompt)
	buf := make([]byte, 128*1024) // 128 KiB is more than enough for a summary
	n := stadoLLMInvoke(
		uint32(uintptr(unsafe.Pointer(&pb[0]))), uint32(len(pb)),
		uint32(uintptr(unsafe.Pointer(&buf[0]))), uint32(len(buf)),
	)
	if n < 0 {
		return nil
	}
	return buf[:n]
}

// forkAt calls stado_session_fork. Returns the new session ID or ""
// when the host denied or the fork itself failed.
func forkAt(atTurnRef, seed string) string {
	ab := []byte(atTurnRef)
	sb := []byte(seed)
	buf := make([]byte, 256)
	var atPtr, atLen uint32
	if len(ab) > 0 {
		atPtr = uint32(uintptr(unsafe.Pointer(&ab[0])))
		atLen = uint32(len(ab))
	}
	var seedPtr, seedLen uint32
	if len(sb) > 0 {
		seedPtr = uint32(uintptr(unsafe.Pointer(&sb[0])))
		seedLen = uint32(len(sb))
	}
	n := stadoSessionFork(
		atPtr, atLen, seedPtr, seedLen,
		uint32(uintptr(unsafe.Pointer(&buf[0]))), uint32(len(buf)),
	)
	if n < 0 {
		return ""
	}
	return string(buf[:n])
}

func nextEvent() []byte {
	buf := make([]byte, 16*1024)
	n := stadoSessionNextEvent(
		uint32(uintptr(unsafe.Pointer(&buf[0]))),
		uint32(len(buf)),
	)
	if n <= 0 {
		return nil
	}
	return buf[:n]
}

// ---- tool: compact ----------------------------------------------------

type compactArgs struct {
	ThresholdTokens int `json:"threshold_tokens"`
}

type compactResult struct {
	Status      string `json:"status"`           // "skipped" | "compacted" | "error"
	Reason      string `json:"reason,omitempty"` // set on skipped / error
	Tokens      int    `json:"tokens,omitempty"` // current session token count
	Threshold   int    `json:"threshold,omitempty"`
	Child       string `json:"child,omitempty"`          // forked session id on success
	SummaryLen  int    `json:"summary_length,omitempty"` // length in bytes of the summary
	LastTurnRef string `json:"last_turn_ref,omitempty"`
}

const defaultThreshold = 10000

type sessionEvent struct {
	Kind string `json:"kind"`
}

// runCompact is the shared compaction pipeline called by both the
// one-shot tool and the background tick. Returns a compactResult
// describing what happened (or why nothing happened).
func runCompact(threshold int) compactResult {
	return runCompactMode(threshold, false)
}

func runCompactForced() compactResult {
	return runCompactMode(defaultThreshold, true)
}

func runCompactMode(threshold int, force bool) compactResult {
	if threshold <= 0 {
		threshold = defaultThreshold
	}
	logAt("info", fmt.Sprintf("auto-compact: threshold=%d tokens", threshold))

	tokStr := readField("token_count")
	if tokStr == nil {
		return compactResult{Status: "error", Reason: "session:read denied or no session attached"}
	}
	tokens, _ := strconv.Atoi(string(tokStr))
	if !force && tokens < threshold {
		logAt("info", fmt.Sprintf("auto-compact: %d < %d, skipping", tokens, threshold))
		return compactResult{
			Status:    "skipped",
			Reason:    "below threshold",
			Tokens:    tokens,
			Threshold: threshold,
		}
	}

	history := readField("history")
	if history == nil {
		return compactResult{Status: "error", Reason: "read history failed"}
	}
	lastTurn := string(readField("last_turn_ref"))

	prompt := buildSummarisePrompt(history)
	logAt("info", fmt.Sprintf("auto-compact: summarising %d bytes of history", len(history)))

	reply := invokeLLM(prompt)
	if reply == nil {
		return compactResult{Status: "error", Reason: "llm:invoke denied / failed / budget exhausted"}
	}

	child := forkAt(lastTurn, string(reply))
	if child == "" {
		return compactResult{Status: "error", Reason: "session:fork denied / failed"}
	}

	logAt("info", "auto-compact: forked child "+child)
	return compactResult{
		Status:      "compacted",
		Tokens:      tokens,
		Threshold:   threshold,
		Child:       child,
		SummaryLen:  len(reply),
		LastTurnRef: lastTurn,
	}
}

//go:wasmexport stado_tool_compact
func stadoToolCompact(argsPtr, argsLen, resultPtr, resultCap int32) int32 {
	// Parse args — threshold_tokens optional, falls back to 10K.
	var a compactArgs
	if argsLen > 0 {
		args := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(argsPtr))), int(argsLen))
		if err := json.Unmarshal(args, &a); err != nil {
			return writeResult(resultPtr, resultCap, compactResult{
				Status: "error", Reason: "bad args json: " + err.Error(),
			})
		}
	}
	return writeResult(resultPtr, resultCap, runCompact(a.ThresholdTokens))
}

// stado_plugin_tick — background-lifecycle entry point. Called once
// per turn boundary by the host. Runs the same compaction pipeline
// with the default threshold; results are logged via stado_log but
// not returned to any caller (the host doesn't see compaction
// metadata at tick time). Always returns 0 (continue) — a production
// plugin might unregister after N consecutive errors, but for the
// demo we keep ticking so the user sees the behaviour across turns.
//
//go:wasmexport stado_plugin_tick
func stadoPluginTick() int32 {
	force := false
	shouldCompact := false
	for i := 0; i < 16; i++ {
		raw := nextEvent()
		if len(raw) == 0 {
			break
		}
		var ev sessionEvent
		if err := json.Unmarshal(raw, &ev); err != nil {
			logAt("warn", "auto-compact: bad session event: "+err.Error())
			continue
		}
		switch ev.Kind {
		case "context_overflow":
			force = true
			shouldCompact = true
		case "turn_complete":
			if !force {
				shouldCompact = true
			}
		}
	}
	if !shouldCompact {
		return 0
	}

	r := runCompact(defaultThreshold)
	if force {
		r = runCompactForced()
	}
	switch r.Status {
	case "skipped":
		// already logged inside runCompact; nothing to add
	case "compacted":
		logAt("info", fmt.Sprintf("auto-compact: tick compacted → child=%s summary=%dB",
			r.Child, r.SummaryLen))
	case "error":
		logAt("warn", "auto-compact: tick error: "+r.Reason)
	}
	return 0
}

// buildSummarisePrompt embeds history JSON into a compaction prompt.
// The prompt nudges the model toward a focused summary that's useful
// as the seed for a fresh conversation — stado's DESIGN says the
// summary becomes the first message of the child session.
func buildSummarisePrompt(history []byte) string {
	return `You are a conversation-summarisation tool for stado, a sandboxed
coding agent. The user has accumulated enough context that the session
should be compacted. Your output will seed a forked session — it becomes
the first user-turn the next conversation sees.

Produce a focused summary that captures:
- The user's overall goal in the original session
- Key decisions made (architecture, libraries, tradeoffs)
- Open questions or next steps
- Any file paths, line numbers, or identifiers the next session needs
  to pick up cleanly

Be terse. Use bullet points. Do not narrate "we discussed X, then Y" —
the reader needs actionable state, not a timeline. Omit backstory that
a fresh reader wouldn't need to make progress.

Here is the conversation history (JSON array of {role, text}):

` + string(history) + `

Summary:`
}

// writeResult encodes r into the host-provided result buffer. Returns
// bytes written, or -1 if the payload overflows resultCap.
func writeResult(resultPtr, resultCap int32, r compactResult) int32 {
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
