---
ep: 56
title: Durable Agent Mailboxes and Supervision
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Accepted
type: Standards
created: 2026-08-12
requires: [4, 33, 50, 55, 59]
see-also: [11, 36, 47]
history:
  - date: 2026-08-12
    status: Accepted
    note: Accepted after product, security, and distributed-systems adversarial review.
  - date: 2026-08-12
    status: Draft
    note: Initial draft borrowing isolation and supervision ideas from Erlang/OTP.
---

# EP-0056: Durable Agent Mailboxes and Supervision

## Problem

Synchronous tool returns are insufficient for retained agents, progress reports,
follow-ups, cancellation, and research completed after a parent turn. Shared files
are an ambiguous and unaudited substitute. Stado needs a durable communication
contract that preserves session isolation and does not transfer authority.

## Goals

- Provide asynchronous immutable messages between authorized sessions.
- Persist delivery state across detach, crash, and restore.
- Define ordering, retry, acknowledgement, expiry, and backpressure honestly.
- Support monitoring and bounded supervision of child lifecycle.
- Trace message causality without injecting internal routing metadata as prompt text.
- Make authority non-transferable through messages.

## Non-goals

- Global exactly-once delivery or total ordering.
- Arbitrary public agent discovery.
- Shared mutable memory between sessions.
- Automatic restart after logical failure by default.
- Treating OTP as a byte-for-byte implementation prescription.

## Design

### Message envelope

```json
{
  "id":"msg_...",
  "sender_session":"...",
  "receiver_session":"...",
  "kind":"request|reply|progress",
  "correlation_id":"optional",
  "causation_id":"optional",
  "sender_sequence":42,
  "content_type":"application/json|text/plain",
  "payload":{},
  "provenance":"untrusted",
  "created_at":"...",
  "expires_at":"optional",
  "reply_to":"optional"
}
```

The broker authenticates sender identity and fills routing/provenance fields.
Model-provided payload cannot forge them. Payload size, content types, recipients,
and send rate are capability/policy bounded.

Lifecycle, cancel, failure, down, lease, and supervision events use a separate
unforgeable broker control plane. Models cannot choose control-event kinds, and
data-mailbox exhaustion cannot block cancellation/completion/down observation.

### Delivery semantics

- Append durably before reporting send success.
- At-least-once delivery.
- Stable message IDs deduplicate duplicate enqueue attempts only.
- Durable enqueue order by sender generation; no global/completion order.
- Per-message delivery generation/state and ack, never an authoritative single cursor.
- Unacked messages can be redelivered after reconnect.
- Expired messages become terminal audit events.
- Full mailboxes return stable retryable backpressure; send is cancellable, holds
  no mailbox/session lock while waiting, and never silently drops messages.

Prompt delivery is receiver-policy controlled, bounded, sender/generation labeled,
and untrusted. Internal metadata stays out of band. Low-level receive/ack are host
APIs initially. Delivery atomically creates a durable receiver-turn/input record
containing `(message_id,delivery_generation)`; broker ack follows that commit,
before arbitrary effects. Recovery resumes that recorded turn instead of creating
a duplicate input turn. Tool/external effects retain their own idempotency/audit
contracts and exactly-once effects are not promised.

### API

```text
agent_message__send
agent_message__reply
agent_message__monitor
agent_message__demonitor
```

The ordinary agent loop receives a host event when deliverable messages arrive.
Messages never interrupt a tool call. Steering policy decides whether they queue
for the next iteration or begin a new turn.

The ordering key is `(sender_session,sender_generation,receiver,sender_sequence)`;
sequence allocates atomically with append and retries reuse ID/sequence. Message
state is `accepted → available → leased/delivered → acked`, or terminal
`expired|dead_letter|undeliverable`. Expiry prevents new delivery but does not undo
started effects. Ack names `(receiver,message_id,delivery_generation)` and is
idempotent after terminal state. A settled contiguous watermark is optimization only.

### Authorization and authority

Default addressing permits parent↔direct-child and retained-child relationships.
Additional topology requires operator-defined policy. A message can request an
operation but cannot provide capabilities, tools, credentials, mounts, trust,
approval, or memory authority. The receiver independently authorizes every action.

### Monitoring and supervision

Monitors receive host-generated lifecycle signals for a session/generation:
`started`, `idle`, `progress`, `completed`, `failed`, `budget_exhausted`,
`cancelled`, and `down`.

Initial restart policies are `never` and bounded `on_transient_failure`. Logical
failure, verification failure, policy denial, or bad output never qualifies as a
transient crash automatically. Restart creates a new generation with the same or
narrower ceiling and preserves the prior failure evidence.
Restart has max-restarts/window, exponential backoff+jitter, and terminal
`restart_exhausted`. Budget exhaustion, cancel, expiry, policy, verification, and
logical failures never restart. A monitor registered after termination immediately
receives one durable current `down` event.

### Storage and audit

Mailbox events use EP-59. Payload is stored once under sensitivity/retention policy;
session traces hold digest references, never duplicate secret bodies. Quotas apply
per receiver and sender→receiver so one sender cannot monopolize capacity.

## Migration / rollout

1. Add durable parent-child messages without automatic prompt delivery.
2. Add receive/ack and restart reconciliation.
3. Integrate retained-child replies and follow-ups.
4. Add monitoring and opt-in transient supervision.
5. Add broader operator-approved topologies only after abuse/backpressure tests.

## Failure modes

- Duplicate enqueue is idempotent by message ID. A failure before receiver-input
  commit leaves the message unacked and eligible for another bounded delivery attempt.
- Dead-letter attempts count only authorization/materialization/input-commit
  failures. Once the receiver input is committed and acked, repeated model/turn
  failure transitions the turn/session to failed or restart-exhausted; it does not
  dead-letter or redeliver the message as a new input.
- Mailbox exhaustion: sender receives backpressure error and retry hint.
- Receiver deleted: terminal undeliverable event.
- Crash before receiver-input commit may redeliver. Crash after commit/ack resumes
  the unique recorded turn without a new message input; arbitrary tool/external
  effects retain their own at-least-once/idempotency contracts.
- Forged authority in payload: treated as untrusted content and reauthorized.

## Test strategy

- Property/state-machine tests for send, deliver, ack, retry, expiry, and dedup.
- Crash injection at every durable transition.
- Ordering tests across multiple senders and generations.
- Mailbox quota, rate, payload, dead-letter, and backpressure tests.
- Cross-topology and authority-transfer adversarial tests.
- Trace-causality and restore end-to-end tests.

## Open questions

- Progress coalescing is required before high-frequency producers are enabled;
  the initial release caps count, bytes, attempts, and TTL and backpressures excess.

## Decision log

### D1. At-least-once, not exactly-once

- **Decided:** durable at-least-once delivery with IDs and acknowledgements.
- **Alternatives:** best effort; exactly once.
- **Why:** crash-safe exactly-once effects are not generally achievable across
  arbitrary agent actions; explicit idempotency is honest.

### D2. Per-pair FIFO only

- **Decided:** order sender generation to receiver, not globally.
- **Alternatives:** total ordering across all agents.
- **Why:** total order adds coordination without a demonstrated product need.

### D3. Messages do not convey authority

- **Decided:** receiver authorization ignores claims in the payload.
- **Alternatives:** transferable capability tokens embedded in messages.
- **Why:** capability attenuation and broker projection remain the sole authority path.

### D4. Restart logical failures never by default

- **Decided:** default `never`; only narrow transient failures may opt into restart.
- **Alternatives:** OTP-style restart on any abnormal exit.
- **Why:** an LLM repeating a bad plan is not made safer by automatic retries.

## Related

- Erlang/OTP design principles
- EP-33 Responsive Supervisor/Worker Lanes
- EP-55 Retained Sub-agents
