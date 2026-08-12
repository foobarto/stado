package researchtool

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/foobarto/stado/pkg/tool"
)

const (
	MemoryName  = "memory__research"
	SessionName = "session__research"
)

type Bridge interface {
	Research(context.Context, string, string) (any, error)
}
type Tool struct{ Kind string }

func (t Tool) Name() string {
	if t.Kind == "session" {
		return SessionName
	}
	return MemoryName
}
func (t Tool) Description() string {
	if t.Kind == "session" {
		return "Delegate a slow, isolated search over authorized historical sessions; returns bounded cited synthesis."
	}
	return "Delegate an isolated high-quality search over active authorized memories; returns bounded cited synthesis."
}
func (t Tool) Schema() map[string]any {
	return map[string]any{"type": "object", "required": []string{"query"}, "properties": map[string]any{"query": map[string]any{"type": "string", "maxLength": 4096}}, "additionalProperties": false}
}
func (t Tool) Class() tool.Class { return tool.ClassNonMutating }
func (t Tool) Run(ctx context.Context, raw json.RawMessage, h tool.Host) (tool.Result, error) {
	var in struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	bridge, ok := h.(Bridge)
	if !ok {
		return tool.Result{Error: "research agent unavailable"}, errors.New("research agent unavailable")
	}
	result, err := bridge.Research(ctx, t.Kind, in.Query)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	b, err := json.Marshal(result)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	return tool.Result{Content: string(b)}, nil
}
