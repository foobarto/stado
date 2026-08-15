---
ep: 67
title: Session Controller Capabilities and Non-Authority Application Selection
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Accepted
type: Standards
created: 2026-08-14
requires: ["EP-0050", "EP-0059", "EP-0063", "EP-0064", "EP-0066"]
extends: ["EP-0050", "EP-0059", "EP-0063", "EP-0064", "EP-0066"]
history:
  - date: 2026-08-14
    status: Accepted
    note: Added crash-safe ancestry-verified compacted-child subject handoff with stable credential staging and exact lost-reply replay.
  - date: 2026-08-14
    status: Accepted
    note: Accepted during the pre-v1 corrective architecture review to separate session control, quality-workflow selection, and operator-origin artifact authority.
---

> **Relationships:** **Requires:** [EP-0050](./0050-broker.md), [EP-0059](./0059-durable-event-and-budget-substrate.md), [EP-0063](./0063-plugin-defined-harness-artifacts.md), [EP-0064](./0064-wasm-lifecycle-applications.md), [EP-0066](./0066-canonical-plugin-authority-and-application-placement.md) · **Extends:** [EP-0050](./0050-broker.md), [EP-0059](./0059-durable-event-and-budget-substrate.md), [EP-0063](./0063-plugin-defined-harness-artifacts.md), [EP-0064](./0064-wasm-lifecycle-applications.md), [EP-0066](./0066-canonical-plugin-authority-and-application-placement.md)

# EP-0067: Session Controller Capabilities and Non-Authority Application Selection

> **Implementation status (2026-08-14):** Session controller authentication,
> durable logical-session adoption, exact application candidate selection, and
> ancestry-verified compacted-child handoff are source-complete. The EP remains
> Accepted because security-significant fresh artifact activation still needs
> a predeclared policy or separately trusted presenter; no in-process command
> or UI callback may impersonate it.

## Problem

The broker's mode-0700 Unix socket and peer-credential check establish the
local OS principal. They do not establish that an arbitrary same-UID client
controls a particular live session. Several new native lifecycle operations
initially accepted a raw session ID for binding, event publication, or schedule
projection. A second client that learned that identifier could interfere with
the session without possessing its controller state.

The corrective supervise design exposed a different category error. EP-64
said the application should activate its proposed contract through an
operator-origin grant before beginning. That couples a quality workflow to an
artifact authority transition even though selecting the contract neither
widens the signed plugin's capabilities nor makes the contract general prompt
authority. An in-process TUI command also cannot prove fresh operator intent
under EP-50's hostile-orchestrator model.

These are three distinct concepts:

1. control of a live broker session;
2. selection of versioned configuration inside an already-admitted application;
3. promotion of generated content into durable prompt or policy authority.

They need different mechanisms and must not borrow one another's names.

## Decision

### Session controller capability

On session creation, the broker mints a random 256-bit controller bearer. It
returns the plaintext once to the native session controller and retains only a
SHA-256 digest. Constant-time comparison authenticates later session-bound
native operations.

The controller capability is required for:

- artifact and lifecycle application admission;
- native durable application-event publication;
- scheduling projection and enforcement polling;
- session taint changes and termination;
- creation of broker children or peers derived from the parent session;
- any later native operation that names an existing session as its authority
  source.

The token never enters WASM linear memory, broker WAL records, trace events,
decision logs, command arguments, environment variables, or operator output.
It is cleared from the native handle on close. A child receives its own bearer;
it does not inherit the parent's.

This is a session-controller credential, not operator-origin authority. A
fully compromised orchestrator that already possesses its own in-memory bearer
can use the broker operations policy permits. The token prevents unrelated
same-UID clients and raw session-ID replay from joining that authority; it does
not repair compromise of the controller process itself.

### Exclusive lifecycle admission

Only one live lifecycle binding may exist for an exact
`(session ID, generation, canonical plugin namespace)` tuple. An authenticated
rebind atomically invalidates the previous opaque binding while retaining the
durable cursor keyed by the stable tuple. Ordinary artifact-only bindings stay
independent because they do not consume the application event cursor.

This permits crash/restart recovery without allowing duplicate application
instances to acknowledge each other's events. Termination invalidates every
binding for the session.

### Durable logical-session reattachment

A TUI process restart must not manufacture a new broker identity for an
existing git conversation. The broker therefore records a durable logical
session scope in the canonical WAL. The scope binds one broker-minted session
ID and generation to both the canonical repository identity and the current
exact git-session subject. Adoption accepts the stored recovery bearer plus
the current working directory; the broker derives the repository identity
itself and rejects a subject, repository, terminated state, or bearer
mismatch. The host does not choose the recovered session ID, generation,
plugin namespace, event cursor, worker run, hold, timer, or input projection.

The native orchestrator stores the recovery bearer in a no-follow 0600 file
below a no-follow 0700 state directory. The broker WAL contains only its
SHA-256 digest. These permissions prevent guessing, forgery by another UID,
and accidental cross-client control. They do **not** protect the bearer from a
malicious process already running as the same UID: such a process can usually
inspect or interfere with the live orchestrator, which necessarily possesses
the equivalent controller capability. This is an explicit local-user trust
assumption, not stronger client authentication.

First creation uses a broker-minted two-phase reservation. An authenticated
live parent asks the broker to reserve the exact canonical repository and git
subject. The broker durably records only unique ticket/resume digests plus a
short expiry, then returns the plaintext once. The client atomically writes the
0600 credential before asking `session.create` to consume that exact
reservation. Losing the reservation reply leaves only an expiring record with
no session or application authority. Losing the create reply is recoverable
from the already-written bearer. Client-selected, low-entropy, reused,
unreserved, expired, cross-repository, or mixed ticket/resume values are
rejected; format validation is not treated as authority. The reservation is
also bound to the exact live parent controller that requested it. If a daemon
restart destroys that process-local parent before commit, a newly
authenticated parent for the same broker-derived principal and repository may
supersede the authority-free reservation, atomically save the newly minted
bearer, and then commit. It cannot consume the old parent's bearer directly.

Successful adoption preserves the stable session ID and generation, rotates
the 256-bit live controller, advances a controller version, and atomically
invalidates every old artifact/application binding. Application state remains
under its existing `(session, generation, plugin)` keys, including journals,
event cursors, worker runs, holds, controls, timers, completions, deferred
input, and continuation records. A clean close durably detaches the scope;
termination is irreversible. A broker WAL epoch change makes a previously
attached scope adoptable after daemon restart, while a renewable in-process
lease prevents two live clients from adopting it concurrently.

The disk recovery bearer remains stable in this one-phase protocol. Rotating
it in the adoption response would create a crash window in which the broker
has invalidated the old value but the client has not yet atomically replaced
its file, permanently stranding valid application state. Any future recovery
bearer rotation requires a two-phase stage-and-confirm protocol. This does not
weaken live exclusivity: the controller and binding version still rotate on
every adoption, and an unexpired live lease rejects a second adopter.

Automatic compacted-child recovery uses a separate two-phase subject handoff.
The source controller reserves the exact child and canonical source-turn ref;
the broker independently verifies direct git ancestry and durably pins the
current repository, principal, generation, controller version, controller
digest, and stable bearer digests. The native client writes the same stable
credential under the child subject before commit. Commit changes only the
broker-recorded logical subject, rotates controller and bindings, and preserves
all application WAL keys. A crash before commit leaves the source authoritative;
a crash after commit can adopt the already-staged child. If the commit reply is
lost, one exact replay authenticated by the prior controller and stable bearer
rotates away the lost live controller and returns a fresh one. An unresolved
outcome remains fail-closed on the native side. Manual forks never use this
transition and cannot inherit the scope.

### Application-local candidate selection

A lifecycle application may use one exact candidate artifact version as its
own versioned quality-workflow configuration when all of the following hold:

- the artifact is session-scoped and visible under the application's
  broker-verified binding;
- its qualified kind belongs to the same canonical plugin identity;
- the application records the exact artifact ID and version in its durable
  namespaced journal before applying workflow effects;
- restart re-reads that exact candidate and fails closed on absence, version
  mismatch, schema mismatch, scope mismatch, or identity mismatch;
- the candidate is not projected into general prompt context and the selection
  grants no capability, scope, budget, or broker authority.

Selection is application state, not an artifact authority transition. It does
not change `candidate` to `active`, consume an operator grant, or claim that a
TUI route proves fresh operator intent. A manifest-declared operator command
may initiate this quality workflow because the signed application already has
its ceiling; the command does not widen it.

For supervise, `/supervise start` proposes the session-scoped contract,
durably selects the exact returned ID/version, and begins the quality gate.
The watchdog's ability to hold, pause, or stop comes from the plugin's signed
manifest plus broker admission, not from the contract artifact. This extends
EP-64's implementation clause that previously required activation of the
contract before work began; its quality semantics and exact-version binding
remain unchanged.

### Operator-origin activation remains separate

Changing an artifact to `active` remains EP-59 operator-origin authority. An
application command, slash-command origin flag, `stado_ui_approve`,
`stado_ui_choose`, same-UID socket peer, controller bearer, or application
binding is not such a grant.

The authority issuer stays broker-private. A future activation coordinator may
accept an exact candidate reference, re-read it, compute the canonical action,
and issue/consume a short-lived grant only after either:

- a predeclared broker-owned operator policy authorizes that exact action; or
- a separately trusted presenter supplies proof the hostile orchestrator
  cannot forge.

Until that presenter or policy exists, security-significant fresh activation
fails closed. A quality-only UI confirmation may improve usability, but must
not be described or stored as operator-origin authority.

## Consequences

- Raw session identifiers are routing labels, not native authority bearers.
- WASM still never receives broker bindings or controller credentials.
- Persistent application recovery has one consumer per durable cursor.
- Restart reopens the exact repository/session broker scope instead of
  attaching durable application state to a newly minted peer.
- Supervise can start as a quality gate without laundering a TUI callback into
  a security credential or making its contract prompt-active.
- Candidate artifacts can serve as exact application-local configuration, but
  cannot leak into general prompt retrieval or grant new effects.
- Learn/memory or other workflows that promote content into shared prompt
  authority still require the real EP-59 activation mechanism. Existing direct
  TUI grant issuance remains architecture debt and must be removed rather than
  generalized from the supervise exception.

## Test strategy

- Missing, wrong, cross-session, and terminated controller bearers reject every
  session-bound native operation.
- Plaintext bearers never appear in stored session handles, WAL records, logs,
  traces, events, JSON diagnostics, or guest payloads.
- Concurrent lifecycle binds leave exactly one usable binding; the old token
  fails immediately and the durable cursor survives the rebind.
- Crash/restart adoption preserves the whole application projection, rotates
  the live controller, rejects an active second adopter, rejects a different
  repository or logical subject, and never writes plaintext bearers to WAL.
- Credential storage rejects symlinked directories/files and modes other than
  0700/0600. A terminated scope is never adoptable.
- An application can select and recover its own exact session candidate, but
  cannot select a foreign kind/session/version or query it as active.
- Candidate selection does not create an authority event or grant record.
- Application commands and generic UI imports cannot invoke artifact
  activation.
- Architecture tests confine grant issuance to the broker authority/coordinator
  package and forbid TUI/CLI code from opening the canonical broker WAL.

## Decision log

### D1. Session control uses a distinct unguessable bearer

- **Decided:** authenticate all native session-bound operations with a
  per-session controller capability whose plaintext is not retained.
- **Why:** same-UID IPC authentication does not prove possession of a live
  session, while a raw session ID is observable routing metadata.

### D2. Quality selection is not artifact activation

- **Decided:** an admitted application may bind its own workflow to an exact
  candidate without changing artifact authority.
- **Why:** selection grants no new effect. Requiring an operator-origin grant
  would either block quality applications or encourage a false claim that the
  hostile orchestrator can authenticate itself.

### D3. Prompt authority still requires EP-59 proof

- **Decided:** no in-process UI or command route can mint activation authority.
- **Why:** presentation and security authority are separate. The limitation is
  architectural and must be stated honestly rather than hidden behind an
  opaque token.

### D4. Logical-session recovery is stable; live control rotates

- **Decided:** bind one durable broker scope to the broker-recorded canonical
  repository plus exact current git-session subject, retain a stable disk
  recovery bearer, and rotate the in-memory controller/binding version on
  adoption.
- **Why:** stable `(session, generation)` identity recovers the entire
  application state. A one-phase disk-bearer rotation is not crash safe;
  exclusive live control comes from the renewable lease and rotated controller
  rather than making the recovery file self-invalidating.
