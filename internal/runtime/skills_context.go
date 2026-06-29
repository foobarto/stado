package runtime

import (
	"context"
	"os"
	"path/filepath"

	"github.com/foobarto/stado/internal/personas"
	"github.com/foobarto/stado/internal/skills"
	"github.com/foobarto/stado/internal/tools"
)

type skillCatalogKey struct{}

// EffectiveSkills loads the skill catalog for a workdir merged with an
// optional persona's additive skills — (cwd ∪ persona). Centralizes the
// persona-dir/paths derivation so every autonomous surface (run, ACP,
// headless, subagent) wires skills identically. Falls back to the process
// cwd when workdir is empty (matches run/TUI behavior).
func EffectiveSkills(workdir string, p *personas.Persona) ([]skills.Skill, error) {
	cwd := workdir
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	var personaSkillPaths []string
	personaDir := ""
	if p != nil {
		personaSkillPaths = p.Skills
		if p.SourcePath != "" {
			personaDir = filepath.Dir(p.SourcePath)
		}
	}
	return skills.Effective(cwd, personaSkillPaths, personaDir)
}

// SkillModelInvocationEnabled reports whether the model-facing skill surface
// — the system-prompt listing AND the skills__load autoload — should appear
// this session. True iff at least one skill is model-visible AND skills__load
// is still registered. Denying skills__load via [tools].disabled unregisters
// it and, through this single gate, also suppresses the listing, so model
// invocation is disabled wholesale (EP-0045 trust rule 3) while user
// invocation (/skill:, --skill, slash:) is unaffected.
func SkillModelInvocationEnabled(reg *tools.Registry, sks []skills.Skill) bool {
	if reg == nil || len(skills.ModelVisible(sks)) == 0 {
		return false
	}
	_, ok := reg.Get("skills__load")
	return ok
}

// WithSkillCatalog attaches the session skill set to ctx for skills__load.
func WithSkillCatalog(ctx context.Context, catalog []skills.Skill) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, skillCatalogKey{}, append([]skills.Skill(nil), catalog...))
}

// SkillCatalogFrom returns the skill catalog stored in ctx, if any.
func SkillCatalogFrom(ctx context.Context) ([]skills.Skill, bool) {
	if ctx == nil {
		return nil, false
	}
	v, ok := ctx.Value(skillCatalogKey{}).([]skills.Skill)
	if !ok || len(v) == 0 {
		return nil, false
	}
	return v, true
}
