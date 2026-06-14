## v0.69.0 — EP-audit follow-ups: plugin categories, fleet cleanup, build determinism — 2026-06-14

Continues the EP-vs-codebase audit work from v0.68.0. Installed plugins' tool
categories now work at runtime, a background-agent resource leak on exit is
closed, and the bundled-wasm build is determinism-gated in CI.

### TUI / Plugins

- **Installed-plugin tool categories work at runtime.** Plugin manifests declare
  per-tool `categories` / `extra_categories`, validated against the canonical
  taxonomy at install — but nothing read them back, so `tools.categories`,
  `tools.in_category`, and the categories field of `tools.search` treated every
  installed tool as category-less. They now surface the manifest's categories,
  and `tools.search` matches on them. Canonical categories stay separate from
  free-form `extra_categories`: the catalog (`tools.categories`) and
  `[tools].autoload_categories` only see canonical entries, while extras are
  shown distinctly in `tools.describe` and accepted for exact `tools.in_category`
  lookups. (EP-0037)

### Fixes

- **Background fleet agents no longer leak past TUI exit.** `/spawn` agents ran
  on goroutines parented off a non-cancellable root context, and the fleet was
  never cancelled on quit — so exiting the UI left subagent goroutines (and the
  provider calls / child processes they drove) running orphaned. The TUI now
  cancels the fleet on every exit path. (EP-0034)

### Infra

- **Bundled-wasm rebuild determinism is gated in CI.** A new `wasm-determinism`
  job builds the `go:embed`'d wasm twice from a cold cache and fails on any sha
  mismatch, so a non-reproducible build can't silently ship inside a signed
  binary. (EP-0042)
- **Removed dead `exec:bash` / `exec:search` / `exec:ast_grep` plugin-host
  fields and their unreachable refuse-no-runner guards** — those capabilities
  were dropped in the no-internal-tools migration, so the fields could never be
  set. (EP-0028)

### Docs

- Corrected EP status/example drift surfaced by the audit: EP-0030's stale
  "Placeholder" body banner (the harness shipped in v0.33.0), EP-0002's
  `[tools.overrides]` example using removed bare tool names, and a note on
  EP-0039 that automatic anchor trust-on-first-use at install time is not yet
  wired (the manual `plugin trust` command and override-fingerprint verification
  are).

