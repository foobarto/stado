package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

type transitionTestRoot struct {
	mu       sync.Mutex
	peers    []*transitionTestPeer
	failNext error
	closed   int
}

func (r *transitionTestRoot) CreateSubagent(context.Context, runtime.BrokerSubagentRequest) (runtime.BrokerController, error) {
	return r, nil
}
func (r *transitionTestRoot) SetTaint(context.Context, runtime.ContextTaint) error { return nil }
func (r *transitionTestRoot) Sandbox() runtime.ExecutorSandbox                     { return runtime.ExecutorSandbox{} }
func (r *transitionTestRoot) Worktree() string                                     { return "" }
func (r *transitionTestRoot) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed++
	return nil
}
func (r *transitionTestRoot) CreatePeer(_ context.Context, cwd string) (runtime.BrokerController, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failNext != nil {
		err := r.failNext
		r.failNext = nil
		return nil, err
	}
	peer := &transitionTestPeer{id: len(r.peers) + 1, cwd: cwd}
	r.peers = append(r.peers, peer)
	return peer, nil
}

type transitionTestPeer struct {
	mu               sync.Mutex
	id               int
	cwd              string
	subject          string
	closed           int
	handoffReserves  int
	handoffCommits   int
	handoffCommitErr error
	moveBeforeError  bool
	taintErr         error
}

type logicalTransitionTestRoot struct {
	*transitionTestRoot
	lastSubject string
	lastCWD     string
}

func (r *logicalTransitionTestRoot) OpenLogicalSession(ctx context.Context, cwd, subject string) (runtime.BrokerController, error) {
	r.lastCWD, r.lastSubject = cwd, subject
	return r.CreatePeer(ctx, cwd)
}

func (p *transitionTestPeer) CreateSubagent(context.Context, runtime.BrokerSubagentRequest) (runtime.BrokerController, error) {
	return p, nil
}
func (p *transitionTestPeer) SetTaint(context.Context, runtime.ContextTaint) error { return p.taintErr }
func (p *transitionTestPeer) Sandbox() runtime.ExecutorSandbox                     { return runtime.ExecutorSandbox{} }
func (p *transitionTestPeer) Worktree() string                                     { return p.cwd }
func (p *transitionTestPeer) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed++
	return nil
}

func (p *transitionTestPeer) ReserveLogicalSessionHandoff(_ context.Context, childSubject, sourceTurnRef string) (runtime.LogicalSessionHandoffReservation, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.handoffReserves++
	return runtime.LogicalSessionHandoffReservation{
		ID: "handoff-test", SourceSubject: p.subject, ChildSubject: childSubject,
		SourceTurnRef: sourceTurnRef, ExpiresAt: time.Now().Add(time.Minute),
	}, nil
}

func (p *transitionTestPeer) CommitLogicalSessionHandoff(_ context.Context, reservation runtime.LogicalSessionHandoffReservation) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.handoffCommits++
	if reservation.SourceSubject != p.subject {
		return errors.New("test handoff source mismatch")
	}
	if p.handoffCommitErr != nil && !p.moveBeforeError {
		return p.handoffCommitErr
	}
	p.subject = reservation.ChildSubject
	if p.handoffCommitErr != nil {
		return p.handoffCommitErr
	}
	return nil
}

func attachTransitionTestPeer(t *testing.T, m *Model, root *transitionTestRoot) *transitionTestPeer {
	t.Helper()
	m.brokerRoot = root
	m.broker = root
	controller, owned, err := m.openSessionBroker(context.Background(), m.session)
	if err != nil {
		t.Fatal(err)
	}
	peer, ok := controller.(*transitionTestPeer)
	if !ok || !owned {
		t.Fatalf("controller=%T owned=%v, want owned test peer", controller, owned)
	}
	m.broker = peer
	m.brokerPeerOwned = true
	peer.subject = m.session.ID
	if m.executor != nil {
		m.executor.DispatchGate = runtime.SchedulingDispatchGate(peer)
	}
	return peer
}

func TestSessionTransitionRotatesPeerAndIsolatesPluginBindings(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)
	root := &transitionTestRoot{}
	firstPeer := attachTransitionTestPeer(t, m, root)
	if got := m.newPluginHostAdapter().broker; got != firstPeer {
		t.Fatalf("session A plugin binding broker = %T %p, want peer A %p", got, got, firstPeer)
	}

	if err := m.switchToSession(ids.second); err != nil {
		t.Fatal(err)
	}
	secondPeer, ok := m.broker.(*transitionTestPeer)
	if !ok || secondPeer == firstPeer {
		t.Fatalf("session B broker = %T %p, want a fresh peer", m.broker, m.broker)
	}
	if got := m.newPluginHostAdapter().broker; got != secondPeer {
		t.Fatalf("session B plugin binding broker = %T %p, want peer B %p", got, got, secondPeer)
	}
	if firstPeer.closed != 1 {
		t.Fatalf("superseded peer close count = %d, want 1", firstPeer.closed)
	}
	if root.closed != 0 {
		t.Fatalf("root transport controller was closed during switch: %d", root.closed)
	}
	if firstPeer.cwd != m.cwd || secondPeer.cwd != m.cwd {
		t.Fatalf("peer cwd = %q/%q, want launch cwd %q", firstPeer.cwd, secondPeer.cwd, m.cwd)
	}
}

func TestSessionTransitionPrefersExactDurableLogicalSubject(t *testing.T) {
	m, _, _ := newSessionSwitchModel(t)
	root := &logicalTransitionTestRoot{transitionTestRoot: &transitionTestRoot{}}
	m.brokerRoot, m.broker = root, root
	controller, owned, err := m.openSessionBroker(context.Background(), m.session)
	if err != nil {
		t.Fatal(err)
	}
	if controller == nil || !owned {
		t.Fatalf("controller=%T owned=%v", controller, owned)
	}
	if root.lastSubject != m.session.ID || root.lastCWD != m.cwd {
		t.Fatalf("logical transition subject/cwd=%q/%q want=%q/%q", root.lastSubject, root.lastCWD, m.session.ID, m.cwd)
	}
}

func TestSessionTransitionPeerFailureRollsBackAtomically(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)
	root := &transitionTestRoot{}
	firstPeer := attachTransitionTestPeer(t, m, root)
	oldSession := m.session
	oldExecutor := m.executor
	admissionErr := errors.New("existing application admission")
	m.applicationAdmissionFailure = admissionErr
	m.applicationFailure = admissionErr
	root.failNext = errors.New("mint denied")

	err := m.switchToSession(ids.second)
	if err == nil || !strings.Contains(err.Error(), "mint denied") {
		t.Fatalf("switch error = %v, want peer mint failure", err)
	}
	if m.session != oldSession || m.executor != oldExecutor || m.broker != firstPeer {
		t.Fatalf("failed transition mutated live session: session=%p executor=%p broker=%p", m.session, m.executor, m.broker)
	}
	if m.applicationAdmissionFailure != admissionErr || m.applicationFailure != admissionErr {
		t.Fatalf("failed transition replaced live application failures: admission=%v dispatch=%v", m.applicationAdmissionFailure, m.applicationFailure)
	}
	if firstPeer.closed != 0 || len(root.peers) != 1 {
		t.Fatalf("failed transition retired live peer or leaked candidate: closed=%d peers=%d", firstPeer.closed, len(root.peers))
	}
}

func TestSessionTransitionLifecycleAdmissionFailureRollsBackAndReapsCandidate(t *testing.T) {
	m, cfg, ids := newSessionSwitchModel(t)
	root := &transitionTestRoot{}
	firstPeer := attachTransitionTestPeer(t, m, root)
	pluginID := "quality-gate-1.0.0"
	pluginDir := filepath.Join(cfg.StateDir(), "plugins", pluginID)
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"name":"quality-gate","version":"1.0.0","capabilities":[],"tools":[],"lifecycle":{}}`)
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.manifest.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.manifest.sig"), []byte("invalid-but-present"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Plugins.Background = []string{pluginID}
	oldSession, oldExecutor := m.session, m.executor
	oldApplication := &runtime.LoadedLifecycleApplication{Dir: "session-a"}
	m.lifecycleApplications = []*runtime.LoadedLifecycleApplication{oldApplication}

	err := m.switchToSession(ids.second)
	if err == nil || !strings.Contains(err.Error(), "broker admission unavailable") {
		t.Fatalf("switch error = %v, want lifecycle admission failure", err)
	}
	if m.session != oldSession || m.executor != oldExecutor || m.broker != firstPeer {
		t.Fatalf("admission failure mutated session A: session=%p executor=%p broker=%p", m.session, m.executor, m.broker)
	}
	if len(m.lifecycleApplications) != 1 || m.lifecycleApplications[0] != oldApplication {
		t.Fatalf("admission failure replaced session A lifecycle composition: %#v", m.lifecycleApplications)
	}
	if len(root.peers) != 2 || root.peers[1].closed != 1 || firstPeer.closed != 0 {
		t.Fatalf("candidate cleanup: peers=%d firstClosed=%d candidateClosed=%d", len(root.peers), firstPeer.closed, root.peers[1].closed)
	}
}

func TestSessionReloadKeepsExactActivePeer(t *testing.T) {
	m, cfg, _ := newSessionSwitchModel(t)
	root := &transitionTestRoot{}
	peer := attachTransitionTestPeer(t, m, root)
	m.cfg = cfg
	m.handleConfigReload()
	if m.broker != peer || len(root.peers) != 1 || peer.closed != 0 {
		t.Fatalf("reload rotated session controller: broker=%p peers=%d closed=%d", m.broker, len(root.peers), peer.closed)
	}
}

func TestSessionTransitionBusyCallbackDoesNotMintPeer(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)
	root := &transitionTestRoot{}
	peer := attachTransitionTestPeer(t, m, root)
	m.applicationPollRunning = true
	if err := m.switchToSession(ids.second); err == nil {
		t.Fatal("switch succeeded while application callback was active")
	}
	if m.broker != peer || len(root.peers) != 1 || peer.closed != 0 {
		t.Fatalf("busy switch touched controllers: broker=%p peers=%d closed=%d", m.broker, len(root.peers), peer.closed)
	}
}

func TestSessionShutdownClosesFinalPeerButNotRoot(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)
	root := &transitionTestRoot{}
	first := attachTransitionTestPeer(t, m, root)
	if err := m.switchToSession(ids.second); err != nil {
		t.Fatal(err)
	}
	second := m.broker.(*transitionTestPeer)
	m.Shutdown()
	m.Shutdown()
	if first.closed != 1 || second.closed != 1 {
		t.Fatalf("peer close counts = %d/%d, want each exactly once", first.closed, second.closed)
	}
	if root.closed != 0 {
		t.Fatalf("shutdown closed root transport controller %d time(s)", root.closed)
	}
}

func TestForkRecoveryMovesExistingScopeBeforeAdoptingChild(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := newForkTestModel(t)
	root := &transitionTestRoot{}
	first := attachTransitionTestPeer(t, m, root)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	child, err := runtime.ForkPluginSession(cfg, m.session, "", "controller transition", "test")
	if err != nil {
		t.Fatal(err)
	}
	m.recoveryPluginActive = true

	if command := m.adoptForkedSession(child.ID, string(stadogit.TurnTagRef(m.session.ID, m.session.Turn())), "controller transition"); command != nil {
		t.Fatal("recovery without a blocked prompt unexpectedly returned a stream command")
	}
	second, ok := m.broker.(*transitionTestPeer)
	if !ok || second != first || m.session.ID != child.ID {
		t.Fatalf("recovery session=%s broker=%T %p, want child on same durable peer", m.session.ID, m.broker, m.broker)
	}
	if first.closed != 0 || len(root.peers) != 1 || first.handoffReserves != 1 || first.handoffCommits != 1 || first.subject != child.ID {
		t.Fatalf("recovery peer lifecycle: closed=%d peers=%d reserves=%d commits=%d subject=%q", first.closed, len(root.peers), first.handoffReserves, first.handoffCommits, first.subject)
	}
}

func TestForkRecoveryCommitReplyLossLeavesSourceFailClosed(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	m := newForkTestModel(t)
	root := &transitionTestRoot{}
	peer := attachTransitionTestPeer(t, m, root)
	peer.handoffCommitErr = errors.New("reply lost")
	peer.moveBeforeError = true
	m.recoveryPluginActive = true
	m.recoveryPrompt = "preserve exact prompt"
	m.input.SetValue("draft")
	source := m.session
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	ref := string(stadogit.TurnTagRef(source.ID, source.Turn()))
	child, err := runtime.ForkPluginSession(cfg, source, ref, "controller transition", "test")
	if err != nil {
		t.Fatal(err)
	}

	if command := m.adoptForkedSession(child.ID, ref, "controller transition"); command != nil {
		t.Fatal("ambiguous handoff returned a provider command")
	}
	if m.session != source || peer.subject != child.ID {
		t.Fatalf("ambiguous handoff local=%s broker=%s, want source local and child broker", m.session.ID, peer.subject)
	}
	if m.applicationFailureSources[applicationFailureSessionHandoff] == nil || !strings.Contains(m.input.Value(), "preserve exact prompt") || !strings.Contains(m.input.Value(), "draft") {
		t.Fatalf("ambiguous handoff not fail-closed: failure=%v input=%q", m.applicationFailureSources[applicationFailureSessionHandoff], m.input.Value())
	}
	retained := m.input.Value()
	_, command, handled := submitInput(m)
	if !handled || command != nil || m.input.Value() != retained {
		t.Fatalf("ordinary input escaped ambiguous handoff gate: handled=%v command=%v input=%q", handled, command != nil, m.input.Value())
	}
}

func TestBrokerSessionTransitionerIsOptional(t *testing.T) {
	m, _, _ := newSessionSwitchModel(t)
	single := &transitionTestPeer{}
	m.brokerRoot = single
	m.broker = single
	controller, owned, err := m.openSessionBroker(context.Background(), m.session)
	if err != nil || controller != single || owned {
		t.Fatalf("optional transition fallback = (%T,%v,%v)", controller, owned, err)
	}
}

var _ runtime.BrokerSessionTransitioner = (*transitionTestRoot)(nil)
var _ runtime.BrokerController = (*transitionTestPeer)(nil)
