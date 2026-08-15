## v0.80.0 — corrective release (unreleased)

### UX / CLI / TUI

- **`/supervise` is an application-owned quality gate, not a native command.**
  The native PR-257 workflow and evaluator were removed after the architecture
  audit. The official WASM lifecycle application under
  `foobarto/stado-plugins/supervise` now owns the wizard, contract, cadence,
  detectors, reviewer/verifier prompts, retries, stale-verdict policy, pivots,
  input routing, and completion policy. The command and three worker tools are
  projected only when that exact signed installed application is explicitly
  enabled in the TUI. There is no native fallback, and other surfaces fail
  closed rather than composing a partial lifecycle host.
- **The release is not available yet.** The complete source and evaluator are
  preserved in a signed local `stado-plugins` commit. The intended first package
  is `supervise/v0.1.0` with `min_stado_version: 0.80.0`; its plugin
  manifest/artifact has not yet been signed with the official offline key or
  published. This entry does not claim that `/supervise` is currently present
  in ordinary installations.
- **Slow reviews have explicit semantics.** Earlier-anchor approvals are
  discarded, earlier-anchor corrections remain labelled advisory steering,
  and earlier-anchor pause/stop results acquire a durable hold and require a
  fresh current-anchor review. Activity alone does not reset the four-turn
  plan-progress stall detector, and a valid parsed verdict survives later
  provider cleanup errors.

### Runtime

- **Model-invocable skills moved out of native stado.** The native
  `skills__load` registration, system-prompt catalog, per-surface autoload and
  result handlers, body trimming, and synthetic user-role injection are gone.
  Exact operation/kind-scoped context-resource imports expose only bounded,
  digest-fenced host observations with native model-visibility, project
  fail-closed, persona provenance, and exact session-ceiling projection. The
  explicit official `skills` WASM source owns search, matching, open, output
  formatting, and generic atomic allowed-tool activation. Loaded Markdown is a
  labeled ordinary tool result across run/TUI/headless/ACP/subagents; operator
  `/skill`, slash, and `--skill` remain native gestures. Exact signed per-tool
  capability subsets keep search catalog-only while load receives open and
  surface-edit authority; installed, override, bundled, and nested dispatch
  enforce the same attenuation. The staged development
  build is reproducible but unsigned/unpublished pending the offline release
  key.

- **Provider access is now a primitive, not a native tool.** The MCP-only
  `llm.invoke` implementation, persona flag, old application-shaped host import,
  and optional/default capability are gone. Exact
  `provider:invoke:<positive-tokens>` now exposes only operator-provider
  construction, credentials, cancellation, cumulative input+output accounting,
  and bounded versioned facts to WASM. The official explicit-opt-in
  `llm-invoke` source owns the tool schema/request/output policy; bundled
  auto-compact uses the same primitive. Cleanup diagnostics remain separate
  from completed text, cache counters are not double-counted, and no USD budget
  enters the ABI. The signed token ceiling is hard: conservative input bounds
  can refuse before dispatch, reported or estimated overruns commit their usage
  but cross the ABI only as `failed/token_budget` with no text, and both the
  official consumer and auto-compact enforce the same terminal fact bound.
- **Registry discovery and surface editing moved out of native applications.**
  A loader-bound, digest-fenced catalog exposes bounded factual pages, while a
  generic session controller applies one validated atomic activation/
  deactivation batch. The explicit official `tool-registry` source owns search,
  describe, grouping, package load, and unload policy. Pages remain capped at
  64; atomic package edits support the signed 4096-tool ceiling and reject a
  4097th name without partial mutation.
- **Installed tool execution retains the exact selected package.** CLI and TUI
  dispatch resolve manifest, canonical identity, and WASM path from the exact
  registry-selected adapter. The former process-global name map is gone, so
  concurrent registries or isolated roots cannot cross-wire same-named signed
  packages.
- **Lifecycle applications use generic authenticated primitives.** The TUI
  provides persistent serialized command/tool/hook/event composition, pinned
  source facts, broker journals and artifacts, immutable operator-input routing,
  retained reviewer admission, leased holds, pause/stop/cancel/completion, and
  application-owned WorkerRuns. Stado exposes facts and enforces effects; it
  does not interpret supervise policy.
- **Agent terminal identity is unambiguous across application boundaries.**
  Spawn and `agent.down` facts now keep the Fleet control handle (`agent_id`)
  distinct from the child conversation/tree coordinate (`session_id`), and an
  omitted execution mode is normalized to the documented `wait` default before
  admission. The official supervise PTY gate now exercises first-action setup,
  a real fresh baseline child, operator rejection and confirmation of its
  proposal, exact WorkerRun activation, live-turn cancellation, durable
  terminal-status recovery, the anchorless first iteration, an exact anchored
  progress claim, a fresh pinned watchdog, current-anchor-only plan advancement,
  and the no-parent-follow-up review barrier. Failed children retain identical
  terminal metadata in `agent.down` and later `agent:read`; application cleanup
  no longer mislabels an already interrupted run as a cancellation handoff.
  Another PTY gate captures ordinary input during a live worker request,
  routes it through the signed application's fresh pinned classifier, proves it
  cannot steer that in-flight request, and delivers the immutable original only
  after the assistant boundary to the next recurrence.
  A further PTY gate proves a structured pivot remains a proposal across a
  byte-identical first/replayed reviewer spawn, exact pinned watchdog review,
  and recommendation-only status; only explicit operator confirmation commits
  the artifact CAS, re-anchors the plan, and releases revised recurrence. Pivot
  panel identifiers are deterministically bounded to the host UI ABI.
  Lifecycle callback failures now preserve bounded
  guest error text, persistent WASI modules observe real wall/monotonic clocks,
  and broker timer bounds remain authoritative.
- **Guidance policy moved out of native stado.** The old Go builder and its
  run/TUI/headless/ACP/subagent prompt callbacks are removed. A TUI lifecycle
  application can read only bounded opaque-binding session facts and the live
  registry ceiling, then append one bounded pre-LLM advisory section through a
  distinct non-denying capability. Failure-open traps or malformed responses
  contribute nothing even under global Lua fail-closed. Reproducible unsigned
  official `guidance` source owns classifiers, thresholds, ordering, and
  wording; offline signing and publication remain release work.
- **Verify Work has a generic asynchronous facts boundary.** An application may
  name one pending turn event and exact WorkerRun version, but never commands or
  an anchor. The broker derives the immutable source, the TUI executes only the
  operator-configured suite through the audited executor, and
  `session.verification_finished` returns bounded result/evidence digests with
  no command/output plaintext or native supervise verdict. `no_suite` is absence,
  not success, and the command suite is at-least-once across the terminal-WAL
  crash window.
- **Official supervise completion now passes its live cross-boundary gate.** A
  real ephemeral-sign/install PTY executes operator-owned Verify Work at the
  exact source-content tree, admits a watchdog review, runs a separate fresh
  verifier, records the full authenticated broker completion fact, and releases
  the exact quality hold. Published lifecycle boundaries now consume their
  iteration even when scheduling immediately pauses; audit-only Git commits do
  not stale unchanged verification content; broker lifecycle IDs remain
  distinct from logical Git subjects; and worker-terminal verification
  cancellation cannot manufacture a second pause.
- **Lifecycle application state survives explicit cold session resume.**
  Durable broker adoption now uses the logical session sidecar's canonical
  user-repository root instead of the mutable process launch cwd. A live
  supervise PTY pauses an exact WorkerRun, reloads its application
  transactionally, resumes the same Git session in a new process, re-adopts the
  same broker journal and run projection, proves no recurrence was duplicated,
  and then performs exact terminal cleanup.
- **Automatic compaction preserves the exact supervised recurrence.** Context
  overflow now binds the loaded auto-compact application's authenticated
  canonical identity, hands the complete broker scope to its direct child, and
  reconciles the existing WorkerRun once instead of replaying its prompt as an
  ordinary turn. Failed recovery restores the prompt and durably cancels the
  source recurrence. An ephemeral-sign/install PTY proves child rebind,
  child-anchored progress review, interrupted status recovery, and cleanup
  without duplicate or bypass execution.

- **Tasks moved to one explicit lifecycle application.** The native JSON store,
  model tool, TUI picker/key/static command, MCP registration, task-specific
  default instructions, and default autoload were deleted. The unsigned
  official `tasks` source owns dynamic `/tasks`, a Do-only session tool, generic
  choice UX, global broker artifacts, and logical tombstones. Its one-way legacy
  importer verifies every immutable artifact ref and a byte-exact archive before
  replacing `tasks.json` with a receipt; files above the exact 16 MiB guest-read
  ceiling fail closed. Signing and publication remain release work.

- **Research moved behind generic evidence receipts.** Native research tools,
  private provider loops, corpus adapters, and direct session-WAL readers were
  deleted. Broker evidence catalog/search/open/validate imports now bind exact
  application and child identities, immutable opened ranges, bounded receipts,
  and mechanically checked citations without claiming entailment. The staged
  unsigned official `research` source owns the parent workflow and exact
  child-only helpers; it is not silently bundled or available until separately
  signed, published, and enabled.

- **Memory and learning no longer have a native fallback.** The native stores,
  prompt injection, adaptive policy, CLI/TUI commands, review UI, and
  cross-surface composition were deleted. The staged unsigned TUI lifecycle
  package owns dynamic `/memory` and `/learn`, candidate-only review, bounded
  context contribution, and a one-way broker-owned migration that preserves
  previously granted historical authority. Fresh candidates cannot become
  active until a separately trusted EP-59 presenter exists; provider reply loss
  remains an explicit ambiguous failure and truncated recovery fails closed.

### Plugins / trust

- **Installed package identity is source-exact and host-receipted.** Flat
  display-name directories, name-derived trust compatibility, unsafe custom
  bundles, and display-name dependency authority were removed. Source-derived
  store keys, host-owned install receipts, exact owner/signer pins, separate
  source revision and package version, per-package rollback floors, locked
  cross-process trust/lock updates, strict active markers, minimum-host gates,
  package-atomic tool collision checks, and uniform CRL/Rekor verification now
  govern install, reopen, dispatch, update, remove, and GC. Friendly names are
  usability aliases only and ambiguity fails closed.

### Dependencies

- **Go 1.26.6 is now the minimum and release toolchain.** This patch release
  fixes the standard-library vulnerabilities reported by the release gate in
  `net/url`, `crypto/tls`, `encoding/xml`, and `encoding/asn1`.

### Docs

- **The repeated architecture audits corrected current-state drift.** Two broad
  passes fixed the source and forward ledger, but later C92-C100 conformance and
  boundary corrections changed the candidate. PLAN therefore retains a
  terminal implementation-versus-EP repeat after conformance and live review,
  before signing, publication, merge, or release. README, DESIGN, the threat
  model, static page, slash reference, and adaptive-context article no longer
  advertise deleted native workflows or
  unimplemented adaptive shadowing. EP-0028 now records its surviving
  HOME/XDG resolver contract as Implemented, while EP-0046 keeps cost
  observational and enforces only turn/token budgets. EP-0002 now names
  v0.80.0 as its implementation release, and a generic EP check requires that
  metadata for every Implemented Standards EP. Architecture and EP tests freeze
  the confirmed drift classes.

- **EP-0062/64 document the corrected supervise boundary.** Feature, security,
  command, narrative, UAT, and evaluation references now point at the official
  plugin source and distinguish a signed source checkpoint from a signed and
  published plugin package.

- **WASM plugins join the feature reel.** Promoted stado's replaceable
  plugin-first tool architecture, capability-gated WASM runtime, shared ABI,
  and distinct embedded-bundled versus signed-installed trust paths in the
  README and static landing page. The native registration debt is now empty.

