package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
)

// TestChoiceDrawer_InputSeedsFromDefault: when the request carries
// an option with Input.Default, the drawer's per-option input slice
// pre-populates with that default so the operator's edit starts from
// the suggested value, not empty. F10.
func TestChoiceDrawer_InputSeedsFromDefault(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	resp := make(chan pluginRuntime.ChoiceResponse, 1)
	_, _ = m.Update(pluginChoiceRequestMsg{
		req: pluginRuntime.ChoiceRequest{
			Prompt: "ip",
			Options: []pluginRuntime.ChoiceOption{
				{ID: "go", Label: "Continue", Input: &pluginRuntime.ChoiceInput{Default: "10.10.14.1"}},
			},
		},
		response: resp,
	})
	if len(m.choiceInputs) != 1 {
		t.Fatalf("choiceInputs len = %d, want 1", len(m.choiceInputs))
	}
	if m.choiceInputs[0] != "10.10.14.1" {
		t.Errorf("choiceInputs[0] = %q, want %q", m.choiceInputs[0], "10.10.14.1")
	}
}

// TestChoiceDrawer_InputTypingCommitsValue: typing into an
// input-bearing row appends to its buffer, Backspace removes the last
// rune, Enter commits the row's id and the typed input value to the
// plugin response. F10.
func TestChoiceDrawer_InputTypingCommitsValue(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	resp := make(chan pluginRuntime.ChoiceResponse, 1)
	_, _ = m.Update(pluginChoiceRequestMsg{
		req: pluginRuntime.ChoiceRequest{
			Prompt: "rhost",
			Options: []pluginRuntime.ChoiceOption{
				{ID: "go", Label: "Continue", Input: &pluginRuntime.ChoiceInput{Default: "10.10.14.1"}},
			},
		},
		response: resp,
	})
	// Backspace twice ("10.10.14.") then "5".
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case got := <-resp:
		if got.Cancelled {
			t.Errorf("cancelled = true, want false")
		}
		if len(got.Selected) != 1 || got.Selected[0] != "go" {
			t.Errorf("selected = %v, want [go]", got.Selected)
		}
		if got.InputValue != "10.10.14.5" {
			t.Errorf("input_value = %q, want %q", got.InputValue, "10.10.14.5")
		}
	default:
		t.Fatal("response not delivered")
	}
}

// TestChoiceDrawer_InputValidatorBlocksInvalid: when the chosen
// option's validator rejects the typed input, the drawer stays open
// with an inline error and no response is sent. The operator can
// keep editing without losing the typed value. F10.
func TestChoiceDrawer_InputValidatorBlocksInvalid(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	resp := make(chan pluginRuntime.ChoiceResponse, 1)
	_, _ = m.Update(pluginChoiceRequestMsg{
		req: pluginRuntime.ChoiceRequest{
			Prompt: "turns",
			Options: []pluginRuntime.ChoiceOption{
				{
					ID: "budget", Label: "Run with budget",
					Input: &pluginRuntime.ChoiceInput{
						Default:   "abc", // not an int
						Validator: &pluginRuntime.ChoiceValidator{Kind: "int"},
					},
				},
			},
		},
		response: resp,
	})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	// No response delivered yet — drawer should still be open.
	select {
	case got := <-resp:
		t.Fatalf("response should not have been delivered: %+v", got)
	default:
	}
	if m.choice == nil {
		t.Error("drawer should remain open after validation failure")
	}
	if m.choiceValidationErr == "" {
		t.Error("expected an inline validation error")
	}
	if !strings.Contains(m.choiceValidationErr, "integer") {
		t.Errorf("err = %q, want it to mention 'integer'", m.choiceValidationErr)
	}

	// Now correct the input and Enter — should commit cleanly.
	for range "abc" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case got := <-resp:
		if got.Cancelled {
			t.Errorf("cancelled = true, want false")
		}
		if got.InputValue != "5" {
			t.Errorf("input_value = %q, want %q", got.InputValue, "5")
		}
	default:
		t.Fatal("response not delivered after fix")
	}
}

// TestChoiceDrawer_BareInputShortcut: a single option carrying only
// an Input field (no Label) opens as a bare input prompt. The drawer
// state still goes through the standard choiceRequest path so cancel
// and resolve semantics are unchanged. F10.
func TestChoiceDrawer_BareInputShortcut(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	resp := make(chan pluginRuntime.ChoiceResponse, 1)
	_, _ = m.Update(pluginChoiceRequestMsg{
		req: pluginRuntime.ChoiceRequest{
			Prompt: "Path?",
			Options: []pluginRuntime.ChoiceOption{
				{ID: "p", Input: &pluginRuntime.ChoiceInput{Default: "out.txt"}},
			},
		},
		response: resp,
	})
	if m.choice == nil {
		t.Fatal("drawer should be open")
	}
	// Type a suffix, commit.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'!'}})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case got := <-resp:
		if got.InputValue != "out.txt!" {
			t.Errorf("input_value = %q, want %q", got.InputValue, "out.txt!")
		}
	default:
		t.Fatal("bare-input response not delivered")
	}
}

// TestChoiceDrawer_NavigateClearsValidationError: navigating away
// from a row with a validation error clears the error so the operator
// doesn't see stale messaging on a different row. F10.
func TestChoiceDrawer_NavigateClearsValidationError(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	resp := make(chan pluginRuntime.ChoiceResponse, 1)
	_, _ = m.Update(pluginChoiceRequestMsg{
		req: pluginRuntime.ChoiceRequest{
			Prompt: "pick",
			Options: []pluginRuntime.ChoiceOption{
				{
					ID: "x", Label: "with input",
					Input: &pluginRuntime.ChoiceInput{
						Default:   "abc",
						Validator: &pluginRuntime.ChoiceValidator{Kind: "int"},
					},
				},
				{ID: "y", Label: "plain"},
			},
		},
		response: resp,
	})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.choiceValidationErr == "" {
		t.Fatal("expected validation error after Enter on bad input")
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.choiceValidationErr != "" {
		t.Errorf("navigation should clear validation error; still: %q", m.choiceValidationErr)
	}
}

// TestRequestPluginChoice_RejectsMultiWithInput: per F10 the multi
// + input combo is unsupported. The bridge entry point rejects the
// combo with a structured error before opening the drawer; that error
// surfaces to the plugin as the choice request's failure, matching
// the host_ui.go fail() path.
func TestRequestPluginChoice_RejectsMultiWithInput(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	// program is non-nil for the picker test model, so the early
	// "choice UI unavailable" branch is not triggered.
	req := pluginRuntime.ChoiceRequest{
		Prompt: "p",
		Multi:  true,
		Options: []pluginRuntime.ChoiceOption{
			{ID: "a", Label: "A", Input: &pluginRuntime.ChoiceInput{Default: ""}},
			{ID: "b", Label: "B"},
		},
	}
	_, err := m.requestPluginChoice(t.Context(), req)
	if err == nil {
		t.Fatal("expected error rejecting multi+input")
	}
	if !strings.Contains(err.Error(), "multi-select") {
		t.Errorf("err = %q, want it to mention multi-select", err.Error())
	}
}
