Session-aware CLI plugin runs need two generic pieces to feel real:

- `stado plugin run --session <id>` must open the target session by
  worktree ID, not by the caller's cwd, and it should backfill the
  `.stado/user-repo` pin when the CLI can infer the repo root. That
  keeps later session-scoped operations attached to the right sidecar.
- Plugin-driven forks must persist the seed summary into the child
  session's `.stado/conversation.jsonl`, not just a trace marker. The
  plugin ABI already treated fork-based recovery as the right model for
  compaction; without the persisted seed, the child resumed empty and
  the model lost the summary it just paid to generate.
- `last_turn_ref` and `session:fork` need to round-trip the same shape.
  The host returns a full `refs/sessions/<id>/turns/N` ref string, so
  the fork resolver must accept that form in addition to `turns/N` and
  full commit SHAs.
