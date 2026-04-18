package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/foobarto/stado/pkg/tool"
)

type Registry struct {
	mu    sync.RWMutex
	tools map[string]tool.Tool
}

func NewRegistry() *Registry {
	return &Registry{
		tools: make(map[string]tool.Tool),
	}
}

func (r *Registry) Register(t tool.Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name()] = t
}

func (r *Registry) Get(name string) (tool.Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

func (r *Registry) All() []tool.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]tool.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out
}

func (r *Registry) Run(ctx context.Context, name string, args json.RawMessage, h tool.Host) (tool.Result, error) {
	t, ok := r.Get(name)
	if !ok {
		return tool.Result{Error: fmt.Sprintf("unknown tool: %s", name)}, fmt.Errorf("unknown tool: %s", name)
	}
	return t.Run(ctx, args, h)
}
