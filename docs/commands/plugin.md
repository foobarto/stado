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
- rollback protection for the canonical, unversioned package/source namespace
  per signer (separate official packages signed by one key have independent
  floors)
- optional CRL state (`[plugins].crl_url`)
- optional Rekor transparency-log inclusion (`[plugins].rekor_url`)

Once verified, `stado plugin install` copies the plugin directory below the
global or project plugin root under a host-derived `remote-<sha256>` or
`local-<sha256>` store key. The complete install record binds that key to the
canonical source, signed manifest digest, WASM digest, signer, version, and
remote ref/commit when applicable. A separate user-state receipt records that
the host accepted that exact store row; project-local record and lock files do
not grant authority by themselves. `stado tool run`
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

`plugin verify` checks signature, digest, rollback state, exact host/guest WASM
function signatures, optional CRL, and optional Rekor lookup. CI that only
needs the structural compatibility check can run it without a trust-store
mutation:

```sh
stado plugin abi-check path/to/plugin/dist [path/to/another/dist ...]
```

`abi-check` verifies the manifest-declared WASM digest, compiles without
executing guest code, and checks every `stado` import plus every required tool
or lifecycle export by exact signature. It deliberately does not authenticate
the manifest signer; release admission still uses `plugin verify` or install.

### Install and run

```sh
stado plugin install .
stado plugin installed
stado tool run greet '{"name":"Ada"}'
stado tool run --session abc123 compact '{"threshold_tokens":5000}'
```

For an official package in a monorepo, use its logical semver identity:

```sh
stado plugin install --trust-anchor \
  github.com/foobarto/stado-plugins/supervise@v0.1.1
```

The preferred upstream release tag is `supervise/v0.1.1`, carrying flat
`plugin.wasm`, `plugin.manifest.json`, and `plugin.manifest.sig` assets. The
manifest's signed package version must be `0.1.1` (a leading `v` is also
accepted), while its independent `min_stado_version` is `0.80.0`.
The owner repository publishes `.stado/author.pub`; accepting it once commits
the full owner key, signer pin, and package floor together only after package
verification succeeds. Subsequent installs from that owner work offline from
that unified trust record when only the anchor is unavailable; uncached
package artifacts still require a reachable source or release cache.

`plugin installed` lists the canonical source, exact store key, display alias,
and scope. Commands and configuration should use the canonical source identity
or store key. A manifest name is accepted only when it resolves to one installed
entry across every applicable root; ambiguity fails closed. Runtime authority
comes from the verified install record plus exact lock row for remote packages.
`Manifest.Name` is never an authority namespace (EP-0066). Pre-C71 flat
`<name>-<version>` directories are rejected and must be explicitly reinstalled;
stado never guesses their source.

Management selectors accept an optional `project:` or `global:` prefix, for
example `stado plugin info project:local-<sha256>`. Scope is not part of the
signed package identity; it only tells the management command which install
root to inspect. An unscoped selector that exists in both roots fails closed,
even when both rows contain identical bytes and the same exact store key.

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
| `stado plugin installed` | Show canonical sources, store keys, aliases, and project/global scope |
| `stado plugin verify <dir>` | Verify a plugin directory in place |
| `stado plugin abi-check <dir> [dir...]` | Compile digest-verified WASM without executing it and compare every host import plus required export to this stado build's exact ABI. Does not authenticate the signer |
| `stado plugin verify-installed <[project:\|global:]canonical-source-or-store-key>` | Re-verify an installed plugin against the trust store (catch trust-store drift) |
| `stado plugin install <dir-or-identity>` | Verify, then copy into the plugin directory. Accepts a local directory OR a remote identity `host/owner/repo@version` (fetched + anchor-verified). Flags: `--local` (install under the current project's `.stado/plugins/`; trust remains user-local), `--force` (reinstall over the same version), `--autoload` (persist tools into `[tools].autoload` in the matching user/project config; project loading still needs the user-level trust gate), `--signer <pubkey>` (inline-pin a local package author; on remote install it may only confirm the already-verified owner key, not grant separate trust), `--trust-anchor` (accept the owner's anchor fingerprint on first sight without prompting; verify out of band), `--accept-tag-rewrite` (after independently verifying a force-moved semver tag, replace its locked full commit; invalid for local or full-commit identities) |
| `stado plugin update <[project:\|global:]canonical-source-or-store-key>` | Update the exact active remote source package in one scope; retained rollback packages retain their immutable lock history. `--check` lists without installing |
| `stado plugin remove <[project:\|global:]canonical-source-or-store-key>` | Uninstall every installed version from that exact source namespace in one scope. Admission receipts are revoked first; immutable lock continuity history remains |
| `stado plugin use <[project:\|global:]canonical-source-or-store-key>` | Select an exact installed store row for its source namespace and scope |
| `stado plugin reload <[project:\|global:]canonical-source-or-store-key>` | Re-read a plugin's tools and capabilities; effective inside a TUI session via `/plugin reload` |
| `stado tool run [--session <id>] [--workdir <path>] <tool> [json-args]` | Invoke one installed tool by name (the owning plugin is resolved automatically), optionally against a persisted session. The tool host is always attached. |
| `stado plugin gc [--keep N] [--apply]` | Sweep older installed plugin versions per (scope, source namespace) group (dry-run by default) |
| `stado plugin doctor <[project:\|global:]canonical-source-or-store-key>` | Inspect manifest + emit per-surface compatibility. Lifecycle applications report the interactive TUI as their only current host |
| `stado plugin info <[project:\|global:]canonical-source-or-store-key>` | Show authenticated identity/store metadata plus the installed manifest |

`plugin install --local` keeps signer trust in user state and does not bypass
the project-content gate. After reviewing the plugin, set
`[plugins] allow_project_plugins = true` in the user config to let runtime
discovery load it; install prints this reminder when the gate is still off.
Global installs use the user lock file, while local installs and updates keep
their lock in the project. Pre-v1 manifests deliberately have no dependency
solver; applications should fail explicitly when an optional cooperating
source is unavailable until an owning EP defines source-exact resolution.
`min_stado_version` is a signed admission gate at install and runtime reopen.
A development, malformed, or prerelease host build cannot satisfy a declared
stable minimum; distribution proofs must use a binary carrying its release
version through the build-time version variable.

## Using plugins from the TUI

- Installed plugins can be invoked directly with `/plugin:<id> <tool> {…}`.
- `[tools].overrides` can replace bundled tools with installed plugins.
  Example: `overrides = { bash = "remote-<sha256>" }`
- The bundled `auto-compact` background plugin is loaded by default.
- `[plugins].background` loads extra installed plugins that tick
  alongside that default.

`stado tool run --session <id>` binds the tool's plugin to the target
session's persisted conversation and worktree, so `session:read`,
`session:fork`, `provider:invoke:<N>`, and the broker-backed EP-0063 artifact imports
work on the CLI path too. Artifact-capable plugins declare their local data
shapes in `artifact_kinds` and request operation-scoped capabilities:
`artifact:propose:<local-kind>`, `artifact:edit:<local-kind>`,
`artifact:read:<qualified-kind-pattern>`, or
`artifact:observe:<qualified-kind-pattern>`. The host injects canonical plugin
and session identity; plugin JSON cannot choose its own scope authority.

Core stado has no native memory/learn command or JSONL writer. The sole legacy
reader is the lifecycle-only `artifact:migrate:legacy-memory-v1` trigger in the
official memory application: the broker owns the fixed source/archive path,
identity, schemas, scope binding, staging, and completion fence. New plugins use
only the generic artifact ABI; the removed `memory:*` contract has no alias.

Without `--session`, the command stays a one-shot no-session path. Ordinary
session reads see no live session; declaring artifact capabilities without an
authenticated broker binding fails closed before tool dispatch.

## Config

Relevant `config.toml` sections:

- `[plugins].crl_url` — signed revocation list URL
- `[plugins].crl_issuer_pubkey` — Ed25519 key used to verify the CRL
- `[plugins].rekor_url` — Rekor transparency-log endpoint
- `[plugins].background` — extra exact installed store keys (or globally
  unambiguous friendly aliases) loaded
  persistently in the TUI/headless server (**user config only**)
- `[plugins].allow_project_plugins` — opt-in to autoload plugins from
  `{cwd}/.stado/plugins/` (default false; cannot be set from project
  config — the gate itself is stripped per EP-0044)
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
- **Remote owner trust is one transaction.** `--trust-anchor` accepts the
  first-sight owner key; it does not write a fingerprint early and then ask for
  a second signer grant. Signature/policy failure leaves no owner or signer pin.
- **Rollback protection is package-scoped and intentional.** Reinstalling an
  older version of the same canonical package under the same signer is
  rejected; a differently versioned sibling package signed by that key is not.
- **Source ref, commit, and package version are distinct.** `plugin-lock.toml`
  records `source_revision`, its dereferenced full `resolved_commit`, the
  canonical signed-manifest digest, WASM digest, and signed `package_version`.
  A moved semver tag or replaced same-commit release package is refused before
  install state changes unless the operator explicitly accepts that rewrite;
  incomplete legacy evidence fails closed instead of becoming a local-path
  identity. A full-SHA pin is never implicitly moved to a semver tag by
  `plugin update`; install a new exact identity deliberately.
- **Monorepo latest is package-aware, not repo-wide.** GitHub discovery ignores
  sibling releases. Exact pinned install remains the fallback for GitLab
  subpackages and for packages whose release is outside the bounded recent
  GitHub release list.
- **Plugin packages and installed store entries must be plain state.**
  Symlinks and special files in a package are rejected at install time; a
  symlink entry in an installed root fails discovery closed.
- **`tool run` without `--session` is not a live session.** If a plugin needs
  `session:*`, either pass `--session <id>` or run it from a TUI/headless
  session. `provider:invoke:<N>` is separate: a one-shot tool host constructs
  the operator-configured provider without exposing credentials, while a live
  session borrows its existing provider.
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
  operator's environment instead of `<state-dir>/plugins/<store-key>/`.
  EP-0027.
- **`plugin gc` is dry-run by default.** Project and global versions are
  grouped independently. Pass `--apply` to actually
  delete. `--keep` (default 1) controls how many newest versions to
  preserve per (scope, canonical source namespace) group; an explicitly active version is
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
- [memory.md](memory.md) — staged official memory/learn lifecycle source and migration contract
- [learning.md](learning.md) — candidate-only review and restart limitations
