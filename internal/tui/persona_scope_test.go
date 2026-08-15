package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/personas"
	rt "github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/skills"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/palette"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

// writeProjectPersona drops a persona .md under {cwd}/.stado/personas/.
func writeProjectPersona(t *testing.T, cwd, name, content string) {
	t.Helper()
	dir := filepath.Join(cwd, ".stado", "personas")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writePersonaSkill drops a skill .md under {cwd}/.stado/personas/<sub>/.
func writePersonaSkill(t *testing.T, cwd, sub, file, content string) {
	t.Helper()
	dir := filepath.Join(cwd, ".stado", "personas", sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newPersonaScopeModel(t *testing.T, cwd string) *Model {
	t.Helper()
	t.Cleanup(func() { palette.RegisterDynamicCommands(nil) })
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(cwd, "m", "p",
		func() (agent.Provider, error) { return nil, nil }, rnd, keys.NewRegistry())
	m.width, m.height = 120, 30
	m.personaResolver = personas.Resolver{CWD: cwd, AllowProject: true}
	// Default-state base skills come from NewModel's skills.Load(cwd); mirror
	// it explicitly so the test doesn't depend on construction order.
	m.baseSkills = append([]skills.Skill(nil), m.skills...)
	return m
}

// skillNames lists the active skill names for assertions.
func (m *Model) testSkillNames() map[string]bool {
	out := map[string]bool{}
	for _, s := range m.skills {
		out[s.Name] = true
	}
	return out
}

// TestPersonaScope_SwitchRescopesSkills: selecting a persona registers its
// declared `skills:` (additive on top of the base cwd skills); switching to
// a different persona removes the prior persona's skills and adds the new
// one's, while the base skill survives both.
func TestPersonaScope_SwitchRescopesSkills(t *testing.T) {
	cwd := t.TempDir()
	// A base (global) cwd skill — must survive every persona switch.
	writeRawSkill(t, cwd, "base.md", "---\nname: base\n---\nbase body")

	// Two personas, each with a distinct skill file under its own subdir.
	writeProjectPersona(t, cwd, "alpha",
		"---\nname: alpha\nskills: [alpha-skills/recon.md]\n---\nALPHA")
	writePersonaSkill(t, cwd, "alpha-skills", "recon.md",
		"---\nname: recon\nslash: recon\n---\nrecon body")
	writeProjectPersona(t, cwd, "bravo",
		"---\nname: bravo\nskills: [bravo-skills/report.md]\n---\nBRAVO")
	writePersonaSkill(t, cwd, "bravo-skills", "report.md",
		"---\nname: report\nslash: report\n---\nreport body")

	m := newPersonaScopeModel(t, cwd)

	// Reproduce-first: before any persona scope is applied, only the base
	// skill is active; the persona-declared skills are absent.
	if names := m.testSkillNames(); names["recon"] || names["report"] {
		t.Fatalf("persona skills should not be active before selection; got %v", names)
	}

	// Switch to alpha → recon appears, base survives, report absent.
	m.applyPersonaSelection("alpha")
	names := m.testSkillNames()
	if !names["base"] {
		t.Errorf("base skill must survive persona selection (additive)")
	}
	if !names["recon"] {
		t.Errorf("alpha's recon skill should be active; got %v", names)
	}
	if names["report"] {
		t.Errorf("bravo's report skill should NOT be active under alpha; got %v", names)
	}
	if m.skillSlash["recon"] != "recon" {
		t.Errorf("recon slash should be registered; skillSlash=%v", m.skillSlash)
	}

	// Switch to bravo → report appears, recon removed, base still survives.
	m.applyPersonaSelection("bravo")
	names = m.testSkillNames()
	if !names["base"] {
		t.Errorf("base skill must survive the second switch")
	}
	if !names["report"] {
		t.Errorf("bravo's report skill should be active; got %v", names)
	}
	if names["recon"] {
		t.Errorf("alpha's recon skill should be removed after switching to bravo; got %v", names)
	}
	if m.skillSlash["report"] != "report" {
		t.Errorf("report slash should be registered after switch; skillSlash=%v", m.skillSlash)
	}
	if _, stillThere := m.skillSlash["recon"]; stillThere {
		t.Errorf("recon slash should be unregistered after switching away; skillSlash=%v", m.skillSlash)
	}
}

// TestPersonaScope_PromotesToolsToSurface: the active persona's tools
// (and recommended_tools) are merged into the per-turn autoload surface;
// switching away removes them. Additive — the default core stays.
func TestPersonaScope_PromotesToolsToSurface(t *testing.T) {
	cwd := t.TempDir()
	// Pick a tool that is NOT in the default autoload core to prove promotion.
	reg := rt.BuildDefaultRegistry(nil)
	base := rt.AutoloadedTools(reg, &config.Config{})
	baseNames := map[string]bool{}
	for _, tl := range base {
		baseNames[tl.Name()] = true
	}
	var promote string
	for _, tl := range reg.All() {
		if !baseNames[tl.Name()] {
			promote = tl.Name()
			break
		}
	}
	if promote == "" {
		t.Skip("no non-core registered tool available to test promotion")
	}

	writeProjectPersona(t, cwd, "tooled",
		"---\nname: tooled\ntools: ["+promote+"]\n---\nTOOLED")
	writeProjectPersona(t, cwd, "plain",
		"---\nname: plain\n---\nPLAIN")

	m := newPersonaScopeModel(t, cwd)
	m.cfg = &config.Config{}
	m.executor = &tools.Executor{Registry: reg, Runner: sandbox.NoneRunner{}}

	surfaceHas := func(name string) bool {
		for _, tl := range m.toolSurfaceForTurn() {
			if tl.Name() == name {
				return true
			}
		}
		return false
	}

	// Reproduce-first: with no persona / a plain persona, the tool is NOT
	// on the per-turn surface.
	m.applyPersonaSelection("plain")
	if surfaceHas(promote) {
		t.Fatalf("tool %q should not be on the surface under a plain persona", promote)
	}

	// Switch to the tooled persona → the tool is promoted onto the surface.
	m.applyPersonaSelection("tooled")
	if !surfaceHas(promote) {
		t.Errorf("tool %q should be promoted onto the per-turn surface under the tooled persona", promote)
	}
	// Default core still present (additive).
	for name := range baseNames {
		if !surfaceHas(name) {
			t.Errorf("promotion must be additive; default %q dropped from surface", name)
		}
	}

	// Switch back to plain → promotion is withdrawn.
	m.applyPersonaSelection("plain")
	if surfaceHas(promote) {
		t.Errorf("tool %q should be withdrawn from the surface after switching back to plain", promote)
	}
}
