package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/foobarto/stado/internal/toolinput"
	"github.com/foobarto/stado/pkg/tool"
)

type Registry struct {
	mu       sync.RWMutex
	tools    map[string]tool.Tool
	instance uint64
	revision uint64
}

var nextRegistryInstance atomic.Uint64

// RegistrySnapshot identifies one exact registry instance and mutation
// revision while carrying its stable name-sorted tool view. The opaque
// instance number is process-local and fences host-import pagination and
// surface edits; it is neither persisted nor exposed directly.
type RegistrySnapshot struct {
	Instance uint64
	Revision uint64
	Tools    []tool.Tool
}

func NewRegistry() *Registry {
	return &Registry{
		tools:    make(map[string]tool.Tool),
		instance: nextRegistryInstance.Add(1),
	}
}

func (r *Registry) Register(t tool.Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Name()] = t
	r.revision++
}

// Unregister removes a tool by name. Idempotent — missing name is a
// silent no-op. Used by config-driven filtering so
// BuildDefaultRegistry can stay a simple register-everything shape
// while callers trim per user config.
func (r *Registry) Unregister(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, present := r.tools[name]; present {
		delete(r.tools, name)
		r.revision++
	}
}

func (r *Registry) Get(name string) (tool.Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// All returns every registered tool sorted by Name. Stable ordering is
// load-bearing for prompt-cache stability — see DESIGN §"Prompt-cache
// awareness": any map-iteration source in the prompt-bytes path invalidates
// the cache on every turn. Callers that want a different ordering must
// re-sort, but the default MUST be Name-sorted.
func (r *Registry) All() []tool.Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.allLocked()
}

// Snapshot returns one exact, stable registry view. Instance distinguishes a
// rebuilt but textually identical registry; Revision distinguishes mutations
// of the same registry.
func (r *Registry) Snapshot() RegistrySnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return RegistrySnapshot{Instance: r.instance, Revision: r.revision, Tools: r.allLocked()}
}

// WithSnapshot runs fn while the registry read lock remains held. It is used
// only by bounded native enforcement primitives that must validate a complete
// projection and commit an external session-surface edit without a registry
// rebuild racing between those two steps. fn must not call back into Registry.
func (r *Registry) WithSnapshot(fn func(RegistrySnapshot) error) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return fn(RegistrySnapshot{Instance: r.instance, Revision: r.revision, Tools: r.allLocked()})
}

func (r *Registry) allLocked() []tool.Tool {
	out := make([]tool.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

func (r *Registry) Run(ctx context.Context, name string, args json.RawMessage, h tool.Host) (tool.Result, error) {
	if err := toolinput.CheckLen(len(args)); err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	t, ok := r.Get(name)
	if !ok {
		return tool.Result{Error: fmt.Sprintf("unknown tool: %s", name)}, fmt.Errorf("unknown tool: %s", name)
	}
	return t.Run(ctx, args, h)
}

// ClassOf returns the mutation class for a registered tool. Lookup order:
//  1. tool.Classifier interface (per-instance)
//  2. Classes static map (per-name, for bundled tools)
//  3. ClassExec default
func (r *Registry) ClassOf(name string) tool.Class {
	t, ok := r.Get(name)
	if ok {
		if c, ok := t.(tool.Classifier); ok {
			return c.Class()
		}
	}
	if c, ok := Classes[name]; ok {
		return c
	}
	return tool.ClassExec
}
