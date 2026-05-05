# TUI slash mutating verbs + typed-prefix handle IDs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wire `/tool enable/disable/autoload/unautoload` slash verbs into the TUI with per-session-by-default semantics + `--save` to persist; and replace bare numeric handle IDs in `/ps` and `/kill` with the locked typed-prefix dotted format (`agent:bf3e`, `proc:fs.7a2b`).

**Architecture:** Two cohesive changes that share `internal/tui/model_commands.go`. (1) A new `sessionToolOverrides` field on `Model` provides an in-memory override layer that's merged with `m.cfg.Tools` when computing the live tool surface — slash mutations write here by default; `--save` additionally calls the existing `config.WriteToolsList{Add,Remove}` helpers. (2) Two new pure helpers `FormatHandleID` / `ParseHandleID` in `internal/plugins/runtime/handles.go` define the canonical typed-prefix string form; `/ps` renderer and `/kill` parser both go through them. No wire-format changes — only operator-facing rendering.

**Tech Stack:** Go 1.22+, cobra, BubbleTea TUI, existing `runtime.Fleet` for live agents, existing `config.WriteToolsList*` helpers in `internal/config/write_defaults.go`.

**Locked decisions** (from `~/.claude/projects/-home-foobarto-Dokumenty-stado/memory/post-audit-design-calls.md`):
- Slash = per-session non-persistent; `--save` flips to write config (writes immediately, no pending state).
- Typed-prefix at operator surface only; internal handle table stays uint32.

**Scope OUT (explicitly):**
- Expanding `/ps` to enumerate proc/term/conn handles from `handleRegistry` (deferred — needs Model→Runtime plumbing that isn't there today; left for a follow-up).
- BACKLOG #18 handle-registry retry bound (same file as this plan touches but separate concern; cleanup pass).
- Tier 1 `conn:` net handles (deferred — Tier 1 networking primitives not yet implemented per BACKLOG #11).

---

## File Structure

### Phase A — typed-prefix handle IDs (`internal/plugins/runtime/handles.go`)

- **Modify:** `internal/plugins/runtime/handles.go` — add two pure helpers (`FormatHandleID(typeTag string, h uint32) string`, `ParseHandleID(s string) (typeTag string, h uint32, err error)`) plus a small `HandleType` type. No change to `alloc/get/free/isType`.
- **Create:** `internal/plugins/runtime/handles_format_test.go` — round-trip + error-path tests.

### Phase B — `/ps` and `/kill` use typed format (`internal/tui/model_commands.go`)

- **Modify:** `internal/tui/model_commands.go` lines 1366-1392 (`renderPS`, `min8`) and 1394-1412 (`handleKillSlash`) — rewrite to use the new helpers. Drop the one-off `strings.TrimPrefix(id, "agent:")` at line 1402.
- **Create:** `internal/tui/handle_ids_test.go` — table-driven tests for `/ps` rendering and `/kill` parsing.

### Phase C — session tool overrides + slash mutating verbs

- **Modify:** `internal/tui/model.go` (or wherever `type Model struct` lives) — add `sessionToolOverrides` field.
- **Modify:** `internal/tui/model_commands.go` `handleToolSlash` (line 455) — add four new verbs (`enable`, `disable`, `autoload`, `unautoload`) plus `--save` flag parsing. Update `case "ls"` (line 461) to reflect overrides in the listing.
- **Modify:** `internal/runtime/executor.go` `ApplyToolFilter` (line 122) and `AutoloadedTools` (line 89) — accept an *additional* override snapshot OR reorganise so the TUI can compose effective config without round-tripping disk. Plan keeps the disk-config function as-is and adds an in-TUI helper that produces a merged `config.Config` view.
- **Create:** `internal/tui/session_tool_overrides.go` — small file holding the override struct + `effectiveTools(*config.Config) config.ToolsConfig` merge function.
- **Create:** `internal/tui/session_tool_overrides_test.go` — merge-correctness tests.
- **Create:** `internal/tui/slash_tool_verbs_test.go` — slash verb behaviour (mutates session state, `--save` calls config writer with right path).

---

## Task 1: Add `FormatHandleID` / `ParseHandleID` helpers

**Files:**
- Modify: `internal/plugins/runtime/handles.go`
- Create: `internal/plugins/runtime/handles_format_test.go`

The handle ID convention from NOTES section 13:
```
plugin:fs              # plugin instance
proc:fs.7a2b           # proc handle owned by fs, registry id 7a2b
term:shell.9c1d
agent:bf3e             # agent (FleetID, not registry uint32)
session:abc12345
conn:web.4f5a
listen:browser.8a91
```

Two ID shapes:
- **Owned-by-plugin** (`proc:fs.7a2b`, `term:shell.9c1d`, `conn:web.4f5a`, `listen:browser.8a91`): `<type>:<plugin>.<hex8>` — uint32 lower-cased hex, padded to 8 chars but trimmed if leading zeros.
- **Free-standing** (`agent:bf3e`, `session:abc12345`, `plugin:fs`): `<type>:<id>` — `id` is whatever the producer chose (FleetID short prefix, SessionID short prefix, plugin name).

Helpers normalise to the *owned-by-plugin* shape only when both `plugin` and a uint32 are present. The free-standing shape uses a separate helper.

- [ ] **Step 1.1: Read `handles.go` and write the failing test file**

```go
// internal/plugins/runtime/handles_format_test.go
package runtime

import (
	"testing"
)

func TestFormatHandleID_OwnedByPlugin(t *testing.T) {
	got := FormatHandleID(HandleTypeProc, "fs", 0x7a2b)
	want := "proc:fs.7a2b"
	if got != want {
		t.Errorf("FormatHandleID(proc, fs, 0x7a2b) = %q, want %q", got, want)
	}
}

func TestFormatHandleID_LongerHexNotPadded(t *testing.T) {
	got := FormatHandleID(HandleTypeTerminal, "shell", 0x9c1d)
	want := "term:shell.9c1d"
	if got != want {
		t.Errorf("FormatHandleID(term, shell, 0x9c1d) = %q, want %q", got, want)
	}
}

func TestFormatHandleID_FullUint32(t *testing.T) {
	got := FormatHandleID(HandleTypeProc, "x", 0xdeadbeef)
	want := "proc:x.deadbeef"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatHandleID_EmptyPlugin(t *testing.T) {
	// Empty plugin → omit the dotted owner; result is "<type>:<hex>".
	got := FormatHandleID(HandleTypeProc, "", 0x42)
	want := "proc:42"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatFreeStandingHandleID(t *testing.T) {
	got := FormatFreeStandingHandleID(HandleTypeAgent, "bf3eabcdef")
	want := "agent:bf3eabcd" // trimmed to 8 chars
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatFreeStandingHandleID_ShortID(t *testing.T) {
	got := FormatFreeStandingHandleID(HandleTypeSession, "abc")
	want := "session:abc"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestParseHandleID_OwnedByPlugin(t *testing.T) {
	typ, plugin, h, err := ParseHandleID("proc:fs.7a2b")
	if err != nil {
		t.Fatalf("ParseHandleID error: %v", err)
	}
	if typ != HandleTypeProc {
		t.Errorf("type = %q, want %q", typ, HandleTypeProc)
	}
	if plugin != "fs" {
		t.Errorf("plugin = %q, want %q", plugin, "fs")
	}
	if h != 0x7a2b {
		t.Errorf("h = %#x, want %#x", h, 0x7a2b)
	}
}

func TestParseHandleID_FreeStanding(t *testing.T) {
	typ, plugin, h, err := ParseHandleID("agent:bf3e")
	if err != nil {
		t.Fatalf("ParseHandleID error: %v", err)
	}
	if typ != HandleTypeAgent {
		t.Errorf("type = %q, want %q", typ, HandleTypeAgent)
	}
	if plugin != "" {
		t.Errorf("plugin should be empty for free-standing; got %q", plugin)
	}
	if h != 0 {
		t.Errorf("h should be 0 for free-standing; got %#x", h)
	}
}

func TestParseHandleID_BareNumericRejected(t *testing.T) {
	if _, _, _, err := ParseHandleID("123456"); err == nil {
		t.Error("ParseHandleID(\"123456\") should fail — needs a type prefix")
	}
}

func TestParseHandleID_UnknownType(t *testing.T) {
	if _, _, _, err := ParseHandleID("nope:fs.1"); err == nil {
		t.Error("unknown type prefix should fail")
	}
}

func TestParseHandleID_RoundTrip(t *testing.T) {
	cases := []struct {
		typ    HandleType
		plugin string
		h      uint32
	}{
		{HandleTypeProc, "fs", 0x7a2b},
		{HandleTypeTerminal, "shell", 0x9c1d},
		{HandleTypeProc, "", 0x42},
	}
	for _, c := range cases {
		s := FormatHandleID(c.typ, c.plugin, c.h)
		typ, plugin, h, err := ParseHandleID(s)
		if err != nil {
			t.Errorf("round-trip %q: parse failed: %v", s, err)
			continue
		}
		if typ != c.typ || plugin != c.plugin || h != c.h {
			t.Errorf("round-trip %q: got (%q,%q,%#x), want (%q,%q,%#x)",
				s, typ, plugin, h, c.typ, c.plugin, c.h)
		}
	}
}
```

- [ ] **Step 1.2: Run the test file, verify compilation fails**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/plugins/runtime/ -run TestFormatHandleID -count=1`
Expected: build failure with `undefined: FormatHandleID`, `undefined: HandleType`, etc.

- [ ] **Step 1.3: Add the implementation to `handles.go`**

Append to `/home/foobarto/Dokumenty/stado/internal/plugins/runtime/handles.go`:

```go
// HandleType is the canonical type-tag string used in operator-facing
// handle IDs (FormatHandleID / ParseHandleID).  These are *not* the
// internal type tags stored in handleEntry; the internal tags can be
// anything the producer chooses, as long as they round-trip
// consistently. The strings here are the public, documented form
// (NOTES §13, EP-0038 §H).
type HandleType string

const (
	HandleTypeProc     HandleType = "proc"
	HandleTypeTerminal HandleType = "term"
	HandleTypeAgent    HandleType = "agent"
	HandleTypeSession  HandleType = "session"
	HandleTypePlugin   HandleType = "plugin"
	HandleTypeConn     HandleType = "conn"   // reserved — Tier 1 net (BACKLOG #11)
	HandleTypeListen   HandleType = "listen" // reserved — Tier 1 net (BACKLOG #11)
)

var knownHandleTypes = map[HandleType]bool{
	HandleTypeProc: true, HandleTypeTerminal: true, HandleTypeAgent: true,
	HandleTypeSession: true, HandleTypePlugin: true, HandleTypeConn: true,
	HandleTypeListen: true,
}

// FormatHandleID renders a typed handle ID for an *owned* handle —
// one allocated by handleRegistry on behalf of a named plugin
// instance.  Format: "<type>:<plugin>.<hex>" (e.g. "proc:fs.7a2b").
// When plugin is empty the dotted owner is omitted: "<type>:<hex>".
// hex is lower-case, no leading zero padding (matches Go's %x).
func FormatHandleID(typ HandleType, plugin string, h uint32) string {
	if plugin == "" {
		return fmt.Sprintf("%s:%x", typ, h)
	}
	return fmt.Sprintf("%s:%s.%x", typ, plugin, h)
}

// FormatFreeStandingHandleID renders a typed handle ID for IDs that
// don't live in handleRegistry — agents (FleetID), sessions
// (stadogit session id), plugin instances (plugin name).  The id is
// trimmed to 8 characters when longer (operator readability;
// matches the existing min8 convention in /ps output).
func FormatFreeStandingHandleID(typ HandleType, id string) string {
	if len(id) > 8 {
		id = id[:8]
	}
	return fmt.Sprintf("%s:%s", typ, id)
}

// ParseHandleID parses an operator-facing handle ID into its parts.
// Returns (type, plugin, hex-handle, err).  For free-standing IDs
// (agent:, session:, plugin:) plugin is "" and h is 0; the caller
// must look up the id by string in the appropriate registry.
//
// Rejects:
//   - bare numerics ("123") — must have a type prefix.
//   - unknown type prefixes.
//   - hex segments that don't fit in uint32.
func ParseHandleID(s string) (HandleType, string, uint32, error) {
	colon := strings.IndexByte(s, ':')
	if colon < 0 {
		return "", "", 0, fmt.Errorf("handle ID %q: missing type prefix (expected e.g. proc:fs.7a2b or agent:bf3e)", s)
	}
	typ := HandleType(s[:colon])
	rest := s[colon+1:]
	if !knownHandleTypes[typ] {
		return "", "", 0, fmt.Errorf("handle ID %q: unknown type %q", s, typ)
	}
	// Owned form: "<plugin>.<hex>".
	if dot := strings.IndexByte(rest, '.'); dot >= 0 {
		plugin := rest[:dot]
		hexStr := rest[dot+1:]
		v, err := strconv.ParseUint(hexStr, 16, 32)
		if err != nil {
			return "", "", 0, fmt.Errorf("handle ID %q: hex segment %q invalid: %w", s, hexStr, err)
		}
		return typ, plugin, uint32(v), nil
	}
	// Free-standing form: "<id>".  Don't try to parse as hex —
	// FleetID / SessionID / plugin names are opaque strings.
	return typ, "", 0, nil
}
```

Add to imports at top of `handles.go`:

```go
import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
)
```

- [ ] **Step 1.4: Run tests, verify all pass**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/plugins/runtime/ -run TestFormatHandleID -run TestParseHandleID -run TestFormat -count=1 -v`
Expected: PASS for all `TestFormatHandleID_*`, `TestFormatFreeStandingHandleID*`, `TestParseHandleID_*`.

- [ ] **Step 1.5: Run full package tests to verify no regressions**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/plugins/runtime/ -count=1`
Expected: PASS.

- [ ] **Step 1.6: Commit**

```bash
cd /home/foobarto/Dokumenty/stado
git add internal/plugins/runtime/handles.go internal/plugins/runtime/handles_format_test.go
git commit -m "feat(runtime): typed-prefix handle ID formatter/parser (BACKLOG #7)

Adds Format/ParseHandleID and FormatFreeStandingHandleID per the
NOTES §13 typed-prefix convention. Operator-facing only; internal
handle table stays uint32. Reserved conn:/listen: types kept for
when Tier 1 networking lands (BACKLOG #11)."
```

---

## Task 2: `/ps` uses `FormatFreeStandingHandleID`

**Files:**
- Modify: `internal/tui/model_commands.go:1366-1392`
- Create: `internal/tui/handle_ids_test.go`

Today `renderPS` uses `min8(e.FleetID)` and `min8(e.SessionID)` to truncate, then formats with `agent:%s` / `  session:%s` strings. The change: route through `FormatFreeStandingHandleID` so the format is centralised.

- [ ] **Step 2.1: Write the failing test**

```go
// internal/tui/handle_ids_test.go
package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/plugins/runtime/runtime" // alias if package is also "runtime"
	rt "github.com/foobarto/stado/internal/runtime"
)

// TestRenderPS_UsesTypedPrefix confirms /ps output uses the
// typed-prefix format ("agent:bf3e", "session:abc12345") rather
// than bare ID strings.
func TestRenderPS_UsesTypedPrefix(t *testing.T) {
	fleet := rt.NewFleet()
	// Inject one running entry so renderPS has something to render.
	id := fleet.Spawn(rt.SpawnOptions{}, "test-prompt")
	if id == "" {
		t.Fatal("Spawn returned empty FleetID")
	}
	// Bump status to "running" via the fleet's normal path — this
	// depends on Fleet's API; if direct status mutation isn't
	// exposed, skip and assert on Spawn defaults.

	m := &Model{fleet: fleet}
	out := m.renderPS(false)

	if !strings.Contains(out, "agent:") {
		t.Errorf("renderPS output should contain 'agent:' typed prefix; got:\n%s", out)
	}
	// Ensure no bare numeric IDs (the old format).
	for _, line := range strings.Split(out, "\n") {
		// Bare numeric would be a line starting with a digit
		// rather than "agent:" / "session:" / "ID" header.
		first := strings.TrimSpace(line)
		if first == "" || strings.HasPrefix(first, "ID") || strings.HasPrefix(first, "agent:") || strings.HasPrefix(first, "session:") {
			continue
		}
		// "ps:" prefix is the no-fleet/no-agents zero-state lines.
		if strings.HasPrefix(first, "ps:") {
			continue
		}
		t.Errorf("renderPS unexpected line shape: %q", first)
	}
	_ = time.Now() // keep import live
}

func TestHandleKillSlash_AcceptsTypedPrefix(t *testing.T) {
	fleet := rt.NewFleet()
	id := fleet.Spawn(rt.SpawnOptions{}, "test-prompt")
	if id == "" {
		t.Fatal("Spawn returned empty FleetID")
	}
	m := &Model{fleet: fleet}
	// Form 1: typed prefix
	m.handleKillSlash([]string{"/kill", "agent:" + id[:8]})
	// Form 2: bare ID — also accepted for back-compat
	id2 := fleet.Spawn(rt.SpawnOptions{}, "test-prompt-2")
	m2 := &Model{fleet: fleet}
	m2.handleKillSlash([]string{"/kill", id2})

	// Successful Cancel manifests as the entry's status moving to
	// FleetStatusCancelled; we don't assert on m.blocks here since
	// the system message format may evolve.
	entries := fleet.List()
	cancelled := 0
	for _, e := range entries {
		if e.Status == rt.FleetStatusCancelled {
			cancelled++
		}
	}
	if cancelled < 1 {
		t.Errorf("expected at least 1 cancelled entry; got %d (entries: %+v)", cancelled, entries)
	}
}
```

- [ ] **Step 2.2: Run test to verify it fails**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/tui/ -run TestRenderPS_UsesTypedPrefix -run TestHandleKillSlash -count=1 -v`
Expected: FAIL — likely on `Spawn` signature mismatch or no current state being injected; this test will need adjustment after looking at `runtime.Fleet.Spawn`'s actual signature.

If `Fleet.Spawn` doesn't exist with that exact signature, simplify the test to construct `runtime.FleetEntry` values via a fake fleet wrapper or a builder — the test author should match the API. Read `internal/runtime/fleet.go:128-180` for the actual `Spawn` signature first.

**Note for the implementer:** if direct construction is hard, replace the fleet-driven test with a unit test of a smaller renderer helper extracted from `renderPS`. Either approach is fine — what matters is that the test fails before edits and passes after.

- [ ] **Step 2.3: Modify `renderPS` to use `FormatFreeStandingHandleID`**

Replace `internal/tui/model_commands.go:1366-1392`:

```go
// renderPS formats /ps output: live fleet agents + handles. EP-0038 §H.
func (m *Model) renderPS(_ bool) string {
	if m.fleet == nil {
		return "ps: no fleet registry (not in an agent session)"
	}
	entries := m.fleet.List()
	if len(entries) == 0 {
		return "ps: no agents running"
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%-20s %-12s %-20s %s\n", "ID", "STATUS", "MODEL", "STARTED"))
	for _, e := range entries {
		age := time.Since(e.StartedAt).Round(time.Second).String()
		agentID := pluginsruntime.FormatFreeStandingHandleID(pluginsruntime.HandleTypeAgent, e.FleetID)
		sb.WriteString(fmt.Sprintf("%-20s %-12s %-20s %s ago\n",
			agentID, string(e.Status), e.Model, age))
		if e.SessionID != "" {
			sessionID := pluginsruntime.FormatFreeStandingHandleID(pluginsruntime.HandleTypeSession, e.SessionID)
			sb.WriteString(fmt.Sprintf("  %-18s driver\n", sessionID))
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}
```

Add import for `pluginsruntime "github.com/foobarto/stado/internal/plugins/runtime"` (use whatever alias is convenient — match what already exists at the top of model_commands.go for the package).

- [ ] **Step 2.4: Modify `handleKillSlash` to use `ParseHandleID` (with bare-ID fallback)**

Replace `internal/tui/model_commands.go:1394-1412`:

```go
// handleKillSlash handles /kill <id>. EP-0038 §H.
// Accepts:
//   - typed-prefix form: "agent:bf3e" (preferred, matches /ps output)
//   - bare ID:           "bf3e..."   (back-compat — copy-paste-friendly)
func (m *Model) handleKillSlash(parts []string) {
	if len(parts) < 2 {
		m.appendBlock(block{kind: "system", body: "/kill <agent-id>  — cancel a running agent"})
		return
	}
	raw := parts[1]
	id := raw
	if typ, _, _, err := pluginsruntime.ParseHandleID(raw); err == nil {
		// Typed-prefix form parsed cleanly. Only "agent:" is
		// kill-routable today; proc:/term: don't have cancel paths
		// hooked into the TUI yet.
		if typ != pluginsruntime.HandleTypeAgent {
			m.appendBlock(block{kind: "system", body: fmt.Sprintf("kill: %s handles aren't kill-routable from /kill yet", typ)})
			return
		}
		// Strip the "agent:" prefix to get the FleetID-or-prefix.
		id = strings.TrimPrefix(raw, "agent:")
	}
	if m.fleet == nil {
		m.appendBlock(block{kind: "system", body: "kill: no fleet registry"})
		return
	}
	if err := m.fleet.Cancel(id); err != nil {
		m.appendBlock(block{kind: "system", body: fmt.Sprintf("kill %s: %v", id, err)})
		return
	}
	m.appendBlock(block{kind: "system", body: fmt.Sprintf("kill: cancelled agent %s", id)})
}
```

Note: this removes `min8` from `renderPS` (the truncation now happens inside `FormatFreeStandingHandleID`). Search the file for any other `min8` callers; if `min8` is used elsewhere, leave it. If only `renderPS` uses it, remove the function declaration too.

- [ ] **Step 2.5: Run focused tests, verify pass**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/tui/ -run TestRenderPS_UsesTypedPrefix -run TestHandleKillSlash -count=1 -v`
Expected: PASS.

- [ ] **Step 2.6: Run package tests for regressions**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/tui/ -count=1`
Expected: PASS for all existing tests too.

- [ ] **Step 2.7: Commit**

```bash
cd /home/foobarto/Dokumenty/stado
git add internal/tui/model_commands.go internal/tui/handle_ids_test.go
git commit -m "feat(tui): /ps and /kill use typed-prefix handle IDs (BACKLOG #7)

renderPS now formats via FormatFreeStandingHandleID; handleKillSlash
parses via ParseHandleID with bare-ID back-compat. Centralises the
typed-prefix convention; internal handle table unchanged."
```

---

## Task 3: Add session tool overrides to Model

**Files:**
- Create: `internal/tui/session_tool_overrides.go`
- Create: `internal/tui/session_tool_overrides_test.go`
- Modify: `internal/tui/model.go` (or wherever `type Model struct` lives — locate via `grep -n "type Model struct" internal/tui/*.go` first)

The override struct holds in-memory edits to `[tools].enabled`, `[tools].disabled`, `[tools].autoload`. A merge function produces an effective `config.ToolsConfig` snapshot the rest of the TUI can use without modifying disk-backed config.

- [ ] **Step 3.1: Locate the Model struct definition**

Run: `cd /home/foobarto/Dokumenty/stado && grep -rn "type Model struct" internal/tui/ | head -5`

Read whichever file owns the struct so the field can be added with the right ordering / json tags.

- [ ] **Step 3.2: Write failing tests for the override struct**

Create `internal/tui/session_tool_overrides_test.go`:

```go
package tui

import (
	"reflect"
	"sort"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

// TestEffectiveTools_NoOverrides returns the disk config unchanged.
func TestEffectiveTools_NoOverrides(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Enabled = []string{"fs.read"}
	cfg.Tools.Disabled = []string{"shell.exec"}
	cfg.Tools.Autoload = []string{"fs.*"}
	ov := sessionToolOverrides{}

	got := ov.effectiveTools(cfg)
	want := cfg.Tools
	if !reflect.DeepEqual(got, want) {
		t.Errorf("effectiveTools without overrides should equal cfg.Tools\n got: %+v\nwant: %+v", got, want)
	}
}

// TestEffectiveTools_EnableOverride: session adds to enabled.
func TestEffectiveTools_EnableOverride(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Enabled = []string{"fs.read"}
	ov := sessionToolOverrides{enableAdd: []string{"shell.exec"}}

	got := ov.effectiveTools(cfg)
	sort.Strings(got.Enabled)
	want := []string{"fs.read", "shell.exec"}
	if !reflect.DeepEqual(got.Enabled, want) {
		t.Errorf("enable add: got %v, want %v", got.Enabled, want)
	}
}

// TestEffectiveTools_DisableRemovesFromEnabled.
func TestEffectiveTools_DisableRemovesFromEnabled(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Enabled = []string{"fs.read", "shell.exec"}
	ov := sessionToolOverrides{disableAdd: []string{"shell.exec"}}

	got := ov.effectiveTools(cfg)
	for _, n := range got.Enabled {
		if n == "shell.exec" {
			t.Errorf("disabled tool should be removed from Enabled; got %v", got.Enabled)
		}
	}
	if !contains(got.Disabled, "shell.exec") {
		t.Errorf("disabled tool should appear in Disabled; got %v", got.Disabled)
	}
}

// TestEffectiveTools_AutoloadAddRemove: in-memory autoload changes.
func TestEffectiveTools_AutoloadAddRemove(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Autoload = []string{"fs.*"}
	ov := sessionToolOverrides{
		autoloadAdd:    []string{"shell.exec"},
		autoloadRemove: []string{"fs.*"},
	}

	got := ov.effectiveTools(cfg)
	if contains(got.Autoload, "fs.*") {
		t.Errorf("autoload remove should drop fs.*; got %v", got.Autoload)
	}
	if !contains(got.Autoload, "shell.exec") {
		t.Errorf("autoload add should include shell.exec; got %v", got.Autoload)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
```

- [ ] **Step 3.3: Run tests, verify they fail to compile**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/tui/ -run TestEffectiveTools -count=1 -v`
Expected: build failure with `undefined: sessionToolOverrides`.

- [ ] **Step 3.4: Add implementation**

Create `internal/tui/session_tool_overrides.go`:

```go
package tui

import (
	"github.com/foobarto/stado/internal/config"
)

// sessionToolOverrides holds in-memory edits to the [tools] section
// produced by /tool enable/disable/autoload/unautoload slash verbs
// without --save.  effectiveTools merges them with a disk-backed
// config.Config to produce a transient view the runtime can use to
// recompute autoloaded / filtered tool surfaces, without writing
// anything to disk.
//
// Slash mutations with --save bypass this struct entirely and call
// config.WriteToolsList{Add,Remove} directly; the Model's field
// stays at its zero value.
type sessionToolOverrides struct {
	enableAdd      []string
	enableRemove   []string
	disableAdd     []string
	disableRemove  []string
	autoloadAdd    []string
	autoloadRemove []string
}

// effectiveTools produces cfg.Tools as it would appear after
// applying the in-memory overrides.  cfg may be nil; the function
// returns a zero-value ToolsConfig populated with only the
// override-add lists in that case.
func (o sessionToolOverrides) effectiveTools(cfg *config.Config) config.ToolsConfig {
	var base config.ToolsConfig
	if cfg != nil {
		base = cfg.Tools
	}
	return config.ToolsConfig{
		Enabled:  applyOverride(base.Enabled, o.enableAdd, o.enableRemove),
		Disabled: applyOverride(base.Disabled, o.disableAdd, o.disableRemove),
		Autoload: applyOverride(base.Autoload, o.autoloadAdd, o.autoloadRemove),
	}
}

// applyOverride returns base ∪ adds \ removes, preserving original
// order and skipping duplicates.
func applyOverride(base, adds, removes []string) []string {
	out := make([]string, 0, len(base)+len(adds))
	skip := map[string]bool{}
	for _, r := range removes {
		skip[r] = true
	}
	seen := map[string]bool{}
	for _, b := range base {
		if skip[b] || seen[b] {
			continue
		}
		seen[b] = true
		out = append(out, b)
	}
	for _, a := range adds {
		if skip[a] || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}
```

- [ ] **Step 3.5: Add the field to `Model`**

In whichever file holds `type Model struct`, add the field. Pick a stable spot among other slash-state fields (look for `attach attachState` — they should sit together since both are session-scoped runtime state):

```go
// sessionToolOverrides holds /tool enable/disable/autoload/
// unautoload edits made without --save. Zero value = no overrides.
sessionToolOverrides sessionToolOverrides
```

- [ ] **Step 3.6: Run tests, verify pass**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/tui/ -run TestEffectiveTools -count=1 -v`
Expected: PASS.

- [ ] **Step 3.7: Commit**

```bash
cd /home/foobarto/Dokumenty/stado
git add internal/tui/session_tool_overrides.go internal/tui/session_tool_overrides_test.go internal/tui/model.go
git commit -m "feat(tui): session tool overrides (BACKLOG #5 foundation)

In-memory layer for /tool slash mutations without --save.
applyOverride preserves order, deduplicates, honours removes.
Zero value behaves as a no-op so existing autoload/filter
flow is unchanged."
```

(If the field went into a file other than `model.go`, swap that path.)

---

## Task 4: Wire `effectiveTools` into the live tool surface

**Files:**
- Modify: `internal/tui/model_commands.go:466-468` (the `/tool ls` block) and any other site that reads `m.cfg.Tools` to compute the live surface.

The TUI already passes `m.cfg` to `runtime.ApplyToolFilter` and `runtime.AutoloadedTools`. With overrides, we hand them a derived snapshot.

- [ ] **Step 4.1: Add a helper on Model that returns an effective config**

In `internal/tui/session_tool_overrides.go`, append:

```go
// effectiveConfig returns a copy of m.cfg with [tools] replaced by
// the override-merged view.  Returns m.cfg unchanged when there are
// no overrides — cheap zero-value path.
func (m *Model) effectiveConfig() *config.Config {
	if m == nil || m.cfg == nil {
		return m.cfg
	}
	if m.sessionToolOverrides.isZero() {
		return m.cfg
	}
	cp := *m.cfg
	cp.Tools = m.sessionToolOverrides.effectiveTools(m.cfg)
	return &cp
}

func (o sessionToolOverrides) isZero() bool {
	return len(o.enableAdd) == 0 && len(o.enableRemove) == 0 &&
		len(o.disableAdd) == 0 && len(o.disableRemove) == 0 &&
		len(o.autoloadAdd) == 0 && len(o.autoloadRemove) == 0
}
```

- [ ] **Step 4.2: Update `/tool ls` to honour overrides**

In `internal/tui/model_commands.go` `handleToolSlash`, change the `case "", "ls":` block to use the effective config:

Replace lines roughly 461-486 (the `case "", "ls":` block). Find this:

```go
		reg := runtime.BuildDefaultRegistry()
		if m.cfg != nil {
			runtime.ApplyToolFilter(reg, m.cfg)
		}
		autoloaded := runtime.AutoloadedTools(reg, m.cfg)
```

Replace with:

```go
		reg := runtime.BuildDefaultRegistry()
		eff := m.effectiveConfig()
		if eff != nil {
			runtime.ApplyToolFilter(reg, eff)
		}
		autoloaded := runtime.AutoloadedTools(reg, eff)
```

- [ ] **Step 4.3: Update agentloop to honour overrides**

The autoload selection at request-build time is `internal/runtime/agentloop.go:235`, which reads `opts.Config`. The TUI sets that field; we need the TUI to pass the effective config when building the request.

Find where the TUI assembles `agentloop.Options` (likely `internal/tui/turn.go` or similar — locate via `grep -rn "AgentLoop\|agentloop.Options\|opts.Config" internal/tui/`). At each call site, replace `opts.Config = m.cfg` (or equivalent) with `opts.Config = m.effectiveConfig()`.

If there are multiple call sites, do them all in this step.

- [ ] **Step 4.4: Add a smoke test confirming overrides reach the autoload computation**

Append to `internal/tui/session_tool_overrides_test.go`:

```go
import (
	rt "github.com/foobarto/stado/internal/runtime"
)

// TestEffectiveConfig_FlowsToAutoloadedTools confirms that an
// in-memory autoload override results in the corresponding tool
// being autoloaded by runtime.AutoloadedTools — the actual
// integration point used by the agent loop.
func TestEffectiveConfig_FlowsToAutoloadedTools(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Autoload = []string{} // disk: nothing autoloaded
	m := &Model{cfg: cfg}
	m.sessionToolOverrides.autoloadAdd = []string{"read"} // fs.read in wire form is "read"

	reg := rt.BuildDefaultRegistry()
	eff := m.effectiveConfig()
	rt.ApplyToolFilter(reg, eff)
	got := rt.AutoloadedTools(reg, eff)

	found := false
	for _, t := range got {
		if t.Name() == "read" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("override should make 'read' autoloaded; got %d autoloaded tools", len(got))
	}
}
```

- [ ] **Step 4.5: Run tests, verify pass**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/tui/ -run TestEffectiveConfig -run TestEffectiveTools -count=1 -v`
Expected: PASS.

- [ ] **Step 4.6: Commit**

```bash
cd /home/foobarto/Dokumenty/stado
git add internal/tui/session_tool_overrides.go internal/tui/session_tool_overrides_test.go internal/tui/model_commands.go internal/tui/turn.go
git commit -m "feat(tui): session tool overrides flow into autoload computation

Adds Model.effectiveConfig() and routes /tool ls + agentloop
turn-build through it. Zero overrides = identity (no behavioural
change for callers without slash mutations)."
```

(Adjust the `git add` paths to whatever files you actually edited in 4.3.)

---

## Task 5: `/tool enable <glob...>` slash verb

**Files:**
- Modify: `internal/tui/model_commands.go` `handleToolSlash`

The slash verb mutates `m.sessionToolOverrides` by default, or calls `config.WriteToolsListAdd` when `--save` is present.

- [ ] **Step 5.1: Write the failing test**

Create or extend `internal/tui/slash_tool_verbs_test.go`:

```go
package tui

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

// TestToolEnable_NoSave: /tool enable shell.exec → in-memory override.
func TestToolEnable_NoSave(t *testing.T) {
	cfg := &config.Config{}
	m := &Model{cfg: cfg}
	m.handleToolSlash([]string{"/tool", "enable", "shell.exec"})

	if !contains(m.sessionToolOverrides.enableAdd, "shell.exec") {
		t.Errorf("enable should add to session overrides; got %+v", m.sessionToolOverrides)
	}
	// Disk config must NOT be mutated.
	if len(cfg.Tools.Enabled) != 0 {
		t.Errorf("/tool enable without --save shouldn't mutate cfg.Tools.Enabled; got %v", cfg.Tools.Enabled)
	}
}

// TestToolEnable_OutputMentionsSession: feedback message is clear
// about the change being session-only.
func TestToolEnable_OutputMentionsSession(t *testing.T) {
	cfg := &config.Config{}
	m := &Model{cfg: cfg}
	m.handleToolSlash([]string{"/tool", "enable", "shell.exec"})
	out := m.lastSystemBlock()
	if !strings.Contains(out, "session") {
		t.Errorf("message should mention session-scope; got: %q", out)
	}
}
```

Add the helper at the bottom (or in `model_test.go` if a similar helper exists):

```go
func (m *Model) lastSystemBlock() string {
	if len(m.blocks) == 0 {
		return ""
	}
	return m.blocks[len(m.blocks)-1].body
}
```

- [ ] **Step 5.2: Run tests, verify they fail**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/tui/ -run TestToolEnable -count=1 -v`
Expected: FAIL — `enable` is currently an unknown verb in `handleToolSlash`.

- [ ] **Step 5.3: Add the `enable` verb to `handleToolSlash`**

In `internal/tui/model_commands.go`, inside the `handleToolSlash` switch (currently at lines 460-521), add a new case before the `default:`:

```go
	case "enable":
		args, save := parseToolMutateArgs(parts[2:])
		if len(args) == 0 {
			m.appendBlock(block{kind: "system", body: "/tool enable <name|glob> [<name|glob>...] [--save]"})
			return
		}
		if save {
			path, err := projectConfigPath()
			if err != nil {
				m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool enable: %v", err)})
				return
			}
			_ = config.WriteToolsListRemove(path, "disabled", args)
			if err := config.WriteToolsListAdd(path, "enabled", args); err != nil {
				m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool enable --save: %v", err)})
				return
			}
			m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool enable --save: wrote %s ([tools].enabled += %v)", path, args)})
			return
		}
		m.sessionToolOverrides.enableAdd = appendUnique(m.sessionToolOverrides.enableAdd, args...)
		m.sessionToolOverrides.disableRemove = appendUnique(m.sessionToolOverrides.disableRemove, args...)
		m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool enable: enabled %v for this session (use --save to persist to .stado/config.toml)", args)})
```

Add the helpers near the bottom of `model_commands.go`:

```go
// parseToolMutateArgs splits /tool {enable,disable,autoload,
// unautoload} args into the actual tool names/globs and the --save
// flag.
func parseToolMutateArgs(rest []string) (args []string, save bool) {
	for _, a := range rest {
		if a == "--save" {
			save = true
			continue
		}
		args = append(args, a)
	}
	return
}

// appendUnique returns slice ∪ {extras}, preserving order.
func appendUnique(slice []string, extras ...string) []string {
	seen := map[string]bool{}
	for _, s := range slice {
		seen[s] = true
	}
	for _, e := range extras {
		if seen[e] {
			continue
		}
		seen[e] = true
		slice = append(slice, e)
	}
	return slice
}

// projectConfigPath returns the path of the project's
// .stado/config.toml (creates the dir if missing). Mirrors
// cmd/stado/tool.go's toolMutateConfigPath default branch.
func projectConfigPath() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	return cwd + "/.stado/config.toml", nil
}
```

Add `"os"` and `"github.com/foobarto/stado/internal/config"` to the imports if not already present.

Update the verb-list error message in the `default:` branch:

```go
	default:
		m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool %s: unknown verb. Try: ls, info, cats, enable, disable, autoload, unautoload, reload", verb)})
```

- [ ] **Step 5.4: Run tests, verify pass**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/tui/ -run TestToolEnable -count=1 -v`
Expected: PASS.

- [ ] **Step 5.5: Commit**

```bash
cd /home/foobarto/Dokumenty/stado
git add internal/tui/model_commands.go internal/tui/slash_tool_verbs_test.go
git commit -m "feat(tui): /tool enable session/save semantics (BACKLOG #5)

Per-session by default; --save writes to project's
.stado/config.toml via existing WriteToolsListAdd/Remove."
```

---

## Task 6: `/tool disable <glob...>` slash verb

**Files:**
- Modify: `internal/tui/model_commands.go` `handleToolSlash`
- Modify: `internal/tui/slash_tool_verbs_test.go`

Mirrors `enable`. When invoked without `--save`, also pulls the tool out of in-memory autoload (matches the CLI behaviour at `cmd/stado/tool.go:289-294` — disabling silently masks autoload).

- [ ] **Step 6.1: Write the failing test**

Append to `internal/tui/slash_tool_verbs_test.go`:

```go
func TestToolDisable_NoSave(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Autoload = []string{"shell.exec"} // pre-existing autoload
	m := &Model{cfg: cfg}
	m.handleToolSlash([]string{"/tool", "disable", "shell.exec"})

	if !contains(m.sessionToolOverrides.disableAdd, "shell.exec") {
		t.Errorf("disable should add to disableAdd; got %+v", m.sessionToolOverrides)
	}
	// Disable must also pull from autoload (in-memory only — disk
	// config.Tools.Autoload stays untouched).
	eff := m.effectiveConfig()
	if contains(eff.Tools.Autoload, "shell.exec") {
		t.Errorf("disable should mask autoload; got effective autoload %v", eff.Tools.Autoload)
	}
}
```

- [ ] **Step 6.2: Run test, verify it fails**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/tui/ -run TestToolDisable -count=1 -v`
Expected: FAIL — `disable` not yet a recognised verb.

- [ ] **Step 6.3: Add the `disable` case**

Insert in `handleToolSlash` after `case "enable":`:

```go
	case "disable":
		args, save := parseToolMutateArgs(parts[2:])
		if len(args) == 0 {
			m.appendBlock(block{kind: "system", body: "/tool disable <name|glob> [<name|glob>...] [--save]"})
			return
		}
		if save {
			path, err := projectConfigPath()
			if err != nil {
				m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool disable: %v", err)})
				return
			}
			_ = config.WriteToolsListRemove(path, "enabled", args)
			_ = config.WriteToolsListRemove(path, "autoload", args)
			if err := config.WriteToolsListAdd(path, "disabled", args); err != nil {
				m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool disable --save: %v", err)})
				return
			}
			m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool disable --save: wrote %s ([tools].disabled += %v)", path, args)})
			return
		}
		m.sessionToolOverrides.disableAdd = appendUnique(m.sessionToolOverrides.disableAdd, args...)
		m.sessionToolOverrides.enableRemove = appendUnique(m.sessionToolOverrides.enableRemove, args...)
		m.sessionToolOverrides.autoloadRemove = appendUnique(m.sessionToolOverrides.autoloadRemove, args...)
		m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool disable: disabled %v for this session (use --save to persist)", args)})
```

- [ ] **Step 6.4: Run tests, verify pass**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/tui/ -run TestToolDisable -count=1 -v`
Expected: PASS.

- [ ] **Step 6.5: Commit**

```bash
cd /home/foobarto/Dokumenty/stado
git add internal/tui/model_commands.go internal/tui/slash_tool_verbs_test.go
git commit -m "feat(tui): /tool disable session/save semantics (BACKLOG #5)

Disable also masks autoload — matches stado tool disable CLI."
```

---

## Task 7: `/tool autoload` and `/tool unautoload` slash verbs

**Files:**
- Modify: `internal/tui/model_commands.go` `handleToolSlash`
- Modify: `internal/tui/slash_tool_verbs_test.go`

- [ ] **Step 7.1: Write the failing tests**

Append to `slash_tool_verbs_test.go`:

```go
func TestToolAutoload_NoSave(t *testing.T) {
	cfg := &config.Config{}
	m := &Model{cfg: cfg}
	m.handleToolSlash([]string{"/tool", "autoload", "fs.read"})

	if !contains(m.sessionToolOverrides.autoloadAdd, "fs.read") {
		t.Errorf("autoload should add; got %+v", m.sessionToolOverrides)
	}
}

func TestToolUnautoload_NoSave(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Autoload = []string{"fs.read"}
	m := &Model{cfg: cfg}
	m.handleToolSlash([]string{"/tool", "unautoload", "fs.read"})

	eff := m.effectiveConfig()
	if contains(eff.Tools.Autoload, "fs.read") {
		t.Errorf("unautoload should remove from effective autoload; got %v", eff.Tools.Autoload)
	}
}
```

- [ ] **Step 7.2: Run tests, verify they fail**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/tui/ -run TestToolAutoload -run TestToolUnautoload -count=1 -v`
Expected: FAIL.

- [ ] **Step 7.3: Add the verbs**

Insert in `handleToolSlash` after `case "disable":`:

```go
	case "autoload":
		args, save := parseToolMutateArgs(parts[2:])
		if len(args) == 0 {
			m.appendBlock(block{kind: "system", body: "/tool autoload <name|glob> [<name|glob>...] [--save]"})
			return
		}
		if save {
			path, err := projectConfigPath()
			if err != nil {
				m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool autoload: %v", err)})
				return
			}
			if err := config.WriteToolsListAdd(path, "autoload", args); err != nil {
				m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool autoload --save: %v", err)})
				return
			}
			m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool autoload --save: wrote %s ([tools].autoload += %v)", path, args)})
			return
		}
		m.sessionToolOverrides.autoloadAdd = appendUnique(m.sessionToolOverrides.autoloadAdd, args...)
		m.sessionToolOverrides.autoloadRemove = removeFromSlice(m.sessionToolOverrides.autoloadRemove, args...)
		m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool autoload: %v for this session (use --save to persist)", args)})

	case "unautoload":
		args, save := parseToolMutateArgs(parts[2:])
		if len(args) == 0 {
			m.appendBlock(block{kind: "system", body: "/tool unautoload <name|glob> [<name|glob>...] [--save]"})
			return
		}
		if save {
			path, err := projectConfigPath()
			if err != nil {
				m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool unautoload: %v", err)})
				return
			}
			if err := config.WriteToolsListRemove(path, "autoload", args); err != nil {
				m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool unautoload --save: %v", err)})
				return
			}
			m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool unautoload --save: wrote %s ([tools].autoload -= %v)", path, args)})
			return
		}
		m.sessionToolOverrides.autoloadRemove = appendUnique(m.sessionToolOverrides.autoloadRemove, args...)
		m.sessionToolOverrides.autoloadAdd = removeFromSlice(m.sessionToolOverrides.autoloadAdd, args...)
		m.appendBlock(block{kind: "system", body: fmt.Sprintf("/tool unautoload: removed %v from this session's autoload (use --save to persist)", args)})
```

Add `removeFromSlice` near `appendUnique`:

```go
// removeFromSlice returns slice with all entries equal to any of
// the targets removed, preserving order.
func removeFromSlice(slice []string, targets ...string) []string {
	skip := map[string]bool{}
	for _, t := range targets {
		skip[t] = true
	}
	out := slice[:0]
	for _, s := range slice {
		if !skip[s] {
			out = append(out, s)
		}
	}
	return out
}
```

- [ ] **Step 7.4: Run tests, verify pass**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/tui/ -run TestToolAutoload -run TestToolUnautoload -count=1 -v`
Expected: PASS.

- [ ] **Step 7.5: Commit**

```bash
cd /home/foobarto/Dokumenty/stado
git add internal/tui/model_commands.go internal/tui/slash_tool_verbs_test.go
git commit -m "feat(tui): /tool autoload + unautoload session/save (BACKLOG #5)

Completes the four mutating verbs: enable/disable/autoload/
unautoload. autoloadAdd and autoloadRemove are mutually exclusive
(adding clears any pending remove and vice versa)."
```

---

## Task 8: `--save` writes the path back in the feedback message

**Files:**
- Modify: `internal/tui/model_commands.go` (no API changes; tighten the existing messages produced by Tasks 5/6/7)

The current `--save` feedback says `wrote <path> ([tools].enabled += [...])`. That's good; this task verifies it via tests.

- [ ] **Step 8.1: Add a test asserting --save writes to disk**

Append to `slash_tool_verbs_test.go`:

```go
import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
)

// TestToolEnable_SaveWritesConfig: /tool enable shell.exec --save
// produces a .stado/config.toml under the cwd containing the
// entry. We chdir into a temp dir so the test doesn't touch the
// repo's actual .stado.
func TestToolEnable_SaveWritesConfig(t *testing.T) {
	tmp := t.TempDir()
	prev, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(prev) })
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{}
	m := &Model{cfg: cfg}
	m.handleToolSlash([]string{"/tool", "enable", "shell.exec", "--save"})

	want := filepath.Join(tmp, ".stado", "config.toml")
	data, err := ioutil.ReadFile(want)
	if err != nil {
		t.Fatalf("expected config at %s: %v", want, err)
	}
	if !strings.Contains(string(data), "shell.exec") {
		t.Errorf("config should mention shell.exec; got: %s", string(data))
	}

	// Session overrides should remain empty — --save bypasses them.
	if len(m.sessionToolOverrides.enableAdd) != 0 {
		t.Errorf("--save should not populate session overrides; got %v", m.sessionToolOverrides.enableAdd)
	}
}
```

- [ ] **Step 8.2: Run tests, verify pass**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/tui/ -run TestToolEnable_SaveWritesConfig -count=1 -v`
Expected: PASS (assuming `WriteToolsListAdd` already creates the parent dir; if not, add `os.MkdirAll(filepath.Dir(path), 0755)` to `projectConfigPath`).

- [ ] **Step 8.3: Commit**

```bash
cd /home/foobarto/Dokumenty/stado
git add internal/tui/slash_tool_verbs_test.go
git commit -m "test(tui): --save writes to project .stado/config.toml"
```

---

## Task 9: Update `/tool` help string and `case "":` zero-state

**Files:**
- Modify: `internal/tui/model_commands.go` (the `default:` and the `case "", "ls":` branches)

The `default:` branch lists verbs — must mention all 8.

- [ ] **Step 9.1: Update the default-branch error message**

Change `internal/tui/model_commands.go` `default:` (line ~520) to:

```go
	default:
		m.appendBlock(block{kind: "system", body: fmt.Sprintf(
			"/tool %s: unknown verb. Try: ls, info, cats, enable, disable, autoload, unautoload, reload\n"+
				"Mutating verbs are session-scoped by default; pass --save to persist to .stado/config.toml.",
			verb)})
```

- [ ] **Step 9.2: Add a help test (no implementation change)**

Append to `slash_tool_verbs_test.go`:

```go
func TestToolSlash_HelpMentionsAllVerbs(t *testing.T) {
	m := &Model{cfg: &config.Config{}}
	m.handleToolSlash([]string{"/tool", "nope"})
	out := m.lastSystemBlock()
	for _, v := range []string{"ls", "info", "cats", "enable", "disable", "autoload", "unautoload", "reload"} {
		if !strings.Contains(out, v) {
			t.Errorf("help should mention verb %q; got: %s", v, out)
		}
	}
	if !strings.Contains(out, "--save") {
		t.Errorf("help should mention --save; got: %s", out)
	}
}
```

- [ ] **Step 9.3: Run tests, verify pass**

Run: `cd /home/foobarto/Dokumenty/stado && go test ./internal/tui/ -run TestToolSlash_HelpMentionsAllVerbs -count=1 -v`
Expected: PASS.

- [ ] **Step 9.4: Commit**

```bash
cd /home/foobarto/Dokumenty/stado
git add internal/tui/model_commands.go internal/tui/slash_tool_verbs_test.go
git commit -m "feat(tui): /tool help lists all 8 verbs + --save hint"
```

---

## Task 10: Documentation update — EP-0037 §I

**Files:**
- Modify: `docs/eps/0037-tool-dispatch-and-operator-surface.md` (the section that lists `/tool` slash verbs)

EP-0037 already documents the slash mirrors as a feature; the `enable/disable/autoload/unautoload` verbs need a sentence saying they default to per-session and accept `--save`.

- [ ] **Step 10.1: Find the relevant section**

Run: `cd /home/foobarto/Dokumenty/stado && grep -n "/tool\|tool ls\|slash" docs/eps/0037-tool-dispatch-and-operator-surface.md | head -20`

Locate the paragraph mentioning `/tool` mirrors.

- [ ] **Step 10.2: Add the per-session/`--save` paragraph**

Edit the located section, adding (or updating) a paragraph that reads:

> Mutating slash verbs (`/tool enable`, `/tool disable`, `/tool autoload`, `/tool unautoload`) default to **per-session non-persistent** — the change applies to the running TUI session only and reverts when stado exits. Pass `--save` (e.g. `/tool disable browser.fetch --save`) to additionally write the change to the project's `.stado/config.toml` via `config.WriteToolsListAdd/Remove`. The CLI (`stado tool enable/...`) is the inverse: persistent by default, no `--save` flag because there's no transient layer to scope it against. Decision rationale: NOTES Q7 — try-then-commit is the natural slash-command shape; preventing accidental `git`-tracked config noise from a debug toggle outweighs the friction of typing `--save` when the change is meant to stick.

Adjust history frontmatter at the top of the EP if it has a per-revision changelog format:

```yaml
  - date: 2026-05-05
    status: Implemented
    note: >
      /tool slash mutating verbs (enable/disable/autoload/unautoload)
      added with per-session-default + --save semantics. Typed-prefix
      handle ID format (FormatHandleID/ParseHandleID) replaces bare
      numerics in /ps and /kill. BACKLOG items #5 and #7.
```

- [ ] **Step 10.3: Commit**

```bash
cd /home/foobarto/Dokumenty/stado
git add docs/eps/0037-tool-dispatch-and-operator-surface.md
git commit -m "docs(ep-0037): document /tool slash mutating verbs + handle ID format"
```

---

## Task 11: Mark BACKLOG items #5 and #7 as done

**Files:**
- Modify: `docs/superpowers/plans/BACKLOG.md`

- [ ] **Step 11.1: Mark items**

In `docs/superpowers/plans/BACKLOG.md`, edit items #5 and #7 to insert a `**Status:** Implemented YYYY-MM-DD (commits <hashes>)` line under each one's existing fields. Don't delete the items — leave them in place as audit trail.

- [ ] **Step 11.2: Commit**

```bash
cd /home/foobarto/Dokumenty/stado
git add docs/superpowers/plans/BACKLOG.md
git commit -m "docs(backlog): mark #5 (slash mutating verbs) + #7 (typed handles) done"
```

---

## Self-Review Checklist

After implementing every task above, verify:

- [ ] `go test ./internal/plugins/runtime/ ./internal/tui/ ./internal/runtime/ -count=1` passes.
- [ ] `go vet ./...` clean.
- [ ] Manual TUI smoke: launch `stado run`, run `/ps`, `/kill <id>`, `/tool ls`, `/tool autoload fs.read`, `/tool ls` again (verify state shows `autoloaded` for `fs.read`), `/tool unautoload fs.read --save`, exit, re-launch, run `/tool ls` (verify the `--save` change persisted).
- [ ] `stado tool ls` (CLI) and `/tool ls` (TUI) produce the same column layout for the same inputs.
- [ ] `/ps` output uses `agent:` and `session:` typed prefixes; `/kill agent:abcd` and `/kill abcd` both succeed.
- [ ] No file in this PR exceeds 800 lines (split if needed).

---

## Spec coverage

Each requirement traced to a task:

- **BACKLOG #7** (typed-prefix handle IDs) → Tasks 1-2 + Task 10 (doc).
- **BACKLOG #5** (slash mutating verbs, per-session default, `--save`) → Tasks 3-9 + Task 10 (doc).
- **NOTES Q7** decision recorded in EP → Task 10.
- **No regressions in existing `/ps`, `/kill`, `/tool ls`** → Task 5.5, 6.4, 7.4, 9.3 + Task 11 self-review.

No placeholders. All code blocks contain executable Go or shell. Function names match across tasks (`FormatHandleID`, `ParseHandleID`, `effectiveTools`, `effectiveConfig`, `appendUnique`, `removeFromSlice`, `parseToolMutateArgs`, `projectConfigPath`).
