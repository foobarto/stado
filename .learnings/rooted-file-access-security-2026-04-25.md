# Prefer rooted file operations for worktree confinement

- A symlink-aware preflight check followed by `os.ReadFile`/`os.WriteFile`
  closes ordinary traversal, but it still has a check/open race if a
  worktree can be mutated concurrently.
- For Go 1.25+, use `os.OpenRoot` plus `Root.ReadFile`, `Root.WriteFile`,
  `Root.MkdirAll`, and `Root.RemoveAll` for worktree- or capability-rooted
  operations. Keep the existing path normalization to produce user-facing
  errors, but make the actual filesystem operation rooted.
- For plugin wasm host imports, validate byte lengths before narrowing to
  u32/i32 ABI values and keep those conversions behind small helpers with
  explicit bounds checks.
