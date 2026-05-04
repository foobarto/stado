# `stado plugin`

Author, trust, verify, install, and run signed WASM plugins.

The plugin surface is split into two halves:

1. **Authoring / publishing** — `init`, `gen-key`, `sign`, `digest`
2. **Consumption / operations** — `trust`, `untrust`, `list`,
   `installed`, `verify`, `install`, `run`

## What it does

Stado plugins are WASM modules with a signed JSON manifest. Before a
plugin can run, stado verifies:

- the manifest signature
- the `plugin.wasm` sha256 recorded in the manifest
- rollback protection (`name` + `version` monotonicity per signer)
- optional CRL state (`[plugins].crl_url`)
- optional Rekor transparency-log inclusion (`[plugins].rekor_url`)

Once verified, `stado plugin install` copies the plugin directory into
`$XDG_DATA_HOME/stado/plugins/<name>-<version>/`. `stado plugin run`
instantiates the module in the wazero runtime and invokes one declared
tool. Add `--session <id>` to bind the run to a persisted session so
session-aware capabilities work on the CLI too.

The repo also ships a product-facing plugin catalog under
[`plugins/`](../../plugins/). The bundled default plugin source is
[`plugins/default/auto-compact/`](../../plugins/default/auto-compact/):
stado loads that one automatically as a background plugin in the TUI
and headless server, and you can also build/install it manually if you
want an explicit `plugin run --session` flow.

## Why it exists

Three separate jobs need one CLI surface:

1. **Trust management.** Users need an explicit signer pinning step
   instead of "download random code and hope". `plugin trust` makes
   that trust decision visible and reviewable.
2. **Offline authoring.** Plugin maintainers need scaffold + signing
   commands that work without external packaging infrastructure.
3. **Runtime isolation.** Plugins are capability-bound and execute
   inside the same runtime whether they are third-party additions or
   overrides for bundled tools.

## Common flow

### Scaffold a new plugin

```sh
stado plugin init my-plugin
cd my-plugin
```

Creates a Go `wasip1` starter with `main.go`, `build.sh`,
`plugin.manifest.template.json`, and a short README.

### Generate a signing key

```sh
stado plugin gen-key my-plugin.seed
```

Writes a 32-byte Ed25519 seed and prints the public key + fingerprint.
Keep the `.seed` file offline.

### Build and sign

```sh
./build.sh
# or manually:
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
stado plugin sign plugin.manifest.json --key my-plugin.seed --wasm plugin.wasm
```

`plugin sign` rewrites the manifest with `wasm_sha256` and
`author_pubkey_fpr`, then writes `plugin.manifest.sig`.

### Trust and verify

```sh
stado plugin trust <pubkey-hex> "Alice Example"
stado plugin verify .
```

`plugin verify` checks signature, digest, rollback state, optional CRL,
and optional Rekor lookup.

### Install and run

```sh
stado plugin install .
stado plugin installed
stado plugin run my-plugin-0.1.0 greet '{"name":"Ada"}'
stado plugin run --session abc123 auto-compact-0.1.0 compact '{"threshold_tokens":5000}'
```

Installed plugin IDs match the directory names under the state dir, so
`plugin installed` prints exactly what `plugin run` expects.

## Command reference

| Command | Purpose |
|---------|---------|
| `stado plugin init <name>` | Scaffold a Go `wasip1` plugin project |
| `stado plugin gen-key <path>` | Generate a new Ed25519 seed for signing |
| `stado plugin sign <manifest.json> --key <seed>` | Fill manifest digest/fingerprint fields and sign |
| `stado plugin digest <file>` | Print a WASM blob's sha256 |
| `stado plugin trust <pubkey> [author]` | Pin a signer pubkey |
| `stado plugin untrust <fingerprint>` | Remove a signer pin |
| `stado plugin list` | Show trusted signer entries |
| `stado plugin installed` | Show installed plugin IDs |
| `stado plugin verify <dir>` | Verify a plugin directory in place |
| `stado plugin install <dir>` | Verify, then copy into the state dir |
| `stado plugin run [--session <id>] [--workdir <path>] [--with-tool-host] <plugin-id> <tool> [json-args]` | Invoke one tool from one installed plugin, optionally against a persisted session |
| `stado plugin gc [--keep N] [--apply]` | Sweep older installed plugin versions per (signer, name) group (dry-run by default) |
| `stado plugin doctor <plugin-id>` | Inspect manifest + emit per-surface compatibility table with the exact flags to pass |
| `stado plugin info <plugin-id>` | Dump installed plugin's manifest as pretty JSON (sibling to doctor — info dumps, doctor analyses) |

## Using plugins from the TUI

- Installed plugins can be invoked directly with `/plugin:<id> <tool> {…}`.
- `[tools].overrides` can replace bundled tools with installed plugins.
  Example: `overrides = { bash = "approval-bash-go-0.1.0" }`
- The bundled `auto-compact` background plugin is loaded by default.
- `[plugins].background` loads extra installed plugins that tick
  alongside that default.

`stado plugin run --session <id>` binds the plugin to the target
session's persisted conversation and worktree, so `session:read`,
`session:fork`, and `llm:invoke` work on the CLI path too. Plugins that
declare `memory:propose`, `memory:read`, or `memory:write` are wired to
the local append-only memory store under the stado state directory; use
`stado memory list|show|edit|approve|supersede|reject|delete|export`
to review that store. Approved memory only enters model prompts after
enabling `[memory].enabled = true`; candidate memories remain
review-only. `stado learning propose` stores stricter EP-16 lesson
candidates in the same append-only store for explicit review.
Without `--session`, the command stays a one-shot no-session path and
session-aware capabilities see zeroed fields.

## Config

Relevant `config.toml` sections:

- `[plugins].crl_url` — signed revocation list URL
- `[plugins].crl_issuer_pubkey` — Ed25519 key used to verify the CRL
- `[plugins].rekor_url` — Rekor transparency-log endpoint
- `[plugins].background` — extra installed plugin IDs loaded
  persistently in the TUI/headless server
- `[memory].enabled` — opt in to injecting approved plugin memories as
  bounded untrusted prompt context
- `[tools].overrides` — map bundled tool names to installed plugin IDs

`stado config show` prints the resolved values.

## Gotchas

- **`plugin list` is not `plugin installed`.** `list` shows trusted
  signers; `installed` shows runnable plugin IDs.
- **Trust is explicit unless you pass `--signer` to install.** The
  TOFU shortcut exists for controlled environments, but it should still
  be backed by out-of-band signer verification.
- **Rollback protection is intentional.** Reinstalling an older version
  under the same signer is rejected.
- **Plugin packages must be plain files.** Symlinks and special files in
  the plugin directory are rejected at install time.
- **`plugin run` without `--session` is not a live session.** If a
  plugin needs `session:*` or `llm:invoke`, either pass `--session <id>`
  or run it from the TUI/headless surfaces.
- **`plugin run` without `--with-tool-host` can't invoke bundled tool
  imports.** Plugins that import `stado_http_get`, `stado_fs_tool_*`,
  `stado_lsp_*`, or `stado_search_*` see a "plugin host has no tool
  runtime context" error unless `--with-tool-host` is passed. The
  flag is opt-in because it widens the host import surface; it
  refuses plugins that declare `exec:bash` (no `sandbox.Runner` is
  available — use `stado run` for those). EP-0028.
- **`plugin run --workdir` defaults to the plugin's install dir, not
  the operator's CWD.** Plugins that scope `fs:read:.` to project
  files (htb-cve-lookup-style lookups against the operator's repo)
  need `--workdir=$PWD` to resolve relative paths against the
  operator's environment instead of `<state-dir>/plugins/<id>/`.
  EP-0027.
- **`plugin gc` is dry-run by default.** Pass `--apply` to actually
  delete. `--keep` (default 1) controls how many newest versions to
  preserve per (signer, name) group. Trust-store entries and
  rollback pins are not touched, so a freshly-deleted older version
  still cannot be reinstalled by accident.
- **When in doubt, run `plugin doctor <id>`.** It parses the
  plugin's manifest and prints which surfaces / flags it needs.
  Faster than reading the source for "why does my plugin fail with
  `stado_http_get returned -1`?" and friends.

## See also

- [docs/features/plugin-authoring.md](../features/plugin-authoring.md) — end-to-end walkthrough for first-time plugin authors
- [README.md](../../README.md) — install channels and high-level plugin summary
- [SECURITY.md](../../SECURITY.md) — plugin-publish cookbook and trust model
- [plugins/README.md](../../plugins/README.md) — bundled/default vs example plugin catalog
- [plugins/examples/README.md](../../plugins/examples/README.md) — concrete opt-in plugin examples
- [memory.md](memory.md) — review plugin-proposed persistent memories
- [learning.md](learning.md) — propose reviewable operational lessons
