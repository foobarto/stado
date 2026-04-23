---
ep: 12
title: Release Integrity and Distribution
author: Bartosz Ptaszynski <foobarto@gmail.com>
status: Implemented
type: Standards
created: 2026-04-23
implemented-in: v0.1.0
see-also: [4, 5, 6, 11]
history:
  - date: 2026-04-23
    status: Accepted
    note: Retrofitted from the shipped reproducible-build, signed-manifest, and self-update trust model.
  - date: 2026-04-23
    status: Implemented
    version: v0.1.0
    note: Reproducible builds, checksum-manifest signing, self-update verification, and distribution surfaces are the shipped release contract.
---

# EP-12: Release Integrity and Distribution

## Problem

stado ships binaries, archives, packages, and a built-in update path. If
release integrity depends on trusting whichever raw asset was downloaded
first, operators cannot reliably distinguish a genuine release from a
tampered mirror, cache, or substitute artifact.

Distribution also has different trust environments. Some users verify
online with GitHub Actions identity and transparency logs; others need a
long-lived offline-verifiable trust root embedded in a release build or
copied into an airgapped environment. One mechanism does not satisfy
both cases cleanly.

## Goals

- Make the checksum manifest, not the asset URL, the trust anchor for
  release verification.
- Preserve reproducible-build and provenance guarantees in the release
  workflow.
- Keep self-update aligned with the same signed-manifest verification
  path as manual installs.
- Support online and offline verification without pretending they are
  the same trust model.

## Non-goals

- Turning `self-update` into a raw-asset downloader that skips manifest
  verification.
- Claiming hosted signed apt/rpm repositories are already part of the
  shipped distribution contract.
- Requiring sigstore tooling inside the runtime for every verification
  path.

## Design

- releases are verified against a signed `checksums.txt` manifest
- cosign keyless and minisign serve different trust/distribution roles
- `self-update` trusts the manifest flow, not raw assets
- `-tags airgap` strips stado-controlled outbound release/update paths
- GitHub Releases, Homebrew tap wiring, and package artifacts are the
  supported distribution surfaces, with hosted apt/rpm infra still an
  operational follow-up

The release workflow builds a reproducible artifact set and publishes a
single checksum manifest as the integrity anchor. `.github/workflows/release.yml`
documents the current contract: cross-platform binaries are produced via
goreleaser, SBOMs are attached, `checksums.txt` is signed with cosign
keyless using GitHub Actions OIDC, and SLSA provenance is emitted for
the release artifacts. Reproducibility flags and pinned timestamps are
part of that workflow, not informal release notes.

stado uses two signature systems on the checksum manifest because they
solve different problems. Cosign keyless ties the published manifest to
the GitHub release workflow identity and Rekor-backed transparency.
Minisign provides the long-lived Ed25519 trust root that can be embedded
into release binaries and carried into offline environments. The project
does not treat those as redundant copies of the same mechanism.

Manual verification and `self-update` both flow through the manifest.
Operators first verify `checksums.txt` and then verify the chosen
archive or package against that manifest. `cmd/stado/selfupdate.go`
follows the same rule: it downloads `checksums.txt`, requires
`checksums.txt.minisig` when the running build has an embedded minisign
root, verifies the manifest, and only then accepts the selected asset's
sha256. The updater does not trust release assets directly.

Airgapped builds are a deliberate release-mode variant, not a docs-only
warning. `cmd/stado/selfupdate_airgap.go` replaces the updater with a
hard refusal and points operators at the manual `download -> verify ->
copy` flow. The same build tag strips stado-controlled outbound release
and update paths while leaving user-chosen provider networking outside
this standard's scope.

Distribution claims stay narrow. GitHub Releases are live, Homebrew tap
wiring is shipped through goreleaser, and `.deb`/`.rpm` package
artifacts are produced today. Hosted signed apt/rpm repositories,
however, remain operational follow-up work owned by distribution
operators rather than a shipped v0.1.0 runtime invariant.

## Open questions

No release-integrity question blocks the shipped v0.1.0 contract. The
remaining open items are operational rollout tasks around hosted package
repositories and release ceremony management, not missing architecture
inside the runtime.

## Decision log

### D1. Trust the checksum manifest, not individual assets

- **Decided:** release verification anchors on signed `checksums.txt`.
- **Alternatives:** sign or trust raw archives and packages
  independently.
- **Why:** one signed manifest keeps manual verification, package
  checking, and self-update on the same trust path.

### D2. Keep cosign and minisign as complementary roles

- **Decided:** cosign keyless covers CI/workflow identity and minisign
  covers long-lived offline-verifiable trust roots.
- **Alternatives:** use only one scheme everywhere.
- **Why:** online provenance and offline distribution have different
  operator needs.

### D3. Make self-update enforce the manifest flow

- **Decided:** `self-update` verifies the manifest before trusting an
  asset digest and refuses updates when the embedded minisign trust path
  is unavailable for a pinned release build.
- **Alternatives:** trust the GitHub asset URL, verify raw assets only,
  or treat minisign as optional for pinned builds.
- **Why:** the updater should not bypass the same release contract the
  rest of the project documents.

### D4. Separate shipped distribution surfaces from operational follow-up

- **Decided:** GitHub Releases, Homebrew tap wiring, and emitted
  package artifacts are the shipped surfaces; hosted signed apt/rpm
  repos are deferred operational work.
- **Alternatives:** describe all package distribution infrastructure as
  already shipped.
- **Why:** the runtime and goreleaser wiring are implemented today, but
  repository hosting and signing operations are still deployment-side
  decisions.

## Related

- [EP-4: Git-Native Sessions and Audit Trail](./0004-git-native-sessions-and-audit.md)
- [EP-5: Capability-Based Sandboxing](./0005-capability-based-sandboxing.md)
- [EP-6: Signed WASM Plugin Runtime](./0006-signed-wasm-plugin-runtime.md)
- [EP-11: Observability and Telemetry](./0011-observability-and-telemetry.md)
- [README.md](../../README.md#install)
- [PLAN.md](../../PLAN.md#phase-10--release--reproducibility--)
