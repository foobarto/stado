# `stado doctor`

Non-destructive environment health-check. Run it when something
isn't working and you want to rule out misconfiguration before
digging into logs.

```sh
stado doctor
```

## What it does

Walks a battery of checks against the host + the loaded config, one
line per probe. Green ✓ = the check passed, the right-hand column
explains what it found:

```
✓ OS/arch             linux/amd64  (ok)
✓ Go runtime          go1.25.0  (ok)
✓ Config file         ~/.config/stado/config.toml  (does not exist yet)
✓ State dir           ~/.local/share/stado  (exists (dir))
✓ ripgrep (rg)        /usr/bin/rg  (ok)
✓ ast-grep            /usr/bin/ast-grep  (ok)
✓ bubblewrap (bwrap)  /usr/bin/bwrap  (ok)
✓ Sandbox runner      bwrap  (ok)
✓ Landlock            available  (kernel ≥ 5.13)
✓ Context thresholds  soft=70% hard=90%  (ok)
✓ Budget caps         (unset — no cost guardrail)  (ok)
✓ Local lmstudio      http://localhost:1234/v1  (running · 13 model(s): …)

all checks passed
```

"Passed" isn't the same as "perfect" — a check that's **optional** or
**informational** still reports ✓; the right column tells you what
the actual state is. The command only exits non-zero when a check
actively blocks stado from running.

## Why it exists

Three drivers:

1. **First-run sanity.** `stado doctor` after install is the fastest
   way to confirm the binary found its bundled tools, the sandbox
   backend is usable, and at least one provider will work.
2. **Local-runner discovery.** The bottom half of the output probes
   `ollama` / `lmstudio` / `llamacpp` / `vllm` on their default
   ports and tells you which ones are running + what models each
   exposes. Handy when you're switching between runners.
3. **CI pre-flight.** Run it early in a pipeline to fail fast when
   a build-server image is missing a dep (e.g. ripgrep absent).
   Exit code is 0 when every check passes or is informational; any
   hard failure bumps it to 1.

## Checks performed

| Category | Probes |
|----------|--------|
| **Runtime** | OS/arch, Go runtime version, config file path, state dir, worktree dir |
| **Tools** | ripgrep, ast-grep, gopls (optional), git, cosign |
| **Sandboxing** | `bwrap` presence, Landlock kernel support, active sandbox backend |
| **Providers** | Resolved default provider, token counter availability |
| **Config** | Context thresholds, budget caps, hooks, tools filter |
| **Local runners** | Probes ollama / llamacpp / vllm / lmstudio endpoints |

Each probe is intentionally stateless — no network calls beyond
localhost, no writes, no credential checks.

## Output flags

```sh
stado doctor --json        # machine-readable; pipe into jq
stado doctor --no-local    # skip local-runner probes (offline CI)
```

`--json` emits one object per check:

```json
{"check":"ripgrep (rg)","status":"ok","value":"/usr/bin/rg","detail":"ok"}
```

The output is newline-delimited JSON (one object per line), so it's
easy to pipe into `jq` or line-oriented tooling.

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | All checks passed (including informational ones) |
| 1 | One or more blocking checks failed, or the doctor command itself errored |

Informational misses (e.g. `gopls not installed`) don't bump the
exit code. A real failure looks like "sandbox runner: no backend
available — both bwrap and firejail missing, Landlock disabled".

## Gotchas

- **"Provider (unset)" is not a failure.** With no `[defaults]`
  provider pinned, stado probes local runners at TUI boot and picks
  the first reachable one. Doctor reports this as the expected
  behaviour.
- **Local-runner probes add ~100ms.** Use `--no-local` in CI if
  you never run against local inference and want doctor to finish
  faster.
- **Stale token-counter warning.** When the active provider doesn't
  expose a client-side tokeniser, the context-percent pill in the
  TUI stays blank. Doctor reports "ctx% accuracy depends on which
  local runner answers" to flag the limitation without treating it
  as a failure.

## See also

- [commands/config.md](./config.md) — what effective config doctor
  is checking against.
- [features/sandboxing.md](../features/sandboxing.md) — how the
  sandbox checks map to runtime behavior.
