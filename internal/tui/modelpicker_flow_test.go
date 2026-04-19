package tui

import (
	"net"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

func dialQuick(addr string) (net.Conn, error) {
	return net.DialTimeout("tcp", addr, 50*time.Millisecond)
}

func newPickerTestModel(t *testing.T, providerName string) *Model {
	t.Helper()
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	reg := keys.NewRegistry()
	m := NewModel("/tmp", "starter-model", providerName,
		func() (agent.Provider, error) { return nil, nil },
		rnd, reg)
	m.width, m.height = 120, 40
	return m
}

// TestOpenModelPicker_Anthropic: `/model` with no args opens the picker
// pre-selected on the current model (starter isn't in the catalog, so
// cursor falls back to 0).
func TestOpenModelPicker_Anthropic(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	m.openModelPicker()
	if !m.modelPicker.Visible {
		t.Fatal("picker should be visible after openModelPicker()")
	}
	sel := m.modelPicker.Selected()
	if sel == nil {
		t.Fatal("picker has no selection")
	}
	// First catalog entry for anthropic.
	if sel.ID != "claude-opus-4-7" {
		t.Errorf("cursor at %q, expected catalog[0]=claude-opus-4-7", sel.ID)
	}
}

// TestOpenModelPicker_UnknownProviderAdvisory: no catalog AND no
// running local runners → system block, picker stays hidden.
//
// This test is skipped when the host has any bundled local runner up,
// because localdetect will populate the picker with the runner's
// live model list and the advisory path doesn't trigger. A CI runner
// shouldn't have those up, so the check exercises where it matters.
func TestOpenModelPicker_UnknownProviderAdvisory(t *testing.T) {
	// Fail-open: if localhost has a runner up, skip this test.
	if hasAnyLocalRunner(t) {
		t.Skip("host has a local runner running; advisory-path test assumes clean env")
	}
	m := newPickerTestModel(t, "some-custom-preset")
	m.openModelPicker()
	if m.modelPicker.Visible {
		t.Error("unknown provider should NOT open the picker")
	}
	if len(m.blocks) == 0 {
		t.Fatal("expected advisory block")
	}
	last := m.blocks[len(m.blocks)-1]
	if last.kind != "system" || !contains(last.body, "no known models") {
		t.Errorf("expected 'no known models' advisory, got %+v", last)
	}
}

// hasAnyLocalRunner is a cheap sniff — dial each of the default local
// endpoints and return true if any accepts a TCP connection. A full
// localdetect probe would add 1+ seconds per case; we just want to
// know "is the environment clean" before running advisory-path tests.
func hasAnyLocalRunner(t *testing.T) bool {
	t.Helper()
	for _, port := range []string{"11434", "8080", "8000", "1234"} {
		conn, err := dialQuick("127.0.0.1:" + port)
		if err == nil {
			_ = conn.Close()
			return true
		}
	}
	return false
}

// TestModelPickerSubmitAppliesSelection: drive the Update flow via a
// submit keypress and confirm m.model changed.
func TestModelPickerSubmitAppliesSelection(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	m.openModelPicker()
	// Cursor starts on claude-opus-4-7. Down + Submit should land on
	// claude-opus-4-6.
	m.Update(tea.KeyMsg{Type: tea.KeyDown})

	// Submit.
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if m.modelPicker.Visible {
		t.Error("picker should have closed after submit")
	}
	if m.model != "claude-opus-4-6" {
		t.Errorf("m.model = %q, want claude-opus-4-6", m.model)
	}
	// Last block should announce the swap.
	last := m.blocks[len(m.blocks)-1]
	if last.kind != "system" || !contains(last.body, "starter-model → claude-opus-4-6") {
		t.Errorf("announce missing: %+v", last)
	}
}

// TestModelPickerEscapeDismisses: Esc closes without mutating m.model.
func TestModelPickerEscapeDismisses(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	m.openModelPicker()
	m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	if m.modelPicker.Visible {
		t.Error("Esc should have closed the picker")
	}
	if m.model != "starter-model" {
		t.Errorf("Esc should not change m.model, got %q", m.model)
	}
}

// TestHandleSlashModelWithArgStillWorks: the args form bypasses the
// picker and applies inline.
func TestHandleSlashModelWithArgStillWorks(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	_ = m.handleSlash("/model claude-sonnet-4-5")
	if m.modelPicker.Visible {
		t.Error("picker should NOT open when /model has args")
	}
	if m.model != "claude-sonnet-4-5" {
		t.Errorf("m.model = %q, want claude-sonnet-4-5", m.model)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
