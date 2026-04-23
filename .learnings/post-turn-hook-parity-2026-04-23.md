`[hooks].post_turn` parity across TUI, `stado run`, and headless works
cleanly if the shared seam is the agent loop's per-turn completion
callback, not surface-specific stream plumbing.

Key details:

- The existing TUI payload's `turn_index` is based on the pre-append
  history length at turn completion time, not the post-append message
  count.
- `runtime.AgentLoopOptions.OnTurnComplete` needed to carry:
  - the pre-append turn index (`len(msgs)`)
  - final turn text
  - final `agent.Usage`
  - turn duration
- Hook disable semantics should still respect `[tools]` trimming of
  `bash`, even on non-TUI surfaces, so hooks don't bypass an explicit
  "no shell" configuration.
