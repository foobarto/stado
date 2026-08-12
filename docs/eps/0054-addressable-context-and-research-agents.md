---
ep: 54
title: Memory and Session Research Agents
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Accepted
type: Standards
created: 2026-08-12
requires: [4, 7, 38, 53, 57, 59]
see-also: [20, 37, 50, 55, 58]
history:
  - date: 2026-08-12
    status: Accepted
    note: Accepted after product, security, and distributed-systems adversarial review.
  - date: 2026-08-12
    status: Draft
    note: Initial draft.
---

# EP-0054: Memory and Session Research Agents

## Problem

Stado either injects information directly into the main model context or
truncates it. Fast memory and session searches return lexical excerpts, while
full historical reconstruction requires the main agent to consume many results.
The runtime has no addressable context substrate and no isolated agent whose
sole job is to inspect a large corpus and return a small cited synthesis.

## Goals

- Keep cheap deterministic search distinct from agentic research.
- Add medium-cost memory research over curated artifacts.
- Add slow historical research over persisted session evidence.
- Protect the main agent context through isolated child sessions and structured
  cited results.
- Enforce total call, token, time, data-read, and result budgets.

## Non-goals

- Replacing direct context for ordinary small tasks.
- An unrestricted REPL or model-generated native code environment.
- Giving research agents write authority over memory, sessions, or worktrees.
- Claiming semantic completeness from either research path.

## Design

### Deferred addressable tool output

Automatically retaining full oversized tool output creates a new secret-retention,
storage, deletion, and fork-authorization surface and is not required for memory
or session research, whose sources already persist. It is deferred until the
three-speed retrieval loop is evaluated. Any later implementation must use a
broker-owned access-controlled manifest; knowing a content digest never grants access.

The earlier candidate shape is retained only as research notes:

```json
{
  "id":"ctx_sha256...",
  "media_type":"text/plain",
  "bytes":4200000,
  "lines":51203,
  "origin":"tool_result",
  "provenance":"untrusted",
  "sensitivity":"normal|private|secret",
  "session_id":"...",
  "producer_call":"...",
  "ceiling_digest":"...",
  "content_digest":"..."
}
```

No `context__metadata/read/search` tool ships in the first slice.

### Fast and research paths

```text
memory__search     structured/FTS lookup, no model
memory__research   isolated agentic analysis of approved artifacts
session__search    existing lexical/regex persisted-session lookup
session__research  isolated agentic analysis of selected historical windows
```

Research tools create read-only child sessions through the broker with a fixed
research persona and an immutable minimal ceiling. They use the ordinary agent
loop, session audit, budgets, and provenance machinery. The parent tool call may
wait for completion in v1 of this EP; retained/background execution belongs to
EP-55.

The host issues an opaque corpus handle derived only from authenticated principal,
canonical repo, session fork point, sensitivity policy, and child ceiling. Query
JSON cannot name foreign identities. Authorization occurs before FTS/ranking;
foreign denied/absent records do not leak through metadata/counts/timing where
practical. Unrelated historical sessions require an EP-59 operator-origin grant.

Memory children receive only `research__catalog`, `research__search_artifacts`,
and `research__open_artifact(id,version,range)`. Session children receive only
`research__search_windows`, `research__open_messages(session,ids|range)`, and
`research__open_evidence(locator,range)`. Every call validates the corpus handle.
Hard budgets cap provider calls, tokens, rows, opens, authorized bytes, result
bytes, turns, and wall time. Full bodies cannot cross to the parent; only validated
bounded citation excerpts can.

### Memory research

Input:

```json
{
  "query":"What release mistakes should I avoid?",
  "scopes":["repo","global"],
  "kinds":["lesson","memory"],
  "tags":["area:release"],
  "max_candidates":100,
  "max_open":20,
  "token_budget":30000
}
```

The host—not the child—resolves trusted repo/session scope. The child sees a
catalog, opens selected full artifacts/evidence through bounded calls, and
returns answer, cited artifact versions, conflicts, possible staleness, missing
information, and suggested escalation. It has no `artifact.activate` or update
capability.

### Session research

The host first uses descriptions, FTS/lexical hits, fork ancestry, turn metadata,
and compaction manifests to select candidate windows. The child may open bounded
conversation ranges and signed trace/tree evidence. It cannot resume or modify
the source session.

The result cites session IDs, turn ranges, trace/tree commits, and confidence.
It may emit an EP-52 learn-candidate suggestion when expensive historical work
recovered durable knowledge absent from memory.

### Result contract

```json
{
  "answer":"bounded synthesis",
  "claims":[{"text":"bounded claim", "citations":[{
    "ref":"precise immutable locator", "range":"bounded range",
    "excerpt":"bounded support", "digest":"sha256:..."
  }]}],
  "conflicts":[],
  "possibly_stale":[],
  "not_found":[],
  "confidence":"low|medium|high",
  "learn_suggestions":[],
  "usage":{"tokens":0,"opened":0,"elapsed_ms":0}
}
```

The host validates locator existence, digest/range, authorization, and that the
child read it. This is citation integrity, not semantic entailment; valid citations
never raise confidence by themselves. Output and citation provenance remain
untrusted in the parent context, and conflicting sources remain visible.

## Migration / rollout

1. Upgrade deterministic memory/session indexes and bounded readers.
2. Ship synchronous `memory__research` with fixed persona and inherited model by
   default; operator policy may select another configured model, with no fallback.
3. Ship `session__research` after precise transcript/trace locators exist.
4. Integrate retained execution through EP-55 only after measured need.
5. Reconsider oversized tool-output retention as a separate accepted slice.

## Failure modes

- Child performs runaway exploration: substrate-enforced aggregate budgets stop it.
- Research hallucinates citations: host citation validation marks result failed.
- Corpus is too tightly coupled for partitioning: child reports low confidence and
  recommends direct/manual inspection.
- Source session is corrupt or audit-invalid: evidence is excluded or explicitly
  labeled unverifiable.

## Test strategy

- Cross-session/cross-repo corpus-handle authorization/enumeration tests.
- Citation validation and fabricated-citation tests.
- Budget exhaustion and child cancellation tests.
- Hard call/open/byte/result caps and full-body exfiltration tests.
- Retrieval quality fixtures comparing fast search, memory research, and session
  research without live provider dependence.
- End-to-end context-size assertions proving raw corpora do not enter the parent.
- Citation resolvability after compaction/migration and precise-locator tests.

## Open questions

- Default context-artifact retention duration must be calibrated against audit and
  private-data expectations before automatic full-result preservation is enabled.

## Decision log

### D1. Research is a child session

- **Decided:** use ordinary broker-projected agent sessions, not hidden provider
  calls or a privileged in-process summarizer.
- **Alternatives:** direct LLM calls; unrestricted RLM REPL; main-agent search.
- **Why:** sessions preserve audit, budgets, provenance, isolation, and one-agent
  ownership while protecting parent context.

### D2. Fast and slow paths stay explicit

- **Decided:** deterministic search and agentic research use different tools.
- **Alternatives:** silently escalate every search to a model.
- **Why:** users and agents need predictable latency, token cost, and fidelity.

### D3. Citation integrity is host-side

- **Decided:** accept only citations to material the child was authorized to read.
- **Alternatives:** trust citation strings in prose.
- **Why:** citations are useful only when mechanically tied to evidence;
  entailment remains a separate semantic judgment.

### D4. Tool-output retention is deferred

- **Decided:** first ship research over already-persisted memory/session corpora.
- **Alternatives:** retain every truncated tool result immediately.
- **Why:** retention is independent higher-risk storage work and is not necessary
  to prove the three-speed retrieval loop.

## Related

- Recursive Language Models, arXiv:2512.24601
- EP-7 Conversation State and Compaction
- EP-53 Harness Artifacts
