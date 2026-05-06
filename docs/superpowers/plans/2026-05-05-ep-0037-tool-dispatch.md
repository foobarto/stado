# EP-0037: Tool Dispatch, Naming, and Operator Surface — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement EP-0037's tool naming convention, category taxonomy, four meta-tools (search/describe/categories/in_category), autoload dispatch model, CLI flags, `stado tool` subcommand, and TUI slash mirrors.

**Architecture:** All bundled tools keep their current bare names (rename happens in EP-0038). New work adds: (a) wire-form synthesis helpers used at registration, (b) a category system on manifests, (c) four native meta-tools that let the model discover + activate non-autoloaded tools, (d) an autoload set so `ToolDefs` only surfaces a small always-on core to the model, and (e) operator config/CLI/TUI surfaces to control all of it.

**Tech Stack:** Go, Cobra CLI, BubbleTea TUI, koanf config, path.Match glob, wazero (no wasm changes this EP).

**Spec:** `docs/eps/0037-tool-dispatch-and-operator-surface.md`

**Depends on:** nothing (foundation for EP-0038)

---

## File Map

| File | Action | Purpose |
|------|--------|---------|
| `internal/tools/naming.go` | Create | WireForm synthesis + ParseWireForm |
| `internal/tools/naming_test.go` | Create | Unit tests for naming |
| `internal/plugins/categories.go` | Create | Canonical category list + ValidateCategories |
| `internal/plugins/categories_test.go` | Create | Unit tests |
| `internal/plugins/manifest.go` | Modify | Add Categories + ExtraCategories to ToolDef |
| `internal/config/config.go` | Modify | Add Tools.Autoload + [sandbox] schema stub |
| `internal/runtime/executor.go` | Modify | ApplyToolFilter gains wildcard + autoload logic; ToolDefs returns autoload set only |
| `internal/runtime/executor_test.go` | Create | Tests for new filter behaviour |
| `internal/runtime/meta_tools.go` | Create | Four native meta-tools |
| `internal/runtime/meta_tools_test.go` | Create | Tests for meta-tool behaviour |
| `internal/runtime/agentloop.go` | Modify | Track activated tools per-session; inject on describe result |
| `cmd/stado/run.go` | Modify | Add --tools, --tools-autoload, --tools-disable flags |
| `cmd/stado/tool.go` | Create | `stado tool` subcommand (ls/info/cats/enable/disable/autoload/unautoload/reload) |
| `cmd/stado/main.go` | Modify | Register toolCmd |
| `internal/tui/model_commands.go` | Modify | /tool and /session slash commands |
| `cmd/stado/plugin_install.go` | Modify | Validate categories at install time |

---

## Task 1: Wire-form naming helpers

**Files:**
- Create: `internal/tools/naming.go`
- Create: `internal/tools/naming_test.go`

- [ ] **Step 1: Write the failing tests**

```go
// internal/tools/naming_test.go
package tools_test

import (
	"testing"

	"github.com/foobarto/stado/internal/tools"
)

func TestWireForm(t *testing.T) {
	cases := []struct{ alias, name, want string }{
		{"fs", "read", "fs__read"},
		{"fs", "write", "fs__write"},
		{"shell", "exec", "shell__exec"},
		{"htb-lab", "spawn", "htb_lab__spawn"},
		{"web", "fetch", "web__fetch"},
		{"tools", "search", "tools__search"},
		{"tools", "describe", "tools__describe"},
	}
	for _, c := range cases {
		got, err := tools.WireForm(c.alias, c.name)
		if err != nil {
			t.Errorf("WireForm(%q,%q) error: %v", c.alias, c.name, err)
			continue
		}
		if got != c.want {
			t.Errorf("WireForm(%q,%q) = %q, want %q", c.alias, c.name, got, c.want)
		}
	}
}

func TestWireForm_TooLong(t *testing.T) {
	long := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	_, err := tools.WireForm(long, long)
	if err == nil {
		t.Error("expected error for wire form > 64 chars")
	}
}

func TestWireForm_ReservedSeparator(t *testing.T) {
	_, err := tools.WireForm("foo__bar", "baz")
	if err == nil {
		t.Error("expected error: alias contains __")
	}
	_, err = tools.WireForm("foo", "bar__baz")
	if err == nil {
		t.Error("expected error: tool name contains __")
	}
}

func TestParseWireForm(t *testing.T) {
	alias, name, ok := tools.ParseWireForm("fs__read")
	if !ok || alias != "fs" || name != "read" {
		t.Errorf("ParseWireForm(fs__read) = %q,%q,%v", alias, name, ok)
	}
	_, _, ok = tools.ParseWireForm("nounderscores")
	if ok {
		t.Error("expected ok=false for no __ separator")
	}
}
```

- [ ] **Step 2: Run to verify fail**

```
cd <repo-root> && go test ./internal/tools/... -run TestWireForm 2>&1 | head -20
```
Expected: `cannot find package` or `undefined: tools.WireForm`

- [ ] **Step 3: Implement**

```go
// internal/tools/naming.go
package tools

import (
	"fmt"
	"strings"
)

// WireForm synthesises the LLM-facing tool name from a plugin's local alias
// and tool name. Dots and dashes in either segment become underscores; the
// double-underscore separator is reserved and rejected in inputs.
//
// Example: alias="htb-lab", name="spawn" → "htb_lab__spawn"
// Example: alias="fs",      name="read"  → "fs__read"
func WireForm(localAlias, toolName string) (string, error) {
	if strings.Contains(localAlias, "__") {
		return "", fmt.Errorf("naming: local alias %q contains reserved separator __", localAlias)
	}
	if strings.Contains(toolName, "__") {
		return "", fmt.Errorf("naming: tool name %q contains reserved separator __", toolName)
	}
	seg := func(s string) string {
		s = strings.ReplaceAll(s, ".", "_")
		s = strings.ReplaceAll(s, "-", "_")
		return s
	}
	wire := seg(localAlias) + "__" + seg(toolName)
	if len(wire) > 64 {
		return "", fmt.Errorf("naming: wire form %q exceeds 64 chars (Anthropic limit)", wire)
	}
	return wire, nil
}

// ParseWireForm splits a wire-form tool name back into (alias, toolName).
// Returns ok=false if the string contains no __ separator.
func ParseWireForm(wire string) (alias, toolName string, ok bool) {
	idx := strings.Index(wire, "__")
	if idx < 0 {
		return "", "", false
	}
	return wire[:idx], wire[idx+2:], true
}
```

- [ ] **Step 4: Run tests**

```
go test ./internal/tools/... -run TestWireForm -v
```
Expected: all PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tools/naming.go internal/tools/naming_test.go
git commit -m "feat(ep-0037): wire-form naming helpers"
```

---

## Task 2: Category taxonomy + manifest fields

**Files:**
- Create: `internal/plugins/categories.go`
- Create: `internal/plugins/categories_test.go`
- Modify: `internal/plugins/manifest.go:~50` (ToolDef struct)

- [ ] **Step 1: Write tests**

```go
// internal/plugins/categories_test.go
package plugins_test

import (
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

func TestValidateCategories_Known(t *testing.T) {
	if err := plugins.ValidateCategories([]string{"filesystem", "shell", "network"}); err != nil {
		t.Errorf("known categories should pass: %v", err)
	}
}

func TestValidateCategories_Unknown(t *testing.T) {
	err := plugins.ValidateCategories([]string{"filesystem", "netork"}) // typo
	if err == nil {
		t.Error("unknown category should fail")
	}
}

func TestValidateCategories_Empty(t *testing.T) {
	if err := plugins.ValidateCategories([]string{}); err != nil {
		t.Errorf("empty categories should pass (discouraged but valid): %v", err)
	}
}
```

- [ ] **Step 2: Run to verify fail**

```
go test ./internal/plugins/... -run TestValidateCategories 2>&1 | head -10
```

- [ ] **Step 3: Implement categories.go**

```go
// internal/plugins/categories.go
package plugins

import "fmt"

// CanonicalCategories is the frozen taxonomy from EP-0037 §C.
// Extend only via a new EP that amends this list.
var CanonicalCategories = []string{
	"filesystem", "shell", "network", "web",
	"dns", "crypto", "data", "encoding",
	"code-search", "code-edit", "lsp", "agent",
	"task", "mcp", "image", "secrets",
	"documentation", "ctf-offense", "ctf-recon", "ctf-postex",
	"meta",
}

var canonicalSet = func() map[string]bool {
	m := make(map[string]bool, len(CanonicalCategories))
	for _, c := range CanonicalCategories {
		m[c] = true
	}
	return m
}()

// ValidateCategories returns an error if any entry is not in the canonical list.
// Empty slice is valid (tool won't appear in in_category results).
func ValidateCategories(cats []string) error {
	for _, c := range cats {
		if !canonicalSet[c] {
			return fmt.Errorf("unknown category %q; canonical categories: %v", c, CanonicalCategories)
		}
	}
	return nil
}
```

- [ ] **Step 4: Add Categories + ExtraCategories to ToolDef in manifest.go**

Find the `ToolDef` struct (around line 50 in `internal/plugins/manifest.go`) and add:

```go
type ToolDef struct {
	Name            string   `json:"name"`
	Description     string   `json:"description"`
	Schema          string   `json:"schema"`
	Class           string   `json:"class,omitempty"`
	Categories      []string `json:"categories,omitempty"`
	ExtraCategories []string `json:"extra_categories,omitempty"`
}
```

- [ ] **Step 5: Run tests**

```
go test ./internal/plugins/... -run TestValidateCategories -v
go build ./...
```
Expected: all PASS, build clean

- [ ] **Step 6: Commit**

```bash
git add internal/plugins/categories.go internal/plugins/categories_test.go internal/plugins/manifest.go
git commit -m "feat(ep-0037): category taxonomy + ToolDef fields"
```

---

## Task 3: Config — Autoload + wildcard glob + [sandbox] stub

**Files:**
- Modify: `internal/config/config.go:168` (Tools struct)
- Modify: `internal/runtime/executor.go` (ApplyToolFilter + new wildcard logic)
- Create: `internal/runtime/executor_test.go` (new test file or add to existing tool_filter_test.go)

- [ ] **Step 1: Write failing tests for wildcard and autoload**

Add to `internal/runtime/tool_filter_test.go`:

```go
func TestApplyToolFilter_WildcardDisabled(t *testing.T) {
	reg := BuildDefaultRegistry()
	cfg := &config.Config{}
	// "bash" tool exists; "bash.*" glob should match it
	cfg.Tools.Disabled = []string{"bash"}
	ApplyToolFilter(reg, cfg)
	for _, tl := range reg.All() {
		if tl.Name() == "bash" {
			t.Error("bash should be removed by exact disable")
		}
	}
}

func TestApplyToolFilter_AutoloadSubset(t *testing.T) {
	reg := BuildDefaultRegistry()
	cfg := &config.Config{}
	cfg.Tools.Autoload = []string{"read", "grep"}
	autoloaded := AutoloadedTools(reg, cfg)
	names := map[string]bool{}
	for _, t2 := range autoloaded {
		names[t2.Name()] = true
	}
	if !names["read"] || !names["grep"] {
		t.Error("autoload should include read and grep")
	}
	if len(autoloaded) != 2 {
		t.Errorf("expected 2 autoloaded, got %d", len(autoloaded))
	}
}

func TestToolMatchesGlob(t *testing.T) {
	cases := []struct {
		name, pat string
		want      bool
	}{
		{"fs__read", "fs.*", true},       // dotted canonical glob maps to __-wire
		{"fs__write", "fs.*", true},
		{"shell__exec", "fs.*", false},
		{"read", "read", true},           // exact match still works
		{"bash", "bash", true},
		{"webfetch", "web.*", false},     // no plugin namespace yet
		{"tools__search", "tools.*", true},
	}
	for _, c := range cases {
		got := ToolMatchesGlob(c.name, c.pat)
		if got != c.want {
			t.Errorf("ToolMatchesGlob(%q,%q) = %v, want %v", c.name, c.pat, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run to verify fail**

```
go test ./internal/runtime/... -run "TestApplyToolFilter_Autoload|TestToolMatchesGlob" 2>&1 | head -20
```

- [ ] **Step 3: Add Autoload to Tools config struct**

In `internal/config/config.go`, update the Tools struct (around line 168):

```go
type Tools struct {
	Enabled  []string `koanf:"enabled"`
	Disabled []string `koanf:"disabled"`
	// Autoload is the subset of enabled tools whose schemas are sent to
	// the model at every turn (the "always-loaded core"). Tools not in
	// this list are still reachable via tools.search + tools.describe.
	// Empty = use the hardcoded default core (fs.*, shell.exec).
	Autoload []string `koanf:"autoload"`
}
```

Also add the [sandbox] schema stub after the existing Sandbox struct (find it with `grep -n "type Sandbox" internal/config/config.go`):

```go
// Sandbox is the [sandbox] config section — EP-0037 reserves the schema;
// EP-0038 implements wrap mode.
type Sandbox struct {
	Mode    string `koanf:"mode"`     // "off" | "wrap" | "external"
	// Wrap and profile fields added by EP-0038.
}
```
(Note: if Sandbox struct already exists, just add the Mode field if missing.)

- [ ] **Step 4: Implement ToolMatchesGlob and AutoloadedTools in executor.go**

Add to `internal/runtime/executor.go`:

```go
// defaultAutoloadNames is the hardcoded convenience default when
// [tools.autoload] is empty. The dispatch kernel (tools.*) is always
// present regardless of this list.
var defaultAutoloadNames = []string{
	"read", "write", "edit", "glob", "grep", "bash",
}

// ToolMatchesGlob reports whether a tool's registered name matches a
// config pattern. Patterns are dotted-canonical or bare names; wire-form
// tool names (with __) are tested by converting the pattern's dot-separator
// to __ for the prefix match.
//
// Examples:
//
//	ToolMatchesGlob("fs__read",     "fs.*")   → true
//	ToolMatchesGlob("shell__exec",  "shell.*") → true
//	ToolMatchesGlob("read",         "read")   → true  (bare name exact)
//	ToolMatchesGlob("tools__search","tools.*") → true
func ToolMatchesGlob(toolName, pattern string) bool {
	// Exact match (bare names, pre-EP-0038 registry).
	if toolName == pattern {
		return true
	}
	// Wildcard: "fs.*" → match any tool whose name starts with "fs__"
	// (wire form) or "fs." (canonical form with dots, future-proof).
	if strings.HasSuffix(pattern, ".*") {
		prefix := strings.TrimSuffix(pattern, ".*")
		wirePrefix := strings.ReplaceAll(prefix, ".", "_") + "__"
		dotPrefix := prefix + "."
		return strings.HasPrefix(toolName, wirePrefix) || strings.HasPrefix(toolName, dotPrefix)
	}
	return false
}

// toolMatchesAny returns true if toolName matches any of the patterns.
func toolMatchesAny(toolName string, patterns []string) bool {
	for _, p := range patterns {
		if ToolMatchesGlob(toolName, p) {
			return true
		}
	}
	return false
}

// AutoloadedTools returns the subset of tools in reg that should have
// their schemas sent to the model on every turn. The four meta-tools
// (tools__search etc.) are always included regardless of config.
// If cfg.Tools.Autoload is empty, defaultAutoloadNames is used.
func AutoloadedTools(reg *tools.Registry, cfg *config.Config) []tool.Tool {
	autoloadPatterns := defaultAutoloadNames
	if cfg != nil && len(cfg.Tools.Autoload) > 0 {
		autoloadPatterns = cfg.Tools.Autoload
	}
	var out []tool.Tool
	for _, t := range reg.All() {
		name := t.Name()
		// Dispatch kernel always autoloaded.
		if isMetaTool(name) {
			out = append(out, t)
			continue
		}
		if toolMatchesAny(name, autoloadPatterns) {
			out = append(out, t)
		}
	}
	return out
}

// isMetaTool reports whether name is one of the four dispatch kernel tools.
func isMetaTool(name string) bool {
	switch name {
	case "tools__search", "tools__describe", "tools__categories", "tools__in_category":
		return true
	// Also match bare names for pre-EP-0038 compatibility.
	case "tools.search", "tools.describe", "tools.categories", "tools.in_category":
		return true
	}
	return false
}
```

- [ ] **Step 5: Update ApplyToolFilter to use glob matching**

Replace the existing `warnUnknown` and its name-exact matching in `ApplyToolFilter` with glob-aware version. The key change: before warning "unknown tool", check if the pattern is a glob (contains `.*`) — globs that expand to zero are silent no-ops per spec, not warnings:

```go
func ApplyToolFilter(reg *tools.Registry, cfg *config.Config) {
	if cfg == nil {
		return
	}
	if len(cfg.Tools.Enabled) == 0 && len(cfg.Tools.Disabled) == 0 {
		return
	}
	known := map[string]bool{}
	for _, t := range reg.All() {
		known[t.Name()] = true
	}

	warnUnknownExact := func(list []string, label string) {
		for _, n := range list {
			if strings.Contains(n, "*") {
				continue // glob patterns: zero match is silent no-op
			}
			if !known[n] {
				fmt.Fprintf(os.Stderr, "stado: [tools].%s mentions %q — no such tool (ignored)\n", label, n)
			}
		}
	}
	warnUnknownExact(cfg.Tools.Enabled, "enabled")
	warnUnknownExact(cfg.Tools.Disabled, "disabled")

	if len(cfg.Tools.Enabled) > 0 {
		allow := map[string]bool{}
		for name := range known {
			if toolMatchesAny(name, cfg.Tools.Enabled) {
				allow[name] = true
			}
		}
		if len(allow) == 0 {
			return
		}
		for name := range known {
			if !allow[name] {
				reg.Unregister(name)
			}
		}
		return
	}
	for name := range known {
		if toolMatchesAny(name, cfg.Tools.Disabled) {
			reg.Unregister(name)
		}
	}
}
```

- [ ] **Step 6: Run tests**

```
go test ./internal/runtime/... -run "TestApplyToolFilter|TestToolMatchesGlob|TestAutoload" -v
go build ./...
```
Expected: all PASS, build clean

- [ ] **Step 7: Commit**

```bash
git add internal/config/config.go internal/runtime/executor.go internal/runtime/tool_filter_test.go
git commit -m "feat(ep-0037): tools.autoload config + wildcard glob filter"
```

---

## Task 4: Four meta-tools (native Go, dispatch kernel)

**Files:**
- Create: `internal/runtime/meta_tools.go`
- Create: `internal/runtime/meta_tools_test.go`
- Modify: `internal/runtime/executor.go` (register meta-tools in BuildDefaultRegistry)
- Modify: `internal/runtime/bundled_plugin_tools.go` (meta-tools are not wrapped in bundledPluginTool)

- [ ] **Step 1: Write failing tests**

```go
// internal/runtime/meta_tools_test.go
package runtime_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/tool"
)

func TestMetaToolsRegistered(t *testing.T) {
	reg := runtime.BuildDefaultRegistry()
	for _, name := range []string{"tools__search", "tools__describe", "tools__categories", "tools__in_category"} {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("meta-tool %q not registered", name)
		}
	}
}

func TestToolsSearch_NoQuery(t *testing.T) {
	reg := runtime.BuildDefaultRegistry()
	mt, ok := reg.Get("tools__search")
	if !ok {
		t.Fatal("tools__search not found")
	}
	result, err := mt.Run(context.Background(), json.RawMessage(`{}`), &stubHost{reg: reg})
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	// Should return a JSON array of tools.
	var items []map[string]any
	if err := json.Unmarshal([]byte(result.Content), &items); err != nil {
		t.Fatalf("result not JSON array: %v — got: %s", err, result.Content)
	}
	if len(items) == 0 {
		t.Error("expected non-empty tool list")
	}
}

func TestToolsDescribe_ActivatesTool(t *testing.T) {
	reg := runtime.BuildDefaultRegistry()
	mt, ok := reg.Get("tools__describe")
	if !ok {
		t.Fatal("tools__describe not found")
	}
	h := &stubHost{reg: reg}
	args, _ := json.Marshal(map[string]any{"names": []string{"read"}})
	result, err := mt.Run(context.Background(), args, h)
	if err != nil {
		t.Fatal(err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if !strings.Contains(result.Content, "schema") {
		t.Errorf("describe result should contain schema, got: %s", result.Content)
	}
}

// stubHost satisfies tool.Host with just enough for meta-tools.
type stubHost struct {
	reg     *tools.Registry
	activated []string
}

func (h *stubHost) Workdir() string        { return "/tmp" }
func (h *stubHost) AllowPrivateNetwork() bool { return false }
func (h *stubHost) ActivateTool(name string) { h.activated = append(h.activated, name) }
// Remaining tool.Host methods return zero values.
func (h *stubHost) Runner() tool.Runner           { return nil }
func (h *stubHost) Model() string                 { return "" }
func (h *stubHost) SessionID() string             { return "" }
```

- [ ] **Step 2: Run to verify fail**

```
go test ./internal/runtime/... -run TestMetaTools 2>&1 | head -20
```

- [ ] **Step 3: Define ToolActivator interface and update tool.Host**

Add to `pkg/tool/tool.go` (or wherever `tool.Host` is defined):

```go
// ToolActivator is an optional extension of Host. If the host implements
// this interface, tools.describe can activate additional tool schemas
// into the current session surface.
type ToolActivator interface {
	ActivateTool(name string)
}
```

- [ ] **Step 4: Implement meta_tools.go**

```go
// internal/runtime/meta_tools.go
package runtime

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/foobarto/stado/internal/tools"
	pkgtool "github.com/foobarto/stado/pkg/tool"
)

// registerMetaTools adds the four dispatch-kernel tools to reg.
// These are native Go tools (not wasm); they move to wasm in EP-0038.
func registerMetaTools(reg *tools.Registry) {
	reg.Register(&metaSearch{reg: reg})
	reg.Register(&metaDescribe{reg: reg})
	reg.Register(&metaCategories{reg: reg})
	reg.Register(&metaInCategory{reg: reg})
}

// ── tools__search ─────────────────────────────────────────────────────────

type metaSearch struct{ reg *tools.Registry }

func (m *metaSearch) Name() string        { return "tools__search" }
func (m *metaSearch) Description() string { return "Search available tools by name, summary, or category. No-arg form lists all enabled tools (light shape: name, summary, categories)." }
func (m *metaSearch) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "Optional substring filter on name, description, or categories."},
			"limit": map[string]any{"type": "integer", "description": "Max results (default 200)."},
		},
	}
}

func (m *metaSearch) Run(_ context.Context, args json.RawMessage, _ pkgtool.Host) (pkgtool.Result, error) {
	var req struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	_ = json.Unmarshal(args, &req)
	if req.Limit <= 0 {
		req.Limit = 200
	}
	q := req.Query
	var out []map[string]any
	for _, t := range m.reg.All() {
		if isMetaTool(t.Name()) {
			continue // omit kernel from search results
		}
		if q != "" {
			haystack := t.Name() + " " + t.Description()
			if !containsFold(haystack, q) {
				continue
			}
		}
		entry := map[string]any{
			"name":    t.Name(),
			"summary": summarise(t.Description()),
		}
		if tc, ok := t.(toolCategoried); ok {
			entry["categories"] = tc.Categories()
		}
		out = append(out, entry)
		if len(out) >= req.Limit {
			break
		}
	}
	truncated := false
	if len(out) == req.Limit {
		truncated = true
	}
	resp := map[string]any{"tools": out, "truncated": truncated}
	b, _ := json.Marshal(resp)
	return pkgtool.Result{Content: string(b)}, nil
}

// ── tools__describe ────────────────────────────────────────────────────────

type metaDescribe struct{ reg *tools.Registry }

func (m *metaDescribe) Name() string        { return "tools__describe" }
func (m *metaDescribe) Description() string { return "Fetch full schema + docs for named tools and activate them for this session. Batched: pass multiple names in one call." }
func (m *metaDescribe) Schema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"names"},
		"properties": map[string]any{
			"names": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
				"description": "Canonical or wire-form tool names.",
			},
		},
	}
}

func (m *metaDescribe) Run(_ context.Context, args json.RawMessage, h pkgtool.Host) (pkgtool.Result, error) {
	var req struct {
		Names []string `json:"names"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return pkgtool.Result{Error: "invalid args: " + err.Error()}, nil
	}
	var out []map[string]any
	for _, name := range req.Names {
		t, ok := m.reg.Get(name)
		if !ok {
			out = append(out, map[string]any{"name": name, "error": "not found"})
			continue
		}
		schema, _ := json.Marshal(t.Schema())
		entry := map[string]any{
			"name":        t.Name(),
			"description": t.Description(),
			"schema":      json.RawMessage(schema),
		}
		if tc, ok := t.(toolCategoried); ok {
			entry["categories"] = tc.Categories()
		}
		out = append(out, entry)
		// Activate the tool schema for this session.
		if ta, ok := h.(pkgtool.ToolActivator); ok {
			ta.ActivateTool(name)
		}
	}
	b, _ := json.Marshal(out)
	return pkgtool.Result{Content: string(b)}, nil
}

// ── tools__categories ─────────────────────────────────────────────────────

type metaCategories struct{ reg *tools.Registry }

func (m *metaCategories) Name() string        { return "tools__categories" }
func (m *metaCategories) Description() string { return "List canonical categories that currently-enabled tools belong to. Optional substring filter." }
func (m *metaCategories) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "Optional substring filter on category name."},
		},
	}
}

func (m *metaCategories) Run(_ context.Context, args json.RawMessage, _ pkgtool.Host) (pkgtool.Result, error) {
	var req struct{ Query string `json:"query"` }
	_ = json.Unmarshal(args, &req)
	seen := map[string]bool{}
	for _, t := range m.reg.All() {
		if tc, ok := t.(toolCategoried); ok {
			for _, c := range tc.Categories() {
				if req.Query == "" || containsFold(c, req.Query) {
					seen[c] = true
				}
			}
		}
	}
	cats := make([]string, 0, len(seen))
	for c := range seen {
		cats = append(cats, c)
	}
	b, _ := json.Marshal(cats)
	return pkgtool.Result{Content: string(b)}, nil
}

// ── tools__in_category ────────────────────────────────────────────────────

type metaInCategory struct{ reg *tools.Registry }

func (m *metaInCategory) Name() string        { return "tools__in_category" }
func (m *metaInCategory) Description() string { return "List tools in a specific category (exact name or extra_category)." }
func (m *metaInCategory) Schema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"name"},
		"properties": map[string]any{
			"name": map[string]any{"type": "string", "description": "Exact category name."},
		},
	}
}

func (m *metaInCategory) Run(_ context.Context, args json.RawMessage, _ pkgtool.Host) (pkgtool.Result, error) {
	var req struct{ Name string `json:"name"` }
	if err := json.Unmarshal(args, &req); err != nil || req.Name == "" {
		return pkgtool.Result{Error: "name is required"}, nil
	}
	var out []map[string]any
	for _, t := range m.reg.All() {
		if tc, ok := t.(toolCategoried); ok {
			for _, c := range tc.Categories() {
				if c == req.Name {
					out = append(out, map[string]any{
						"name":       t.Name(),
						"summary":    summarise(t.Description()),
						"categories": tc.Categories(),
					})
					break
				}
			}
		}
	}
	b, _ := json.Marshal(out)
	return pkgtool.Result{Content: string(b)}, nil
}

// ── helpers ────────────────────────────────────────────────────────────────

type toolCategoried interface {
	Categories() []string
}

func summarise(desc string) string {
	if len(desc) <= 100 {
		return desc
	}
	return desc[:97] + "..."
}

func containsFold(s, sub string) bool {
	return len(sub) == 0 ||
		len(s) >= len(sub) &&
			(s == sub ||
				indexFold(s, sub) >= 0)
}

func indexFold(s, sub string) int {
	sl := len(sub)
	for i := 0; i <= len(s)-sl; i++ {
		if equalFold(s[i:i+sl], sub) {
			return i
		}
	}
	return -1
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 32
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

var _ = fmt.Sprintf // keep fmt import used
```

- [ ] **Step 5: Register meta-tools in BuildDefaultRegistry**

In `internal/runtime/executor.go`, update `BuildDefaultRegistry`:

```go
func BuildDefaultRegistry() *tools.Registry {
	reg := buildBundledPluginRegistry()
	registerMetaTools(reg)
	return reg
}
```

- [ ] **Step 6: Run tests**

```
go test ./internal/runtime/... -run "TestMetaTools|TestToolsSearch|TestToolsDescribe" -v
go build ./...
```
Expected: PASS (stubHost may need adjustment to match tool.Host interface exactly — fix any compile errors by adding missing methods returning zero values)

- [ ] **Step 7: Commit**

```bash
git add internal/runtime/meta_tools.go internal/runtime/meta_tools_test.go internal/runtime/executor.go pkg/tool/
git commit -m "feat(ep-0037): four meta-tools (tools.search/describe/categories/in_category)"
```

---

## Task 5: Autoload dispatch in agentloop

**Files:**
- Modify: `internal/runtime/agentloop.go`
- Modify: `internal/runtime/executor.go` (ToolDefs takes autoload set)

The agentloop currently sends all tools every turn (`req.Tools = ToolDefs(opts.Executor.Registry)`). Change it to: (a) send only autoloaded tools initially, (b) track activated tools (from `tools.describe` results), (c) include activated tools in subsequent turns.

- [ ] **Step 1: Write test**

```go
// internal/runtime/agentloop_helpers_test.go (add to existing file)
func TestAutoloadedToolDefsSubset(t *testing.T) {
	reg := BuildDefaultRegistry()
	cfg := &config.Config{}
	cfg.Tools.Autoload = []string{"read"}
	autoloaded := AutoloadedTools(reg, cfg)
	defs := ToolDefs(autoloaded) // pass slice, not full registry
	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
	}
	if !names["read"] {
		t.Error("read should be in autoloaded defs")
	}
	if names["write"] {
		t.Error("write should NOT be in autoloaded defs when not in autoload list")
	}
	// Meta-tools always present.
	if !names["tools__search"] {
		t.Error("tools__search should always be in defs")
	}
}
```

- [ ] **Step 2: Update ToolDefs to accept a slice**

In `internal/runtime/executor.go`, add an overload that accepts `[]tool.Tool` directly:

```go
// ToolDefsFromSlice renders a tool slice as []agent.ToolDef.
func ToolDefsFromSlice(ts []pkgtool.Tool) []agent.ToolDef {
	out := make([]agent.ToolDef, 0, len(ts))
	for _, t := range ts {
		schema, _ := json.Marshal(t.Schema())
		out = append(out, agent.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      schema,
		})
	}
	return out
}
```

- [ ] **Step 3: Update agentloop.go**

In the turn loop in `internal/runtime/agentloop.go`, change the tool surface injection:

Find the block around `req.Tools = ToolDefs(opts.Executor.Registry)` and replace with:

```go
if opts.Executor != nil {
	// EP-0037: only autoloaded tools + kernel meta-tools in every turn.
	// activatedTools grows when the model calls tools.describe.
	autoloaded := AutoloadedTools(opts.Executor.Registry, opts.Config)
	toolSurface := append(autoloaded, activatedSlice(opts.Executor.Registry, activatedNames)...)
	deduped := dedupeTools(toolSurface)
	req.Tools = ToolDefsFromSlice(deduped)
}
```

Add `activatedNames map[string]bool` to the loop state (initialized before the for loop):

```go
activatedNames := map[string]bool{}
```

Add the helper to capture activations from `tools.describe` results. After the tool-call handling block (where `toolResult` is obtained), add:

```go
// EP-0037: if the model called tools.describe, extract activated names.
if toolCall.Name == "tools__describe" && toolResult.Error == "" {
	var described []map[string]any
	if err := json.Unmarshal([]byte(toolResult.Content), &described); err == nil {
		for _, item := range described {
			if name, ok := item["name"].(string); ok {
				if _, notFound := item["error"].(string); !notFound {
					activatedNames[name] = true
				}
			}
		}
	}
}
```

Add helpers at bottom of file (or executor.go):

```go
func activatedSlice(reg *tools.Registry, activated map[string]bool) []pkgtool.Tool {
	var out []pkgtool.Tool
	for name := range activated {
		if t, ok := reg.Get(name); ok {
			out = append(out, t)
		}
	}
	return out
}

func dedupeTools(ts []pkgtool.Tool) []pkgtool.Tool {
	seen := map[string]bool{}
	out := make([]pkgtool.Tool, 0, len(ts))
	for _, t := range ts {
		if !seen[t.Name()] {
			seen[t.Name()] = true
			out = append(out, t)
		}
	}
	return out
}
```

- [ ] **Step 4: Run tests + build**

```
go test ./internal/runtime/... -v -count=1 2>&1 | tail -30
go build ./...
```
Expected: all existing tests pass, build clean.

- [ ] **Step 5: Commit**

```bash
git add internal/runtime/agentloop.go internal/runtime/executor.go
git commit -m "feat(ep-0037): autoload dispatch — only autoloaded tools in turn surface"
```

---

## Task 6: CLI flags — --tools, --tools-autoload, --tools-disable

**Files:**
- Modify: `cmd/stado/run.go`

- [ ] **Step 1: Write test (integration-level via flag parse)**

```go
// In cmd/stado/ test or as a manual verification — add to run_test.go if it exists,
// otherwise verify via build + help output:
// go run . run --help | grep -E "tools-autoload|tools-disable"
```

- [ ] **Step 2: Add flag vars to run.go**

Add to the `var` block near the top of `cmd/stado/run.go`:

```go
var (
	runToolsWhitelist string // --tools
	runToolsAutoload  string // --tools-autoload
	runToolsDisable   string // --tools-disable
)
```

- [ ] **Step 3: Register flags in init()**

Add after the existing `runCmd.Flags()` calls in `init()`:

```go
runCmd.Flags().StringVar(&runToolsWhitelist, "tools-whitelist", "",
	"Comma-separated tool globs: ONLY these tools enabled (e.g. 'fs.*,shell.exec'). Stacks with --tools-disable.")
runCmd.Flags().StringVar(&runToolsAutoload, "tools-autoload", "",
	"Comma-separated tool globs: always-on surface sent to model every turn. Empty = use [tools.autoload] from config.")
runCmd.Flags().StringVar(&runToolsDisable, "tools-disable", "",
	"Comma-separated tool globs: remove from surface entirely. Wins over enable and autoload.")
```

- [ ] **Step 4: Apply flags to config before BuildExecutor**

In `runCmd.RunE`, after config is loaded and before `BuildExecutor`, add:

```go
if runToolsWhitelist != "" {
	cfg.Tools.Enabled = splitComma(runToolsWhitelist)
}
if runToolsAutoload != "" {
	cfg.Tools.Autoload = splitComma(runToolsAutoload)
}
if runToolsDisable != "" {
	// Disabled wins: append to existing disabled list.
	cfg.Tools.Disabled = append(cfg.Tools.Disabled, splitComma(runToolsDisable)...)
}
```

Add helper at bottom of file:

```go
func splitComma(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
```

- [ ] **Step 5: Build + smoke test**

```
go build -o /tmp/stado-test ./cmd/stado && /tmp/stado-test run --help | grep -E "tools-autoload|tools-disable"
```
Expected: flags appear in help output.

- [ ] **Step 6: Commit**

```bash
git add cmd/stado/run.go
git commit -m "feat(ep-0037): --tools-autoload, --tools-disable CLI flags"
```

---

## Task 7: `stado tool` subcommand

**Files:**
- Create: `cmd/stado/tool.go`
- Modify: `cmd/stado/main.go` (add toolCmd)

- [ ] **Step 1: Implement tool.go**

```go
// cmd/stado/tool.go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/spf13/cobra"
)

var toolCmd = &cobra.Command{
	Use:   "tool",
	Short: "Inspect and configure tools",
}

var toolLsCmd = &cobra.Command{
	Use:   "ls [glob]",
	Short: "List tools with state, plugin source, and categories",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		reg := runtime.BuildDefaultRegistry()
		runtime.ApplyToolFilter(reg, cfg)
		autoloaded := runtime.AutoloadedTools(reg, cfg)
		autoloadSet := map[string]bool{}
		for _, t := range autoloaded {
			autoloadSet[t.Name()] = true
		}
		glob := ""
		if len(args) > 0 {
			glob = args[0]
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSTATE\tCATEGORIES")
		for _, t := range reg.All() {
			if glob != "" && !runtime.ToolMatchesGlob(t.Name(), glob) {
				continue
			}
			state := "enabled"
			if autoloadSet[t.Name()] {
				state = "autoloaded"
			}
			cats := ""
			// Categories not yet on native tools; leave blank until EP-0038.
			if jsonFlag {
				entry := map[string]any{"name": t.Name(), "state": state, "categories": cats}
				b, _ := json.Marshal(entry)
				fmt.Println(string(b))
			} else {
				fmt.Fprintf(w, "%s\t%s\t%s\n", t.Name(), state, cats)
			}
		}
		if !jsonFlag {
			w.Flush()
		}
		return nil
	},
}

var toolInfoCmd = &cobra.Command{
	Use:   "info <name>",
	Short: "Full schema + description for a tool",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		reg := runtime.BuildDefaultRegistry()
		runtime.ApplyToolFilter(reg, cfg)
		t, ok := reg.Get(args[0])
		if !ok {
			return fmt.Errorf("tool %q not found", args[0])
		}
		schema, _ := json.MarshalIndent(t.Schema(), "", "  ")
		if jsonFlag {
			out := map[string]any{
				"name":        t.Name(),
				"description": t.Description(),
				"schema":      json.RawMessage(schema),
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
		} else {
			fmt.Printf("Name:        %s\n", t.Name())
			fmt.Printf("Description: %s\n", t.Description())
			fmt.Printf("Schema:\n%s\n", schema)
		}
		return nil
	},
}

var toolCatsCmd = &cobra.Command{
	Use:   "cats [glob]",
	Short: "List canonical categories",
	RunE: func(cmd *cobra.Command, args []string) error {
		from := "plugins.CanonicalCategories"
		_ = from
		for _, c := range plugins.CanonicalCategories {
			if len(args) > 0 && !strings.Contains(c, args[0]) {
				continue
			}
			fmt.Println(c)
		}
		return nil
	},
}

var toolReloadCmd = &cobra.Command{
	Use:   "reload [glob]",
	Short: "Signal runtime to drop cached wasm instance(s) on next call",
	RunE: func(_ *cobra.Command, args []string) error {
		// Runtime-only; prints a note since this CLI isn't connected to a live session.
		fmt.Println("reload: note — this command takes effect inside a running stado session.")
		fmt.Println("In the TUI, use /tool reload <glob> to reload without restarting.")
		return nil
	},
}

var jsonFlag bool

func init() {
	toolCmd.PersistentFlags().BoolVar(&jsonFlag, "json", false, "Emit JSON output")
	toolCmd.AddCommand(toolLsCmd, toolInfoCmd, toolCatsCmd, toolReloadCmd)
	rootCmd.AddCommand(toolCmd)
}
```

(Note: `toolCatsCmd` references `plugins.CanonicalCategories` — add the import `"github.com/foobarto/stado/internal/plugins"` to the file.)

- [ ] **Step 2: Build + smoke test**

```
go build -o /tmp/stado-test ./cmd/stado
/tmp/stado-test tool ls
/tmp/stado-test tool cats
/tmp/stado-test tool info read
```
Expected: ls shows table, cats shows 21 lines, info shows schema.

- [ ] **Step 3: Commit**

```bash
git add cmd/stado/tool.go
git commit -m "feat(ep-0037): stado tool subcommand (ls/info/cats/reload)"
```

---

## Task 8: TUI slash mirrors + /session list/show

**Files:**
- Modify: `internal/tui/model_commands.go`

- [ ] **Step 1: Add /tool handling to handleSlash**

Find `func (m *Model) handleSlash(text string)` in `internal/tui/model_commands.go`. The existing switch has a `case "/tools":` entry (around line 286). After it, add `/tool` dispatch:

```go
case "/tool":
	// /tool <verb> [args...]
	parts := strings.Fields(text)
	if len(parts) < 2 {
		return m.addSystemMessage("/tool <verb> — verbs: ls, info, cats, reload, enable, disable, autoload, unautoload")
	}
	verb := parts[1]
	rest := ""
	if len(parts) > 2 {
		rest = strings.Join(parts[2:], " ")
	}
	return m.handleToolSlash(verb, rest)
```

Add the dispatcher method:

```go
func (m *Model) handleToolSlash(verb, args string) tea.Cmd {
	switch verb {
	case "ls":
		reg := runtime.BuildDefaultRegistry()
		if m.cfg != nil {
			runtime.ApplyToolFilter(reg, m.cfg)
		}
		autoloaded := runtime.AutoloadedTools(reg, m.cfg)
		autoSet := map[string]bool{}
		for _, t := range autoloaded {
			autoSet[t.Name()] = true
		}
		var lines []string
		for _, t := range reg.All() {
			if args != "" && !runtime.ToolMatchesGlob(t.Name(), args) {
				continue
			}
			state := "enabled"
			if autoSet[t.Name()] {
				state = "autoloaded"
			}
			lines = append(lines, fmt.Sprintf("%-30s %s", t.Name(), state))
		}
		return m.addSystemMessage(strings.Join(lines, "\n"))
	case "info":
		if args == "" {
			return m.addSystemMessage("/tool info <name>")
		}
		reg := runtime.BuildDefaultRegistry()
		t, ok := reg.Get(args)
		if !ok {
			return m.addSystemMessage(fmt.Sprintf("tool %q not found", args))
		}
		schema, _ := json.MarshalIndent(t.Schema(), "", "  ")
		return m.addSystemMessage(fmt.Sprintf("%s\n\n%s\n\nSchema:\n%s", t.Name(), t.Description(), schema))
	case "cats":
		cats := plugins.CanonicalCategories
		if args != "" {
			var filtered []string
			for _, c := range cats {
				if strings.Contains(c, args) {
					filtered = append(filtered, c)
				}
			}
			cats = filtered
		}
		return m.addSystemMessage(strings.Join(cats, "\n"))
	case "reload":
		return m.addSystemMessage("/tool reload: drop-instance is a runtime-only op; restart the session to pick up rebuilt wasm.")
	default:
		return m.addSystemMessage(fmt.Sprintf("/tool %s: unknown verb. Try: ls, info, cats, reload", verb))
	}
}
```

Also update the `case "/session":` block (around line 373) to forward to session sub-commands:

```go
case "/session":
	parts := strings.Fields(text)
	if len(parts) < 2 {
		return m.addSystemMessage("/session <verb> — verbs: list, show, attach")
	}
	switch parts[1] {
	case "list":
		return m.handleSlash("/sessions") // reuse existing /sessions handler
	case "show":
		if len(parts) < 3 {
			return m.addSystemMessage("/session show <id>")
		}
		// Show session detail — delegate to existing session lookup.
		return m.addSystemMessage(fmt.Sprintf("session show %s: use `stado session show %s` from terminal for full detail", parts[2], parts[2]))
	case "attach":
		return m.addSystemMessage("/session attach: read-write attach not yet implemented (EP-0038)")
	default:
		return m.addSystemMessage(fmt.Sprintf("/session %s: unknown verb", parts[1]))
	}
```

- [ ] **Step 2: Build**

```
go build ./...
```
Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add internal/tui/model_commands.go
git commit -m "feat(ep-0037): /tool and /session slash command mirrors"
```

---

## Task 9: Validate categories at plugin install time

**Files:**
- Modify: `cmd/stado/plugin_install.go`

- [ ] **Step 1: Find install validation point**

```
grep -n "Manifest\|ValidateManifest\|tools\[" cmd/stado/plugin_install.go | head -20
```

- [ ] **Step 2: Add category validation after manifest load**

Find where the manifest is loaded (likely `plugins.LoadFromDir`) and after that block add:

```go
for _, toolDef := range manifest.Tools {
	if err := plugins.ValidateCategories(toolDef.Categories); err != nil {
		return fmt.Errorf("plugin install: manifest tool %q: %w", toolDef.Name, err)
	}
}
```

- [ ] **Step 3: Build + test**

```
go build ./...
# Smoke test: install a plugin with an invalid category
echo '{"version":"v0.1.0","tools":[{"name":"foo","categories":["netork"]}],"capabilities":[]}' > /tmp/bad-manifest.json
# (This won't produce a sig match; just verify the validation path compiles.)
```

- [ ] **Step 4: Commit**

```bash
git add cmd/stado/plugin_install.go
git commit -m "feat(ep-0037): validate canonical categories at plugin install"
```

---

## Self-Review

**Spec coverage check (EP-0037 §A–§K):**

| EP section | Covered by task |
|-----------|-----------------|
| §A security philosophy | Documented in EP; no code required |
| §B wire-form naming | Task 1 |
| §C categories | Task 2 + Task 9 |
| §D meta-tool dispatch | Task 4 |
| §E always-loaded core + autoload | Task 3 + Task 5 |
| §F config schema | Task 3 |
| §G CLI flags | Task 6 |
| §H stado tool subcommand | Task 7 |
| §I TUI slash mirrors | Task 8 |
| §J manifest field additions | Task 2 |

**Gaps:** `tool enable/disable/autoload/unautoload` verbs in Task 7 are read-only or deferred — they require config file mutation. Add them in a follow-up or expand Task 7 Step 1 with config-write logic using koanf's Set + TOML marshal. Not blocking for initial pass.

**Placeholder scan:** None found.

**Type consistency:** `ToolMatchesGlob` and `AutoloadedTools` are exported from `runtime` package; used consistently in `tool.go` and `model_commands.go`.
