// auto-compact — Phase 7.1b validating plugin. Exercises all four
// session/LLM host imports end-to-end:
//
//   session:read       → read token_count, history, last_turn_ref
//   llm:invoke:30000   → ask the active model to summarise the history
//   session:fork       → fork at the last turn with the summary as seed
//   (session:observe)  → not used in this one-shot variant; a future
//                         continuously-running version would subscribe to
//                         turn-boundary events
//
// Invocation pattern (matches DESIGN §"Plugin extension points for
// context management" — fork-as-recovery, not in-place rewrite):
//
//   /plugin:auto-compact-0.1.0 compact '{"threshold_tokens": 10000}'
//   → {"status":"skipped","tokens":4200,"threshold":10000}     (under threshold)
//   → {"status":"compacted","child":"<uuid>","tokens_used":N}  (forked)
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

// ---- tool: compact ----------------------------------------------------

type compactArgs struct {
	ThresholdTokens int `json:"threshold_tokens"`
}

type compactResult struct {
	Status        string `json:"status"`                    // "skipped" | "compacted" | "error"
	Reason        string `json:"reason,omitempty"`          // set on skipped / error
	Tokens        int    `json:"tokens,omitempty"`          // current session token count
	Threshold     int    `json:"threshold,omitempty"`
	Child         string `json:"child,omitempty"`           // forked session id on success
	SummaryLen    int    `json:"summary_length,omitempty"`  // length in bytes of the summary
	LastTurnRef   string `json:"last_turn_ref,omitempty"`
}

const defaultThreshold = 10000

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
	if a.ThresholdTokens <= 0 {
		a.ThresholdTokens = defaultThreshold
	}

	logAt("info", fmt.Sprintf("auto-compact: threshold=%d tokens", a.ThresholdTokens))

	// 1. Token count — if host denies session:read, bail early.
	tokStr := readField("token_count")
	if tokStr == nil {
		return writeResult(resultPtr, resultCap, compactResult{
			Status: "error", Reason: "session:read denied or no session attached",
		})
	}
	tokens, _ := strconv.Atoi(string(tokStr))
	if tokens < a.ThresholdTokens {
		logAt("info", fmt.Sprintf("auto-compact: %d < %d, skipping", tokens, a.ThresholdTokens))
		return writeResult(resultPtr, resultCap, compactResult{
			Status:    "skipped",
			Reason:    "below threshold",
			Tokens:    tokens,
			Threshold: a.ThresholdTokens,
		})
	}

	// 2. History + last turn ref for the summarisation input + fork point.
	history := readField("history")
	if history == nil {
		return writeResult(resultPtr, resultCap, compactResult{
			Status: "error", Reason: "read history failed",
		})
	}
	lastTurn := string(readField("last_turn_ref"))

	// 3. Ask the LLM for a summary. The prompt keeps stado's DESIGN
	//    invariants visible so the model knows the output will seed
	//    a forked session's first turn, not rewrite history.
	prompt := buildSummarisePrompt(history)
	logAt("info", fmt.Sprintf("auto-compact: summarising %d bytes of history", len(history)))

	reply := invokeLLM(prompt)
	if reply == nil {
		return writeResult(resultPtr, resultCap, compactResult{
			Status: "error", Reason: "llm:invoke denied / failed / budget exhausted",
		})
	}

	// 4. Fork with the summary as seed. Empty atTurnRef = parent tree
	//    HEAD (more common in practice since the fork point is
	//    conceptually "now"); pass last_turn_ref to root at that tag.
	atRef := lastTurn
	child := forkAt(atRef, string(reply))
	if child == "" {
		return writeResult(resultPtr, resultCap, compactResult{
			Status: "error", Reason: "session:fork denied / failed",
		})
	}

	logAt("info", "auto-compact: forked child "+child)
	return writeResult(resultPtr, resultCap, compactResult{
		Status:      "compacted",
		Tokens:      tokens,
		Threshold:   a.ThresholdTokens,
		Child:       child,
		SummaryLen:  len(reply),
		LastTurnRef: lastTurn,
	})
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
