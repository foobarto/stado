---
ep: 58
title: Measured Adaptive Retrieval
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Implemented
implemented-in: v0.78.0
type: Standards
created: 2026-08-12
requires: [52, 53, 54, 57, 59]
see-also: [7, 11, 15, 16]
history:
  - date: 2026-08-12
    status: Implemented
    version: v0.78.0
    note: Shipped in v0.78.0 as part of the memory, context, and continual-harness implementation.
  - date: 2026-08-12
    status: Accepted
    note: Accepted after product, security, and distributed-systems adversarial review.
  - date: 2026-08-12
    status: Draft
    note: Initial draft.
---

# EP-0058: Measured Adaptive Retrieval

## Problem

Default-on bounded memory retrieval prevents a single turn from overflowing, but
it does not prevent the store from accumulating irrelevant guidance or repeatedly
surfacing unused lessons. Continual learning is useful only if retrieval remains
selective, stale knowledge is visible, and the system measures outcomes without
allowing a model to declare its own advice successful.

## Goals

- Define hot, warm, cold, and historical retrieval tiers.
- Use scope, trigger, tags/groups, recency, evidence, and observed use to rank.
- Measure consideration, opening, citation, following, contradiction, and gated
  outcomes.
- Demote or flag noisy/stale artifacts without deleting audit history.
- Escalate explicitly from fast retrieval to memory or session research.
- Provide evaluation reports comparing cost, usefulness, and failure recurrence.

## Non-goals

- Autonomous deletion or silent semantic rewriting.
- Model self-rating as the sole success signal.
- Always-on vector embeddings.
- Guaranteeing that relevant knowledge is always retrieved.
- Optimizing a single opaque relevance score without operator visibility.

## Design

### Three paths and internal tiers

The public model remains: bounded automatic/fast search, medium-cost memory
research, and slow session research. Hot/warm/cold are internal exposure classes.

1. **Hot** — tiny active artifact bodies with strong trigger/scope match and
   recent validated use; eligible for bounded automatic prompt injection.
2. **Warm** — compact catalog entries available through fast search. Any prompt
   catalog has its own hard token/item cap and cannot duplicate hot bodies.
3. **Cold** — full content, evidence, and old/rare artifacts; opened
   explicitly or by `memory__research`.
4. **Historical** — raw session/context evidence accessed through
   `session__research`.

Tier is a derived retrieval classification, not artifact authority. Operators can
pin or exclude an artifact. Secret artifacts are never promoted into model-visible
tiers.

### Rank explanation

Every result includes component scores/reasons:

```text
scope exact, trigger lexical match, tag match, group match, recency,
validated-use boost, contradiction penalty, stale penalty,
repeated-ignore penalty, operator pin/exclude
```

Initial ranking is deterministic. A model reranker may reorder an already
authorized bounded candidate set but cannot add excluded items or change scope.

### Outcome evidence

Exposure/open/citation observations do not establish causation. Outcome association
is labeled non-causal. `validated_help` requires:

- the operator explicitly marked it useful;
- an external evaluator or controlled benchmark tied to the declared validation
  produced a labeled result.

Model judgment may be recorded separately as advisory. Contradiction or repeated
failure after following an artifact creates a review signal; it does not silently
edit the artifact.

### Promotion, demotion, and staleness

- Candidate → active remains explicit EP-52/53 authority.
- Warm/cold → hot is derived from configurable evidence thresholds or operator pin.
- Hot → warm/cold initially happens automatically only for expiry, deterministic
  invalidation, or operator exclusion. Non-use/contradiction are shadow signals.
- Operator-pinned, mandatory, and security artifacts cannot adaptively demote.
- Any semantic revision, scope widening, or reactivation requires review.
- Archived items remain searchable and auditable.

### Escalation policy

Automatic prompt retrieval never silently starts a research agent. The main agent
or operator selects `memory__research`; that result may recommend
`session__research`. Budgets and expected cost are visible in tool metadata and
results.

### Evaluation

Reports include:

- relevant-mistake recurrence rate;
- retrieval precision and artifact open/citation rates;
- candidate approval/rejection and rollback rates;
- stale/contradiction rates;
- parent context tokens saved;
- research-agent tokens, latency, and citation validity;
- successful task/gate rate with and without adaptive retrieval;
- sub-agent value relative to cost.

Evaluation records artifact versions, source/index sequence, ranking-policy/config
digest, and observation watermark. Causal task claims require paired/randomized
runs with leakage control; historical replay measures retrieval only.

Evaluation supports shadow mode: rank and log what would have surfaced without
injecting it. Default automation cannot advance beyond shadow/manual gates until
the configured evaluation threshold is met.

## Migration / rollout

1. Record observations with no rank behavior change.
2. Add explainable deterministic warm/cold ranking and operator reports.
3. Run hot-tier policy in shadow mode.
4. Enable automatic narrowing/demotion first.
5. Enable bounded hot injection only after comparative evaluation.
6. Keep model reranking optional and later.

## Failure modes

- Feedback loop rewards popular but bad advice: gated outcomes and contradiction
  signals outweigh mere surfacing/citation.
- New useful artifact remains cold: explicit pin/search/research remains available.
- Automatic demotion hides important guidance: audit explains rank and operator can pin.
- Metric gaming: host facts and external gates are separated from model judgments.
- Ranking version changes behavior: version is recorded per result and shadow-tested.

## Test strategy

- Deterministic rank and explanation golden tests.
- Lifecycle transition/state-machine tests.
- Adversarial popularity, repeated-ignore, contradiction, and stale fixtures.
- Shadow-versus-active comparative integration tests.
- Prompt-budget and context-token accounting assertions.
- Evaluation reports over fixed historical replay corpora.

## Open questions

- Numeric default thresholds must come from shadow-mode data and are intentionally
  not standardized in this draft.

## Decision log

### D1. Tier is derived, authority is stored

- **Decided:** retrieval temperature is recalculable policy; activation remains an
  explicit artifact event.
- **Alternatives:** persist hot/cold as the artifact's authority state.
- **Why:** ranking can evolve without rewriting knowledge or weakening review.

### D2. Automatic policy may narrow first

- **Decided:** automatic demotion is allowed before automatic promotion/injection.
- **Alternatives:** enable both together.
- **Why:** narrowing exposure is safer and generates evaluation data without
  increasing prompt authority.

### D3. Research escalation is explicit

- **Decided:** automatic retrieval cannot incur an invisible agentic token bill.
- **Alternatives:** silently start research whenever fast confidence is low.
- **Why:** cost and latency are core semantics of the three-speed system.

## Related

- EP-52 Learn
- EP-53 Harness Artifacts
- EP-54 Research Agents
