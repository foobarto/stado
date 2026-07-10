## v0.76.0 — architecture consolidation and TUI reliability — 2026-07-10

### Skills (EP-0045 Phase 1 — model-invocable skills)

- **Skills are now model-invocable.** Each skill's `name` + `description`
  enters the model's per-turn context cheaply, and the model can pull a
  skill's body into the conversation on its own initiative via the
  `skills__load` tool — progressive disclosure, matching the Agent Skills
  standard. `/skill:<name>`, `--skill`, and `slash:` user invocation keep
  working unchanged; they are now one of two ways to invoke a skill.
- **Directory-form skills.** A skill may be a `<name>/SKILL.md` bundle that
  ships supporting files (scripts, references) the body points to via
  `${STADO_SKILL_DIR}`; flat `.stado/skills/<name>.md` files keep working.
- **New frontmatter:** `when_to_use` (extra trigger text in the listing),
  `disable-model-invocation` (keep a skill user-only), `user-invocable: false`
  (model-only background skill, hidden from `/skill`), and `allowed-tools`
  (tools pre-approved while the skill is active — project skills are
  fail-closed until the EP-44 project-trust gate; persona skills are trusted
  by location).
- Skills now reach every autonomous surface (`run`, TUI, ACP, headless,
  subagents) uniformly, not just the TUI/`run`.

#### Breaking

- **Every existing `.stado/skills/<name>.md` becomes model-invocable on
  upgrade.** Its `description` now enters the model-facing listing and the
  model may load it without the operator asking for it. To keep a skill
  user-only, add `disable-model-invocation: true` to its frontmatter. To
  turn model invocation off wholesale, deny `skills__load` via
  `[tools].disabled` (user invocation is unaffected). Pre-1.0 clean break,
  no opt-in shim (EP-0045 D7).

### CLI

- **`stado agents` removed; folded into `stado session`.** The `agents`
  command was a thin operational veneer over sessions whose subcommands
  duplicated `session`: `agents attach` was identical to `session attach`,
  and `agents list` surfaced the same live/stale rows `session list`
  already shows via its STATUS column (plus `session show <id>` for the
  tree/trace SHAs). The one behaviour unique to `agents` — signalling a
  running agent's process before removing its worktree while **keeping**
  the sidecar history — is now `stado session kill <id>`. `session delete`
  stays the destructive sibling that also purges the refs.

- **`stado headless` removed; folded into `stado run --headless`.** The
  JSON-RPC 2.0 daemon is now a mode of `run` (the non-interactive surface)
  rather than its own top-level command. The wire protocol, methods, and
  behaviour are unchanged; `--headless` skips run's one-shot path (no prompt,
  no turn cap, no 10-minute timeout) and serves the persistent stdio server.
  `--persona` sets the default persona for new sessions.

- **Project-local plugin installs are complete.** `stado plugin install
  --local <dir-or-identity>` now installs into the discovered project's
  `.stado/plugins/` directory while keeping signer trust user-local.
  Dependency resolution searches project and global plugin roots, first-time
  lock files stay in the project, and `--local --autoload` updates the project
  config instead of the user-global config.

#### Removed surfaces

- `stado agents list` -> `stado session list` (STATUS=`live` marks running
  sessions; `session show <id>` prints the tree/trace SHAs).
- `stado agents attach <id>` -> `stado session attach <id>` (identical output).
- `stado agents kill <id>` -> `stado session kill <id>` (same signal +
  worktree removal, history preserved). Pre-1.0 clean break, no alias.
- `stado headless` -> `stado run --headless` (same JSON-RPC daemon; per-call
  inputs go through the `session.*` methods). Pre-1.0 clean break, no alias.

### TUI

- **Plugin approval and choice drawers cancel reliably.** `Esc` and `Ctrl+G`
  resolve the pending request as denied/cancelled, and the global `Ctrl+C`
  modal close path now includes both drawers instead of leaving the plugin
  blocked until timeout.
- **Plugin diagnostics no longer corrupt the alternate screen.** Registry
  rebuilds emit each installed-plugin warning once, before the TUI starts,
  rather than writing raw stderr over live input, sidebar, and result rows.
- **Landing plugin summaries respect terminal width.** Long plugin lists now
  truncate with an ellipsis instead of clipping a plugin name at the edge.

### Plugins

- **Project-local management is consistent.** `list`, `installed`, `info`,
  `doctor`, `verify-installed`, `reload`, `update`, `remove`, `gc`, and `use`
  now search or preserve the same project-before-global scope used by runtime
  discovery. Active-version markers and lock files remain in their install
  scope instead of leaking project choices into user-global state. Updates
  replace superseded versioned lock rows, and GC preserves explicitly active
  versions in addition to its newest-version budget.
- **Dependencies are verified, not inferred from directory names.** Install
  accepts a required plugin only after its manifest signature and WASM digest
  verify against the user trust store. Plugin lock reads and atomic writes also
  reject symlink redirection and share one maximum file-size contract.

### Security

- **`session kill` verifies process ownership.** Session PID records now carry
  an OS process-creation identity, and signalling is bound to a stable pidfd or
  process handle. A live legacy, mismatched, or otherwise unverifiable PID is
  never signalled; platforms without a stable handle fail closed, and the
  worktree is preserved when ownership cannot be proven or termination fails.
- **Headless daemon flags fail loudly.** `stado run --headless` rejects
  one-shot run and safety flags it cannot honor instead of silently ignoring
  them; persistent `--provider`, `--model`, and `--no-sandbox` controls remain
  supported, while session behavior is configured through config and JSON-RPC.
- **Registry diagnostics are terminal-safe.** Runtime registry warnings strip
  control sequences, and rebuilds performed while the TUI owns the alternate
  screen, including background subagents, use a quiet diagnostic path.

### Fixes

- **Large skill catalogs no longer hang** while fitting the model-facing
  listing to its byte budget; persona skill load warnings are preserved.
- **Model-only skills remain model-only.** `user-invocable: false` entries are
  absent from `/skill` and direct `/skill:<name>` injection is refused.
- **Live zero-turn sessions remain visible** in the default session list, and
  Windows liveness checks now use process handles rather than Unix signals.

### Infra

- **PTY UAT is self-contained and leak-free.** Render, approval, and choice
  tests use an in-tree WASM fixture; Chrome is closed gracefully even through
  the Flatpak wrapper; XDG state and background assertions are isolated.
- **Broker daemon integration tests use short Unix-socket paths** and surface
  early server errors, avoiding false handshake timeouts under a long
  `GOTMPDIR`.

