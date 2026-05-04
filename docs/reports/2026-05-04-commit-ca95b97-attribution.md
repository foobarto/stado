# Note on commit `ca95b97` attribution

Commit message says only: *test(workdirpath): regression for
relative-workdir EvalSymlinks bug*.

Actual diff also shipped, by accident:

- `cmd/stado/run.go` — `--quiet` flag wiring + emitter signature
  change.
- `cmd/stado/run_emitter_test.go` — new test file for the quiet
  emitter behaviour.
- `CHANGELOG.md` — `## Unreleased` entries for the `--quiet` flag and
  the `stado doctor` auto-skip-local-probe change.

Cause: two Claude Code processes were active in `~/Dokumenty/stado`
simultaneously. The other process modified those paths in the
working tree after I had earlier `git add`'d related paths during
the v0.26.0 WIP merge. When I ran `git add internal/workdirpath/...
&& git commit`, the index already had the other paths queued from
earlier rounds, so they all went in.

The code is correct + complete + tested; only the message-vs-content
attribution is misleading. Decided not to force-push an amend because
that's destructive on `main` and the other Claude may already have
based subsequent work on the published HEAD.

For anyone tracing history: the canonical commit message for the
`--quiet` + `doctor` skip-local-probe work is the CHANGELOG entries
themselves (in the `## Unreleased` block). The workdirpath test
addition stands on its own.
