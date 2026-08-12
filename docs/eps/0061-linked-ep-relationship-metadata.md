---
ep: 61
title: Linked EP Relationship Metadata
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Superseded
type: Process
created: 2026-08-12
extends: ["EP-0001"]
superseded-by: ["EP-0062"]
history:
  - date: 2026-08-12
    status: Accepted
    note: Accepted as a navigability refinement to the EP metadata process.
  - date: 2026-08-12
    status: Implemented
    note: Migrated the EP catalogue and authoring guidance to canonical links.
  - date: 2026-08-12
    status: Superseded
    superseded-by: ["EP-0062"]
    note: GitHub renders Markdown syntax in YAML frontmatter literally; EP-0062 separates machine metadata from rendered links.
---

> **Relationships:** **Extends:** [EP-0001](./0001-ep-purpose-and-guidelines.md) · **Superseded by:** [EP-0062](./0062-rendered-ep-relationship-links.md)

# EP-0061: Linked EP Relationship Metadata

## Problem

EP-0001 defined relationship metadata as lists of numeric EP IDs. Those values
are compact and machine-readable, but they are not directly navigable in a
rendered EP. The catalogue also acquired an `extends` field without defining
its relationship to `extended-by` in the canonical authoring guidance.

## Decision

All EP-reference fields use YAML lists of quoted relative Markdown links, even
when only one value is present. Each link uses the canonical four-digit
`EP-NNNN` label and exact target filename. Bare numeric IDs are no longer valid
relationship values.

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

The existing catalogue is migrated atomically. New EPs use the linked form in
the template, and documentation closeout audits every relationship field for
bare IDs, missing targets, and absent reciprocal extension links.

## Decision log

### D1. Use Markdown links as YAML strings

- **Decided:** Store canonical relative Markdown links directly in each list.
- **Why:** Links remain readable to simple tooling while becoming immediately
  navigable on GitHub and other Markdown renderers.

### D2. Define both directions of extension

- **Decided:** `extends` and `extended-by` are reciprocal metadata.
- **Why:** Readers need to navigate both from a refinement to its foundation
  and from an older design to its follow-up.
