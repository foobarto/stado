# CHANGELOG

Notable changes to stado, reverse-chronological. Pre-1.0; breaking
changes still allowed between tags. Sections: UX / CLI / TUI /
Plugins / Infra / Fixes.

## Stability commitments

These hold across all releases until stado hits 1.0; after 1.0 they
become semver guarantees.

- **`stado stats --json` schema.** Output carries `"schema_version": N`.
  Within a major schema version, the shape is stable: no key is
  renamed or removed, no value type changes, no semantic re-meaning of
  an existing key. Pure additions (new keys appearing alongside existing
  ones) are allowed without bumping. Renames, removals, and type
  changes bump `schema_version` and ship with a migration note in this
  changelog under the release that bumps it. Current: schema 1.
- **`stado tool list --json` schema.** Output carries
  `"schema_version": N` and the envelope
  `{schema_version, count, tools[]}`. Same rules as
  `stado stats --json`: additions are free, renames / removals / value-
  type changes bump `schema_version` and ship with a migration note.
  Current: schema 1.
- **Plugin wasm calling contract.** The exports a plugin must provide
  (`stado_alloc`, `stado_free`, `stado_tool_<name>` per ToolDef) and
  the return-length-or-negative-error wire format are stable across
  pre-1.0 releases. Host-side imports under the `stado` namespace may
  be added (additive) or removed (breaking). Removals ship in the
  release's `### Removed surfaces` section with substitutions in the
  release's `### Plugin ABI migration note`. Eager ABI verify at
  `session/new` (when `--tools` is set) surfaces stale-ABI plugins
  with the specific missing imports — no silent retries.

## v0.60.1 — TUI v2 background fixes + /compact recovery — 2026-06-10

### Fixes

- **Chat input box renders solid again.** The Bubble Tea v2 migration left the
  textarea's per-cell styles with no background, so the input box (and its
  waiting/placeholder state) showed through grey instead of the dark surface.
  Every cell — text, cursor line, placeholder, the empty filler rows, and the
  `Do · model · provider` status row — now paints the surface background; the
  box is hard-clamped to its width so the full-bleed fill can't overflow a
  narrow terminal.
- **Command palette, inline slash popup, and every picker** (model, persona,
  theme, session, agent, fleet, task) paint solid. Foreground-only spans over
  the dark modal were leaving grey holes between columns and after short text
  (most visible as the model picker's grey right-hand provider column, and the
  palette shifting colour when filtering). Selected rows stay uniformly
  highlighted. New `colorbg` template helper backs the status row.
- **`/compact` recovers from an errored turn.** A session that hit a provider
  context-overflow (e.g. a large tool result → `context window exceeds limit`)
  was unrecoverable: `/compact` reported "busy — wait for the current turn to
  finish" though nothing was running, and continuing just re-sent the
  over-limit context. Compaction now proceeds from the error state (it only
  blocks a genuinely in-flight stream), and the summary request truncates tool
  content so it fits even when the live context overflowed.

## v0.60.0 — Bubble Tea v2 (Shift+Enter) + TUI/CLI usability pass — 2026-06-10

### TUI — Bubble Tea v2 upgrade

- **Shift+Enter inserts a newline** in the chat input (Enter still submits). The
  TUI moved from Bubble Tea v1.3 to v2 (`charm.land/{bubbletea,bubbles,lipgloss}/v2`),
  which negotiates terminal keyboard enhancements and so can distinguish
  Shift+Enter from Enter — impossible under v1.3. `ctrl+j` still works too. No
  other intended UX change; the migration is mechanical (key/view/mouse/colour
  APIs). One internal note: lipgloss v2's `.Width` now includes the border, so
  bordered modals were resized accordingly.
- Two teatest-based PTY integration tests are temporarily disabled (build-tagged
  `teatest_v1`) until a Bubble Tea v2-compatible `teatest` is published; the
  real-PTY tmux harness and the pty-bridge browser E2E still cover the TUI.
- **The Ctrl+P palette and the `?`/`/help` overlay now fit the terminal.** Both
  render more rows than a typical terminal is tall; v2's compositor clips the
  frame to the canvas (v1 scrolled the overflow, losing the top instead of the
  bottom). The palette windows its list around the cursor; the help overlay
  scrolls (`↑/↓`, `pgup/pgdn`, `g/G`) with a position footer.

A round of UAT-driven usability fixes. Several change user-visible behaviour
(command exit codes, the session repo-id); pre-1.0, no deprecation shims.

### TUI

- **The session manager (ctrl+x l) no longer floods with other projects'
  sessions.** `session list`, the TUI session manager, `agents list`, and `gc`
  augmented their listing from the global flat worktree dir without filtering
  by the per-worktree repo pin, so every project's sessions leaked into every
  repo's view (2032 cross-project entries → 159 for this repo). Filtering is
  symlink-tolerant, so the `/home`↔`/var/home` ostree split no longer hides a
  repo's own sessions.
- **`/todo` now appears in the Ctrl+P palette and `/help`** (it was handled but
  unlisted).

### CLI

- **Group commands reject an unknown/typo'd subcommand** (`session`, `config`,
  `config providers`, `plugin`, `tool`, `agents`, `schedule`, `harness`,
  `completion`) with a nonzero exit instead of printing help and exiting 0.
  Notably fixes `stado completion <shell> > file` silently writing help text
  into the sourced file on a typo'd shell name.
- **`stado doctor` now checks the provider key for `ollama-cloud`** (and stays
  in sync with the provider catalogue) — it previously reported "all checks
  passed" while `stado run` 401'd on a missing `OLLAMA_CLOUD_API_KEY`.
- **`stado tool categories --json` emits JSON** instead of silently ignoring
  `--json` and printing plaintext.
- **Cleaner persona/skill errors.** A missing `--persona` no longer triples the
  prefix (`persona: persona: not found`) and now names the persona; a missing
  `--skill` with no skills installed no longer prints a dangling `available: `.

### Fixes

- **`stado daemon stop` (and idle-timeout) now actually terminate the daemon.**
  They closed the listener but never returned `Serve()`, leaking the process —
  the daemon hosts stateful tools (shell PTYs, browser, LSP). Existing leaked
  daemons need a one-time `pkill -TERM -f 'stado daemon'` after upgrading.
- **Repo-id is symlink-canonicalised**, so a repo reached via `/home` vs
  `/var/home` on ostree distros no longer splits its sessions/audit chain across
  two sidecars. Sidecars under the old hash become orphaned-but-on-disk; see
  `.agent/decisions/2026-06-07-repo-id-canonicalization.md`.
- **`plugin installed` / `plugin list` no longer show a phantom "active" row**
  (the reserved active-version marker dir was enumerated as a plugin).

### Docs

- **Sandboxing doc corrected:** outbound network is *not* restricted by the
  current bwrap+Landlock runner; network deny-by-default is deferred to the
  EP-0050 broker (Draft).

## v0.59.1 — landing fits short terminals + working build provenance — 2026-06-01

### Fixes

- **Landing screen no longer hides the input box on short terminals.** When
  running unsandboxed, the multi-line sandbox-warning banner wraps to ~14
  rows at 80 cols; on a 24-row terminal it consumed the whole height and
  pushed the "Type a message" input box (and mode marker) off-screen. The
  banner is now capped to the room left after reserving the input box +
  hint, with a `… (+N more — see scrollback)` marker; the full banner still
  lives in scrollback. Found by the pty-bridge TUI E2E suite
  (`LandingReflow@80x24`), regressed by the v0.58.1 in-band-banner work.

### Infra

- **Build provenance now actually publishes.** The SLSA-3 provenance job
  (`slsa-framework/slsa-github-generator`) failed on every release since it
  was added — it's tag-pinned internally and this repo's Actions policy
  requires SHA-pinned actions, so it was rejected at "Set up job" and never
  produced an attestation. Replaced it with GitHub-native
  `actions/attest-build-provenance` (SHA-pinned), which attests every
  released artifact via `checksums.txt`. Verify with
  `gh attestation verify <artifact> --repo foobarto/stado`. Cosign signing
  is unchanged.

## v0.59.0 — MiniMax: Anthropic-compatible endpoint + M3 — 2026-06-01

### CLI

- **New `minimax-anthropic` provider.** Reaches MiniMax's
  Claude-compatible endpoint (`https://api.minimax.io/anthropic`) through
  the native anthropic-sdk-go with a base-URL override, so it gets prompt
  caching and extended-thinking support the OpenAI-compat path lacks.
  This is MiniMax's *recommended* path for M2.x reasoning and the one its
  Coding Plan subscription uses with Claude-Code-style tools. Set
  `MINIMAX_API_KEY` (works with a pay-as-you-go *or* Coding Plan key) and
  `provider = "minimax-anthropic"`. The existing `minimax` provider
  (OpenAI-compat `/v1`) is unchanged and still covers both billing modes.
- **`anthropic.New` gained `WithBaseURL` / `WithName` options** so the
  bundled anthropic provider can target third-party Anthropic-compatible
  endpoints without mislabeling them as first-party Anthropic. A custom
  base URL no longer borrows `ANTHROPIC_API_KEY` — the caller supplies the
  right credential.

### TUI

- **MiniMax-M3 in the model picker.** Added the 1M-context multimodal
  flagship (launched 2026-06-01) to the MiniMax catalog under both the
  `minimax` and `minimax-anthropic` providers; M2.7/M2.5/M2.1 (each with
  `-highspeed`) and M2 remain. No `M3-highspeed`/`M2-highspeed` exist per
  MiniMax's docs.

### Notes

- Over the OpenAI-compat `/v1` endpoint, MiniMax reportedly ignores
  `reasoning_effort`; prefer `minimax-anthropic` for reasoning-heavy work.
  The Coding Plan key has model restrictions (some `-highspeed` variants
  require a pay-as-you-go key), and the `/anthropic` Coding-Plan path is a
  slightly non-standard Anthropic protocol — watch for SSE/field quirks.

## v0.58.2 — TUI: wrap the startup banner on the landing screen — 2026-05-29

### Fixes

- **Startup banner no longer overflows the landing screen.** The v0.58.1
  banner was rendered without a width constraint, so the long unsandboxed
  warning line (~200 chars) didn't wrap — it overran the terminal width
  (lipgloss padded every banner line to that width) and the unwrapped
  height measurement threw off the landing-screen layout. The banner now
  wraps to the terminal width and uses the same tone it has in scrollback
  (`systemBlockTone`); `logoMaxH` is floored to avoid a negative value on
  short terminals. `splitBannerLines` also normalizes CRLF. Caught by a
  post-release review of #78.

## v0.58.1 — TUI: show startup banner in-band — 2026-05-28

### Fixes

- **Startup banner no longer vanishes under the TUI.** The sandbox
  posture, broker session, and writable-paths lines (plus the
  git-state-unavailable and system-prompt-template warnings) were
  written to stderr just before the TUI opened its alternate screen,
  which cleared the terminal — so they flashed and disappeared, never
  visible in the running TUI. They now render as a system block at the
  top of the session (after any replayed conversation). `stado run` /
  `stado headless` are unchanged — they don't take the alternate
  screen, so they still print to stderr.

  Scope: the always-present banner plus those two startup warnings.
  Deeper startup stderr (plugin-loader / ABI-verify warnings) is not
  yet captured.

## v0.58.0 — minimax.io provider — 2026-05-28

### UX

- **`minimax` provider added** as an OAI-compatible cloud preset
  (`https://api.minimax.io/v1`, `MINIMAX_API_KEY`). Model catalog
  pre-loaded with the seven MiniMax-M2.x chat models
  (`MiniMax-M2.7`, `MiniMax-M2.7-highspeed`, `MiniMax-M2.5`,
  `MiniMax-M2.5-highspeed`, `MiniMax-M2.1`, `MiniMax-M2.1-highspeed`,
  `MiniMax-M2`) at 200K context. `stado config providers`, `stado doctor`,
  the headless server providers list, and the TUI model picker
  all surface it.

  Note: minimax's `temperature` range is `(0.0, 1.0]` — narrower
  than OpenAI's `[0.0, 2.0]`. stado does not pre-validate; values
  outside the range return a 4xx from minimax at request time.

## v0.57.1 — TUI: lift system-block content truncation — 2026-05-28

### Fixes

- **TUI: long system blocks (notably `/tool ls` and `/tool <name>`
  result output) were truncated to ~480 chars.**
  `internal/tui/blocks_render.go` applied `truncate(blk.body, width*6)`
  to every system and `btw` block before rendering, so on a typical
  80-column terminal anything past ~12 lines was silently cut.
  Observed symptoms: `/tool ls` displayed only the first ~12 of ~180
  registered tools, and `/tool <name>` appeared to "do nothing"
  because the `plugin <id>/<tool> → <content>` envelope rendered as
  an empty-looking box once the cap fell inside the content. Fix
  removes the truncate cap entirely — lipgloss already re-wraps to
  the available width, and the viewport handles scroll. Regression
  test: `TestSystemBlock_LongBodyNotTruncated`.

## v0.57.0 — v1 security architecture: broker, sandbox-first default, mount table, ceiling/effective, taint substrate — 2026-05-27

The v1 security architecture rollout. DESIGN.md §"Broker" and §"Sandbox"
specify the model; PLAN.md §"v1 security architecture rollout" lists
the phases; this release lands phases 0–8 as a single PR (operator
ruling — the changes are far-reaching and shipping piecemeal would
fight tests + CI).

Decision record: see `docs/eps/0050-broker.md` for the design
rationale. Brand-new EP describing phase 1 in detail with cross-
references to DESIGN.md and PLAN.md.

### v1 security architecture rollout

- **Phase 0 — doc pass.** DESIGN.md grew §"Sessions and sub-agents",
  §"Broker", §"Context management" → "Provenance and taint",
  §"Audit" expansion (trust-root invariant, audit-trace writer
  invariant, broker-decision log); revised §"Sandbox" with sandbox-
  first default, `--no-sandbox` flag, launch-cwd RW boundary,
  sandbox-mode startup announcement, mount-and-namespace invariant
  table; replaced ASCII diagrams with mermaid. PLAN.md grew the
  §"v1 security architecture rollout" section with phases 0–8 +
  explicit non-goals table (proxy-as-floor / external policy engine
  / HTTPS-git / content classifier / airgap integration / per-call
  approval as containment).

- **Phase 1 — broker substrate.** `internal/broker/` package with
  `Service`, `Policy`, `SessionHandle`, `Purpose` (main-chat /
  subagent / tool-run), `Profile` (default / hardened / no-sandbox),
  `CapabilityRequest`, `Decision`, `DecisionWriter` / `DecisionRecord`,
  `DispatchError`. New JSON-RPC method namespace `broker.v1.*` on
  the existing daemon UDS socket: `session.create`, `session.terminate`,
  `toolrun.sandbox`, `policy.query`. `internal/daemon/ServerOpts.BrokerDispatcher`
  field routes the namespace to the broker via a typed-error bridge
  in `cmd/stado/broker_bridge.go`. Operator policy file at
  `$XDG_CONFIG_HOME/stado/policy.toml` (TOML), with a binary-embedded
  permissive default. `cmd/stado/broker_client.go` `attachToBroker` +
  `BrokerSession.Close()` helper; wired into every orchestrator entry
  point: TUI (`stado`), `stado run`, `stado headless`, `stado acp`,
  `stado mcp-server`. New e2e tests over real UDS daemon
  (`cmd/stado/broker_e2e_test.go`).

- **Phase 1g — `--sandbox-fs` retired, `--no-sandbox` added.**
  Pre-1.0 breaking change with inverted polarity. The earlier
  UX-pressured retreat (run.go:281–290 comment, now rewritten) is
  reversed: sandboxed-by-default across all entry points; the
  operator opts out via `--no-sandbox`. No deprecation alias. The
  launch cwd is the writable boundary in both modes.

- **Phase 2 — sandbox-first default enforcement.** `STADO_BROKER_ATTACH`
  default flipped to on (set to `0`/`false`/`off`/`no` to opt out for
  development scenarios). `BrokerSession.Ceiling` typed as
  `sandbox.Policy` (was opaque `any`). New
  `BrokerSession.AnnounceSandboxMode(w, surface)` banner emitted by
  every interactive surface at startup: sandbox state, profile,
  writable mount summary, credential-mask count.

- **Phase 3 — mount-and-namespace invariant table.**
  `internal/broker/mount_table.go` lifts DESIGN.md's table into code:
  `MountMode` enum (`NotMounted` / `ReadOnly` / `ReadWrite` /
  `BrokerOnly`), `DefaultMountTable(cwd)` + `HardenedMountTable(cwd)`,
  `ToPolicy()` translation, `MaskedPaths()` for the announcement
  banner. CI assertions in `mount_table_test.go` catch silent
  widening — the "no ssh private key in FSRead" invariant is now a
  runtime guarantee. `~/.ssh/config` decision: default profile
  mounts RO as-is (operator ergonomics); hardened profile gets a
  synthesised minimal config (no arbitrary-execution directives).

- **Phase 4 — session capability model in code.** Ceiling /
  effective vocabulary applied to `SessionHandle`. `Service.NarrowEffective`
  enforces the drop-only invariant — attempts to widen return
  `ErrEffectiveWiderThanCeiling` with operator-facing guidance
  ("fork a new session instead"). `SubagentCeiling(parent, role,
  mode, write_scope)` mechanically projects a child ceiling that
  is GUARANTEED a subset of the parent. `IsSubsetOf` per-field
  comparison with path-prefix semantics for FS sets (catches the
  /workfoo vs /work separator-prefix bug class via test).

- **Phase 5a — broker-decision log.**
  `$XDG_DATA_HOME/stado/broker/decisions.jsonl` (mode 0600, parent
  dir 0700). Every admit / deny / policy.query records a JSONL line
  with timestamp + full CapabilityRequest + full Decision.
  `cmd/stado/broker_bridge.go:buildBrokerService` opens the log
  file; nil-writer path is restricted to tests.

- **Phase 5c — broker ceiling threaded into Executor.Runner.**
  `internal/sandbox/ceiling_runner.go` `CeilingRunner` decorator
  wraps an existing Runner with a ceiling Policy; every per-tool-
  call Policy is intersected with the ceiling before delegating.
  Wired into `stado run` after `attachToBroker` (skipped for
  Skipped sessions + `--no-sandbox`). The mount table now ACTUALLY
  ENFORCES at the runner layer — bash can't reach paths the ceiling
  denies.

- **Phase 6 — taint substrate.** Per-session `Taint` state (Clean /
  Tainted), `Service.SetTaint` / `Taint` / `EvaluateWithTaint`,
  `broker.v1.session.taint` IPC method. Tainted context overlay
  refuses elevated-capability sub-agent grants (`git-fetch` /
  `git-sub-agent` roles reserved by `isElevatedSubagentRole` for
  phase 7). Context-management ingestion-site wiring deferred to
  phase 6b.

- **Phase 7 substrate — ssh-agent + git sub-agent.** Per operator
  ruling, the phase most expected to need tuning lands last. This
  release ships the broker-side substrate: `RoleGitFetch` /
  `RoleGitSubagent` constants, `GitSubagentSpec` (declared hosts +
  key paths + write scope + egress mode), `ProjectGitSubagentCeiling`
  (mechanical projection with attenuation guarantees + SSH_AUTH_SOCK
  Env injection), `IsForbiddenForGitSubagent` (dispatch-side
  fetch-not-push guard with allowlist + fail-safe default).
  Runtime wiring (bwrap socket bind-mount, approval-once prompt,
  hardened ssh-config synthesis, spawn_agent → broker integration)
  flagged for phase 7b follow-up.

- **Phase 8 — `stado session tree` as broker client.** The
  standalone cobra subcommand notifies the broker of the
  newly-forked session via `attachToBroker` + immediate `Close`
  after the sidecar fork commits. Best-effort: broker-unreachable
  scenarios print a stderr warning but don't fail the fork.

### Non-goals (explicitly deferred — see EP-0050 + PLAN.md)

- CONNECT / egress proxy as the v1 enforcement floor. The Linux
  network namespace is the floor; `internal/sandbox/proxy.go` is
  preserved byte-equal as a refinement layer.
- External / relational policy engine (OPA, Rego). CAP model
  sufficient for v1.
- HTTPS-git credential confinement. Bearer-token reality;
  documented honestly in DESIGN.md §"Git sub-agent" → "HTTPS-git
  is a known limitation".
- Content-safety classifier in the trust-critical path.
- Airgap-mode (`-tags airgap`) integration. Deferred to future
  phase.
- Per-tool-call approval as the containment boundary. Tool calls
  stay yolo-by-default for the chat session; the new approval is
  capability-grant at session-creation for socket-bearing
  sub-agents only.

### Carry-overs from in-flight security work (#68 / #69 / #70)

The v1 architecture branch was cut from main at commit `c9dfb2e`,
which already carried these merged-but-unreleased security fixes:

- **`#68` providers: `inherit_env` opt-in.** Reconciles PR #65/M
  acpwrap env-scrub (v0.56.0) with EP-0032's auth-env inherit
  contract. New per-provider `inherit_env = ["GEMINI_API_KEY",
  "OPENAI_API_KEY", …]` opt-in to `[acp.providers.<name>]` and
  `[mcp.providers.<name>]`; listed vars extracted from
  `os.Environ()` and forwarded to wrapped subprocess. Restores
  wrapped-agent auth flows (gemini-acp, opencode-acp, codex-mcp)
  silently broken in v0.56.0. EP-0032 amended; CHANGELOG of
  v0.56.0 retroactively flags the break.
- **`#69` tool: `CheckWritePath` factored + hardened.** Symlink
  + case-insensitive bypass closure (3-way convergent). Codex
  P0 finding from the post-v0.56.0 review.
- **`#70` runtime: wasm host imports capped before allocation.**
  Codex P2 — prevents an attacker-influenced manifest from
  exhausting wazero's import-resolution allocator at module-
  instantiation time.

### Infra

- Branch: `sec/v1-architecture` (16 commits 62e6aef → 25c05f6
  for the v1 work + commits since the v0.56.0 tag for #68/#69/#70).
- All existing tests pass under `-race` (cmd/stado, internal/broker,
  internal/daemon, internal/sandbox, internal/runtime). The
  test-binary auto-spawn refusal in `daemon.EnsureRunning` means
  every existing test transparently hits the Skipped broker path,
  so the broker-attach default flip is regression-clean.

### Migration notes

These are user-visible behavior shifts that don't break a public
schema but do change what an operator sees or how a pre-v0.57.0
workflow lands. None ship deprecation aliases (pre-1.0).

- **`--sandbox-fs` is gone; `--no-sandbox` is the inverse-polarity
  replacement.** `stado run --sandbox-fs` now errors with "unknown
  flag." Anyone whose script depended on the pre-v0.57.0 *opt-in*
  shape should drop the flag (sandbox is now the default) — or, if
  the script explicitly disabled sandboxing, add `--no-sandbox`.
- **Writable boundary moved.** Pre-v0.57.0 `--sandbox-fs` confined
  writes to `Session.WorktreePath` + `/tmp`. v0.57.0 confines them
  to the launch cwd + `/tmp` unconditionally for `stado run`. The
  worktree is still under cwd when launched from inside a project,
  so the typical case is unchanged; the explicit alternate-cwd
  + worktree-symlink path now writes to the cwd subtree, not the
  worktree.
- **`STADO_DAEMON=off` no longer reliably blocks the daemon for
  orchestrator surfaces.** TUI, `stado run`, `stado headless`,
  `stado acp`, and `stado mcp-server` now attach to the broker
  (an evolution of the daemon) on launch via `attachToBroker`, which
  auto-spawns the daemon as needed and does NOT consult
  `STADO_DAEMON`. To prevent broker attach on those surfaces, set
  `STADO_BROKER_ATTACH=0` (or `false` / `off` / `no`). The
  `STADO_DAEMON=off` knob still works for the `stado tool run`
  single-shot path and for the PTY refusal error message —
  `cmd/stado/tool_run_daemon.go` flags this explicitly.
- **New 2–3 lines of stderr at every orchestrator launch.** Each
  surface now prints a sandbox-mode banner after broker attach:
  - `stado: sandbox=default session=<sessionid> (broker-mediated)`
  - `stado: writable: /work, /tmp`
  - `stado: N credential paths masked (~/.ssh/id_*, ~/.aws, …)`
  Skipped attaches print a `broker attach skipped (<reason>)` line
  instead. Tests and scripts that capture stderr verbatim may need
  to be loosened.

## v0.56.0 — deep-dive backlog sweep (I/J/K/L/M/N/O/Q clusters) — 2026-05-24

Closes 12 of the 14 remaining deep-dive findings carried over from the
post-v0.54.0 codex + gemini security review. Four bundled PRs (#62–#65),
2 P0 / 7 P1 / 3 P2 across TUI rendering, tool-dispatch filter, sandbox
host-guards, audit message parsing, provider env isolation, daemon
resource caps, and supervisor caching. One finding (C8/P — audit v1
signature downgrade) is **parked** for design review pending operator
input on the v0.54.0-era v2 signature migration shape (see
[internal/audit/PARKED-c8-p-v1-downgrade.md](internal/audit/PARKED-c8-p-v1-downgrade.md)).

### TUI

- **SanitizeForTerminal sweep — PR #49 sibling misses** (PR #62 —
  Cluster J, Codex G3/J-a P0 + G4/J-b P0 + C2/J-c P1). Five remaining
  unsanitized-text-to-terminal sites scrubbed at the earliest store
  boundary:
  - `model_stream.go` EvThinkingDelta accumulator + block body
    (`m.turnThinking` was the lone case in handleStreamEvent's event
    switch appending raw `ev.Text` after PR #49). Sanitize-before-
    budget so the byte counter matches what's actually stored
    (Codex round-1 catch — escape-heavy thinking streams were
    false-positive-rejecting under the prior order).
  - `modelpicker/picker.go` catalog row render — `it.ID`, `it.Origin`,
    `it.Note` all `StripControlChars` (single-line table layout).
  - `fleetpicker/picker.go` row + detail-pane field rendering —
    helper `singleLineSafe` does `\n/\r/\t → " "` BEFORE
    StripControlChars to keep word boundaries intact (Copilot
    round-1 catch — bare StripControlChars mashed "two\nwords" into
    "twowords").
  - `host_ui_render.go` decode boundary — every plugin-supplied
    string at the wasm→host trust boundary is sanitized once:
    Title/Footer/ID + every Section body kind (text/kv/list/code/
    table/diff). Header zones strip; multi-line prose zones preserve
    \n/\t/\r.
  - `handler_tools.go` `onPluginApprovalRequest` — title `StripControlChars`,
    body `SanitizeForTerminal`. Approval drawer's lipgloss.Render
    now sees a trustable struct.

### Tool dispatch + sandbox host-guards

- **`daemon dispatch` consults `[tools].enabled`** (PR #63 —
  G2/I-a P0). PR #50 fixed the same shape in `/tool` slash dispatch,
  `runToolByName`, and mcp-server registry; daemon dispatch was the
  surviving sibling miss. When operator sets a non-empty Enabled
  allowlist, tools outside it now refuse at dispatch.

- **daemon `toolInAllowList` glob-matches via runtime.ToolMatchesGlob**
  (PR #63 — G7/I-b P1). Pre-fix `a == tool` exact-string match
  silently failed against wire-form names — `["shell.*"]` allowlist
  rejected `shell__bash`. Routed through the canonical matcher so
  wire / canonical / wildcard / legacy-bare forms all line up with
  every other filter surface.

- **mcp-server re-applies `ApplyToolFilter` after llm.invoke
  registration** (PR #63 — C1/I-c P1). The mcp-server-only
  `llm.invoke` tool was registered AFTER
  `BuildRegistryWithPlugins`→`ApplyToolFilter` had run, so
  `[tools].disabled=["llm.invoke"]` couldn't remove it.

- **`acpHost` + `daemonToolHost` implement `WritePathGuard`** (PR #63
  — K P0). Both hosts previously lacked `CheckWritePath`, so
  `fs.write` through the ACP server's host or the daemon RPC
  bypassed the `.git`-write guard #050 + acpwrap had in place.
  Both now implement the same defense verbatim (walk path segments,
  refuse any `.git` segment).

- **Plugin runtime: per-tool cfg + invokeReg replace package
  globals** (PR #63 — C4/Q P2). `installedRunCfg` /
  `installedInvokeReg` were rebound by every
  `registerInstalledPluginTools` call; `/tool info` triggering an
  unfiltered registry build leaked its tool surface into the
  in-flight session's nested-invoke dispatch. Refactored to
  per-tool storage on `bundledPluginTool` / `installedPluginTool` /
  `pluginOverrideTool` so each build's tools are anchored to that
  build's registry pointer.

### Audit

- **`audit.ParseMessage` is the canonical parser; duplicate impls
  removed** (PR #64 — G6/L P1). `cmd/stado/stats.go` +
  `internal/runtime/sessionstats/sessionstats.go` each had a copy
  using the pre-#51 `TrimSpace(line[:idx])` shape — the exact bug
  Codex #143 round 2 hardened against. Both routed through the
  canonical `audit.ParseMessage`; the false claim of an import
  cycle that justified the duplicates is gone (cmd/stado has
  imported internal/audit elsewhere for a long time).

### Providers + resource caps

- **ACP env scrub** (PR #65 — C3/M P1). The ACP wrapper inherited
  the full `os.Environ()` by default (worst when `Config.Env` was
  empty — Go's `os/exec` inherits everything if `cmd.Env` stays
  nil). Extracted MCP's `scrubbedEnv`/`envSafelist` into shared
  `internal/providers/envscrub`; ACP now uses the same scrub
  unconditionally. Cloud creds / API keys / minisign secrets stop
  at the trust boundary.

- **stado_tool_invoke caps name + args at the wire boundary**
  (PR #65 — C5/N-a P2). Pre-fix `nameLen` / `argsLen` were trusted
  before any cap check — a plugin passing `argsLen=2GB` forced the
  host to allocate the same. Now refuses up front against
  `maxPluginRuntimeToolNameBytes` (1 KiB) + the existing
  `maxPluginRuntimeToolArgsBytes` (1 MiB).

- **daemon `readFramedLine` caps live allocation at
  MaxRequestBytes** (PR #65 — C6/N-b P2). Pre-fix
  `bufio.Reader.ReadBytes('\n')` buffered the ENTIRE line before
  the MaxRequestBytes check — a same-UID peer sending a 32 MiB+
  line forced the daemon to allocate the same. Replaced with
  ReadSlice + chunked `append` (amortized O(n), not O(n²) per
  Codex P2 round-1 catch); cap-overflow keeps consuming to the
  frame's `\n` so the connection resyncs without separate drain
  logic.

- **Supervisor cache keyed by (provider, model)** (PR #65 — C7/O
  P2). `cachedSupervisorLookup` returned the first cached provider
  regardless of mid-session config changes — operator who
  reconfigured supervisor kept hitting the stale instance until
  restart. New `supervisorCacheKey{provider, model}` invalidates
  on change; rotation closes the prior provider via `io.Closer`
  assertion so ACP/MCP-wrapped subprocesses are reaped properly
  (Codex P2 round-1 catch — naive overwrite leaked the old
  subprocess + FDs).

### Parked

- **C8/P (audit v1 signature downgrade via ExtractSignature)** —
  the codex-proposed fix (scheme marker + bounded v1 fallback)
  breaks v0.54.0-era v2 signatures (no marker to distinguish them
  from downgrade-attack targets; canonical bytes differ between
  v1 and v2, so blanket "no v1 fallback when marker absent"
  rejects legitimate v0.54.0 v2 sigs). Needs operator design
  call between cutoff-date, body-marker-with-grace-period,
  per-repo strict-pin, or re-sign-in-place. Tracked in
  [internal/audit/PARKED-c8-p-v1-downgrade.md](internal/audit/PARKED-c8-p-v1-downgrade.md).

### Breaking surfaces

None this cycle — all fixes are defense additions over existing
behavior. The bundled-tool runtime refactor (Q) preserves
observable semantics; existing plugins see the same surface.

### Removed surfaces

- Local copies of `envSafelist` / `scrubbedEnv` in
  `internal/providers/mcpwrap/provider.go` (replaced by thin
  wrappers calling `envscrub.Scrub`).
- Duplicate `parseCommitMessage` impls in `cmd/stado/stats.go` +
  `internal/runtime/sessionstats/sessionstats.go` (replaced by
  thin wrappers calling `audit.ParseMessage`).
- Package globals `installedRunCfg` + `installedInvokeReg` in
  `internal/runtime/installed_tools.go` (replaced by per-tool
  fields).

### Plugin ABI migration note

No host-import additions or removals. The wasm calling contract
is unchanged.

## v0.55.0 — security hardening cluster H+R+S+T+U — 2026-05-23

Clears all four validated-Codex findings dropped post-v0.54.0 plus the
deep-dive's macOS sandbox escape (H). Five PRs (#55–#59), 3 HIGH /
2 MEDIUM criticality. Backward-compatible (audit v2 signatures, the
sandbox-profile and tool-filter behaviors are strictly more-restrictive
per least-privilege; one operator-facing change documented under
*Breaking surfaces* below).

### Sandbox

- **CWD `(allow process-exec (subpath cwd))` clause dropped** (PR #55 —
  Cluster H, deep-dive finding). The macOS `sbx_profile` allowed
  arbitrary process-exec under the operator's checkout — a write tool
  that planted a binary at `<cwd>/foo` could exec it from inside the
  sandbox, breaking the per-tool exec allowlist. Removed the clause;
  `Policy.Exec` is now the only path to exec. `TestRenderSandboxProfile_CWDNotProcessExecable`
  pins the invariant. Tools that legitimately need to exec their
  output must list the path in their `Policy.Exec` allowlist.

- **Firejail `bind_rw` policy fail-closed** (PR #57 — Cluster S, Codex
  HIGH/HIGH). Pre-fix `pickRunner` returned an empty string when
  `runner="firejail"` + `bind_rw` non-empty (`firejailCanEnforce`
  returns false for any non-empty `BindRW` — firejail can't faithfully
  enforce an arbitrary RW allow-list at all; no firejail flag
  combination makes this configuration safe); `doRewrap` then treated empty
  runner as the "no wrapper installed" case and, with default
  `RefuseNoRunner=false`, warned + returned nil → tools ran **fully
  unsandboxed**. Operators who configured `mode="wrap"` +
  `runner="firejail"` + `bind_rw="..."` silently lost all sandbox
  confinement.

  `pickRunner` now returns `(string, error)` and emits a hard error for
  the unenforceable-policy case. `doRewrap` checks the policy error
  before the missing-wrapper warn-and-continue path. The "no runner
  installed at all" path keeps the prior warn+nil behavior under
  `RefuseNoRunner=false`. Copilot/Codex round-1 catch: the
  `firejailInstalledButUnsafe` heuristic now only fires when
  `exec.LookPath("firejail")` actually finds the binary.

### TUI

- **Kill-switch can no longer be defeated by tool-cancel race** (PR #56 —
  Cluster R, Codex HIGH/HIGH, regression of #46). Pre-fix
  `cancelRunningTool` + `clearPendingToolQueue` cleared the tool context
  and dropped the queue, but `executeCallAsync` converted the
  `context.Canceled` into a normal `toolResultMsg{Content: "cancelled
  by user"}`. `onToolResult` then appended it → `advanceToolQueue`
  drained the (empty) queue → `toolsExecutedMsg` → `onToolsExecuted`
  unconditionally called `startStream()` → the model could request
  another bash/network tool. Operator-pressed Esc was effectively a
  no-op.

  New `Model.turnCancelled` flag, set by both cancel helpers and by
  `clearPendingToolQueue` when work was dropped, cleared at
  `startStream`. `onToolsExecuted` checks the flag before dispatching
  the next provider request — when set, it persists the `tool_result`
  blocks to history (keeps the conversation paired so the next turn
  isn't rejected by OpenAI), promotes any queued prompt instead, and
  refuses to re-stream the cancelled turn.

### Audit

- **Snapshot failure on mutating tools surfaces as Error**
  (PR #59 — Cluster U, Codex MEDIUM/MEDIUM). `BuildTreeFromDir` has
  hard caps (256 MiB blob, 200000 entries, depth 128). Pre-fix a
  mutating tool whose post-snapshot exceeded the cap got a `slog.Warn`
  + no tree commit; trace ref was written but the signed post-state
  tree was silently absent — `audit verify` doesn't notice missing
  refs. An attacker-influenced prompt could create the oversized state
  via auto-approved tools, then any subsequent mutation slipped past
  the signed-audit invariant.

  New contract: snapshot failure on a mutating tool now appends
  `"audit snapshot failed (tree-ref skipped, mutation NOT in signed
  audit): <reason>"` to BOTH `meta.Error` (visible in the trace
  commit's `Error:` trailer and the JSON audit exporter) AND
  `res.Error` (returned to the model). The mutation itself isn't
  undone — it already happened — but downstream consumers see the
  gap. Cluster U round-1 catch from both Codex P2 and Copilot: the
  fix initially left `meta.Summary` stamped `[ok]` and the OTel span
  at `OK` even with the `Error:` trailer present, so operator
  dashboards keyed on Summary/span saw success. v0.55.0 restamps
  Summary, the `tool.outcome` span attribute, and the span status
  to match the surfaced error.

  Exec tools' snapshots remain informational (used only to detect
  preTree→postTree diff) — failure stays a slog warning, no
  `res.Error` mutation.

### Release

- **Upstream binary digests pinned in source** (PR #58 — Cluster T,
  Codex HIGH/HIGH, regression of EP-0042 Part B). Pre-fix
  `hack/fetch-binaries.go` fetched the expected SHA256 digests live
  from the SAME upstream release source as the binaries themselves
  (ripgrep's `.sha256` sidecar; ast-grep's `expanded_assets` HTML).
  If the upstream release/account was compromised, the attacker-
  controlled bytes passed the same-source digest check; signed stado
  release embedded them; bundled rg/ast-grep executed attacker
  native code under user privileges. The generated digest pins were
  `.gitignore`-d, so digests were not reviewable in source control.

  New `hack/binary-pins.json` is committed and reviewable, with
  per-version per-asset digests for ripgrep 14.1.1 and ast-grep 0.38.7
  across the five-platform matrix (Linux amd64/arm64, Darwin
  amd64/arm64, Windows amd64). `fetchRipgrep`/`fetchAstGrep` look up
  the digest there; live-fetched sidecar/expanded-assets paths and
  the `internal/releaseassets` helper are removed. Upstream account
  compromise no longer leaks into the signed stado release —
  reviewers see digest changes in PRs.

  Copilot round-1 catches: `flag.Parse` runs before `loadPinnedDigests`
  so `-h` / invalid-flag invocations no longer fail on a missing pin
  file; `normalizeAndValidateDigests` trims, lowercases, and
  hex.DecodeString-validates every entry at load time, naming the bad
  asset by tool@version (typos surface before the full download
  rather than as a generic post-download "digest mismatch").

### Breaking surfaces (one — pre-1.0 no-kid-gloves)

- **`mode="wrap"` + `runner="firejail"` + `bind_rw="..."`** now hard-errors
  at sandbox-setup time instead of silently running unsandboxed. There
  is no firejail flag that fixes this — the operator must either drop
  `bind_rw`, or switch runner to `bwrap` (which can enforce arbitrary
  RW binds). `RefuseNoRunner=true` does NOT remediate this case: the
  policy-unenforceable path now hard-errors regardless of that flag.
  (`RefuseNoRunner` still controls the separate "no runner installed at
  all" case: default `false` warns + continues unsandboxed, `true`
  hard-errors.) The pre-fix behavior was a fail-open vulnerability;
  there is no compatibility carve-out.

### Removed surfaces

- `internal/releaseassets` package (depended on by the old live-fetched
  digest path; no longer needed since digests are pinned in source).
- `fetchSHA256Sidecar`, `fetchGitHubExpandedAssetDigests`,
  `maxFetchSidecarBytes`, `maxFetchMetadataBytes` constants from
  `hack/fetch-binaries.go`.

### Plugin ABI migration note

No host-import additions or removals. The wasm calling contract is
unchanged.

### Fixes

- The `m.turnCancelled` flag is set even when `cancelRunningTool`
  returns false (closes the timing window where `toolCancel` was nil
  but `pendingCalls` was non-empty between `onToolResult` clearing
  the pointer and `advanceToolQueue` starting the next tool —
  Copilot round-1 catch on Cluster R).
- Cancelled turns now persist the `tool_result` blocks to `m.msgs`
  so the conversation history stays paired (the assistant message
  containing the `tool_use` blocks is already persisted by
  `onTurnComplete`; leaving the `tool_use` unpaired produces an
  invalid history rejected by OpenAI Chat Completion on the next
  turn — Copilot round-1 catch on Cluster R).

## v0.54.0 — security hardening cluster C+D+E — 2026-05-23

Closes the 10/10 P0 sweep from the second-pass Codex triage with three
substantial security PRs on top of v0.53.0. All three preserve backward
compatibility (audit v1 signatures still verify; old `[tools].enabled`
behavior is the only intentionally-narrowed surface — and that's strictly
more-restrictive per least-privilege).

### Audit

- **Signature scheme bumped v1 → v2** (PR #51 — Cluster D, Codex #138).
  Pre-fix the signed payload covered tree + parents + body. Author /
  committer / timestamps were NOT bound — an attacker with sidecar write
  could rewrite the author identity or backdate the commit time via
  `git filter-branch` / `git replace --graft` / direct object surgery and
  the signature on the unchanged tree+parents+body still verified.
  Tamper-evidence broke silently.

  v2 framing (`stado-audit-v2`) adds `author <name> <email> <unix-time>`
  and `committer <name> <email> <unix-time>` lines after the parent
  block — mirroring git's own commit-object format. Changing any field
  invalidates the v2 signature. `audit.VerifyV2` tries v2 first then
  falls back to v1, so pre-fix audit history continues to verify
  cleanly after operators upgrade. `audit.Signer.Sign` (v1) is kept so
  the existing `state/git.CommitSigner` interface stays stable for
  legacy stub signers; production paths route through the new
  `CommitSignerV2` extension interface via type assertion.

- **Trailer-injection defense, two layers** (PR #51 — Cluster D,
  Codex #143 + #144). Pre-fix `CommitMeta.formatMessage` wrote trailer
  values verbatim — `Plugin: "evil\nTool: bash\nAgent: forged"` injected
  three trailer lines that `audit/export.go`'s `parseMessage` honored
  under last-write-wins. `CompactionMeta.formatMessage` wrote the
  summary verbatim before the trailer block — `"Tool: bash"` in a
  summary line injected a fake trailer.

  Layer 1 (format-time): new `cleanTrailerValue` + `cleanTrailerKey`
  helpers wrap every trailer write — values get newlines flattened to
  spaces, C0/DEL/C1 control runes stripped; keys enforce ASCII
  alnum/-/_ grammar. Compaction summary is two-space indented per
  line. Layer 2 (parse-time): `parseMessage` rewritten to recognize
  only the LAST contiguous run of well-formed (unindented, grammar-
  matching) trailer lines — `[A-Za-z][-_A-Za-z0-9]*:` shape, no
  whitespace trim on keys. Anything before that run is body. Codex
  caught the layer-1-without-layer-2 gap on round 2.

### TUI

- **Terminal-escape sanitizer at every untrusted-text-to-terminal sink**
  (PR #49 — Cluster C, Codex #077 P0 + 8 P1/P2 siblings). New
  `textutil.SanitizeForTerminal(s)` strips control runes except
  `\n`/`\t`/`\r` (prose-safe; existing `StripControlChars` keeps
  stripping everything for single-line identifiers). Wired at nine
  sinks across `sessionstats` (at-quit summary names), `tui` (model
  picker, EvTextDelta assistant prose), plugin runtime (`stado_ui_print`,
  `stado_progress`, choice prefix / default / label / prompt + manifest
  `host.Manifest.Name`), CLI (`plugin info`, usage table model column,
  memory list IDs). `#077` was P0 because the at-quit summary writes
  to stderr AFTER Bubble Tea releases the terminal — OSC 52 (clipboard
  hijack), OSC 8 (clickable-hyperlink injection), CSI cursor moves
  reach a terminal that's no longer eating escapes.

### Tool dispatch

- **Six `[tools].enabled/.disabled` bypasses fixed** (PR #50 —
  Cluster E). `ApplyToolFilter` now applies Disabled as a subtractive
  pass after the Enabled allowlist — pre-fix
  `[tools].enabled = ["*"]` + `[tools].disabled = ["bash"]` left bash
  registered (Codex #096). `/tool` slash
  dispatch now consults the filter (Codex #064, P0 — direct bypass
  of operator security control). `/tool unautoload <name>` on a
  default config now actually removes from the autoload set (Codex
  #088, P0 — the prior empty Autoload fell back to defaults, silently
  reverting the operator's intent). Plugin nested-invoke via
  `stado_tool_invoke` honors `[tools].disabled` (Codex #071). `stado
  tool run <X>` checks the user-typed query string against patterns
  in addition to the resolved registered name (Codex #089) AND
  enforces `[tools].enabled` with a `--force` escape (Codex #123).

### Breaking surfaces (three — all per pre-1.0 no-kid-gloves norm)

- **`[tools].enabled` + `[tools].disabled` with overlapping entries**
  — DISABLED now wins (least-privilege). Pre-fix enabled won and was
  pinned as intent by a test (the test was pinning a bug; replaced
  by `TestApplyToolFilter_DisabledWinsOverEnabled`).

- **`stado tool run <X>`** with `[tools].enabled` non-empty and `<X>`
  not in the allowlist: previously ran; now refused with an actionable
  error. `--force` escape preserves the operator-explicit override.

- **macOS sandbox profile** (covered in v0.53.0 — restated for the
  v0.54.0-aware audit migration note below): tools without an
  explicit `Policy.Exec` allowlist hit "process-exec denied" inside
  sandbox-exec. Matches the Linux side.

### Audit migration note (third-party verifiers)

The signed-payload framing bumped from `stado-audit-v1` to
`stado-audit-v2`. Existing v1 signatures continue to verify via
`audit.VerifyV2`'s fallback path; new commits produce v2 signatures.
Third-party tooling that re-implements the audit signature scheme
should accept BOTH framings during the migration window (try v2
first, fall back to v1) and switch to v2-only emission when the
operator has confirmed all historical commits in scope are v2.

### Removed surfaces

None — backward compatibility preserved at every layer.

### Plugin ABI migration note

No host-import additions or removals. The wasm calling contract is
unchanged.

### Fixes

- `[tools].autoload` materialization now uses canonical-aware
  matching, so `/tool unautoload shell.bash` removes the wire-form
  `shell__bash` default (Copilot caught this on PR #50 round 2).
- `sessionToolOverrideHidesTool` uses glob-aware matching, so
  `disableAdd=["fs.*"]` correctly hides `fs__read` (Copilot, PR #50).
- `handleToolExecSlash` builds its effective config from the
  freshly-loaded disk cfg, not the possibly-stale in-memory snapshot
  (Codex P1, PR #50).

## v0.53.0 — security hardening cluster + EP-0042 follow-on — 2026-05-23

Six security fixes (one P0 cluster A pair, one P0 cluster F single, one P0 cluster G single, one P0 cluster B pair plus the queue-clear fix) on top of the previously-shipped EP-0042 (binaries-out-of-tree) work that hadn't been tagged yet. Three breaking surfaces — see "Breaking" below.

### Plugins

- **Seed deny-list for 12 leaked Ed25519 fingerprints** (`internal/plugins/revoked.go`, PR #40).
  The example-plugin `.seed` files that were committed before the `.seed` gitignore landed are now hard-revoked. Trust path refuses to verify any manifest carrying those fingerprints — even if the operator previously trusted them — and the runtime-override path (verifyPluginOverride) now consults the same deny-list before any wasm-digest check, closing a bypass Copilot caught during review. Fingerprint lookup is case-insensitive. See SECURITY.md "Built-in deny-list" for the full list.

- **EP-0042 Part A — optional/demo plugins moved to `foobarto/stado-plugins`** (PR #25). 23 plugins (browser, http-session, mcp-client, web-search, etc.) live in the separate signed-bundle repo; install with `stado plugin install github.com/foobarto/stado-plugins/<plugin>@v0.1.0`. The anchor pubkey (`.stado/author.pub`, fp `57a3e58c…`) is offline-held.

- **EP-0042 Part B — bundled wasm built from source at build time, not committed** (PR #36). `make build` runs `plugins/bundled/build.sh` automatically; no `.wasm` files in the source tree. CI builds wasm before every job. The decision NOT to commit/sign/fetch the binary was settled (`docs/eps/0042-binaries-out-of-source-tree.md` D6) — see SECURITY.md for the provenance argument.

### Sandbox

- **`sandbox.WarnIfHostUnsandboxed` — once-per-process warning at every TUI / run / headless / session-resume entry point** (PR #42, `internal/sandbox/announce.go`). Surfaces "host subprocesses are inheriting the host's FS + network" when no process-containment is active. Suppress with `STADO_SUPPRESS_SANDBOX_WARN=1`. Distinct messages for `mode=off`, `mode=wrap` (configured-but-not-rewrapped — TUI/headless/resume don't re-exec today), and `mode=external` (operator-managed-but-no-wrapper-evidence). See SECURITY.md "Host sandbox".

- **`sandbox.DenyAll()` actually denies exec** (PR #43). The constructor literally named "DenyAll" returned `Policy{Exec: nil}`, which `ResolveBinary` treats as "no restriction" — DenyAll allowed every binary. Now returns `Exec: []string{}` explicitly so the deny path engages. Test `TestDenyAll_DeniesExec` pins the invariant against future regression. Same fix for `ReadOnlyFS()`.

- **macOS `sbx_profile` no longer emits a blanket `(allow process-exec*)`** (PR #43). The wildcard defeated the per-binary allowlist (sandbox-exec union semantics) — every exec was allowed on macOS regardless of `Policy.Exec`. Now drops the wildcard and resolves `Policy.Exec` basenames to absolute paths via `exec.LookPath` so the `(literal "...")` predicate matches what sandbox-exec actually sees.

### Runtime

- **ACP ABI verifier compiles the bytes that were actually verified** (PR #44, `internal/runtime/installed_abi.go`). Previously read the wasm twice: first with unguarded `os.ReadFile` (no size cap, follows symlinks), then with `ReadVerifiedWASM` whose return was discarded, then compiled the FIRST (unverified) bytes. A local attacker who could mutate the installed plugin dir could substitute a 4 GiB file or a symlink to `/dev/zero` and OOM the host, OR race a swap between the two reads. Single verified read now.

### TUI

- **BTW questions go through the configured supervisor provider** (PR #45). `[supervisor] provider = ...` + `[supervisor] enabled = true` was documented as a privacy partition (operator points the supervisor at a local Ollama, keeps a cloud worker for heavier turns) but `startBtw` ignored the config — every BTW question went to the worker, leaking the transcript. New `resolveSupervisorLane` helper at `internal/tui/supervisor_lane.go` redirects via a Model-level cached provider (so ACP/MCP supervisors that spawn subprocesses don't get re-spun per call). Lookup failure surfaces as a btwResultMsg error rather than silently falling back — defeating the trust boundary is worse than a loud failure.

- **Cancel kill-switch restored for in-flight tools and pending tool queue** (PR #46). Esc/Ctrl+G, Alt+Enter, and the `/cancel`/`/stop`/`/queue-now`/`/force` slash commands previously only fired `m.streamCancel`, so during the tool-execution phase of a turn (when `streamCancel` is nil and the active tool's context is held by `toolCancel`) the operator had no way to stop a runaway bash, network, or plugin tool — the kill switch silently did nothing. New `cancelRunningTool` + `clearPendingToolQueue` helpers (`internal/tui/cancel.go`); all four cancel paths now drop the running tool AND the pending tool queue, reporting `(N pending tool(s) dropped)` in the system block when applicable.

### Bundled tools

- **`ls` + `fs` exec path-guarded to workdir** (PR #38). Operands resolving outside the operator's workdir are now refused with a clear error, closing a Codex finding about workdir-escape via crafted path operands.

### Infra

- **OpenSSF Scorecard hardening + Best-Practices badge** (PR #27). SHA-pinned actions, least-privilege token permissions, badge in README.

- **Dependency bumps**: mcp-go, x/mod, x/crypto, anthropic-sdk-go (PR #34); google.golang.org/api 0.278.0 → 0.280.0 (#29); codeql-action 3.36.0 → 4.36.0 (#28).

### Removed surfaces

- All previously-committed `plugins/bundled/*.wasm` files (EP-0042 Part B). `make wasm` builds them on demand; `.gitignore` covers the output dir.
- All `plugins/examples/*/` directories (EP-0042 Part A) — those plugins live in `foobarto/stado-plugins` now. Install via `stado plugin install github.com/foobarto/stado-plugins/<name>@v0.1.0`.
- The unconditional `(allow process-exec*)` line in the macOS sandbox-exec profile (see Breaking below).

### Plugin ABI migration note

No host-import additions or removals. The wasm calling contract is unchanged.

### Breaking

- **macOS sandboxed tools that didn't declare an Exec allowlist** will now hit "process-exec denied" inside sandbox-exec. Previously the blanket `(allow process-exec*)` allowed everything; with that gone, callers MUST declare every binary they need to spawn in `Policy.Exec`. This is the intended capability semantics per DESIGN §"Capabilities are declared, the OS enforces" — the prior behavior was a bug, not a feature. Matches the Linux side.

- **BTW questions now go to the supervisor provider when configured.** Operators who had `[supervisor]` configured but were relying on the bug (questions still going to the worker) will see their BTWs reach the supervisor for the first time. This is what the config was supposed to do all along; the migration is to make sure the supervisor provider is actually reachable.

- **`/cancel`, `/stop`, `/queue-now`, `/force`, Esc/Ctrl+G, and Alt+Enter now also abort the pending tool queue.** Previously a multi-tool turn would continue through pending tools after the cancel; now the whole turn ends. Operators relying on the "cancel current, finish rest" semantic do not get it — that semantic wasn't intentional in the first place.

### Fixes

- **EP-0042 build-environment bug fixes** (PR #35): `hack/fetch-binaries.go` was writing to `internal/tools/{rg,astgrep}/` but the embed package lives at `internal/{rg,astgrep}/`; fixed plus stale `.gitignore` paths.
- **Bundled wasm CI**: ci.yml + codeql.yml run `bash plugins/bundled/build.sh` before test/lint/build/CodeQL jobs.

## v0.52.7 — bump golang.org/x/net (GO-2026-5026) — 2026-05-22

### Infra

- **Bump `golang.org/x/net` v0.54.0 → v0.55.0** (and direct dep
  `golang.org/x/sys` v0.44.0 → v0.45.0, pulled along by the upgrade) to clear
  `GO-2026-5026` — idna's failure to reject ASCII-only Punycode-encoded
  labels, reached via `httpclient.Client.Request → http.Client.Do →
  idna.ToASCII`. The govulncheck CI step flagged it on the v0.52.6 run;
  `govulncheck ./...` is now clean again. Scope is the root module / shipped
  `stado` binary; the optional `browser`/`browser-minimal` plugin modules
  still pin older `x/net` and are tracked separately.

## v0.52.6 — `stado tool ls` plugin column — 2026-05-22

### Fixes

- **`stado tool ls` now shows the plugin for autoloaded tools.** The PLUGIN
  column was blank for user-plugin tools (only bundled tools populated it); it
  now falls back to the tool's `PluginName()`, so every plugin-backed tool lists
  its source consistently (and in `--json`).

## v0.52.5 — fix TUI version display — 2026-05-22

### Fixes

- **TUI showed `0.0.0-dev`.** The TUI reads `internal/version.Version`, but the
  Makefile only ldflag-set `main.version` (used by `stado --version`), so
  `make`-built binaries displayed the right `--version` yet `0.0.0-dev` in the
  status bar/landing. The Makefile now sets both (goreleaser already did). Also
  fixed the sidebar/status modal, which hardcoded `"0.0.0-dev"` instead of
  reading the version var — that affected released binaries too.

## v0.52.4 — security hardening (batch 5: caps / ACP / trust / sandbox) — 2026-05-22

### Security

- **Capability path containment** — fs symlink caps no longer escape the workdir
  via a repo-controlled suffix symlink (#016); `cfg:*` path templates are
  contained under the cfg value and reject `..` (#054).
- **Plugin trust on every run** — `stado tool run` of an installed plugin now
  re-verifies digest + signature + revocation (CRL/Rekor) (#023); the bundle
  verifier always requires a valid wasm digest (no empty-digest bypass, #026);
  user bundles can't shadow a trusted built-in wasm by name (#027).
- **ACP/MCP isolation** — ACP permission auto-approve is scoped to stado's own
  MCP/fs methods and fails closed otherwise (#051); the ACP wrapper's
  send-on-closed-channel panic is fixed (#052); MCP-wrapped subprocesses get a
  scrubbed env + worktree cwd (#048); global MCP auto-registration requires
  explicit consent (#049); ACP fs writes into `.git/` are refused (#050,
  defense-in-depth).
- **Sandbox fail-closed** — firejail wrap mode no longer returns an unconfined
  wrap when it can't enforce the filesystem policy (#035).
- **Plugin dev** — the manifest-seed copy uses symlink-safe, size-limited writes
  (#004).

Several related findings (#008 nested-invoke audit, #036/#056 unsandboxed
exec/LSP posture, #027 bundler-key pinning, #050 full worktree routing) are
deferred pending a design decision.

## v0.52.3 — .env loader guard, gofmt sweep, Scorecard workflow — 2026-05-22

### Security

- **`.env` can't inject dynamic-loader keys (#044).** A repo-committed `.env`
  auto-loaded from cwd could set `LD_PRELOAD`/`LD_LIBRARY_PATH`/`DYLD_*` (or
  `PATH`) — code execution in every child process. These keys are now filtered
  from `.env`. (`STADO_*` remains loadable — configuring stado via `.env` is a
  supported feature.)

### CI / Docs

- **OpenSSF Scorecard workflow added** — the README badge had no backing run; it
  now publishes results so the badge resolves. SARIF uploaded to code scanning.
- **Security Policy badge** linking `SECURITY.md`.
- **gofmt sweep** — formatted 39 files with the go.mod-pinned toolchain (gofmt
  was not lint-enforced, so drift had accumulated).

## v0.52.2 — security hardening (batch 4) — 2026-05-22

### Security

- **Detection no longer execs untrusted binaries (#047/#053).** Integration
  version-probing only runs binaries resolved from trusted well-known install
  paths, not PATH — opening `/model` or running `doctor` in a repo with a
  shadowing `./bin` is no longer arbitrary code execution.
- **Git-status probe hardened (#057).** The TUI status bar overrides
  `core.fsmonitor`/`core.hooksPath` and sets `GIT_CONFIG_NOSYSTEM` so a malicious
  repo `.git/config` can't exec on open.
- **Secret capability tri-state (#029).** Declaring only `secrets:write` no
  longer confers read of any secret (and vice-versa); an undeclared read/write
  is now denied instead of treated as broad.
- **`/plugin reload` honors tool filters (#028).** Reload routes through the
  full registry composition, so disabled tools stay disabled.
- **Project aliases dropped (#002).** A repo's `.stado/config.toml [aliases]`
  (an exec vector via slash-command expansion) is stripped, like `[hooks]`.

## v0.52.1 — security hardening (batch 3: host-import gates) — 2026-05-22

### Security

- **HTTP upload egress bypass (#014).** `stado_http_upload_create` now enforces
  the same per-host net allow-list as `stado_http_request`.
- **Proxy egress bypass (#022).** `proxy_url`'s host is now checked against the
  manifest allow-list (the proxy is dialed and sees the request + any creds).
- **LSP fs-scope bypass (#012).** `stado_lsp_*` now gate the requested path
  through the plugin's `fs:read` scope, not just workdir containment.

## v0.52.0 — security hardening (review-item decisions) — 2026-05-22

The three Codex findings that needed an operator design decision, now resolved.

### Security

- **`tool:invoke` caller-cap inheritance (#021).** A nested tool invocation now
  runs the callee under the *caller's* capabilities, not the callee's own
  manifest. A plugin holding only `tool:invoke:bash` can no longer gain
  shell/fs/net by invoking the bundled bash tool — no privilege escalation by
  delegating to a more-powerful tool.
- **Session-scoped shell PTYs (#015).** Switching sessions now tears down the
  prior session's live PTYs, so a session can't read/write shells spawned under
  another session.
- **Live-cwd audit fidelity (#030).** TUI live-cwd execution is intended; the
  audit now builds the signed tree from the directory actually written
  (`h.Workdir()`) instead of the unchanged sidecar worktree, so the tree ref
  reflects reality. A failed snapshot degrades gracefully (skips the audit tree,
  never fails the tool).

## v0.51.1 — security hardening (batch 2 of Codex triage) — 2026-05-22

### Security

- **Plugin signing seeds no longer committed.** 11 Ed25519 private signing
  seeds were tracked under `plugins/demos`/`plugins/optional`; untracked and
  `**/*.seed` gitignored. The plugin install copy now excludes `*.seed` and the
  dev-only `.stado/` dir so `plugin use-dev`'s `dev.seed` can't ship into an
  installed package. (Keys remain in history — treat as compromised; rotation /
  deny-listing is tracked separately.)
- **Repo-controlled prompt files no longer follow symlinks.** The security
  harness override (`.stado/harness/security.md`) and persona files
  (`.stado/personas/*.md`) used bare reads; a symlinked override would be
  followed to an arbitrary local file (e.g. `~/.ssh/id_rsa`) and spliced into
  the system prompt. Both now reject symlinks and cap size.

## v0.51.0 — security hardening (batch 1 of Codex triage) — 2026-05-22

First batch of fixes from the Codex security review (`.tmp/security-todo/`).

### Security

- **Critical: rg/ast_grep flag-injection RCE.** `rg.search` appended arbitrary
  ripgrep flags verbatim while holding only `exec:proc:rg`; `--pre` /
  `--search-zip` / `--hostname-bin` make ripgrep execute an attacker-named
  program (`stado_exec` validates only `argv[0]`). Now denylists exec-spawning
  flags. Both `rg` and `ast_grep` also constrain the search/target path to the
  workdir (no absolute / `..`), closing ast_grep's `--update-all` write-escape.
- **Project config `[hooks]` RCE.** A repo-committed `.stado/config.toml` could
  set `[hooks].post_turn = "/bin/sh -c …"` for near-zero-effort RCE on
  `stado run`. The project overlay now strips `[hooks]` (with a notice); legit
  project model/provider/tool overrides still apply.
- **Tool-filter bypass.** `[tools].disabled=["bash"|"webfetch"|…]` (pre-EP-0038
  bare names) was a silent no-op, leaving the wasm replacement live. Legacy
  filter names now translate to their canonical and match.
- **DNS-rebinding / fail-open dial guards.** Plugin TCP/UDP dial + `sendto` and
  the http client validated then re-dialed the hostname (rebind window); sendto
  failed open on lookup error. All now resolve+guard once and dial the validated
  IP (fail closed), and the http guard also blocks `0.0.0.0`/multicast.

## v0.50.2 — docs refresh: personas, threat model, EP-0041 — 2026-05-22

### Docs

- **Personas:** gave the `/persona` slash command its own prominent section and
  corrected the false "session-only" claim — an in-chat switch persists to
  `[defaults].persona` and survives restarts.
- **Threat model** re-walked for the all-wasm architecture: every tool is a
  signed wasm plugin with capability-gated FS/exec/net at the host-import
  boundary (bundled tools default to workdir-scoped caps); containment is
  capability-based not approval-based; host-default exec sandbox under
  mcp-server/daemon.
- **EP-0041** (shell PTY tool naming) added, capturing the v0.49.0
  `read_until`/`screenshot` rename; removed the `docs/superpowers/plans` scratch
  dir and stale `docs/reports/` artifacts.

## v0.50.1 — wider / palette on the landing page — 2026-05-22

### Fixes

- **Inline `/` palette is now wide on the landing page too.** v0.50.0 uncapped
  the palette, but the landing page renders the input through a compact 64-col
  centred card, so the nested palette stayed narrow and truncated descriptions
  there. The landing card now widens while the palette is open and returns to
  its compact size when closed.

## v0.50.0 — wider inline command palette — 2026-05-22

### TUI

- **Inline `/` command palette now uses the full width.** It was capped at 110
  columns (a budget shared with the centred modal), which truncated long command
  descriptions even though the input frame directly below it spans the full
  width. The inline popup is anchored to that frame, so it now matches its width;
  descriptions render untruncated on wide terminals. The `Ctrl+P` modal keeps its
  cap (it's centred and benefits from the whitespace guard).

## v0.49.1 — inline command-palette wrap fix — 2026-05-22

### Fixes

- **Command palette inline (`/`) view no longer wraps rows — the actual fix.**
  v0.49.0 truncated long descriptions but missed the real cause the tester
  reported: `InlineView`/`View` built the box with `.Width(boxW)` assuming that
  was the total outer width. In this lipgloss version `.Width` is content+padding
  and the rounded border adds 2 cols on top, so the box rendered `boxW+2` wide.
  Nested in the input frame (no slack), the overflow made lipgloss word-wrap
  each row, spilling the `/name`+keybinding onto the next line. Now reserves the
  border width (`.Width(boxW-2)`). Also fixes the modal (`Ctrl+P`) on terminals
  ≤66 cols, where the floored modal width overflowed the same way.

## v0.49.0 — shell tool affordance rename + palette long-row fix — 2026-05-22

### Plugins

- **shell tool affordance (breaking): `shell.expect` → `shell.read_until`,
  `shell.snapshot` → `shell.screenshot`.** Renamed for tool-selection
  affordance — agents pick PTY-output tools by name, and `expect` / `snapshot`
  weren't the words they reached for, so they defaulted to `shell.read` and got
  raw ANSI escapes. `shell.read`'s description now cross-references both
  siblings. Behavior and output shapes are unchanged; only the agent-facing tool
  names changed (the `stado_terminal_expect` / `stado_terminal_snapshot` host
  imports keep their names). Old names removed outright — no aliases (pre-1.0).

### Fixes

- **Command palette no longer scrambles long rows.** Non-selected rows didn't
  truncate long descriptions (the selected-row path did), so at narrow terminal
  widths the row overflowed its box and the `/name` + keybinding wrapped onto the
  next line — scattered fragments mixed into the palette. Both row variants now
  truncate consistently.

## v0.48.9 — plugin run → tool run cleanup + dev-loop fixes — 2026-05-21

### Plugins

- **`plugin doctor` now emits correct `stado tool run` advice.** It was
  still printing the removed `stado plugin run` command — along with the
  old `<plugin-id> <tool>` argument shape and the removed
  `--with-tool-host` flag — left over from the `plugin run` → `tool run`
  migration (c2cd90d). The surface-compatibility table, the per-capability
  notes, and the suggested invocation now use `stado tool run <tool>` with
  the correct flags (`--workdir`, `--session`); bundled-tool imports no
  longer claim to need a flag (the tool host is attached on every
  `tool run`).

### Fixes

- **`make test` no longer fails on parent-walk suites.** The Makefile
  defaulted `GOTMPDIR` to `$(CURDIR)/.tmp` — inside the repo. Because
  `go test` roots `t.TempDir()` at `GOTMPDIR`, suites that walk parent
  directories for `.git` / `.env` / `AGENTS.md` / `CLAUDE.md` (dotenv,
  instructions, memory, learning) escaped their temp dir into the repo's
  own files and failed deterministically. CI was unaffected — it leaves
  `GOTMPDIR` at the default `/tmp` (clean ancestors). `GOTMPDIR` now
  defaults to `/var/tmp/stado-gotmp-$(id -u)`: off `/tmp` (per-user quota
  on the dev host), outside the repo tree, and a real path rather than one
  under the Fedora-Atomic `/home → /var/home` symlink (which the fs/grep
  walk guards reject). A guard forces any repo-internal `GOTMPDIR` back
  out.

### Docs

- Completed the `plugin run` → `tool run` migration across user docs:
  14 plugin READMEs and the command / feature / plugin reference docs now
  use `stado tool run <tool>` (tool name only, no plugin-id) with
  `--with-tool-host` removed. Historical records (EP-0028, EP-0038, the
  v0.26.0 release notes) carry supersession notes rather than rewrites.

### Infra

- Cleared the `make lint` (staticcheck) backlog: dropped an unused test
  field (`toolError`) and helper type (`recordingRunner`); replaced a
  nil-literal context with a nil-valued variable in the
  `ProgressFromContext` guard test so the SA1012 check passes under direct
  `staticcheck` (not just golangci-lint's `//nolint`).

## v0.48.8 — Plugin cfg:* capability fixes — 2026-05-21

### Plugins

- **`cfg:state_dir` now resolves on every execution surface.** The
  `stado_cfg_state_dir` host import (EP-0029) only had its value
  populated on the path that runs through `pluginrun.Run` — the CLI,
  installed tools, registry overrides, and bundled plugins. Two
  execution paths bypass that wiring and left the value empty: the
  TUI's operator-driven `/tool` / `/plugin:` invocations and the
  EP-0038b builtin-as-wasm migration tool. A plugin declaring
  `cfg:state_dir` invoked from the TUI read an empty string. Both
  paths now populate it from the active config, so the capability
  behaves identically everywhere a plugin can run. A `writeCfgValue`
  contract test now covers the host import's value-flow (correct
  length, empty → 0, over-buffer / over-ceiling → -1).
- **`fs:read:cfg:state_dir/…` path-templates honor symlinked state
  dirs (EP-0031).** On systems where the state-dir crosses a symlink
  — Fedora Atomic / Silverblue, where `/home → /var/home` — the
  templated capability was silently denied. `cfg.StateDir()` returns
  the `/home/…` (symlink) form from `os.UserHomeDir()`, while fs host
  imports resolve the requested path through `EvalSymlinks` to the
  `/var/home/…` form, so the allow-list prefix compare missed. Literal
  cap paths already aliased both forms at parse time; cfg-templated
  caps resolve at check time and now apply the same symlink alias
  there. This was the exact case the EP exists to solve — verified
  end-to-end on Atomic Fedora, where a plugin reading
  `<state-dir>/plugins` went from denied to listing the install
  directory.

### Infra

- Dependency bumps: `golang.org/x/net` 0.53.0 → 0.54.0 (host and
  plugin trees), `golang.org/x/crypto` 0.50.0 → 0.51.0,
  `github.com/sahilm/fuzzy` 0.1.1 → 0.1.2,
  `github.com/fsnotify/fsnotify`, and the go_modules group; CI
  `sigstore/cosign-installer` 4.1.1 → 4.1.2.
- EP-0027 (repo-root discovery consolidation) marked Implemented;
  docs spring-cleaning. No behaviour change.

## v0.48.7 — Release pipeline fix + lint cleanup — 2026-05-21

### Fixes

- **goreleaser now packages the split LICENSE files.** v0.48.6's
  dual-license commit replaced `LICENSE` with `LICENSE-APACHE` +
  `LICENSE-MIT` but left `.goreleaser.yaml` globbing the old
  single-file name, so the v0.48.6 release run aborted with
  `globbing failed for pattern LICENSE: matching "./LICENSE":
  file does not exist`. Archives and nfpms (.deb/.rpm) contents now
  reference both files; the nfpms `license:` field is the SPDX
  expression `MIT OR Apache-2.0`. v0.48.7 is what v0.48.6 was
  supposed to ship — the dual-license content reaches users this
  tag.
- **Lint clean on `main`.** Seven golangci-lint findings that had
  been red since v0.48.6 — three `errcheck` on `defer c.Close()`
  paths in `cmd/stado/daemon.go` / `tool_run_daemon.go`, two
  `staticcheck` S1016 redundant struct-literals
  (`internal/plugins/runtime/host_ui*.go`), one ST1021 doc-comment
  ordering on `pty.Manager`, one SA4006 dead assignment in
  `internal/tui/ptyblock/model_test.go` — are fixed. Unblocks
  dependabot PRs that inherit the lint job from `main`.

### Infra

- `.learnings/` and `.agent/specs/done/` snapshot directories are
  removed from the repo tree (already-archived design notes were
  carrying weight in git status). No behaviour change.

## v0.48.6 — Dual-license (MIT OR Apache-2.0) — 2026-05-10

### License

- **stado is now dual-licensed under MIT OR Apache-2.0** at the
  recipient's option. Strictly more permissive than the prior
  Apache-2.0-only license — everyone who held a valid Apache-2.0
  license to v0.48.5 still does, and now also has the option of
  taking the work under MIT instead.
- The single `LICENSE` file is replaced by `LICENSE-APACHE` and
  `LICENSE-MIT` at the repo root, following the Rust ecosystem
  convention. The README's License section now points at both
  files and notes the at-your-option choice.
- `## Contribution` section added to the README: contributions are
  dual-licensed under the same terms unless explicitly stated
  otherwise. The standard Apache-2.0 §5 boilerplate.
- No code change.

## v0.48.5 — Auto-compact manifest dedup — 2026-05-10

### Infra

- **One canonical source for the auto-compact manifest.** The Go-coded
  duplicate at `internal/runtime/background_defaults.go::autoCompactManifest()`
  is gone, replaced by `bundled.MustManifest("auto-compact")` reading
  from `internal/plugins/bundled/manifests/auto-compact.json` — copied
  from `plugins/bundled/auto-compact/plugin.manifest.template.json` by
  the build script. New host-side surface
  `bundled.Manifest(name)`/`bundled.MustManifest(name)` parallel to the
  existing `bundled.Wasm`/`bundled.MustWasm`.

  Closes the v0.48.2 carry-over follow-up
  (`.agent/specs/done/kill-autocompact-manifest-duplication.md`),
  Option A interim. Option C (manifest-flag refactor that would kill
  the whole host-side per-plugin policy file) stays deferred — the
  trigger is a second background plugin.

  Pure refactor — no user-visible behaviour change. The
  `BundledBackgroundPlugin.Manifest.Version` field is now `"0.1.0"`
  (from the template) instead of the runtime `internal/version.Version`
  string, but no user-facing display reads that field for auto-compact.
  `stado plugin list` shows `auto-compact v0.0.0-dev` before and
  after, because that view is sourced from `bundled.Info.Version`
  which is unchanged. *(The original cut of this CHANGELOG entry as
  shipped at the v0.48.5 tag claimed the plugin-list display would
  change to `v0.1.0`; that was wrong — corrected on main after
  release.)*

  Build-script change: `plugins/bundled/build.sh` now copies
  background-plugin manifest templates into the embed-friendly
  location after the wasm builds. Today the loop covers only
  `auto-compact`; future background plugins are added to that loop.

## v0.48.4 — Land doc/cross-ref updates omitted from v0.48.3 — 2026-05-10

### Fixes

- **`plugins/README.md` now matches the actual layout.** v0.48.3's
  cleanup commit captured the file moves and the `optional/ls`
  deletion but missed staging the sed-pass updates to non-renamed
  files. As shipped in v0.48.3, `plugins/README.md` still said
  "Two flavors" and pointed at the pre-v0.48.2
  `internal/bundledplugins/` paths; `plugins/optional/README.md`'s
  table still listed the demo rows; the empty-plugin-list TUI
  message in `internal/tui/model_plugins.go:694` still referenced
  `plugins/optional/hello/` which no longer exists.

  This release lands those edits — three lanes documented,
  `internal/plugins/bundled/` paths corrected, optional/ table
  rewritten without demo rows, TUI message retargeted to
  `plugins/demos/hello/`. Cross-refs across `README.md`,
  `SECURITY.md`, `cmd/stado/plugin_init.go`,
  `cmd/stado/plugin_use_dev_test.go`,
  `docs/plugins/host-imports.md`,
  `hack/pty-bridge/TEST-PLAN.md`, plus relative refs inside
  remaining `optional/` plugins, all caught up.

  No code-behaviour change. The miss was purely doc + one TUI
  string; the file-system layout has been correct since v0.48.3.

## v0.48.3 — Demos lane + ls dedup — 2026-05-10

### Infra

- **Three plugin lanes** under `plugins/`: `bundled/` (in-binary,
  unchanged), `optional/` (user-facing installable, unchanged), and
  the new `demos/` for plugin-author showcases and approval-flow test
  fixtures. Eleven plugins moved out of `optional/` into `demos/`:
  `approval-{ast-grep,bash,demo,edit,write}-go`, `choose-demo-go`,
  `expect-demo-go`, `render-demo-go`, `hello`, `hello-go`,
  `state-dir-info`. Each is `// Manual test tool only` or a minimal
  greeter — keeping them in `optional/` was crowding the user-facing
  surface. The new `plugins/demos/README.md` indexes them with what
  each demo validates and notes the retire-when-superseded rule.

- **Drop `plugins/optional/ls/`.** Identical body to
  `plugins/bundled/ls/` (both arrived in `01648d8`); the bundled
  lane supersedes it. The `fs.ls` tool surface is unchanged.

- **Doc and cross-reference cleanup.** `plugins/README.md` now lists
  three lanes; stale `internal/bundledplugins/` paths fixed to
  `internal/plugins/bundled/` (carry-over from the v0.48.2 package
  consolidation that the original commit didn't touch). Cross-refs
  inside stado source (`README.md`, `SECURITY.md`,
  `cmd/stado/plugin_init.go`, `internal/tui/model_plugins.go`,
  `hack/pty-bridge/TEST-PLAN.md`, `docs/plugins/host-imports.md`)
  and inside remaining `optional/` plugin READMEs / build scripts
  retargeted to the new `demos/` paths.

  No behaviour change. The `optional/README.md` table now also
  lists `encode-zig`, `hash-id-rust`, `http-session`, and
  `persistent-shell`, which were already present in the directory
  but missing from the table.

## v0.48.2 — Plugin host-side package consolidation — 2026-05-10

### Infra

- **Plugin packages consolidated under `internal/plugins/`.** The
  three sibling packages `internal/plugins`, `internal/bundledplugins`,
  `internal/userbundled` become an umbrella with origin-flavored
  subpackages: `internal/plugins/{bundled,userbundled,runtime}`.
  Package `bundledplugins` renamed to `bundled` to match its directory.
  Package `userbundled` directory moved; package name unchanged.

  The split is principled, not cosmetic: `plugins/` is the install/trust
  pipeline (manifest, identity, rekor, crl, lockfile), `plugins/bundled/`
  is the embedded asset store and inventory for in-binary wasm,
  `plugins/userbundled/` is operator-supplied wasm appended at bundle
  time, `plugins/runtime/` is the wasm host machinery (origin-agnostic).
  Naming relationship `internal/plugins/runtime/` (wasm-host plumbing)
  vs `internal/runtime/` (agent-loop runtime) documented in the umbrella
  package doc rather than renamed away.

- **Auto-compact host-side policy lifted out of the bundled package.**
  `DefaultBackgroundPlugins`, `LookupBackgroundPlugin`,
  `BundledBackgroundPlugin`, and the auto-compact manifest helpers
  move to `internal/runtime/background_defaults.go`. The bundled
  package's `auto_compact.go` shrinks to its inventory `init()` call
  alone — the bundled package now owns *what wasm ships in the binary*
  and nothing else. The Go-coded `autoCompactManifest()` duplicates
  `plugins/bundled/auto-compact/plugin.manifest.template.json`; the
  duplication is preserved as-is and tracked as a follow-up
  (`.agent/specs/open/kill-autocompact-manifest-duplication.md`).

- **Package boundary docs added.** New `internal/plugins/doc.go` and
  `internal/plugins/bundled/doc.go`; existing package docs on
  `manifest.go`, `userbundled/init.go`, and `runtime/runtime.go`
  extended with explicit boundary statements naming what belongs in
  each package and what doesn't.

  No behaviour change. No public API change (these are `internal/`
  packages — `bundledplugins.X` callers don't exist outside the repo).
  Bundled tool surface, capability vocabulary, host imports, default
  background plugins all identical.

## v0.48.1 — Plugin tree consolidation — 2026-05-10

### Infra

- **All plugin source under `plugins/`.** Bundled plugin sources moved
  from `internal/bundledplugins/modules/<name>/` to
  `plugins/bundled/<name>/`; the build script and the `auto-compact`
  module moved alongside. Optional plugins moved from
  `plugins/examples/` to `plugins/optional/`. The `plugins/default/
  browser/` plugin moved to `plugins/optional/browser/` (the production
  tier-1+tier-2 implementation); the smaller demo formerly at
  `plugins/examples/browser/` moved to `plugins/optional/browser-minimal/`
  to disambiguate. `plugins/default/` is gone.

  The host-side registry code (`embed.go`, `list.go`, `auto_compact.go`)
  stays at `internal/bundledplugins/` because Go's `//go:embed` only
  sees siblings of the importing file — the compiled wasm artefacts
  still land at `internal/bundledplugins/wasm/`. Only sources moved.

  No behaviour change. The bundled tool surface, capability vocabulary,
  and tool registrations are identical. Plugin authors who reference
  `plugins/examples/` paths in scripts or documentation should retarget
  to `plugins/optional/`. Dependabot config updated to match the new
  layout.

  New [`plugins/README.md`](plugins/README.md) documents the
  bundled-vs-optional split and points authors at the right tree for a
  given plugin's lifecycle.

## v0.48.0 — `stado daemon`, host-default sandbox, `shell.expect` + `shell.snapshot` — 2026-05-09

### Security

- **`stado_exec` / `stado_proc_spawn` auto-sandbox under MCP and
  daemon.** Plugins calling these host imports without supplying their
  own `sandbox` field now get the host-default protective policy
  (bwrap / sandbox-exec PID + uid namespace isolation) when invoked
  from `stado mcp-server` or `stado daemon`. The 2026-05-09 review
  flagged that the mcp-server header comment claimed this happened —
  it didn't, because there was no plumbing. The plumbing is now
  there: `tool.SandboxPolicyProvider` interface, `Host.DefaultSandboxPolicy`
  field, `runtime.NewDefaultSandboxPolicy(workdir)` constructor.
  `stado run` / `stado tool run` / TUI deliberately do NOT set a
  default — operator-explicit invocations preserve legacy unsandboxed
  semantics.

  Default policy values: PID + uid namespace isolation, network
  passthrough (`Net="allow"`), filesystem reads of `/bin /sbin /tmp
  /var/tmp /run` plus the runner's automatic `/usr /lib /lib64 /etc
  /proc /dev` mounts, writes to `/tmp /var/tmp` and the workdir's
  CWD. An earlier draft of this commit shipped `&sandboxPolicy{CWD:
  workdir}` and claimed it was permissive; in fact the empty Net
  string fell through the translation switch leaving NetDenyAll
  (full network deny) and `/bin` / `/sbin` weren't bound, so
  literal `/bin/sh` invocations failed at execvp. Both bugs caught
  in the 2026-05-09 second-pass review and fixed here.

  Behaviour change: bash invocations through MCP / daemon previously
  ran with the operator's full UID privileges; they now run in a bwrap
  process namespace by default. Plugin authors who need to bypass
  the host policy (debug / bootstrap scenarios) set
  `"sandbox": {"unsandboxed": true}` in their stado_exec request —
  this is a distinct field from absence/null because JSON unmarshal
  collapses both into `*sandboxPolicy(nil)`, which the resolver
  treats as "use host default."

  Cross-platform: the host-default policy enforcement requires a
  real sandbox runner. On Linux without bwrap or macOS without
  sandbox-exec or any Windows host (which currently has no native
  confinement story), `stado_exec` with a non-nil policy returns an
  explicit error rather than silently passing through unsandboxed.

  Host-as-ceiling (mid-day update): when a host default is set,
  guest plugin policy can only TIGHTEN it — never weaken. The
  resolver intersects the two policies field by field: FSRead /
  FSWrite / Exec / Env keep only entries in BOTH lists; Net "deny"
  wins from either side; CWD is host's (operator-chosen, plugin
  can't redirect). Pre-fix, `Unsandboxed: true` from the wasm
  guest weakened any host default to nil; that hole is now closed
  — Unsandboxed is honored only when there's no host default to
  enforce (stado run / stado tool run / TUI). Plugin authors who
  truly need to bypass operator policy work with the operator to
  remove the host default, not via an in-band claim.

  nil-vs-empty list semantics: an absent guest field (JSON omitted,
  unmarshals to nil slice) means "no opinion" → inherits host's
  list. An explicit `[]` (non-nil empty slice) means "lock down to
  nothing." So an agent can paranoid-restrict by sending
  `"fs_read": []` while still inheriting default network/CWD
  policy.

### CLI

- **`stado daemon` — long-running peer for stateful tool calls.** New
  subcommand family (`stado daemon start|stop|status`) hosting a
  Unix-domain-socket JSON-RPC service that holds the state stateful
  tools (live PTYs from `shell.spawn`, future browser cookie jars and
  LSP connections) need to keep across `stado tool run` invocations.
  Before this commit, `stado tool run shell.spawn` returned an `id` no
  follow-up `shell.read` call could find — every invocation got a fresh
  empty `pty.Manager`. With the daemon, the spawn-then-read pattern
  agents rely on works.

  Socket: `$XDG_RUNTIME_DIR/stado/daemon.sock` on Linux (override via
  `$STADO_DAEMON_SOCKET`); mode 0700, owner-only; SO_PEERCRED uid check
  on Linux as defence-in-depth.

  Auto-spawn: PTY-bound shell tools auto-start the daemon (forking
  `stado daemon start --quiet` and waiting up to 2 s for the socket).
  Operators who prefer to manage the daemon manually set
  `STADO_DAEMON=manual`; `STADO_DAEMON=off` disables daemon dispatch
  entirely (PTY tools then refuse with the classic single-shot
  advisory).

  Project isolation: each `(git_root_or_cwd, STADO_SESSION_ID)` pair
  gets its own `pty.Manager`, so a session created from one repository
  can't be enumerated or attached from another. Cross-project kill is
  refused. Sessions persist for the daemon's lifetime; daemon restart
  loses them (children of the daemon process; document-only — no
  re-parenting magic).

  Idle timeout: 30 min default with zero live sessions and no
  in-flight calls; configurable via `--idle-timeout`. Set to 0 to
  disable. Stale-socket cleanup runs at start: a daemon that died
  ungracefully and left a socket file behind gets cleaned up; a live
  daemon makes the new attempt fail fast with `socket in use`.

  Authorization posture: auto-approve, matching `stado mcp-server` —
  the calling client is the authorisation boundary. Operators who need
  human-in-the-loop approval prompts use the TUI (`stado`) or
  `stado run` without `--tools`; the daemon doesn't yet bounce
  approval requests back to the calling client (planned for a future
  release).

### Plugins

- **`shell.expect` — read-until-pattern PTY primitive.** A new bundled
  tool that reads from an existing `shell.spawn` session until one of
  a configured set of patterns matches, the timeout elapses, or the
  process exits. Replaces the model loop of `shell.read with timeout
  → substring-check → loop` (typically 3–7 turns per prompt-wait)
  with a single tool call. Args: `id`, `patterns` (1..16 strings),
  `regex?` (default false; when true, patterns are RE2),
  `timeout_ms?` (default 30000; 0 = check buffer only). Response is
  one of three discriminated shapes — match (`{matched:true,
  pattern_index, before, match}`), timeout (`{matched:false,
  timeout:true, before}`), or eof (`{matched:false, eof:true,
  before, exit_code}`). `before` and `match` are base64 because PTY
  output routinely includes non-UTF8 sequences. After-match bytes
  are pushed back into the session's ring so subsequent
  `shell.read` / `shell.expect` see them. Across patterns, the
  earliest byte position wins; ties go to the lower index — useful
  for "either a prompt or an error" branching without two separate
  calls. New host import `stado_terminal_expect` gated behind the
  existing `terminal:open` capability; plugins that already declare
  it gain access without manifest changes. For full-screen TUIs use
  `shell.snapshot` instead — `shell.expect` operates on the raw byte
  stream where ANSI escapes interleave with content. Demo plugin
  ships at `plugins/examples/expect-demo-go/`.

- **`shell.snapshot` — rendered terminal screen capture.** A new
  bundled tool feeds every byte read from a PTY session through a
  `vt10x` emulator alongside the existing ring buffer, then exposes
  the rendered grid as plain text plus optional self-contained SVG.
  Lets agents observe full-screen TUIs (vim, htop, gdb-tui, less,
  midnight commander) where `shell.read` returns ANSI escape stew
  that's hard to interpret. Read-only — no attach required, safe to
  call concurrently with `shell.read`. Args: `id`, `with_svg?`
  (default false; SVG is ~30–60 KB for 120×32). Returns
  `{text, cols, rows, cursor:{x,y,visible}, title, svg?}`. Two new
  host imports — `stado_pty_snapshot` and the `stado_terminal_snapshot`
  alias — gated behind the existing `terminal:open` capability;
  plugins that already declare it gain access without manifest
  changes. Adds `github.com/hinshun/vt10x` as a dependency (MIT,
  ~1.5k LoC pure-Go VT100 emulator).

- **F10 ACP wire format extension.** `session/update kind=choice`
  notifications now carry the per-option `prefix` and `input`
  metadata; `session/choice_response` accepts an `inputValue`
  field. The server validates `inputValue` against the chosen
  option's validator before resolving — on failure returns an RPC
  error and keeps the request open so the client can correct the
  input and resend with the same `requestId`. The previous
  rejection of input-bearing options on the ACP bridge is gone;
  ACP clients that don't yet implement input rendering ignore the
  metadata and resolve with an empty `inputValue` (graceful
  degradation). `stado acp --help` updated to enumerate the new
  fields. Multi-select with input fields stays unsupported.

### Infra (2026-Q2 refactor program)

Code-quality refactor program (2026-Q2). Behaviour-preserving
across the program except where explicitly noted; the public CLI
+ ABI surface is unchanged. Plan in
`docs/superpowers/plans/2026-05-07-refactor-program.md`.

### Infra

- **`internal/workdirpath` API simplification (A2).** The 23
  exported legacy fns (`Resolve`, `OpenRootNoSymlink`,
  `MkdirAllUnderUserConfig`, the `*Root*` family, etc.) are gone
  from the public surface. Four narrow resolver types
  (`Resolver`, `UserConfigResolver`, `StrictResolver`,
  `RootResolver`) own the API by trust model. Callers across
  17 packages migrated. Internal impls preserved as private
  package-helpers — same security-critical no-symlink walks, just
  visibility-narrowed. Phase 2.1 / commits through `492e0de`.
- **TUI `Update` dispatcher split (A3).** `internal/tui/model_update.go`
  was a single 1544-line `Update()` with a giant type-switch.
  Now ~100 LoC dispatcher routing to per-family handlers in 5
  sibling files: `handler_lifecycle.go` (window/title/log/loop/
  monitor/recovery), `handler_stream.go` (provider streaming),
  `handler_tools.go` (tool/plugin events), `handler_picker_response.go`
  (picker-active KeyMsg dispatch), `handler_input.go`
  (KeyMsg + MouseMsg). Phase 2.3 / commit `6f2208f`.
- **TUI `model_render.go` shrink (A1).** From 1937 to 302 LoC.
  Eight per-concern in-package extractions: `sidebar.go`,
  `landing.go`, `quit_confirm.go`, `input_box.go`, `approval.go`,
  `choice.go`, `status_bar.go`, `blocks_render.go`. The plan's
  unified `Overlay`/`Picker` interface goal was deliberately
  scoped out (decision D14): the 5 "overlays" turned out to have
  3 different composition models in `View()` and a single
  interface couldn't honestly cover them. Phase 2.2 / commits
  through `20fc54f`.
- **`internal/config/config.go` split (B1).** From 986 to 735 LoC.
  System-prompt-template lifecycle moved to
  `system_prompt_template.go`; per-config-dir / per-state-dir
  path resolution moved to `paths.go`. Phase 3.1 / commit
  `8ea8ab1`.
- **Bundled tool schema builder (B2).** New
  `internal/runtime/schema` package with composable helpers
  (`Object`, `Props`, `String`, `Integer`, `Array`,
  `StringEnum`, `Empty`). All 34 bundled wasm tools' input
  schemas migrated from inline `map[string]any` literals
  (143 → 1 deliberate any-shape literal). Phase 3.2 /
  commits `bfcf586` + `1c28fa4`.
- **Phase 1 plugin-host bridge contract tests** (already shipped
  in earlier work; this branch carries forward 47 + 13 + 17
  contract tests as the regression baseline for the program).

### Fixes

- **Bazzite / Atomic-Fedora `RemoveAll` gap.** EP-0028 added
  `*UnderUserConfig` resolution for read/open/mkdir but missed
  RemoveAll. Five caller sites
  (`cmd/stado/{session,agents,plugin_gc,plugin_install}.go`,
  `internal/tui/model_sessions.go`) were broken on hosts where
  `/home → /var/home` is a system symlink — `stado session
  delete` and friends rejected at the `/home` component.
  `UserConfigResolver.RemoveAll` closes the gap. Behavior change
  carve-out (D13). Commit `b1b0b23`.
- **`tools__list_categories` and `tools__in_category` always
  returned empty.** A `toolCategoried` interface in
  `internal/runtime/meta_tools.go` had zero implementations;
  every type-assertion fell through silently. Rewired to use
  `LookupToolMetadata(name).Categories` per EP-0037 §C — the
  two meta tools now actually surface the bundled tool taxonomy.
  `tools__list` / `tools__describe` outputs now include a
  `categories` field for tools with metadata. Commit `e1fc00f`.

### Internal

- Code paths that pass the workdirpath migration are quietly
  cleaner: per-package callers now hold a `*Resolver` /
  `*UserConfigResolver` instead of repeating workdir / anchor
  args in every call. No semantic change.

### Not landing in this release

- **B3 (bridge lifecycle interface).** The plan called for a
  shared `Bridge` interface (Init/Dispose/Name) on
  `Session/Memory/Approval/Choice/Fleet`. Audit found zero
  bridges with Close/Dispose work, no shared setup loop to
  factor, no log line that would benefit from `Name()`. Skipped
  — adding the interface as no-ops would be ceremony without
  consolidation. Notes in journal; revisit if a real lifecycle
  caller emerges.

## v0.47.0 — TUI slash ergonomics, `stado_ui_print`, `stado_ui_choose` input fields

Headline shipping new operator and plugin surfaces:

- **TUI slash ergonomics.** `/tool <name> [args]` (and `/t`) covers
  bundled and installed tools in one path; `/alias create | list |
  rm` lets operators define their own slash shortcuts with `{N}`
  positional substitution. The long `/plugin:<name> <tool>` shape
  is no longer the only manual-dispatch route, and bundled tools
  finally have a TUI invocation path.
- **`stado_ui_print`** — new fire-and-forget plain-text emit for
  plugins, gated by a `ui:print` capability (F9a; TUI slice).
- **`stado_ui_choose` input fields** — each option may carry a
  `prefix` and an optional editable `input` field with a
  validator (length / regex / int / path / multiline). Bare-input
  shortcut renders single-option-with-input as a plain prompt.
  TUI ships end-to-end; non-TUI bridges reject input-bearing
  options pending follow-on (F10).
- **Fixes:** `/plugin:<name>` resolves to the active installed
  version (no `-<ver>` required); `stado tool list --json` emits a
  single valid JSON document instead of NDJSON; `stado tool run`
  refuses PTY-bound shell tools with an actionable advisory;
  `stado mcp-server --help` documents newline-delimited JSON-RPC
  framing and prints a startup advisory when stdin is a TTY.

### Plugins

- **`stado_ui_print` — fire-and-forget plain-text emit (F9a, TUI
  slice).** Plugins emit text into the operator's view without
  needing a structured payload. Wire shape:
  `{text, severity?, eol?, stream_id?}` with severity in
  `{"", "info", "warn", "error"}`. Text capped at 8 KiB per
  call; larger payloads belong in `stado_ui_render` (F9b). Gated
  by a new `ui:print` manifest capability.

  TUI surface appends a system-style block per call with severity
  prefixes (`[warn]` / `[error]`) so callouts stand out from
  default info emits. Non-TUI bridges (ACP / MCP / headless) drop
  on the floor for now — F9b lands proper non-TUI rendering and
  the ACP `kind=text` payload extension.

  `stream_id` is preserved on the wire but the renderer does not
  yet coalesce successive same-id emits — that lands with F9b's
  continuation rendering. Spec:
  `.agent/specs/done/f9a-ui-print.md`.

- **`stado_ui_choose` per-option input fields (F10).** Each option
  may now declare a `prefix` decoration and an `input` field
  carrying a `default` value plus an optional `validator`
  (`length` / `regex` / `int` / `path` / `multiline`). The
  response gains `input_value` for the chosen option's typed text;
  pre-F10 callers (options without `prefix` / `input`) decode and
  resolve identically to before. Bare-input shortcut: a single
  option with `input` and no `label` renders as a plain TUI input
  prompt instead of a one-row chooser.

  TUI handles editing inline — printable runes and Backspace edit
  the focused row's buffer; Enter validates against the option's
  validator (re-prompts inline on failure) and commits with the
  typed value. Multi-select with input fields is rejected at the
  bridge with a structured error. Validators run host-side so
  invalid input never reaches the plugin.

  ACP / MCP / headless surfaces reject options carrying `input`
  with `"channel does not yet support per-option input fields"` —
  plugins targeting both TUI and ACP detect the error and fall
  back to plain choice on the non-TUI path. Wiring the new fields
  into the ACP `kind=choice` payload is a follow-on slice.
  Spec: `.agent/specs/done/f10-ui-choice-input.md`.

### TUI

- **`/tool <name> [json-args]` (and `/t` alias).** Single command for
  manual tool dispatch — bundled tools (`fs.read`, `agent.spawn`,
  `http_request`) and installed plugin tools alike. Mirrors
  `stado tool run` from the CLI so muscle memory carries across
  surfaces. The existing management verbs
  (`/tool ls / info / enable / disable / autoload / unautoload /
  reload`) flow through the same command.
  PTY-bound shell tools refused with the same advisory `stado tool run`
  uses (B5 reasoning).
- **`/alias create | list | rm` — operator-defined slash shortcuts.**
  Aliases live globally in `~/.config/stado/config.toml` under
  `[aliases]`. Names are written without the leading `/`; expansion
  must start with `/`. Positional args use `{1}`, `{2}`, … in the
  expansion and are substituted from the call site.
  Example: `/alias create read /tool fs.read {"path":"{1}"}` lets
  you type `/read foo.txt` instead.
  Names that shadow a built-in slash command (e.g. `/help`,
  `/tool`, `/plugin`) are rejected at create time. Hand-edited
  config.toml entries that would shadow are defensively skipped at
  resolution time. Calling an alias without enough positional args
  surfaces a precise `{N}` error rather than a confused downstream
  failure.

### CLI

- `stado mcp-server --help` now leads with a "WIRE FORMAT" section
  explicitly stating that MCP v1 stdio uses newline-delimited
  JSON-RPC, NOT LSP-style Content-Length framing. Adds a
  copy-pasteable smoke test (`echo '{"jsonrpc":"2.0",…}' |
  stado mcp-server | head -1`). When stdin is a TTY at startup
  (operator typed the command directly with no client
  connecting), the server now prints a stderr advisory pointing
  at the smoke test and Ctrl+D — pre-fix this looked like a
  hang. (B6)
- `stado tool run` now refuses PTY-bound shell tools (`shell.spawn`
  / `list` / `attach` / `read` / `write` / `detach` / `signal` /
  `resize` / `destroy`) with an actionable advisory pointing at the
  TUI, MCP server, and agent loop. The CLI is single-shot — its
  Runtime (and the Runtime's `pty.Manager`) lives only for the
  duration of one invocation, so a `shell.spawn` could never be
  observed by a `shell.list` from a separate `tool run`. Pre-fix
  behaviour was a confusing silent empty list. One-shot
  `shell.exec` / `shell.bash` / `shell.sh` / `shell.zsh` continue
  to work — they don't bind a PTY. The `--session` flag's help
  now also clarifies that it carries session-aware capabilities
  (audit log, memory, fork) but does NOT persist PTYs. (B5)
- `stado tool list --json` now emits a single valid JSON document
  (envelope: `{schema_version, count, tools[]}`) instead of NDJSON.
  Pre-v0.46.2 behaviour broke `python3 -m json.tool`, `jq .`, and any
  strict-JSON parser; operators relying on the streaming shape can
  recover it with `stado tool list --json | jq -c '.tools[]'`. The
  envelope's stability is now part of the project-wide commitments
  above. (B4)
- `/plugin:<name>` (bare name, no `-<version>` suffix) in the TUI
  resolves to the active installed version, so
  `/plugin:demo greet` runs the active demo's `greet` tool. The
  literal `<name>-<version>` form continues to pin to a specific
  installed version.

### TUI

- `/plugin` (bare) listing no longer clips after the first plugin when
  any tool description is long. Two fixes:
  1. The system-block renderer was capping body length at `width*6`
     (~480 chars on a normal terminal), which was hiding subsequent
     plugins entirely. The cap is removed — `lipgloss.Width(width)`
     still wraps each visual line correctly, but no longer truncates
     the total body.
  2. Each tool's description is now summarised inline (whitespace
     collapsed, capped at 120 runes with a `…` suffix when longer)
     so a verbose `Description` field can't hard-wrap to column 0
     and dismantle the indented `  /plugin:NAME` →
     `    · TOOL — DESC` hierarchy. Operators get the full text via
     `/plugin <name>` (per-plugin view, unchanged).

## v0.46.1 — Dependency bumps + CI maintenance

Patch release: dependabot backlog cleared, no behaviour changes for
operators or plugins.

### CI

- `golangci/golangci-lint-action` **v8 → v9**. Required maintenance
  upgrade — GitHub deprecates node20 on runners, v9 ships the node24
  runtime. Pinned `version: v2.11.4` and `args: --timeout=5m`
  unchanged.
- `actions/checkout` **v5 → v6**.

### Dependencies (main module)

- `github.com/anthropics/anthropic-sdk-go` **v1.37.0 → v1.40.0**.
  Additive across the three minor bumps: `Type()` on errors, Memory
  beta API, structured-outputs auto-parse, Workload Identity
  Federation, OIDC profiles, header-via-env support. Only
  behaviour change worth flagging: v1.39 added a 10-minute default
  HTTP client timeout — benign, turn-level cancellation still
  drives cleanly.
- `github.com/mark3labs/mcp-go` **v0.48.0 → v0.52.0**. Pre-1.0
  additive feature releases — `WithInteger` schema option,
  `ListPrompts` / `ListResources`, opt-in input + output schema
  validation, `CommandTransport` for subprocess MCP servers, OAuth
  Protected Resource Metadata (RFC 9728), Go 1.23 iterator methods,
  opt-in CORS, `LoggingTransport`, `SchemaCache`. None of the
  additions is on by default.
- `google.golang.org/api` **v0.189.0 → v0.278.0**. 89-version
  pre-1.0 jump but our surface only imports
  `google.golang.org/api/option` for the Gemini provider client
  construction; the `option` package is rock-stable. Tests pass
  under `-race` for `internal/providers/google`.
- `github.com/mattn/go-isatty` **v0.0.20 → v0.0.22**.
- `github.com/charmbracelet/x/ansi` **v0.11.6 → v0.11.7**.

### Dependencies (plugin examples)

- `plugins/default/browser` and `plugins/examples/browser`:
  `github.com/PuerkitoBio/goquery` **v1.10.0 → v1.12.0**.
- `plugins/examples/browser`: `golang.org/x/net` **v0.43.0 → v0.53.0**.

### Fixes

- **Lint backlog cleared** across nine sites that had been failing
  CI sporadically: deferred-Close errcheck wrapping, an ineffectual
  reassignment in `agentloop`'s inbox-flush branch, several
  staticcheck nits, and one unused helper. No behaviour changes.
- **Race-detector compatibility** for the ACP test suite: the
  `writerSync` helper exposes locked `String()` / `Bytes()`
  accessors so tests reading the captured-buffer don't race the
  bridge goroutine's `Write`. `TestSessionCancelCancelsSpawnedSubagent`
  in headless skips under `-race` because of a known concurrent-write
  race in go-git's `dotgit.cleanObjectList` upstream — a sidecar-wide
  write mutex is the proper fix and is tracked separately. The
  test's wall-clock deadline also bumps from 2s to 10s so slow CI
  runners stop flaking on the timeout path.

## v0.46.0 — Step 7 (fs.* wasm) + ACP round 2 (resume, persona, tool_summary)

The fs family closes out the EP-no-internal-tools migration's most
visible step (Step 7), and the ACP surface fills out: session resume,
persona pinning, end-of-turn tool_summary for tool-only turns, the
approval bridge symmetric to the choice bridge, and a documentation
sweep that brings `--help` to parity with the protocol comment.
Several smaller paper-cuts get fixed alongside (`stats --json` schema
versioning, sessionstats ASCII fallback, demos relocated out of the
bundled tool surface).

### Plugins

- **Demos moved to `plugins/examples/`**. The `approval_demo` static
  bundled tool is removed from the stado binary; its source lives at
  [`plugins/examples/approval-demo-go/`](plugins/examples/approval-demo-go)
  with the standard build / sign / install layout. A new sibling
  [`plugins/examples/choose-demo-go/`](plugins/examples/choose-demo-go)
  exercises the `ui:choice` cap + `stado_ui_choose` primitive end-to-end
  (the missing Done-def item from the ui_choose spec). Reasoning: stado
  shouldn't bundle test/demo tools — the model sees the registry and
  shouldn't be tempted to call them. Operators who want to manually
  exercise the approval / choice drawers `stado plugin install` the
  example. Test that drove `bundledplugins.MustWasm("approval_demo")`
  now compiles the example at test time (skips when no `go` toolchain
  is on PATH or the example dir is trimmed from a downstream tree).

### Architecture

- **EP-no-internal-tools Step 7** (final family). The fs.read/write/edit/
  glob/grep + readctx.read tools migrate to the wasm `fs.wasm` bundled
  plugin, dispatching through `stado_fs_*` primitives end-to-end. The
  seven previously-separate wasm modules (read/write/edit/glob/grep/
  read_with_context/readctx-ng) collapse into one. Wire form: `fs__read`,
  `fs__write`, `fs__edit`, `fs__glob`, `fs__grep`, `readctx__read`.
- New host primitive **`stado_fs_last_error`** lets wasm plugins retrieve
  the host's structured error message (scope-guard violation, capability
  deny, IO failure) after a `stado_fs_*` primitive returns -1. Used by
  the wasm fs plugin to surface the specific cause to the model
  ("outside write_scope" vs generic "write failed").

### ACP

- **Configurable `MaxTurns`** (B1/F1 from integration testing).
  Resolution order: `session/new`'s optional `{"maxTurns": N}` param
  (per-session pin from the ACP client) → `stado acp --max-turns N`
  / `--no-turn-limit` (operator CLI flag, mirrors `stado run`) →
  `[acp] max_turns = N` in `config.toml` (operator default) →
  built-in fallback (50 with `--tools`, 1 without).
  `--no-turn-limit` sets an effectively unlimited cap for
  engagement workflows that need many turns.
- **Eager plugin ABI verify** (B2/F2/D1). When `stado acp --tools`
  receives `session/new`, every active installed plugin's wasm is
  compiled (without instantiation) and checked against TWO ABI
  surfaces:
  1. **wasm-side exports** — `stado_alloc`, `stado_free`, and one
     `stado_tool_<name>` per ToolDef in the manifest.
  2. **host-side imports** — every function the plugin imports from
     the `stado` namespace must still be provided by the runtime.
     Catches plugins built against an older stado that reference host
     primitives deleted in this release (D1: v0.44.x plugins
     importing `stado_fs_tool_read` after Step 7 removed it).
  Mismatches surface as a single RPC error listing rebuild-required
  plugins (with the specific missing symbols), instead of the agent
  retrying broken tool calls through the entire turn budget. The
  message distinguishes "missing exports" from "imports removed in
  this stado version" so the operator knows whether to update the
  plugin manifest or just rebuild against the new runtime.
- **Documentation** for `session/update` event kinds, `.env`
  auto-load behaviour, and the new `MaxTurns` knobs added to
  `stado acp --help` and the server protocol comment. `--help`
  now enumerates all five kinds (`text`, `tool_call`, `subagent`,
  `choice`, `approval`) with their wire fields and required
  client-side response RPC.
- **Session resume**. `stado acp --resume <id-or-label>` (operator
  default) and `session/new {"resumeSession": "<UUID>"}` (per-call
  pin) attach to an existing git-native session: the prior
  conversation history is loaded via `runtime.LoadConversation`, the
  worktree becomes the session workdir, and the wire `sessionId`
  matches the git session UUID so it round-trips across processes.
  CLI accepts the full lookup vocabulary (UUID, prefix ≥8, or
  description substring) and resolves before the JSON-RPC loop
  starts; the wire `resumeSession` requires a canonical UUID so a
  malformed id surfaces a precise error instead of a "no worktree"
  red herring. Resuming an already-active session in the same server
  process is rejected with `CodeInvalidParams` to keep the message
  thread coherent. Removes the multi-dispatch context loss where
  long-running engagements re-discover the same workspace state at
  the start of every new ACP session.
- **`kind=tool_summary` end-of-turn notification** for tool-only
  turns (closes tester finding F7). When a `session/prompt` produces
  ≥1 tool call but zero text deltas, stado emits a `session/update`
  notification with `kind=tool_summary` carrying `toolCount` (int),
  `lastTool` (string), and `lastError` (bool) before the prompt
  response is sent. Lets ACP clients construct a minimal reply when
  `text` is empty without parsing the per-call stream themselves.
  Fires at most once per `session/prompt`; never fires when text was
  emitted; fires on AgentLoop errors too (a turn that hits MaxTurns
  after a tool call still gives the client the partial summary).
  `--help` now spells out the rule alongside the existing
  empty-`text` documentation.
- **Persona on headless + ACP envelopes** (closes the personas-design
  Done-def gap). `stado acp --persona <name>` and
  `stado headless --persona <name>` set an operator default that
  applies to every new session unless overridden per-call.
  `session/new {"persona": "<name>"}` (ACP) and
  `session.new {"persona": "<name>"}` (headless) carry the override.
  Resolution mirrors `stado run`: project (`{cwd}/.stado/personas/`)
  → user (`<config-dir>/personas/`) → bundled. CLI flags fail at
  startup on a bad name; wire-form bad names fail `session/new` with
  `CodeInvalidParams` before the session is registered. The resolved
  persona threads into `AgentLoopOptions.Persona` for the loop's
  system-prompt assembly, identical behaviour to `stado run --persona`.

### CLI

- `stado stats --json` output now carries `"schema_version": 1`. The
  schema is stable within a major version; renames/removals bump it
  and are documented in this changelog with a migration note. Pure
  additions (new keys) do not bump.

### TUI

- Landing screen now lists the autoloaded plugin set under the input
  box (`12 plugins  agent · fs · rg · shell · …`). Operators can see
  what surface the next prompt can reach without running
  `stado tool list` first. Pulled from the live registry, so installed
  plugins show up alongside bundled ones; the `stado-builtin-tool-`
  manifest prefix is stripped for the display label.
- Quit-confirm modal (Ctrl-D) polished. Title is action-oriented
  ("Quit stado?"); keys render as bordered keycap chips with quit /
  cancel labels; an in-flight subline surfaces ("an in-flight
  shell.bash call will be cancelled" / "the current response will
  stop streaming") so the user knows what they'd lose by quitting
  now. The modal now overlays on top of the chat (rows above and
  below remain visible) instead of replacing the whole frame with an
  empty canvas. New helper at internal/tui/overlays/CenterOver
  composites popups over a base render — reused by the
  approval-drawer polish.
- New host primitive **`stado_ui_choose`** (Q3). Wasm plugins with
  the new `ui:choice` capability can prompt the operator for a
  single or multi-choice answer. Wire format: JSON request
  `{prompt, options:[{id,label}], multi, default}`, JSON response
  `{selected:[...], cancelled:bool}`; errors use the existing
  negative-length tool-side wire. The TUI drawer renders below the
  input with ↑/↓ navigate, Space toggle in multi mode, Enter confirm,
  Esc cancel; long option lists scroll inside the drawer (max 8
  visible at once with above/below indicators). Single-flight: a
  second concurrent request is rejected with `cancelled=true` so
  plugins see a clean signal. Headless / MCP server return a
  structured `"interactive UI unavailable"` error so the plugin
  can decide how to fall back — no silent default-pick.
- **ACP integration** for `stado_ui_choose` (Phase B). When a plugin
  calls the primitive during an ACP session, the server emits a
  `session/update` notification with `kind=choice` carrying
  `requestId`, `prompt`, `options[]`, `multi`, `default[]`, and
  blocks on a paired `session/choice_response` RPC from the client
  with `{sessionId, requestId, selected[], cancelled}`. Session
  cancel and connection drops resolve every pending request with
  `cancelled=true` so plugin calls don't deadlock.
- **At-quit session summary** (Q1). When the TUI exits cleanly, a
  per-session summary lands in the terminal scrollback covering
  uptime, total tool calls, tokens in/out, total cost, and per-model
  + per-tool breakdowns. Source is the same git-native trace ref
  `stado stats` reads, so the totals are authoritative across crashes
  and resumes — no live tally drift. Empty sessions print a single
  "no tool calls this session" line.
  Implementation: new `internal/runtime/sessionstats` package walks
  one session's trace ref and renders a focused summary, distinct
  from the cross-session aggregator that backs `stado stats`.
  Box-drawing chars (`──`) and the ellipsis (`…`) fall back to ASCII
  (`--` / `...`) on non-UTF-8 terminals — the package inspects
  `LC_ALL` → `LC_CTYPE` → `LANG` for a UTF-8 hint and degrades
  gracefully so the summary stays readable in CI logs, minimal SSH
  sessions, and `LANG=C` shells instead of degrading to `?` mojibake.
- Approval drawer (plugin-requested human approval) polished.
  Title gets a ⚠ icon prefix; body renders in a faint code-block
  frame when it's command-shaped (multi-line, contains `$ ` or
  backticks) so long shell commands stay scannable; buttons gain
  keycap chips ("Y / Allow", "N / Deny") with the active button
  using both tone-coloured border AND fill so selection contrast
  survives low-contrast themes. Hint line distinguishes the two
  modes: defocused ("Y allow · N deny · ↑ focus drawer"),
  focused ("← / → switch · Enter confirm · ↓ return to input").
- **ACP integration** for `stado_ui_approve`. Symmetric with the
  Q3 Phase B `stado_ui_choose` ACP wiring: when a plugin calls the
  primitive during an ACP session, the server emits `session/update`
  with `kind=approval` carrying `requestId`, `title`, and `body`,
  then blocks on a paired `session/approval_response` RPC from the
  client with `{sessionId, requestId, allow:bool, cancelled:bool}`.
  `cancelled=true` collapses to `allow=false` at the wasm boundary
  (`stado_ui_approve` returns 0/deny), preserving the operator-
  dismissed signal in the wire format for clients that want to
  surface it. Session cancel and connection drops resolve every
  pending approval with `cancelled=true` so plugin calls don't
  deadlock. Closes the gap where ACP plugins calling
  `stado_ui_approve` previously got `-1 unavailable` because the
  ACP host didn't implement `ApprovalBridge`.

### Fixes

- **CI lint backlog cleared.** `golangci-lint` had been failing
  on a sticky set of small violations across nine sites
  (deferred-Close errcheck, ineffectual assignment in agentloop's
  inbox-flush branch, several staticcheck nits, one unused helper).
  Cleaned out so CI's lint job goes green; no behaviour changes.

### Removed surfaces

- Host imports: `stado_fs_tool_read`, `stado_fs_tool_write`,
  `stado_fs_tool_edit`, `stado_fs_tool_glob`, `stado_fs_tool_grep`,
  `stado_fs_tool_read_context`. Step 7 replaces the
  fs/readctx native-tool delegates with true wasm primitives —
  the `stado_fs_*` family — plus a wasm-side `fs.wasm` plugin that
  rebuilds glob/grep/edit/read_with_context on top of them.
- Native packages: `internal/tools/{fs,readctx}` — the fs/readctx
  native source is deleted; the model surface reaches `fs__*` and
  `readctx__*` only through `internal/bundledplugins/modules/fs`.

### Plugin ABI migration note (for plugin authors)

The wasm calling contract (`stado_alloc` / `stado_free` /
`stado_tool_<name>` exports, return-len-or-negative-error wire
format) is unchanged. New primitives
(`stado_fs_last_error`, `sandbox` arg on `stado_exec` / `stado_proc_spawn`,
`stado_fs_readdir`, `stado_fs_stat`) are additive — old plugins that
don't declare the new caps don't see them.

**Substitution table.** If your plugin imports a host function that no
longer exists, swap as below:

| Removed import (release) | Replacement | Notes |
|---|---|---|
| `stado_fs_tool_read` (v0.46.0) | `stado_fs_read` / `stado_fs_read_partial` | The `_partial` variant takes `(offset, length)` for paged reads; declare `fs:read:<path>` cap. |
| `stado_fs_tool_write` (v0.46.0) | `stado_fs_write` | Declare `fs:write:<path>` cap; scope guard is enforced host-side. |
| `stado_fs_tool_edit` (v0.46.0) | `stado_fs_read` + `stado_fs_write` | Read, mutate in-wasm, write back. Reference impl: `internal/bundledplugins/modules/fs/main.go`. |
| `stado_fs_tool_glob` (v0.46.0) | `stado_fs_readdir` + `filepath.Match` | Walk dirs in-wasm; reference impl: `fs.wasm`'s `glob` entry. Or invoke the bundled `fs__glob` tool via `stado_tool_invoke`. |
| `stado_fs_tool_grep` (v0.46.0) | `stado_fs_readdir` + `stado_fs_read` + regex | Walk + read + match in-wasm. Or invoke the bundled `fs__grep` tool via `stado_tool_invoke`. |
| `stado_fs_tool_read_context` (v0.46.0) | `stado_fs_read` + line-window formatting | Format your own ±N-line context window around the hit. Or invoke the bundled `readctx__read` tool via `stado_tool_invoke`. |
| `stado_http_get` (v0.45.0) | `stado_http_request` | The new primitive is method-agnostic; pass `"GET"` to keep behaviour. RFC1918 / loopback / link-local refusals carry over. |
| `stado_exec_bash` (v0.45.0) | `stado_exec` (+ optional `sandbox` arg) | Drops the hardcoded bwrap policy; pass `sandbox: {...}` per call if you want the old policy back. Cap parse: `exec:bash` / `exec:shallow_bash` → `exec:proc:bash`. |
| `stado_search_ripgrep` / `stado_search_ast_grep` (v0.45.0) | `stado_exec` with `rg` / `ast-grep`, or `stado_tool_invoke("rg__search", …)` | The bundled `rg.wasm` / `astgrep.wasm` plugins are reusable from another plugin via `stado_tool_invoke`. Cap parse: `exec:search` / `exec:ast_grep` → `exec:proc:rg` / `exec:proc:ast-grep`. |

For plugin authors rebuilding against v0.46.x:

1. Re-run your plugin build script — wasm bytes change with toolchain
   updates and the manifest's `wasm_sha256` must match.
2. Re-sign the manifest (`stado plugin sign <dir>`).
3. Re-install via `stado plugin install <path>`.

Stale-ABI plugins now fail fast with a single `session/new` error in
ACP mode (see the ACP section above) and surface a clear `plugin ABI
incomplete` message in CLI / agent-loop dispatch — no more silent
retries. The fail-fast error names the specific missing imports so you
can map them to this table.

## v0.45.0 — No internal tools (Steps 0–5)

First half of the EP-no-internal-tools migration. Spec at
`docs/superpowers/specs/2026-05-06-no-internal-tools-design.md`.

The model-facing tool surface is moving plugin-shaped end-to-end. This
release ships the architectural unblock + four plugin family migrations.
The remaining steps (lspfind primitive refactor, fs-family rewrites,
tasks-as-plugin, VerifiedPluginSource unification, bundled-plugin
external verify) are scoped for a follow-up release.

### Architecture

- **`internal/runtime/pluginrun/Run`** is the unified wasm-plugin
  invoker. All four plugin invocation paths (CLI `tool run`, agent
  loop, MCP server, in-plugin `stado_tool_invoke`) dispatch through it.
- **`installedPluginTool.Run` is now a real invoker** instead of a
  sentinel error. Pre-Step-0 the agent loop and MCP server silently
  failed for installed plugins (only the CLI special-cased the
  dispatch). All three paths are uniform now.
- **`runtime.BuildRegistryWithPlugins(cfg)`** is the shared registry
  helper used by both `BuildExecutor` and the MCP server. Pre-Step-0.5
  the MCP server bypassed `BuildExecutor`, missing MCP-attach +
  wasm-migration + tool-overrides; now both surfaces see the same
  tool surface (minus `llm.invoke`, which the MCP server adds on top
  as documented MCP-only).

### Plugin families migrated

| Family | Was | Now |
|---|---|---|
| `webfetch` / `web.fetch` | native `webfetch.WebFetchTool` | wasm `web__fetch` via `stado_http_request` primitive |
| `bash` | native `bash.BashTool` (bare name, hardcoded bwrap policy) | wasm `shell__bash` (+ `shell__exec`, `shell__sh`, `shell__zsh`) via `stado_exec` |
| `ripgrep` | native `rg.Tool` | wasm `rg__search` via `stado_exec` |
| `ast_grep` | native `astgrep.Tool` | wasm `astgrep__search` via `stado_exec` |
| `http_request` | native `httpreq.RequestTool` (delegate) | true primitive `stado_http_request` (impl moved to `internal/httpreq/`) |

### Primitive surface changes

- `stado_exec` and `stado_proc_spawn` gain an optional `sandbox`
  field — when set, the call routes through `sandbox.Runner` with
  the supplied policy. When omitted, runs unsandboxed (today's
  default). **Plugin author opts in; stado is unbiased.**
- New: `stado_fs_readdir(path, offset, buf, cap)` — paged dir listing
  as JSON `[{name, type, mode}]`. Caller paginates via offset.
- New: `stado_fs_stat(path, buf, cap)` — `{mode, size, mtime, type}`.
- `procAllowed` matcher accepts both absolute-path globs
  (`exec:proc:/usr/bin/bash`) and slash-free basename globs
  (`exec:proc:bash`). Cross-distro portable. Mixed forms (relative
  paths with slashes) rejected at cap parse time.

### Removed surfaces

- Host imports: `stado_http_get`, `stado_exec_bash`, `stado_search_ripgrep`, `stado_search_ast_grep`.
- Caps: `exec:bash`, `exec:shallow_bash`, `exec:search`, `exec:ast_grep`. Use `exec:proc:<binary-glob>` instead.
- Native packages: `internal/tools/{httpreq,webfetch,bash,rg,astgrep}` (the latter two moved their bundled-binary helpers to `internal/{rg,astgrep}` as subsystem packages).
- Wasm shim modules: `ripgrep` (the legacy `stado_search_ripgrep` shim).

### Behavior changes

- **bash no longer routes through `sandbox.Runner` by default.** The
  native `bash.BashTool` hardcoded a bwrap policy (workdir + /tmp,
  net-deny). The new wasm `shell.bash` runs unsandboxed unless the
  plugin author passes a `sandbox` arg to `stado_exec` (per
  unbiased-runtime principle). Same trust model as every other
  `exec:proc` plugin.
- **`web.fetch` returns raw response body** instead of HTML→markdown
  conversion. The native webfetch ran `golang.org/x/net/html` to
  extract text; the wasm rewrite skips that step. If markdown
  conversion turns out to matter for model UX, restore via a shared
  `internal/bundledplugins/sdk/` helper.
- **Default autoload list** (`executor.go:43`) renames bare `bash` to
  wire-form `shell__bash`. Operators with custom `[tools].enabled`
  config that pinned `bash` need to update.

### Migration notes for plugin authors

- Manifests using `exec:bash` / `exec:shallow_bash` / `exec:search` /
  `exec:ast_grep` need re-signing with `exec:proc:<binary>`.
- Plugins using `stado_http_get` should switch to `stado_http_request`.
- Plugins using `stado_search_ripgrep` / `stado_search_ast_grep`
  should spawn the binaries via `stado_exec` directly (or add their
  own search helper).

## v0.44.2 — Two bugfixes

### Fixes

- **`stado_tool_invoke` now reaches installed plugins.** The CLI's
  `host.ToolInvoke.Invoke` callback called `reg.Run`, which for
  installed plugins hit `installedPluginTool.Run`'s sentinel error
  ("not invokable directly via Tool.Run"). Bundled plugins worked
  because their `Run()` is real. Detect the installed-plugin case via
  `runtime.LookupInstalledModule` and dispatch through
  `runPluginInvocation`, mirroring `tool_run.go`'s branch.
  (`cmd/stado/plugin_invoke_shared.go`)

- **`stado tool run` for installed plugins now defaults workdir to
  CWD.** The installed-plugin branch passed `filepath.Dir(wasmPath)` as
  `InstallDir` (= default workdir) — the plugin install directory under
  `~/.local/share/stado/plugins/<name>-<ver>/`. The bundled-plugin
  branch already used `os.Getwd()`. Net effect: relative paths in
  plugin args resolved against the install dir, not the operator's
  CWD. Surface symptom on Fedora Atomic looked like a `/home →
  /var/home` symlink-aliasing bug because `--workdir
  /var/home/<project>` "fixed" it; the actual root cause was the
  asymmetric default. The pre-existing `symlinkAlias` mechanism (v0.40.0)
  was already handling the real symlink case. (`cmd/stado/tool_run.go`)

## v0.44.1 — TUI persona surface

The TUI half of v0.44.0's personas feature — `/persona` slash command,
fuzzy-search picker, and a status-line indicator. Swaps live without
restarting the session; the next turn picks up the new operating
manual via `personas.AssembleSystem`.

### TUI

- **`/persona <name>`** switches the active persona for the current
  TUI session. `/persona` with no arg opens a fuzzy-search picker
  modeled on `/model` — type to filter, arrow keys to move, Enter
  to confirm, Esc to cancel. Picker entries are sourced project →
  user → bundled and dedupe by name (project shadows user shadows
  bundled), labeled accordingly.

- **Status-line indicator.** Active persona name appears as a quiet
  `· <name>` segment after the cost column when set. Only renders
  when a persona is active so the line stays uncluttered for
  operators who haven't picked one.

- **Persistence.** Selecting a persona via picker or `/persona <name>`
  also writes `[defaults].persona` to the active config — the next
  `stado` invocation boots with the same operator persona unless
  `--persona` overrides it.

### Internals

- `internal/tui/personapicker/` — modal picker (fuzzy search +
  list nav). Lighter than `modelpicker` (no favorites, no provider
  switch). Tests cover preselection, fuzzy filter, escape, no-match.
- `internal/tui/model_persona.go` — `initPersona` (boot resolution
  from `cfg.Defaults.Persona`), `switchPersona`, `openPersonaPicker`,
  `applyPersonaSelection`, `personaOrigin` (bundled/project/user
  labeling).
- `model_stream.turnSystemPrompt` now prefers `personas.AssembleSystem`
  when `m.persona != nil`; falls back to legacy
  `instructions.ComposeSystemPrompt` when no persona is set.
- `config.WriteDefaultPersona` — TOML write helper mirroring
  `WriteDefaults`. Empty value clears the key.

## v0.44.0 — Personas

Eight bundled operating-manual personas; `--persona` everywhere a
turn is initiated; `stado_llm_invoke` ABI cleaned up to JSON args
with persona / model / system / sampling fields.

### Personas

- **Eight bundled personas** under `internal/personas/library/`:
  `default`, `software-engineer`, `qa-tester`, `technical-writer`,
  `prose-writer`, `prose-editor`, `researcher`, `offsec`. Full
  6–10 KB operating manuals — switch via `/persona <name>` (TUI;
  v0.44.1) or `--persona <name>` (CLI). Operators add their own
  under `~/.stado/personas/` or `{project}/.stado/personas/`;
  resolution is project → user → bundled. Inheritance via
  `inherits: <name>` frontmatter (one level deep, cycle-detected).

- **`[defaults].persona`** in `config.toml` pins the session-default
  persona. `--persona` flags on `stado run` and `stado mcp-server`
  override per call / per server.

- **`agent.spawn` gains `persona`.** Sub-agents inherit the parent's
  persona unless their spawn args specify one — enabling the
  writer→editor / engineer→qa-tester delegation pattern. Threaded
  through `subagent.Request`, `SpawnOptions`, `SubagentRunner`, and
  `AgentLoopOptions`.

- **`AgentLoopOptions.Persona`** + `personas.AssembleSystem` —
  centralized system-prompt assembly: persona body → project
  AGENTS.md → memory context → per-call extra. Replaces the
  per-surface ad-hoc concatenation.

### Plugin runtime — ABI change

- **`stado_llm_invoke` is now JSON-args** —
  `(args_ptr, args_len, out_ptr, out_max) → i32`, where args is
  `{prompt, persona?, model?, system?, max_tokens?, temperature?}`.
  Replaces the bare-prompt + 4-i32 shape. Single-user repo so we
  break the wire cleanly rather than ship a v2 import. Bundled
  `auto-compact` plugin updated; existing third-party plugins (none
  yet) need to migrate.

### MCP

- **New native `llm.invoke` tool** registered by `stado mcp-server`
  (only — not in TUI / `stado run` paths where the model is the
  consumer, not a tool client). Lets external MCP clients (Claude
  Desktop, Zed, etc.) hit stado's configured provider with persona
  selection. Args: `{prompt, persona?, model?, system?, max_tokens?,
  temperature?}`.

- **`agent.spawn` schema gains `persona`** — visible to MCP clients
  automatically because the schema flows through to MCP exposure.

### Deferred to v0.44.1

- TUI `/persona` slash command + persona picker modal + status-line
  indicator. CLI + ABI surfaces ship now; the TUI surface follows.

## v0.43.1 — Windows build fix

### Fixes

- **goreleaser Windows target.** `syscall.SetsockoptInt` takes
  `syscall.Handle` on Windows but the EP-0038i `setBroadcastFD`
  helper was casting `fd` to `int` (POSIX shape). Split the helper
  into `host_net_setsockopt_unix.go` (`!windows`) and
  `host_net_setsockopt_windows.go` so each platform casts the
  `uintptr` from `SyscallConn.Control` to its own `SetsockoptInt`
  argument type. Cross-verified with `GOOS=windows GOARCH=amd64
  go build` and `GOOS=darwin GOARCH=amd64 go build`.

## v0.43.0 — stado_progress agent-loop integration (closes the EP-0038 backlog)

### Plugin runtime — agent-loop integration

- **`stado_progress` now reaches the model.** v0.38 introduced
  operator-visibility for progress emissions (TUI sidebar, `stado
  plugin run` stderr). v0.42 adds the model-visibility half: while
  a tool runs, emissions are collected per-call via a context-
  threaded `tool.ProgressCollector`; on successful return,
  `Executor.Run` prepends a `[progress] plugin: text` log to the
  tool's result envelope so the model sees the trail. Bounded at
  64 entries per call (FIFO drop on overflow). Suppressed when the
  tool errored (errored results stay clean). This is atomic from
  the model's POV — mid-tool model streaming would need an
  LLM-API streaming-tool-call contract that doesn't exist today;
  closing that gap would shift the agent-loop contract and is
  filed for if/when an upstream provider ships native support.

## v0.42.0 — EP-0038i ICMP echo (closes the network surface)

### Plugin runtime — new host imports

- **`stado_net_icmp_echo(host, timeout_ms?, count?, payload_size?)`** —
  ICMP echo (ping) for plugins doing reachability sweeps where a
  closed TCP port and a dropped IP are different signals. New
  capability `net:icmp`. Tries an unprivileged ICMP socket first
  (Linux `net.ipv4.ping_group_range` covers the running uid; macOS
  supports this without sysctl since 10.10); falls back to raw
  (`SOCK_RAW` + `IPPROTO_ICMP`) which needs `CAP_NET_RAW`. Error
  message names the fix (`sysctl ping_group_range` or `CAP_NET_RAW`).
  Private-IP guard via `NetHTTPRequestPrivate` — without that cap,
  loopback / RFC1918 / link-local destinations are refused at
  resolve. Result includes per-echo RTTs in milliseconds plus
  sent/received counts. Bounds: count ≤ 64, payload ≤ 1500 bytes.
  Uses `golang.org/x/net/icmp` (already a transitive dep).

## v0.41.0 — UDP broadcast/multicast + FleetBridge messaging real impl

### Plugin runtime — new host imports

- **`stado_agent_send_message` real impl.** Was a stub that validated
  the agent ID and silently dropped the body. Now: `Fleet` carries a
  per-agent inbox queue (bounded at 64 messages); `SendMessage` pushes
  onto it; `AgentLoop` drains queued messages at every turn boundary
  and prepends them as user-role inputs in the next turn request.
  Wired through a new optional `InboxAwareSpawner` interface
  (`Spawner` + `WithInbox(fn func() []string) Spawner`); the fleet
  type-asserts and supplies a closure drawing from the right inbox.
  `SubagentRunner` implements both. Effect: the bundled agent
  plugin's `agent.send_message` tool actually delivers messages
  mid-loop instead of being a no-op.

- **`stado_net_setopt(lst_udp, key, value)`** — broadcast / multicast
  setopts on a UDP listener handle. Five keys: `broadcast` (toggles
  `SO_BROADCAST`, required for sendto to broadcast addresses);
  `multicast_join` / `multicast_leave` (join/leave a multicast group
  on an optional named interface); `multicast_loopback` (whether
  multicast we send is looped back to us); `multicast_ttl` (TTL /
  hop limit on outgoing multicast packets, 0..255). All keys gated
  by the new `net:multicast:udp` capability. Group addresses
  validated as multicast (224.0.0.0/4 for IPv4, ff00::/8 for IPv6).
  Multicast wiring uses `golang.org/x/net/ipv4|ipv6` (already a
  transitive dep). Useful for discovery protocols (mDNS, SSDP,
  WS-Discovery, BACnet, NBNS).

## v0.40.0 — TUI tool-expand + mouse + PTY persistence + EP-0038i imports

Bundle release covering UI quality-of-life, two operator-reported
fixes, and three deferred plugin-runtime imports (HTTP upload
streaming, JSON set, AXFR DNS).

### Plugin runtime — new host imports

- **`stado_dns_resolve_axfr(zone, server, timeout_ms?)`** — DNS zone
  transfer (RFC 5936). Useful for security tooling enumerating
  internal zones on misconfigured / permissive infrastructure. New
  capability `dns:axfr` (implies `dns:resolve`). Plugin must name
  the authoritative server explicitly — no recursion. REFUSED rcodes
  land in `result.error` rather than crashing. Adds the
  `github.com/miekg/dns` dependency (single direct dep; the standard
  Go DNS library; binary impact bounded by the imports we use).

- **`stado_json_set(json, path, value) → modified_json`** — companion
  to v0.38.0's `_get` / `_format`. Mutates a value at a dotted path
  in a JSON document and returns the canonical bytes of the modified
  document. The `value` payload is itself parsed as JSON and embedded
  at the target. New object keys are added; out-of-range / non-numeric
  array indices return -1 (no implicit array growth). Walking
  through nil auto-creates intermediate objects so plugins can build
  nested structure incrementally. Empty path replaces the whole doc.
  No capability gating (pure compute, bounded to 256 KB input).

- **`stado_http_upload_create` + `_upload_write` + `_upload_finish`** —
  chunked HTTP request body delivery (EP-0038i, the symmetric
  counterpart to v0.38.0 response streaming). Plugins can now upload
  multi-GB payloads without buffering the whole body in wasm memory.
  New typed handle `httpup:<id>`; `_upload_finish` returns a
  `httpresp:<id>` so the plugin drains the response via the existing
  `stado_http_response_read` / `_close` imports — upload + download
  streaming compose. Reuses `net:http_request[:<host>]` cap; no new
  cap surface. Per-Runtime cap of 8 concurrent in-flight uploads;
  reaped on Runtime shutdown. Args JSON narrows to method/url/
  headers/timeout_ms/content_length — no `body_b64`. Out of scope:
  HTTP/2 server-push, multipart streaming, trailers, true bidi
  duplex.

### Fixes

- **`fs:read:.` / `fs:write:.` on Fedora Atomic / Silverblue / Bazzite.**
  On these distros `/home` is a symlink to `/var/home`. When the
  operator's workdir was the symlink form (`/home/user/repo`), the
  cap parser stored the literal path in `h.FSRead` but `realPath()`
  at file-access time resolved through the symlink to
  `/var/home/user/repo/...`. The cap-glob compare failed, so every
  `fs_read` call silently denied. Fix: at cap-parsing time, also
  append the `EvalSymlinks`-resolved form when it differs from the
  literal — both forms are now in the allowlist. Best-effort:
  missing path or EvalSymlinks failure falls back to the literal.
  Reported by the operator on Fedora Atomic.

- **Bundled `shell.*` / `pty.*` PTY persistence across calls.** Each
  `bundledPluginTool.Run` was creating a fresh `pluginRuntime.New`,
  which in turn created its own `pty.NewManager()`. So
  `shell.spawn` returned an id that the next call's `shell.attach` /
  `read` / `write` couldn't see — every dispatch got a fresh empty
  registry and the second call returned `pty: session not found`.
  Reported on `v0.37.0+`. Fix: new optional `tool.PTYProvider`
  interface; long-lived hosts (TUI session, MCP server, headless
  agent loop) construct one `*pty.Manager` and expose it via
  `PTYManager()`. The bundled-plugin Run path now type-asserts and
  reuses the shared manager when present, falling back to the
  per-call manager otherwise (one-shot `stado plugin run` / `stado
  tool run` are still single-process so the per-call fallback is
  fine for those). Added a regression test
  (`TestBundledPluginTool_HonoursPTYProvider`) that spawns a real
  PTY between two `shell__list` dispatches and confirms the second
  call sees it.

### TUI

- **Expand older tool calls.** `Shift+Tab` previously only toggled
  the latest tool call or assistant turn details. New navigation:
  `Alt+Up` / `Alt+Down` move focus to the previous / next
  expandable block (rendered with a left-edge accent marker);
  `Shift+Tab` then toggles whichever block is focused. With no
  focus, `Shift+Tab` falls back to the previous "latest" behaviour.
- **Mouse left-click expands a tool block.** Click any tool /
  expandable assistant block in the conversation to focus + toggle
  it. Clicks past the conversation pane (sidebar, input) fall
  through to default behaviour.
- **`[tui].mouse_capture` config option** — default `true` (current
  behaviour: app captures mouse events for click-to-expand + scroll
  wheel). Operators who prefer terminal-native click-drag-to-
  select-text can set `false` to disable capture entirely. With
  capture on, holding `Shift` while click-dragging usually bypasses
  app capture in modern terminals.
- **`stado plugin list` PATH column** — each row now shows the
  on-disk path to the plugin's `.wasm` (or `(embedded)` for bundled
  plugins). Useful for `cp` / `file` / `wasm-objdump` / `sha256sum`
  workflows without remembering the state-dir layout.
- **Cleaner text selection.** When the sidebar is hidden (`Ctrl+T`)
  the chat column no longer pads each row to a fixed width —
  click-drag-to-select copies just the visible text instead of a
  trail of pad spaces. The sidebar adjacency problem (rectangular
  terminal selection grabs sidebar text too) remains terminal-side;
  `tui.md` documents three escape hatches: hide sidebar first,
  block-mode `Alt+drag`, or set `[tui].mouse_capture = false`.

## v0.39.0 — session.search plugin + TUI progress surface

### Plugins

- **`session.search`** — new bundled wasm plugin offering grep-style
  search over the current session's message history. Substring
  (default) or RE2 regex (`is_regex: true`); case-insensitive by
  default; optional role filter; bounded results + snippet length.
  Capability: `session:read` (existing — no new host imports).
  Search core lives in `searchcore/` so it builds + tests on the
  host arch alongside the wasip1-only main module.

### TUI

- **`stado_progress` surfaces in the sidebar.** Closes the half-shipped
  EP-0038h piece. Bundled wasm plugins emitting `stado_progress` now
  show as `PROGRESS [plugin] text` entries in the TUI's log-tail
  sidebar — always visible (no `--sidebar-debug` required), styled as
  accent. Wired via a new `tool.ProgressEmitter` optional Host
  extension; `bundled_plugin_tools.Run` type-asserts and populates
  `host.Progress`. Headless runs without an attached operator stay
  silent (nil callback, plugin doesn't fail).

## v0.38.0 — EP-0038h: JSON helpers + UDP stateless + HTTP streaming + stado_progress

Bundle of four deferred items from v0.36–v0.37. Each is small on its
own; releasing them together avoids per-tag overhead.

### Plugin runtime — new host imports

- **`stado_json_get(json, path) → bytes`** + **`stado_json_format(json, indent) → bytes`** —
  host-side JSON conveniences. Plugins extract one value from an
  HTTP response or pretty-print a payload without bundling a 50 KB
  parser into every wasm binary. Path is dotted form (`a.b.0.c`); no
  filters or globs. No capability gating (pure compute, bounded to
  256 KB input).
- **`stado_net_listen("udp", host, port)` + `stado_net_sendto` + `stado_net_recvfrom`** —
  stateless UDP. A UDP listen handle backs both
  send-to-anyone and recv-from-anyone, mirroring Go's
  `net.PacketConn`. New cap `net:listen:udp:<host>:<port>`. Outbound
  peers in `_sendto` are gated by the **same `net:dial:udp:` glob set**
  as connect-mode UDP — a UDP listener can't be a wildcard spray gun.
  `_recvfrom` packs `(body_len << 32) | addr_len` into an i64 return
  with `-1` / `-2` sentinels in the body slot.
- **`stado_http_request_stream` + `stado_http_response_read` + `stado_http_response_close`** —
  chunked HTTP body delivery. Resolves the large-payload OOM in
  `stado_http_request`. New typed handle `httpresp:<id>`. Reuses
  existing `net:http_request[:<host>]` cap; no new cap surface.
  Per-Runtime cap of 8 concurrent open streams; reaped on Runtime
  shutdown. Out of scope: request-body streaming (uploads), HTTP/2
  push, multipart, `proxy_url` (use the non-streaming variant for
  SOCKS pivots).
- **`stado_progress(text_ptr, text_len) → i32`** — operator-visible
  progress emission for long-running tools (>2s). v1 is
  operator-visibility only; the agent / model sees only the final
  tool result. Mid-tool partial output to the model would break
  tool-call atomicity in current LLM contracts and is explicitly
  out of scope. 4 KB max per call. `stado plugin run` prints
  `[plugin] text` to stderr; TUI integration follows.

### Plugin manifest

- New capability vocabulary:
  - `net:listen:udp:<host>:<port>` — bind a UDP socket for
    stateless send/recv (verbatim host-port match like TCP listen)
- Plugin doctor classifies the new UDP listen cap.

### Deferred

ICMP raw sockets (per operator: last), AXFR DNS, FleetBridge
messaging real impl, `stado_json_set` / jq-style queries, UDP
broadcast/multicast set-options, HTTP request body streaming
(uploads), `stado_progress` agent-loop integration (model sees
mid-tool partials).

## v0.37.0 — EP-0038g: net expansion (UDP + Unix sockets + listen/accept)

Continuation of the v0.36.0 EP-0038f TCP work. Plugins can now talk
non-HTTP datagrams, Unix domain sockets, and accept inbound
connections.

### Plugin runtime — new host imports

- **`stado_net_dial("udp", host, port, timeout_ms)`** — connect-mode
  UDP client. Same `conn` handle / read-write-close lifecycle as TCP.
  Capability: `net:dial:udp:<host-glob>:<port-glob>`. Private-IP guard
  shared with TCP (`net:http_request_private` extends to UDP).
  Unblocks NTP, DNS-over-UDP, syslog, custom binary RPC.
- **`stado_net_dial("unix", path, 0, timeout_ms)`** — Unix domain
  socket client. Capability: `net:dial:unix:<path-glob>` (path glob
  via `filepath.Match`). Path validation refuses `..` traversal and
  socket paths > 104 bytes (BSD `sun_path` upper bound).
- **`stado_net_listen` + `stado_net_accept` + `stado_net_close_listener`** —
  server-side networking for `tcp` and `unix` transports. New typed
  handle prefix `listen:<id>`. Per-Runtime cap of 8 listeners; 9th
  bind returns -1. Accept timeout is required, clamped to
  `[default 5s, max 30s]` (DoS guard); -2 distinguishes timeout from
  error. Unix listeners auto-remove their socket file on close and on
  Runtime shutdown.

### Plugin manifest

- New capability vocabulary:
  - `net:dial:udp:<host>:<port>` — outbound UDP
  - `net:dial:unix:<path-glob>` — outbound Unix socket
  - `net:listen:tcp:<host>:<port>` — bind TCP (host = `127.0.0.1` for
    loopback, `0.0.0.0` for any-interface; **verbatim match — no
    implicit widening**, operator must opt in to public binds
    explicitly)
  - `net:listen:unix:<path-glob>` — bind Unix socket
- Plugin doctor (`stado plugin doctor`) classifies each new cap with
  per-transport notes.

### Bug fixes (carried in)

- **`net:dial:tcp:` parser regression from v0.36.0.** The capability
  was silently never reaching the parser block (`SplitN(cap, ":", 3)`
  doesn't expose `parts[2..4]`); it was instead being junk-populated
  into `NetHost`. The v0.36.0 access-layer tests masked the gap.
  Fixed by re-splitting `net:dial:*` / `net:listen:*` caps on every
  colon. v0.36.0 plugins that relied on `net:dial:tcp:*` now actually
  work as documented.

### Deferred

ICMP raw sockets (needs `CAP_NET_RAW`), AXFR DNS, HTTP request
streaming, FleetBridge messaging real impl, `stado_json_*`
conveniences, and UDP stateless `send_to`/`recv_from` remain on the
backlog. Per the architectural-reset spec, `stado_progress` streaming
for partial tool output is also still pending — needs agent-loop
integration design.

## v0.36.0 — Lazy-load realised, host imports doc, EP-0038f TCP, tester-feedback items

This release closes the central architectural-reset gap (lazy-load
not actually filtering the per-turn surface) and ships seven new
plugin-runtime imports addressing concrete tester pain.

### Plugin runtime — new host imports

- **`stado_http_request` proxy_url field** — http(s) + socks5(h)
  schemes route the request through a proxy. Use case: after a
  ligolo-ng pivot every WASM tool reaches inner subnets without
  dropping to bash. Dial guard still applies to the proxy itself;
  `net:http_request_private` covers loopback proxies.
- **`stado_instance_get/set/delete/list`** — process-lifetime in-memory
  KV store with per-plugin namespacing. Resolves multi-step exploit
  chains needing state across tool calls (auth cookies, session
  tokens). Bounded: 1 MB per value, 16 MB per plugin. Capabilities:
  `state:read[:<glob>]`, `state:write[:<glob>]`.
- **`stado_tool_invoke`** — wasm plugins call other registered tools.
  Capability: `tool:invoke[:<name-glob>]`. Recursion depth-limited
  (4). Errors wrapped in JSON envelope.
- **`stado_net_dial / read / write / close`** — Tier 1 TCP raw socket
  primitives (BACKLOG #11 — partial; UDP/Unix/listen/ICMP deferred to
  EP-0038g). Capability: `net:dial:tcp:<host-glob>:<port-glob>`.
  Same private-address dial guard as http_request.
- **`stado_secrets_delete`** — wasm wrapper for the existing
  `secrets.Store.Remove`. Cap-gated by `secrets:write`.
- **Four new meta-tools** (always autoloaded alongside `tools__search`
  / `_describe` / `_categories` / `_in_category`):
  `tools__activate`, `tools__deactivate`, `plugin__load`,
  `plugin__unload`. Lets the agent skip the describe round-trip when
  a parent already named the tool, and bulk-load/unload all of a
  plugin's tools.

### Lazy-load realised (EP-0037 §E)

The architectural reset's central design — "stado stops broadcasting
every tool's schema in the system prompt" — was 80% built but the TUI
wasn't actually filtering per-turn `toolDefs()`. Fixed:

- `Model.activatedTools` map; cleared on `/clear`.
- `toolSurfaceForTurn()` returns autoload ∪ activated, with Plan-mode
  + session-override filters.
- `Host.ActivateTool` / `DeactivateTool` (`pkg/tool.ToolActivator` /
  `ToolDeactivator`) now actually implemented.
- `tools__describe` results parsed and added to activation set after
  every tool turn (via `runtime.AbsorbActivatedFromDescribe`).

Net effect: a session with N installed plugins now sends only autoload
core (~8 tools) + whatever the model has explicitly activated, rather
than every tool's schema every turn.

### Tool-surface configuration

- **`[tools].autoload_categories`** — list of category names. Every
  tool whose `tools[].categories` overlaps joins the autoload set.
  Layered on top of name-based autoload. Lets operators run lean with
  rich plugin sets (declare `["recon"]`; pull exploit_* on demand via
  `tools.activate`).

### Plugin manifest

- **`requires []string`** — plugin dependency declarations.
  `["http-session >= 0.1.0", "secrets-store"]`. `stado plugin install`
  verifies each entry is installed at a satisfying version; install
  fails with a multi-error listing every unsatisfied dep at once.

### Tool surface — operator-collapsed surface

- **`spawn_agent` removed** — was already done in v0.35.0 but worth
  noting again: the canonical agent-spawn surface is `agent.spawn`
  (wasm), part of the unified `agent.*` family. Manifests declaring
  `subagent.Tool` registrations need to drop them.

### Documentation

- **`docs/plugins/host-imports.md`** — comprehensive reference for
  every wasm host import (~70 total). Tier 1/2/3 grouping, capability
  gates, ABI conventions, Patterns + anti-patterns section addressing
  tester feedback ("don't conflate plugin execution with agent
  orchestration"; "use exec:proc:<binary> over exec:bash"). Linked
  from `docs/features/plugin-authoring.md` as the first stop.

### Plugin doctor

- New cap classifications: `state:*`, `tool:invoke:*`, `net:dial:*`.

### Deferred

- **Tier 1 net beyond TCP** — UDP, Unix sockets, listen/accept,
  ICMP. EP-0038g (own design cycle).
- **`stado_progress` streaming** — partial-output channel for tools
  that take >2s. Out of scope this cycle; deserves design care for
  agent-loop integration.
- **`stado_dns_resolve_axfr`**, **`stado_json_*`**, **HTTP streaming**
  — housekeeping items also for EP-0038g.

## v0.35.2 — `.github/dependabot.yml` for explicit example-plugin scans

### Infra

- **`.github/dependabot.yml`** — explicit scan configuration covering
  the parent gomod module, all example-plugin subdirs (`/plugins/default/*`,
  `/plugins/examples/*` — 18 of them total) via the `directories`
  (plural) glob shape, and GitHub Actions in `.github/workflows/`.
  Without this, dependabot's auto-discovery rescanned the plugin
  go.mods on a slow cadence; v0.35.1's fix took longer than expected
  to flip its 4 alerts to "fixed". Explicit weekly schedule + commit-
  message scopes (`chore(deps)`, `chore(plugin-example)`,
  `chore(ci)`) keep future bumps consistent.

## v0.35.1 — Dependabot bumps for golang.org/x/net

### Fixes

- **Browser-plugin go.mods bumped to `golang.org/x/net v0.43.0`** —
  closes 4 dependabot alerts (GHSA-qxp5-gwg8-xv66 HTTP-proxy IPv6
  bypass, GHSA-vvgc-356p-c3xw XSS) on
  `plugins/default/browser/go.mod` and `plugins/examples/browser/go.mod`
  where x/net was pinned at v0.30.0.

### Infra

- `go mod tidy` promoted `github.com/fsnotify/fsnotify v1.7.0` from
  `// indirect` to direct in the parent module — it was always
  consumed directly by the plugin-dev-watch wiring; the indirect
  tag was stale from when the dep was first added.

## v0.35.0 — Plugin bundle, dev watch, Tier 2 (HTTP client + secrets), spawn_agent collapse

### Breaking changes

- **CLI** — `--tools-whitelist` renamed to `--tools` (canonical per
  architectural-reset NOTES §10). No back-compat alias kept; pre-1.0
  means scripts using the old name need updating. The previous bool
  `--tools` (on/off gate) is removed; use `--no-tools` for pure-chat
  mode. `--tools` is now the comma-separated whitelist (empty = all
  installed tools enabled).
- **CLI breaking** — `stado plugin run <plugin-id> <tool> [args]`
  removed. Use `stado tool run <name> [args]` instead — it resolves
  bundled and installed tools uniformly through the live registry.
  Accepts both canonical (`fs.read`) and wire (`fs__read`) names;
  `--session` and `--workdir` carry over; `--force` overrides
  `[tools].disabled` for one-off invocation.
- **Tool surface** — `spawn_agent` (native) removed in favour of
  `agent__spawn` (wasm). Both paths went through `SubagentRunner`;
  the wasm form is a strict superset (adds `agent__list`,
  `read_messages`, `send_message`, `cancel`, async mode). Default
  autoload list rewritten: `spawn_agent` → `agent__spawn`. Manifests
  declaring `subagent.Tool` registrations need to drop them.

### Plugin authoring

- **`stado plugin bundle <ids>... --out=<binary>`** — appends already-
  compiled wasm plugins to the trailing bytes of a stado binary,
  producing a portable customised stado without requiring a Go
  toolchain. Two-level signature verification: per-plugin author
  signature + outer ephemeral-by-default bundler signature seals
  the payload. `--bundling-key=<seed>` for persistent identity,
  `--allow-unsigned` to skip per-plugin trust-store check,
  `--allow-shadow` to override tool-name collisions. Sub-actions:
  `--strip --from=<bundled>` (extract vanilla copy),
  `--info --from=<binary>` (list bundle contents). Runtime escape
  hatch: `--unsafe-skip-bundle-verify` boots a tampered bundle
  with a loud warning + permanent `[unsafe-skip-verify]` marker
  in `--version`. Spec at
  `docs/superpowers/specs/2026-05-06-plugin-bundle-design.md`.
- **`stado plugin dev <dir> --watch`** — file-watch + auto-rebuild
  + auto-reinstall under a `0.0.0-dev` sentinel that gets cleaned
  up on Ctrl+C. 250ms debounce; requires `<dir>/build.sh`.
  Persistence-free in spirit (the sentinel install + active marker
  are removed on watch exit). Reuses the unified-registry slot —
  plugin tools become visible via `stado tool run` / `tool list`
  / mcp-server immediately. Spec at
  `docs/superpowers/specs/2026-05-06-plugin-dev-watch-mode-design.md`.
- **`stado plugin install --autoload`** — persists the newly-installed
  plugin's tools into `[tools].autoload` so they load into every
  session without a separate `stado tool autoload` call.
- **`stado plugin reload <name>`** (CLI) + `/plugin reload [<name>]`
  (TUI) — CLI is advisory (tool calls re-read plugin.wasm per
  invocation); TUI rebuilds the executor's tools registry so
  plugins installed AFTER session start become visible without
  restarting.
- **`stado plugin sign --key-env <ENVVAR>` + `--quiet`** — CI-
  friendly signing flow. The seed is read from an env var (hex or
  base64), eliminating the temp-file dance for runner secrets.
  `--quiet` suppresses informational stdout.

### Plugin runtime — Tier 2 (EP-0038e)

- **Stateful HTTP client** — new `internal/httpclient/Client` with
  cookie jar, redirect cap (default 10, with `follow_subdomain_only`),
  per-host + total connection mux limits, dial guard (RFC1918 /
  loopback / link-local refused unless `AllowPrivate=true`), and
  exact + suffix-glob host allowlist. Wasm imports
  `stado_http_client_create / _close / _request` gated by
  `net:http_client` capability; the existing
  `net:http_request:<host>` allowlist still bounds reachable hosts.
  Per-Runtime cap of 64 open clients prevents resource exhaustion.
- **Operator secret store** — new `internal/secrets/Store` backs
  `<StateDir>/secrets/<name>` with mode-0600 files and refuse-on-
  permission-widening. Wasm imports `stado_secrets_get / _put /
  _list` gated by `secrets:read[:<glob>]` and `secrets:write[:<glob>]`
  capabilities. Every call (allowed or denied) emits a structured
  audit event — names yes, values never. New CLI:
  `stado secrets set/get/list/rm <name>`. Spec at
  `docs/superpowers/specs/2026-05-06-ep-0038e-tier2-stateful-design.md`.
- **`stado plugin doctor`** — added cap-vs-sandbox cross-check that
  flags concrete mismatches between manifest caps and `[sandbox]`
  config. E.g. `net:http_request` with `[sandbox.wrap].network = "off"`
  → error; `fs:read:/etc/passwd` not in `bind_ro` → warn; etc.
- **Unified registry follow-ups** — `tool run gtfobins.lookup`
  (dotted form) now resolves installed plugins whose authors use
  the single-underscore wire convention (`gtfobins_lookup`); tier-4
  fallback in `lookupToolInRegistry`. `plugin info <name>` (bare
  name, no version) resolves via the new
  `runtime.ResolveInstalledPluginDir` helper.

### Tool dispatch

- **`tools__describe`** — accepts `name: "foo"` (single) OR
  `names: ["foo","bar"]` (batched). Both forms can be passed in
  one call; entries are merged and deduped preserving order.
  Replaces the names-array-only schema.

### CLI

- **`stado --unsafe-skip-bundle-verify`** — top-level persistent
  flag for runtime-skip of bundled-payload verification. Loud
  stderr warning; permanent `[unsafe-skip-verify]` marker in
  `--version` output.
- **`stado --version` custom-bundle marker** — when a binary
  contains a user-bundled payload, version output appends
  `(custom: N plugins, bundler=<8-char-fpr>)` for operator
  visibility.
- **`stado secrets set/get/list/rm`** — see Plugin runtime above.

### Plugin metadata

- New capability vocabulary entries (with `plugin doctor`
  classification): `net:http_client` (creates HTTP clients +
  uses existing host allowlist), `secrets:read[:<glob>]`,
  `secrets:write[:<glob>]`.

### Infra

- **Security/PII audit infrastructure** —
  `.gitleaks.toml` extends the default ruleset with project-specific
  allowlists (binary noise, OAI-compat test URI, examples).
  `.pre-commit-config.yaml` runs gitleaks + detect-private-key +
  trailing-whitespace + EOL hooks on every commit.
  `.github/workflows/secret-scan.yml` runs gitleaks-action against
  every PR and push to main.
  Working-tree path-leak strip: 121 `/home/<username>/...`
  occurrences across docs replaced with `~`/`<repo-root>`.
  Editorial pass on `docs/eps/notes/2026-05-05-architectural-reset.md`
  (2018 lines of chat transcript curated to 152-line summary).
- **Sandbox test resilience** — bwrap test now prefers
  `/usr/bin/python3` over `which python3` so linuxbrew-style
  environments (where python lives at a path bwrap doesn't bind)
  don't false-fail.

### Deviation documentation

- **`buildNativeRegistry()` retention** — original EP-0038b Task 5
  called for deletion; documented as a deliberate retention in
  EP-0038's "Deviation" section. Native code stays as the parity-
  test backstop and as the operational fallback when a wasm
  family misbehaves in production.

### Fixes

- **fsnotify integration** — added as a direct dep for plugin dev
  watch mode; previously listed `// indirect`.

## v0.34.1 — Atomic Fedora / Bazzite, multi-tool wasm, exec:proc multi-glob

### Fixes

- **`/home → /var/home` strict-walk regression** — every tool that touched a
  session worktree (`~/.local/state/stado/worktrees/...`) failed on Atomic
  Fedora / Bazzite with `directory component is a symlink: home` because the
  no-symlink walk used by `treebuild`, `subagent_adoption`, audit/minisign,
  several `cmd/stado/*.go` writers, and most TUI / tool readers rejected the
  operator-supplied `/home` link. Fixed by extending the EP-0028 trust-anchor
  model to file/dir Open: new `OpenRegularFileUnderUserConfig` +
  `ReadRegularFileUnderUserConfigNoLimit` plus migration of ~17 call sites
  to the anchored variants. In-userspace attacker boundary preserved (strict
  walk from anchor down). Adversarial paths outside HOME / XDG still get
  the strict from-`/` check via the fallback inside `OpenRoot*UnderUserConfig`.
- **TUI bash / native-tool cwd parity** — TUI tools now operate on the user's
  launch CWD (matching `stado run` default) instead of the session audit
  worktree. Both override paths fixed (`resetForSession` and
  `model_stream.go` host-adapter wiring). The session worktree remains in
  use only for turn-boundary tree commits.
- **Multi-tool wasm "missing ABI exports"** — `newBundledWasmTool` was
  setting `def.Name` to the full export name (`stado_tool_ls`), but the
  dispatcher prepends `stado_tool_` again — `fs.ls`, `shell.spawn/...`,
  `agent.spawn/...`, `web.fetch`, `dns.resolve` all looked up
  `stado_tool_stado_tool_<X>` and failed. Strip the prefix.
- **`exec:proc:<glob>` allows multiple scoped binaries per manifest** —
  `Host.ExecProcGlob` was a single string; the second declaration silently
  overwrote the first. Switched to `[]string` so e.g. `fs.ls` declaring
  both `/bin/ls` and `/usr/bin/ls` works regardless of which is the
  symlink and which the canonical path.
- **`plugin run` exec:bash gate restored to documented behavior** — was
  drifting toward always-refuse; restored EP-0028 D1's warn-loud-run-by-
  default with `[sandbox] refuse_no_runner = true` opt-in for hard refusal.
- **`cobra.Command.Context()` nil-fallback** in `plugin run` (panic surfaced
  under -race in CI when `RunE` invoked without a parent `Execute()`).
- **`spawn_agent` autoload** — restored to the default tool surface so the
  native subagent SubagentEvent path stays reachable without explicit
  `tools.describe` activation (regression from EP-0037 autoload).

### Tool dispatch (EP-0037 §F follow-up)

- **`stado tool enable / disable / autoload / unautoload`** — the locked
  config-mutating verbs. Replaces TOML hand-edits to `[tools]` for
  managing visibility and per-turn surface. Flags: `--global` (writes
  `~/.config/stado/config.toml` instead of project's `.stado/config.toml`),
  `--config <path>` (explicit target), `--dry-run`. Inverse-list cleanup
  on the right side: `tool enable` strips from disabled; `tool disable`
  strips from enabled and autoload.

### Lint / CI

- Cleared five dead-code findings exposed once golangci-lint stopped
  early-exiting (`resolveToolName`, `subagentSpawnerAdapter`,
  `monitorLineMsg`, `loopStatusLabel`, `toolLike`).
- `host_proc.go` receiver name `host` → `h` (ST1016).
- `host_compress.go` defer wrapped to satisfy errcheck.
- `sandbox/wrap.go` error string reflowed (ST1005).

### Docs

- Restored architectural-reset notes (2026-05-05) under
  `docs/eps/notes/2026-05-05-architectural-reset.md` — the design history
  that produced EP-0037/0038/0039.

## v0.34.0 — Tool list UX, terminal aliases, fs.ls, web/dns wasm, remote install

### Tool surface (EP-0037 follow-ups)

- **`stado tool list`** — replaces `tool ls`, adds **PLUGIN** + **CATEGORIES**
  columns and renders dotted canonical names (`fs.read`, `shell.exec`). Bundled
  tool metadata layer maps both wire and bare names. Hidden tools (`approval_demo`,
  superseded natives like `webfetch`/`spawn_agent`) drop out of listings.
- **`plugin list`** — proper table: NAME / VERSION / TOOLS / AUTHOR / FINGERPRINT
  / TRUST.
- **`plugin info`** — defaults to human-readable tool schemas / params /
  capabilities; `--json` for scripting.
- **Autoload fix** — `spawn_agent` re-added to the default autoload set so the
  native `SubagentEvent` path is reachable without explicit `tools.describe`
  activation (regression from EP-0037).

### ABI v2 wasm modules (EP-0038 follow-ups)

- **`stado_terminal_*` aliases** for the existing `stado_pty_*` host imports
  (`open/list/attach/detach/write/read/signal/resize/close`). Capability check
  enforced at call time, so multi-tool wasm modules link cleanly even with
  partial cap grants.
- **`shell.wasm`** — full PTY surface: `shell.spawn/list/attach/detach/read/`
  `write/resize/signal/destroy` plus one-shot `shell.exec/bash/sh/zsh`.
  Capabilities: `terminal:open` (PTY) + `exec:proc` (one-shot).
- **`fs.ls`** — folded into `fs.wasm` via `stado_exec` over `/bin/ls`. Bare `ls`
  binary embedding dropped.
- **`web.wasm`** — `web.fetch` wrapper over `stado_http_get`. Native `webfetch`
  hidden in favour of the wasm version.
- **`dns.wasm`** — `dns.resolve` over `stado_dns_resolve` (A/AAAA/TXT/MX/NS/PTR).
- **`agent.*` tools** — `agent.spawn/list/read_messages/send_message/cancel`
  wired through `tool.AgentFleetProvider` → `FleetBridge`. Native `spawn_agent`
  remains as the authoritative SubagentEvent path; `agent.spawn` is the
  wasm-backed surface.

### Plugin distribution (EP-0039 follow-ups)

- **Remote install** — `stado plugin install github.com/owner/repo@vX.Y.Z`.
  Three-tier resolution: GitHub release artefact → raw tree fetch → source build
  (deferred). Lock file written on success.
- **`plugin update [--check]`** — drift check / re-pin against lock entries.
- **`plugin verify-installed`** — re-verify signatures of installed plugins.
- **Project-walking lock file** — `.stado/plugin-lock.toml` discovered up the
  directory tree.

### Plugin examples

- **`plugins/examples/browser`** — Tier 1 + 2 wasm browser sample.
- **`plugins/examples/image-info`** — image metadata wasm sample.
- **`plugins/examples/ls`**, **`plugins/examples/mcp-client`**, others.

## v0.33.0 — Tool dispatch, ABI v2, plugin distribution, supervisor lane, browser

### Tool dispatch and operator surface (EP-0037)

- **Wire-form naming** — canonical dotted (`fs.read`) + wire `__` form (`fs__read`).
  21-entry frozen category taxonomy, validated at `plugin install` time.
- **Four meta-tools** — `tools__search/describe/categories/in_category` as
  non-disableable dispatch kernel. `tools.describe` activates non-autoloaded schemas
  into the session surface.
- **Autoload dispatch** — only the autoloaded core hits the turn's tool surface
  (default: `read/write/edit/glob/grep/bash` + kernel). Configurable via
  `[tools.autoload]` or `--tools-autoload`.
- **`stado tool ls|info|cats|reload`** subcommand.
- **CLI** — `--tools-whitelist`, `--tools-autoload`, `--tools-disable`.
- **TUI** — `/tool ls|info|cats|reload`, `/session list|show|attach|detach`.

### ABI v2 and bundled wasm tools (EP-0038)

- **New host imports** — `stado_proc_spawn/read/write/wait/kill/close`,
  `stado_exec`, `stado_bundled_bin`, `stado_fs_read_partial` (offset/length partial
  read, D24), `stado_dns_resolve`, `stado_hash/hmac` (md5/sha1/sha256/sha512),
  `stado_compress/decompress` (gzip/zlib). Handle registry (32-bit, collision check).
- **Wasm migration** — bundled wasm plugins: `fs`, `shell`, `rg`, `readctx`,
  `agent`. Per-tool parity flags (`[runtime.use_wasm.*]`). `fs` and `shell` parity
  tests pass. `ApplyWasmMigration()` activated from `BuildExecutor`.
- **Agent surface** — `FleetBridge` + `stado_agent_*` host imports. `FleetBridgeAdapter`
  wraps existing Fleet + SubagentRunner. `agent.spawn/list/read_messages/send_message/cancel`.
- **Sandbox wrap-mode** — `[sandbox] mode = "wrap"` re-execs under
  bwrap/firejail/sandbox-exec. `[sandbox.wrap]` config for binds and network.
  `--with-tool-host` deprecated (ToolHost always wired).
- **TUI** — `/ps`, `/top`, `/kill`, `/stats`, `/sandbox`, `/config`. `[YOU]` marker
  on operator messages in multi-producer sessions.
- **`/session attach` RW** — inject messages into a running agent session.

### Plugin distribution and trust (EP-0039)

- **VCS identity** — `github.com/owner/repo[@subdir]@vX.Y.Z`. Floating refs
  rejected. `plugin install` validates semver/SHA format.
- **Anchor-of-trust** — per-owner TOFU via `AnchorTrustStore`.
- **Lock file** — `.stado/plugin-lock.toml` per project.
- **SHA256 drift detection** — auto-reinstall on wasm sha256 change. `--force` flag.
- **Quality pass** — `plugin trust --pubkey-file`, `plugin use <name>@<ver>`,
  `plugin dev <dir>` (one-step authoring loop).

### Supervisor lane (EP-0033)

- **`[supervisor] enabled`** — when on, input during a streaming turn is classified
  before queuing: questions → `tools.btw` answer, steer phrases → guidance note,
  interrupt phrases → cancel worker.
- **`/supervisor on|off|status`** TUI slash commands.

### Security-research harness (EP-0030)

- **`stado run --mode security`** — activates recon-first discipline, abusability
  filter (PoC-or-it-didn't-happen), candidate vs verified split, engagement folder
  conventions in the system prompt.
- **`stado harness init`** — creates `notes/engagements/` and
  `.stado/harness/security.md` (customisable prompt override).

### Browser plugin

- **`plugins/default/browser`** — two-tier browser, auto-available to all models.
  - **Tier 1** (no deps): `browser_open`, `browser_click`, `browser_query` — HTTP
    fetch, cookie jar, Chrome/Firefox/Safari headers. `needs_js: true` escalation hint.
  - **Tier 2** (requires `chromium`/`google-chrome`): `browser_cdp_open`,
    `browser_cdp_navigate`, `browser_cdp_eval`, `browser_cdp_screenshot`,
    `browser_cdp_click_element`, `browser_cdp_type`, `browser_cdp_scroll`,
    `browser_cdp_close` — real headless Chrome, full JS, real DOM events, keyboard,
    scroll triggers. Anti-detection: `--disable-blink-features=AutomationControlled`.

### Infra / Fixes

- **Makefile** — `GOTMPDIR` redirected off `/tmp` to avoid per-user quota.
- **EP-0032** (ACP client) — phases A+B marked Implemented.

## v0.32.0 — /loop, /monitor, stado schedule, .stado/ project dir, sampling args

### TUI

- **`/loop [duration] <prompt>`** — repeat a prompt automatically. Immediate-repeat
  (fires as soon as each turn finishes) or timed (`/loop 5m "check deploy"`). Agent
  self-terminates by including `[LOOP_DONE]` in its response; operator cancels with
  `/loop stop`. Status bar shows `↻ loop (5m)` while active. EP-0036.

- **`/monitor <cmd>`** — stream a process's stdout into the current session as
  `[monitor]` system blocks. Each stdout line is injected as a notification so the
  agent can react to log events, CI output, or any live stream. `/monitor stop` kills
  the background process. EP-0036.

### CLI

- **`stado schedule`** — persistent scheduled runs. `create --cron "0 9 * * *"
  --prompt "..."` persists entries to `<state-dir>/schedules.json`. Subcommands:
  `list`, `rm`, `run-now`, `install-cron`, `uninstall-cron`. `install-cron` writes
  OS crontab entries for all active schedules; `uninstall-cron` removes them. No
  daemon required — OS cron handles timing. EP-0036.

- **`stado run --temperature / --top-p / --top-k`** — one-shot sampling overrides.
  Also configurable in `config.toml` (or `.stado/config.toml`) under `[sampling]`.
  Wired into TurnRequest for both TUI and headless AgentLoop. Zero/nil = provider
  default. EP-0036.

### Config

- **`.stado/` project-local directory** — commit stado config alongside the repo.
  Three artefacts: `.stado/config.toml` (overlays user config, project wins),
  `.stado/AGENTS.md` (stado-specific agent instructions, sits between `AGENTS.md`
  and `CLAUDE.md` in the walk), `.stado/plugins/` (project-local plugin search
  dir, supplements global state-dir). Discovery walks cwd upward; nearest wins.
  New helpers: `Config.ProjectStadoDir()`, `.ProjectPluginsDir()`, `.AllPluginDirs()`.
  EP-0035.

### Plugins

- **`plugins/examples/http-session`** (Go, ~3.5 MB) — reusable HTTP session wrapper
  on top of `stado_http_request`. Cookie jar + default-header merging + base-URL
  resolution across tool calls. State persisted to disk (wasm instance freshness).

- **`plugins/examples/encode-zig`** (Zig, ~5 KB) — base64/base64url/hex/url/html
  encode+decode. Zig SDK proof: 700× smaller than the Go equivalent; documents the
  2 MiB arena constraint for non-Go plugin authors.

- **`plugins/examples/hash-id-rust`** (Rust, source-only) — hash identification,
  17 types, `#![no_std]`, `wasm32-unknown-unknown`. Rust SDK proof; builds when
  `rustup target add wasm32-unknown-unknown`.

### Fixes

- `stado --version` now shows a readable string across all build paths: `make build`
  → `v0.31.0-N-gabcdef-dirty` (git describe); `go install ...@tag` → `vX.Y.Z`;
  bare `go build` → Go's pseudo-version; fallback → `0.0.0-dev+<hash>`.

## v0.31.0 — net:http_request_private opt-in for lab IPs

### Plugin host imports

- **`net:http_request_private` capability + `tool.HostNetworkPolicy`
  interface.** Loosens the `stado_http_request` dial guard to permit
  RFC1918, loopback, link-local, and CGNAT destinations when the
  manifest declares the cap. Multicast, unspecified, IPv4/IPv6
  reserved, and documentation ranges remain refused — those are
  never valid HTTP destinations regardless of policy. The strict
  public-only path is still the default; opt-in is per-plugin and
  visible in the manifest. Implemented via type-assertion on
  `tool.Host`: hosts return true from `AllowPrivateNetwork()` to
  flip the dial guard. Tests cover cap-granted-loopback-allowed,
  cap-denied-loopback-blocked, and cap-granted-multicast-still-
  refused.

## v0.30.0 — net:http_request capability

### Plugin host imports

- **New `net:http_request` capability + `stado_http_request` host
  import.** Generic HTTP client (GET / POST / PUT / DELETE / PATCH /
  HEAD) with custom request headers and request body. Replaces the
  GET-only / markdown-converting shape of `stado_http_get` for
  plugins that need to drive REST APIs (auth headers, JSON bodies,
  status codes other than 200, response headers like
  `Set-Cookie`). Capability surface: `net:http_request` (broad,
  any public host) and `net:http_request:<hostname>` (narrow,
  per-host allowlist). Request/response bodies are base64 in/out
  for binary-safe JSON transport. Same private-network dial guard
  as `stado_http_get` (RFC1918 / loopback / link-local refused
  before TLS handshake); a future `net:http_request_private` cap
  will gate lab-IP access for plugins that need it. The new
  allowlist (`Host.NetReqHost`) is kept separate from the
  existing `Host.NetHost` (http_get's allowlist) so a manifest
  declaring only one method's hosts can't reach the other.

### Plugins

- **`exec:pty` capability + nine new host imports for persistent
  shell sessions.** `stado_pty_create / list / attach / detach /
  write / read / signal / resize / destroy` expose a runtime-shared
  PTY registry (`internal/plugins/runtime/pty/Manager`) that survives
  plugin instantiation freshness — sessions created in one tool call
  remain reachable from later calls. Per-session ring buffer (default
  64 KiB, configurable 4 KiB-4 MiB, terminal-scrollback semantics)
  captures output while detached so reattach replays the backlog.
  Single-attach-at-a-time per session with `force=true` recovery
  path for "previous attacher crashed without detaching". Reaper hook
  on `Runtime.Close` cleans up orphans.

- **New example plugin: `persistent-shell-0.1.0`.** Wraps the
  `exec:pty` host imports as nine plugin tools (`shell_create`,
  `shell_list`, `shell_attach`, `shell_detach`, `shell_write`,
  `shell_read`, `shell_signal`, `shell_resize`, `shell_destroy`).
  Replaces the fresh-process-per-call shape of `stado_exec_bash` for
  workflows that need interactive stdin/stdout across tool calls —
  driving `ssh` sessions, watching `nc` listeners, running
  `msfconsole` step-by-step, attaching to long-running TUIs.
  Base64-or-string data wire format supports both UTF-8 commands and
  raw bytes (Ctrl-C, terminal escape sequences). Minimal Go-→-wasm
  plugin modeled after `webfetch-cached`. See
  `plugins/examples/persistent-shell/README.md` for workflow
  patterns.

### Providers

- **ACP client — wrap external coding-agent CLIs as stado providers
  (phase A of EP-0032).** stado already speaks ACP as a server (Zed
  drives stado). This is the inverse: stado as ACP **client** wrapping
  an external CLI (`gemini --acp`, `opencode acp`, future
  zed-compatible variants). Configure via:

  ```toml
  [acp.providers.gemini-acp]
  binary = "gemini"
  args   = ["--acp"]
  ```

  Then `stado run --provider gemini-acp --prompt "..."` (or pin in
  `[defaults].provider`). The wrapped agent uses ITS OWN tools
  — phase A's deliberate scope. Phase B will add opt-in tool-host
  capability so wrapped agents can call stado's tool registry; phase
  C adds per-call hybrid. Audit boundary: stado records the
  conversation boundary, not the wrapped agent's internal tool calls
  — that's the trust boundary when handing off to a third-party
  agent. End-to-end tested with `gemini --acp` on a real Google
  account; `opencode acp` should work too (same canonical
  Zed-spec dialect). EP-0032 has the full design + decision log.

### CLI

- **Added `stado integrations`** — detect external coding-agent CLIs
  (claude / gemini / codex / opencode / zed / aider) installed on the
  current host and report what protocols each speaks (ACP / MCP).
  Scans PATH for known binaries + HOME / XDG_*_HOME for config dirs;
  per-agent version probe with a 2s sub-timeout so a hung CLI doesn't
  stall the whole sweep. `--json` emits structured output for piping
  to `jq`. Backed by a new `internal/integrations/` registry — adding
  a new known agent is a one-place change. Same registry surfaces
  detected agents under "Agent: <name>" rows in `stado doctor`.
  Foundation for the future ACP-client-driven dispatch features.
  Operator request.

- **Per-direction token budgets.** `[budget]` accepts the new
  `warn_input_tokens` / `hard_input_tokens` / `warn_output_tokens` /
  `hard_output_tokens` alongside the just-introduced combined
  `warn_tokens` / `hard_tokens`. Output tokens are 3–5× pricier
  than input on most paid providers — an output-only cap is the
  cheap way to constrain spend without restricting context;
  conversely an input-only cap bounds context-window growth without
  limiting generation length. Every cap fires independently;
  whichever crosses first aborts. Agent loop emits per-direction
  telemetry (`turn.tokens_in`, `turn.tokens_out`,
  `loop.cumulative_tokens_in/out`). TUI status pill prefers USD →
  combined → input → output. Operator request — refines the
  combined-cap addition from the same iteration.

- **Added `stado config providers`** — list the bundled provider
  catalogue (3 native + 7 OAI-compat cloud + 4 OAI-compat local)
  with per-provider API-key status (✓ set / ✗ unset) and the
  endpoint each preset points at. `stado config providers setup
  <name>` prints copy-pasteable setup steps; `--write` adds the
  `[inference.presets.<name>]` block to config.toml. Reuses the
  existing `internal/config/provider_registry.go` so adding a new
  provider is a one-place change. Operator request.

- **`[budget]` accepts token thresholds**: `warn_tokens` and
  `hard_tokens` mirror the existing `warn_usd` / `hard_usd`. The
  agent loop returns `ErrTokenCapExceeded` once cumulative
  input+output tokens cross `hard_tokens`; the TUI status pill
  shows `budget 12.3k/100k tok` while between warn and hard.
  Both pairs are independent — set USD-only, tokens-only, both,
  or neither. Useful for local-runner setups (Ollama / LM Studio
  / vLLM) where CostUSD is always zero and the meaningful budget
  is throughput, not dollars. Operator request.

- **Added `stado run --no-turn-limit`** — disables the max-turn
  cap so the agent loop runs until no tool calls remain or the
  context is cancelled. Useful for long-running multi-step tasks
  where the turn cap is the wrong control surface (prefer budget
  caps or context timeouts). Beats `--max-turns` when both set.
  Operator request.

### TUI

- **Animated terminal-tab title.** While the agent is busy
  (streaming or compacting) the OSC 0/2 window title cycles a
  braille spinner glyph: `⠋ stado` → `⠙ stado` → ...; idle
  resets to `stado`. Many emulators (kitty, alacritty, iTerm,
  Ghostty, Windows Terminal, GNOME Terminal) render the title in
  the tab strip, so users get visual "I'm working on it" feedback
  even when they've switched windows. Polled at 5fps via
  `tea.SetWindowTitle`; OSC sequences are deduped so we don't
  spam the terminal when nothing's changed. Operator request.

- **Wider command palette** so long descriptions don't wrap as the
  user moves through the list. Modal scales to 2/3 of screen,
  clamped [64, 110] cols (was [48, 80]); inline cap raised 88 →
  110. Operator-feedback report.

- **Drop category headers from the slash-command popup while
  filtering.** Categories ("Quick", "Session", "View") help
  orient when browsing the full list (empty query) but add
  clutter when the user is searching for a specific command.
  Now: filtered → flat list of matches; browsing → grouped by
  category. Operator-feedback report.

### Plugins

- **`stado plugin run --with-tool-host` now supports `exec:bash`
  plugins** under `sandbox.Detect()` (bwrap on Linux, sandbox-exec
  on macOS) — the same runner the agent loop uses. v0.26.0 refused
  unconditionally because no Runner was wired in; v0.27.0 narrows
  the refusal to: *manifest declares `exec:bash` AND no native
  sandbox is available* (NoneRunner). EP-0005 is preserved — we
  don't substitute the operator's CLI invocation for a real
  syscall/file-access filter, we just stop refusing cases where a
  real one IS available. Resolves EP-0028 D1.

### Fixes

- **Completed the Atomic Fedora boot fix — pass 3 (full audit).** Pass 2
  (v0.26.2) added a multi-probe regression test that surfaced the
  audit-key + sidecar wall. A static audit of every remaining strict
  from-`/` strict-walk caller surfaced 11 more boot-time HOME-rooted
  paths spread across 10 files: config.toml read (loaded only when a
  config exists, so the empty-namespace test missed it), session
  worktree mkdir + open, materialize tree mkdir + open + wipe,
  conversation worktree open, tasks store mkdir + open, theme TOML read,
  model recents mkdir + open, instructions walk-up read,
  binext cache dir mkdir + open, traceparent write + read, session-fork
  worktree mkdir, plugin install destination mkdir + open. Migrated all
  to the trust-anchor variants (same threat model as v0.26.1/v0.26.2).
  Extended the regression test with `config show` (exercises the
  load-existing-config path that fired on real users with config.toml
  present) and added `XDG_CACHE_HOME` to the namespace setenv block so
  binext probes resolve the right anchor. The test now runs 5 probes
  end-to-end and verifies all reach the application logic past every
  known boot-time MkdirAll/OpenRoot/Read wall.

- **Completed the Atomic Fedora boot fix — pass 2.** v0.26.1's
  `hack/test-on-fedora-atomic.sh` test harness only exercised
  `stado config-path`, which leaves three more boot-time surfaces
  unchecked. Fanning the test out to `doctor --no-local --json`,
  `session list`, and `audit verify` surfaced two more strict
  from-`/` walks: `internal/audit/key.go` (the audit signing key
  load+create path, ~`.config/stado/audit/...`) and
  `internal/state/git/sidecar.go` (the sidecar bare repo init +
  alternates dir, under `~/.local/state/stado/sessions/`). Both
  trip on Atomic's `/home → /var/home` symlink whenever a normal
  user runs anything that signs a commit or enumerates sessions —
  i.e. nearly every real workflow. Migrated to the trust-anchor
  variants (`ReadRegularFileUnderUserConfigLimited` /
  `OpenRootUnderUserConfig` / `MkdirAllUnderUserConfig`); same
  threat model as v0.26.1.

- **Reworked `hack/test-on-fedora-atomic.sh` as a multi-probe
  regression suite.** The script now runs four boot-touching
  probes in the bwrap namespace and reports per-probe PASS/REGRESSION,
  so partial regressions surface specifically. Adding new probes
  is one line in the `PROBES=()` array. `make fedora-atomic-test`
  is the entry point.

- **Completed the Atomic Fedora `/home → /var/home` boot fix.** v0.26.0
  migrated three call sites (`config dir`, `audit key dir`, `worktree
  root`) from the strict from-`/` `MkdirAllNoSymlink` to the
  trust-anchor-aware `MkdirAllUnderUserConfig`. Four more call sites in
  `internal/config/config.go` still walked from `/` and tripped the
  same wall: the system-prompt-template ensure (`MkdirAllNoSymlink` →
  `MkdirAllUnderUserConfig`), the system-prompt-template root opener
  (`OpenRootNoSymlink` → `OpenRootUnderUserConfig`), and two read-paths
  for the system-prompt template (a new
  `ReadRegularFileUnderUserConfigLimited` helper, mirroring the
  existing `Mkdir`/`OpenRoot` wrappers). On Atomic the v0.26.0 binary
  booted via `--version` (which prints before any FS work) but failed
  on `stado config-path` and any normal startup that triggered
  `config.Load()`. Added `hack/test-on-fedora-atomic.sh` — a
  `bwrap`-based regression test that simulates the `/home → /var/home`
  symlink layout — plus a `make fedora-atomic-test` target. Threat
  model unchanged: symlinks ABOVE the trust anchor are tolerated as
  operator-supplied OS layout, symlinks UNDER the anchor are still
  rejected by the strict `OpenRootNoSymlinkUnder` walk; see EP-0028.

### CLI

- **Added `stado run --quiet`.** Suppresses `▸ tool(args)` preview lines
  on stdout in non-JSON mode. Tools still execute and still commit to
  the audit log; only the inline preview is elided. Pairs with `--json`
  for scripted use: `--json` for structured event-per-line output, or
  `--quiet` for plain text-only stdout. Dogfood-note item from the
  external workflow integration: `stado run --tools` interleaved
  agent text with tool-call previews and INFO log lines, making it
  hard to pipe to `jq` or post-process.
- **Updated `stado run --help` body** to explicitly call out `--json`
  (preferred for scripted use) and `--quiet` (suppress tool-call
  previews). Same dogfood note: `--json` was discoverable via flag
  description but not the command's `Long` description body, so users
  who only read the body missed the canonical scripted-parse mode.
- **`stado doctor` auto-skips local-runner probe when `[defaults].provider`
  pins a remote provider.** Probing four `localhost:*` ports each with
  a 1s TCP timeout adds ~4s on machines without any local runners; the
  probe is informational, not a blocker, so when the user has explicitly
  pinned `provider = "anthropic"` (or any other remote provider, including
  an OAI-compat preset whose endpoint resolves non-local), `doctor` now
  skips the probe and prints a `Local probe: skipped (...)` annotation
  row instead. Explicit `--no-local` still works as before; `provider = ""`
  (auto-detect mode) still triggers the probe. Dogfood-note item.

- **Added `stado --version`.** The `stado version` subcommand has long
  printed `collectBuildInfo().Version`; cobra's standard `--version`
  global flag is now wired to the same source so both surfaces agree.
- **Added `stado plugin run --workdir <path>`.** Lets the operator
  override the plugin's `host.Workdir` (the path that `fs:read:.` /
  `fs:write:.` capabilities and relative file paths resolve against).
  Default unchanged: install dir, for backward compatibility. Pass
  `--workdir=$PWD` when the plugin is meant to read files from the
  operator's repo (the common case for project-specific plugins).
  EP-0027 documents the rationale.
- **Added `stado plugin run --with-tool-host`.** Wires `host.ToolHost`
  so plugins that import bundled tools (`stado_http_get`,
  `stado_fs_tool_*`, `stado_lsp_*`, `stado_search_*`) can be exercised
  end-to-end from the CLI. Without this, `tool_imports.go` returned
  the documented "plugin host has no tool runtime context" error and
  net/exec/lsp paths were only reachable via `stado run`. Refuses
  plugins that declare `exec:bash` because the `sandbox.Runner` the
  agent loop normally provides is not available here — those need to
  run via `stado run` (EP-0005 forbids substituting human approval
  for runtime policy). EP-0028 walks through the design.
- **Added `stado plugin gc [--keep N] [--apply]`.** Sweeps older
  installed plugin versions per (signer fingerprint, manifest name)
  group, keeping the `--keep` newest (default 1). Dry-run by default;
  `--apply` actually deletes. Trust-store entries and rollback pins
  are deliberately untouched, so a freshly-deleted older version
  still cannot be reinstalled. Solves the "`plugin installed` shows
  `htb-cve-lookup-0.1.0`, `-0.2.0`, `-0.3.0` after enough iteration"
  authoring-loop pain.
- **Added `stado plugin doctor <id>`.** Inspects an installed
  plugin's manifest, classifies each declared capability, and prints
  a per-surface compatibility table with the exact `plugin run`
  flag combination needed (or "use the TUI / `stado run`" when the
  plugin requires the full agent loop). Closes the
  "`stado_http_get returned -1` — now what?" first-time-author
  loop: doctor explains which knob to flip without making the
  operator read the source.
- **`--provider` and `--model` flags on every command.** Pass
  `stado --provider ollama-cloud --model kimi-k2.6` (or any subcommand)
  to override `[defaults].provider` / `[defaults].model` for one
  invocation without editing config.toml or pre-exporting
  `STADO_DEFAULTS_*`. Shipped as persistent root flags so `stado run`,
  `stado` (TUI), and other subcommands all honour them.

### Plugins

- **`internal/workdirpath` exports `LooksLikeRepoRoot`,
  `FindRepoRoot`, `FindRepoRootOrEmpty`** as the single source of
  truth for "what counts as a git working tree". The predicate now
  rejects empty `.git/` directories (which previously fooled the
  walker into returning the wrong repo root); every git tree must
  have a HEAD file or a gitfile pointer to be accepted. The 6 inline
  walkers across `cmd/stado/`, `internal/runtime/`, and
  `internal/memory/` now delegate to the shared helper. EP-0027.
- **New bundled example: `plugins/examples/webfetch-cached/`.**
  Drop-in replacement for the bundled `webfetch` tool that adds a
  SHA-256-keyed disk cache. Demonstrates three v0.26.0 plugin-surface
  features in one ~140-line plugin: wrapping a bundled-tool host
  import (`stado_http_get` via `--with-tool-host`), workdir-rooted
  fs capabilities (`fs:read:.cache/stado-webfetch` via `--workdir`),
  and `[tools].overrides` for transparent bundled-tool replacement.
  Solves the "Anthropic WebFetch hard-codes a 15-min TTL" friction
  documented in the round-1 dogfood notes.
- **New `cfg:*` capability vocabulary — first concrete capability
  `cfg:state_dir`.** Introduces a read-only configuration-introspection
  surface for plugins. The `cfg:state_dir` capability gates the
  `stado_cfg_state_dir(buf, cap) → int32` host import, which writes
  the operator's stado state-dir path (`$XDG_DATA_HOME/stado/` or
  fallback) into the caller's buffer. Unblocks the lean-core
  migration of operator-tooling commands (`plugin doctor`,
  `plugin gc`, future `plugin info`) from `cmd/stado/` to bundled
  plugins under `plugins/default/` — those tools all need to learn
  the install dir at `<state-dir>/plugins/`. EP-0029 documents the
  capability vocabulary; future `cfg:config_dir`, `cfg:worktree_dir`,
  etc. follow the same per-field opt-in pattern (no globs).
- **`fs:read:cfg:state_dir/...` path-templating in fs caps.**
  Direct extension of the `cfg:*` vocabulary: manifest fs caps
  can now reference cfg values inline as
  `fs:read:cfg:state_dir/plugins`, `fs:write:cfg:state_dir/scratch`,
  etc. Resolution is at-check time against `host.StateDir`. The
  matching `cfg:state_dir` cap MUST also be declared — missing
  cap, empty value, or unknown cfg name all fail-closed (silently
  filter to no-match → access denied). Unblocks portable plugin
  manifests for any operator-tooling that reads under the state
  dir; before this, plugins had to either hardcode an absolute
  path per-operator or shift the resolution onto the `--workdir`
  invocation flag. EP-0031 walks through the templating shape
  and fail-closed semantics. New bundled examples
  (`plugins/examples/state-dir-info/`,
  `plugins/examples/webfetch-cached/`) demonstrate the cfg:* +
  workdir-rooted patterns.

### Inference

- **Custom OAI-compat presets can now declare an API-key env var.**
  `[inference.presets.<name>].api_key_env = "FOO_API_KEY"` plumbs the
  named env var through the OAI-compat client. When unset, custom
  preset names fall back to `STADO_PRESET_<UPPER>_API_KEY` (hyphens
  normalized to underscores). Previously only the hardcoded list of
  builtin preset names (litellm, groq, etc.) could pick up a key, so
  user-defined presets like `ollama-cloud` would 401.
- **`ollama-cloud` is now a builtin preset.** `[defaults].provider =
  "ollama-cloud"` resolves to `https://ollama.com/v1` with
  `OLLAMA_CLOUD_API_KEY` as the default credential env. No more
  litellm-aliasing workaround required.
- **Anthropic auto-raises `max_tokens` when thinking is enabled.**
  Anthropic enforces `max_tokens > thinking.budget_tokens`. The default
  thinking budget (16K) used to exceed the default `max_tokens` (8K)
  and surface as a 400 from the provider. Stado now widens
  `max_tokens` to `budget + 1024` whenever the caller's ceiling is
  smaller, while never lowering an explicit larger ceiling.

### Fixes

- **`stado` no longer fails to boot when `/home` is a symlink.**
  `MkdirAllNoSymlink` walks from `/` and rejects any symlink in any
  path component — too strict for HOME-rooted system paths on
  Fedora Atomic / Silverblue (`/home` → `/var/home`) and similar
  setups. New helpers `workdirpath.MkdirAllUnderUserConfig` /
  `OpenRootUnderUserConfig` anchor the no-symlink walk at the
  operator's `XDG_*_HOME` / `HOME` environment, so OS-level
  symlinks ABOVE the trust anchor are accepted while symlinks
  WITHIN user space are still rejected. 13 HOME-rooted call sites
  (config dir, state dir, worktree dir, audit keys, memory store,
  plugin install / state files) migrated to the new helpers; the
  strict `MkdirAllNoSymlink` stays for genuinely-untrusted callers
  (in-repo sandbox writes from inside plugin host imports). EP-0028.
- **Empty `/tmp/.git/` no longer fools session GC + lesson document
  paths.** A stray empty `.git/` directory in any parent of CWD was
  enough to make `findRepoRoot` (and its 5 cousins) return the wrong
  path, silently re-pinning sessions and lesson document target dirs
  to that bogus parent. Production code path observed: a user who
  ran `stado run --prompt …` from `/tmp/myproject` (no real `.git`)
  would get sessions pinned to `/tmp` if anything else had previously
  created `/tmp/.git/`. Test impact: this fixed
  `TestSessionGC_ApplyActuallyDeletes` and four `TestLearningCLI_*`
  tests that had been failing on `main` in any CI/dev environment
  with `/tmp/.git/` pollution.
- **Warn when a stale `system-prompt.md` template drops
  `{{ .ProjectInstructions }}`.** When `AGENTS.md` / `CLAUDE.md` is
  loaded but the active template doesn't reference the
  `ProjectInstructions` field, stado now prints a stderr advisory
  pointing at both files so the user can re-add the block (or delete
  the template to regenerate the default). Without this, project rules
  could silently fail to reach the model on installs predating the
  template's `ProjectInstructions` hook.
- **Validated pinned plugin pubkeys.** Manifest verification now re-derives
  the stored signer pubkey fingerprint before trusting a pinned entry, so
  malformed trust-store records cannot authorize the wrong key.
- **Validated OpenAI-compatible endpoints.** Custom OAI-compatible endpoints
  now must use HTTP(S), include a host, and avoid URL-embedded credentials.
- **Hardened prompt and symbol reads.** Project instruction files, project
  skill files, and TUI symbol scans now read regular files through the
  no-symlink opener so repo-controlled symlinks cannot redirect prompt or
  symbol discovery.
- **Capped prompt and template reads.** Project instruction files, project
  skills, system prompt templates, theme files, TUI template overlays, symbol
  scans, and audit key loads now reject oversized regular files before
  parsing.
- **Capped config loads.** `config.toml` now loads through a bounded
  no-symlink reader instead of the koanf file provider, rejecting oversized or
  symlinked user config files before TOML parsing.
- **Capped bundled binary cache verification.** Bundled tool cache hits now
  require an exact byte-size match and hash through a bounded reader before
  reusing an existing executable.
- **Capped plugin install copies.** Plugin installs now reject oversized
  package files during rooted directory copies and remove partial destinations
  if a source grows past the copy ceiling.
- **Bounded plugin install package walks.** Plugin installs now stream package
  directory entries in batches and reject packages that exceed entry-count or
  nesting-depth limits.
- **Bounded installed-plugin listing.** CLI, headless, and TUI plugin listing
  now stream installed-plugin directory entries in batches and reject oversized
  state directories.
- **Capped sidecar tree blob writes.** Session tree snapshots now reject
  oversized worktree files and detect regular files that change size while
  being streamed into sidecar git objects.
- **Bounded sidecar tree snapshot walks.** Session tree snapshots now stream
  directory entries in batches and reject worktrees that exceed entry-count or
  nesting-depth limits.
- **Capped self-update fallback copies.** Cross-device self-update installs
  now reject oversized replacement binaries before streaming through the
  atomic copy fallback.
- **Capped sidecar tree materialization writes.** Materialization now rejects
  oversized regular blobs before writing them back to a worktree and removes
  partial files if a blob stream exceeds the write ceiling.
- **Bounded sidecar tree materialization walks.** Materialization now rejects
  sidecar trees that exceed entry-count or nesting-depth limits before the
  restore path can grow unbounded worktree state.
- **Bounded sidecar materialization cleanup walks.** Replacing or zero-tree
  materialization now streams cleanup discovery in batches and fails before
  deletion if stale worktree traversal exceeds entry-count or depth limits.
- **Bounded worktree session listing.** CLI and TUI session enumeration now
  stream worktree-root entries in batches and reject oversized session state
  directories.
- **Bounded grep tool walks.** The in-process grep tool now streams rooted
  directory traversal in batches and rejects oversized walk depth or entry
  counts before scanning files.
- **Bounded skill discovery.** Project skill loading now streams `.stado/skills`
  directory entries through rooted no-symlink handles and rejects oversized
  skill directories before parsing files.
- **Bounded read-context package discovery.** The `read_with_context` tool now
  streams local Go package directory entries in batches and skips import
  packages that exceed the package-entry cap.
- **Bounded TUI repo scans.** TUI document and symbol pickers now stream repo
  traversal in batches and stop on entry-count or nesting-depth limits.
- **Bounded TUI file picker scans.** The `@` file picker now streams repo file
  discovery in batches and stops on traversal entry-count or depth limits.
- **Bounded TUI template discovery.** Bundled and overlay template discovery
  now reads template directories in batches and rejects oversized template
  entry sets before parsing.
- **Capped model-list decoding.** Local provider detection and
  OpenAI-compatible capability probes now reject oversized model-list
  responses before decoding JSON.
- **Capped self-update release metadata.** Self-update now rejects oversized
  GitHub release API responses before decoding release JSON.
- **Bounded glob tool expansion.** The in-process glob tool now walks through
  rooted bounded traversal, skips symlink directory traversal, and stores only
  the output-budgeted matches while counting total matches.
- **Capped command output capture.** Hooks, bash, ripgrep, and ast-grep now
  capture child-process stdout and stderr through bounded buffers instead of
  unbounded in-memory buffers.
- **Capped command probe captures.** TUI git status checks and Linux pasta
  capability probes now avoid unbounded `Output`/`CombinedOutput` captures.
- **Capped LSP frame reads.** LSP message framing now rejects oversized header
  lines, header blocks, and message bodies before allocation.
- **Capped tool-call inputs.** Providers and the tool executor now reject
  oversized function-call argument payloads before accumulating or replaying
  them into tool execution.
- **Reduced streamed reasoning memory growth.** Anthropic streaming now emits
  thinking and signature deltas without duplicating them in provider-local
  buffers.
- **Hardened TUI stream errors.** Provider stream errors now put the TUI into
  an error state instead of letting partial assistant turns complete normally.
- **Capped direct tool dispatch inputs.** Registry, MCP-server, and plugin
  adapter paths now share the tool-call input ceiling before dispatch.
- **Capped MCP bridge and plugin-run payloads.** Remote MCP tool text is now
  output-budgeted before entering model context, and one-shot plugin runs
  reject oversized JSON arguments before starting a wasm runtime.
- **Capped plugin tool results.** External plugin tool content and tool-side
  errors now share a model-context output budget after the wasm ABI call.
- **Capped streamed assistant output.** Runtime and TUI streams now reject
  oversized assistant text or thinking deltas before unbounded accumulation.
- **Capped subagent and LSP tool results.** `spawn_agent` results and LSP
  lookup output now share explicit model-context budgets before being returned
  to the parent model.
- **Capped task picker input buffers.** Task search, title, and body editing
  now stop at bounded byte limits before oversized pasted input can grow TUI
  memory.
- **Capped TUI prompt input.** The main prompt editor now enforces a byte
  ceiling before oversized pasted input can grow draft and history buffers.
- **Capped memory append logs.** Persistent memory payloads, events, and log
  files now enforce byte ceilings before append or replay.
- **Capped TUI picker inputs.** Agent, model, session, theme, and slash-command
  pickers now bound pasted query and rename input before fuzzy matching.
- **Capped conversation append logs.** Conversation JSONL records and total log
  size are checked before append, with final symlink/non-regular files rejected.
- **Hardened plugin state reads.** Plugin trust and revocation state files now
  reject final symlinks before opening cached JSON.
- **Blocked private webfetch targets.** The `webfetch` tool now denies
  loopback, private, link-local, and reserved IP targets at dial time.
- **Filtered external LSP locations.** LSP definition/reference results now
  drop paths outside the active workdir before rendering tool output.
- **Validated plugin run IDs.** TUI `/plugin:<id>` invocations and tool
  override plugin references now reject traversal before resolving plugin
  directories.
- **Validated background plugin IDs.** TUI background-plugin config entries now
  use the installed-plugin path guard before manifest loading.
- **Preserved plugin rollback pins.** Re-trusting an existing plugin signer now
  keeps its last verified version so inline `plugin install --signer` cannot
  reset rollback protection.
- **Made plugin TOFU pinning atomic.** Inline plugin signer pins are now saved
  only after the signer matches and verifies the manifest, avoiding trust-store
  pollution on failed installs.
- **Streamed task store JSON I/O.** Task store loading and saving now decode
  and encode through the store byte ceiling instead of staging the whole JSON
  document in memory.
- **Streamed git tree materialization.** Session tree materialization now
  streams regular blob contents to destination files, caps symlink blob reads,
  and bounds encoded commit bytes used for SSH signing.
- **Rooted read-context module discovery.** The `read_with_context` tool now
  reads target/import files through workdir-rooted handles and stops Go module
  probing at the tool workdir boundary.
- **Hardened audit key loads.** Existing audit signing keys are now read
  through the no-symlink opener, matching the existing protected key creation
  path.
- **Hardened default system prompt reads.** Auto-managed system prompt
  templates now reject symlinked default files before validation or legacy
  upgrades.
- **Hardened tree blob reads.** Session sidecar tree snapshots now open
  regular file blobs through the no-symlink opener, while preserving symlink
  entries as symlink blobs.
- **Hardened self-update source reads.** Self-update archive extraction and
  cross-device copy fallback now reject symlinked source paths before reading
  release payloads or replacement binaries.
- **Capped self-update downloads.** Self-update checksum manifests, minisig
  signatures, release archives, and extracted binaries now reject oversized
  inputs before unbounded reads or writes.
- **Hardened sandbox proxy CONNECT handling.** The sandbox network proxy now
  rejects malformed CONNECT targets before dialing, applies request-header and
  dial timeouts, and keeps user-controlled text out of status lines.
- **Hardened plugin signing inputs.** Plugin digest and signing commands now
  reject symlinked key, manifest, and WASM source paths before hashing or
  signing plugin artifacts.
- **Capped plugin signing inputs.** Plugin signing now rejects oversized
  manifests and WASM files before hashing or rewriting signed metadata, and
  `plugin digest` applies the same WASM size limit as package verification.
- **Streamed minisign artifact signing.** Release artifact signing now hashes
  files incrementally before writing `.minisig` sidecars instead of reading
  whole artifacts into memory.
- **Capped bundled-binary fetch inputs.** The release helper now uses explicit
  HTTP timeouts and rejects oversized checksum sidecars, release metadata,
  archives, and extracted tool binaries.
- **Bounded file-read tool memory.** The `read` tool now streams full-file and
  ranged reads into the existing output budget instead of loading the whole
  file before truncating.
- **Bounded read-context inputs.** The `read_with_context` tool now enforces a
  hard per-file read ceiling and caps Go import-scan and `go.mod` reads before
  parsing.
- **Capped LSP document opens.** Definition, references, hover, and document
  symbol tools now reject oversized source files before sending document text
  to a language server.
- **Capped edit-tool file loads.** Search/replace edits now reject oversized
  source files and replacement results before loading or writing unbounded
  content.
- **Streamed subagent adoption copies.** Worker adoption now validates child
  file inputs before removing parent targets and streams regular files through
  a capped atomic copy.
- **Capped plugin runtime FS I/O.** Host `fs_read` and `fs_write` now enforce
  path and payload ceilings and read allowed files through bounded
  regular-file reads.
- **Capped plugin host import memory reads.** Runtime host imports now reject
  oversized plugin-controlled strings and byte payloads before copying them
  out of Wasm memory.
- **Capped runtime state reads.** Session metadata and raw conversation log
  reads now verify regular files and enforce byte ceilings before loading
  worktree state into memory.
- **Capped remaining rooted state reads.** Repo pin, config, sidecar
  alternates, TUI model state, git HEAD, and grep reads now use bounded
  regular-file opens instead of direct whole-file root reads.
- **Hardened regular-file open races.** Shared no-symlink regular-file opens
  and plugin package copy reads now verify the opened file still matches the
  pre-open `Lstat` result.
- **Hardened webfetch redirects and reads.** Webfetch now rejects redirects
  that leave the original host and caps raw response reads before markdown
  conversion so plugin host grants cannot be bypassed via cross-host redirects.
- **Capped online plugin metadata responses.** CRL fetches and Rekor online
  checks now reject oversized success bodies before parsing them.
- **Capped plugin package reads.** Plugin manifest, signature, author pubkey,
  and WASM package reads now enforce size limits and verify the opened file
  still matches the pre-open `Lstat` result.
- **Centralized rooted directory reopening.** Plugin capability file access,
  builtin grep traversal, subagent adoption, plugin scaffolding, branch-status
  rendering, release helper writes, and shared workdir file helpers now use the
  no-symlink root opener; direct `os.OpenRoot` use is isolated to that primitive.
- **Hardened explicit output and learning roots.** CLI file-output helpers,
  minisign artifact signing, learning document writes, and learning repo-pin
  reads now reject symlinked parent/root directories before reading or writing.
- **Hardened worktree metadata roots.** Session memory toggles, user-repo pins,
  descriptions, pid files, conversation logs, and traceparent files now reject
  symlinked worktree roots before reading or writing session metadata.
- **Hardened rooted state/cache opens.** Task, memory, plugin-state, model
  state, config, sidecar, bundled-tool cache, plugin scaffolding/install, and
  tree-materialization roots now reopen directories with no-symlink checks.
- **Hardened release helper writes.** The bundled-binary fetch helper now
  creates generated source and asset parent directories through rooted
  no-symlink directory creation.
- **Hardened plugin package verification reads.** Plugin manifest,
  signature, author-pubkey sidecar, and WASM digest reads now reject
  symlinked plugin directory components and package files before
  verification.
- **Hardened destructive directory cleanup.** Session and agent worktree
  deletion, TUI session deletion, failed plugin-install cleanup, and
  zero-tree materialization wipes now reject symlinked directory components
  before removing recursive paths.
- **Rooted memory log reads and appends.** Approved-memory storage now opens
  its append log through `os.Root` scoped to the memory-store directory,
  rejecting symlink escapes for read and append operations.
- **Rooted conversation log access.** Session conversation reads, appends,
  rewrites, and raw-log hashing now stay confined to the session worktree's
  `.stado` directory, rejecting conversation-file and `.stado` symlink escapes.
- **Rooted session metadata files.** Session descriptions and user-repo pins
  now read and write through the session worktree root across runtime, memory,
  and learning paths, rejecting `.stado` and metadata-file symlink escapes.
- **Rooted session pid metadata.** `.stado-pid` reads and writes now stay
  scoped to the session worktree root, preventing pid-file symlink escapes.
- **Rooted traceparent metadata.** Fork traceparent files now read and write
  through the child worktree root, rejecting traceparent symlink escapes.
- **Rooted session memory opt-out metadata.** The per-session memory-disabled
  marker now reads, writes, and removes through the session root, rejecting
  `.stado` symlink escapes.
- **Rooted raw conversation exports.** `stado session export --format jsonl`
  now reads raw logs through the runtime conversation root instead of a direct
  worktree path read.
- **Validated TUI session metadata actions.** Session rename and delete actions
  now use the shared session ID validator, preventing special local IDs from
  writing metadata outside an actual session worktree.
- **Rooted learning document writes.** `stado learning document` now writes
  Markdown notes through rooted `.learnings` handles, rejecting symlink escapes
  before documenting and rejecting a lesson.
- **Rooted session tree materialization.** Fork/revert materialization now
  writes files and directories through a destination root and replaces stale
  destination symlinks instead of following them. Destructive prune/wipe
  cleanup now removes stale paths through the same destination root.
- **Hardened plugin install copies.** Plugin installs now copy package
  contents through rooted source/destination handles, reject destination
  symlinks, and re-check the installed manifest/signature/WASM digest after
  copy so package swaps cannot land unverified bytes.
- **Hardened self-update extraction.** Self-update now writes the release
  binary into its already-open temp file instead of reopening by path and
  rejects tar/zip entries named like the binary unless they are regular files.
- **Hardened plugin state writes.** Plugin CRL and trust-store saves now use
  rooted, exclusive random temp files and reject non-regular state files so
  pre-created temp symlinks cannot redirect writes outside the state directory.
- **Hardened bundled tool cache extraction.** Bundled binary extraction now
  rejects path-like tool names, writes through rooted random temp files, and
  replaces cache symlinks instead of treating them as valid cache hits.
- **Hardened conversation seeding.** Fresh child-session conversation logs now
  seed through rooted, exclusive random temp files so pre-created predictable
  temp symlinks cannot redirect the rewrite.
- **Hardened default prompt template writes.** The automatically managed
  default `system-prompt.md` now creates and upgrades through rooted file
  handles and avoids rewriting legacy defaults through symlinks.
- **Hardened sidecar alternates writes.** Sidecar Git alternates metadata now
  updates through rooted, exclusive random temp files and replaces alternates
  symlinks instead of following them.
- **Hardened session metadata writes.** Session descriptions, repo pins, and
  pid markers now replace through rooted random temp files and reject final
  metadata symlinks instead of following them.
- **Hardened session memory opt-out writes.** The per-session
  memory-disabled marker now writes through rooted random temp files and
  rejects marker symlinks instead of following them.
- **Hardened traceparent writes.** Fork traceparent metadata now writes
  through rooted random temp files and rejects traceparent symlinks instead
  of following them.
- **Hardened config defaults writes.** TUI model, theme, and thinking-display
  preference updates now reject config-file symlinks and save through rooted
  random temp files.
- **Hardened worktree rooted writes.** File tools, plugin filesystem writes,
  and subagent adoption now save through rooted random temp files while
  rejecting final symlink/non-regular write targets.
- **Hardened model picker state writes.** Recent and favorite model state now
  reads regular files only and saves through rooted random temp files instead
  of following state-file symlinks.
- **Hardened private key creation.** Audit signing keys and plugin signing
  seeds now create with exclusive rooted file handles and refuse to overwrite
  existing or symlinked key paths.
- **Hardened config init writes.** `stado config init` now creates config
  templates through rooted exclusive handles and refuses symlinked or
  non-regular config paths, including when `--force` is set.
- **Hardened plugin signing artifact writes.** `stado plugin sign` now writes
  manifests, signatures, and author pubkey sidecars through rooted random temp
  files and rejects final symlink/non-regular targets.
- **Hardened session export writes.** `stado session export -o` now saves
  through rooted random temp files and refuses final symlink/non-regular
  output paths.
- **Hardened plugin scaffold writes.** `stado plugin init --force` now refuses
  symlinked scaffold directories/files and writes generated scripts with the
  intended executable mode through rooted atomic replacement.
- **Hardened minisign sidecar writes.** Release `.minisig` files now save
  through rooted random temp files and refuse final symlink/non-regular
  sidecar paths.
- **Hardened self-update replacement writes.** Self-update now rejects
  symlinked binary replacement paths and uses rooted temp+rename writes for
  copy fallback installation.
- **Hardened release-helper writes.** Bundled release asset refreshes now save
  fetched binaries, generated embed files, and manifests through rooted atomic
  writes instead of following output-path symlinks.
- **Hardened CLI output directory creation.** Session exports, plugin
  scaffolding, and plugin installs now reject symlinked output parent
  directories before creating missing write targets.
- **Hardened state/config directory creation.** Config defaults, default
  prompts, plugin state, task/memory stores, audit keys, model picker state,
  bundled tool caches, and plugin filesystem writes now reject symlinked
  parent directories before creating missing write roots.
- **Hardened session/worktree directory creation.** Session worktree roots,
  sidecar repositories, materialized trees, `.stado` metadata directories,
  learning documents, and subagent adoption paths now reject symlinked
  directory components before creating missing write targets.

## v0.25.7 — 2026-04-26

### Infra

- **Updated SLSA provenance generation to v2.1.0.** Release provenance now
  uses the newer pinned generator workflow to avoid the Node 20 deprecation
  path in future tagged releases.

## v0.25.6 — 2026-04-26

### Fixes

- **Rooted task-store reads and writes.** Shared task data now opens,
  temp-writes, and renames through `os.Root` scoped to the task-store
  directory, rejecting symlink escapes for the persisted store file.

## v0.25.5 — 2026-04-26

### Fixes

- **Rooted the shared task-store lock file.** The cross-process task
  lock now opens, stats, and removes its lock file through `os.Root`
  scoped to the task-store directory, clearing the remaining gosec path
  warning without weakening the lock.

## v0.25.4 — 2026-04-25

### Infra

- **Disabled cosign v3's signing-config path for legacy checksum
  signatures.** Release checksum signing now keeps the existing
  `checksums.txt.sig` and `checksums.txt.cert` artifacts while using
  cosign v3.

## v0.25.3 — 2026-04-25

### Infra

- **Kept release checksum signing on the documented sig/cert artifacts.**
  Cosign v3 signing now disables the new bundle format so releases continue
  publishing `checksums.txt.sig` and `checksums.txt.cert` for `install.sh`
  and documented manual verification.

## v0.25.2 — 2026-04-25

### Infra

- **Updated the release cosign verifier to v3.** The release workflow now
  installs `sigstore/cosign-installer@v4.1.1` and `cosign v3.0.6`, matching
  `goreleaser/goreleaser-action@v7`'s checksum verification requirements.

## v0.25.1 — 2026-04-25

### Infra

- **Updated the GoReleaser GitHub Action to Node 24.** Release builds now
  use `goreleaser/goreleaser-action@v7`, removing the Node 20 deprecation
  annotation from tagged release runs.

## v0.25.0 — 2026-04-25

### TUI

- **Added a shared task manager.** `/tasks` and `Ctrl+X K` open a
  persistent task browser/editor, while `/tasks add <title>` creates an
  open task directly from the input.

### Agent

- **Added the `tasks` tool.** Tool-enabled agents, `stado run --tools`,
  headless/ACP, and MCP server clients can store, list, read, edit, and
  delete shared tasks outside the repo worktree.

### Infra

- **Cleared the gosec backlog.** Runtime state, config, cache, and
  session metadata now use tighter file modes, workdir reads prefer
  rooted helpers where practical, and intentional dynamic path/exec
  cases carry narrow `#nosec` justifications.
- **Hardened shared task state.** The task store now uses cross-process
  locking, validates persisted task files on load, enforces size/count
  limits, and caps model-facing task output before it enters context.

### Fixes

- **Rejected malformed tree entries during session materialization.**
  Session fork/revert materialization now refuses raw Git tree entry
  names that would escape the destination worktree before joining them
  to filesystem paths.
- **Tightened session and Git ref validation.** Session IDs from refs
  and worktree listings are filtered before filesystem probes, turn-ref
  lookups parse numeric `turns/<N>` targets, raw commit lookups require
  valid 40-hex hashes, and `session land` rejects invalid branch names.
- **Normalized copied file permissions in trusted state transitions.**
  Plugin installs now strip group/world permissions from package files,
  and subagent adoption maps child file modes to git-style `0644` or
  `0755` instead of preserving overly broad source modes.

## v0.24.0 — 2026-04-25

### Agent

- **Added a first read-only `spawn_agent` slice.** The parent model can
  fork a bounded child session for read-only repo investigation; the
  child gets only non-mutating tools, cannot recursively spawn, and
  returns a structured result with the child session/worktree.
- **Bounded spawned agents by wall-clock timeout.** `spawn_agent` now
  accepts `timeout_seconds` with default/cap behavior and returns a
  structured timeout result when the child exceeds its budget.
- **Surfaced spawned children in the TUI.** Successful `spawn_agent`
  tool results now add a system notice with child status, session ID,
  worktree, and attach command.
- **Added headless subagent lifecycle notifications.** Headless clients
  now receive `session.update { kind: "subagent", phase: "started" |
  "finished", ... }` when `spawn_agent` creates and completes a child.
- **Pinned parent cancellation for spawned agents.** Runtime and
  headless tests now assert that cancelling the parent operation cancels
  the running child and emits a finished/error subagent event.
- **Documented the write-capable subagent contract.** EP-13 now defines
  worker-mode ownership scopes, write-scope enforcement, conflict
  checks, and explicit adoption semantics.
- **Pinned subagent write-scope validation.** `spawn_agent` request
  decoding now normalizes and rejects unsafe future `write_scope`
  entries before worker mode is exposed.
- **Added a scoped write guard for subagents.** Mutating file tools now
  honor an optional host-level write-path guard, and EP-13's worker host
  has a tested `ScopedWriteHost` implementation.
- **Built the internal worker subagent path.** Runtime tests now exercise
  scoped `workspace_write` children with read/search plus `write`/`edit`,
  while the public `spawn_agent` decoder still rejects worker mode.
- **Reported internal worker outputs.** Worker subagent results now
  include changed files from the child tree diff and collected
  `scope_violations` for rejected scoped writes.
- **Added subagent adoption planning.** Runtime can now dry-run child
  adoption by comparing parent and child changes against the fork tree
  and reporting conflicts without mutating either session.
- **Added internal subagent adoption apply.** Non-conflicting child
  changes can now be copied into the parent worktree and recorded as
  `subagent_adopt` trace/tree commits by an internal runtime helper.
- **Exposed explicit session adoption.** `stado session adopt` now
  dry-runs child-to-parent adoption by default, supports `--apply`, and
  reports conflicts before mutating the parent.
- **Exposed scoped worker subagents.** `spawn_agent` now accepts
  `role=worker`, `mode=workspace_write`, required `ownership`, and
  normalized `write_scope`; TUI/headless surfaces report worker changed
  files and scope violations for explicit adoption.
- **Aligned ACP subagent notifications.** ACP `session/update`
  notifications now surface subagent lifecycle fields, including worker
  changed files and scope violations.
- **Added subagent adoption commands to interop events.** Headless and
  ACP finished-worker notifications now include an `adoptionCommand`
  when child changes are available to review and apply.
- **Added subagent adoption commands to tool results.** Worker
  `spawn_agent` results now include `adoption_command` when child
  changes are available, so the parent model sees the exact review/apply
  command instead of inferring it from session IDs.
- **Closed EP-13 subagent spawn.** The synchronous read-only and scoped
  worker `spawn_agent` contract is now documented as implemented after
  CLI/TUI adoption surfaces and live local-provider dogfood.

### TUI

- **Added session memory opt-out.** `/memory off` and `stado memory
  session off` now disable approved-memory prompt retrieval for the
  current session/worktree; `/memory on` and `stado memory session on`
  re-enable it while leaving `[memory].enabled` as the global gate.
- **Distinguished LM Studio installed vs loaded models.** Local
  detection now uses LM Studio loaded-state data for auto-fallback and
  picker rows, while doctor and `/providers` show installed-but-not-loaded
  remediation.
- **Aligned TUI preference docs.** The TUI and slash-command docs now
  describe `[tui].theme`, `[tui].thinking_display`, and the custom
  `theme.toml` fallback accurately.
- **Added shell symbols to inline `@` completion.** Top-level
  `.sh`/`.bash` functions now appear as symbol rows that insert
  `path:line` locations.
- **Closed EP-20 inline context completion.** The scoped `@` surface now
  covers agents, sessions, skills, docs, files, and repo-shaped symbol
  scanners.
- **Aligned implemented EP index rows.** EP-14 and EP-24 now show
  `Implemented` in the EP README table, matching their frontmatter.
- **Closed EP-22 theme catalog and picker.** The scoped catalog,
  picker, direct mode shortcuts, custom-theme row, markdown style, and
  config persistence goals are documented as implemented.
- **Pinned EP-23 status rows read-only.** Status rows stay read-only
  with inline action hints; active remediation remains in focused
  commands and config files.
- **Closed EP-23 status modal.** `/status` now keeps the modal read-only
  while showing cached background-plugin lifecycle issues and cached MCP
  attach health, including connected/tool counts and the latest attach
  error.
- **Closed EP-21 assistant turn metadata.** Footer metadata is
  documented as display-only while `conversation.jsonl` remains the
  provider-message transcript.
- **Pinned EP-13 subagent concurrency policy.** The current L4 model is
  one active child per parent session/tool queue; higher child
  concurrency is future scheduler work.
- **Closed EP-19 model/provider picker UX.** Favorites and recents stay
  per-machine state, credentials stay outside picker state, and true
  connect/OAuth is left as future provider-specific work.
- **Stored bundled theme selection in config.** `/theme` now persists
  bundled theme ids as `[tui].theme`; custom `theme.toml` remains the
  fallback path when no bundled theme is pinned.
- **Restored thinking blocks on session resume.** Persisted
  provider-native thinking now rehydrates as separate TUI thinking
  blocks so display modes still apply after restart.
- **Annotated failed tool results in assistant details.** Assistant turn
  metadata now marks requested tool counts with failed/rejected result
  counts after tool execution finishes.
- **Persisted thinking display mode.** `/thinking` and `Ctrl+X H` now
  save the display-only thinking mode to `[tui].thinking_display`, and
  the TUI restores it on startup.
- **Finished footer repository density.** The compact status row now
  shows repo-relative cwd segments inside git worktrees and appends `*`
  to the branch or detached SHA when the worktree has uncommitted
  changes, using a short cache to keep renders cheap.
- **Added an in-TUI subagent adoption command.** `/adopt [child]
  [--apply]` now dry-runs the latest adoptable worker child by default
  and applies non-conflicting child changes only when `--apply` is
  explicit.
- **Made inactive-session policy visible.** `/sessions` now states that
  inactive sessions are parked and lists the active-work blockers that
  must clear before switching.
- **Added Python symbols to inline `@` completion.** Top-level Python
  `class`, `def`, and `async def` declarations now appear as symbol rows
  that insert `path:line` locations.
- **Added JS/TS symbols to inline `@` completion.** Top-level
  JavaScript and TypeScript class, function, and variable declarations
  now appear as bounded symbol rows.
- **Expanded the bundled theme catalog.** Added `stado-rose`, a dark
  neutral theme with rose and cyan accents.
- **Surfaced provider credential health in status.** `/status` now
  reports whether the active provider's conventional API key env var is
  set, missing, or not required by a local preset.
- **Added direct provider setup hints.** `/provider <name>` now prints
  the same setup/remediation guidance available from `Ctrl+A` in the
  model picker.
- **Added credential health to providers overview.** `/providers` now
  shows whether the active provider's conventional API key env var is
  missing, set, or not required.
- **Named configured MCP servers in status.** `/status` now summarizes
  configured MCP server names without starting or probing them.
- **Added markdown style control for themes.** `theme.toml` can now set
  `[markdown].style` to `auto`, `light`, or `dark`; `auto` keeps the
  existing background-luminance detection.
- **Showed custom themes in the picker.** When the current
  `theme.toml` does not match a bundled theme, `/theme` now shows it as
  the current custom row and selecting it closes the picker without
  rewriting the override.
- **Surfaced trace IDs in the status modal.** When the TUI has a valid
  OTel span context, `/status` now shows the current trace id for
  copy/paste into a collector UI.
- **Grouped inline slash suggestions.** The `/` suggestion surface now
  shows compact Quick/Session/View group labels, matching the modal
  command palette without leaving the input.
- **Matched markdown rendering to light themes.** Assistant markdown now
  uses Glamour's light style when the active theme background is light,
  and falls back to the dark style for dark/contrast themes.
- **Added local-runner no-model remediation.** `/providers` now shows
  runner-specific next steps when a reachable local backend has no
  models loaded, including the LM Studio `lms load <model>` path.
- **Added a subagent activity overview.** `/subagents` now lists recent
  spawned child sessions with status, worktree, changed-file counts,
  scope violations, and adoption commands.
- **Added expandable assistant turn details.** `Shift+Tab` can now expand
  the latest assistant footer to show token, cache, tool, and trace
  details without making normal transcript rows noisier.
- **Added action hints to the status modal.** Provider, model, tools,
  plugin, MCP, OTel, budget, and context rows now show the focused
  command or config file to open next.
- **Added direct light/dark theme shortcuts.** `/theme light`,
  `/theme dark`, and `/theme toggle` now switch bundled themes without
  opening the theme picker.
- **Toned down the startup landing logo.** The empty-session landing
  view now samples the embedded banner down to a compact fixed-height
  mark so the prompt stays visually primary on large terminals.
- **Added docs to inline `@` completion.** The TUI picker now groups root
  Markdown docs and `docs/**/*.md` before ordinary file matches.
- **Added Go symbols to inline `@` completion.** Top-level Go
  declarations now appear as symbol rows that insert `path:line`
  references.
- **Preserved per-session provider/model selection.** Switching TUI
  sessions now restores each session's selected provider and model, and
  invalidates the live provider when the restored provider differs.
- **Blocked session switches during background plugin ticks.** The TUI
  now waits for running or queued background plugin ticks before
  switching, creating, or forking sessions in the same process.
- **Added subagent activity to the sidebar.** TUI `spawn_agent` lifecycle
  events now populate a recent child-session activity section with
  running/completed status, changed-file counts, scope violations, and
  adoption readiness.

### CLI

- **Added lesson-specific review commands.** `stado learning
  edit|approve|reject|delete|supersede` now wraps the append-only memory
  review flow while preserving lesson fields such as trigger, rationale,
  evidence, tags, scope, and expiry.
- **Added learning document handoff.** `stado learning document <id>`
  now writes a lesson to `.learnings/` without overwriting existing
  notes and rejects the lesson from prompt retrieval.
- **Added stale lesson file checks.** `stado learning stale` now finds
  approved lessons that cite missing evidence files; `--apply` marks
  them candidate for review so they stop being retrieved.
- **Added lesson-only export.** `stado learning export` now emits folded
  lesson items as local JSON for audit and recovery workflows.

### Docs

- **Closed EP-16 learning system.** The local learning workflow is now
  documented as implemented after proposal, review, approval, retrieval,
  document-elsewhere, stale-file review, and lesson-only export shipped.
- **Closed EP-15 memory system.** The implemented standard now records
  the local JSONL baseline, review/edit/supersede/export surfaces, and
  session-local retrieval opt-out.
- **Narrowed the subagent EP open questions.** EP-13 now reflects the
  shipped worker summaries, adoption commands, `/subagents`, and sidebar
  subagent activity, leaving only concurrency policy open.
- **Closed the multi-session TUI EP.** EP-14 now records the
  active-session-only policy, confirmed delete semantics, and
  command-palette/session-overview UI shape as implemented.
- **Closed the inline slash shortcut-hint question.** EP-26 now records
  that inline slash rows show command IDs and secondary keyboard
  shortcuts together, with regression coverage.
- **Refreshed the opencode TUI UAT backlog.** The report now reflects
  the shipped L4 subagent, context completion, landing, status, turn
  metadata, and theme slices and narrows the remaining work.
- **Documented worker subagent update fields.** Headless/ACP docs and
  CLI help now describe `subagent` lifecycle payloads, worker
  `forkTree` / `changedFiles` / `scopeViolations` /
  `adoptionCommand` fields, and the explicit `stado session adopt`
  review flow.

### Fixes

- **Closed rooted file-access security findings.** Native fs tools, plugin
  fs imports, config writes, and subagent adoption now use rooted file
  operations for symlink-safe confinement; gosec no longer reports G703,
  G122, or HIGH findings.
- **Hardened plugin rollback checks.** Plugin trust verification now
  compares semver precedence instead of raw strings, so versions like
  `1.2.0` cannot pass after `1.10.0`.
- **Stabilized tmux TUI UAT startup.** The real-PTY harness now waits for
  the shell to initialize and sends the launch command literally before
  pressing Enter.
- **Kept JS/TS symbol completion top-level only.** Indented nested
  JavaScript and TypeScript declarations are no longer indexed as
  top-level `@` symbol rows.

## v0.23.1 — 2026-04-25

### Docs

- **Saved the L3 checkpoint for future sessions.** Added a dated
  handoff report covering the `v0.21.2` through `v0.23.0` release loop,
  verification state, and next candidate work.

## v0.23.0 — 2026-04-25

### TUI

- **Preserved per-session draft and scroll state.** Switching sessions
  in one TUI process now caches the inactive session's editor draft and
  chat scroll offset, then restores them when switching back.

## v0.22.0 — 2026-04-25

### TUI

- **Added model-picker provider setup actions.** `Ctrl+A` inside the
  model picker now closes the picker and prints selected-provider setup
  guidance for API-key env vars, configured preset endpoints, or local
  runner startup.

## v0.21.2 — 2026-04-25

### Docs

- **Refreshed the opencode TUI UAT report.** The report now separates
  the original `v0.4.2` comparison from current `v0.21.1` status and
  reprioritizes remaining work around subagents, provider remediation,
  session state caching, and docs/symbol context completion.

## v0.21.1 — 2026-04-25

### TUI

- **Kept session identity visible in the footer.** The dense chat footer
  now includes the active session label, or short session id when no
  label exists, alongside cwd, branch, version, usage, cost, and command
  hints.

## v0.21.0 — 2026-04-25

### TUI

- **Extended inline `@` completion to skills.** Loaded project skills
  now appear after agents and sessions; accepting a skill mention
  injects that skill's prompt body into the conversation and removes
  the mention from the draft.

## v0.20.0 — 2026-04-25

### TUI

- **Extended inline `@` completion to sessions.** The editor now shows
  session rows after agents and before files; accepting a session-only
  mention switches to that session, while accepting one inside a longer
  prompt inserts an explicit `session:<id>` reference.

## v0.19.0 — 2026-04-25

### CLI

- **Added explicit learning lesson capture.** `stado learning
  propose/list/show` now records EP-16 lesson candidates in the
  append-only memory store with required trigger and evidence metadata.

### Prompt

- **Separated approved lessons from ordinary memory.** Lessons with
  `memory_kind: "lesson"` are retrieved through the opt-in memory path
  and rendered under an "Operational lessons" prompt section.

## v0.18.0 — 2026-04-25

### CLI

- **Added append-only memory supersession.** `stado memory supersede
  <id>` now replaces an approved memory with a new approved item while
  preserving the original as a `superseded` folded audit tombstone.

### Fixes

- **Fixed folded memory supersede handling.** Supersede events now mark
  the source memory as `superseded` instead of resolving the event
  against the replacement id.

## v0.17.0 — 2026-04-25

### CLI

- **Added append-only memory edits.** `stado memory edit <id>` can now
  update candidate or approved memory summaries, bodies, metadata,
  tags, and expiry while preserving the JSONL audit log as explicit
  `edit` events.

## v0.16.0 — 2026-04-24

### Prompt

- **Added opt-in approved-memory prompt context.** `[memory].enabled =
  true` now injects bounded, scoped, non-secret approved memories into
  TUI, `stado run`, headless, and ACP prompts as labeled untrusted
  context.

### CLI

- **Surfaced memory prompt settings in config.** `stado config init`
  now documents `[memory]`, and `stado config show` prints the resolved
  enable flag plus item/token caps.

## v0.15.0 — 2026-04-24

### CLI

- **Added memory review commands.** `stado memory` now lists, shows,
  approves, rejects, deletes, and exports the local append-only memory
  store used by `memory:*` plugins.

## v0.14.0 — 2026-04-24

### Plugins

- **Added the first memory host API slice.** Plugins can now declare
  `memory:propose`, `memory:read`, and `memory:write` to use a
  capability-gated local append-only memory store for candidate capture,
  approved-memory retrieval, and explicit write mutations.

### Infra

- **Pinned CI and release tool versions.** GitHub workflows now opt into
  Node 24 action execution and pin GoReleaser / golangci-lint versions
  instead of relying on `latest`.

### Docs

- **Accepted EP-15 memory-system design.** The memory plugin standard
  now defines item schema, scopes, host APIs, retrieval, review controls,
  storage, and prompt-injection defenses.
- **Accepted EP-16 learning-plugin design.** The learning standard now
  defines lesson candidates, approval, retrieval, invalidation, and its
  relationship to the EP-15 memory substrate.

## v0.13.1 — 2026-04-24

### Fixes

- **Restored session compaction auditability.** TUI compaction now keeps
  `.stado/conversation.jsonl` append-only, records raw-log digests on
  compaction markers, and creates real turn-boundary refs for pure chat
  and no-file-change turns. `stado run --session` also attaches to the
  persisted session and records a turn boundary when tools are disabled;
  headless and ACP git-backed prompts persist their transcripts before
  later compaction; headless compaction now writes the same raw-log
  audit marker when a git-backed session is attached.

## v0.13.0 — 2026-04-24

### Prompt

- **Aligned the default system prompt with cairn.** New first-run
  `system-prompt.md` templates now include cairn's governing
  principles, six-phase workflow, session artefact discipline, and
  autonomous-work safety rules while keeping stado identity fixed.
  Untouched generated default templates from prior releases are updated
  automatically; customized templates are left alone.

## v0.12.0 — 2026-04-24

### TUI

- **Added thinking display modes.** `Ctrl+X H` and `/thinking` now cycle
  provider-native thinking output between full, tail-only, and hidden
  display without changing what is persisted.
- **Improved model and slash workflows.** Model selection now persists
  the chosen model/provider as the new default, `Ctrl+X M` opens the
  model picker, `/` opens inline fuzzy command suggestions, and `Ctrl+P`
  remains the full command palette.
- **Clarified manual approval demo use.** The `approval_demo` tool spec
  now warns that it is a human-triggered manual test tool only.
- **Added mode-coloured input rails.** Do, Plan, and BTW now use distinct
  left-rail colours in the chat input.

## v0.11.0 — 2026-04-24

### TUI

- **Expanded multi-session management.** The session overlay now supports
  switch/resume, new, rename, fork, and confirmed delete actions without
  leaving the TUI.

## v0.10.0 — 2026-04-24

### TUI

- **Improved footer density.** The chat status row now keeps compact cwd,
  git branch, and version context on the left while preserving usage and
  command hints on the right.

## v0.9.0 — 2026-04-24

### TUI

- **Clarified LSP readiness.** The status modal now explains that LSP
  tools activate when supported files are read and lists detected
  language-server binaries.

## v0.8.0 — 2026-04-24

### TUI

- **Added a status modal.** `/status` and `Ctrl+X S` now show a compact
  provider, model, tool, plugin, MCP, OTel, sandbox, and context summary.

## v0.7.0 — 2026-04-24

### TUI

- **Added a bundled theme picker.** `/theme` and `Ctrl+X T` now open a
  picker for `stado-dark`, `stado-light`, and `stado-contrast`; picking
  one updates the running TUI and persists it to `theme.toml`.

## v0.6.0 — 2026-04-24

### TUI

- **Started unified `@` completion.** The inline `@` picker now shows
  Do, Plan, and BTW agents before repo files; accepting an agent switches
  the active agent and consumes the mention.
- **Added assistant turn footers.** Completed assistant responses now
  show compact metadata for the agent, model/provider, elapsed time,
  tool count, token delta, and cost delta.

## v0.5.0 — 2026-04-24

### TUI

- **Added a first-class agent picker.** `ctrl+x a` and `/agents` now
  open a modal for switching between Do, Plan, and BTW while preserving
  the existing `Tab` Do/Plan toggle and `ctrl+x ctrl+b` BTW shortcut.
- **Made the active agent visible in the sidebar.** The Agent section now
  shows the current Do, Plan, or BTW agent without restoring the old
  noisy mode suffix in the session header.
- **Added the first in-TUI multi-session workflow.** `ctrl+x l` opens a
  searchable session switcher and `ctrl+x n` creates/switches to a
  fresh session in the same TUI process. Switching is blocked while a
  draft, queued prompt, approval, compaction, stream, or tool is active.
- **Improved model picker continuity.** The picker now marks the
  current model and remembers recent model/provider selections under
  stado state so frequently used choices surface first.
- **Added model picker favorites.** Press `ctrl+f` in the model picker
  to favorite or unfavorite the highlighted model; favorites persist in
  stado state and appear before recents.
- **Calmed the default sidebar.** Debug-only diagnostics such as info
  logs, unknown context limits, unbounded budgets, and normal sandbox
  status now stay hidden unless `/debug` enables sidebar diagnostics
  or warnings/errors need attention.
- **Auto-title fresh sessions from the first prompt.** The TUI now writes
  a compact session description from the first user message when no
  manual `/describe` label exists, improving future session lists and
  switchers without overwriting user labels.

### Infra

- **Refreshed the real-PTY TUI UAT harness for the landing view.** The
  tmux harness now isolates config/state, avoids live-provider
  nondeterminism, expects the startup landing view to be sidebar-free,
  and checks the current rail-card message treatment.

## v0.4.2 — 2026-04-24

### Fixes

- **Fixed main CI failures from the `v0.4.1` push.** Cleaned up new
  lint findings and removed the remaining `-race` hazards in TUI trace
  logging and Linux sandbox cleanup.

## v0.4.1 — 2026-04-24

### Docs

- **Documented release versioning policy.** Minor releases now mean new
  features or meaningful behavior adjustments; patch releases mean
  smaller fixes, docs/process updates, dependency bumps, or contained
  internal changes.

## v0.4.0 — 2026-04-24

### TUI

- **Added the first-run landing view.** A new opencode-style startup
  screen centers the stado ANSI logo, model/provider status, command
  hints, and the editable prompt before the first message.
- **Made the chat input taller by default.** The editor now reserves
  three extra visible rows so multi-line prompts do not collapse the
  interaction area immediately.
- **Fixed the first-message freeze path.** TUI trace logging and
  renderer/log-tail fixes keep input responsive while thinking and
  response blocks stream into the chat history.
- **First-turn provider startup is now async instead of blocking the
  UI.** When no default provider is pinned, the TUI now probes local
  runners at startup, queues the first prompt behind that probe if it is
  still in flight, and replays the prompt automatically when the probe
  resolves.
- **Added focused TUI trace logging for startup / first-turn issues.**
  `STADO_TUI_TRACE=1 stado` now emits timestamped trace lines for the
  provider probe, first-submit queueing, provider resolution, and stream
  start into the sidebar log tail.

### CLI / Infra

- **Added a configurable default system prompt template.** First-run
  config creation now writes `system-prompt.md` under the stado config
  directory, and the TUI, run, ACP, headless, MCP, and session-resume
  surfaces all compose prompts from the same template.
- **OpenTelemetry is now actually bootstrapped by the runtime-facing
  command surfaces.** `stado` (TUI), `stado session resume`, `stado run`,
  `stado headless`, `stado acp`, `stado mcp-server`, and session
  fork/revert flows now start the configured OTel runtime and flush it on
  shutdown instead of leaving the shipped spans as no-ops.
- **Release builds now stamp both CLI and TUI version strings.**
  Goreleaser sets the root command version and the sidebar/bundled
  plugin version value from the same tag.

### Docs

- **Added standalone command guides for every shipped top-level
  command.** The docs index now links `agents`, `audit`, `stats`,
  `headless`, `acp`, `mcp-server`, `verify`, `self-update`, and the
  small generated/informational commands.
- **Moved planned work into EP placeholders.** `BUGS.md` now stays
  focused on active bugs, while planned subagents, multi-session TUI,
  memory, learning, tool approval policy, and system-prompt work are
  covered by EPs.
- **Refreshed stale design/config/context docs.** The docs now reflect
  plugin approval cards, current context accounting, bundled
  auto-compact behavior, and the actual `config` command surface.

## v0.3.0 — 2026-04-24

### CLI / Infra

- **Shipped first-install `install.sh`.** Linux/macOS installs can now
  follow a signed-manifest path on day one: the script verifies
  `checksums.txt` with `cosign`, verifies the matching archive against
  that manifest, and installs `stado` to `~/.local/bin` by default.
- **Direct command coverage now includes `agents` and `audit`.**
  `stado agents list/attach/kill` and `stado audit verify/export/pubkey`
  now have dedicated command-level tests instead of depending only on
  lower-level helper coverage.

### TUI

- **Custom template overlays are now live in the shipped app.** Files
  under `$XDG_CONFIG_HOME/stado/templates/*.tmpl` now override the
  bundled renderer templates at boot, matching the long-documented
  `render.NewWithOverlay` contract.

## v0.2.2 — 2026-04-24

### CLI / Infra

- **Provider credential lookup is now centralized.** The direct
  provider constructors, TUI provider builder, and `stado doctor` now
  share one source of truth for provider-name-to-env-var resolution
  under `internal/config` instead of carrying separate maps.
- **Bundled hosted-provider overrides keep their API-key env lookup.**
  If you override a bundled hosted provider name like `groq`,
  `openrouter`, or `deepseek` via `[inference.presets.<name>]`, the TUI
  now still injects the conventional API key for that provider instead
  of silently dropping it.

## v0.2.1 — 2026-04-24

### Security / Hardening

- **Linux `net:<host>` subprocess policies are now real proxy-only
  network sandboxes.** Instead of sharing the host netns and relying on
  proxy env vars alone, the Linux runner now wraps `bwrap` in
  `pasta --splice-only` and forwards only the local proxy port into the
  private netns.

## v0.2.0 — 2026-04-23

+ Plugin-driven context-management release: shipped bundled
auto-compaction by default, promoted session-aware plugin flows on the
CLI/headless surfaces, reorganized the shipped/example plugin catalog,
and tightened several local authority boundaries.

### TUI / Plugins

- **Bundled auto-compaction is now on by default.** Stado ships the
  `auto-compact` plugin source at `plugins/default/auto-compact/`,
  bundles it into the binary as a default background plugin, and loads
  it automatically in the TUI/headless server.
- **Hard-threshold TUI recovery now forks and replays automatically.**
  When the TUI hits the hard context threshold, it emits a
  `context_overflow` event to background plugins; the bundled
  auto-compact plugin responds by forking a compacted child session and
  the blocked prompt is replayed there.
- **Plugin layout is now product-facing.** The repo now uses
  `plugins/default/` for shipped bundled plugin sources and
  `plugins/examples/` for opt-in samples; the old internal
  `builtinplugins` package was renamed to `bundledplugins`.

### CLI / Headless

- **`[hooks].post_turn` now has cross-surface parity.** The same
  notification-only shell hook now fires on completed turns in the TUI,
  `stado run`, and headless `session.prompt`, with the same JSON payload
  shape and the same bash-disable guard when `bash` is removed from the
  active tool set.
- **`stado plugin run` can now attach to persisted sessions.** Pass
  `--session <id>` to give a plugin real `session:read`,
  `session:fork`, and `llm:invoke` access on the CLI path instead of the
  old "zeroed session" fallback.
- **Plugin-created forked sessions now persist their seed summary.**
  Session-aware plugins that fork a child session now write the seeded
  summary into the child's `.stado/conversation.jsonl`, so resuming the
  child picks up with that summary already loaded.
- **`plugin install` no-op output is now explicit.** Reinstalling the
  same plugin version prints a stdout `skipped:` line instead of only a
  stderr advisory, so scripts and users can distinguish "already
  installed" from silent failure.

### Security / Hardening

- **Session/agent/plugin path traversal holes were closed.** Session
  and agent worktree lookups now validate local IDs before joining
  paths, installed plugin IDs are checked before resolving under the
  plugin state dir, and `session:fork` no longer accepts foreign-session
  refs or raw commit hashes.
- **Writable install/update paths now propagate final flush errors.**
  Plugin install and self-update now treat `Sync` / `Close` failures as
  real errors instead of reporting success after a partial write.
- **Timeouts were added to external HTTP control paths.** Self-update,
  CRL/Rekor, and local-provider probe calls now use explicit HTTP
  clients instead of the process-wide default client with no timeout.
- **Capability-less stdio MCP servers are now refused.** Local MCP
  subprocesses must declare `capabilities` in config instead of falling
  back to caller privileges.
- **Sandbox defaults are narrower.** The built-in `bash` tool now uses
  deny-all networking on the sandboxed subprocess path by default, and
  the docs now describe the remaining Linux `net:<host>` limitation
  honestly as proxy-mediated rather than a raw-socket firewall.
- **Several crash/corruption edges were removed.** The LSP client no
  longer panics on bad pending entries, plugin FS reads fail on overflow
  instead of silently truncating, and TUI aggregate usage accounting now
  stays on the Bubble Tea event loop instead of being mutated from the
  stream goroutine.

## v0.1.3 — 2026-04-23

+ Sandbox follow-up release: Linux subprocess host-allowlist policies
now route through the local CONNECT proxy as originally designed, and
the README now distinguishes Linux, macOS, Windows, and WASM tool
sandbox behavior more precisely.

### Infra / Security

- **Linux `net:<host>` subprocess policies now wire through the local
  CONNECT-allowlist proxy.** `BwrapRunner` starts the loopback proxy for
  `NetAllowHosts`, injects `HTTP_PROXY` / `HTTPS_PROXY` into the child,
  and clears `NO_PROXY` so HTTPS-aware subprocesses and MCP stdio
  servers actually honor the configured host allowlist instead of
  bypassing it.
- **Runner env propagation is now handled at the runner boundary.**
  The sandbox runner interface accepts the candidate child environment
  directly so Linux `bwrap`, macOS `sandbox-exec`, Windows passthrough,
  and the fallback runner all perform filtering from the same source of
  truth.

### Docs

- **README sandbox wording now matches the implementation.** The docs
  now call out that Linux has the strongest shipped path, macOS has
  real subprocess sandboxing but not Linux-style whole-process
  narrowing, Windows v1 is still warning-only, and WASM tools are
  sandboxed by `wazero` host-import gates rather than the OS subprocess
  runner.

## v0.1.2 — 2026-04-23

+ Docs + CLI parity release: ships the documented `doctor` automation
surface, refreshes the top-level/docs catalog for the actual shipped
runtime, and lands a large internal source split to make the codebase
easier to read and maintain.

### CLI

- **`stado doctor` now exposes the documented machine/CI flags.**
  `--json` emits newline-delimited JSON (`check`, `status`, `value`,
  `detail`) and `--no-local` skips local-runner probes for faster or
  offline CI preflight. Blocking doctor failures now exit 1, matching
  the command guide.

### Docs

- **README refresh for the current release and CLI surface.** The
  install section now documents the signed `checksums.txt` verification
  flow that releases actually publish, the quick-start plugin commands
  include the missing sign/trust steps, and the configuration / docs /
  roadmap sections now point at shipped guides instead of stale
  placeholder wording.
- **Retroactive EP backfill for the major shipped design decisions.**
  `docs/eps/` now includes accepted records for the provider seam,
  git-native session model, sandboxing, plugin runtime, conversation
  state, repo-local prompt inputs, guardrails, and interop surfaces,
  and the docs indexes now link that catalog directly.
- **Roadmap + command docs now call out the actual remaining product
  gaps.** `PLAN.md`, `README.md`, and the relevant `docs/` guides now
  explicitly describe the unfinished user-visible surfaces: Windows
  sandbox v2, the advisory-only CLI `session compact` shim, the pending
  `install.sh` first-install path, and template-overlay support that
  exists in the renderer but is not yet wired into the TUI entry point.

### Infra

- **Large source files were split by concern without changing package
  boundaries or exported surfaces.** The TUI model, session/plugin CLI,
  plugin host runtime, headless server, runtime loop, and git commit
  internals are now spread across smaller focused files, making the
  codebase easier to review and maintain without changing the shipped
  behavior.

## v0.1.1 — 2026-04-23

+ Release follow-up: fixes the bundle-fetch path that broke the `v0.1.0`
release workflow, and is the first successful 0.1 release build.

### Infra / Release

- **Bundled-tool release fetches no longer depend on GitHub REST asset
  digests.** `hack/fetch-binaries.go` now reads ripgrep checksums from
  upstream `.sha256` sidecars, reads ast-grep checksums from GitHub's
  public `expanded_assets` fragment, and aborts immediately on any
  supported-target fetch failure instead of silently skipping a bundle
  and letting the compiler fail later.

## v0.1.0 — 2026-04-23

+ Built-in tools now ship through the same signed WASM runtime as
third-party plugins, macOS sandboxing is shipped alongside Linux, and
the public plugin workflow is documented end-to-end. Pre-1.0: breaking
changes still allowed between tags.

### Plugins / Tool runtime

- **Built-in tools now load through the plugin runtime.** The default
  `read` / `write` / `edit` / `glob` / `grep` / `bash` / `webfetch` /
  `ripgrep` / `ast_grep` / `read_with_context` / LSP tools are now
  embedded signed WASM modules instantiated through the same wazero host
  surface used for third-party plugins. That removes a large native-vs-
  plugin split, makes override behavior consistent, and keeps the
  bundled tool surface auditable.
- **Approval wrappers moved into plugins.** The old in-process
  "approval tool" path is gone. Approval behavior now lives in explicit
  example plugins (`approval-bash-go`, `approval-write-go`,
  `approval-edit-go`, `approval-ast-grep-go`) plus a bundled
  `approval_demo` module that exercises the `ui:approval` import.

### Docs

- **README refresh for the 0.1.0 surface.** The install section now
  documents release assets and the Homebrew tap that already exists, the
  plugin command examples no longer mention the removed GitHub bot
  workflow generator, and the shipped-status sections now reflect the
  macOS sandbox + WASM plugin runtime that are already live.
- **`stado plugin` now has a command guide.** `docs/README.md` links a
  new `docs/commands/plugin.md` guide covering scaffold → sign → trust
  → verify → install → run, plus the distinction between trusted
  signers (`plugin list`) and installed plugin IDs (`plugin installed`).

### Infra / Security

- **Removed the `stado github` bot workflow generator.** The GitHub
  comment-triggered bot path added a high-risk hosted-runner execution
  surface that was not core to stado's runtime model. The CLI command,
  its workflow template, and related docs references are gone.
- **Plugin FS sandbox now resolves symlinks before capability checks.**
  `stado_fs_read` / `stado_fs_write` used to call `os.ReadFile` / `os.WriteFile`
  directly, which follows symlinks. A plugin with `fs:read:/allowed` could
  create a symlink in `/allowed` pointing outside the tree and read arbitrary
  files. The new `realPath()` helper resolves symlinks before the
  `allowRead` / `allowWrite` check, so escape is caught.
- **Plugin install path traversal prevention.** Manifest `Name` and `Version`
  are validated with `filepath.IsLocal()` plus explicit rejection of `/` and
  `\` characters so a malicious manifest can't write outside the plugins
  directory with fields like `Name: "../../../etc"`.
- **Headless session ID no longer reuses numbers after deletion.**
  `sessionNew` used `len(s.sessions)+1`, so deleting `h-1` and creating a new
  session would overwrite `h-2`. Now uses a monotonic `nextID` counter.
- **Headless session operations are mutex-protected.** `sessionPrompt`,
  `sessionCancel`, and `sessionCompact` all raced on `sess.messages`,
  `sess.cancel`, and `sess.gitSess`. Each `hSession` now carries its own
  `sync.Mutex`.
- **ACP session operations are mutex-protected.** Same race pattern as headless
  — `session/prompt` and `session/cancel` dispatched on separate goroutines
  could corrupt message history or cancel the wrong turn. Fixed with per-
  `acpSession` mutex and monotonic ID counter.
- **MCP client leak on reconnect fixed.** `attachMCP` called from every
  `BuildExecutor` reconnected each configured MCP server without closing the
  previous `client.Client`. After several tool-enabled prompts the process
  leaked stdio MCP subprocesses. `Connect` now closes the old client inside
  the lock after a successful replacement.
- **`run --session --tools` now opens the correct worktree.** When `--session`
  was set, tools still opened a session from the caller's cwd instead of the
  resumed session's worktree. Running from a different directory would
  execute mutating tools against the wrong repo.
- **Host-safe SDK split for bundled WASM modules.** `internal/bundledplugins/sdk`
  now keeps the real pointer-based implementation behind `//go:build wasip1`
  and provides a host-only stub for tests and lint. That stops host-side
  tooling from treating wasm32 offsets as native pointers while preserving
  the ABI used by the embedded modules.

### UX


- **Async tool execution — TUI no longer blocks on long-running tools.**
  `bash sleep 30` (or any slow tool call) used to freeze the entire
  interface until the command returned. Tool calls now run on a
  goroutine and ferry their result back via `toolResultMsg`, so the
  user can keep typing, scroll, or cancel while the tool is in-flight.
  Same pattern already used for `/plugin:...` invocations.
- **Queued prompts get visual feedback.** When you hit Enter while a
  turn is still streaming, the follow-up message is appended to the
  chat immediately with a muted "queued — runs when the current turn
  finishes" pill. Previously the only signal was a tiny status-bar
  label that was easy to miss. Ctrl+C on a queued prompt now also
  removes the block, not just the internal buffer.
- **Render caching eliminates glamour-induced keypress lag.** Long
  conversations used to stutter during streaming because every frame
  re-ran glamour/markdown on every historical block. Two changes fix
  this: (1) `Renderer` now memoises `glamour.TermRenderer` instances
  per width (creating one costs 5–10 ms), and (2) each conversation
  block caches its last rendered output, invalidating only when its
  body, width, expand state, or tool result changes. The live
  assistant block still re-renders every tick (it is growing), but
  everything else is near-free.
- **`[approvals]` allowlist is actually wired to the TUI.** The config
  parser has supported `mode = "allowlist"` and `allowlist = ["read",
  "grep"]` for a while, but `Run()` never called `SetApprovals()`, so
  the allowlist was silently ignored. Now tools named in the allowlist
  auto-execute without the `⚠ y/n` prompt.
- **Gated `?` and `/` keybindings.** Typing a literal `?` or `/` inside
  a non-empty prompt no longer pops the help overlay or command
  palette — the characters insert as text instead. Both shortcuts
  still work when the input box is empty.
- **Tool cancellation actually works.** Approved tool calls run on a
  goroutine so the UI stays responsive. Previously there was no way
  to cancel a long-running tool after approving it. Now Ctrl+C
  propagates cancellation into the tool's context; the goroutine
  exits with "cancelled by user" instead of running to completion.
- **Live "tool running" indicator with elapsed counter.** Tool blocks
  now show `running 3.2s` while the command is active, refreshed
  every 250 ms via `toolTickMsg`. No more silent 30-second waits
  where the user can't tell if stado is working or frozen.
- **Narrow-terminal startup hint.** Terminals narrower than 90 columns
  no longer show a blank empty-state — they get `"Send a message to
  get started — /help for commands"` so first-time users aren't
  staring at empty whitespace.
- **`Ctrl+C` during streaming confirms cancellation.** Cancelling a turn
  now drops a one-time "turn cancelled" system block into the chat,
  so users know the keystroke registered instead of wondering if
  the model finished coincidentally.
- **Collapsed tool cards show an expand affordance.** When a tool
  call has completed but the card is collapsed, a muted
  `shift+tab` hint is appended to the header row so the user
  knows they can expand it.
- **Sidebar placeholder when no model is set.** If `model` is empty in
  config, the sidebar Model field now reads `"no model set — /model"`
  instead of a completely blank line.

### Infra

- **Test coverage: 5 new UAT scenarios** for previously uncovered
  slash commands: `/split`, `/todo`, `/provider` (uninitialised),
  `/tools` (populated + empty paths).

## v0.0.1 — 2026-04-21
+ ACP + plugin ABI + MCP client + MCP server surfaces are all
feature-complete relative to the ranked research list (AGENTS.md
auto-load, `[budget]` cost gate, `.stado/skills/`, `[hooks]`
post_turn, and `stado mcp-server`). Pre-1.0: breaking changes still
allowed between tags.

### Iteration-cycle additions (post-initial-sweep)

- **`stado mcp-server` — expose stado's tools as an MCP server.**
  Every bundled stado tool (read, grep, ripgrep, ast-grep, bash,
  webfetch, file ops, LSP-find) is registered with an MCP v1
  server over stdio. Other MCP-aware agents (Claude Desktop,
  Cursor, etc.) can call stado as a tool backend. Scope is
  tools-only in this release — no resources, no prompts, no
  sampling. `[tools].enabled` / `[tools].disabled` trim the
  exposed surface same as the TUI and `run` paths, so an MCP
  client only sees what stado is currently configured to offer.
  Auto-approve host rooted at process cwd — the MCP client is
  assumed to be the authorization boundary. Closes the last item
  in the ranked research list.
- **`/context` is a one-stop session-state view.** Used to show
  only token + threshold info. Now also renders: session id,
  cost, budget caps (when set), loaded instructions file, skill
  names, configured post_turn hook. Answers "what does this
  session look like to the model?" without bouncing across
  /budget, /skill, sidebar.
- **`/session` slash command** — prints the current session id,
  worktree path, and description label. Copy-paste target for
  `stado session fork`, `session tree`, `session attach` in
  other shells. Explains itself when invoked outside a live
  session instead of silently failing.
- **TUI sidebar surfaces loaded skills.** A new "Skills: N — /skill"
  row renders when skills are loaded from `.stado/skills/`. Users
  no longer have to know the slash command in advance to discover
  the feature — a repo with a skills directory advertises itself.
  The row stays hidden when no skills are loaded so empty repos
  don't see a misleading "0 skills" row.
- **`stado headless --help` documents `plugin.list`/`plugin.run`.**
  Both RPC methods landed months ago but the help text never
  listed them, so CI integrators had to read the server code to
  learn they existed. Added the shape summary for both plus the
  full set of `session.update` notification kinds.
- **`stado run --skill <name>`** — skills are now CLI-usable, not
  just a TUI feature. Resolves `.stado/skills/<name>.md` from cwd
  and uses the body as the prompt. Combines with `--prompt` (skill
  body prepended) so the reusable skill plus a per-invocation ask
  compose naturally. Unknown skill lists the available names in the
  error message so a typo doesn't force a filesystem grep. Useful
  in CI: a repo can ship `.stado/skills/ci-review.md` and pipelines
  invoke `stado run --skill ci-review` instead of inlining the
  full prompt text.
- **`/retry` slash command.** Regenerates the last assistant turn
  from the same user prompt — equivalent to the "regenerate" button
  in ChatGPT/Claude web UIs. Truncates the conversation back to the
  last user message (dropping assistant + tool-role messages) and
  re-streams. No-ops with a hint when there's nothing to retry, the
  last message is already a user prompt, or a stream is already
  running (avoids doubling cost and racing the goroutine).
- **`agents list` hides stale/empty rows by default.** Same problem
  `session list` had pre-dogfood: every aborted run leaves a PID
  file + empty worktree in the agents listing, so the output was
  30+ stale rows with dashes everywhere. Now hidden; `--all`
  restores the full view. A row is worth showing when the process
  is alive OR there's committed content on the tree/trace refs.
- **`stado doctor` now surfaces opt-in feature config** — Budget
  caps, Lifecycle hooks, Tools filter. All render as ✓ regardless
  of whether they're set; the point is to make the features
  discoverable and let users verify that their config.toml took
  effect. "Did I configure the budget cap?" is now a `doctor`-
  answerable question instead of a config-file-read task.
- **Lifecycle hooks — `[hooks]` section (MVP, notification-only).**
  Users can wire a shell command to the `post_turn` event; stado
  runs `/bin/sh -c <cmd>` with a JSON payload on stdin carrying
  turn index, input/output tokens, cost, duration, and a ≤200-char
  excerpt of the assistant text. 5-second wall-clock cap per hook.
  stdout/stderr go to stado's own stderr with a `stado[hook:<event>]`
  prefix so they're distinguishable in shared terminals. Failures
  are logged, never propagated — a broken hook can't poison the
  next turn. MVP scope is deliberate; a richer "approve tool call
  via external policy" form can grow on top.
- **Help overlay (`?`) now lists slash commands.** Users had to
  open the palette separately to learn that `/budget`, `/skill`,
  `/model`, etc. existed. The overlay now appends the palette's
  Commands table below the keybindings section, grouped the same
  way (Quick / Session / View).
- **Skills: `.stado/skills/*.md` auto-loader.** Drop a markdown file
  with frontmatter `name:` / `description:` in a `.stado/skills/`
  directory and stado exposes it as `/skill:<name>` in the TUI.
  Invocation injects the body as a user message so the next turn
  acts on it. `/skill` alone lists what's loaded. Resolution walks
  from cwd upward — nearest-wins for module-level overrides in a
  monorepo. Bodies without frontmatter use the filename stem as
  the name. Matches the emerging cross-vendor convention for
  reusable prompt fragments.
- **`stats --json` now emits a valid empty shape when there are no
  sessions.** Previously stdout was empty and `(no sessions in
  window)` leaked to stderr, which broke `stado stats --json | jq`
  in a fresh repo. Matches the already-valid empty case for
  "sessions exist but no tool calls in window."
- **`config init` template now covers `[budget]` + an AGENTS.md
  pointer.** The generated template was the only docs users saw
  for many knobs; adding budget + pointing at AGENTS.md closes the
  gap between config knobs users can see and features actually
  available.
- **`session list` hides empty rows by default.** Zero-turn +
  zero-message + zero-compaction sessions were cluttering the
  default output — `session list` on a long-lived repo was showing
  50 empties per 3 real rows. Now hidden; `--all` restores the
  full listing. A stderr footer reports how many were hidden with
  a copy-pasteable `session gc --apply` pointer.
- **`stado doctor` stops failing on missing optional tools.** gopls
  is only needed by the `lsp-find` tool; stado works fine without
  it. Now rendered as ✓ with a "not found — optional" detail
  instead of ✗, and the exit code no longer flips to 2 when the
  only missing dep is optional. New `checkOptionalBin` helper
  separates "must-have" from "nice-to-have" checks.
- **`stado config show` now prints `[budget]` and `[tools]`.** Both
  sections were silently absent — users could set them in
  config.toml but couldn't confirm they took effect without
  reading the loader. Budget always renders (with "(unset)"
  labels) so the knob doubles as documentation.
- **`[budget]` cost guardrail.** Two opt-in thresholds:
  `warn_usd` paints a yellow status-bar pill `budget $X/$cap` and
  appends a one-time system block once the cumulative session cost
  crosses it. `hard_usd` blocks further turns with an actionable
  hint; `/budget ack` unblocks for the rest of the session; `/budget
  reset` re-arms the gate. `stado run` maps the hard cap onto
  `AgentLoopOptions.CostCapUSD` and exits 2 with a pointer at the
  config knob. Defaults are 0 (disabled) so local-runner users with
  no cost concerns see no guardrail UI. Misconfigured pairs
  (`hard_usd ≤ warn_usd`) drop the hard cap with a stderr warning.
- **Project-level instructions auto-loader.** Stado now walks up
  from cwd looking for `AGENTS.md` (preferred, cross-vendor
  convention) or `CLAUDE.md` (fallback) and injects the file body
  into every turn as a system prompt. `stado run` prints the
  resolved path to stderr; the TUI sidebar gains an `Instructions`
  row showing the file's basename. Missing file is a silent no-op;
  a broken file becomes a stderr warning — the TUI never fails to
  boot because of an instructions file. Wired into TUI,
  `stado run`, ACP server, and the headless JSON-RPC session.prompt.
  Compaction retains its purpose-specific summarisation prompt —
  only user-facing turns pick up `AGENTS.md`.
- `[tools]` config section lets users trim the bundled tool set.
  `enabled = [...]` acts as an explicit allowlist; `disabled =
  [...]` removes specific tools from the default. When both are
  set, `enabled` wins. Unknown names log a stderr warning and are
  ignored, so configs survive tool renames.
  Applies everywhere the executor is built: TUI, `run`, `headless`,
  and the headless `tools.list` RPC.
- `stado plugin init <name>` — scaffold a new plugin project. Go
  wasip1 template with the full ABI surface (stado_alloc,
  stado_free, stado_tool_*, stado_log import) plus a working demo
  tool. `--dir` relocates; `--force` overwrites. Pairs with
  SECURITY.md's publish cookbook — zero → signed plugin in
  minutes.
- `stado session logs <id> -f` — follow mode live-tails the trace
  ref. Useful for multi-terminal workflows: agent runs in pane 1,
  logs watches in pane 2. `--interval` tunes poll frequency
  (default 500ms).

#### Earlier iterations

Continued polish after the first round of dogfood fixes. Each item
landed independently so the history tells the shape of the
feature set.

- `stado run --session <id>` — continue a long-running session
  from the CLI. Loads the prior conversation, appends the new
  prompt, persists the exchange so the TUI resume picks it up.
  Useful for scripted follow-ups: `stado run --session react
  "what was that hook we extracted?"`. Same id/prefix/description
  resolver as `session resume`.
- `stado session logs <id>` — render the session's trace ref as
  a scannable one-line-per-tool-call feed. Fills the gap between
  `session show` (summary) and `audit export` (JSONL). Shows
  time, tool(arg), summary, tokens, cost, duration, and marks
  errors with ✗. `--limit N` to cap; accepts the same lookup
  resolver.
- `stado config show` — print the resolved effective config
  (TOML + env + defaults merged). Human table by default, `--json`
  for jq. Answers "why is stado using X?" without reading the
  loader. Highlights when `config.toml` doesn't exist yet.
- `stado stats --json` — structured output for dashboards, CI
  gating, jq piping. Shape:
  `{window_days, total{calls,tokens_in,tokens_out,cost_usd},
  total_duration_ms, by_model, by_tool}`. Empty-window case emits
  a valid empty shape so scripts don't special-case.
- Shell-style aliases on frequent subcommands: `session ls` →
  `list`, `session rm` → `delete`, `session cat` → `export`.
- `session list` status column is now colourised — live green,
  idle grey, detached dim. Respects `NO_COLOR` / `FORCE_COLOR` /
  isatty so piped output stays plain.

### UX sweep — dogfood-driven findings (pre-release polish)

**Session management.**

- `stado session describe <id> [text]` — attach a human-readable
  label to a session. Stored in `<worktree>/.stado/description`.
  `--clear` removes; no-text mode prints the current label.
  Surfaces in `session list` (new DESCRIPTION column), `session
  show` (label: line), and the TUI `/sessions` overview.
- `stado session resume <id>` now accepts UUID prefixes (≥8
  chars) and case-insensitive description substrings.
  Ambiguous matches list candidates so you can narrow:
  `stado session resume react` → resolves via description.
- `stado session search <query>` — grep across every session's
  persisted conversation. Case-insensitive substring by default;
  `--regex` switches to RE2. Flags: `--session <id>` to scope,
  `--max N` to cap hits. Output is `session:<id> msg:<n>
  role:<role>  <excerpt>` for easy piping.
- `stado session export <id>` — render the conversation as
  markdown (default) or raw JSONL (`--format jsonl`). `-o
  session.md` writes to a file; otherwise stdout. Markdown has
  per-role headers, fenced tool-call JSON, fenced tool-result
  bodies, thinking blocks as blockquotes (signature stripped).
- `stado session gc [--apply]` — sweep zero-turn, zero-message,
  zero-compaction sessions older than `--older-than` (default
  24h). Dry-run by default; `--apply` to actually delete. Live
  sessions are always skipped.
- `stado session show` now renders a `usage` line summarising
  tool calls, token counts, cost, and wall time for the session.
- `session list` gains a DESCRIPTION column; `Status` values
  refined to `live` / `idle` / `detached` (was `attached`),
  reflecting whether a process actually holds the worktree.

**TUI additions.**

- `@`-file fuzzy autocomplete in the input. Typing `@` opens an
  inline picker of repo files; Up/Down navigate; Tab/Enter
  accepts, replacing the `@query` fragment with the path plus a
  trailing space. Esc closes without changing the buffer. Email-
  style `user@x` deliberately does NOT trigger — the `@` has to
  be at start of input or follow whitespace.
- **Message queuing.** Enter during streaming no longer silently
  drops your message. The user block appears in the chat right
  away; the LLM-facing `msgs` add is deferred to drain so the
  current turn's context isn't mutated. Ctrl+C/Esc with a queue
  pending clears the queue (doesn't also cancel the stream —
  that's the second press).
- Status row surfaces: cumulative cost, cache-hit ratio (when
  non-zero), and a `queued: <excerpt>` pill while something is
  queued.
- `/describe` slash command — mirrors the CLI subcommand:
  `/describe <text>`, `/describe --clear`, or `/describe` alone
  to read back the current label. Sidebar now renders the
  session label under the stado title.
- `/sessions` overview lists sessions with descriptions when set.

**Shell tab-completion** for session IDs on every session
subcommand that takes one: `show`, `attach`, `resume`, `delete`,
`fork`, `describe`, `revert`, `land`, `tree`, `export`.
Descriptions attach as completion hints — `<TAB>` in bash/zsh/fish
shows "id    description" alongside.

### Opencode / Pi gap features

Three features added after researching the top coding-agent CLIs.

- **`stado stats`** — cost + usage dashboard aggregated from the
  git-native audit log (trace-ref trailers). Works offline /
  airgap — no OTel collector required. Flags: `--days` (default
  7), `--session`, `--model`, `--tools` to include a per-tool
  breakdown. Sorted by cost descending.
- **`stado github install`** — writes a
  `.github/workflows/stado-bot.yml` that fires on issue/PR
  comments starting with `@stado`. Runs `stado run --prompt`
  inside the user's Actions runner and posts the reply back via
  `gh api`. `--force` overwrites; `install` / `uninstall` pair is
  idempotent.
- Status-row cache-hit pill. Renders `cache NN%` when the
  provider reports non-zero prompt-cache reads (ratio is
  `CacheReadTokens / (CacheReadTokens + InputTokens)`).

### Plugins + headless

- **Headless plugin surface.** `plugin.list` and `plugin.run`
  JSON-RPC methods; plugin-driven forks emit
  `session.update { kind: "plugin_fork", plugin, reason, child,
  at_turn_ref, childWorktree }`. Background plugins load on
  `Serve()` entry and tick on `session.prompt` completion.
  Closes the deferred K2 line item.
- **Shutdown ordering** in headless. `Conn.WaitPendingExceptCaller`
  lets the `shutdown` handler drain earlier in-flight requests
  before replying, so `plugin.run` responses can't arrive
  *after* the shutdown ACK.
- **`providers.list.current`** now reports the resolved provider
  (previously parroted `cfg.Defaults.Provider` which is blank on
  the local-fallback path).
- **Persistent plugin lifecycle.** Plugins that export
  `stado_plugin_tick` load once per TUI boot via
  `[plugins].background = [...]` and fire on every turn
  boundary.
- Second validating plugin: `plugins/examples/session-recorder/`
  — `session:read` + `fs:read` + `fs:write` + `stado_plugin_tick`.
  Appends JSONL per turn. Proves the ABI generalises beyond
  auto-compaction.
- `stado plugin installed` — list installed plugin IDs (was
  conflated with the trust-store list before).

### Terminal hygiene

- **OSC color-query responses** no longer leak into the
  textarea. Root cause: bubbletea v1.3 has no OSC parser, and
  slow terminals answer `\x1b]11;?` after stado has acquired
  stdin, so the payload lands as Alt-prefixed rune bursts.
  Two-layer fix: byte-level `oscStripReader` that state-machines
  through the response across Read boundaries + `tea.WithFilter`
  backstop for the Alt-wrapped shape. Both removed once
  bubbletea v2 (native OSC parser) lands.
- **Raw-mode regression** from the OSC wrapper — fixed. The
  earlier stripper was a plain `io.Reader`, which made bubbletea's
  `initInput` type assertion (`p.input.(term.File)`) fail: no raw
  mode, no epoll cancel path, so keystrokes echoed to the
  terminal cursor instead of reaching the TUI. New
  `oscStripFile` embeds `*os.File` so `Fd()`/`Write()`/`Close()`/
  `Name()` forward to stdin and bubbletea can still call
  `term.MakeRaw(fd)`. cancelreader's epoll reads via
  `file.Read()` which routes through the filter.
- **Sidebar no longer latches closed** on the first render. View()
  used to flip `sidebarOpen = false` when width was below the
  min threshold — but the very first View() call runs before
  any `WindowSizeMsg` arrives, at width=0, permanently closing
  the sidebar. Now the flag is preserved; only the current-frame
  render is skipped.
- `hack/tmux-uat.sh` — real-PTY harness. Spawns `./stado` in a
  detached tmux session, asserts against the captured pane.
  Orthogonal to teatest: it catches regressions in the termios +
  cancelreader path (the exact path the two fixes above sit on).

### CLI polish

- `stado` in a non-TTY context now exits 1 with an actionable
  pointer to `run --prompt` / `headless` (was exit 0 with a raw
  `/dev/tty: no such device` leak).
- `stado version` and `stado verify` agree — both read the
  shared `collectBuildInfo()`.
- `stado doctor` uses correct pluralisation ("1 check failed"
  vs "2 checks failed").
- `session attach <unknown>` / `session delete <missing>` print
  concise errors (previously wrapped OS stat errors).
- `plugin trust` error explains both unlock paths: out-of-band
  pubkey trust or `plugin verify . --signer <pubkey>` TOFU.
- Provider-fallback message no longer says "no
  ANTHROPIC_API_KEY" — now "no provider configured — using local
  <runner>". Stale from an earlier anthropic-first era.
- Config init template bumped `claude-sonnet-4-5` →
  `claude-sonnet-4-6`.
- Top-level description: "Sandboxed, git-native coding-agent
  runtime" (was "AI CLI harness and editor" — stado is not an
  editor).

### Testing

- 30 UAT scenario tests covering the enumerated user-facing
  flows in `.learnings/UAT_SCENARIOS.md`. 3 in
  `uat_direct_test.go` + 16 in `uat_scenarios_test.go` + 11 in
  `uat_scenarios_extended_test.go`. All direct-Update —
  teatest's virtual terminal fights stado's sidebar+viewport
  layout for reliable snapshot assertions.
- Phase 11.5 PTY harness for interactive `session tree` —
  teatest-backed end-to-end test that navigates + presses `f` to
  fork. Simpler layout, reliable under teatest.

### Infra

- `hack/otel-compose/` — Jaeger-all-in-one compose fixture for
  eyeballing OTel traces during development. Closes Phase 6
  verify.
- Plugin-publish cookbook in SECURITY.md — nine-step guide from
  `gen-key` through rotation.

### Fixes

- Session list's "attached" status now reflects whether a pid is
  actually alive (reads `.stado-pid` + `signal(0)` probe). Was
  "worktree exists on disk" regardless of whether anyone was
  using it.
- Removed the dead `short()` helper from `cmd/stado/session.go`
  that lint caught after an adjacent-file edit triggered a re-
  lint.

---

## Earlier

See `git log --oneline` for pre-changelog history. PLAN.md has the
phase-by-phase roadmap; most ✅ rows there landed before this
changelog started.
