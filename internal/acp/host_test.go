package acp

import (
	"strings"
	"testing"

	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
)

// TestACPHost_RequestChoice_AcceptsInputFields: the F10 ACP follow-on
// drops the previous rejection — input-bearing options now flow
// through the bridge and reach the server-side choice routing. The
// test fixture has no real ACP server connection, so we don't
// expect a successful round-trip; we DO want any error to come from
// the server layer, not the (now removed) input-rejection guard.
func TestACPHost_RequestChoice_AcceptsInputFields(t *testing.T) {
	h := &acpHost{server: &Server{}, sessionID: "test"}
	req := pluginRuntime.ChoiceRequest{
		Prompt: "p",
		Options: []pluginRuntime.ChoiceOption{
			{ID: "a", Label: "A", Input: &pluginRuntime.ChoiceInput{Default: ""}},
		},
	}
	_, err := h.RequestChoice(t.Context(), req)
	if err == nil {
		// Bridge let it through; the test fixture has no live conn.
		return
	}
	if strings.Contains(err.Error(), "does not yet support per-option input") {
		t.Errorf("F10 ACP follow-on should accept input fields; got the legacy rejection: %v", err)
	}
	if strings.Contains(err.Error(), "F10 TUI-only slice") {
		t.Errorf("legacy rejection text present after follow-on landed: %v", err)
	}
}

// TestACPHost_RequestChoice_AcceptsPlainOptions: plain options
// (no Input) pass through unchanged — F10 follow-on doesn't
// regress the existing behaviour.
func TestACPHost_RequestChoice_AcceptsPlainOptions(t *testing.T) {
	h := &acpHost{server: &Server{}, sessionID: "test"}
	req := pluginRuntime.ChoiceRequest{
		Prompt: "p",
		Options: []pluginRuntime.ChoiceOption{
			{ID: "a", Label: "A"},
			{ID: "b", Label: "B"},
		},
	}
	_, err := h.RequestChoice(t.Context(), req)
	if err == nil {
		return
	}
	if strings.Contains(err.Error(), "input fields") {
		t.Errorf("input-fields rejection should not trip on plain options: %v", err)
	}
}

// TestACPHost_RequestChoice_RejectsMultiWithInput: multi-select
// combined with per-option input fields stays unsupported even
// after the F10 ACP follow-on — same reasoning as the TUI bridge
// (the UX of typing into N rows is unsolved). Plugins must pick
// one or the other.
func TestACPHost_RequestChoice_RejectsMultiWithInput(t *testing.T) {
	h := &acpHost{server: &Server{}, sessionID: "test"}
	req := pluginRuntime.ChoiceRequest{
		Prompt: "p",
		Multi:  true,
		Options: []pluginRuntime.ChoiceOption{
			{ID: "a", Label: "A", Input: &pluginRuntime.ChoiceInput{Default: ""}},
			{ID: "b", Label: "B"},
		},
	}
	_, err := h.RequestChoice(t.Context(), req)
	if err == nil {
		t.Fatal("expected rejection for multi+input combo")
	}
	if !strings.Contains(err.Error(), "multi-select") {
		t.Errorf("err = %q, want 'multi-select' refusal", err.Error())
	}
}
