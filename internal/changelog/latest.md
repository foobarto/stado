## v0.79.0 — bounded orchestration and feature showcase — 2026-08-12

### Runtime

- **Native bounded orchestration guidance.** Added guidance across TUI, CLI,
  headless, ACP, and spawned-agent turns. Strong unreviewed mechanical signals
  encourage candidate-only `stado learn`/`/learn`; fixed request shapes
  recommend isolated memory/session research; active children and unread
  mailboxes trigger coordination reminders. Guidance is capability-aware,
  payload-free, capped, and repeated signal shapes aggregate during a bounded
  cooldown to prevent learning bloat without hiding later recurrence.

### Docs

- **Impact-focused feature reel.** Added it to the README and static landing page,
  highlighting isolated research, retained subagents, durable mailboxes,
  adaptive retrieval, trajectory learning, session signals, the durable
  broker, Verify Work command gates, Lua lifecycle hooks, and native bounded
  orchestration with links to their EPs.
- **Navigable EP relationships.** Replaced bare numeric
  `requires`, `extends`, `supersedes`, `superseded-by`, `extended-by`, and
  `see-also` values with canonical labels and validated rendered links across
  the full EP catalogue.
- **Repository-wide documentation audit.** Added coverage for previously
  unindexed operator commands, reconciled slash and lean-core guides, documented
  the v0.78 adaptive-context architecture in `DESIGN.md`, repaired moved example
  links, and completed `implemented-in` metadata for older implemented EPs.

### Infra

- **Dependency and Actions refresh.** Consolidated Dependabot updates across the Go dependency graph and pinned
  GitHub Actions. Notable upgrades include Anthropic SDK 1.62.0, MCP Go 0.57.0,
  OpenTelemetry 1.45.0, go-git 5.19.2, Google API 0.292.0, gRPC 1.83.0,
  checkout 7.0.1, setup-go 7.0.0, CodeQL 4.37.3, and Scorecard 2.4.4.

