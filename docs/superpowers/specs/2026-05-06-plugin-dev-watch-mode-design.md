# Plugin dev watch mode — `stado plugin dev <dir> --watch`

**Status:** approved 2026-05-06; awaiting writing-plans pass.
**Author:** Bartosz Ptaszynski (with brainstorming assistance).
**Branch:** `feat/plugin-dev-watch-mode` (off `main` after the name-resolution merge).

## Problem

`stado plugin dev <dir>` (`cmd/stado/plugin_use_dev.go:61`) collapses the
gen-key → sign → trust → install pipeline into a single command.
Operators still have to re-run it on every wasm rebuild — manually,
or via a custom script. The plugin authoring inner loop (write →
build → test → repeat) is the highest-friction part of plugin
development today.

The user-facing ask:

> When working on a wasm plugin the dev will have a rapid loop
> cycle of write, build, test, repeat. Having to bump version,
> sign, install then use new version on stado will be annoying
> (even if dev can script it - they shouldn't have to). Let's
> make sure the `stado plugin dev` sub-command allows for super-
> streamlined way to work for devs (ideally it would even watch
> for file changes and auto-compile the wasm plugin for the dev,
> auto load and enable it). It will NOT actually permanently add
> the plugin, this is purely for dev only.

## Locked decisions

From the 2026-05-06 brainstorming session (Q1–Q8):

| # | Decision | Rationale |
|---|---|---|
| Q1 | **Reuse the installed-plugin slot in the unified registry.** No separate `DevPluginRegistry`. Dev plugins register through the same `registerInstalledPluginTools` path; persistence-free comes from cleanup on watch-exit, not separate storage. | Unified registry just landed (PR 4); splitting it again would re-fragment the surface. |
| Q2 | **250ms debounce.** Hardcoded constant. | Absorbs save-storms (autoformat-on-save) without feeling sluggish. YAGNI on configurability. |
| Q3 | **Require `<dir>/build.sh`.** No auto-detection of make/tinygo. | Plugins target varied wasm toolchains; `build.sh` is the universal indirection. Missing → clear error. |
| Q4 | **One-time trust bootstrap.** First run: full sign+trust+install. Subsequent rebuilds in the watch loop: re-sign + re-install only (skip trust). | Trust is idempotent; re-running on every save is wasted work. Signing is microseconds. |
| Q5 | **Sandbox: same caps as manifest declares.** No dev bypass. | Dev mode tests prod-like behavior; bypass defeats the point. |
| Q6 | **Dev plugin wins over installed wins over bundled,** via registry overwrite + marker file pinning to `0.0.0-dev`. Watch exit removes install dir + marker. Process crash leaves stale state; `stado plugin dev <dir> --watch` is idempotent on re-init. | Reuses unified-registry's `pickActiveVersion` marker mechanism untouched. |
| Q7 | **One watcher per `dev --watch` invocation.** Multiple plugins = multiple terminals. | Trivially correct; no scope creep. |
| Q8 | **TUI live-reload out of scope.** Operator iterates with `stado tool run` (rebuilds registry every invocation). Long-running TUI sessions don't auto-reload — documented limitation; follow-up cycle adds mtime polling at turn boundaries. | TUI live-reload is its own subsystem (poll cadence, race against running tool calls). Bounded scope keeps this cycle shippable. |

## Architecture

### Component 1 — `--watch` flag on `pluginDevCmd`

`cmd/stado/plugin_use_dev.go`:

Current `pluginDevCmd.RunE` does the full first-time pipeline once and
exits. New behavior:

```go
var pluginDevWatch bool

// Inside pluginDevCmd.RunE, after the existing 4-step first-run
// pipeline succeeds:
if pluginDevWatch {
    return runDevWatchLoop(cmd.Context(), dir, cmd.OutOrStdout(), cmd.ErrOrStderr())
}
return nil
```

Init:

```go
pluginDevCmd.Flags().BoolVar(&pluginDevWatch, "watch", false,
    "After first install, watch <dir> for changes and rebuild + reinstall on save")
```

### Component 2 — `runDevWatchLoop`

New file `cmd/stado/plugin_dev_watch.go` (~250 lines):

```go
package main

// runDevWatchLoop spawns an fsnotify watcher on dir, debounces events,
// runs build.sh on each batch, and re-invokes the sign + install steps
// of pluginDevCmd. Returns when ctx is cancelled (Ctrl+C handled by
// cobra's signal-handling) and cleans up the dev install on exit.
//
// First-run signature/trust/install has already happened by the time
// this is called; the loop only handles subsequent rebuilds.
func runDevWatchLoop(ctx context.Context, dir string, stdout, stderr io.Writer) error
```

Internals:

- `fsnotify.NewWatcher()`, walk `dir` with `filepath.WalkDir`, add
  every directory under it (skipping ignored ones — `.git`, `.stado`,
  `node_modules`, anything matching `*.wasm`). The watcher only emits
  events for explicitly-added directories; recursive watching is the
  caller's responsibility.
- Single goroutine reads from `watcher.Events`. On any event matching
  a non-ignored path, write `time.Now()` to a shared `lastEvent` and
  reset a 250ms timer.
- When the timer fires, run `rebuildOnce(dir)`. On success: print
  `[dev] reloaded <name>@<sha-prefix>` to stderr. On failure: print
  the build error and continue (don't crash the watcher).
- `defer cleanup(dir)` removes the dev install dir + marker on exit.

### Component 3 — `rebuildOnce`

```go
// rebuildOnce runs <dir>/build.sh, re-signs the manifest with the dev
// seed, and re-installs (--force) under the 0.0.0-dev sentinel
// version. The signature flow reuses pluginSignCmd + pluginInstallCmd
// to keep one source of truth for the install pipeline.
//
// Returns the wasm sha-prefix on success for the operator-visible log.
func rebuildOnce(dir string, stderr io.Writer) (shaPrefix string, err error)
```

Flow:
1. Exec `./build.sh` with `dir` as CWD. Stderr/stdout streamed to the
   passed writers. Exit non-zero → return error.
2. Re-sign the manifest with `<dir>/.stado/dev.seed` (existing
   `pluginSignCmd.RunE`, with manifest-version overridden to
   `0.0.0-dev` — see Component 4).
3. Re-install with `--force` (existing `pluginInstallCmd.RunE`).
4. Read the wasm to compute its sha-prefix for logging.

### Component 4 — `0.0.0-dev` sentinel handling

`internal/plugins/devmode.go` (NEW, ~80 lines):

```go
package plugins

// DevSentinelVersion is the version string used for plugins
// installed via `plugin dev --watch`. Lives at
// <state>/plugins/<name>-0.0.0-dev/ and is pinned via the active-
// marker mechanism. Watch-exit cleanup removes both.
const DevSentinelVersion = "0.0.0-dev"

// PinActiveDev writes the active-version marker for `name` pointing
// at DevSentinelVersion. Called once on first watch-loop start
// (before the first rebuild) so subsequent registrations through
// runtime.registerInstalledPluginTools pick up the dev install.
func PinActiveDev(stateDir, name string) error

// CleanupDev removes the dev install dir + active marker. Idempotent.
// Called on watch-loop exit.
func CleanupDev(stateDir, name string) error
```

These helpers don't add new logic — they wrap `os.WriteFile` /
`os.RemoveAll` with the right paths so the watch loop and any
follow-up tooling have a single API.

`internal/runtime/installed_tools.go` already accepts arbitrary
version strings (the no-v form passes `splitInstalledID` and
`semverize` correctly). `0.0.0-dev` semver-parses; the marker
mechanism handles the pin. **No runtime/installed_tools changes
needed.**

### Component 5 — Manifest version override

The existing `plugin sign` command reads the version from the
manifest template. For dev mode, we want the *installed* manifest's
version to be `0.0.0-dev` regardless of what the operator wrote in
their template (so every rebuild produces the same install dir,
overwriting cleanly with `--force`).

Two options:
- **A.** Add a `--manifest-version` flag to `pluginSignCmd` that
  overrides the version field at sign time.
- **B.** Mutate the manifest in-memory in `rebuildOnce` before
  invoking sign, then restore.

**Pick: A.** Cleaner; no global-state mutation; reusable for other
scripted workflows. Wire only `plugin dev --watch` to set it; the
default behavior of `plugin sign` is unchanged.

### Component 6 — Build script contract

`<dir>/build.sh` is treated as opaque:
- Required to exist; absent → error before the watcher starts.
- Must produce `<dir>/plugin.wasm` as its output.
- CWD is `<dir>`. Environment passed through.
- Stdout/stderr streamed to the operator's terminal (helpful for
  build progress).

No timeout; long builds just block the next debounce window.
Operators kill the watch with Ctrl+C if they want to stop a runaway.

### Component 7 — Cleanup on exit

`cmd/stado/plugin_dev_watch.go`:

```go
func cleanup(dir string) {
    cfg, err := config.Load()
    if err != nil { return }
    name := readPluginName(dir)  // from manifest template
    _ = plugins.CleanupDev(cfg.StateDir(), name)
}
```

Triggered by:
- `defer` from `runDevWatchLoop` — covers normal Ctrl+C through
  cobra's signal handling.
- A second-level `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)`
  inside the loop ensures cleanup runs even on race conditions.

Stale state from process kill (`-9`) is acceptable: re-running
`stado plugin dev <dir> --watch` overwrites the install dir on the
first rebuild, and the marker file is harmless in isolation.

## File map

| Action | Path | Net lines |
|---|---|---|
| Create | `cmd/stado/plugin_dev_watch.go` | ~250 |
| Create | `cmd/stado/plugin_dev_watch_test.go` | ~150 |
| Create | `internal/plugins/devmode.go` | ~80 |
| Create | `internal/plugins/devmode_test.go` | ~80 |
| Modify | `cmd/stado/plugin_use_dev.go` (--watch flag wiring) | ~20 |
| Modify | `cmd/stado/plugin_sign.go` (--manifest-version flag) | ~15 |
| Modify | `go.mod` / `go.sum` (fsnotify dep) | ~3 |

Total: ~600 net (memory estimated 300-500; the test split is the
delta), 2 modified + 4 new files.

## Testing strategy

### Unit — `internal/plugins/devmode.go`

- `TestPinActiveDev`: writes marker, asserts file contents.
- `TestCleanupDev_RemovesDirAndMarker`: install fixture, call
  cleanup, assert both gone.
- `TestCleanupDev_Idempotent`: call cleanup twice, no error.

### Integration — `cmd/stado/plugin_dev_watch.go`

- **Watch loop debounce:** mock fsnotify channel; emit 5 events in
  100ms; assert `rebuildOnce` is called exactly once after 250ms.
- **Build failure resilience:** rebuildOnce returns error; assert
  the watcher continues, error is logged, no panic.
- **Cleanup on context cancel:** start watch with a short-lived
  context; assert dev install dir is removed after cancel.
- **First-run + watch composition:** end-to-end test that runs
  pluginDevCmd with --watch, simulates a file change via direct
  channel send, asserts the install dir's wasm sha changes.

### Manual smoke

1. `cd ~/projects/myplugin && stado plugin dev . --watch`
2. Edit a Go source file, save.
3. Observe: build runs, `[dev] reloaded myplugin@<sha>` printed.
4. In another terminal: `stado tool run myplugin.lookup '{...}'`
   succeeds with the new wasm.
5. Ctrl+C the watch.
6. `stado tool run myplugin.lookup` fails — installed plugin gone.
7. `ls ~/.local/share/stado/plugins/` no longer has `myplugin-0.0.0-dev`.

## Risks + mitigations

- **Risk:** fsnotify on Linux is recursive-by-default-NO; we walk
  and add per-directory. New subdirs created during a session are
  not watched.
  - *Mitigation:* on each event, if it's a `Create` for a new dir,
    add it to the watcher. Standard fsnotify pattern.

- **Risk:** Build script outputs a partial wasm during compile; the
  watcher fires on each write, attempting to install a corrupt file.
  - *Mitigation:* the 250ms debounce coalesces events. Build scripts
    typically produce the wasm at the end of compile in one write.
    For multi-step builds, operators can use `mv` to atomic-rename.

- **Risk:** `plugin install --force` on the same dir while a tool
  invocation from the *previous* version is still running.
  - *Mitigation:* the wasm runtime instantiates per-invocation, with
    each instance holding its own copy of the bytes. New invocations
    pick up new bytes; in-flight ones complete on the old. No race.

- **Risk:** Operator's tool run hits the dev install before sign+trust
  finishes (timing edge during first rebuild).
  - *Mitigation:* `runDevWatchLoop` only starts the watcher after
    the synchronous first-run pipeline completes successfully. A
    parallel `tool run` during this window sees the *previous*
    install (or nothing) — not a corrupt state.

- **Risk:** Long-running TUI session doesn't see the new wasm.
  - *Mitigation:* documented limitation. Operator uses CLI
    (`stado tool run`) for the iteration loop; TUI is for sessions
    against stable plugins. Follow-up cycle adds TUI mtime polling.

## Out of scope

- TUI live-reload (Q8 — explicit follow-up).
- Multi-plugin watch in one invocation (Q7).
- Auto-detection of make / tinygo / cargo build (Q3 — `build.sh` only).
- Configurable debounce window (Q2 — 250ms hardcoded).
- Hot-reload of in-flight tool invocations (sandbox runs are
  process-instances; new bytes apply to next call).
- Persistence-free in-memory wasm (we use the `<state>/plugins/`
  filesystem path; cleanup at watch-exit gives "persistence-free
  in spirit").

## Verification plan

After implementation:

1. `go test ./... -count=1` passes.
2. `go vet ./...` clean.
3. Manual smoke per the steps in "Testing strategy."
4. Rebuild loop timing: edit-save-edit-save-edit-save in <500ms,
   observe exactly one rebuild fires (debounce works).
5. Crash test: kill stado mid-build (`-9`); restart watch; observe
   the new install overwrites cleanly.

## Handoff (2026-05-06)

- **What shipped:** `stado plugin dev <dir> --watch` watches the
  plugin source dir, debounces saves at 250ms, runs `<dir>/build.sh`,
  re-signs the manifest with `--manifest-version 0.0.0-dev`, and
  re-installs with `--force`. Cleanup on Ctrl+C removes the
  `<state>/plugins/<name>-0.0.0-dev/` install + active marker.
  Six commits on `feat/plugin-dev-watch-mode`:
  - `chore(deps): add fsnotify`
  - `feat(plugins): DevSentinelVersion + Pin/CleanupDev helpers`
  - `feat(cli): plugin sign --manifest-version override`
  - `feat(cli): runDevWatchLoop + rebuildOnce + debounceLoop`
  - `feat(cli): plugin dev --watch flag`
  - `test(cli): integration test for plugin dev --watch cleanup`
- **Tests:** 4 unit tests in `internal/plugins/devmode_test.go`
  (sentinel + pin + cleanup + idempotence) and 4 in
  `cmd/stado/plugin_dev_watch_test.go` (debounce coalesce, error-
  resilience, context-cancel exit, cleanup-on-cancel integration).
  All green.
- **What's left:** TUI live-reload (Q8 — long-running TUI sessions
  don't auto-reload; iteration loop runs through `stado tool run`).
  Filed as a follow-up cycle item.
- **Manual smoke:** Pending user verification against an active
  plugin source dir. The bundled hello-demo example would need a
  `build.sh` tweak (its build.sh signs with `hello-demo.seed`,
  but the watch loop expects to drive signing itself via
  `.stado/dev.seed` — operators using the watch flow should make
  their build.sh produce only the wasm, not sign).
- **What to watch:** dev install left over after `kill -9` of the
  watch process — re-run of `plugin dev --watch` overwrites cleanly,
  but operators may see stale `<name>-0.0.0-dev` dirs if the watch
  ever crashes outside the deferred cleanup path.
