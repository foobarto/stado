// stado_instance_* — process-lifetime KV store for plugins that need
// state across tool calls (auth cookies, session tokens, accumulated
// data through a multi-step exploit chain). Per-Runtime store; per-
// plugin key namespacing prevents one plugin from reading another's
// keys. Capability gates: state:read[:<glob>] and state:write[:<glob>]
// (same shape as secrets:read/write).
//
// This is the in-memory analogue to stado_secrets_*. Use:
//   - state for intra-session ephemeral data (cookies, intermediate
//     tokens): cleared at process exit
//   - secrets for operator-managed durable values: persists across
//     restarts at <state-dir>/secrets/<name>
//
// Per-plugin store is bounded: 1 MB per value, 16 MB total per plugin.
// Beyond that, _set returns -1.

package runtime

import (
	"context"
	"path/filepath"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

const (
	stateMaxValueBytes = 1 << 20 // 1 MB per key
	stateMaxTotalBytes = 16 << 20 // 16 MB per plugin
)

// StateAccess records a plugin's manifest-declared state:read /
// state:write capability patterns. The Store itself is per-Runtime;
// the host caller wires it after NewHost returns, like SecretsAccess.
type StateAccess struct {
	Store      *InstanceStore
	ReadGlobs  []string
	WriteGlobs []string
	PluginName string
}

// CanRead returns true when key matches any of ReadGlobs (empty
// ReadGlobs = match-all).
func (s *StateAccess) CanRead(key string) bool {
	if s == nil {
		return false
	}
	if len(s.ReadGlobs) == 0 {
		return true
	}
	for _, g := range s.ReadGlobs {
		if matched, _ := filepath.Match(g, key); matched {
			return true
		}
	}
	return false
}

// CanWrite returns true when key matches any of WriteGlobs.
func (s *StateAccess) CanWrite(key string) bool {
	if s == nil {
		return false
	}
	if len(s.WriteGlobs) == 0 {
		return true
	}
	for _, g := range s.WriteGlobs {
		if matched, _ := filepath.Match(g, key); matched {
			return true
		}
	}
	return false
}

// InstanceStore is a per-Runtime in-memory KV store with per-plugin
// namespacing. Keys are scoped under the plugin name so plugin A
// can't read plugin B's keys. Bounded: stateMaxValueBytes per value,
// stateMaxTotalBytes per plugin.
type InstanceStore struct {
	mu      sync.Mutex
	entries map[string]map[string][]byte // pluginName → key → value
	totals  map[string]int                // pluginName → cumulative bytes
}

// NewInstanceStore returns an empty in-memory KV store.
func NewInstanceStore() *InstanceStore {
	return &InstanceStore{
		entries: map[string]map[string][]byte{},
		totals:  map[string]int{},
	}
}

// Get returns the value for plugin's key. ok=false when not found.
func (s *InstanceStore) Get(plugin, key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries[plugin] == nil {
		return nil, false
	}
	v, ok := s.entries[plugin][key]
	if !ok {
		return nil, false
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, true
}

// Set stores value under plugin's key. Refuses with an explanatory
// error when the value would exceed per-key or per-plugin caps.
func (s *InstanceStore) Set(plugin, key string, value []byte) error {
	if len(value) > stateMaxValueBytes {
		return &stateLimitError{kind: "value", limit: stateMaxValueBytes, got: len(value)}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries[plugin] == nil {
		s.entries[plugin] = map[string][]byte{}
	}
	prevSize := len(s.entries[plugin][key])
	delta := len(value) - prevSize
	if s.totals[plugin]+delta > stateMaxTotalBytes {
		return &stateLimitError{kind: "plugin-total", limit: stateMaxTotalBytes, got: s.totals[plugin] + delta}
	}
	cp := make([]byte, len(value))
	copy(cp, value)
	s.entries[plugin][key] = cp
	s.totals[plugin] += delta
	return nil
}

// Delete removes plugin's key. Idempotent.
func (s *InstanceStore) Delete(plugin, key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries[plugin] == nil {
		return
	}
	if v, ok := s.entries[plugin][key]; ok {
		s.totals[plugin] -= len(v)
		delete(s.entries[plugin], key)
	}
}

// List returns the sorted keys for plugin matching the prefix
// (empty prefix → all keys).
func (s *InstanceStore) List(plugin, prefix string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.entries[plugin] == nil {
		return nil
	}
	out := make([]string, 0, len(s.entries[plugin]))
	for k := range s.entries[plugin] {
		if prefix == "" || hasPrefixState(k, prefix) {
			out = append(out, k)
		}
	}
	// Sort for deterministic output.
	for i := 1; i < len(out); i++ {
		j := i
		for j > 0 && out[j-1] > out[j] {
			out[j-1], out[j] = out[j], out[j-1]
			j--
		}
	}
	return out
}

func hasPrefixState(s, prefix string) bool {
	if len(prefix) > len(s) {
		return false
	}
	return s[:len(prefix)] == prefix
}

type stateLimitError struct {
	kind  string
	limit int
	got   int
}

func (e *stateLimitError) Error() string {
	return "stado_instance: " + e.kind + " size " + itoa(e.got) + " exceeds limit " + itoa(e.limit)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// registerInstanceImports wires the four host imports.
func registerInstanceImports(builder wazero.HostModuleBuilder, host *Host) {
	builder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module, keyPtr, keyLen, outPtr, outMax int32) int32 {
			if host.State == nil || host.State.Store == nil {
				return -1
			}
			key, ok := readMemoryString(mod, uint32(keyPtr), uint32(keyLen))
			if !ok {
				return -1
			}
			if !host.State.CanRead(key) {
				return -1
			}
			v, ok := host.State.Store.Get(host.State.PluginName, key)
			if !ok {
				return -1
			}
			n := int32(len(v))
			if n > outMax {
				return n // tells caller to re-call with bigger buffer
			}
			if !mod.Memory().Write(uint32(outPtr), v) {
				return -1
			}
			return n
		}).
		Export("stado_instance_get")

	builder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module, keyPtr, keyLen, valPtr, valLen int32) int32 {
			if host.State == nil || host.State.Store == nil {
				return -1
			}
			key, ok := readMemoryString(mod, uint32(keyPtr), uint32(keyLen))
			if !ok {
				return -1
			}
			if !host.State.CanWrite(key) {
				return -1
			}
			val, ok := mod.Memory().Read(uint32(valPtr), uint32(valLen))
			if !ok {
				return -1
			}
			if err := host.State.Store.Set(host.State.PluginName, key, val); err != nil {
				return -1
			}
			return 0
		}).
		Export("stado_instance_set")

	builder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module, keyPtr, keyLen int32) int32 {
			if host.State == nil || host.State.Store == nil {
				return -1
			}
			key, ok := readMemoryString(mod, uint32(keyPtr), uint32(keyLen))
			if !ok {
				return -1
			}
			if !host.State.CanWrite(key) {
				return -1
			}
			host.State.Store.Delete(host.State.PluginName, key)
			return 0
		}).
		Export("stado_instance_delete")

	builder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module, prefixPtr, prefixLen, outPtr, outMax int32) int32 {
			if host.State == nil || host.State.Store == nil {
				return -1
			}
			// List requires broad-read (empty ReadGlobs OR a "*"
			// pattern). Per-key read globs alone don't grant list.
			if !canBroadStateRead(host.State) {
				return -1
			}
			prefix := ""
			if prefixLen > 0 {
				p, ok := readMemoryString(mod, uint32(prefixPtr), uint32(prefixLen))
				if !ok {
					return -1
				}
				prefix = p
			}
			keys := host.State.Store.List(host.State.PluginName, prefix)
			joined := joinNL(keys)
			n := int32(len(joined))
			if n > outMax {
				return n
			}
			if !mod.Memory().Write(uint32(outPtr), []byte(joined)) {
				return -1
			}
			return n
		}).
		Export("stado_instance_list")
}

func canBroadStateRead(s *StateAccess) bool {
	if s == nil {
		return false
	}
	if len(s.ReadGlobs) == 0 {
		return true
	}
	for _, g := range s.ReadGlobs {
		if g == "*" {
			return true
		}
	}
	return false
}

func joinNL(strs []string) string {
	var n int
	for _, s := range strs {
		n += len(s) + 1
	}
	if n == 0 {
		return ""
	}
	buf := make([]byte, 0, n)
	for i, s := range strs {
		if i > 0 {
			buf = append(buf, '\n')
		}
		buf = append(buf, s...)
	}
	return string(buf)
}

// readMemoryString reads len bytes from wasm memory at ptr and returns
// the result as a Go string. Used by the state imports + others.
// Returns ok=false on any out-of-bounds.
func readMemoryString(mod api.Module, ptr, ln uint32) (string, bool) {
	b, ok := mod.Memory().Read(ptr, ln)
	if !ok {
		return "", false
	}
	// Make a copy — wasm memory may be re-mapped.
	out := make([]byte, len(b))
	copy(out, b)
	return string(out), true
}
