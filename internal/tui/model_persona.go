package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/personas"
	"github.com/foobarto/stado/internal/skills"
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
	// Fold the launch persona's additive skills into the active set. At
	// NewModel time m.baseSkills hasn't been captured yet (initPersona runs
	// later, from app.go), so snapshot the cwd-discovered set as the base
	// here before layering persona skills on top. Warnings go to stderr —
	// matching the build-time skills/instructions loader policy (the TUI
	// isn't live yet at launch).
	m.baseSkills = append([]skills.Skill(nil), m.skills...)
	m.applyPersonaScope(stderrSkillSlashWarn)
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
	// Re-scope the active skill set: base (cwd) skills ∪ this persona's
	// additive skills. Tools re-scope for free (toolSurfaceForTurn reads
	// m.persona per turn). Warnings surface as system blocks since the TUI
	// is live by the time a /persona switch happens.
	m.applyPersonaScope(func(msg string) {
		m.appendBlock(block{kind: "system", body: msg})
	})
	return nil
}

// applyPersonaScope recomputes the effective skill set as
// baseSkills ∪ personaSkills(active persona) and re-registers the skill
// slash commands. Additive: the base (cwd-discovered) skills are never
// dropped — a persona only ever ADDS skills on top. Called at launch
// (initPersona) and on every /persona switch so persona skills come and
// go with the active persona. tools re-scope for free per-turn via
// toolSurfaceForTurn; this only handles the skill layer.
func (m *Model) applyPersonaScope(emit func(string)) {
	personaSks, warnings := m.personaSkills(m.persona)
	// Effective set: base first (nearest-wins so a base skill shadows a
	// persona skill of the same name — the global surface is authoritative
	// and additive personas can't silently override it).
	seen := make(map[string]bool, len(m.baseSkills)+len(personaSks))
	effective := make([]skills.Skill, 0, len(m.baseSkills)+len(personaSks))
	for _, s := range m.baseSkills {
		if seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		effective = append(effective, s)
	}
	for _, s := range personaSks {
		if seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		effective = append(effective, s)
	}
	m.skills = effective
	for _, w := range warnings {
		if emit != nil {
			emit(w)
		}
	}
	// Re-register the dynamic skill-slash layer (replaces atomically) so the
	// palette + /<name> dispatch map track the new effective set.
	m.registerSkillSlashCommands(emit)
}

// personaSkills loads the persona's additive `skills:` files, resolved
// relative to the persona file's own directory (filepath.Dir(SourcePath)).
// Bundled personas have an empty SourcePath → no on-disk dir → nothing to
// load (acceptable for v1; bundled personas ship tools, not skill files).
// Returns the loaded skills plus any non-fatal warnings (unknown path,
// symlink/traversal escape, oversize) for the caller to surface.
func (m *Model) personaSkills(p *personas.Persona) ([]skills.Skill, []string) {
	if p == nil || len(p.Skills) == 0 || strings.TrimSpace(p.SourcePath) == "" {
		return nil, nil
	}
	baseDir := filepath.Dir(p.SourcePath)
	return skills.LoadPaths(baseDir, p.Skills)
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
