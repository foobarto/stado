---
ep: 54
title: Memory and Session Research Agents
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Accepted
type: Standards
created: 2026-08-12
requires: ["EP-0004", "EP-0007", "EP-0038", "EP-0050", "EP-0053", "EP-0057", "EP-0059", "EP-0066"]
see-also: ["EP-0002", "EP-0020", "EP-0037", "EP-0055", "EP-0058"]
history:
  - date: 2026-08-14
    status: Accepted
    note: Native host primitives and unsigned official plugin development source now implement the corrected slow-path research placement. The exact memory__search fast path plus signed installation, publication, and release proof remain outstanding; the contract is therefore not yet shipped as Implemented.
  - date: 2026-08-14
    status: Partial
    note: Corrected after the implementation audit found that the shipped research tools used a private in-process provider loop and direct native corpus/WAL adapters rather than the architecture specified here.
  - date: 2026-08-12
    status: Implemented
    version: v0.78.0
    note: Initially marked implemented when useful research behavior shipped; corrected during the 2026-08-14 placement audit.
  - date: 2026-08-12
    status: Accepted
    note: Accepted after product, security, and distributed-systems adversarial review.
  - date: 2026-08-12
    status: Draft
    note: Initial draft.
---

> **Relationships:** **Requires:** [EP-0004](./0004-git-native-sessions-and-audit.md), [EP-0007](./0007-conversation-state-and-compaction.md), [EP-0038](./0038-abi-v2-bundled-wasm-and-runtime.md), [EP-0050](./0050-broker.md), [EP-0053](./0053-versioned-harness-artifacts-and-index.md), [EP-0057](./0057-session-state-journal-decisions-and-signals.md), [EP-0059](./0059-durable-event-and-budget-substrate.md), [EP-0066](./0066-canonical-plugin-authority-and-application-placement.md) · **See also:** [EP-0002](./0002-all-tools-as-plugins.md), [EP-0020](./0020-inline-context-completion.md), [EP-0037](./0037-tool-dispatch-and-operator-surface.md), [EP-0055](./0055-retained-resumable-subagents.md), [EP-0058](./0058-measured-adaptive-retrieval.md)

# EP-0054: Memory and Session Research Agents

## Problem

Fast lexical retrieval is cheap but shallow. Asking the main agent to inspect a
large artifact or conversation corpus consumes its working context and invites
it to confuse retrieved text with instructions. Research therefore needs an
isolated model loop, while access to persisted evidence must remain scoped and
mechanically accountable.

## Goals

- Keep deterministic search distinct from agentic research.
- Run research in an ordinary, auditable child session rather than a hidden
  provider call.
- Keep corpus selection, authority, budgets, and evidence receipts native while
  keeping prompts, search workflow, and result policy in a signed plugin.
- Return a bounded synthesis whose citations are tied to bytes the exact child
  actually opened.

## Non-goals

- Treating research as a security boundary or semantic fact checker.
- Giving a research child write authority over artifacts, sessions, or files.
- Letting guest JSON select another repository, session, principal, plugin, or
  broker generation.
- Retaining every oversized tool result; that remains a separate storage and
  authorization problem.

## Design

### Explicit fast and slow paths

```text
memory__search     deterministic artifact lookup
memory__research   isolated agentic analysis of active visible artifacts
session__search    deterministic persisted-conversation lookup
session__research  isolated agentic analysis of the current session lineage
```

`memory__research` and `session__research` are explicit tools in the official
`research` WASM package. They accept one bounded `query`. The package owns the
prompt, selected corpus workflow, result schema, and what counts as useful
research. Native Stado does not register either tool and has no research
provider loop.

Current source status is intentionally narrower than this Accepted contract.
The bundled `session__search` fast path exists, and the unsigned official
`research` source implements both slow paths. The exact `memory__search` wire
tool is not yet shipped; the memory lifecycle application's `memory` list
action is useful inspection but is not represented as that deterministic
search contract. EP-54 must remain Accepted until this gap is implemented or a
later accepted decision replaces the fast-path name and semantics, and until
the official research package is signed, installed, and release-verified.

Each outer tool calls ordinary `stado_agent_spawn` synchronously with:

- bundled `researcher` persona;
- role `explorer`, mode and tool profile `read_only`;
- at most 8 turns, 120 seconds, and 30,000 tokens;
- an exact three-tool `narrow_tools` projection for one corpus.

This is the same AgentLoop, broker child admission, provider accounting,
cancellation, and session audit path used by other subagents. Provider cleanup
diagnostics remain separate from a valid terminal result.

### Child-only evidence tools

The signed package declares six helpers:

```text
research__artifact_catalog  research__artifact_search  research__artifact_open
research__session_catalog   research__session_search   research__session_open
```

Every helper has `agent_child_only: true` and one exact per-tool evidence
capability. Installation alone never places these tools on an ordinary parent
turn. A broker-created child sees one only when `narrow_tools` names that exact
tool and the loader-verified signed spawning package namespace owns the helper;
guest JSON cannot supply that owner. A direct native spawn, bundled agent tool,
or unrelated plugin cannot guess a helper name into existence. Missing or
unknown requested names fail closed, and `read_only` is not a wildcard.

This projection is defense in depth, not the authority boundary. Every tool
call creates a fresh WASM Host, so the native loader also obtains an opaque
broker token for the exact selected signed `ToolDef`. The broker reloads and
verifies the package, derives that tool's capability subset, and rejects a
token used for a sibling tool's corpus or operation. The token never enters
guest memory.

### Generic native evidence boundary

Native Stado exposes only four generic imports:

```text
stado_evidence_catalog   stado_evidence_search
stado_evidence_open      stado_evidence_validate
```

The broker derives principal, canonical repository, session generation,
canonical package, and corpus scope from authenticated controller and plugin
bindings. A request chooses only `artifact` or `session`, never an authority
identity. Artifact evidence contains active broker-visible artifact versions.
Session evidence contains complete JSONL records from the authenticated current
session and at most its 99 nearest fork ancestors; unrelated sessions are
neither enumerated nor readable. Each conversation log has an 8 MiB search-scan
ceiling, and cancellation is checked throughout enumeration.

Session references use immutable per-record byte ranges and digests:

```text
conversation.jsonl:bytes:<start>-<end>
```

Appending later turns or an append-only compaction marker does not invalidate
an earlier complete record. Incomplete trailing records are excluded. Opening
re-authorizes lineage, rereads the exact range, and compares the digest.

The broker enforces, per exact child session, generation, and canonical
package:

- 40 aggregate catalog/search/open calls;
- 20 opens;
- 256 KiB total returned bytes, including catalog and search;
- 100 rows, 32 KiB per opened body, 16 KiB final result, and 1 KiB excerpts.

Budget check and WAL append share the artifact-state mutation lock, so distinct
concurrent calls cannot overspend. Exact request/response replay is idempotent
and does not double-count. Usage and open receipts survive Host recreation and
broker restart.

### Result and citation integrity

The child returns strict JSON:

```json
{
  "answer": "bounded synthesis",
  "claims": [{
    "text": "one material claim",
    "citations": [{
      "ref": {
        "corpus": "artifact|session",
        "kind": "host value",
        "id": "host value",
        "version": 1,
        "locator": "host value",
        "digest": "sha256:..."
      },
      "excerpt": "exact substring copied from opened body",
      "entailment_verified": false
    }]
  }],
  "conflicts": [],
  "possibly_stale": [],
  "not_found": [],
  "confidence": "low|medium|high",
  "learn_suggestions": []
}
```

The outer plugin submits that object and the returned child session ID to
`stado_evidence_validate`. The broker accepts only an exact direct child of the
calling parent whose durable purpose is subagent, role is `explorer`, and mode
is `read_only`. Every citation must match an open receipt for the child's exact
generation, direct parent, and canonical package, and its excerpt must be an
exact substring of the opened immutable body. Fabricated locators, digests,
excerpts, sibling children, and foreign sessions fail the whole tool call.

The host always emits `entailment_verified: false`. These checks prove which
authorized bytes were opened and quoted; they do not prove that a claim follows
from those bytes, is complete, current, or wise. The result remains untrusted
model output in the parent context.

## Failure modes

- **Runaway exploration:** child turn/time/token ceilings and broker read caps
  stop it.
- **Concurrent overspend:** serialized fold-and-append admits no more than the
  durable aggregate ceiling.
- **Host recreation or restart:** usage and receipts fold from the broker WAL;
  they are not instance-local counters.
- **Fabricated citation:** exact ref, receipt, body digest, and excerpt checks
  reject it.
- **Conversation advances or compacts:** prior complete byte-range references
  remain valid because the log is append-only.
- **Foreign corpus probe:** authenticated scope is resolved before enumeration,
  and guest input has no foreign authority selector.

## Test strategy

- Parent/no-narrow/read-only/unrelated/matching child-only projection matrix.
- Broker-token sibling-capability misuse and unknown-tool bind rejection.
- Same-call idempotency and distinct-call concurrency tests under the open cap,
  including race builds.
- Current-lineage enumeration and foreign-session rejection.
- Reference stability after append, compaction, and source reconstruction.
- Fabricated ref, digest, excerpt, foreign child, write-capable child, and
  non-subagent validation failures.
- Official plugin strict-input, exact spawn-profile, fixed-corpus, large host
  response, race, vet, and reproducible unsigned WASM build checks.

## Deferred work

- Retained/background research may compose EP-0055 after measured need.
- Unrelated historical sessions require an explicit operator-origin grant and
  are not silently added to the v1 corpus.
- Automatic retention of oversized tool results remains a separate EP because
  it creates deletion, sensitivity, fork-authorization, and secret-retention
  obligations.

## Decision log

### D1. Research is an ordinary child session

- **Decided:** broker-created AgentLoop, not a private provider loop.
- **Why:** one audited child path preserves budgets, provenance, cancellation,
  persona, and tool projection without adding privileged orchestration code.

### D2. Workflow is WASM; evidence authority is native

- **Decided:** the official plugin owns research policy while the host owns
  authenticated facts, bounds, receipts, and mechanical integrity.
- **Why:** this follows EP-2/38/66: native primitives bridge the WASM garden;
  they do not become a product application.

### D3. Citation integrity is not semantic verification

- **Decided:** validate exact opened bytes and direct child provenance only.
- **Why:** a native substring check can prove a citation exists; it cannot prove
  entailment, completeness, or truth.

### D4. Child visibility and authority are independently narrowed

- **Decided:** require signed `agent_child_only` exact projection bound to the
  loader-verified signed spawning package owner, plus a broker-minted exact-tool
  capability token.
- **Why:** model presentation, WASM Host reconstruction, and durable broker
  authority are different boundaries and must each fail closed.
