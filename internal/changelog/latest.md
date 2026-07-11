## v0.77.0 — verified completion and brokered subagents — 2026-07-10

### TUI / CLI

- **Command verification can gate model completion.** Configure ordered
  `[verify].commands` in user config, pass repeatable `stado run --verify`
  flags, or control the configured gate in the TUI with `/verify`. Failed
  commands return bounded feedback to the model; passing commands accept the
  turn; exhaustion exits distinctly as `verify_exhausted`. Headless and ACP
  clients receive verification status updates. Project config cannot enable
  this operator-controlled execution surface.
- **Verification is recoverable and work-preserving.** Partial assistant work
  and the final verification critique are persisted before strict
  infrastructure failures or retry exhaustion return. Queued TUI input is
  restored on failure, and operator cancellation leaves completion explicitly
  unverified instead of silently accepting it.

### Runtime

- **Ordinary subagents now receive broker-owned child sessions.** Children are
  constrained to broker-managed worktrees, inherit the parent's effective
  profile, and can only narrow masks, timeouts, sockets, filesystem policy,
  and sandbox capabilities. Ordinary children do not receive SSH agent access;
  the elevated git-child path is intentionally deferred to issue #238.
- **Broker context taint now follows tool results.** Prompts begin clean and
  become tainted after tool output enters model context across autonomous and
  TUI loops, so subsequent broker policy evaluation can distinguish trusted
  operator input from derived context.

### Observability

- **Live telemetry matches the documented instrument set.** Tool latency now
  covers the full invocation with tool and outcome attributes, including
  denials and errors. Turn token/cache usage is recorded by provider, model,
  and direction, and declaration-only metrics were removed rather than
  advertised without data.

### Security

- **Broker ceilings now apply consistently across execution surfaces.** TUI,
  `stado run`, `stado run --headless`, ACP, and `mcp-server` all derive one
  executor sandbox decision from the broker session. Long-lived servers apply
  it to every executor they create, and TUI session switches preserve it.
  The global `--no-sandbox` flag now selects `NoneRunner` and removes the
  autonomous host's default sandbox policy on every surface instead of only
  changing the broker announcement. WASM process imports use that same runner,
  and execution CWDs are symlink-resolved against the ceiling before
  bubblewrap mounts them; a CWD is mounted read-only unless `FSWrite` permits
  it.
- **Lua lifecycle hooks no longer expose filesystem module loaders.** The safe
  hook VM removes `dofile`, `loadfile`, `module`, and `require` after loading
  its limited standard libraries. EP-0051 now defines the supported lifecycle
  surface, failure posture, audit behavior, and current runtime boundaries.

### Dependencies

- **Go 1.26.5 is now the minimum and release toolchain.** This picks up the
  standard-library fixes for GO-2026-5856 and GO-2026-4970; both had reachable
  call paths under the previous 1.26.4 build.
- Folded in Dependabot's six Go updates: Bubbles 2.1.1, Bubble Tea 2.0.8,
  Lip Gloss 2.0.5, Anthropic SDK 1.56.0, MCP Go 0.55.1, and Google API
  0.287.0.
- Folded in Dependabot's eight pinned GitHub Actions updates, including
  checkout 7.0.0, setup-go 6.5.0, golangci-lint-action 9.3.0, CodeQL 4.36.2,
  GoReleaser action 7.2.3, and build provenance attestation 4.1.1.

### Fixes

- **Bundled one-shot shell tools execute again.** `shell.exec`, `shell.sh`,
  `shell.bash`, and `shell.zsh` now authorize the absolute shell path their
  wasm wrapper actually launches. The previous basename-only capability was
  correctly rejected for path-containing argv, leaving every one-shot shell
  call unusable. The broker mount table also retains `/bin` and `/sbin` when
  composing its ceiling with the host process policy.
- **Bundled shell failures now remain failures across the WASM ABI.** Non-zero
  host exit codes produce tool errors with captured output, and the wrapper's
  error writer returns the required negative length instead of accidentally
  turning error JSON into a successful result. Verification commands therefore
  cannot pass merely because the shell process exited non-zero.

