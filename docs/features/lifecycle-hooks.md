# `[[hooks.lifecycle]]` — scriptable deny/mutate hooks (Lua)

Lifecycle hooks are **deterministic, scriptable interception points** around
the tool-dispatch and LLM seams. Unlike the fire-and-forget shell
[`post_turn` hook](./hooks.md), a lifecycle hook runs a **Lua policy** that
can **deny** (veto an action) or **mutate** (rewrite tool args, the LLM
request, or an output) — synchronously, in config order, with a per-hook
timeout.

Use them to enforce a policy that the model cannot talk its way around:
block `rm -rf`, redact secrets out of tool results before the model sees
them, pin a cheaper model for a class of turns, append a compliance banner
to every reply, etc.

## Why Lua (and why not JavaScript)

The substrate is [gopher-lua](https://github.com/yuin/gopher-lua) — an
embedded Lua VM, **not** a shell-out and **not** JavaScript. Lua gives you
real control flow and string handling for policy logic while staying small,
fast (no process spawn per call), and easy to sandbox: stado opens the VM
with only the safe standard libraries (see [Sandbox surface](#sandbox-surface)).

## Quick start

Lifecycle hooks live in your **user/global** config (`~/.stado/config.toml`
or the platform equivalent), as one or more `[[hooks.lifecycle]]` array
tables:

```toml
[[hooks.lifecycle]]
name = "deny-rm-rf"
lua  = """
  function pre_tool(p)
    if p.tool == "shell__bash" and string.find(p.args, "rm %-rf") then
      return { deny = "rm -rf blocked by policy" }
    end
  end
"""
```

That's it. Every shell tool call is now screened; a matching one is vetoed
and the reason is surfaced to the model as an errored tool result.

> **Security: project config cannot define hooks.** The entire `[hooks]`
> table — including `[[hooks.lifecycle]]` — is stripped from project
> (`.stado/config.toml`) config before it reaches the hook runner. Lua is a
> code-execution vector; an untrusted repo must not be able to inject a
> policy script. Hooks only take effect from your user/global config.
> A project-level `[hooks]` table is silently ignored, not merged.

## Configuration

Each `[[hooks.lifecycle]]` entry has:

| Key        | Type   | Meaning |
|------------|--------|---------|
| `name`     | string | Short identifier surfaced in failure logs. Optional; defaults to `lifecycle[N]`. |
| `lua`      | string | Inline Lua source for the hook body. |
| `lua_file` | string | Path to a `.lua` file read at startup. Ignored when `lua` is set. |

Provide **exactly one** of `lua` / `lua_file`. A hook that fails to compile
is skipped with a warning at startup (the agent still boots) — a broken
policy must not prevent stado from starting.

Multiple hooks run **serially in declaration order**. See
[Composition & ordering](#composition--ordering).

```toml
[[hooks.lifecycle]]
name = "audit-everything"
lua_file = "/home/me/.stado/hooks/audit.lua"

[[hooks.lifecycle]]
name = "redact-secrets"
lua  = """
  function post_tool(p)
    return { mutate = { result = string.gsub(p.result, "AKIA%w+", "[REDACTED]") } }
  end
"""
```

> **A `post_tool` mutate is NOT a confidentiality control.** It rewrites what
> the *model* sees, but the **original, pre-mutation** tool result is still
> recorded in the session's git sidecar audit trail (so `stado audit verify`
> can prove what the tool actually returned). A "redact secrets" hook keeps
> secrets out of the model's context, **not** off local disk — the unredacted
> bytes remain in `~/.local/share/stado/sessions/<id>.git` and are recoverable
> by anyone with access to that sidecar. Treat redaction hooks as
> context-hygiene, not secret-erasure; if a value must never be persisted,
> don't let the tool emit it in the first place (Codex #11).

## The handler contract

A hook is a Lua chunk that defines one or more **global functions** named
after the lifecycle points it handles:

| Function     | Point      | Fires |
|--------------|------------|-------|
| `pre_tool`   | `pre_tool` | Before a tool runs. |
| `post_tool`  | `post_tool`| After a tool runs, before the result reaches the model. |
| `pre_llm`    | `pre_llm`  | Before a turn request is sent to the provider. |
| `post_llm`   | `post_llm` | After a turn streams back, before the assistant text is committed. |
| `post_turn`  | `post_turn`| On every completed turn boundary (informational). |

Define only the functions you need; stado discovers which points a hook
subscribes to and only fires it at those points. A chunk that defines none
of the five is rejected at startup.

Each handler receives the payload as a Lua **table** `p` and returns one of:

```lua
-- 1. nil / nothing  → continue (no opinion, proceed unchanged)
function pre_tool(p) end

-- 2. deny           → veto (PRE) or replace the result (POST)
function pre_tool(p)
  return { deny = "reason string shown to the model / operator" }
end

-- 3. mutate         → rewrite the payload's mutable fields
function pre_tool(p)
  return { mutate = { args = '{"path":"/safe/dir"}' } }
end
```

`deny` takes precedence over `mutate` if a table somehow carries both. A
`mutate` table only needs the fields you want to change; everything else is
left intact (stado clones the original payload and overwrites the named
fields).

## Payload fields per point

All payloads carry a common header: `event` (the point name), `timestamp`
(unix millis), and `turn_index`.

### `pre_tool`
| Field   | Type   | Mutable | Notes |
|---------|--------|:-------:|-------|
| `tool`  | string |         | Tool name, e.g. `shell__bash`, `fs__read`. |
| `class` | string |         | Mutation class: `non-mutating` / `state-mutating` / `mutating` / `exec`. Gate on side-effect risk. |
| `args`  | string |   yes   | Raw JSON args as a string. |

- **Deny** → the tool is skipped entirely; the reason is returned to the
  model as an errored tool result. Nothing ran, nothing is recorded to the
  audit ref.
- **Mutate** (`args`) → the tool runs with the rewritten JSON; the audit
  trailers record the mutated args.

### `post_tool`
| Field    | Type   | Mutable | Notes |
|----------|--------|:-------:|-------|
| `tool`   | string |         | |
| `class`  | string |         | |
| `args`   | string |         | The (possibly pre-mutated) args the tool ran with. |
| `result` | string |   yes   | The tool's result content the model will see. |
| `error`  | string |   yes   | The tool's error string. |

- **Mutate** → rewrite `result` / `error`; the rewritten bytes are what the
  audit ref hashes.
- **Deny** → the action already happened, so deny is treated as a request to
  **replace** the result with the reason and flag it as an error.

### `pre_llm`
| Field       | Type   | Mutable | Notes |
|-------------|--------|:-------:|-------|
| `model`     | string |   yes   | The model id for this turn. |
| `system`    | string |   yes   | The system prompt for this turn. |
| `num_msgs`  | int    |         | Message-history length (read-only). |
| `num_tools` | int    |         | Tool count for this turn. |

- **Deny** → the turn is **aborted** before any provider call; the reason
  surfaces to the operator/client.
- **Mutate** → rewrite `system` and/or `model`. **Message history is NOT
  exposed for mutation** — rewriting append-only history would void the
  prompt-cache invariant. System + model knobs only.

### `post_llm`
| Field          | Type   | Mutable | Notes |
|----------------|--------|:-------:|-------|
| `text`         | string |   yes   | The assistant text. |
| `num_tool_use` | int    |         | Tool-call count this turn. |
| `tokens_in`    | int    |         | |
| `tokens_out`   | int    |         | |
| `cost_usd`     | float  |         | |

- **Mutate** (`text`) → rewrite the assistant text that gets committed to
  history (and re-rendered in the TUI). Tool calls are reported for
  inspection but **not** mutated here — `pre_tool` covers per-call arg
  rewriting.
- **Deny** → the generation already happened, so deny **replaces** the
  assistant text with the reason.

### `post_turn`
| Field         | Type  | Mutable | Notes |
|---------------|-------|:-------:|-------|
| `text`        | string| (info)  | Assistant text of the completed turn. |
| `tokens_in`   | int   |         | |
| `tokens_out`  | int   |         | |
| `cost_usd`    | float |         | |
| `duration_ms` | int   |         | Wall time of the turn. |

`post_turn` is **informational** — the turn is over, so deny/mutate have no
downstream effect. The point exists so one script can observe every
boundary. (This is the scriptable counterpart to the legacy shell
[`post_turn` hook](./hooks.md), which still fires independently.)

## Where the points fire

stado has two turn-execution surfaces and both drive the same hooks:

- **Headless / non-interactive** (`stado run`, `session.prompt`) runs through
  `runtime.AgentLoop`, which fires all five points.
- **The interactive TUI** streams the provider call directly (not through
  `AgentLoop`). The tool-side points (`pre_tool` / `post_tool`) run through
  the shared executor; `pre_llm`, `post_llm`, and `post_turn` are wired into
  the TUI's stream loop with the **same deny/mutate semantics**. So a policy
  behaves identically whether you run it interactively or headless.

## Composition & ordering

Hooks run **serially, in declaration order**, filtered to those subscribing
to the firing point:

- The **first deny short-circuits**: remaining hooks do not run, and the
  action is vetoed (PRE) / replaced (POST).
- A **mutate is threaded into the next hook** — hook 2 sees hook 1's
  rewritten payload. The final result carries the last mutation unless a
  later hook denies.
- A hook that subscribes to no explicit points (the "one script handles
  everything" case is implicit from which functions it defines) runs at
  every point it defines a handler for.

## Sandbox surface

Each hook VM is opened with **only the safe standard libraries**:

| Loaded | Excluded |
|--------|----------|
| `base`, `string`, `table`, `math` | `os`, `io`, `debug`, `package`/loaders |

That is enough for policy logic — string matching, table manipulation,
arithmetic — and nothing for filesystem or process escape. There is no
network, no file access, no shelling out. (Tightening this to a
stado-broker-mediated surface is a future security pass.)

The VM is opened **once per hook** and reused across calls (the runner is
serial, so there is no concurrent access). Globals you set at the top level
of the chunk persist across invocations — handy for a precompiled pattern,
but don't rely on per-call state hygiene beyond the payload you're handed.

## Timeout & error posture

- **5-second per-hook wall-clock cap.** A hook that blocks is cancelled.
- **Panics are recovered** — a Lua runtime fault never crashes the agent.

What happens on a fault (error, timeout, panic, or a mutation that returns
the wrong payload type) depends on the **fail-open / fail-closed** knob:

```toml
[hooks]
fail_closed = false   # default
```

- **`fail_closed = false` (default) — FAIL-OPEN.** A faulting hook is logged
  and treated as **Continue**. A broken policy hook must not wedge the agent
  loop. This is the safe default for convenience/telemetry policies.
- **`fail_closed = true` — FAIL-CLOSED.** The same fault is converted into a
  **deny**: the action is vetoed (PRE) or replaced (POST). Use this when a
  hook enforces a security boundary that a silent fail-open would breach — if
  the gate can't be evaluated, the action doesn't happen.

`fail_closed` only changes the **fault** path. A hook that runs cleanly
(Continue / Deny / Mutate) behaves identically under either setting.

Fault lines are logged to stderr with a `stado[hook]` prefix, naming the
hook and whether it failed open or closed.

## Examples

**Block destructive shell commands:**
```toml
[[hooks.lifecycle]]
name = "deny-destructive"
lua  = """
  function pre_tool(p)
    if p.class == "exec" and string.find(p.args, "rm %-rf /") then
      return { deny = "refusing rm -rf on an absolute path" }
    end
  end
"""
```

**Redact AWS keys out of every tool result before the model sees them:**
```toml
[[hooks.lifecycle]]
name = "redact-aws-keys"
lua  = """
  function post_tool(p)
    local cleaned = string.gsub(p.result, "AKIA%w+", "[REDACTED-AWS-KEY]")
    if cleaned ~= p.result then
      return { mutate = { result = cleaned } }
    end
  end
"""
```

**Pin a cheaper model for short conversations:**
```toml
[[hooks.lifecycle]]
name = "cheap-early-turns"
lua  = """
  function pre_llm(p)
    if p.num_msgs < 4 then
      return { mutate = { model = "claude-haiku-4-5" } }
    end
  end
"""
```

**Append a compliance banner to every reply:**
```toml
[[hooks.lifecycle]]
name = "compliance-banner"
lua  = """
  function post_llm(p)
    return { mutate = { text = p.text .. "\\n\\n— generated under policy X" } }
  end
"""
```

**Hard gate that must run (fail-closed):**
```toml
[hooks]
fail_closed = true

[[hooks.lifecycle]]
name = "must-screen-exec"
lua  = """
  function pre_tool(p)
    if p.class == "exec" and string.find(p.args, "curl") then
      return { deny = "outbound network via curl is blocked" }
    end
  end
"""
```
With `fail_closed = true`, if `must-screen-exec` ever errors or times out,
the `exec` tool is denied rather than allowed through.

## Gotchas

- **Hooks are global-config only.** A `[hooks]` table in a project's
  `.stado/config.toml` is ignored. This is deliberate — see the security
  note above.
- **`pre_llm` cannot rewrite history.** Only `system` and `model`. If you
  need to inject context, do it via the system prompt.
- **`post_turn` deny/mutate is a no-op.** The turn is already over; the point
  is observational. Use `post_llm` to alter the reply.
- **No per-call state isolation.** The VM is reused; top-level globals
  persist across calls.
- **Fail-open by default.** A telemetry-style policy that throws will be
  *skipped*, not enforced. Set `fail_closed = true` for anything
  security-relevant.

## See also

- [features/hooks.md](./hooks.md) — the legacy fire-and-forget shell
  `post_turn` hook (notification-only; runs alongside lifecycle hooks).
- [features/sandboxing.md](./sandboxing.md) — stado's tool sandbox + broker
  model the hook surface will eventually integrate with.
- [commands/tui.md](../commands/tui.md) — interactive surface.
- [commands/run.md](../commands/run.md) — non-interactive surface.
```
