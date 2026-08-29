# stado Security Policy and Model

## Reporting a vulnerability

Do not report exploitable behavior in a public issue or pull request.

**Preferred:** use GitHub's
[private vulnerability reporting](https://github.com/foobarto/stado/security/advisories/new)
to open a confidential security advisory.

**Backup:** email `bartosz@foobarto.me` with `[stado-security]` in the subject.
PGP is strongly preferred for sensitive material; the fingerprint is
`F858 BE96 A0D1 E6FD CAF7 C4D6 06FD 46A0 2874 AF8D` and the public key is at
<https://foobarto.me/.well-known/openpgpkey/foobarto.me.asc>.

Include the affected release or commit, a minimal reproduction, concrete
impact, and relevant host/kernel details. We aim to acknowledge reports within
72 hours and coordinate disclosure on a 90-day default, or sooner after a fix
ships. The account-wide rules of engagement and safe-harbor statement are in
the [default security policy](https://github.com/foobarto/.github/blob/main/SECURITY.md).

Security fixes target the latest published release and the default branch.
Older releases receive fixes only when a project advisory says so.

---

## Security model

stado implements three layers of supply-chain protection; the first two are
active for v0.80.x, while the third is provisioned separately:

1. **Reproducible builds** — `-trimpath -buildvcs=true -buildid=` with a
   pinned `mod_timestamp` produce bit-for-bit identical binaries from
   the same source tree. Independent rebuilders can confirm published
   releases weren't tampered with.
2. **Cosign keyless signing** — the checksum manifest covering every release
   artifact is signed by a GitHub Actions OIDC-issued certificate via Fulcio,
   with the signature and certificate uploaded alongside it. Verifiable with
   `cosign verify-blob`, with a Rekor transparency-log entry.
3. **Minisign Ed25519 signing (not yet provisioned)** — once a long-lived
   project key is provisioned, `checksums.txt` is additionally signed and the
   corresponding public key is compiled into release builds. That makes strict
   offline `stado self-update` possible and lets `stado verify
   --show-builtin-keys` expose the embedded root. v0.80.x currently ships
   without a Minisign asset or embedded root.

This document covers the operational procedures for the **minisign**
half. Cosign keyless is fully automated via GitHub Actions and has no
human-in-the-loop.

For runtime trust boundaries (sandboxing, plugin capabilities, and
untrusted repository content), see
[docs/security/threatmodel.md](docs/security/threatmodel.md) and
[docs/commands/config.md](docs/commands/config.md) (project overlay strip-list).

---

## Repository configuration trust

A cloned repository may ship `.stado/config.toml`, `.stado/personas/`,
and `.stado/plugins/`. Stado treats repo contents as **untrusted** (EP-0044):

- Operator-domain keys in project config (`[hooks]`, `[keymap]`,
  `[plugins].background`, `[mcp.servers]`, `[sandbox]`, …) are **stripped**
  before merge. Set them in user config (`~/.config/stado/config.toml`).
- Project personas and project-local plugin autoload are **off by default**.
  Enable only for repos you trust:
  - `[defaults] allow_project_persona = true`
  - `[plugins] allow_project_plugins = true`
- Those opt-in flags cannot be set from project config (also stripped).

Plugin trust (signer pinning, CRL, Rekor) is separate: even with
`allow_project_plugins`, wasm still must verify against the global trust
store.

---

## Minisign key ceremony

### Generating the master keypair

Run **once** on an airgapped machine. The private key must never touch
an online host again.

```sh
# Requires the reference minisign tool (https://jedisct1.github.io/minisign/)
# — available via apt/brew/cargo/zig install. Any Ed25519 minisign key
# works with stado's verifier; the tool is just a key-management
# convenience.
minisign -G -p stado.pub -s stado.key
```

Store `stado.key` on encrypted offline media (hardware token, encrypted
USB, paper backup). The password prompted during `-G` is the only
protection on the key file itself — pick a real passphrase.

`stado.pub` is the file distributors read. Its trailing base64 line is
**not** the bare key — it decodes to a 42-byte blob laid out as
`signature_algorithm(2) || key_id(8, little-endian) || ed25519_pubkey(32)`.
stado's verifier (`internal/audit/minisign.go` + `cmd/stado/selfupdate.go`)
pins the **raw 32-byte key, base64-encoded**. The embedding step below
strips the 10-byte `alg||key_id` prefix and re-base64s the inner 32
bytes; feeding the 42-byte minisign line in directly is the most common
wiring mistake — the verifier rejects it as "embedded minisign pubkey
malformed".

### Embedding the pubkey into release builds

stado reads the pinned pubkey from `audit.EmbeddedMinisignPubkey`.
It is empty by default, so local/dev builds do not carry a release
trust root and `stado self-update` refuses to run. The v0.80.x releases
also leave it empty because no project Minisign key has been provisioned.
Once that key exists, release builds seed it via `-ldflags`.

Derive the pubkey from `stado.pub`. The trailing base64 line decodes
to `alg(2) || key_id(8, little-endian) || pubkey(32)`; take the inner 32
bytes for the pubkey (the key-id, bytes 2..10, is computed below for
reference only — it is not embedded today; see the note after the snippet):

```sh
# PUBKEY_B64 = base64 of the RAW 32-byte Ed25519 key (bytes 10..42 of
#              the decoded .pub line). This is what the verifier pins —
#              NOT the 42-byte minisign line.
# KEYID      = the 64-bit signer id as a DECIMAL uint64 (bytes 2..10,
#              little-endian). Reference only — NOT embedded today (see the
#              note below); a future --show-builtin-keys would surface it.
read -r PUBKEY_B64 KEYID < <(python3 - "$(tail -n 1 stado.pub)" <<'PY'
import base64, struct, sys
blob = base64.b64decode(sys.argv[1])           # 42 bytes: alg||key_id||key
key_id = struct.unpack('<Q', blob[2:10])[0]    # little-endian uint64
raw32  = blob[10:42]                           # raw Ed25519 public key
print(base64.standard_b64encode(raw32).decode(), key_id)
PY
)

go build \
  -ldflags "\
    -X github.com/foobarto/stado/internal/audit.EmbeddedMinisignPubkey=$PUBKEY_B64 \
  " \
  -o stado ./cmd/stado
```

The signer key-id is **not** embedded: `-X` only sets `string` variables and
`audit.EmbeddedMinisignKeyID` is a `uint64` (injecting it link-errors with
"not a var of type string"). It is display-only — verification does not use
it today — so it stays `0`; surfacing it via `--show-builtin-keys` is a
deferred follow-up (a `string`-shim + `strconv.ParseUint` in Go). The
`$KEYID` derived above is unused for now.

For goreleaser-driven releases this `-X` fragment already lives in
`.goreleaser.yaml`'s `builds[].ldflags`, guarded so an unset env still
compiles. Provision the raw-32 base64 as the CI secret
`STADO_MINISIGN_PUBKEY_B64` — never check it into git.

### Signing a release

Once the Minisign key is provisioned, each tagged release must additionally
sign `checksums.txt`. There are two ways to produce
`checksums.txt.minisig`, and the release pipeline supports both. **Pick one** —
they are mutually exclusive per release (two signatures over the same manifest
would collide on the asset name).

**Path B-online — key in a CI secret (lowest-friction, wired today).**
`.github/workflows/release.yml` carries a dormant "Minisign-sign
checksums" step that runs only when the secret `STADO_MINISIGN_SECKEY`
is present (see *Provisioning the CI secrets* below). When set, CI
installs minisign, signs `dist/checksums.txt`, and uploads the
`.minisig`. The key lives in GitHub Actions secrets — convenient, but
the private key touches an online runner. Use this if the threat model
accepts a hot signing key (most projects pre-1.0).

**Path B-offline — airgapped key (highest assurance).** Leave
`STADO_MINISIGN_SECKEY` unset (CI step stays dormant) and sign out of
band:

```sh
# 1. Let goreleaser / CI produce checksums.txt in the usual way.
# 2. Download/transfer checksums.txt to the airgapped machine.
# 3. Sign it (minisign >= 0.10 prehashes by default → the "ED" format
#    stado's verifier requires; do NOT pass -l/legacy):
minisign -Sm checksums.txt -s stado.key -t "stado <version> signed $(date -u +%Y-%m-%dT%H:%M:%SZ)"
# → produces checksums.txt.minisig alongside.
# 4. Upload the .minisig as a release asset:
gh release upload <tag> checksums.txt.minisig --clobber
```

The private key never leaves the airgapped host. This is the posture the
"airgap-safe by construction" claim above is really about.

> **Trade-off in one line:** B-online trusts the CI runner with a live
> signing key (convenient, hot key); B-offline keeps the key airgapped
> and pays a manual sneakernet+upload step per release (slower, cold
> key). The embedded-pubkey verify path is identical either way.

Either way, `stado self-update` looks for `checksums.txt.minisig` in the
release's assets and requires it alongside an embedded minisign pubkey
in the running binary. Missing either side of that pair is a hard
failure.

### Provisioning the CI secrets (for B-online)

Only needed for Path B-online. From the derived values above plus the
key file, set three GitHub Actions repository secrets:

| Secret | Value |
|---|---|
| `STADO_MINISIGN_PUBKEY_B64` | base64 of the raw 32-byte pubkey (see *Embedding the pubkey*) — embedded via ldflags so self-update can verify offline |
| `STADO_MINISIGN_SECKEY` | the full contents of `stado.key` (the encrypted minisign secret-key file, comment line included). **Presence of this secret is what arms the CI signing step.** |
| `STADO_MINISIGN_PASSWORD` | the passphrase chosen during `minisign -G` (CI feeds it to minisign on stdin) |

With none of these set — today's state — the release builds and
publishes exactly as before: the embedded pubkey stays `""`, and the
minisign signing/upload steps skip. Provisioning the pubkey alone
(without `STADO_MINISIGN_SECKEY`) embeds the trust root but leaves signing
to the offline path; that's a valid B-offline configuration.

### Ordered operator ceremony (summary)

1. `minisign -G -p stado.pub -s stado.key` on an airgapped host; store
   `stado.key` on encrypted offline media; remember the passphrase.
2. Derive `PUBKEY_B64` (raw-32 base64) from `stado.pub` with the snippet
   under *Embedding the pubkey*. (The key-id is not embedded today — see
   that section's note — so deriving it is optional.)
3. Set GH secret `STADO_MINISIGN_PUBKEY_B64` (both paths need it — it
   embeds the verify root).
4. Choose the signing path:
   - **B-online:** also set `STADO_MINISIGN_SECKEY` + `STADO_MINISIGN_PASSWORD`. CI signs + uploads automatically.
   - **B-offline:** leave those two unset; sign `checksums.txt` on the airgapped host and `gh release upload <tag> checksums.txt.minisig` per release.
5. Cut a release. Confirm a `checksums.txt.minisig` asset exists and that
   `stado self-update` reports "minisign verified" against the published
   binary.

### Verifying a release (end users)

Normally invisible — `stado self-update` runs the check automatically.
Manual verification:

```sh
stado verify --show-builtin-keys          # prints the embedded fingerprint
minisign -Vm checksums.txt -p stado.pub   # verifies with the standalone tool
```

`stado verify` does not verify individual assets. Manual asset checks
stay on the published manifest path: verify `checksums.txt` first, then
confirm the chosen archive/package digest against that manifest.

### Key rotation plan

If the private key is compromised:

1. **Immediately** publish a CRL-style advisory in the releases feed
   ("key X revoked as of YYYY-MM-DD — do not trust signatures after
   this date").
2. Generate a new keypair via the ceremony above.
3. Cut a new release built with the new pubkey embedded. Announce the
   new fingerprint in release notes.
4. End users upgrading past that version get the new embedded pubkey
   and refuse signatures from the old one.

stado doesn't ship a runtime minisign-key-trust-list — the embedded
key is singular and immutable per binary. Rotation is a binary-rebuild
event, not a config change. This is a deliberate tradeoff: simpler
verification path, harder key rotation. For projects that need
on-the-fly rotation, cosign's Fulcio path is the alternative (that's
also signed unconditionally).

---

## Plugin signing

Third-party plugins follow the same Ed25519 pattern at a different
scope. See [EP-0006](docs/eps/0006-signed-wasm-plugin-runtime.md)
for the manifest + trust-store + CRL + Rekor layers. Summary:

- Plugin authors generate their own keypair (`stado plugin gen-key`).
- Users pin author pubkeys on first install (`stado plugin trust`).
- Install-time verification checks signature + wasm sha256 + rollback
  + optional CRL + optional Rekor inclusion proof.
- Revocation happens via the CRL (operated by the project) and Rekor
  (public transparency log).

Plugins are a separate trust domain from the stado binary itself;
compromising a plugin signing key doesn't affect release-signing
integrity.

### Plugin-publish cookbook (for third-party maintainers)

Step-by-step for maintainers who want to publish an offline-signed
plugin. Assumes you already have working `plugin.wasm` +
`plugin.manifest.json` templates — see the `hello` plugin in
[foobarto/stado-plugins](https://github.com/foobarto/stado-plugins) for a
minimal starting point and `plugins/bundled/auto-compact/` for the
full session-capable shape.

#### 1. Generate a signing key (one-time per maintainer identity)

```sh
# On an airgapped or otherwise-trusted machine:
stado plugin gen-key plugin-signer.seed

# → prints:
#   pubkey (hex):   <64 hex chars>
#   fingerprint:    <short fpr>
#   seed written:   plugin-signer.seed (chmod 0600 — keep offline)
```

- Treat the `.seed` file like any other private key: offline storage,
  no backups to cloud drives, `chmod 0600`.
- The fingerprint is short enough to print on a business card; users
  will verify the pubkey-hex matches the fingerprint on first install.
- One key per *maintainer identity*, not per plugin — the same key
  can sign every plugin you ship.

#### 2. Publish the pubkey + fingerprint

Distribute **via a channel outside your plugin-distribution channel** so
a compromise of one doesn't take down the other. Good options:

- Your project's homepage (HTTPS, not just GitHub Pages on a custom domain)
- A DNS TXT record under your domain
- A transparency-log service (sigstore, etc.)
- Print on conference swag

The users' pinning step (`stado plugin trust <pubkey> "<comment>"`) is
a one-time trust decision; make it easy for them to verify.

#### 3. Fill in manifest metadata

In `plugin.manifest.json` fill in every field *before* signing:

```json
{
  "name":             "my-plugin",
  "version":          "0.3.1",
  "author":           "alice@example.com",
  "capabilities":     ["session:read", "provider:invoke:50000"],
  "tools":            [ /* ... */ ],
  "min_stado_version": "0.9.0",
  "timestamp_utc":    "2026-04-20T10:15:00Z",
  "nonce":            "<random hex — openssl rand -hex 16>"
}
```

`wasm_sha256` + `author_pubkey_fpr` are filled automatically by
`stado plugin sign` — leave them empty in the template. Bump `version`
for every release; stado's rollback guard rejects installs that go
backwards. `nonce` prevents replay of old signed manifests under the
same version.

#### 4. Sign the manifest

```sh
stado plugin sign plugin.manifest.json --key plugin-signer.seed --wasm plugin.wasm

# Produces:
#   plugin.manifest.json   (with wasm_sha256 + author_pubkey_fpr filled in)
#   plugin.manifest.sig    (base64 Ed25519 signature)
```

Both files must ship side-by-side — the install verifier reads the
`.sig` from the same directory as `.json`.

#### 5. (Optional) Upload to Rekor for public verifiability

```sh
# One-time: point stado at the public Rekor instance.
# (Or run your own — Rekor is Apache-2-licensed.)
echo '[plugins]
rekor_url = "https://rekor.sigstore.dev"' >> ~/.config/stado/config.toml

# Rekor upload happens automatically during `stado plugin verify`
# when rekor_url is set AND the manifest has no prior entry. Users
# who pass through `stado plugin install` see the entry UUID printed
# to stderr; absence is advisory (the trust store is still the
# authoritative gate).
```

Uploading is a unilateral action — once logged, the entry is
append-only. Do it before distributing so users' `verify` calls find
an entry instead of advising "no log entry".

#### 6. Distribute the plugin directory

Ship everything in a `<plugin>/dist/` shape (as in
[foobarto/stado-plugins](https://github.com/foobarto/stado-plugins)):

```
my-plugin/
├── plugin.wasm
├── plugin.manifest.json       # signed
├── plugin.manifest.sig        # signature
└── README.md                  # usage + capability explanation
```

A tarball, a git tag, a GitHub release — any medium works. The verifier
doesn't care about transport, only that the four files land together.

#### 7. Revocation (only if the key is compromised)

Contact the stado project to add your key to the CRL — the CRL is
operated by the project and signed by a separate key pinned in
`[plugins].crl_issuer_pubkey`. **Do not rotate silently:** users who
installed under the old key need to see a revocation event, not just
a new plugin version with a new signer.

After revocation:

1. Generate a fresh key (back to step 1).
2. Publish the new pubkey + rotation-event notice via the same channel
   as step 2.
3. Re-sign + re-distribute every still-supported plugin version.
4. Users re-run `stado plugin trust <new-pubkey>` + re-install.

##### Built-in deny-list (project-managed)

stado ships a hardcoded deny-list of Ed25519 *fingerprints* whose
corresponding private seeds were committed to this repo's git history
before the `.seed` gitignore landed (the seeds were untracked in v0.51.1,
but history retains them forever). Any clone or mirror has the seeds, so
anyone can forge a manifest signature matching these fingerprints.

Every trust-verification entry point consults `IsRevoked()`
(`internal/plugins/revoked.go`) and refuses to verify a manifest under a
revoked fingerprint **even if the operator has trusted it** via
`stado plugin trust`. The covered paths are `(*TrustStore).VerifyManifest`
(standard verify), `(*TrustStore).TrustVerified` (TOFU/pin), and
`internal/runtime.verifyPluginOverride` (runtime override / installed-plugin
path). All return the same `plugins.RevokedError` so behavior is uniform.
This is a hard deny — there is no escape hatch. If you add a new
verification entry point, it MUST consult `IsRevoked` too, or this
guarantee is silently lost.

The currently-revoked fingerprints (each maps to a leaked demo-seed file
preserved in git history; the list may grow over time as more keys are
revoked — see `internal/plugins/revoked.go` for the live set):

| Fingerprint | Leaked seed |
|---|---|
| `6c48b56f20c9c344` | `plugins/examples/browser/browser-demo.seed` |
| `65eae6fb74279268` | `plugins/examples/encode-zig/encode-zig-demo.seed` |
| `5bc3855d455e44c4` | `plugins/examples/hello/hello-demo.seed` |
| `08aa1288d1af3d9a` | `plugins/examples/hello-go/hello-go-demo.seed` |
| `28f0fa4d25503211` | `plugins/examples/http-session/http-session-demo.seed` |
| `6c9bf7180872f90c` | `plugins/examples/image-info/image-info-demo.seed` |
| `effd536ec1e7eb14` | `plugins/examples/ls/ls-demo.seed` |
| `f701ee55897ada64` | `plugins/examples/mcp-client/mcp-client-demo.seed` |
| `45016a163a795f9f` | `plugins/examples/persistent-shell/persistent-shell-demo.seed` |
| `ff8436c9d0ab8450` | `plugins/examples/state-dir-info/state-dir-info-demo.seed` |
| `33ecd5793539691c` | `plugins/examples/webfetch-cached/webfetch-cached-demo.seed` |
| `a3128a188d7af698` | `plugins/examples/web-search/web-search-demo.seed` |

The corresponding plugins moved to
[`foobarto/stado-plugins`](https://github.com/foobarto/stado-plugins) under
a *new* anchor key (fingerprint `57a3e58ce484c5e5`); the demo seeds above
are not used by anything stado ships today. The deny-list is a
belt-and-suspenders defense against an older install that pinned one of
these keys, or an attacker forging a plugin under one of them.

To remediate as a plugin maintainer who lost a seed: same flow as the
project-managed CRL above — generate a fresh key, publish a rotation
notice, re-sign, re-distribute. The deny-list refusal *cannot* be
overridden from the operator side; the only path forward is a new key.

#### 8. Rotating without compromise (hygiene)

Annual rotation is good practice even without an incident. Same flow
as revocation minus the CRL step: publish a *rotation notice*, sign
future releases with the new key, leave the old key's
already-published signatures valid (they're still verifiable — the
CRL's absence is what matters).

#### 9. Testing the publish flow locally

Before pushing to users, round-trip a fresh install against your own
trust store:

```sh
stado plugin gen-key test.seed                                          # step 1
stado plugin sign plugin.manifest.json --key test.seed --wasm plugin.wasm  # step 4
stado plugin trust $(grep 'pubkey' test-output | awk '{print $3}') "test"  # step 2
stado plugin verify .                                                    # should pass
stado plugin install .                                                   # should install
stado tool run hello '{}'                                               # smoke-test
```

A green round-trip here means end users will also succeed, assuming
they've pinned your real pubkey.

---

## Host sandbox

stado derives a Linux host-default executor policy across TUI, `stado run`,
`stado run --headless`, ACP, and `mcp-server`. Process and PTY imports that do
not supply a guest policy receive that default, with the launch cwd and
temporary directories as the intended writable filesystem scope. A
broker-attached session projects an additional process ceiling; the guest
policy and broker ceiling can narrow the host default but cannot widen it.

Policy selection alone does not prove that every kernel mechanism is active:

- WASM process and PTY imports use bubblewrap when it is available and fail
  rather than silently run direct when their requested policy has no enforcing
  Linux runner. The lower-level `sandbox.Detect()` abstraction itself falls
  back to `NoneRunner`; caller paths that use that generic runner, including
  stdio MCP subprocess launch, are not namespace-confined when bubblewrap is
  unavailable.
- The seccomp deny-list is defense in depth on the normal bubblewrap paths. It
  is skipped for host-allowlist networking through `pasta`, and a filter setup
  failure emits a warning and continues under bubblewrap without seccomp.
- Bubblewrap subprocesses enter through a narrow trampoline that applies the
  effective Landlock filesystem policy inside the completed mount namespace
  before exec. Unsupported kernels emit a warning and retain bubblewrap alone;
  a rule failure on a kernel that passed the support probe fails closed. The
  `pasta` host-allowlist path also warns and skips the helper because portable
  inherited-descriptor delivery is not established. Direct `stado run`
  additionally applies Landlock to the parent stado process.

Use `stado doctor` to inspect the runner and Landlock availability on the
actual host. Treat `runner: none`, a seccomp warning, or a Landlock warning as
a real reduction in containment, not as equivalent to the full Linux posture.

`--no-sandbox` is the explicit operator opt-out from the host-default WASM
process/PTY policy on every top-level surface; it also skips Landlock where
that layer is used. It does not disable the separately capability-derived
runner for configured stdio MCP servers, which still uses `sandbox.Detect()`.
`STADO_BROKER_ATTACH=0` only skips the broker-projected ceiling; it does not
disable the local host-default sandbox.

The older `[sandbox] mode = "wrap"` configuration remains available as an
optional additional outer wrapper around stado itself. Its bwrap/firejail
re-exec path and `WarnIfHostUnsandboxed` notices describe that outer-wrapper
layer, not whether model-invoked subprocesses receive the default executor
policy. Do not use `mode = "off"` or `STADO_SUPPRESS_SANDBOX_WARN=1` as a
substitute for `--no-sandbox`; those settings only control the legacy wrapper
and its notice.

The default broker profile binds the operator's home directory read-only while
masking private SSH key material. Stado does not forward `$SSH_AUTH_SOCK` or
offer a generic host-socket bind capability; sandboxed process environments
drop SSH-agent variables, and the autonomous process profile does not mount
broad `/run`. Its `/tmp` and `/var/tmp` are private tmpfs mounts, so host IPC
and credential sockets in scratch directories are not inherited.
Git-over-SSH credentials must be provided outside Stado's sandbox authority,
preferably as short-lived, narrowly scoped credentials. The exact capability
and containment boundaries are maintained in
[the threat model](docs/security/threatmodel.md) and
[sandboxing documentation](docs/features/sandboxing.md).

Linux is the only supported platform now and through v1. Darwin and Windows
carry no current build, runtime, or containment promise
([EP-0065](docs/eps/0065-linux-only-platform-scope.md)).
