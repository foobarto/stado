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

// Codex K P0 regression: pre-fix acpHost did NOT implement
// pkg/tool.WritePathGuard, so an `fs.write` call routed through the
// ACP server's host bypassed the .git-write guard that acpwrap's
// DefaultHost had. The fs.write tool's runtime asserts the interface
// and skips the check when absent — silently letting attacker-
// influenced ACP clients corrupt the worktree's git metadata. After
// fix acpHost.CheckWritePath enforces the same defense as acpwrap's
// implementation; this test pins the contract.
func TestACPHost_CheckWritePath_RefusesGitMetadata(t *testing.T) {
	h := &acpHost{server: nil, sessionID: "x", workdir: "/work"}
	cases := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"absolute .git/HEAD", "/work/.git/HEAD", true},
		{"relative .git/config", ".git/config", true},
		{"nested .git in subdir", "src/.git/objects/foo", true},
		{"path resolved through ..", "src/../.git/HEAD", true},
		{"plain source file ok", "main.go", false},
		{"absolute non-git ok", "/work/src/main.go", false},
		{"file named gitignore (no .git seg) ok", "src/.gitignore", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := h.CheckWritePath(c.path)
			if (err != nil) != c.wantErr {
				t.Errorf("CheckWritePath(%q) err = %v, wantErr = %v", c.path, err, c.wantErr)
			}
			if c.wantErr && err != nil && !strings.Contains(err.Error(), ".git") {
				t.Errorf("error should mention .git; got %q", err.Error())
			}
		})
	}
}
