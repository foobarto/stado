package providerpicker

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

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
