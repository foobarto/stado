package runtime

import (
	"strings"
	"testing"

	pluginruntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/skills"
	"github.com/foobarto/stado/pkg/tool"
)

type skillContextCeiling map[string]bool

func (c skillContextCeiling) AllowsToolSurface(name string) bool          { return c[name] }
func (c skillContextCeiling) ApplyToolSurface(tool.ToolSurfaceEdit) error { return nil }

func TestSkillContextResourcesEnforceVisibilityTrustAndExactCeiling(t *testing.T) {
	access := NewSkillContextResourceAccess([]skills.Skill{
		{Name: "hidden", Body: "operator only", DisableModelInvocation: true, Scope: skills.ScopeProject},
		{Name: "project", Body: "project body", Scope: skills.ScopeProject, AllowedTools: []string{"fs__read"}},
		{Name: "persona", Description: "inspect code", WhenToUse: "during review", Body: "persona body", Scope: skills.ScopePersona, AllowedTools: []string{"fs__read", "globally_disabled", "unknown__tool"}},
	})
	snapshot, err := access.Catalog("skill", skillContextCeiling{"fs__read": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Resources) != 2 {
		t.Fatalf("resources = %+v, want public project + persona only", snapshot.Resources)
	}
	byName := map[string]pluginruntime.ContextResource{}
	for _, resource := range snapshot.Resources {
		byName[resource.Name] = resource
		if !resource.ModelVisible {
			t.Fatalf("model-hidden resource escaped native projection: %+v", resource)
		}
	}
	if _, ok := byName["hidden"]; ok {
		t.Fatal("disable-model-invocation resource reached catalog")
	}
	if got := byName["project"].EffectiveAllowedTools; len(got) != 0 {
		t.Fatalf("project allowed-tools must remain inert, got %v", got)
	}
	if got := byName["persona"].EffectiveAllowedTools; len(got) != 1 || got[0] != "fs__read" {
		t.Fatalf("persona effective tools = %v, want exact filtered-registry intersection", got)
	}
	if byName["persona"].Summary != "inspect code — during review" {
		t.Fatalf("summary = %q", byName["persona"].Summary)
	}
}

func TestSkillContextOpenIsDigestFencedAndPreservesBundleExpansion(t *testing.T) {
	access := NewSkillContextResourceAccess([]skills.Skill{{
		Name: "check", Body: "Run ${STADO_SKILL_DIR}/check.sh", Dir: "/repo/.stado/skills/check",
		Path: "/repo/.stado/skills/check/SKILL.md", Scope: skills.ScopePersona,
		AllowedTools: []string{"fs__read"},
	}})
	ceiling := skillContextCeiling{"fs__read": true}
	snapshot, err := access.Catalog("skill", ceiling)
	if err != nil {
		t.Fatal(err)
	}
	resource := snapshot.Resources[0]
	opened, err := access.Open("skill", resource.ID, snapshot.Digest, ceiling)
	if err != nil {
		t.Fatal(err)
	}
	if opened.ID != resource.ID || opened.Digest != resource.Digest || opened.ContentFormat != "text/markdown" {
		t.Fatalf("opened facts drifted: %+v", opened)
	}
	if opened.Content != "Run /repo/.stado/skills/check/check.sh" {
		t.Fatalf("content = %q", opened.Content)
	}
	if _, err := access.Open("skill", resource.ID, snapshot.Digest, skillContextCeiling{}); err == nil {
		t.Fatal("open accepted a stale catalog digest after effective ceiling changed")
	}
	if _, err := access.Open("skill", resource.ID, snapshot.Digest, ceiling); err != nil {
		t.Fatalf("repeat exact open: %v", err)
	}
}

func TestSkillContextResourceIDBindsDeclaredTools(t *testing.T) {
	makeID := func(declared string) string {
		access := NewSkillContextResourceAccess([]skills.Skill{{
			Name: "same", Body: "same", Path: "/same", Scope: skills.ScopeProject, AllowedTools: []string{declared},
		}})
		snapshot, err := access.Catalog("skill", nil)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot.Resources[0].ID
	}
	if makeID("fs__read") == makeID("shell__exec") {
		t.Fatal("resource id does not bind declared allowed-tools")
	}
}

func TestSkillContextPersonaAllowedToolsAreBounded(t *testing.T) {
	declared := make([]string, maxSkillContextEffectiveTools+1)
	ceiling := skillContextCeiling{}
	for i := range declared {
		declared[i] = string(rune('a'+i%26)) + string(rune('A'+i/26))
		ceiling[declared[i]] = true
	}
	access := NewSkillContextResourceAccess([]skills.Skill{{Name: "wide", Body: "x", Scope: skills.ScopePersona, AllowedTools: declared}})
	if _, err := access.Catalog("skill", ceiling); err == nil {
		t.Fatalf("catalog accepted more than %d persona allowed-tools", maxSkillContextEffectiveTools)
	}
}

func TestSkillContextModelContentCeilingAccepts128KiBAndOmitsNextByte(t *testing.T) {
	accepted := strings.Repeat("x", maxSkillContextModelContentBytes)
	access := NewSkillContextResourceAccess([]skills.Skill{
		{Name: "accepted", Body: accepted, Scope: skills.ScopeProject},
		{Name: "oversize", Body: accepted + "x", Scope: skills.ScopeProject},
	})
	snapshot, err := access.Catalog("skill", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Resources) != 1 || snapshot.Resources[0].Name != "accepted" {
		t.Fatalf("model resources = %+v, want only exact-128KiB skill", snapshot.Resources)
	}
	opened, err := access.Open("skill", snapshot.Resources[0].ID, snapshot.Digest, nil)
	if err != nil {
		t.Fatal(err)
	}
	if opened.Content != accepted {
		t.Fatalf("opened content length = %d, want %d", len(opened.Content), len(accepted))
	}
}

func TestAgentLoopToolSurfaceCeilingIsExactAndIndependentOfActiveState(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	all := reg.Snapshot().Tools
	if len(all) == 0 {
		t.Fatal("default registry is empty")
	}
	known := all[0].Name()
	ceiling := make(map[string]bool, len(all))
	for _, candidate := range all {
		ceiling[candidate.Name()] = true
	}
	surface := &sessionToolSurface{activated: map[string]bool{known: true}, ceiling: ceiling}
	if !surface.AllowsToolSurface(known) || surface.AllowsToolSurface("absent__tool") {
		t.Fatalf("captured ceiling is not exact: known=%v absent=%v", surface.AllowsToolSurface(known), surface.AllowsToolSurface("absent__tool"))
	}
	if err := surface.ApplyToolSurface(tool.ToolSurfaceEdit{Deactivate: []string{known}}); err != nil {
		t.Fatal(err)
	}
	if !surface.AllowsToolSurface(known) {
		t.Fatal("deactivation narrowed immutable session ceiling")
	}
	if err := surface.ApplyToolSurface(tool.ToolSurfaceEdit{Activate: []string{known}}); err != nil {
		t.Fatalf("reactivate permitted tool: %v", err)
	}
	if err := surface.ApplyToolSurface(tool.ToolSurfaceEdit{Activate: []string{"absent__tool"}}); err == nil {
		t.Fatal("surface activated a tool outside the captured registry")
	}
}
