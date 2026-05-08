# Stado TODO

## Build / deployment

### D1: Plugins using deleted host imports break silently after ABI changes ~~RESOLVED~~
Plugins importing removed primitives (e.g. `stado_fs_tool_glob` post-Step 7)
failed silently at tool-call time. ABI verify catches this at `session/new`.
v0.46.0's `### Plugin ABI migration note` now carries an explicit substitution
table covering every removed primitive across v0.45.0 / v0.46.0
(`stado_fs_tool_*`, `stado_http_get`, `stado_exec_bash`, `stado_search_*`)
mapped to its replacement (`stado_fs_*`, `stado_http_request`, `stado_exec`,
`stado_tool_invoke`). The fail-fast ABI error names the specific missing
imports so authors can map them to the table.

## Bugs

### B1: MaxTurns hardcoded in ACP server with --tools ~~FIXED~~
`MaxTurns: 10` was compiled in. Now configurable via `session/new {"maxTurns": N}`,
`stado acp --max-turns N` / `--no-turn-limit`, and `[acp] max_turns` in config.toml.

### B2: Plugin ABI mismatch silent during ACP sessions ~~FIXED~~
Stale-ABI plugins caused silent tool-call failures through the full turn budget.
**Fix:** eager ABI verify at `session/new` returns a structured error listing
rebuild-required plugins with specific missing symbols.

### B3: ACP protocol event kinds undocumented ~~FIXED~~
`session/update` kinds and their wire fields undocumented in `--help`.
**Fix:** `stado acp --help` now enumerates all five kinds with required
client-side response RPCs.

### B4: `stado tool list --json` emits multiple JSON values ~~RESOLVED~~
Pre-v0.46.2 emitted NDJSON which broke `python3 -m json.tool`, `jq .`,
and any strict-JSON parser. Output now carries a single envelope:
`{ "schema_version": 1, "count": N, "tools": [...] }` — wired through
the project-wide stability commitment so a future rename / removal /
type change bumps `schema_version` with a migration note. Operators
relying on the streaming shape can recover it with
`stado tool list --json | jq -c '.tools[]'`.

### B5: CLI `shell.spawn --session` does not persist PTYs across tool runs ~~RESOLVED (refusal + clarified --session help)~~

**Root cause.** `pluginRuntime.Runtime` owns its `pty.Manager`
per-process. `stado tool run` is single-shot — Runtime is created,
tool dispatches, Runtime closes — so PTYs spawned in one invocation
cannot survive into the next, regardless of `--session`. The
`--session` flag was always about session-aware capabilities (audit
log, memory, fork), not PTY persistence; that nuance was buried in
the help text.

**Resolution.** `stado tool run` now refuses the PTY-bound shell
tools (`shell.spawn` / `list` / `attach` / `read` / `write` /
`detach` / `signal` / `resize` / `destroy`) with an advisory
pointing at the surfaces that hold PTY state across calls — TUI
(`stado`), MCP server (`stado mcp`), agent loop (`stado run`).
One-shot `shell.exec` / `shell.bash` / `shell.sh` / `shell.zsh`
remain available because they don't bind a PTY. The `--session`
flag help now explicitly states it does not bridge PTYs. Tests:
`TestToolRun_RefusesPTYBoundShellTools` and
`TestToolRun_AllowsOneShotShellTools`.

**True cross-CLI persistence remains a separate spec.** Building a
shell-supervisor sidecar (or daemon socket) so `shell.spawn` from
one CLI run can be observed by another would be a multi-day
project — track separately if operators need `stado` as a tmux
replacement from the CLI.

### B6: Verify/document MCP stdio framing

During the Aero HTB run, direct `stado mcp-server` testing worked reliably with
newline-delimited JSON-RPC messages, but a Content-Length framed stdio request
did not produce a usable response. Since `mcp-server --help` describes this as
an MCP v1 stdio server, verify whether standard MCP clients expect
Content-Length framing here. If newline framing is intentional, document it
explicitly; if not, add Content-Length support or a clear protocol error.

## Missing features

### F1: ACP session/new max_turns param ~~FIXED~~
`{"maxTurns": N}` now pins turn budget per-session. `--max-turns` / `--no-turn-limit`
also available as operator CLI flags.

### F2: ABI verify before first session/prompt ~~FIXED~~
Installed plugins checked at `session/new` when `--tools`; mismatch returns a
structured error before any turns run.

### F3: stats --session --json schema stability ~~RESOLVED~~
Output carries `"schema_version": 1`; renames/removals must bump it.
The CHANGELOG now has a top-level `## Stability commitments` section
pinning the contract project-wide (additive=no bump, rename/remove/type-change=bump
+ migration note). `stado stats --help` surfaces the same commitment so
operators see it at the tool surface, not buried in a release entry.

### F4: Document .env auto-load behavior in ACP --help ~~FIXED (v0.46.0)~~
`stado acp --help` now has an **Environment** section documenting `.env` auto-load
from CWD upward with recommended credential injection pattern.

### F5: Plugin ABI migration path for breaking changes ~~RESOLVED (v0.46.0)~~
All three sub-items covered: CHANGELOG has per-release `### Plugin ABI migration note`
blocks with explicit symbol removals + rebuild checklists; eager ABI verify at
`session/new` (commit `af32d1e`) acts as the "rebuild required" shim — mismatches
surface before any prompt, not during tool calls.

### F6: ACP model with tool-call-only turns produces 0-byte reply ~~RESOLVED via F7~~
Superseded by F7 (kind=tool_summary). stado now emits a structured summary event
for tool-only turns; ACP clients (e.g. glorbo) use it to construct a non-empty reply.

### F7: kind=tool_summary notification for tool-only turns ~~FIXED (v0.46.0)~~
`kind=tool_summary` lands in v0.46.0: emitted at end of any turn with ≥1 tool call
but 0 text deltas. Wire: `{kind="tool_summary", toolCount: N, lastTool: string,
lastError: bool}`. Glorbo-side handling tracked as F9 in glorbo's TODO.

### F8: Persona loader should accept external paths / XDG overrides ~~FIXED~~
Resolution order `{cwd}/.stado/personas → <config-dir>/personas → bundled`
means any downstream toolkit can drop a persona file under `.stado/personas/`
without forking stado.

### F9: stado_ui_render + stado_ui_print — companions to existing stado_ui_choice ~~PARTIAL: F9a print TUI shipped~~

**Status (2026-05-08).** F9a (`stado_ui_print`) ships TUI-only:
new `ui:print` capability, JSON wire `{text, severity?, eol?,
stream_id?}` with 8 KiB text cap, fire-and-forget into the TUI
scrollback as a system block with severity prefix. Non-TUI bridges
drop on the floor for this slice. ACP `kind=text` extension and
MCP/headless rendering tracked as the F9b follow-on. F9b
(`stado_ui_render` — structured panel with text/kv/list/code/
table/diff body kinds) is the remaining heavy lifting.



**Context (2026-05-08).** Plugins driving multi-step interactive workflows
need to (a) display structured information panels, (b) stream plain text
between structured events, and (c) collect operator input — all transparently
across TUI / ACP / MCP / headless channels, same routing model
`stado_ui_choice` already follows. Plugin emits structured payload; runtime
translates per-channel; session records raw payload for replay.
(Free-form text *capture* is folded into `stado_ui_choice` per F10, not
a separate primitive.)

**New manifest capability `ui`** gating render + print + choice together.
Existing `stado_ui_choice` plugins migrate by adding `ui` to capabilities.

**`stado_ui_render(panel)` — fire-and-forget structured emit.**

Schema:
- `title` (≤80 chars), `sections[]`, optional `variant` (info/ok/warn/error/recommendation),
  optional `id` (referenceable from later choice), optional `footer` (≤200 chars)
- Section bodies (typed, not free-form markdown):
  `text` | `kv` (labeled fields) | `list` (with markers) | `code` (language-tagged) |
  `table` (≤200 rows) | `diff` (before/after, for "edit prompt" preview)
- Size caps enforced at WASM boundary: 64 KiB total / 32 KiB per section
- Text bodies: markdown subset only; ANSI / HTML / control chars stripped
- Audit: panel struct lands in session as tool-exec event payload verbatim
  (not a rendered snapshot — replay re-renders against current channel)

**`stado_ui_print(text, opts?)` — fire-and-forget plain-text emit.**

The simple-text counterpart to `render`. Used for streaming status updates,
progress lines, raw model output passing through, debug breadcrumbs, or
any prose that doesn't merit a structured panel.

- `text`: plain string; markdown subset allowed in TUI/ACP/MCP, stripped
  to plain in headless.
- `opts`:
  - `severity?: "info"|"warn"|"error"` — coloring hint for TUI; passes
    through verbatim to ACP/MCP as a field; styled or ignored per channel.
  - `eol?: bool` — append newline (default true).
  - `stream_id?: string` — opaque label so a subsequent print with the same
    `stream_id` can be rendered as a continuation (TUI may render on the
    same line or in a coalesced block).
- Size cap: 8 KiB per call. Larger payloads should use `stado_ui_render`
  with a `code` body so the renderer can paginate.
- Audit: text content + opts recorded as the tool-exec event payload.
- Non-blocking; no return value beyond success/error (size violation, etc.).

**Per-channel renderers (runtime):**
- TUI: bordered panel for `render`; inline scrollback for `print`; widget per
  panel body kind (kv→aligned columns, table→ascii grid, code→monospace block
  w/ language tag, etc.)
- ACP: new `session/update kind=panel` for `render`; existing `kind=text`
  for `print` (severity carried as a structured field). Clients render
  structured or fall back to text.
- MCP: panel struct returned alongside `text` field in tool result; `print`
  output appended to the tool result `text` stream.
- Headless / scripts: panels → JSON to stderr (or `--ui-render-file`);
  prints → stdout (with optional severity prefix `[warn]` etc.)

**Operational requirements:**
- Ordering: panels and prints emitted within a tool call render in order;
  both are non-blocking. Interleaved `stado_ui_choice` calls block for response.
- Backpressure: if channel disconnected, emit succeeds silently (still recorded
  to session). Errors only for malformed input (size cap, schema violation).
- `variant`/`severity` are styling hints, not load-bearing — channels without
  color must still convey severity from the text. Plugin must duplicate
  severity in the body when it matters.
- `stado plugin doctor` surfaces `ui` capability with one-line summary.

**Non-requirements (deliberately):** no streaming/replace updates for panels
(re-emit panel for "progress"; use `print` with a shared `stream_id` for
continuations), no animations, no in-panel interactivity (always paired with
separate choice), no plugin-controlled layout, no rich HTML.

**Estimated scope:** 600-1000 LOC across plugin host SDK + ACP / MCP / TUI
renderers + schema validation. Sessions already record tool-exec events
verbatim, so audit serialization is mostly free.

### F10: Collapse `stado_ui_input` into `stado_ui_choice` — each option carries an optional editable field ~~RESOLVED (TUI slice)~~

**Status (2026-05-08).** TUI surface ships end-to-end:
`{prefix, input{default, validator}}` per option, validators run
host-side (length / regex / int / path / multiline), bare-input
shortcut renders single-option-with-input as a plain prompt, multi
+ input rejected at the bridge. Pre-F10 callers unaffected. ACP /
MCP / headless reject input-bearing options with a structured
error; wiring the new fields through the `kind=choice` payload is a
follow-on slice — track separately when the ACP client (glorbo /
similar) needs it.

**Supersedes the `stado_ui_input` portion of F9.** Refinement from the
2026-05-08 design conversation: instead of two primitives (`choice` for
pick-one, `input` for free-form text), a single richer `stado_ui_choice`
covers both — each option can declare an optional editable field with a
default value, so the same primitive expresses pure choice, choice +
parameter, prompt-with-input, or just an input box.

**Revised `stado_ui_choice` shape:**

```
stado_ui_choice(message, options[], multi_select?) -> ChoiceResult

option = {
  label?: string,            // r/o label (chooser display); optional iff input present
  prefix?: string,           // r/o prefix shown alongside the input field
  input?: {                  // optional r/w editable field
    default: string,         // initial value; "" = empty input box
    validator?: { kind: "length"|"regex"|"multiline"|"int"|"path", spec?: string },
  },
}

ChoiceResult = {
  selected_index: number,    // which option was picked
  selected_label: string,    // canonical label of the picked option
  input_value: string,       // text entered if option had `input`; "" otherwise
}
```

**Express each prior use case in the unified API:**

```
# 1. Pure choice (today's behavior — unchanged from caller's POV)
options = [
  { label: "Run" }, { label: "Swap" }, { label: "Edit" }, { label: "Skip" },
]
# returns { selected_index: 0, selected_label: "Run", input_value: "" }

# 2. Choice with parameterized branches (new — replaces edit-then-confirm)
options = [
  { label: "Run with model",  prefix: "model:",  input: { default: "gpt-5.5" } },
  { label: "Run with budget", prefix: "turns:",  input: { default: "3", validator: { kind: "int" } } },
  { label: "Skip" },
]
# returns { selected_index: 1, selected_label: "Run with budget", input_value: "5" }

# 3. Pure free-form input (replaces planned stado_ui_input)
options = [
  { label: "Continue", input: { default: "10.10.14.1" } },
]
# operator edits value, hits Enter
# returns { selected_index: 0, selected_label: "Continue", input_value: "10.10.14.5" }

# 4. Bare input prompt (no chooser noise)
options = [
  { input: { default: "" } },           # label omitted, runtime renders as plain input box
]
# returns { selected_index: 0, selected_label: "", input_value: "<typed>" }
```

**Per-channel rendering:**

- **TUI:** multi-row picker; rows with `input` highlight an editable field;
  Tab/Enter cycles + commits. **Special case:** if exactly one option and
  it has `input` and no `label`, render as a plain input prompt instead of
  a one-row chooser (the "bare input" shape above).
- **ACP:** `session/update kind=choice` payload carries the option list
  with input metadata per option; client returns `{selected_index, input_value}`.
- **MCP:** tool result includes the structured choice; calling agent
  responds with the chosen index + entered value via follow-up tool call.
- **Headless:** decision file specifies `{selected_index | selected_label,
  input_value}` per choice id; missing input_value falls back to the
  option's default.

**Cancellation:** caller declares a "cancel" choice explicitly (e.g.,
`{ label: "Cancel" }` or a sentinel). Runtime does not synthesize
cancellation behind the plugin's back; if the channel disconnects mid-prompt,
the call returns an error to the plugin so it can clean up state itself.

**Validators run runtime-side before returning:**
- `length` — `{min, max}` chars
- `regex` — pattern + optional error message
- `int` — accepts only integer text
- `path` — must parse as filesystem path
- `multiline` — boolean flag changing input widget; not really a validator
On validation failure, runtime re-prompts (TUI) or returns a structured
error (ACP/MCP) so the calling agent can retry with corrected input.

**Migration impact (F9 → F11):**
- F9's `stado_ui_input` removed from the primitive surface.
- F9's `stado_ui_render` unchanged.
- F9's `ui` capability still gates everything.
- Existing callers of `stado_ui_choice` with simple `string[]` options
  continue to work — runtime auto-promotes plain strings to
  `{ label: string }`.

**Why this is better than two primitives:**
- One primitive, one return type, one renderer.
- Choice + input combinations ("run with this override" cases) become a
  single round-trip instead of choice → input → confirm.
- Plugins can author small composite UIs without managing prompt order.
- Headless / scripted operators have one decision-file shape to populate.

## BUG: `stado tool run --session ... shell.spawn` loses PTY session immediately

Superseded by B5 above in the current TODO; kept as the original reproducer for
the CLI PTY persistence behavior.

Observed from `/var/home/foobarto/Dokumenty/htb-writeups` while trying to use
stado's bundled shell PTY for an HTB workflow:

```bash
stado tool run --session aero shell.spawn '{"argv":["bash","-lc","read x; echo got:$x"],"cols":80,"rows":24}'
# -> {"id":1}
stado tool run --session aero shell.list '{}'
# -> []
stado tool run --session aero shell.read '{"id":1,"timeout_ms":100}'
# -> {"error":"read: pty: session not found"}
```

The same happened with `argv=["ssh","-tt","john@192.168.122.203"]`: spawn
returned `{"id":1}`, but the follow-up read could not find the session. Either
`tool run` is not persisting shell plugin state under `--session`, or
`shell.spawn` exits/detaches without surfacing the process failure in its
result. Expected: a spawned PTY remains visible to `shell.list` and readable by
`shell.read` for the same `--session`, or `shell.spawn` returns a failure if it
cannot persist.
