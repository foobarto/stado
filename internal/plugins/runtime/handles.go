package runtime

import (
	"math/rand/v2"
	"sync"
)

// handleRegistry is a per-Runtime store for stateful opaque handle values.
// Handles are 32-bit random IDs with a type tag for safety checks (EP-0038 §G).
// Shared across plugin instances on the same Runtime — agents and sessions
// outlive plugin instance restarts (EP-0038 D13).
type handleRegistry struct {
	mu      sync.Mutex
	entries map[uint32]handleEntry
}

type handleEntry struct {
	typeTag string
	value   any
}

func newHandleRegistry() *handleRegistry {
	return &handleRegistry{entries: make(map[uint32]handleEntry)}
}

// alloc allocates a new handle. Retries on the rare 32-bit collision (EP-0038 D22).
func (r *handleRegistry) alloc(typeTag string, value any) uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		id := rand.Uint32()
		if id == 0 {
			continue // zero is the invalid/null handle
		}
		if _, exists := r.entries[id]; exists {
			continue // collision — re-roll
		}
		r.entries[id] = handleEntry{typeTag: typeTag, value: value}
		return id
	}
}

func (r *handleRegistry) get(handle uint32) (any, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[handle]
	if !ok {
		return nil, false
	}
	return e.value, true
}

func (r *handleRegistry) free(handle uint32) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.entries, handle)
}

func (r *handleRegistry) isType(handle uint32, typeTag string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[handle]
	return ok && e.typeTag == typeTag
}
