package providerpicker

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// modalBoxWidth returns the display-width of the centred modal BOX, ignoring
// the blank padding lipgloss.Place adds to fill the canvas. View() always
// returns a canvas exactly screenWidth wide (Place pads with spaces), so the
// load-bearing measurement is the bordered box, not the canvas. Each rendered
// line has leading/trailing Place padding stripped, then the widest remaining
// (ANSI-aware) line width is the box width.
func modalBoxWidth(s string) int {
	max := 0
	for _, line := range strings.Split(s, "\n") {
		// Trim Place's plain-space padding from both ends so only the box
		// (border + its background-filled interior) is measured. ANSI styling
		// on the box means its own cells aren't bare spaces.
		trimmed := strings.Trim(line, " ")
		if trimmed == "" {
			continue
		}
		if w := lipgloss.Width(trimmed); w > max {
			max = w
		}
	}
	return max
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	default:
		return tea.KeyPressMsg{Text: s}
	}
}

func typeText(m *Model, s string) {
	for _, r := range s {
		m.Update(tea.KeyPressMsg{Text: string(r)})
	}
}

func sampleItems() []Item {
	return []Item{
		{Provider: "anthropic", Kind: "native", EnvVar: "ANTHROPIC_API_KEY", Configured: true, Source: "env", NeedsKey: true, AllowsBaseURL: false},
		{Provider: "minimax-anthropic", Kind: "anthropic-compat-cloud", EnvVar: "MINIMAX_API_KEY", Configured: false, NeedsKey: true, AllowsBaseURL: true},
		{Provider: "ollama", Kind: "oai-compat-local", EnvVar: "", Configured: true, NeedsKey: false, AllowsBaseURL: true},
	}
}

// TestListsProvidersWithRedactedStatus: every provider row renders its
// configured/unset marker + env-var NAME, and NO secret value appears.
func TestListsProvidersWithRedactedStatus(t *testing.T) {
	m := New()
	m.Open(sampleItems(), true, "")
	if !m.Visible {
		t.Fatal("picker should be visible after Open")
	}
	out := m.View(100, 30)

	for _, want := range []string{"anthropic", "ANTHROPIC_API_KEY", "set", "minimax-anthropic", "unset", "MINIMAX_API_KEY", "(no key needed)"} {
		if !strings.Contains(out, want) {
			t.Errorf("list view missing %q\n---\n%s", want, out)
		}
	}
}

// TestSaveEmitsRefCommandNoSecretInOutput: editing a provider and pressing
// Enter emits a Save command carrying the env var; the rendered form never
// echoes the typed secret.
func TestSaveEmitsRefCommandNoSecretInOutput(t *testing.T) {
	m := New()
	m.Open(sampleItems(), true, "anthropic")
	// Enter the form for the first (anthropic) row.
	if _, handled := m.Update(key("enter")); !handled {
		t.Fatal("enter should be handled")
	}
	if m.mode != modeForm {
		t.Fatalf("expected form mode, got %v", m.mode)
	}

	// Type a secret into the masked key field. Navigate to the key field
	// first (env -> key, since base_url is disabled for native).
	m.formField = fieldKey
	const secret = "sk-super-secret-123"
	typeText(m, secret)

	// The masked value must NOT leak into the rendered modal.
	out := m.View(100, 30)
	if strings.Contains(out, secret) {
		t.Fatalf("secret leaked into rendered form:\n%s", out)
	}
	if !strings.Contains(out, "•") {
		t.Errorf("masked key field should render dots; view:\n%s", out)
	}

	cmd, _ := m.Update(key("enter"))
	if cmd.Type != CommandSave {
		t.Fatalf("expected CommandSave, got %v", cmd.Type)
	}
	if cmd.Provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic", cmd.Provider)
	}
	if cmd.EnvVar != "ANTHROPIC_API_KEY" {
		t.Errorf("env var = %q, want ANTHROPIC_API_KEY", cmd.EnvVar)
	}
	if cmd.Secret != secret {
		t.Errorf("command should carry the secret for the keyring write; got %q", cmd.Secret)
	}
}

// TestRemovePathEmitsRemoveCommand: ctrl+d on a key-bearing provider then
// confirm emits a Remove command.
func TestRemovePathEmitsRemoveCommand(t *testing.T) {
	m := New()
	m.Open(sampleItems(), true, "anthropic")
	if _, handled := m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}); !handled {
		t.Fatal("ctrl+d should be handled")
	}
	if m.mode != modeRemove {
		t.Fatalf("expected remove mode, got %v", m.mode)
	}
	out := m.View(100, 30)
	if !strings.Contains(out, "Unset") {
		t.Errorf("remove confirmation should mention Unset:\n%s", out)
	}
	cmd, _ := m.Update(key("y"))
	if cmd.Type != CommandRemove {
		t.Fatalf("expected CommandRemove, got %v", cmd.Type)
	}
	if cmd.Provider != "anthropic" {
		t.Errorf("provider = %q, want anthropic", cmd.Provider)
	}
}

// TestRemoveSkippedForLocalRunner: ctrl+d on a no-key provider does nothing
// (there's no credential to unset).
func TestRemoveSkippedForLocalRunner(t *testing.T) {
	m := New()
	m.Open(sampleItems(), true, "ollama")
	// Open lands the cursor on ollama (the 3rd, no-key item).
	if sel := m.Selected(); sel == nil || sel.Provider != "ollama" {
		t.Fatalf("expected cursor on ollama, got %+v", m.Selected())
	}
	m.Update(tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl})
	if m.mode == modeRemove {
		t.Fatal("ctrl+d on a no-key provider should not open remove mode")
	}
}

// TestKeyFieldHiddenWithoutKeyring: when no keyring is available the form
// shows the export hint instead of a key-entry field, and the key field is
// not editable.
func TestKeyFieldHiddenWithoutKeyring(t *testing.T) {
	m := New()
	m.Open(sampleItems(), false, "anthropic")
	m.Update(key("enter")) // open form for anthropic
	if m.fieldEditable(fieldKey) {
		t.Error("key field must not be editable without a keyring")
	}
	out := m.View(100, 30)
	if !strings.Contains(out, "export ANTHROPIC_API_KEY") {
		t.Errorf("expected env-export hint without a keyring:\n%s", out)
	}
}

// TestLayeredEscFormToList: Esc in the form returns to the list, not all
// the way closed.
func TestLayeredEscFormToList(t *testing.T) {
	m := New()
	m.Open(sampleItems(), true, "anthropic")
	m.Update(key("enter")) // -> form
	if m.mode != modeForm {
		t.Fatalf("expected form mode, got %v", m.mode)
	}
	m.Update(key("esc")) // form -> list
	if m.mode != modeList {
		t.Fatalf("esc should drop form->list, got mode %v", m.mode)
	}
	if !m.Visible {
		t.Fatal("esc from form should not close the picker")
	}
	m.Update(key("esc")) // list -> close
	if m.Visible {
		t.Fatal("esc from list should close the picker")
	}
}

// TestBaseURLFieldGatedByKind: the base_url field is editable only for
// kinds that allow an override.
func TestBaseURLFieldGatedByKind(t *testing.T) {
	m := New()
	m.Open(sampleItems(), true, "anthropic")
	m.Update(key("enter")) // native anthropic — base_url disallowed
	if m.fieldEditable(fieldBaseURL) {
		t.Error("native provider must not allow a base_url override")
	}
	m.mode = modeList
	// minimax-anthropic allows base_url.
	m.selectProvider("minimax-anthropic")
	m.Update(key("enter"))
	if !m.fieldEditable(fieldBaseURL) {
		t.Error("anthropic-compat-cloud provider should allow a base_url override")
	}
}

// marginBudget is the cols the modal box must leave free at narrow terminals:
// a 2-col margin on each side (4 total) so the centred box never touches the
// edge — breathing room, and headroom against off-by-one edge clipping.
const marginBudget = 4

// TestModalLeavesMarginAtNarrowWidths: the centred modal must leave a small
// breathing-room margin at narrow terminals. At 60 cols the old code clamped
// modalW up to its 58 floor, which (border included) filled cols 2..59 with
// only a 1-col margin each side (Place centres the 58-wide box on the 60-col
// canvas) — risking edge clipping. The box width must stay <=
// screenWidth-marginBudget so a 2-col margin survives on each side.
func TestModalLeavesMarginAtNarrowWidths(t *testing.T) {
	for _, screenW := range []int{60, 58, 50, 44} {
		m := New()
		m.Open(sampleItems(), true, "")
		// Exercise every mode: the form/remove bodies can be wider than the
		// list, so the cap must hold for all of them.
		m.Update(key("enter")) // -> form for the first item
		for _, modeName := range []string{"list", "form", "remove"} {
			switch modeName {
			case "list":
				m.mode = modeList
			case "form":
				m.mode = modeForm
			case "remove":
				m.mode = modeRemove
			}
			out := m.View(screenW, 30)
			if got := modalBoxWidth(out); got > screenW-marginBudget {
				t.Errorf("mode=%s screenW=%d: modal box width %d leaves no breathing room (want <= %d)\n---\n%s",
					modeName, screenW, got, screenW-marginBudget, out)
			}
		}
	}
}

// TestModalStillUsableWidthAtRoomyTerminal: the margin cap must not shrink the
// modal at terminals wide enough for the design's half-screen width — at 120
// cols the modal should still be the clamped 58..98 half-screen size, not the
// narrow-terminal cap.
func TestModalStillUsableWidthAtRoomyTerminal(t *testing.T) {
	m := New()
	m.Open(sampleItems(), true, "")
	out := m.View(120, 40)
	// 120/2 = 60, clamped to [58,98] = 60; well below the 120-4 cap.
	if got := modalBoxWidth(out); got < 58 {
		t.Errorf("at 120 cols the modal should keep its half-screen width, got %d", got)
	}
	if got := modalBoxWidth(out); got > 120-2 {
		t.Errorf("modal width %d exceeds the margin budget at 120 cols", got)
	}
}

// TestMaskSecretNeverReturnsBytes: the masking helper returns only dots,
// length-capped, never the original characters.
func TestMaskSecretNeverReturnsBytes(t *testing.T) {
	got := maskSecret("abcDEF123")
	if strings.ContainsAny(got, "abcDEF123") {
		t.Fatalf("maskSecret leaked characters: %q", got)
	}
	if got != strings.Repeat("•", 9) {
		t.Fatalf("maskSecret = %q, want 9 dots", got)
	}
	if maskSecret("") != "" {
		t.Fatal("maskSecret(\"\") should be empty")
	}
	long := maskSecret(strings.Repeat("x", 100))
	if len([]rune(long)) != 24 {
		t.Fatalf("maskSecret should cap at 24 dots, got %d", len([]rune(long)))
	}
}
