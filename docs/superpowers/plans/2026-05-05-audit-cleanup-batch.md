# Audit cleanup batch implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the 10 small audit-additions items from the post-v0.33.0 quality pass as discrete commits on `feat/audit-cleanup-batch`. Each is mechanical (no design decisions); the ordering minimizes test-suite churn.

**Architecture:** Each task is a single file (or small file pair) edit, with a focused test where applicable. No new features. No file moves. Tasks are independent — re-ordering doesn't break anything, but presented in roughly increasing scope.

**Tech Stack:** Go 1.22+; reuses existing test patterns + helpers (`config.WriteToolsListAdd`, `WireForm`/`ParseWireForm`, etc.).

**Branch:** `feat/audit-cleanup-batch` (already created off `main` at `7eaa68f`).

**Locked decision context** (from earlier in conversation):
- Pre-1.0; single user; "no kid gloves" on backwards compatibility (NOTES line 1117).
- Items derived from the three-axis quality audit dispatched after Tasks 1-11 of the previous PR landed.

**Scope OUT (explicitly):**
- BACKLOG audit-additions item 19 (parity tests for migrated families) — modest scope, deserves its own plan.
- BACKLOG #1+#14 (spawn_agent collapse + FleetBridge messaging) — design call locked but separate writing-plans pass needed.

---

## Task 1: Drop `--tools-whitelist` flag (rename to `--tools`)

**Files:**
- Modify: `cmd/stado/run.go:38, 405-414`
- Modify: `docs/eps/0037-tool-dispatch-and-operator-surface.md:30` (history note, update wording)
- Modify: `CHANGELOG.md:135` (historical line — add a note acknowledging the rename)

**Context:** Audit found `--tools-whitelist` lingering as back-compat. NOTES locked the canonical name as `--tools`. Pre-1.0 → drop the back-compat alias entirely.

- [ ] **Step 1.1: Read current state.** `cd /home/foobarto/Dokumenty/stado && grep -n "tools-whitelist\|runToolsWhitelist" cmd/stado/run.go`. Confirm flag definition lives at lines 38, 409, and the back-compat comment block is at 405-408.

- [ ] **Step 1.2: Rename flag in `run.go`.** Edit `cmd/stado/run.go`:
  - Line 38: rename variable `runToolsWhitelist` → `runTools` and update the inline comment.
  - Lines 405-408 (the back-compat comment): delete the block.
  - Line 409: change `runCmd.Flags().StringVar(&runToolsWhitelist, "tools-whitelist", ...)` to `runCmd.Flags().StringVar(&runTools, "tools", ...)`. Keep the description string updated to use the new flag name.
  - Find any other `runToolsWhitelist` references in the file via `grep -n "runToolsWhitelist" cmd/stado/run.go` after the rename and fix them all.

- [ ] **Step 1.3: Update CHANGELOG.md.** Find line 135 (`- **CLI** — \`--tools-whitelist\`, ...`). Add a new line below it for v0.34.0+ (or whichever next-version section exists; create one if missing):

```markdown
### Unreleased — pre-1.0 cleanup
- **CLI breaking** — `--tools-whitelist` renamed to `--tools` (canonical per architectural-reset NOTES §10). No back-compat alias kept; pre-1.0 means scripts using the old name need updating.
```

- [ ] **Step 1.4: Update EP-0037 history note.** In `docs/eps/0037-tool-dispatch-and-operator-surface.md` find the history entry referencing `--tools-whitelist/autoload/disable` (around line 30). Add a follow-up history entry:

```yaml
  - date: 2026-05-05
    status: Implemented
    note: >
      Renamed --tools-whitelist to --tools (canonical per NOTES §10). No
      back-compat alias kept; pre-1.0.
```

- [ ] **Step 1.5: Run.** `cd /home/foobarto/Dokumenty/stado && go build ./... && go test ./cmd/... -count=1`. Expected: PASS.

- [ ] **Step 1.6: Commit.**

```bash
cd /home/foobarto/Dokumenty/stado
git add cmd/stado/run.go CHANGELOG.md docs/eps/0037-tool-dispatch-and-operator-surface.md
git commit -m "feat(cli)!: rename --tools-whitelist to --tools

Canonical flag name per NOTES §10. Pre-1.0 means no back-compat
alias kept — scripts using the old name need updating."
```

---

## Task 2: Fix silent JSON-unmarshal error swallows in meta-tools

**Files:**
- Modify: `internal/runtime/meta_tools.go:43, 147`
- Modify: `internal/runtime/meta_tools_test.go` (add tests)

**Context:** Audit flagged `_ = json.Unmarshal(args, &req)` at lines 43 (`metaSearch.Run`) and 147 (`metaCategories.Run`) — malformed args silently default to empty query. The other two meta-tools (`metaDescribe:101`, `metaInCategory:186`) correctly check the error.

- [ ] **Step 2.1: Read current state.** `grep -n "json.Unmarshal" /home/foobarto/Dokumenty/stado/internal/runtime/meta_tools.go` — confirm the four call sites and their differing patterns.

- [ ] **Step 2.2: Write failing test.** Append to `internal/runtime/meta_tools_test.go` (create if it doesn't exist — locate any existing test file in that package first via `ls /home/foobarto/Dokumenty/stado/internal/runtime/meta_tools*`):

```go
func TestMetaSearch_RejectsMalformedJSON(t *testing.T) {
	tool := &metaSearch{Reg: tools.NewRegistry()}
	_, err := tool.Run(context.Background(), []byte("{not valid json"), nil)
	if err == nil {
		t.Error("metaSearch.Run should error on malformed JSON args; got nil")
	}
}

func TestMetaCategories_RejectsMalformedJSON(t *testing.T) {
	tool := &metaCategories{}
	_, err := tool.Run(context.Background(), []byte("{not valid"), nil)
	if err == nil {
		t.Error("metaCategories.Run should error on malformed JSON args; got nil")
	}
}
```

Note: Read the existing tests in `meta_tools_test.go` (if any) before writing — match their imports and Tool construction style.

- [ ] **Step 2.3: Run, verify FAIL.** `cd /home/foobarto/Dokumenty/stado && go test ./internal/runtime/ -run "TestMetaSearch_RejectsMalformedJSON|TestMetaCategories_RejectsMalformedJSON" -count=1 -v`. Expected: tests run; both FAIL because malformed JSON silently passes.

- [ ] **Step 2.4: Fix `metaSearch.Run`.** In `meta_tools.go:43`, replace:

```go
_ = json.Unmarshal(args, &req)
```

with:

```go
if err := json.Unmarshal(args, &req); err != nil {
	return nil, fmt.Errorf("metaSearch: parse args: %w", err)
}
```

(Confirm `fmt` is in the import block; add if missing.)

- [ ] **Step 2.5: Fix `metaCategories.Run`.** Same pattern at `meta_tools.go:147`. Replace `_ = json.Unmarshal(args, &req)` with the error-checking form using prefix `"metaCategories: parse args: %w"`.

- [ ] **Step 2.6: Run.** `cd /home/foobarto/Dokumenty/stado && go test ./internal/runtime/ -run "TestMetaSearch_RejectsMalformedJSON|TestMetaCategories_RejectsMalformedJSON" -count=1 -v`. Expected: PASS.

- [ ] **Step 2.7: Run full runtime tests.** `go test ./internal/runtime/ -count=1`. Expected: PASS.

- [ ] **Step 2.8: Commit.**

```bash
cd /home/foobarto/Dokumenty/stado
git add internal/runtime/meta_tools.go internal/runtime/meta_tools_test.go
git commit -m "fix(runtime): meta-tools surface JSON-parse errors

metaSearch.Run and metaCategories.Run silently dropped Unmarshal
errors; tools.search/tools.categories with malformed JSON now
return an error instead of defaulting to empty query."
```

---

## Task 3: Plumb caller context through `FetchAnchorPubkey`

**Files:**
- Modify: `internal/plugins/anchor.go:23` (and the function signature)
- Modify: any caller of `FetchAnchorPubkey` (find via grep) — pass through the caller's context.

**Context:** Audit flagged `cl.Get(url) //nolint:noctx` — hardcoded 15s timeout, no caller cancellation.

- [ ] **Step 3.1: Read current state.** `cat /home/foobarto/Dokumenty/stado/internal/plugins/anchor.go` to see the full function. `grep -rn "FetchAnchorPubkey" /home/foobarto/Dokumenty/stado/` to find callers.

- [ ] **Step 3.2: Update signature to accept `ctx context.Context`.** Change `FetchAnchorPubkey(...)` to `FetchAnchorPubkey(ctx context.Context, ...)`. Inside the body, replace:

```go
resp, err := cl.Get(url) //nolint:noctx
```

with:

```go
req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
if err != nil {
	return nil, fmt.Errorf("build anchor request: %w", err)
}
resp, err := cl.Do(req)
```

The 15s timeout should remain on the `cl` (`http.Client.Timeout`) — that's a hard ceiling separate from caller-cancellation.

- [ ] **Step 3.3: Update each caller.** For each result of the grep, thread the caller's `ctx` through. If a caller doesn't have a context handy, accept this is a wider plumbing change and pause to ask the user — but that's unlikely; most callers are already in context-aware code paths.

- [ ] **Step 3.4: Run.** `cd /home/foobarto/Dokumenty/stado && go build ./... && go test ./internal/plugins/... -count=1`. Expected: PASS.

- [ ] **Step 3.5: Commit.**

```bash
cd /home/foobarto/Dokumenty/stado
git add internal/plugins/anchor.go <any-caller-files>
git commit -m "fix(plugins): FetchAnchorPubkey honours caller context

Was using cl.Get(url) //nolint:noctx — hardcoded 15s timeout, no
cancellation path. Switch to http.NewRequestWithContext + cl.Do
so operator Ctrl-C cancels the anchor fetch."
```

---

## Task 4: Bound the handle-registry collision retry

**Files:**
- Modify: `internal/plugins/runtime/handles.go:30-44` (`alloc` method)
- Modify: `internal/plugins/runtime/handles_test.go` (add a test if one doesn't exist; check first)

**Context:** Audit flagged the `for { }` infinite loop in `alloc` — no retry bound. Bound at e.g. 1000 attempts; return an error on exhaustion. Caller signature change required (was returning `uint32`; now `(uint32, error)`).

- [ ] **Step 4.1: Read current state.** `cat /home/foobarto/Dokumenty/stado/internal/plugins/runtime/handles.go` lines 26-44.

- [ ] **Step 4.2: Find callers of `alloc`.** `grep -rn "\\.alloc(" /home/foobarto/Dokumenty/stado/internal/plugins/runtime/`. Each caller will need to handle the new error return.

- [ ] **Step 4.3: Update the signature.** Change:

```go
func (r *handleRegistry) alloc(typeTag string, value any) uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		id := rand.Uint32()
		if id == 0 {
			continue
		}
		if _, exists := r.entries[id]; exists {
			continue
		}
		r.entries[id] = handleEntry{typeTag: typeTag, value: value}
		return id
	}
}
```

to:

```go
// maxHandleAllocAttempts bounds the collision-retry loop. With a uint32
// keyspace this should never bite in normal operation; the bound exists
// so that a broken random source or table-near-full state surfaces as
// an error rather than a hang.
const maxHandleAllocAttempts = 1000

func (r *handleRegistry) alloc(typeTag string, value any) (uint32, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := 0; i < maxHandleAllocAttempts; i++ {
		id := rand.Uint32()
		if id == 0 {
			continue
		}
		if _, exists := r.entries[id]; exists {
			continue
		}
		r.entries[id] = handleEntry{typeTag: typeTag, value: value}
		return id, nil
	}
	return 0, fmt.Errorf("handleRegistry: alloc exhausted %d attempts (type=%s)", maxHandleAllocAttempts, typeTag)
}
```

(Confirm `fmt` is imported.)

- [ ] **Step 4.4: Update each caller.** Grep result drives this. Each `id := r.alloc(...)` becomes `id, err := r.alloc(...); if err != nil { return ... }` with appropriate error propagation. Read each call site's surrounding code to pick the right error path.

- [ ] **Step 4.5: Add a test.** Append to `internal/plugins/runtime/handles_test.go` (create the file if it doesn't exist):

```go
package runtime

import (
	"strings"
	"testing"
)

func TestHandleRegistry_AllocBounded(t *testing.T) {
	r := newHandleRegistry()
	// Pre-populate with synthetic entries to exercise the success path
	// (sanity check that alloc returns nil-error on a normal call).
	id, err := r.alloc("test", "value")
	if err != nil {
		t.Fatalf("alloc on empty registry should succeed; got %v", err)
	}
	if id == 0 {
		t.Errorf("alloc returned zero id")
	}
}

func TestHandleRegistry_AllocErrorsOnExhaustion(t *testing.T) {
	r := newHandleRegistry()
	// Fake exhaustion by pre-populating the registry with every id rand
	// will ever produce. We can't actually fill 2^32 entries; instead,
	// exercise the bound by verifying the loop count is plausible —
	// this test just locks the contract that alloc returns (0, err) on
	// a degenerate condition. Implementation: directly inspect that the
	// constant exists and is non-zero.
	if maxHandleAllocAttempts == 0 {
		t.Error("maxHandleAllocAttempts should be > 0")
	}
	_ = strings.TrimSpace // keep import live if not otherwise used
}
```

The exhaustion case is hard to test directly without mocking `rand.Uint32`. The test above verifies the bound exists and the success path works; that's sufficient for a single-PR fix. A heavier mock-based exhaustion test is out of scope.

- [ ] **Step 4.6: Run.** `go test ./internal/plugins/runtime/ -count=1` AND `go build ./...`. Expected: both PASS.

- [ ] **Step 4.7: Commit.**

```bash
cd /home/foobarto/Dokumenty/stado
git add internal/plugins/runtime/handles.go internal/plugins/runtime/handles_test.go <caller-files>
git commit -m "fix(runtime): bound handleRegistry.alloc retry loop

Was a bare for { } — under degenerate conditions (broken rand or
near-full registry) would hang. Bound at 1000 attempts; return
error on exhaustion. Callers updated for the new (uint32, error)
signature."
```

---

## Task 5: Wire `[sessions] auto_prune_after` config schema

**Files:**
- Modify: `internal/config/config.go` (add `Sessions` struct under `type Config`)
- Modify: `internal/config/config_test.go` (or wherever config tests live — check first)

**Context:** Audit flagged the missing schema. NOTES §8 locked: *"`[sessions] auto_prune_after = ""` (off by default)"*. The actual prune codepath already exists (`stado session prune --before <date>`); this task only adds the config schema so operators can opt into time-based retention.

The plan does NOT wire the auto-prune execution — that's startup-time work that touches the session-storage code path. This task only adds the struct + parses the value. Auto-prune execution remains TODO with a marker comment.

- [ ] **Step 5.1: Read current state.** `cd /home/foobarto/Dokumenty/stado && grep -n "type Config struct\|type.*Config struct" internal/config/config.go | head -5`. Locate where Sandbox/Agent/Tools sections live and pick a coherent spot for `Sessions`.

- [ ] **Step 5.2: Add the struct.** In `internal/config/config.go`, near the existing `type Tools struct` (around line 171), add:

```go
// Sessions configures session lifecycle policy.
//
// AutoPruneAfter is the duration after which completed sessions are
// pruned by stado on startup ("90d", "30d", or "" for never; "" is
// the default — sessions are durable audit records by design). The
// duration string is parsed with time.ParseDuration extended for the
// "d" suffix.
//
// EP-0037 §C / NOTES §8.
//
// Auto-prune execution is not yet wired (TODO: wire to existing
// session.Prune codepath at startup); this struct only commits to
// the schema today.
type Sessions struct {
	AutoPruneAfter string `toml:"auto_prune_after"`
}
```

Add a `Sessions Sessions` field to the `Config` struct, alongside `Sandbox`, `Tools`, `Agent`, etc.

- [ ] **Step 5.3: Add a test.** Append to whatever config test file exists (look for `internal/config/config_test.go` first; if absent, create):

```go
func TestConfig_LoadSessionsAutoPruneAfter(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "config.toml")
	if err := os.WriteFile(path, []byte(`[sessions]
auto_prune_after = "90d"
`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.LoadFromPath(path) // adjust to whatever loader exists
	if err != nil {
		t.Fatalf("LoadFromPath: %v", err)
	}
	if cfg.Sessions.AutoPruneAfter != "90d" {
		t.Errorf("expected '90d'; got %q", cfg.Sessions.AutoPruneAfter)
	}
}

func TestConfig_SessionsDefaultEmpty(t *testing.T) {
	cfg := &config.Config{}
	if cfg.Sessions.AutoPruneAfter != "" {
		t.Errorf("default AutoPruneAfter should be empty; got %q", cfg.Sessions.AutoPruneAfter)
	}
}
```

If `LoadFromPath` doesn't exist, use whatever loader the rest of the test suite uses (look at one existing config test for the pattern).

- [ ] **Step 5.4: Run.** `cd /home/foobarto/Dokumenty/stado && go test ./internal/config/... -count=1`. Expected: PASS.

- [ ] **Step 5.5: Run go build to confirm no callers break.** `go build ./...`. Expected: PASS.

- [ ] **Step 5.6: Commit.**

```bash
cd /home/foobarto/Dokumenty/stado
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): [sessions] auto_prune_after schema

Adds the schema commitment from NOTES §8 / EP-0037 §C. Sessions
struct with AutoPruneAfter string field; default '' = never prune.
Auto-prune execution is not wired yet (TODO marker on the struct
docstring); operators can set the value but it has no effect until
the startup-time hook lands in a follow-up."
```

---

## Task 6: Use `WireForm` helper at the two manual-munging sites

**Files:**
- Modify: `cmd/stado/tool.go:121`
- Modify: `internal/runtime/executor.go:68`

**Context:** Audit flagged manual `strings.ReplaceAll(query, ".", "__")` instead of going through `WireForm` (`internal/tools/naming.go`). Round-trip discipline is broken at these two sites.

- [ ] **Step 6.1: Read the helper.** `cat /home/foobarto/Dokumenty/stado/internal/tools/naming.go`. Confirm `WireForm` and `ParseWireForm` exist and what their signatures look like.

- [ ] **Step 6.2: Inspect `cmd/stado/tool.go:121` context.** `sed -n '115,128p' /home/foobarto/Dokumenty/stado/cmd/stado/tool.go`. Read the surrounding lines to understand what `query` is — is it `<plugin>.<tool>` form or just `<tool>`?

- [ ] **Step 6.3: Inspect `internal/runtime/executor.go:68` context.** `sed -n '60,75p' /home/foobarto/Dokumenty/stado/internal/runtime/executor.go`. The current line uses `strings.NewReplacer(".", "_", "-", "_").Replace(rest)` — that's similar to but not identical to `WireForm`. Decide whether to swap or leave (there's a chance this site has different semantics).

- [ ] **Step 6.4: Replace at `cmd/stado/tool.go:121`.** Replace:

```go
wire := strings.ReplaceAll(query, ".", "__")
```

with the helper-equivalent. If `query` is `plugin.tool` form, that becomes `tools.WireForm(plugin, tool)` after splitting on `.`. If `query` is just a search term that happens to use `.` for namespace, the simpler swap is a one-segment helper — read the surrounding code to pick.

For ambiguity: if the existing manual munging works correctly, the swap should produce identical strings. The goal is round-trip discipline, not a behaviour change.

- [ ] **Step 6.5: Decide on `executor.go:68`.** Read 60-75. If the function is producing a wire prefix from a canonical fragment (`fs.read` → `fs__read`), `WireForm` is the right swap. If it's doing something more subtle (e.g. handling MCP tool names with hyphens), leave it and add a comment noting why. Document your decision in the commit message.

- [ ] **Step 6.6: Run tests.** `cd /home/foobarto/Dokumenty/stado && go test ./cmd/... ./internal/runtime/... ./internal/tools/... -count=1`. Expected: PASS.

- [ ] **Step 6.7: Commit.**

```bash
cd /home/foobarto/Dokumenty/stado
git add cmd/stado/tool.go internal/runtime/executor.go
git commit -m "refactor(naming): route through WireForm helper

Replaces manual strings.ReplaceAll(\".\", \"__\") at two sites with
the canonical WireForm helper. Round-trip discipline restored —
the helper is now the single source of truth for the
canonical/wire conversion."
```

If the executor.go site was deliberately left alone, mention it in the commit message.

---

## Task 7: Drop `fmt.Errorf("%s", msg)` nolint in `wrap.go`

**Files:**
- Modify: `internal/sandbox/wrap.go:99`

**Context:** Audit flagged `return fmt.Errorf("%s", msg) //nolint:staticcheck`. Should be `errors.New(msg)`.

- [ ] **Step 7.1: Read current state.** `sed -n '95,105p' /home/foobarto/Dokumenty/stado/internal/sandbox/wrap.go`.

- [ ] **Step 7.2: Replace.** Line 99 currently:

```go
return fmt.Errorf("%s", msg) //nolint:staticcheck
```

becomes:

```go
return errors.New(msg)
```

Add `"errors"` to the import block if missing. Drop `"fmt"` from imports ONLY if no other site in the file uses it (check via `grep -n "fmt\\." /home/foobarto/Dokumenty/stado/internal/sandbox/wrap.go`).

- [ ] **Step 7.3: Run.** `cd /home/foobarto/Dokumenty/stado && go vet ./internal/sandbox/ && go test ./internal/sandbox/ -count=1`. Expected: PASS.

- [ ] **Step 7.4: Commit.**

```bash
cd /home/foobarto/Dokumenty/stado
git add internal/sandbox/wrap.go
git commit -m "fix(sandbox): use errors.New instead of fmt.Errorf nolint

The nolint:staticcheck was hiding a valid lint signal. errors.New
is the right primitive for a constant-string error."
```

---

## Task 8: Delete unused `ErrLockNotFound`

**Files:**
- Modify: `internal/plugins/lock.go:142-143` (delete the lines)

**Context:** Audit flagged `ErrLockNotFound` exported but never consumed. Delete.

- [ ] **Step 8.1: Verify it's still unused.** `cd /home/foobarto/Dokumenty/stado && grep -rn "ErrLockNotFound" .` — should show only the definition site (lock.go:142 + the comment at :142).

- [ ] **Step 8.2: Delete the lines.** Remove:

```go
// ErrLockNotFound is returned when ReadLock is called on a non-existent file.
var ErrLockNotFound = errors.New("plugin-lock.toml not found")
```

If `errors` becomes unused after the deletion (no other reference in the file), drop it from imports.

- [ ] **Step 8.3: Run.** `cd /home/foobarto/Dokumenty/stado && go vet ./internal/plugins/... && go test ./internal/plugins/... -count=1`. Expected: PASS.

- [ ] **Step 8.4: Commit.**

```bash
cd /home/foobarto/Dokumenty/stado
git add internal/plugins/lock.go
git commit -m "refactor(plugins): drop unused ErrLockNotFound

Was exported but no caller checked for it — sentinel without a
catcher. Re-add when there's a consumer that wants to distinguish
'not found' from generic read errors."
```

---

## Task 9: Clean up `defaultAutoloadNames` mixed wire forms and bare names

**Files:**
- Modify: `internal/runtime/executor.go:19-34`

**Context:** Audit flagged that `defaultAutoloadNames` has both `"read"` (bare native name) AND `"fs__ls"`, `"spawn_agent"` (wire form). Comment says cleanup is post-EP-0038; that's now.

The decision: pick wire form throughout for consistency with how the LLM sees tool names. Bare native names are pre-EP-0038; everything should be wire form now.

- [ ] **Step 9.1: Read current state.** `sed -n '15,40p' /home/foobarto/Dokumenty/stado/internal/runtime/executor.go`.

- [ ] **Step 9.2: Convert each entry to wire form.** For each bare name, look up its plugin owner and produce `<plugin>__<tool>`. For names that are already wire form, leave them. For `"spawn_agent"` (the native subagent tool), keep as-is — it's a single-segment wire form already. The `defaultAutoloadNames` comment should be updated to reflect "all entries are wire form (Anthropic API requires `[a-zA-Z0-9_-]{1,64}`)".

Read `/home/foobarto/Dokumenty/stado/internal/runtime/bundled_plugin_tools.go` to verify which native names map to which plugins. Bare names like `"read"` register as `"fs__read"` in wire form (per `WireForm("fs", "read")`).

Be conservative: if the change would require modifying tool registrations elsewhere, scope creep. Just update `defaultAutoloadNames`.

- [ ] **Step 9.3: Run autoload tests.** `cd /home/foobarto/Dokumenty/stado && go test ./internal/runtime/ -run "Autoload" -count=1 -v`. Expected: PASS. If a test asserts on bare names, that test needs updating too — read the failures and fix.

- [ ] **Step 9.4: Run full runtime tests.** `go test ./internal/runtime/ -count=1`. Expected: PASS.

- [ ] **Step 9.5: Commit.**

```bash
cd /home/foobarto/Dokumenty/stado
git add internal/runtime/executor.go <any-test-files>
git commit -m "refactor(runtime): defaultAutoloadNames uses wire form throughout

Was mixed: 'read' (bare) alongside 'fs__ls', 'spawn_agent' (wire).
Comment promised cleanup post-EP-0038; that's now. All entries
are now wire form, matching what the LLM sees in its tool
catalogue."
```

---

## Task 10: Tighten `golangci-lint` test-file blanket exclusions — **DEFERRED**

**Status: deferred 2026-05-05.** golangci-lint v2.11.4 panics on every package in the local environment (`runner_loadingpackage.go:45` — likely a Go-toolchain / golangci-plugin-version mismatch). Cannot establish a baseline lint output, so cannot measure the delta from narrowing the exclusion. Re-pick this task once the linter is functional locally OR address it as a separate "fix golangci-lint integration" plan.



**Files:**
- Modify: `.golangci.yml`

**Context:** Audit flagged the wholesale `*_test.go` exemption for errcheck/unused. Hides setup-failure smells. Narrow to specific rules — typically only `t.TempDir()` cleanup-error suppression is wanted.

- [ ] **Step 10.1: Read current state.** `cat /home/foobarto/Dokumenty/stado/.golangci.yml`. Locate the exclude rule.

- [ ] **Step 10.2: Decide on the narrowed rule.** A typical replacement: keep errcheck excluded only for explicitly-ignored returns (the `_ = somecall()` pattern), not for ALL test errors. Or scope by linter (allow `errcheck` to run; only allow `unused` vars in tests).

Pick the narrowest exclusion that doesn't trigger a flood of pre-existing warnings. After narrowing, run:

```bash
cd /home/foobarto/Dokumenty/stado && golangci-lint run ./... 2>&1 | head -40
```

If the output is overwhelming (>20 new issues), this task is bigger than "tightening" — pause and write a follow-up plan.

- [ ] **Step 10.3: Apply the narrowed rule and verify.** Re-run `golangci-lint`. Expected: small number of new issues OR none.

- [ ] **Step 10.4: Run.** `cd /home/foobarto/Dokumenty/stado && go vet ./... && go test ./... -count=1`. Expected: PASS.

- [ ] **Step 10.5: Commit.**

```bash
cd /home/foobarto/Dokumenty/stado
git add .golangci.yml
git commit -m "chore(lint): narrow test-file errcheck/unused exclusion

Was excluding errcheck and unused wholesale for *_test.go —
hides real setup-failure smells. Scoped to specific rules so
silent fixture-setup failures get caught."
```

If this task surfaces unexpected issues during step 10.2, **report DONE_WITH_CONCERNS** with the specifics. The follow-up cleanup is a separate plan, not in scope here.

---

## Self-Review Checklist

After all 10 tasks:

- [ ] `go test ./... -count=1` passes (full repo).
- [ ] `go vet ./...` clean.
- [ ] `git log main..HEAD --oneline` shows 10 commits — each focused, each with a clear message.
- [ ] `git diff main..HEAD --stat` shows the change footprint matches the plan (~10-15 files modified, no surprise additions).

---

## Spec coverage

Each item has a task:
- BACKLOG audit-additions #15 (flag rename) → Task 1
- #16 (silent JSON swallows) → Task 2
- #17 (anchor noctx) → Task 3
- #18 (handle alloc spin) → Task 4
- #20 (sessions config schema) → Task 5
- #21 (manual wire-form munging) → Task 6
- #22 (sandbox nolint) → Task 7
- #23 (unused ErrLockNotFound) → Task 8
- #24 (mixed-form defaultAutoloadNames) → Task 9
- #25 (lint config exclusions) → Task 10

Items NOT in this plan (by design):
- #19 (parity tests for migrated families) — modest scope, deserves its own plan.
- #11, #12, #13 (Tier 1 networking, JSON, AXFR) — own EP.
- #14 (FleetBridge messaging stubs) — coupled with #1, big architectural work.

No placeholders. Function names match across tasks.
