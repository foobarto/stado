package tui

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/instructions"
	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/plugins/runtime/pty"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/skills"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/subagent"
	"github.com/foobarto/stado/internal/textutil"
	"github.com/foobarto/stado/internal/toolinput"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/foobarto/stado/pkg/tool"
)

// LoadBackgroundPlugins instantiates every plugin listed in
// cfg.Plugins.Background plus the default bundled auto-compact plugin
// as persistent (tickable) plugins. A single failing plugin surfaces
// as a system block in the TUI but doesn't abort the others.
func (m *Model) LoadBackgroundPlugins(cfg *config.Config) {
	// Per-persona plugins (2026-06-13): the active-at-launch persona's
	// `plugins:` are added to the background set. LAUNCH-ONLY — a live
	// /persona switch does NOT start/stop background plugins (the lifecycle
	// is session-start-only; LoadBackgroundPlugins runs once at boot, after
	// initPersona). A nil persona contributes nothing.
	var personaPlugins []string
	if m.persona != nil {
		personaPlugins = m.persona.Plugins
	}
	ids := effectiveBackgroundPluginIDs(cfg, personaPlugins)
	if len(ids) == 0 {
		return
	}
	ctx := m.rootCtx
	if ctx == nil {
		ctx = context.Background()
	}
	rt, err := pluginRuntime.New(ctx)
	if err != nil {
		m.appendBlock(block{kind: "system", body: "background plugins: runtime: " + err.Error()})
		return
	}
	m.bgPluginRuntime = rt

	pluginRoots := cfg.AllPluginDirs() // EP-0035: global + project .stado/plugins/
	for _, id := range ids {
		if _, bundled := runtime.LookupBackgroundPlugin(id); !bundled {
			app, recognized, note := m.loadOneLifecycleApplication(ctx, cfg, pluginRoots, id)
			if recognized {
				if note != "" {
					if app == nil {
						m.recordBackgroundPluginIssue(note)
						m.appendBlock(block{kind: "system", body: note})
						if m.applicationAdmissionFailure == nil {
							m.applicationAdmissionFailure = errors.New(note)
							m.applicationFailure = m.applicationAdmissionFailure
						}
					} else {
						slog.Info(note)
					}
				}
				continue
			}
		}
		bp, note := m.loadOneBackground(ctx, rt, cfg, pluginRoots, id)
		if note != "" {
			if bp != nil {
				slog.Info(note)
			} else {
				m.recordBackgroundPluginIssue(note)
				m.appendBlock(block{kind: "system", body: note})
			}
		}
		if bp != nil {
			m.backgroundPlugins = append(m.backgroundPlugins, bp)
		}
	}
	// EP-0064 ordering is source-independent and stable across config edits:
	// operator Lua hooks were installed before this loader, then enabled WASM
	// applications run by canonical signed identity. Native fact-only observers
	// (currently LSP diagnostics) are appended by the caller afterwards.
	sortLifecycleApplications(m.lifecycleApplications)
	for _, application := range m.lifecycleApplications {
		m.lifecycleHooks = m.lifecycleHooks.Append(application.Application)
	}
	if m.executor != nil {
		m.executor.Hooks = m.lifecycleHooks
	}
}

// loadOneLifecycleApplication recognizes a manifest-declared EP-0064
// application and routes it through the generic verified loader. The bool says
// whether the ID was an application; once true, callers must never fall back to
// the weaker legacy background loader after an admission failure.
func (m *Model) loadOneLifecycleApplication(ctx context.Context, cfg *config.Config, pluginRoots []string, id string) (*runtime.LoadedLifecycleApplication, bool, string) {
	pkg, err := plugins.ResolveInstalledPackage(pluginRoots, id)
	if err != nil {
		return nil, false, ""
	}
	dir, mf := pkg.Dir, &pkg.Manifest
	if mf.Lifecycle == nil {
		return nil, false, ""
	}
	identity := pkg.Identity
	if previous := lifecycleApplicationByCanonical(m.lifecycleApplications, identity.Canonical); previous != nil {
		return nil, true, fmt.Sprintf("lifecycle application %s duplicates canonical identity %s; one session may own exactly one instance", id, identity.Canonical)
	}
	brokerController, ok := m.broker.(runtime.ApplicationBrokerController)
	if !ok {
		return nil, true, fmt.Sprintf("lifecycle application %s: broker admission unavailable", id)
	}
	host := m.newPluginHostAdapter(*mf)
	loaded, err := runtime.LoadInstalledLifecycleApplication(ctx, dir, runtime.LifecycleApplicationLoadOptions{
		Config: cfg, Broker: brokerController, Workdir: m.cwd, ToolHost: host,
		InvokeExecutor: m.executor,
		ConfigureHost: func(runtimeHost *pluginRuntime.Host) {
			runtimeHost.ApprovalBridge = host
			runtimeHost.ChoiceBridge = host
			runtimeHost.PrintBridge = host
			runtimeHost.RenderBridge = host
			runtimeHost.FleetBridge = host.fleetBridge
			runtimeHost.PTYManager = host.pty
			runtimeHost.Progress = func(plugin, text string) { host.EmitProgress(plugin, text) }
			if bridge := m.buildPluginBridge(runtimeHost.Identity.Canonical); bridge != nil {
				runtimeHost.SessionBridge = bridge
			}
		},
	})
	if err != nil {
		return nil, true, fmt.Sprintf("lifecycle application %s: %v", id, err)
	}
	// Defense in depth if identity resolution changes between classification
	// and verified load; no second instance is projected to commands or tools.
	if previous := lifecycleApplicationByCanonical(m.lifecycleApplications, loaded.Identity.Canonical); previous != nil {
		_ = loaded.Close(context.Background())
		return nil, true, fmt.Sprintf("lifecycle application %s duplicates canonical identity %s; one session may own exactly one instance", id, loaded.Identity.Canonical)
	}
	if err := m.admitLoadedLifecycleApplication(loaded); err != nil {
		_ = loaded.Close(context.Background())
		return nil, true, fmt.Sprintf("lifecycle application %s: %v", id, err)
	}
	return loaded, true, fmt.Sprintf("lifecycle application %s loaded as %s", id, loaded.Identity.Canonical)
}

// admitLoadedLifecycleApplication is the atomic projection half of staged
// loading. It validates every operator command before registering any tools,
// hooks, or routes. A duplicate application claim invalidates the already
// staged route as well as the new claimant, so configuration order cannot
// select an owner.
func (m *Model) admitLoadedLifecycleApplication(loaded *runtime.LoadedLifecycleApplication) error {
	for _, command := range loaded.Manifest.Commands {
		fullName := "/" + command.Name
		if IsReservedSlashName(fullName) {
			return fmt.Errorf("command %s conflicts with a native operator command", fullName)
		}
		if skillName := m.skillSlash[command.Name]; skillName != "" {
			return fmt.Errorf("command %s conflicts with skill %s", fullName, skillName)
		}
		if _, conflicted := m.applicationCommandConflicts[command.Name]; conflicted {
			return fmt.Errorf("command /%s has multiple application owners", command.Name)
		}
		if previous := m.applicationCommands[command.Name]; previous != nil {
			m.recordApplicationCommandConflict(command.Name)
			return fmt.Errorf("command /%s conflicts with %s", command.Name, previous.Identity.Canonical)
		}
	}
	if err := m.projectLifecycleApplication(loaded); err != nil {
		return err
	}
	return nil
}

// recordApplicationCommandConflict makes duplicate lifecycle-application
// ownership independent of configuration order. A failed second admission
// must not leave the first claimant callable: the provider barrier alone does
// not guard operator commands, aliases, or dynamic discovery.
func (m *Model) recordApplicationCommandConflict(name string) {
	delete(m.applicationCommands, name)
	if m.applicationCommandConflicts == nil {
		m.applicationCommandConflicts = make(map[string]struct{})
	}
	m.applicationCommandConflicts[name] = struct{}{}
}

// lifecycleApplicationByCanonical enforces EP-0064's exact-one identity
// invariant independently of the configured install alias. Two aliases that
// verify to the same signed canonical identity must never produce two modules.
func lifecycleApplicationByCanonical(applications []*runtime.LoadedLifecycleApplication, canonical string) *runtime.LoadedLifecycleApplication {
	for _, application := range applications {
		if application != nil && application.Identity.Canonical == canonical {
			return application
		}
	}
	return nil
}

// projectLifecycleApplication exposes every entry point from one loaded
// object. Commands retain this exact pointer, hooks/events consume its
// Application, and registered tools are the adapters already bound to that
// application's serialization gate by the runtime loader.
func (m *Model) projectLifecycleApplication(loaded *runtime.LoadedLifecycleApplication) error {
	if loaded == nil {
		return nil
	}
	// Admission is atomic. A lifecycle application's persistent adapter may
	// replace only the ordinary installed adapter derived from the same exact
	// package namespace. Native, bundled, override, and other-application name
	// collisions fail before any registry or command route changes.
	projected := make([]tool.Tool, 0, len(loaded.ModelTools))
	if m.executor != nil && m.executor.Registry != nil {
		seen := make(map[string]bool, len(loaded.ModelTools))
		for _, applicationTool := range loaded.ModelTools {
			if applicationTool == nil || !runtime.ToolPermittedByConfig(applicationTool.Name(), m.cfg) {
				continue
			}
			if seen[applicationTool.Name()] {
				return fmt.Errorf("tool %q is declared more than once", applicationTool.Name())
			}
			seen[applicationTool.Name()] = true
			if existing, ok := m.executor.Registry.Get(applicationTool.Name()); ok && existing != applicationTool {
				metadata := runtime.ToolMetadataFor(existing)
				if metadata.PackageNamespace != loaded.Identity.Namespace || metadata.LifecycleApplication != "" {
					return fmt.Errorf("tool %q conflicts with an existing registry owner", applicationTool.Name())
				}
			}
			projected = append(projected, applicationTool)
		}
		for _, applicationTool := range projected {
			m.executor.Registry.Register(applicationTool)
		}
	}
	m.lifecycleApplications = append(m.lifecycleApplications, loaded)
	if len(loaded.Manifest.Commands) > 0 && m.applicationCommands == nil {
		m.applicationCommands = make(map[string]*runtime.LoadedLifecycleApplication)
	}
	for _, command := range loaded.Manifest.Commands {
		m.applicationCommands[command.Name] = loaded
	}
	m.applicationToolProjectionGeneration.Add(1)
	return nil
}

// effectiveBackgroundPluginIDs is the launch-time background-plugin set:
// bundled defaults, then cfg.Plugins.Background, then the active-at-launch
// persona's `plugins:` — unioned and deduped, in that precedence order.
// personaPlugins is launch-only (a live /persona switch never re-runs this).
func effectiveBackgroundPluginIDs(cfg *config.Config, personaPlugins []string) []string {
	var ids []string
	seen := map[string]struct{}{}
	add := func(list []string) {
		for _, id := range list {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	add(runtime.DefaultBackgroundPlugins())
	if cfg != nil {
		add(cfg.Plugins.Background)
	}
	add(personaPlugins)
	return ids
}

// loadOneBackground reads + verifies + instantiates a single plugin.
// Returns (plugin, advisory) — advisory is non-empty on load
// failure OR on successful load so the user knows the plugin is
// active. nil plugin signals "skip this one."
func (m *Model) loadOneBackground(ctx context.Context, rt *pluginRuntime.Runtime, cfg *config.Config, pluginRoots []string, id string) (*pluginRuntime.BackgroundPlugin, string) {
	if bundled, ok := runtime.LookupBackgroundPlugin(id); ok {
		identity, err := plugins.RuntimeIdentityForBundledSource(bundled.ID, bundled.Manifest)
		if err != nil {
			return nil, fmt.Sprintf("background plugin %s: identity: %v", bundled.ID, err)
		}
		host := pluginRuntime.NewHostWithIdentity(bundled.Manifest, identity, "", nil)
		host.ApprovalBridge = tuiApprovalBridge{model: m}
		if bridge := m.buildPluginBridge(identity.Canonical); bridge != nil {
			host.SessionBridge = bridge
		}
		bp, err := pluginRuntime.LoadBackgroundPlugin(ctx, rt, bundled.WASM, host)
		if err != nil {
			return nil, fmt.Sprintf("background plugin %s: load: %v", bundled.ID, err)
		}
		return bp, fmt.Sprintf("background plugin %s loaded (bundled default)", bundled.ID)
	}
	if id == "." || id == ".." || strings.Contains(id, "../") || strings.Contains(id, `..\`) ||
		strings.ContainsRune(id, '\x00') || filepath.IsAbs(id) {
		return nil, fmt.Sprintf("background plugin %s: invalid plugin id", id)
	}

	pkg, err := plugins.ResolveInstalledPackage(pluginRoots, id)
	if err != nil {
		return nil, fmt.Sprintf("background plugin %s: %v", id, err)
	}
	dir, mf, sig := pkg.Dir, &pkg.Manifest, pkg.Signature
	if mf.Lifecycle != nil {
		return nil, fmt.Sprintf("background plugin %s: lifecycle manifests require the persistent TUI application loader and cannot use the legacy BackgroundPlugin path", id)
	}
	wasmPath := filepath.Join(dir, "plugin.wasm")
	wasmBytes, err := plugins.ReadVerifiedWASM(mf.WASMSHA256, wasmPath)
	if err != nil {
		return nil, fmt.Sprintf("background plugin %s: digest mismatch: %v", id, err)
	}
	identity := pkg.Identity
	if cfg != nil {
		if err := runtime.VerifyInstalledPlugin(ctx, cfg, dir, mf, sig); err != nil {
			return nil, fmt.Sprintf("background plugin %s: signature: %v", id, err)
		}
	}
	host := pluginRuntime.NewHostWithIdentity(*mf, identity, dir, nil)
	host.ApprovalBridge = tuiApprovalBridge{model: m}
	if bridge := m.buildPluginBridge(identity.Canonical); bridge != nil {
		host.SessionBridge = bridge
	}
	bp, err := pluginRuntime.LoadBackgroundPlugin(ctx, rt, wasmBytes, host)
	if err != nil {
		return nil, fmt.Sprintf("background plugin %s: load: %v", id, err)
	}
	return bp, fmt.Sprintf("background plugin %s loaded (ticking on every turn boundary)", id)
}

// tickBackgroundPluginsWithEvent pushes one event onto every loaded
// background plugin's bridge, refreshes the bridge to the current
// session snapshot, then invokes stado_plugin_tick. Returns a tea.Cmd
// because the ticks run in a goroutine so a slow plugin can't freeze
// the UI. Plugins returning non-zero are dropped from the active set.
func (m *Model) tickBackgroundPluginsWithEvent(payload []byte) tea.Cmd {
	if len(m.backgroundPlugins) == 0 {
		return nil
	}
	if m.backgroundTickRunning {
		m.backgroundTickQueued = true
		m.backgroundTickPayload = append([]byte(nil), payload...)
		return nil
	}
	m.backgroundTickRunning = true
	active := m.backgroundPlugins
	return func() tea.Msg {
		survivors := active[:0:len(active)]
		var issues []string
		for _, bp := range active {
			if bp.Host != nil {
				if bridge := m.buildPluginBridge(bp.Host.Identity.Canonical); bridge != nil {
					bp.Host.SessionBridge = bridge
					bridge.Emit(payload)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			unregister, err := bp.Tick(ctx)
			cancel()
			if err != nil || unregister {
				name := "plugin"
				if bp != nil && strings.TrimSpace(bp.Name()) != "" {
					name = bp.Name()
				}
				if err != nil {
					issues = append(issues, fmt.Sprintf("%s tick: %v", name, err))
				} else {
					issues = append(issues, fmt.Sprintf("%s unregistered itself during tick", name))
				}
				_ = bp.Close(context.Background())
				continue
			}
			survivors = append(survivors, bp)
		}
		return backgroundTickResultMsg{survivors: survivors, issues: issues}
	}
}

// applicationTurnBoundary publishes one generic broker-stamped host-fact
// event and services the signed application queue before returning control to
// Bubble Tea. Because the command does not complete while a broker hold is
// active, a lifecycle application cannot race the next provider dispatch.
func (m *Model) applicationTurnBoundary(results []agent.ToolResultBlock, continuation applicationBoundaryContinuation) tea.Cmd {
	if len(m.lifecycleApplications) == 0 || m.session == nil {
		return nil
	}
	publisher, ok := m.broker.(runtime.ApplicationEventPublisher)
	if !ok || publisher == nil {
		return func() tea.Msg {
			return applicationBoundaryMsg{continuation: continuation, err: errors.New("configured lifecycle applications require broker event publication")}
		}
	}
	applications := append([]*runtime.LoadedLifecycleApplication(nil), m.lifecycleApplications...)
	for _, app := range applications {
		if app != nil && app.Application != nil {
			if bridge := m.buildPluginBridge(app.Identity.Canonical); bridge != nil {
				app.Application.Host.SessionBridge = bridge
			}
		}
	}
	input := runtime.TurnCommitInput{
		Iteration: m.applicationIteration, TreeBefore: m.turnTreeBefore,
		ProviderName: m.turnProvider, Model: m.turnModel, Usage: m.turnUsage,
		CumulativeTokens: m.totalTokens(), TokenLimit: m.budgetHardTokens,
		Text: m.turnText, Calls: append([]agent.ToolUseBlock(nil), m.turnToolCalls...),
		Results: append([]agent.ToolResultBlock(nil), results...), Duration: time.Since(m.turnStart),
	}
	if m.executor != nil && m.executor.Registry != nil {
		input.ToolClasses = make(map[string]string, len(input.Calls))
		for _, call := range input.Calls {
			input.ToolClasses[call.Name] = m.executor.Registry.ClassOf(call.Name).String()
		}
	}
	session := m.session
	controller := m.broker
	executor := m.executor
	verificationHost := hostAdapter{workdir: m.cwd, executorSandbox: m.executorSandbox}
	if executor != nil {
		verificationHost.readLog = executor.ReadLog
		verificationHost.runner = executor.Runner
	}
	verificationConfig := m.verifyConfig
	verificationPump := &m.applicationVerificationMu
	verificationScope, verificationGeneration := m.applicationVerificationScope()
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(verificationScope, 30*time.Minute)
		defer cancel()
		_, err := runtime.PublishSessionTurnCommitted(ctx, publisher, session, input)
		published := err == nil
		if err == nil {
			err = runtime.DispatchLifecycleApplicationEvents(ctx, applications, 32)
		}
		if err == nil {
			var verified int
			verified, err = serviceApplicationVerificationsSerialized(verificationPump, ctx, applications, executor, controller, verificationHost, verificationConfig, applicationVerificationAnchorForSession(session))
			if err == nil && verified > 0 {
				// Terminal verification records synthesize targeted durable events.
				// Deliver them before ticking so an application can release its own
				// scheduling hold without racing another worker dispatch.
				err = runtime.DispatchLifecycleApplicationEvents(ctx, applications, 32)
			}
		}
		if err == nil {
			for index, app := range applications {
				if app == nil || app.Application == nil {
					continue
				}
				unregister, tickErr := app.Application.Tick(ctx)
				if tickErr == nil && !unregister {
					continue
				}
				closed := app.Manifest.Lifecycle != nil && app.Manifest.Lifecycle.Failure == "closed"
				if tickErr != nil && closed {
					err = fmt.Errorf("lifecycle application %s tick: %w", app.Identity.Canonical, tickErr)
					break
				}
				_ = app.Close(context.Background())
				applications[index] = nil
			}
		}
		completed := false
		if err == nil {
			completed, err = runtime.WaitForApplicationScheduleStatus(ctx, controller, applications)
		}
		return applicationBoundaryMsg{continuation: continuation, applications: applications, published: published, completed: completed, err: err, generation: verificationGeneration}
	}
}

func applicationPollTickCmd() tea.Cmd {
	return tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return applicationPollTickMsg{} })
}

func (m *Model) pollLifecycleApplicationEvents() tea.Cmd {
	if m.applicationPollRunning {
		return nil
	}
	applications := append([]*runtime.LoadedLifecycleApplication(nil), m.lifecycleApplications...)
	if len(applications) == 0 {
		return func() tea.Msg { return applicationPollResultMsg{} }
	}
	for _, app := range applications {
		if app != nil && app.Application != nil {
			if bridge := m.buildPluginBridge(app.Identity.Canonical); bridge != nil {
				app.Application.Host.SessionBridge = bridge
			}
		}
	}
	executor := m.executor
	verificationHost := hostAdapter{workdir: m.cwd, executorSandbox: m.executorSandbox}
	if executor != nil {
		verificationHost.readLog = executor.ReadLog
		verificationHost.runner = executor.Runner
	}
	verificationConfig := m.verifyConfig
	verificationSession := m.session
	verificationController := m.broker
	verificationPump := &m.applicationVerificationMu
	verificationScope, verificationGeneration := m.applicationVerificationScope()
	m.applicationPollRunning = true
	m.applicationPollGeneration = verificationGeneration
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(verificationScope, 30*time.Minute)
		defer cancel()
		err := runtime.DispatchLifecycleApplicationEvents(ctx, applications, 32)
		verified := 0
		if err == nil {
			verified, err = serviceApplicationVerificationsSerialized(verificationPump, ctx, applications, executor, verificationController, verificationHost, verificationConfig, applicationVerificationAnchorForSession(verificationSession))
		}
		if err == nil && verified > 0 {
			err = runtime.DispatchLifecycleApplicationEvents(ctx, applications, 32)
		}
		if err == nil {
			for index, app := range applications {
				if app == nil || app.Application == nil {
					continue
				}
				unregister, tickErr := app.Application.Tick(ctx)
				if tickErr == nil && !unregister {
					continue
				}
				closed := app.Manifest.Lifecycle != nil && app.Manifest.Lifecycle.Failure == "closed"
				if tickErr != nil && closed {
					err = fmt.Errorf("lifecycle application %s tick after verification: %w", app.Identity.Canonical, tickErr)
					break
				}
				_ = app.Close(context.Background())
				applications[index] = nil
			}
		}
		return applicationPollResultMsg{applications: applications, err: err, generation: verificationGeneration}
	}
}

func (m *Model) closeBackgroundPlugins(ctx context.Context) {
	m.closeLifecycleApplications(ctx)
	for _, bp := range m.backgroundPlugins {
		_ = bp.Close(ctx)
	}
	m.backgroundPlugins = nil
	if m.bgPluginRuntime != nil {
		_ = m.bgPluginRuntime.Close(ctx)
		m.bgPluginRuntime = nil
	}
	// Reap any PTYs opened by bundled shell.* / pty.* tools during
	// the session — without this they'd outlive the TUI process.
	if m.ptyManager != nil {
		m.ptyManager.CloseAll()
		m.ptyManager = nil
	}
	// Reap any LSP servers the session launched (post-edit diagnostics /
	// the *ViaManager tool seams) — without this gopls / pyright would
	// outlive the TUI process.
	if m.lspManager != nil {
		m.lspManager.CloseAll()
		m.lspManager = nil
	}
}

// backgroundTickResultMsg carries the post-tick surviving plugin
// list back to the UI goroutine so the assignment to m.backgroundPlugins
// happens under the bubbletea event loop rather than racing with it.
type backgroundTickResultMsg struct {
	survivors []*pluginRuntime.BackgroundPlugin
	issues    []string
}

type applicationCommandResultMsg struct {
	name        string
	application *runtime.LoadedLifecycleApplication
	result      pluginRuntime.CommandResult
	err         error
}

func (m *Model) recordBackgroundPluginIssue(issue string) {
	issue = strings.TrimSpace(issue)
	if issue == "" {
		return
	}
	m.backgroundPluginIssues = append(m.backgroundPluginIssues, issue)
	const maxBackgroundPluginIssues = 3
	if len(m.backgroundPluginIssues) > maxBackgroundPluginIssues {
		m.backgroundPluginIssues = append([]string(nil), m.backgroundPluginIssues[len(m.backgroundPluginIssues)-maxBackgroundPluginIssues:]...)
	}
}

type tuiApprovalBridge struct {
	model *Model
}

func (b tuiApprovalBridge) RequestApproval(ctx context.Context, title, body string) (bool, error) {
	if b.model == nil {
		return false, errors.New("approval UI unavailable")
	}
	return b.model.requestPluginApproval(ctx, title, body)
}

// tuiChoiceBridge implements pluginRuntime.ChoiceBridge over the TUI
// model's drawer. Single-flight: the bridge serialises requests via
// the model's pluginChoiceRequestMsg path, and the model rejects a
// second concurrent request before it ever lands here. Q3.
type tuiChoiceBridge struct {
	model *Model
}

func (b tuiChoiceBridge) RequestChoice(ctx context.Context, req pluginRuntime.ChoiceRequest) (pluginRuntime.ChoiceResponse, error) {
	if b.model == nil {
		return pluginRuntime.ChoiceResponse{}, errors.New("choice UI unavailable")
	}
	return b.model.requestPluginChoice(ctx, req)
}

// tuiPrintBridge implements pluginRuntime.PrintBridge over the TUI
// model. Fire-and-forget: posts a pluginPrintMsg into the program
// loop and returns nil. The model's Update handler appends a
// system-styled block carrying the text + severity. Drop-on-floor
// when m.program is nil so test models without a live tea.Program
// don't deadlock waiting on a sendMsg into a missing program.
// F9a.
type tuiPrintBridge struct {
	model *Model
}

func (b tuiPrintBridge) Print(_ context.Context, text string, opts pluginRuntime.PrintOpts) error {
	if b.model == nil || b.model.program == nil {
		return nil
	}
	b.model.sendMsg(pluginPrintMsg{text: text, opts: opts})
	return nil
}

// tuiRenderBridge implements pluginRuntime.RenderBridge over the TUI
// model. Fire-and-forget: posts a pluginRenderMsg carrying the
// decoded Panel into the program loop and returns nil. The model's
// Update handler renders the panel to ASCII (bordered per body kind)
// and appends it as a system block. Same drop-on-nil-program guard
// the print bridge uses, for the same reason. Spec: F9b.2
// (.agent/specs/open/f9b-ui-render.md).
type tuiRenderBridge struct {
	model *Model
}

func (b tuiRenderBridge) Render(_ context.Context, panel pluginRuntime.Panel) error {
	if b.model == nil || b.model.program == nil {
		return nil
	}
	b.model.sendMsg(pluginRenderMsg{panel: panel})
	return nil
}

func (m *Model) turnCompleteEvent() []byte {
	turn := 0
	if m.session != nil {
		turn = m.session.Turn()
	}
	return []byte(fmt.Sprintf(`{"kind":"turn_complete","turn":%d}`, turn))
}

func (m *Model) contextOverflowEvent(prompt string) []byte {
	return []byte(fmt.Sprintf(
		`{"kind":"context_overflow","turn":%d,"prompt":%q,"fraction":%.4f,"hard_threshold":%.4f}`,
		m.currentTurnNumber(), prompt, m.contextFraction(), m.ctxHardThreshold,
	))
}

func (m *Model) currentTurnNumber() int {
	if m.session == nil {
		return 0
	}
	return m.session.Turn()
}

func (m *Model) hasAutoCompactBackgroundPlugin() bool {
	return m.autoCompactBackgroundPluginIdentity() != ""
}

func (m *Model) autoCompactBackgroundPluginIdentity() string {
	for _, bp := range m.backgroundPlugins {
		if bp != nil && bp.Name() == "auto-compact" {
			if bp.Host != nil && strings.TrimSpace(bp.Host.Identity.Canonical) != "" {
				return bp.Host.Identity.Canonical
			}
			return bp.Name()
		}
	}
	return ""
}

// hostAdapter implements tool.Host for the executor goroutine. Tool calls
// themselves are yolo by default; the adapter only exposes an explicit
// approval bridge for plugins that request it.
// readLog delegates PriorRead/RecordRead to the Executor's shared log so
// the read tool can dedup across a session's turns.
type hostAdapter struct {
	workdir     string
	readLog     *tools.ReadLog
	runner      sandbox.Runner
	approval    tuiApprovalBridge
	choice      tuiChoiceBridge
	print       tuiPrintBridge  // F9a — stado_ui_print routing
	render      tuiRenderBridge // F9b.2 — stado_ui_render routing
	spawn       func(context.Context, subagent.Request) (subagent.Result, error)
	fleetBridge pluginRuntime.FleetBridge
	// progress receives stado_progress emissions from bundled wasm
	// plugins. Wired so the TUI surfaces progress lines in the
	// sidebar log tail. EP-0038h.
	progress        func(plugin, text string)
	executorSandbox runtime.ExecutorSandbox
	broker          runtime.BrokerController
	provider        agent.Provider
	defaultModel    string

	// pty is the TUI-session-lifetime PTY manager shared across every
	// bundled-plugin tool dispatch. Without this each call would
	// build a fresh pluginRuntime with its own pty.NewManager(), and
	// shell.spawn → shell.read/write across calls would fail with
	// "session not found." Bug-fix per operator report.
	pty *pty.Manager
}

// modelToolSurfaceController is the atomic TUI implementation of the generic
// session-surface primitive. Allows describes only the immutable/current
// ceiling, not whether a tool is presently active, so deactivate never makes
// a permitted tool impossible to reactivate.
type modelToolSurfaceController struct {
	model   *Model
	ceiling map[string]bool
}

func newModelToolSurfaceController(model *Model) modelToolSurfaceController {
	controller := modelToolSurfaceController{model: model, ceiling: make(map[string]bool)}
	if model == nil || model.executor == nil || model.executor.Registry == nil {
		return controller
	}
	for _, candidate := range model.executor.Registry.Snapshot().Tools {
		controller.ceiling[candidate.Name()] = true
	}
	return controller
}

func (c modelToolSurfaceController) AllowsToolSurface(name string) bool {
	return c.model != nil && c.ceiling[name] && !c.model.sessionToolOverrideHidesTool(name)
}

func (c modelToolSurfaceController) ApplyToolSurface(edit tool.ToolSurfaceEdit) error {
	if c.model == nil {
		return errors.New("session tool surface unavailable")
	}
	seen := make(map[string]string, len(edit.Activate)+len(edit.Deactivate))
	for _, group := range []struct {
		label string
		names []string
	}{{"activate", edit.Activate}, {"deactivate", edit.Deactivate}} {
		for _, name := range group.names {
			if !c.AllowsToolSurface(name) {
				return fmt.Errorf("tool %q is outside the session ceiling", name)
			}
			if prior := seen[name]; prior != "" {
				return fmt.Errorf("tool %q occurs more than once (%s, %s)", name, prior, group.label)
			}
			seen[name] = group.label
		}
	}
	c.model.activatedToolsMu.Lock()
	defer c.model.activatedToolsMu.Unlock()
	if c.model.activatedTools == nil {
		c.model.activatedTools = make(map[string]bool)
	}
	for _, name := range edit.Activate {
		c.model.activatedTools[name] = true
	}
	for _, name := range edit.Deactivate {
		delete(c.model.activatedTools, name)
	}
	return nil
}

// newPluginHostAdapter builds the one session-scoped host surface shared by
// ordinary plugin tools and persistent lifecycle applications. Keeping this in
// one constructor prevents applications from silently receiving a different
// sandbox ceiling, broker, retained-agent fleet, or UI contract than tools.
func (m *Model) newPluginHostAdapter(manifests ...plugins.Manifest) hostAdapter {
	rootCtx := m.rootCtx
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	// Persistent applications are admitted before the TUI's first model turn,
	// while ordinary provider construction is lazy. An application that signed
	// agent/provider authority must nevertheless be able to use that authority
	// as its first command (for example, a read-only baseline child). Build an
	// explicitly configured provider at admission for only those manifests;
	// unrelated applications retain the lazy startup contract.
	if m.provider == nil && m.providerName != "" && m.buildProvider != nil {
		needsProvider := false
		for _, manifest := range manifests {
			for _, capability := range manifest.Capabilities {
				if capability == "agent:spawn" || strings.HasPrefix(capability, "provider:invoke:") {
					needsProvider = true
					break
				}
			}
		}
		if needsProvider {
			if provider, err := m.buildProvider(); err == nil {
				m.provider = provider
			}
		}
	}
	var readLog *tools.ReadLog
	var runner sandbox.Runner
	if m.executor != nil {
		readLog = m.executor.ReadLog
		runner = m.executor.Runner
	}
	var fleetBridge pluginRuntime.FleetBridge
	if subagentRunner, ok := m.buildSubagentRunner(); ok && m.fleet != nil {
		var fleetSpawner runtime.Spawner = subagentRunner
		if publisher, ok := m.broker.(runtime.ApplicationEventPublisher); ok {
			fleetSpawner = runtime.ApplicationEventLeasedSpawner(fleetSpawner, publisher)
		}
		adapter := &runtime.FleetBridgeAdapter{Fleet: m.fleet, Spawner: fleetSpawner, RootCtx: rootCtx}
		if m.cfg != nil && m.session != nil {
			// Retained execution is an optional extension of the ordinary
			// fleet bridge. In the interactive daemon-backed surface the
			// broker WAL can already be owned by the daemon, so configuring
			// the process-local retained coordinator may legitimately fail.
			// Keep ordinary async/wait children available; spawnRetained
			// independently fails closed while Retained remains nil.
			_, _ = runtime.ConfigureRetainedBridge(rootCtx, m.cfg, m.session, adapter)
		}
		fleetBridge = adapter
	}
	return hostAdapter{
		workdir: m.cwd, readLog: readLog, runner: runner,
		executorSandbox: m.executorSandbox, broker: m.broker,
		approval: tuiApprovalBridge{model: m}, choice: tuiChoiceBridge{model: m},
		print: tuiPrintBridge{model: m}, render: tuiRenderBridge{model: m},
		spawn: m.buildSubagentSpawner(), fleetBridge: fleetBridge,
		provider: m.provider, defaultModel: m.model,
		progress: func(plugin, text string) {
			m.pushLogLine("PROGRESS [" + plugin + "] " + text)
		},
		pty: m.ptyManager,
	}
}

func (h hostAdapter) AgentFleetBridge() any { return h.fleetBridge }

func (h hostAdapter) PluginProviderBridge(identityCanonical string) (pluginRuntime.ProviderBridge, error) {
	if identityCanonical == "" || h.provider == nil {
		return nil, errors.New("TUI provider bridge unavailable")
	}
	return &pluginRuntime.NativeProviderBridge{Provider: h.provider, DefaultModel: h.defaultModel}, nil
}

func (h hostAdapter) ArtifactBrokerBinding(ctx context.Context, identity plugins.RuntimeIdentity, manifest plugins.Manifest, toolName string) (pluginRuntime.ArtifactBridgeBinding, error) {
	brokerController, ok := h.broker.(runtime.ArtifactBrokerController)
	if !ok {
		return pluginRuntime.ArtifactBridgeBinding{}, errors.New("artifact broker binding unavailable")
	}
	return brokerController.BindArtifacts(ctx, identity, manifest, toolName)
}

func (h hostAdapter) EvidenceBrokerBinding(ctx context.Context, identity plugins.RuntimeIdentity, manifest plugins.Manifest, toolName string) (pluginRuntime.EvidenceBridgeBinding, error) {
	brokerController, ok := h.broker.(runtime.EvidenceBrokerController)
	if !ok {
		return pluginRuntime.EvidenceBridgeBinding{}, errors.New("evidence broker binding unavailable")
	}
	return brokerController.BindEvidence(ctx, identity, manifest, toolName)
}

func (h hostAdapter) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}

func (h hostAdapter) Workdir() string        { return h.workdir }
func (h hostAdapter) Runner() sandbox.Runner { return h.runner }

func (h hostAdapter) DefaultSandboxPolicy() any {
	return h.executorSandbox.DefaultSandboxPolicy(h.workdir)
}

func (h hostAdapter) RequestApproval(ctx context.Context, title, body string) (bool, error) {
	return h.approval.RequestApproval(ctx, title, body)
}

func (h hostAdapter) RequestChoice(ctx context.Context, req pluginRuntime.ChoiceRequest) (pluginRuntime.ChoiceResponse, error) {
	return h.choice.RequestChoice(ctx, req)
}

// Print implements pluginRuntime.PrintBridge so the host adapter
// satisfies the interface assertion in attachLifecycleBridges,
// which wires the print bridge onto the per-plugin runtime host.
// Routes through tuiPrintBridge → pluginPrintMsg → Update handler.
// F9a.
func (h hostAdapter) Print(ctx context.Context, text string, opts pluginRuntime.PrintOpts) error {
	return h.print.Print(ctx, text, opts)
}

// Render implements pluginRuntime.RenderBridge so attachLifecycleBridges
// wires the render bridge onto the per-plugin runtime host. Routes
// through tuiRenderBridge → pluginRenderMsg → Update handler. F9b.2.
func (h hostAdapter) Render(ctx context.Context, panel pluginRuntime.Panel) error {
	return h.render.Render(ctx, panel)
}

func (h hostAdapter) SpawnSubagent(ctx context.Context, req subagent.Request) (subagent.Result, error) {
	if h.spawn == nil {
		return subagent.Result{}, errors.New("spawn_agent unavailable: current host does not support subagents")
	}
	return h.spawn(ctx, req)
}

func (h hostAdapter) PriorRead(key tool.ReadKey) (tool.PriorReadInfo, bool) {
	if h.readLog == nil {
		return tool.PriorReadInfo{}, false
	}
	return h.readLog.PriorRead(key)
}

func (h hostAdapter) RecordRead(key tool.ReadKey, info tool.PriorReadInfo) {
	if h.readLog == nil {
		return
	}
	h.readLog.RecordRead(key, info)
}

// EmitProgress implements pkg/tool.ProgressEmitter — surfaces
// stado_progress payloads to the TUI's sidebar log tail. EP-0038h.
func (h hostAdapter) EmitProgress(plugin, text string) {
	if h.progress != nil {
		h.progress(plugin, text)
	}
}

// PTYManager implements pkg/tool.PTYProvider — bundled shell.* /
// pty.* tools reuse the TUI-session-lifetime manager via this hook
// so session ids survive across consecutive tool dispatches.
func (h hostAdapter) PTYManager() any { return h.pty }

// buildPluginBridge wires the live TUI's Session + active provider
// behind a SessionBridgeImpl so plugins that declared session/provider
// capabilities see real conversation state. Returns nil when the TUI
// has no session or provider — plugins with those capabilities will
// error cleanly at call time, matching the `stado tool run` CLI
// path's behaviour. `pluginName` is retained for session-fork attribution;
// provider invocation identity comes only from the runtime Host identity.
func (m *Model) buildPluginBridge(pluginName string) *pluginRuntime.SessionBridgeImpl {
	if m.session == nil && m.provider == nil {
		return nil
	}
	msgs := append([]agent.Message(nil), m.msgs...) // snapshot by copy
	bridge := pluginRuntime.NewSessionBridge(m.session, m.provider, m.model)
	bridge.PluginName = pluginName
	bridge.MessagesFn = func() []agent.Message { return msgs }
	bridge.TokensFn = func() int { return m.usage.InputTokens }
	if m.session != nil {
		bridge.LastTurnRef = func() string {
			return string(stadogit.TurnTagRef(m.session.ID, m.session.Turn()))
		}
		bridge.ForkFn = m.pluginForkAt(pluginName)
	}
	return bridge
}

func (m *Model) adoptForkedSession(childID, atTurnRef, seed string) tea.Cmd {
	// Only the automatic compaction path is allowed to move a durable logical
	// scope. Manual forks remain independent conversations and are merely
	// announced by onPluginFork.
	if !m.recoveryPluginActive {
		return m.rejectAutomaticRecoveryHandoff("auto-recovery child was not adopted because no recovery operation is active", false)
	}
	if m.session == nil || m.session.Sidecar == nil {
		return m.rejectAutomaticRecoveryHandoff("auto-recovery forked a child session, but no live parent session is attached", false)
	}
	if strings.TrimSpace(atTurnRef) == "" {
		return m.rejectAutomaticRecoveryHandoff("auto-recovery child was not adopted because its exact source turn was not recorded", false)
	}
	cfg, err := config.Load()
	if err != nil {
		return m.rejectAutomaticRecoveryHandoff("auto-recovery: config load: "+err.Error(), false)
	}
	child, err := stadogit.OpenSession(m.session.Sidecar, filepath.Dir(m.session.WorktreePath), childID)
	if err != nil {
		return m.rejectAutomaticRecoveryHandoff("auto-recovery: open child session: "+err.Error(), false)
	}
	exec, err := runtime.BuildExecutorQuiet(child, cfg, "stado-tui", m.metrics)
	if err != nil {
		return m.rejectAutomaticRecoveryHandoff("auto-recovery: executor: "+err.Error(), false)
	}
	handoff, ok := m.broker.(runtime.BrokerLogicalSessionHandoff)
	if !ok || handoff == nil {
		return m.rejectAutomaticRecoveryHandoff("auto-recovery child was not adopted because the authenticated broker handoff is unavailable", false)
	}
	reservation, err := handoff.ReserveLogicalSessionHandoff(m.rootCtx, childID, atTurnRef)
	if err != nil {
		return m.rejectAutomaticRecoveryHandoff("auto-recovery: reserve durable session handoff: "+err.Error(), false)
	}
	if err := handoff.CommitLogicalSessionHandoff(m.rootCtx, reservation); err != nil {
		// A commit error may mean the broker applied the atomic subject move but
		// its reply was lost. Never resume the source in that ambiguity. The
		// staged child credential provides the deterministic restart path.
		return m.rejectAutomaticRecoveryHandoff("auto-recovery: durable session handoff outcome is unresolved: "+err.Error(), true)
	}
	prepared, err := m.prepareSessionTransitionWithBroker(m.rootCtx, child, exec, m.broker, m.brokerPeerOwned, true)
	if err != nil {
		// The broker already owns the child subject. This branch must stay
		// fail-closed rather than falling back to the now-fenced source.
		return m.rejectAutomaticRecoveryHandoff("auto-recovery: stage child lifecycle applications after handoff: "+err.Error(), true)
	}
	warnings := append([]string(nil), prepared.warnings...)

	prompt := m.recoveryPrompt
	queued := m.queuedPrompt
	draft := m.input.Value()
	recoverApplicationWorker := m.recoveryApplicationWorker
	m.recoveryPrompt = ""
	m.recoveryPluginName = ""
	m.recoveryPluginActive = false
	m.recoveryApplicationWorker = false
	if retireErr := m.commitSessionTransition(context.Background(), prepared); retireErr != nil {
		warnings = append(warnings, retireErr.Error())
	}
	m.msgs = nil
	m.blocks = nil
	m.todos = nil
	m.queuedPrompt = ""
	m.steeringMsg = "" // #16: don't carry a parent-turn steer into the child session
	m.pendingCalls = nil
	m.pendingResults = nil
	m.turnToolCalls = nil
	m.usage = agent.Usage{}
	m.cumulativeInputTokens = 0
	m.budgetWarned = false
	m.budgetAcked = false
	m.state = stateIdle
	m.LoadPersistedConversation()
	if res, err := instructions.Load(m.cwd); err == nil {
		m.systemPrompt = res.Content
		m.systemPromptPath = res.Path
	} else {
		m.systemPrompt = ""
		m.systemPromptPath = ""
	}
	if sks, err := skills.Load(m.cwd); err == nil {
		m.skills = sks
	} else {
		m.skills = nil
	}
	// Re-derive skill slash shortcuts for the recovered session's cwd so
	// the palette + dispatch map track the new skill set. Warnings go to a
	// system block since the TUI is live.
	m.registerSkillSlashCommands(func(msg string) {
		m.appendBlock(block{kind: "system", body: msg})
	})

	body := fmt.Sprintf("auto-recovery: switched to compacted child session %s", childID)
	for _, warning := range warnings {
		body += "\n" + warning
	}
	if strings.TrimSpace(seed) != "" {
		body += "\nsummary: " + trimSeed(seed, 120)
	}
	if recoverApplicationWorker {
		m.input.SetValue(mergeRecoveryInput("", queued, draft))
		m.appendBlock(block{kind: "system", body: body + "\nreconciling the exact application worker in the child session"})
		m.renderBlocks()
		return tea.Batch(
			m.reconcileApplicationOperatorInput(applicationInputAfterNone),
			m.reconcileApplicationWorkerRun(),
		)
	}
	if prompt == "" {
		m.input.SetValue(mergeRecoveryInput("", queued, draft))
		m.appendBlock(block{kind: "system", body: body})
		m.renderBlocks()
		return nil
	}
	m.appendBlock(block{kind: "system", body: body + "\nreplaying blocked prompt in the child session"})
	if err := m.setBrokerTaint(runtime.ContextClean); err != nil {
		m.input.SetValue(mergeRecoveryInput(prompt, queued, draft))
		m.appendBlock(block{kind: "system", body: "broker taint reset failed: " + err.Error()})
		m.renderBlocks()
		return nil
	}
	m.appendUser(prompt)
	retainedDraft := draft
	if retainedDraft == prompt {
		retainedDraft = ""
	}
	if retained := mergeRecoveryInput("", queued, retainedDraft); retained != "" {
		m.input.SetValue(retained)
	} else {
		m.input.Reset()
	}
	m.renderBlocks()
	return m.startStream()
}

func (m *Model) rejectAutomaticRecoveryHandoff(message string, failClosed bool) tea.Cmd {
	recoverApplicationWorker := m.recoveryApplicationWorker
	m.input.SetValue(mergeRecoveryInput(m.recoveryPrompt, m.queuedPrompt, m.input.Value()))
	m.queuedPrompt = ""
	m.recoveryPrompt = ""
	m.recoveryPluginName = ""
	m.recoveryPluginActive = false
	m.recoveryApplicationWorker = false
	if failClosed {
		m.setApplicationFailureSource(applicationFailureSessionHandoff, errors.New(message))
	}
	m.appendBlock(block{kind: "system", body: message})
	m.renderBlocks()
	if recoverApplicationWorker && m.loop != nil && m.loop.application != nil {
		return m.cancelApplicationLoop(message, false)
	}
	return nil
}

func mergeRecoveryInput(prompt, queued, draft string) string {
	values := make([]string, 0, 3)
	for _, value := range []string{prompt, queued, draft} {
		if value == "" {
			continue
		}
		duplicate := false
		for _, existing := range values {
			if value == existing {
				duplicate = true
				break
			}
		}
		if !duplicate {
			values = append(values, value)
		}
	}
	return strings.Join(values, "\n")
}

// pluginForkAt returns a ForkFn closure that drives the same
// fork-from-point primitive `stado session fork --at` uses: resolve
// at_turn_ref against the parent session's refs, create a new session
// rooted at that commit, materialise the worktree, then write a
// trace-ref marker to the new session tagged with `Plugin: <name>`
// whose Summary is the plugin-provided seed message. Returns the new
// session ID so the plugin can surface it.
//
// DESIGN invariant: the parent session is never modified. The child
// carries a trace marker commit that records what the plugin
// summarised, and the child's persisted conversation is seeded with
// that summary as its first user turn.
//
// Also posts a pluginForkMsg so the TUI update loop can render a
// user-visible notification (DESIGN invariant 4 — "user-visible by
// default").
func (m *Model) pluginForkAt(pluginName string) func(ctx context.Context, atTurnRef, seed string) (string, error) {
	return func(ctx context.Context, atTurnRef, seed string) (string, error) {
		if m.session == nil {
			return "", fmt.Errorf("plugin fork: no live session")
		}
		cfg, err := config.Load()
		if err != nil {
			return "", fmt.Errorf("plugin fork: load config: %w", err)
		}
		childSess, err := runtime.ForkPluginSession(cfg, m.session, atTurnRef, seed, pluginName)
		if err != nil {
			return "", err
		}
		childID := childSess.ID

		// Notify the user asynchronously via the tea program. Not a
		// synchronous block — the plugin is waiting on this function
		// to return, and we don't want to sleep here waiting for the
		// UI. If the program isn't attached (test harness), the send
		// is a no-op.
		if m.program != nil {
			m.program.Send(pluginForkMsg{
				plugin:    pluginName,
				childID:   childID,
				atTurnRef: atTurnRef,
				seed:      seed,
			})
		}
		return childID, nil
	}
}

// runPluginToolAsync spawns a fresh wazero runtime, instantiates the
// module under its capability-bound Host, invokes the tool, and posts
// the outcome back via pluginRunResultMsg. Hard-capped at 30s so a
// runaway plugin can't wedge the TUI.
//
// `print` and `render` bridges are attached when non-nil so plugins
// declaring `ui:print` / `ui:render` from the /tool or /plugin: slash
// path see the operator's surface (F9a, F9b). Pre-fix these were
// only wired through the agent loop's hostAdapter — operator-driven
// invocations never saw the panel/print emit because pluginrun.Run
// uses attachLifecycleBridges to pick them off the host, and the
// host built here didn't carry them.
func runPluginToolAsync(cfg *config.Config, dir string, mf *plugins.Manifest, identity plugins.RuntimeIdentity, tdef plugins.ToolDef, argsJSON, pluginID string, wasmBytes []byte, bridge *pluginRuntime.SessionBridgeImpl, approval pluginRuntime.ApprovalBridge, print pluginRuntime.PrintBridge, render pluginRuntime.RenderBridge, choice pluginRuntime.ChoiceBridge) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if err := toolinput.CheckLen(len(argsJSON)); err != nil {
			return pluginRunResultMsg{plugin: pluginID, tool: tdef.Name, errMsg: err.Error()}
		}

		rt, err := pluginRuntime.New(ctx)
		if err != nil {
			return pluginRunResultMsg{plugin: pluginID, tool: tdef.Name, errMsg: "runtime: " + err.Error()}
		}
		defer func() { _ = rt.Close(ctx) }()

		if err := identity.ValidateManifest(*mf); err != nil {
			return pluginRunResultMsg{plugin: pluginID, tool: tdef.Name, errMsg: "identity: " + err.Error()}
		}
		host := pluginRuntime.NewHostWithIdentity(*mf, identity, dir, nil)
		// EP-0029 D4: this operator-driven (/tool, /plugin:) path runs
		// the plugin directly rather than via pluginrun.Run, so populate
		// StateDir here too — otherwise a plugin declaring cfg:state_dir
		// invoked from the TUI would read an empty string.
		if cfg != nil {
			host.StateDir = cfg.StateDir()
		}
		host.ApprovalBridge = approval
		// F9a / F9b / Q3: attach print + render + choice bridges when
		// supplied, so plugins emitting via `stado_ui_print` /
		// `stado_ui_render` / `stado_ui_choose` from operator-driven
		// invocations (/tool, /plugin:) reach the TUI surface — not
		// just from agent-loop tool calls.
		if print != nil {
			host.PrintBridge = print
		}
		if render != nil {
			host.RenderBridge = render
		}
		if choice != nil {
			host.ChoiceBridge = choice
		}
		// Attach the live bridge only when the plugin declared a session
		// capability or the generic provider primitive and the caller supplied
		// one. Provider use is not session authority, but this bridge also
		// implements the bounded provider surface for direct TUI invocation.
		if bridge != nil && (host.SessionObserve || host.SessionRead || host.SessionFork || host.ProviderInvokeBudget > 0) {
			host.SessionBridge = bridge
		}
		if err := pluginRuntime.InstallHostImports(ctx, rt, host); err != nil {
			return pluginRunResultMsg{plugin: pluginID, tool: tdef.Name, errMsg: "host imports: " + err.Error()}
		}
		mod, err := rt.InstantiateWithIdentity(ctx, wasmBytes, *mf, identity)
		if err != nil {
			return pluginRunResultMsg{plugin: pluginID, tool: tdef.Name, errMsg: "instantiate: " + err.Error()}
		}
		defer func() { _ = mod.Close(ctx) }()

		pt, err := pluginRuntime.NewPluginTool(mod, tdef)
		if err != nil {
			return pluginRunResultMsg{plugin: pluginID, tool: tdef.Name, errMsg: err.Error()}
		}
		res, err := pt.Run(ctx, []byte(argsJSON), nil)
		if err != nil {
			msg := err.Error()
			if res.Error != "" {
				msg = res.Error
			}
			return pluginRunResultMsg{plugin: pluginID, tool: tdef.Name, errMsg: msg}
		}
		if res.Error != "" {
			return pluginRunResultMsg{plugin: pluginID, tool: tdef.Name, errMsg: res.Error}
		}
		return pluginRunResultMsg{plugin: pluginID, tool: tdef.Name, content: res.Content}
	}
}

// pluginToolIndent is the column the per-plugin tool list is indented to
// under each "/plugin:<name>" header, keeping the nested hierarchy.
const pluginToolIndent = 4

// pluginToolListWidth is the WrapDescList width for a plugin's nested
// tool block: the panel width minus the nesting indent. It must NOT
// exceed width-pluginToolIndent, or the indented lines overflow the
// enclosing system block, which re-wraps them and breaks the hanging
// indent (the same class of bug as slashListWidth's old floor). A
// too-wide floor would re-introduce that overflow at narrow viewports,
// so the result is floored at 1 (WrapDescList itself floors its inner
// descWidth at 1 and never panics) rather than clamped UP to a fixed
// minimum wider than the available space.
func pluginToolListWidth(width int) int {
	w := width - pluginToolIndent
	if w < 1 {
		w = 1
	}
	return w
}

// renderInstalledPluginList scans all pluginRoots and returns a human
// body enumerating each installed plugin with the tools it declares.
// Helpful discovery block for the bare `/plugin` command. EP-0035:
// scans all roots so project-local plugins are listed alongside global.
//
// width is the live panel width; each plugin's tools are rendered with
// render.WrapDescList (no truncation, hanging-indented descriptions) and
// then indented under the plugin header in the nested hierarchy.
func renderInstalledPluginList(width int, pluginRoots ...string) string {
	seen := map[string]struct{}{}
	var installed []plugins.InstalledPackage
	for _, root := range pluginRoots {
		packages, err := plugins.ListInstalledPackages(root)
		if err != nil {
			return "Installed plugin store error: " + err.Error()
		}
		for _, pkg := range packages {
			key := pkg.Identity.Canonical + "#" + pkg.Identity.ManifestDigest
			if _, already := seen[key]; !already {
				seen[key] = struct{}{}
				installed = append(installed, pkg)
			}
		}
	}
	if len(installed) == 0 {
		return "No plugins installed. Run `stado plugin install github.com/foobarto/stado-plugins/<plugin>@<version>` to add one."
	}

	// Inner width for the tool descriptions: the panel minus the nesting
	// indent, never wider than the available space (see pluginToolListWidth).
	toolWidth := pluginToolListWidth(width)

	var sb strings.Builder
	sb.WriteString("Installed plugins:")
	for _, pkg := range installed {
		sb.WriteString("\n  /plugin:" + pkg.Identity.Canonical + "  (" + pkg.Manifest.Name + ")")
		mf := &pkg.Manifest
		if len(mf.Tools) == 0 {
			continue
		}
		// Full descriptions, no truncation: WrapDescList hangs continuation
		// lines at the gutter so a long body stays inside the nested block
		// instead of wrapping back to column 0. The "· " bullet rides in
		// the Name so it aligns in the gutter; the whole block is then
		// indented under the plugin header.
		rows := make([]render.DescRow, 0, len(mf.Tools))
		for _, t := range mf.Tools {
			rows = append(rows, render.DescRow{Name: "· " + t.Name, Desc: textutil.SanitizeForTerminal(t.Description)})
		}
		block := render.WrapDescList(rows, toolWidth)
		pad := strings.Repeat(" ", pluginToolIndent)
		for _, ln := range strings.Split(block, "\n") {
			sb.WriteString("\n" + pad + ln)
		}
	}
	sb.WriteString("\n\nRun a tool with  /plugin:<name> <tool> [json-args]")
	sb.WriteString("\nSee one plugin's full descriptions with  /plugin <name>")
	return sb.String()
}

// renderPluginTools formats one plugin's manifest tools for display
// when the user asks about it specifically.
func renderPluginTools(nameVer string, m *plugins.Manifest) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Plugin %s  (author=%s, caps=%d)", nameVer, m.Author, len(m.Capabilities)))
	if len(m.Tools) == 0 {
		sb.WriteString("\n  (no tools declared)")
		return sb.String()
	}
	sb.WriteString("\nTools:")
	for _, t := range m.Tools {
		sb.WriteString("\n  · " + t.Name)
		if t.Description != "" {
			sb.WriteString("\n      " + textutil.SanitizeForTerminal(t.Description))
		}
	}
	sb.WriteString(fmt.Sprintf("\n\nRun with  /plugin:%s <tool> [json-args]", nameVer))
	return sb.String()
}
