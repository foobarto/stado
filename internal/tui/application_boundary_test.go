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

	"github.com/foobarto/stado/internal/plugins"
	pluginruntime "github.com/foobarto/stado/internal/plugins/runtime"
	stadoruntime "github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
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

func TestLifecycleApplicationCanonicalIdentityCannotBeProjectedTwice(t *testing.T) {
	existing := &stadoruntime.LoadedLifecycleApplication{Identity: plugins.RuntimeIdentity{Canonical: "signed:quality@example"}}
	alias := &stadoruntime.LoadedLifecycleApplication{Identity: plugins.RuntimeIdentity{Canonical: "signed:quality@example"}}
	if got := lifecycleApplicationByCanonical([]*stadoruntime.LoadedLifecycleApplication{existing}, alias.Identity.Canonical); got != existing {
		t.Fatalf("duplicate canonical identity resolved to %#v", got)
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
	id := "quality-1.0.0"
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name":"quality","version":"1.0.0","wasm_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","capabilities":[],"tools":[],"lifecycle":{}}`
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.manifest.sig"), []byte("test-only"), 0o600); err != nil {
		t.Fatal(err)
	}

	background, note := (&Model{}).loadOneBackground(context.Background(), nil, nil, []string{root}, id)
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
