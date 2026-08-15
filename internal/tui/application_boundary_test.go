package tui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	pluginruntime "github.com/foobarto/stado/internal/plugins/runtime"
	stadoruntime "github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/tui/palette"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/go-git/go-git/v5/plumbing"
)

func TestLifecycleApplicationProjectionSharesOneLoadedInstance(t *testing.T) {
	application := &pluginruntime.LifecycleApplication{}
	applicationTool := &pluginruntime.PluginTool{}
	loaded := &stadoruntime.LoadedLifecycleApplication{
		Identity: plugins.RuntimeIdentity{Canonical: "signed:quality@example"},
		Manifest: plugins.Manifest{
			Lifecycle: &plugins.LifecycleDef{},
			Commands:  []plugins.CommandDef{{Name: "quality", Description: "quality gate"}},
		},
		Application: application,
		Tools:       []*pluginruntime.PluginTool{applicationTool},
	}
	m := &Model{}
	m.projectLifecycleApplication(loaded)

	if len(m.lifecycleApplications) != 1 || m.lifecycleApplications[0] != loaded {
		t.Fatalf("application projection = %#v", m.lifecycleApplications)
	}
	if m.applicationCommands["quality"] != loaded {
		t.Fatal("command route did not retain the persistent loaded instance")
	}
	if m.lifecycleApplications[0].Application != application || m.lifecycleApplications[0].Tools[0] != applicationTool {
		t.Fatal("hook/event owner and model tool adapters did not remain on the same loaded instance")
	}
}

func TestTasksCommandIsOwnedOnlyByExplicitLifecycleApplication(t *testing.T) {
	if IsReservedSlashName("/tasks") || palette.CheckSlashCollision("/tasks") {
		t.Fatal("/tasks still has a native reserved or static-palette owner")
	}
	loaded := &stadoruntime.LoadedLifecycleApplication{
		Identity:    plugins.RuntimeIdentity{Namespace: "github.com/foobarto/stado-plugins/tasks", Canonical: "github.com/foobarto/stado-plugins/tasks@v0.1.0"},
		Manifest:    plugins.Manifest{Lifecycle: &plugins.LifecycleDef{Failure: "closed"}, Commands: []plugins.CommandDef{{Name: "tasks"}}},
		Application: &pluginruntime.LifecycleApplication{},
	}
	m := &Model{}
	if err := m.admitLoadedLifecycleApplication(loaded); err != nil {
		t.Fatalf("explicit tasks application command was rejected: %v", err)
	}
	if m.applicationCommands["tasks"] != loaded {
		t.Fatal("/tasks did not resolve to the admitted persistent application")
	}
}

func TestLifecycleApplicationCanonicalIdentityCannotBeProjectedTwice(t *testing.T) {
	existing := &stadoruntime.LoadedLifecycleApplication{Identity: plugins.RuntimeIdentity{Canonical: "signed:quality@example"}}
	alias := &stadoruntime.LoadedLifecycleApplication{Identity: plugins.RuntimeIdentity{Canonical: "signed:quality@example"}}
	if got := lifecycleApplicationByCanonical([]*stadoruntime.LoadedLifecycleApplication{existing}, alias.Identity.Canonical); got != existing {
		t.Fatalf("duplicate canonical identity resolved to %#v", got)
	}
}

func TestLifecycleApplicationOrderIsCanonicalAcrossDecisionAuthorities(t *testing.T) {
	contributor := &stadoruntime.LoadedLifecycleApplication{
		Identity: plugins.RuntimeIdentity{Canonical: "github.com/foobarto/stado-plugins/guidance@v1.0.0"},
		Manifest: plugins.Manifest{Capabilities: []string{"lifecycle:observe:pre_llm", "lifecycle:contribute:pre_llm"}},
	}
	decider := &stadoruntime.LoadedLifecycleApplication{
		Identity: plugins.RuntimeIdentity{Canonical: "github.com/example/policy@v1.0.0"},
		Manifest: plugins.Manifest{Capabilities: []string{"lifecycle:observe:pre_llm", "lifecycle:decide:pre_llm"}},
	}
	applications := []*stadoruntime.LoadedLifecycleApplication{contributor, decider}
	sortLifecycleApplications(applications)
	if applications[0] != decider || applications[1] != contributor {
		t.Fatalf("canonical order = %q, %q", applications[0].Identity.Canonical, applications[1].Identity.Canonical)
	}
}

func TestLifecycleApplicationCommandOwnsSlashBeforeAlias(t *testing.T) {
	loaded := &stadoruntime.LoadedLifecycleApplication{
		Identity:    plugins.RuntimeIdentity{Canonical: "github.com/acme/quality@v1.0.0"},
		Application: &pluginruntime.LifecycleApplication{},
		Manifest: plugins.Manifest{
			Commands: []plugins.CommandDef{{Name: "quality", Description: "quality gate"}},
		},
	}

	t.Run("stale alias cannot shadow owner", func(t *testing.T) {
		m := newBudgetModel(t)
		m.cfg = &config.Config{Aliases: config.Aliases{}}
		m.applicationCommands = map[string]*stadoruntime.LoadedLifecycleApplication{"quality": loaded}
		m.cfg.Aliases["quality"] = "/help"
		command := m.handleSlash("/quality inspect this")
		if command == nil || !m.applicationCommandRunning {
			t.Fatalf("application command was shadowed: command=%v running=%v", command != nil, m.applicationCommandRunning)
		}
	})

	t.Run("alias may target owner", func(t *testing.T) {
		m := newBudgetModel(t)
		m.cfg = &config.Config{Aliases: config.Aliases{}}
		m.applicationCommands = map[string]*stadoruntime.LoadedLifecycleApplication{"quality": loaded}
		m.cfg.Aliases["gate"] = "/quality {1}"
		command := m.handleSlash("/gate objective")
		if command == nil || !m.applicationCommandRunning {
			t.Fatalf("alias did not target application: command=%v running=%v", command != nil, m.applicationCommandRunning)
		}
	})
}

func TestLifecycleApplicationCommandCollisionIsOrderIndependentAndHasNoFallback(t *testing.T) {
	for _, order := range [][2]string{
		{"github.com/acme/first@v1.0.0", "github.com/acme/second@v1.0.0"},
		{"github.com/acme/second@v1.0.0", "github.com/acme/first@v1.0.0"},
	} {
		t.Run(order[0]+"_then_"+order[1], func(t *testing.T) {
			t.Cleanup(func() { palette.RegisterDynamicCommands(nil) })
			m := newBudgetModel(t)
			m.cfg = &config.Config{Aliases: config.Aliases{"supervise": "/help"}}
			first := &stadoruntime.LoadedLifecycleApplication{
				Identity:    plugins.RuntimeIdentity{Canonical: order[0]},
				Manifest:    plugins.Manifest{Commands: []plugins.CommandDef{{Name: "supervise", Description: "first"}}},
				Application: &pluginruntime.LifecycleApplication{},
			}
			second := &stadoruntime.LoadedLifecycleApplication{
				Identity:    plugins.RuntimeIdentity{Canonical: order[1]},
				Manifest:    plugins.Manifest{Commands: []plugins.CommandDef{{Name: "supervise", Description: "second"}}},
				Application: &pluginruntime.LifecycleApplication{},
			}
			if err := m.admitLoadedLifecycleApplication(first); err != nil {
				t.Fatal(err)
			}
			if err := m.admitLoadedLifecycleApplication(second); err == nil || !strings.Contains(err.Error(), "conflicts") {
				t.Fatalf("second staged owner collision = %v", err)
			}
			if m.applicationCommands["supervise"] != nil {
				t.Fatal("first application owner remained callable after duplicate claim")
			}
			m.registerSkillSlashCommands(func(string) {})
			if dynamicHasCommand("/supervise") {
				t.Fatal("conflicted application command remained dynamically discoverable")
			}
			// A stale lower-precedence skill or alias must not become a fallback
			// merely because the application route was invalidated.
			m.skillSlash = map[string]string{"supervise": "stale-skill-owner"}
			if command := m.handleSlash("/supervise status"); command != nil || m.applicationCommandRunning {
				t.Fatalf("ambiguous command dispatched: command=%v running=%v", command != nil, m.applicationCommandRunning)
			}
			if len(m.blocks) == 0 || !strings.Contains(m.blocks[len(m.blocks)-1].body, "ownership is ambiguous") {
				t.Fatalf("ambiguous command fell through to an alias, skill, or native owner: %#v", m.blocks)
			}
			m.closeLifecycleApplications(context.Background())
			if len(m.lifecycleApplications) != 0 || len(m.applicationCommands) != 0 || len(m.applicationCommandConflicts) != 0 {
				t.Fatalf("composition reload retained old owner or conflict: apps=%d commands=%v conflicts=%v", len(m.lifecycleApplications), m.applicationCommands, m.applicationCommandConflicts)
			}
		})
	}
}

func TestSuperviseApplicationFailureAndAbsenceNeverFallBack(t *testing.T) {
	t.Run("admitted callback failure is terminal", func(t *testing.T) {
		application := &pluginruntime.LifecycleApplication{}
		loaded := &stadoruntime.LoadedLifecycleApplication{
			Identity:    plugins.RuntimeIdentity{Canonical: "github.com/foobarto/stado-plugins/supervise@v0.1.0"},
			Manifest:    plugins.Manifest{Commands: []plugins.CommandDef{{Name: "supervise"}}},
			Application: application,
		}
		m := newBudgetModel(t)
		m.applicationCommands = map[string]*stadoruntime.LoadedLifecycleApplication{"supervise": loaded}

		command := m.handleSlash("/supervise status")
		if command == nil {
			t.Fatal("admitted application command did not dispatch")
		}
		message, ok := command().(applicationCommandResultMsg)
		if !ok || message.err == nil {
			t.Fatalf("failed application callback returned %#v", message)
		}
		_, followup := onApplicationCommandResult(m, message)
		if followup != nil || len(m.blocks) == 0 || !strings.Contains(m.blocks[len(m.blocks)-1].body, "callback is unavailable") {
			t.Fatalf("callback failure was not surfaced as final application result: %#v", m.blocks)
		}
	})

	t.Run("absent application is unknown", func(t *testing.T) {
		m := newBudgetModel(t)
		if command := m.handleSlash("/supervise status"); command != nil || m.applicationCommandRunning {
			t.Fatalf("absent application dispatched a fallback: command=%v running=%v", command != nil, m.applicationCommandRunning)
		}
		if len(m.blocks) == 0 || !strings.Contains(m.blocks[len(m.blocks)-1].body, "unknown command: /supervise") {
			t.Fatalf("absent application did not use ordinary unknown-command path: %#v", m.blocks)
		}
	})
}

func TestAliasCreationRejectsActiveApplicationAndSkillOwners(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*Model)
		want  string
	}{
		{
			name: "application",
			setup: func(m *Model) {
				m.applicationCommands = map[string]*stadoruntime.LoadedLifecycleApplication{"owned": {}}
			},
			want: "signed lifecycle application command",
		},
		{
			name: "skill",
			setup: func(m *Model) {
				m.skillSlash = map[string]string{"owned": "quality-skill"}
			},
			want: "skill slash command",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := newBudgetModel(t)
			test.setup(m)
			m.handleAliasCreate([]string{"owned", "/help"})
			if len(m.blocks) == 0 || !strings.Contains(m.blocks[len(m.blocks)-1].body, test.want) {
				t.Fatalf("alias collision block = %#v", m.blocks)
			}
		})
	}
}

func TestOrdinaryInputWaitsForApplicationCommandHandoff(t *testing.T) {
	m := newBudgetModel(t)
	m.applicationCommandRunning = true
	m.input.SetValue("begin the worker turn")

	_, command, handled := submitInput(m)
	if !handled || command != nil {
		t.Fatalf("submit handled=%v command=%v", handled, command)
	}
	if got := m.input.Value(); got != "begin the worker turn" {
		t.Fatalf("draft = %q, want preserved", got)
	}
	if len(m.msgs) != 0 || m.state == stateStreaming {
		t.Fatalf("ordinary provider turn started: msgs=%#v state=%v", m.msgs, m.state)
	}
	if len(m.blocks) == 0 || !strings.Contains(m.blocks[len(m.blocks)-1].body, "application command or worker handoff is still running") {
		t.Fatalf("blocks = %#v", m.blocks)
	}
}

func TestLegacyBackgroundLoaderRejectsLifecycleManifest(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "quality-source")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"quality","version":"1.0.0","wasm_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","capabilities":[],"tools":[],"lifecycle":{}}`
	if err := os.WriteFile(filepath.Join(source, "plugin.manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "plugin.manifest.sig"), []byte("test-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	mf, _, err := plugins.LoadFromDir(source)
	if err != nil {
		t.Fatal(err)
	}
	record, err := plugins.NewLocalInstallRecord(source, *mf)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, record.StoreKey)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{"plugin.manifest.json", "plugin.manifest.sig"} {
		data, readErr := os.ReadFile(filepath.Join(source, filename))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err := os.WriteFile(filepath.Join(dir, filename), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := plugins.WriteInstallRecord(dir, record, *mf); err != nil {
		t.Fatal(err)
	}

	background, note := (&Model{}).loadOneBackground(context.Background(), nil, nil, []string{root}, record.StoreKey)
	if background != nil || !strings.Contains(note, "persistent TUI application loader") {
		t.Fatalf("legacy lifecycle load = %#v, %q", background, note)
	}
}

type applicationBoundaryBroker struct {
	event  stadoruntime.HostApplicationEvent
	checks int
	state  stadoruntime.ScheduleState
}

func (b *applicationBoundaryBroker) CreateSubagent(context.Context, stadoruntime.BrokerSubagentRequest) (stadoruntime.BrokerController, error) {
	return b, nil
}
func (b *applicationBoundaryBroker) SetTaint(context.Context, stadoruntime.ContextTaint) error {
	return nil
}
func (b *applicationBoundaryBroker) Sandbox() stadoruntime.ExecutorSandbox {
	return stadoruntime.ExecutorSandbox{}
}
func (b *applicationBoundaryBroker) Worktree() string { return "" }
func (b *applicationBoundaryBroker) Close() error     { return nil }
func (b *applicationBoundaryBroker) PublishApplicationEvent(_ context.Context, event stadoruntime.HostApplicationEvent) (uint64, error) {
	b.event = event
	return 7, nil
}
func (b *applicationBoundaryBroker) CheckSchedule(context.Context) (stadoruntime.ScheduleStatus, error) {
	b.checks++
	if b.state != "" {
		return stadoruntime.ScheduleStatus{State: b.state}, nil
	}
	if b.checks == 1 {
		return stadoruntime.ScheduleStatus{State: stadoruntime.ScheduleHeld, Until: time.Now().Add(-time.Millisecond)}, nil
	}
	return stadoruntime.ScheduleStatus{State: stadoruntime.ScheduleActive}, nil
}

func TestApplicationSuccessfulCompletionEndsWorkerRecurrence(t *testing.T) {
	m := &Model{theme: theme.Default(), width: 80, height: 24, loop: &loopState{prompt: "continue"}, state: stateStreaming}
	_, command := onApplicationBoundaryResult(m, applicationBoundaryMsg{
		continuation: applicationBoundaryContinueTools,
		completed:    true,
	})
	if command != nil {
		t.Fatal("successful completion unexpectedly scheduled more worker work")
	}
	if m.loop != nil || m.state != stateIdle {
		t.Fatalf("loop=%#v state=%v, want stopped/idle", m.loop, m.state)
	}
	if len(m.blocks) == 0 || !strings.Contains(m.blocks[len(m.blocks)-1].body, "completed the worker run") {
		t.Fatalf("completion was not surfaced: %#v", m.blocks)
	}
}

func TestApplicationPublishedIterationAdvancesAcrossConsumedPause(t *testing.T) {
	m := &Model{
		theme: theme.Default(), width: 80, height: 24, state: stateStreaming,
		loop: &loopState{prompt: "continue", application: &stadoruntime.LoadedLifecycleApplication{}},
	}
	_, command := onApplicationBoundaryResult(m, applicationBoundaryMsg{
		published: true,
		err:       &stadoruntime.ScheduleBlockedError{Status: stadoruntime.ScheduleStatus{State: stadoruntime.SchedulePaused}},
	})
	if command != nil || m.loop != nil || m.state != stateIdle {
		t.Fatalf("pause did not end local recurrence: loop=%#v state=%v command=%v", m.loop, m.state, command != nil)
	}
	if m.applicationIteration != 1 {
		t.Fatalf("published paused iteration = %d, want 1", m.applicationIteration)
	}
}

func TestApplicationUnpublishedFailureDoesNotAdvanceIteration(t *testing.T) {
	m := &Model{theme: theme.Default(), width: 80, height: 24, state: stateStreaming, applicationIteration: 3}
	_, _ = onApplicationBoundaryResult(m, applicationBoundaryMsg{err: errors.New("publish failed")})
	if m.applicationIteration != 3 {
		t.Fatalf("unpublished failure iteration = %d, want 3", m.applicationIteration)
	}
}

func TestApplicationTurnBoundaryPublishesFactsBeforeContinuation(t *testing.T) {
	root := t.TempDir()
	sidecar, err := stadogit.OpenOrInitSidecar(filepath.Join(root, "sessions.git"), root)
	if err != nil {
		t.Fatal(err)
	}
	session, err := stadogit.CreateSession(sidecar, filepath.Join(root, "worktrees"), "tui-app-boundary", plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	broker := &applicationBoundaryBroker{}
	m := &Model{
		rootCtx: context.Background(), session: session, broker: broker,
		lifecycleApplications: []*stadoruntime.LoadedLifecycleApplication{{}},
		turnText:              "candidate", turnStart: time.Now(), turnProvider: "test", turnModel: "model",
	}
	// The dispatcher intentionally skips an application whose module is nil;
	// the non-empty owner set is enough to exercise the host publication and
	// strict scheduling barrier without constructing a WASM fixture here.
	command := m.applicationTurnBoundary(nil, applicationBoundaryFinishTurn)
	if command == nil {
		t.Fatal("application boundary command was not created")
	}
	message, ok := command().(applicationBoundaryMsg)
	if !ok || message.err != nil || message.continuation != applicationBoundaryFinishTurn {
		t.Fatalf("boundary message = %#v", message)
	}
	if broker.checks != 2 || broker.event.Kind != stadoruntime.SessionTurnCommittedEvent {
		t.Fatalf("checks=%d event=%#v", broker.checks, broker.event)
	}
	var facts stadoruntime.SessionTurnCommittedV1
	if err := json.Unmarshal(broker.event.Data, &facts); err != nil {
		t.Fatal(err)
	}
	if facts.Schema != stadoruntime.SessionTurnCommittedSchemaV1 || facts.Assistant.MessageRef == "" || facts.Assistant.Digest == "" {
		t.Fatalf("facts = %#v", facts)
	}
}

func TestApplicationPollFailureBlocksProviderUntilDispatcherRecovers(t *testing.T) {
	m := &Model{rootCtx: context.Background(), theme: theme.Default(), width: 80, height: 24}
	_, next := onApplicationPollResult(m, applicationPollResultMsg{err: errors.New("event callback trapped")})
	if next == nil || m.applicationFailure == nil {
		t.Fatalf("failure=%v next=%v", m.applicationFailure, next)
	}
	if command := m.startStream(); command != nil {
		t.Fatal("fail-closed lifecycle dispatcher allowed provider continuation")
	}
	if len(m.blocks) == 0 || !strings.Contains(m.blocks[len(m.blocks)-1].body, "event callback trapped") {
		t.Fatalf("missing fail-closed operator message: %#v", m.blocks)
	}

	_, next = onApplicationPollResult(m, applicationPollResultMsg{})
	if next == nil || m.applicationFailure != nil {
		t.Fatalf("dispatcher recovery failure=%v next=%v", m.applicationFailure, next)
	}
}

func TestApplicationPollWithoutApplicationsCompletesImmediately(t *testing.T) {
	m := &Model{rootCtx: context.Background()}
	command := m.pollLifecycleApplicationEvents()
	if command == nil {
		t.Fatal("periodic application poll command missing")
	}
	message, ok := command().(applicationPollResultMsg)
	if !ok || message.err != nil || message.applications != nil {
		t.Fatalf("poll result = %#v", message)
	}
}

func TestApplicationPollCannotClearAdmissionFailure(t *testing.T) {
	admissionErr := errors.New("signed lifecycle application admission failed")
	m := &Model{
		rootCtx: context.Background(), theme: theme.Default(), width: 80, height: 24,
		applicationAdmissionFailure: admissionErr,
		applicationFailure:          admissionErr,
	}
	_, _ = onApplicationPollResult(m, applicationPollResultMsg{})
	if !errors.Is(m.applicationFailure, admissionErr) {
		t.Fatalf("successful empty poll cleared fail-closed admission error: %v", m.applicationFailure)
	}
}
