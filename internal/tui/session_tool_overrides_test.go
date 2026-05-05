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
