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
| `stado plugin run [--session <id>] <plugin-id> <tool> [json-args]` | Invoke one tool from one installed plugin, optionally against a persisted session |

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
`stado memory list|show|approve|reject|delete|export` to review that
store. Without `--session`, the command stays a one-shot no-session path
and session-aware capabilities see zeroed fields.

## Config

Relevant `config.toml` sections:

- `[plugins].crl_url` — signed revocation list URL
- `[plugins].crl_issuer_pubkey` — Ed25519 key used to verify the CRL
- `[plugins].rekor_url` — Rekor transparency-log endpoint
- `[plugins].background` — extra installed plugin IDs loaded
  persistently in the TUI/headless server
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

## See also

- [README.md](../../README.md) — install channels and high-level plugin summary
- [SECURITY.md](../../SECURITY.md) — plugin-publish cookbook and trust model
- [plugins/README.md](../../plugins/README.md) — bundled/default vs example plugin catalog
- [plugins/examples/README.md](../../plugins/examples/README.md) — concrete opt-in plugin examples
- [memory.md](memory.md) — review plugin-proposed persistent memories
