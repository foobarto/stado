# Learn lifecycle application

Core stado no longer exposes native `stado learn` or `stado learning`
commands. The accepted learning workflow is owned by the same official,
TUI-only lifecycle package as `/memory`.

> **Availability (2026-08-14):** The package source is complete in the staged
> official plugin repository, but it is unsigned and unpublished. `/learn`
> appears only after the signed package is published, installed, admitted, and
> explicitly enabled. There is no native CLI or TUI fallback.

## Review flow

```text
/learn
/learn tool argument corrections
```

The application catalogs and opens a bounded set of authorized session
evidence, records a durable review intent, invokes the configured provider, and
strictly validates at most five suggestions. Every resulting memory or lesson
is a candidate. The broker accepts only opaque evidence receipt IDs issued for
exact opens under the same plugin/session/generation binding and persists the
broker-derived evidence references.

The durable journal records the review identity, evidence set, provider, model,
usage, cleanup facts, result, proposal materialization, and completion. A
durable result whose proposals were only partly materialized is recovered before
new evidence is selected, so restart does not strand it. Journal projection is
strictly bounded and fails closed if the relevant history is truncated.

There is one honest cost limitation: provider invocation has no durable
idempotency primitive. If the provider succeeded but its reply was lost before
the result was journaled, the outcome is ambiguous. Recovery refuses to invoke
the provider automatically again, avoiding an unacknowledged duplicate charge,
but it cannot claim that the first call did not charge.

## Authority

The application has no activation capability and does not interpret a command
or UI boolean as operator authority. Fresh promotion awaits a separately
trusted, predeclared EP-59 presenter that binds a consumed grant to the exact
artifact version, body, scope, and actor. Historically approved legacy records
may already be active after the one-way migration because that preserves a
previous operator grant; it is not fresh model-selected activation.

Candidates and active artifacts remain untrusted guidance below operator and
repository instructions. They cannot grant tools, expand scope, approve a
plugin, or rewrite their provenance.

See [Memory and learn lifecycle application](memory.md) for migration and
retrieval details, and [EP-0052](../eps/0052-learn-trajectory-refinement.md)
for the accepted target contract.
