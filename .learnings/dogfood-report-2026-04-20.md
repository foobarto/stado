# Dogfood report — 2026-04-20

Ran through stado's CLI surface + plugin lifecycle + headless RPC from
a clean-room XDG setup using a freshly-built binary (`0.0.0-dev`).
Golden paths all work; findings below are prioritized from
most-impactful to nits.

**Update (same-day):** Batch 1 (text-only) and Batch 2 (behavior +
tests) fixes shipped — findings #3, #4, #5, #7, #10, #11, #12, #13,
#15, #16, #19 are resolved in the same commit trailer as this report.
Batch 3 items (#1, #2, #6, #8, #14) left for design discussion.

Tested: `version`/`verify`, `doctor`, `config init`, `session
new/list/show/tree/fork/attach/delete`, `agents list`, `audit
pubkey/verify`, `plugin gen-key/sign/trust/verify/install/run/list`,
`run --prompt`, `headless` (session.new, tools.list, plugin.list,
plugin.run, providers.list, shutdown), no-tty TUI launch.

Not tested: interactive TUI slash commands (needs real terminal),
sandbox-fs integration with tool calls, `self-update`, `acp` server,
fresh install via `self-update`, airgap build, cross-platform paths.

## P1 — real bugs that bite

1. **Headless shutdown returns before in-flight requests.** Each
   dispatch runs on its own goroutine; `shutdown` completes instantly
   while a `plugin.run` is still executing, so the shutdown response
   arrives *before* the plugin's result. Fix: either serialise
   shutdown (drain pending requests) or document that clients must
   not send further requests after shutdown and must block on all
   outstanding responses first. `internal/acp/jsonrpc.go:96-115`.

2. **`headless.providers.list` current field is always empty.** Returns
   `{"current": ""}` even when a local provider was auto-detected and
   is actively serving requests. Clients can't tell which provider
   they're actually talking to. Should reflect the resolved provider,
   not just `cfg.Defaults.Provider`. `internal/headless/server.go:69-73`.

3. **Fallback message name-drops Anthropic misleadingly.** `"stado:
   no ANTHROPIC_API_KEY — falling back to local lmstudio at ..."` fires
   whenever no provider is configured — even if the user never intended
   Anthropic. Leftover from the anthropic-first era that was since
   corrected. Better: `"no provider configured — using local <runner>
   at <endpoint> (set defaults.provider to pin)"`.
   `internal/tui/app.go:184-186`.

4. **`stado` (no args) in non-TTY context exits 0.** Prints `Error:
   tui: could not open a new TTY: open /dev/tty: no such device or
   address` but returns exit code 0 — scripts can't detect the
   failure. Separately: the error leaks a low-level kernel message.
   Preferred: `"stado: interactive TUI requires a TTY — try \`stado run
   --prompt ...\` for one-shot or \`stado headless\` for scripting"`
   and exit 1.

5. **`Status: attached` in `session list` is a lie.** The field is set
   to "attached" whenever a worktree directory exists on disk, with
   zero relation to whether a stado process is using it. Dogfood
   session produced 3 "attached" sessions after 0 live processes
   existed. Fix: inspect the `.stado-pid` file; "attached" ⇔ pid alive.
   Otherwise "detached" or "idle". `internal/runtime/session_summary.go:42-46`.

## P2 — rough edges

6. **`session list` accumulates junk sessions.** `run --prompt`,
   `session new`, and each headless session all leave persistent
   sessions on disk. After an afternoon of dogfooding, the list is
   noise. Consider: `--keep=false` default for `run --prompt`
   (auto-delete when no work was committed), or a `session gc`
   subcommand that drops zero-turn sessions older than N hours.

7. **`version` and `verify` disagree.** `version` prints
   `0.0.0-dev`; `verify` prints `v0.0.0-20260419224213-81a3813595e1`.
   Both are reading the same `go build -ldflags` inputs. Pick one
   format and make both consistent.

8. **`plugin trust` error prints fingerprint but command takes
   pubkey.** When verify fails, the error is `"author <fpr> is not
   pinned; run \`stado plugin trust <pubkey>\` first"`. The
   fingerprint is printed, but `trust` takes the full hex pubkey —
   user has to find it elsewhere. Either accept fingerprint in
   `trust`, or echo the manifest's pubkey in the error
   (`stado plugin verify .` already has it).

9. **`plugin run` stderr bleeds into stdout consumers.** `stado plugin
   run X greet '{}'` emits both `2026/04/20 17:01:52 INFO greet
   invoked plugin=hello-go` (stderr) AND `{"message":"Hello, X!"}`
   (stdout). Scripts piping stdout to `jq` are fine; humans eyeballing
   the terminal see log noise mixed in. Default log level could drop
   to warn for interactive `plugin run`.

10. **`doctor` pluralization.** `"1 check(s) failed"`. Bikeshed but
    it's the first thing a troubleshooter reads. `"1 check failed" /
    "2 checks failed"`.

11. **`session attach <unknown>` leaks OS path.** `"attach: session
    does-not-exist has no worktree: stat /tmp/.../worktrees/does-not-
    exist: no such file or directory"`. The stat path is noise; the
    useful bit is "no worktree for this id". Strip the stat wrapper.

12. **`session delete` is silently idempotent.** Second delete on a
    missing session prints `"deleted X"` identically to the first.
    Clients can't tell whether they did useful work. Distinct
    "already deleted" / "not found" message would be clearer.

13. **`plugin --help` contains stale roadmap text.** `"The wazero
    runtime that actually executes plugin wasm is a follow-up; the
    trust layer lands first..."` — but wazero landed in Phase 7.1
    (commit well before v0.0.0-dev). Remove.

14. **`plugin list` is trust-list, not installed-list.** `stado plugin
    list` shows pinned signers. Users expect "list installed plugins"
    (what headless `plugin.list` RPC returns). Add `plugin installed`
    subcommand OR rename `plugin list` → `plugin authors` / `plugin
    installed` as separate subcommands.

15. **Config template pins a stale model.** `provider = "anthropic"`
    comment suggests `model = "claude-sonnet-4-5"` — the current best
    is `claude-sonnet-4-6` (or `claude-opus-4-7`). Update when model
    IDs bump.

## P3 — genuinely small / wontfix candidates

16. `stado` top-level description is `"AI CLI harness and editor"`.
    Stado isn't an editor. Drop the "and editor" or replace with
    something true ("coding-agent runtime"?).

17. `audit pubkey` output is just `<fpr> <pubkey>` with no header.
    Fine for scripting; a `--header` or a labeled default for humans
    would help.

18. Headless dispatcher goroutines — ordering caveat isn't documented.
    Relevant when diagnosing issue #1 above.

19. `plugin run unknown` error wraps a `stat` path; same pattern as
    #11.

## Strong points (worth keeping)

- **`doctor` is excellent.** Full provider/sandbox/binary/runner
  audit with colored success/fail markers. First-run UX sells the
  product from this one command.
- **Plugin lifecycle golden path is fluid.** Eight commands from
  `gen-key` to `run` — zero surprises once the keys are pinned.
- **Error messages are mostly actionable.** Most errors tell you
  the exact next command to run.
- **Empty states are handled everywhere.** `(no sessions)`, `(no
  agents)`, `(no plugin signers pinned)` are consistent.
- **Local-provider auto-detection works.** LM Studio was detected,
  probed, and used as the default with no config.
- **Headless JSON-RPC is feature-complete.** New plugin.list /
  plugin.run / plugin_fork all round-tripped cleanly.
- **`config init` writes a well-commented template.** Every section
  has prose explaining what it does and when you'd change it.

## Suggested fix order

Batch 1 (low-risk text-only, 30 min):
- #3 (fallback message)
- #10 (pluralization)
- #13 (stale help text)
- #15 (stale model in config)
- #16 (description)

Batch 2 (behavior, 1-2 hours, tests):
- #7 (version consistency)
- #4 (no-tty exit code + message)
- #5 (attached status real-ness)
- #11 / #19 (error path leaks)
- #12 (idempotent delete clarity)

Batch 3 (design-level, needs discussion):
- #1 (headless shutdown ordering)
- #2 (providers.list current)
- #6 (session GC policy)
- #8 (trust-by-fingerprint or pubkey echo)
- #14 (plugin list / installed)
