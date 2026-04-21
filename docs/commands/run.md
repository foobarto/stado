# `stado run`

Non-interactive: pipe a prompt through the agent loop and print the
result. Used for scripting, CI integrations, one-shot reviews, and
batch processing.

## What it does

Given `--prompt "..."`:

1. Builds the provider using the same resolution path as the TUI
   (config default → local probe → `STADO_DEFAULTS_PROVIDER` env).
2. Loads any project-level instructions (`AGENTS.md` / `CLAUDE.md`)
   walking up from cwd, same as the TUI.
3. Constructs a single-user-message `agent.TurnRequest`, calls
   `StreamTurn`, streams text-deltas to stdout.
4. If `--tools` is enabled: opens a session worktree (sidecar-backed,
   signed refs) so tool calls are auditable. Runs multiple turns
   until the model stops requesting tools or hits `--max-turns`.

Exit codes:
- `0` success
- `1` provider / IO error
- `2` max-turns reached OR cost-cap exceeded (see `[budget]`)

## Why it exists

Three orthogonal use cases share this surface:

1. **Scripting**: `result=$(stado run --prompt "extract the regex from X")`
   lets bash pipe LLM output into anything. No config required for
   local runners; the zero-arg `stado run` falls back to whatever
   local endpoint is alive.

2. **CI**: GitHub Actions / GitLab CI can run `stado run --session
   <id> --prompt "…"` to continue a long-running review session
   across pipeline stages. The session persists in the sidecar repo;
   subsequent invocations replay the conversation.

3. **Batch review**: `stado github install` writes a workflow that
   runs `stado run` on `@stado`-prefixed PR comments. The result
   gets posted back via `gh api`.

The TUI is the primary user interface. `stado run` is the mechanical
sibling — same core runtime, no terminal UI, stdout-friendly.

## How to use it

### Minimum invocation

```sh
stado run --prompt "summarise the last 10 commits"
```

Streams raw text to stdout. Exits 0 on success.

### Enabling tools

```sh
stado run --tools --prompt "find every TODO in this repo"
```

With `--tools`, the model can call `read` / `grep` / `ripgrep` /
`bash` / `webfetch` / `read_with_context` / `ast_grep` / `edit` /
`write` / `glob` / LSP-backed symbol tools. Each call lands in the
session's audit log.

Tool execution uses the auto-approve host — there's no interactive
y/n in run mode. Scope it via `[tools]` in `config.toml` if that's
too broad.

### Continue a prior session

```sh
stado run --session abc123 --prompt "what was that refactor we discussed?"
```

Loads `priorMsgs` from the session's persisted conversation, appends
the new user message, streams the response, and persists the
exchange so the next `--session abc123` call sees the full history.
Lookup accepts uuid / uuid-prefix (≥8 chars) / description substring.

### Structured output

```sh
stado run --json --prompt "list each function in main.go"
```

Emits JSON lines (one per event: text delta, tool call, tool result,
final usage). Useful for jq piping + CI gating.

### From a reusable skill

```sh
stado run --skill review --prompt "the current diff"
```

`--skill review` resolves `.stado/skills/review.md` from cwd, uses
its body as the prompt. `--prompt` appends so you can layer a
per-invocation ask on top. Unknown skill → actionable error listing
what's available.

## Flags

| Flag | Meaning |
|------|---------|
| `--prompt <text>` | The prompt text (or pass positionally) |
| `--skill <name>` | Load `.stado/skills/<name>.md` as (part of) the prompt |
| `--tools` | Enable tool-calling with git-native audit |
| `--sandbox-fs` | Landlock: writes confined to worktree + `/tmp` |
| `--session <id-or-label>` | Continue an existing session |
| `--max-turns N` | Cap turns (default 20) |
| `--json` | Emit JSON Lines instead of raw text |

## Config

Relevant `config.toml` sections:

- `[defaults]` — `provider`, `model`.
- `[agent].thinking` / `thinking_budget_tokens` — extended-thinking
  on providers that support it.
- `[budget].hard_usd` — hard cost cap; crossing exits 2 with
  `ErrCostCapExceeded`.
- `[tools].enabled` / `[tools].disabled` — trim the bundled tool
  set.
- `[hooks].post_turn` — shell hook on turn completion (runs in
  `stado run` too, not just the TUI).

`stado config show` prints the resolved effective config.

## Gotchas

- **`--tools` opens a session each invocation** unless `--session` is
  passed. They accumulate. `session gc --apply` periodically.
- **`--sandbox-fs` is Linux only.** macOS + Windows run unsandboxed
  regardless of the flag (Windows v2 sandbox is deferred).
- **Streaming to pipes is line-buffered.** When redirecting, tokens
  may appear in chunks. Use `--json` for deterministic event
  boundaries.
- **Hard cap check is turn-boundary.** A single very long turn can
  overshoot the cap — the loop checks after the turn completes.
- **AGENTS.md loading is cwd-walk.** Run from a subdirectory and
  stado walks up to find the instructions file; first hit wins.

## See also

- [session.md](session.md) — what `--session` operates on
- [features/skills.md](../features/skills.md) — the `--skill` flag
- [features/budget.md](../features/budget.md) — the cost gate
- [features/instructions.md](../features/instructions.md) — AGENTS.md loader
