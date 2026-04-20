package tui

// Extended UAT scenarios promoting the sibling-covered areas
// (catalogue C/D/E/F/G) into named user-story tests. Sister file to
// uat_scenarios_test.go. Splits keep compile times reasonable on
// incremental edits.
//
// Scope: model picker, file-picker deep flows, approval y/n,
// compaction y/n/e, context thresholds. Persistence (L) stays in
// conversation_persistence_test.go — it needs a real Session which
// is out of scope for direct-Update UAT.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/pkg/agent"
)

// ============================================================
// C. Model picker
// ============================================================

// C1: `/model` with no args opens the picker.
func TestUAT_SlashModelOpensPicker(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle
	if m.modelPicker.Visible {
		t.Fatal("picker should start hidden")
	}
	_ = m.handleSlash("/model")
	if !m.modelPicker.Visible {
		t.Error("/model with no args should open the picker")
	}
}

// C4: Esc from model picker closes without swapping.
func TestUAT_ModelPickerEscClosesWithoutSwap(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle
	origModel := m.model
	_ = m.handleSlash("/model")
	if !m.modelPicker.Visible {
		t.Skip("picker didn't open — environment-dependent")
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.modelPicker.Visible {
		t.Error("Esc should close the picker")
	}
	if m.model != origModel {
		t.Errorf("Esc mutated model: %q → %q", origModel, m.model)
	}
}

// ============================================================
// D. File picker — deep flows
// ============================================================

// D1-D2 round-trip (sanity scenario): empty @ lists files; typing
// narrows.
func TestUAT_FilePickerOpenAndNarrow(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle
	if err := os.WriteFile(filepath.Join(m.cwd, "main.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.cwd, "README.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'@'}})
	if !m.filePicker.Visible {
		t.Fatal("@ should open picker")
	}
	if len(m.filePicker.Matches) < 2 {
		t.Errorf("empty-@ should list all files; got %d", len(m.filePicker.Matches))
	}
	for _, r := range "main" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	sel := m.filePicker.Selected()
	if !strings.Contains(sel, "main") {
		t.Errorf("@main should surface main.go; got %q", sel)
	}
}

// D6: Esc closes picker; input buffer unchanged except for the @
// that triggered it.
func TestUAT_FilePickerEscLeavesBufferIntact(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle
	_ = os.WriteFile(filepath.Join(m.cwd, "a.go"), []byte("x"), 0o644)

	typeString(m, "@a")
	if !m.filePicker.Visible {
		t.Fatal("@a should open picker")
	}
	beforeVal := m.input.Value()
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.filePicker.Visible {
		t.Error("Esc should close picker")
	}
	if m.input.Value() != beforeVal {
		t.Errorf("Esc mutated input buffer: %q → %q", beforeVal, m.input.Value())
	}
}

// ============================================================
// E. Approval flow
// ============================================================

// E1: approval + 'n' → tool-result with IsError=true and "Denied"
// content flows via the toolsExecutedMsg that advanceApproval
// returns. The results aren't left on m.pendingResults because
// advanceApproval drains them into the returned cmd.
func TestUAT_ApprovalStateRoutesYN(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateApproval
	m.approval = &approvalRequest{
		toolName: "bash",
		toolID:   "call-1",
		args:     `{"cmd":"ls"}`,
	}
	m.pendingCalls = []agent.ToolUseBlock{
		{ID: "call-1", Name: "bash", Input: []byte(`{"cmd":"ls"}`)},
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})

	if m.approval != nil {
		t.Error("n should clear approval request")
	}
	if cmd == nil {
		t.Fatal("deny should return a cmd carrying toolsExecutedMsg")
	}
	msg := cmd()
	tem, ok := msg.(toolsExecutedMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want toolsExecutedMsg", msg)
	}
	if len(tem.results) != 1 {
		t.Fatalf("results = %d, want 1", len(tem.results))
	}
	res := tem.results[0]
	if !res.IsError {
		t.Error("denied tool should produce IsError=true result")
	}
	if !strings.Contains(res.Content, "Denied") {
		t.Errorf("deny content should mention 'Denied': %q", res.Content)
	}
}

// E2: 'y' approves — without a real executor, executeCall still
// runs and returns an "unavailable" result. The distinguishing
// signal vs deny is the lack of "Denied" in the content body.
func TestUAT_ApprovalYApprovesAndAdvances(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateApproval
	m.approval = &approvalRequest{toolName: "read", toolID: "call-2"}
	m.pendingCalls = []agent.ToolUseBlock{
		{ID: "call-2", Name: "read", Input: []byte(`{}`)},
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if m.approval != nil {
		t.Error("y should clear approval request")
	}
	if cmd == nil {
		t.Fatal("approve should return a cmd carrying toolsExecutedMsg")
	}
	tem, ok := cmd().(toolsExecutedMsg)
	if !ok {
		t.Fatalf("cmd returned wrong type: %T", cmd)
	}
	if len(tem.results) != 1 {
		t.Fatalf("results = %d, want 1", len(tem.results))
	}
	if strings.Contains(tem.results[0].Content, "Denied") {
		t.Errorf("approved call should NOT contain 'Denied': %q", tem.results[0].Content)
	}
}

// ============================================================
// F. Compaction flow
// ============================================================

// F2: pending compaction + 'y' → msgs replaced with the summary.
func TestUAT_CompactionYReplacesMessages(t *testing.T) {
	m := scenarioModel(t)
	// Seed an existing conversation.
	m.msgs = []agent.Message{
		agent.Text(agent.RoleUser, "old one"),
		agent.Text(agent.RoleAssistant, "old two"),
	}
	// Simulate a completed compaction turn.
	m.state = stateCompactionPending
	m.pendingCompactionSummary = "user asked about X; we established Y"

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	if m.state == stateCompactionPending {
		t.Error("y should resolve the pending state")
	}
	// After compaction-accept, msgs should contain the summary —
	// exact shape is compact.ReplaceMessages's call but presence is
	// what we check.
	joined := ""
	for _, msg := range m.msgs {
		for _, b := range msg.Content {
			if b.Text != nil {
				joined += b.Text.Text
			}
		}
	}
	if !strings.Contains(joined, "user asked about X") {
		t.Errorf("msgs should contain summary post-accept; joined=%q", joined)
	}
}

// F3: pending compaction + 'n' → msgs preserved, state back to idle.
func TestUAT_CompactionNDiscards(t *testing.T) {
	m := scenarioModel(t)
	origMsgs := []agent.Message{agent.Text(agent.RoleUser, "keep me")}
	m.msgs = origMsgs
	m.state = stateCompactionPending
	m.pendingCompactionSummary = "would replace"

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})

	if m.state == stateCompactionPending {
		t.Error("n should resolve the pending state")
	}
	if len(m.msgs) != 1 || m.msgs[0].Content[0].Text.Text != "keep me" {
		t.Errorf("n should preserve msgs; got %+v", m.msgs)
	}
}

// F4: pending + 'e' → enters stateCompactionEditing with the
// summary pre-filled in the input.
func TestUAT_CompactionESwitchesToEdit(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateCompactionPending
	m.pendingCompactionSummary = "draft summary"
	m.savedDraftBeforeEdit = ""

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})

	if m.state != stateCompactionEditing {
		t.Errorf("state = %v, want compaction-editing", m.state)
	}
	if m.input.Value() != "draft summary" {
		t.Errorf("input should be pre-filled with summary; got %q", m.input.Value())
	}
}

// ============================================================
// G. Context thresholds
// ============================================================

// G1: above hard threshold, Enter-submit is refused — no stream
// starts, no user block appears, but the recovery advisory is
// rendered as a system block.
func TestUAT_HardThresholdBlocksSubmit(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle
	// Force aboveHardThreshold to report true: usage.InputTokens /
	// MaxContextTokens > ctxHardThreshold. MaxContextTokens is read
	// from provider.Capabilities() — set it on the stub via a richer
	// fake.
	m.provider = thresholdStub{max: 1000}
	m.usage.InputTokens = 950 // 95%, above 90% hard
	m.ctxHardThreshold = 0.90

	typeString(m, "new turn please")
	priorBlocks := len(m.blocks)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if cmd != nil {
		t.Error("hard-threshold submit should return nil cmd (no stream)")
	}
	// System advisory block appended.
	if len(m.blocks) != priorBlocks+1 {
		t.Fatalf("advisory block not appended: %d → %d", priorBlocks, len(m.blocks))
	}
	advisory := m.blocks[len(m.blocks)-1]
	if advisory.kind != "system" {
		t.Errorf("advisory should be system kind, got %q", advisory.kind)
	}
	if !strings.Contains(advisory.body, "hard threshold") {
		t.Errorf("advisory should mention threshold: %q", advisory.body)
	}
	// User's draft preserved so they can fork/compact then resubmit.
	if m.input.Value() != "new turn please" {
		t.Errorf("input should NOT be cleared on block: %q", m.input.Value())
	}
}

// thresholdStub surfaces a MaxContextTokens so aboveHardThreshold
// can compute a real fraction. All other methods no-op.
type thresholdStub struct {
	max int
}

func (thresholdStub) Name() string { return "threshold-stub" }
func (s thresholdStub) Capabilities() agent.Capabilities {
	return agent.Capabilities{MaxContextTokens: s.max}
}
func (thresholdStub) StreamTurn(_ context.Context, _ agent.TurnRequest) (<-chan agent.Event, error) {
	ch := make(chan agent.Event)
	close(ch)
	return ch, nil
}

// G2 (below soft): a modest usage doesn't block.
func TestUAT_BelowSoftThresholdSubmitsNormally(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle
	m.provider = thresholdStub{max: 100000}
	m.usage.InputTokens = 1000 // 1% — well under soft
	m.ctxSoftThreshold = 0.70
	m.ctxHardThreshold = 0.90

	typeString(m, "small prompt")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if m.state != stateStreaming {
		t.Errorf("state = %v, want streaming (submit should go through)", m.state)
	}
}
