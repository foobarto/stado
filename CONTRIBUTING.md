# Contributing to stado

Thanks for your interest. stado is pre-1.0 and iterating fast — PRs,
bug reports, and one-line corrections are all welcome.

## Build

```sh
git clone https://github.com/foobarto/stado
cd stado
go build -o stado ./cmd/stado
```

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
```

The tmux harness spawns `./stado` in a detached session, asserts
against the rendered pane, and catches regressions in the termios +
cancelreader path that teatest can't (the virtual terminal fakes
those layers). Skip it with `STADO_SKIP_TMUX_UAT=1` if tmux isn't
installed.

When you touch bundled wasm tools under `internal/bundledplugins/`,
verify both the host build and the `wasip1` build. The shared SDK in
`internal/bundledplugins/sdk/` is imported by `GOOS=wasip1` modules,
but host-side tests and linters still load the package, so the real
pointer-based implementation must stay behind `//go:build wasip1`
with a safe host stub for `!wasip1`.

```sh
go test ./internal/bundledplugins/sdk
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared ./internal/bundledplugins/modules/approval_demo
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
