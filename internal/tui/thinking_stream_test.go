package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/pkg/agent"
)

// TestThinkingOnlyStreamDrainsAndReturnsToIdle guards the freeze bug
// where a turn that emitted only thinking deltas could leave the TUI
// stuck in streaming. We simulate a closed stream buffer directly so
// the test exercises the tick-drain path that keeps Bubble Tea
// responsive under high-rate reasoning output.
func TestThinkingOnlyStreamDrainsAndReturnsToIdle(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateStreaming
	m.turnStart = time.Now()

	for i := 0; i < 256; i++ {
		m.streamBuf = append(m.streamBuf, agent.Event{
			Kind: agent.EvThinkingDelta,
			Text: "step ",
		})
	}
	m.streamBufClosed = true

	_, cmd := m.Update(streamTickMsg{})
	if cmd == nil {
		t.Fatal("streamTick should schedule streamDoneMsg when the buffer closes")
	}
	if len(m.blocks) == 0 || m.blocks[len(m.blocks)-1].kind != "thinking" {
		t.Fatalf("expected a visible thinking block, got %+v", m.blocks)
	}

	_, _ = m.Update(cmd())
	if m.state != stateIdle {
		t.Fatalf("state = %v, want stateIdle after streamDone", m.state)
	}
	if len(m.msgs) != 1 || m.msgs[0].Role != agent.RoleAssistant {
		t.Fatalf("msgs = %+v, want one assistant message", m.msgs)
	}
	if body := m.msgs[0].Content[0].Thinking.Text; !strings.Contains(body, "step") {
		t.Fatalf("thinking body missing accumulated content: %+v", m.msgs[0].Content)
	}

	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'o'}})
	if m.input.Value() != "o" {
		t.Fatalf("input stayed unresponsive after thinking stream: %q", m.input.Value())
	}
}

func TestStreamErrorStopsTickLoop(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateStreaming

	_, _ = m.Update(streamErrorMsg{err: errors.New("boom")})
	if m.state != stateError {
		t.Fatalf("state = %v, want stateError", m.state)
	}
	_, cmd := m.Update(streamTickMsg{})
	if cmd != nil {
		t.Fatal("stream tick should stop once the stream errors")
	}
}

func TestToolTickStopsWhenNoToolIsRunning(t *testing.T) {
	m := scenarioModel(t)
	_, cmd := m.Update(toolTickMsg{})
	if cmd != nil {
		t.Fatal("tool tick should not reschedule without an active tool")
	}
}

func TestClearResetsCompactionState(t *testing.T) {
	m := scenarioModel(t)
	m.state = stateCompactionPending
	m.compacting = true
	m.pendingCompactionSummary = "summary"
	m.savedDraftBeforeEdit = "draft"
	m.compactionBlockIdx = 3
	m.blocks = []block{{kind: "assistant", body: "summary"}}
	m.msgs = []agent.Message{agent.Text(agent.RoleAssistant, "summary")}

	if cmd := m.handleSlash("/clear"); cmd != nil {
		t.Fatalf("clear returned unexpected cmd: %v", cmd)
	}
	if m.state != stateIdle {
		t.Fatalf("state = %v, want idle", m.state)
	}
	if m.compacting || m.pendingCompactionSummary != "" || m.savedDraftBeforeEdit != "" || m.compactionBlockIdx != 0 {
		t.Fatalf("compaction state not cleared: compacting=%v pending=%q draft=%q idx=%d",
			m.compacting, m.pendingCompactionSummary, m.savedDraftBeforeEdit, m.compactionBlockIdx)
	}
	if len(m.blocks) != 0 || len(m.msgs) != 0 {
		t.Fatalf("conversation not cleared: blocks=%d msgs=%d", len(m.blocks), len(m.msgs))
	}
}
