package runtime

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
)

// TestEmitProgress_CallsHostCallback: when Host.Progress is set, the
// helper invokes it with the plugin name + text.
func TestEmitProgress_CallsHostCallback(t *testing.T) {
	host := NewHost(plugins.Manifest{Name: "demo"}, "/tmp", nil)
	var gotPlugin, gotText string
	var calls int
	host.Progress = func(plugin, text string) {
		gotPlugin, gotText = plugin, text
		calls++
	}

	if got := emitProgress(host, "checking host 17/256"); got != 0 {
		t.Errorf("expected 0 from emitProgress; got %d", got)
	}
	if calls != 1 {
		t.Errorf("expected 1 callback call, got %d", calls)
	}
	if gotPlugin != "demo" {
		t.Errorf("plugin name: %q", gotPlugin)
	}
	if gotText != "checking host 17/256" {
		t.Errorf("text: %q", gotText)
	}
}

// TestEmitProgress_NilCallbackSilentDrop: with no Host.Progress, the
// helper returns 0 (drop silently) — the plugin doesn't fail because
// the operator surface isn't wired.
func TestEmitProgress_NilCallbackSilentDrop(t *testing.T) {
	host := NewHost(plugins.Manifest{Name: "demo"}, "/tmp", nil)
	host.Progress = nil
	if got := emitProgress(host, "noop"); got != 0 {
		t.Errorf("nil callback: expected 0 (silent drop), got %d", got)
	}
}

// TestEmitProgress_OverlongTextRefused: payloads above 4 KB return -1.
func TestEmitProgress_OverlongTextRefused(t *testing.T) {
	host := NewHost(plugins.Manifest{Name: "demo"}, "/tmp", nil)
	called := false
	host.Progress = func(_, _ string) { called = true }
	overlong := strings.Repeat("x", maxProgressTextBytes+1)
	if got := emitProgress(host, overlong); got != -1 {
		t.Errorf("overlong: expected -1, got %d", got)
	}
	if called {
		t.Error("callback should not fire for overlong text")
	}
}

// TestEmitProgress_EmptyTextNoOp: empty string returns 0 without
// invoking the callback.
func TestEmitProgress_EmptyTextNoOp(t *testing.T) {
	host := NewHost(plugins.Manifest{Name: "demo"}, "/tmp", nil)
	called := false
	host.Progress = func(_, _ string) { called = true }
	if got := emitProgress(host, ""); got != 0 {
		t.Errorf("empty: expected 0, got %d", got)
	}
	if called {
		t.Error("callback should not fire for empty text")
	}
}

// TestEmitProgress_PassesPluginName: callback receives the plugin's
// manifest name regardless of text content.
func TestEmitProgress_PassesPluginName(t *testing.T) {
	host := NewHost(plugins.Manifest{Name: "scanner-pro"}, "/tmp", nil)
	var got string
	host.Progress = func(plugin, _ string) { got = plugin }
	emitProgress(host, "x")
	if got != "scanner-pro" {
		t.Errorf("plugin name: %q", got)
	}
}
