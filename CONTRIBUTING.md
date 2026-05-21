# Contributing to stado

Thanks for your interest. stado is pre-1.0 and iterating fast — PRs,
bug reports, and one-line corrections are all welcome.

## Build

```sh
git clone https://github.com/foobarto/stado
cd stado
make            # → ./stado (default target — wraps `go build -buildvcs=false`)
```

`make help` lists the rest: `test`, `lint` (matches CI's golangci
config), `check` (lint + test — the local pre-push gate),
`fetch-binaries` (mirrors goreleaser's before-hook), `clean`,
`install`. Plain `go build -o stado ./cmd/stado` still works if you
prefer.

Dev builds don't embed `ripgrep` / `ast-grep`; they fall back to the
PATH copies. `stado doctor` will tell you what's missing.

Release builds (the ones `goreleaser` produces) add
`-tags stado_embed_binaries` which `//go:embed` platform-specific
binaries from `bundled/`. The `hack/fetch-binaries.go` generator
runs at release time via `.goreleaser.yaml`.

## Test

```sh
go test ./...               # full suite
go test -race ./...         # CI runs this
hack/tmux-uat.sh all        # real-PTY TUI harness (16 scenarios)
cd hack/pty-bridge && \
  STADO_PTY_BRIDGE_E2E=1 STADO_BIN=$PWD/../../stado go test -v
                            # xterm.js + headless Chrome harness
```

Three layers of TUI test coverage with different cost/coverage
trade-offs:

- **`internal/tui/uat_*_test.go`** — fastest, runs in `go test ./...`.
  Drives the bubbletea Update loop directly via teatest. Catches
  message-routing and Model-state regressions, can't see ANSI
  escape codes or terminal redraws.
- **`hack/tmux-uat.sh`** — real PTY via tmux, asserts against the
  rendered pane via grep. Catches termios + cancelreader
  regressions teatest can't. Skip with `STADO_SKIP_TMUX_UAT=1` if
  tmux isn't installed.
- **`hack/pty-bridge/`** — full visual rendering through xterm.js
  in headless Chrome via `chromedp`. Catches escape-code +
  real-terminal-width layout regressions the other two can't.
  Costs a Chrome dependency and ~3-15s per scenario; opt-in via
  `STADO_PTY_BRIDGE_E2E=1`. Lives in its own go.mod so chromedp +
  gorilla/websocket stay out of the main module.

When you touch bundled wasm tools under `plugins/bundled/`, verify
both the host build and the `wasip1` build. The shared SDK at
`internal/bundledplugins/sdk/` is imported by `GOOS=wasip1` modules,
but host-side tests and linters still load the package, so the real
pointer-based implementation must stay behind `//go:build wasip1`
with a safe host stub for `!wasip1`.

The host-side registry that wires the embedded wasm into the
runtime stays at `internal/bundledplugins/` (Go's `//go:embed` only
sees siblings of the importing file). Only the wasm sources moved
to `plugins/bundled/`.

```sh
go test ./internal/bundledplugins/sdk
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared ./plugins/bundled/shell
bash plugins/bundled/build.sh   # rebuild all bundled wasm at once
```

## Lint

```sh
golangci-lint run --timeout=5m
```

CI blocks on this. Unused fields, unused imports, ineffective
assignments, and the full default `staticcheck` set all fail the
build — fix warnings before pushing.

`golangci-lint` runs against the host toolchain, so it sees the
`!wasip1` side of `internal/bundledplugins/sdk/`. If host builds start
depending on raw wasm pointer casts again, lint will fail even if the
`wasip1` module still compiles.

## External AI review (optional)

For pre-release or post-large-refactor passes, running `claude` and
`codex` CLIs as independent quality scanners catches issues each
tool misses on its own. Codex tends to find concurrency hazards
(Go-specific), Claude tends to find security/architectural ones.

```sh
# Verify both are authenticated first
claude --permission-mode bypassPermissions -p "What is 2+2?"
codex --full-auto exec "What is 2+2?"
```

The prompt:

> Review the Go project stado for overall quality. Focus, most to
> least important: 1) concurrent data races / goroutine hazards;
> 2) error handling — silent drops or unactionable wrapping;
> 3) security — plugin sandboxing / trust / secrets; 4) leaking
> abstractions and tight coupling; 5) testing gaps; 6) allocation
> hot paths; 7) UX polish. For each finding give severity
> (P1/P2/P3), file:line range, one-sentence impact, one-sentence
> fix idea. Limit ~15. Be specific.

Run:

```sh
codex --full-auto exec review "$(cat /tmp/review-prompt.txt)" > /tmp/codex-review.out
claude --permission-mode bypassPermissions -p "$(cat /tmp/review-prompt.txt)" > /tmp/claude-review.out
```

Diff the two outputs by finding; converging signals are the ones to
take seriously first.

## Run locally

```sh
./stado                            # TUI (recommended)
./stado run --prompt "hi"          # one-shot
./stado headless                   # JSON-RPC daemon on stdio
./stado acp                        # Zed Agent-Client-Protocol server
./stado mcp-server                 # expose tools as MCP v1
```

Pick a provider by setting `STADO_DEFAULTS_PROVIDER=<name>`
(anthropic / openai / google / ollama / lmstudio / etc.). stado
probes local runners automatically if nothing's pinned.

## Layout

| Directory | What's inside |
|-----------|---------------|
| `cmd/stado/` | CLI subcommands |
| `internal/runtime/` | Core agent loop — shared by TUI, run, ACP, headless |
| `internal/tui/` | bubbletea UI |
| `internal/providers/` | Anthropic / OpenAI / Google / oaicompat |
| `internal/tools/` | Bundled tools (bash/fs/grep/…) |
| `internal/plugins/` | wasm plugin runtime (wazero) + ABI |
| `internal/state/git/` | Sidecar bare repo, signed refs, turn tags |
| `internal/sandbox/` | Landlock + bwrap + seccomp + CONNECT proxy |
| `docs/` | Per-command + per-feature guides |

## Git workflow

- Branch from `main`. Short, descriptive name.
- Every commit message has a one-line subject + a paragraph body.
  The body explains **why**, not **what** — `git diff` shows the
  what. Look at `git log` for the house style.
- No force-push to `main`. Force-push your own feature branch fine.
- Sign commits if you can (cosign is already a release
  dependency). Not a hard requirement yet.

## Release versioning

stado is pre-1.0, but release numbers still communicate impact:

- Cut a minor release (`v0.N.0`) for new features or meaningful
  adjustments to existing behavior.
- Cut a patch release (`v0.N.P`) for smaller fixes, docs/process
  updates, dependency bumps, and contained internal changes.

Do not reuse an existing tag. Update `CHANGELOG.md` before tagging so
the release note exists at the tagged commit.

## Submitting a PR

1. Make sure `go test ./...`, `golangci-lint run`, and
   `hack/tmux-uat.sh all` all pass locally.
2. Update `CHANGELOG.md` under `## Unreleased` with a one-sentence
   hook + a short "why" paragraph.
3. Open the PR with a short summary + a test-plan checklist.
4. CI must go green before merge. Flaky tests should be fixed, not
   retried.

## Design principles

See [DESIGN.md](DESIGN.md) for the long form. Four commitments:

1. **The user's repo is read-only until they say otherwise.** Agent
   state lives outside. Landing is always explicit.
2. **Every action is auditable and tamper-evident.** Signed commits,
   structured trailers, Ed25519 provenance.
3. **Capabilities are declared, the OS enforces.** Not "we promise,"
   kernel-level confinement.
4. **No lossy abstraction over provider capabilities.** Thinking
   blocks and cache breakpoints round-trip verbatim.

When in doubt about a tradeoff, re-read the relevant paragraph and
keep whichever interpretation preserves more of the four.

## Reporting bugs

- Open an issue with: stado version (`stado version`), platform,
  provider being used, repro steps, and any stderr output.
- If it's a TUI-specific bug, paste the output of `stado doctor`
  and say which terminal emulator you're in (tmux / alacritty /
  kitty / iTerm / Windows Terminal / etc.) — TUI regressions
  usually hinge on terminal quirks.
- Security-sensitive reports go through the channel in
  [SECURITY.md](SECURITY.md) (publish cookbook section has the
  contact).
