# gosec backlog cleanup

When clearing gosec findings in this repo, split the work by rule and prefer
real hardening before suppressions:

- Use `0o700`/`0o600` for stado-owned runtime state, config, cache, session
  metadata, and trace files. Use `0o750` for generated directories that are
  meant to live in a project tree.
- Keep public or executable artifacts at their required modes, but annotate the
  exact call site with `#nosec` and a reason, for example signed plugin sidecars
  or extracted bundled binaries.
- For workdir-scoped reads, prefer `workdirpath.ReadFile` when the original
  relative path is available. It keeps the final open rooted through `os.Root`.
- For dynamic path findings that are already fixed metadata paths under a
  resolved worktree/plugin/config directory, use a narrow inline `#nosec G304`
  reason instead of disabling a rule globally.
- `gosec -fmt=json -out <file>` may not leave the output file behind on a
  zero-finding run; the zero exit code is the reliable pass signal.
