---
ep: 62
title: Rendered EP Relationship Links
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Implemented
type: Process
created: 2026-08-12
extends: ["EP-0001"]
supersedes: ["EP-0061"]
history:
  - date: 2026-08-12
    status: Accepted
    note: Accepted as a rendering correction to EP-0061.
  - date: 2026-08-12
    status: Implemented
    note: Split canonical relationship metadata from its rendered navigation line.
---

> **Relationships:** **Extends:** [EP-0001](./0001-ep-purpose-and-guidelines.md) · **Supersedes:** [EP-0061](./0061-linked-ep-relationship-metadata.md)

# EP-0062: Rendered EP Relationship Links

## Problem

EP-0061 stored Markdown link syntax inside YAML strings. GitHub displays YAML
frontmatter in a metadata table but does not parse Markdown nested in those
values, so readers saw literal `[EP-NNNN](path)` text instead of links.

## Decision

Relationship frontmatter stores canonical `EP-NNNN` labels as YAML strings.
Each EP with relationship metadata also has a generated **Relationships** line
immediately after its frontmatter. That line uses ordinary Markdown links and
is the human navigation surface.

The frontmatter remains the single semantic source. Validation must ensure the
rendered line contains the same fields, labels, order, and targets. Bare numeric
IDs and Markdown syntax embedded inside YAML values are invalid.

This EP supersedes EP-0061's representation while retaining its relationship
semantics and catalogue-wide navigability requirement.

## Decision log

### D1. Separate metadata from presentation

- **Decided:** Keep compact labels in YAML and render links in Markdown.
- **Why:** GitHub reliably renders ordinary Markdown links, while tooling can
  parse stable identifiers without interpreting Markdown syntax inside YAML.

## Implementation clarification — 2026-08-12

EP-0001's accepted examples retain the numeric representation that was current
when that process record was adopted. They are historical, not authoring
templates. For current relationship value syntax and rendered navigation,
EP-0062 and `docs/eps/README.md` govern.
