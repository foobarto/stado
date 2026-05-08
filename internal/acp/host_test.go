package acp

import (
	"strings"
	"testing"

	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
)

// TestACPHost_RequestChoice_RejectsInputFields: F10 stage 3 guard —
// the ACP bridge rejects any choice request whose options carry
// per-option Input fields, since the kind=choice wire format does
// not yet plumb them through. Plugins targeting both TUI and ACP
// detect this structured error and fall back to plain choice on
// the ACP path. Removing the guard ships with the F10 ACP follow-on.
func TestACPHost_RequestChoice_RejectsInputFields(t *testing.T) {
	h := &acpHost{server: &Server{}, sessionID: "test"}
	req := pluginRuntime.ChoiceRequest{
		Prompt: "p",
		Options: []pluginRuntime.ChoiceOption{
			{ID: "a", Label: "A", Input: &pluginRuntime.ChoiceInput{Default: ""}},
		},
	}
	_, err := h.RequestChoice(t.Context(), req)
	if err == nil {
		t.Fatal("expected rejection for input-bearing option")
	}
	if !strings.Contains(err.Error(), "input fields") {
		t.Errorf("err = %q, want it to mention 'input fields'", err.Error())
	}
}

// TestACPHost_RequestChoice_AcceptsPlainOptions: a request without
// any Input fields passes the F10 guard and reaches the server's
// choice routing. The actual server-side path errors here because
// the test fixture has no real session, but the failure should be
// the post-guard path, not the F10 rejection.
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
		// No real session is configured on the fixture, so we don't
		// expect success — but we DO want the failure to come from
		// the server's routing layer, not the F10 guard.
		return
	}
	if strings.Contains(err.Error(), "input fields") {
		t.Errorf("F10 guard tripped on plain options: %v", err)
	}
}
