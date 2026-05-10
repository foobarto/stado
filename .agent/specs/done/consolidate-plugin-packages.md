# Consolidate plugin packages under internal/plugins/

## What ships

The plugin-related package tree gets one umbrella with origin-flavored
subpackages instead of three sibling packages whose relationship has
to be discovered by reading source.

Before:

```
internal/plugins/          install/trust pipeline (manifest, identity, rekor, crl, …)
internal/plugins/runtime/  wasm host machinery (Module, Host, BackgroundPlugin)
internal/bundledplugins/   embedded asset store + inventory for in-binary wasm
internal/userbundled/      user wasm appended to the binary at bundle time
```

After:

```
internal/plugins/             install/trust pipeline (unchanged)
internal/plugins/runtime/     wasm host machinery (unchanged)
internal/plugins/bundled/     embedded asset store + inventory  (was internal/bundledplugins)
internal/plugins/userbundled/ user wasm                          (was internal/userbundled)
```

Plus: `auto_compact.go`'s host-side runtime policy lifts out of the
bundled package and into `internal/runtime/background_defaults.go`,
so the bundled package owns *what wasm ships in the binary* and
nothing else. Package doc comments added for all four plugin
packages so the boundary survives future contributors.

## Acceptance criteria

- `internal/bundledplugins/` no longer exists; `internal/plugins/bundled/`
  exists with `package bundled` (was `package bundledplugins`).
- `internal/userbundled/` no longer exists; `internal/plugins/userbundled/`
  exists with `package userbundled` (unchanged name).
- `internal/runtime/background_defaults.go` owns
  `DefaultBackgroundPlugins`, `LookupBackgroundPlugin`,
  `BundledBackgroundPlugin`, plus the auto-compact manifest helpers
  that came with them.
- `internal/plugins/bundled/auto_compact.go` retains only the
  `init()` `RegisterModule` call — the bundled package's claim that
  `auto-compact` is part of the inventory. (The Go-coded manifest
  duplication with `plugins/bundled/auto-compact/plugin.manifest.template.json`
  is **not** addressed here — see follow-up below.)
- All call sites updated:
  - 28 files importing `internal/bundledplugins` (5 in `cmd/stado/`,
    1 in `internal/headless/`, 2 in `internal/plugins/runtime/`,
    3 in `internal/runtime/`, 2 in `internal/tui/`,
    2 in `internal/userbundled/`, 13 in `plugins/bundled/*/main.go`).
  - 1 file importing `internal/userbundled` (`cmd/stado/main.go`).
  - 4 call sites for the auto-compact host-side glue
    (`internal/tui/model_plugins.go` ×3,
    `internal/headless/plugins.go` ×2 — re-counted during execution).
- `plugins/bundled/build.sh` updated: `WASM_OUT` path and the
  comment-block reference both point at the new location.
- All four plugin packages carry a doc comment defining what
  belongs in them and what doesn't.
- `make build` passes; `make test` passes;
  `plugins/bundled/build.sh` rebuilds wasm artifacts;
  `./stado --help` runs without panic.
- One commit per phase (A renames bundled, B renames userbundled,
  C extracts host-side glue, D adds package docs).

## Non-goals

- **Killing the `autoCompactManifest()` Go duplication.** Doing it
  cleanly needs either `go:generate` plumbing or a build-step that
  copies `plugin.manifest.template.json` into the bundled package's
  embed tree alongside the wasm, plus a new `bundled.Manifest(name)`
  accessor. Both are real design calls; doing them inside a rename
  PR muddies the diff. Tracked as follow-up
  `kill-autocompact-manifest-duplication`.
- **Adding a `background: true` manifest schema field.** Premature
  until a second background plugin actually shows up
  (`internal/plugins/runtime/background.go` mentions
  telemetry-bridge / session-recorder as plausible future ones —
  none today).
- **Renaming `internal/plugins` itself.** Already correct as the
  umbrella once the subpackages move in.
- **Reorganizing `internal/plugins/runtime/`.** Already well-placed.
- **Touching `plugins/bundled/<plugin>/` source code beyond import
  paths.** The 13 wasm plugin sources need their SDK import path
  updated; nothing else.
- **Changing the relationship between `internal/runtime/` and
  `internal/plugins/runtime/`.** Both keep their current scope —
  `internal/runtime/` is agent-loop runtime, `internal/plugins/runtime/`
  is wasm-host plumbing. Naming clash documented in package docs.

## Design sketch

### Phase A — rename bundledplugins → plugins/bundled

1. `git mv internal/bundledplugins internal/plugins/bundled`
2. In every moved Go file: `package bundledplugins` → `package bundled`.
   (Test files in `*_test.go` may use `package bundledplugins_test`
   — those become `package bundled_test`.)
3. Update import paths across the repo:
   - `github.com/foobarto/stado/internal/bundledplugins` →
     `github.com/foobarto/stado/internal/plugins/bundled`
   - `github.com/foobarto/stado/internal/bundledplugins/sdk` →
     `github.com/foobarto/stado/internal/plugins/bundled/sdk`
4. Update identifier references at all 28 call sites:
   `bundledplugins.X` → `bundled.X`. Importer aliases handled by
   `goimports`.
5. Update `plugins/bundled/build.sh`:
   - `WASM_OUT="$REPO_ROOT/internal/bundledplugins/wasm"` →
     `WASM_OUT="$REPO_ROOT/internal/plugins/bundled/wasm"`
   - Header comment line referencing
     `internal/bundledplugins/wasm/` updated to match.
6. `make build` then `make test`. If both green:
7. Commit: `refactor(plugins): move internal/bundledplugins → internal/plugins/bundled`.

### Phase B — rename userbundled → plugins/userbundled

1. `git mv internal/userbundled internal/plugins/userbundled`
2. Package name stays `userbundled` (matches new directory).
3. Update one importer: `cmd/stado/main.go` —
   `internal/userbundled` → `internal/plugins/userbundled`.
4. `userbundled/init.go`'s import of the bundled package is already
   on the new path (Phase A landed first).
5. `make build && make test`. If green:
6. Commit: `refactor(plugins): move internal/userbundled → internal/plugins/userbundled`.

### Phase C — lift auto-compact host-side policy

1. Create `internal/runtime/background_defaults.go` containing
   what's currently in `internal/plugins/bundled/auto_compact.go`
   *except* the `init()` `RegisterModule` call. That's:
   - `autoCompactID` const
   - `BundledBackgroundPlugin` struct
   - `DefaultBackgroundPlugins()`
   - `LookupBackgroundPlugin(id)`
   - `isAutoCompactID(id)`
   - `autoCompactManifest()` (still duplicated — moves as-is, dedup is follow-up)
   - `autoCompactSchema()`
2. The new file imports `internal/plugins/bundled` for `bundled.MustWasm`
   and the `Author` constant.
3. `internal/plugins/bundled/auto_compact.go` shrinks to:

   ```go
   package bundled

   func init() {
       RegisterModule("auto-compact", "compact",
           []string{"session:observe", "session:read", "session:fork", "llm:invoke:30000"})
   }
   ```

   File kept (not deleted) so the inventory claim has a clear home.
4. Update callers:
   - `internal/tui/model_plugins.go`: three references
     (`bundled.DefaultBackgroundPlugins`, `bundled.LookupBackgroundPlugin`)
     → `runtime.DefaultBackgroundPlugins`, `runtime.LookupBackgroundPlugin`
     (importing `internal/runtime`).
   - `internal/headless/plugins.go`: two references, same swap.
5. `make build && make test`. If green:
6. Commit: `refactor(runtime): lift auto-compact host-side policy out of plugins/bundled`.

### Phase D — package docs

Four `doc.go` files (or extended comments on an existing file in the
package) that name what each package owns and what doesn't:

1. `internal/plugins/doc.go` — umbrella. One paragraph each on the
   four subpackages and the install/trust files at the top level.
2. `internal/plugins/bundled/doc.go` — assets + inventory only.
   *Runtime policy goes in `internal/runtime/`, not here.*
3. `internal/plugins/userbundled/doc.go` — operator-supplied wasm
   appended to the binary via `stado plugin bundle`.
4. `internal/plugins/runtime/doc.go` — wasm host machinery,
   origin-agnostic. Note the naming relationship with
   `internal/runtime/` (different layer; see umbrella doc).

`make build`. Commit: `docs(plugins): document package boundaries for plugins umbrella`.

## Risk and self-critique

**Why this might be wrong.**

- Phase C moves the auto-compact-specific Go manifest into the
  agent-loop runtime package. That's still a per-plugin policy
  hard-coded in Go — the smell isn't gone, just relocated. Counter:
  the smell now lives in the package whose job is host-side
  bootstrap policy. The cleaner fix is the manifest-flag refactor,
  which is intentionally deferred (see Non-goals). Marking the
  duplication explicitly in `background_defaults.go` with a
  `// TODO: dedupe with plugins/bundled/auto-compact/plugin.manifest.template.json`
  pointer is more honest than papering over it.
- The naming clash `internal/runtime/` vs `internal/plugins/runtime/`
  is a real cost. Counter: they're at different layers
  (agent-loop vs wasm-host). The package docs name the distinction.
  Renaming one of them is bigger churn than the consolidation
  itself, for marginal gain.
- Phase A is a 28-file sweep. A single bad regex during identifier
  rewrite could land subtle bugs (e.g. the substring
  `bundledplugins` appearing in comments or strings being rewritten
  the same way as code references). Mitigate: after each phase,
  `grep -rn "bundledplugins\|internal/bundledplugins"` in the tree
  and verify only intentional references (spec/journal historical
  references) remain.
- The 13 `plugins/bundled/<name>/main.go` files use `//go:build wasip1`,
  so `go vet ./...` from the main module won't typecheck them.
  Mitigate: run `plugins/bundled/build.sh` after Phase A to confirm
  the wasm rebuild still works with the new SDK import path.
- `auto-compact` has its own `go.mod`
  (`module github.com/foobarto/stado/plugins/default/auto-compact`) —
  separate Go module. Its `main.go` does *not* import the SDK
  (confirmed via grep). So Phase A doesn't have to touch it. Other
  12 plugins are part of the main module and do import the SDK.

**Assumptions checked before starting.**

- `gopls rename` (or the IDE equivalent) handles identifier rewrites
  reliably across the 28 files. Backup plan: hand-driven sed +
  goimports if gopls misbehaves.
- The auto-compact `init() RegisterModule` call's effect (eager
  inventory registration at package load) is preserved when the
  file shrinks — the function body stays semantically identical,
  just shorter file.
- No external code (third-party plugins, downstream tooling) imports
  the moving paths. Internal-only imports — verified by the import
  prefix `github.com/foobarto/stado/internal/...`.
- The current commit messages follow conventional-commits style
  (`type(scope): description`) — confirmed against recent log.

## Done definition

- `make build` exits 0.
- `make test` exits 0.
- `bash plugins/bundled/build.sh` exits 0 and produces wasm
  artifacts in the new path
  (`internal/plugins/bundled/wasm/*.wasm`).
- `./stado --help` exits 0.
- `grep -rn "internal/bundledplugins" .` returns hits only in
  spec / journal / historical-reference files, not in source.
- `grep -rn "internal/userbundled" .` returns 0 hits in source.
- All four plugin packages have package-level doc comments
  describing their boundary.
- Four commits on `main`, one per phase, conventional-commits format.
- Spec moved to `.agent/specs/done/consolidate-plugin-packages.md`
  with handoff section appended.
- `STATE.md` updated to reflect the new state.
- Follow-up spec stub
  `.agent/specs/open/kill-autocompact-manifest-duplication.md`
  created so the deferred dedup work isn't lost.

---

## Handoff (2026-05-10)

### What shipped

Four commits on `main`, in order:

- **`7f697db`** — Phase A: `internal/bundledplugins` → `internal/plugins/bundled`. Package renamed to `bundled`. 28 importers updated (5 in `cmd/stado/`, 1 in `headless/`, 2 in `plugins/runtime/` tests, 3 in `runtime/`, 2 in `tui/`, 2 in `userbundled/` (now `plugins/userbundled/`), 13 wasm sources at `plugins/bundled/<name>/main.go`). `plugins/bundled/build.sh` `WASM_OUT` repointed.
- **`eaf0d9c`** — Phase B: `internal/userbundled` → `internal/plugins/userbundled`. Package name unchanged. One external importer (`cmd/stado/main.go`).
- **`126163f`** — Phase C: lifted `DefaultBackgroundPlugins`, `LookupBackgroundPlugin`, `BundledBackgroundPlugin`, `autoCompactManifest`, `autoCompactSchema`, `isAutoCompactID`, `autoCompactID` from `plugins/bundled/auto_compact.go` into `internal/runtime/background_defaults.go`. The bundled-package `auto_compact.go` is now 11 lines containing only the `init() RegisterModule(...)` inventory call. 6 call sites updated across `internal/tui/model_plugins.go` (3) and `internal/headless/plugins.go` (3).
- **`6cdc057`** — Phase D: package docs added. New `internal/plugins/doc.go` (umbrella) and `internal/plugins/bundled/doc.go`. Existing `manifest.go`, `userbundled/init.go`, `runtime/runtime.go` doc comments extended with boundary statements.

Final tree under `internal/plugins/`:

```
plugins/
  doc.go                      # umbrella doc — install/trust pipeline + subpackage map
  manifest.go, identity.go,
  anchor.go, lock.go, trust.go,
  rekor*, crl*, …             # install/trust pipeline (unchanged code)
  bundled/                    # was internal/bundledplugins
    doc.go                    # boundary doc — assets + inventory only
    embed.go, list.go,
    auto_compact.go (11 lines),
    sdk/, wasm/
  userbundled/                # was internal/userbundled
    init.go, init_test.go     # extended doc; code unchanged
  runtime/                    # unchanged location
    runtime.go (extended doc),
    background.go, host_*.go, …
```

Binary built and installed with `./stado install --force` (per operator standing rule).

### What's left

- **`autoCompactManifest()` Go duplication** — explicit non-goal, deferred. Follow-up spec at `.agent/specs/open/kill-autocompact-manifest-duplication.md` lays out three options (JSON copy + bundled.Manifest accessor / go:generate / manifest-flag refactor) with a recommendation to do Option A interim and Option C when a second background plugin appears.
- **Naming clash `internal/runtime/` vs `internal/plugins/runtime/`** — documented in the umbrella doc, not renamed. Different layers (agent-loop runtime vs wasm-host plumbing). Renaming is bigger churn for marginal gain.
- **Pre-existing `slices.Contains` style nits** in `bundled/list.go:121` and `bundled/list_test.go:131` — predate this PR (since `4a2cfdf`). Out of scope.

### What surprised

- `internal/headless/plugins.go` had **3** auto-compact references (358, 362, 380), not 2 as the spec said. Same swap, just one more line. Spec "Acceptance criteria" was off by one.
- `internal/plugins/bundled/list_test.go` referenced the moved `autoCompactID` constant. The Phase C subagent inlined the literal `"auto-compact"` rather than re-declaring the constant in the bundled package — the test asserts the registered name; literal is more honest.
- Phase B's subagent had to update two doc-comment references in `bundled/list.go` to the old `internal/userbundled` path (Phase A leftovers). Flagged transparently.
- Stale IDE/LSP diagnostics during execution claimed unresolved `bundledplugins` references that didn't exist on disk. Verified by grep — disk state was clean throughout. Operator's editor needs to reopen the moved files to pick up new paths.

### What to watch

- **Anyone re-adding host-side policy to `internal/plugins/bundled/`.** The package doc at `bundled/doc.go` names the boundary explicitly; reviewers can point at it. The drift-risk shape is "future contributor adds a per-plugin lifecycle adapter to the bundled package because that's where the existing one lived." Catch it in review.
- **`autoCompactManifest()` drifting from the JSON template.** Two source-of-truth locations exist until the follow-up spec lands. The TODO comment in `background_defaults.go` flags it but doesn't enforce it. Worth a glance during any auto-compact-related work.
- **Build script behavior.** `plugins/bundled/build.sh` rebuilds wasm during smoke verification — if the rebuild produces byte-different output (which it can, due to embedded build-time data), `git status` will flag it as dirty. Phase C's subagent caught this and used `git checkout` to restore the artifacts before committing. If a future verifier doesn't, the next commit picks up unrelated wasm bytes.
