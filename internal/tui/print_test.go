package tui

import (
	"strings"
	"testing"

	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
)

// TestPluginPrint_AppendsSystemBlock: a stado_ui_print emit lands
// as a system-style block in the conversation flow. Severity-tagged
// emits surface the prefix so warn / error stand out without the
// renderer needing per-emit metadata. Fire-and-forget — no response
// channel, no state changes beyond the appended block. F9a.
func TestPluginPrint_AppendsSystemBlock(t *testing.T) {
	cases := []struct {
		name, severity, text, want string
	}{
		{
			name: "info default has no prefix",
			text: "scan started",
			want: "scan started",
		},
		{
			name:     "warn prefixed",
			severity: "warn",
			text:     "rate limit close",
			want:     "[warn] rate limit close",
		},
		{
			name:     "error prefixed",
			severity: "error",
			text:     "connection refused",
			want:     "[error] connection refused",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newPickerTestModel(t, "anthropic")
			before := len(m.blocks)
			_, _ = m.Update(pluginPrintMsg{
				text: tc.text,
				opts: pluginRuntime.PrintOpts{Severity: tc.severity, EOL: true},
			})
			if len(m.blocks) != before+1 {
				t.Fatalf("expected one new block; before=%d after=%d", before, len(m.blocks))
			}
			added := m.blocks[len(m.blocks)-1]
			if added.kind != "system" {
				t.Errorf("kind = %q, want %q", added.kind, "system")
			}
			if !strings.Contains(added.body, tc.want) {
				t.Errorf("body = %q, want contains %q", added.body, tc.want)
			}
		})
	}
}

// TestPluginPrint_NilProgramDropsOnFloor: the print bridge must
// not block or error when the model has no live tea.Program — the
// fire-and-forget contract requires it to swallow the call so a
// plugin doesn't deadlock waiting on a non-existent Program loop.
// F9a.
func TestPluginPrint_NilProgramDropsOnFloor(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	m.program = nil // pretend no live program

	bridge := tuiPrintBridge{model: m}
	if err := bridge.Print(t.Context(), "x", pluginRuntime.PrintOpts{}); err != nil {
		t.Errorf("nil-program print should drop silently, got: %v", err)
	}
}
