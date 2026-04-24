package tui

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/memory"
	stadogit "github.com/foobarto/stado/internal/state/git"
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
// an AGENTS.md is picked up at NewModel time and surfaces in req.System
// on the first streamed turn, after stado's own identity preamble.
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
	for _, want := range []string{"Identify as stado", "model: m", "provider: capture", strings.TrimSpace(body)} {
		if !strings.Contains(prov.last.System, want) {
			t.Errorf("req.System missing %q:\n%s", want, prov.last.System)
		}
	}
}

// TestInstructions_MissingFileLeavesSystemEmpty: a project with no
// AGENTS.md / CLAUDE.md leaves project instructions empty. The sidebar's
// Instructions line stays empty; per-turn requests still get stado's
// identity preamble from turnSystemPrompt.
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

func TestInstructions_MemoryContextFlowsIntoTurnRequestWhenEnabled(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	dir := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	repoID, err := stadogit.RepoID(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	store := memory.Store{Path: filepath.Join(cfg.StateDir(), "memory", "memory.jsonl"), Actor: "test"}
	item := memory.Item{
		ID:         "mem_tui",
		Scope:      "repo",
		RepoID:     repoID,
		Kind:       "preference",
		Summary:    "Prefer focused tests",
		Confidence: "approved",
	}
	raw, _ := json.Marshal(memory.UpdateRequest{Action: "upsert", Item: &item})
	if err := store.Update(context.Background(), raw); err != nil {
		t.Fatal(err)
	}

	prov := &captureReqProvider{done: make(chan struct{})}
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(dir, "m", "p", func() (agent.Provider, error) { return prov, nil }, rnd, keys.NewRegistry())
	m.cfg = cfg
	m.cfg.Memory.Enabled = true
	m.msgs = []agent.Message{agent.Text(agent.RoleUser, "write focused tests")}
	m.startStream()

	select {
	case <-prov.done:
	case <-time.After(2 * time.Second):
		t.Fatal("StreamTurn never called")
	}
	if !strings.Contains(prov.last.System, "[repo/preference mem_tui] Prefer focused tests") {
		t.Fatalf("req.System missing memory context:\n%s", prov.last.System)
	}
}
