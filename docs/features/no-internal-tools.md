# No Internal Tools

**Accepted contract:** every stado-owned model-visible product behavior belongs
in a signed WASM plugin. Native Go exposes generic primitives and enforcement;
it does not own application workflow. See
[EP-0002](../eps/0002-all-tools-as-plugins.md),
[EP-0037](../eps/0037-tool-dispatch-and-operator-surface.md),
[EP-0038](../eps/0038-abi-v2-bundled-wasm-and-runtime.md), and
[EP-0066](../eps/0066-canonical-plugin-authority-and-application-placement.md).

## The boundary

Native stado may provide capabilities WASM cannot obtain from inside its
garden:

- filesystem, process, network, terminal, provider, and cryptographic
  primitives;
- broker-stamped session/turn anchors and bounded host observations about tree,
  trace, repository, and child execution;
- broker-owned artifacts, journals, mailboxes, timers, budgets, leases, and
  state transitions;
- operator UI transport and operator-origin grants;
- sandbox, audit, capability, identity, ordering, and scheduling enforcement.

WASM applications decide things two implementations could reasonably decide
differently:

- tool schemas, prompts, output formatting, and integration behavior;
- discovery, skills, research, learning, task, and guidance workflows;
- supervision contracts, detectors, cadence, evidence selection, verdict
  policy, correction, pivots, and completion policy.

A large plugin is not a failure of this boundary. The plugin sandbox and a
clean core exist precisely so product applications can grow and innovate
without gaining ambient host authority.

## No permanent carve-outs

Bootstrapping does not justify native model tools. The operator and loader can
enable signed plugins without giving the model an application-specific native
registry API. If a WASM application lacks a necessary capability, add the
smallest generic primitive and keep the product policy in WASM.

External MCP tools are a different category: stado adapts a tool implemented by
an operator-enabled external server. The adapter remains broker-, sandbox-,
taint-, audit-, and schema-bounded. “External” is not an authority exemption.

## Completed native application cutovers

The native model-visible migration allowlist is empty. Tasks completed the
remaining cutover: the JSON store, native model tool, TUI picker/key/static
command, MCP registration, and default autoload were deleted. The explicit
official lifecycle application owns dynamic `/tasks`, its ordinary Do-turn
tool, and global plugin-defined broker artifacts; publication remains release
work, never a reason to restore a native fallback.

Guidance is no longer part of this list. Native stado exposes a bounded
opaque-binding session-context projection, volatile TUI quality facts, the live
registry ceiling, and a 16 KiB append-only contribution validator. The explicit
official `guidance` lifecycle application owns classifiers, thresholds,
ordering, and wording. It is TUI-only; no run/headless/ACP/subagent
native prompt fallback exists.

Supervise is no longer part of this list. Its native command, model tools,
detectors, prompts, workflow service, evaluator, and fallback path were removed;
the official lifecycle application source lives in `foobarto/stado-plugins`.
Its offline-key signature and publication are release work, not permission to
restore a native implementation.

Research is no longer native migration debt. The unsigned development source
for `memory__research` and `session__research` lives in the explicit official
`research` WASM package and spawns ordinary broker-created read-only child
AgentLoops. It is not a shipped installed surface until that package is signed,
published, installed, and release-verified. The six signed
`agent_child_only` helpers appear only under an exact child `narrow_tools`
projection owned by the loader-verified signed spawning package namespace and have exact
per-tool evidence capabilities. Native Stado retains
only authenticated corpus scope, immutable session ranges, bounded
catalog/search/open operations, durable read receipts and budgets, and
mechanical citation validation. The private provider loop, native tool
registrations, direct corpus adapters, and CLI/TUI WAL bridges are removed.

Registry discovery is no longer part of this list either. Native stado exposes
only a bounded loader-bound catalog projection and a digest-fenced atomic
session-surface edit. Search, grouping, formatting, describe-and-activate, and
whole-package workflow live in the official `tool-registry` WASM package. Its
eight tools are explicitly installed and autoloaded by the operator; stado does
not silently bundle or make them non-disableable.

Model-invocable skills are no longer part of this list. Native stado exposes
only operation/kind-scoped, digest-fenced context facts with model visibility,
scope/provenance, immutable identity, and exact allowed-tool ceilings. Search,
matching, open, result formatting, and activation requests live in the
explicitly installed official `skills` WASM package. The opened body remains a
tool result; native prompt listings, exact-name handlers, and synthetic user
messages have no fallback. Explicit operator `/skill`, slash, and `--skill`
gestures remain native because they accurately represent operator input.

Provider invocation is no longer part of the native-application debt. Native
stado exposes only `stado_provider_invoke`: provider construction and
credentials, loader-authenticated identity, cancellation, exact manifest token
ceilings, accounting, and bounded facts. Prompt/request shape, output policy,
and the model-facing `llm__invoke` contract live in the explicit-opt-in
official WASM source. The MCP server retains only the adapter for tools actually
implemented by an operator-enabled external MCP server.

Bundled core tools are no longer part of this debt. Each module now owns a
source-adjacent embedded manifest, and one verified loader derives its model
name, description, schema, class, categories, capabilities, and ABI export.
There is no native fallback or per-tool implementation switch.

The architecture guard carries a shrinking allowlist for these paths. It may
shrink; it may not grow. The target before v1 is empty.

## Shared dispatch and identity

Every plugin invocation surface uses the shared runtime path so capability
checks, canonical identity, broker bindings, audit attribution, lifecycle
bridges, and output bounds do not depend on whether a call came from TUI,
`stado run`, headless, ACP, MCP, an override, or another plugin.

`Manifest.Name` is presentation metadata. Installed and bundled production
paths must supply source-bound canonical identity; they never fall back
to a local identity. That identity owns secrets, instance state, artifact kinds,
lifecycle instances, broker admission, idempotency, and audit attribution.

## Author checklist

Before adding native model-visible code, ask:

1. Is this a broker-verified or kernel-observed fact the guest cannot supply,
   an OS/runtime operation, a durable authority write, or an enforcement seam
   that WASM cannot provide?
2. Could two product implementations choose different policy here?
3. Can one smaller typed host import make the application possible in WASM?

If the second answer is yes, the behavior belongs in a plugin. If the third is
yes, add the primitive rather than the native application.

Plugin authors should use the current
[host-import reference](../plugins/host-imports.md) and
[ABI conventions](../plugins/abi-reference.md). Removed delegate imports and
memory-specific aliases are not compatibility surfaces before v1.
