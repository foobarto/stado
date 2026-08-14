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

## Current migration debt

The source tree still contains native model-visible behavior from the earlier
migration. These are tracked deletions, not accepted exceptions:

- registry meta-tools (`tools.*`, `plugin.load`, `plugin.unload`);
- `skills.load`;
- memory/session research dispatchers;
- the `tasks` tool;
- native guidance composition;
- native supervise tools, detectors, prompts, and workflow services.

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
