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
  - date: 2026-04-24
    status: Accepted
    note: Documented release-numbering policy: minor for features or meaningful behavior changes, patch for smaller changes.
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

The repo and runtime define the release contract around a signed
checksum manifest, reproducible artifacts, and offline-verifiable trust
roots when a release is prepared with the full ceremony. The checked-in
automation in `.github/workflows/release.yml` already covers the
reproducible goreleaser build matrix, SBOM attachment, cosign keyless
signing of `checksums.txt`, and SLSA provenance emission. Those are
concrete properties of the current GitHub Actions path, not informal
release notes.

stado uses two signature systems on the checksum manifest because they
solve different problems. Cosign keyless ties the published manifest to
the GitHub release workflow identity and Rekor-backed transparency.
Minisign provides the long-lived Ed25519 trust root that the repo and
runtime know how to verify and that release builds can embed for offline
verification. The project does not treat those as redundant copies of
the same mechanism.

The minisign half is supported and expected by the release contract, but
it is not fully created by the checked-in GitHub Actions path alone.
The offline minisign signing ceremony and ldflags seeding of
`EmbeddedMinisignPubkey` and `EmbeddedMinisignKeyID` remain
operator-supplied release-process steps. That distinction matters:
the repo contains the verification logic and the release workflow for
the cosign-backed path today, while the minisign root publication and
binary pinning still depend on how a given release is cut.

Manual verification and `self-update` both flow through the manifest.
Operators first verify `checksums.txt` and then verify the chosen
archive or package against that manifest. `cmd/stado/selfupdate.go`
follows the same rule: it downloads `checksums.txt`, requires an
embedded minisign pubkey in the running build, requires a published
`checksums.txt.minisig` alongside the release, verifies that manifest,
and only then accepts the selected asset's sha256. The updater does not
trust release assets directly and does not fall back to raw-asset or
unsigned-manifest verification.

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

Version bumps are chosen by impact, even while the project remains
pre-1.0. New features and meaningful adjustments to existing behavior
cut a minor release (`v0.N.0`). Smaller fixes, docs/process changes,
dependency bumps, and contained internal changes cut a patch release
(`v0.N.P`). Existing tags are immutable; the changelog entry must exist
at the tagged commit.

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
  asset digest and refuses updates unless the running binary has an
  embedded minisign pubkey and the release publishes
  `checksums.txt.minisig`.
- **Alternatives:** trust the GitHub asset URL, verify raw assets only,
  or treat minisign as optional for self-update.
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

### D5. Use impact-based pre-1.0 version bumps

- **Decided:** minor releases carry new features or meaningful behavior
  adjustments; patch releases carry smaller changes.
- **Alternatives:** always increment patch, or pick versions ad hoc per
  release.
- **Why:** users need version numbers to signal behavioral impact, and
  pre-1.0 does not remove the need for predictable release semantics.

## Related

- [EP-4: Git-Native Sessions and Audit Trail](./0004-git-native-sessions-and-audit.md)
- [EP-5: Capability-Based Sandboxing](./0005-capability-based-sandboxing.md)
- [EP-6: Signed WASM Plugin Runtime](./0006-signed-wasm-plugin-runtime.md)
- [EP-11: Observability and Telemetry](./0011-observability-and-telemetry.md)
- [README.md](../../README.md#install)
- [PLAN.md](../../PLAN.md#phase-10--release--reproducibility--)
