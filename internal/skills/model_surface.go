package skills

import (
	"fmt"
	"sort"
	"strings"
)

// Scope records where a skill was discovered. Project skills come from the
// cwd walk (.stado/skills/); persona skills from a persona's skills: list.
type Scope int

const (
	ScopeProject Scope = iota
	ScopePersona
)

// Skill is one parsed skill file.
// (Fields extended in skills.go — this file holds model-surface helpers.)

// UserVisible returns skills the operator may invoke via /skill or slash:.
func UserVisible(sks []Skill) []Skill {
	out := make([]Skill, 0, len(sks))
	for _, sk := range sks {
		if sk.UserInvocable {
			out = append(out, sk)
		}
	}
	return out
}

// ListingDescription is the text the model matches on: description plus
// optional when_to_use.
func (sk Skill) ListingDescription() string {
	desc := strings.TrimSpace(sk.Description)
	when := strings.TrimSpace(sk.WhenToUse)
	switch {
	case desc != "" && when != "":
		return desc + " — " + when
	case when != "":
		return when
	default:
		return desc
	}
}

// RenderedBody returns the skill body with ${STADO_SKILL_DIR} expanded.
func (sk Skill) RenderedBody() string {
	body := sk.Body
	if sk.Dir != "" {
		body = strings.ReplaceAll(body, "${STADO_SKILL_DIR}", sk.Dir)
	}
	return body
}

// AllowedToolsEffective reports whether allowed-tools from this skill may
// take effect. Project skills are fail-closed until EP-44 TOFU lands;
// persona-scoped skills are operator-authored relative to the persona file.
func (sk Skill) AllowedToolsEffective() bool {
	return sk.Scope == ScopePersona && len(sk.AllowedTools) > 0
}

// InertSkills returns the names of skills that can never be invoked: both
// disable-model-invocation (omitted from the model resource catalog) AND
// user-invocable: false (hidden from /skill and slash:).
// Such a skill is dead config — neither initiator can reach it — so callers
// surface it as a load-time warning rather than letting it sit silently.
func InertSkills(sks []Skill) []string {
	var out []string
	for _, sk := range sks {
		if sk.DisableModelInvocation && !sk.UserInvocable {
			out = append(out, sk.Name)
		}
	}
	return out
}

// Merge combines base and extra skill sets; base entries win on name collision.
func Merge(base, extra []Skill) []Skill {
	seen := make(map[string]bool, len(base)+len(extra))
	out := make([]Skill, 0, len(base)+len(extra))
	for _, s := range base {
		if seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		out = append(out, s)
	}
	for _, s := range extra {
		if seen[s.Name] {
			continue
		}
		seen[s.Name] = true
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Find returns the skill named name, or nil.
func Find(sks []Skill, name string) *Skill {
	for i := range sks {
		if sks[i].Name == name {
			return &sks[i]
		}
	}
	return nil
}

// Effective loads cwd skills merged with an optional persona's additive skills.
//
// Load returns successfully-parsed skills ALONGSIDE a per-file error for one
// bad entry (oversize, symlink, unreadable). Effective keeps that partial
// catalog and still merges persona skills, propagating the warning rather than
// returning nil — a single bad skill file must not black-hole every valid
// project + persona skill from operator gestures or context-resource facts.
func Effective(cwd string, personaSkills []string, personaDir string) ([]Skill, error) {
	base, err := Load(cwd)
	if len(personaSkills) == 0 || strings.TrimSpace(personaDir) == "" {
		return base, err
	}
	extra, warnings := LoadPaths(personaDir, personaSkills)
	if len(warnings) > 0 {
		warningErr := fmt.Errorf("%s", strings.Join(warnings, "; "))
		if err != nil {
			err = fmt.Errorf("%v; %w", err, warningErr)
		} else {
			err = warningErr
		}
	}
	return Merge(base, extra), err
}
