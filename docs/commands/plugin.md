# `stado plugin`

Author, trust, verify, install, and run signed WASM plugins.

The plugin surface is split into two halves:

1. **Authoring / publishing** — `init`, `gen-key`, `sign`, `digest`
2. **Consumption / operations** — `trust`, `untrust`, `list`,
   `installed`, `verify`, `install`

There is no `stado plugin run`. An installed tool is invoked via
`stado tool run <name>` (see below).

## What it does

Stado plugins are WASM modules with a signed JSON manifest. Before a
plugin can run, stado verifies:

- the manifest signature
- the `plugin.wasm` sha256 recorded in the manifest
- rollback protection for the canonical source/version identity per signer
- optional CRL state (`[plugins].crl_url`)
- optional Rekor transparency-log inclusion (`[plugins].rekor_url`)

Once verified, `stado plugin install` copies the plugin directory into
`$XDG_DATA_HOME/stado/plugins/<name>-<version>/`; `--local` uses the current
project's `.stado/plugins/<name>-<version>/` instead. `stado tool run`
instantiates the owning module in the wazero runtime and invokes one
declared tool by name. Add `--session <id>` to bind the run to a
persisted session so session-aware capabilities work on the CLI too.

The stado repository keeps reproducible bundled plugin source under
[`plugins/bundled/`](../../plugins/bundled/). Official optional plugins and
cross-language examples live in
[foobarto/stado-plugins](https://github.com/foobarto/stado-plugins). The bundled default plugin source is
[`plugins/bundled/auto-compact/`](../../plugins/bundled/auto-compact/):
stado loads that one automatically as a background plugin in the TUI
and headless server, and you can also build/install it manually if you
want an explicit `tool run --session` flow.

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
stado tool run greet '{"name":"Ada"}'
stado tool run --session abc123 compact '{"threshold_tokens":5000}'
```

Installed plugin IDs match the directory names under their project or global
plugin root. `plugin installed` lists those IDs with `scope=project` or
`scope=global`; `stado tool run` then takes the tool name (not the plugin ID)
and resolves the owning plugin for you. These names are presentation/install
aliases. Runtime authority comes from the verified lock/source identity and
manifest digest; `Manifest.Name` is not an authority namespace (EP-0066).

## Command reference

| Command | Purpose |
|---------|---------|
| `stado plugin init <name>` | Scaffold a Go `wasip1` plugin project |
| `stado plugin dev <dir>` | Build, sign, trust, and install from a local directory in one shot (development workflow — bypasses the trust prompt for ad-hoc keys) |
| `stado plugin gen-key <path>` | Generate a new Ed25519 seed for signing |
| `stado plugin sign <manifest.json> --key <seed>` | Fill manifest digest/fingerprint fields and sign |
| `stado plugin digest <file>` | Print a WASM blob's sha256 |
| `stado plugin trust <pubkey> [author]` | Pin a signer pubkey |
| `stado plugin untrust <fingerprint>` | Remove a signer pin |
| `stado plugin untrust-anchor <host/owner>` | Clear a pinned owner anchor (e.g. after a key rotation) so the next remote install re-runs trust-on-first-use |
| `stado plugin list` | List trusted signers + installed plugins, with author and trust status |
| `stado plugin installed` | Show installed plugin IDs with project/global scope |
| `stado plugin verify <dir>` | Verify a plugin directory in place |
| `stado plugin verify-installed <plugin-id>` | Re-verify an installed plugin against the trust store (catch trust-store drift) |
| `stado plugin install <dir-or-identity>` | Verify, then copy into the plugin directory. Accepts a local directory OR a remote identity `host/owner/repo@version` (fetched + anchor-verified). Flags: `--local` (install under the current project's `.stado/plugins/`; trust remains user-local), `--force` (reinstall over the same version), `--autoload` (persist tools into `[tools].autoload` in the matching user/project config; project loading still needs the user-level trust gate), `--signer <pubkey>` (inline-pin the author key), `--trust-anchor` (accept the owner's anchor fingerprint on first sight without prompting; verify out of band) |
| `stado plugin update <plugin-id>` | Fetch the latest tagged version of an installed plugin (GitHub/GitLab release API) and install it side-by-side in the same project/global scope. `--check` lists available updates without installing |
| `stado plugin remove <name>` | Uninstall a plugin from project and global roots (all installed versions) and drop matching scoped lock entries so `update` won't reinstall it |
| `stado plugin use <name>@<version>` | Switch the active version in the scope where that plugin is installed; project and global markers are independent |
| `stado plugin reload <plugin-id>` | Re-read a plugin's tools and capabilities; effective inside a TUI session via `/plugin reload` |
| `stado tool run [--session <id>] [--workdir <path>] <tool> [json-args]` | Invoke one installed tool by name (the owning plugin is resolved automatically), optionally against a persisted session. The tool host is always attached. |
| `stado plugin bundle [--out <file>] [<plugin-id> …]` | Bundle installed plugins into a portable stado binary (no Go toolchain required at the destination) |
| `stado plugin gc [--keep N] [--apply]` | Sweep older installed plugin versions per (scope, signer, name) group (dry-run by default) |
| `stado plugin doctor <plugin-id>` | Inspect manifest + emit per-surface compatibility table with the exact flags to pass |
| `stado plugin info <plugin-id>` | Dump installed plugin's manifest as pretty JSON (sibling to doctor — info dumps, doctor analyses) |

`plugin install --local` keeps signer trust in user state and does not bypass
the project-content gate. After reviewing the plugin, set
`[plugins] allow_project_plugins = true` in the user config to let runtime
discovery load it; install prints this reminder when the gate is still off.
Dependencies declared through `requires` must already be installed at a
satisfying version with a trusted manifest signature and matching WASM digest;
a lookalike directory name is not accepted. Global installs use the user lock
file, while local installs and updates keep their lock in the project.

## Using plugins from the TUI

- Installed plugins can be invoked directly with `/plugin:<id> <tool> {…}`.
- `[tools].overrides` can replace bundled tools with installed plugins.
  Example: `overrides = { bash = "approval-bash-go-0.1.0" }`
- The bundled `auto-compact` background plugin is loaded by default.
- `[plugins].background` loads extra installed plugins that tick
  alongside that default.

`stado tool run --session <id>` binds the tool's plugin to the target
session's persisted conversation and worktree, so `session:read`,
`session:fork`, `llm:invoke`, and the broker-backed EP-0063 artifact imports
work on the CLI path too. Artifact-capable plugins declare their local data
shapes in `artifact_kinds` and request operation-scoped capabilities:
`artifact:propose:<local-kind>`, `artifact:edit:<local-kind>`,
`artifact:read:<qualified-kind-pattern>`, or
`artifact:observe:<qualified-kind-pattern>`. The host injects canonical plugin
and session identity; plugin JSON cannot choose its own scope authority.

`stado memory ...` is the native operator UX for the legacy memory JSONL store,
not a plugin host-import contract and not a generic artifact administration
surface. Use `stado learn migrate` for its one-way import into broker-owned
memory/lesson artifact kinds. The remaining native memory commands and config
names are explicit migration debt rather than a reason for new plugins to use
the removed `memory:*` ABI.

Without `--session`, the command stays a one-shot no-session path. Ordinary
session reads see no live session; declaring artifact capabilities without an
authenticated broker binding fails closed before tool dispatch.

## Config

Relevant `config.toml` sections:

- `[plugins].crl_url` — signed revocation list URL
- `[plugins].crl_issuer_pubkey` — Ed25519 key used to verify the CRL
- `[plugins].rekor_url` — Rekor transparency-log endpoint
- `[plugins].background` — extra installed plugin IDs loaded
  persistently in the TUI/headless server (**user config only**)
- `[plugins].allow_project_plugins` — opt-in to autoload plugins from
  `{cwd}/.stado/plugins/` (default false; cannot be set from project
  config — the gate itself is stripped per EP-0044)
- `[memory].enabled` — inject approved memory artifacts plus any not-yet-migrated
  legacy memories as bounded untrusted prompt context (on by default; set
  `false` to opt out)
- `[tools].overrides` — map bundled tool names to installed plugin IDs

`stado config show` prints the resolved values.

## Gotchas

- **Project-local plugins are off by default.** Repos may ship
  `.stado/plugins/` for convenience, but stado ignores them unless
  the operator sets `[plugins] allow_project_plugins = true` in user
  config. A one-time stderr warning names the skipped directory.
- **`plugin list` is not `plugin installed`.** `list` shows the detailed
  bundled/installed catalog and trust state; `installed` is the concise,
  scope-labelled ID list.
- **Trust is explicit unless you pass `--signer` to install.** The
  TOFU shortcut exists for controlled environments, but it should still
  be backed by out-of-band signer verification.
- **Rollback protection is intentional.** Reinstalling an older version
  under the same signer is rejected.
- **Plugin packages must be plain files.** Symlinks and special files in
  the plugin directory are rejected at install time.
- **`tool run` without `--session` is not a live session.** If a
  plugin needs `session:*` or `llm:invoke`, either pass `--session <id>`
  or run it from the TUI/headless surfaces.
- **Removed delegate imports have no compatibility aliases.** Plugins using
  `stado_http_get`, `stado_fs_tool_*`, `stado_search_*`, or
  `stado_exec_bash` fail to instantiate. Use `stado_http_request`,
  `stado_fs_*`, `stado_proc_*` plus `bundled-bin:<name>`, and the retained
  `stado_lsp_*` primitives. The tool host is attached on every
  `stado tool run`; subprocess and PTY imports still require a surface with a
  sandbox runner such as `stado run` or the TUI.
- **`tool run --workdir` defaults to the plugin's install dir, not
  the operator's CWD.** Plugins that scope `fs:read:.` to project
  files (htb-cve-lookup-style lookups against the operator's repo)
  need `--workdir=$PWD` to resolve relative paths against the
  operator's environment instead of `<state-dir>/plugins/<id>/`.
  EP-0027.
- **`plugin gc` is dry-run by default.** Project and global versions are
  grouped independently. Pass `--apply` to actually
  delete. `--keep` (default 1) controls how many newest versions to
  preserve per (scope, signer, name) group; an explicitly active version is
  always preserved in addition to that newest-version budget. Trust-store entries and
  rollback pins are not touched, so a freshly-deleted older version
  still cannot be reinstalled by accident.
- **When in doubt, run `plugin doctor <id>`.** It parses the
  plugin's manifest and prints which surfaces / flags it needs.
  Faster than reading the source for "why does my plugin fail with
  `stado_http_request returned -1`?" and friends.

## See also

- [docs/features/plugin-authoring.md](../features/plugin-authoring.md) — end-to-end walkthrough for first-time plugin authors
- [docs/plugins/abi-reference.md](../plugins/abi-reference.md) — systematic ABI reference (memory, return codes, handles, manifest schema)
- [docs/plugins/host-imports.md](../plugins/host-imports.md) — function-by-function reference for every host import
- [README.md](../../README.md) — install channels and high-level plugin summary
- [SECURITY.md](../../SECURITY.md) — plugin-publish cookbook and trust model
- [plugins/README.md](../../plugins/README.md) — bundled plugin catalog (opt-in plugins live in [stado-plugins](https://github.com/foobarto/stado-plugins))
- [foobarto/stado-plugins](https://github.com/foobarto/stado-plugins) — concrete opt-in plugin examples (installable)
- [memory.md](memory.md) — review plugin-proposed persistent memories
- [learning.md](learning.md) — review trajectories and manage operational lessons
