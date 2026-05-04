package tui

import (
	"context"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

// compactStubProvider is a minimal agent.Provider that replays a fixed
// summary over TextDelta → Done events. Used to exercise the /compact
// flow without hitting a real LLM.
type compactStubProvider struct {
	summary string
}

func (compactStubProvider) Name() string                  { return "compact-stub" }
func (compactStubProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }

func (p compactStubProvider) StreamTurn(ctx context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	ch := make(chan agent.Event, 8)
	go func() {
		defer close(ch)
		ch <- agent.Event{Kind: agent.EvTextDelta, Text: p.summary}
		ch <- agent.Event{Kind: agent.EvDone}
	}()
	return ch, nil
}

func newCompactTestModel(t *testing.T, p agent.Provider) *Model {
	t.Helper()
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	reg := keys.NewRegistry()
	m := NewModel("/tmp", "test-model", "compact-stub",
		func() (agent.Provider, error) { return p, nil }, rnd, reg)
	// Layout needs non-zero dimensions so renderBlocks doesn't NPE on a
	// zero-width terminal.
	m.width, m.height = 80, 24
	return m
}

// TestCompactEmptyConversation: /compact on an empty msgs list should
// produce a system advisory and leave state at Idle — no stream kicked
// off, no pending state.
func TestCompactEmptyConversation(t *testing.T) {
	m := newCompactTestModel(t, compactStubProvider{summary: "unused"})

	cmd := m.startCompaction()
	if cmd != nil {
		t.Errorf("expected no tea.Cmd for empty-convo case")
	}
	if m.state != stateIdle {
		t.Errorf("state = %d, want Idle", m.state)
	}
	// Advisory block appended.
	last := m.blocks[len(m.blocks)-1]
	if last.kind != "system" || !strings.Contains(last.body, "empty") {
		t.Errorf("expected empty-convo advisory, got %+v", last)
	}
}

// TestCompactAcceptReplacesMessages — the happy path. Fire /compact,
// simulate the streaming summary completing, drive the y/n resolver,
// and assert msgs was replaced with the compacted form.
func TestCompactAcceptReplacesMessages(t *testing.T) {
	m := newCompactTestModel(t, compactStubProvider{summary: "key decisions preserved"})
	m.msgs = []agent.Message{
		agent.Text(agent.RoleUser, "help me ship feature X"),
		agent.Text(agent.RoleAssistant, "sure, let's plan it"),
		agent.Text(agent.RoleUser, "the auth bug is a blocker"),
	}

	// Kick off compaction synchronously (bypass tea.Program.Send by
	// calling the internal stream handler directly after startCompaction
	// returns).
	_ = m.startCompaction()

	// The streaming goroutine inside startCompaction sends events via
	// m.sendMsg which routes through m.program — we don't have an
	// attached program in this test, so events drop. Instead, replay
	// what the event loop would've done manually.
	m.handleStreamEvent(agent.Event{Kind: agent.EvTextDelta, Text: "key decisions preserved"})
	cmd := m.onTurnComplete()
	if cmd != nil {
		t.Errorf("onTurnComplete after compaction should not produce a tea.Cmd")
	}
	if m.state != stateCompactionPending {
		t.Fatalf("state = %d, want CompactionPending", m.state)
	}
	if m.pendingCompactionSummary != "key decisions preserved" {
		t.Errorf("pending summary = %q", m.pendingCompactionSummary)
	}

	// Accept with 'y'.
	m.resolveCompaction(true)
	if m.state != stateIdle {
		t.Errorf("state = %d, want Idle", m.state)
	}
	if len(m.msgs) != 1 {
		t.Fatalf("expected 1 message after compaction, got %d: %+v", len(m.msgs), m.msgs)
	}
	body := m.msgs[0].Content[0].Text.Text
	if !strings.Contains(body, "key decisions preserved") {
		t.Errorf("compacted msg missing summary: %q", body)
	}
	if !strings.Contains(body, "[compaction summary") {
		t.Errorf("compacted msg missing DESIGN-spec'd marker: %q", body)
	}
}

// TestCompactDeclineKeepsMessages covers the 'n' path.
func TestCompactDeclineKeepsMessages(t *testing.T) {
	m := newCompactTestModel(t, compactStubProvider{summary: "s"})
	original := []agent.Message{
		agent.Text(agent.RoleUser, "original content"),
	}
	m.msgs = append([]agent.Message(nil), original...)

	_ = m.startCompaction()
	m.handleStreamEvent(agent.Event{Kind: agent.EvTextDelta, Text: "proposed summary"})
	_ = m.onTurnComplete()

	if m.state != stateCompactionPending {
		t.Fatalf("state = %d, want CompactionPending", m.state)
	}

	m.resolveCompaction(false)

	if m.state != stateIdle {
		t.Errorf("state after decline = %d, want Idle", m.state)
	}
	if len(m.msgs) != len(original) {
		t.Errorf("decline mutated msgs: got %d want %d", len(m.msgs), len(original))
	}
	if m.msgs[0].Content[0].Text.Text != "original content" {
		t.Errorf("decline corrupted content: %q", m.msgs[0].Content[0].Text.Text)
	}
	if m.pendingCompactionSummary != "" {
		t.Errorf("pending summary not cleared after decline: %q", m.pendingCompactionSummary)
	}
}

// TestCompactEmptyResultAborts — model returns empty summary → no state
// transition to CompactionPending, advisory emitted, msgs untouched.
func TestCompactEmptyResultAborts(t *testing.T) {
	m := newCompactTestModel(t, compactStubProvider{summary: ""})
	m.msgs = []agent.Message{agent.Text(agent.RoleUser, "hi")}

	_ = m.startCompaction()
	// No text delta: simulate the stream producing only Done.
	_ = m.onTurnComplete()

	if m.state != stateIdle {
		t.Errorf("state = %d, want Idle", m.state)
	}
	if len(m.msgs) != 1 {
		t.Errorf("msgs length changed despite aborted compaction")
	}
	// At least one system block explaining the abort.
	var found bool
	for _, b := range m.blocks {
		if b.kind == "system" && strings.Contains(b.body, "empty summary") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected empty-summary advisory in blocks: %+v", m.blocks)
	}
}

// TestCompactIgnoresKeysBeforeStreamDone — during the summarisation
// stream (state=streaming, compacting=true), a 'y' key should NOT
// accidentally trigger the accept path before the pending summary is
// ready.
func TestCompactIgnoresKeysBeforeStreamDone(t *testing.T) {
	m := newCompactTestModel(t, compactStubProvider{summary: "s"})
	m.msgs = []agent.Message{agent.Text(agent.RoleUser, "hi")}
	_ = m.startCompaction()
	// State is now streaming (set by startCompaction before the goroutine).
	if m.state != stateStreaming {
		t.Fatalf("state = %d, want Streaming", m.state)
	}
	// Resolve must no-op in non-pending state.
	m.resolveCompaction(true)
	if m.state != stateStreaming {
		t.Errorf("resolveCompaction mutated state outside pending: %d", m.state)
	}
	if len(m.msgs) != 1 {
		t.Errorf("resolveCompaction outside pending mutated msgs")
	}
}

// TestCompactEditRoundTrip — the 'e' path. After compaction produces a
// summary, the user edits it inline, commits, and the updated text
// flows into both pendingCompactionSummary (used on 'y' apply) and
// the visible assistant block (what the user is reading). The draft
// they had in flight is restored to the input on commit.
func TestCompactEditRoundTrip(t *testing.T) {
	m := newCompactTestModel(t, compactStubProvider{summary: "v1 summary"})
	m.msgs = []agent.Message{agent.Text(agent.RoleUser, "hi")}
	m.input.SetValue("user's in-progress prompt")

	_ = m.startCompaction()
	m.handleStreamEvent(agent.Event{Kind: agent.EvTextDelta, Text: "v1 summary"})
	_ = m.onTurnComplete()
	if m.state != stateCompactionPending {
		t.Fatalf("state=%d, want CompactionPending", m.state)
	}

	// Enter edit mode. Input should be preloaded with the summary;
	// the stashed draft preserved.
	m.enterSummaryEdit()
	if m.state != stateCompactionEditing {
		t.Fatalf("enter: state=%d, want CompactionEditing", m.state)
	}
	if m.input.Value() != "v1 summary" {
		t.Errorf("editor not preloaded: %q", m.input.Value())
	}
	if m.savedDraftBeforeEdit != "user's in-progress prompt" {
		t.Errorf("draft not stashed: %q", m.savedDraftBeforeEdit)
	}

	// Revise the summary and commit.
	m.input.SetValue("v2 — revised summary")
	m.commitSummaryEdit()
	if m.state != stateCompactionPending {
		t.Errorf("commit: state=%d, want CompactionPending", m.state)
	}
	if m.pendingCompactionSummary != "v2 — revised summary" {
		t.Errorf("pending not updated: %q", m.pendingCompactionSummary)
	}
	if m.blocks[m.compactionBlockIdx].body != "v2 — revised summary" {
		t.Errorf("visible block not updated: %q", m.blocks[m.compactionBlockIdx].body)
	}
	if m.input.Value() != "user's in-progress prompt" {
		t.Errorf("draft not restored: %q", m.input.Value())
	}
	if m.savedDraftBeforeEdit != "" {
		t.Errorf("savedDraftBeforeEdit should be cleared after commit")
	}

	// 'y' now applies the revised summary, not the original.
	m.resolveCompaction(true)
	if m.state != stateIdle {
		t.Errorf("resolve: state=%d, want Idle", m.state)
	}
	applied := m.msgs[0].Content[0].Text.Text
	if !strings.Contains(applied, "v2 — revised summary") {
		t.Errorf("applied summary missing edit: %q", applied)
	}
}

// TestCompactEditCancelRestores — Esc/'n' during edit discards the
// editor buffer and restores the draft, leaving the pending summary
// + visible block unchanged.
func TestCompactEditCancelRestores(t *testing.T) {
	m := newCompactTestModel(t, compactStubProvider{summary: "original"})
	m.msgs = []agent.Message{agent.Text(agent.RoleUser, "hi")}
	m.input.SetValue("draft")

	_ = m.startCompaction()
	m.handleStreamEvent(agent.Event{Kind: agent.EvTextDelta, Text: "original"})
	_ = m.onTurnComplete()

	m.enterSummaryEdit()
	m.input.SetValue("throwaway revision")
	m.cancelSummaryEdit()

	if m.state != stateCompactionPending {
		t.Errorf("cancel: state=%d, want CompactionPending", m.state)
	}
	if m.pendingCompactionSummary != "original" {
		t.Errorf("pending summary mutated on cancel: %q", m.pendingCompactionSummary)
	}
	if m.blocks[m.compactionBlockIdx].body != "original" {
		t.Errorf("visible block mutated on cancel: %q", m.blocks[m.compactionBlockIdx].body)
	}
	if m.input.Value() != "draft" {
		t.Errorf("draft not restored after cancel: %q", m.input.Value())
	}
}

// TestEnterSummaryEdit_NoopOutsidePending — guarding the helpers so a
// stray 'e' keystroke from another state can't poison the editor /
// summary buffer.
func TestEnterSummaryEdit_NoopOutsidePending(t *testing.T) {
	m := newCompactTestModel(t, compactStubProvider{summary: "x"})
	m.input.SetValue("user's draft")
	m.state = stateStreaming
	m.enterSummaryEdit()
	if m.state != stateStreaming {
		t.Errorf("enterSummaryEdit mutated state from non-pending: %d", m.state)
	}
	if m.input.Value() != "user's draft" {
		t.Errorf("editor overwritten outside pending: %q", m.input.Value())
	}
}

// Catch a compilation regression — the key binding the confirmation
// uses MUST exist in the default registry so users can actually
// confirm compaction.
func TestCompactConfirmationUsesApproveDenyKeys(t *testing.T) {
	reg := keys.NewRegistry()
	// Fabricate a 'y' keypress and confirm the default registry routes
	// it to keys.Approve — the binding we rely on in Update.
	yKey := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
	if !reg.Matches(yKey, keys.Approve) {
		t.Errorf("'y' must map to keys.Approve for compaction confirm to work")
	}
	nKey := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}}
	if !reg.Matches(nKey, keys.Deny) {
		t.Errorf("'n' must map to keys.Deny for compaction decline to work")
	}
	_ = fmt.Sprintf("") // silence unused on some toolchains
}
