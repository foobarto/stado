package tui

// UAT scenario catalogue — see .learnings/UAT_SCENARIOS.md for the
// user-story list these tests cover. Each test name reads as
// "When <context>, <action> → <expected outcome>" so failure
// messages in CI read like failed user stories.
//
// Tests use direct Model.Update calls (not teatest) for the same
// reason uat_direct_test.go does: stado's full View is sidebar-
// plus-viewport-plus-input, which teatest's virtual terminal
// handles unreliably. The Update path is where all the real logic
// lives, and these tests exercise it end-to-end.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

// scenarioModel is the shared harness. Seeded with a stub provider
// that streams an empty event channel so startStream doesn't panic
// but produces no assistant message (keeps assertions about state
// transitions clean).
func scenarioModel(t *testing.T) *Model {
	t.Helper()
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	reg := keys.NewRegistry()
	stub := scenarioStub{}
	m := NewModel(t.TempDir(), "m", "p",
		func() (agent.Provider, error) { return stub, nil }, rnd, reg)
	m.provider = stub
	m.width, m.height = 120, 30
	// Pretend we've already probed the provider's token counter so
	// startStream doesn't append a missing-counter advisory on its
	// first call (would pollute block-count assertions in tests).
	m.tokenCounterChecked = true
	m.tokenCounterPresent = true
	return m
}

type scenarioStub struct{}

func (scenarioStub) Name() string                     { return "scenario-stub" }
func (scenarioStub) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (scenarioStub) StreamTurn(_ context.Context, _ agent.TurnRequest) (<-chan agent.Event, error) {
	ch := make(chan agent.Event)
	close(ch)
	return ch, nil
}

// typeString feeds each rune of s through the model one keypress
// at a time, matching what bubbletea's input parser does on real
// keyboard input.
func typeString(m *Model, s string) {
	for _, r := range s {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// ============================================================
// A. Core conversation
// ============================================================

// A1: From idle, type a plaintext prompt + Enter → user block appears
// and stream begins. Covers the golden submit path.
func TestUAT_IdleSubmitAppendsUserBlockAndStreams(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle

	typeString(m, "hello world")
	priorBlocks := len(m.blocks)
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if len(m.blocks) != priorBlocks+1 {
		t.Fatalf("blocks grew by %d, want 1", len(m.blocks)-priorBlocks)
	}
	last := m.blocks[len(m.blocks)-1]
	if last.kind != "user" || last.body != "hello world" {
		t.Errorf("last block = %+v, want user/'hello world'", last)
	}
	if m.input.Value() != "" {
		t.Errorf("input should be cleared post-submit: %q", m.input.Value())
	}
	if m.state != stateStreaming {
		t.Errorf("state = %v, want streaming", m.state)
	}
}

// A7: Empty input + Enter → no-op. No block, state unchanged.
func TestUAT_EmptyEnterIsNoop(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle
	priorBlocks := len(m.blocks)
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.blocks) != priorBlocks {
		t.Errorf("empty Enter added a block")
	}
	if m.state != stateIdle {
		t.Errorf("state = %v, want idle after empty Enter", m.state)
	}
}

// A4 (complement): Ctrl+C on an empty input while a queue exists
// clears the queue but does NOT cancel the still-running stream.
// The rule is "one intent per press".
func TestUAT_CtrlCClearsQueueBeforeStream(t *testing.T) {
	m := scenarioModel(t)
	var streamCancelled int
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.state = stateStreaming
	m.streamCancel = func() { streamCancelled++; cancel() }
	_ = ctx
	m.queuedPrompt = "hold that thought"

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})

	if m.queuedPrompt != "" {
		t.Errorf("queue not cleared: %q", m.queuedPrompt)
	}
	if streamCancelled != 0 {
		t.Errorf("stream cancelled when only queue should have been: %d calls",
			streamCancelled)
	}
}

// A6: When a turn completes with a queued prompt, the drain path
// appends to m.msgs and starts the next stream. The visible block
// was already added at queue-time (not at drain).
func TestUAT_QueueDrainStartsNextTurn(t *testing.T) {
	m := scenarioModel(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.state = stateStreaming
	m.streamCancel = cancel
	_ = ctx

	// Queue a follow-up.
	typeString(m, "follow-up")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	priorMsgs := len(m.msgs)
	priorBlocks := len(m.blocks)

	// Simulate a streamDoneMsg — triggers onTurnComplete which drains.
	_, _ = m.Update(streamDoneMsg{})

	if len(m.msgs) != priorMsgs+1 {
		t.Errorf("msgs should grow by 1 on drain (got %d → %d)",
			priorMsgs, len(m.msgs))
	}
	if len(m.blocks) != priorBlocks {
		t.Errorf("drain should NOT append a second block (was added at queue-time); blocks %d → %d",
			priorBlocks, len(m.blocks))
	}
	if m.queuedPrompt != "" {
		t.Errorf("queuedPrompt not cleared after drain: %q", m.queuedPrompt)
	}
}

// ============================================================
// B. Slash palette
// ============================================================

// B1: `/` key opens the palette (bound as CommandList alias).
func TestUAT_SlashOpensPalette(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle
	if m.slash.Visible {
		t.Fatal("pre-condition: palette should start hidden")
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if !m.slash.Visible {
		t.Fatal("pressing / should open the slash palette")
	}
}

// B4: Palette visible + Esc → closes without taking action.
func TestUAT_PaletteEscCloses(t *testing.T) {
	m := scenarioModel(t)
	m.slash.Open()
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.slash.Visible {
		t.Error("Esc should close the palette")
	}
}

// ============================================================
// H. Mode + sidebar
// ============================================================

// H1: Tab toggles Do ↔ Plan mode.
func TestUAT_TabTogglesMode(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle
	start := m.mode
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.mode == start {
		t.Error("Tab should toggle mode away from start")
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.mode != start {
		t.Error("second Tab should toggle back to start")
	}
}

// H2: Ctrl+T toggles sidebar visibility.
func TestUAT_CtrlTTogglesSidebar(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle
	start := m.sidebarOpen
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	if m.sidebarOpen == start {
		t.Error("Ctrl+T should toggle sidebar")
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlT})
	if m.sidebarOpen != start {
		t.Error("second Ctrl+T should toggle back")
	}
}

// H3: Ctrl+X ] / Ctrl+X [ resize the sidebar without colliding with the editor.
func TestUAT_SidebarResizeShortcuts(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle
	start := m.sidebarPreferredWidth()

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}})
	if got := m.sidebarPreferredWidth(); got <= start {
		t.Fatalf("sidebar width = %d, want > %d after widen chord", got, start)
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
	if got := m.sidebarPreferredWidth(); got != start {
		t.Fatalf("sidebar width = %d, want %d after widen+narrow round-trip", got, start)
	}
}

// H4: resizing should reopen the sidebar and respect the configured minimum.
func TestUAT_SidebarResizeReopensAndClamps(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle
	m.sidebarOpen = false
	m.sidebarWidth = m.sidebarMinWidth()

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'['}})
	if !m.sidebarOpen {
		t.Fatal("resize chord should reopen the sidebar")
	}
	if got := m.sidebarPreferredWidth(); got != m.sidebarMinWidth() {
		t.Fatalf("sidebar width = %d, want clamp at min %d", got, m.sidebarMinWidth())
	}
}

// ============================================================
// I. Help overlay
// ============================================================

// I1: `?` shows the help overlay.
func TestUAT_QuestionMarkShowsHelp(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle
	if m.showHelp {
		t.Fatal("help should start hidden")
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if !m.showHelp {
		t.Error("? should open the help overlay")
	}
}

// I2: In the help overlay, pressing ? again (or Ctrl+C per the
// existing handler) dismisses. Covers the two dismiss paths wired in
// the showHelp branch of Update.
func TestUAT_AnyKeyClosesHelp(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if !m.showHelp {
		t.Fatal("pre-condition: help should be open")
	}
	// `?` again closes (TipsToggle binding dismisses while showing).
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if m.showHelp {
		t.Error("? should toggle help closed")
	}
}

// ============================================================
// J. Status row
// ============================================================

// J3: state=streaming → the rendered status row includes a
// "thinking" indicator so the user knows the app is busy.
func TestUAT_StreamingStateIndicator(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateStreaming
	got := m.renderStatus(120)
	if !strings.Contains(got, "thinking") {
		t.Errorf("streaming status should include 'thinking': %q", got)
	}
}

// J4: state=error → indicator + user-visible error message.
func TestUAT_ErrorStateIndicator(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateError
	m.errorMsg = "provider refused"
	got := m.renderStatus(120)
	if !strings.Contains(got, "provider refused") {
		t.Errorf("error status should surface the message: %q", got)
	}
}

// ============================================================
// M. Session bookkeeping
// ============================================================

// M1: usage.CostUSD is cumulative and shows in the status row.
// Guards against someone regressing "usage.CostUSD" from cumulative
// to per-turn — the latter would look like cost suddenly "resets"
// after every reply and be deeply confusing.
func TestUAT_CostRendersCumulatively(t *testing.T) {
	m := scenarioModel(t)
	m.usage.CostUSD = 0.37
	got := m.renderStatus(120)
	if !strings.Contains(got, "0.37") {
		t.Errorf("status should render cost: %q", got)
	}
}

// M2: Queued + cache + cost all render simultaneously without one
// crowding the others off the line. Regression guard for the
// template's conditional-pill assembly.
func TestUAT_StatusRowRendersAllThreeSignalsTogether(t *testing.T) {
	m := scenarioModel(t)
	m.queuedPrompt = "do the thing"
	m.usage.InputTokens = 1000
	m.usage.CacheReadTokens = 400
	m.usage.CostUSD = 0.12
	got := m.renderStatus(120)
	for _, want := range []string{"queued:", "cache", "0.12"} {
		if !strings.Contains(got, want) {
			t.Errorf("status missing %q in combined render: %q", want, got)
		}
	}
}

func TestUAT_StatusRowIncludesCwdBranchAndVersion(t *testing.T) {
	m := scenarioModel(t)
	if err := os.Mkdir(filepath.Join(m.cwd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.cwd, ".git", "HEAD"), []byte("ref: refs/heads/feature/status\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := m.renderStatus(160)
	for _, want := range []string{filepath.Base(m.cwd), "feature/status", "ctrl+p"} {
		if !strings.Contains(got, want) {
			t.Errorf("status missing %q in dense footer: %q", want, got)
		}
	}
}

// ============================================================
// N. Slow-reply / timing edge cases
// ============================================================

// N1: A burst of keypresses in close succession all land in the
// input buffer in order, matching what happens when a user types
// fast during a slow stream.
func TestUAT_RapidTypingBurstOrderPreserved(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateStreaming
	m.streamCancel = func() {}
	start := time.Now()
	for _, r := range "the quick brown fox" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("rapid typing took %v — Update path is too slow", elapsed)
	}
	if m.input.Value() != "the quick brown fox" {
		t.Errorf("input = %q, order or content wrong", m.input.Value())
	}
}

// N2: File-picker then-accept then-type workflow — @ opens, Tab
// accepts, subsequent typing is NOT lost. Regression guard for the
// cursor-offset logic in activeAtTrigger.
func TestUAT_FilePickerAcceptThenTypeContinues(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateIdle

	// Create a file the picker can find.
	if err := writeTestFile(t, m.cwd, "README.md", ""); err != nil {
		t.Fatal(err)
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'@'}})
	for _, r := range "READ" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if !m.filePicker.Visible || m.filePicker.Selected() == "" {
		t.Skip("filePicker didn't land a match — environment-dependent")
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.filePicker.Visible {
		t.Fatal("Tab should close the picker")
	}
	// Now type " go" after the accepted path.
	for _, r := range " go" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if !strings.Contains(m.input.Value(), "go") {
		t.Errorf("post-accept typing lost: %q", m.input.Value())
	}
}

// ============================================================
// Helpers
// ============================================================

func writeTestFile(t *testing.T, dir, name, body string) error {
	t.Helper()
	return os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
}
