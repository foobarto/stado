## v0.66.1 — minisign release-signing wired (dormant) — 2026-06-13

Plumbs minisign signing into the release pipeline so `stado self-update` can
verify a release once a signing key is provisioned. Inert until then: with no
key configured a release builds and publishes byte-identically to v0.66.0.
Resolves the minisign-gap flagged in the v0.66.0 docs note.

### Infra

- **Minisign release-signing wired into the pipeline (dormant-until-key).**
  `.goreleaser.yaml` injects the embedded verify pubkey via `-ldflags`, and
  `release.yml` signs `checksums.txt` with the release key and uploads
  `checksums.txt.minisig` — both gated on the signing key being present
  (`STADO_MINISIGN_SECKEY`). With the key unset the steps skip cleanly and the
  release is unchanged; provisioning the key arms minisign verification for
  `stado self-update`. The CI sign step forces prehashing (`minisign -S -H`)
  so the signature is the prehashed "ED" (BLAKE2b) algorithm stado's verifier
  requires. The operator ceremony (key generation + secret provisioning) is
  documented in SECURITY.md. cosign keyless signing remains the always-on
  layer.

### Fixes

- **`stado verify --show-builtin-keys` no longer prints a misleading
  `keyid: 0`.** When a pubkey is pinned but no signer key-id is embedded the
  output says so explicitly (matching the JSON form, which omits it). The
  minisign key-id is a `uint64` and cannot be set by the `-X` linker flag, so
  it is intentionally not embedded today — display-only; verification does not
  use it.

