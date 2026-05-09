# shell.expect — read-until-pattern PTY primitive

## What ships

A new bundled PTY tool, `shell.expect`, that reads from an
existing PTY session until one of: a configured pattern matches,
a timeout elapses, the underlying process exits, or the session
is destroyed. Returns which pattern matched, the bytes that
arrived before the match, and the matched bytes themselves.
Unconsumed bytes (the "after-match" tail) stay in the session's
ring buffer for subsequent `shell.read` / `shell.expect` calls.

Replaces the current model loop of `shell.read with timeout →
substring-check the bytes → loop` (typically 3-7 turns per
prompt-wait) with a single tool call. Closest prior art:
Tcl/POSIX `expect(1)`'s `expect <pattern>` form (without the
script DSL — just the primitive).

Wire shape:

```jsonc
{
  "id":         uint64,        // session ID from shell.spawn
  "patterns":   ["password:", "$ "],   // 1..16 patterns
  "regex":      false,         // optional; when true, patterns are RE2
  "timeout_ms": 30000          // absolute time budget; 0 = check buffer only
}
```

Response:

```jsonc
// Match
{
  "matched":       true,
  "pattern_index": 0,           // which patterns[i] matched
  "before":        "<bytes received before the match>",   // base64
  "match":         "<the matched bytes>"                  // base64
}

// Timeout
{
  "matched": false,
  "timeout": true,
  "before":  "<all bytes seen so far>"                    // base64
}

// EOF (process exited)
{
  "matched":   false,
  "eof":       true,
  "before":    "<bytes seen so far>",                     // base64
  "exit_code": 0
}
```

`before` and `match` are base64 because PTY bytes routinely
include non-UTF8 sequences (mostly ANSI escape codes for
TUI sessions); JSON-string encoding would corrupt them.

## Acceptance criteria

- [ ] **AC1.** `shell.expect` registered in
  `internal/runtime/tool_metadata.go` under the `shell` plugin
  alongside the other PTY tools, ClassNonMutating, autoloaded
  per the existing shell-tool convention.
- [ ] **AC2.** Match against the EXISTING ring-buffer contents
  (bytes that arrived between the last read and this expect)
  fires immediately without waiting for new bytes. Test:
  spawn → write "abc\ndef\n" → wait for the write to drain →
  expect ["def"] returns `matched=true, pattern_index=0,
  match="def"`, before contains "abc\n".
- [ ] **AC3.** Match against newly-arrived bytes works. Test:
  spawn slow producer (`bash -c 'sleep 0.5; echo HELLO'`) →
  expect ["HELLO"] with timeout 2000ms returns
  `matched=true, pattern_index=0` after ~500ms.
- [ ] **AC4.** Multi-pattern returns the INDEX of the first
  match in the byte stream. Test: produce "BBB\nAAA\n" → expect
  ["AAA", "BBB"] returns `pattern_index=1` (BBB matched first
  in the byte order, which is patterns[1]).
- [ ] **AC5.** Regex mode (`regex=true`) compiles patterns as
  RE2 and matches accordingly. Invalid regex returns a
  structured error before any reading happens. Test: expect
  `["err.*r"], regex=true` matches "Connection error: refused".
- [ ] **AC6.** Timeout returns `matched=false, timeout=true`
  with the `before` field carrying every byte seen during the
  wait. The session stays alive (subsequent `shell.read`
  works). Test: expect on a quiet session with 100ms timeout
  returns the timeout shape after ~100ms; subsequent
  `shell.read` returns nothing (buffer was drained into
  `before`).
- [ ] **AC7.** EOF returns `matched=false, eof=true` with the
  exit code populated when the underlying process exits before
  any pattern matches. Test: spawn `bash -c 'exit 3'` → expect
  ["nope"] returns `eof=true, exit_code=3`.
- [ ] **AC8.** After-match bytes stay readable. Test: produce
  "PROMPT> ready data" → expect ["PROMPT> "] succeeds; a
  subsequent `shell.read` returns "ready data".
- [ ] **AC9.** Concurrent expect on the same session is
  rejected with a structured error. Test: launch two expect
  goroutines against the same id; the second returns
  `error: "session N: expect already in progress"` (or
  equivalent — leverages existing attach exclusivity).
- [ ] **AC10.** Pattern count cap (1..16) enforced at decode
  time; 0 patterns returns "patterns required", 17+ returns
  "too many patterns". Empty pattern strings rejected.
- [ ] **AC11.** Tests in
  `internal/plugins/runtime/pty/manager_test.go` cover AC2-AC10
  using the existing PTY test-spawn helpers; pass under `go
  test -race`.
- [ ] **AC12.** `docs/plugins/host-imports.md` Tier 1 section
  adds a `stado_terminal_expect` row with signature +
  capability + return-shape pointer. The shell tool docs in
  `docs/commands/` (or wherever shell.* is documented) get a
  `shell.expect` entry showing one minimal substring example
  + one regex example.

## Non-goals

- **Not a Tcl `expect` reimplementation.** No `interact`,
  no `spawn-via-expect`, no DSL, no script files. Just the
  one read-until-pattern primitive — anything more belongs
  in user-space scripts the model writes.
- **No glob patterns.** Substring + RE2 covers every real
  case; glob would just be a third syntax to document.
- **No "expect_then_send" combo.** Compose at the model layer
  (expect → write). Combining loses the pattern-index return
  value's usefulness.
- **No matching against snapshot-rendered text.** For
  full-screen TUIs (mc, vim, htop), the model uses
  `shell.snapshot` to read the rendered grid. Expect operates
  on the raw post-PTY byte stream — the same bytes the ring
  buffer holds. Expect on a TUI session matches against ANSI
  escape sequences interleaved with content; doable but the
  wrong tool. The spec calls this out explicitly so the model
  picks the right primitive.
- **No pattern caching across calls.** Each call compiles
  patterns fresh. Hot loops re-compiling the same patterns
  pay a small cost (~µs per pattern); not worth a stateful
  cache layer.
- **No streaming progress callbacks.** Expect is synchronous-
  blocking from the model's perspective. Long-running expects
  with no match are limited by the configured timeout, not by
  intermediate notifications.

## Design sketch

### PTY manager extension — `internal/plugins/runtime/pty/manager.go`

New method `(*Manager).Expect(id uint64, patterns []Pattern,
timeout time.Duration) (ExpectResult, error)` where:

```go
type Pattern struct {
    Bytes []byte // substring (raw bytes)
    Regex *regexp.Regexp // when regex mode; mutually exclusive with Bytes
}

type ExpectResult struct {
    Matched      bool
    PatternIndex int      // valid only when Matched
    Before       []byte   // bytes seen before the match (or all bytes on timeout/eof)
    Match        []byte   // matched bytes (valid only when Matched)
    Timeout      bool
    EOF          bool
    ExitCode     int      // valid only when EOF
}
```

Implementation:

1. Acquire the session's expect-lock (fail-fast if held).
2. Drain whatever's in the ring into a local buffer.
3. Scan: for each byte position in the buffer, check each
   pattern; first match wins (lowest position; ties broken by
   patterns[i] index).
4. If no match yet: register a per-session "expect waiter"
   that the PTY reader-goroutine notifies when new bytes
   arrive. Wait on the channel with the timeout.
5. On wake: append new bytes to the local buffer, re-scan
   (but only from the "tail-len-LongestPattern" position to
   avoid re-checking already-scanned bytes).
6. On match: split the buffer into `before | match | after`,
   PUSH `after` back into the front of the ring buffer (new
   ring method needed: `Unshift([]byte)`), return.
7. On timeout: return all-bytes-as-before, leave ring empty.
8. On process exit during wait: return EOF + exit code.

The "scan from tail-len-LongestPattern" optimisation matters
for hot loops where new bytes arrive in small chunks but
patterns are long.

### New ring buffer method — `internal/plugins/runtime/pty/ring.go`

`(*ringBuffer).Unshift(b []byte) (dropped int)` — push `b` to
the FRONT of the ring (so subsequent ReadN sees it first).
Existing data shifts right; if combined size exceeds capacity,
drop oldest tail bytes (mirrors the existing scrollback
discard semantics). Returns drop count.

Used only by Expect's "after-match goes back" path; Read keeps
its current semantics. ~30 LOC + 3 ring tests.

### Wasm host import — `internal/plugins/runtime/host_pty.go`

New import next to the existing terminal_read:

```go
//go:wasmimport stado stado_terminal_expect
//   args: (idLo, idHi, argsPtr, argsLen, resPtr, resCap) → int32
```

Args JSON shape:

```jsonc
{
  "patterns":   ["password:", "$ "],
  "regex":      false,
  "timeout_ms": 30000
}
```

The host import:

1. Decode args; validate (1..16 patterns, no empty strings,
   timeout ≥ 0, regex compiles cleanly).
2. Compile patterns into the Pattern slice.
3. Call `manager.Expect(id, patterns, timeout)`.
4. Marshal ExpectResult into the response shape (base64 the
   byte fields).
5. Write to resPtr; return the standard byte-count / -n
   convention.

Cap-gated by the existing `exec:pty` capability — no new
capability vocabulary entry needed.

### Wasm plugin tool wrapper —
`internal/bundledplugins/modules/shell/main.go`

```go
//go:wasmexport stado_tool_expect
func stadoToolExpect(argsPtr, argsLen, resPtr, resCap int32) int32 {
    // Unmarshal args (id + patterns + regex + timeout_ms)
    // Validate (id != 0, len(patterns) in 1..16, ...)
    // Call host stado_terminal_expect, return its bytes verbatim
}
```

The plugin tool's job is shape validation (so error messages
say "shell.expect: id required" rather than the host's
generic "id must be > 0"). Host does the heavy lifting.

### Tool metadata + autoload —
`internal/runtime/tool_metadata.go`

Add one line:

```go
"shell.expect": {Canonical: "shell.expect", Plugin: "shell",
    Categories: []string{"shell"}},
```

Existing autoload list in `internal/bundledplugins/list.go`
already covers everything in the shell module; new tool name
gets picked up automatically.

### Tests —
`internal/plugins/runtime/pty/manager_test.go`

10 test functions covering AC2-AC10:

- `TestExpect_MatchExistingBufferContents`
- `TestExpect_MatchOnNewArrivals`
- `TestExpect_MultiPatternReturnsFirstMatchByByteOrder`
- `TestExpect_RegexMode`
- `TestExpect_RegexInvalidPattern`
- `TestExpect_Timeout`
- `TestExpect_EOFWithExitCode`
- `TestExpect_AfterMatchBytesStayReadable`
- `TestExpect_ConcurrentExpectRejected`
- `TestExpect_PatternCountCaps`

Reuse the existing test-spawn helpers
(`spawnTestSession`, `writeAndDrain`, etc.). Run under -race.

### Build artefacts

- `internal/bundledplugins/wasm/shell.wasm` rebuilt via
  `internal/bundledplugins/build.sh` (per project convention
  all 13 bundled wasm files are rebuilt and committed
  together — see commits `003cea7`, `09c8002`).

## Risk and self-critique

- **Where this design might be wrong: pattern matching against
  raw PTY bytes works for line protocols (netcat / SSH / REPL
  prompts) but is awkward for TUIs.** A model that uses expect
  on a `top` session will see ANSI escape codes interleaved
  with text and patterns won't match the rendered words. The
  spec calls this out under non-goals; if it becomes a real
  source of confusion, the next slice would add a
  `against: "rendered" | "raw"` arg to expect, where
  "rendered" runs the bytes through vt10x first and matches
  against the resulting text grid. Not in this spec — wait
  for evidence the rendered-mode is needed.

- **Concurrency model.** I'm assuming first-expect-wins
  exclusivity matches what the existing PTY manager does for
  `attach` / `read`. If the manager allows multiple concurrent
  reads (it might via the ring + per-call cursors), expect
  needs a stronger guard. AC9 forces me to verify on
  implementation.

- **Pattern compilation cost.** For hot expect loops calling
  the same patterns 100 times, the per-call regex compile
  burns a few µs per pattern. I considered a per-session
  pattern cache; rejected as premature — the model rarely hot-
  loops expect, and if it does, it can compose
  `expect → write → expect` rather than re-call with the same
  patterns. Worth measuring before adding the cache.

- **Why not extend `shell.read` with a `wait_for` arg?**
  Because read's contract is "give me up to N bytes"; expect's
  is "give me bytes up to and including X". Different return
  shape (pattern_index, match field). Adding wait_for to read
  would muddy both. Separate tool, clean shapes.

- **Why base64 for `before` / `match`?** PTY output routinely
  includes non-UTF8 byte sequences (especially from TUI
  applications using box-drawing chars in non-UTF8 codepages,
  or random binary that gets through). JSON strings can't
  carry those without escape-sequence corruption. Base64 is
  the sane wire for "raw bytes the model wants intact".
  Mirrors what the existing `shell.snapshot` does for its
  `body_b64` field.

- **What if the model passes `timeout_ms: 0`?** Per spec,
  that's "check the existing buffer once and return immediately"
  — useful for non-blocking check-and-branch loops. Documented
  in the wire-shape comments.

- **Process-exit detection during wait.** The PTY reader
  goroutine signals EOF on the existing-attach mechanism
  somehow (need to verify in `manager.go`); expect must
  subscribe to that signal. If the signal isn't already
  exposed, the implementation needs a small addition to the
  manager's wait-for-bytes channel to also carry the EOF
  event.

## Done definition

- All 12 acceptance criteria satisfied with passing tests
  (`go test ./internal/plugins/runtime/pty/... -race -count=1`
  green).
- `go build ./...` clean.
- `internal/bundledplugins/wasm/shell.wasm` rebuilt and
  committed alongside the source change (per project
  convention).
- `docs/plugins/host-imports.md` updated per AC12;
  `docs/commands/` shell-tool docs add the shell.expect entry.
- A short demo in `plugins/examples/expect-demo-go/` (or as
  an addition to an existing shell-using demo) showing the
  canonical use: spawn netcat → expect "Connection
  established" → write payload → expect "$ " → read response.
  Same pattern other example plugins already follow
  (`render-demo-go`, `choose-demo-go`); ~80 LOC of demo + a
  README block.
- Final manual smoke: `stado tool run shell.spawn` then
  `stado tool run shell.expect` against a real PTY (e.g.
  `bash -c 'echo READY; sleep 0.5; echo MORE'`) returns the
  expected match envelope.

## Notional rollout phases (for an implementation goal)

1. **Phase 1 — manager + tests** (`internal/plugins/runtime/pty/`).
   Land `Manager.Expect`, `ringBuffer.Unshift`, and the 10
   manager tests. ~250 LOC + tests. No wasm changes yet.
2. **Phase 2 — host import + plugin wrapper.** Add
   `stado_terminal_expect` host import, the plugin's
   `stado_tool_expect` wrapper, and the tool metadata entry.
   Rebuild bundled wasm. ~150 LOC. Manual smoke via
   `stado tool run shell.expect`.
3. **Phase 3 — docs + demo plugin.** `docs/plugins/host-
   imports.md` row, shell command docs entry, and the
   `expect-demo-go` example plugin. ~60 LOC + ~50 LOC docs.

Each phase is independently shippable; phase 1 alone is
useful for in-tree consumers (`stado` itself) before the wasm
exposure lands.

## References

- Tcl/POSIX `expect(1)` man page — the prior art for the
  pattern semantics.
- Existing `shell.read` / `shell.snapshot` in
  `internal/bundledplugins/modules/shell/main.go` — the wire
  + capability shape new tools follow.
- `internal/plugins/runtime/pty/manager.go` —
  `(*Manager).Read`'s timeout-blocking pattern is the closest
  prior art for how Expect waits for new bytes.
- The original design discussion that surfaced this gap is in
  the conversation transcript, not in tree.
