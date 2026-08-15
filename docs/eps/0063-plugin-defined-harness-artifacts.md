---
ep: 63
title: Plugin-Defined Harness Artifacts
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Accepted
type: Standards
created: 2026-08-14
requires: ["EP-0038", "EP-0039", "EP-0050", "EP-0053", "EP-0058", "EP-0059"]
extends: ["EP-0053"]
extended-by: ["EP-0066", "EP-0067", "EP-0069"]
see-also: ["EP-0015", "EP-0016", "EP-0031", "EP-0052", "EP-0054"]
history:
  - date: 2026-08-14
    status: Accepted
    note: Generic broker artifacts, authenticated imports, strict signed projections, and the fail-closed staged legacy migration are source-complete. The official memory/learn package remains unsigned and unpublished, and fresh activation awaits a separately trusted presenter.
  - date: 2026-08-14
    status: Accepted
    note: Clarified exact `self#<local-kind>` read/observe selectors. The host and broker independently bind them to the admitted canonical identity so development packages need neither a forged official lock nor a wildcard grant.
  - date: 2026-08-14
    status: Accepted
    note: Accepted after the full EP catalogue and supervise architecture audit.
  - date: 2026-08-14
    status: Draft
    note: Initial draft generalizing EP-53 beyond hardcoded memory and lesson records.
---

> **Relationships:** **Requires:** [EP-0038](./0038-abi-v2-bundled-wasm-and-runtime.md), [EP-0039](./0039-plugin-distribution-and-trust.md), [EP-0050](./0050-broker.md), [EP-0053](./0053-versioned-harness-artifacts-and-index.md), [EP-0058](./0058-measured-adaptive-retrieval.md), [EP-0059](./0059-durable-event-and-budget-substrate.md) · **Extends:** [EP-0053](./0053-versioned-harness-artifacts-and-index.md) · **Extended by:** [EP-0066](./0066-canonical-plugin-authority-and-application-placement.md), [EP-0067](./0067-session-controller-and-application-selection.md), [EP-0069](./0069-agent-owned-memory-authority.md) · **See also:** [EP-0015](./0015-memory-system-plugin.md), [EP-0016](./0016-learning-self-improvement-plugin.md), [EP-0031](./0031-fs-cap-path-templates.md), [EP-0052](./0052-learn-trajectory-refinement.md), [EP-0054](./0054-addressable-context-and-research-agents.md)

# EP-0063: Plugin-Defined Harness Artifacts

> **Implementation status (2026-08-14):** The generic broker envelope,
> authenticated artifact/evidence ABI, archived signed descriptors,
> deterministic projections, secret filtering, and one-way legacy migration
> are source-complete. The official memory/learn package remains unsigned and
> fresh activation has no separately trusted presenter, so the EP remains
> Accepted and core provides no native fallback.

## Problem

EP-53 moved memory and lessons into a versioned broker-owned artifact store,
but its public shape still hardcodes those two application concepts. Summary,
content, trigger, expected outcome, and validation are common columns even
though most are specific to the learning application. The WASM ABI still uses
`stado_memory_*`, writes a separate legacy JSONL store, and cannot authenticate
the calling session well enough to expose session-scoped artifacts.

This makes a generic primitive look like a built-in application. A supervision
contract, research result, policy exception, evaluation case, or future plugin
record must either pretend to be a memory, add another hardcoded kind to stado,
or create a parallel store with weaker authority and indexing semantics.

## Goals

- Keep scope, authority, provenance, evidence, sensitivity, relations,
  versioning, and observations broker-owned and uniform.
- Move kind-specific fields into a dynamic `data` object validated by a schema
  declared in the plugin's signed manifest.
- Derive globally stable kinds from canonical plugin identity rather than a
  display alias supplied by plugin code.
- Replace the `stado_memory_*` ABI with capability-gated `stado_artifact_*`
  imports backed by the authenticated broker.
- Preserve deterministic, rebuildable indexing without executing plugin code
  inside the broker indexer.
- Keep historical artifacts inspectable after plugin upgrade or removal.

## Non-goals

- Letting a plugin define authority transitions, scope visibility, retention,
  sensitivity rules, or relation leak behavior.
- Treating a JSON schema or a signed plugin as operator approval for artifact
  activation.
- Running arbitrary plugin extractors in the broker or SQLite rebuild path.
- Using `stado_cfg_state_dir` as an authority or shared-storage bridge.
- Promising that every artifact kind is useful to generic prompt retrieval.

## Design

### Common envelope and kind data

The canonical versioned envelope is:

```json
{
  "api_version": "stado.dev/artifact/v1",
  "id": "art_...",
  "version": 3,
  "kind": "github.com/acme/reviewer/checks#review-contract",
  "kind_schema": {
    "plugin_identity": "github.com/acme/reviewer/checks@v2.1.0",
    "plugin_commit": "0123456789abcdef0123456789abcdef01234567",
    "manifest_digest": "sha256:...",
    "local_name": "review-contract",
    "schema_digest": "sha256:..."
  },
  "scope": "session|repo|global",
  "scope_binding": {
    "principal": "host populated",
    "canonical_repo_id": "host populated when applicable",
    "anchor_session_id": "host populated when applicable",
    "anchor_fork_point": "host populated when applicable"
  },
  "authority": "candidate|active|rejected|superseded|retired|deleted",
  "tags": ["quality:review"],
  "groups": ["stado/supervision"],
  "provenance": {"origins": ["untrusted"], "created_by": "plugin"},
  "evidence_refs": ["session:.../turn:..."],
  "sensitivity": "normal|private|secret",
  "data": {},
  "created_at": "...",
  "updated_at": "...",
  "expires_at": "..."
}
```

`data` must be a JSON object. The broker validates it against the exact archived
schema identified by `kind_schema`; it does not contain Go switches for plugin
kinds. The qualified `kind` is host-constructed from the plugin instance's
stable, unversioned canonical source namespace and its manifest-declared local
name. The exact version remains in `kind_schema`, so plugin upgrades retain one
logical queryable kind without losing historical interpretation. Plugin aliases
and manifest `name` fields are presentation only and cannot create identity.

The common envelope remains deliberately opinionated. Scope and immutable
binding prevent data leaks; authority prevents generated prose from becoming
instructions; provenance, evidence, sensitivity, and relations compose across
applications; version and timestamps support optimistic concurrency and audit.
Application-specific fields do not belong at that level.

Relations remain separately versioned broker records whose endpoint visibility
is checked on both write and read. They are part of the common artifact service,
not an inline field a plugin can smuggle into `data` or the envelope.

The memory and lesson kinds therefore use data such as:

```json
{
  "summary": "Run the race suite after scheduler changes",
  "content": "The ordinary suite did not reproduce the failure.",
  "trigger": "A scheduler or mailbox path changes",
  "expected_outcome": "The race suite passes",
  "validation": "go test -race ./internal/runtime/..."
}
```

Other kinds are not required to use any of those fields.

### Signed manifest declaration

A plugin manifest may declare artifact kinds:

```json
{
  "artifact_kinds": [{
    "name": "review-contract",
    "schema": "{...exact JSON Schema bytes...}",
    "index": [
      {"pointer": "/objective", "role": "title"},
      {"pointer": "/constraints", "role": "text"},
      {"pointer": "/criteria", "role": "text"}
    ]
  }]
}
```

Local names are bounded lowercase identifiers and unique within one manifest.
Schemas use the supported, bounded JSON Schema subset, require an object root,
and are compiled at install/load time. Unknown schema vocabulary is rejected,
not ignored. Exact canonical schema bytes, schema digest, manifest digest,
canonical identity, version, and resolved commit are archived in the broker
before the kind can accept artifacts.

`index` is an optional deterministic projection. JSON pointers select bounded
string or string-array values from `data`; roles are a small host-defined set
such as `title`, `text`, and `trigger`. The broker executes no WASM and calls no
plugin callback while rebuilding an index. Invalid/missing projected values are
reported and skipped according to the signed declaration; they never invalidate
the canonical artifact after schema validation.

An upgraded plugin may change a schema only by producing a different digest.
Existing versions keep the exact old descriptor. A plugin may declare an
explicit converter for its own application workflow, but conversion proposes a
new artifact version through the ordinary broker API; the indexer never runs it.

### Broker-owned registration and identity

Canonical plugin identity becomes an explicit runtime input separate from the
manifest. For remote plugins it is the EP-39 identity and resolved commit; for
bundled plugins it is a release-stable `stado.dev/bundled/<plugin>@<version>`
identity bound to the stado build; a signed local-development installation gets
an explicit unstable, install-path-bound `local://...` identity and cannot
impersonate a bundled or remotely sourced plugin.

At admission, the broker independently reopens the installed package, checks
the source-scoped pinned signature and WASM digest, and recomputes either the
exact EP-39 lock identity or the exact local install-path identity. It then
intersects capabilities with the active session and global policy and mints an
opaque binding scoped to the plugin identity, session generation, principal,
repository, ancestry, and
ceiling. The WASM guest never sees or supplies that binding. A host bridge may
transport it, but cannot use request fields to widen it. Local development
identities require an explicit broker trust path and cannot obtain artifact
authority merely by supplying a self-consistent manifest.

The broker registers kinds only from that verified admission and archives
unseen descriptors idempotently. Uninstalling a plugin removes its executable
availability, not the archived descriptor required to inspect historical data.
The binding is capability authority, not proof that prose was authored by an
uncompromised plugin: proposals remain candidates and evaluative observations
still require their independent host/operator evidence under EP-53.

### WASM artifact imports

The pre-1.0 memory imports are replaced, not aliased indefinitely:

```text
stado_artifact_propose(request_json, response_buffer)
stado_artifact_query(query_json, response_buffer)
stado_artifact_edit(request_json, response_buffer)
stado_artifact_observe(request_json, response_buffer)
```

The host derives plugin identity, principal, repository, active session,
ancestry, and capability ceiling. Those fields are absent from guest authority
input. The guest names one of its declared local kinds when proposing and uses
qualified kind selectors when querying. It cannot propose another plugin's
kind, forge session ancestry, or activate a candidate through these imports.
For an exact kind declared by the same manifest, `self#<local-kind>` is a
non-authoritative selector: the host rewrites it from the authenticated runtime
identity and the broker independently resolves the signed capability during
admission. Wildcard and undeclared self selectors are invalid.

Capabilities are operation- and kind-scoped:

```text
artifact:propose:<local-kind>
artifact:read:<qualified-kind-pattern>
artifact:edit:<local-kind>
artifact:observe:<qualified-kind-pattern>
```

The `<qualified-kind-pattern>` position also accepts the exact
`self#<declared-local-kind>` form described above. It never accepts `self#*`.

Broad read patterns are high-trust declarations and still intersect broker
scope/sensitivity policy. `edit` creates a candidate version under EP-53 rules;
it does not mutate an active version in place. Activation, rejection,
retirement, deletion, aliases, and operator grants remain trusted operator or
host actions outside the model-facing guest ABI.

Imports call typed broker methods. Neither the plugin host nor a TUI wrapper may
open the shared WAL. Mutations may carry a bounded guest logical
`idempotency_key`; omission derives one from the exact operation/payload. The
broker hashes that logical key with the verified plugin namespace, operation,
principal, scope, and exact scope binding. Same-input retries therefore return
the exact prior durable result across sessions for global scope and across
broker restart; reuse with different normalized guest-controlled input fails
closed. Session keys additionally bind the session/generation and cannot cross
them. There is no dual write. The broker resolves every authority field and
capability from its opaque admission binding and remains the only authoritative
writer under EP-59.

This replaces EP-53's provisional `${state}/artifacts/` physical store wording.
Artifacts, kind descriptors, relations, and observations are typed namespaces
inside EP-59's one canonical broker WAL. Derived indexes may have separate
rebuildable files, but never a parallel authoritative log.

### Generic query and derived index

Queries filter common envelope fields and may carry a kind-specific data query
only where the registered descriptor declares a supported projection. Generic
results always include the qualified kind and schema identity. Callers that do
not understand a kind can still render the envelope and raw bounded `data`.

Query responses are digest-fenced pages: each returns at most 50 items plus
`page_digest`, `next_offset`, and `complete`. Offset zero establishes a digest
over the complete ordered visible `(id,version)` projection; every later page
must repeat it. A changed projection rejects the continuation and requires a
restart at zero. Offsets are bounded and a nonzero offset without an exact
digest fails closed. Applications must also impose their own total aggregate
bound; pagination is not permission for unbounded guest memory.

The rebuildable index stores common fields plus descriptor-selected text. Its
location and schema are artifact-oriented rather than memory-oriented. Search
results identify the index sequence and completeness under EP-58. Private and
secret handling, relation endpoint authorization, quotas, and observations keep
EP-53 semantics.

## Migration / rollout

1. Add canonical runtime plugin identity and manifest kind validation.
2. Teach the artifact service to store `data` plus archived kind descriptors;
   add deterministic declared projections to the derived index.
3. Admit the exact signed official memory/learn identity and migrate EP-53
   memory/lesson fields into its declared `data` objects while preserving IDs,
   versions, authority, exact legacy bytes, and an auditable converter
   descriptor. Core stado fabricates neither a bundled identity nor hardcoded
   production kinds.
4. Add authenticated broker RPC and `stado_artifact_*` host imports.
5. Port bundled/installed consumers and remove `stado_memory_*`, the legacy
   `MemoryBridge`, and its separate `memory.jsonl` writer.
6. Rename cache/layout text from memory to artifacts where compatibility does
   not require retaining an old read path.

Migration is one-way, idempotent, and logically atomic. Bounded migration stages
are inert until one final marker binds and validates the complete ordered stage
set, exact installed identity and descriptors, source watermark, archive digest,
and destination expectations. A compatibility reader recognizes only the exact
retired shapes during migration; new writes never emit the old top-level shape.
Historically approved memories and lessons become active uniformly because that
preserves an already granted legacy authority event; fresh candidates still
require the independent activation path.

## Failure modes

- Missing/invalid schema declaration: plugin installation or activation fails.
- Descriptor archive unavailable: artifact write fails before an unknown kind
  can enter the log.
- Plugin removed: historical artifacts remain inspectable from archived schema;
  application-specific editing is unavailable until a compatible owner returns.
- Index projection points at the wrong type: canonical write remains valid when
  its schema permits it, projection records a bounded diagnostic, and rebuild
  continues without that value.
- Broker unavailable or identity context absent: host import fails closed; it
  never falls back to ambient files or a direct WAL writer.
- Capability requests another plugin's local kind: deny before broker mutation.
- Schema evolution races an edit: expected descriptor digest/version conflict is
  returned and the caller must reload.

## Test strategy

- Manifest tests for kind names, schema subset, digest stability, projections,
  canonical identity, duplicate kinds, and bounded inputs.
- Artifact property tests over arbitrary valid `data` objects and descriptors.
- Cross-plugin identity/capability/scope/sensitivity adversarial tests.
- Golden migration tests for every memory/lesson state and legacy archive.
- Historical rendering tests after upgrade, uninstall, and descriptor rotation.
- Broker IPC tests proving independently verified opaque admission, expiry on
  session/generation change, CAS, idempotency, and no guest control of
  binding/authority fields.
- Index rebuild/search parity tests with multiple unrelated plugin kinds.
- ABI tests that reject all removed `stado_memory_*` imports after migration.

## Open questions

- Whether a later release needs declarative display hints beyond indexed title
  and text projections. Raw JSON plus generic projections is sufficient first.
- Whether installed plugins may query broad cross-plugin kind patterns by
  default operator policy; the capability grammar supports it but initial policy
  may restrict it to bundled applications.

## Decision log

### D1. Plugin-defined data, host-defined envelope

- **Decided:** plugins own kind schemas and `data`; the broker owns the common
  authority, scope, evidence, provenance, and lifecycle envelope.
- **Alternatives:** hardcode every kind in Go; let plugins own whole records.
- **Why:** this preserves a small native authority primitive without making
  stado the application-schema registry or trusting guests with visibility.

### D2. Kind identity derives from canonical plugin namespace

- **Decided:** the host constructs qualified kinds from the stable unversioned
  namespace of the effective EP-39 identity and a signed local name; the exact
  version and commit stay in the schema descriptor.
- **Alternatives:** manifest display name; free-form kind string; schema digest
  alone.
- **Why:** names collide and can be forged, while identity plus archived schema
  remains attributable and historically interpretable.

### D3. Index projections are declarative

- **Decided:** signed JSON pointers select values for host-defined index roles.
- **Alternatives:** execute WASM extractors during rebuild; index all JSON text;
  refuse indexing for dynamic kinds.
- **Why:** deterministic projections retain rebuildability and containment while
  allowing useful search across application-defined data.

### D4. The old memory ABI is replaced

- **Decided:** migrate to `stado_artifact_*` and remove the ambient legacy bridge.
- **Alternatives:** retain memory aliases indefinitely; add a second artifact ABI.
- **Why:** pre-1.0 is the right time to remove a misleading, unauthenticated
  contract instead of teaching new plugins both paths.

## Related

- [Plugin ABI reference](../plugins/abi-reference.md)
- [Plugin host imports](../plugins/host-imports.md)
- [What Survives the Window](../articles/adaptive-context.md)
