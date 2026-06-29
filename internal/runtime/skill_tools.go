package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/foobarto/stado/internal/skills"
	pkgtool "github.com/foobarto/stado/pkg/tool"
)

// SkillLoadResponse is the skills__load tool result before agentloop trims
// the body for user-message injection.
type SkillLoadResponse struct {
	Name         string   `json:"name"`
	Body         string   `json:"body"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	Loaded       bool     `json:"loaded"`
}

// ── skills__load ───────────────────────────────────────────────────────────

type metaSkillLoad struct{}

func (m *metaSkillLoad) Name() string { return "skills__load" }
func (m *metaSkillLoad) Description() string {
	return "Load a skill by name into the conversation. Use when a skill description matches the task. Returns the skill body for injection; call only for skills listed in the system prompt."
}
func (m *metaSkillLoad) Schema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"name"},
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Skill name from the available-skills listing.",
			},
		},
	}
}
func (m *metaSkillLoad) Run(ctx context.Context, args json.RawMessage, h pkgtool.Host) (pkgtool.Result, error) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return pkgtool.Result{Error: "invalid args: " + err.Error()}, nil
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return pkgtool.Result{Error: "skills__load: `name` required"}, nil
	}
	catalog, ok := SkillCatalogFrom(ctx)
	if !ok {
		return pkgtool.Result{Error: "skills__load: no skill catalog in this session"}, nil
	}
	sk := skills.Find(catalog, name)
	if sk == nil {
		return pkgtool.Result{Error: fmt.Sprintf("skills__load: skill %q not found", name)}, nil
	}
	if sk.DisableModelInvocation {
		return pkgtool.Result{Error: fmt.Sprintf("skills__load: skill %q is not model-invocable", name)}, nil
	}
	body := sk.RenderedBody()
	if body == "" {
		return pkgtool.Result{Error: fmt.Sprintf("skills__load: skill %q has an empty body", name)}, nil
	}
	var allowed []string
	if sk.AllowedToolsEffective() {
		allowed = append([]string(nil), sk.AllowedTools...)
		if act, ok := h.(pkgtool.ToolActivator); ok {
			for _, toolName := range allowed {
				act.ActivateTool(toolName)
			}
		}
	}
	resp := SkillLoadResponse{
		Name:         sk.Name,
		Body:         body,
		AllowedTools: allowed,
		Loaded:       true,
	}
	b, _ := json.Marshal(resp)
	return pkgtool.Result{Content: string(b)}, nil
}

// SkillLoadAllowedTools returns allowed_tools from a successful skills__load
// result. Used by agentloop to promote persona-scoped pre-approvals into the
// per-turn surface on headless/run/ACP paths (the TUI activates synchronously
// via ToolActivator during Run).
func SkillLoadAllowedTools(content string) []string {
	var resp SkillLoadResponse
	if err := json.Unmarshal([]byte(content), &resp); err != nil || !resp.Loaded {
		return nil
	}
	return resp.AllowedTools
}

// AbsorbSkillLoad inspects a skills__load result. When loaded, returns the
// body to inject as a user message and a shortened tool-result JSON.
func AbsorbSkillLoad(content string) (injectBody string, trimmed string) {
	var resp SkillLoadResponse
	if err := json.Unmarshal([]byte(content), &resp); err != nil || !resp.Loaded || resp.Body == "" {
		return "", content
	}
	injectBody = resp.Body
	short := map[string]any{
		"name":   resp.Name,
		"loaded": true,
	}
	if len(resp.AllowedTools) > 0 {
		short["allowed_tools_activated"] = resp.AllowedTools
	}
	b, _ := json.Marshal(short)
	return injectBody, string(b)
}
