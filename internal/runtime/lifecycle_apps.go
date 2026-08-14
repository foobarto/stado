package runtime

// Generic installed lifecycle-application loading (EP-0064).
//
// This package owns source resolution, trust verification, canonical runtime
// identity, broker admission, and host-primitive wiring. It deliberately does
// not know what an application does. Supervision, compaction, learning, or any
// future workflow remains signed WASM policy selected by its manifest.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	pluginruntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/plugins/runtime/pty"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/tool"
)

// LifecycleApplicationLoadOptions are host-owned inputs for one persistent
// application instance. Broker and Config are required: lifecycle applications
// are never admitted from unsigned bytes and never receive an identity or
// session anchor supplied by the guest.
type LifecycleApplicationLoadOptions struct {
	Config   *config.Config
	Broker   ApplicationBrokerController
	Workdir  string
	ToolHost tool.Host

	// ConfigureHost attaches surface-specific primitives (session, fleet, UI,
	// PTY, progress). It runs after capability parsing and authority-store
	// provisioning, but before imports are installed.
	ConfigureHost func(*pluginruntime.Host)

	// InvokeExecutor is the exact active executor used by stado_tool_invoke.
	// A nil value leaves that import unavailable even if a malformed caller
	// somehow supplied a registry elsewhere.
	InvokeExecutor *tools.Executor
	SecretsAudit   func(pluginruntime.SecretsAuditEvent)
}

func lifecyclePluginRoots(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	roots := cfg.AllPluginDirs()
	if cfg.Plugins.AllowProjectPlugins || cfg.ProjectPluginsDir() == "" {
		return roots
	}
	project := filepath.Clean(cfg.ProjectPluginsDir())
	filtered := roots[:0:len(roots)]
	for _, root := range roots {
		if filepath.Clean(root) != project {
			filtered = append(filtered, root)
		}
	}
	return filtered
}

// LoadedLifecycleApplication retains every resource whose lifetime must match
// the admitted (plugin identity, session, generation) tuple.
type LoadedLifecycleApplication struct {
	Dir         string
	Manifest    plugins.Manifest
	Identity    plugins.RuntimeIdentity
	Runtime     *pluginruntime.Runtime
	Application *pluginruntime.LifecycleApplication
	Bridge      pluginruntime.ApplicationBridge
	Controller  pluginruntime.ApplicationControllerBridge
	Events      pluginruntime.ApplicationEventTransport
	Tools       []*pluginruntime.PluginTool
	dispatchMu  sync.Mutex
}

// LoadInstalledLifecycleApplication verifies and starts a lifecycle application
// from an installed package directory. There is intentionally no unsigned or
// manifest-only fallback. Local development still uses the normal explicit
// install/trust path and receives a source-bound, unstable identity.
func LoadInstalledLifecycleApplication(ctx context.Context, pluginDir string, opts LifecycleApplicationLoadOptions) (*LoadedLifecycleApplication, error) {
	if opts.Config == nil {
		return nil, errors.New("lifecycle application: config is required")
	}
	if opts.Broker == nil {
		return nil, errors.New("lifecycle application: broker admission is required")
	}
	mf, sig, err := plugins.LoadFromDir(pluginDir)
	if err != nil {
		return nil, fmt.Errorf("lifecycle application: load manifest: %w", err)
	}
	if mf.Lifecycle == nil {
		return nil, fmt.Errorf("lifecycle application: plugin %q has no lifecycle declaration", mf.Name)
	}
	if err := VerifyInstalledPlugin(ctx, opts.Config, pluginDir, mf, sig); err != nil {
		return nil, fmt.Errorf("lifecycle application: verify %s@%s: %w", mf.Name, mf.Version, err)
	}
	wasmBytes, err := plugins.ReadVerifiedWASM(mf.WASMSHA256, filepath.Join(pluginDir, "plugin.wasm"))
	if err != nil {
		return nil, fmt.Errorf("lifecycle application: read verified wasm: %w", err)
	}
	identity, err := RuntimeIdentityForPluginDir(pluginDir, *mf)
	if err != nil {
		return nil, fmt.Errorf("lifecycle application: canonical identity: %w", err)
	}
	return loadVerifiedLifecycleApplication(ctx, pluginDir, *mf, identity, wasmBytes, opts)
}

// loadVerifiedLifecycleApplication is the post-verification composition seam.
// Keeping it separate makes the authority wiring testable without weakening the
// public installed-package trust gate.
func loadVerifiedLifecycleApplication(ctx context.Context, pluginDir string, mf plugins.Manifest, identity plugins.RuntimeIdentity, wasmBytes []byte, opts LifecycleApplicationLoadOptions) (_ *LoadedLifecycleApplication, err error) {
	if err := identity.ValidateManifest(mf); err != nil {
		return nil, fmt.Errorf("lifecycle application: identity: %w", err)
	}
	rt, err := pluginruntime.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("lifecycle application: runtime: %w", err)
	}
	keepRuntime := false
	defer func() {
		if !keepRuntime {
			_ = rt.Close(context.Background())
		}
	}()

	host := pluginruntime.NewHostWithIdentity(mf, identity, opts.Workdir, nil)
	host.StateDir = opts.Config.StateDir()
	host.AttachAuthorityStores(opts.Config.StateDir(), rt.InstanceStore(), opts.SecretsAudit)
	attachLifecycleHostSurface(host, opts.ToolHost)
	if opts.ConfigureHost != nil {
		opts.ConfigureHost(host)
	}
	binding, err := opts.Broker.BindApplication(ctx, identity, mf)
	if err != nil {
		return nil, fmt.Errorf("lifecycle application: broker admission: %w", err)
	}
	host.ArtifactBridge = binding.Artifact.Bridge
	host.ArtifactCaller = binding.Artifact.Caller
	host.ApplicationBridge = binding.Application
	host.ApplicationAnchor = binding.Anchor
	if err := binding.Anchor.Validate(); err != nil {
		return nil, fmt.Errorf("lifecycle application: broker anchor: %w", err)
	}

	if host.ToolInvoke != nil && opts.InvokeExecutor != nil && opts.ToolHost != nil {
		host.ToolInvoke.Invoke = func(callCtx context.Context, name string, args json.RawMessage) (string, error) {
			result, runErr := opts.InvokeExecutor.Run(callCtx, name, args, opts.ToolHost)
			if runErr != nil {
				return "", runErr
			}
			if result.Error != "" {
				return "", errors.New(result.Error)
			}
			return result.Content, nil
		}
	}

	app, err := pluginruntime.LoadLifecycleApplication(ctx, rt, wasmBytes, host, binding.Anchor)
	if err != nil {
		return nil, err
	}
	appTools, err := app.Tools()
	if err != nil {
		_ = app.Close(context.Background())
		return nil, fmt.Errorf("lifecycle application: tools: %w", err)
	}
	keepRuntime = true
	return &LoadedLifecycleApplication{
		Dir: pluginDir, Manifest: mf, Identity: identity, Runtime: rt,
		Application: app, Bridge: binding.Application, Controller: binding.Controller,
		Events: binding.Events, Tools: appTools,
	}, nil
}

type ApplicationWorkerRunStatus string

const (
	ApplicationWorkerRunRequested       ApplicationWorkerRunStatus = "requested"
	ApplicationWorkerRunResumeRequested ApplicationWorkerRunStatus = "resume_requested"
	ApplicationWorkerRunActive          ApplicationWorkerRunStatus = "active"
	ApplicationWorkerRunCancelled       ApplicationWorkerRunStatus = "cancelled"
	ApplicationWorkerRunCompleted       ApplicationWorkerRunStatus = "completed"
	ApplicationWorkerRunInterrupted     ApplicationWorkerRunStatus = "interrupted"
	ApplicationWorkerRunStopped         ApplicationWorkerRunStatus = "stopped"
)

type ApplicationWorkerRunConflict string

const (
	ApplicationWorkerRunRejectOperatorLoop  ApplicationWorkerRunConflict = "reject"
	ApplicationWorkerRunReplaceOperatorLoop ApplicationWorkerRunConflict = "replace_operator_loop"
)

// ApplicationWorkerRun is the runtime-neutral projection consumed by a UI.
// The broker, not the callback result, supplies every authority and version
// field. A command result names only RunID.
type ApplicationWorkerRun struct {
	SessionID        string                       `json:"session_id"`
	Generation       uint64                       `json:"generation"`
	PluginID         string                       `json:"plugin_id"`
	RunID            string                       `json:"run_id"`
	Version          uint64                       `json:"version"`
	WALSequence      uint64                       `json:"wal_sequence"`
	Objective        string                       `json:"objective"`
	Prompt           string                       `json:"prompt"`
	Conflict         ApplicationWorkerRunConflict `json:"conflict"`
	Status           ApplicationWorkerRunStatus   `json:"status"`
	TerminalReason   string                       `json:"terminal_reason,omitempty"`
	TerminalSequence uint64                       `json:"terminal_sequence,omitempty"`
}

func (a *LoadedLifecycleApplication) WorkerRun(ctx context.Context, runID string) (ApplicationWorkerRun, error) {
	return a.callWorkerRun(ctx, "worker.get", struct {
		RunID string `json:"run_id"`
	}{RunID: runID})
}

func (a *LoadedLifecycleApplication) ActivateWorkerRun(ctx context.Context, run ApplicationWorkerRun) (ApplicationWorkerRun, error) {
	return a.callWorkerRun(ctx, "worker.activate", struct {
		RunID           string `json:"run_id"`
		ExpectedVersion uint64 `json:"expected_version"`
	}{RunID: run.RunID, ExpectedVersion: run.Version})
}

func (a *LoadedLifecycleApplication) ActivateResumedWorkerRun(ctx context.Context, run ApplicationWorkerRun) (ApplicationWorkerRun, error) {
	return a.callWorkerRun(ctx, "worker.resume.activate", struct {
		RunID           string `json:"run_id"`
		ExpectedVersion uint64 `json:"expected_version"`
	}{RunID: run.RunID, ExpectedVersion: run.Version})
}

func (a *LoadedLifecycleApplication) CancelWorkerRun(ctx context.Context, run ApplicationWorkerRun, reason string) (ApplicationWorkerRun, error) {
	return a.callWorkerRun(ctx, "worker.cancel", struct {
		RunID           string `json:"run_id"`
		ExpectedVersion uint64 `json:"expected_version"`
		Reason          string `json:"reason"`
	}{RunID: run.RunID, ExpectedVersion: run.Version, Reason: reason})
}

func (a *LoadedLifecycleApplication) callWorkerRun(ctx context.Context, operation string, input any) (ApplicationWorkerRun, error) {
	if a == nil || a.Controller == nil || a.Application == nil {
		return ApplicationWorkerRun{}, errors.New("lifecycle application controller bridge is unavailable")
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return ApplicationWorkerRun{}, err
	}
	response, err := a.Controller.CallApplicationController(ctx, operation, payload)
	if err != nil {
		return ApplicationWorkerRun{}, err
	}
	var run ApplicationWorkerRun
	if err := json.Unmarshal(response, &run); err != nil {
		return ApplicationWorkerRun{}, fmt.Errorf("lifecycle application worker response: %w", err)
	}
	anchor := a.Application.Anchor
	if run.SessionID != anchor.SessionID || run.Generation != anchor.SessionGeneration || run.PluginID != a.Identity.Namespace || run.RunID == "" || run.Version == 0 || run.WALSequence == 0 || run.Objective == "" || run.Prompt == "" {
		return ApplicationWorkerRun{}, errors.New("lifecycle application worker response has invalid broker scope")
	}
	return run, nil
}

// DispatchPendingEvents delivers broker events in WAL order and advances the
// durable cursor only after the application returns an explicit ack. A trap,
// timeout, invalid response, or host cancellation leaves the event pending for
// replay after restart. Unregister is returned to the owning surface, which
// closes the instance without acknowledging further events.
func (a *LoadedLifecycleApplication) DispatchPendingEvents(ctx context.Context, limit int) (bool, error) {
	if a == nil || a.Application == nil || a.Events == nil {
		return false, nil
	}
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	events, err := a.Events.Pending(ctx, limit)
	if err != nil {
		return false, err
	}
	for _, event := range events {
		disposition, err := a.Application.DeliverEvent(ctx, event)
		if err != nil {
			return false, err
		}
		if disposition == pluginruntime.EventUnregister {
			return true, nil
		}
		if err := a.Events.Acknowledge(ctx, event.BrokerSeq); err != nil {
			return false, err
		}
	}
	return false, nil
}

// DispatchLifecycleApplicationEvents services every admitted application in
// canonical order. Closed applications fail the owning worker loop; fail-open
// applications are removed after an observable warning. This function only
// runs application callbacks. It never weakens or consumes a broker hold.
func DispatchLifecycleApplicationEvents(ctx context.Context, applications []*LoadedLifecycleApplication, limit int) error {
	for index, application := range applications {
		if application == nil || application.Application == nil {
			continue
		}
		unregister, err := application.DispatchPendingEvents(ctx, limit)
		if err == nil && !unregister {
			continue
		}
		closed := application.Manifest.Lifecycle != nil && application.Manifest.Lifecycle.Failure == "closed"
		if err != nil && closed {
			return fmt.Errorf("lifecycle application %s event dispatch: %w", application.Identity.Canonical, err)
		}
		if err != nil {
			slog.Warn("lifecycle application event dispatch failed open", "application", application.Identity.Canonical, "err", err)
		} else {
			slog.Info("lifecycle application unregistered from durable events", "application", application.Identity.Canonical)
		}
		_ = application.Close(context.Background())
		applications[index] = nil
	}
	return nil
}

// WaitForApplicationSchedule is the strict-live barrier. A leased hold pauses
// only worker dispatch: the host continues polling the broker event cursor so
// timer.due and agent completion can let the application finish its review.
// Pause and stop are terminal requests for this loop and surface immediately.
// A successful completion returns completed=true with no error; agent loops
// end normally after committing any turn already in progress.
func WaitForApplicationScheduleStatus(ctx context.Context, controller BrokerController, applications []*LoadedLifecycleApplication) (completed bool, err error) {
	const pollInterval = 250 * time.Millisecond
	for {
		status, err := SchedulingStatus(ctx, controller)
		if err != nil {
			return false, err
		}
		switch status.State {
		case "", ScheduleActive:
			return false, nil
		case ScheduleCompleted:
			return true, nil
		case SchedulePaused, ScheduleStopped:
			return false, &ScheduleBlockedError{Status: status}
		case ScheduleHeld:
			if err := DispatchLifecycleApplicationEvents(ctx, applications, 32); err != nil {
				return false, err
			}
		default:
			return false, &ScheduleBlockedError{Status: status}
		}

		wait := pollInterval
		if !status.Until.IsZero() {
			remaining := time.Until(status.Until)
			if remaining <= 0 {
				continue
			}
			if remaining < wait {
				wait = remaining
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return false, ctx.Err()
		case <-timer.C:
		}
	}
}

// WaitForApplicationSchedule preserves the error-only barrier used by callers
// that do not own an agent loop. Successful completion is not an error; the
// agent-loop path uses WaitForApplicationScheduleStatus to consume it.
func WaitForApplicationSchedule(ctx context.Context, controller BrokerController, applications []*LoadedLifecycleApplication) error {
	_, err := WaitForApplicationScheduleStatus(ctx, controller, applications)
	return err
}

// attachLifecycleHostSurface copies the generic host primitives implemented by
// a surface's tool.Host onto the persistent application host. This is the same
// contract used by ordinary plugin dispatch; it contains no application
// policy and every bridge still has its own signed capability gate.
func attachLifecycleHostSurface(host *pluginruntime.Host, surface tool.Host) {
	if host == nil || surface == nil {
		return
	}
	host.AttachToolHost(surface)
	if fleetProvider, ok := surface.(tool.AgentFleetProvider); ok {
		if fleet, ok := fleetProvider.AgentFleetBridge().(pluginruntime.FleetBridge); ok {
			host.FleetBridge = fleet
		}
	}
	if ptyProvider, ok := surface.(tool.PTYProvider); ok {
		if manager, ok := ptyProvider.PTYManager().(*pty.Manager); ok {
			host.PTYManager = manager
		}
	}
	if bridge, ok := surface.(pluginruntime.ApprovalBridge); ok {
		host.ApprovalBridge = bridge
	}
	if bridge, ok := surface.(pluginruntime.ChoiceBridge); ok {
		host.ChoiceBridge = bridge
	}
	if bridge, ok := surface.(pluginruntime.PrintBridge); ok {
		host.PrintBridge = bridge
	}
	if bridge, ok := surface.(pluginruntime.RenderBridge); ok {
		host.RenderBridge = bridge
	}
	if progress, ok := surface.(tool.ProgressEmitter); ok {
		host.Progress = progress.EmitProgress
	}
}

// Close terminates the application module before closing its private host
// runtime. It is safe to call repeatedly.
func (a *LoadedLifecycleApplication) Close(ctx context.Context) error {
	if a == nil {
		return nil
	}
	var first error
	if a.Application != nil {
		first = a.Application.Close(ctx)
	}
	if a.Runtime != nil {
		if err := a.Runtime.Close(ctx); first == nil {
			first = err
		}
	}
	return first
}
