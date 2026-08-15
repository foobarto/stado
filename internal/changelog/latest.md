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

