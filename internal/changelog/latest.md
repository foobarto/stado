## v0.80.1 — release metadata correction (2026-08-15)

### Docs / release evidence

- **Release metadata corrected.** Correct the immutable-homepage release
  markers missed by v0.80.0, update the pinned install example, and record the
  published state of the official offline-key-signed `supervise/v0.1.1`
  application.
- **Official supervise build corrected.** Record and correct the terminal
  release-audit finding in the initial `supervise/v0.1.0` package: its Go
  1.26.6 build retained the absolute GOROOT.
  Corrective v0.1.1 uses trimpath, an empty build ID, and a pinned toolchain;
  two distinct GOROOT locations now produce byte-identical WASM. Fresh release
  bytes passed signature, digest, isolated install, and installed-package
  verification against the official owner anchor.
- **Release integrity documented as shipped.** Make the release-integrity
  wording match the actual artifacts: v0.80.x ships a Cosign-signed checksum
  manifest, SBOMs, and GitHub provenance. Minisign is wired but inactive until
  its release key is provisioned, so these releases do not claim a `.minisig`
  asset or embedded root.
- **Terminal audit repaid; connector review debt retained.** Complete the
  terminal implementation-versus-EP/source audit for this patch while carrying
  the bypassed final connector review forward explicitly. The metadata patch
  does not retroactively claim that pre-v0.80.0 review ran.
- **Race gate restored.** Remove an unused unsynchronized write from the shared
  Fleet test spawner after the release-gate race suite exercised two
  generation-scoped spawns concurrently. Production Fleet accesses were not
  part of the conflicting pair.
- **Published release verified.** The SSH-agent-signed `v0.80.1` tag resolves to
  commit `1d77b3bd5527ec78b206fc0bfeb9b59c8c1e1216`. All eleven downloaded
  release assets match GitHub's published digests, all eight payloads match the
  checksum manifest, and Cosign verifies that manifest. The static amd64 binary
  reports stado 0.80.1 and Go 1.26.6. Independent GitHub attestation
  verification binds all eight payloads to the tag, commit, Release workflow,
  and transparency-log entry. The tagged homepage validator is green.
- **Release-build dirty marker explained and prevented.** The immutable
  v0.80.1 binary embeds the exact commit but reports `modified: true` because
  GoReleaser writes unignored `dist/` metadata before invoking `go build`.
  Ignore that generated tree and guard the rule so the next release can prove
  `modified: false`; this is build-state metadata, not a source, checksum,
  signature, or provenance mismatch.

Release evidence SHA-256:

- `checksums.txt`: `73ade54a61da7196609bef0b47ec9548e9fe87f3d7bc47f5ed655898e21f57ba`
- `checksums.txt.cert`: `40762fad7d649c02187bd5c227d1551f209a98f0482c82eb38c7a9ad307fea81`
- `checksums.txt.sig`: `54625d046ad5d03b66628485ceb7ad879b77221084e1049c4ac71036c4ad2776`
- amd64 archive: `be3be9c2b6b0f84eebac7d84b682d5a506ee8ef3b2a1a622e9015ebc7b94ea14`
- arm64 archive: `4959a990d22bbb6431b77b6512dfa16beef123a4b6e1d538324bb65f15bf1fb1`

