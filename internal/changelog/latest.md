## v0.66.1 — minisign release-signing wired (dormant) — 2026-06-13

Plumbs minisign signing into the release pipeline so `stado self-update` can
verify a release once the signing secrets are provisioned. Inert until then:
with no secrets configured a release builds and publishes byte-identically to
v0.66.0. Resolves the minisign-gap flagged in the v0.66.0 docs note.

### Infra

- **Minisign release-signing wired into the pipeline (dormant-until-key).**
  `.goreleaser.yaml` embeds the verify pubkey via `-ldflags` (from
  `STADO_MINISIGN_PUBKEY_B64`), and `release.yml` signs `checksums.txt` with
  the release key and uploads `checksums.txt.minisig` (gated on
  `STADO_MINISIGN_SECKEY`). With both unset the embedded pubkey stays empty and
  the signing steps skip cleanly — the release is byte-identical to v0.66.0.
  **Arming `stado self-update` requires BOTH secrets:**
  `STADO_MINISIGN_PUBKEY_B64` so the shipped binary carries the trust root to
  verify against, *and* `STADO_MINISIGN_SECKEY` (+ `STADO_MINISIGN_PASSWORD`)
  so CI produces the `.minisig` — provisioning the signing key alone publishes
  a signature the binaries still can't check. The CI sign step forces
  prehashing (`minisign -S -H`) so the signature is the prehashed "ED"
  (BLAKE2b) algorithm stado's verifier requires. The operator ceremony (key
  generation + secret provisioning, including the offline-signing path) is
  documented in SECURITY.md. cosign keyless signing remains the always-on
  layer.

### Fixes

- **`stado verify --show-builtin-keys` no longer prints a misleading
  `keyid: 0`.** When a pubkey is pinned but no signer key-id is embedded the
  output says so explicitly (matching the JSON form, which omits it). The
  minisign key-id is a `uint64` and cannot be set by the `-X` linker flag, so
  it is intentionally not embedded today — display-only; verification does not
  use it.

