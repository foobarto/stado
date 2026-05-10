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

---

## Handoff (2026-05-10)

### What shipped

Option A landed in commit `7549f37`. The auto-compact manifest
now has a single canonical source — the template at
`plugins/bundled/auto-compact/plugin.manifest.template.json`.
`internal/runtime/background_defaults.go::autoCompactManifest()`
and `autoCompactSchema()` are gone, replaced by a call to
`bundled.MustManifest("auto-compact")`.

New host-side surface in `internal/plugins/bundled/`:

- `manifest.go` — `Manifest(name)` / `MustManifest(name)` parallel
  to the existing `Wasm(name)` / `MustWasm(name)`.
- `manifests/auto-compact.json` — embed-friendly copy of the
  template, committed so a fresh clone builds without first
  running `build.sh`.

`plugins/bundled/build.sh` adds a `MANIFEST_OUT` path and a copy
step at the end that keeps `manifests/<name>.json` in sync with
each plugin's `plugin.manifest.template.json`. Today the loop
covers just `auto-compact`; future background plugins are added
to that loop.

The template was brought in line with the production values the
Go code had been overriding (author `stado`, short tool
description, nonce `bundled-auto-compact`). Two fields that the
Go code overrode with `version.Version` are now stable:
`version: "0.1.0"` and `min_stado_version: "0.1.0"`. That's the
plugin's own functional version, decoupled from the stado binary
release version. User-visible side-effect: `stado plugin list`
shows `auto-compact v0.1.0` instead of `auto-compact v0.48.4`.

### What's left

- **Option C — manifest-flag refactor** stays deferred. The
  trigger is a second background plugin (`token-budget`,
  `telemetry-bridge`, etc.). When that lands, add `background:
  bool` to the manifest schema, have the host enumerate
  `bundled.List()` and start flagged plugins, and delete
  `DefaultBackgroundPlugins`/`LookupBackgroundPlugin`/
  `BundledBackgroundPlugin` along with whatever's left of
  `background_defaults.go`. Today's `for name in auto-compact;
  do …; done` loop in `build.sh` already generalises to N
  plugins for free.

- **Inventory registration inconsistency** flagged in the spec's
  Out-of-scope section is unchanged: auto-compact registers via
  `init()` in the bundled package, the others register from
  `internal/runtime/bundled_plugin_tools.go` at runtime startup.
  Orthogonal to manifest dedup; left for whoever picks up the
  Option C refactor.

### What surprised

- The template carried real divergence from the Go code's effective
  behaviour, not just a stale copy. Three different fields
  (`author`, `description`, `nonce`) plus the version overrides.
  The template was effectively a phase-7.1b demo artefact never
  retrofitted to production; the Go code had been the de-facto
  canonical source while the template lay drifting. This refactor
  flipped the polarity — template is canonical now.
- Wasm-rebuild byte drift on the verification rebuild: identical
  trap as Phase C of v0.48.2 (Go toolchain produces non-byte-stable
  output between runs). Reverted via `git checkout HEAD --
  internal/plugins/bundled/wasm/` before commit. Same fix as last
  time; possibly worth a build-script flag to suppress the
  rebuild during `make build` flows that don't need it.

### What to watch

- **Plugin authors who edit `plugin.manifest.template.json`** for
  auto-compact will now see their changes reflected at runtime
  after `bash plugins/bundled/build.sh`. Without running the build
  script, the embedded copy at
  `internal/plugins/bundled/manifests/auto-compact.json` stays
  stale — both files are committed, so fresh clones get the
  current state, but in-place edits need `build.sh` to propagate.
  Worth noting in `CONTRIBUTING.md` if/when one exists.
- **Version-string change.** Anywhere downstream that parsed
  auto-compact's manifest version (probably nowhere) would now
  see `0.1.0` instead of the binary version. Caught only on
  release-cut for the next session if it surfaces.
