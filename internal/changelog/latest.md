## v0.64.1 — TUI-usability fixes (tool-panel sanitize, hooks/slash discoverability) — 2026-06-12

Fixes from a fresh TUI-usability UAT pass on v0.64.0 (#128). The headline is a
terminal-escape sanitization gap; the rest smooth the newer hooks / slash /
config surfaces.

### Security

- **Tool-output panel now sanitizes terminal escapes.** The collapsible tool
  panel rendered the tool **result, name, and args** (and `/tool` / `/plugin:`
  side-channel output) verbatim, while assistant/thinking text was already
  sanitized. A tool/model emitting crafted bytes could rewrite the terminal
  title (OSC 0), inject a clickable hyperlink to an attacker URL (OSC 8), or
  ring the bell (BEL). All of these display paths now route through
  `textutil.SanitizeForTerminal` at store-time (the executor still receives the
  raw arguments — only the rendered copies are scrubbed).

### TUI

- **Lifecycle hooks are now visible.** `stado config show` renders the
  `[hooks]` section (and `[tui.sidebar]`/`[tui.footer]`); `stado doctor`
  reports configured lifecycle hooks instead of `(unset)`; `stado config init`
  documents the sidebar/footer sections and the deny/mutate/fail_closed hook
  config (replacing the stale "notification-only" text).
- **Broken lifecycle hooks now surface a warning.** A hook that fails to load
  is no longer silently dropped: the skip-warning is emitted before the
  provider build (so a first run without an API key still sees it) and is shown
  in the TUI startup notices instead of being swallowed by the alt-screen.
- **Slash-command discoverability.** Nine working commands (`/stats`, `/ps`,
  `/config`, `/sandbox`, `/fleet`, `/kill`, `/spawn`, `/cancel`, `/supervisor`)
  were invisible in `/help`, the `Ctrl+P` palette, and the inline `/` popup;
  they are now registered. The popup also no longer misdirects a prefix to a
  different command (`/stat` now offers `/stats`, `/ps` offers `/ps`). `/tool`
  output lands in the collapsible panel instead of flooding scrollback.
- **Status bar no longer overflows.** A long provider error wrapped several
  rows past the frame border; it is now flattened and ellipsized to the
  measured width so the bar stays a single row.

### Dependencies

- Bumped the `go` directive to **1.26.4** (clears the two Go stdlib advisories),
  patch-swept the module tree (incl. **goldmark 1.7.17**, clearing an XSS
  advisory across goldmark / glamour / x/tools / goldmark-emoji), and forced
  **golang-jwt/jwt v5.3.1** via `replace` (past the v5.2.2 fix for CVE-2025-30204,
  a JWT amplification issue pinned transitively by openai-go). `govulncheck`
  reports zero reachable vulnerabilities. The remaining Snyk-flagged transitive
  advisories (aws-sdk-go-v2 via unused Bedrock support; go-git, whose fix is the
  v6 major migration) are unreachable and left for a follow-up.

