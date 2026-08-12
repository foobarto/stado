# Adaptive context and learning

Stado treats memory and context management as a security-sensitive harness
subsystem. Durable knowledge remains below operator and repository instructions,
does not grant capabilities, and is never activated merely because a model wrote
it.

## Learning

`stado learn --session-id <id>` reviews deterministic trajectory signals in an
isolated, tool-less model call. `stado learn run` is the explicit equivalent.
The interactive `/learn [focus]` command runs the same review after the current
completed trajectory. Reviews create bounded `lesson` candidates with evidence
references; they do not activate them.

`stado learn candidates [--session-id <id>]` and `stado learn artifact <id>`
inspect the versioned store. `stado learn migrate` archives and imports the
pre-EP-53 JSONL store idempotently. Ordinary CLI processes cannot approve an
artifact because an agent with shell access could invoke the same command.

In the TUI:

```text
/learn candidates
/learn show art_...
/learn approve art_...
```

Approval is a fresh operator-origin TUI action bound to the exact artifact text,
version, and scope. The one-use grant and activation commit atomically in the
broker WAL. Agent/plugin shells cannot mint that grant. The former pre-v1
`stado learning` noun has been replaced by `stado learn`.

The first deterministic signal set covers repeated identical tool failures,
argument changes followed by success, verification fail→pass, recurring policy
or scope denials, and explicit operator corrections. One-off tool failures do not
produce a lesson signal.

## Retrieval speeds

Fast retrieval injects only active, authorized, non-expired artifacts under hard
item/token limits. Session artifacts reach the creating session and descendants,
never siblings or ancestors. The injected section is explicitly labeled as
untrusted reviewable context.

Memory research is the slower, higher-quality path. Its isolated child receives
only catalog/search/open tools over an authorized artifact corpus. Historical
session research uses the same agent contract over broker-authorized session
paths and bounded message windows. The parent receives a synthesis and exact
digest/version/excerpt citations, not the explored raw corpus. Citation validation
proves byte provenance, not semantic entailment.

## Persistence and organization

Artifacts have immutable host-bound global/repository/session scope, versions,
authority state, sensitivity, tags, hierarchical group labels, evidence,
provenance, expiry, and a small relation vocabulary. SQLite FTS5 is disposable:
the broker WAL is canonical and the index is rebuilt or rejected whenever its
sequence/digest checkpoint is stale. Private and secret bodies stay out of the
ordinary FTS index.

Legacy memory JSONL bytes are archived unchanged and digest-bound during
migration. Approved ordinary memories remain active; approved behavioral lessons
become `legacy_active` and require reaffirmation. Unresolvable session bindings
are quarantined rather than widened.

## Retained agents and messaging

Retained children use durable admissions, immutable fork points, runtime nonces,
broker epochs, leases, attenuated ceiling digests, and recursive root-budget
reservations. Historical context contributes data, never authority. A resumed
historical session is always a new child identity.

The model-callable `agent__spawn` accepts `source:{session_id,at}`, model/persona,
role/mode/write scope, tool-profile narrowing, turn/time/token budgets, and
`execution:"retained"`. `async` controls whether the call waits. Retained handles
remain visible to `agent__list`; `agent__send_message`, `agent__read_messages`,
and cancellation address a live durable handle. Once terminal, resumption is an
explicit new `agent__spawn` whose `source.session_id` is the prior child; this
restores a bounded tainted transcript and the exact selected turn tree into a new
identity, and permits a newly attenuated model/tool/budget profile. Terminal
follow-ups fail rather than accumulating undeliverable work.

Agent mailboxes provide authorized parent↔child data messages with per-sender
ordering, backpressure, expiry, per-message delivery generations, and an isolated
control plane. Delivery commits a unique receiver-input record and acknowledgement
atomically; recovery resumes that turn instead of injecting the message twice.
Tool/external effects still require their own idempotency.

Supervision defaults to no restart. Only host-classified transient runtime
failures may use bounded restart intensity and exponential backoff. Logical,
policy, budget, cancellation, and verification failures do not restart.

## Adaptive ranking

Usage observations distinguish mechanical exposure/open/citation from externally
evaluated outcomes. Adaptive scores are versioned, explainable, and initially
shadow-only. Mandatory or pinned security artifacts cannot be demoted by model
feedback, and temporal association is not presented as causal evidence. Run
`stado learn retrieval-report` to inspect accumulated shadow comparisons; shadow
scores never change the active selection in this phase.
