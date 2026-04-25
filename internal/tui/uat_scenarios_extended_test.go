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
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/foobarto/stado/pkg/tool"
)

// ============================================================
// C. Model picker
// ============================================================

// C1: `/model` with no args opens the picker when the catalog has
// items. Uses a known provider name ("anthropic") so CatalogFor
// returns a populated list — without this, CI environments with no
// local runners detected land on the empty-catalog branch (system
// advisory instead of picker).
func TestUAT_SlashModelOpensPicker(t *testing.T) {
	m := scenarioModel(t)
	m.providerName = "anthropic"
	m.state = stateIdle
	if m.modelPicker.Visible {
		t.Fatal("picker should start hidden")
	}
	_ = m.handleSlash("/model")
	if !m.modelPicker.Visible {
		t.Error("/model with a populated catalog should open the picker")
	}
}

// C4: Esc from model picker closes without swapping.
func TestUAT_ModelPickerEscClosesWithoutSwap(t *testing.T) {
	m := scenarioModel(t)
	m.providerName = "anthropic"
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

// D3: the first submitted user message should render as its own full card in
// the conversation area, not collapse into the line directly above the input.
func TestUAT_FirstSubmittedMessageRendersAsSeparateCard(t *testing.T) {
	m := scenarioModel(t)
	m.sidebarOpen = false
	m.state = stateIdle

	for _, r := range "hello" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	out := m.View()
	plain := ansi.Strip(out)
	if got := strings.Count(plain, "hello"); got != 1 {
		t.Fatalf("rendered view should contain submitted text once, got %d\nfull output:\n%s", got, plain)
	}
	if got := strings.Count(plain, "│"); got < 2 {
		t.Fatalf("expected separate left-rail panels for the submitted card and input box, got %d rails\nfull output:\n%s", got, plain)
	}
}

// ============================================================
// E. Approval flow
// ============================================================

// E1: an explicit plugin approval prompt routes 'n' back to the
// waiting caller and clears the popup.
func TestUAT_ApprovalStateRoutesYN(t *testing.T) {
	m := scenarioModel(t)
	resp := make(chan bool, 1)
	_, _ = m.Update(pluginApprovalRequestMsg{
		title:    "Plugin approval",
		body:     "Allow the demo plugin to continue?",
		response: resp,
	})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if m.approval != nil {
		t.Error("n should clear approval request")
	}
	if cmd != nil {
		t.Fatalf("deny should not return a follow-up cmd, got %T", cmd)
	}
	select {
	case allow := <-resp:
		if allow {
			t.Fatal("plugin approval should have been denied")
		}
	default:
		t.Fatal("plugin approval response was not delivered")
	}
}

// E2: 'y' allows an explicit plugin approval prompt.
func TestUAT_ApprovalYApprovesAndAdvances(t *testing.T) {
	m := scenarioModel(t)
	resp := make(chan bool, 1)
	_, _ = m.Update(pluginApprovalRequestMsg{
		title:    "Plugin approval",
		body:     "Allow the demo plugin to continue?",
		response: resp,
	})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if m.approval != nil {
		t.Error("y should clear approval request")
	}
	if cmd != nil {
		t.Fatalf("approve should not return a follow-up cmd, got %T", cmd)
	}
	select {
	case allow := <-resp:
		if !allow {
			t.Fatal("plugin approval should have been allowed")
		}
	default:
		t.Fatal("plugin approval response was not delivered")
	}
}

// E2b: approval no longer blocks draft editing; normal typing should
// stay in the textarea until the user explicitly resolves the prompt.
func TestUAT_ApprovalKeepsInputEditable(t *testing.T) {
	m := scenarioModel(t)
	resp := make(chan bool, 1)
	_, _ = m.Update(pluginApprovalRequestMsg{
		title:    "Plugin approval",
		body:     "Allow the demo plugin to continue?",
		response: resp,
	})

	typeString(m, "draft")

	if m.input.Value() != "draft" {
		t.Fatalf("input = %q, want draft preserved while approval is pending", m.input.Value())
	}
	if m.approval == nil {
		t.Fatal("approval should still be pending after draft edits")
	}
	if m.state != stateApproval {
		t.Fatalf("state = %v, want stateApproval", m.state)
	}
}

// E2c: Up focuses the approval card, Left/Right switches the active
// choice, and Enter resolves the selected action without touching the
// in-progress draft underneath.
func TestUAT_ApprovalArrowNavigationConfirmsSelection(t *testing.T) {
	m := scenarioModel(t)
	resp := make(chan bool, 1)
	_, _ = m.Update(pluginApprovalRequestMsg{
		title:    "Plugin approval",
		body:     "Allow the demo plugin to continue?",
		response: resp,
	})
	typeString(m, "draft")

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if !m.approvalFocused {
		t.Fatal("Up should focus the approval card")
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	if m.approvalAllowSelected {
		t.Fatal("Right should move selection to deny")
	}

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("approval enter should not return a cmd, got %T", cmd)
	}
	select {
	case allow := <-resp:
		if allow {
			t.Fatal("Right+Enter should have denied the approval")
		}
	default:
		t.Fatal("plugin approval response was not delivered")
	}
	if m.input.Value() != "draft" {
		t.Fatalf("input = %q, want draft preserved after arrow-key approval flow", m.input.Value())
	}
}

// E2d: a provider-emitted tool call that was not offered for the turn
// must not enter the approval flow; it should be turned into an error
// result immediately.
func TestUAT_DisallowedToolCallSkipsApproval(t *testing.T) {
	m := scenarioModel(t)
	m.turnAllowed = map[string]struct{}{"read": {}}
	m.pendingCalls = []agent.ToolUseBlock{
		{ID: "call-4", Name: "bash", Input: []byte(`{"command":"ls"}`)},
	}
	m.blocks = append(m.blocks, block{kind: "tool", toolID: "call-4", toolName: "bash"})

	cmd := m.advanceToolQueue()
	if m.approval != nil {
		t.Fatal("disallowed tool call should not open approval UI")
	}
	if cmd == nil {
		t.Fatal("disallowed tool call should still return a toolsExecutedMsg cmd")
	}
	msg := cmd()
	tem, ok := msg.(toolsExecutedMsg)
	if !ok {
		t.Fatalf("cmd returned %T, want toolsExecutedMsg", msg)
	}
	if len(tem.results) != 1 {
		t.Fatalf("results = %d, want 1", len(tem.results))
	}
	if !tem.results[0].IsError {
		t.Fatal("disallowed tool call should surface as an error result")
	}
	if !strings.Contains(tem.results[0].Content, "not available for this turn") {
		t.Fatalf("unexpected error content: %q", tem.results[0].Content)
	}
	if got := m.blocks[len(m.blocks)-1].toolResult; !strings.Contains(got, "not available for this turn") {
		t.Fatalf("tool block result = %q, want unavailable tool error", got)
	}
}

// E3: the old /approvals command now explains that tool approvals were
// removed and plugins must request approval explicitly.
func TestUAT_ApprovalsSlashIsDeprecated(t *testing.T) {
	m := scenarioModel(t)
	m.handleSlash("/approvals always read")
	last := m.blocks[len(m.blocks)-1]
	if last.kind != "system" || !strings.Contains(last.body, "tool-call approvals were removed") {
		t.Fatalf("unexpected /approvals response: %q", last.body)
	}
}

// E5: approval renders as a dedicated card without duplicating the
// bordered input frame underneath it.
func TestUAT_ApprovalViewRendersSingleInputBox(t *testing.T) {
	m := scenarioModel(t)
	_, _ = m.Update(pluginApprovalRequestMsg{
		title:    "Plugin approval",
		body:     "Allow the demo plugin to continue?",
		response: make(chan bool, 1),
	})

	out := ansi.Strip(m.View())
	inline, err := m.renderer.Exec("input_status", map[string]any{
		"Mode":         m.mode.String(),
		"Model":        m.model,
		"ProviderName": m.providerDisplayName(),
		"Hint":         "",
	})
	if err != nil {
		t.Fatalf("render input_status: %v", err)
	}
	statusLine := strings.TrimSpace(ansi.Strip(inline))

	if !strings.Contains(out, "Plugin approval") {
		t.Fatalf("approval card missing from view: %q", out)
	}
	if got := strings.Count(out, statusLine); got != 1 {
		t.Fatalf("input status rendered %d times, want exactly one", got)
	}
}

// E4: queued user message renders as a block with a "queued" pill and
// clears the pill once the running stream drains and the follow-up
// gets dispatched. This is the visible counterpart to queuedPrompt
// — before we kept the follow-up in the status bar only, which made
// it easy to miss.
func TestUAT_QueuedPromptRendersBlockAndClearsOnDrain(t *testing.T) {
	m := scenarioModel(t)
	// Manually fabricate the state submit would produce during a
	// streaming turn: a queued block + the text in queuedPrompt.
	m.state = stateStreaming
	m.blocks = append(m.blocks, block{kind: "user", body: "hi"})
	m.blocks = append(m.blocks, block{kind: "user", body: "follow-up", queued: true})
	m.queuedPrompt = "follow-up"

	// End the fake turn — onTurnComplete should drain queuedPrompt
	// and clear the queued flag on the matching block.
	m.state = stateIdle
	_ = m.onTurnComplete()

	if m.blocks[1].queued {
		t.Error("queued flag should be cleared after drain")
	}
	if m.queuedPrompt != "" {
		t.Errorf("queuedPrompt = %q, want empty", m.queuedPrompt)
	}
}

// E5: Ctrl+C with a queued-but-not-yet-dispatched prompt wipes both
// the pending text AND the block we appended for visual feedback.
// Leaving the block behind would show a dangling "queued" pill with
// no matching message in history.
func TestUAT_QueuedPromptCtrlCDropsBlock(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateStreaming
	m.blocks = append(m.blocks, block{kind: "user", body: "queued-msg", queued: true})
	m.queuedPrompt = "queued-msg"

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})

	if m.queuedPrompt != "" {
		t.Error("Ctrl+C should clear queuedPrompt")
	}
	for _, blk := range m.blocks {
		if blk.queued {
			t.Errorf("queued block survived Ctrl+C: %+v", blk)
		}
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

// ============================================================
// H. Missing slash-command UAT
// ============================================================

// H1: /split toggles splitView and renders a system advisory.
func TestUAT_SplitTogglesSplitView(t *testing.T) {
	m := scenarioModel(t)
	if m.splitView {
		t.Fatal("splitView should start false")
	}
	cmd := m.handleSlash("/split")
	if cmd != nil {
		t.Fatal("/split should return nil cmd")
	}
	if !m.splitView {
		t.Error("/split should set splitView = true")
	}
	last := m.blocks[len(m.blocks)-1]
	if last.kind != "system" || !strings.Contains(last.body, "split view: on") {
		t.Errorf("expected split-on advisory, got: %q", last.body)
	}

	// Toggle off again.
	m.handleSlash("/split")
	if m.splitView {
		t.Error("second /split should toggle splitView = false")
	}
}

// H2: /todo adds a todo item when given a title.
func TestUAT_TodoAddsItem(t *testing.T) {
	m := scenarioModel(t)
	if len(m.todos) != 0 {
		t.Fatal("todos should start empty")
	}
	m.handleSlash("/todo review PR")
	if len(m.todos) != 1 {
		t.Fatalf("expected 1 todo, got %d", len(m.todos))
	}
	if m.todos[0].Title != "review PR" {
		t.Errorf("todo title = %q, want 'review PR'", m.todos[0].Title)
	}
	if m.todos[0].Status != "open" {
		t.Errorf("todo status = %q, want open", m.todos[0].Status)
	}
}

// H3: /provider without an initialised provider shows "not yet initialised".
func TestUAT_ProviderShowsUninitialised(t *testing.T) {
	m := scenarioModel(t)
	m.provider = nil
	m.providerName = "anthropic"
	m.handleSlash("/provider")
	last := m.blocks[len(m.blocks)-1]
	if last.kind != "system" || !strings.Contains(last.body, "not yet initialised") {
		t.Errorf("expected 'not yet initialised' advisory, got: %q", last.body)
	}
}

func TestUAT_ProviderNamedShowsSetup(t *testing.T) {
	m := scenarioModel(t)
	m.handleSlash("/provider lmstudio")
	last := m.blocks[len(m.blocks)-1]
	for _, want := range []string{"provider setup: lmstudio", "bundled endpoint", "lms load <model>"} {
		if last.kind != "system" || !strings.Contains(last.body, want) {
			t.Fatalf("/provider <name> setup missing %q: %+v", want, last)
		}
	}
}

// H4: /tools lists the tools visible to the current mode.
func TestUAT_ToolsListsVisibleForMode(t *testing.T) {
	m := scenarioModel(t)
	ex := &tools.Executor{Registry: tools.NewRegistry()}
	ex.Registry.Register(dummyTool{name: "read", desc: "read a file", class: tool.ClassNonMutating})
	ex.Registry.Register(dummyTool{name: "bash", desc: "run shell", class: tool.ClassExec})
	ex.Registry.Register(dummyTool{name: "write", desc: "write a file", class: tool.ClassMutating})
	m.executor = ex

	m.handleSlash("/tools")
	last := m.blocks[len(m.blocks)-1]
	if last.kind != "system" || !strings.Contains(last.body, "Visible tools (Do mode):") {
		t.Errorf("expected tools list, got: %q", last.body)
	}
	for _, want := range []string{"read", "bash", "write"} {
		if !strings.Contains(last.body, want) {
			t.Errorf("Do mode tools list should include %q, got: %q", want, last.body)
		}
	}

	m.mode = modePlan
	m.handleSlash("/tools")
	last = m.blocks[len(m.blocks)-1]
	if last.kind != "system" || !strings.Contains(last.body, "Visible tools (Plan mode):") {
		t.Errorf("expected plan-mode tools list, got: %q", last.body)
	}
	if !strings.Contains(last.body, "read") {
		t.Errorf("Plan mode tools list should include 'read', got: %q", last.body)
	}
	for _, hidden := range []string{"bash", "write"} {
		if strings.Contains(last.body, hidden) {
			t.Errorf("Plan mode tools list should hide %q, got: %q", hidden, last.body)
		}
	}

	m.mode = modeBTW
	m.handleSlash("/tools")
	last = m.blocks[len(m.blocks)-1]
	if last.kind != "system" || !strings.Contains(last.body, "Visible tools (BTW mode):") {
		t.Errorf("expected BTW-mode tools list, got: %q", last.body)
	}
	if !strings.Contains(last.body, "read") {
		t.Errorf("BTW mode tools list should include 'read', got: %q", last.body)
	}
	for _, hidden := range []string{"bash", "write"} {
		if strings.Contains(last.body, hidden) {
			t.Errorf("BTW mode tools list should hide %q, got: %q", hidden, last.body)
		}
	}
}

type dummyTool struct {
	name  string
	desc  string
	class tool.Class
}

func (d dummyTool) Name() string        { return d.name }
func (d dummyTool) Description() string { return d.desc }
func (d dummyTool) Schema() map[string]any {
	return map[string]any{"type": "object"}
}
func (d dummyTool) Class() tool.Class { return d.class }
func (d dummyTool) Run(_ context.Context, _ json.RawMessage, _ tool.Host) (tool.Result, error) {
	return tool.Result{}, nil
}

// H5: /tools with no executor shows the unavailable advisory.
func TestUAT_ToolsNoExecutorUnavailable(t *testing.T) {
	m := scenarioModel(t)
	m.executor = nil
	m.handleSlash("/tools")
	last := m.blocks[len(m.blocks)-1]
	if last.kind != "system" || !strings.Contains(last.body, "no tools registered") {
		t.Errorf("expected unavailable advisory, got: %q", last.body)
	}
}
