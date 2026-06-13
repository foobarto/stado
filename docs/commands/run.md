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
4. With tools enabled (the default): opens a session worktree (sidecar-backed,
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
stado run --prompt "find every TODO in this repo"
```

Tools are **on by default**. The model can call `read` / `grep` /
`ripgrep` / `bash` / `webfetch` / `read_with_context` / `ast_grep` /
`edit` / `write` / `glob` / `tasks` / LSP-backed symbol tools, a
session worktree (sidecar-backed, signed refs) is opened so calls are
auditable, and each call lands in the session's audit log.

Pass `--no-tools` for pure-chat mode — no tools, no session, no audit
log. Pass `--tools=fs.*,shell.exec` to whitelist a comma-separated
subset of tools instead of enabling the full set.

Tool execution uses the auto-approve host — there's no interactive
y/n in run mode. Scope it via `[tools]` in `config.toml` if that's
too broad.

The `tasks` tool stores work items in stado state rather than the
session worktree. It is audited as state-mutating trace metadata, but it
does not create a tree commit.

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
| `--tools <globs>` | Whitelist a comma-separated subset of tools (default: all enabled) |
| `--no-tools` | Disable tools — pure-chat mode (no session, no audit) |
| `--tools-autoload <globs>` | Comma-separated globs always sent to the model every turn (default: `[tools.autoload]` from config) |
| `--tools-disable <globs>` | Comma-separated globs removed from the surface entirely (wins over enable + autoload) |
| `--mode <general\|security>` | Harness mode: `general` (default) or `security` (recon discipline + abusability filters) |
| `--persona <name>` | Persona used as the operating manual / system prompt |
| `--no-sandbox` | Opt out of the v0.57.0 default sandbox: disables Landlock + ceiling-runner. Use only for development scenarios. |
| `--session <id-or-label>` | Continue an existing session |
| `--max-turns N` | Cap turns (default 20) |
| `--no-turn-limit` | Remove the turn cap entirely |
| `--temperature <f>` | Sampling temperature (0 = provider default) |
| `--top-p <f>` | Nucleus sampling top-p (0 = provider default) |
| `--top-k <n>` | Top-k sampling (0 = provider default) |
| `--json` | Emit JSON Lines instead of raw text |
| `--quiet` | Suppress tool-call preview lines on stdout (non-JSON mode) |

## Config

Relevant `config.toml` sections:

- `[defaults]` — `provider`, `model`.
- `[agent].thinking` / `thinking_budget_tokens` — extended-thinking
  on providers that support it.
- `[budget].hard_usd` — hard cost cap; crossing exits 2 with
  `ErrCostCapExceeded`.
- `[tools].enabled` / `[tools].disabled` — trim the bundled tool
  set.
- `[hooks].post_turn` — lifecycle shell hook fired after each completed
  turn in `stado run` too. Disabled when `bash` is removed from the
  active tool set.

`stado config show` prints the resolved effective config.

## Gotchas

- **`--tools` opens a session each invocation** unless `--session` is
  passed. They accumulate. `session gc --apply` periodically.
- **Sandbox-by-default since v0.57.0.** `stado run` applies Landlock
  + the broker-projected ceiling-runner by default on Linux; macOS
  gets the ceiling-runner + sandbox-exec for tool execution but no
  Linux-style whole-process narrowing. Windows v2 sandboxing is
  still deferred. Pass `--no-sandbox` to opt out (NoneRunner, no
  Landlock).
- **`--sandbox-fs` retired in v0.57.0.** The pre-v0.57.0 flag is
  gone with no deprecation alias; passing it produces an "unknown
  flag" error. The new default is the sandboxed mode; `--no-sandbox`
  is the inverse-polarity opt-out.
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
- [features/tasks.md](../features/tasks.md) — the shared `tasks` tool
- [features/budget.md](../features/budget.md) — the cost gate
- [features/instructions.md](../features/instructions.md) — AGENTS.md loader
