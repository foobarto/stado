---
ep: 3
title: Provider-Native Agent Interface
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Implemented
type: Standards
created: 2026-04-23
implemented-in: v0.1.0
see-also: [4, 7, 10, 11]
history:
  - date: 2026-04-23
    status: Accepted
    note: Retrofitted from the provider pivot that landed before v0.0.1.
  - date: 2026-04-23
    status: Implemented
    version: v0.1.0
    note: The four-provider seam and capability-driven runtime branching are the shipped default.
---

# EP-3: Provider-Native Agent Interface

## Problem

stado is a coding-agent runtime, not a generic chat wrapper. The agent
loop depends on ordered typed messages, provider-native thinking and
tool-use blocks, cache telemetry, and model capability discovery. A
lowest-common-denominator SDK would erase the details the runtime uses
to stay correct.

## Goals

- Keep `pkg/agent.Provider` as the runtime seam.
- Preserve ordered typed blocks across provider round-trips.
- Drive runtime branching from `Capabilities`.
- Prefer direct provider integrations over meta-SDK abstractions.

## Non-goals

- A public provider SDK for external integrators.
- Flattening provider-native semantics into a generic chat model.
- Supporting providers that cannot stream turns or express tool calls.

## Design

The shipped seam is `pkg/agent.Provider`:

```go
type Provider interface {
    Name() string
    Capabilities() Capabilities
    StreamTurn(ctx context.Context, req TurnRequest) (<-chan Event, error)
}
```

Messages are ordered typed blocks. Text, thinking, tool-use, tool-
result, and image blocks keep their sequence because that ordering is
part of the provider contract and the runtime does not treat it as
cosmetic.

`StreamTurn` emits the event union the runtime consumes today: text
deltas, thinking deltas, tool-call start/args/end, cache hit/miss,
usage, done, and error. Provider-native payloads that matter for later
turns remain attached rather than normalized away.

`Capabilities` drives runtime branching. Prompt caching, thinking,
parallel tool calls, vision, and context-window behavior are selected
from declared support rather than from provider-name switches in every
caller.

The shipped providers are direct integrations for Anthropic, OpenAI,
Google, and an OpenAI-compatible HTTP transport. stado prefers direct
provider code because it preserves provider-native semantics and keeps
failures local to the adapter instead of a shared abstraction layer.

## Decision log

### D1. Use a coding-agent seam, not a generic LLM abstraction

- **Decided:** `pkg/agent.Provider` is the stable runtime seam.
- **Alternatives:** adopt a generic chat-completions abstraction or a
  third-party multi-provider framework.
- **Why:** stado's hard parts are tool-use sequencing, thinking,
  caching, and context management. Hiding those behind a generic layer
  would make the agent runtime less correct and harder to debug.

### D2. Preserve ordered typed blocks end-to-end

- **Decided:** messages remain ordered lists of typed blocks.
- **Alternatives:** normalize everything into strings or provider-
  specific payload maps.
- **Why:** ordering is part of the protocol contract for several
  providers, especially around thinking and tool-use round-trips.

### D3. Make capabilities part of the runtime contract

- **Decided:** runtime branching is driven by a first-class
  `Capabilities` struct.
- **Alternatives:** hard-code behavior per provider or discover support
  ad hoc in each caller.
- **Why:** capability-driven branching keeps the rest of the runtime
  honest about what a model can do without duplicating vendor logic.

### D4. Integrate providers directly

- **Decided:** shipped providers are direct first-party integrations plus
  one OAI-compatible transport.
- **Alternatives:** depend on a meta-SDK that abstracts every provider.
- **Why:** direct integrations keep failure modes local and make it
  possible to preserve provider-native semantics when the APIs diverge.

## Related

- [EP-4: Git-Native Sessions and Audit Trail](./0004-git-native-sessions-and-audit.md)
- [EP-7: Conversation State and Compaction](./0007-conversation-state-and-compaction.md)
- [EP-10: Interop Surfaces: MCP, ACP, and Headless](./0010-interop-surfaces-mcp-acp-headless.md)
- [DESIGN.md](../../DESIGN.md#provider-interface-pkgagent)
- [PLAN.md](../../PLAN.md#phase-1--coding-agent-provider-interface--)
