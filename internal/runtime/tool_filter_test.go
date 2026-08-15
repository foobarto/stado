package runtime

import (
	"sort"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	pkgtoolPkg "github.com/foobarto/stado/pkg/tool"
)

// TestApplyToolFilter_DefaultKeepsEverything: no config values →
// registry unchanged.
func TestApplyToolFilter_DefaultKeepsEverything(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	before := len(reg.All())
	cfg := &config.Config{}
	ApplyToolFilter(reg, cfg)
	if got := len(reg.All()); got != before {
		t.Errorf("no-config filter shouldn't change count; was %d, got %d", before, got)
	}
}

// TestApplyToolFilter_EnabledAllowlist: only the listed tools
// survive.
func TestApplyToolFilter_EnabledAllowlist(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	cfg := &config.Config{}
	cfg.Tools.Enabled = []string{"fs__read", "fs__grep"}
	ApplyToolFilter(reg, cfg)

	var names []string
	for _, tl := range reg.All() {
		names = append(names, tl.Name())
	}
	sort.Strings(names)
	want := []string{"fs__grep", "fs__read"}
	if len(names) != 2 {
		t.Fatalf("expected 2 tools (read+grep), got %d: %v", len(names), names)
	}
	for i, n := range want {
		if names[i] != n {
			t.Errorf("names[%d] = %q, want %q", i, names[i], n)
		}
	}
}

// TestApplyToolFilter_DisabledRemovesNamed: disabled removes only
// the listed tools; all others remain.
func TestApplyToolFilter_DisabledRemovesNamed(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	before := len(reg.All())
	cfg := &config.Config{}
	cfg.Tools.Disabled = []string{"shell__bash", "web__fetch"}
	ApplyToolFilter(reg, cfg)

	after := len(reg.All())
	if after != before-2 {
		t.Errorf("disabled should trim 2 tools; was %d → %d", before, after)
	}
	for _, tl := range reg.All() {
		if tl.Name() == "shell__bash" || tl.Name() == "web__fetch" {
			t.Errorf("tool %q should have been removed", tl.Name())
		}
	}
}

// TestApplyToolFilter_EnabledWinsOverDisabled: when both are set,
// Enabled is authoritative (allowlist) and Disabled is ignored.
// TestApplyToolFilter_DisabledWinsOverEnabled: when both lists name the
// same tool, the more-restrictive list (disabled) wins. Codex #096
// caught the inverse — enabled previously won, which meant an
// allowlist of `*` plus a disable of `bash` left bash registered. The
// fix applies disabled as a subtractive pass after the enabled
// allowlist; this test pins the new semantic so a future "tidy up"
// that reverts the order regresses loudly. Replaces the prior
// TestApplyToolFilter_EnabledWinsOverDisabled which pinned the bug.
func TestApplyToolFilter_DisabledWinsOverEnabled(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	cfg := &config.Config{}
	cfg.Tools.Enabled = []string{"fs__read"}
	cfg.Tools.Disabled = []string{"fs__read"}
	ApplyToolFilter(reg, cfg)

	tools := reg.All()
	for _, t0 := range tools {
		if t0.Name() == "fs__read" {
			t.Errorf("expected fs__read removed by disabled-after-allowlist pass; still registered")
		}
	}
}

// TestApplyToolFilter_DisabledSubtractsFromGlobAllowlist: the realistic
// configuration Codex #096 highlighted — broad allowlist plus a few
// specific denies. Pre-fix `["*"]` + disable `bash` left bash
// registered (allow matched everything, disabled was unreachable).
// Now bash is removed; other tools the allowlist matched stay.
func TestApplyToolFilter_DisabledSubtractsFromGlobAllowlist(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	beforeReadPresent := false
	for _, t0 := range reg.All() {
		if t0.Name() == "fs__read" {
			beforeReadPresent = true
			break
		}
	}
	if !beforeReadPresent {
		t.Skip("default registry doesn't include fs__read on this build")
	}

	cfg := &config.Config{}
	cfg.Tools.Enabled = []string{"*"}
	cfg.Tools.Disabled = []string{"fs__read"}
	ApplyToolFilter(reg, cfg)

	for _, t0 := range reg.All() {
		if t0.Name() == "fs__read" {
			t.Errorf("[tools].enabled=[\"*\"] + disable=[\"fs__read\"] should remove fs__read via subtractive pass; still registered")
		}
	}
	// At least one non-fs__read tool should remain (allowlist still matched).
	if len(reg.All()) == 0 {
		t.Error("subtractive disabled pass should not empty the registry when allowlist matched other tools")
	}
}

// TestApplyToolFilter_UnknownNamesTolerated: a typo in either list
// shouldn't panic or remove anything unexpected.
func TestApplyToolFilter_UnknownNamesTolerated(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	before := len(reg.All())
	cfg := &config.Config{}
	cfg.Tools.Disabled = []string{"nopes-not-a-real-tool"}
	ApplyToolFilter(reg, cfg)
	if got := len(reg.All()); got != before {
		t.Errorf("unknown tool name should be a no-op; was %d, got %d", before, got)
	}
}

// TestApplyToolFilter_EmptyAllowFailsClosed: when [tools].enabled is
// non-empty but matches zero registered tools, the registry is emptied
// (not left untouched). Operator typos / refs to uninstalled tools
// shouldn't silently re-expose the entire tool surface — that defeats
// the whole point of an allowlist. The filter prints a stderr advisory
// naming the unmatched patterns and unregisters everything. Replaces
// the prior TestApplyToolFilter_UnknownEnabledNamesDoNotEmptyRegistry,
// which asserted the buggy fall-open behaviour as if it were intent.
func TestApplyToolFilter_EmptyAllowFailsClosed(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	if len(reg.All()) == 0 {
		t.Fatal("default registry should contain tools; got empty")
	}
	cfg := &config.Config{}
	cfg.Tools.Enabled = []string{"renamed-tool", "missing-tool"}
	ApplyToolFilter(reg, cfg)
	if len(reg.All()) != 0 {
		t.Errorf("unmatched [tools].enabled should empty the registry; got %v", listNames(reg.All()))
	}
}

// TestApplyToolFilter_CanonicalNameMatchesWireForm: an operator
// configuring [tools].enabled = ["fs.read"] reasonably expects the
// registered wire-form tool fs__read to survive the filter and other
// tools to be dropped. Before the canonical-vs-wire match in
// ToolMatchesGlob, exact-canonical patterns silently failed to match
// any wire-form name, the empty-allow fall-open then left every tool
// enabled, and the operator's allowlist was effectively a no-op.
// Lock the right behaviour in.
func TestApplyToolFilter_CanonicalNameMatchesWireForm(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	cfg := &config.Config{}
	cfg.Tools.Enabled = []string{"fs.read"}
	ApplyToolFilter(reg, cfg)
	if _, ok := reg.Get("fs__read"); !ok {
		t.Errorf("fs__read should survive [tools].enabled=[fs.read] (canonical → wire match)")
	}
	for _, tl := range reg.All() {
		if tl.Name() != "fs__read" {
			t.Errorf("tool %q should have been filtered out by [fs.read] allowlist", tl.Name())
		}
	}
}

func TestSkillsLoadHasNoNativeFallback(t *testing.T) {
	if _, ok := BuildDefaultRegistry(nil).Get("skills__load"); ok {
		t.Fatal("native registry resurrected skills__load; it belongs to an explicitly installed WASM plugin")
	}
}

func TestToolMatchesGlob(t *testing.T) {
	cases := []struct {
		name, pat string
		want      bool
	}{
		// Exact bare-name match (pre-EP-0038 tools)
		{"fs__read", "fs__read", true},
		{"bash", "bash", true},
		{"webfetch", "web.*", false}, // bare "webfetch" has no __ separator, doesn't match "web.*"
		// Wire-form with dotted glob
		{"fs__read", "fs.*", true},
		{"fs__write", "fs.*", true},
		{"shell__exec", "fs.*", false},
		{"tools__search", "tools.*", true},
		{"tools__describe", "tools.*", true},
		// Universal wildcard
		{"fs__read", "*", true},
		{"fs__read", "*", true},
		// No match
		{"web__fetch", "fs.*", false},
		// Canonical-form pattern against wire-form registered name —
		// what an operator types in [tools].enabled / --tools.
		{"fs__read", "fs.read", true},
		{"shell__bash", "shell.bash", true},
		{"fs__read", "shell.bash", false},
		// Canonical-with-dash: alias "htb-lab" → wire "htb_lab"
		{"htb_lab__spawn", "htb-lab.spawn", false}, // exact canonical doesn't normalise dashes; pattern must use the wire-segment form. Documented behaviour.
		// Removed pre-v1 bare aliases are not a second contract language.
		// Operators configure exact manifest names or canonical dotted names.
		{"web__fetch", "webfetch", false},
		{"shell__exec", "bash", false},
		{"fs__read", "read", false},
		{"fs__ls", "ls", false},
		{"web__fetch", "ripgrep", false},
		// Empty inputs.
		{"", "", true},
		{"fs__read", "", false},
	}
	for _, c := range cases {
		got := ToolMatchesGlob(c.name, c.pat)
		if got != c.want {
			t.Errorf("ToolMatchesGlob(%q, %q) = %v, want %v", c.name, c.pat, got, c.want)
		}
	}
}

func TestAutoloadedTools_DefaultCore(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	cfg := &config.Config{} // empty — use hardcoded defaults
	autoloaded := AutoloadedTools(reg, cfg)

	names := map[string]bool{}
	for _, tl := range autoloaded {
		names[tl.Name()] = true
	}
	// Default convenience tools must be present. Step 4 of EP-no-internal-
	// tools renamed bare 'bash' to wire-form 'shell__bash'.
	for _, want := range []string{"fs__read", "fs__write", "fs__edit", "fs__glob", "fs__grep", "shell__bash"} {
		if !names[want] {
			t.Errorf("default autoload should include %q", want)
		}
	}
}

// TestAutoloadedTools_CategoriesAddTools: AutoloadCategories pulls
// tools whose Categories metadata overlaps; layered on top of the
// name-based autoload (union, deduped).
func TestAutoloadedTools_CategoriesAddTools(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	cfg := &config.Config{}
	cfg.Tools.AutoloadCategories = []string{"file"}
	autoloaded := AutoloadedTools(reg, cfg)
	// The bundled fs tools are tagged "file"; verify some of them
	// surface even without an explicit name-based autoload.
	want := []string{"fs__read", "fs__write"}
	got := map[string]bool{}
	for _, t := range autoloaded {
		got[t.Name()] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Errorf("autoload-category=file should pull %q; got: %v", name, listNames(autoloaded))
		}
	}
}

// listNames is a small helper to render a tool slice as a sorted
// comma-joined string for error messages.
func listNames(ts []pkgtoolPkg.Tool) string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.Name())
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func TestAutoloadedTools_CustomAutoload(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	cfg := &config.Config{}
	cfg.Tools.Autoload = []string{"fs__read", "fs__grep"}
	autoloaded := AutoloadedTools(reg, cfg)

	names := map[string]bool{}
	for _, tl := range autoloaded {
		names[tl.Name()] = true
	}
	if !names["fs__read"] || !names["fs__grep"] {
		t.Error("custom autoload should include read and grep")
	}
	if names["bash"] {
		t.Error("bash should NOT be autoloaded when not in custom autoload list")
	}
}

// TestAutoloadedToolsWithExtra_PromotesPersonaTools: extra patterns
// (a persona's EffectiveTools) are merged ADDITIVELY into the autoload
// surface on top of cfg's set — the global defaults are never dropped.
func TestAutoloadedToolsWithExtra_PromotesPersonaTools(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	cfg := &config.Config{} // empty → hardcoded core defaults

	base := AutoloadedTools(reg, cfg)
	baseNames := map[string]bool{}
	for _, tl := range base {
		baseNames[tl.Name()] = true
	}
	// Pick a registered tool that is NOT in the default core to prove
	// promotion adds it.
	var promote string
	for _, tl := range reg.All() {
		if !baseNames[tl.Name()] {
			promote = tl.Name()
			break
		}
	}
	if promote == "" {
		t.Skip("no non-core registered tool available to test promotion")
	}

	got := AutoloadedToolsWithExtra(reg, cfg, []string{promote})
	gotNames := map[string]bool{}
	for _, tl := range got {
		gotNames[tl.Name()] = true
	}
	if !gotNames[promote] {
		t.Errorf("persona extra %q should be promoted into autoload surface; got %v", promote, listNames(got))
	}
	// Additive: every default-core tool must still be present.
	for name := range baseNames {
		if !gotNames[name] {
			t.Errorf("promotion must be additive; default %q disappeared", name)
		}
	}
}

// TestAutoloadedToolsWithExtra_NilExtraEqualsPlain: passing no extras is
// identical to plain AutoloadedTools (no behavior drift for the default path).
func TestAutoloadedToolsWithExtra_NilExtraEqualsPlain(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	cfg := &config.Config{}
	plain := listNames(AutoloadedTools(reg, cfg))
	withNil := listNames(AutoloadedToolsWithExtra(reg, cfg, nil))
	if plain != withNil {
		t.Errorf("nil-extra should equal plain autoload:\n plain=%s\n  nil =%s", plain, withNil)
	}
}

// TestAutoloadedToolsWithExtra_GlobPromotion: a glob extra promotes every
// matching registered tool (additive).
func TestAutoloadedToolsWithExtra_GlobPromotion(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	cfg := &config.Config{}
	cfg.Tools.Autoload = []string{"fs__read"} // narrow base
	got := AutoloadedToolsWithExtra(reg, cfg, []string{"fs.*"})
	names := map[string]bool{}
	for _, tl := range got {
		names[tl.Name()] = true
	}
	for _, want := range []string{"fs__read", "fs__write", "fs__edit"} {
		if !names[want] {
			t.Errorf("fs.* extra should promote %q; got %v", want, listNames(got))
		}
	}
}

func TestApplyToolFilter_WildcardDisabled(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	cfg := &config.Config{}
	cfg.Tools.Disabled = []string{"bash"} // exact name
	ApplyToolFilter(reg, cfg)
	for _, tl := range reg.All() {
		if tl.Name() == "bash" {
			t.Error("bash should be removed by disabled list")
		}
	}
}

func TestApplyToolFilter_GlobDisabled(t *testing.T) {
	// After EP-0038 tools get wire names, this tests glob removal.
	// For now just verify zero-match glob is a silent no-op.
	reg := BuildDefaultRegistry(nil)
	before := len(reg.All())
	cfg := &config.Config{}
	cfg.Tools.Disabled = []string{"nonexistent.*"} // glob matching nothing
	ApplyToolFilter(reg, cfg)
	if got := len(reg.All()); got != before {
		t.Errorf("zero-match glob disable should be no-op; was %d, got %d", before, got)
	}
}
