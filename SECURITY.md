# stado Security Model

stado ships three layers of supply-chain protection:

1. **Reproducible builds** — `-trimpath -buildvcs=true -buildid=` with a
   pinned `mod_timestamp` produce bit-for-bit identical binaries from
   the same source tree. Independent rebuilders can confirm published
   releases weren't tampered with.
2. **Cosign keyless signing** — every release asset is signed by a
   GitHub Actions OIDC-issued certificate via Fulcio, with the
   signature + cert uploaded alongside the artefact. Verifiable with
   `cosign verify-blob`. Implicit Rekor transparency-log entry.
3. **Minisign Ed25519 signing** — `checksums.txt` additionally signed
   with a long-lived project key, offline-held. The corresponding
   public key is compiled into every stado binary, so
   `stado self-update` and `stado verify` can check signatures without
   reaching out to Fulcio / Rekor. Airgap-safe by construction.

This document covers the operational procedures for the **minisign**
half. Cosign keyless is fully automated via GitHub Actions and has no
human-in-the-loop.

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
the 32-byte Ed25519 public key encoded as minisign expects.

### Embedding the pubkey into release builds

stado reads the pinned pubkey from `audit.EmbeddedMinisignPubkey`
(empty by default — dev builds skip verification with an advisory).
Release builds seed it via `-ldflags`:

```sh
# Extract the raw base64 from stado.pub (skip the comment line):
PUBKEY=$(tail -n 1 stado.pub)

# Seed both the pubkey and the key id. Key id is the 64-bit signer id
# that minisign embeds in each signature — lets stado reject signatures
# from the wrong signer even if someone substitutes a different key.
KEYID=$(head -c 10 stado.pub | tail -c 8 | xxd -p -c 8)   # simplified

go build \
  -ldflags "\
    -X github.com/foobarto/stado/internal/audit.EmbeddedMinisignPubkey=$PUBKEY \
    -X github.com/foobarto/stado/internal/audit.EmbeddedMinisignKeyID=$KEYID \
  " \
  -o stado ./cmd/stado
```

For goreleaser-driven releases, put these `-X` fragments in
`.goreleaser.yaml`'s `builds[].ldflags` (the values come from
repository secrets / CI variables, never checked into git).

### Signing a release

On every tagged release, sign `checksums.txt` with the offline key:

```sh
# 1. Let goreleaser / CI produce checksums.txt in the usual way.
# 2. Transfer checksums.txt to the airgapped machine (sneakernet).
# 3. Sign it:
minisign -Sm checksums.txt -s stado.key -t "stado <version> signed $(date -u +%Y-%m-%dT%H:%M:%SZ)"
# → produces checksums.txt.minisig alongside.
# 4. Transfer the .minisig back and upload as a release asset.
```

`stado self-update` looks for `checksums.txt.minisig` in the release's
assets; when present AND the running binary has a pinned pubkey, the
signature is enforced. Missing one side of the pair is advisory.

### Verifying a release (end users)

Normally invisible — `stado self-update` runs the check automatically.
Manual verification:

```sh
stado verify --show-builtin-keys          # prints the embedded fingerprint
minisign -Vm checksums.txt -p stado.pub   # verifies with the standalone tool
```

`stado verify <artefact>` (single-file form) also works when you've
downloaded assets by hand and want to confirm the digest + signature
before installing.

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
scope. See [PLAN §7](PLAN.md#phase-7--wasm-plugin-runtime--signed-manifest--v1)
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
`plugin.manifest.json` templates — see `examples/plugins/hello/` for a
minimal starting point and `examples/plugins/auto-compact/` for the
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
  "capabilities":     ["session:read", "llm:invoke:50000"],
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

Ship everything in `examples/plugins/<name>/` shape:

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
stado plugin run my-plugin-0.3.1 hello '{}'                              # smoke-test
```

A green round-trip here means end users will also succeed, assuming
they've pinned your real pubkey.

---

## Reporting a vulnerability

Open a GitHub security advisory on
`github.com/foobarto/stado/security/advisories`. Please don't open a
public issue for anything that looks exploitable.

We aim to acknowledge reports within 72 hours.
