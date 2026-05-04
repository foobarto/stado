---
ep: 0030
title: Security-research default harness — agent / subagent / skills / plugins
author: Bartosz Ptaszynski
status: Placeholder
type: Standards
created: 2026-05-04
history:
  - date: 2026-05-04
    status: Placeholder
    note: Idea capture per EP-0001 §"Placeholders". Not yet a worked design.
see-also: [0002, 0008, 0013, 0017]
---

# EP-0030: Security-research default harness — agent / subagent / skills / plugins

> **Status: Placeholder.** This EP reserves a number and captures the
> problem statement + intended scope. The worked design is deferred —
> see EP-0001 §"Placeholders" for the lifecycle.

## Problem

stado's default out-of-the-box harness behaviour (the system prompt
template, the bundled subagent definitions, the example skills, the
default plugin set, the behavioural conventions for all of the above)
is currently general-purpose. For the workflows the operator most
cares about — bug bounty hunting, penetration testing, and the
programming activities that support them — a general-purpose harness
under-delivers in measurable ways:

- **Recon discipline.** Information-gathering should be the first
  phase of any engagement; the default harness doesn't enforce
  this, leading the agent to jump to exploitation before mapping
  the attack surface.
- **Data organisation.** Engagement notes accumulate in unbounded
  scratch files; the agent doesn't have a default convention for
  structured storage (engagement folder layout, walkthrough
  templates, scan archives, loot retention).
- **Critical / counterfactual thinking.** The default agent rarely
  asks "is this actually exploitable, or just a weakness?"; rarely
  validates abusability with an end-to-end PoC; rarely compares the
  attacker's pre- and post-condition states to compute genuine
  delta-uplift over what they could already do.
- **Anti-confirmation-bias hygiene.** First-look signals get
  promoted to "findings" without verification. The agent doesn't
  default-maintain candidate vs verified lists, doesn't push back
  on its own claims, doesn't switch frame when stuck.
- **Programming as support, not as goal.** When the agent writes
  helper scripts (recon harnesses, exploit code, payload
  generators), it should default-treat them as throwaway tools
  servicing the engagement, not as production code. The current
  defaults swing toward over-engineering.

## Goals (intended; not yet designed)

- A bundled subagent definition (or set of definitions) that codify
  the recon → organise → exploit → verify → write-up loop.
- A bundled set of skills covering the standard analytical moves
  (cross-version diff, sibling-product comparison,
  reference-implementation lookup, abusability filter,
  prerequisite-vs-impact check, candidate vs verified split,
  frame-switch when stuck — i.e., the behavioural recipes that
  already live in the user's `~/Dokumenty/htb-writeups/CLAUDE.md`
  and the htb-loop skill).
- A bundled engagement-folder convention (`notes/engagements/<box>/`
  layout, walkthrough template, scan/loot archives) the harness
  defaults to.
- A bundled set of plugins covering the operator-tooling that
  these workflows lean on (CVE-index lookup, payload generators,
  recon-summary classifiers, postmortem extractors).
- A behavioural conventions doc that the system prompt pulls in by
  default for security-research-mode runs, with strong opinions
  about analytical discipline, counterfactual thinking, and
  abusability validation.

## Non-goals (intended)

- Replacing the general-purpose default harness for non-security
  workflows. The security-research harness is a **mode**, opt-in
  via configuration or CLI flag.
- Bundling specific exploit kits, vendor-CVE recipes, or
  jurisdiction-specific tooling. Those belong in operator-supplied
  plugins, not in stado's default surface.
- Anything that crosses the line from defensive-and-research-use to
  offensive-against-non-authorised-targets. stado is a tool; the
  operator's authorisation boundary is theirs to enforce.

## Open questions

- Is this one EP or several? The goals list above touches multiple
  surfaces (subagents, skills, plugins, system prompt). It may be
  cleaner to split into per-surface EPs (e.g., EP-0030: Security-
  research subagent definitions; EP-003N: Security-research skills
  catalog) and have this EP serve as the umbrella narrative.
- Mode-selection mechanism: a CLI flag (`stado run --mode
  security`), a config-toml setting (`[harness].mode = "security"`),
  a per-project skill that wires it (`/skill load security-mode`),
  or auto-detection from the project structure (presence of
  `CLAUDE.md` security-mode marker)?
- Reuse vs port: the user's `~/Dokumenty/htb-writeups/` already
  has a substantial harness (`CLAUDE.md`, `notes/operations.md`,
  the htb-loop skill, payload generators, the recon script,
  etc.). How much of that is portable to stado-bundled defaults vs
  how much stays operator-specific?
- Capability/EP-0017 (tool-surface policy and plugin-approval UI)
  interaction: a security-research mode plausibly wants different
  default approval policies (e.g., bash exec under explicit
  approval gates by default; outbound network constrained to the
  declared engagement scope).

## Decision log

(Empty — placeholder. The decision log starts when this EP is
upgraded from Placeholder → Draft.)

## Related

- EP-0002 — All Tools as WASM Plugins. The bundled-plugin part of
  this work would land as new entries under `plugins/default/`.
- EP-0008 — Repo-Local Instructions and Skills. The skills-catalog
  + instructions surface is the natural delivery mechanism for the
  behavioural conventions.
- EP-0013 — Subagent Spawn Tool. The subagent-definitions part of
  this work would build on the subagent surface.
- EP-0017 — Tool Surface Policy and Plugin Approval UI. The
  approval-defaults part of this work interacts directly with
  EP-0017's policy machinery.
- Operator-side prior art (primary inspiration sources for the
  design phase):
  - `~/Dokumenty/bounty-hunting/` — growing collection of
    methodology, rules, and patterns specifically for bug bounty
    work. Should be the canonical input when this EP transitions
    Placeholder → Draft.
  - `~/Dokumenty/htb-writeups/` — existing real-world implementation
    of much of what this EP would generalise + bundle (CLAUDE.md,
    notes/operations.md, scripts/payloads/, .claude/skills/htb-loop/,
    the recon and escalation scripts).
