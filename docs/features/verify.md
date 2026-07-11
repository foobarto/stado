# Verify-work command gate

Verification is an opt-in phase between the model saying it is done and stado
accepting the turn. It runs deterministic commands in order; all must pass.

```toml
[verify]
max_rounds = 3
strict = false
commands = [
  "go vet ./...",
  "go test ./internal/... -count=1",
]
```

The commands execute through `shell__exec` on the active executor. They use
the same sandbox ceiling, lifecycle hooks, audit commits, output budget, and
telemetry as a model-issued shell call. Disabling `shell__exec` makes the gate
unavailable rather than bypassing the tool policy.

At a candidate completion:

1. stado renders/emits `verifying` and runs commands in order.
2. Exit 0 advances to the next command; all pass accepts the completion.
3. A non-zero exit short-circuits the round and feeds the command plus output
   back to the model as a new verification critique.
4. The model gets another normal turn. `max_rounds` bounds this cycle.
5. A final failure ends as `verify_exhausted` with the last critique.

Failure to launch the verification shell or set up its executor/sandbox is an
infrastructure error. Once the shell launches, every non-zero command result is
a failed gate, including exit 126 or 127 from a command inside that shell. The
default is fail-open with a visible warning for infrastructure errors so broken
local tooling does not wedge every session. Set `strict = true` to make those
errors fatal.

Use repeatable `stado run --verify '<command>'` flags for per-run commands, or
`--no-verify` to disable user config. In the TUI, `/verify` toggles the phase
and `/verify status` shows its command count, retry bound, and strict posture.
Headless and ACP clients receive `kind=verify` status notifications.
Programmatic surfaces emit `pending_candidate` before generation and buffer
candidate provider events while verification is enabled. Rejected candidates
remain in persisted repair history but are not streamed or returned as the
accepted result; only a passing (or explicitly fail-open) candidate is
published. A turn that requests tools closes the pending state with
`tool_continuation`; its buffered events are then published and verification
waits for the later no-tool completion.

If generation ends before a candidate can be gated, the phase closes with
`generation_error` (or `cancelled` for operator cancellation). Budget and token
caps also close the phase without publishing the unaccepted candidate.

## Trust boundary

`[verify]` is stripped from project `.stado/config.toml`, including mixed-case
spellings. It is accepted only from operator/user config or an explicit run
flag. This prevents a repository from running commands merely
because an operator opened it. Sandboxing is an additional boundary, not a
substitute for origin trust.

Phase 1 is command-only. The independent LLM judge described in EP-46 remains
deferred until the command gate and the structured loop-result contract have
settled.
