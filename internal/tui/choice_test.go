package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
)

// TestChoiceDrawer_SingleSelect: incoming pluginChoiceRequestMsg
// opens the drawer; ↓ moves cursor; Enter resolves with the cursor's
// option id and cancelled=false.
func TestChoiceDrawer_SingleSelect(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	resp := make(chan pluginRuntime.ChoiceResponse, 1)
	_, _ = m.Update(pluginChoiceRequestMsg{
		req: pluginRuntime.ChoiceRequest{
			Prompt: "pick",
			Options: []pluginRuntime.ChoiceOption{
				{ID: "a", Label: "Alpha"},
				{ID: "b", Label: "Bravo"},
				{ID: "c", Label: "Charlie"},
			},
		},
		response: resp,
	})
	if m.choice == nil {
		t.Fatal("choice drawer should be open")
	}
	if m.state != stateChoice {
		t.Errorf("state = %v, want stateChoice", m.state)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case got := <-resp:
		if got.Cancelled {
			t.Errorf("cancelled = true, want false")
		}
		if len(got.Selected) != 1 || got.Selected[0] != "b" {
			t.Errorf("selected = %v, want [b]", got.Selected)
		}
	default:
		t.Fatal("response not delivered")
	}
	if m.choice != nil {
		t.Error("choice should be nil after resolve")
	}
}

// TestChoiceDrawer_MultiSelect_SpaceTogglesEnterConfirms: in multi
// mode Space toggles the cursor option, Enter emits the sorted
// toggled ids.
func TestChoiceDrawer_MultiSelect_SpaceTogglesEnterConfirms(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	resp := make(chan pluginRuntime.ChoiceResponse, 1)
	_, _ = m.Update(pluginChoiceRequestMsg{
		req: pluginRuntime.ChoiceRequest{
			Prompt: "pick many",
			Multi:  true,
			Options: []pluginRuntime.ChoiceOption{
				{ID: "a", Label: "A"},
				{ID: "b", Label: "B"},
				{ID: "c", Label: "C"},
			},
		},
		response: resp,
	})
	// Toggle cursor (a) on, move to b, toggle, confirm.
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case got := <-resp:
		if got.Cancelled {
			t.Errorf("cancelled = true, want false")
		}
		if len(got.Selected) != 2 || got.Selected[0] != "a" || got.Selected[1] != "b" {
			t.Errorf("selected = %v, want [a b]", got.Selected)
		}
	default:
		t.Fatal("response not delivered")
	}
}

// TestChoiceDrawer_EscCancels: Esc returns cancelled=true to plugin.
func TestChoiceDrawer_EscCancels(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	resp := make(chan pluginRuntime.ChoiceResponse, 1)
	_, _ = m.Update(pluginChoiceRequestMsg{
		req: pluginRuntime.ChoiceRequest{
			Prompt:  "pick",
			Options: []pluginRuntime.ChoiceOption{{ID: "a", Label: "A"}},
		},
		response: resp,
	})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	select {
	case got := <-resp:
		if !got.Cancelled {
			t.Errorf("cancelled = false, want true")
		}
		if len(got.Selected) != 0 {
			t.Errorf("selected = %v, want empty", got.Selected)
		}
	default:
		t.Fatal("response not delivered")
	}
	if m.choice != nil {
		t.Error("choice should be nil after Esc")
	}
}

// TestChoiceDrawer_DefaultIDPreToggles: in single mode, Default[0]
// sets the initial cursor; in multi mode, every Default id starts
// toggled on.
func TestChoiceDrawer_DefaultIDPreToggles(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	resp := make(chan pluginRuntime.ChoiceResponse, 1)
	_, _ = m.Update(pluginChoiceRequestMsg{
		req: pluginRuntime.ChoiceRequest{
			Prompt:  "pick",
			Default: []string{"c"},
			Options: []pluginRuntime.ChoiceOption{
				{ID: "a", Label: "A"},
				{ID: "b", Label: "B"},
				{ID: "c", Label: "C"},
			},
		},
		response: resp,
	})
	if m.choiceCursor != 2 {
		t.Errorf("single-mode default cursor = %d, want 2", m.choiceCursor)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	<-resp

	// Multi mode: a + c pre-toggled.
	resp2 := make(chan pluginRuntime.ChoiceResponse, 1)
	_, _ = m.Update(pluginChoiceRequestMsg{
		req: pluginRuntime.ChoiceRequest{
			Prompt:  "pick many",
			Multi:   true,
			Default: []string{"a", "c"},
			Options: []pluginRuntime.ChoiceOption{
				{ID: "a", Label: "A"},
				{ID: "b", Label: "B"},
				{ID: "c", Label: "C"},
			},
		},
		response: resp2,
	})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	select {
	case got := <-resp2:
		if len(got.Selected) != 2 || got.Selected[0] != "a" || got.Selected[1] != "c" {
			t.Errorf("multi-mode default selected = %v, want [a c]", got.Selected)
		}
	default:
		t.Fatal("response not delivered")
	}
}

// TestChoiceDrawer_ConcurrentRejected: a second request while one is
// open is rejected with cancelled=true and doesn't replace the
// active drawer.
func TestChoiceDrawer_ConcurrentRejected(t *testing.T) {
	m := newPickerTestModel(t, "anthropic")
	first := make(chan pluginRuntime.ChoiceResponse, 1)
	_, _ = m.Update(pluginChoiceRequestMsg{
		req: pluginRuntime.ChoiceRequest{
			Prompt:  "first",
			Options: []pluginRuntime.ChoiceOption{{ID: "a", Label: "A"}},
		},
		response: first,
	})
	second := make(chan pluginRuntime.ChoiceResponse, 1)
	_, _ = m.Update(pluginChoiceRequestMsg{
		req: pluginRuntime.ChoiceRequest{
			Prompt:  "second",
			Options: []pluginRuntime.ChoiceOption{{ID: "x", Label: "X"}},
		},
		response: second,
	})
	select {
	case got := <-second:
		if !got.Cancelled {
			t.Errorf("second request should be cancelled, got %+v", got)
		}
	default:
		t.Fatal("second request response not delivered")
	}
	if m.choice == nil || m.choice.prompt != "first" {
		t.Errorf("first drawer should still be active; got %+v", m.choice)
	}
}
