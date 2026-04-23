# Session and Plugin Path Boundaries

- Any session ID that becomes `filepath.Join(cfg.WorktreeDir(), id)` must go through `worktreePathForID()`. Raw IDs are dangerous even in "read-only" commands because neighboring destructive commands (`agents kill`, `session delete`) tend to copy the same pattern.
- Ref-derived IDs are not automatically trustworthy. Sidecar refs can be malformed or hostile, so list/search/show paths should validate them too instead of assuming ref names are clean.
- Installed plugin IDs need the same treatment. Use `plugins.InstalledDir(...)` instead of joining raw IDs into `$XDG_DATA_HOME/stado/plugins`.
- `session:fork` should stay session-scoped. Accept only `turns/<N>` (or the equivalent full ref under the current session) rather than arbitrary full refs or raw commit hashes, otherwise plugins can escape their attached session authority.
- For install/update writes, explicit `Sync` + `Close` error handling is part of the success path. Deferred closes are not enough when replacing binaries or copying signed plugin payloads.
