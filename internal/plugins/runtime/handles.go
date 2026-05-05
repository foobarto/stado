package runtime

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
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

// maxHandleAllocAttempts bounds the collision-retry loop. With a uint32
// keyspace this should never bite in normal operation; the bound exists
// so a broken random source or near-full registry surfaces as an error
// rather than a hang.
const maxHandleAllocAttempts = 1000

// alloc allocates a new handle. Retries on the rare 32-bit collision
// (EP-0038 D22), bounded at maxHandleAllocAttempts attempts. Returns
// (id, nil) on success; (0, err) when the bound is exhausted.
func (r *handleRegistry) alloc(typeTag string, value any) (uint32, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := 0; i < maxHandleAllocAttempts; i++ {
		id := rand.Uint32()
		if id == 0 {
			continue // zero is the invalid/null handle
		}
		if _, exists := r.entries[id]; exists {
			continue // collision — re-roll
		}
		r.entries[id] = handleEntry{typeTag: typeTag, value: value}
		return id, nil
	}
	return 0, fmt.Errorf("handleRegistry: alloc exhausted %d attempts (type=%s)", maxHandleAllocAttempts, typeTag)
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

// HandleType is the canonical type-tag string used in operator-facing
// handle IDs (FormatHandleID / ParseHandleID).  These are *not* the
// internal type tags stored in handleEntry; the internal tags can be
// anything the producer chooses, as long as they round-trip
// consistently. The strings here are the public, documented form
// (NOTES §13, EP-0038 §H).
type HandleType string

const (
	HandleTypeProc     HandleType = "proc"
	HandleTypeTerminal HandleType = "term"
	HandleTypeAgent    HandleType = "agent"
	HandleTypeSession  HandleType = "session"
	HandleTypePlugin   HandleType = "plugin"
	HandleTypeConn     HandleType = "conn"   // reserved — Tier 1 net (BACKLOG #11)
	HandleTypeListen   HandleType = "listen" // reserved — Tier 1 net (BACKLOG #11)
)

var knownHandleTypes = map[HandleType]bool{
	HandleTypeProc: true, HandleTypeTerminal: true, HandleTypeAgent: true,
	HandleTypeSession: true, HandleTypePlugin: true, HandleTypeConn: true,
	HandleTypeListen: true,
}

// ownedHandleTypes are the types whose ID payload is a uint32 hex
// allocated by handleRegistry.  For these, "<type>:<hex>" with no
// dot is still an owned ID with an empty plugin owner; for the
// other (free-standing) types, the payload is an opaque string.
var ownedHandleTypes = map[HandleType]bool{
	HandleTypeProc:     true,
	HandleTypeTerminal: true,
	HandleTypeConn:     true,
	HandleTypeListen:   true,
}

// FormatHandleID renders a typed handle ID for an *owned* handle —
// one allocated by handleRegistry on behalf of a named plugin
// instance.  Format: "<type>:<plugin>.<hex>" (e.g. "proc:fs.7a2b").
// When plugin is empty the dotted owner is omitted: "<type>:<hex>".
// hex is lower-case, no leading zero padding (matches Go's %x).
func FormatHandleID(typ HandleType, plugin string, h uint32) string {
	if plugin == "" {
		return fmt.Sprintf("%s:%x", typ, h)
	}
	return fmt.Sprintf("%s:%s.%x", typ, plugin, h)
}

// freeStandingIDDisplayChars is the maximum number of characters
// shown for free-standing opaque IDs (agent FleetID, session id,
// plugin name) in operator-facing handle strings.  Matches the
// existing min8 convention in /ps output.
const freeStandingIDDisplayChars = 8

// FormatFreeStandingHandleID renders a typed handle ID for IDs that
// don't live in handleRegistry — agents (FleetID), sessions
// (stadogit session id), plugin instances (plugin name).  The id is
// trimmed to freeStandingIDDisplayChars characters when longer
// (operator readability).
func FormatFreeStandingHandleID(typ HandleType, id string) string {
	if len(id) > freeStandingIDDisplayChars {
		id = id[:freeStandingIDDisplayChars]
	}
	return fmt.Sprintf("%s:%s", typ, id)
}

// ParseHandleID parses an operator-facing handle ID into its parts.
// Returns (type, owner-or-id, hex-handle, err).  The second return
// is overloaded by type:
//
//   - Owned types (proc/term/conn/listen) with a dotted owner
//     ("proc:fs.7a2b"): owner-or-id is the plugin name ("fs"),
//     hex-handle is the parsed uint32.
//   - Owned types without a dot ("proc:42"): owner-or-id is "",
//     hex-handle is the parsed uint32.
//   - Free-standing types (agent/session/plugin), e.g. "agent:bf3e":
//     owner-or-id is the opaque id payload ("bf3e"), hex-handle is 0.
//     The caller looks the id up in the appropriate registry.
//
// Rejects:
//   - bare numerics ("123") — must have a type prefix.
//   - unknown type prefixes.
//   - empty payload after the type prefix ("agent:", "proc:").
//   - hex segments that don't fit in uint32 (owned types only).
func ParseHandleID(s string) (HandleType, string, uint32, error) {
	colon := strings.IndexByte(s, ':')
	if colon < 0 {
		return "", "", 0, fmt.Errorf("handle ID %q: missing type prefix (expected e.g. proc:fs.7a2b or agent:bf3e)", s)
	}
	typ := HandleType(s[:colon])
	rest := s[colon+1:]
	if !knownHandleTypes[typ] {
		return "", "", 0, fmt.Errorf("handle ID %q: unknown type %q", s, typ)
	}
	if rest == "" {
		return "", "", 0, fmt.Errorf("handle ID %q: empty payload after type prefix", s)
	}
	// Owned form: "<plugin>.<hex>".
	if dot := strings.IndexByte(rest, '.'); dot >= 0 {
		plugin := rest[:dot]
		hexStr := rest[dot+1:]
		v, err := strconv.ParseUint(hexStr, 16, 32)
		if err != nil {
			return "", "", 0, fmt.Errorf("handle ID %q: hex segment %q invalid: %w", s, hexStr, err)
		}
		return typ, plugin, uint32(v), nil
	}
	// No dot. For owned-style types (proc/term/conn/listen), the
	// rest is a bare hex value with empty plugin owner — this is
	// what FormatHandleID emits when called with an empty plugin.
	// For free-standing types (agent/session/plugin), the rest is
	// an opaque id string returned in the second slot.
	if ownedHandleTypes[typ] {
		v, err := strconv.ParseUint(rest, 16, 32)
		if err != nil {
			return "", "", 0, fmt.Errorf("handle ID %q: hex segment %q invalid: %w", s, rest, err)
		}
		return typ, "", uint32(v), nil
	}
	return typ, rest, 0, nil
}
