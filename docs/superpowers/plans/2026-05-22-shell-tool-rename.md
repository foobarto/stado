# Shell Tool Affordance Rename — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename `shell.expect` → `shell.read_until` and `shell.snapshot` → `shell.screenshot` (and add cross-referencing descriptions) so LLM agents reach for the right PTY-output tool by name; delete the old names outright (pre-1.0, no aliases).

**Architecture:** Pure rename + description rewrite. No behavior change, no logic moves. The bundled shell plugin (`plugins/bundled/shell/main.go`) is a thin wasm wrapper over host imports; only its two `//go:wasmexport` names change. The host imports (`stado_terminal_expect` / `stado_terminal_snapshot`) are untouched. The compiled `shell.wasm` is rebuilt and re-embedded.

**Tech Stack:** Go, `GOOS=wasip1 -buildmode=c-shared` wasm, `//go:embed`.

**Spec:** `.agent/specs/open/shell-tool-affordance.md`

**Key constraint discovered during planning — DO NOT "fix":** The PTY-bound refusal lists in `cmd/stado/tool_run.go` and `internal/tui/model_commands.go` differ *intentionally*. `snapshot` is refused in the CLI single-shot path (can't reach daemon-hosted PTYs) but **omitted** from the TUI `/tool` path because it is read-only + needs no attach, so the TUI's in-process `pty.Manager` can serve a one-off capture. Preserve this asymmetry: rename in place, do **not** add `screenshot` to the TUI list.

---

### Task 1: Rename the wasm exports + rebuild shell.wasm

**Files:**
- Modify: `plugins/bundled/shell/main.go` (around lines 336–420)
- Regenerate: `internal/plugins/bundled/wasm/shell.wasm` (via `plugins/bundled/build.sh`)

- [ ] **Step 1: Rename the expect export → read_until**

In `plugins/bundled/shell/main.go`, change the `//go:wasmexport stado_tool_expect` block:

```go
//go:wasmexport stado_tool_read_until
func stadoToolReadUntil(argsPtr, argsLen, resPtr, resCap int32) int32 {
```

Update the error strings in that function body from `"expect: "` and `"expect failed"` to `"read_until: "` and `"read_until failed"`. Update the leading doc comment header from `// shell_expect — block until …` to `// shell_read_until — block until …` (text only).

- [ ] **Step 2: Rename the snapshot export → screenshot**

Change the `//go:wasmexport stado_tool_snapshot` block:

```go
//go:wasmexport stado_tool_screenshot
func stadoToolScreenshot(argsPtr, argsLen, resPtr, resCap int32) int32 {
```

Update the error string `"snapshot: "` → `"screenshot: "` and the doc comment header `// shell_snapshot — capture …` → `// shell_screenshot — capture …` (text only). Leave the `stadoTerminalSnapshot(...)` host-import call unchanged.

- [ ] **Step 3: Rebuild the embedded wasm**

Run: `GO="$(command -v go)" ./plugins/bundled/build.sh`
Expected: prints `building shell.wasm (ep-0038b)` (among others) and exits 0; `internal/plugins/bundled/wasm/shell.wasm` is rewritten.

- [ ] **Step 4: Verify it compiles**

Run: `go build ./...`
Expected: success (no references to the old export names remain in compiled code).

- [ ] **Step 5: Commit**

```bash
git add plugins/bundled/shell/main.go internal/plugins/bundled/wasm/shell.wasm
git commit -m "refactor(shell): rename wasm exports expect→read_until, snapshot→screenshot"
```

---

### Task 2: Rename registrations + rewrite descriptions

**Files:**
- Modify: `internal/runtime/bundled_plugin_tools.go` (read ~167–175, snapshot ~201–211, expect ~212–221)

- [ ] **Step 1: Rewrite shell.read's description (cross-references)**

Replace the description string in the `stado_tool_read` Register call with:

```
"Read whatever output is currently buffered from a PTY session and return immediately. Args: id, max_bytes?, timeout_ms?. Returns {data?, data_b64, n, eof?}. This is the RAW byte stream including ANSI escape sequences — if you're driving a full-screen or interactive program (vim, htop, an installer, a menu) and the output looks like escape-code garbage, use shell.screenshot to see the rendered screen instead. To block until a specific prompt or pattern appears (rather than returning whatever is buffered now), use shell.read_until. Requires attach."
```

- [ ] **Step 2: Rename + rewrite the snapshot registration → screenshot**

In the `stado_tool_snapshot` Register call, change the export name to `"stado_tool_screenshot"`, the wire name to `"shell__screenshot"`, and the description to:

```
"Capture the rendered terminal screen of a PTY session — what a human would actually see on screen, with ANSI escapes already resolved. Use this, not shell.read, whenever a session is running a full-screen or interactive program: TUIs (vim, htop, gdb-tui), curses menus, installers, progress bars — anything that repaints the screen. Returns {text, cols, rows, cursor:{x,y,visible}, title, svg?}. Args: id, with_svg? (default false; SVG is ~30–60 KB for 120×32). Read-only: no attach required."
```

Leave the schema and `shellSessionCaps` arguments unchanged.

- [ ] **Step 3: Rename + rewrite the expect registration → read_until**

In the `stado_tool_expect` Register call, change the export name to `"stado_tool_read_until"`, the wire name to `"shell__read_until"`, and the description to:

```
"Read the raw byte stream from a PTY session until one of the given patterns matches, the timeout elapses, or the process exits — the one-call replacement for a read-and-check loop. Returns one of: {matched:true, pattern_index, before(b64), match(b64)} | {matched:false, timeout:true, before(b64)} | {matched:false, eof:true, before(b64), exit_code}. Args: id, patterns (1..16 strings), regex? (default false; when true, patterns are RE2), timeout_ms? (default 30000; 0 = check buffer only). Matching operates on the raw byte stream; for full-screen TUIs use shell.screenshot instead. Requires attach."
```

Leave the schema and caps arguments unchanged.

- [ ] **Step 4: Verify it compiles**

Run: `go build ./internal/runtime/`
Expected: success.

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/bundled_plugin_tools.go
git commit -m "refactor(shell): rename tool registrations + cross-reference descriptions"
```

---

### Task 3: Update canonical metadata

**Files:**
- Modify: `internal/runtime/tool_metadata.go` (lines 72–73)

- [ ] **Step 1: Replace the two canonical entries**

Change line 72 from:
```go
	"shell.snapshot": {Canonical: "shell.snapshot", Plugin: "shell", Categories: []string{"shell"}},
```
to:
```go
	"shell.screenshot": {Canonical: "shell.screenshot", Plugin: "shell", Categories: []string{"shell"}},
```

Change line 73 from:
```go
	"shell.expect":   {Canonical: "shell.expect", Plugin: "shell", Categories: []string{"shell"}},
```
to:
```go
	"shell.read_until": {Canonical: "shell.read_until", Plugin: "shell", Categories: []string{"shell"}},
```

- [ ] **Step 2: Confirm `LookupToolMetadata` resolves the new wire names**

Read the `LookupToolMetadata` function in the same file. Confirm it normalizes `shell__screenshot` / `shell__read_until` (wire form, `__`) to the dotted canonical key — i.e. there is no separate hand-maintained wire→canonical table that also needs the rename. If such a table exists, rename its entries too.

Run: `go build ./internal/runtime/`
Expected: success.

- [ ] **Step 3: Commit**

```bash
git add internal/runtime/tool_metadata.go
git commit -m "refactor(shell): canonical metadata expect→read_until, snapshot→screenshot"
```

---

### Task 4: Update PTY-bound refusal lists (preserve the asymmetry)

**Files:**
- Modify: `cmd/stado/tool_run.go` (`ptyBoundShellTool`, lines ~244–264)
- Modify: `internal/tui/model_commands.go` (`ptyBoundShellToolName`, lines ~1422–1440; comment ~1224)

- [ ] **Step 1: CLI list — rename both entries**

In `cmd/stado/tool_run.go` `ptyBoundShellTool`, change `"shell.snapshot",` → `"shell.screenshot",` and `"shell.expect":` → `"shell.read_until":`. Update the inline comment that says "shell.expect rides the same per-Runtime pty.Manager…" to read "shell.read_until rides the same…". Update the comment at line ~92 listing the family (`… / write / snapshot / signal …` → `… / write / screenshot / signal …`).

- [ ] **Step 2: TUI list — rename ONLY expect→read_until, keep screenshot absent**

In `internal/tui/model_commands.go` `ptyBoundShellToolName`:
- canonical switch: `"shell.destroy", "shell.expect":` → `"shell.destroy", "shell.read_until":`
- wire switch: `"shell__destroy", "shell__expect":` → `"shell__destroy", "shell__read_until":`

Do **NOT** add `shell.screenshot` / `shell__screenshot`. Add a one-line comment above the canonical switch:

```go
	// NOTE: shell.screenshot is deliberately ABSENT — it is read-only and
	// needs no attach, so a one-off /tool dispatch can serve it from the
	// in-process pty.Manager. Only attach-requiring PTY tools are refused here.
```

Update the comment block at ~1224 listing the family names accordingly (it lists `… / write / detach / signal …` with no snapshot — leave that, just confirm `expect` → `read_until` if mentioned).

- [ ] **Step 3: Verify it compiles**

Run: `go build ./cmd/... ./internal/tui/...`
Expected: success.

- [ ] **Step 4: Commit**

```bash
git add cmd/stado/tool_run.go internal/tui/model_commands.go
git commit -m "refactor(shell): rename refusal-list entries; document screenshot asymmetry"
```

---

### Task 5: Update tests to the new names

**Files:**
- Modify: `internal/runtime/tool_metadata_test.go` (lines ~20, ~25)
- Modify: `internal/runtime/bundled_plugin_tools_test.go` (lines ~162–202)
- Modify: `cmd/stado/tool_run_test.go` (lines ~137, ~147–148)
- Modify: `internal/plugins/runtime/host_pty_snapshot_e2e_test.go` (lines ~42, ~100–146)

- [ ] **Step 1: tool_metadata_test.go**

Update the test cases: `{"canonical shell.snapshot", "shell.snapshot", "shell.snapshot", "shell"}` → use `shell.screenshot` in all three string fields; `{"wire shell__snapshot", "shell__snapshot", "shell.snapshot", "shell"}` → `shell__screenshot` / `shell.screenshot`. If there are equivalent `shell.expect` / `shell__expect` cases, rename them to `shell.read_until` / `shell__read_until`. If none exist, add a `read_until` case mirroring the screenshot one.

- [ ] **Step 2: bundled_plugin_tools_test.go**

In `TestBundledShellExpect_RoundTripsThroughWasm`: change `reg.Get("shell__expect")` → `reg.Get("shell__read_until")` and the `"shell__expect missing"` fatal message accordingly. Rename the test func to `TestBundledShellReadUntil_RoundTripsThroughWasm` and update the doc comment (`stado_terminal_expect → manager.Expect` host-import description stays — that ABI is unchanged).

- [ ] **Step 3: tool_run_test.go**

Change the describe-output assertions: `{"name":"shell__snapshot"}` → `{"name":"shell__screenshot"}` (line ~137) and the two `"name":"shell__snapshot"` occurrences in the assertion + error message (lines ~147–148) → `shell__screenshot`.

- [ ] **Step 4: host_pty_snapshot_e2e_test.go**

This test invokes tools by short name through a local harness. Update `{Name: "snapshot", Class: "NonMutating"}` (line ~42) and every `invoke("snapshot", …)` call (lines ~106, ~135) to `"screenshot"`. Read the harness's `invoke` helper first to confirm whether `Name` maps to the wasm export (`stado_tool_screenshot`) — if it constructs the export name, update that mapping too. Keep the literal string `"snapshot-marker"` (it's test payload data, not a tool name).

- [ ] **Step 5: Run the affected tests**

Run: `go test ./internal/runtime/... ./cmd/stado/... ./internal/plugins/runtime/... -run 'Metadata|Shell|Snapshot|Screenshot|ReadUntil|Describe|Expect' -v`
Expected: PASS (the e2e screenshot round-trip proves behavior is unchanged under the new name).

- [ ] **Step 6: Commit**

```bash
git add internal/runtime/tool_metadata_test.go internal/runtime/bundled_plugin_tools_test.go cmd/stado/tool_run_test.go internal/plugins/runtime/host_pty_snapshot_e2e_test.go
git commit -m "test(shell): update tests to read_until/screenshot names"
```

---

### Task 6: Update demo, docs, comments, changelog

**Files:**
- Modify/rename: `plugins/demos/expect-demo-go/` (main.go, plugin.manifest.template.json, README.md)
- Modify: `plugins/optional/session-recorder/main.go`
- Modify: `docs/plugins/host-imports.md`
- Modify: `internal/tui/ptyblock/render.go` (comments only, lines ~2, ~12, ~59–60, ~156)
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Demo plugin**

Rename the directory `plugins/demos/expect-demo-go` → `plugins/demos/read-until-demo-go` (`git mv`). In its `main.go`, `plugin.manifest.template.json`, and `README.md`, replace every `shell.expect` / `stado_tool_expect` / `shell__expect` reference with the `read_until` form and update prose. Then regenerate its key and re-sign:

```bash
cd plugins/demos/read-until-demo-go
stado plugin gen-key read-until-demo-go.seed   # if build.sh expects a seed of this name; else match build.sh
./build.sh
```

Confirm the seed filename matches what `build.sh`/`.gitignore` expects (the `*.seed` stays untracked).

- [ ] **Step 2: session-recorder**

In `plugins/optional/session-recorder/main.go`, replace `shell.snapshot`/`shell.expect` (and any `stado_tool_*`/`shell__*` forms) references with the renamed equivalents. If it's a comment/doc reference only, update the text; if it invokes the tool, update the tool name string.

- [ ] **Step 3: Docs**

In `docs/plugins/host-imports.md`, rename the agent-facing tool rows `shell.snapshot` → `shell.screenshot` and `shell.expect` → `shell.read_until`. Leave host-import rows (`stado_terminal_snapshot` / `stado_terminal_expect`) as-is — those names are unchanged. In `internal/tui/ptyblock/render.go`, update the comment mentions of "shell.snapshot" → "shell.screenshot" (comments only; no code).

- [ ] **Step 4: CHANGELOG**

Add an entry under the next unreleased section:

```markdown
### Changed
- **shell plugin (breaking):** renamed `shell.expect` → `shell.read_until` and
  `shell.snapshot` → `shell.screenshot` for tool-selection affordance. Old names
  removed (no aliases). Behavior and output shapes are unchanged.
```

- [ ] **Step 5: Verify build + commit**

Run: `go build ./...`
Expected: success.

```bash
git add -A
git commit -m "docs,demo: migrate expect→read_until, snapshot→screenshot references"
```

---

### Task 7: Full verification + final sweep

- [ ] **Step 1: Full check**

Run: `make check`
Expected: lint clean + full suite green (GOTMPDIR defaults off-repo).

- [ ] **Step 2: Final grep — no live old names**

Run: `grep -rn 'shell\.expect\|shell\.snapshot\|stado_tool_expect\|stado_tool_snapshot\|shell__expect\|shell__snapshot' --include='*.go' --include='*.md' --include='*.json' . | grep -v '.claude/worktrees' | grep -v '/.git/'`
Expected: only intentional historical mentions in `CHANGELOG.md`. Zero live registrations, callers, refusal-list entries, or doc rows. (Host-import names `stado_terminal_*` are fine — different pattern, not matched here.)

- [ ] **Step 3: Manual E2E on the real binary**

```bash
make build && ./stado install --force
```
Then, in an interactive `stado run` session, spawn a shell, and verify:
- `stado tool run shell.read_until '{"id":<N>,"patterns":["$ "]}'` returns a match object.
- `stado tool run shell.screenshot '{"id":<N>}'` returns `{text, cols, rows, …}`.
- `stado tool run shell.expect …` and `shell.snapshot …` now error as unknown tools.

Expected: new names work; old names are gone.

- [ ] **Step 4: Move the spec to done + commit**

```bash
git mv .agent/specs/open/shell-tool-affordance.md .agent/specs/done/shell-tool-affordance.md
# (after writing the handoff note in the spec per project CLAUDE.md)
git add -A
git commit -m "chore: shell tool rename complete; spec → done"
```
