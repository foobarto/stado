## v0.70.0 — plugin secret isolation + anchor trust-on-first-use — 2026-06-14

Two plugin-security gaps from the EP-vs-codebase audit, closed: plugin secrets
are now isolated per plugin identity, and remote plugin installs verify the
owner's anchor key (bound to the manifest signature) on first use.

### Security

- **Plugin secrets are namespaced by plugin identity (EP-0038 D19).** The
  secrets store was a single flat keyspace, so one plugin could read,
  overwrite, or delete another's secret of the same name whenever both held a
  matching `secrets:read|write` glob. Plugin-written secrets now live in a
  per-plugin scope; reads fall back to the operator-provisioned shared keyspace
  (so an operator-set API key stays readable by any granted plugin, and secrets
  written by older versions keep working), and a plugin can no longer overwrite
  or delete an operator secret. The `stado secrets` CLI is unchanged.

- **Remote plugin install verifies the owner anchor on first use (EP-0039).**
  `stado plugin install <host/owner/repo@ver>` now checks the owner's anchor
  pubkey and binds it to the manifest:
  - the manifest must be signed by the owner's anchor key (the anchor
    fingerprint must equal the manifest's signer) — a manifest signed by some
    other globally-trusted key is refused;
  - first sight of an owner prompts (or `--trust-anchor` to accept
    non-interactively; refuses on a non-TTY) and records the fingerprint;
  - a changed fingerprint refuses (key rotation / compromise), and the new
    `stado plugin untrust-anchor <host/owner>` clears the pin after an expected
    rotation;
  - a first-time install whose anchor can't be fetched (404 / network) fails
    closed — cached owners are unaffected.

  Previously the downloaded anchor key was discarded and the trust flow never
  ran.

### Docs

- Swept EP/DESIGN doc-drift surfaced by the audit: the `NoneRunner` warning
  comment, the cache-event / `MaxParallelToolCalls` emission claims in
  DESIGN.md, the airgap `self-update` verify hint (it pointed at
  `stado verify <artifact>`, which takes no artifact arg), and the EP-0030 /
  EP-0033 status banners.

