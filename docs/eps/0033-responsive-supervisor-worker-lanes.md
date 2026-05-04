---
ep: 0033
title: Responsive frontline — supervisor + worker lanes
author: Bartosz Ptaszynski
status: Draft
type: Standards
created: 2026-05-04
history:
  - date: 2026-05-04
    status: Draft
    note: Initial draft. Captures the feature ask as user-described, the design space worked out in conversation, and the implementation shape that reuses /btw as the supervisor primitive.
see-also: [0007, 0014, 0032]
---

# EP-0033: Responsive frontline — supervisor + worker lanes

> **Status: Draft.** No code yet. The /btw infrastructure already
> implements every primitive this EP needs; the work is connecting
> them into a persistent supervisor lane plus a small classifier
> head, gated behind an opt-in config block.

## Problem

CLI agents — including stado today — feel **blocking** in a way no
human collaborator does. While a turn is running tools (build,
grep, edit, network call) the user can type but their input just
queues; nothing actually answers until the in-flight turn ends and
the next turn picks up the queued input. Quick clarifying questions
("did the build finish?", "actually skip the migration") cost a
full-turn round-trip even when the answer is on the screen already.

The asymmetry is structural: today there is one agent voice,
serialised against tool I/O. The user's experience is "wait,
queue, wait, queue."

A human collaborator behaves differently. They keep working on the
slow thing while staying ready to answer a side question.
Interrupting them is cheap; redirecting them mid-task is normal.
Stado's `/btw` command (`internal/tui/model_stream.go:221`) is a
single-shot version of this — fire a side question, get a
non-mutating answer streamed back, the main thread keeps running.
This EP **promotes /btw from a one-shot side-channel to an always-on
supervisor lane** so the user is never waiting on the worker to
finish before getting acknowledged.

## Goals

- **Always-responsive frontline.** While a worker turn is grinding
  through tool calls, the user can type and get a streamed reply
  from the supervisor — without waiting on the worker to finish.
- **Supervisor decides queue/steer/interrupt.** Not the user. The
  default for new user input mid-turn is *queue* (pile up after the
  current turn) or *steer* (append guidance the worker reads at its
  next turn boundary). *Interrupt* is reserved for cases where the
  worker is mid-something the user has explicitly contradicted.
- **Honesty over speculation.** Supervisor answers from what's on
  the shared transcript. If the answer isn't there yet, supervisor
  says "still waiting, latest signal was X" — never speculates
  ("probably done by now").
- **Provider-agnostic, top-down.** This is not an ACP-specific
  feature. The session-orchestration layer above `agent.Provider`
  handles the supervisor/worker split; no provider type needs to
  change.
- **Different model per lane.** The supervisor and worker can use
  separate provider entries — separate models, separate API keys,
  separate transports — to support cost-optimised pairings (Haiku
  supervisor + Opus worker), privacy splits (local Ollama
  supervisor + cloud Opus worker), or homogeneous setups.
- **Live model/provider switching per lane.** Mid-session, the user
  can swap the supervisor's or the worker's provider and model
  independently via slash command, without tearing down the session.
  Common pattern: start with a cheap supervisor, upgrade to a
  larger one when the conversation gets complex.
- **Opt-in.** Off by default. The doubled API load against a single
  rate-limit bucket (when supervisor and worker share a provider)
  is a real failure mode users must opt into knowingly.
- **Reuse `/split`.** When supervisor mode and `/split` are both on,
  the existing pane partition reclassifies so worker activity goes
  to the top pane and the supervisor conversation owns the bottom.

## Non-goals

- **No real-time conversation merging.** The shared transcript is
  append-only. There is no 3-way merge of divergent histories. If
  the supervisor answers something while the worker is mid-turn, the
  worker reads the new transcript at its next turn boundary, not
  mid-tool-call.
- **No mid-tool-call interrupts of the worker.** Worker turns are
  atomic at tool-call granularity. Cancellation can happen at the
  next tool-call boundary; mid-call cancellation is a separate
  feature gated by tool-side SIGINT support.
- **No automatic supervisor ↔ worker role swap.** A turn is either
  supervisor-class (read-only tools, `ClassNonMutating`) or
  worker-class (full registry). The classifier decides which lane
  handles a user input but does not promote the supervisor to
  worker mid-session.
- **Not a multi-worker fleet.** This EP defines exactly two lanes:
  one supervisor, one worker. Multi-worker (e.g. parallel research
  + implementation) is a separable design, possibly EP-0034.
- **No rewrite of `agent.Provider`.** Providers continue to
  implement `StreamTurn` exactly as they do today; this is a
  session-orchestration concern, not a provider concern.

## Design

### Architectural placement

Supervisor/worker is a **session-orchestration concept** sitting
above the provider abstraction. The work is in `internal/tui` plus
a new `internal/session/lanes` package that owns the shared
transcript and the classifier head. No `pkg/agent` interface
changes; no provider implementation changes.

```
            +--------------------+        +-----------------------+
   user --> |  classifier head   |  -->   |  supervisor lane       |
            |  (small policy)    |        |  provider.StreamTurn   |
            +--------------------+        +-----------------------+
                       |                              |
                       v                              v
            +--------------------+        +-----------------------+
            |  worker queue      |  -->   |  worker lane           |
            |  (steer/queue ops) |        |  provider.StreamTurn   |
            +--------------------+        +-----------------------+
                       \                             /
                        \                           /
                         v                         v
                    +-----------------------------------+
                    |  shared append-only transcript    |
                    |  (single source of truth)         |
                    +-----------------------------------+
```

### Shared transcript — append-only, two writers

LLM context is a token sequence + a KV cache, not a git tree.
Divergent histories don't 3-way merge. The model that works:

- **One transcript per session.** Append-only.
- **Two writers.** Worker writes its turn outputs (text deltas, tool
  calls, tool results) chronologically. Supervisor writes its
  side-replies tagged with a distinct `block.kind` so they don't
  pollute the worker's prompt rebuild.
- **Each lane reads at its own turn boundaries.** When the worker
  re-prompts after a tool result, it reads the transcript up to that
  moment — including any supervisor turns and queued user inputs
  that landed in the meantime. When the supervisor takes a turn, it
  reads the same transcript including the worker's in-flight stream.
- **No fork, no rebase.** Just chronological append + role markers.
  This is the same shape as a Slack channel where two people type
  concurrently — order is wall-clock, no merging.

The supervisor's side-replies are **excluded from the worker's
prompt rebuild.** Worker only sees its own turns + user turns + tool
results, never supervisor chatter. This is the same "don't pollute
the main `m.msgs`" invariant that `/btw` already enforces today.

### Supervisor as evolved `/btw`

Today `/btw` (`internal/tui/model_stream.go:221`):

- Snapshots `m.msgs` at fire time.
- Streams a non-mutating reply (`ClassNonMutating` tool registry only).
- Does NOT mutate `m.msgs` — the side query and reply land in a
  visually distinct `block{kind: "btw"}` lane.

The supervisor lane is `/btw` with the snapshot replaced by a **live
tail** of the shared transcript and an **inbox** that receives user
inputs classified as supervisor-bound. Reuses:

- The `ClassNonMutating` tool-rights model (no risk of supervisor
  mutations clashing with worker).
- The streaming machinery (`provider.StreamTurn` event channel,
  `EvTextDelta`).
- The visual lane (`block{kind: "btw"}` or a new sibling kind, TBD).
- The "don't pollute worker prompt" invariant.

The novel pieces narrow to:

1. **Live tail instead of snapshot.** Worker's `EvTextDelta` events
   tee into the shared transcript so supervisor's tail sees them as
   they land.
2. **Classifier head.** Each user input runs through a small policy
   model that picks `{answer-from-context, queue, steer, interrupt}`.
3. **Worker queue.** Operations from the classifier (steer / queue)
   land on a queue the worker drains at turn boundaries.

### Classifier head

A small prompt run on the supervisor's provider before each user
input is dispatched. Classifies into four buckets:

| Bucket               | Action                                                    | Default bias |
|----------------------|-----------------------------------------------------------|--------------|
| `answer-from-context`| Supervisor answers directly from the shared transcript.   | Common       |
| `queue`              | Append to worker's input queue; worker reads at next turn.| Common       |
| `steer`              | Append a steering note to transcript; worker reads inline.| Common       |
| `interrupt`          | Cancel worker at next tool-call boundary, then queue.     | Rare         |

Default bias is heavily toward `queue` and `steer`. `interrupt` is
reserved for cases where the user has explicitly contradicted the
worker's in-flight direction, or where the worker is about to do
something destructive the user wants stopped.

The classifier is itself a tuning surface. The EP ships with two
reference prompts — one for small-fast tier (Haiku, Gemini Flash,
Llama-7B) and one for large-thinking tier (Opus, Gemini Pro,
GPT-4o). Users running other supervisor models tune the classifier
prompt as a config-level prompt knob.

### Honesty hierarchy

Supervisor system prompt enforces, in priority order:

1. **Full info if available.** Answer from the shared transcript
   when the answer is on it.
2. **Partial info with confidence label.** "Worker's last tool
   output 30s ago was the build starting; haven't seen the
   build-finished marker yet."
3. **Just-say-waiting.** "Worker still on it, no signal yet on your
   question."

Speculation from priors is forbidden. "Probably done by now" / "I
think it succeeded" are not allowed responses. The supervisor's
prompt includes negative examples to anchor this.

This is the most important invariant of the design. If the
supervisor speculates, the responsive frontline collapses to "two
voices, both half-informed, neither helpful" — strictly worse than
the current single-blocking-voice.

### Worker streaming requirement

Both lanes must stream progressively, not deliver whole-turn-at-end.

- **Worker without progressive streaming** → supervisor sees nothing
  until worker turn ends → supervisor's "still waiting" becomes the
  only honest answer it can ever give → feature degenerates.
- **Supervisor without progressive streaming** → the responsive feel
  collapses; user types and waits 5s for a reply.

Session start validates `Capabilities().SupportsStreaming` on both
provider entries. If either fails, supervisor mode refuses to enable
with a clear error pointing at the offending provider config.

For ACP-wrapped providers (EP-0032), worker streaming is supplied by
the wrapped agent's `session/update` notifications — already
plumbed in `internal/acp/client.go` for phase A. No additional work
on the ACP side.

### Configuration

```toml
[supervisor]
enabled  = true                # off by default
provider = "anthropic-haiku"   # references a [providers.<name>] entry
model    = "claude-haiku-4-5"  # optional override of the provider default

[providers.anthropic-haiku]
type          = "anthropic"
api_key       = "$ANTHROPIC_API_KEY"
default_model = "claude-haiku-4-5"

[providers.anthropic-opus]
type          = "anthropic"
api_key       = "$ANTHROPIC_API_KEY"
default_model = "claude-opus-4-7"

[defaults]
provider = "anthropic-opus"    # the worker
```

Four supported pairings, no special-casing:

- **Same provider, same model** — testing / single-model debugging.
- **Same provider, different model** — Anthropic Opus worker +
  Anthropic Haiku supervisor. The "cost relief" path; ~1.05× total
  spend vs ~2× for same-model pairing.
- **Different provider, different model** — local Ollama supervisor
  + cloud Opus worker. Privacy-sensitive use case where the
  classifier and quick replies stay on-device.
- **Different provider, same family** — Anthropic supervisor +
  ACP-wrapped Claude Code worker (the supervisor reads the wrapped
  worker's stream, classifier runs on raw Anthropic).

If `[supervisor]` is omitted or `enabled = false`, stado behaves
exactly as today.

### Live mid-session switching

Config sets defaults; slash commands switch lanes live without
session teardown.

| Command                                     | Effect                                                      |
|---------------------------------------------|-------------------------------------------------------------|
| `/provider <name>`                          | Switches the **worker** provider (back-compat with today).  |
| `/provider worker <name>`                   | Same as above, explicit lane.                               |
| `/provider supervisor <name>`               | Switches the **supervisor** provider.                       |
| `/model <name>`                             | Switches the **worker** model on its current provider.      |
| `/model worker <name>`                      | Same as above, explicit lane.                               |
| `/model supervisor <name>`                  | Switches the **supervisor** model on its current provider.  |
| `/supervisor on` / `/supervisor off`        | Toggles supervisor mode for this session.                   |
| `/supervisor status`                        | Prints the current supervisor lane config + classifier stats.|

The lane-aware form (`worker|supervisor`) is optional for `/provider`
and `/model` to preserve back-compat with today's `/provider <name>`.
Without supervisor mode enabled, the lane argument is rejected with
a hint pointing at `/supervisor on`.

Switch semantics — what happens to in-flight state when the lane
changes:

- **Worker switch mid-turn:** the in-flight worker turn finishes on
  the old provider. The next turn (queued user input or post-tool
  re-prompt) starts on the new provider. Tool-call state survives
  the switch unchanged; only the LLM endpoint changes.
- **Supervisor switch mid-turn:** the in-flight supervisor reply is
  cancelled cleanly (supervisor turns are read-only and short, so
  cancel-and-restart is cheap). The next supervisor turn uses the
  new provider.
- **Capability re-validation:** on switch, the new provider's
  `Capabilities().SupportsStreaming` is checked. If it fails, the
  switch is rejected with the same clear error as session-start
  validation, and the lane stays on the previous provider.
- **Transcript carry-over:** the shared transcript is unaffected.
  The new provider on a lane reads the same transcript the old
  provider was reading, with the appropriate per-lane visibility
  rules (worker excludes supervisor blocks, supervisor sees
  everything).

Combined with `/agent` (existing) for switching the supervisor's
**system prompt** profile (cost-relief profile vs full-capability
profile), the user has three orthogonal knobs per lane: provider,
model, prompt — all live-switchable. The provider and model live in
this EP; the agent/prompt switching reuses today's `/agent`
machinery.

### `/split` pane reclassification

Existing `/split` partition (`internal/tui/model_commands.go:193`):

- Today: activity (tools + system) on top, conversation (assistant
  text + user) on bottom.

When supervisor mode is enabled, the routing rules change:

| Mode                                       | Top pane                                                  | Bottom pane                            |
|--------------------------------------------|-----------------------------------------------------------|----------------------------------------|
| `/split` on, supervisor off (today)        | tool calls + system                                       | assistant text + user                  |
| `/split` on, supervisor on (this EP)       | worker tool calls + worker thinking + worker text         | supervisor responses + user            |
| `/split` off, supervisor on (this EP)      | one pane, worker stream and supervisor responses interleaved by wall-clock, distinguished by `block.kind` styling | —                                      |
| `/split` off, supervisor off (today)       | one pane                                                  | —                                      |

No new TUI primitives. The pane partition mechanism already exists;
the EP just adds new routing rules for which `block.kind` lands in
which pane when supervisor mode is on.

The intended UX with `/split` on + supervisor on: the bottom pane
becomes "talk to the responsive frontline," the top pane becomes
"watch the worker grind." Glance-only access to detail without the
conversation feeling cluttered.

## Migration / rollout

Phased to keep blast radius contained.

**Phase A — single-model supervisor.** `[supervisor]` block accepts
only `enabled = true|false`; supervisor uses the worker's provider
config with a fixed system prompt. Validates the responsiveness UX,
the classifier prompt, the transcript-tail mechanics, the `/split`
reclassification — without dual-provider config complexity. Ship as
opt-in feature flag in a vX.Y.0 release.

**Phase B — distinct provider/model per lane.** Adds the full
`[supervisor] provider = "..."` + optional `model = "..."` config.
Adds capability validation at session start. Ships in a follow-up
release once phase A has bake time on the user's daily-driver
config.

**Phase C — `interrupt` classification.** Phases A and B ship with
the classifier emitting only `{answer-from-context, queue, steer}`.
The `interrupt` bucket is the riskiest path (cancel worker
mid-flight, requires tool-call-boundary cancellation hooks); shipped
last after the queue/steer primitives have proven reliable.

Each phase lands behind a config gate (`[supervisor] enabled =
true`) so users on default config see no behaviour change.

## Failure modes

- **Rate-limit exhaustion (same-provider pairing).** Two concurrent
  `StreamTurn` calls against one API token bucket. Mitigation:
  per-direction rate-limit budgets (already exist post-`255c28a`),
  supervisor backoff under contention (supervisor is the polite
  voice; under 429 the worker keeps going, supervisor stops typing).
  Surfaced via the existing budget UX.
- **Supervisor speculates anyway.** Despite prompt discipline, a
  small model may invent answers. Mitigation: classifier prompt
  includes negative examples; output-side guardrail can scan
  supervisor responses for tells like "probably" / "I think" /
  "should be" and replace with a deterministic "still waiting"
  fallback. (Probably overkill for phase A; revisit if it becomes a
  real problem in dogfood.)
- **Classifier mis-routes.** User says "stop" expecting interrupt,
  classifier emits `queue`. Mitigation: explicit slash commands
  (`/stop`, `/redirect`) bypass the classifier and go directly to
  the worker queue with the requested op. Classifier handles the
  natural-language path; users have a deterministic escape hatch.
- **Worker emits no progressive deltas.** Provider says it streams
  but actually buffers. Mitigation: a watchdog timer at session
  start sends a `say "hello"` smoke turn and verifies deltas arrive
  before the final marker. If the provider fails the smoke test,
  refuse to enable supervisor mode with a clear error.
- **Two-voice UX confuses users.** Without strong visual
  distinction users won't know which lane answered. Mitigation:
  separate `block.kind` for supervisor responses with a distinct
  colour and a left-margin marker (`▎sup`). Document in
  `/help supervisor`.
- **Worker output never reaches transcript-tail consumers.**
  Streaming events plumbed through `internal/tui/model_stream.go`
  but not exposed to the supervisor lane. Mitigation: the tee
  point is `EvTextDelta` arrival in `Model.Update`; the same event
  fans out to the existing render path AND the supervisor's tail
  channel. One-line addition; covered by integration test.

## Test strategy

- **Unit:** classifier prompt over a fixture set of user inputs,
  verifying bucket assignment matches the expected distribution
  (heavy toward queue/steer, rare interrupt).
- **Unit:** transcript writer round-trip — supervisor turn appended,
  worker prompt rebuild excludes supervisor blocks, worker turn
  appended, supervisor tail sees both.
- **Unit:** capability validation rejects non-streaming providers
  for both lanes at session start.
- **Integration:** end-to-end with a slow tool (3-second sleep). User
  fires a "are you still working?" mid-tool. Supervisor responds
  with "yes, last signal 1.2s ago, still in tool X" — verified
  against expected response shape via prompt-test fixtures.
- **Integration:** redirect flow. User says "actually skip the
  migration" mid-codegen. Classifier emits `steer`. Worker reads the
  steering note at its next turn boundary and adjusts. Verified by
  asserting the second worker turn's plan reflects the steer.
- **TUI:** `/split` + supervisor mode pane routing — worker text
  appears in top pane, supervisor responses in bottom pane.
  Existing UAT scenario test framework (`uat_scenarios_test.go`)
  covers this.
- **Cost-budget:** with same-provider Haiku/Opus pairing, dogfood a
  realistic 30-minute session and measure token spend vs a
  baseline single-Opus session. Target: ≤ 1.10× baseline.

## Open questions

- **Block kind for supervisor responses.** Reuse `btw` (already
  rendered with distinct styling) or introduce `supervisor` as a
  sibling? Probably introduce a sibling — supervisor turns are
  ongoing, not one-shot; conflating them with `/btw` would muddy
  the meaning of the block kind. Leaning toward
  `block{kind: "supervisor"}` reusing `/btw` styling for v1.
- **Classifier model independence.** Should the classifier use the
  supervisor's provider, or always use the small-fast tier even
  when the supervisor is large? Leaning supervisor's provider for
  config simplicity; revisit if classification quality varies.
- **Supervisor's tool budget.** Today `/btw` runs on
  `ClassNonMutating` and has access to read/grep/glob/etc. Does the
  supervisor get the same? Probably yes. Should it have a
  per-supervisor-turn token budget to prevent runaway? Probably
  yes; the existing `[budget]` config is the natural mechanism.
- **`/split` on by default in supervisor mode?** When the user opts
  into supervisor mode, the `/split` partition is the better UX. Do
  we auto-enable it, or leave the toggle to the user? Leaning
  user-toggle for surprise minimisation; document as a recommended
  pairing in `/help supervisor`.

## Decision log

### D1. Append-only shared transcript, not git-style fork+merge

- **Decided:** Single chronological transcript with two writers,
  each reading at its own turn boundaries. No fork, no rebase.
- **Alternatives:** Git-tree-style context with cheap forks and
  merge-on-rejoin. KV-cache fork via prompt-prefix sharing with
  divergent suffixes.
- **Why:** LLM context is a token sequence + KV cache; divergent
  edits don't 3-way merge because the worker's intermediate
  decisions are baked into its outputs. The Slack-channel model
  (chronological log, two writers, role markers) maps directly to
  what the LLMs need without forcing a merge step that has no
  defined semantics.

### D2. Supervisor uses `ClassNonMutating` tools (same as `/btw`)

- **Decided:** Supervisor lane has access only to read/grep/glob/
  etc. — no Bash, Edit, Write, or other mutating tools.
- **Alternatives:** Full registry; Plan-mode's specific allowlist;
  fully tool-less supervisor.
- **Why:** Eliminates supervisor↔worker mutation conflicts by
  construction. The supervisor's job is to read the transcript and
  answer / classify, not to take action — actions go through the
  worker queue. Reuses the existing `/btw` rights model so no new
  policy machinery.

### D3. Provider-agnostic, top-down; no `agent.Provider` changes

- **Decided:** Supervisor/worker split lives in stado-core
  orchestration above `agent.Provider`. All provider types (native
  SDK, exec:bash plugin, ACP-wrapped) participate without changes.
- **Alternatives:** Provider-internal supervisor (each provider
  implements its own dual-stream); ACP-only feature; new
  `Provider` interface variant.
- **Why:** The provider abstraction stays simple — one job, stream
  a turn. Orchestration is a stado concern. Every provider type
  inherits the feature for free as long as it streams progressively.
  The alternative — provider-internal supervisor — fragments the
  contract and creates per-provider variance for the user.

### D4. Opt-in, off by default

- **Decided:** `[supervisor] enabled = false` default. Users must
  knowingly opt in.
- **Alternatives:** On by default with cost guardrails; on by
  default for specific provider pairings (Haiku/Opus); progressive
  rollout to a fraction of sessions.
- **Why:** Doubled API load against a single rate-limit bucket
  (when supervisor and worker share a provider) is a real failure
  mode. The cost ratio depends heavily on the chosen pair (1.05× to
  2×). Users should choose this consciously, especially the same-
  provider same-model pairing where the tax is highest. Default-on
  would erode trust in stado as a "knows what it's doing"
  cost-aware tool.

### D5. Supervisor decides queue/steer/interrupt, not the user

- **Decided:** Classifier head picks the routing for each user input.
  User has explicit `/stop` / `/redirect` slash-command escape
  hatches.
- **Alternatives:** User picks via UI affordance (e.g. modifier key
  for "interrupt" vs "queue"); always queue, never interrupt;
  always steer.
- **Why:** The user's stated preference is "mostly queue/steer,
  decide with intelligence." Asking the user to classify each
  message defeats the responsiveness goal — typing turns into a
  multi-modal decision tree. The classifier handles the natural-
  language path; slash commands handle the deterministic one. Same
  pattern as `/btw` vs implicit BTW-mode-toggle keybindings today.

### D6. Distinct provider entries per lane

- **Decided:** `[supervisor] provider = "..."` references a named
  `[providers.<name>]` entry, independent of `[defaults] provider`.
  Optional `model` override.
- **Alternatives:** Supervisor always uses worker's provider
  (model-only override); supervisor inlined into provider config
  (no separate provider entry needed).
- **Why:** Enables the four pairings (same/same, same/different
  model, different provider, mixed family) without special-casing.
  Cost-relief use case (Haiku supervisor + Opus worker) and
  privacy-split use case (local supervisor + cloud worker) both
  flow from the same config shape. Inlining would couple them and
  preclude provider-mix.

### D7. `/split` reclassifies routing under supervisor mode

- **Decided:** When supervisor mode is on and `/split` is on,
  worker activity → top pane, supervisor responses + user → bottom
  pane. Reuses the existing `splitView` partition; adds new
  `block.kind` → pane routing rules.
- **Alternatives:** New three-pane TUI mode for supervisor; force
  user into a non-split mode under supervisor; leave routing as
  today and accept that worker stream + supervisor responses
  interleave.
- **Why:** The existing two-pane partition maps cleanly to the
  primary use case ("talk to frontline, glance at worker"). No new
  TUI primitives. Three panes would be over-design for v1; flat
  interleave loses the responsiveness affordance the feature is
  trying to provide.

### D8. Live lane switching via lane-aware slash commands

- **Decided:** `/provider` and `/model` accept an optional first
  argument `worker|supervisor`. Without it, default to worker for
  back-compat. Supervisor switch cancels any in-flight supervisor
  turn; worker switch lets the in-flight worker turn finish on the
  old provider.
- **Alternatives:** Two parallel commands (`/provider` for worker,
  `/supervisor-provider` for supervisor); short prefix variants
  (`/sup-provider`); modifier-key affordance (e.g. shift+`/provider`
  picks supervisor).
- **Why:** One mental model, one completion flow, full back-compat.
  Today's `/provider <name>` keeps working unchanged; the supervisor
  variant is one extra word. Two parallel commands fragment the
  surface and force users to remember which command targets which
  lane. Modifier keys are invisible to new users and don't compose
  with autocomplete. Capability re-validation on switch (rejecting
  non-streaming providers) reuses session-start logic so the path
  is symmetric — no per-switch surprise.

### D9. Honesty hierarchy: full info → partial info → "still waiting"

- **Decided:** Supervisor prompt enforces a strict hierarchy of
  answer types, with speculation from priors explicitly forbidden.
- **Alternatives:** Allow "I think probably X" speculation when
  prior evidence is strong; defer all uncertain answers to
  "waiting"; let the model's natural calibration handle it.
- **Why:** If the supervisor speculates, two voices both
  half-informed is strictly worse than the current single-blocking
  voice. The user explicitly stated this preference; capturing it
  as a load-bearing prompt invariant is the right level of
  rigour. Honest "still waiting" preserves the supervisor's value
  as a known-good signal of session state.

## Related

- [EP-0007: Conversation State and Compaction](./0007-conversation-state-and-compaction.md)
  — defines the transcript model that the shared append-only
  transcript extends.
- [EP-0014: Multi-Session TUI](./0014-multi-session-tui.md) — TUI
  framework that hosts the supervisor and worker lanes.
- [EP-0032: ACP client — wrap external coding-agent CLIs as stado
  providers](./0032-acp-client-wrap-external-agents.md) — ACP-wrapped
  providers participate in supervisor mode via the same
  provider-agnostic interface; no ACP-specific work required.
- `/btw` infrastructure: `internal/tui/model_stream.go:221` —
  `startBtw`, the canonical reference primitive for the supervisor
  lane.
- `/split` infrastructure: `internal/tui/model_commands.go:193` —
  the pane partition mechanism this EP reclassifies.
