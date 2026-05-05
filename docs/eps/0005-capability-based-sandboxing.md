---
ep: 5
title: Capability-Based Sandboxing
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Implemented
type: Standards
created: 2026-04-23
implemented-in: v0.1.0
see-also: [6, 9, 10, 12, 37, 38]
history:
  - date: 2026-04-23
    status: Accepted
    note: Retrofitted from the sandbox and threat-model work that landed across Linux, macOS, MCP, and plugin surfaces before v0.1.0.
  - date: 2026-04-23
    status: Implemented
    version: v0.1.0
    note: Capability-based policy, Linux/macOS enforcement, and explicit degraded-mode behavior are shipped.
---

# EP-5: Capability-Based Sandboxing

## Problem

Coding agents execute untrusted output from both the model and the tools
the model invokes. A UX prompt asking "are you sure?" is not a
meaningful containment boundary. stado needs a runtime where the things
the agent can do are bounded by declared capabilities and enforced by
the OS or the WASM runtime.

The same project also needs one shared capability model that can be
applied consistently to bundled tools, external MCP servers, and WASM
plugins, even though individual surfaces expose different capability
families on top of that common core.

## Goals

- Express runtime permissions as explicit capabilities.
- Enforce those capabilities in the kernel or runtime, not in prompt
  text.
- Reuse one capability model across tools, MCP servers, and plugins.
- Degrade on weaker platforms with explicit warnings rather than silent
  false security.

## Non-goals

- Perfectly identical sandbox behavior on every operating system.
- Plain-HTTP support inside the network sandbox.
- Treating human approval as a substitute for kernel or runtime policy.

## Design

The core abstraction is `sandbox.Policy`, which constrains filesystem
reads and writes, executable paths, environment variables, network
policy, and working directory. Policy composition is an intersection:
merging can narrow permissions but never widen them.

Linux enforcement is layered and shipped:

- Landlock constrains filesystem access
- bubblewrap isolates exec-oriented processes
- seccomp reduces the syscall surface
- an HTTPS CONNECT allowlist proxy constrains network egress

macOS enforcement is also shipped. stado generates `sandbox-exec`
profiles from the same policy vocabulary and launches child processes
under those derived profiles.

Windows v1 is explicitly degraded. stado warns that the platform is not
receiving equivalent isolation and does not describe the current Windows
path as matching Linux or macOS enforcement.

The core capability model is shared across runtime surfaces. Minimal
examples include:

- `fs:read:<path>` / `fs:write:<path>`
- `net:<host>` / `net:allow` / `net:deny`
- `exec:<binary>`
- `env:<VAR>`

Bundled tools, MCP stdio servers, and plugins all consume that shared
policy model. Plugins receive capability-gated host imports; MCP
children receive derived child-process policies; local tool execution
receives OS-level confinement from the same declared capabilities. The
plugin/runtime surface also includes additional capability families
beyond the minimal examples above, so the listed strings are examples of
the shared model rather than an exhaustive grammar for every surface.

Known platform gaps are part of the standard, not exceptions to it. The
runtime is allowed to degrade only when it is explicit, warned, and
represented honestly in UX and documentation.

## Decision log

### D1. Enforce capabilities in the runtime, not in the prompt

- **Decided:** the security boundary is capability-based runtime
  enforcement.
- **Alternatives:** rely on prompts, reviews, or soft approval gates.
- **Why:** untrusted tool output and prompt injection make intent-based
  controls insufficient on their own.

### D2. Use one capability vocabulary across surfaces

- **Decided:** tools, MCP servers, and plugins share one capability
  model.
- **Alternatives:** one config shape for MCP, another for plugins, and
  bespoke flags for local exec.
- **Why:** a shared vocabulary keeps the security model legible and
  reduces the chance that one surface quietly becomes more privileged.

### D3. Prefer layered Linux enforcement

- **Decided:** Linux uses Landlock, bubblewrap, seccomp, and the CONNECT
  proxy together.
- **Alternatives:** pick one sandbox primitive and accept its gaps.
- **Why:** filesystem, process, syscall, and network isolation solve
  different problems; no single mechanism covers them all well.

### D4. Fail open only with loud warnings

- **Decided:** unsupported or degraded paths are explicit and warned.
- **Alternatives:** refuse to run everywhere weaker than Linux or stay
  silent and hope the user notices.
- **Why:** a weaker platform is still useful, but only if stado is honest
  about the missing boundary.

## Related

- [EP-6: Signed WASM Plugin Runtime](./0006-signed-wasm-plugin-runtime.md)
- [EP-9: Session Guardrails and Hooks](./0009-session-guardrails-and-hooks.md)
- [EP-10: Interop Surfaces: MCP, ACP, and Headless](./0010-interop-surfaces-mcp-acp-headless.md)
- [EP-12: Release Integrity and Distribution](./0012-release-integrity-and-distribution.md)
- [DESIGN.md](../../DESIGN.md#sandbox-internalsandbox)
- [docs/features/sandboxing.md](../features/sandboxing.md)
