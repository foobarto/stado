---
ep: 1
title: EP Purpose and Guidelines
author: Your Name <you@example.com>
status: Draft
type: Process
created: YYYY-MM-DD
history:
  - date: YYYY-MM-DD
    status: Draft
    note: Initial draft — bootstraps the EP process itself, modelled after PEP-1.
---

# EP-1: EP Purpose and Guidelines

## What is an EP?

EP stands for **Enhancement Proposal**. An EP is a design document
that describes a proposed change to a project — a new feature, a
non-trivial refactor, a shift in architecture, or a process change
that affects how contributors work.

The idea is borrowed directly from Python's
[PEP process](https://peps.python.org/pep-0001/), Rust's
[RFC process](https://github.com/rust-lang/rfcs), and Kubernetes' KEPs.
Same motivation: **a numbered, stable, append-only record of the design
decisions that outlive any one implementation PR.**

## Why bother?

Chat history rots. Commit messages are too short. PR descriptions get
buried. `CHANGELOG.md` records *what* changed, not *why*. When a
contributor a year from now asks "why does this work this way?", the
answer should be a file they can read, not an archaeological dig
through chat logs.

EPs exist to answer:

- **What** is being proposed (design, contract, module layout).
- **Why** this approach was chosen over the alternatives (decision log).
- **What** the migration path looks like.
- **Who** decided it and **when**.

They are **not** implementation plans. An EP describes the destination;
the plan describes the route. One accepted EP can produce many
implementation PRs.

## When to write an EP

**Write an EP when:**

- The change introduces a new public contract (CLI flag, config schema,
  on-disk layout, API surface).
- The change touches a load-bearing invariant documented in the
  project's design docs or a previously accepted EP.
- The change reverses or supersedes an earlier decision.
- The change spans multiple modules or milestones and benefits from a
  shared reference.
- A contributor asks "should we do X or Y?" and the answer isn't
  obvious — the discussion itself is worth capturing.

**Do not write an EP for:**

- Bug fixes.
- Dependency bumps.
- Refactors contained to one module that don't change behaviour.
- Documentation tweaks.
- Performance improvements without API changes.

When in doubt, err toward writing one. A rejected EP is cheaper than a
contested feature with no paper trail.

## Lifecycle

```
              ┌────────────────┐
              │  Placeholder   │   (optional starting state)
              └────────┬───────┘
                       │
                       ▼
                ┌───────────┐
                │   Draft   │
                └─────┬─────┘
                      │
           ┌──────────┼──────────┐
           │          │          │
           ▼          ▼          ▼
     ┌──────────┐ ┌───────────┐ ┌──────────┐
     │ Accepted │ │ Withdrawn │ │ Rejected │
     └────┬─────┘ └───────────┘ └──────────┘
          │
          ▼
     ┌───────────────┐
     │  Implemented  │
     └───────┬───────┘
             │
             ▼
     ┌───────────────┐
     │  Superseded   │
     └───────────────┘
```

- **Placeholder** — optional initial state. Reserves a number and
  captures a basic idea (problem statement + a few sentences of
  shape + open questions). Does **not** claim the design space is
  worked out; does **not** authorise implementation. Lower review
  bar to merge; the point is to land the parking spot. See
  §"Placeholders" below.
- **Draft** — author is iterating. Expect edits. Safe to comment on
  and push back against. Design space is being actively worked out.
- **Accepted** — approved for implementation (or, for Informational
  EPs, approved as the canonical record). Content is now treated
  as append-only; substantive changes go in a companion EP that
  supersedes this one.
- **Implemented** — the accepted design has shipped. Optional: add a
  "Shipped in" line referencing a release tag or PR.
- **Superseded** — a later EP has replaced this one. The frontmatter
  gains `superseded-by: <number>`.
- **Withdrawn** — the author pulled it before acceptance. Kept in the
  repo for historical reference.
- **Rejected** — the community/maintainers declined it. Kept for
  historical reference.

The status transition that matters most is **Draft → Accepted**. Before
that line, the document is a conversation. After it, the document is a
contract.

## Placeholders

A **Placeholder** is a triage-approved idea with a number and a
paragraph, but not yet a worked-out design. It exists to capture
thinking that shouldn't be lost without committing to the
work of producing a full Draft.

**Who can land a Placeholder:**

- Maintainers can open Placeholder PRs directly for ideas they want
  to park.
- External contributors can land a Placeholder after going through
  the project's contribution triage. If the maintainer responds
  "go ahead as Placeholder" (rather than "go ahead as Draft"), the
  contributor opens a Placeholder PR.

**What a Placeholder must contain:**

- Frontmatter (ep number, title, author, `status: Placeholder`,
  type, `created`, and a `history` entry).
- A **Problem** section stating what's missing or broken.
- A **Goals / Non-goals** pair — even at sketch level — so the scope
  isn't unbounded.
- An **Open questions** section listing the major decisions still
  to be worked out. This is the load-bearing part of a Placeholder.
- At least one or two decision log entries for the design choices
  that *are* already settled ("this will be data-driven," "this
  supersedes EP-X," etc.). Missing entries are explicitly labelled
  "to be captured during the brainstorm that takes this EP to
  Draft."

**What a Placeholder must NOT do:**

- Claim a design is ready for review.
- Authorise implementation work.
- Include speculative detail the author isn't ready to defend.
- Pretend Open Questions don't exist.

**Promotion to Draft:** when the open questions are worked out and
the design space is substantially explored, the PR changing
`status: Placeholder → Draft` should:

- Fill in the remaining decision log entries.
- Either resolve the Open Questions inline or move them to §"Open
  questions" with the narrower shape.
- Add a history entry recording the promotion and a pointer to any
  brainstorm session or discussion that worked it out.

**Low review bar for merging a Placeholder.** The point of the
status is rapid capture. A Placeholder PR should merge quickly if
the triage-gate was passed — reviewers check the problem statement
and open-questions list for coherence, not for design correctness.
Correctness is the bar for Draft → Accepted, not for Placeholder.

## Numbering

Sequential, four-digit, starting at 0001. New EPs claim the next
available number at **merge time**, not draft time. If two PRs race for
the same number, the first merged wins; the other rebases and bumps.

Reasoning: date-prefixed filenames avoid the collision but make
citations awkward ("see EP-2026-04-17-provider-registry" vs "see
EP-2"). Sequential wins on readability; the rebase cost is trivial at
most project throughputs.

EP-1 is this document, by convention. EP-2+ are content proposals.

## File layout

```
docs/eps/
├── README.md                  # index of all EPs + status legend
├── 0000-template.md           # skeleton for new EPs
├── 0001-ep-purpose-and-guidelines.md
└── NNNN-short-kebab-title.md
```

Filename: `NNNN-short-kebab-title.md`. Keep titles under ~60 characters.

## Required frontmatter

Every EP begins with YAML frontmatter:

```markdown
---
ep: 2
title: Short, descriptive title
author: Name <email@example.com>
status: Draft
type: Standards | Informational | Process
created: YYYY-MM-DD
history:
  - date: YYYY-MM-DD
    status: Draft
    note: Initial draft.
---
```

The top-level `status` field is the **current** state — readers and
tooling look at this first. The `history` field is the **append-only
journal** of every state transition. Both matter; they serve different
audiences.

Optional fields, added as they become relevant:

- `updated: YYYY-MM-DD` — last substantive edit.
- `requires: [N, M]` — EPs this one depends on (the reader should
  read those first for context).
- `superseded-by: N` — this EP is replaced by another; readers should
  follow the link forward.
- `supersedes: [N, M]` — this EP replaces prior EPs listed here.
- `extended-by: [N, M]` — later EPs that build on this one without
  replacing it. Useful for forward navigation so a reader of this EP
  discovers follow-up work.
- `see-also: [N, M]` — loosely related EPs, looser than `extended-by`.
- `implemented-in: vX.Y.Z` — release where this first shipped.
- `discussion-at: <URL>` — link to issue/PR where debate happened.

All EP-reference fields use YAML lists (`[1, 3, 7]`) even when only
one value is present (`[4]`) — makes tooling simpler and keeps the
schema consistent.

### The `history` field

`history` is a YAML list of mappings. Each entry records one state
transition in the EP's life. **Append-only** — entries are never
edited or deleted.

**Required keys per entry:**

- `date: YYYY-MM-DD` — when the transition happened.
- `status:` — the status the EP moved *to* (`Placeholder`, `Draft`,
  `Accepted`, `Implemented`, `Superseded`, `Withdrawn`, `Rejected`).

**Optional keys:**

- `version: vX.Y.Z` — release tag where this state landed. Used
  chiefly for `status: Implemented` entries.
- `note:` — short human-readable context (one sentence). "Initial
  draft," "Approved after architecture review," "Shipped in v0.0.4,"
  "Replaced by EP-42 due to X," etc.
- `superseded-by: N` — when status flips to `Superseded`, record the
  replacing EP here. Mirrors the top-level `superseded-by:` field
  (they must agree).
- `pr: <URL>` — link to the PR that landed this transition.

**Example history:**

```yaml
history:
  - date: 2026-04-17
    status: Draft
    note: Initial draft.
  - date: 2026-04-20
    status: Accepted
    note: Approved after review; implementation queued for v0.1.0.
    pr: https://github.com/example/project/pull/NN
  - date: 2026-08-15
    status: Implemented
    version: v0.1.0
    note: Feature shipped.
    pr: https://github.com/example/project/pull/NNN
  - date: 2027-02-01
    status: Superseded
    superseded-by: 42
    note: Replaced by EP-42 which extends the design.
```

**The rule: every status change appends a history entry.** Draft →
Accepted appends one. Accepted → Implemented appends one. Accepted →
Superseded appends one (and also updates `superseded-by` at the top
level). This happens in the same PR that changes the status.

**Why both top-level `status` and `history`:** the top-level is
ergonomic — one field tells you the current state. `history` is the
audit trail for "when did this ship? when was it reversed?" Without
the journal, those answers require git blame across renames; with
it, `grep -A 2 'status: Implemented' docs/eps/*.md` tells you every
EP that's shipped and when.

**When retrofitting an old decision** into an EP, the initial
`history` entry's date can be the retrofit date (today), with a note
like "retrofitted from pre-EP planning artifacts; original decision
dated around YYYY-MM-DD" if the original timeline matters.

## Types

- **Standards** — proposes a change to the project's code, on-disk
  layout, CLI, API, or user-visible behaviour. Most EPs are Standards.
- **Informational** — documents a design rationale, captures historical
  context, or describes a convention. No implementation work implied.
- **Process** — changes how contributors work. This EP (EP-1) is
  Process. A future "how we version releases" or "how we handle
  security disclosures" would also be Process.

## Expected sections

A Standards-type EP should cover, in roughly this order:

1. **Problem** — what's broken or missing today.
2. **Goals** — what this proposal achieves.
3. **Non-goals** — what this proposal explicitly doesn't do.
4. **Design** — modules, data shapes, contracts, interfaces.
5. **Migration / rollout** — how this lands without breaking users.
6. **Failure modes** — what can go wrong, how it surfaces.
7. **Test strategy** — how the implementation is validated.
8. **Open questions** — decisions deferred to implementation or later
   EPs.
9. **Decision log** — the "why this and not that" record. See below.
10. **Related** — links to prior EPs, notes, or external references.

Not every section is mandatory for every EP — an Informational EP may
skip migration and failure modes. But the structure gives readers a
predictable place to look.

## Decision log

The decision log is the load-bearing bit. An EP without a decision log
is just a design description; with one, it's a design decision.

Every non-obvious choice gets an entry:

```markdown
### DX. Short name of the decision

- **Decided:** what the EP commits to.
- **Alternatives:** what else was considered (even if briefly).
- **Why:** one or two sentences explaining the reasoning.
```

Use **DX** (D1, D2, …) rather than hierarchical numbering. Easy to cite
from later EPs: "EP-2 D6 says we split invocation from parsing, but
EP-7 revisits this because…"

The decision log is append-only once the EP is Accepted. If a later
conversation reverses a decision, the correct move is a new EP that
supersedes this one, not an edit.

## Bootstrap carve-out

The EP framework itself is introduced as a bootstrap. The append-only
rule (see "Updating EPs") only becomes binding **after an EP has been
referenced from outside its own PR** — cited in a commit message,
linked from another shipped EP, mentioned in released docs,
discussed publicly.

Until that threshold, in-place edits to Draft or Accepted EPs are
fine. This prevents applying "production discipline" to EPs that
are still being drafted in rapid iteration, and avoids the
theatre of superseding documents nobody has yet depended on.

Practical rule of thumb:

- **Before the first commit landing the EP:** edit in place freely.
- **After the commit but before any external reference:** in-place
  edits still acceptable; update the `updated:` frontmatter field if
  you want an audit trail.
- **After the EP is cited from outside its own PR:** append-only.
  Substantive changes require a new superseding EP.

The bootstrap window isn't a number of days — it's "has this EP
started being referenced as a stable artifact?" For most EPs that
answer is "yes" as soon as they land. For the initial batch authored
the first few days of the EP process, it's looser.

## Process for proposing an EP

1. Copy `docs/eps/0000-template.md` to a new filename. Use the next
   available number (check the README index).
2. Fill in frontmatter with `status: Draft`.
3. Write the content. Ask for feedback in a PR.
4. Iterate. Comments become commits on the EP's PR.
5. When consensus is reached, update `status: Accepted` and merge.
6. Open implementation PRs that reference the EP number in their
   descriptions.
7. When shipped, update `status: Implemented` and optionally add
   `implemented-in:`.

## Process for withdrawing, superseding, or rejecting

- **Withdraw:** the author updates `status: Withdrawn`, appends a
  history entry noting the reason, and adds a short paragraph at the
  top of the document explaining why. The EP stays in the repo.
- **Supersede:** the replacing EP's frontmatter has `supersedes: [N]`;
  the replaced EP's frontmatter gains `superseded-by: M`,
  `status: Superseded`, and a new history entry. Both stay in the repo.
- **Reject:** a maintainer updates `status: Rejected` and appends a
  history entry with the reason. Used for:
  - External proposals that were reviewed but not accepted.
  - Internal drafts that were explored in depth and ruled out.
  Rejected EPs stay in the repo on purpose — the "we considered this
  and said no" record is load-bearing for avoiding re-litigation.

## Updating EPs — bidirectional links are mandatory

Forward-reference metadata (`extended-by`, `superseded-by`, `see-also`)
is the navigation layer. A reader landing on an old EP should be able
to discover follow-up work without guessing. This only works if the
links are kept bidirectional.

**The rule:** when a new EP extends or supersedes an older one, the
same PR that adds the new EP **must** also update the older EP's
frontmatter. Both changes ship together or neither ships.

Examples:

- **EP-8 extends EP-2.** EP-8's frontmatter lists `requires: [2]` or
  `see-also: [2]`; the PR that adds EP-8 also updates EP-2's
  frontmatter to include `extended-by: [..., 8]`.
- **EP-12 supersedes EP-4.** EP-12's frontmatter has `supersedes:
  [4]`; the PR updates EP-4 to set `superseded-by: 12` and
  `status: Superseded`.

Rationale: without this rule, forward-links rot immediately — nobody
remembers to go back and update the old file months later. Making it a
same-PR requirement is cheap and keeps navigation honest. Review
checklist: "does this new/edited EP need to update any older ones?"

## Rejected alternatives for the process itself

### Date-based numbering

Considered `YYYY-MM-DD-topic` instead of `NNNN-topic`. Rejected because
citations like "see EP-2" are noticeably shorter than "see
EP-2026-04-17-provider-registry" and come up dozens of times in PRs,
commit messages, and chat. Merge-conflict cost of sequential numbering
is trivial at most project throughputs.

### Skipping the process entirely

Considered just keeping `docs/specs/` and calling it a day. Rejected
because EPs add value beyond "spec documents":

- Named status lifecycle (`Draft → Accepted → Implemented`) that tells
  readers what they can rely on.
- Append-only contract after acceptance that keeps history honest.
- Numbered citations that simplify cross-references.
- Decision log as a required section, not an afterthought.

### Lightweight RFCs via GitHub issues

Considered doing everything in GitHub issues with a `rfc` label.
Rejected because issues don't live in the repo — they're hostage to the
hosting platform, and cloning the project doesn't clone the design
history.

## Decision log (for this EP)

### D1. EPs as append-only, not wiki-style

- **Decided:** after Acceptance, substantive edits require a new EP
  that supersedes the old one.
- **Alternatives:** wiki-style living documents that any committer can
  update.
- **Why:** an EP's value is as a point-in-time record. "What did we
  decide on this date, and why?" should have a durable answer even
  after the code changes. Living documents blur the signal between
  "this was the original decision" and "this is the current behaviour."
  The main design doc is the living document; EPs are the decision
  record.

### D2. Decision log as a required section

- **Decided:** every Standards EP must include a decision log
  documenting alternatives and rationale.
- **Alternatives:** leave decision capture optional; rely on PR
  discussion.
- **Why:** the rationale is the part that rots fastest and matters
  most. An EP without a decision log is a spec; projects already have
  spec directories for that. Making it required gives Future Us the
  context to judge whether a decision still holds when circumstances
  change.

### D3. Sequential numbering, not date-prefixed

- **Decided:** `NNNN-kebab-title.md`, starting at 0001.
- **Alternatives:** `YYYY-MM-DD-kebab-title.md`.
- **Why:** citations are cleaner and more common than filename
  collisions. See "Rejected alternatives" above.

### D4. EP-1 is Process, authored as the bootstrap

- **Decided:** the first EP defines the EP process itself, mirroring
  PEP-1.
- **Alternatives:** start with a content EP and document the process
  in a README.
- **Why:** the process document needs the same treatment it prescribes
  for other proposals — versioned, superseded if changed, append-only.
  A plain README can be silently rewritten, losing the "what did the
  process look like when EP-2 was accepted?" answer. EP-1 being
  Process is the standard convention (PEP-1, Kubernetes KEP-1) for
  exactly this reason.

## References

- [PEP-1: PEP Purpose and Guidelines](https://peps.python.org/pep-0001/) —
  the direct inspiration.
- [Rust RFCs](https://github.com/rust-lang/rfcs) — similar
  scope/lifecycle, different numbering.
- [Kubernetes KEPs](https://github.com/kubernetes/enhancements) — a
  richer lifecycle with stages, probably overkill for most projects.

## Related

- `docs/eps/0000-template.md` — skeleton for new EPs.
- `docs/eps/README.md` — index of all EPs and their status.
