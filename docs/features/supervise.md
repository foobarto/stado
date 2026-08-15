# Supervised work (`/supervise`)

`/supervise` is an opt-in quality gate for long-running or multi-stage work. It
keeps one selected contract in the worker loop, reviews anchored progress with
fresh read-only watchdogs, and accepts completion only after deterministic
checks and an independent verifier.

The workflow is owned by the official `supervise` WASM lifecycle application,
not by native stado code. Its source lives at
[`foobarto/stado-plugins/supervise`](https://github.com/foobarto/stado-plugins/tree/main/supervise).
The current signed plugin release is `supervise/v0.1.1`, with
`min_stado_version: 0.80.0`. Plugin and stado versions are deliberately
independent.

> **Availability:** the official offline-key-signed manifest and WASM are
> published at
> [`supervise/v0.1.1`](https://github.com/foobarto/stado-plugins/releases/tag/supervise/v0.1.1).
> A normal stado install still does not expose `/supervise` by default: the
> package must be explicitly installed, trusted, and enabled. There is no
> native fallback.
>
> `/supervise` appears only when that exact signed, installed
> lifecycle application is explicitly enabled for the session. The interactive
> TUI is the only application host in the current/v1 scope. `stado run`,
> headless JSON-RPC, ACP, ephemeral plugin execution, and child agent loops fail
> closed when asked to compose a lifecycle application.

Supervision is a quality gate, not a security feature. It is designed to catch
stalls, scope drift, forgotten requirements, weak evidence, and premature
completion in ordinary agent work. It does not make hostile repository content
safe, contain a malicious worker, protect secrets, or turn model agreement into
an authorization decision. The broker, sandbox, plugin capability system,
hooks, repository trust, and central operator policy remain the security
boundary.

This is different from `/supervisor`: that command controls EP-0033's
responsive off-band answer lane. It does not plan, monitor, stop, or verify the
main worker.

## Enable and start

Install the package, then explicitly enable the signed application. Merely
having a package on disk does not activate its lifecycle behavior or command.

```sh
stado plugin install --trust-anchor \
  github.com/foobarto/stado-plugins/supervise@v0.1.1
```

In the TUI:

```text
/supervise
/supervise Implement resumable imports without breaking existing callers
/supervise start Implement resumable imports without breaking existing callers
```

The application asks for the objective, watchdog mode (`event` by default or
`live`), plan-pivot policy (`user` by default or `watchdog`), and a
standard/high-assurance/custom scrutiny profile. Advanced choices cover
requirements and verification hints, independent watchdog/verifier provider
and model, provider-native thinking, effort, token budgets, timeouts,
retry/backoff, correction limits, and required/advisory failure posture.

Reviewer budgets are token-only in v1. Thinking and effort are separate:
`thinking=auto|on|off` controls provider reasoning, while
`effort=low|medium|high|xhigh|max` is forwarded when the selected provider
supports it. Provider credentials remain native and are never exposed to the
plugin.

A fresh baseline architect proposes constraints, non-goals, acceptance
criteria, an ordered plan with explicit done conditions, definition of done,
verification, and risks. The application renders that exact proposal and asks
for quality-workflow confirmation. Acceptance selects one exact
session-scoped candidate artifact for this application run. It does not
activate prompt authority, widen plugin capabilities, or authenticate a
security-sensitive operator grant.

## Modes

| Mode | Reviews | Failure behavior |
|------|---------|------------------|
| `event` | Coalesced detector events, plus an optional review every configured N worker turns | A trigger receives three bounded fresh attempts by default. Ten consecutive exhausted triggers pause the standard profile. |
| `live` | Every completed worker turn, plus detector events; optional strict turn barrier | Reviews retry with durable capped backoff. Strict live holds the next worker turn until a current-anchor result exists. |

Activity is not the same as progress. The four-turn stall detector resets only
when the active step changes or the completed-step count advances. New evidence,
a changed tree, or more tool calls remain useful review context, but do not hide
a stalled plan.

Every watchdog and verifier is a fresh bounded child. A typed handoff carries
open concerns and probes without keeping a second unbounded conversation. A
successfully parsed, anchored verdict remains valid even if later provider
cleanup reports an error; cleanup is diagnostic, not verdict authority.

Stale watchdog answers have three deliberately different outcomes:

- stale `continue` or `approve` is discarded;
- stale `correct` is queued as explicitly earlier-anchor advisory steering and
  does not pause the worker;
- stale `pause` or `stop` first acquires a durable scheduling hold and forces a
  fresh watchdog review at the latest anchor. Only that current result may
  release, steer, pause, or stop. If confirmation fails, the run pauses for the
  operator.

This lets useful steering arrive without turning every slow review into a
barrier, while preventing an obsolete stop decision from being ignored or
blindly applied. A current `stop` request is enforced by the broker; model prose
does not stop a loop by itself.

## What the application owns

The signed application owns the choices that another supervision application
could make differently:

- contract setup and exactly one active plan step;
- detector thresholds, cadence, cooldowns, retries, and handoff;
- reviewer/verifier prompts and strict verdict decoding;
- progress, pivot, correction, and completion policy;
- routing immutable operator follow-ups as related work or deferred work;
- recovery of its bounded journal state; and
- token-only watchdog/verifier budgeting.

Its three model-facing tools are dynamically projected only to the exact active
application WorkerRun:

- `supervise__report_progress` records criterion-linked evidence;
- `supervise__request_pivot` proposes a complete replacement baseline; and
- `supervise__request_completion` creates an explicit completion candidate.

The tools are application requests, not authority grants. They do not appear
without the enabled application, and there is no Go-owned command or tool with
the same name waiting underneath.

Plan-only pivots may be accepted by a watchdog when configured. Changes to the
objective, criteria, constraints, or other contract fields return to the
operator's quality confirmation. Destructive changes, push, merge, release,
publish, deploy, secrets, and external commitments remain central security or
operator policy. Supervise does not infer authorization by parsing shell or
`gh` text.

Ordinary follow-up prompts are captured as immutable broker input while the
application owns recurrence. A fresh anchored reviewer classifies each input;
related text is delivered unchanged at a safe worker boundary, while unrelated
or uncertain input becomes durable deferred work. The application cannot
rewrite, drop, or acknowledge an input before selecting a disposition. Use
`/steer <message>` when you explicitly want to affect the current turn.

## What stado owns

Native stado supplies generic primitives a sandboxed application cannot create:
canonical plugin/session identity, immutable tree/turn facts, ordered broker
events, artifact and journal storage, attenuated child admission, mailbox and
input routing, leased scheduling holds, pause/stop/cancel/completion effects,
budget ceilings, audited tool execution, and the WASM sandbox itself.

Review children receive only bounded read/search/context tools over an exact
host-pinned repository source. They get no shell, mutation, ambient network,
credential, approval, or deployment capability. The chosen reviewer provider
does receive the evidence it asks for, so selecting a different provider is a
data-egress trust choice.

## Completion and Verify Work

The worker cannot complete by ending a turn or writing persuasive prose. It
must use `supervise__request_completion` after every criterion and ordered plan
step has advanced.

The application then requests the operator-configured `[verify].commands`
suite through the generic `session:verification:request` bridge. The guest names
the active WorkerRun version and a still-pending source event; it cannot provide
the commands, tree anchor, or executable authority. The broker derives the
exact anchor, and the TUI runs the suite through the ordinary audited executor
only after the source callback acknowledgement is durable.

The resulting `stado.dev/session-verification-facts/v1` event contains bounded
facts and evidence references: suite/command/result digests, typed outcomes and
failure fingerprints, exact trace/tree commits, and terminal broker evidence.
It contains no command or output plaintext and no supervise verdict. A missing
suite is recorded as `no_suite`, never as a pass. Native verification is
at-least-once across the irreducible crash window after a command finishes but
before its terminal WAL append, so verification commands must tolerate retry.

Command facts are only the first gate. The application next asks a watchdog to
review the explicit completion candidate and then uses a separate fresh
verifier over the same contract, anchor, tree, and immutable evidence. Only a
current verifier approval lets the application request broker completion. A
required verifier failure pauses; an advisory failure invalidates the candidate
and resumes work. Neither failure implies success.

## Lifecycle

```text
/supervise status
/supervise resume
/supervise cancel
```

Contract selection, detector history, pending children, leased holds, worker
run state, input dispositions, and event cursors are durable. `resume` retries
the exact interrupted phase; it never resurrects a cancelled, completed, or
stopped WorkerRun. `cancel` is terminal and preserves the audit record. An
inactive installed application acknowledges lifecycle boundaries without
blocking ordinary work; a terminal run becomes dormant only after exact child,
worker, hold, and journal cleanup succeeds.

Nested supervision is not supported. The root worker remains responsible for
its children, while the watchdog can inspect their authenticated lifecycle,
scope, results, diff, and budget facts.

## Evaluation

The comparative scenarios and evaluator live with the official application:

- [`supervise/evals`](https://github.com/foobarto/stado-plugins/tree/main/supervise/evals)
- [`supervise/eval/cmd/supervise-eval`](https://github.com/foobarto/stado-plugins/tree/main/supervise/eval/cmd/supervise-eval)

Build the evaluator from that plugin module, then validate/score with:

```sh
go build -o /tmp/supervise-eval ./eval/cmd/supervise-eval
/tmp/supervise-eval scenario evals/scenarios/retry-thrash.json
/tmp/supervise-eval score --input observations.jsonl
```

The protocol pins both arms and keeps worker, watchdog, and verifier tokens
separate. The scenarios are testable claims, not published evidence that a
second model universally improves work.

For the design rationale and example failure modes, read
[The Loop Needs a Witness](../articles/supervise-in-practice.md). The normative
quality contract is [EP-0062](../eps/0062-harness-enforced-supervised-work.md);
the application/host placement and lifecycle ABI are
[EP-0064](../eps/0064-wasm-lifecycle-applications.md).
