## v0.71.0 — plugin update/remove + proc-read timeout + dep bumps — 2026-06-14

Closes the remaining actionable plugin-CLI gaps from the post-audit survey,
plus a host-import fix and routine security-adjacent dependency updates.

### Plugins

- **`stado plugin update` actually updates.** The latest-tag lookup was a stub
  that returned the current version, so `update` never did anything. It now
  queries the GitHub (`/releases/latest`) and GitLab (`/releases/permalink/latest`)
  release APIs; unsupported hosts report a clear error instead of silently
  no-op'ing. (Fixing the stub also exposed and fixed a latent bug: the update
  installed via a child command's `Execute()`, which runs the *root* command —
  the TUI — so it would never have installed; it now calls the install path
  directly.)
- **New `stado plugin remove <name>`.** Uninstalls every installed version of a
  plugin and drops its `plugin-lock.toml` entry so `plugin update` won't
  resurrect it. The name is validated against path traversal / glob injection.
- **`stado_proc_read` honors `timeout_ms`.** The per-read timeout was accepted
  but ignored, so a plugin polling a quiet subprocess blocked the call
  indefinitely; it now bounds the read with a deadline on the stdout pipe.

### Infra

- Dependency bumps: `anthropic-sdk-go` 1.46.0 → 1.50.1; `golang.org/x`
  crypto/net/mod/sys/sync/term/text/tools (incl. security-adjacent x/crypto +
  x/net fixes). `govulncheck` clean.
- Added 36 regression tests pinning the security boundaries hardened in
  v0.68.0–v0.70.0: FS-tool symlink confinement, broker ceiling narrow-only
  invariant, `TrustVerified` TOFU ladder, revocation-list exact membership, and
  per-plugin secrets host isolation.

