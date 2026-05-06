package tui

import (
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/personas"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

func personaSlashModel(t *testing.T) *Model {
	t.Helper()
	rnd, _ := render.New(theme.Default())
	m := NewModel(t.TempDir(), "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())
	m.width, m.height = 120, 30
	// Resolver bound to bundled-only — no project / user dirs.
	m.personaResolver = personas.Resolver{}
	if p, err := m.personaResolver.Load("default"); err == nil {
		m.persona = p
	}
	return m
}

// TestPersonaSlashSwitch confirms /persona <name> resolves and replaces
// the active persona, and that the system prompt picks up the new body.
func TestPersonaSlashSwitch(t *testing.T) {
	m := personaSlashModel(t)
	m.handleSlash("/persona offsec")
	if m.persona == nil || m.persona.Name != "offsec" {
		t.Fatalf("expected persona=offsec, got %+v", m.persona)
	}
	if got := m.turnSystemPrompt(""); !strings.Contains(got, m.persona.Body[:64]) {
		t.Errorf("system prompt should contain persona body; got prefix %q", first(got, 80))
	}
}

func TestPersonaSlashUnknown(t *testing.T) {
	m := personaSlashModel(t)
	m.handleSlash("/persona does-not-exist")
	// Persona stays as the previous value (default); system block
	// records the failure.
	if m.persona == nil || m.persona.Name != "default" {
		t.Errorf("unknown persona should not replace active one; got %+v", m.persona)
	}
	found := false
	for _, b := range m.blocks {
		if b.kind == "system" && strings.Contains(b.body, "does-not-exist") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected system block to mention failure for unknown name")
	}
}

// TestPersonaSlashOpensPicker: bare /persona opens the modal.
func TestPersonaSlashOpensPicker(t *testing.T) {
	m := personaSlashModel(t)
	if m.personaPicker.Visible {
		t.Fatal("picker should start closed")
	}
	m.handleSlash("/persona")
	if !m.personaPicker.Visible {
		t.Fatal("/persona with no arg should open the picker")
	}
	if len(m.personaPicker.Items) == 0 {
		t.Error("picker should have items from bundled library")
	}
}

// TestTurnSystemPromptFallsBackWithoutPersona checks that nil persona
// preserves the legacy ComposeSystemPrompt path.
func TestTurnSystemPromptFallsBackWithoutPersona(t *testing.T) {
	m := personaSlashModel(t)
	m.persona = nil
	got := m.turnSystemPrompt("")
	// Legacy path returns a non-empty string from the template even
	// when systemPrompt is empty (template can render runtime metadata).
	// We only need to confirm it's not the persona body.
	if strings.Contains(got, "operating manual") {
		t.Errorf("legacy path leaked persona body: %q", first(got, 120))
	}
}

func first(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
