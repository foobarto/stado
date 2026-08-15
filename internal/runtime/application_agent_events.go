package runtime

// Generic broker-stamped terminal child facts (EP-0064, cleanup ledger C25).
//
// This projection deliberately says only what the host observed. Whether a
// completed, failed, cancelled, timed-out, or scope-violating child should make
// a worker continue, pause, pivot, or stop is lifecycle-application policy.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/foobarto/stado/internal/subagent"
)

const (
	AgentDownEvent    = "agent.down"
	AgentDownSchemaV1 = "stado.dev/agent-down-facts/v1"

	maxAgentFactIDBytes   = 256
	maxAgentFactTextBytes = 256
	maxAgentFactPaths     = 64
	// Three independent path sets can appear in one event. 128 bytes keeps the
	// worst-case JSON projection below the broker's 64 KiB event-data ceiling
	// even when every retained byte needs JSON escaping.
	maxAgentFactPathBytes    = 128
	maxAgentFactViolations   = 32
	maxAgentFactEvidenceRefs = 8
)

// AgentDownFactsV1 is the bounded guest-visible payload. Parent identity and
// generation are intentionally absent: the broker-authored event envelope is
// their sole authority. Error text, local worktree paths, prompts, controller
// capabilities, and adoption commands are also deliberately excluded.
type AgentDownFactsV1 struct {
	Schema   string                  `json:"schema"`
	Child    AgentTerminalFactsV1    `json:"child"`
	Budget   AgentBudgetFactsV1      `json:"budget"`
	Terminal AgentTerminalMetadataV1 `json:"terminal"`
	Scope    AgentScopeFactsV1       `json:"scope"`
	Changes  *AgentChangeFactsV1     `json:"changes,omitempty"`
	Failure  *AgentFailureFactsV1    `json:"failure,omitempty"`
}

type AgentTerminalFactsV1 struct {
	AgentID   string `json:"agent_id,omitempty"`
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	Role      string `json:"role,omitempty"`
	Mode      string `json:"mode,omitempty"`
	Execution string `json:"execution,omitempty"`
}

// AgentBudgetFactsV1 fields are admitted limits, not measured terminal usage. Host-measured
// usage belongs to the separate generic terminal-metadata work in ledger C29.
type AgentBudgetFactsV1 struct {
	TokenLimit     int `json:"token_limit,omitempty"`
	TurnLimit      int `json:"turn_limit,omitempty"`
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// AgentTerminalMetadataV1 mirrors the token-only host facts returned by agent
// polling. Currency estimates and raw cleanup/provider diagnostics are not
// application-policy inputs and never enter this projection.
type AgentTerminalMetadataV1 struct {
	Usage         AgentTokenUsageV1   `json:"usage"`
	UsageComplete bool                `json:"usage_complete"`
	Cleanup       *AgentCleanupFactV1 `json:"cleanup,omitempty"`
}

type AgentTokenUsageV1 struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int `json:"cache_write_tokens,omitempty"`
}

type AgentCleanupFactV1 struct {
	Kind        string `json:"kind"`
	Fingerprint string `json:"fingerprint"`
}

type AgentScopeFactsV1 struct {
	Ownership           string   `json:"ownership,omitempty"`
	WritePaths          []string `json:"write_paths,omitempty"`
	WritePathsDigest    string   `json:"write_paths_digest,omitempty"`
	WritePathsTruncated bool     `json:"write_paths_truncated,omitempty"`
	Violations          []string `json:"violations,omitempty"`
	ViolationsDigest    string   `json:"violations_digest,omitempty"`
	ViolationsTruncated bool     `json:"violations_truncated,omitempty"`
}

type AgentChangeFactsV1 struct {
	ForkTreeDigest        string   `json:"fork_tree_digest,omitempty"`
	ChangedPaths          []string `json:"changed_paths,omitempty"`
	ChangedPathsDigest    string   `json:"changed_paths_digest,omitempty"`
	ChangedPathsTruncated bool     `json:"changed_paths_truncated,omitempty"`
}

type AgentFailureFactsV1 struct {
	Fingerprint string `json:"fingerprint"`
}

// AgentDownObserver captures the authenticated publisher generation when a
// child starts. A later terminal callback therefore cannot cross a session
// rebind or generation change. started is observation-only; only terminal facts
// enter the durable application stream.
type AgentDownObserver struct {
	parentSessionID string
	publisher       ApplicationEventPublisher
	contextProvider ApplicationEventContextProvider

	mu       sync.Mutex
	children map[string]*agentDownChildAnchor
}

type agentDownChildAnchor struct {
	scope   ApplicationEventContext
	release func()
}

func NewAgentDownObserver(parentSessionID string, publisher ApplicationEventPublisher) (*AgentDownObserver, error) {
	parentSessionID = strings.TrimSpace(parentSessionID)
	if parentSessionID == "" {
		return nil, errors.New("runtime: agent.down parent session is required")
	}
	if publisher == nil {
		return nil, errors.New("runtime: agent.down publisher is required")
	}
	provider, ok := publisher.(ApplicationEventContextProvider)
	if !ok {
		return nil, errors.New("runtime: agent.down publisher has no authenticated context")
	}
	return &AgentDownObserver{
		parentSessionID: parentSessionID,
		publisher:       publisher,
		contextProvider: provider,
		children:        make(map[string]*agentDownChildAnchor),
	}, nil
}

// ApplicationEventLeasedSpawner decorates asynchronous Fleet launches with a
// synchronous publisher-context lease. Fleet invokes one of the pinning seams
// before it starts its goroutine, which closes the small race where a logical
// session could switch after accepting agent.start but before the child emits
// its started event. The launch lease hands off naturally to AgentDownObserver's
// per-child lease and is released when SpawnSubagent returns.
//
// A publisher with no admitted application generation is a transparent no-op;
// ordinary agent spawning must not depend on a lifecycle application existing.
func ApplicationEventLeasedSpawner(spawner Spawner, publisher ApplicationEventPublisher) Spawner {
	if spawner == nil || publisher == nil {
		return spawner
	}
	if _, ok := publisher.(ApplicationEventContextLeaser); !ok {
		return spawner
	}
	if _, ok := publisher.(ApplicationEventContextProvider); !ok {
		return spawner
	}
	return &applicationEventLeasedSpawner{spawner: spawner, publisher: publisher}
}

type applicationEventLeasedSpawner struct {
	spawner   Spawner
	publisher ApplicationEventPublisher
	release   func()
}

func (s *applicationEventLeasedSpawner) SpawnSubagent(ctx context.Context, req subagent.Request) (subagent.Result, error) {
	if s == nil || s.spawner == nil {
		return subagent.Result{}, errors.New("runtime: application-event spawner unavailable")
	}
	if s.release != nil {
		defer s.release()
	}
	return s.spawner.SpawnSubagent(ctx, req)
}

func (s *applicationEventLeasedSpawner) PinSpawnSource(ctx context.Context) (Spawner, error) {
	delegate := s.spawner
	if pinner, ok := delegate.(SnapshotSpawner); ok {
		var err error
		delegate, err = pinner.PinSpawnSource(ctx)
		if err != nil {
			return nil, err
		}
	}
	return s.withLease(delegate)
}

func (s *applicationEventLeasedSpawner) PinSpawnRequestSource(ctx context.Context, source *subagent.Source) (Spawner, error) {
	pinner, ok := s.spawner.(RequestSourceSpawner)
	if !ok {
		if source == nil {
			delegate := s.spawner
			if snapshotter, snapshotOK := delegate.(SnapshotSpawner); snapshotOK {
				var err error
				delegate, err = snapshotter.PinSpawnSource(ctx)
				if err != nil {
					return nil, err
				}
			}
			return s.withLease(delegate)
		}
		return nil, errors.New("runtime: requested source requires a source-pinning spawner")
	}
	delegate, err := pinner.PinSpawnRequestSource(ctx, source)
	if err != nil {
		return nil, err
	}
	return s.withLease(delegate)
}

func (s *applicationEventLeasedSpawner) PinSpawnForkPoint(ctx context.Context, point SpawnForkPoint) (Spawner, error) {
	pinner, ok := s.spawner.(ForkPointSpawner)
	if !ok {
		return nil, errors.New("runtime: retained execution requires an exact fork-point spawner")
	}
	delegate, err := pinner.PinSpawnForkPoint(ctx, point)
	if err != nil {
		return nil, err
	}
	return s.withLease(delegate)
}

func (s *applicationEventLeasedSpawner) WithInbox(fn func() []string) Spawner {
	clone := *s
	if aware, ok := s.spawner.(InboxAwareSpawner); ok {
		clone.spawner = aware.WithInbox(fn)
	}
	return &clone
}

func (s *applicationEventLeasedSpawner) withLease(delegate Spawner) (Spawner, error) {
	if delegate == nil {
		return nil, errors.New("runtime: application-event source pin returned nil spawner")
	}
	provider := s.publisher.(ApplicationEventContextProvider)
	if provider.ApplicationEventContext().Generation == 0 {
		return delegate, nil
	}
	scope, release, err := s.publisher.(ApplicationEventContextLeaser).LeaseApplicationEventContext()
	if err != nil {
		return nil, err
	}
	if err := validateAgentEventContext(scope); err != nil {
		if release != nil {
			release()
		}
		return nil, err
	}
	if release == nil {
		release = func() {}
	}
	return &applicationEventLeasedSpawner{spawner: delegate, publisher: s.publisher, release: release}, nil
}

// AgentDownEventCallback composes durable terminal publication with an existing
// surface callback. It captures publisher context on the child's synchronous
// started event, then publishes terminal facts before notifying the outer UI.
// Brokerless/test surfaces retain their existing callback unchanged.
func AgentDownEventCallback(parentSessionID string, publisher ApplicationEventPublisher, next func(SubagentEvent)) func(SubagentEvent) {
	if publisher == nil {
		return next
	}
	observer, err := NewAgentDownObserver(parentSessionID, publisher)
	if err != nil {
		slog.Warn("agent.down durable observer unavailable", "parent_session", parentSessionID, "err", err)
		return next
	}
	return func(event SubagentEvent) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var publishErr error
		for attempt := 0; attempt < 3; attempt++ {
			_, _, publishErr = observer.Observe(ctx, event)
			if publishErr == nil || event.Phase != "finished" {
				break
			}
			delay := time.Duration(25*(1<<attempt)) * time.Millisecond
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				attempt = 3
			case <-timer.C:
			}
		}
		cancel()
		if event.Phase == "finished" {
			// A failed terminal publication retains its lease during bounded
			// retry, but must not pin a retired broker session forever once this
			// callback gives up. Successful observation already released it.
			observer.ReleaseChild(event.ChildSession)
		}
		if publishErr != nil {
			slog.Warn("agent.down durable publication failed",
				"parent_session", parentSessionID, "child_session", event.ChildSession, "err", publishErr)
		}
		if next != nil {
			next(event)
		}
	}
}

// Observe records a start anchor or publishes one terminal event. The bool is
// true only when a terminal event was durably accepted (including an
// idempotent broker replay).
func (o *AgentDownObserver) Observe(ctx context.Context, event SubagentEvent) (bool, uint64, error) {
	if o == nil {
		return false, 0, nil
	}
	if strings.TrimSpace(event.ParentSession) != o.parentSessionID {
		return false, 0, fmt.Errorf("runtime: agent.down parent mismatch: got %q", event.ParentSession)
	}
	childID := strings.TrimSpace(event.ChildSession)
	if childID == "" || len(childID) > maxAgentFactIDBytes {
		return false, 0, errors.New("runtime: agent.down child session is invalid")
	}
	if event.Phase == "started" {
		anchor, err := o.anchorPublisherContext()
		if err != nil {
			return false, 0, err
		}
		scope := anchor.scope
		if scope.Generation == 0 {
			// No lifecycle application is admitted for this broker session.
			// Avoid filling the durable event stream when there is no possible
			// subscriber; a later application-created child captures its context
			// on that child's own started event.
			anchor.release()
			return false, 0, nil
		}
		if err := validateAgentEventContext(scope); err != nil {
			anchor.release()
			return false, 0, err
		}
		o.mu.Lock()
		previous := o.children[childID]
		o.children[childID] = anchor
		o.mu.Unlock()
		if previous != nil {
			previous.release()
		}
		return false, 0, nil
	}
	if event.Phase != "finished" || !terminalAgentStatus(event.Status) {
		return false, 0, nil
	}

	o.mu.Lock()
	anchor, ok := o.children[childID]
	o.mu.Unlock()
	var scope ApplicationEventContext
	if !ok {
		// A restored producer can observe a terminal event after losing its
		// in-memory start map. Capture the current authenticated context; broker
		// idempotency still collapses replay of the same factual payload.
		scope = o.contextProvider.ApplicationEventContext()
	} else {
		scope = anchor.scope
	}
	if scope.Generation == 0 {
		return false, 0, nil
	}
	if err := validateAgentEventContext(scope); err != nil {
		return false, 0, err
	}
	facts, refs, err := BuildAgentDownFacts(event)
	if err != nil {
		return false, 0, err
	}
	raw, err := json.Marshal(facts)
	if err != nil {
		return false, 0, fmt.Errorf("runtime: encode agent.down: %w", err)
	}
	// Evidence refs are part of terminal identity. A retained child can reuse
	// its logical child ID across supervised generations while producing a new
	// immutable trace/tree head; those are distinct observations, while a retry
	// of the same terminal callback keeps identical refs and remains idempotent.
	digestMaterial, err := json.Marshal(struct {
		Facts        json.RawMessage `json:"facts"`
		EvidenceRefs []string        `json:"evidence_refs,omitempty"`
	}{Facts: raw, EvidenceRefs: refs})
	if err != nil {
		return false, 0, fmt.Errorf("runtime: encode agent.down identity: %w", err)
	}
	digest := digestFact(digestMaterial)
	sequence, err := o.publisher.PublishApplicationEvent(ctx, HostApplicationEvent{
		ID:                 AgentDownEvent + ":" + strings.TrimPrefix(digest, "sha256:"),
		Kind:               AgentDownEvent,
		Data:               raw,
		EvidenceRefs:       refs,
		IdempotencyKey:     AgentDownEvent + ":" + digest,
		ExpectedSessionID:  scope.SessionID,
		ExpectedGeneration: scope.Generation,
	})
	if err != nil {
		return false, 0, err
	}
	o.mu.Lock()
	if current, exists := o.children[childID]; exists && current == anchor {
		delete(o.children, childID)
	}
	o.mu.Unlock()
	if ok {
		anchor.release()
	}
	return true, sequence, nil
}

func (o *AgentDownObserver) anchorPublisherContext() (*agentDownChildAnchor, error) {
	if leaser, ok := o.publisher.(ApplicationEventContextLeaser); ok {
		scope, release, err := leaser.LeaseApplicationEventContext()
		if err != nil {
			return nil, err
		}
		if release == nil {
			release = func() {}
		}
		return &agentDownChildAnchor{scope: scope, release: release}, nil
	}
	return &agentDownChildAnchor{
		scope:   o.contextProvider.ApplicationEventContext(),
		release: func() {},
	}, nil
}

// ReleaseChild abandons any captured publisher lease for childSession. The
// ordinary success path calls this implicitly; callback owners use it after
// their bounded retry budget is exhausted so a retired broker session cannot
// be pinned forever by a permanently failed observation.
func (o *AgentDownObserver) ReleaseChild(childSession string) {
	if o == nil {
		return
	}
	childID := strings.TrimSpace(childSession)
	o.mu.Lock()
	anchor := o.children[childID]
	delete(o.children, childID)
	o.mu.Unlock()
	if anchor != nil {
		anchor.release()
	}
}

func validateAgentEventContext(scope ApplicationEventContext) error {
	if strings.TrimSpace(scope.SessionID) == "" || scope.Generation == 0 {
		return errors.New("runtime: agent.down authenticated session/generation unavailable")
	}
	return nil
}

func terminalAgentStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "completed", "cancelled", "error", "failed", "timeout", "down":
		return true
	default:
		return false
	}
}

func BuildAgentDownFacts(event SubagentEvent) (AgentDownFactsV1, []string, error) {
	childID := strings.TrimSpace(event.ChildSession)
	status := strings.TrimSpace(event.Status)
	if childID == "" || len(childID) > maxAgentFactIDBytes || !terminalAgentStatus(status) {
		return AgentDownFactsV1{}, nil, errors.New("runtime: agent.down requires a bounded child and terminal status")
	}
	writePaths, writeTruncated := boundedAgentStrings(event.WriteScope, maxAgentFactPaths)
	violations, violationsTruncated := boundedAgentStrings(event.ScopeViolations, maxAgentFactViolations)
	changed, changedTruncated := boundedAgentStrings(event.ChangedFiles, maxAgentFactPaths)
	facts := AgentDownFactsV1{
		Schema: AgentDownSchemaV1,
		Child: AgentTerminalFactsV1{
			AgentID:   boundedFactText(strings.TrimSpace(event.AgentID), maxAgentFactIDBytes).text,
			SessionID: childID,
			Status:    status,
			Role:      boundedFactText(strings.TrimSpace(event.Role), maxAgentFactTextBytes).text,
			Mode:      boundedFactText(strings.TrimSpace(event.Mode), maxAgentFactTextBytes).text,
			Execution: boundedFactText(strings.TrimSpace(event.Execution), maxAgentFactTextBytes).text,
		},
		Budget: AgentBudgetFactsV1{
			TokenLimit: nonNegative(event.TokenBudget), TurnLimit: nonNegative(event.MaxTurns),
			TimeoutSeconds: nonNegative(event.TimeoutSeconds),
		},
		Terminal: AgentTerminalMetadataV1{
			Usage: AgentTokenUsageV1{
				InputTokens: nonNegative(event.Terminal.Usage.InputTokens), OutputTokens: nonNegative(event.Terminal.Usage.OutputTokens),
				CacheReadTokens: nonNegative(event.Terminal.Usage.CacheReadTokens), CacheWriteTokens: nonNegative(event.Terminal.Usage.CacheWriteTokens),
			},
			UsageComplete: event.Terminal.UsageComplete,
		},
		Scope: AgentScopeFactsV1{
			Ownership:  boundedFactText(strings.TrimSpace(event.Ownership), maxAgentFactTextBytes).text,
			WritePaths: writePaths, WritePathsDigest: digestStringSlice(event.WriteScope), WritePathsTruncated: writeTruncated,
			Violations: violations, ViolationsDigest: digestStringSlice(event.ScopeViolations), ViolationsTruncated: violationsTruncated,
		},
	}
	if cleanup := event.Terminal.Cleanup; cleanup != nil {
		kind := boundedAgentFactText(cleanup.Kind, maxAgentFactTextBytes).text
		fingerprint := boundedAgentFingerprint(cleanup.Fingerprint)
		if kind != "" && fingerprint != "" {
			facts.Terminal.Cleanup = &AgentCleanupFactV1{Kind: kind, Fingerprint: fingerprint}
		}
	}
	if len(changed) > 0 || strings.TrimSpace(event.ForkTree) != "" {
		facts.Changes = &AgentChangeFactsV1{
			ForkTreeDigest: digestOptionalFact(event.ForkTree), ChangedPaths: changed,
			ChangedPathsDigest: digestStringSlice(event.ChangedFiles), ChangedPathsTruncated: changedTruncated,
		}
	}
	if strings.TrimSpace(event.Error) != "" {
		facts.Failure = &AgentFailureFactsV1{Fingerprint: digestFact([]byte(strings.TrimSpace(event.Error)))}
	}
	refs := boundedAgentEvidenceRefs(event)
	return facts, refs, nil
}

func boundedAgentStrings(values []string, limit int) ([]string, bool) {
	out := make([]string, 0, min(len(values), limit))
	truncated := len(values) > limit
	for _, value := range values {
		if len(out) == limit {
			break
		}
		bounded := boundedAgentFactText(value, maxAgentFactPathBytes)
		if bounded.text == "" {
			continue
		}
		truncated = truncated || bounded.truncated
		out = append(out, bounded.text)
	}
	return out, truncated
}

func boundedAgentFactText(value string, limit int) boundedText {
	value = strings.ToValidUTF8(strings.TrimSpace(value), "?")
	value = strings.Map(func(r rune) rune {
		// Keep JSON expansion bounded and lifecycle payloads single-line. These
		// characters are not needed to identify a repo-relative path or a scope
		// violation and otherwise expand up to six bytes under encoding/json.
		switch {
		case r < 0x20, r == 0x7f, r == '<', r == '>', r == '&', r == '"', r == '\\', r == '\u2028', r == '\u2029':
			return '?'
		default:
			return r
		}
	}, value)
	return boundedFactText(value, limit)
}

func digestStringSlice(values []string) string {
	if len(values) == 0 {
		return ""
	}
	raw, _ := json.Marshal(values)
	return digestFact(raw)
}

func digestOptionalFact(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return digestFact([]byte(value))
}

func boundedAgentFingerprint(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) == len("sha256:")+64 && strings.HasPrefix(value, "sha256:") {
		for _, r := range strings.TrimPrefix(value, "sha256:") {
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
				return digestFact([]byte(value))
			}
		}
		return value
	}
	if value == "" {
		return ""
	}
	return digestFact([]byte(value))
}

func boundedAgentEvidenceRefs(event SubagentEvent) []string {
	refs := make([]string, 0, 2)
	for _, ref := range []string{event.TreeRef, event.TraceRef} {
		ref = strings.TrimSpace(ref)
		if ref == "" || len(ref) > 1024 || len(refs) == maxAgentFactEvidenceRefs {
			continue
		}
		refs = append(refs, ref)
	}
	return refs
}
