package tui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	pluginruntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/tool"
)

type projectedApplicationTool struct {
	name     string
	class    tool.Class
	metadata runtime.ToolMetadata
	runs     int
}

func (t *projectedApplicationTool) Name() string           { return t.name }
func (t *projectedApplicationTool) Description() string    { return "application control" }
func (t *projectedApplicationTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (t *projectedApplicationTool) Class() tool.Class      { return t.class }
func (t *projectedApplicationTool) ToolMetadata() runtime.ToolMetadata {
	return t.metadata
}
func (t *projectedApplicationTool) Run(context.Context, json.RawMessage, tool.Host) (tool.Result, error) {
	t.runs++
	return tool.Result{Content: "ok"}, nil
}

type applicationToolBroker struct {
	run   runtime.ApplicationWorkerRun
	found bool
	err   error
}

func (b *applicationToolBroker) CreateSubagent(context.Context, runtime.BrokerSubagentRequest) (runtime.BrokerController, error) {
	return b, nil
}
func (b *applicationToolBroker) SetTaint(context.Context, runtime.ContextTaint) error {
	return nil
}
func (b *applicationToolBroker) Sandbox() runtime.ExecutorSandbox { return runtime.ExecutorSandbox{} }
func (b *applicationToolBroker) Worktree() string                 { return "" }
func (b *applicationToolBroker) Close() error                     { return nil }
func (b *applicationToolBroker) ActiveApplicationWorkerRun(context.Context) (runtime.ApplicationWorkerRun, bool, error) {
	return b.run, b.found, b.err
}

func projectedTool(name, owner string, optedIn, planVisible bool, class tool.Class) *projectedApplicationTool {
	metadata := runtime.ToolMetadata{
		Canonical:            runtime.CanonicalToolName(name),
		Plugin:               "quality",
		PackageNamespace:     owner,
		LifecycleApplication: owner,
	}
	if optedIn {
		metadata.ApplicationWorker = owner
		metadata.ApplicationWorkerPlan = planVisible
	}
	return &projectedApplicationTool{name: name, class: class, metadata: metadata}
}

func projectedSessionTool(name, owner string, optedIn, planVisible bool, class tool.Class) *projectedApplicationTool {
	metadata := runtime.ToolMetadata{
		Canonical: runtime.CanonicalToolName(name), Plugin: "tasks",
		PackageNamespace: owner, LifecycleApplication: owner,
	}
	if optedIn {
		metadata.ApplicationSession = owner
		metadata.ApplicationSessionPlan = planVisible
	}
	return &projectedApplicationTool{name: name, class: class, metadata: metadata}
}

func projectedWorkerApplication(run runtime.ApplicationWorkerRun, modelTools ...tool.Tool) *runtime.LoadedLifecycleApplication {
	return &runtime.LoadedLifecycleApplication{
		Identity: plugins.RuntimeIdentity{Namespace: run.PluginID, Canonical: run.PluginID + "@v1.0.0"},
		Application: &pluginruntime.LifecycleApplication{Anchor: pluginruntime.ApplicationAnchor{
			SessionID: run.SessionID, SessionGeneration: run.Generation,
		}},
		ModelTools: modelTools,
	}
}

func applicationToolModel(run runtime.ApplicationWorkerRun, application *runtime.LoadedLifecycleApplication, candidates ...tool.Tool) *Model {
	registry := tools.NewRegistry()
	for _, candidate := range candidates {
		registry.Register(candidate)
	}
	broker := &applicationToolBroker{run: run, found: true}
	return &Model{
		cfg:                   &config.Config{Tools: config.Tools{Autoload: []string{"*"}}},
		executor:              &tools.Executor{Registry: registry},
		broker:                broker,
		mode:                  modeDo,
		lifecycleApplications: []*runtime.LoadedLifecycleApplication{application},
		loop:                  &loopState{application: application, workerRun: run},
	}
}

func surfaceHas(surface []tool.Tool, name string) bool {
	for _, candidate := range surface {
		if candidate.Name() == name {
			return true
		}
	}
	return false
}

func TestApplicationWorkerProjectionIsOwnedAndModeBound(t *testing.T) {
	run := tuiWorkerRun(runtime.ApplicationWorkerRunActive)
	plan := projectedTool("quality__progress", run.PluginID, true, true, tool.ClassStateMutating)
	doOnly := projectedTool("quality__complete", run.PluginID, true, false, tool.ClassStateMutating)
	internal := projectedTool("quality__internal", run.PluginID, false, false, tool.ClassNonMutating)
	ordinary := &projectedApplicationTool{name: "ordinary__read", class: tool.ClassNonMutating}
	application := projectedWorkerApplication(run, plan, doOnly, internal)
	m := applicationToolModel(run, application, plan, doOnly, internal, ordinary)

	for _, test := range []struct {
		mode inputMode
		yes  []string
		no   []string
	}{
		{mode: modeDo, yes: []string{ordinary.name, plan.name, doOnly.name}, no: []string{internal.name}},
		{mode: modePlan, yes: []string{ordinary.name, plan.name}, no: []string{doOnly.name, internal.name}},
		{mode: modeBTW, yes: []string{ordinary.name}, no: []string{plan.name, doOnly.name, internal.name}},
	} {
		m.mode = test.mode
		surface := m.toolSurfaceForTurn()
		for _, name := range test.yes {
			if !surfaceHas(surface, name) {
				t.Errorf("mode %v omitted %s from %v", test.mode, name, surface)
			}
		}
		for _, name := range test.no {
			if surfaceHas(surface, name) {
				t.Errorf("mode %v exposed %s in %v", test.mode, name, surface)
			}
		}
	}

	m.mode = modeDo
	m.loop = nil
	if surfaceHas(m.toolSurfaceForTurn(), plan.name) || surfaceHas(m.toolSurfaceForTurn(), internal.name) {
		t.Fatal("lifecycle application tools escaped into an ordinary turn")
	}
	m.loop = &loopState{application: application, workerRun: run}
	m.loop.workerRun.Status = runtime.ApplicationWorkerRunCompleted
	if surfaceHas(m.toolSurfaceForTurn(), plan.name) {
		t.Fatal("terminal worker retained its application tool")
	}
}

func TestApplicationWorkerProjectionPreservesEveryToolCeiling(t *testing.T) {
	run := tuiWorkerRun(runtime.ApplicationWorkerRunActive)
	control := projectedTool("quality__control", run.PluginID, true, true, tool.ClassStateMutating)
	application := projectedWorkerApplication(run, control)
	m := applicationToolModel(run, application, control)

	m.cfg.Tools.Enabled = []string{"ordinary__read"}
	if surfaceHas(m.toolSurfaceForTurn(), control.name) {
		t.Fatal("application declaration widened the global enabled allowlist")
	}
	m.cfg.Tools.Enabled = nil
	m.cfg.Tools.Disabled = []string{"quality.*"}
	if surfaceHas(m.toolSurfaceForTurn(), control.name) {
		t.Fatal("application declaration bypassed the global disabled list")
	}
	m.cfg.Tools.Disabled = nil
	m.sessionToolOverrides.disableAdd = []string{"quality.*"}
	if surfaceHas(m.toolSurfaceForTurn(), control.name) {
		t.Fatal("application declaration bypassed the session disabled ceiling")
	}
	m.sessionToolOverrides = sessionToolOverrides{}
	if !surfaceHas(m.toolSurfaceForTurn(), control.name) {
		t.Fatal("valid owner and policy ceilings did not project the application tool")
	}

	otherRun := run
	otherRun.PluginID = "github.com/acme/other"
	otherApplication := projectedWorkerApplication(otherRun)
	m.lifecycleApplications = append(m.lifecycleApplications, otherApplication)
	m.loop = &loopState{application: otherApplication, workerRun: otherRun}
	if surfaceHas(m.toolSurfaceForTurn(), control.name) {
		t.Fatal("one application received another application's opted-in tool")
	}
}

func TestApplicationSessionProjectionIsExactModeBoundAndCeilingBound(t *testing.T) {
	run := tuiWorkerRun(runtime.ApplicationWorkerRunActive)
	plan := projectedSessionTool("tasks__read", run.PluginID, true, true, tool.ClassNonMutating)
	doOnly := projectedSessionTool("tasks", run.PluginID, true, false, tool.ClassStateMutating)
	internal := projectedSessionTool("tasks__internal", run.PluginID, false, false, tool.ClassNonMutating)
	application := projectedWorkerApplication(run, plan, doOnly, internal)
	m := applicationToolModel(run, application, plan, doOnly, internal)
	m.loop = nil // application_session is independent of WorkerRun ownership.

	for _, test := range []struct {
		mode inputMode
		yes  []string
		no   []string
	}{
		{mode: modeDo, yes: []string{plan.name, doOnly.name}, no: []string{internal.name}},
		{mode: modePlan, yes: []string{plan.name}, no: []string{doOnly.name, internal.name}},
		{mode: modeBTW, no: []string{plan.name, doOnly.name, internal.name}},
	} {
		m.mode = test.mode
		surface := m.toolSurfaceForTurn()
		for _, name := range test.yes {
			if !surfaceHas(surface, name) {
				t.Errorf("mode %v omitted application session tool %s", test.mode, name)
			}
		}
		for _, name := range test.no {
			if surfaceHas(surface, name) {
				t.Errorf("mode %v exposed forbidden application session tool %s", test.mode, name)
			}
		}
	}

	m.mode = modeDo
	m.cfg.Tools.Enabled = []string{"ordinary__read"}
	if surfaceHas(m.toolSurfaceForTurn(), doOnly.name) {
		t.Fatal("application_session widened global enabled ceiling")
	}
	m.cfg.Tools.Enabled = nil
	m.cfg.Tools.Disabled = []string{"tasks"}
	if surfaceHas(m.toolSurfaceForTurn(), doOnly.name) {
		t.Fatal("application_session bypassed global disabled ceiling")
	}
	m.cfg.Tools.Disabled = nil
	m.sessionToolOverrides.disableAdd = []string{"tasks"}
	if surfaceHas(m.toolSurfaceForTurn(), doOnly.name) {
		t.Fatal("application_session bypassed session disabled ceiling")
	}
	m.sessionToolOverrides = sessionToolOverrides{}
	if !surfaceHas(m.toolSurfaceForTurn(), doOnly.name) {
		t.Fatal("exact admitted application_session tool remained hidden")
	}
}

func TestApplicationSessionTurnAndExecutionGuardExactInstanceGeneration(t *testing.T) {
	run := tuiWorkerRun(runtime.ApplicationWorkerRunActive)
	control := projectedSessionTool("tasks", run.PluginID, true, false, tool.ClassStateMutating)
	application := projectedWorkerApplication(run, control)
	m := applicationToolModel(run, application, control)
	m.loop = nil
	m.turnAllowed = map[string]struct{}{control.name: {}}
	m.turnToolInstances = map[string]tool.Tool{control.name: control}
	if !m.turnAllowsTool(control.name) {
		t.Fatal("exact current application_session binding was rejected")
	}
	anchor := application.Application.Anchor
	if err := validateApplicationSessionExecution(m.executor, control.name, control, application, anchor); err != nil {
		t.Fatalf("exact current application session rejected: %v", err)
	}

	replacement := projectedSessionTool(control.name, run.PluginID, true, false, tool.ClassStateMutating)
	m.executor.Registry.Register(replacement)
	if m.turnAllowsTool(control.name) {
		t.Fatal("provider response crossed an application_session adapter rebind")
	}
	if err := validateApplicationSessionExecution(m.executor, control.name, control, application, anchor); err == nil {
		t.Fatal("execution guard accepted replaced registry adapter")
	}
	m.executor.Registry.Register(control)
	m.lifecycleApplications = []*runtime.LoadedLifecycleApplication{projectedWorkerApplication(run, control)}
	m.applicationToolProjectionGeneration.Add(1)
	if m.turnAllowsTool(control.name) {
		t.Fatal("provider response crossed an exact application module rebind")
	}
	m.lifecycleApplications = []*runtime.LoadedLifecycleApplication{application}
	application.Application.Anchor.SessionGeneration++
	if err := validateApplicationSessionExecution(m.executor, control.name, control, application, anchor); err == nil {
		t.Fatal("execution guard accepted stale application session generation")
	}
	application.Application.Anchor = anchor
	m.mode = modeBTW
	if m.turnAllowsTool(control.name) {
		t.Fatal("application_session crossed into BTW after projection")
	}
}

func TestApplicationWorkerToolAdmissionIsAtomicAndCollisionSafe(t *testing.T) {
	owner := "github.com/acme/quality"
	first := projectedTool("quality__first", owner, true, false, tool.ClassStateMutating)
	second := projectedTool("quality__conflict", owner, true, false, tool.ClassStateMutating)
	existing := &projectedApplicationTool{name: second.name, class: tool.ClassNonMutating}
	registry := tools.NewRegistry()
	registry.Register(existing)
	m := &Model{cfg: &config.Config{}, executor: &tools.Executor{Registry: registry}}
	loaded := &runtime.LoadedLifecycleApplication{
		Identity:   plugins.RuntimeIdentity{Namespace: owner, Canonical: owner + "@v1.0.0"},
		Manifest:   plugins.Manifest{Commands: []plugins.CommandDef{{Name: "quality", Description: "quality"}}},
		ModelTools: []tool.Tool{first, second},
	}
	if err := m.projectLifecycleApplication(loaded); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("collision error = %v", err)
	}
	if _, found := registry.Get(first.name); found || len(m.lifecycleApplications) != 0 || len(m.applicationCommands) != 0 {
		t.Fatal("failed admission partially projected tools, applications, or commands")
	}
	if current, _ := registry.Get(second.name); current != existing {
		t.Fatal("failed admission replaced the pre-existing registry owner")
	}

	ordinarySamePackage := &projectedApplicationTool{name: first.name, class: tool.ClassNonMutating, metadata: runtime.ToolMetadata{PackageNamespace: owner}}
	registry = tools.NewRegistry()
	registry.Register(ordinarySamePackage)
	m = &Model{cfg: &config.Config{}, executor: &tools.Executor{Registry: registry}}
	loaded.ModelTools = []tool.Tool{first}
	if err := m.projectLifecycleApplication(loaded); err != nil {
		t.Fatalf("same-package ordinary adapter replacement: %v", err)
	}
	if current, _ := registry.Get(first.name); current != first {
		t.Fatal("persistent adapter did not replace its exact same-package ordinary adapter")
	}

	existingLifecycle := projectedTool(first.name, owner, false, false, tool.ClassNonMutating)
	registry = tools.NewRegistry()
	registry.Register(existingLifecycle)
	m = &Model{cfg: &config.Config{}, executor: &tools.Executor{Registry: registry}}
	if err := m.projectLifecycleApplication(loaded); err == nil {
		t.Fatal("a second lifecycle instance replaced an admitted persistent adapter")
	}
}

func TestApplicationWorkerTurnBindingAndExecutionRecheckExactOwner(t *testing.T) {
	run := tuiWorkerRun(runtime.ApplicationWorkerRunActive)
	control := projectedTool("quality__control", run.PluginID, true, true, tool.ClassStateMutating)
	application := projectedWorkerApplication(run, control)
	m := applicationToolModel(run, application, control)
	m.turnAllowed = map[string]struct{}{control.name: {}}
	m.turnToolInstances = map[string]tool.Tool{control.name: control}
	if !m.turnAllowsTool(control.name) {
		t.Fatal("exact current application tool binding was rejected")
	}

	replacement := projectedTool(control.name, run.PluginID, true, true, tool.ClassStateMutating)
	m.executor.Registry.Register(replacement)
	if m.turnAllowsTool(control.name) {
		t.Fatal("provider response crossed an exact-instance rebind")
	}
	m.executor.Registry.Register(control)
	m.sessionToolOverrides.disableAdd = []string{control.name}
	if m.turnAllowsTool(control.name) {
		t.Fatal("a session ceiling change did not invalidate the earlier provider projection")
	}
	m.sessionToolOverrides = sessionToolOverrides{}
	m.mode = modeBTW
	if m.turnAllowsTool(control.name) {
		t.Fatal("an application tool crossed into BTW after projection")
	}
	m.mode = modeDo

	controller := m.broker.(runtime.ApplicationWorkerRunController)
	if err := validateApplicationWorkerExecution(context.Background(), m.executor, controller, control.name, control, run); err != nil {
		t.Fatalf("exact active owner rejected: %v", err)
	}
	broker := m.broker.(*applicationToolBroker)
	broker.run.Version++
	if err := validateApplicationWorkerExecution(context.Background(), m.executor, controller, control.name, control, run); err == nil {
		t.Fatal("stale broker run version remained executable")
	}
	broker.run = run
	broker.run.WALSequence++
	if err := validateApplicationWorkerExecution(context.Background(), m.executor, controller, control.name, control, run); err == nil {
		t.Fatal("stale broker WAL anchor remained executable")
	}
	broker.run = run
	broker.run.Status = runtime.ApplicationWorkerRunStopped
	if err := validateApplicationWorkerExecution(context.Background(), m.executor, controller, control.name, control, run); err == nil {
		t.Fatal("terminalized application worker remained executable")
	}
}

func TestModelToolSurfaceControllerCapturesExactCeilingWithoutActiveStateDrift(t *testing.T) {
	known := projectedTool("demo__known", "source/demo@v1", false, false, tool.ClassNonMutating)
	registry := tools.NewRegistry()
	registry.Register(known)
	model := &Model{executor: &tools.Executor{Registry: registry}, activatedTools: map[string]bool{known.Name(): true}}
	controller := newModelToolSurfaceController(model)
	if !controller.AllowsToolSurface(known.Name()) || controller.AllowsToolSurface("demo__absent") {
		t.Fatalf("captured ceiling is not exact: known=%v absent=%v", controller.AllowsToolSurface(known.Name()), controller.AllowsToolSurface("demo__absent"))
	}
	if err := controller.ApplyToolSurface(tool.ToolSurfaceEdit{Deactivate: []string{known.Name()}}); err != nil {
		t.Fatal(err)
	}
	if !controller.AllowsToolSurface(known.Name()) {
		t.Fatal("deactivation narrowed immutable TUI ceiling")
	}
	if err := controller.ApplyToolSurface(tool.ToolSurfaceEdit{Activate: []string{known.Name()}}); err != nil {
		t.Fatalf("reactivate permitted tool: %v", err)
	}
	if err := controller.ApplyToolSurface(tool.ToolSurfaceEdit{Activate: []string{"demo__absent"}}); err == nil {
		t.Fatal("TUI surface activated a tool outside the captured registry")
	}
	model.sessionToolOverrides.disableAdd = []string{known.Name()}
	if controller.AllowsToolSurface(known.Name()) {
		t.Fatal("session override did not narrow captured registry ceiling")
	}
}
