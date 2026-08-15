---
ep: 51
title: Lua Lifecycle Hook Contract
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Implemented
type: Standards
created: 2026-07-10
implemented-in: v0.77.0
requires: ["EP-0009", "EP-0044"]
extends: ["EP-0009"]
extended-by: ["EP-0064"]
see-also: ["EP-0004", "EP-0005", "EP-0010", "EP-0017"]
history:
  - date: 2026-07-10
    status: Implemented
    version: v0.77.0
    note: >
      Retrofitted the shipped Lua lifecycle-hook system as its canonical
      Standards contract. This EP supersedes EP-9 D3 for Lua lifecycle hooks;
      EP-9 remains authoritative for budgets and the notification-only shell
      post_turn hook. The implementation audit also removed dofile, loadfile,
      module, and require from the Lua base environment so the documented
      no-filesystem boundary is true in code.
---

> **Relationships:** **Requires:** [EP-0009](./0009-session-guardrails-and-hooks.md), [EP-0044](./0044-repo-config-trust-boundary.md) · **Extends:** [EP-0009](./0009-session-guardrails-and-hooks.md) · **Extended by:** [EP-0064](./0064-wasm-lifecycle-applications.md) · **See also:** [EP-0004](./0004-git-native-sessions-and-audit.md), [EP-0005](./0005-capability-based-sandboxing.md), [EP-0010](./0010-interop-surfaces-mcp-acp-headless.md), [EP-0017](./0017-tool-surface-policy-and-plugin-approval-ui.md)

# EP-51: Lua Lifecycle Hook Contract

## Problem

EP-9 deliberately made its original shell `post_turn` hook informational.
The later Lua lifecycle-hook implementation added synchronous deny and mutate
decisions at LLM and tool boundaries, reversing EP-9 D3 without first giving
that more powerful surface a Standards contract. The feature documentation
described the code, but the trust boundary, failure posture, audit behavior,
and surface coverage were easy to overstate.

This EP is the canonical contract for Lua lifecycle hooks. It supersedes
EP-9 D3 only for this hook family. The shell `[hooks].post_turn` notification
and EP-9's budget decisions are unchanged.

## Goals

- Define the five lifecycle points and their deny/mutate semantics.
- Keep payload mutation narrow enough to preserve append-only history.
- State the config trust boundary and Lua VM capability boundary.
- Pin ordering, timeout, build failure, and runtime failure behavior.
- Name the surfaces that are wired and those that are not.
- Make tool denials and mutations auditable.

## Non-goals

- Replacing the kernel sandbox or broker policy with Lua policy.
- Letting hooks mutate conversation history or tool identity/classification.
- Treating a post-action deny as an undo operation.
- Changing the legacy shell `post_turn` notification hook.
- Claiming lifecycle-hook coverage for surfaces that do not construct a
  lifecycle runner.

## Design

### Configuration and trust

Hooks are declared in operator/user config as ordered `[[hooks.lifecycle]]`
entries with exactly one effective source: inline `lua`, or `lua_file` when
inline source is empty. `[hooks]` is stripped case-insensitively from project
`.stado/config.toml` by the EP-44 overlay gate. A repository cannot install,
enable, or change a lifecycle hook.

`BuildLifecycleRunnerWithWarnings` compiles hooks in config order. A missing,
unreadable, invalid, or handler-free script is skipped and surfaced as a
warning. Build-time failures are always fail-open so one broken entry does not
prevent the process from starting. A runner is nil when no valid hook remains;
all call sites treat that as a no-op.

### Points and decisions

The recognized points are:

| Point | Timing | Mutable fields | Deny behavior |
|---|---|---|---|
| `pre_llm` | before provider call | `model`, `system` | abort the provider turn |
| `post_llm` | after generation, before history append | `text` | replace assistant text with the reason |
| `pre_tool` | before tool dispatch | `args` | skip the tool and return an errored result |
| `post_tool` | after dispatch, before model/audit result | `result`, `error` | replace the result and mark it errored |
| `post_turn` | after the completed turn | none with downstream effect | informational only |

Each function returns continue (`nil`), `{deny = "reason"}`, or
`{mutate = {...}}`. Deny wins when a result contains both. Hooks run serially
in declaration order at a point; the first deny short-circuits. A mutation is
validated for the current point and threaded into the next hook, so later
hooks observe the effective payload.

Message history and tool-call identity are deliberately not mutable.
`pre_llm` can change only the per-request system prompt and model;
`pre_tool` can change only raw JSON arguments. This preserves the runtime's
append-only prompt-cache invariant and the registry's tool classification.

### Audit semantics

A `pre_tool` deny writes a denial trace commit when the executor owns a git
session. The commit attributes the deciding hook and reason; no tool or tree
mutation occurs.

A `post_tool` mutation records both the original and effective result in two
linked trace commits, including the original/effective hashes and the hook
identity. The model receives the effective result. A post-action deny or
mutation cannot undo side effects that already occurred.

LLM-side mutations affect conversation history but do not currently create a
separate hook-specific trace commit. The normal persisted conversation records
the effective text.

### Lua capability boundary

Each configured hook owns one mutex-protected gopher-lua state reused across
calls. Only the base, string, table, and math libraries are opened. The base
environment then removes `dofile`, `loadfile`, `module`, and `require`.
`os`, `io`, `debug`, and `package` are not opened. Hooks therefore have no
filesystem, process, module-loader, or network API through the VM.

This is defense in depth, not an untrusted-code promise: hook source is
operator-authored configuration. VM globals persist between calls, so scripts
may cache pure policy state; each `Run` call is mutex-serialized.

### Failure posture

Every hook invocation has a five-second default timeout and panic recovery.
Runtime errors, timeouts, panics, and invalid mutation payloads follow
`[hooks].fail_closed`:

- `false` (default): log and continue with the last valid payload.
- `true`: convert the fault into a deny. PRE actions are skipped; POST outputs
  are replaced according to the point semantics above.

`fail_closed` applies to runtime evaluation only. It does not make startup fail
when a script cannot be read or compiled.

### Surface boundary

The shipped wiring is explicit:

- TUI: all five points. LLM points live in the TUI stream loop; tool points
  live in the shared executor.
- `stado run`: all five points through `runtime.AgentLoop` plus the executor.
- Headless `session.prompt`: all five points through `runtime.AgentLoop` plus
  the executor.

ACP, MCP server, direct `stado tool run`/daemon dispatch, and nested subagent
loops do not currently build a lifecycle runner. They still use shared runtime
and executor code, but a nil hook runner makes lifecycle policy inert on those
surfaces. This is an explicit compatibility boundary, not implicit parity.
Extending coverage requires a follow-up that defines concurrency, warning
delivery, and client-visible denial behavior for that surface.

## Migration / rollout

The runtime contract is additive for users without lifecycle hooks. Existing
valid scripts keep their point names and payload shapes. Scripts that relied
on the accidentally exposed base loaders (`dofile`, `loadfile`, `module`, or
`require`) now fail, by design; `lua_file` remains the supported way for the
operator to load a hook source file at startup.

## Failure modes

- A bad source file is skipped with a startup warning; other hooks still load.
- A runtime fault follows `fail_closed` and names the hook in stderr output.
- A deny reason is surfaced to the model/operator through the point's result.
- A post-action decision does not roll back side effects and must not be
  presented as if it did.
- A surface outside the wiring table does not run hooks; docs and tests must
  not imply otherwise.

## Test strategy

- Unit tests cover discovery, deny, mutate, chaining, short-circuit, timeout,
  panic recovery, invalid payloads, and both failure postures.
- Sandbox tests assert that OS, IO, package, and base file/module loaders are
  absent.
- Executor tests assert pre-tool denial audit and two-commit post-tool mutation
  provenance.
- Agent-loop and TUI tests assert equivalent LLM-point behavior and effective
  history text.

## Decision log

### D1. Supersede EP-9 D3 only for Lua lifecycle hooks

- **Decided:** Lua hooks may synchronously deny and mutate; the shell
  `post_turn` hook remains notification-only.
- **Alternatives:** force both hook families into the original informational
  contract, or silently broaden EP-9.
- **Why:** the two surfaces have different power and failure semantics; one
  overloaded contract would be misleading.

### D2. Keep mutation field-scoped

- **Decided:** each point exposes only the fields listed above.
- **Alternatives:** arbitrary message/tool object replacement.
- **Why:** narrow mutation preserves append-only history, stable tool identity,
  and audit attribution.

### D3. Fail open by default, fail closed by explicit operator choice

- **Decided:** convenience hooks do not wedge work, while policy hooks can opt
  into hard denial on evaluation failure.
- **Alternatives:** one fixed posture for every hook.
- **Why:** notification/formatting hooks and security gates have different
  operational needs, but the choice must remain operator-authored.

### D4. Document real surface coverage

- **Decided:** coverage follows explicit runner construction, not shared-code
  inference.
- **Alternatives:** claim parity because surfaces share `AgentLoop`/Executor.
- **Why:** a nil runner is behaviorally significant; callers need a reliable
  answer about whether policy executes.

## Related

- [EP-9: Session Guardrails and Hooks](./0009-session-guardrails-and-hooks.md)
- [EP-44: Repo-config trust boundary](./0044-repo-config-trust-boundary.md)
- [Lifecycle hooks](../features/lifecycle-hooks.md)
- [Shell post-turn hooks](../features/hooks.md)
