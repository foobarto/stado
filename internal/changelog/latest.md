## v0.63.0 — lifecycle hooks, hashline edits, native LSP, slash & tree & provider UX — 2026-06-12

A seven-feature batch. Two of the seven change existing contracts —
read the **Breaking surfaces** section before upgrading.

### TUI

- **Lifecycle hooks — deny + mutate seam (F1).** A scriptable hook seam
  fires at tool-call and LLM boundaries (pre-tool, post-tool, pre-llm,
  post-llm, post-turn). Each hook can **allow**, **deny** (block the
  action with a reason surfaced to the model), or **mutate** (rewrite
  the payload — e.g. redact an argument, rewrite a prompt). Hooks are
  Lua scripts (gopher-lua) plus a small set of built-ins, ordered, and
  run in the TUI's llm-side path as well as the headless/agent loop.
  A `fail_closed` knob flips the error posture: by default a hook that
  errors is logged and skipped (fail-open); with `[hooks].fail_closed =
  true` a hook error denies the action. Config: `[[hooks.lifecycle]]`
  entries (point + Lua/builtin). See `docs/features/lifecycle-hooks.md`.
- **Native LSP — post-edit diagnostics + sidebar (native-lsp).** A
  session-scoped `LSPClientManager` owns language-server lifecycles
  (spawn, crash-restart, reap on session switch). After an edit, stado
  pulls diagnostics for the touched file and surfaces them in a new
  `diagnostics` sidebar section (list it in `[tui.sidebar].sections` to
  show it). Diagnostics are wired as a post-edit lifecycle hook
  (`lspfind.NewDiagnosticsHook`), appended after the operator's hooks;
  the diagnostics store resets on session switch. LSP hosts are closed
  on agent-loop teardown (`defer lspfind.CloseAll()`).
- **Fixed-height collapsible tool-output panel (tool-output-panel).**
  A tool block's output is capped to a configurable number of rows
  (`[tui].tool_output_collapsed_height`, default clamped to [3, 20])
  with an expand affordance, so a chatty tool result no longer floods
  the scrollback.
- **Dynamic slash-command registry + skill `slash:` shortcuts (F2).**
  The slash palette now reads a runtime registry (`allCommands()` =
  static built-ins + dynamically registered shortcuts) so both the
  Ctrl+P modal and the inline "/" popup surface the dynamic layer. A
  skill can declare a `slash:` frontmatter shortcut; typing that bare
  command injects the skill's prompt body (same effect as
  `/skill:<name>`). Built-in commands always win a name collision;
  collisions between dynamic shortcuts are rejected at registration.
  The registry re-registers on `/reload` so newly-added skills appear
  without a restart.
- **`/tree` — session forest navigation (tree-popup).** A new session
  forest model (`ForkSessionAtTurn`) plus a `/tree` popup (ctrl+x g)
  that renders the fork graph, lets you navigate it, **switch** to a
  node, **branch** a new session at a specific turn boundary, and
  **peek** read-only at another node without switching. ctrl+c closes
  the peek first (before quitting), and the picker closes on session
  switch.
- **`/provider` credential modal (provider-creds).** Bare `/provider`
  opens a credential manager modal — add / modify / unset a provider's
  credential (values redacted in the UI). `/provider <name>` keeps the
  per-provider setup-help text. Backed by a new `stado auth` CLI
  (env-first credential management) storing secrets in the OS keyring
  (go-keyring), plus a per-provider `base_url` override on inference
  presets (anthropic-compat endpoints).

### CLI

- **`stado auth`** — provider credential management (env-first, OS
  keyring via go-keyring). Add, modify, unset, and list provider
  credentials from the command line; the TUI `/provider` modal is a
  front-end over the same store.

### Plugins

- **Hashline content-anchored edits (hashline).** The fs plugin's edit
  contract is now LINE#HASH-anchored (see **Breaking surfaces**). The
  native fs implementation and the bundled `fs.wasm` plugin are updated
  in lockstep; FS parity (`STADO_PARITY_FS=1`) holds.

### Breaking surfaces (two — pre-1.0 no-kid-gloves)

- **fs edit contract is now LINE#HASH-anchored — exact-string `edit`
  REPLACED.** Reads now prefix every line with a `LINE#HASH:` token
  (1-indexed line number + a short content hash). The edit op
  (`replace_text`) anchors on that `LINE#HASH` reference (e.g. `11#KT`)
  instead of matching an exact substring of file content. This replaces
  the old exact-string edit contract outright — there is no
  compatibility shim. Callers (and models) that constructed edits from
  a raw content substring must switch to the line-anchored form; the
  read output now carries the anchors they need. The native fs tool and
  the bundled `fs.wasm` plugin both move to this contract together.
- **Bare `/provider` no longer prints provider capabilities.** It now
  opens the credential modal. The provider/capability overview moved to
  **`/status`** (the full provider/tool/plugin/sandbox/telemetry status)
  and **`/providers`** (active provider + detected local runners).
  `/provider <name>` still prints that provider's setup hints.

