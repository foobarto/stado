package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/subagent"
)

type scopedAgentEventPublisher struct {
	mu       sync.Mutex
	scope    ApplicationEventContext
	events   []HostApplicationEvent
	byKey    map[string]uint64
	nextSeq  uint64
	failures int
	leases   int
	closing  bool
	closed   bool
}

func newScopedAgentEventPublisher(session string, generation uint64) *scopedAgentEventPublisher {
	return &scopedAgentEventPublisher{
		scope: ApplicationEventContext{SessionID: session, Generation: generation},
		byKey: make(map[string]uint64),
	}
}

func (p *scopedAgentEventPublisher) ApplicationEventContext() ApplicationEventContext {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.scope
}

func (p *scopedAgentEventPublisher) LeaseApplicationEventContext() (ApplicationEventContext, func(), error) {
	p.mu.Lock()
	if p.closed || p.closing || p.scope.Generation == 0 {
		p.mu.Unlock()
		return ApplicationEventContext{}, nil, errors.New("publisher retired")
	}
	p.leases++
	scope := p.scope
	p.mu.Unlock()
	var once sync.Once
	return scope, func() {
		once.Do(func() {
			p.mu.Lock()
			p.leases--
			if p.leases == 0 && p.closing {
				p.closed = true
			}
			p.mu.Unlock()
		})
	}, nil
}

func (p *scopedAgentEventPublisher) closeForSwitch() {
	p.mu.Lock()
	p.closing = true
	if p.leases == 0 {
		p.closed = true
	}
	p.mu.Unlock()
}

func (p *scopedAgentEventPublisher) closeState() (leases int, closing, closed bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.leases, p.closing, p.closed
}

func (p *scopedAgentEventPublisher) setScope(session string, generation uint64) {
	p.mu.Lock()
	p.scope = ApplicationEventContext{SessionID: session, Generation: generation}
	p.mu.Unlock()
}

func (p *scopedAgentEventPublisher) PublishApplicationEvent(_ context.Context, event HostApplicationEvent) (uint64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failures > 0 {
		p.failures--
		return 0, errors.New("temporary broker failure")
	}
	if event.ExpectedSessionID != p.scope.SessionID || event.ExpectedGeneration == 0 || event.ExpectedGeneration != p.scope.Generation {
		return 0, errors.New("stale publisher generation")
	}
	if sequence := p.byKey[event.IdempotencyKey]; sequence != 0 {
		return sequence, nil
	}
	p.nextSeq++
	p.byKey[event.IdempotencyKey] = p.nextSeq
	p.events = append(p.events, event)
	return p.nextSeq, nil
}

func (p *scopedAgentEventPublisher) failNext(count int) {
	p.mu.Lock()
	p.failures = count
	p.mu.Unlock()
}

func (p *scopedAgentEventPublisher) snapshot() []HostApplicationEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]HostApplicationEvent(nil), p.events...)
}

func TestAgentDownLateChildStaysWithCapturedParent(t *testing.T) {
	publisherA := newScopedAgentEventPublisher("controller-a", 1)
	publisherB := newScopedAgentEventPublisher("controller-b", 1)
	observerA, err := NewAgentDownObserver("session-a", publisherA)
	if err != nil {
		t.Fatal(err)
	}
	observerB, err := NewAgentDownObserver("session-b", publisherB)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := observerA.Observe(t.Context(), SubagentEvent{
		Phase: "started", ParentSession: "session-a", ChildSession: "child-a", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	// Model an active-session switch: A retires from the UI while its child is
	// running, then B starts independently. A's publisher remains alive only
	// because the started observation holds a bounded lease.
	publisherA.closeForSwitch()
	if leases, closing, closed := publisherA.closeState(); leases != 1 || !closing || closed {
		t.Fatalf("retired A publisher leases=%d closing=%v closed=%v", leases, closing, closed)
	}
	if _, _, err := observerB.Observe(t.Context(), SubagentEvent{
		Phase: "started", ParentSession: "session-b", ChildSession: "child-b", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	published, _, err := observerA.Observe(t.Context(), terminalAgentEvent("session-a", "child-a"))
	if err != nil || !published {
		t.Fatalf("late A child published=%v err=%v", published, err)
	}
	if got := len(publisherA.snapshot()); got != 1 {
		t.Fatalf("parent A events=%d, want 1", got)
	}
	if got := len(publisherB.snapshot()); got != 0 {
		t.Fatalf("active parent B received A child: %d event(s)", got)
	}
	if leases, closing, closed := publisherA.closeState(); leases != 0 || !closing || !closed {
		t.Fatalf("A publisher not released after terminal event: leases=%d closing=%v closed=%v", leases, closing, closed)
	}
}

type gatedAgentDownSpawner struct {
	callback func(SubagentEvent)
	entered  chan struct{}
	proceed  chan struct{}
}

func (s *gatedAgentDownSpawner) SpawnSubagent(ctx context.Context, _ subagent.Request) (subagent.Result, error) {
	close(s.entered)
	select {
	case <-ctx.Done():
		return subagent.Result{}, ctx.Err()
	case <-s.proceed:
	}
	s.callback(SubagentEvent{
		Phase: "started", ParentSession: "session-a", ChildSession: "child-a", Status: "running",
	})
	s.callback(terminalAgentEvent("session-a", "child-a"))
	return subagent.Result{ChildSession: "child-a"}, nil
}

func TestAgentDownAcceptedLaunchSurvivesSessionSwitchBeforeChildStart(t *testing.T) {
	publisherA := newScopedAgentEventPublisher("controller-a", 1)
	publisherB := newScopedAgentEventPublisher("controller-b", 1)
	callback := AgentDownEventCallback("session-a", publisherA, nil)
	base := &gatedAgentDownSpawner{
		callback: callback, entered: make(chan struct{}), proceed: make(chan struct{}),
	}
	fleet := NewFleet()
	spawner := ApplicationEventLeasedSpawner(base, publisherA)
	id, err := fleet.Spawn(t.Context(), spawner, "bounded task", SpawnOptions{ParentSessionID: "session-a"})
	if err != nil {
		t.Fatal(err)
	}
	<-base.entered

	// The launch was accepted by A, but its goroutine has not emitted started.
	// Retiring A now must defer closure on the synchronously acquired launch
	// lease; B becoming active must not receive A's later terminal event.
	publisherA.closeForSwitch()
	if leases, closing, closed := publisherA.closeState(); leases != 1 || !closing || closed {
		t.Fatalf("pre-start A lease missing: leases=%d closing=%v closed=%v", leases, closing, closed)
	}
	observerB, err := NewAgentDownObserver("session-b", publisherB)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := observerB.Observe(t.Context(), SubagentEvent{
		Phase: "started", ParentSession: "session-b", ChildSession: "child-b", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	close(base.proceed)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		entry, ok := fleet.Get(id)
		if ok && entry.Status != FleetStatusRunning {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if got := len(publisherA.snapshot()); got != 1 {
		t.Fatalf("late pre-start A child events=%d, want 1", got)
	}
	if got := len(publisherB.snapshot()); got != 0 {
		t.Fatalf("active B received A child: %d event(s)", got)
	}
	if leases, closing, closed := publisherA.closeState(); leases != 0 || !closing || !closed {
		t.Fatalf("A launch lease not released: leases=%d closing=%v closed=%v", leases, closing, closed)
	}
}

func TestAgentDownRejectsSiblingRootAndStaleGeneration(t *testing.T) {
	publisher := newScopedAgentEventPublisher("controller-a", 7)
	observer, err := NewAgentDownObserver("session-a", publisher)
	if err != nil {
		t.Fatal(err)
	}
	for _, parent := range []string{"session-sibling", "session-root"} {
		if _, _, err := observer.Observe(t.Context(), terminalAgentEvent(parent, "foreign-child")); err == nil {
			t.Fatalf("foreign parent %q was accepted", parent)
		}
	}
	if got := len(publisher.snapshot()); got != 0 {
		t.Fatalf("foreign child leaked %d event(s)", got)
	}
	if _, _, err := observer.Observe(t.Context(), SubagentEvent{
		Phase: "started", ParentSession: "session-a", ChildSession: "child-a", Status: "running",
	}); err != nil {
		t.Fatal(err)
	}
	publisher.setScope("controller-a", 8)
	if _, _, err := observer.Observe(t.Context(), terminalAgentEvent("session-a", "child-a")); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale generation error=%v", err)
	}
	if got := len(publisher.snapshot()); got != 0 {
		t.Fatalf("stale child was relabelled into new generation: %d event(s)", got)
	}
}

func TestAgentDownRestartRetryIsIdempotent(t *testing.T) {
	publisher := newScopedAgentEventPublisher("controller-a", 3)
	event := terminalAgentEvent("session-a", "child-a")
	firstObserver, _ := NewAgentDownObserver("session-a", publisher)
	first, firstSequence, err := firstObserver.Observe(t.Context(), event)
	if err != nil || !first {
		t.Fatalf("first publish=%v sequence=%d err=%v", first, firstSequence, err)
	}
	// A new observer represents an orchestrator restart that lost its in-memory
	// start map and retries the same terminal observation.
	restartedObserver, _ := NewAgentDownObserver("session-a", publisher)
	retry, retrySequence, err := restartedObserver.Observe(t.Context(), event)
	if err != nil || !retry || retrySequence != firstSequence {
		t.Fatalf("retry publish=%v sequence=%d want=%d err=%v", retry, retrySequence, firstSequence, err)
	}
	if got := len(publisher.snapshot()); got != 1 {
		t.Fatalf("idempotent retry appended %d events, want 1", got)
	}
}

func TestAgentDownRetainedRetryWithNewEvidenceIsDistinct(t *testing.T) {
	publisher := newScopedAgentEventPublisher("controller-a", 3)
	observer, _ := NewAgentDownObserver("session-a", publisher)
	first := terminalAgentEvent("session-a", "retained-child")
	first.TraceRef = "git:refs/stado/sessions/retained-child/trace@1111111111111111111111111111111111111111"
	second := first
	second.TraceRef = "git:refs/stado/sessions/retained-child/trace@2222222222222222222222222222222222222222"
	_, firstSequence, err := observer.Observe(t.Context(), first)
	if err != nil {
		t.Fatal(err)
	}
	_, secondSequence, err := observer.Observe(t.Context(), second)
	if err != nil {
		t.Fatal(err)
	}
	if firstSequence == 0 || secondSequence == firstSequence {
		t.Fatalf("distinct retained evidence collapsed: first=%d second=%d", firstSequence, secondSequence)
	}
	if got := len(publisher.snapshot()); got != 2 {
		t.Fatalf("retained generations published %d events, want 2", got)
	}
}

func TestAgentDownCallbackRetriesSameDurableObservation(t *testing.T) {
	publisher := newScopedAgentEventPublisher("controller-a", 3)
	publisher.failNext(1)
	var observed []SubagentEvent
	callback := AgentDownEventCallback("session-a", publisher, func(event SubagentEvent) {
		observed = append(observed, event)
	})
	callback(SubagentEvent{Phase: "started", ParentSession: "session-a", ChildSession: "child-a", Status: "running"})
	callback(terminalAgentEvent("session-a", "child-a"))
	if got := len(publisher.snapshot()); got != 1 {
		t.Fatalf("bounded retry published %d events, want 1", got)
	}
	if len(observed) != 2 {
		t.Fatalf("outer callback events=%d, want start and terminal exactly once", len(observed))
	}
}

func TestAgentDownPayloadIsBoundedAndCarriesNoAuthorityOrErrorText(t *testing.T) {
	secret := "controller_DO_NOT_LEAK"
	paths := make([]string, maxAgentFactPaths+8)
	for i := range paths {
		paths[i] = fmt.Sprintf("path/%03d/%s", i, strings.Repeat("x", maxAgentFactPathBytes))
	}
	event := terminalAgentEvent("session-a", "child-a")
	event.AgentID = "fleet-agent-a"
	event.Worktree = "/tmp/" + secret
	event.Error = "provider cleanup exposed " + secret
	event.Terminal = subagent.TerminalMetadata{
		Usage: subagent.TokenUsage{
			InputTokens: 101, OutputTokens: 23, CacheReadTokens: 7, CacheWriteTokens: 3,
		},
		UsageComplete: true,
		Cleanup:       &subagent.CleanupDiagnostic{Kind: "provider_close", Fingerprint: secret},
	}
	event.ChangedFiles = paths
	event.WriteScope = paths
	event.ScopeViolations = paths
	event.TreeRef = "git:refs/stado/sessions/child-a/tree@0123456789012345678901234567890123456789"
	event.TraceRef = "git:refs/stado/sessions/child-a/trace@abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	facts, refs, err := BuildAgentDownFacts(event)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(facts)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, []byte("session-a")) || bytes.Contains(raw, []byte("/tmp/")) {
		t.Fatalf("authority/local/error material leaked into payload: %s", raw)
	}
	if len(raw) >= 64<<10 {
		t.Fatalf("bounded payload is %d bytes, broker limit is 65536", len(raw))
	}
	if facts.Failure == nil || facts.Failure.Fingerprint == "" {
		t.Fatal("error fingerprint missing")
	}
	if facts.Child.AgentID != "fleet-agent-a" || facts.Child.SessionID != "child-a" {
		t.Fatalf("control/session identity was not preserved distinctly: %+v", facts.Child)
	}
	if facts.Terminal.Usage.InputTokens != 101 || facts.Terminal.Usage.OutputTokens != 23 || !facts.Terminal.UsageComplete {
		t.Fatalf("terminal token facts missing: %+v", facts.Terminal)
	}
	if facts.Terminal.Cleanup == nil || facts.Terminal.Cleanup.Kind != "provider_close" || facts.Terminal.Cleanup.Fingerprint == secret {
		t.Fatalf("cleanup fact was lost or leaked raw detail: %+v", facts.Terminal.Cleanup)
	}
	if !facts.Changes.ChangedPathsTruncated || !facts.Scope.WritePathsTruncated || !facts.Scope.ViolationsTruncated {
		t.Fatalf("truncation facts missing: %+v", facts)
	}
	if len(facts.Changes.ChangedPaths) != maxAgentFactPaths || len(facts.Scope.WritePaths) != maxAgentFactPaths || len(facts.Scope.Violations) != maxAgentFactViolations {
		t.Fatalf("unbounded path projection: changed=%d write=%d violations=%d", len(facts.Changes.ChangedPaths), len(facts.Scope.WritePaths), len(facts.Scope.Violations))
	}
	if len(refs) != 2 {
		t.Fatalf("evidence refs=%v", refs)
	}
}

func terminalAgentEvent(parent, child string) SubagentEvent {
	return SubagentEvent{
		Phase: "finished", ParentSession: parent, ChildSession: child,
		Role: "explorer", Mode: "read_only", Execution: "wait", Status: "completed",
		MaxTurns: 4, TokenBudget: 2048, TimeoutSeconds: 60,
		WriteScope: []string{"internal/runtime"}, ChangedFiles: []string{"internal/runtime/example.go"},
		ForkTree: "0123456789012345678901234567890123456789",
		Terminal: subagent.TerminalMetadata{
			Usage: subagent.TokenUsage{InputTokens: 100, OutputTokens: 20}, UsageComplete: true,
		},
	}
}
