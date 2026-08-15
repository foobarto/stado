package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/foobarto/stado/internal/plugins"
	pluginruntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/tools"
)

// ContextTaint is the broker-visible provenance state for the current
// operator turn. Tool and subagent results are untrusted at ingestion; a new
// operator prompt resets the state to clean.
type ContextTaint string

const (
	ContextClean   ContextTaint = "clean"
	ContextTainted ContextTaint = "tainted"
)

// BrokerSubagentRequest is the runtime-owned child request shape. It avoids a
// dependency from runtime back to the CLI's concrete daemon client.
type BrokerSubagentRequest struct {
	Role       string
	Mode       string
	WriteScope []string
}

// BrokerController is the narrow orchestration contract runtime loops need.
// A created child is itself a controller so nested children inherit the same
// broker mediation and taint tracking.
type BrokerController interface {
	CreateSubagent(context.Context, BrokerSubagentRequest) (BrokerController, error)
	SetTaint(context.Context, ContextTaint) error
	Sandbox() ExecutorSandbox
	Worktree() string
	Close() error
}

// BrokerSessionTransitioner is the optional controller extension used by
// long-lived frontends when their logical session changes. The returned peer
// has an independent broker session/controller token while sharing only the
// implementation's transport. Callers own the peer and must close it without
// closing the root controller that minted it.
//
// Keeping this separate from BrokerController lets short-lived runtimes and
// test controllers remain deliberately single-session.
type BrokerSessionTransitioner interface {
	CreatePeer(context.Context, string) (BrokerController, error)
}

// BrokerLogicalSessionTransitioner is the durable variant used by the TUI
// after it has opened an exact git conversation. Implementations must bind the
// returned controller to both cwd's canonical repository and subject; callers
// cannot select the recovered broker session/generation.
type BrokerLogicalSessionTransitioner interface {
	OpenLogicalSession(context.Context, string, string) (BrokerController, error)
}

// LogicalSessionHandoffReservation is an authority-free native receipt for one
// exact automatic-recovery child. The implementation pre-stages the stable
// recovery credential before returning it; only CommitLogicalSessionHandoff
// can move the broker-owned application scope.
type LogicalSessionHandoffReservation struct {
	ID            string
	SourceSubject string
	ChildSubject  string
	SourceTurnRef string
	ExpiresAt     time.Time
}

// BrokerLogicalSessionHandoff is the native-only two-phase continuation seam.
// It is deliberately separate from ordinary session switching: manual forks
// open an independent broker scope and never call these methods.
type BrokerLogicalSessionHandoff interface {
	ReserveLogicalSessionHandoff(context.Context, string, string) (LogicalSessionHandoffReservation, error)
	CommitLogicalSessionHandoff(context.Context, LogicalSessionHandoffReservation) error
}

// ArtifactBrokerController is the optional broker extension used only by the
// verified native plugin loader. The returned opaque binding never enters WASM
// memory; host imports retain it and submit bounded guest payloads through it.
type ArtifactBrokerController interface {
	BindArtifacts(context.Context, plugins.RuntimeIdentity, plugins.Manifest, string) (pluginruntime.ArtifactBridgeBinding, error)
}

// EvidenceBrokerController is the optional broker extension used by the
// verified native plugin loader for read-only corpus access and citation
// integrity. The opaque binding stays native and is scoped to the exact
// session generation and canonical plugin identity.
type EvidenceBrokerController interface {
	BindEvidence(context.Context, plugins.RuntimeIdentity, plugins.Manifest, string) (pluginruntime.EvidenceBridgeBinding, error)
}

// ApplicationBrokerController admits a persistent WASM application and
// returns its authenticated session anchor plus native-held typed bridges.
type ApplicationBrokerController interface {
	BindApplication(context.Context, plugins.RuntimeIdentity, plugins.Manifest) (pluginruntime.ApplicationBinding, error)
}

// HostApplicationEvent is a bounded native observation submitted to the
// broker's durable application stream. IdempotencyKey must be derived from a
// stable host anchor (for example session generation + committed turn ref), not
// random retry state.
type HostApplicationEvent struct {
	ID             string
	Kind           string
	Data           json.RawMessage
	EvidenceRefs   []string
	IdempotencyKey string
	// ExpectedGeneration is an optional native-held freshness guard. The broker
	// still supplies the authoritative generation; when this value is non-zero it
	// rejects publication after a session incarnation has changed instead of
	// silently relabelling an old observation with the new generation.
	ExpectedSessionID  string
	ExpectedGeneration uint64
}

type ApplicationEventPublisher interface {
	PublishApplicationEvent(context.Context, HostApplicationEvent) (uint64, error)
}

// ApplicationEventContext is the authenticated native publisher context. It is
// never serialized into guest data: session and generation remain fields of the
// broker-authored event envelope.
type ApplicationEventContext struct {
	SessionID  string
	Generation uint64
}

// ApplicationEventContextProvider is implemented by production broker session
// controllers. Long-running native observations capture this context before
// asynchronous work begins and submit its generation as a freshness guard.
type ApplicationEventContextProvider interface {
	ApplicationEventContext() ApplicationEventContext
}

// ApplicationEventContextLeaser keeps one authenticated publisher context
// alive while native work that began in that context completes asynchronously.
// The lease carries no controller capability. Release must be idempotent and
// must eventually be called, whether publication succeeds or is abandoned.
//
// Long-lived frontends use this seam when switching logical sessions: the old
// broker controller may retire immediately from the UI, but its underlying
// session is not terminated until terminal observations from already-running
// children have either been durably published or given up after bounded retry.
type ApplicationEventContextLeaser interface {
	LeaseApplicationEventContext() (ApplicationEventContext, func(), error)
}

type ScheduleState string

const (
	ScheduleActive    ScheduleState = "active"
	ScheduleHeld      ScheduleState = "held"
	SchedulePaused    ScheduleState = "paused"
	ScheduleStopped   ScheduleState = "stopped"
	ScheduleCompleted ScheduleState = "completed"
)

// ScheduleStatus is the broker's current shared-loop enforcement decision.
// It contains only enough detail to stop dispatch and explain why; plugin
// namespace projections remain broker-private.
type ScheduleStatus struct {
	State      ScheduleState
	ReasonCode string
	Reason     string
	Until      time.Time
	Sequence   uint64
}

// SchedulingController is the optional broker extension consulted immediately
// before every provider turn and tool dispatch. Implementations consume durable
// pause/stop requests once and continue returning current leased holds.
type SchedulingController interface {
	CheckSchedule(context.Context) (ScheduleStatus, error)
}

// ApplicationWorkerRunController is a native-only aggregate projection used
// by a long-lived UI to reconcile durable application recurrence after a
// restart or rebind. It never exposes the cross-plugin projection to WASM.
type ApplicationWorkerRunController interface {
	ActiveApplicationWorkerRun(context.Context) (ApplicationWorkerRun, bool, error)
}

var (
	ErrScheduleHeld      = errors.New("runtime: session held")
	ErrSchedulePaused    = errors.New("runtime: session paused")
	ErrScheduleStopped   = errors.New("runtime: session stopped")
	ErrScheduleCompleted = errors.New("runtime: session completed")
)

type ScheduleBlockedError struct {
	Status ScheduleStatus
}

func (e *ScheduleBlockedError) Error() string {
	reason := e.Status.ReasonCode
	if reason == "" {
		reason = e.Status.Reason
	}
	if reason == "" {
		reason = string(e.Status.State)
	}
	return fmt.Sprintf("runtime: session %s: %s", e.Status.State, reason)
}

func (e *ScheduleBlockedError) Unwrap() error {
	switch e.Status.State {
	case ScheduleHeld:
		return ErrScheduleHeld
	case SchedulePaused:
		return ErrSchedulePaused
	case ScheduleStopped:
		return ErrScheduleStopped
	case ScheduleCompleted:
		return ErrScheduleCompleted
	default:
		return nil
	}
}

// CheckScheduling is the shared enforcement seam. A controller without the
// scheduling extension predates EP-0064 and remains active; a configured
// extension failing to answer fails closed by returning its error.
func CheckScheduling(ctx context.Context, controller BrokerController) error {
	status, err := SchedulingStatus(ctx, controller)
	if err != nil {
		return err
	}
	if status.State == "" || status.State == ScheduleActive {
		return nil
	}
	// Successful completion is consumed by the agent-loop boundary. It is not
	// a dispatch failure, pause, or stop error; an in-flight turn may finish and
	// the next loop barrier returns success.
	if status.State == ScheduleCompleted {
		return nil
	}
	return &ScheduleBlockedError{Status: status}
}

// SchedulingStatus returns the broker projection without deciding whether a
// leased hold should abort the caller. Provider/tool dispatch uses
// CheckScheduling and fails immediately. Lifecycle owners use this lower seam
// to keep servicing durable timers and application events while the worker is
// held, without allowing another provider or tool call through.
func SchedulingStatus(ctx context.Context, controller BrokerController) (ScheduleStatus, error) {
	scheduler, ok := controller.(SchedulingController)
	if !ok || scheduler == nil {
		return ScheduleStatus{State: ScheduleActive}, nil
	}
	status, err := scheduler.CheckSchedule(ctx)
	if err != nil {
		return ScheduleStatus{}, err
	}
	if status.State == "" {
		status.State = ScheduleActive
	}
	return status, nil
}

type scheduleDispatchGate struct {
	controller BrokerController
}

func (g scheduleDispatchGate) BeforeTool(ctx context.Context) error {
	return CheckScheduling(ctx, g.controller)
}

func SchedulingDispatchGate(controller BrokerController) tools.DispatchGate {
	if _, ok := controller.(SchedulingController); !ok {
		return nil
	}
	return scheduleDispatchGate{controller: controller}
}

type verificationDispatchGate struct {
	controller BrokerController
}

// BeforeTool bypasses only ScheduleHeld for a controller-authenticated native
// verification pump. A quality application is expected to hold recurrence
// while verification runs; applying the ordinary gate would deadlock that
// workflow. Pause and stop still block, controller errors still fail closed,
// and this gate is installed only on an executor copy retained by the native
// pump, never on the worker executor. Successful completion is terminal here
// too: unlike an already in-flight ordinary tool, a queued verifier must not
// begin after completion won the worker race.
func (g verificationDispatchGate) BeforeTool(ctx context.Context) error {
	status, err := SchedulingStatus(ctx, g.controller)
	if err != nil {
		return err
	}
	if status.State == ScheduleHeld || status.State == ScheduleActive || status.State == "" {
		return nil
	}
	return &ScheduleBlockedError{Status: status}
}

// NativeVerificationDispatchGate is deliberately not selected by guest data.
// Possession of the native BrokerController and successful verification claim
// are the authority; the returned gate only narrows an executor copy's handling
// of the already-authenticated session schedule.
func NativeVerificationDispatchGate(controller BrokerController) tools.DispatchGate {
	if _, ok := controller.(SchedulingController); !ok {
		return nil
	}
	return verificationDispatchGate{controller: controller}
}
