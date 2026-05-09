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

### B6: Verify/document MCP stdio framing ~~RESOLVED (documented + interactive advisory)~~

**Verified.** `stado mcp-server` uses newline-delimited JSON-RPC
2.0 (one JSON message per line) — that's the MCP v1 stdio
contract; LSP's Content-Length framing is a different transport.
Confirmed by reading `mcp-go/server/stdio.go` (line 506:
`reader.ReadString('\n')`).

**Documented.** `mcp-server --help` now leads with a "WIRE FORMAT"
section explicitly calling out newline framing and warning against
Content-Length preludes. Added a copy-pasteable smoke test:
`echo '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | stado mcp-server | head -1`.

**Catch the common-confusion case.** When stdin is a TTY at startup
(operator typed `stado mcp-server` directly with no client
connecting), the server now prints a stderr advisory pointing at
the smoke test and how to exit with Ctrl+D — pre-fix this looked
like a hang.

True Content-Length framing support would be a transport-layer
change in mcp-go upstream; out of scope for stado.

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

### F9: stado_ui_render + stado_ui_print — companions to existing stado_ui_choice ~~RESOLVED~~

**F9a (`stado_ui_print`)** ships TUI-only (2026-05-08): new
`ui:print` capability, JSON wire `{text, severity?, eol?,
stream_id?}` with 8 KiB text cap, fire-and-forget into the TUI
scrollback as a system block with severity prefix. Non-TUI
print routing remains a separate F9a follow-on (independently
landable; not in scope for this F9 closure).

**F9b (`stado_ui_render`)** ships across all four channels
(2026-05-09):

- **F9b.1** host scaffolding (`internal/plugins/runtime/
  host_ui_render.go` + capability + `Panel` types + size /
  schema validation, ~125 LOC + 14 tests).
- **F9b.2** TUI renderer (`internal/tui/panel_render.go` +
  bridge wiring; bordered system-block widget per body kind;
  matches existing rounded-overlay aesthetic; 14 tests).
- **F9b.3** ACP wire (`internal/acp/render_bridge.go`):
  `session/update kind=panel` notification, fire-and-forget.
  Documented in `acp --help`'s notification-kind enumeration.
  3 tests.
- **F9b.4** MCP wire (`cmd/stado/mcp_render_bridge.go`):
  per-call panel accumulator + `CallToolResult.StructuredContent`
  + ASCII fallback in text content. 7 tests.
- **F9b.5** Headless wire (`internal/headless/render_bridge.go`):
  `session.update kind=panel` on the existing JSON-RPC
  notification stream (deviation from spec's original
  `--ui-render-file` flag — unifying on the existing wire is
  cleaner; documented in commit `f1830df`). 3 tests.
- **F9b.6** Demo plugin + docs: `plugins/examples/render-demo-go/`
  (canonical pattern for plugin authors), `docs/plugins/
  host-imports.md` updated with the `stado_ui_render` row +
  `ui:render` in the capability vocabulary table. Spec at
  `.agent/specs/open/f9b-ui-render.md` reflects the as-shipped
  scope.

**Body kinds shipped:** text / kv / list (bullet, numbered, check)
/ code (with optional language tag) / table (with column-width
detection + proportional narrowing on overflow) / diff (-/+
markers).

**Size caps enforced at the WASM boundary:** 64 KiB total payload,
32 KiB per section, 200 rows × 16 cols on tables, 80-char title /
200-char footer / 64-char id per spec.

**Wire shape symmetry:** ACP, MCP, and headless all emit the same
panel JSON envelope (with three lightly-coupled `panelToWire`
helpers — one per surface; if a fourth surface needs the same
helper it graduates to a shared package per the project's
"don't speculatively abstract" discipline).

**Two known follow-on slices captured as future work, not as
unresolved AC items in this entry:**

- **F9a non-TUI print routing** — `ui:print` currently drops on the
  floor for ACP / MCP / headless. Independent of F9b.
- **F9b.5b agent-loop tool dispatch** — `headless.pluginRun` wires
  RenderBridge directly. Plugins that emit panels DURING
  `runtime.AgentLoop` tool dispatch (i.e. the agent calls a tool
  that emits a panel) need an explicit `opts.Host` carrying the
  bridge — a separable refactor inside `internal/runtime/agentloop.go`.

**The `ui` umbrella capability** mentioned in the original F9 design
is intentionally NOT introduced. Per-primitive caps (`ui:approval`,
`ui:choice`, `ui:print`, `ui:render`) remain canonical; introducing
a coarser-grained umbrella now would force rewriting three
already-shipped manifests for marginal ergonomic value. Captured
as out-of-scope in the f9b spec; revisit when consolidation has
clearer demand.



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

### F10: Collapse `stado_ui_input` into `stado_ui_choice` — each option carries an optional editable field ~~RESOLVED (TUI + ACP)~~

**Status update (2026-05-08, post-v0.47.0).** ACP follow-on shipped:
`session/update kind=choice` carries per-option `prefix` + `input`
metadata; `session/choice_response` accepts `inputValue`. The server
validates `inputValue` against the chosen option's validator before
resolving — failure returns an RPC error and keeps the request open
for retry. ACP clients that don't yet render input fields ignore
the metadata and resolve with empty `inputValue` (graceful
degradation). MCP / headless surfaces still inherit the unmodified
pluginrun bridge — those channels don't have a real choice rendering
layer yet, so input fields silently no-op there until those surfaces
grow choice support.

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

## BUG: `stado tool run --session ... shell.spawn` loses PTY session immediately ~~RESOLVED (stado daemon)~~

Resolved by `stado daemon` — the long-running peer that holds pty.Manager
across `stado tool run` invocations. PTY-bound tools (shell.spawn / list /
attach / read / write / snapshot / signal / resize / destroy) auto-route
through the daemon (auto-spawned on first call). The reproducer below now
returns the spawned session in `shell.list` and reads correctly.

Project scoping: `STADO_SESSION_ID` (or the discovered git root) selects the
daemon-side scope, so sessions in repo A are invisible to a call from repo B.

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

## BUG: MCP `web__fetch` returns `stado_http_request returned -1` ~~RESOLVED~~

(Folds in the paired `web_tech_detect` report above — same root cause.)

**Root cause.** `stado_http_request` host import uses a negative-return
convention to surface structured errors: when the call fails (capability
denied / host-allowlist mismatch / dial-guard refusal / network error /
backend HTTP error), the host writes a human-readable error message
into the plugin's response buffer and returns `-n` where `n` is the
length of that message (see
`internal/plugins/runtime/host_imports.go::encodeToolSidePayload` and
the leading comment block in `host_http_request.go`). Every other
plugin in this repo that calls `stado_http_request` (`browser`,
`http-session`, `web-search`, `mcp-client`) reads the message back via
`respBuf[:-n]` and surfaces it; the bundled `web` module
(`internal/bundledplugins/modules/web/main.go`) was the lone outlier —
it just emitted the literal string `"web.fetch: stado_http_request
returned -1"` and dropped the host-side reason on the floor. Operators
saw an opaque `-1` instead of e.g. `denied: insufficient capabilities
…` or `private network address … denied` or the upstream HTTP error.

**Resolution.** Read the host's structured payload back from the buffer
when `n < 0` and surface it: `respBuf[:-n]` is the message, prefixed
with `web.fetch: `. Same shape every other plugin already uses; the
bundled `web` module is now consistent. With this fix the AC2
reproducer (`stado tool run web.fetch '{"url":"http://10.129.227.148/"}'`)
returns the real HTTP body when reachable, or a structured failure
message naming the actual cause when not.

**Re. the paired `web_tech_detect` AC1 report.** That tool is a
third-party plugin not in this repo; we cannot patch it directly.
However, the `web__fetch` fix demonstrates the canonical pattern, the
host's negative-return convention is now documented inline at the
fix site, and `docs/plugins/host-imports.md`'s ABI conventions
section already describes `-1` semantics. The third-party plugin
authors should adopt the same `string(respBuf[:-n])` pattern; once
they rebuild, `web_tech_detect` will surface real errors too. No
host-side change is required for AC1 — the host has been writing
structured payloads all along; consumer plugins were silently
discarding them.

**Tests.** `TestBundledWebFetch_PropagatesHostStructuredError` in
`internal/runtime/bundled_plugin_tools_test.go` exercises the failure
path via a loopback URL (the bundled web manifest declares
`net:http_request` but not `net:http_request_private`, so the dial
guard refuses with a deterministic structured error); asserts the
old `"returned -1"` text is gone and a host-side reason
("127.0.0.1" / "private" / "denied" / etc.) actually propagates.

**Bundled wasm artefacts:** `internal/bundledplugins/wasm/web.wasm`
rebuilt via `internal/bundledplugins/build.sh`; per project
convention all 13 bundled wasm files are rebuilt and committed
together (see commits `003cea7`, `f5d45bc`, `09c8002` for prior
batch-rebuild precedent).

## BUG: `tools.describe` is registered but `tool run` cannot find its source plugin ~~RESOLVED~~

**Root cause.** Meta-tools (`tools.search` / `tools.describe` /
`tools.categories` / `tools.in_category` / `tools.activate` /
`tools.deactivate` / `plugin.load` / `plugin.unload`) are native Go
implementations in `internal/runtime/meta_tools.go`. They have no WASM
module backing them, so `cmd/stado/tool_run.go`'s lookup pipeline —
which dispatches via `bundledplugins.LookupModuleByToolName` first,
then `runtime.LookupInstalledModule` — always fell through to the
generic "tool registered but its source plugin not found" message even
though the tool was live in every other surface (TUI, MCP server,
agent loop, headless RPC).

**Resolution.** Added a meta-tool short-circuit in `runToolByName`
(`cmd/stado/tool_run.go`) that runs the registered Go implementation
directly with `tools.NullHost{}` when `runtime.IsMetaTool` returns
true. NullHost satisfies `tool.Host` but does *not* implement
`pkgtool.ToolActivator` / `ToolDeactivator`, so the four
non-session-aware meta-tools (`search` / `describe` / `categories` /
`in_category`) work standalone in `tool run`, while the four
session-aware ones (`activate` / `deactivate` / `plugin.load` /
`plugin.unload`) surface their existing structured "current host does
not support activation" error rather than silently no-op.

**Tests.** `TestToolRun_DispatchesMetaToolDescribe` (covers the
original reproducer with both wire and canonical forms),
`TestToolRun_DispatchesMetaToolSearch`,
`TestToolRun_MetaToolActivateReportsHostMissing`. All in
`cmd/stado/tool_run_test.go`.

**Note on the public surface.** `internal/runtime.isMetaTool` was
exported as `IsMetaTool` for the dispatch wire-up. Internal-only API
within the project; no semver impact.
