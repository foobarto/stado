# Kill autoCompactManifest() duplication

**Created by:** consolidate-plugin-packages spec (2026-05-10) — explicit
non-goal, deferred for separate design choice.
**Status:** Open / not scheduled.

## What ships

The auto-compact manifest declared once, in
`plugins/bundled/auto-compact/plugin.manifest.template.json`, and
loaded by the host at startup. The Go-coded duplicate at
`internal/runtime/background_defaults.go::autoCompactManifest()`
goes away.

## Why this isn't done today

After the plugin-package consolidation (commits 7f697db…6cdc057),
`autoCompactManifest()` lives in `internal/runtime/background_defaults.go`
with a `// TODO: dedupe` pointer. It's a Go-coded copy of the
canonical manifest at
`plugins/bundled/auto-compact/plugin.manifest.template.json`, which
is the file the plugin author would update if they changed the
declared capabilities or tool description. The two can drift —
already drifted slightly: the template carries
`author: "stado example"` and a long phase-7.1b description while
the Go version carries `Author: bundledplugins.Author` (now
`bundled.Author = "stado"`) and a shorter description.

The deferred reason: the clean fix needs a design choice that
wasn't worth coupling to a rename PR.

## The design choices

Three viable approaches, each with cost.

### Option A — JSON copy at build time + bundled.Manifest accessor

`plugins/bundled/build.sh` already copies wasm artifacts into
`internal/plugins/bundled/wasm/`. Extend it to also copy the
`plugin.manifest.template.json` of background plugins (today: just
auto-compact) into `internal/plugins/bundled/manifests/<name>.json`.
Add a `bundled.Manifest(name string) (plugins.Manifest, error)`
accessor parallel to `bundled.Wasm`, that reads from a sibling
embed.FS and parses JSON. `background_defaults.go` calls
`bundled.Manifest("auto-compact")` instead of declaring the
manifest in Go.

**Cost.** ~30 lines of new bundled-package code, one extra build
step in build.sh, one new embed.FS. Pattern symmetric with
existing wasm handling. JSON parse cost is trivial (one-time
session bootstrap).

**Benefit.** One canonical source. Plugin authors who edit the
template see the change reflected without touching Go.

### Option B — go:generate from the template

Add a `//go:generate` directive in `background_defaults.go` that
reads the template and writes the Go literal. Authors run
`go generate ./...` after editing the template; CI verifies the
generated file matches.

**Cost.** Generator script (or codegen via `text/template`). CI
check. New step in the contributor workflow that contributors will
forget. No runtime change.

**Benefit.** Compile-time-only — no JSON parsing at startup. Clean
diff: the Go file is generated, edits go to the template only.

### Option C — manifest-flag refactor (kills the whole file)

Add `background: bool` to the manifest schema. At session boot,
host enumerates `bundled.List()`, parses each module's manifest
(loaded via Option A's mechanism or wasm-embedded manifest), starts
the ones with `background: true`. `DefaultBackgroundPlugins`,
`LookupBackgroundPlugin`, `BundledBackgroundPlugin`, and
`autoCompactManifest` all delete entirely.

**Cost.** Schema change (manifest version bump? backwards-compat
story for installed plugins?). Touches `internal/plugins/manifest.go`
and any signing/trust logic that hashes the schema. Higher blast
radius.

**Benefit.** Eliminates the whole "host-side per-plugin policy"
category. Any future background plugin (telemetry-bridge,
session-recorder — see comment in
`internal/plugins/runtime/background.go`) plugs in by setting the
flag, no host code change.

## Recommendation

Option C is the right long-term shape but premature today —
auto-compact is the only background plugin; one consumer doesn't
justify a schema change. Wait for the second background plugin to
appear, then do C.

In the meantime, Option A is the cheap interim. ~30 lines, no new
contributor workflow step, kills the duplication. If a second
background plugin shows up before C is justified, A still
generalizes to N plugins for free.

Skip Option B — go:generate adds workflow friction without saving
runtime cost worth measuring.

## Pre-conditions

- None. The current state is functional; the duplication is a
  drift-risk smell, not a correctness problem.

## Out of scope

- The schema change (Option C) — a separate spec when the second
  background plugin lands.
- Unifying inventory registration paths. Today some bundled
  modules register themselves via `init()` in the bundled package
  (auto-compact's one-liner) while the others get registered by
  `internal/runtime/bundled_plugin_tools.go` at runtime startup.
  That inconsistency is real but orthogonal to manifest dedup.
