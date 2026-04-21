package tui

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

// captureReqProvider is a minimal agent.Provider that records the last
// TurnRequest it saw so tests can assert on req.System without spinning
// up a real LLM roundtrip. The done channel signals when StreamTurn
// has been called, so tests don't poll with sleeps.
type captureReqProvider struct {
	last agent.TurnRequest
	done chan struct{}
}

func (p *captureReqProvider) Name() string                     { return "capture" }
func (p *captureReqProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (p *captureReqProvider) StreamTurn(_ context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	p.last = req
	close(p.done)
	ch := make(chan agent.Event)
	close(ch)
	return ch, nil
}

// TestInstructions_AgentsMdFlowsIntoTurnRequest: a project root with
// an AGENTS.md is picked up at NewModel time and surfaces as
// req.System on the first streamed turn. Without this the model
// would have zero project context across the whole conversation —
// the "bring your own rules" feature that every other coding-agent
// CLI defaults to.
func TestInstructions_AgentsMdFlowsIntoTurnRequest(t *testing.T) {
	dir := t.TempDir()
	body := "always write tests first\n"
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	prov := &captureReqProvider{done: make(chan struct{})}
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(dir, "m", "p", func() (agent.Provider, error) { return prov, nil }, rnd, keys.NewRegistry())

	m.msgs = []agent.Message{agent.Text(agent.RoleUser, "hello")}
	m.startStream()
	// Wait for the provider-goroutine to record the request.
	select {
	case <-prov.done:
	case <-time.After(2 * time.Second):
		t.Fatal("StreamTurn never called")
	}
	if prov.last.System != body {
		t.Errorf("req.System = %q, want %q", prov.last.System, body)
	}
}

// TestInstructions_MissingFileLeavesSystemEmpty: a project with no
// AGENTS.md / CLAUDE.md must NOT inject a bogus prompt. The sidebar's
// Instructions line stays empty and req.System == "".
func TestInstructions_MissingFileLeavesSystemEmpty(t *testing.T) {
	dir := t.TempDir()
	prov := &captureReqProvider{}
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(dir, "m", "p", func() (agent.Provider, error) { return prov, nil }, rnd, keys.NewRegistry())
	if m.systemPrompt != "" {
		t.Errorf("expected empty systemPrompt, got %q", m.systemPrompt)
	}
	if m.systemPromptPath != "" {
		t.Errorf("expected empty systemPromptPath, got %q", m.systemPromptPath)
	}
}
