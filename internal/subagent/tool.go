// Package subagent defines the first-class spawn_agent tool contract.
package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/foobarto/stado/pkg/tool"
)

const (
	ToolName     = "spawn_agent"
	DefaultRole  = "explorer"
	DefaultMode  = "read_only"
	DefaultTurns = 6
	MaxTurns     = 12
	// Timeout bounds wall-clock time for the child loop. It is separate
	// from MaxTurns because a single provider/tool call can still hang.
	DefaultTimeoutSeconds = 180
	MaxTimeoutSeconds     = 900
)

// Request is the JSON shape the parent model passes to spawn_agent.
//
// The first implementation intentionally supports only read_only
// children. The schema keeps Role/Ownership explicit so the same contract
// can grow to write-scoped workers later without changing the user-facing
// tool name.
type Request struct {
	Prompt    string `json:"prompt"`
	Role      string `json:"role,omitempty"`
	Mode      string `json:"mode,omitempty"`
	Ownership string `json:"ownership,omitempty"`
	MaxTurns  int    `json:"max_turns,omitempty"`
	// TimeoutSeconds is capped by MaxTimeoutSeconds. Zero means default,
	// not unlimited.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// Result is the structured payload returned to the parent model.
type Result struct {
	Status         string `json:"status"`
	Role           string `json:"role"`
	Mode           string `json:"mode"`
	ChildSession   string `json:"child_session"`
	Worktree       string `json:"worktree"`
	Text           string `json:"text,omitempty"`
	MessageCount   int    `json:"message_count,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	Error          string `json:"error,omitempty"`
}

// Spawner is the host-side capability the runtime/TUI/headless surfaces
// implement when they can create and run child sessions.
type Spawner interface {
	SpawnSubagent(ctx context.Context, req Request) (Result, error)
}

// Tool exposes spawn_agent to the provider.
type Tool struct{}

func (Tool) Name() string { return ToolName }

func (Tool) Description() string {
	return "Spawn a bounded read-only sidecar agent for parallel repo investigation. Returns the child session id and concise findings."
}

func (Tool) Schema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"prompt"},
		"properties": map[string]any{
			"prompt": map[string]any{
				"type":        "string",
				"description": "Self-contained task for the child agent. Include exact files, questions, and expected output.",
			},
			"role": map[string]any{
				"type":        "string",
				"description": "Child role. Only explorer is executable in this release.",
				"enum":        []string{"explorer"},
				"default":     DefaultRole,
			},
			"mode": map[string]any{
				"type":        "string",
				"description": "Execution mode. Only read_only is executable in this release.",
				"enum":        []string{DefaultMode},
				"default":     DefaultMode,
			},
			"ownership": map[string]any{
				"type":        "string",
				"description": "Optional file/module scope the child owns for investigation.",
			},
			"max_turns": map[string]any{
				"type":        "integer",
				"description": "Maximum child agent turns. Defaults to 6 and is capped at 12.",
				"minimum":     1,
				"maximum":     MaxTurns,
				"default":     DefaultTurns,
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"description": "Maximum wall-clock seconds for the child agent. Defaults to 180 and is capped at 900.",
				"minimum":     1,
				"maximum":     MaxTimeoutSeconds,
				"default":     DefaultTimeoutSeconds,
			},
		},
	}
}

func (Tool) Class() tool.Class { return tool.ClassNonMutating }

func (Tool) Run(ctx context.Context, raw json.RawMessage, h tool.Host) (tool.Result, error) {
	req, err := DecodeRequest(raw)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	spawner, ok := h.(Spawner)
	if !ok {
		err := errors.New("spawn_agent unavailable: current host does not support subagents")
		return tool.Result{Error: err.Error()}, err
	}
	res, err := spawner.SpawnSubagent(ctx, req)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	data, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	return tool.Result{Content: string(data)}, nil
}

// DecodeRequest validates and normalises a spawn_agent request.
func DecodeRequest(raw json.RawMessage) (Request, error) {
	var req Request
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &req); err != nil {
			return Request{}, fmt.Errorf("spawn_agent: decode args: %w", err)
		}
	}
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.Role = strings.TrimSpace(req.Role)
	req.Mode = strings.TrimSpace(req.Mode)
	req.Ownership = strings.TrimSpace(req.Ownership)
	if req.Prompt == "" {
		return Request{}, errors.New("spawn_agent: prompt is required")
	}
	if req.Role == "" {
		req.Role = DefaultRole
	}
	if req.Role != DefaultRole {
		return Request{}, fmt.Errorf("spawn_agent: role %q is not supported yet; use %q", req.Role, DefaultRole)
	}
	if req.Mode == "" {
		req.Mode = DefaultMode
	}
	if req.Mode != DefaultMode {
		return Request{}, fmt.Errorf("spawn_agent: mode %q is not supported yet; use %q", req.Mode, DefaultMode)
	}
	if req.MaxTurns <= 0 {
		req.MaxTurns = DefaultTurns
	}
	if req.MaxTurns > MaxTurns {
		req.MaxTurns = MaxTurns
	}
	if req.TimeoutSeconds <= 0 {
		req.TimeoutSeconds = DefaultTimeoutSeconds
	}
	if req.TimeoutSeconds > MaxTimeoutSeconds {
		req.TimeoutSeconds = MaxTimeoutSeconds
	}
	return req, nil
}
