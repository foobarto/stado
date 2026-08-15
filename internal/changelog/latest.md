## v0.80.2 — clean release build metadata (2026-08-15)

### Infra / docs

- **Clean release build state.** Keep GoReleaser's generated root `dist/`
  directory ignored and guard that invariant so `-buildvcs=true` records an
  otherwise clean release checkout as `modified: false`.
- **Sandbox documentation corrected.** Replace the obsolete opt-in-wrapper
  description in `SECURITY.md` with the default-on Linux executor policy used
  across TUI, run, headless, ACP, and MCP surfaces. The legacy `[sandbox]`
  wrapper remains an optional additional outer process wrapper; it is not the
  primary tool-execution boundary.
- **Release metadata advanced.** Update the pinned install example and tagged
  homepage markers to v0.80.2 without rewriting either published v0.80.x tag.

