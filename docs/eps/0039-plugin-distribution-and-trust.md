---
ep: 39
title: Plugin distribution and trust — anchor repo, versioned identity, lock file
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Implemented
type: Standards
created: 2026-05-05
implemented-in: v0.33.0
see-also: [6, 12, 35, 37, 38]
history:
  - date: 2026-05-05
    status: Draft
    note: Initial draft. Companion to EP-0037 (dispatch + naming) and EP-0038 (ABI v2 + bundled wasm). Extends EP-0006's signed-WASM trust model with VCS-based identity, anchor-of-trust pattern, multi-version coexistence, and lock file.
  - date: 2026-05-05
    status: Draft
    note: >
      Revision pass after codex + gemini independent review. Material edits: install layout
      keyed by canonical identity, not manifest name (codex #3, gemini #6); active-version
      state moved per-project + per-user, removing global symlink (codex #3, gemini #6); §B
      requires full 40-char SHA in lock with tag-move detection on every install (codex #14,
      #15); §E source build split into Mode A (deterministic, signed-manifest-bound) and
      Mode B (dev-only, local key) with no silent bridge (codex #16); §C added documented
      out-of-band first-install verification mechanisms (gemini #8); §A added monorepo
      subdir tag convention (codex #17, gemini #17); §K reframed `/plugin install` TUI
      semantics as install-durable / session-flip-enable / opt-in-pin (codex #18). Decision
      log gained D12–D17 capturing the changes.
  - date: 2026-05-05
    status: Implemented
    note: >
      Implemented in v0.33.0. ParseIdentity() with strict semver/SHA validation;
      AnchorTrustStore (per-owner TOFU, separate from per-key TrustStore); lock.go
      (hand-rolled TOML, NewLock/ReadLock/Write); SHA256 drift detection at install +
      --force flag; plugin trust --pubkey-file; plugin use <name>@<version>;
      plugin dev <dir> (gen-key → sign → trust → install). Remote git fetch / 3-tier
      artefact resolution and plugin update/update-anchor deferred as follow-ons.
---

# EP-0039: Plugin distribution and trust — anchor repo, versioned identity, lock file

## Problem

Stado has signed plugin manifests + a local trust-store today
(EP-0006). It works well when operator == plugin developer: build,
sign, install from local directory, trust the pubkey once. It does
not yet have a story for the operator-isn't-author scenario:

- **No remote install.** EP-0006 §"Non-goals" lists "automatic
  plugin discovery or a central registry" — and no central
  registry is the right call — but that has been read as
  "no remote install at all." A user wanting to install
  someone else's plugin today has to clone the repo, build it,
  copy the artefacts to the install dir, and `stado plugin trust`
  the author key out-of-band.
- **No version pinning.** A plugin is `name@version`; multiple
  versions can't coexist on disk; rolling back means reinstall
  from scratch with whatever version is around.
- **No reproducible installs.** Two operators on different
  machines following the same instructions get whatever's in
  whatever upstream they fetched from. No checksums, no lock
  file.
- **Pubkey discovery is per-repo.** A plugin author with five
  plugins maintains the same pubkey in five repos and risks
  drift; an operator trusts the same author five times.

The user's HTB toolkit (twelve plugins under `htb-writeups/htb-
toolkit/`) is a concrete pressure: each plugin has its own author
key today; reinstalling the toolkit requires twelve trust prompts.
That's the wrong shape.

## Goals

- Identity: `<host>/<owner>/<repo>[/<plugin-subdir>]@<version>`,
  Go-modules style. Resolves via prefix-matched VCS for github,
  gitlab, codeberg, generic git hosts. Vanity-import-style
  discovery deferred to v2.
- Strict versioning: only semver tags (`vX.Y.Z`) or full commit
  SHAs. No floating tags; `latest`/`main`/`HEAD`/branch-names
  rejected at install time with a hint to use `plugin update
  --check`.
- Anchor-of-trust pattern: each owner publishes a
  well-known `<host>/<owner>/stado-plugins/.stado/author.pub`.
  Plugin repos do NOT carry their own pubkey. One key per owner;
  one signing entity per repo.
- Trust = author fingerprint. TOFU on first install per owner,
  with scope choice (exact repo / `<owner>/*` / `*`). Persists
  across versions and across repos. Mismatched fingerprint on
  new version = loud refusal with explicit resolution paths.
- Multi-version coexistence on disk: install side-by-side, switch
  active version with `plugin use`, pin via `[plugins.<name>]
  version = "vX.Y.Z"`.
- Lock file: `.stado/plugin-lock.toml` per project, mirrors
  `go.sum` semantics. `plugin install --from-lock` reproduces
  exactly.
- Sandbox interaction: install respects `[sandbox] http_proxy`,
  fails fast under `network = "off"`, supports local tarball cache
  for offline reinstalls after first fetch.
- CLI surface: `plugin install <repo>@<version>`, `plugin update`,
  `plugin trust`, `plugin untrust`, `plugin use`, `plugin
  update-anchor`. Plus the `plugin` quality pass items
  flagged from prior EPs (sha drift, `--pubkey-file`, `dev`,
  `sign`, `--autoload`).

## Non-goals

- A central plugin registry. EP-0006's non-goal is preserved.
  EP-0039 adds VCS-based install via URL, not a discovery
  service.
- Plugin proxy / mirror infrastructure. Schema reserved
  (`[plugins] mirror = "..."`) but not implemented v1.
- Vanity import paths (Go-style `<meta name="stado-import">`
  HTML tag discovery). Deferred to v2.
- Modifying EP-0012's binary distribution model. Plugin
  distribution is its own domain; binary self-update is
  EP-0012's territory.
- Tracking plugin downloads, telemetry on install events.
  Operator action remains private to their machine.
- Auto-update on plugin version changes. Operators run
  `plugin update` deliberately.
- `[sandbox]` implementation. That's EP-0038. Reference here is
  to how install honours the configured sandbox state.

## Design

### §A — Identity

Plugin identifier syntax:

```
<host>/<owner>/<repo>[/<plugin-subdir>]@<version>
```

Examples:

```
github.com/foobarto/superduperplugin@v1.0.0
github.com/foobarto/htb-writeups/htb-toolkit/gtfobins@v0.1.0
gitlab.com/myorg/internal-tool@v2.4.1
codeberg.org/user/cool-plugin@a1b2c3d4f56789
git.sr.ht/~user/myplugin@v0.1.0
```

Identity rules:

- Path segments separate `<host>`, `<owner>`, `<repo>`. Optional
  trailing `<plugin-subdir>` (single or multi-segment) for
  monorepo layouts.
- Version after `@` is required. `<repo>@v1.0.0` parses; `<repo>`
  alone errors with "version required".
- The full path is the **canonical identity** throughout stado.
  Per EP-0037 §B's revision, the **manifest does not carry a
  top-level `name:` field**. Plugin identity comes from the
  install source. Tools the plugin exposes get a wire-form
  prefix from the **operator-assigned local alias** (per EP-0037
  §B), defaulting to the repo's last path segment — so a plugin
  installed from `github.com/foo/bar` defaults to alias `bar`
  and exposes tools like `bar.read`, `bar.write`. The operator
  can override at install time with `--alias=fs` to get
  `fs.read`, `fs.write` instead. Two installs from different
  repos can coexist at different aliases.

#### Monorepo subdir tags

Codex review #17 / gemini review #17 surfaced that a 12-plugin
monorepo (HTB toolkit) cannot release independent versions if all
twelve plugins share repo-wide tags. The convention for monorepo
plugin versioning:

```
github.com/foobarto/htb-writeups/htb-toolkit/gtfobins@v0.1.0

Resolves at install time to:
  - Tag preference order:
    1. Subdir-prefixed:   htb-toolkit/gtfobins/v0.1.0  (preferred)
    2. Repo-wide:         v0.1.0                       (fallback)
  - First match wins.
```

Authors publish per-subdir tags (`htb-toolkit/gtfobins/v0.1.0`,
`htb-toolkit/payload-generator/v0.2.0`) so each plugin moves
independently. Stado fetches the subdir-prefixed tag first; if
absent (single-plugin repo), falls back to repo-wide tag. The
canonical lock-file entry records the actual tag name resolved:

```toml
[plugins."github.com/foobarto/htb-writeups/htb-toolkit/gtfobins"]
version = "v0.1.0"
tag = "htb-toolkit/gtfobins/v0.1.0"   # actual resolved tag
commit_sha = "a1b2c3d4...e7f8"
...
```

`plugin update --check <repo>/<subdir>` queries the subdir-
prefixed tag space first, reporting newer available subdir tags.
For monorepos that prefer single-version-everywhere semantics
(release lockstep), the author skips subdir-prefixed tags and
all plugins under the repo-wide tag move together — also
supported.

Bundled GitHub Release assets follow the same convention:
release `htb-toolkit/gtfobins/v0.1.0` carries assets for that
plugin only (`plugin.wasm`, `plugin.manifest.json`,
`plugin.manifest.sig` — no need for filename prefixes since
each release is for one plugin). Repo-wide releases for
single-plugin repos work as before (release `v0.1.0` carries
assets for that one plugin).

Single-plugin repos ignore the subdir-tag layer entirely;
identity is `<host>/<owner>/<repo>@<version>` with no subdir
segment.

#### Host prefix matching (v1)

```
github.com/owner/repo       → https://github.com/owner/repo.git
gitlab.com/owner/repo       → https://gitlab.com/owner/repo.git
codeberg.org/owner/repo     → https://codeberg.org/owner/repo.git
git.sr.ht/~user/repo        → https://git.sr.ht/~user/repo
git.<custom-domain>/<...>   → https://git.<...>/<owner>/<repo>.git
```

Generic git-host fallback: any prefix matching `git.<host>/...` is
treated as a git URL. Other hosts unrecognised at v1 — install
errors with a clear message.

#### Vanity / private hosts (v2)

Reserved for v2: `<meta name="stado-import">` HTML discovery at
the import path's URL, allowing `mydomain.com/myname/plugin` →
`git@private.gitlab.com:myname/plugin.git` mappings without
hardcoding mydomain.com as a known host. Not in v1 scope.

### §B — Strict versioning

Accepted version formats:

- Semver tags: `v0.1.0`, `v1.0.0`, `v1.2.3-rc.1` (pre-release
  per semver spec accepted; build metadata stripped).
- **Full 40-char commit SHAs**. Codex review #14 surfaced that
  short (7+) abbreviations can become ambiguous as the repo
  grows; "names exactly one artefact forever" requires the full
  object ID. CLI-side: `stado plugin install <repo>@<7-char>`
  is accepted, but the abbreviation is resolved against the
  remote at install time and the **full SHA is what gets
  recorded** in the lock file and trust state. The lock-file
  identifier is always the full 40-char form.

Rejected with helpful error:

- `@latest` — replaced over time; breaks signing chain.
- `@main`, `@HEAD`, `@master`, `@develop`, any branch name —
  pointer-based; same problem.
- Empty (no `@`) — version is mandatory.

#### Tag-move detection

Codex review #15 surfaced that semver tags **can be force-moved**
in git. A signed `v1.0.0` today and `v1.0.0` after force-push are
different bytes; lock files only catch this on a *second* install
attempt. The implementation:

- On every install where the lock file already records the
  identity (e.g. operator runs `plugin install <repo>@v1.0.0`
  and the lock has `<repo>@v1.0.0` already), stado fetches the
  current resolved object SHA for that tag and compares with the
  lock's recorded SHA. **Mismatch refuses the install** with the
  same loud-refusal shape as a fingerprint mismatch (§G):

```
Tag rewrite detected for github.com/foo/bar @ v1.0.0:
  Locked SHA:          a1b2c3d4...e7f8 (installed 2026-05-05)
  Current SHA:         9f8e7d6c...3210
  Manifest signature:  matches anchor pubkey (49cb...)

The semver tag was force-moved on the upstream repo since this
project last installed it. This is either a deliberate author
re-release (legitimate) or a tampered upstream (compromise).

Resolutions:
  - Update lock to accept the new bytes:
      stado plugin install <repo>@v1.0.0 --accept-tag-rewrite
    (the lock entry's SHA is replaced; the new bytes are
    re-verified against the anchor and accepted on
    fingerprint match).
  - Pin to the previous SHA explicitly:
      stado plugin install <repo>@a1b2c3d4...e7f8
  - Walk away — the lock continues to reference the previous
    bytes; existing installs unaffected.
```

The lock entry records `tag = "v1.0.0"` AND `commit_sha =
"a1b2c3d4..."` so the "tag → bytes" relationship is checked, not
just the eventual sha256 of the wasm artefact. A first install
of a tag (no prior lock entry) does not have anything to
compare against; the tag's resolved SHA is recorded for next
time.

#### Stado does not claim semver tags are immutable

Tags are immutable by author convention (semver discipline) and
by trust — stado refuses to silently switch underlying bytes,
which is the part that matters for the signing chain. The trust
model is "by author" (per §C/D); the tag-move detection is the
trust-by-author bookkeeping.

Error message:

```
Plugin install: floating tags are not accepted.
  github.com/foo/bar@latest
                    ^ use a semver tag (vX.Y.Z) or full commit SHA

To find the newest published version:
  stado plugin update --check github.com/foo/bar
```

#### Why strict

A signed `v1.0.0` is signed forever. A signed `latest` is whatever
the author last pushed there, including a key-rotated
post-compromise version. Refusing floating tags preserves the
property "every install identifier names exactly one signed
artefact, forever." The cost is operators must specify versions;
the benefit is the signing chain doesn't have a hole.

`plugin update --check` is the supported way to find newer
versions: it queries the anchor's `index.toml` (§E.4) or git tag
list and shows what's available without actually installing.

#### Pre-release and build metadata

`v1.2.3-rc.1` accepted as a semver tag (the pre-release suffix is
ordered per semver). `v1.2.3+meta` build metadata stripped before
comparison; the canonical form is `v1.2.3` for storage and
display.

### §C — Anchor-of-trust pattern

Every owner publishes one well-known anchor repo containing the
single signing pubkey:

```
<host>/<owner>/stado-plugins/.stado/author.pub
```

Examples:

- `github.com/foobarto/stado-plugins/.stado/author.pub`
- `gitlab.com/myorg/stado-plugins/.stado/author.pub`

Plugin repos do NOT carry author.pub. The pubkey is fetched
from the anchor at install time, cached locally at
`~/.cache/stado/anchors/<host>/<owner>/author.pub` after first
fetch.

#### File format

```
# .stado/author.pub
49cbeaa8289e6623da8f7a13fc88b2d7f8a13fc88b2d7f8a13fc88b2d7f8a13fc
```

A single line of hex-encoded ed25519 public key. Comments (lines
starting with `#`) skipped. Trailing whitespace trimmed.

#### One key per owner; one signing entity per repo

The architecture commitment is **single-author repos** for plugin
distribution. A repo with plugins by multiple humans must
designate one signer (the gatekeeper); the manifest's `author:`
field is display-only, the signature is the authority.

This means an organisation (cncf, kubernetes, etc.) shipping
plugins under `<host>/<org>/...` maintains one anchor key for the
org. Individual contributors don't have their own keys; they
contribute via PRs, and the org's release infrastructure signs.

Multi-author repos with mixed signing are not supported. Operators
who want fine-grained author attribution should use separate org
namespaces.

#### Verification chain

On every install:

1. Parse identity → `<host>/<owner>/<repo>[/<plugin-subdir>]@<version>`.
2. Resolve owner segment (`<host>/<owner>/`); fetch
   `<host>/<owner>/stado-plugins/.stado/author.pub` (cached after
   first fetch). Cache invalidation: `stado plugin update-anchor
   <host>/<owner>` re-fetches; `plugin update` re-fetches as a
   side effect.
3. Fetch the artefact for `<repo>@<version>` (per §D — release
   asset, dist/ in tree, or source build).
4. Verify `plugin.manifest.sig` against the anchor pubkey. Refuse
   on mismatch (§F failure modes).
5. Verify `plugin.wasm` sha256 against the manifest's
   `wasm_sha256` field. Refuse on mismatch.
6. Apply the operator's TOFU/trust state (§D below).

#### Why one key per owner

- Author maintains key in ONE place across all their plugin repos.
- Operator's TOFU prompt happens once per owner, not once per repo.
- Key rotation is one commit to the anchor repo.
- Operator's mental model matches: "trust foobarto" is a single
  decision, not twelve (HTB toolkit example).
- The user explicitly signed off on this in the EP-0037/0038/0039
  design conversation.

#### Anchor unavailability

The anchor is required only on **first install** from a given
owner; afterwards stado uses the cached pubkey. Cache invalidation
is operator-driven: `plugin update-anchor` or `plugin update`
trigger a re-fetch. If the anchor is offline at first-install time
for an owner, the install fails with a clear message
(`anchor repo at <url> not reachable; first-time install requires
anchor; cached owners are unaffected`).

#### Anchor squatting / first-install verification

Gemini review #8 surfaced that TOFU only protects against changes
to a key already seen — it does NOT help an operator's first-ever
install verify "is this fingerprint correct or am I trusting a
squatter?" The operator typically can't tell if `github.com/foo/`
was deleted and re-registered to an attacker since the trust
prompt looks identical.

Optional out-of-band verification mechanisms (not all required v1;
ship as available + document):

1. **Signed DNS TXT record** at the author's claimed domain.
   ```
   stado plugin trust --verify-dns github.com/foobarto
   ```
   stado looks up `_stado-anchor.foobarto.dev` (configurable per
   `[plugins]` config) and expects a TXT record:
   ```
   anchor=github.com/foobarto/stado-plugins fpr=49cbeaa8289e6623
   ```
   If the TXT record matches the anchor's pubkey fingerprint,
   the trust prompt skips the manual confirmation. If absent,
   the prompt continues as TOFU. v1 implementation: yes if
   straightforward; otherwise reserved.

2. **GitHub identity verification** (GitHub-only). A future
   sigstore-style integration where the anchor's pubkey is
   ALSO published as a GitHub release on the anchor repo
   signed by the GitHub Actions runner identity. Operator
   verifies the signature chain rooted in GitHub's OIDC
   issuer. **Reserved** for a future EP; not v1.

3. **Out-of-band fingerprint card.** Author publishes their
   anchor fingerprint on a personal site / talk / repo
   README; operator verifies manually before running
   `stado plugin trust <fpr> --scope='<host>/<owner>/*'`.
   Not automated; documented as the conservative path.

The TOFU-without-OOB-verification path remains available; it's
the same threat model as `ssh` first-connect and is acceptable
for the operator who consciously trusts the URL they typed in.
But the EP names the mechanisms above so the threat-model
discussion is on the record.

### §D — Trust model: TOFU + scope

On first install from any owner, stado fetches the anchor pubkey
(§C verification chain step 2), then prompts the operator:

```
First-time install: github.com/foobarto/superduperplugin@v1.0.0

Author key:        49cbeaa8289e6623
Anchor:            github.com/foobarto/stado-plugins/.stado/author.pub
Trust the key for what scope?

  [1] github.com/foobarto/superduperplugin   (this exact repo)
  [2] github.com/foobarto/*                  (any repo by this author)
  [3] *                                      (any repo signed with this key)
  [n] do not trust; cancel install

Selection [1/2/3/n] (default: 1):
```

#### Trust state file

`~/.config/stado/trust.toml` (or `[XDG_CONFIG_HOME]/stado/trust.toml`):

```toml
[[trusted_authors]]
fingerprint = "49cbeaa8289e6623"
scope = "github.com/foobarto/*"
trusted_at = "2026-05-05T10:00:00Z"
note = "Bartosz Ptaszynski — htb-toolkit, stado examples"

[[trusted_authors]]
fingerprint = "aa77ff..."
scope = "github.com/cncf/*"
trusted_at = "..."
note = "CNCF — kubernetes-related plugins"
```

#### Scopes

Three scope shapes, in increasing breadth:

- `<host>/<owner>/<repo>` — exact repo (most conservative).
- `<host>/<owner>/*` — all of an owner's repos (default for that
  owner).
- `*` — any repo signed with this fingerprint (most permissive;
  for foundations or shared signing keys).

Subsequent install attempts: stado checks the new install's
identity against trusted scopes; matches the fingerprint against
trusted entries' fingerprints; if both match, install proceeds
silently. If fingerprint matches but scope doesn't, prompt
appears asking to widen scope.

#### Pre-trust without installing

```bash
stado plugin trust github.com/foobarto                         # scope = github.com/foobarto/*
stado plugin trust github.com/foobarto --scope=author          # explicit alias
stado plugin trust github.com/foobarto --scope=repo            # github.com/foobarto/superduperplugin only
stado plugin trust github.com/foobarto --scope=any             # *
stado plugin trust 49cbeaa8289e6623 --scope='github.com/foobarto/*'  # by fingerprint, explicit scope
```

#### Untrust

```bash
stado plugin untrust 49cbeaa8289e6623           # by fingerprint
stado plugin untrust github.com/foobarto        # by owner; fingerprint resolved from anchor cache
```

Untrust removes the entry from `trust.toml`. It does NOT uninstall
plugins that were installed under that trust — those continue to
work; future updates from that owner will re-prompt for trust.

### §E — Artefact resolution (three-tier fallback)

When fetching `github.com/foo/bar@v1.0.0`, stado tries:

1. **GitHub Release attached to the tag**: download `plugin.wasm`,
   `plugin.manifest.json`, `plugin.manifest.sig` as release assets.
   Preferred — no source build, no toolchain needed.
2. **Files at `dist/` in the tagged tree**: if no release, fetch
   `dist/plugin.wasm`, `dist/plugin.manifest.json`,
   `dist/plugin.manifest.sig` via `git archive` or raw raw URL.
3. **Source build**: clone tag, run `build.sh` if present, expect
   `plugin.wasm` artefact afterwards. **Last resort, opt-in via
   `--build` flag**, requires Go/Rust/Zig/etc. toolchain present.
   See "Source build trust model" below.

Tier 1 covers casual operators (downloading pre-built wasm).
Tier 2 supports authors who don't bother with releases. Tier 3 is
for development workflows.

#### Source build trust model

Codex review #16 surfaced a real problem: who signs the wasm that
`build.sh` produces? `--build` runs an arbitrary script before
trust is established; the resulting wasm doesn't match any
pre-signed manifest because the manifest references a wasm
sha256 the build hadn't yet produced.

The resolution: source build operates in two clearly-distinct
modes, never silently bridging them.

**Mode A: deterministic build with committed signed manifest.**
The repo commits both `plugin.manifest.json` (with `wasm_sha256`)
and `plugin.manifest.sig` AT THE TAGGED TREE. The `build.sh` is
expected to produce a wasm whose sha256 matches the committed
manifest. Stado runs the build, computes the sha256 of the
artefact, and refuses install on mismatch:

```
Source build sha256 mismatch:
  Manifest declares: abc123...
  Build produced:    9f8e7d...

The build is non-deterministic or the manifest is stale.
Stado will not install a build whose bytes differ from what the
author signed.

Resolutions:
  - Re-build with the documented toolchain version
    (often: a pinned Go / Rust / Zig version listed in
    plugin.manifest.json's "build" section)
  - Use the release-asset path instead: stado plugin install
    <repo>@v1.0.0 (drop --build)
```

This mode preserves the signing chain — operator trusts the
author's signed manifest; the build is just a way to recover
the wasm that matches it. Sandbox: `build.sh` runs under
`bwrap --bind <tagged-tree-clone> /work --unshare-net` (no
network for the build step) — operators sandbox the build
explicitly via `--build --sandbox-build` flag (default off
v1, opt-in; recommended for any non-author install).

**Mode B: dev-only local build with local signing.**
Operator working on their own plugin. `stado plugin dev <dir>`
(per §I) handles this: gen-key + build + trust the dev key
locally + install --force. The dev key is TOFU-pinned in
`trust.toml` with `dev = true`; trust scope is the dir, not
the eventual upstream repo. When the plugin is published,
the operator switches to Mode A signing for distribution.

`--build` without an existing committed signed manifest is
**Mode B only**; stado refuses Mode A interpretation if the
manifest is missing and prompts the operator: "no signed
manifest committed; use `stado plugin dev .` for local-key
sign-and-install, or check whether the upstream author has
release artefacts (`plugin install <repo>@<version>`)."

The two modes are operator-explicit. There is no
`--build --without-signed-manifest --trust-the-build-output`
escape hatch; if the operator wants to install unsigned wasm
they're either in dev mode (use `plugin dev`) or going outside
stado's trust model.

#### Multi-plugin repos

For `<repo>/<plugin-subdir>@<version>` paths:

- Tier 1: release assets prefixed with subdir, e.g.
  `htb-toolkit-gtfobins-plugin.wasm`. Convention: release author
  prefixes assets with the plugin subdir for disambiguation.
- Tier 2: artefacts at `<plugin-subdir>/dist/plugin.wasm`,
  `<plugin-subdir>/dist/plugin.manifest.json`, etc.
- Tier 3: build script in `<plugin-subdir>/build.sh`.

#### Lock file recording

After successful install, stado records in
`.stado/plugin-lock.toml` (per-project) or
`~/.config/stado/plugin-lock.toml` (per-user, for global installs)
which tier was used:

```toml
[plugins."github.com/foobarto/superduperplugin"]
version = "v1.0.0"
wasm_sha256 = "abc123..."
manifest_sha256 = "def456..."
author_fingerprint = "49cbeaa8289e6623"
source = "release"           # release | tree | build
installed_at = "2026-05-05T10:00:00Z"
```

`plugin install --from-lock` reproduces exactly: same version,
same source tier, same checksums. Differs from `plugin install`
in that it refuses to fall through tiers; if `source = "release"`
in the lock file but no release exists at install time, error
out instead of silently falling back to `dist/` or build.

#### Anchor publishability index (reserved, optional)

The anchor repo MAY (not MUST) publish an `index.toml` listing
the owner's plugins:

```toml
# foobarto/stado-plugins/index.toml
[author]
name = "Bartosz Ptaszynski"
contact = "..."
canonical_url = "github.com/foobarto/stado-plugins"

[plugins.superduperplugin]
description = "..."
repo = "github.com/foobarto/superduperplugin"
versions_supported = ["v1.x", "v2.x"]
categories = ["network", "ctf-recon"]

[plugins.htb-toolkit]
description = "..."
repo = "github.com/foobarto/htb-writeups"
plugin_subdir_pattern = "htb-toolkit/*"
plugins_in_subdir = [
    "gtfobins", "cve-index", "hash", "kerberos",
    "ldap", "netexec", "payload-generator",
    "peas-runner", "exfil-server", "hostfile-manager",
    "htb-lab"
]
```

Convention reserved for `index.toml` filename. Used by:

- `plugin update --check <repo>` — queries `versions_supported`
  to find the newest tag matching constraints.
- `plugin search <query>` (future, when registry-style discovery
  becomes a real concern) — fetches anchors of trusted authors,
  parses `index.toml`, ranks by query match.

Not required for v1; reserved for backward-compatible later
adoption.

### §F — Multi-version coexistence

#### Install layout (per-canonical-identity, content-addressed)

Per EP-0037 §B's identity revision, install dirs are keyed by a
hash of the canonical identity (not by manifest `name:`, which
no longer exists). This prevents the codex review #3 / gemini
review #6 collision between two repos that would have both
declared `name = "fs"`:

```
$XDG_DATA_HOME/stado/plugins/
  github.com/
    foobarto/
      superduperplugin/
        v1.0.0/
          plugin.wasm
          plugin.manifest.json
          plugin.manifest.sig
        v1.1.0/
        v2.0.0/
        installs.toml          # per-canonical-identity install metadata
                                # (default local alias, install times, etc.)
    bar/
      fs/
        v1.0.0/                # totally separate from foobarto/fs
        ...
```

(Or hash-encoded shorter paths under a flat
`stado/plugins/<sha-of-canonical-identity>/<version>/` if file-
system-tree depth becomes a concern — implementation choice.)

#### Active-version state is per-project, not global

Codex review #3 / gemini review #6 also flagged that the previous
draft's "active version" symlink was global state — Project A
needing v1 and Project B needing v2 would fight over the same
symlink. **Active-version state lives per-project**, not
globally:

- `.stado/plugin-lock.toml` records the active version per
  identity for the project. When the project's stado loads, the
  lock file's pinned versions take effect for that project.
- For operator CLI usage outside any project (`stado plugin run
  ...` without a CWD-detected `.stado/`), the user-level
  `~/.config/stado/plugin-state.toml` records a per-user default
  active version. `plugin use <repo> <version>` without a project
  writes to the user-level file; with a project (CWD has
  `.stado/`), writes to the project lock.
- There is **no global `<name>.active` symlink**. Past draft
  language about a `.active` symlink is replaced with this
  per-scope state.

Lock-file shape extension:

```toml
[plugins."github.com/foobarto/superduperplugin"]
version = "v2.0.0"            # active version for this project
local_alias = "superduper"    # optional override of default alias
wasm_sha256 = "abc..."
manifest_sha256 = "def..."
author_fingerprint = "49cb..."
tag = "v2.0.0"
commit_sha = "a1b2c3...e7f8"   # full 40-char (per §B)
source = "release"
installed_at = "2026-05-05T10:00:00Z"
```

When stado loads, each plugin's active version is resolved by:

1. Project's `.stado/plugin-lock.toml` if present.
2. User's `~/.config/stado/plugin-state.toml` if no project lock
   entry.
3. The single-installed version if exactly one is on disk.
4. **Refuse to start** with a clear error if multiple versions
   are installed and no resolution applies — operator must
   explicitly `plugin use` to pick.

#### CLI

```bash
# Install a new version; prompts to switch active or keep current
stado plugin install github.com/foo/bar@v1.0.0
stado plugin install github.com/foo/bar@v2.0.0
# Output:
#   v2.0.0 installed alongside v1.0.0.
#   Make v2.0.0 active for project '/cwd'? [Y/n]:

stado plugin install github.com/foo/bar@v2.0.0 --keep-active
# install but don't switch active

stado plugin install github.com/foo/bar@v2.0.0 --autoload
# install and pin to autoload (per EP-0037 [tools.autoload])

stado plugin use github.com/foo/bar v1.0.0
# switch active version (rollback)

stado plugin ls --all-versions
# shows all installed versions, marks active

stado plugin remove github.com/foo/bar@v1.0.0
# remove specific version

stado plugin remove github.com/foo/bar
# remove ALL versions; confirms first
```

#### At runtime

At most one version of a given plugin name is active per session.
The model never sees two `fs.read` tools; the active version's
tool surface wins. Project config can pin:

```toml
# .stado/config.toml
[plugins.fs]
version = "v2.0.0"           # pinned; install-time defaults respected
                              # only if absent
```

If the pinned version isn't installed, stado errors at startup
with a clear message:

```
Project pins github.com/foo/fs@v2.0.0 but only v1.x is installed.
  Install with: stado plugin install github.com/foo/fs@v2.0.0
  Or remove pin from .stado/config.toml.
```

#### Why allow side-by-side

Three concrete cases:

- **Rollback**: v2.1.0 regressed something; `plugin use ... v2.0.0`
  reverts in one command without re-fetching.
- **A/B during dev**: author iterates; operator switches between
  candidate versions to compare.
- **Project pinning**: project A uses `v1.x`, project B uses
  `v2.x`; operator's machine has both.

### §G — Update flow

```bash
stado plugin update github.com/foo/bar              # bump to latest matching tag, update lock
stado plugin update --all                           # update every locked plugin
stado plugin update --check                         # show available updates without installing
stado plugin update --check github.com/foo/bar      # check single plugin
stado plugin update github.com/foo/bar@v2.0.0       # explicit target version
```

#### Drift detection

`update` warns when a plugin's `wasm_sha256` changes but the
author fingerprint stays the same:

```
github.com/foobarto/superduperplugin: v1.0.0 → v1.0.1
  Author fingerprint:    49cbeaa8289e6623 (unchanged)
  WASM sha256:           abc123... → def456...
```

Expected — just informs. No prompt.

`update` errors when the author fingerprint changes:

```
github.com/foobarto/superduperplugin: v1.0.0 → v2.0.0
  Old fingerprint:       49cbeaa8289e6623
  New fingerprint:       aa77ff...

This is either a key rotation event or a compromise. Stado
refuses to install.

To resolve:
  - If foobarto rotated keys legitimately, the new key should be
    in the anchor.
    Run: stado plugin update-anchor github.com/foobarto
    (Re-fetches the anchor and re-prompts trust on the new
    fingerprint.)
  - If you don't trust the new key, do nothing. Existing versions
    stay installed and continue to work. The pin in
    .stado/config.toml continues to point at the older known-good
    version.
  - Force-trust the new key (if you've verified it
    out-of-band):
    stado plugin trust aa77ff... --scope='github.com/foobarto/*'
    Then re-run plugin install / update.
```

Three explicit resolution paths: legitimate rotation
(`update-anchor`), pin to known-good older version (no action),
or out-of-band trust (`plugin trust <fpr>`).

#### Anchor pubkey rotation

Same protection. `update-anchor github.com/foobarto` fetches
fresh from the anchor; if the cached fingerprint differs, prompts
the operator:

```
github.com/foobarto anchor rotation:
  Old fingerprint:       49cbeaa8289e6623
  New fingerprint:       bb88cc...

Either foobarto rotated keys (legitimate) or the anchor was
compromised. Trust the new key?  [y/N]
```

If `y`: cached fingerprint replaced; future installs and updates
verify against the new key. If `N`: cache unchanged; new installs
fail with the same fingerprint-mismatch error until the operator
decides.

### §H — Sandbox interaction

Per EP-0038, operators may run with `[sandbox]` configured. Plugin
install respects this:

#### Network constraints

- `[sandbox] http_proxy = "http://..."` — install honours; outbound
  requests go through proxy. Useful for operators behind corporate
  proxies.
- `[sandbox.wrap] network = "off"` (per EP-0038 §I) — install
  fails fast: `install requires network egress; current sandbox
  mode 'wrap' has 'network = off'. Either drop the constraint
  temporarily, install offline (see --from-lock), or arrange
  out-of-band installation.`
- `[sandbox.wrap] network = "namespaced"` — install proceeds
  inside the namespace; whatever DNS/proxy the namespace has
  configured applies.

#### Local cache

`~/.cache/stado/plugin-tarballs/` stores fetched artefacts (one
per `<repo>@<version>`). After first fetch, subsequent installs
of the same identity reuse the cached tarball without
re-fetching. Cache invalidation: `stado plugin install
--no-cache <repo>@<version>` forces re-fetch; otherwise the
tarball is treated as immutable.

The cache enables offline reinstalls: an operator who has fetched
plugin X can later reinstall it under `network = "off"` because
no fetch is needed. `plugin install --from-lock` respects the
cache too: it verifies the cached tarball matches the lock file's
sha256 before extracting.

#### Mirror (reserved)

```toml
# .stado/config.toml
[plugins]
mirror = "https://my-mirror.local"
```

Reserved schema; not implemented v1. When set in v2+, the mirror
prefix replaces all `<host>/...` lookups: the install tool
fetches from `https://my-mirror.local/<host>/<owner>/<repo>`
instead of `<host>/<owner>/<repo>` directly. Useful for
self-hosted teams; preserves the identity-as-canonical-name
model (the lock file still records `github.com/foo/bar`, even
if the bytes came from `my-mirror.local`).

### §I — `stado plugin` quality pass

Items flagged in EP-0037 for a separate scoped PR; included here
for completeness as part of EP-0039's surface. None require ABI
changes.

#### sha256 drift detection on install

Today: `stado plugin install <local-dir>` skips if `name`+`version`
already installed. New behaviour: detect when the local manifest's
`wasm_sha256` differs from the installed manifest, treat as
unambiguous evidence the operator rebuilt, and `reinstalling
(sha256 changed)` automatically. `--force` accepted but not
required for this case.

#### `plugin trust --pubkey-file`

Today's `plugin trust <hex-pubkey>` argument-only form retains;
`--pubkey-file <path>` added for the common case of trusting a
key whose hex form lives at a known path. Useful pre-EP-0039
(when authors still have author.pubkey files in their repos);
post-EP-0039, the anchor flow displaces this for new plugins,
but the explicit form stays for legacy keys.

#### `plugin dev <dir>`

Inner-loop development command:

```bash
stado plugin dev .
# Equivalent to:
#   stado plugin gen-key (if not already done in this dir)
#   ./build.sh
#   stado plugin trust <pubkey> (TOFU-pinned dev key)
#   stado plugin install --force .
#   stado plugin reload <name>
```

Uses a TOFU-pinned dev key — a fingerprint marked `dev = true` in
trust.toml, scoped to the plugin repo path only. Prevents
accidental promotion of a dev key to a release-key trust scope.

#### `plugin sign <dir> --key <path>`

Explicit signing step, separate from `install`. Useful for CI
pipelines that build plugin artefacts but don't install them.

#### `plugin install --autoload`

Pin to `[tools.autoload]` at install time, persisted in
`.stado/config.toml`. Saves the operator the second `tool
autoload` call.

#### `plugin doctor` extends with cap-vs-sandbox checks

Per EP-0038 §I, `plugin doctor` now checks declared caps against
the active `[sandbox]` configuration:

```
github.com/foobarto/superduperplugin@v1.0.0:
  declares: net:dial:tcp:*:*
  current sandbox: [sandbox.wrap] network = "namespaced"
  WARNING: plugin caps will fail at runtime; namespaced network
           denies arbitrary outbound dial.
  resolution:
    - drop the cap (plugin doesn't actually need it)
    - configure [sandbox.wrap] bind for the targets the plugin
      reaches
    - run with --sandbox=<profile> that includes the targets
```

#### `plugin ls` output renderer matches `tool ls`

Same column layout, `--json` shape, status indicators. Renderer
shared between the two commands.

### §J — Migration

#### From the current state

Today's plugin model uses per-repo author.pubkey, single-version
install, no lock file, no version pinning. EP-0039 changes:

1. **Anchor pattern is opt-in for authors**, mandatory for new
   remote installs after EP-0039. Authors maintain their existing
   per-repo pubkey for legacy plugin-install-from-local-dir
   workflows; remote installs require the anchor.
2. **Existing trust entries** (per-repo fingerprints from
   pre-EP-0039 installs) continue to work for installs from local
   directories. Remote VCS install verifies against anchor; if
   the repo's existing per-repo pubkey matches the operator's
   trusted fingerprint AND matches the anchor's pubkey, install
   proceeds without re-prompt.
3. **Multi-version coexistence** is opt-in. The first time a
   plugin is installed at multiple versions, stado migrates the
   per-plugin install dir from `<name>/` to `<name>-v.../`
   layout transparently; existing installs continue to work
   under either layout.
4. **Lock file** is created on first `plugin install <remote>`
   that targets the project. No retroactive lock file generation
   for plugins installed before EP-0039 — the operator can
   regenerate by running `plugin install --update-lock` (or
   editing the lock file directly).

#### From the user's HTB toolkit (12 plugins, single author)

Migration path for the HTB toolkit specifically:

1. Author creates `github.com/foobarto/stado-plugins` anchor repo
   with `.stado/author.pub` containing one ed25519 key.
2. Re-signs all twelve plugin manifests under
   `github.com/foobarto/htb-writeups/htb-toolkit/<plugin>` with
   the same key.
3. Tags each plugin with a semver tag (`v0.1.0` first time).
4. Operator installs with one TOFU prompt the first time:
   ```
   stado plugin install github.com/foobarto/htb-writeups/htb-toolkit/gtfobins@v0.1.0
   # First-time install of any github.com/foobarto/* plugin
   # → trust prompt for fingerprint X (scope: github.com/foobarto/*)
   # → install proceeds
   stado plugin install github.com/foobarto/htb-writeups/htb-toolkit/payload-generator@v0.1.0
   # Same fingerprint, same scope: install proceeds silently
   ```
5. Twelve trust prompts collapse to one. The lock file records
   each plugin separately; reinstalls reproduce.

### §K — CLI surface delta

Adds to existing `stado plugin`:

```
plugin install <local-path-or-archive>           # existing (unchanged)
plugin install <repo>[@version]                  # NEW: remote
plugin install --from-lock                       # NEW: reproducible install
plugin install ... --autoload                    # NEW: pin to autoload
plugin install ... --build                       # NEW: opt-in source build fallback
plugin install ... --keep-active                 # NEW: install but don't switch active
plugin install ... --no-cache                    # NEW: bypass tarball cache
plugin install ... --force                       # NEW (formalised): override sha drift / version conflict

plugin update <name>|--all|--check               # NEW
plugin update-anchor <host>/<owner>              # NEW: re-fetch anchor pubkey
plugin use <repo> <version>                      # NEW: switch active version
plugin trust <repo|fpr> [--scope=...]            # extended: scope arg
plugin trust <fpr> --pubkey-file <path>          # NEW: legacy file-based trust
plugin untrust <fpr>|<owner>                     # NEW
plugin verify <name>                             # NEW: re-verify signature against trust
plugin remove <repo>[@version]                   # NEW (or extended): version-specific remove
plugin sign <dir> --key <path>                   # NEW: explicit signing step
plugin dev <dir>                                 # NEW: gen-key+build+trust+install+reload loop

plugin ls --all-versions                         # NEW flag
plugin ls --json                                 # NEW flag (consistent with tool ls)

plugin doctor <name>                             # extended: cap-vs-sandbox checks
```

In TUI: `/plugin install github.com/foo/bar@v1.0.0` always
**performs the install fully** (downloads, verifies, writes to
disk, mutates trust state, updates the user-level
`plugin-state.toml`). Codex review #18 flagged that earlier
"per-session by default" framing was incoherent — install IS
durable by definition; you can't make it session-local.

What IS per-session by default for `/plugin install`:

- Whether the newly-installed plugin's tools become **enabled
  for the current session** (so the model can call them
  immediately without restart).
- Whether the new install becomes the project's **active version
  pin** (writes to `.stado/plugin-lock.toml`).

`/plugin install <repo>@<version>` defaults: install durable,
enabled this-session, NOT pinned to project. Flags:

- `/plugin install ... --pin-project` — also write to
  `.stado/plugin-lock.toml` so the install persists across
  sessions in this project.
- `/plugin install ... --autoload` — also add to
  `[tools.autoload]` for this project (requires `--pin-project`
  too — autoload without pinning the version makes no sense).
- `/plugin install ... --no-enable` — install but don't enable
  for this session (only useful for "I'm setting up but not
  ready to use yet" workflows).

The CLI `stado plugin install` defaults are different: install
durable, enabled, AND pinned to project (commits to
`.stado/plugin-lock.toml`). Operator who runs the CLI command
intentionally is opting into team-shareable state.

## Migration / rollout

EP-0039 is additive at the plugin-install layer:

- Existing local-dir installs continue to work unchanged.
- Existing trust entries continue to work for local installs.
- Remote VCS install is new; first use per owner triggers the
  TOFU anchor prompt.
- Lock file appears on first remote install per project; existing
  projects without a lock file aren't affected unless they install
  remotely.
- The install-dir layout migrates from flat `<name>-<version>/`
  (collision-prone — codex review #3) to canonical-identity-keyed
  `<host>/<owner>/<repo>/<version>/` (per §F). Existing flat-
  layout installs are migrated transparently on first run after
  EP-0039 lands; identity is reconstructed from the manifest's
  signed metadata + lock file when available, or the operator
  is prompted to confirm the canonical identity for orphan
  installs.
- Active-version state moves from a global `<name>.active` symlink
  to per-project `.stado/plugin-lock.toml` + per-user
  `~/.config/stado/plugin-state.toml` (per §F). Existing global
  symlinks are replaced by user-level state on migration.
- Quality-pass items (sha drift, `--pubkey-file`, `update`, etc.)
  are isolated changes to existing CLI commands.

The pre-1.0 stance per the EP conversation: temporary instability
during EP-0037/0038/0039 rollout is acceptable; the user is the
sole operator and can adapt to format changes between versions.

## Failure modes

- **Anchor fetch fails on first install.** Stado errors with
  the URL it couldn't reach + suggested resolutions: check
  network, check whether the owner's anchor repo exists, install
  from local dir as fallback. No silent install.
- **Anchor pubkey malformed (not 64 hex chars).** Refused at
  parse time; `invalid ed25519 pubkey at <path>`.
- **Manifest signature verifies but author fingerprint not
  trusted.** Trust prompt appears (TOFU). Operator declines:
  install cancelled, no plugin installed.
- **wasm_sha256 doesn't match manifest.** Refused; treats as
  potential tampering. Same failure mode as today's local install.
- **Floating tag in install identifier.** Refused at parse time
  with the helpful error message in §B.
- **Install network egress refused by sandbox.** Per §H, fails
  fast with explicit guidance.
- **Lock file installation fails because the `source = "release"`
  isn't available anymore.** `plugin install --from-lock` errors
  out instead of silently falling back to `dist/` or build.
  Operator either updates lock or does a fresh `plugin install
  <repo>@<version>` to bump.
- **Multi-version conflict at startup.** Project pins
  `[plugins.fs] version = "v2.0.0"` but only `v1.x` installed.
  Stado refuses to start; clear error pointing at the install
  command needed.
- **Anchor pubkey rotation while the user has uninstalled
  plugins from that owner.** `plugin update-anchor` prompts for
  the new fingerprint as expected; if the user has no plugins
  from that owner currently, the prompt still applies (any
  future install against this owner will use the new key).
- **Conflicting trust scopes.** Operator runs `plugin trust
  github.com/foo --scope=author` then later `plugin trust
  github.com/foo --scope=any`. Both entries persist; `*` (any)
  takes precedence on match (more specific scope is fine; less
  specific scope subsumes it).
- **Anchor repo repurposed.** Operator's cached pubkey points to
  fingerprint X; the anchor's current pubkey is now Y because
  the original owner deleted their account and someone
  squatted the name. `update-anchor` prompts; operator's choice.
  Cached fingerprint protects against silent compromise; loud
  prompt makes it visible.

## Test strategy

- **Identity parsing.** `<host>/<owner>/<repo>@<version>`,
  with/without subdir, all version forms (semver, sha7+,
  rejected forms).
- **Anchor fetching.** Mock GitHub raw responses; anchor present
  + valid; missing; malformed pubkey; rate-limited (429).
- **Trust state.** TOFU prompt at first install; scope
  selection; subsequent installs at same scope succeed silently;
  scope mismatch prompts widening; explicit `plugin
  trust`/`plugin untrust`; `--scope=author|repo|any` aliases.
- **Three-tier resolution.** Release present → uses release;
  release absent + dist/ present → uses dist/; both absent +
  --build → builds.
- **Lock file.** Round-trip: install + verify lock file shape;
  `--from-lock` reproduces; lock file with stale checksums
  refuses install.
- **Multi-version.** Install v1, install v2 (alongside),
  switch active, run a tool from each, remove v1, default
  `plugin remove <name>` confirms before removing all.
- **Update flow.** `update` no-op when at latest; `update`
  bumps to next semver tag; `update --check` prints without
  installing; `update --all` updates everything; sha drift
  same fingerprint warns; fingerprint change errors with
  resolution paths.
- **Anchor rotation.** Cached fingerprint X; anchor now Y;
  `update-anchor` prompts; accept → cache replaced; reject →
  cache unchanged.
- **Sandbox interaction.** `[sandbox] http_proxy` honoured;
  `[sandbox.wrap] network = "off"` fails fast; `--from-lock`
  with cached tarball succeeds offline.
- **HTB toolkit migration scenario.** Twelve plugins under one
  owner; first install triggers one prompt; all twelve
  install with one TOFU; lock file lists twelve entries.
- **Plugin dev cycle.** `plugin dev .` runs the gen+build+trust+
  install+reload chain; subsequent `plugin dev .` reuses key
  from previous run.
- **Installing from a local directory** continues to work
  exactly as before (regression coverage for the unchanged
  path).

## Open questions

- **Should the anchor support a key list (multiple keys, tagged)
  for key rotation grace periods?** Old key + new key both valid
  for N days during rotation. Position: not v1; one key, hard
  rotation. Operators in foundation contexts may want this; add
  in v2 if pressure surfaces.
- **Should anchor-as-index publish a JSON-LD or sigstore-bundle
  shape rather than TOML?** Aligns better with broader supply-chain
  tooling. Position: TOML for v1 (consistent with stado's
  config files); revisit when sigstore integration is added per
  EP-0006.
- **Plugin proxies / mirrors at v2.** `[plugins] mirror = "..."`
  config reserved. Open question: signed mirror manifests
  (proxy signs that "I serve the canonical artefact for
  github.com/foo/bar@v1.0.0")? Position: defer until concrete
  consumer.
- **Vanity import paths at v2.** Go-style `<meta>` discovery.
  Open question: how to verify the vanity host's pubkey
  serving — does it host its own anchor at
  `<vanity-host>/<owner>/stado-plugins/`, or is it the redirected
  upstream host's anchor that's authoritative? Position: latter
  (vanity host is just an alias; the canonical signing is by
  the actual repo's owner). Decided when v2 is drafted.
- **Plugin uninstall and cache cleanup.** `plugin remove`
  removes the install dir but doesn't touch the tarball cache.
  Open question: prune cached tarballs for removed plugins
  automatically, or leave to operator? Position: leave to
  operator; `plugin cache clear` future command. Cache is
  bounded in size by plugin count + version count, neither
  large.
- **Owner deletion / repository transfer.** GitHub user
  deletes account + someone else takes the name. Operator's
  cached pubkey is X; anchor at the same URL now serves Y.
  `update-anchor` catches this on next operation. Open
  question: should stado also detect the URL → new-account
  transition via API? Position: no — same TOFU model applies.
  Cached fingerprint is the trust anchor, not the URL.

## Decision log

### D1. Identity = URL + version, Go-modules style

- **Decided:** plugin identifier is `<host>/<owner>/<repo>[/<plugin-subdir>]@<version>`.
- **Alternatives:** name-based identifiers with a registry lookup
  (mirroring npm / cargo / pypi); free-form URL with no
  expected structure; opaque hash-based identifiers.
- **Why:** Go modules is the closest precedent — VCS-based,
  no central registry, version pinning. The URL form is
  self-documenting (operator can visit it in a browser),
  scales to multiple hosts naturally, and the prefix-matching
  parse is simple. EP-0006 §"Non-goals" prohibits a central
  registry; this design preserves that while enabling remote
  install.

### D2. Strict versioning: no floating tags

- **Decided:** only semver tags and full commit SHAs accepted.
  `latest`, `main`, `HEAD`, branch names rejected at parse
  time.
- **Alternatives:** allow floating tags with a warning; require
  semver only (refuse SHAs); require SHAs only (refuse semver).
- **Why:** floating tags break the signing chain — a signed
  `latest` is whatever the last-pushed-with-this-tag is. Sha
  pins are sometimes needed (pre-release work, custom builds);
  allowing both keeps the surface complete without losing the
  signing-as-permanent property. The error message for floating
  tags points operators at `plugin update --check` for the
  newest-version use case.

### D3. Anchor-of-trust pattern, not per-repo pubkey

- **Decided:** each owner publishes a single
  `<host>/<owner>/stado-plugins/.stado/author.pub`. Plugin
  repos do not carry author.pub.
- **Alternatives:** per-repo author.pub (today's de-facto
  pattern); per-organisation Sigstore identity; central trust
  registry.
- **Why:** Homebrew-tap analogue — one place per owner. Trust
  scales linearly with owners, not with repos. Operator's
  TOFU prompt happens once per owner; HTB toolkit (12
  plugins) becomes one prompt instead of twelve. Author key
  rotation is one commit. Sigstore is heavier than pre-1.0
  warrants; future work.

### D4. Single signing entity per repo

- **Decided:** plugin repos have one signer. Manifest's
  `author:` field is display-only; the signature is the
  authority.
- **Alternatives:** multi-author signing schemas (TUF / Notary);
  per-tool authorship within a plugin.
- **Why:** simplicity. Multi-author signing introduces threshold
  schemes, key revocation per individual contributor, etc.
  Pre-1.0 doesn't need that. Organisations route plugin
  contributions through PRs to a release-signing identity.
  Display-only `author:` field accommodates "this plugin's
  human author is X, but the signing entity is Y" cases
  (foundation-published plugins by individual contributors).

### D5. Trust = author fingerprint with scope, not per-repo

- **Decided:** TOFU prompts on first install per owner; scope
  defaults to `<owner>/*`. Persists across versions and
  repos within the trusted scope.
- **Alternatives:** per-repo TOFU; per-version TOFU; default
  scope is `*` (max permissive).
- **Why:** scope choice gives operators control over breadth.
  `<owner>/*` is the natural default (most operators trust
  authors as people, not repo-by-repo). `*` is for foundations
  with shared keys. Per-repo is the conservative fallback.
  Prompt-once-per-owner is the operator-friendly behaviour.

### D6. Three-tier artefact resolution

- **Decided:** GitHub release → `dist/` in tree → source build.
  `--build` flag opts into source build explicitly.
- **Alternatives:** source build only (operator builds always);
  release only (no fallback).
- **Why:** different authors have different release habits. Some
  publish releases with prebuilt assets (best UX for casual
  operators); some commit `dist/` artefacts to the repo (don't
  want to manage releases); some only have source. Tiered
  fallback covers all three. `--build` opt-in for tier 3
  prevents accidental long-running builds.

### D7. Multi-version coexistence; one active per name per session

- **Decided:** install side-by-side at `<name>-<version>/`;
  `.active` symlink tracks active. Project pins via
  `[plugins.<name>] version = "..."`.
- **Alternatives:** allow multiple active versions
  simultaneously (different tool-name prefixes per version);
  one-version-only per name.
- **Why:** rollback (real operational need), A/B during dev
  (real authoring need), per-project pinning (real team
  coordination need). Multiple-active would surface as
  multiple tools with the same wire form (collision per
  EP-0037). One-only loses the rollback / A/B properties.

### D8. Mismatched fingerprint = loud refusal, three resolutions

- **Decided:** when a new install/update has a fingerprint
  different from the trusted scope's expected fingerprint,
  refuse with three explicit resolutions: `update-anchor`,
  pin older version, force-trust new key.
- **Alternatives:** silent acceptance (treat as new key);
  prompt operator interactively; refuse without resolution
  paths.
- **Why:** silent acceptance is the worst case (compromise looks
  like normal update). Loud refusal with resolutions makes the
  operator decide explicitly. Three resolutions cover the three
  legitimate scenarios (rotation, distrust, out-of-band trust).

### D9. Lock file mirrors go.sum semantics

- **Decided:** `.stado/plugin-lock.toml` per project; records
  version, sha256, fingerprint, source tier; `--from-lock`
  reproduces exactly.
- **Alternatives:** no lock file (every install resolves at
  install time); per-user lock file only; lock file shared
  via a separate file per plugin.
- **Why:** reproducibility across machines is a real team-
  coordination need. `go.sum` is the closest precedent; per-
  project lock file commits with the codebase, travels with
  the repo. Single TOML file matches the rest of stado's
  config style.

### D10. Anchor as optional plugin index (reserved)

- **Decided:** anchor repo MAY publish `index.toml` listing
  the owner's plugins. Reserved schema, optional, used by
  `plugin update --check` and future `plugin search`.
- **Alternatives:** require index.toml (every author maintains
  one); separate index repo / API; no index at all.
- **Why:** opt-in keeps the bar low for plugin authors (just
  publish a pubkey, nothing else needed). Operators that want
  discoverability use `plugin search` (future) which queries
  the index when present. No index = repo + tag list still
  visible via git tags / GitHub API; just less rich.

### D11. Quality pass: bundled into EP-0039, not separate PR

- **Decided:** sha drift detection, `plugin trust --pubkey-file`,
  `plugin update`, `plugin dev`, `plugin sign`, `plugin
  install --autoload`, doctor extensions all ship in EP-0039.
- **Alternatives:** separate small PR for the quality items;
  spread across EP-0037 / EP-0038 / EP-0039 by topic.
- **Why:** they're all plugin-CLI surface adjustments and
  share the same testing harness. Consolidating reduces
  review surface and eases the migration story (one EP for
  "the plugin CLI as it stands after EP-0039 lands").

### D12. Identity-keyed install layout, per-project active version

- **Decided:** install dir is `<host>/<owner>/<repo>/<version>/`
  (canonical identity), not `<name>-<version>/`. Active-version
  state lives per-project in `.stado/plugin-lock.toml` plus per-
  user fallback in `~/.config/stado/plugin-state.toml`. The
  global `<name>.active` symlink is gone.
- **Alternatives:** keep the manifest-name-based flat layout
  (the original draft); per-user-only active state; per-system
  global symlink (the original draft).
- **Why:** codex review #3 / gemini review #6 surfaced two
  collision problems: two repos both declaring `name = "fs"`
  collide on disk, and a global symlink can't differ between
  Project A pinning v1 and Project B pinning v2. Identity-keyed
  layout removes the on-disk collision; per-project state
  removes the symlink-fight. Per-user fallback handles the
  no-project case.

### D13. Full 40-char SHA in lock + tag-move detection

- **Decided:** lock file always records the full 40-char commit
  SHA. CLI accepts 7+ char abbreviations but resolves and
  records the full form. Stado refuses install when a tag's
  resolved SHA differs from the lock's recorded SHA, with three
  resolution paths (`--accept-tag-rewrite`, pin to previous
  SHA, walk away).
- **Alternatives:** 7-char SHAs in lock (codex review #14:
  ambiguity grows with repo size); trust the tag without SHA
  comparison (codex review #15: silent acceptance of force-
  moved tags); refuse to install short SHAs at all (loses CLI
  ergonomics).
- **Why:** the trust-by-author model relies on "every install
  identifier names exactly one signed artefact, forever";
  short SHAs break uniqueness over time, and force-moved tags
  break the property entirely without lock-side enforcement.
  Recording the full SHA + checking it on every install closes
  both holes; the CLI ergonomics of accepting short forms is
  preserved by resolving them at install time.

### D14. Source build mode A vs mode B, no silent bridge

- **Decided:** `--build` operates in two modes, never mixed.
  Mode A: deterministic build whose output sha256 must match a
  committed signed manifest, build runs in a sandbox. Mode B:
  dev-only, via `stado plugin dev`, with locally-generated key.
  No `--without-signed-manifest` escape hatch.
- **Alternatives:** trust the build output (the original
  ambiguity codex review #16 flagged); refuse source build
  entirely (loses development workflows); require sigstore
  for build provenance (heavyweight for v1).
- **Why:** the trust model and the build model conflict if
  silent: who signed the wasm just produced? Mode A keeps the
  author's signing chain — the build is just a way to recover
  bytes the author already signed. Mode B is honest about
  local dev — operator's local key, dev-pinned scope. The
  two modes are explicit; no in-between.

### D15. Out-of-band first-install verification mechanisms reserved

- **Decided:** signed DNS TXT verification (optional v1 if
  straightforward), GitHub-identity verification (reserved for
  future EP), out-of-band fingerprint cards (documented v1).
  TOFU-without-OOB-verification remains the operator-default
  path; the EP names alternative mechanisms so the threat model
  is on the record.
- **Alternatives:** require OOB verification on every first
  install (heavyweight, blocks first install from any new
  owner); ignore the threat (gemini review #8 correctly
  surfaced it).
- **Why:** TOFU is the same threat model as `ssh` first-connect
  and is acceptable for the consciously-trusting operator.
  OOB mechanisms close the gap for operators who want
  more; documenting them keeps the threat-model honest
  without forcing the heavyweight path on everyone.

### D16. Monorepo subdir tag convention

- **Decided:** monorepo plugins use `<plugin-subdir>/<version>`
  tag form (`htb-toolkit/gtfobins/v0.1.0`). Stado prefers
  subdir-prefixed tags, falls back to repo-wide tags for
  single-plugin repos. Lock file records the actual tag name
  resolved.
- **Alternatives:** require lockstep versioning across a
  monorepo (the original draft; codex review #17 flagged
  this is unworkable for the HTB toolkit's 12 plugins);
  separate-tag-namespace via dedicated tags repo (heavyweight);
  tarball-only releases (gemini review #17: works but loses
  source-of-truth tag structure).
- **Why:** real monorepos (HTB toolkit, kubernetes-style
  bundled-plugin orgs) need per-plugin versioning. The subdir-
  prefixed convention preserves the canonical-identity path
  shape while allowing independent release cadence. Single-
  plugin repos remain unaffected (no subdir layer).

### D17. TUI install: durable side-effects, session-flip enable, opt-in pinning

- **Decided:** `/plugin install` always installs durably (downloads,
  verifies, mutates trust + plugin store). Default per-session
  effects: enabled this session, NOT pinned to project.
  `--pin-project`, `--autoload`, `--no-enable` flags toggle the
  pinning + visibility behaviour. CLI `stado plugin install`
  defaults to pin-project.
- **Alternatives:** session-only-everything (codex review #18:
  incoherent — install IS durable); always-pin-on-install (CLI
  defaults today; bad TUI UX since trying things is the point);
  prompt every time.
- **Why:** install side-effects are durable by definition; only
  visibility/enablement and project-pinning can be session-
  scoped. CLI vs TUI defaults reflect intent: CLI = team-
  shareable durable state; TUI = experimentation with explicit
  opt-in to persistence.

## Related

### Predecessors

- **EP-0006** (Signed WASM Plugin Runtime) — extended.
  EP-0039 builds on EP-0006's signed-manifest verification
  and trust-store model. EP-0006 §"Non-goals" #1 ("automatic
  plugin discovery or central registry") is amended: VCS-based
  remote install is added; central registry remains out of
  scope. EP-0006 D1 (signed manifests + explicit trust pinning)
  unchanged. EP-0006 frontmatter gains `extended-by: [39]`.
- **EP-0012** (Release Integrity and Distribution) — see-also.
  Different domain: EP-0012 is binary release integrity with
  cosign + minisign; EP-0039 is plugin distribution with
  per-author anchors. The two systems coexist; the binary's
  trust root and the plugin author's trust root are
  independent.
- **EP-0035** (Project-local `.stado/` directory) — extended.
  EP-0039 adds `.stado/plugin-lock.toml` per-project.
  EP-0035's discovery walk is unchanged; the lock file lives
  alongside `config.toml` and `AGENTS.md`.

### Companion EPs

- **EP-0037** (Tool dispatch + naming + categories) — independent.
  EP-0039 references EP-0037's `[plugins.<name>]` config schema
  for per-plugin version pinning.
- **EP-0038** (ABI v2 + bundled wasm + runtime) — independent.
  EP-0039 references EP-0038's `[sandbox]` for install-time
  network constraints. The bundled wasm plugins shipped by
  EP-0038 are NOT subject to EP-0039 (they're embedded, not
  remotely installed); this EP only governs user-installed
  external plugins.

### External references

- Go modules — `<host>/<owner>/<repo>[/<subdir>]@<version>` is
  the closest precedent for plugin identity. Stado's strict-
  versioning (no floating tags) is more conservative than Go's
  pseudo-version scheme, intentional given the signing-chain
  property.
- Homebrew taps — `<owner>/<tap-repo>/Formula/<formula>.rb` is
  the analogue for the owner-publishes-anchor pattern.
  EP-0039's `<host>/<owner>/stado-plugins` mirrors that shape.
- TUF (The Update Framework) — alternative trust model with
  threshold-based signing and key roles. Considered as future
  work; rejected for v1 per simplicity.
- Sigstore — alternative trust model with keyless signing via
  OIDC + Rekor transparency log. Adjacent to EP-0006's
  optional CRL/Rekor checks; EP-0039 doesn't prescribe but
  doesn't preclude.
- Anthropic's Claude API tool schema — referenced by EP-0037
  for the tool-name regex constraint; not relevant to EP-0039.
