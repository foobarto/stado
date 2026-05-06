// Package llmtool exposes stado's LLM as an MCP-callable tool.
// Registered by `stado mcp-server` so external clients (Claude
// Desktop, Zed, custom integrations) can invoke stado's configured
// provider with persona selection.
//
// Not registered in TUI / `stado run` paths — there the model is the
// LLM consumer, not the caller. Plugins that need persona-selecting
// LLM calls inside agent loops use the stado_llm_invoke host import.
package llmtool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/foobarto/stado/internal/personas"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/foobarto/stado/pkg/tool"
)

// Tool implements the MCP-exposed llm.invoke surface.
type Tool struct {
	// Provider builds a fresh provider for each call. Lazy because
	// the active provider may depend on operator config we want
	// re-read across long-lived MCP server lifetimes.
	Provider func() (agent.Provider, error)
	// DefaultModel is used when args.Model is empty.
	DefaultModel string
	// DefaultPersona is the operator-pinned persona at server start
	// (`stado mcp-server --persona <name>` or [defaults].persona).
	// Empty = bundled "default". Per-call args.persona overrides.
	DefaultPersona string
	// CWD + ConfigDir power the persona resolver.
	CWD       string
	ConfigDir string
}

func (Tool) Name() string        { return "llm__invoke" }
func (Tool) Description() string {
	return "Run a single LLM completion against stado's configured provider. " +
		"Optionally select a persona (operating manual / system prompt) and override the model. " +
		"Returns the assistant text reply."
}
func (Tool) Schema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"prompt"},
		"properties": map[string]any{
			"prompt":      map[string]any{"type": "string"},
			"persona":     map[string]any{"type": "string", "description": "Persona name; empty inherits server default. See `stado plugin list --personas` for available personas."},
			"model":       map[string]any{"type": "string", "description": "Model override; empty uses the server default."},
			"system":      map[string]any{"type": "string", "description": "Extra system content appended after the persona body."},
			"max_tokens":  map[string]any{"type": "integer"},
			"temperature": map[string]any{"type": "number"},
		},
	}
}

func (Tool) Class() tool.Class { return tool.ClassExec }

type args struct {
	Prompt      string  `json:"prompt"`
	Persona     string  `json:"persona,omitempty"`
	Model       string  `json:"model,omitempty"`
	System      string  `json:"system,omitempty"`
	MaxTokens   int     `json:"max_tokens,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
}

func (t Tool) Run(ctx context.Context, raw json.RawMessage, _ tool.Host) (tool.Result, error) {
	var a args
	if err := json.Unmarshal(raw, &a); err != nil || a.Prompt == "" {
		return tool.Result{Error: "prompt is required"}, fmt.Errorf("llm_invoke: invalid args")
	}
	if t.Provider == nil {
		return tool.Result{Error: "provider not wired"}, fmt.Errorf("llm_invoke: provider not configured on this server")
	}
	prov, err := t.Provider()
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}

	// Persona resolution: per-call → server default → bundled "default".
	personaName := a.Persona
	if personaName == "" {
		personaName = t.DefaultPersona
	}
	if personaName == "" {
		personaName = "default"
	}
	resolver := personas.Resolver{CWD: t.CWD, ConfigDir: t.ConfigDir}
	persona, perr := resolver.Load(personaName)
	if perr != nil {
		return tool.Result{Error: "persona " + personaName + ": " + perr.Error()}, perr
	}

	model := a.Model
	if model == "" {
		model = t.DefaultModel
	}

	system := personas.AssembleSystem(persona, "", "", a.System)
	req := agent.TurnRequest{
		Model:    model,
		System:   system,
		Messages: []agent.Message{agent.Text(agent.RoleUser, a.Prompt)},
	}
	if a.MaxTokens > 0 {
		req.MaxTokens = a.MaxTokens
	}
	if a.Temperature > 0 {
		v := a.Temperature
		req.Temperature = &v
	}

	ch, err := prov.StreamTurn(ctx, req)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	var b strings.Builder
	for ev := range ch {
		if ev.Kind == agent.EvTextDelta {
			b.WriteString(ev.Text)
		}
	}
	return tool.Result{Content: b.String()}, nil
}
