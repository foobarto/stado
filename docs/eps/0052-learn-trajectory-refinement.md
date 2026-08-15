---
ep: 52
title: Learn — Evidence-Backed Trajectory Refinement
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Accepted
type: Standards
created: 2026-08-12
supersedes: ["EP-0016"]
requires: ["EP-0015", "EP-0021", "EP-0046", "EP-0053", "EP-0057", "EP-0059"]
extended-by: ["EP-0069"]
see-also: ["EP-0007", "EP-0009", "EP-0044", "EP-0047"]
history:
  - date: 2026-08-14
    status: Accepted
    note: The candidate-only TUI lifecycle application is source-complete in the staged official plugin repository, but remains unsigned and unpublished. Fresh activation still requires the separately trusted EP-59 presenter, and provider reply loss cannot yet be made exactly once.
  - date: 2026-08-12
    status: Accepted
    note: The earlier v0.78.0 native implementation did not satisfy the plugin-application, authority, and crash-accounting contract and was removed during the pre-v1 corrective migration.
  - date: 2026-08-12
    status: Accepted
    note: Accepted after product, security, and distributed-systems adversarial review.
  - date: 2026-08-12
    status: Draft
    note: Initial draft following the RLM and Continual Harness design review.
---

> **Relationships:** **Supersedes:** [EP-0016](./0016-learning-self-improvement-plugin.md) · **Requires:** [EP-0015](./0015-memory-system-plugin.md), [EP-0021](./0021-assistant-turn-metadata.md), [EP-0046](./0046-verify-work-phase.md), [EP-0053](./0053-versioned-harness-artifacts-and-index.md), [EP-0057](./0057-session-state-journal-decisions-and-signals.md), [EP-0059](./0059-durable-event-and-budget-substrate.md) · **Extended by:** [EP-0069](./0069-agent-owned-memory-authority.md) · **See also:** [EP-0007](./0007-conversation-state-and-compaction.md), [EP-0009](./0009-session-guardrails-and-hooks.md), [EP-0044](./0044-repo-config-trust-boundary.md), [EP-0047](./0047-structured-loop-result-and-output.md)

# EP-0052: Learn — Evidence-Backed Trajectory Refinement

> **Implementation status (2026-08-14):** Source for the explicit,
> candidate-only `/learn` lifecycle application is complete in the staged
> official plugin repository. It is unsigned and unpublished. Core stado has no
> native `stado learn` fallback. Fresh activation and the full review presenter
> remain Accepted work, and provider success followed by reply loss is an
> explicitly irreducible ambiguity until provider invocation gains durable
> idempotency.

## Problem

EP-16 ships a safe lesson store and manual CLI review flow, but it does not
close the learning loop. The agent must already know what it learned and invoke
`stado learning propose` out of band. Repeated tool failures, corrected
assumptions, verification failures, successful recurring procedures, and user
corrections are not assembled into evidence-backed candidates during the
session that produced them.

The `learning` noun also describes the implementation rather than the operator
action. Before v1 stado can replace it with a smaller canonical `learn` surface.

## Goals

- Make `/learn` and the signed lifecycle application's `learn` tool the
  canonical learning interfaces.
- Analyze structured trajectory facts plus bounded surrounding conversation.
- Produce small, typed, evidence-bound candidates for operator review.
- Learn primarily from mistakes and corrections, while also capturing repeated
  validated successes.
- Keep durable repo/global behavior changes explicit and reversible.
- Record learning reviews even when they correctly produce no candidate.

## Non-goals

- Rewriting the base system prompt or project instructions.
- Treating model interpretation as a trust-critical fact.
- Automatically approving repo/global artifacts.
- Persisting verbatim untrusted repository or tool output as guidance.
- Training model weights.

## Design

### Surfaces

The accepted target surface is owned by one explicit TUI lifecycle application:

```text
/learn [focus]
learn {"action":"review","focus":"optional"}
```

`stado learning` and the temporary native `stado learn` surface are removed,
not retained as aliases. Existing lesson items remain valid and are migrated by
EP-53. Inspection uses bounded generic artifact queries owned by the admitted
application; fresh activation remains outside the guest ABI.

Bare `/learn` is a host command that captures the immediately preceding completed
turn and schedules a dedicated review job. If invoked while generation is active,
it queues at that turn's boundary. It never holds the active-turn lock during
provider inference. A focused call narrows review but cannot widen data or
capability access. `candidates` lists artifact candidates; `reviews` lists review
jobs/results. Existing manual proposal remains available as `learn propose`.

### Inputs

The trusted host builds a `LearnReviewInput` containing:

- session and turn identifiers;
- typed signals from EP-57;
- tool-call/result identifiers, exit status, normalized error class, and
  argument-delta metadata;
- verification gates and outcomes from EP-46;
- user-correction markers and bounded adjacent messages;
- file/tree/trace commit references;
- memory artifacts surfaced, opened, cited, contradicted, or followed;
- child results and message references;
- current active artifact catalog and recent learning history.

Origin and taint remain host metadata. Conversation excerpts are serialized as
quoted evidence, never as instructions to the reviewer.

### Proposal contract

The reviewer returns strict JSON with observations and no more than the
configured candidate limit (default 4):

```json
{
  "summary": "one sentence",
  "observations": [{
    "signal_ids": ["sig_..."],
    "interpretation": "what pattern the facts support"
  }],
  "candidates": [{
    "artifact_kind": "lesson|memory",
    "scope": "session|repo|global",
    "summary": "action-oriented title",
    "trigger": "when this applies",
    "content": "bounded proposed guidance",
    "tags": ["area:tui"],
    "groups": ["stado/verification"],
    "evidence_refs": ["trace:...", "session:.../turn:..."],
    "expected_outcome": "observable improvement",
    "validation": "how later use can be checked"
  }]
}
```

Every schema-valid proposal is stored as a candidate. The host rejects unknown
evidence references, unsupported scope, oversized content, or known secret
matches and applies best-effort redaction. It does **not** claim generic secret or
paraphrase detection. Every candidate retains field-level origin/taint derived
from its strongest source. Behavioral lessons require at least one independently
verified host fact; untrusted content alone cannot satisfy that requirement.

Review displays each semantic claim beside exact bounded evidence excerpts and
the future injected text. Reviewer output is rendered as untrusted data, never as
instructions to the reviewer UI. Global behavioral guidance cannot be approved by
a one-click transformation of model prose: the operator must edit or explicitly
reaffirm the exact final text. Activation never upgrades provenance, taint, or
instruction priority.

### Authority and activation

- Session candidates may be activated only through the EP-59 operator-origin
  authority primitive or predeclared operator policy and remain supplemental,
  labeled context.
- Repo/global candidates require operator approval before retrieval.
- Approval changes eligibility, never provenance or taint history.
- All activated content remains untrusted advice below operator messages and
  repo instructions and has no tool/capability semantics.

### Review history and rollback

Each review is a durable EP-59 job with an idempotency key and captured
`as_of_sequence`, recording input digests, model/provider,
prompt version, candidates, rejection reasons, cost, and status. Artifact edits
use EP-53 versions and supersession. Rollback means appending a restoration or
supersession event; no prior event is removed.

### Trigger policy

Initial release runs only on explicit `/learn` or the application's visible
`learn` tool. Later host
suggestions may fire after repeated failures, user correction, verification
exhaustion, compaction, rollback, or goal completion. Suggestions may initiate a
review only when operator config permits; they still create candidates only.

## Migration / rollout

1. Add the new review/event contracts without changing prompt retrieval.
2. Migrate EP-16 lesson records into EP-53 artifact versions.
3. Remove native CLI documentation and command registration; admit the signed
   TUI lifecycle package explicitly.
4. Add `/learn` and review UI.
5. Add opt-in lifecycle suggestions after explicit use is measured.

## Failure modes

- Malicious evidence attempts prompt laundering: provenance is preserved, exact
  evidence is visible, host facts are required, and activation does not promote
  authority; semantic paraphrase detection is not relied upon.
- Reviewer invents evidence: unknown references reject the candidate.
- Weak model emits noisy lessons: bounded candidates remain review-only.
- Review races an active turn: requests serialize at the turn boundary.
- Review output truncates or fails schema validation: no state change occurs.
- Repeated reviews create duplicates: EP-53 FTS/tag candidates surface them
  during review; no silent merge occurs.
- Crash before a durable review result: intent recovery prevents an automatic
  second provider call, but provider success followed by reply loss remains
  ambiguous because the provider primitive has no durable idempotency key. The
  review fails honestly instead of claiming exactly-once cost. Once a result is
  durable, exact receipt-bound candidates resume idempotently without another
  provider call.

## Test strategy

- Schema, evidence-reference, size, sensitivity, and scope tests.
- Adversarial prompt-laundering fixtures from files, tools, web, and children.
- Turn-boundary serialization and cancellation tests.
- CLI/TUI tests for proposal, review, approval, and history.
- Migration tests from every shipped EP-15/16 event shape.
- End-to-end tests where a repeated verified failure yields a candidate while a
  one-off unsupported hypothesis does not.
- Adversarial paraphrase, Unicode, encoded, split-span, cross-artifact, and
  cited-but-unsupported laundering fixtures.

## Open questions

- Exact deterministic rules for recognizing a user correction belong to EP-57
  implementation experiments; false positives must remain signals, not facts.

## Decision log

### D1. Learn, not refine or learning

- **Decided:** use the verb `learn` for the application tool, slash command,
  and lifecycle.
- **Alternatives:** retain `learning`; copy Prime Agent's `refine`.
- **Why:** `learn` names the operator intent and covers extraction, review,
  validation, and supersession rather than only mutation.

### D2. Candidate-only model writes

- **Decided:** model review can create candidates but cannot approve durable
  repo/global behavior.
- **Alternatives:** immediately apply every refinement; prohibit in-session
  learning entirely.
- **Why:** this closes the loop without making untrusted trajectory content an
  authority-laundering path.

### D3. Facts first, transcript second

- **Decided:** reviews consume typed trajectory facts with bounded quoted
  context, not an unstructured full transcript.
- **Alternatives:** ask a model to rediscover all patterns from conversation.
- **Why:** deterministic evidence is cheaper, auditable, and less injectable.

### D4. Remove native pre-v1 commands

- **Decided:** remove `stado learning` and the temporary native `stado learn`
  implementation instead of carrying compatibility commands.
- **Alternatives:** indefinite alias or warning-only deprecation period.
- **Why:** pre-v1 is the time to converge on one vocabulary; persisted data is
  migrated independently of CLI compatibility.

## Related

- EP-15 Memory System Plugin
- EP-16 Learning and Self-Improvement Plugin (superseded)
- EP-46 Verify-Work Phase
- Continual Harness, arXiv:2605.09998
