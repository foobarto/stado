# ACP-wrapped agent compatibility (EP-0032 phase B)

Real-world per-agent setup notes for `tools = "stado"` in
`[acp.providers.<name>]`. These are the practical findings from
end-to-end smoke testing, not the spec.

The phase B design has stado advertise both standard ACP fs/* client
capabilities AND mount itself as an MCP server via `session/new.mcpServers`
(EP-0032 D6). In practice, agents vary in which channels they honor.

| Agent       | Binary    | ACP args     | Honors `session/new.mcpServers` | Setup needed for `tools = "stado"`                                            |
|-------------|-----------|--------------|----------------------------------|-------------------------------------------------------------------------------|
| **opencode**| `opencode`| `["acp"]`    | ✅ Yes                           | None — works out of the box.                                                  |
| **gemini**  | `gemini`  | `["--acp"]`  | ❌ No                            | Register stado as MCP server in gemini config + trust the working folder.     |
| **codex**   | `codex`   | (none — no stdio ACP-agent mode) | n/a    | Not invocable as ACP agent over stdio. **WRAP via MCP** instead: see `[mcp.providers.codex-mcp]` below. |
| **claude**  | `claude`  | (none — no stdio ACP-agent mode) | n/a    | Same as codex; claude-cli's ACP role is being-the-agent for Zed-as-client, not exposing stdio for stado-as-client. |
| **zed**     | `zed`     | n/a          | ✅ (per spec) when wrapped       | Editor, not a stdio CLI agent — out of scope for `acpwrap`.                   |
| **hermes**  | `~/.hermes/hermes-agent/hermes` | conditional | needs Python extras  | ACP/MCP both work *if* the user installs Python extras: `pip install 'agent-client-protocol>=0.9'` (ACP) or `pip install 'mcp>=1.2'` (MCP). `~/.local/bin/hermes` is a separate broken entry-point — use the agent path.|

## Per-agent setup instructions

### opencode

```toml
[acp.providers.opencode-acp-stado]
binary = "opencode"
args   = ["acp"]
tools  = "stado"
```

No additional setup. `opencode acp` honors `session/new.mcpServers`
directly — process tree during a session shows
`opencode acp` → `stado mcp-server`. End-to-end MCP tool calls flow
through stado's Executor and sandbox.

### gemini

```toml
[acp.providers.gemini-acp-stado]
binary = "gemini"
args   = ["--acp", "-m", "gemini-2.5-flash"]
tools  = "stado"
```

**Required setup (one-time per host):**

1. Register stado as a gemini MCP server at user scope:
   ```
   gemini mcp add -s user stado <absolute-path-to-stado> mcp-server
   ```

   This writes to `~/.gemini/settings.json`'s `mcpServers` map.

2. Trust the working folder (gemini gates MCP loading on workspace
   trust):
   ```
   ~/.gemini/trustedFolders.json:
     {"<absolute-path-to-cwd>": "TRUST_FOLDER", ...}
   ```

3. Use a non-rate-limited model (`-m gemini-2.5-flash`) if
   `gemini-3-flash-preview` is hitting `MODEL_CAPACITY_EXHAUSTED`.

**Why:** gemini-cli's `--acp` mode does NOT honor MCP servers passed
through `session/new.mcpServers`. It loads MCP servers from its own
`~/.gemini/settings.json` (user scope) plus per-project config (default
scope). Trust gating must succeed before any MCP server is spawned.

**Permission model:** gemini's default approval policy emits
`session/request_permission` requests for every tool call. Stado's
toolhost auto-approves with the agent's most-permissive
`allow_always`-shaped option (see `BuildRequestHandler` /
`handleRequestPermission` in `internal/providers/acpwrap/toolhost.go`).
The user's opt-in is `tools = "stado"` itself; per-call approval is
intentionally bypassed.

### codex (via MCP wrap)

codex does NOT expose a stdio-ACP-agent mode. **Use the new
`mcpwrap` provider** — `codex mcp-server` advertises two MCP tools
(`codex` for first turn, `codex-reply` for continuation) that stado
calls via MCP `tools/call`. Smoke-tested working end-to-end.

```toml
[mcp.providers.codex-mcp]
binary        = "codex"
args          = ["mcp-server"]
call_tool     = "codex"
continue_tool = "codex-reply"

# Optional pinning of model/sandbox/etc — passed verbatim to every
# tools/call's arguments map:
[mcp.providers.codex-mcp.call_tool_overrides]
model           = "gpt-5.2"
sandbox         = "read-only"
approval-policy = "never"
```

No setup needed beyond `codex login` (handled by codex itself).
Stado spawns `codex mcp-server`, runs MCP `initialize`, then on each
StreamTurn calls `codex` (first turn) or `codex-reply` (subsequent,
threaded by the captured `threadId`). The tool's
`{threadId, content}` result becomes the assistant turn — single
EvTextDelta, no progressive streaming (codex's MCP server returns
whole-turn synchronously).

Caveat: the same approach would work for any agent that exposes a
"run-a-session" tool via MCP. The `[mcp.providers]` schema is
generic — `prompt_arg_key`, `thread_id_arg_key`,
`content_result_key`, `thread_id_result_key` all override defaults
to match agents whose MCP tools use different field names.

### claude (Anthropic Claude Code CLI)

Same story as codex: claude-cli exposes itself as an ACP **agent for
Zed** (i.e. Zed-as-client wraps claude-as-agent), not as a stdio
ACP-agent stado-as-client can wrap. No `--acp` flag.

If a future claude revision adds a stdio ACP-agent mode, registering
stado would use:

```
claude mcp add stado <absolute-path-to-stado> mcp-server
```

(claude's `mcp add` accepts the bare command — no `--` separator.)

### zed

Zed is the editor; it consumes ACP agents (it's the canonical
`session/new.mcpServers` honorer per the spec). Out of scope for
`acpwrap` which wraps stdio CLIs.

### hermes

Hermes is the only surveyed agent supporting **both** ACP-agent
mode (`hermes acp`) and MCP-server mode (`hermes mcp serve`). Both
require Python extras that hermes-agent doesn't bundle by default:

```
# For ACP wrap:
pip install 'agent-client-protocol>=0.9'

# For MCP wrap:
pip install 'mcp>=1.2'
```

(Adjust to your hermes Python environment — pipx, venv, or
system-pip depending on how hermes was installed. The hermes
source at `~/.hermes/hermes-agent/pyproject.toml` lists `acp` and
similar extras under `[project.optional-dependencies]`; running
`pip install -e '~/.hermes/hermes-agent[acp]'` installs them
in-place.)

**Path note:** the working binary is
`~/.hermes/hermes-agent/hermes`. Some installs leave a separate
`~/.local/bin/hermes` Python entry-point that's broken (raises
`ModuleNotFoundError: hermes_cli`). Always point provider configs
at the agent path.

Once the extras are installed, hermes wraps either way — config
follows the gemini-acp pattern for ACP or the codex-mcp pattern
for MCP.

## Auto-registration — current state

Phase B v1.x does NOT auto-register stado as an MCP server with the
wrapped agent. Stado does emit the spec-canonical
`session/new.mcpServers` entry, which works for opencode but is
silently ignored by gemini.

For phase B v1.2 (next): stado should detect the wrapped agent's
identity at provider startup and either:
- Verify stado is registered (run `<agent> mcp list`, parse, check)
  and warn with the exact registration command when missing, OR
- Auto-register opt-in (config flag: `auto_register_mcp = true`)
  before launching the subprocess.

The warn-by-default path is the least-surprise option since
auto-registering modifies the user's global agent config without
explicit consent.

## Forcing stado-only routing

By default the wrapped agent retains its own built-in tools and
typically prefers them over stado's advertised ones (the spec's
"agents bias toward client-trusted fs/terminal" claim doesn't
empirically hold for these CLIs — they bias toward what they ship
with).

To force every tool call through stado's audit/sandbox stack, the
user passes the wrapped CLI's own built-in-disabling flag in the
provider's `args`:

```toml
[acp.providers.gemini-acp-stado-only]
binary = "gemini"
# When/if gemini exposes such a flag — current gemini-cli does not.
args   = ["--acp", "-m", "gemini-2.5-flash", "--disable-builtin-tools"]
tools  = "stado"
```

As of writing none of the surveyed agents expose a documented
"disable built-ins" flag. Achieving full audit coverage requires
either:
- Patching the wrapped agent (out of scope), or
- Wrapping a future revision that exposes the flag, or
- Using a sandbox at the OS level that mediates all of the wrapped
  agent's filesystem/exec activity (e.g. firejail, bwrap) — partially
  achievable today via stado's existing sandbox infrastructure but
  not integrated into the wrapped-agent subprocess spawn path.

## Smoke-test commands

Reproducible end-to-end verification (gemini path):

```
$ mkdir -p /tmp/stado-acp-smoke/.config/stado /tmp/stado-acp-smoke/work
$ echo 'test content' > /tmp/stado-acp-smoke/work/README.md
$ cat > /tmp/stado-acp-smoke/.config/stado/config.toml <<'EOF'
[defaults]
provider = "gemini-acp-stado"

[acp.providers.gemini-acp-stado]
binary = "gemini"
args   = ["--acp", "-m", "gemini-2.5-flash"]
tools  = "stado"
EOF

# One-time per host:
$ gemini mcp add -s user stado $(which stado) mcp-server
$ # add /tmp/stado-acp-smoke/work to ~/.gemini/trustedFolders.json

# Run with full debug:
$ cd /tmp/stado-acp-smoke/work
$ STADO_ACP_WIRE_DEBUG=1 STADO_ACP_TOOLHOST_DEBUG=1 \
  XDG_CONFIG_HOME=/tmp/stado-acp-smoke/.config \
  stado run --provider gemini-acp-stado \
    --prompt "Use mcp_stado_read to read README.md and reply CONTENT:<exact>"

# Expected:
# - stderr: [acpwrap toolhost] dispatch session/request_permission ...
# - stderr: [acpwrap toolhost] dispatch fs/read_text_file ... (if forced via --disable-builtin-tools)
# - stdout: CONTENT:test content
# - ps -ef --forest: gemini --acp → stado mcp-server (child)
```

Reproducible end-to-end verification (opencode path) — no setup:

```
$ # same /tmp/stado-acp-smoke layout but with provider:
[acp.providers.opencode-acp-stado]
binary = "opencode"
args   = ["acp"]
tools  = "stado"

$ stado run --provider opencode-acp-stado --prompt "..."
```
