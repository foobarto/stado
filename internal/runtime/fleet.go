package runtime

// Background-agent fleet — async wrapper around the synchronous
// SubagentRunner.SpawnSubagent primitive. Each entry tracks one
// goroutine running a child stadogit session; the TUI's /spawn
// slash command pushes here, the /fleet modal reads from here.
//
// See docs/eps/0034-background-agents-fleet.md for design rationale,
// status state machine, and what's deferred to phase B.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/foobarto/stado/internal/subagent"
)

// FleetStatus enumerates background-agent lifecycle states. A v1
// entry transitions running → {completed, cancelled, error} once
// and stays terminal.
type FleetStatus string

const (
	FleetStatusRunning   FleetStatus = "running"
	FleetStatusCompleted FleetStatus = "completed"
	FleetStatusCancelled FleetStatus = "cancelled"
	FleetStatusError     FleetStatus = "error"
)

// FleetEntry is one background agent's metadata + lifecycle. Read
// access via Fleet.List returns a copy under the mutex; the TUI
// must NEVER hold a pointer to a registry-internal entry across
// frames.
type FleetEntry struct {
	// FleetID is the registry's canonical id, assigned at Spawn
	// time (UUID). Stable for the entry's lifetime.
	FleetID string
	// ParentSessionID is the host-bound session that spawned this entry. It is
	// retained separately from the child SessionID so evidence consumers can
	// select one supervision tree from a process-wide fleet registry.
	ParentSessionID string
	// SessionID is the child's stadogit session id, populated once
	// SpawnSubagent has created the session. Empty until then.
	SessionID string
	// Prompt is the initial user prompt that started this agent.
	// Truncated for display; full text lives in the child's
	// transcript.
	Prompt string
	// Provider / Model the child was launched with. Display only.
	Provider string
	Model    string
	// StartedAt is when Spawn was called.
	StartedAt time.Time
	// EndedAt is when the goroutine returned (running entries: zero).
	EndedAt time.Time
	// Status is the current lifecycle state. Terminal states
	// (completed / cancelled / error) are append-only — once set, no
	// further transitions.
	Status FleetStatus
	// LastActivity bumps on every progress hook fire.
	LastActivity time.Time
	// LastTool is the most recent tool name observed via progress.
	// Empty when the child hasn't issued a tool call yet.
	LastTool string
	// LastText is the trailing slice of recent assistant text.
	// Bounded — see lastTextMaxBytes.
	LastText string
	// Result is the child's final assistant text on completion.
	Result string
	// Error is populated when Status == error.
	Error string
	// Terminal is host-collected token and cleanup metadata. It is independent
	// of Result so a later cleanup diagnostic cannot discard valid final text.
	Terminal subagent.TerminalMetadata
}

// SpawnOptions are caller-supplied overrides at /spawn time. Empty
// fields fall back to caller-supplied defaults from the active
// session (parent provider/model/agent).
type SpawnOptions struct {
	Provider             string
	Model                string
	Thinking             string
	ThinkingBudgetTokens int
	ReasoningEffort      string
	// ParentSessionID is host-owned provenance, not a child-selectable option.
	ParentSessionID string
	// Role / Mode / Turns / TimeoutSeconds map to subagent.Request
	// fields. Zero means "use subagent.Request defaults" (DefaultRole,
	// DefaultMode, DefaultTurns, DefaultTimeoutSeconds).
	Role           string
	Mode           string
	MaxTurns       int
	TimeoutSeconds int
	// Persona names the operating manual for the child agent. Empty
	// = inherit the parent's persona. EP-0038i.
	Persona     string
	Ownership   string
	WriteScope  []string
	Source      *subagent.Source
	ToolProfile string
	NarrowTools []string
	TokenBudget int
	Execution   string
	// ChildToolOwner is the authenticated caller package namespace injected by
	// the WASM Host. Direct native/operator spawns leave it empty.
	ChildToolOwner string
}

// Fleet is the in-memory registry of background agents.
type Fleet struct {
	mu      sync.Mutex
	entries map[string]*FleetEntry // keyed by FleetID
	// spawnClaims make one logical application spawn replayable across a WASM
	// module rebind while this host process and Fleet remain live. They are
	// deliberately process-local: after process death every in-process child is
	// terminal too, so a durable broker record must not pretend it is still live.
	spawnClaims map[string]*fleetSpawnClaim
	// cancels parallels entries — kept separate so List can return
	// pure-data copies of entries without leaking goroutine handles.
	cancels map[string]context.CancelFunc
	// inboxes parallels entries with a slice of pending operator-/
	// peer-injected messages per agent. The agent's loop drains its
	// inbox at turn boundaries and prepends queued messages as
	// user-role inputs in the next turn. Bounded — see
	// fleetInboxMaxMessages.
	inboxes map[string][]string
}

type FleetSpawnAdmission struct {
	ID        string
	SessionID string
	Status    string
}

type fleetSpawnClaim struct {
	digest string
	done   chan struct{}
	result FleetSpawnAdmission
	err    error
}

// fleetInboxMaxMessages caps an agent's pending-inbox depth so a
// runaway producer can't unbounded-grow stado's heap. New messages
// past the cap are dropped silently — the producer can re-send.
const fleetInboxMaxMessages = 64

// NewFleet returns an empty fleet.
func NewFleet() *Fleet {
	return &Fleet{
		entries:     map[string]*FleetEntry{},
		spawnClaims: map[string]*fleetSpawnClaim{},
		cancels:     map[string]context.CancelFunc{},
		inboxes:     map[string][]string{},
	}
}

// ClaimSpawn serializes admission of one logical child under a native-derived
// application scope. The first successful admission is retained for the life
// of the Fleet; same-digest replay returns it, while key reuse with different
// input fails closed. A failed pre-admission attempt is removed so a later
// retry may make progress.
func (f *Fleet) ClaimSpawn(ctx context.Context, scope, key, digest string, admit func() (FleetSpawnAdmission, error)) (FleetSpawnAdmission, error) {
	if key == "" {
		return admit()
	}
	if scope == "" || digest == "" {
		return FleetSpawnAdmission{}, errors.New("fleet: idempotent spawn requires authenticated scope and request digest")
	}
	claimKey := scope + "\x00" + key

	f.mu.Lock()
	if f.spawnClaims == nil {
		f.spawnClaims = map[string]*fleetSpawnClaim{}
	}
	if prior, ok := f.spawnClaims[claimKey]; ok {
		if prior.digest != digest {
			f.mu.Unlock()
			return FleetSpawnAdmission{}, errors.New("fleet: idempotency key reused with a different spawn request")
		}
		done := prior.done
		f.mu.Unlock()
		select {
		case <-ctx.Done():
			return FleetSpawnAdmission{}, ctx.Err()
		case <-done:
			return prior.result, prior.err
		}
	}
	claim := &fleetSpawnClaim{digest: digest, done: make(chan struct{})}
	f.spawnClaims[claimKey] = claim
	f.mu.Unlock()

	result, err := admit()
	f.mu.Lock()
	claim.result, claim.err = result, err
	if err != nil && f.spawnClaims[claimKey] == claim {
		delete(f.spawnClaims, claimKey)
	}
	close(claim.done)
	f.mu.Unlock()
	return result, err
}

// SendMessage queues a message for delivery to the named agent's next
// turn. Returns an error when the agent doesn't exist; silently drops
// messages past fleetInboxMaxMessages. Empty / whitespace-only bodies
// are rejected to keep noise out of the agent's transcript.
func (f *Fleet) SendMessage(id, body string) error {
	body = strings.TrimRight(body, "\r\n")
	if strings.TrimSpace(body) == "" {
		return errors.New("fleet: message body is empty")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.entries[id]; !ok {
		return fmt.Errorf("fleet: agent %q not found", id)
	}
	q := f.inboxes[id]
	if len(q) >= fleetInboxMaxMessages {
		return nil // drop silently per the cap; producer can retry
	}
	f.inboxes[id] = append(q, body)
	return nil
}

// DrainInbox returns and clears the queued messages for the named
// agent. Returns nil when the agent doesn't exist or has no
// pending messages. The agent's loop calls this at turn boundaries.
func (f *Fleet) DrainInbox(id string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := f.inboxes[id]
	if len(q) == 0 {
		return nil
	}
	delete(f.inboxes, id)
	return q
}

// Spawner is the minimal interface Fleet needs from the runtime —
// matches subagent.Spawner exactly so existing implementations
// (SubagentRunner, hostAdapter, fakes) plug in unchanged.
type Spawner interface {
	SpawnSubagent(ctx context.Context, req subagent.Request) (subagent.Result, error)
}

// SnapshotSpawner freezes any mutable parent-session source before Fleet
// returns control to its caller. Without this seam an asynchronous child can
// begin after the parent has already advanced, so a reviewer requested at one
// application anchor may silently inspect a later tree. The pinned Spawner is
// still used in the goroutine; only source selection happens synchronously.
type SnapshotSpawner interface {
	PinSpawnSource(context.Context) (Spawner, error)
}

// RequestSourceSpawner synchronously resolves the caller's requested source
// before an asynchronous fleet launch returns. This is stronger than
// SnapshotSpawner: an application can pass an authenticated immutable turn_ref
// and know the child will inspect that exact tree even if the worker advances
// before the launch goroutine runs.
type RequestSourceSpawner interface {
	PinSpawnRequestSource(context.Context, *subagent.Source) (Spawner, error)
}

// SpawnForkPoint is the runtime-neutral immutable source anchor admitted for
// retained execution. It deliberately mirrors only source identity and
// evidence coordinates; it carries no guest authority and does not import the
// broker's retained-execution package into the ordinary fleet primitive.
type SpawnForkPoint struct {
	SourceSessionID    string
	SourceGeneration   uint64
	CommittedTurn      int
	ConversationDigest string
	CompactionLineage  string
	TreeCommit         string
	TraceCommit        string
	EventSequence      uint64
}

// ForkPointSpawner consumes an already host-resolved immutable fork point
// synchronously. Retained execution requires this stronger contract: the
// returned Spawner must keep using this exact anchor across delayed launch and
// restart, rather than resolving a mutable selector in its goroutine.
type ForkPointSpawner interface {
	PinSpawnForkPoint(context.Context, SpawnForkPoint) (Spawner, error)
}

// InboxAwareSpawner is the optional extension a Spawner implements
// when it can deliver mid-loop operator/peer messages to the child.
// Fleet.runGoroutine type-asserts to wire AgentSendMessage delivery.
type InboxAwareSpawner interface {
	Spawner
	WithInbox(fn func() []string) Spawner
}

// Spawn starts a new background agent. Returns immediately with the
// FleetID; the goroutine drives the child to completion / error /
// cancellation in the background. The supplied Spawner is the
// runtime-side implementation that does the actual fork + agent
// loop — typically SubagentRunner, but Fleet doesn't depend on the
// concrete type so tests can substitute a fake.
//
// rootCtx scopes the entry's lifetime: when rootCtx is cancelled
// (e.g. stado is exiting), all running entries are cancelled
// cooperatively.
func (f *Fleet) Spawn(rootCtx context.Context, spawner Spawner, prompt string, opts SpawnOptions) (string, error) {
	if strings.TrimSpace(prompt) == "" {
		return "", errors.New("fleet: prompt is required")
	}
	if spawner == nil {
		return "", errors.New("fleet: spawner is nil")
	}
	if sourcePinner, ok := spawner.(RequestSourceSpawner); ok {
		pinned, err := sourcePinner.PinSpawnRequestSource(rootCtx, opts.Source)
		if err != nil {
			return "", fmt.Errorf("fleet: pin requested source: %w", err)
		}
		if pinned == nil {
			return "", errors.New("fleet: pin requested source returned nil spawner")
		}
		spawner = pinned
	} else if opts.Source != nil {
		return "", errors.New("fleet: requested source requires a source-pinning spawner")
	} else if snapshotter, ok := spawner.(SnapshotSpawner); ok {
		pinned, err := snapshotter.PinSpawnSource(rootCtx)
		if err != nil {
			return "", fmt.Errorf("fleet: pin spawn source: %w", err)
		}
		if pinned == nil {
			return "", errors.New("fleet: pin spawn source returned nil spawner")
		}
		spawner = pinned
	}

	id := uuid.NewString()
	now := time.Now()
	entry := &FleetEntry{
		FleetID:         id,
		ParentSessionID: opts.ParentSessionID,
		Prompt:          prompt,
		Provider:        opts.Provider,
		Model:           opts.Model,
		StartedAt:       now,
		Status:          FleetStatusRunning,
		LastActivity:    now,
	}

	ctx, cancel := context.WithCancel(rootCtx)

	f.mu.Lock()
	f.entries[id] = entry
	f.cancels[id] = cancel
	f.mu.Unlock()

	go f.runGoroutine(ctx, id, spawner, opts)
	return id, nil
}

// runGoroutine drives one entry to completion. Wraps the synchronous
// SpawnSubagent and translates its return value / error into the
// entry's terminal Status under the mutex.
func (f *Fleet) runGoroutine(ctx context.Context, id string, spawner Spawner, opts SpawnOptions) {
	defer func() {
		f.mu.Lock()
		delete(f.cancels, id)
		f.mu.Unlock()
	}()

	req := subagent.Request{
		AgentID:              id,
		Prompt:               f.entryPrompt(id),
		Role:                 opts.Role,
		Mode:                 opts.Mode,
		MaxTurns:             opts.MaxTurns,
		TimeoutSeconds:       opts.TimeoutSeconds,
		Persona:              opts.Persona,
		Ownership:            opts.Ownership,
		WriteScope:           append([]string(nil), opts.WriteScope...),
		Source:               opts.Source,
		Provider:             opts.Provider,
		Model:                opts.Model,
		Thinking:             opts.Thinking,
		ThinkingBudgetTokens: opts.ThinkingBudgetTokens,
		ReasoningEffort:      opts.ReasoningEffort,
		ToolProfile:          opts.ToolProfile,
		NarrowTools:          append([]string(nil), opts.NarrowTools...),
		TokenBudget:          opts.TokenBudget,
		Execution:            opts.Execution,
		ChildToolOwner:       opts.ChildToolOwner,
	}

	// Wire the inbox source so AgentSendMessage delivery actually
	// reaches this child's loop. Spawners that don't support it
	// (no WithInbox method) fall back to the original spawner;
	// FleetBridge.AgentSendMessage still queues but messages won't
	// be drained until the spawner gains support.
	if iaSpawner, ok := spawner.(InboxAwareSpawner); ok {
		spawner = iaSpawner.WithInbox(func() []string { return f.DrainInbox(id) })
	}

	res, err := spawner.SpawnSubagent(ctx, req)

	f.mu.Lock()
	defer f.mu.Unlock()
	entry, ok := f.entries[id]
	if !ok {
		return
	}
	entry.EndedAt = time.Now()
	if res.ChildSession != "" {
		entry.SessionID = res.ChildSession
	}
	entry.Terminal = res.Terminal
	switch {
	case err != nil && errors.Is(ctx.Err(), context.Canceled):
		entry.Status = FleetStatusCancelled
		entry.Error = err.Error()
	case err != nil:
		entry.Status = FleetStatusError
		entry.Error = err.Error()
	default:
		entry.Status = FleetStatusCompleted
		entry.Result = res.Text
	}
}

func (f *Fleet) entryPrompt(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.entries[id]
	if !ok {
		return ""
	}
	return e.Prompt
}

// Cancel terminates a running entry. Idempotent — calling Cancel on
// a terminal entry is a no-op.
func (f *Fleet) Cancel(id string) error {
	f.mu.Lock()
	cancel, ok := f.cancels[id]
	if ok {
		delete(f.cancels, id)
	}
	f.mu.Unlock()
	if !ok {
		return nil
	}
	cancel()
	return nil
}

// Get returns a copy of one entry, or (zero, false) if not found.
func (f *Fleet) Get(id string) (FleetEntry, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.entries[id]
	if !ok {
		return FleetEntry{}, false
	}
	return *e, true
}

// List returns a copy of all entries sorted by status (running first)
// then by StartedAt descending (newest first within each status group).
// Safe to call from the TUI Update loop — never returns pointers
// into the registry.
func (f *Fleet) List() []FleetEntry {
	f.mu.Lock()
	out := make([]FleetEntry, 0, len(f.entries))
	for _, e := range f.entries {
		out = append(out, *e)
	}
	f.mu.Unlock()
	sortFleetEntries(out)
	return out
}

// Remove drops a terminal entry from the registry. Returns false
// when the entry isn't found OR is still running (cancellation
// must complete before removal). Used by the fleet modal to clear
// finished agents the user no longer wants visible.
func (f *Fleet) Remove(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.entries[id]
	if !ok {
		return false
	}
	if e.Status == FleetStatusRunning {
		return false
	}
	delete(f.entries, id)
	return true
}

// UpdateProgress is the hook callers wire into their event stream
// to bump LastActivity / LastTool / LastText on a running entry.
// Bounded by lastTextMaxBytes; reentrant; no-op on terminal entries.
//
// `id` here is the FleetID, NOT the stadogit session id. Use
// FindByChildSession to map session id → FleetID when wiring from
// SubagentRunner.OnEvent (which only knows the child id).
func (f *Fleet) UpdateProgress(id, tool, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.entries[id]
	if !ok || e.Status != FleetStatusRunning {
		return
	}
	e.LastActivity = time.Now()
	if tool != "" {
		e.LastTool = tool
	}
	if text != "" {
		e.LastText = truncateLastText(text)
	}
}

// FindByChildSession scans entries for a matching SessionID and
// returns the FleetID when found. Used by progress hooks that only
// see the child session id.
func (f *Fleet) FindByChildSession(sessionID string) (string, bool) {
	if strings.TrimSpace(sessionID) == "" {
		return "", false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, e := range f.entries {
		if e.SessionID == sessionID {
			return id, true
		}
	}
	return "", false
}

// SetSessionID populates an entry's child session id once SubagentRunner
// has created the session. Called from the SubagentRunner.OnEvent
// hook on the "session_created" phase.
func (f *Fleet) SetSessionID(fleetID, sessionID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.entries[fleetID]
	if !ok {
		return
	}
	if e.SessionID == "" {
		e.SessionID = sessionID
	}
}

// CancelAll cancels every running entry. Used during stado clean
// exit so child goroutines unwind cooperatively.
func (f *Fleet) CancelAll() {
	f.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(f.cancels))
	for _, c := range f.cancels {
		cancels = append(cancels, c)
	}
	f.cancels = map[string]context.CancelFunc{}
	f.mu.Unlock()
	for _, c := range cancels {
		c()
	}
}

// sortFleetEntries orders running entries first (alphabetical
// stability not guaranteed within status groups beyond StartedAt).
// Mutates in place.
func sortFleetEntries(es []FleetEntry) {
	statusRank := map[FleetStatus]int{
		FleetStatusRunning:   0,
		FleetStatusCancelled: 1,
		FleetStatusError:     2,
		FleetStatusCompleted: 3,
	}
	sort.Slice(es, func(i, j int) bool {
		ra, rb := statusRank[es[i].Status], statusRank[es[j].Status]
		if ra != rb {
			return ra < rb
		}
		return es[i].StartedAt.After(es[j].StartedAt)
	})
}

const lastTextMaxBytes = 200

func truncateLastText(s string) string {
	if len(s) <= lastTextMaxBytes {
		return s
	}
	return "…" + s[len(s)-(lastTextMaxBytes-1):]
}

// FleetSummary is a condensed display string for one entry — used by
// the picker's main row, single-line.
func (e FleetEntry) Summary() string {
	short := e.FleetID
	if len(short) >= 8 {
		short = short[:8]
	}
	statusPad := fmt.Sprintf("%-9s", e.Status)
	prompt := truncatePrompt(e.Prompt, 40)
	last := e.LastTool
	if last == "" {
		last = "-"
	}
	return fmt.Sprintf("%s %s %s · last: %s", short, statusPad, prompt, last)
}

func truncatePrompt(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
