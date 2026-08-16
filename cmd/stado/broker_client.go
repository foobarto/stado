package main

// broker_client.go — shared helper used by orchestrator entry points
// (stado run, TUI, stado run --headless, stado acp, stado mcp-server) to
// attach to the broker and create a session.
//
// The returned ceiling is converted to runtime.ExecutorSandbox once at each
// entry point and passed into every executor factory owned by that surface.
//
// Auto-spawn behavior matches dispatchViaDaemon in tool_run_daemon.go:
// fast-path dial, on failure spawn `stado daemon start --quiet`
// detached, poll until socket appears. Test binaries are refused by
// daemon.EnsureRunning (Go test binary detection); for those, this
// helper returns a "skipped" BrokerSession so existing entry-point
// tests don't need to spawn a daemon.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/foobarto/stado/internal/broker"
	"github.com/foobarto/stado/internal/brokercredential"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/daemon"
	"github.com/foobarto/stado/internal/plugins"
	pluginruntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/telemetry"
	"github.com/foobarto/stado/internal/trajectory"
	"github.com/foobarto/stado/internal/tui"
	"github.com/foobarto/stado/pkg/agent"
)

// inlineTUIRunner is the function launchInlineTUI uses to boot the TUI. It is a
// var (defaulting to tui.Run) so tests can capture the notices and executor
// sandbox policy without booting the real alt-screen TUI (which blocks on
// stdin/render).
var inlineTUIRunner = tui.Run

// brokerExecutorSandbox turns the process-level broker decision into the
// shared runtime policy used by TUI, run, headless, ACP, and MCP executors.
func brokerExecutorSandbox(bs *BrokerSession, disabled bool) runtime.ExecutorSandbox {
	if bs == nil {
		return runtime.ExecutorSandbox{Disabled: disabled}
	}
	disabled = disabled || bs.Profile == broker.ProfileNoSandbox
	policy := runtime.ExecutorSandbox{Disabled: disabled}
	policy.Ceiling = bs.Ceiling
	policy.EnforceCeiling = !bs.Skipped && !disabled
	return policy
}

// launchInlineTUI attaches to the broker for an inline TUI launch — the bare
// `stado` command AND `stado session resume` — folds the sandbox-mode banner
// into the startup notices, enforces the broker's projected ceiling (including
// credential-directory masks), and boots the TUI.
// Both entry points share it so a resumed session gets exactly the same
// sandbox treatment as a fresh one. Closes the broker session on return.
func launchInlineTUI(ctx context.Context, cfg *config.Config, notices []string, metrics telemetry.Metrics) error {
	cwd, _ := os.Getwd()
	bs, err := attachToBroker(ctx, brokerPurposeFromFlags(), brokerProfileFromFlags(), cwd)
	if err != nil {
		return fmt.Errorf("stado: %w", err)
	}
	credentialStore, err := brokercredential.New(cfg.StateDir())
	if err != nil {
		_ = bs.Close()
		return fmt.Errorf("stado: durable broker session credentials: %w", err)
	}
	bs.logicalCredentials = credentialStore
	var banner strings.Builder
	bs.AnnounceSandboxMode(&banner, "stado")
	notices = append(notices, splitBannerLines(banner.String())...)
	defer func() {
		if closeErr := bs.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "stado: broker session.terminate: %v\n", closeErr)
		}
	}()
	return inlineTUIRunner(cfg, notices, brokerExecutorSandbox(bs, noSandbox), metrics, bs)
}

// brokerAttachTimeout is the maximum wall-clock time the helper
// waits for the broker to be reachable (including auto-spawn).
// Mirrors daemonAutoSpawnTimeout in tool_run_daemon.go.
const brokerAttachTimeout = 3 * time.Second

// envBrokerAttach gates whether orchestrator entry points attach to
// the broker. v1 default in this release (phase 2 of the rollout):
// **attach** — every entry point goes through the broker by default.
// Set to "0" / "false" / "off" / "no" to opt out (development /
// unusual environments where the broker won't reach).
//
// Test infrastructure: existing tests stay green automatically
// because daemon.EnsureRunning refuses to auto-spawn the host
// binary as a daemon when it detects a Go test binary; the helper
// translates that into a Skipped session so no test setup needs to
// know about the broker.
const envBrokerAttach = "STADO_BROKER_ATTACH"

// BrokerSession is what an orchestrator entry point holds after a
// successful attach. SessionID and Ceiling are mirrored from the
// broker's reply. Skipped reports whether the attach was skipped
// (broker unreachable in a test binary, env opt-out, etc.) — entry
// points use this to decide whether to bother calling Close.
type BrokerSession struct {
	SessionID    string
	Purpose      broker.Purpose
	Profile      broker.Profile // mirrored from the request; useful for the startup banner
	TraceRef     string
	Ceiling      sandbox.Policy // typed; phase 2 uses this to inform runner choice
	Skipped      bool
	SkipReason   string
	WorktreePath string

	// client is the daemon connection that holds this session.
	// Closed by Close() after issuing session.terminate.
	client                  *daemon.Client
	controllerToken         string
	ownsClient              bool
	applicationMu           sync.RWMutex
	applicationGeneration   uint64
	applicationLeases       uint64
	applicationClosePending bool
	applicationClosed       bool
	logicalMu               sync.Mutex
	scheduleMu              sync.Mutex
	lastControlSequence     uint64
	logicalCredentials      *brokercredential.Store
	durableSubject          string
	heartbeatStop           chan struct{}
	heartbeatDone           chan struct{}
}

var _ runtime.BrokerSessionTransitioner = (*BrokerSession)(nil)
var _ runtime.BrokerLogicalSessionTransitioner = (*BrokerSession)(nil)
var _ runtime.BrokerLogicalSessionHandoff = (*BrokerSession)(nil)
var _ runtime.ApplicationWorkerRunController = (*BrokerSession)(nil)
var _ trajectory.Writer = (*BrokerSession)(nil)

type brokerArtifactBridge struct {
	client *daemon.Client
	token  string
}

type brokerEvidenceBridge struct {
	client *daemon.Client
	token  string
}

type brokerApplicationBridge struct {
	client *daemon.Client
	token  string
}

type brokerApplicationEventBridge struct {
	client *daemon.Client
	token  string
}

func (s *BrokerSession) BindArtifacts(ctx context.Context, identity plugins.RuntimeIdentity, manifest plugins.Manifest, toolName string) (pluginruntime.ArtifactBridgeBinding, error) {
	if s == nil || s.Skipped || s.client == nil || s.SessionID == "" {
		return pluginruntime.ArtifactBridgeBinding{}, errors.New("artifact broker unavailable for this session")
	}
	var result broker.ArtifactBindResult
	if err := s.client.Call(ctx, broker.MethodArtifactBind, broker.ArtifactBindParams{
		SessionID: s.SessionID, ControllerToken: s.controllerToken,
		Identity: identity, Manifest: manifest, ToolName: toolName,
	}, &result); err != nil {
		return pluginruntime.ArtifactBridgeBinding{}, err
	}
	return pluginruntime.ArtifactBridgeBinding{
		Bridge: &brokerArtifactBridge{client: s.client, token: result.BindingToken},
		Caller: pluginruntime.ArtifactCallerContext{
			Principal: result.Principal, CanonicalRepoID: result.CanonicalRepoID,
			SessionID: result.SessionID, SessionGeneration: result.SessionGeneration,
			AncestorSessionIDs: append([]string(nil), result.AncestorSessionIDs...),
		},
	}, nil
}

func (s *BrokerSession) BindEvidence(ctx context.Context, identity plugins.RuntimeIdentity, manifest plugins.Manifest, toolName string) (pluginruntime.EvidenceBridgeBinding, error) {
	if s == nil || s.Skipped || s.client == nil || s.SessionID == "" {
		return pluginruntime.EvidenceBridgeBinding{}, errors.New("evidence broker unavailable for this session")
	}
	var result broker.ArtifactBindResult
	params := broker.ArtifactBindParams{SessionID: s.SessionID, ControllerToken: s.controllerToken, Identity: identity, Manifest: manifest, ToolName: toolName}
	if err := s.client.Call(ctx, broker.MethodEvidenceBind, broker.EvidenceBindParams(params), &result); err != nil {
		return pluginruntime.EvidenceBridgeBinding{}, err
	}
	return pluginruntime.EvidenceBridgeBinding{Bridge: &brokerEvidenceBridge{client: s.client, token: result.BindingToken}}, nil
}

func (b *brokerEvidenceBridge) CallEvidence(ctx context.Context, operation string, payload []byte) ([]byte, error) {
	if b == nil || b.client == nil || b.token == "" {
		return nil, errors.New("evidence broker binding unavailable")
	}
	var response json.RawMessage
	if err := b.client.Call(ctx, broker.MethodEvidenceCall, broker.EvidenceCallParams{
		BindingToken: b.token, Operation: operation, Payload: json.RawMessage(payload),
	}, &response); err != nil {
		return nil, err
	}
	return append([]byte(nil), response...), nil
}

func (s *BrokerSession) BindApplication(ctx context.Context, identity plugins.RuntimeIdentity, manifest plugins.Manifest) (pluginruntime.ApplicationBinding, error) {
	if s == nil || s.Skipped || s.client == nil || s.SessionID == "" {
		return pluginruntime.ApplicationBinding{}, errors.New("application broker unavailable for this session")
	}
	var result broker.ApplicationBindResult
	if err := s.client.Call(ctx, broker.MethodApplicationBind, broker.ApplicationBindParams{
		SessionID: s.SessionID, ControllerToken: s.controllerToken,
		Identity: identity, Manifest: manifest,
	}, &result); err != nil {
		return pluginruntime.ApplicationBinding{}, err
	}
	s.applicationMu.Lock()
	s.applicationGeneration = result.SessionGeneration
	s.applicationMu.Unlock()
	caller := pluginruntime.ArtifactCallerContext{
		Principal: result.Principal, CanonicalRepoID: result.CanonicalRepoID,
		SessionID: result.SessionID, SessionGeneration: result.SessionGeneration,
		AncestorSessionIDs: append([]string(nil), result.AncestorSessionIDs...),
	}
	return pluginruntime.ApplicationBinding{
		Anchor: pluginruntime.ApplicationAnchor{
			SessionID: result.SessionID, SessionGeneration: result.SessionGeneration,
			CanonicalRepoID: result.CanonicalRepoID,
		},
		Artifact: pluginruntime.ArtifactBridgeBinding{
			Bridge: &brokerArtifactBridge{client: s.client, token: result.BindingToken}, Caller: caller,
		},
		Evidence:    pluginruntime.EvidenceBridgeBinding{Bridge: &brokerEvidenceBridge{client: s.client, token: result.BindingToken}},
		Application: &brokerApplicationBridge{client: s.client, token: result.BindingToken},
		Controller: &brokerApplicationControllerBridge{
			client: s.client, sessionID: result.SessionID,
			controllerToken: s.controllerToken, bindingToken: result.BindingToken,
		},
		Events: &brokerApplicationEventBridge{client: s.client, token: result.BindingToken},
	}, nil
}

func (b *brokerApplicationBridge) CallApplication(ctx context.Context, operation, requestID string, payload []byte) ([]byte, error) {
	if b == nil || b.client == nil || b.token == "" {
		return nil, errors.New("application broker binding unavailable")
	}
	var response json.RawMessage
	if err := b.client.Call(ctx, broker.MethodApplicationCall, broker.ApplicationCallParams{
		BindingToken: b.token, RequestID: requestID, Operation: operation,
		Payload: json.RawMessage(payload),
	}, &response); err != nil {
		return nil, err
	}
	return append([]byte(nil), response...), nil
}

func (b *brokerApplicationEventBridge) Pending(ctx context.Context, limit int) ([]pluginruntime.ApplicationEvent, error) {
	if b == nil || b.client == nil || b.token == "" {
		return nil, errors.New("application event binding unavailable")
	}
	var wire []broker.ApplicationEventResult
	if err := b.client.Call(ctx, broker.MethodApplicationEventsNext, broker.ApplicationEventsNextParams{BindingToken: b.token, Limit: limit}, &wire); err != nil {
		return nil, err
	}
	out := make([]pluginruntime.ApplicationEvent, 0, len(wire))
	for _, event := range wire {
		out = append(out, pluginruntime.ApplicationEvent{
			Kind: event.Kind, BrokerSeq: event.WALSequence,
			EvidenceRefs: append([]string(nil), event.EvidenceRefs...), Data: append(json.RawMessage(nil), event.Data...),
		})
	}
	return out, nil
}

func (b *brokerApplicationEventBridge) Acknowledge(ctx context.Context, sequence uint64) error {
	if b == nil || b.client == nil || b.token == "" || sequence == 0 {
		return errors.New("application event acknowledgement unavailable")
	}
	var result json.RawMessage
	return b.client.Call(ctx, broker.MethodApplicationEventsAck, broker.ApplicationEventsAckParams{
		BindingToken: b.token, RequestID: fmt.Sprintf("ack:%d", sequence), Sequence: sequence,
	}, &result)
}

func (s *BrokerSession) PublishApplicationEvent(ctx context.Context, event runtime.HostApplicationEvent) (uint64, error) {
	if s == nil || s.Skipped || s.client == nil {
		return 0, errors.New("application event publisher unavailable for this session")
	}
	s.applicationMu.RLock()
	sessionID, admittedGeneration := s.SessionID, s.applicationGeneration
	s.applicationMu.RUnlock()
	if sessionID == "" {
		return 0, errors.New("application event publisher unavailable for this session")
	}
	if strings.TrimSpace(event.IdempotencyKey) == "" {
		return 0, errors.New("application event idempotency key is required")
	}
	if event.ExpectedSessionID != "" && event.ExpectedSessionID != sessionID {
		return 0, errors.New("application event session changed")
	}
	expectedGeneration := event.ExpectedGeneration
	if expectedGeneration == 0 {
		expectedGeneration = admittedGeneration
	}
	var result broker.ApplicationEventResult
	if err := s.client.Call(ctx, broker.MethodApplicationEventPublish, broker.ApplicationEventPublishParams{
		SessionID: sessionID, ControllerToken: s.controllerToken, ExpectedGeneration: expectedGeneration,
		RequestID: event.IdempotencyKey, ID: event.ID,
		Kind: event.Kind, Data: event.Data, EvidenceRefs: event.EvidenceRefs,
	}, &result); err != nil {
		return 0, err
	}
	return result.WALSequence, nil
}

// ApplicationEventContext returns the native-held broker scope most recently
// admitted for this session's lifecycle applications. The controller token is
// intentionally not part of this projection and never reaches WASM.
func (s *BrokerSession) ApplicationEventContext() runtime.ApplicationEventContext {
	if s == nil {
		return runtime.ApplicationEventContext{}
	}
	s.applicationMu.RLock()
	sessionID, generation := s.SessionID, s.applicationGeneration
	s.applicationMu.RUnlock()
	return runtime.ApplicationEventContext{SessionID: sessionID, Generation: generation}
}

// LeaseApplicationEventContext retains this exact authenticated session and
// generation while an asynchronous native observation is outstanding. Closing
// a superseded TUI peer becomes deferred, not redirected: the old peer can
// publish its child's terminal facts, then its last release terminates it.
func (s *BrokerSession) LeaseApplicationEventContext() (runtime.ApplicationEventContext, func(), error) {
	if s == nil {
		return runtime.ApplicationEventContext{}, nil, errors.New("application event publisher unavailable for this session")
	}
	s.applicationMu.Lock()
	if s.Skipped || s.client == nil || s.SessionID == "" || s.applicationGeneration == 0 || s.applicationClosePending || s.applicationClosed {
		s.applicationMu.Unlock()
		return runtime.ApplicationEventContext{}, nil, errors.New("application event publisher unavailable for this session")
	}
	scope := runtime.ApplicationEventContext{SessionID: s.SessionID, Generation: s.applicationGeneration}
	s.applicationLeases++
	s.applicationMu.Unlock()

	var once sync.Once
	return scope, func() {
		once.Do(s.releaseApplicationEventContext)
	}, nil
}

func (s *BrokerSession) releaseApplicationEventContext() {
	if s == nil {
		return
	}
	s.logicalMu.Lock()
	s.applicationMu.Lock()
	if s.applicationLeases > 0 {
		s.applicationLeases--
	}
	var state brokerSessionCloseState
	var closeNow bool
	if s.applicationLeases == 0 && s.applicationClosePending {
		state, closeNow = s.beginCloseLocked()
	}
	s.applicationMu.Unlock()
	s.logicalMu.Unlock()
	if closeNow {
		if err := finishBrokerSessionClose(state); err != nil {
			fmt.Fprintf(os.Stderr, "stado: deferred broker session.terminate: %v\n", err)
		}
	}
}

func (s *BrokerSession) CheckSchedule(ctx context.Context) (runtime.ScheduleStatus, error) {
	if s == nil || s.Skipped || s.client == nil || s.SessionID == "" {
		return runtime.ScheduleStatus{State: runtime.ScheduleActive}, nil
	}
	s.scheduleMu.Lock()
	defer s.scheduleMu.Unlock()
	var projection broker.SessionScheduleResult
	if err := s.client.Call(ctx, broker.MethodSessionSchedule, broker.SessionScheduleParams{
		SessionID: s.SessionID, ControllerToken: s.controllerToken,
	}, &projection); err != nil {
		return runtime.ScheduleStatus{}, err
	}
	consumedBefore := s.lastControlSequence
	status := s.scheduleStatusFromProjection(projection)
	if status.State == runtime.SchedulePaused || status.State == runtime.ScheduleStopped {
		var consumed broker.SessionScheduleResult
		if err := s.client.Call(ctx, broker.MethodSessionScheduleConsume, broker.SessionScheduleConsumeParams{
			SessionID: s.SessionID, ControllerToken: s.controllerToken, Sequence: status.Sequence,
		}, &consumed); err != nil {
			// Do not locally consume a result the broker failed to durably apply.
			s.lastControlSequence = consumedBefore
			return runtime.ScheduleStatus{}, err
		}
	}
	return status, nil
}

func (s *BrokerSession) ActiveApplicationWorkerRun(ctx context.Context) (runtime.ApplicationWorkerRun, bool, error) {
	if s == nil || s.Skipped || s.client == nil || s.SessionID == "" {
		return runtime.ApplicationWorkerRun{}, false, nil
	}
	var projection broker.SessionScheduleResult
	if err := s.client.Call(ctx, broker.MethodSessionSchedule, broker.SessionScheduleParams{
		SessionID: s.SessionID, ControllerToken: s.controllerToken,
	}, &projection); err != nil {
		return runtime.ApplicationWorkerRun{}, false, err
	}
	if projection.ActiveWorkerRun == nil {
		return runtime.ApplicationWorkerRun{}, false, nil
	}
	run := projection.ActiveWorkerRun
	return runtime.ApplicationWorkerRun{
		SessionID: run.SessionID, Generation: run.Generation, PluginID: run.PluginID,
		RunID: run.RunID, Version: run.Version, WALSequence: run.WALSequence,
		Objective: run.Objective, Prompt: run.Prompt,
		Conflict: runtime.ApplicationWorkerRunConflict(run.Conflict), Status: runtime.ApplicationWorkerRunStatus(run.Status),
		TerminalReason: run.TerminalReason, TerminalSequence: run.TerminalSequence,
	}, true, nil
}

func (s *BrokerSession) scheduleStatusFromProjection(projection broker.SessionScheduleResult) runtime.ScheduleStatus {
	consumed := s.lastControlSequence
	latest := consumed
	if projection.LatestPause != nil {
		latest = max(latest, projection.LatestPause.WALSequence)
	}
	if projection.LatestStop != nil {
		latest = max(latest, projection.LatestStop.WALSequence)
	}
	if projection.LatestCompletion != nil {
		latest = max(latest, projection.LatestCompletion.WALSequence)
	}
	// Stop is the strongest unconsumed terminal request. Successful completion
	// comes next because it is an accepted transition, then pause. Advancing to
	// the newest sequence consumes lower-priority facts atomically.
	if projection.LatestStop != nil && projection.LatestStop.WALSequence > consumed {
		s.lastControlSequence = latest
		return runtime.ScheduleStatus{
			State: runtime.ScheduleStopped, ReasonCode: projection.LatestStop.ReasonCode,
			Reason: projection.LatestStop.Reason, Sequence: projection.LatestStop.WALSequence,
		}
	}
	if projection.LatestCompletion != nil && projection.LatestCompletion.WALSequence > consumed {
		s.lastControlSequence = latest
		return runtime.ScheduleStatus{
			State: runtime.ScheduleCompleted, Reason: projection.LatestCompletion.Summary,
			Sequence: projection.LatestCompletion.WALSequence,
		}
	}
	if projection.LatestPause != nil && projection.LatestPause.WALSequence > consumed {
		s.lastControlSequence = latest
		return runtime.ScheduleStatus{
			State: runtime.SchedulePaused, ReasonCode: projection.LatestPause.ReasonCode,
			Reason: projection.LatestPause.Reason, Sequence: projection.LatestPause.WALSequence,
		}
	}
	if len(projection.ReviewingOperatorInputs) > 0 {
		input := projection.ReviewingOperatorInputs[0]
		return runtime.ScheduleStatus{
			State: runtime.ScheduleHeld, ReasonCode: "operator-input.reviewing",
			Reason: "a signed application is reviewing captured operator input", Sequence: input.WALSequence,
		}
	}
	if len(projection.ActiveHolds) > 0 {
		hold := projection.ActiveHolds[0]
		return runtime.ScheduleStatus{
			State: runtime.ScheduleHeld, ReasonCode: hold.ReasonCode,
			Reason: hold.Reason, Until: hold.LeaseUntil, Sequence: hold.WALSequence,
		}
	}
	return runtime.ScheduleStatus{State: runtime.ScheduleActive, Sequence: projection.AsOfSequence}
}

func (b *brokerArtifactBridge) call(ctx context.Context, method, requestID string, payload []byte) ([]byte, error) {
	if b == nil || b.client == nil || b.token == "" {
		return nil, errors.New("artifact broker binding unavailable")
	}
	var response json.RawMessage
	if err := b.client.Call(ctx, method, broker.ArtifactCallParams{
		BindingToken: b.token, RequestID: requestID, Payload: json.RawMessage(payload),
	}, &response); err != nil {
		return nil, err
	}
	return append([]byte(nil), response...), nil
}

func (b *brokerArtifactBridge) Propose(ctx context.Context, _ pluginruntime.ArtifactCaller, requestID string, payload []byte) ([]byte, error) {
	return b.call(ctx, broker.MethodArtifactPropose, requestID, payload)
}

func (b *brokerArtifactBridge) Query(ctx context.Context, _ pluginruntime.ArtifactCaller, requestID string, payload []byte) ([]byte, error) {
	return b.call(ctx, broker.MethodArtifactQuery, requestID, payload)
}

func (b *brokerArtifactBridge) Edit(ctx context.Context, _ pluginruntime.ArtifactCaller, requestID string, payload []byte) ([]byte, error) {
	return b.call(ctx, broker.MethodArtifactEdit, requestID, payload)
}

func (b *brokerArtifactBridge) Observe(ctx context.Context, _ pluginruntime.ArtifactCaller, requestID string, payload []byte) ([]byte, error) {
	return b.call(ctx, broker.MethodArtifactObserve, requestID, payload)
}

type brokerSessionCloseState struct {
	client          *daemon.Client
	sessionID       string
	controllerToken string
	ownsClient      bool
	durable         bool
	heartbeatStop   chan struct{}
	heartbeatDone   chan struct{}
}

// Close issues broker.v1.session.terminate against the session. Only the root
// handle (ownsClient) closes the underlying daemon connection; independently
// owned peers retire their exact session/controller while leaving the shared
// transport usable by the root and sibling peers. A session with outstanding
// application-event leases is retired from its caller immediately but remains
// authenticated until the final lease publishes or abandons its bounded
// observation. Idempotent: safe to call even when Skipped, when the session was
// never created, or after a previous Close. Returns the synchronous terminate
// error only; a deferred terminate error is reported when the last lease
// releases.
func (s *BrokerSession) Close() error {
	if s == nil || s.Skipped {
		return nil
	}
	s.logicalMu.Lock()
	s.applicationMu.Lock()
	if s.applicationClosed || s.client == nil || s.SessionID == "" {
		s.applicationMu.Unlock()
		s.logicalMu.Unlock()
		return nil
	}
	if s.applicationLeases > 0 {
		s.applicationClosePending = true
		s.applicationMu.Unlock()
		s.logicalMu.Unlock()
		return nil
	}
	state, closeNow := s.beginCloseLocked()
	s.applicationMu.Unlock()
	s.logicalMu.Unlock()
	if !closeNow {
		return nil
	}
	return finishBrokerSessionClose(state)
}

// beginCloseLocked detaches all authority before performing daemon I/O. This
// keeps concurrent Close calls idempotent and prevents a publisher from racing
// a terminal request with credentials that are already being retired.
func (s *BrokerSession) beginCloseLocked() (brokerSessionCloseState, bool) {
	if s.applicationClosed || s.client == nil || s.SessionID == "" {
		return brokerSessionCloseState{}, false
	}
	state := brokerSessionCloseState{
		client: s.client, sessionID: s.SessionID,
		controllerToken: s.controllerToken, ownsClient: s.ownsClient,
		durable: s.durableSubject != "", heartbeatStop: s.heartbeatStop, heartbeatDone: s.heartbeatDone,
	}
	if s.heartbeatStop != nil {
		close(s.heartbeatStop)
	}
	s.heartbeatStop = nil
	s.heartbeatDone = nil
	s.applicationClosed = true
	s.applicationClosePending = false
	s.SessionID = ""
	s.applicationGeneration = 0
	s.controllerToken = ""
	if s.ownsClient {
		s.client = nil
	}
	return state, true
}

func finishBrokerSessionClose(state brokerSessionCloseState) error {
	if state.client == nil || state.sessionID == "" {
		return nil
	}
	if state.ownsClient {
		defer func() { _ = state.client.Close() }()
	}
	if state.heartbeatDone != nil {
		<-state.heartbeatDone
	}
	ctx, cancel := context.WithTimeout(context.Background(), brokerAttachTimeout)
	defer cancel()
	method := broker.MethodSessionTerminate
	params := any(broker.SessionTerminateParams{SessionID: state.sessionID, ControllerToken: state.controllerToken})
	if state.durable {
		method = broker.MethodSessionDetach
		params = broker.SessionDetachParams{SessionID: state.sessionID, ControllerToken: state.controllerToken}
	}
	err := state.client.Call(ctx, method, params, nil)
	if err == nil {
		return nil
	}
	var rpcErr *daemon.Error
	if errors.As(err, &rpcErr) && (rpcErr.Code == daemon.ErrCodeBrokerSessionTerminated ||
		rpcErr.Code == daemon.ErrCodeBrokerSessionNotFound) {
		// Already terminated by some other path — idempotent.
		return nil
	}
	return err
}

// Sandbox returns the broker-projected executor decision for this session.
func (s *BrokerSession) Sandbox() runtime.ExecutorSandbox {
	return brokerExecutorSandbox(s, false)
}

func (s *BrokerSession) Worktree() string {
	if s == nil {
		return ""
	}
	return s.WorktreePath
}

// SetTaint updates the broker's mechanical provenance state for this turn.
func (s *BrokerSession) SetTaint(ctx context.Context, taint runtime.ContextTaint) error {
	if s == nil || s.Skipped || s.SessionID == "" {
		return nil
	}
	callCtx, cancel := context.WithTimeout(ctx, brokerAttachTimeout)
	defer cancel()
	return s.client.Call(callCtx, broker.MethodSessionTaint, broker.SessionTaintParams{
		SessionID: s.SessionID, ControllerToken: s.controllerToken,
		Taint: string(taint),
	}, nil)
}

func (s *BrokerSession) snapshotControllerAuthority() (*daemon.Client, string, string, bool) {
	if s == nil || s.Skipped {
		return nil, "", "", false
	}
	// Handoff rotates controller authority under logicalMu, while Close retires
	// it under logicalMu then applicationMu. Match that order and return one
	// internally consistent snapshot for asynchronous callers.
	s.logicalMu.Lock()
	defer s.logicalMu.Unlock()
	s.applicationMu.RLock()
	defer s.applicationMu.RUnlock()
	if s.applicationClosed || s.client == nil || s.SessionID == "" || s.controllerToken == "" {
		return nil, "", "", false
	}
	return s.client, s.SessionID, s.controllerToken, true
}

// EnsureTrajectoryObjective submits the first objective through the
// authenticated broker controller. The broker derives the durable logical
// subject and owns the canonical WAL write.
func (s *BrokerSession) EnsureTrajectoryObjective(ctx context.Context, objective string) error {
	client, sessionID, controllerToken, ok := s.snapshotControllerAuthority()
	if !ok {
		return nil
	}
	callCtx, cancel := context.WithTimeout(ctx, brokerAttachTimeout)
	defer cancel()
	return client.Call(callCtx, broker.MethodSessionContextObjective, broker.SessionContextObjectiveParams{
		SessionID: sessionID, ControllerToken: controllerToken, Objective: objective,
	}, nil)
}

// RecordTrajectoryToolOutcome submits bounded mechanical outcome facts. The
// broker authors the subject, principal, evidence ref, actor, and idempotency
// key before appending the canonical event.
func (s *BrokerSession) RecordTrajectoryToolOutcome(ctx context.Context, turn, invocation int, call agent.ToolUseBlock, result agent.ToolResultBlock) error {
	client, sessionID, controllerToken, ok := s.snapshotControllerAuthority()
	if !ok {
		return nil
	}
	sum := sha256.Sum256(call.Input)
	content := strings.ToLower(result.Content)
	denied := result.IsError && (strings.Contains(content, "permission") || strings.Contains(content, "outside write_scope") || strings.Contains(content, "denied"))
	callCtx, cancel := context.WithTimeout(ctx, brokerAttachTimeout)
	defer cancel()
	return client.Call(callCtx, broker.MethodSessionContextToolOutcome, broker.SessionContextToolOutcomeParams{
		SessionID: sessionID, ControllerToken: controllerToken,
		Turn: turn, Invocation: invocation, CallID: call.ID, Tool: call.Name, ArgsDigest: hex.EncodeToString(sum[:]),
		Succeeded: !result.IsError, Denied: denied,
	}, nil)
}

// CreateSubagent asks the broker to mint a child projected from this
// session's current effective policy. Skipped broker sessions preserve the
// local development/test fallback without claiming enforcement.
func (s *BrokerSession) CreateSubagent(ctx context.Context, req runtime.BrokerSubagentRequest) (runtime.BrokerController, error) {
	if s == nil {
		return nil, errors.New("broker child: parent session unavailable")
	}
	if s.Skipped {
		return &BrokerSession{
			Purpose:    broker.PurposeSubagent,
			Profile:    s.Profile,
			Skipped:    true,
			SkipReason: s.SkipReason,
			Ceiling:    s.Ceiling,
		}, nil
	}
	if s.client == nil || s.SessionID == "" {
		return nil, errors.New("broker child: parent session closed")
	}
	var result broker.SessionHandleResult
	callCtx, cancel := context.WithTimeout(ctx, brokerAttachTimeout)
	defer cancel()
	err := s.client.Call(callCtx, broker.MethodSessionCreate, broker.SessionCreateParams{
		Purpose: broker.PurposeSubagent, Profile: s.Profile,
		ParentSessionID: s.SessionID, ParentControllerToken: s.controllerToken,
		Role: req.Role, Mode: req.Mode, WriteScope: req.WriteScope,
	}, &result)
	if err != nil {
		return nil, err
	}
	return &BrokerSession{
		SessionID:       result.SessionID,
		controllerToken: result.ControllerToken,
		Purpose:         broker.PurposeSubagent,
		Profile:         s.Profile,
		TraceRef:        result.TraceRef,
		Ceiling:         result.Ceiling,
		WorktreePath:    result.CWD,
		client:          s.client,
	}, nil
}

// CreatePeer mints an independent top-level broker handle over the existing
// daemon connection. Long-lived ACP/headless servers assign one peer per
// logical client session so concurrent prompts cannot race a shared taint bit.
func (s *BrokerSession) CreatePeer(ctx context.Context, cwd string) (runtime.BrokerController, error) {
	if s == nil {
		return nil, errors.New("broker peer: parent connection unavailable")
	}
	if s.Skipped {
		return &BrokerSession{
			Purpose: broker.PurposeMainChat, Profile: s.Profile,
			Skipped: true, SkipReason: s.SkipReason, Ceiling: s.Ceiling,
		}, nil
	}
	if s.client == nil {
		return nil, errors.New("broker peer: connection closed")
	}
	var result broker.SessionHandleResult
	callCtx, cancel := context.WithTimeout(ctx, brokerAttachTimeout)
	defer cancel()
	if err := s.client.Call(callCtx, broker.MethodSessionCreate, broker.SessionCreateParams{
		Purpose: broker.PurposeMainChat, Profile: s.Profile, CWD: cwd,
		ParentSessionID: s.SessionID, ParentControllerToken: s.controllerToken,
	}, &result); err != nil {
		return nil, err
	}
	return &BrokerSession{
		SessionID: result.SessionID, controllerToken: result.ControllerToken,
		Purpose: result.Purpose, Profile: s.Profile,
		TraceRef: result.TraceRef, Ceiling: result.Ceiling,
		WorktreePath: result.CWD, client: s.client,
	}, nil
}

// OpenLogicalSession creates or adopts the exact durable broker scope for one
// git conversation. The recovery bearer is read/written only by the native
// orchestrator; WASM and model-visible payloads never receive it.
func (s *BrokerSession) OpenLogicalSession(ctx context.Context, cwd, subject string) (runtime.BrokerController, error) {
	if s == nil {
		return nil, errors.New("durable broker peer: parent connection unavailable")
	}
	if s.Skipped {
		return &BrokerSession{
			Purpose: broker.PurposeMainChat, Profile: s.Profile,
			Skipped: true, SkipReason: s.SkipReason, Ceiling: s.Ceiling,
		}, nil
	}
	if s.client == nil || s.SessionID == "" {
		return nil, errors.New("durable broker peer: parent connection closed")
	}
	if s.logicalCredentials == nil {
		return nil, errors.New("durable broker peer: credential store unavailable")
	}
	credential, loadErr := s.logicalCredentials.Load(subject)
	create := errors.Is(loadErr, brokercredential.ErrNotFound)
	if loadErr != nil && !create {
		return nil, fmt.Errorf("durable broker peer: load credential: %w", loadErr)
	}
	reserveCredential := func() (broker.SessionAdoptionCredential, error) {
		var reserved broker.SessionReserveResult
		reserveCtx, cancel := context.WithTimeout(ctx, brokerAttachTimeout)
		defer cancel()
		if err := s.client.Call(reserveCtx, broker.MethodSessionReserve, broker.SessionReserveParams{
			ParentSessionID: s.SessionID, ParentControllerToken: s.controllerToken,
			Subject: subject, CWD: cwd,
		}, &reserved); err != nil {
			return broker.SessionAdoptionCredential{}, err
		}
		credential := broker.SessionAdoptionCredential{
			Subject: reserved.Subject, Ticket: reserved.Ticket, ResumeSecret: reserved.ResumeSecret,
		}
		if reserved.Subject != subject || !reserved.ExpiresAt.After(time.Now()) || broker.ValidateSessionAdoptionCredential(credential) != nil {
			return broker.SessionAdoptionCredential{}, errors.New("broker returned an invalid durable-session reservation")
		}
		if err := s.logicalCredentials.Save(credential); err != nil {
			return broker.SessionAdoptionCredential{}, fmt.Errorf("persist reserved credential: %w", err)
		}
		return credential, nil
	}
	if create {
		reserved, err := reserveCredential()
		if err != nil {
			return nil, fmt.Errorf("durable broker peer: reserve credential: %w", err)
		}
		credential = reserved
	}
	var result broker.SessionHandleResult
	call := func(method string, params any) error {
		callCtx, cancel := context.WithTimeout(ctx, brokerAttachTimeout)
		defer cancel()
		return s.client.Call(callCtx, method, params, &result)
	}
	if !create {
		err := call(broker.MethodSessionAdopt, broker.SessionAdoptParams{
			Subject: credential.Subject, Ticket: credential.Ticket,
			ResumeSecret: credential.ResumeSecret, CWD: cwd,
		})
		if err == nil {
			if result.Subject != subject || result.AdoptionTicket != credential.Ticket || result.ResumeSecret != credential.ResumeSecret {
				_ = detachLogicalSession(s.client, result.SessionID, result.ControllerToken)
				return nil, errors.New("durable broker peer: adoption response changed the exact subject or recovery bearer")
			}
		} else if !unrecordedLogicalCredential(err) {
			return nil, err
		} else {
			create = true
		}
	}
	if create {
		createParams := func() broker.SessionCreateParams {
			return broker.SessionCreateParams{
				Purpose: broker.PurposeMainChat, Profile: s.Profile, CWD: cwd, Subject: subject,
				AdoptionTicket: credential.Ticket, ResumeSecret: credential.ResumeSecret,
				ParentSessionID: s.SessionID, ParentControllerToken: s.controllerToken,
			}
		}
		if err := call(broker.MethodSessionCreate, createParams()); err != nil {
			var rpcErr *daemon.Error
			if !errors.As(err, &rpcErr) || rpcErr.Code != daemon.ErrCodeBrokerSessionScopeCredential {
				return nil, err
			}
			// A pre-staged reservation may expire while the client is down, or be
			// bound to the process-local parent lost in a daemon restart. It has no
			// application state, so obtain and atomically replace it once.
			reserved, reserveErr := reserveCredential()
			if reserveErr != nil {
				return nil, fmt.Errorf("durable broker peer: renew expired reservation: %w", reserveErr)
			}
			credential = reserved
			if err := call(broker.MethodSessionCreate, createParams()); err != nil {
				return nil, err
			}
		}
		if result.Subject != subject || result.AdoptionTicket != credential.Ticket || result.ResumeSecret != credential.ResumeSecret {
			_ = retireUnstoredLogicalSession(s.client, result)
			return nil, errors.New("durable broker peer: broker returned mismatched logical subject or recovery bearer")
		}
	}
	peer := &BrokerSession{
		SessionID: result.SessionID, controllerToken: result.ControllerToken,
		Purpose: result.Purpose, Profile: s.Profile, TraceRef: result.TraceRef,
		Ceiling: result.Ceiling, WorktreePath: result.CWD, client: s.client,
		logicalCredentials: s.logicalCredentials, durableSubject: subject,
	}
	peer.startHeartbeat()
	return peer, nil
}

// ReserveLogicalSessionHandoff durably binds an automatic-recovery child to
// this exact source controller and turn, then pre-stages the stable recovery
// bearer under the child subject. A failed stage never reaches commit, so the
// source remains authoritative.
func (s *BrokerSession) ReserveLogicalSessionHandoff(ctx context.Context, childSubject, sourceTurnRef string) (runtime.LogicalSessionHandoffReservation, error) {
	if s == nil || s.Skipped || s.client == nil || s.SessionID == "" || s.durableSubject == "" || s.logicalCredentials == nil {
		return runtime.LogicalSessionHandoffReservation{}, errors.New("durable broker handoff unavailable")
	}
	s.logicalMu.Lock()
	defer s.logicalMu.Unlock()
	var reserved broker.SessionSubjectHandoffReservation
	callCtx, cancel := context.WithTimeout(ctx, brokerAttachTimeout)
	defer cancel()
	if err := s.client.Call(callCtx, broker.MethodSessionHandoffReserve, broker.SessionHandoffReserveParams{
		SessionID: s.SessionID, ControllerToken: s.controllerToken,
		ChildSubject: childSubject, SourceTurnRef: sourceTurnRef,
	}, &reserved); err != nil {
		return runtime.LogicalSessionHandoffReservation{}, err
	}
	if reserved.ID == "" || reserved.SourceSubject != s.durableSubject || reserved.ChildSubject != childSubject ||
		reserved.SourceTurnRef != sourceTurnRef || !reserved.ExpiresAt.After(time.Now()) {
		return runtime.LogicalSessionHandoffReservation{}, errors.New("broker returned a mismatched logical-session handoff reservation")
	}
	staged, err := s.logicalCredentials.StageHandoff(s.durableSubject, childSubject)
	if err != nil {
		return runtime.LogicalSessionHandoffReservation{}, fmt.Errorf("pre-stage child recovery credential: %w", err)
	}
	if staged.Subject != childSubject || broker.ValidateSessionAdoptionCredential(staged) != nil {
		return runtime.LogicalSessionHandoffReservation{}, errors.New("pre-staged child recovery credential is invalid")
	}
	return runtime.LogicalSessionHandoffReservation{
		ID: reserved.ID, SourceSubject: reserved.SourceSubject, ChildSubject: reserved.ChildSubject,
		SourceTurnRef: reserved.SourceTurnRef, ExpiresAt: reserved.ExpiresAt,
	}, nil
}

// CommitLogicalSessionHandoff moves the existing broker scope and updates this
// native controller in place. The child credential was written before this
// call; after a successful RPC, even a process crash can reopen the child.
func (s *BrokerSession) CommitLogicalSessionHandoff(ctx context.Context, reservation runtime.LogicalSessionHandoffReservation) error {
	if s == nil || s.Skipped || s.client == nil || s.SessionID == "" || s.durableSubject == "" || s.logicalCredentials == nil {
		return errors.New("durable broker handoff unavailable")
	}
	s.logicalMu.Lock()
	defer s.logicalMu.Unlock()
	if reservation.ID == "" || reservation.SourceSubject != s.durableSubject || reservation.ChildSubject == "" ||
		reservation.SourceTurnRef == "" || !reservation.ExpiresAt.After(time.Now()) {
		return errors.New("durable broker handoff reservation is stale or mismatched")
	}
	credential, err := s.logicalCredentials.Load(reservation.ChildSubject)
	if err != nil {
		return fmt.Errorf("load pre-staged child recovery credential: %w", err)
	}
	oldToken, oldSubject := s.controllerToken, s.durableSubject
	stop, done := s.heartbeatStop, s.heartbeatDone
	if stop != nil {
		close(stop)
		s.heartbeatStop, s.heartbeatDone = nil, nil
	}
	if done != nil {
		<-done
	}
	params := broker.SessionHandoffCommitParams{
		SessionID: s.SessionID, ControllerToken: oldToken, HandoffID: reservation.ID,
		ChildSubject: reservation.ChildSubject, Ticket: credential.Ticket, ResumeSecret: credential.ResumeSecret,
	}
	var result broker.SessionHandleResult
	commit := func() error {
		result = broker.SessionHandleResult{}
		callCtx, cancel := context.WithTimeout(ctx, brokerAttachTimeout)
		defer cancel()
		return s.client.Call(callCtx, broker.MethodSessionHandoffCommit, params, &result)
	}
	err = commit()
	if err != nil {
		var rpcErr *daemon.Error
		if errors.As(err, &rpcErr) {
			s.startHeartbeat()
			return err
		}
		// The request may have committed even though its reply was lost. Replay
		// the exact prior-controller+bearer reservation once; the broker rotates
		// away the lost controller and returns a fresh one.
		firstErr := err
		if err = commit(); err != nil {
			// Ambiguous means neither source nor child may run locally. Do not
			// restart the source heartbeat with a possibly invalid token.
			s.controllerToken = ""
			s.durableSubject = reservation.ChildSubject
			return fmt.Errorf("logical-session handoff commit outcome is ambiguous: %w", errors.Join(firstErr, err))
		}
	}
	if result.SessionID != s.SessionID || result.Subject != reservation.ChildSubject || result.ControllerToken == "" ||
		result.ControllerToken == oldToken || result.AdoptionTicket != credential.Ticket || result.ResumeSecret != credential.ResumeSecret {
		// A syntactically successful reply is still authoritative evidence that
		// the source may have moved. Do not revive its heartbeat or leave a
		// usable source controller when the response cannot be trusted.
		s.controllerToken = ""
		s.durableSubject = reservation.ChildSubject
		return errors.New("broker returned a mismatched committed logical-session handoff; controller is fail-closed")
	}
	s.controllerToken = result.ControllerToken
	s.durableSubject = reservation.ChildSubject
	s.startHeartbeat()
	// The old file is no longer authority. Best-effort removal reduces stale
	// operator confusion; failure is harmless because broker subject matching
	// permanently fences old-subject adoption.
	_ = s.logicalCredentials.Remove(oldSubject)
	return nil
}

func unrecordedLogicalCredential(err error) bool {
	var rpcErr *daemon.Error
	return errors.As(err, &rpcErr) && (rpcErr.Code == daemon.ErrCodeBrokerSessionNotFound ||
		rpcErr.Code == daemon.ErrCodeBrokerSessionScopeCredential)
}

const brokerSessionHeartbeatInterval = 10 * time.Second

func (s *BrokerSession) startHeartbeat() {
	if s == nil || s.client == nil || s.durableSubject == "" || s.SessionID == "" {
		return
	}
	stop, done := make(chan struct{}), make(chan struct{})
	s.heartbeatStop, s.heartbeatDone = stop, done
	client, sessionID, controllerToken := s.client, s.SessionID, s.controllerToken
	go func() {
		defer close(done)
		ticker := time.NewTicker(brokerSessionHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				callCtx, cancel := context.WithTimeout(context.Background(), brokerAttachTimeout)
				_ = client.Call(callCtx, broker.MethodSessionHeartbeat, broker.SessionHeartbeatParams{
					SessionID: sessionID, ControllerToken: controllerToken,
				}, nil)
				cancel()
			}
		}
	}()
}

func retireUnstoredLogicalSession(client *daemon.Client, result broker.SessionHandleResult) error {
	if client == nil || result.SessionID == "" || result.ControllerToken == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), brokerAttachTimeout)
	defer cancel()
	return client.Call(ctx, broker.MethodSessionTerminate, broker.SessionTerminateParams{
		SessionID: result.SessionID, ControllerToken: result.ControllerToken,
	}, nil)
}

func detachLogicalSession(client *daemon.Client, sessionID, controllerToken string) error {
	if client == nil || sessionID == "" || controllerToken == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), brokerAttachTimeout)
	defer cancel()
	return client.Call(ctx, broker.MethodSessionDetach, broker.SessionDetachParams{
		SessionID: sessionID, ControllerToken: controllerToken,
	}, nil)
}

// brokerAttachOptIn reports whether the broker attach is enabled
// for this orchestrator invocation. v2: defaults to on. Set
// STADO_BROKER_ATTACH=0 (or false/off/no) to opt out — useful for
// development scenarios where the broker can't be reached or for
// debugging the pre-broker code paths.
func brokerAttachOptIn() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(envBrokerAttach)))
	switch v {
	case "0", "false", "off", "no":
		return false
	}
	return true
}

// attachToBroker auto-spawns the broker if absent, dials it, and
// issues broker.v1.session.create with the supplied purpose +
// profile. Returns a BrokerSession the caller must Close on exit.
//
// Behavior matrix:
//
//   - Opt-out (envBrokerAttach explicitly set to a falsy value
//     0/false/off/no): returns a Skipped BrokerSession with reason
//     "opt-out (STADO_BROKER_ATTACH=<value>)". Entry point proceeds
//     as today (no broker session, no ceiling enforcement).
//   - Broker reachable: dials, creates session, returns the handle.
//   - Broker not reachable + spawn refused (test binary): returns a
//     Skipped BrokerSession with reason "test-binary". Entry-point
//     tests don't have to spawn a daemon.
//   - Broker not reachable + spawn failed (real binary): returns an
//     error. The entry point should surface it as "couldn't start
//     the sandbox broker: <reason>; either fix the reason or pass
//     --no-sandbox" (per AC1.6 in the phase-1 spec).
func attachToBroker(ctx context.Context, purpose broker.Purpose, profile broker.Profile, cwd string) (*BrokerSession, error) {
	if !brokerAttachOptIn() {
		raw := strings.ToLower(strings.TrimSpace(os.Getenv(envBrokerAttach)))
		return &BrokerSession{
			Skipped:    true,
			SkipReason: fmt.Sprintf("opt-out (STADO_BROKER_ATTACH=%q)", raw),
		}, nil
	}

	socketPath, err := daemon.SocketPath()
	if err != nil {
		return nil, fmt.Errorf("broker attach: resolve socket path: %w", err)
	}
	stadoBin, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("broker attach: resolve stado binary: %w", err)
	}

	dialCtx, cancel := context.WithTimeout(ctx, brokerAttachTimeout)
	defer cancel()
	cl, _, err := daemon.EnsureRunning(dialCtx, socketPath, stadoBin, brokerAttachTimeout)
	if err != nil {
		if isTestBinaryRefusal(err) {
			return &BrokerSession{Skipped: true, SkipReason: "test-binary auto-spawn refused"}, nil
		}
		return nil, fmt.Errorf("broker attach: %w", err)
	}

	params := broker.SessionCreateParams{
		Purpose: purpose,
		Profile: profile,
		CWD:     cwd,
	}
	var result broker.SessionHandleResult
	callCtx, callCancel := context.WithTimeout(ctx, brokerAttachTimeout)
	defer callCancel()
	if err := cl.Call(callCtx, broker.MethodSessionCreate, params, &result); err != nil {
		_ = cl.Close()
		return nil, fmt.Errorf("broker attach: session.create: %w", err)
	}

	return &BrokerSession{
		SessionID:       result.SessionID,
		controllerToken: result.ControllerToken,
		Purpose:         result.Purpose,
		Profile:         profile,
		TraceRef:        result.TraceRef,
		Ceiling:         result.Ceiling,
		WorktreePath:    result.CWD,
		client:          cl,
		ownsClient:      true,
	}, nil
}

// AnnounceSandboxMode writes a one-time startup banner to w
// describing the sandbox state for surface (TUI / stado run /
// headless / ACP / mcp-server). Called from each entry point after
// attachToBroker so the operator sees the active profile + mount
// summary on stderr at every launch.
//
// When the broker attach is Skipped (test-binary refusal, env
// opt-out, etc.) the message indicates that — the operator can see
// from the banner whether the broker is or isn't in the path. This
// is the positive counterpart to today's WarnIfHostUnsandboxed,
// which only fires when sandboxing is NOT in place (DESIGN.md
// §"Sandbox" → "Sandbox-mode startup announcement").
func (s *BrokerSession) AnnounceSandboxMode(w io.Writer, surface string) {
	if w == nil {
		return
	}
	if s == nil || s.Skipped {
		reason := "(unknown reason)"
		if s != nil && s.SkipReason != "" {
			reason = "(" + s.SkipReason + ")"
		}
		fmt.Fprintf(w, "%s: broker attach skipped %s — broker ceiling inactive; local sandbox policy still applies unless --no-sandbox is set\n",
			surface, reason)
		return
	}
	profileTag := string(s.Profile)
	if profileTag == "" {
		profileTag = "(unknown)"
	}
	fmt.Fprintf(w, "%s: sandbox=%s session=%s (broker-mediated)\n", surface, profileTag, s.SessionID)
	writableSummary := summarizeFSWrite(s.Profile, s.Ceiling.FSWrite)
	fmt.Fprintf(w, "%s: writable: %s\n", surface, writableSummary)
	if maskedCount := countMaskedPaths(s.Profile); maskedCount > 0 {
		fmt.Fprintf(w, "%s: %d credential paths masked (~/.ssh/id_*, ~/.aws, ~/.git-credentials, …)\n",
			surface, maskedCount)
	}
}

// countMaskedPaths returns the count of credential-bearing paths
// the mount table explicitly denies in this profile. Used by the
// banner so operators see at-a-glance that secrets are masked
// rather than having to inspect the full mount table.
func countMaskedPaths(profile broker.Profile) int {
	if profile == broker.ProfileNoSandbox || profile == "" {
		return 0
	}
	return len(broker.MountTableFor(profile, "").MaskedPaths())
}

// summarizeFSWrite returns a short human-readable summary of the
// session's writable filesystem grant. Phase 2 default is launch
// cwd + /tmp; phase 3's mount table tightens this.
//
// ProfileNoSandbox produces an empty FSWrite (projectCeiling
// returns sandbox.Policy{}) because there is no broker-enforced
// ceiling — but the operator chose this mode explicitly and the
// runner is NoneRunner. Reporting "(none — read-only sandbox)"
// would directly contradict the sandbox=no-sandbox mode line.
// Cloud-review bug_004 / PR #71.
func summarizeFSWrite(profile broker.Profile, writes []string) string {
	if profile == broker.ProfileNoSandbox {
		return "(all paths — no OS-level fence applied)"
	}
	if len(writes) == 0 {
		return "(none — read-only sandbox)"
	}
	return strings.Join(writes, ", ")
}

// brokerPurposeFromFlags maps the current command's flags/context
// to a broker.Purpose. Phase 1: stado run, TUI, headless, ACP,
// MCP server all map to PurposeMainChat. Sub-agent spawns use
// PurposeSubagent (wired in phase 4 via spawn_agent → broker).
//
// `stado tool run` does NOT attach a broker session: it dispatches via the
// daemon (dispatchViaDaemon), and the daemon's daemonToolHost applies its own
// protective sandbox policy to the wasm tool call. Broker-mediated tool-run via
// the broker.v1.toolrun.sandbox handshake is deferred EP-0050 phase work and is
// not wired from the client yet (there is no attachToolRunBroker).
func brokerPurposeFromFlags() broker.Purpose {
	return broker.PurposeMainChat
}

// brokerProfileFromFlags maps the current command's flags to a
// broker.Profile. The persistent --no-sandbox flag selects the explicit
// opt-out profile; otherwise the sandboxed default. Honoured by every entry
// point that attaches to the broker (TUI, run, acp, run --headless, mcp-server,
// session tree), so the opt-out is no longer a run-only special case.
func brokerProfileFromFlags() broker.Profile {
	if noSandbox {
		return brokerProfileNoSandbox()
	}
	return broker.ProfileDefault
}

// brokerProfileNoSandbox returns the explicit operator opt-out
// profile. Used by `stado run --no-sandbox` (phase 1g) and any
// future surface that wires its own opt-out flag.
//
// Per DESIGN.md §"Broker" → "Non-session sandbox requests" the
// broker still mediates the request — the operator's decision is
// captured in the broker-decision log. ProfileNoSandbox configures
// the runtime to use NoneRunner with no namespace isolation.
func brokerProfileNoSandbox() broker.Profile {
	return broker.ProfileNoSandbox
}

// isTestBinaryRefusal reports whether err is the test-binary
// auto-spawn refusal from daemon.EnsureRunning. The message is the
// stable test we can match against — EnsureRunning emits a
// `daemon auto-spawn refused: host binary ... looks like a Go test
// binary` string we can grep for here without exporting a sentinel.
func isTestBinaryRefusal(err error) bool {
	return err != nil && strings.Contains(err.Error(), "looks like a Go test binary")
}
