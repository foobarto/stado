# Memory and learn lifecycle application

Core stado no longer has a native `stado memory` command, a `[memory]` config
section, or an ambient memory context path. Memory and learning policy belongs
to one official lifecycle application, and that application runs only in the
interactive TUI.

> **Availability (2026-08-14):** The official package is source-complete in the
> staged `stado-plugins` repository but is unsigned and unpublished. The
> commands below appear only after the signed package is published, installed,
> admitted with its exact identity, and explicitly enabled. There is no native
> compatibility fallback.

## Application commands

The package declares these commands:

```text
/memory status
/memory on
/memory off
/memory list
/memory add <text>
/learn [focus]
```

`/memory on|off|status` changes the exact session/generation application
setting in the broker journal. `/memory list` shows a bounded, digest-fenced
view. `/memory add` proposes a candidate; it does not activate one. `/learn`
reviews bounded broker-opened evidence and can only propose receipt-bound
candidates.

Fresh candidate activation is intentionally absent. An in-process command or
UI response is not proof of operator intent. Promotion remains unavailable
until a separately trusted, predeclared EP-59 presenter can issue and consume a
grant for the exact artifact version, text, and scope.

When enabled, the application's `pre_llm` callback contributes a small labeled
projection of active, authorized artifacts. The host validates a strict
response envelope and byte/item bounds. This contribution is TUI-only:
`stado run`, headless, ACP, and child application contexts do not receive an
implicit native memory block.

## Legacy data migration

The only legacy bridge is the lifecycle-only
`artifact:migrate:legacy-memory-v1` operation. The application can trigger it,
but cannot choose a path, supply bytes, forge identity, select scope bindings,
or control destination IDs.

The broker reads the fixed rooted legacy file `memory/memory.jsonl`, archives
the exact bytes and fsyncs the archive before any canonical write, and verifies
the installed application identity plus both declared kind/schema digests.
Large stores are split into bounded inert stages. Nothing becomes visible until
one final completion marker validates the complete ordered stage set, exact
source watermark, archive digest, identity, and destination expectations.

Migration is one-way and fail-closed. A changed source, corrupt or unbound
record, conflicting destination, missing/extra stage, archive mismatch, or
quarantine condition leaves the source untouched and exposes no partial
artifacts. A completed marker permanently fences rereading the source.

Historically approved memories and lessons both migrate to `active`, preserving
authority already granted through the old operator workflow. Candidate,
rejected, superseded, and deleted states remain exact. This historical rule
does not let the application activate a fresh candidate.

Legacy aliases are preserved only as bounded provenance references and
migration evidence; `legacy_id` is not part of the generic artifact envelope.

## Scope and sensitivity

The broker derives principal, repository, session anchor, ancestry, and
generation from the authenticated binding. The guest cannot widen them. Secret
records are preserved canonically and remain available to native/operator audit,
but application queries and evidence surfaces exclude them, including exact-ID
opens.

Plugin authors should use the generic artifact imports documented in
[Plugin host imports](../plugins/host-imports.md) and
[EP-0063](../eps/0063-plugin-defined-harness-artifacts.md). A new
memory-shaped JSONL writer or `stado_cfg_state_dir` authority bridge is not a
supported extension point.
