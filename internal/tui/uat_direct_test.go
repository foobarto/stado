package tui

// UAT-style tests that drive the Model via direct Update() calls.
// Scope: the three dogfood bugs from 2026-04-20 screenshot:
//   1. Queued user messages invisible until stream ends.
//   2. Chat "blocks" during streaming — typing not visible.
//   3. OSC-tail bytes ("rgb:1e1e/...") leaking into the textarea.
//
// Why not teatest here: the full TUI render has a sidebar +
// viewport-backed conversation area; teatest's virtual terminal
// snapshot doesn't guarantee the block landed in a stable frame
// before assertion. Direct Update calls exercise the exact same
// code path (key routing → state transitions → block appends)
// without the render-timing fragility.

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

// uatStub is a no-op agent.Provider. StreamTurn returns an
// immediately-closed channel so a submit doesn't panic on nil.
type uatStub struct{}

func (uatStub) Name() string                     { return "uat-stub" }
func (uatStub) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (uatStub) StreamTurn(_ context.Context, _ agent.TurnRequest) (<-chan agent.Event, error) {
	ch := make(chan agent.Event)
	close(ch)
	return ch, nil
}

// uatModel wires a Model with a stubbed provider and 120x30 pseudo-
// screen. Every UAT test starts from here so the harness stays
// consistent.
func uatModel(t *testing.T) *Model {
	t.Helper()
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	reg := keys.NewRegistry()
	stub := uatStub{}
	m := NewModel(t.TempDir(), "m", "p",
		func() (agent.Provider, error) { return stub, nil }, rnd, reg)
	m.provider = stub
	m.width, m.height = 120, 30
	return m
}

// TestUAT_TypingDuringStreamingBuildsBuffer: dogfood-bug #2.
// Feeding runes while state=stateStreaming must accumulate in the
// input buffer (no "blocked" feeling). State stays streaming; no
// new block appears until Enter.
func TestUAT_TypingDuringStreamingBuildsBuffer(t *testing.T) {
	m := uatModel(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.state = stateStreaming
	m.streamCancel = cancel
	_ = ctx

	for _, r := range "draft-while-busy" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	if m.input.Value() != "draft-while-busy" {
		t.Errorf("input = %q, want 'draft-while-busy' (typing should land in buffer during streaming)",
			m.input.Value())
	}
	if m.state != stateStreaming {
		t.Errorf("state = %v, typing shouldn't change state", m.state)
	}
}

// TestUAT_SubmitWhileStreamingAppendsUserBlock: dogfood-bug #3.
// Enter during streaming must immediately push the user's message
// to blocks — they see it in the chat. The block appears BEFORE
// the stream completes (m.msgs add is deferred to drain so the
// current turn's context window isn't mutated).
func TestUAT_SubmitWhileStreamingAppendsUserBlock(t *testing.T) {
	m := uatModel(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.state = stateStreaming
	m.streamCancel = cancel
	_ = ctx

	for _, r := range "wait check this" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	priorMsgCount := len(m.msgs)
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// Visible immediately via blocks.
	if len(m.blocks) == 0 {
		t.Fatal("blocks empty after queued submit — user message not visible")
	}
	last := m.blocks[len(m.blocks)-1]
	if last.kind != "user" || last.body != "wait check this" {
		t.Errorf("last block = %+v, want user/'wait check this'", last)
	}

	// m.msgs UNCHANGED — the current turn's provider context must
	// not see the queued user message yet. It'll get added on drain
	// (onTurnComplete) before the next turn starts.
	if len(m.msgs) != priorMsgCount {
		t.Errorf("msgs grew prematurely: was %d, now %d — queue should defer msgs add to drain",
			priorMsgCount, len(m.msgs))
	}

	// Queue state: pending prompt + input cleared.
	if m.queuedPrompt != "wait check this" {
		t.Errorf("queuedPrompt = %q", m.queuedPrompt)
	}
	if m.input.Value() != "" {
		t.Errorf("input should be cleared: %q", m.input.Value())
	}
}

// TestUAT_OSCTailDroppedByFilter: dogfood-bug #1. A bubbletea
// KeyMsg shaped like the leaked OSC tail must be dropped by the
// filter, never reaching the editor.
func TestUAT_OSCTailDroppedByFilter(t *testing.T) {
	for _, tail := range []string{
		"rgb:1e1e/1e1e/1e1e",
		"rgb:1e1e/1e1e",                        // shorter ragged split
		"]11;rgb:1e1e/1e1e/1e1e",               // full prefix (Alt-wrapped shape)
	} {
		msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tail)}
		if got := filterOSCResponses(nil, msg); got != nil && !strings.Contains(tail, "]11") {
			// rgb: + / form — must drop.
			t.Errorf("tail %q should have been dropped, got %+v", tail, got)
		}
	}
}

// Note: slash commands (`/foo`) can't be type-tested like text
// prompts because `/` is bound as CommandList (opens the slash
// palette — keys/defaults.go). The palette has its own input
// buffer and its own Update loop. Testing the palette during
// streaming is out of scope for this UAT suite; see
// plugin_slash_test.go for that surface.
