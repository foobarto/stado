package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/foobarto/stado/internal/skills"
	pkgtool "github.com/foobarto/stado/pkg/tool"
)

type skillActivateHost struct {
	activated []string
}

func (h *skillActivateHost) Approve(context.Context, pkgtool.ApprovalRequest) (pkgtool.Decision, error) {
	return pkgtool.DecisionAllow, nil
}
func (h *skillActivateHost) Workdir() string { return "/tmp" }
func (h *skillActivateHost) PriorRead(pkgtool.ReadKey) (pkgtool.PriorReadInfo, bool) {
	return pkgtool.PriorReadInfo{}, false
}
func (h *skillActivateHost) RecordRead(pkgtool.ReadKey, pkgtool.PriorReadInfo) {}
func (h *skillActivateHost) ActivateTool(name string) {
	h.activated = append(h.activated, name)
}

func TestMetaSkillLoad_InjectsBody(t *testing.T) {
	catalog := []skills.Skill{{
		Name:        "refactor",
		Description: "refactor code",
		Body:        "Do the refactor.",
		Scope:       skills.ScopeProject,
	}}
	ctx := WithSkillCatalog(context.Background(), catalog)
	tool := &metaSkillLoad{}
	res, err := tool.Run(ctx, json.RawMessage(`{"name":"refactor"}`), &skillActivateHost{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	body, trimmed := AbsorbSkillLoad(res.Content)
	if body != "Do the refactor." {
		t.Fatalf("body = %q", body)
	}
	if trimmed == res.Content {
		t.Fatal("expected trimmed tool result")
	}
}

func TestMetaSkillLoad_RespectsDisableModelInvocation(t *testing.T) {
	catalog := []skills.Skill{{
		Name:                   "deploy",
		DisableModelInvocation: true,
		Body:                   "ship",
	}}
	ctx := WithSkillCatalog(context.Background(), catalog)
	tool := &metaSkillLoad{}
	res, err := tool.Run(ctx, json.RawMessage(`{"name":"deploy"}`), &skillActivateHost{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Error == "" {
		t.Fatal("expected error for disable-model-invocation skill")
	}
}

// TestSkillModelInvocationEnabled gates the model-facing surface (listing +
// skills__load autoload) on: ≥1 model-visible skill AND skills__load still
// registered. Denying the tool (EP-0045 rule 3) flips it false, which
// suppresses the listing too — model invocation off wholesale.
func TestSkillModelInvocationEnabled(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	modelSkill := []skills.Skill{{Name: "refactor", Description: "x", Body: "y"}}

	if !SkillModelInvocationEnabled(reg, modelSkill) {
		t.Error("want true: model-visible skill + skills__load registered")
	}
	if SkillModelInvocationEnabled(reg, nil) {
		t.Error("want false: no skills")
	}
	hidden := []skills.Skill{{Name: "deploy", DisableModelInvocation: true, Body: "z"}}
	if SkillModelInvocationEnabled(reg, hidden) {
		t.Error("want false: only disable-model-invocation skills")
	}
	if SkillModelInvocationEnabled(nil, modelSkill) {
		t.Error("want false: nil registry")
	}

	// Deny skills__load → gate false even with a model-visible skill.
	denied := BuildDefaultRegistry(nil)
	denied.Unregister("skills__load")
	if SkillModelInvocationEnabled(denied, modelSkill) {
		t.Error("want false: skills__load denied/unregistered disables model invocation")
	}
}

func TestSkillLoadAllowedTools(t *testing.T) {
	raw := `{"name":"recon","body":"x","allowed_tools":["fs__read","fs__grep"],"loaded":true}`
	got := SkillLoadAllowedTools(raw)
	want := []string{"fs__read", "fs__grep"}
	if len(got) != len(want) {
		t.Fatalf("allowed = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("allowed = %v, want %v", got, want)
		}
	}
	if SkillLoadAllowedTools(`{"loaded":false}`) != nil {
		t.Error("non-loaded result should return nil")
	}
	if SkillLoadAllowedTools("not json") != nil {
		t.Error("invalid json should return nil")
	}
}

func TestMetaSkillLoad_AllowedToolsPersonaOnly(t *testing.T) {
	catalog := []skills.Skill{{
		Name:         "recon",
		Body:         "look around",
		Scope:        skills.ScopePersona,
		AllowedTools: []string{"fs__read"},
	}}
	ctx := WithSkillCatalog(context.Background(), catalog)
	host := &skillActivateHost{}
	tool := &metaSkillLoad{}
	res, err := tool.Run(ctx, json.RawMessage(`{"name":"recon"}`), host)
	if err != nil || res.Error != "" {
		t.Fatalf("load: err=%v res=%s", err, res.Error)
	}
	if len(host.activated) != 1 || host.activated[0] != "fs__read" {
		t.Fatalf("activated: %v", host.activated)
	}
}
