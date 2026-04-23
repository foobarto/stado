# Doctor CLI surface

- `cmd/stado/main.go` exits `1` for ordinary command errors, so a
  subcommand should not document a distinct "internal error" exit code
  unless it has explicit custom exit-code plumbing.
- `stado doctor --json` is newline-delimited JSON, one object per
  check, with `check`, `status`, `value`, and `detail` fields.
- `stado doctor --no-local` is the fast/offline path for CI and skips
  local-runner probes entirely.
