package tui

import (
	"context"
	"errors"
	"sort"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/internal/lspfind"
	"github.com/foobarto/stado/internal/runtime"
)

// lifecycleComposition is a not-yet-installed set of application instances
// and hooks. Session transitions build this against their candidate broker
// peer and executor while the live composition keeps serving the old session.
// Only a fully admitted composition is committed by a session transition.
type lifecycleComposition struct {
	applications       []*runtime.LoadedLifecycleApplication
	commands           map[string]*runtime.LoadedLifecycleApplication
	commandConflicts   map[string]struct{}
	hooks              *hooks.LifecycleRunner
	failure            error
	admissionFailure   error
	backgroundIssues   []string
	informationalNotes []string
}

func (c *lifecycleComposition) close(ctx context.Context) {
	if c == nil {
		return
	}
	for _, application := range c.applications {
		_ = application.Close(ctx)
	}
	c.applications = nil
}

// closeLifecycleApplications tears down only EP-0064 application instances.
// Legacy tick-only background plugins have a process lifetime and are owned by
// closeBackgroundPlugins. Keeping the two lifetimes separate lets a TUI switch
// its exact session/generation binding without restarting unrelated legacy
// observers.
func (m *Model) closeLifecycleApplications(ctx context.Context) {
	m.invalidateApplicationVerificationScope()
	if len(m.lifecycleApplications) > 0 {
		m.applicationToolProjectionGeneration.Add(1)
	}
	for _, application := range m.lifecycleApplications {
		_ = application.Close(ctx)
	}
	m.lifecycleApplications = nil
	m.applicationCommands = nil
	m.applicationCommandConflicts = nil
}

// stageLifecycleApplications builds a composition without disturbing the
// currently installed applications. The caller may temporarily point the
// model at a candidate session/controller/executor before calling this; host
// bridges still capture the real Model pointer, but admission and tool
// registration use the candidate fields. No callbacks may be running while
// staging (the switch/reload busy gates enforce that invariant).
func (m *Model) stageLifecycleApplications(ctx context.Context, cfg *config.Config) (*lifecycleComposition, []string) {
	savedApplications := m.lifecycleApplications
	savedCommands := m.applicationCommands
	savedCommandConflicts := m.applicationCommandConflicts
	savedHooks := m.lifecycleHooks
	savedFailure := m.applicationFailure
	savedAdmissionFailure := m.applicationAdmissionFailure
	savedFailureSources := m.applicationFailureSources

	m.lifecycleApplications = nil
	m.applicationCommands = nil
	m.applicationCommandConflicts = nil
	m.applicationFailure = nil
	m.applicationAdmissionFailure = nil
	m.applicationFailureSources = nil
	defer func() {
		m.lifecycleApplications = savedApplications
		m.applicationCommands = savedCommands
		m.applicationCommandConflicts = savedCommandConflicts
		m.lifecycleHooks = savedHooks
		m.applicationFailure = savedFailure
		m.applicationAdmissionFailure = savedAdmissionFailure
		m.applicationFailureSources = savedFailureSources
	}()

	base, warnings := hooks.BuildLifecycleRunnerWithWarnings(cfg)
	m.lifecycleHooks = base
	composition := &lifecycleComposition{hooks: base}
	if cfg == nil {
		return composition, warnings
	}

	pluginRoots := cfg.AllPluginDirs()
	for _, id := range effectiveBackgroundPluginIDs(cfg, m.currentPersonaPluginIDs()) {
		if _, legacyBundled := runtime.LookupBackgroundPlugin(id); legacyBundled {
			continue
		}
		application, recognized, note := m.loadOneLifecycleApplication(ctx, cfg, pluginRoots, id)
		if !recognized {
			continue
		}
		if application == nil {
			if note == "" {
				note = "configured lifecycle application could not be admitted"
			}
			composition.backgroundIssues = append(composition.backgroundIssues, note)
			warnings = append(warnings, note)
			if m.applicationAdmissionFailure == nil {
				m.applicationAdmissionFailure = errors.New(note)
				m.applicationFailure = m.applicationAdmissionFailure
			}
		} else if note != "" {
			composition.informationalNotes = append(composition.informationalNotes, note)
		}
	}

	sortLifecycleApplications(m.lifecycleApplications)
	for _, application := range m.lifecycleApplications {
		m.lifecycleHooks = m.lifecycleHooks.Append(application.Application)
	}
	// Native LSP diagnostics are fact-only and remain last in the chain.
	if cfg.LSP.AutoDiagnostics && m.lspManager != nil && m.lspDiagnostics != nil {
		m.lifecycleHooks = m.lifecycleHooks.Append(lspfind.NewDiagnosticsHook(m.lspManager, m.lspDiagnostics, m.cwd))
	}
	composition.applications = m.lifecycleApplications
	composition.commands = m.applicationCommands
	composition.commandConflicts = m.applicationCommandConflicts
	composition.hooks = m.lifecycleHooks
	composition.failure = m.applicationFailure
	composition.admissionFailure = m.applicationAdmissionFailure
	return composition, warnings
}

func sortLifecycleApplications(applications []*runtime.LoadedLifecycleApplication) {
	sort.SliceStable(applications, func(i, j int) bool {
		return applications[i].Identity.Canonical < applications[j].Identity.Canonical
	})
}

// installLifecycleComposition is the commit half of staging. The new fields
// become visible before old applications are closed, so there is no interval
// in which a successfully admitted quality gate is silently absent.
func (m *Model) installLifecycleComposition(ctx context.Context, composition *lifecycleComposition) {
	if composition == nil {
		return
	}
	// Cancel native verification before changing the session/application
	// composition. The command goroutine holds only this immutable scope and
	// the old session pointer; it never races by consulting mutable Model fields.
	m.replaceApplicationVerificationScope()
	oldApplications := m.lifecycleApplications
	// Provider turns bind lifecycle tools to exact module pointers. Every
	// composition commit invalidates that projection even when a replacement
	// verifies to the same canonical package identity and tool names.
	m.applicationToolProjectionGeneration.Add(1)
	m.lifecycleApplications = composition.applications
	m.applicationCommands = composition.commands
	m.applicationCommandConflicts = composition.commandConflicts
	m.lifecycleHooks = composition.hooks
	m.applicationFailure = composition.failure
	m.applicationAdmissionFailure = composition.admissionFailure
	if persistent := m.persistentApplicationFailure(); persistent != nil {
		m.applicationFailure = persistent
	}
	if m.loop != nil && m.loop.application != nil {
		if replacement := m.lifecycleApplicationForWorkerRun(m.loop.workerRun); replacement != nil {
			m.loop.application = replacement
		} else {
			m.loop = nil
			m.setApplicationFailureSource(applicationFailureWorkerRecovery, errors.New("active application worker run lost its admitted lifecycle application during rebind"))
		}
	}
	if m.executor != nil {
		m.executor.Hooks = m.lifecycleHooks
	}
	for _, note := range composition.backgroundIssues {
		m.recordBackgroundPluginIssue(note)
	}
	for _, note := range composition.informationalNotes {
		m.pushLogLine(note)
	}
	composition.applications = nil
	for _, application := range oldApplications {
		_ = application.Close(ctx)
	}
}

func (m *Model) applicationVerificationScope() (context.Context, uint64) {
	if m.applicationVerificationContext == nil {
		m.replaceApplicationVerificationScope()
	}
	return m.applicationVerificationContext, m.applicationVerificationGeneration
}

func (m *Model) replaceApplicationVerificationScope() {
	if m.applicationVerificationCancel != nil {
		m.applicationVerificationCancel()
	}
	parent := m.rootCtx
	if parent == nil {
		parent = context.Background()
	}
	m.applicationVerificationGeneration++
	m.applicationVerificationContext, m.applicationVerificationCancel = context.WithCancel(parent)
	// Any running poll belongs to the cancelled generation. A new composition
	// may schedule its own poll immediately; the old result carries its captured
	// generation and cannot clear or overwrite that new poll.
	m.applicationPollRunning = false
	m.applicationPollGeneration = 0
}

func (m *Model) invalidateApplicationVerificationScope() {
	if m.applicationVerificationCancel != nil {
		m.applicationVerificationCancel()
	}
	m.applicationVerificationGeneration++
	m.applicationVerificationContext = nil
	m.applicationVerificationCancel = nil
	m.applicationPollRunning = false
	m.applicationPollGeneration = 0
}

// rebindLifecycleApplications constructs one fresh composition for the
// Model's current session, broker controller, and executor. The caller must
// have stopped all application callbacks and installed a fresh tool registry
// first. A configured application that cannot be admitted blocks provider
// dispatch; falling back to a legacy/background path would silently discard
// its quality gate.
func (m *Model) rebindLifecycleApplications(ctx context.Context, cfg *config.Config) []string {
	composition, warnings := m.stageLifecycleApplications(ctx, cfg)
	m.installLifecycleComposition(ctx, composition)
	return warnings
}

func (m *Model) currentPersonaPluginIDs() []string {
	if m.persona == nil {
		return nil
	}
	return append([]string(nil), m.persona.Plugins...)
}
