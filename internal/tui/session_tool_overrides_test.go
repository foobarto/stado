package tui

import (
	"reflect"
	"sort"
	"testing"

	"github.com/foobarto/stado/internal/config"
	rt "github.com/foobarto/stado/internal/runtime"
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
	ov := sessionToolOverrides{
		enableRemove: []string{"shell.exec"},
		disableAdd:   []string{"shell.exec"},
	}

	got := ov.effectiveTools(cfg)
	for _, n := range got.Enabled {
		if n == "shell.exec" {
			t.Errorf("disabled tool should be removed from Enabled; got %v", got.Enabled)
		}
	}
	if !containsString(got.Disabled, "shell.exec") {
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
	if containsString(got.Autoload, "fs.*") {
		t.Errorf("autoload remove should drop fs.*; got %v", got.Autoload)
	}
	if !containsString(got.Autoload, "shell.exec") {
		t.Errorf("autoload add should include shell.exec; got %v", got.Autoload)
	}
}

// Codex #088: when the operator runs `/tool unautoload bash` on a
// default-empty config, the override must take effect. Pre-fix,
// applyOverride returned an empty Autoload, AutoloadedTools fell
// back to defaultAutoloadNames, and bash was still autoloaded.
// Post-fix, effectiveTools materializes the runtime defaults into
// the override-input before applying the remove, so the resulting
// Autoload is `defaults - bash` — and AutoloadedTools sees that
// non-empty list and uses it directly.
func TestEffectiveTools_AutoloadRemove_OnEmptyBase_MaterializesDefaults(t *testing.T) {
	cfg := &config.Config{} // empty Autoload (operator never set [tools].autoload)
	ov := sessionToolOverrides{
		autoloadRemove: []string{"shell__bash"},
	}
	got := ov.effectiveTools(cfg)

	// Result must be non-empty (else AutoloadedTools would silently
	// fall back to defaults — the very bug we're fixing).
	if len(got.Autoload) == 0 {
		t.Fatal("autoload remove on empty base should materialize defaults — Autoload empty would silently revert")
	}
	// shell__bash should be gone.
	for _, n := range got.Autoload {
		if n == "shell__bash" {
			t.Errorf("shell__bash should be removed; still present in %v", got.Autoload)
		}
	}
	// Other defaults should still be present (e.g. fs__read).
	found := false
	for _, n := range got.Autoload {
		if n == "fs__read" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("other defaults should remain after removing one; got %v", got.Autoload)
	}
}

// Parity: /tool autoload --save on a default-empty config must also
// produce defaults + the new addition, NOT just the addition (which
// would silently drop every default the operator was relying on).
func TestEffectiveTools_AutoloadAdd_OnEmptyBase_MaterializesDefaults(t *testing.T) {
	cfg := &config.Config{}
	ov := sessionToolOverrides{
		autoloadAdd: []string{"mcp__llm__invoke"},
	}
	got := ov.effectiveTools(cfg)

	// New addition present.
	found := false
	for _, n := range got.Autoload {
		if n == "mcp__llm__invoke" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("autoload add should include the new tool; got %v", got.Autoload)
	}
	// Defaults still present.
	foundDef := false
	for _, n := range got.Autoload {
		if n == "fs__read" {
			foundDef = true
			break
		}
	}
	if !foundDef {
		t.Errorf("autoload add on empty base should not drop defaults; got %v", got.Autoload)
	}
}

// When base.Autoload is NON-empty (operator explicitly configured),
// the materialization must NOT trigger — the operator's narrowed
// list is the source of truth. Defends the "don't widen operator's
// explicit narrowing" invariant.
func TestEffectiveTools_AutoloadRemove_OnNonEmptyBase_NoMaterialization(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Autoload = []string{"my_only_tool"}
	ov := sessionToolOverrides{
		autoloadRemove: []string{"my_only_tool"},
	}
	got := ov.effectiveTools(cfg)

	if len(got.Autoload) != 0 {
		t.Errorf("operator's narrowed list should not be re-widened by materialization; got %v", got.Autoload)
	}
}

// No override at all → result is identical to base, no materialization.
func TestEffectiveTools_AutoloadNoOverride_NoMaterialization(t *testing.T) {
	cfg := &config.Config{} // empty Autoload
	var ov sessionToolOverrides
	got := ov.effectiveTools(cfg)
	if len(got.Autoload) != 0 {
		t.Errorf("no override should leave Autoload empty (deferring to AutoloadedTools defaults); got %v", got.Autoload)
	}
}

func TestEffectiveTools_NilConfig(t *testing.T) {
	// Nil cfg is allowed; result is the override-add lists only.
	ov := sessionToolOverrides{enableAdd: []string{"x"}}
	got := ov.effectiveTools(nil)
	if !containsString(got.Enabled, "x") {
		t.Errorf("nil cfg with override should still produce Enabled=[x]; got %v", got)
	}
}

func TestSessionToolOverrides_IsZero(t *testing.T) {
	var ov sessionToolOverrides
	if !ov.isZero() {
		t.Error("zero value should be isZero=true")
	}
	ov.enableAdd = []string{"x"}
	if ov.isZero() {
		t.Error("populated overrides should be isZero=false")
	}
}

// containsString — local helper for slice membership checks. The
// existing tui test helper named `contains` is a substring matcher
// over strings, so a separate helper is needed here.
func containsString(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// TestEffectiveConfig_NoOverrides returns m.cfg unchanged.
func TestEffectiveConfig_NoOverrides(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Autoload = []string{"fs.*"}
	m := &Model{cfg: cfg}
	if m.effectiveConfig() != cfg {
		t.Error("zero overrides should return identical *Config (pointer equality)")
	}
}

// TestEffectiveConfig_NilCfg returns nil.
func TestEffectiveConfig_NilCfg(t *testing.T) {
	m := &Model{cfg: nil}
	if m.effectiveConfig() != nil {
		t.Error("nil cfg should produce nil effectiveConfig")
	}
}

// TestEffectiveConfig_OverridesProduceCopy: when overrides exist,
// returned config is distinct from m.cfg and reflects the merged view.
func TestEffectiveConfig_OverridesProduceCopy(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Autoload = []string{"fs.read"}
	m := &Model{cfg: cfg}
	m.sessionToolOverrides.autoloadAdd = []string{"shell.exec"}

	eff := m.effectiveConfig()
	if eff == cfg {
		t.Error("with overrides, effectiveConfig should return a NEW config, not the original pointer")
	}
	if !containsString(eff.Tools.Autoload, "shell.exec") {
		t.Errorf("effective autoload should include override-added; got %v", eff.Tools.Autoload)
	}
	// Original unchanged.
	if containsString(cfg.Tools.Autoload, "shell.exec") {
		t.Errorf("original cfg.Tools.Autoload was mutated; got %v", cfg.Tools.Autoload)
	}
}

// TestEffectiveConfig_FlowsToAutoloadedTools confirms the override
// flows into the runtime.AutoloadedTools computation. This is the
// integration check the slash verbs depend on.
func TestEffectiveConfig_FlowsToAutoloadedTools(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tools.Autoload = nil // disk: nothing autoloaded
	m := &Model{cfg: cfg}
	m.sessionToolOverrides.autoloadAdd = []string{"fs__read"}

	reg := rt.BuildDefaultRegistry(nil)
	eff := m.effectiveConfig()
	rt.ApplyToolFilter(reg, eff)
	got := rt.AutoloadedTools(reg, eff)

	found := false
	for _, tt := range got {
		if tt.Name() == "fs__read" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("override should make 'fs__read' autoloaded; got %d autoloaded tools", len(got))
	}
}

// TestSessionToolOverrideHidesTool covers the small predicate.
func TestSessionToolOverrideHidesTool(t *testing.T) {
	m := &Model{}
	m.sessionToolOverrides.disableAdd = []string{"shell.exec"}
	if !m.sessionToolOverrideHidesTool("shell.exec") {
		t.Error("disableAdd should hide the tool")
	}
	if m.sessionToolOverrideHidesTool("fs.read") {
		t.Error("unrelated tool shouldn't be hidden")
	}

	// disableRemove un-hides what disableAdd would otherwise hide.
	m.sessionToolOverrides.disableRemove = []string{"shell.exec"}
	if m.sessionToolOverrideHidesTool("shell.exec") {
		t.Error("disableRemove should beat disableAdd")
	}

	// enableRemove also hides.
	m2 := &Model{}
	m2.sessionToolOverrides.enableRemove = []string{"web.fetch"}
	if !m2.sessionToolOverrideHidesTool("web.fetch") {
		t.Error("enableRemove should hide the tool")
	}
}
