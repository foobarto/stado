## v0.80.2 — clean release build metadata (2026-08-15)

### Infra / docs

- **Clean release build state.** Keep GoReleaser's generated root `dist/`
  directory ignored and guard that invariant so `-buildvcs=true` records an
  otherwise clean release checkout as `modified: false`.
- **Sandbox documentation corrected.** Replace the obsolete opt-in-wrapper
  description with the default-on Linux executor policy used across TUI, run,
  headless, ACP, and MCP surfaces, including the honest enforcement-availability
  and defense-in-depth fallbacks and the exact scope of `--no-sandbox`. The
  legacy `[sandbox]` wrapper remains an optional additional outer process
  wrapper; it is not the primary tool-execution boundary.
- **Release metadata advanced.** Update the pinned install example and tagged
  homepage markers to v0.80.2 without rewriting either published v0.80.x tag.
- **Published release verified.** The SSH-agent-signed `v0.80.2` tag resolves
  to commit `51bba58e306b39c51cf6af7b34be76322e647dd2`. All eleven downloaded
  release assets match GitHub's published digests, all eight payloads match the
  checksum manifest, and Cosign verifies that manifest against the exact
  tagged Release workflow identity and transparency log. Independent GitHub
  attestation verification binds all eight payloads to the tag, commit,
  workflow, and GitHub-hosted runner. The static amd64 binary reports stado
  0.80.2, Go 1.26.6, the exact release commit, and `modified: false`. Both
  downloaded SBOMs are valid SPDX JSON, and the tagged homepage validator is
  green. Minisign remained deliberately unprovisioned, so no `.minisig` asset
  was emitted.

Release evidence SHA-256:

- `checksums.txt`: `a6311210b6bb2fbbdea6be747d2793e08e878552eca887e3fa7edba69240e78d`
- `checksums.txt.cert`: `29044a06ff818fe66ccf1c0c8f1d284d114222b1a0bd04e3c3fd283952765d1d`
- `checksums.txt.sig`: `78b6663d51650aad4d18c054322d5ba251bb2a3e4c64993ccea1d77070de8f18`
- amd64 archive: `f0be942e2f8fdca59a4c5111e4e2eeac6f85d9b9fdb8800f587b921fee2d2c60`
- arm64 archive: `5dccf5174472294ad24748d67d10a101b20a580e1d92830a340b74bd9fd2f332`

