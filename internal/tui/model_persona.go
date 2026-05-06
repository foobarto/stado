package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/personas"
	"github.com/foobarto/stado/internal/tui/personapicker"
)

// initPersona seeds the resolver and loads the active persona from
// cfg.Defaults.Persona. Empty / unresolvable names fall back to the
// bundled "default" persona; a fully unresolvable default leaves
// m.persona == nil so turnSystemPrompt falls back to the legacy
// instructions.ComposeSystemPrompt path.
func (m *Model) initPersona(cfg *config.Config) {
	m.personaResolver = personas.Resolver{
		CWD:       m.cwd,
		ConfigDir: config.ConfigDir(),
	}
	name := ""
	if cfg != nil {
		name = strings.TrimSpace(cfg.Defaults.Persona)
	}
	if name == "" {
		name = "default"
	}
	if p, err := m.personaResolver.Load(name); err == nil {
		m.persona = p
	}
}

// personaName returns the active persona name, or "" when none is set.
// Used by the status renderer.
func (m *Model) personaName() string {
	if m.persona == nil {
		return ""
	}
	return m.persona.Name
}

// switchPersona resolves name and replaces m.persona. Returns an error
// describing why the switch failed; on success the next turn picks up
// the new persona via turnSystemPrompt.
func (m *Model) switchPersona(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("persona: empty name")
	}
	p, err := m.personaResolver.Load(name)
	if err != nil {
		return fmt.Errorf("persona %q: %w", name, err)
	}
	m.persona = p
	return nil
}

// persistDefaultPersona writes [defaults].persona to the active config.
// No-op when no config path is bound (e.g. tests). Mirrors
// persistDefaultModel.
func (m *Model) persistDefaultPersona(name string) error {
	if m.cfg == nil || strings.TrimSpace(m.cfg.ConfigPath) == "" {
		return nil
	}
	if err := config.WriteDefaultPersona(m.cfg.ConfigPath, name); err != nil {
		return fmt.Errorf("save default persona: %w", err)
	}
	m.cfg.Defaults.Persona = strings.TrimSpace(name)
	return nil
}

// openPersonaPicker builds the item list (project → user → bundled,
// deduped by name) and shows the modal seeded on the active persona.
func (m *Model) openPersonaPicker() {
	all, err := m.personaResolver.List()
	if err != nil {
		m.appendBlock(block{kind: "system", body: "persona: list failed: " + err.Error()})
		return
	}
	if len(all) == 0 {
		m.appendBlock(block{kind: "system", body: "persona: no personas resolvable"})
		return
	}
	items := make([]personapicker.Item, 0, len(all))
	for _, p := range all {
		items = append(items, personapicker.Item{
			ID:          p.Name,
			Title:       p.Title,
			Description: p.Description,
			Origin:      m.personaOrigin(p),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	m.personaPicker.Open(items, m.personaName())
	m.layout()
}

// personaOrigin labels the picker entry's source: bundled (embedded),
// project (cwd .stado/personas/), or user (~/.config/stado/personas/).
func (m *Model) personaOrigin(p personas.Persona) string {
	if p.SourcePath == "" {
		return "bundled"
	}
	if m.personaResolver.CWD != "" &&
		strings.HasPrefix(p.SourcePath, m.personaResolver.CWD) {
		return "project"
	}
	return "user"
}

// applyPersonaSelection swaps in the picked persona and persists the
// choice. Used by both the picker confirm path and direct
// /persona <name> dispatch.
func (m *Model) applyPersonaSelection(name string) {
	old := m.personaName()
	if err := m.switchPersona(name); err != nil {
		m.appendBlock(block{kind: "system", body: err.Error()})
		return
	}
	body := fmt.Sprintf("persona: %s → %s", emptyAsDefault(old), m.personaName())
	if err := m.persistDefaultPersona(m.personaName()); err != nil {
		body += "\n" + err.Error()
	}
	m.appendBlock(block{kind: "system", body: body})
}

func emptyAsDefault(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}
