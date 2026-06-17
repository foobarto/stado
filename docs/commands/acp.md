# `stado acp`

Run stado as a Zed Agent Client Protocol server over stdio.

## What it does

ACP is the editor-facing server surface. It speaks JSON-RPC 2.0 using
Zed's Agent Client Protocol shape and reuses stado's provider/runtime
wiring. By default it is a prompt/response agent. With `--tools`, it
opens a git-native session and runs the full audited tool loop.

## Usage

```sh
stado acp
stado acp --tools
```

Example Zed config:

```json
{
  "agent_servers": {
    "stado": {
      "command": "stado",
      "args": ["acp", "--tools"]
    }
  }
}
```

### Flags

| Flag | What it does |
|------|--------------|
| `--tools` | Open a git-native session and run the full audited tool loop. |
| `--max-turns <n>` | Per-prompt turn cap (operator default; `0` falls back to `[acp].max_turns` or the built-in). |
| `--no-turn-limit` | Effectively unlimited per-prompt turns; overrides `--max-turns`. |
| `--resume <id-or-label>` | Resume an existing git-native session by id, prefix (≥8 chars), or description substring. A bad query fails at startup. Per-call equivalent: `session/new {"resumeSession": ID}`. |
| `--persona <name>` | Default persona for new sessions. Resolution: bundled + user (`~/.config/stado/personas/`); project (`.stado/personas/`) only when `[defaults] allow_project_persona = true` in user config. Empty falls back to `[defaults].persona`, then bundled `default`. Per-session override: `session/new {"persona": NAME}`. |
| `--provider <name>` | Override `[defaults].provider` for this process (persistent root flag). |
| `--model <id>` | Override `[defaults].model` for this process (persistent root flag). |

## Protocol Surface

The current server supports:

- `initialize`
- `session/new`
- `session/prompt`
- `session/cancel`
- `shutdown`

The client replies to agent-initiated requests with
`session/choice_response` (selection prompts) and
`session/approval_response` (tool/action approvals).

When tools are enabled, tool-call notifications are sent as
`session/update` events and tool execution is committed to the same
sidecar `tree` and `trace` refs used by the TUI and `stado run`; the
git-backed transcript is appended to `.stado/conversation.jsonl` as
turns complete.

Subagent lifecycle notifications also use `session/update` with
`kind: "subagent"`. The payload includes `phase`, `status`, `role`,
`mode`, `child`, `childWorktree`, `parentSession`, and
`timeout_seconds`. Finished worker notifications may also include
`forkTree`, `changedFiles`, `scopeViolations`, and `adoptionCommand`;
clients should treat those as visibility fields and use
`stado session adopt` for explicit child-to-parent adoption.

## Gotchas

- ACP sessions are editor sessions, not the same as `stado session`
  CLI entries unless `--tools` opens the sidecar path for execution.
- Provider init is lazy. Startup can succeed even when provider
  credentials are missing; the first prompt surfaces the failure.
- Use `stado headless` when you want an editor-neutral JSON-RPC method
  set rather than Zed ACP.

## See also

- [headless.md](headless.md) — general JSON-RPC daemon.
- [session.md](session.md) — persisted sidecar sessions.
- [../eps/0010-interop-surfaces-mcp-acp-headless.md](../eps/0010-interop-surfaces-mcp-acp-headless.md)
