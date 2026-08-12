---
ep: 61
title: Linked EP Relationship Metadata
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Implemented
type: Process
created: 2026-08-12
extends: ["EP-0001"]
history:
  - date: 2026-08-12
    status: Accepted
    note: Accepted as a navigability refinement to the EP metadata process.
  - date: 2026-08-12
    status: Implemented
    note: Migrated the EP catalogue and authoring guidance to canonical links.
  - date: 2026-08-12
    status: Implemented
    note: Corrected the representation after GitHub rendered Markdown embedded in YAML values literally; canonical labels now mirror to ordinary Markdown navigation.
---

> **Relationships:** **Extends:** [EP-0001](./0001-ep-purpose-and-guidelines.md)

# EP-0061: Linked EP Relationship Metadata

## Problem

EP-0001 defined relationship metadata as lists of numeric EP IDs. Those values
are compact and machine-readable, but they are not directly navigable in a
rendered EP. The catalogue also acquired an `extends` field without defining
its relationship to `extended-by` in the canonical authoring guidance.

## Decision

Relationship frontmatter stores canonical `EP-NNNN` labels as YAML strings.
Each EP with relationship metadata also has a generated **Relationships** line
immediately after its frontmatter. That line uses ordinary Markdown links and
is the human navigation surface. Bare numeric IDs and Markdown syntax embedded
inside YAML values are invalid.

The relationship fields are `requires`, `extends`, `supersedes`,
`superseded-by`, `extended-by`, and `see-also`:

- `extends` points from a later EP to an earlier EP whose design it builds on
  without replacing.
- `extended-by` is the reciprocal link from that earlier EP to the later EP.
- Adding either side of this strong extension relationship requires updating
  the other side in the same PR.
- The existing EP-0001 semantics for the other fields remain unchanged.

This EP refines EP-0001's metadata representation and extension rules; it does
not replace the rest of the accepted EP process.

## Migration

The existing catalogue is migrated atomically. New EPs use canonical labels in
the template and mirror them as rendered links. Repository validation checks
field syntax, targets, ordering, the rendered mirror, and reciprocal extension
links.

## Decision log

### D1. Separate metadata from presentation

- **Decided:** Keep compact labels in YAML and render links in ordinary
  Markdown immediately after the frontmatter.
- **Why:** GitHub does not parse Markdown nested inside its frontmatter table;
  the split keeps metadata stable and navigation genuinely clickable.

### D2. Define both directions of extension

- **Decided:** `extends` and `extended-by` are reciprocal metadata.
- **Why:** Readers need to navigate both from a refinement to its foundation
  and from an older design to its follow-up.

## Implementation clarification — 2026-08-12

EP-0001's accepted examples retain the numeric representation that was current
when that process record was adopted. They are historical, not authoring
templates. For current relationship syntax and rendered navigation, this EP
and `docs/eps/README.md` govern.
