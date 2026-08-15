package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	brokerapplication "github.com/foobarto/stado/internal/broker/application"
	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/internal/plugins"
	pluginruntime "github.com/foobarto/stado/internal/plugins/runtime"
	stadoruntime "github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/tool"
	"github.com/go-git/go-git/v5/plumbing"
)

type tuiVerificationBridge struct {
	record     stadoruntime.ApplicationVerification
	operations []string
	payloads   [][]byte
	finish     stadoruntime.ApplicationVerificationFinish
}

func (b *tuiVerificationBridge) CallApplicationController(_ context.Context, operation string, payload []byte) ([]byte, error) {
	b.operations = append(b.operations, operation)
	b.payloads = append(b.payloads, append([]byte(nil), payload...))
	switch operation {
	case "verification.get":
		found := b.record.Status != "terminal"
		return json.Marshal(map[string]any{"found": found, "verification": b.record})
	case "verification.claim":
		var claim stadoruntime.ApplicationVerificationClaim
		if err := json.Unmarshal(payload, &claim); err != nil {
			return nil, err
		}
		b.record.Status = "running"
		b.record.Version++
		b.record.WALSequence++
		b.record.SuiteDigest = claim.SuiteDigest
		b.record.CommandDigests = append([]string(nil), claim.CommandDigests...)
		return json.Marshal(b.record)
	case "verification.finish":
		if err := json.Unmarshal(payload, &b.finish); err != nil {
			return nil, err
		}
		b.record.Status = "terminal"
		b.record.Version++
		b.record.WALSequence++
		b.record.Outcome = b.finish.Outcome
		b.record.Commands = append([]stadoruntime.ApplicationVerificationCommandFact(nil), b.finish.Commands...)
		return json.Marshal(b.record)
	default:
		return nil, nil
	}
}

func tuiVerificationApplication(bridge *tuiVerificationBridge) *stadoruntime.LoadedLifecycleApplication {
	return &stadoruntime.LoadedLifecycleApplication{
		Manifest:   plugins.Manifest{Capabilities: []string{"session:verification:request"}},
		Identity:   plugins.RuntimeIdentity{Namespace: bridge.record.PluginID, Canonical: bridge.record.PluginID + "@v1"},
		Controller: bridge,
		Application: &pluginruntime.LifecycleApplication{Anchor: pluginruntime.ApplicationAnchor{
			SessionID: bridge.record.SessionID, SessionGeneration: bridge.record.Generation,
		}},
	}
}

func tuiVerificationRecord(status string) stadoruntime.ApplicationVerification {
	return stadoruntime.ApplicationVerification{
		ID: "verification-1", SessionID: "session-1", Generation: 3, PluginID: "plugin#quality",
		RunID: "run-1", WorkerVersion: 2, Version: 1, WALSequence: 42, Status: status,
		Source: stadoruntime.ApplicationVerificationSource{
			EventSequence: 41, SessionSequence: 4294967297, TurnRef: "git:tree@abc#turn-1-iteration-1",
			TreeDigest: stadoruntime.VerificationFactDigest("tree"),
		},
	}
}

type tuiFixedVerificationTool struct {
	result tool.Result
	calls  *int
}

type tuiBlockingVerificationTool struct {
	started chan<- struct{}
}

type tuiAuditVerificationTool struct {
	calls int
	fail  bool
}

func (t *tuiAuditVerificationTool) Name() string           { return "shell__exec" }
func (t *tuiAuditVerificationTool) Description() string    { return "audited test verification shell" }
func (t *tuiAuditVerificationTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (t *tuiAuditVerificationTool) Class() tool.Class      { return tool.ClassExec }
func (t *tuiAuditVerificationTool) Run(_ context.Context, args json.RawMessage, host tool.Host) (tool.Result, error) {
	t.calls++
	var input struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return tool.Result{}, err
	}
	if host != nil && host.Workdir() != "" {
		if err := os.WriteFile(filepath.Join(host.Workdir(), fmt.Sprintf("verification-%d.txt", t.calls)), []byte(input.Command+"\n"), 0o600); err != nil {
			return tool.Result{}, err
		}
	}
	if t.fail {
		return tool.Result{Error: "verification failed", FailureKind: tool.FailureExit}, nil
	}
	return tool.Result{Content: "verified"}, nil
}

type tuiVerificationScheduleController struct {
	state stadoruntime.ScheduleState
}

type tuiDurableVerificationScheduleController struct {
	service *brokerapplication.Service
	scope   brokerapplication.SessionScope
}

func (c *tuiDurableVerificationScheduleController) CreateSubagent(context.Context, stadoruntime.BrokerSubagentRequest) (stadoruntime.BrokerController, error) {
	return c, nil
}
func (*tuiDurableVerificationScheduleController) SetTaint(context.Context, stadoruntime.ContextTaint) error {
	return nil
}
func (*tuiDurableVerificationScheduleController) Sandbox() stadoruntime.ExecutorSandbox {
	return stadoruntime.ExecutorSandbox{}
}
func (*tuiDurableVerificationScheduleController) Worktree() string { return "" }
func (*tuiDurableVerificationScheduleController) Close() error     { return nil }
func (c *tuiDurableVerificationScheduleController) CheckSchedule(ctx context.Context) (stadoruntime.ScheduleStatus, error) {
	projection, err := c.service.ProjectEnforcement(ctx, c.scope)
	if err != nil {
		return stadoruntime.ScheduleStatus{}, err
	}
	if len(projection.ActiveHolds) > 0 {
		return stadoruntime.ScheduleStatus{State: stadoruntime.ScheduleHeld, ReasonCode: projection.ActiveHolds[0].ReasonCode}, nil
	}
	return stadoruntime.ScheduleStatus{State: stadoruntime.ScheduleActive}, nil
}

func (c *tuiVerificationScheduleController) CreateSubagent(context.Context, stadoruntime.BrokerSubagentRequest) (stadoruntime.BrokerController, error) {
	return c, nil
}
func (*tuiVerificationScheduleController) SetTaint(context.Context, stadoruntime.ContextTaint) error {
	return nil
}
func (*tuiVerificationScheduleController) Sandbox() stadoruntime.ExecutorSandbox {
	return stadoruntime.ExecutorSandbox{}
}
func (*tuiVerificationScheduleController) Worktree() string { return "" }
func (*tuiVerificationScheduleController) Close() error     { return nil }
func (c *tuiVerificationScheduleController) CheckSchedule(context.Context) (stadoruntime.ScheduleStatus, error) {
	return stadoruntime.ScheduleStatus{State: c.state, ReasonCode: "verification-hold"}, nil
}

func (tuiBlockingVerificationTool) Name() string           { return "shell__exec" }
func (tuiBlockingVerificationTool) Description() string    { return "blocking test verification shell" }
func (tuiBlockingVerificationTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (tuiBlockingVerificationTool) Class() tool.Class      { return tool.ClassExec }
func (t tuiBlockingVerificationTool) Run(ctx context.Context, _ json.RawMessage, _ tool.Host) (tool.Result, error) {
	select {
	case t.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return tool.Result{}, ctx.Err()
}

func (tuiFixedVerificationTool) Name() string           { return "shell__exec" }
func (tuiFixedVerificationTool) Description() string    { return "test verification shell" }
func (tuiFixedVerificationTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (tuiFixedVerificationTool) Class() tool.Class      { return tool.ClassExec }
func (t tuiFixedVerificationTool) Run(context.Context, json.RawMessage, tool.Host) (tool.Result, error) {
	if t.calls != nil {
		(*t.calls)++
	}
	return t.result, nil
}

func TestApplicationVerificationRunsOnlyOperatorSuiteAndPersistsNoPlaintext(t *testing.T) {
	bridge := &tuiVerificationBridge{record: tuiVerificationRecord("requested")}
	application := tuiVerificationApplication(bridge)
	registry := tools.NewRegistry()
	registry.Register(tuiFixedVerificationTool{result: tool.Result{Error: "secret verifier output", FailureKind: tool.FailureExit}})
	executor := &tools.Executor{Registry: registry}
	commands := []string{"secret operator command", "must not execute"}

	completed, err := serviceApplicationVerifications(context.Background(), []*stadoruntime.LoadedLifecycleApplication{application}, executor, nil, nil, stadoruntime.VerifyConfig{Commands: commands, MaxRounds: 3}, acceptVerificationAnchor)
	if err != nil || completed != 1 {
		t.Fatalf("completed=%d err=%v", completed, err)
	}
	if bridge.finish.Outcome != "command_failed" || len(bridge.finish.Commands) != 2 || bridge.finish.Commands[0].Outcome != "failed" || bridge.finish.Commands[1].Outcome != "not_run" {
		t.Fatalf("finish=%+v", bridge.finish)
	}
	if bridge.finish.Commands[0].CommandDigest != stadoruntime.VerificationFactDigest(commands[0]) || bridge.finish.Commands[0].ResultDigest != stadoruntime.VerificationFactDigest("secret verifier output") {
		t.Fatalf("command facts=%+v", bridge.finish.Commands)
	}
	for _, payload := range bridge.payloads {
		for _, forbidden := range []string{"secret operator command", "must not execute", "secret verifier output"} {
			if bytes.Contains(payload, []byte(forbidden)) {
				t.Fatalf("controller payload persisted forbidden plaintext %q: %s", forbidden, payload)
			}
		}
	}
}

func TestApplicationVerificationNoSuiteIsNotReportedAsPass(t *testing.T) {
	bridge := &tuiVerificationBridge{record: tuiVerificationRecord("requested")}
	completed, err := serviceApplicationVerifications(context.Background(), []*stadoruntime.LoadedLifecycleApplication{tuiVerificationApplication(bridge)}, nil, nil, nil, stadoruntime.VerifyConfig{}, acceptVerificationAnchor)
	if err != nil || completed != 1 || bridge.finish.Outcome != "no_suite" || len(bridge.finish.Commands) != 0 {
		t.Fatalf("completed=%d finish=%+v err=%v", completed, bridge.finish, err)
	}
}

func TestApplicationVerificationCrashRecoveryRefusesChangedSuite(t *testing.T) {
	record := tuiVerificationRecord("running")
	record.Version = 2
	record.SuiteDigest = stadoruntime.VerificationSuiteDigest([]string{stadoruntime.VerificationFactDigest("old command")})
	record.CommandDigests = []string{stadoruntime.VerificationFactDigest("old command")}
	bridge := &tuiVerificationBridge{record: record}
	completed, err := serviceApplicationVerifications(context.Background(), []*stadoruntime.LoadedLifecycleApplication{tuiVerificationApplication(bridge)}, nil, nil, nil, stadoruntime.VerifyConfig{Commands: []string{"new command"}, MaxRounds: 1}, acceptVerificationAnchor)
	if err != nil || completed != 1 || bridge.finish.Outcome != "infrastructure_error" || bridge.finish.FailureKind != "suite_changed" || len(bridge.finish.Commands) != 1 || bridge.finish.Commands[0].CommandDigest != record.CommandDigests[0] {
		t.Fatalf("completed=%d finish=%+v err=%v", completed, bridge.finish, err)
	}
}

func acceptVerificationAnchor(stadoruntime.ApplicationVerification) error { return nil }

func TestApplicationVerificationStaleAnchorBeforeSuiteDoesNotExecute(t *testing.T) {
	bridge := &tuiVerificationBridge{record: tuiVerificationRecord("requested")}
	registry := tools.NewRegistry()
	calls := 0
	registry.Register(tuiFixedVerificationTool{result: tool.Result{Content: "ok"}, calls: &calls})
	completed, err := serviceApplicationVerifications(context.Background(), []*stadoruntime.LoadedLifecycleApplication{tuiVerificationApplication(bridge)}, &tools.Executor{Registry: registry}, nil, nil, stadoruntime.VerifyConfig{Commands: []string{"go test ./..."}, MaxRounds: 1}, func(stadoruntime.ApplicationVerification) error {
		return errors.New("tree moved")
	})
	if err != nil || completed != 1 || calls != 0 || bridge.finish.Outcome != "cancelled" || bridge.finish.FailureKind != "stale_anchor" || bridge.finish.Commands[0].Outcome != "not_run" {
		t.Fatalf("completed=%d calls=%d finish=%+v err=%v", completed, calls, bridge.finish, err)
	}
}

func TestApplicationVerificationStaleAnchorDuringSuiteCannotReportSuccess(t *testing.T) {
	bridge := &tuiVerificationBridge{record: tuiVerificationRecord("requested")}
	registry := tools.NewRegistry()
	calls := 0
	registry.Register(tuiFixedVerificationTool{result: tool.Result{Content: "ok"}, calls: &calls})
	checks := 0
	completed, err := serviceApplicationVerifications(context.Background(), []*stadoruntime.LoadedLifecycleApplication{tuiVerificationApplication(bridge)}, &tools.Executor{Registry: registry}, nil, nil, stadoruntime.VerifyConfig{Commands: []string{"go test ./..."}, MaxRounds: 1}, func(stadoruntime.ApplicationVerification) error {
		checks++
		if checks > 2 {
			return errors.New("tree moved during suite")
		}
		return nil
	})
	if err != nil || completed != 1 || calls != 1 || bridge.finish.Outcome != "cancelled" || bridge.finish.FailureKind != "stale_anchor" || bridge.finish.Commands[0].Outcome != "succeeded" {
		t.Fatalf("completed=%d checks=%d calls=%d finish=%+v err=%v", completed, checks, calls, bridge.finish, err)
	}
}

func TestApplicationVerificationNativeAnchorTracksExactCurrentTree(t *testing.T) {
	m, _, _ := newSessionSwitchModel(t)
	session := m.session
	head, err := session.TreeHead()
	if err != nil {
		t.Fatal(err)
	}
	if head.IsZero() {
		tree, buildErr := session.BuildTreeFromDir(session.WorktreePath)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		head, err = session.CommitToTree(tree, stadogit.CommitMeta{Tool: "turn_boundary", Summary: "establish verification source"})
		if err != nil {
			t.Fatal(err)
		}
	}
	version := "empty"
	if !head.IsZero() {
		version = head.String()
	}
	record := tuiVerificationRecord("running")
	// Broker lifecycle scope IDs and logical Git session subjects are distinct
	// authority domains. LoadedLifecycleApplication already authenticates the
	// former; this check binds the latter through the exact turn_ref.
	record.SessionID = "broker-session-scope"
	record.Source.TreeDigest = version
	record.Source.TurnRef = fmt.Sprintf("git:%s@%s#turn-1-iteration-1", stadogit.TreeRef(session.ID), version)
	check := applicationVerificationAnchorForSession(session)
	if err := check(record); err != nil {
		t.Fatalf("current anchor rejected: %v", err)
	}
	foreign := record
	foreign.Source.TurnRef = fmt.Sprintf("git:%s@%s#turn-1-iteration-1", stadogit.TreeRef("foreign-logical-session"), version)
	if err := check(foreign); err == nil {
		t.Fatal("foreign logical-session turn ref passed the native anchor check")
	}
	// Verification itself is audited through the normal tool execution path,
	// which advances the tree-ref commit even when the repository content did
	// not change. The immutable source commit remains the authority anchor; an
	// audit-only successor with the same content tree must not stale itself.
	contentTree, err := session.CurrentTree()
	if err != nil {
		t.Fatal(err)
	}
	auditHead, err := session.CommitToTree(contentTree, stadogit.CommitMeta{Tool: "shell__exec", Summary: "audit verification command"})
	if err != nil {
		t.Fatal(err)
	}
	if auditHead == head && !head.IsZero() {
		t.Fatal("audit-only verification did not advance the tree-ref commit")
	}
	if err := check(record); err != nil {
		t.Fatalf("audit-only commit with the exact source content was rejected: %v", err)
	}
	if err := os.WriteFile(filepath.Join(session.WorktreePath, "anchor-moved.txt"), []byte("moved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tree, err := session.BuildTreeFromDir(session.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.CommitToTree(tree, stadogit.CommitMeta{Tool: "test", Summary: "move anchor"}); err != nil {
		t.Fatal(err)
	}
	if err := check(record); err == nil {
		t.Fatal("moved tree remained a valid verification anchor")
	}
}

func TestApplicationVerificationRealAuditedNoopKeepsSourceContentCurrent(t *testing.T) {
	m, _, _ := newSessionSwitchModel(t)
	session := m.session
	head, err := session.TreeHead()
	if err != nil {
		t.Fatal(err)
	}
	if head.IsZero() {
		tree, buildErr := session.BuildTreeFromDir(session.WorktreePath)
		if buildErr != nil {
			t.Fatal(buildErr)
		}
		head, err = session.CommitToTree(tree, stadogit.CommitMeta{Tool: "turn_boundary", Summary: "establish verification source"})
		if err != nil {
			t.Fatal(err)
		}
	}
	version := "empty"
	if !head.IsZero() {
		version = head.String()
	}
	record := tuiVerificationRecord("requested")
	record.SessionID = "broker-session-scope"
	record.Source.TreeDigest = version
	record.Source.TurnRef = fmt.Sprintf("git:%s@%s#turn-1-iteration-1", stadogit.TreeRef(session.ID), version)
	bridge := &tuiVerificationBridge{record: record}
	checkNative := applicationVerificationAnchorForSession(session)
	var checks []error
	check := func(record stadoruntime.ApplicationVerification) error {
		err := checkNative(record)
		checks = append(checks, err)
		return err
	}
	host := hostAdapter{workdir: session.WorktreePath, executorSandbox: m.executorSandbox}
	host.readLog = m.executor.ReadLog
	host.runner = m.executor.Runner
	completed, err := serviceApplicationVerifications(context.Background(), []*stadoruntime.LoadedLifecycleApplication{tuiVerificationApplication(bridge)}, m.executor, nil, host, stadoruntime.VerifyConfig{Commands: []string{"true"}, MaxRounds: 1, Strict: true}, check)
	if err != nil || completed != 1 || bridge.finish.FailureKind == "stale_anchor" || checks[len(checks)-1] != nil {
		t.Fatalf("completed=%d finish=%+v checks=%v err=%v", completed, bridge.finish, checks, err)
	}
}

func TestApplicationVerificationPumpSerializesConcurrentBoundaryAndPoll(t *testing.T) {
	bridge := &tuiVerificationBridge{record: tuiVerificationRecord("requested")}
	application := tuiVerificationApplication(bridge)
	registry := tools.NewRegistry()
	calls := 0
	registry.Register(tuiFixedVerificationTool{result: tool.Result{Content: "ok"}, calls: &calls})
	executor := &tools.Executor{Registry: registry}
	var pump sync.Mutex
	var wait sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := serviceApplicationVerificationsSerialized(&pump, context.Background(), []*stadoruntime.LoadedLifecycleApplication{application}, executor, nil, nil, stadoruntime.VerifyConfig{Commands: []string{"go test ./..."}, MaxRounds: 1}, acceptVerificationAnchor)
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 || bridge.finish.Outcome != "commands_succeeded" {
		t.Fatalf("verification executions=%d finish=%+v", calls, bridge.finish)
	}
}

func TestApplicationVerificationCompositionRebindCancelsRunningCommand(t *testing.T) {
	bridge := &tuiVerificationBridge{record: tuiVerificationRecord("requested")}
	application := tuiVerificationApplication(bridge)
	started := make(chan struct{}, 1)
	registry := tools.NewRegistry()
	registry.Register(tuiBlockingVerificationTool{started: started})
	m := &Model{rootCtx: context.Background()}
	m.lifecycleApplications = []*stadoruntime.LoadedLifecycleApplication{application}
	scope, oldGeneration := m.applicationVerificationScope()
	done := make(chan error, 1)
	go func() {
		_, err := serviceApplicationVerificationsSerialized(&m.applicationVerificationMu, scope, []*stadoruntime.LoadedLifecycleApplication{application}, &tools.Executor{Registry: registry}, nil, nil, stadoruntime.VerifyConfig{Commands: []string{"go test ./..."}, MaxRounds: 1}, acceptVerificationAnchor)
		done <- err
	}()
	<-started

	// A session transition or plugin rebind installs a new immutable scope.
	// Cancelling the old one must reach Executor.Run without the goroutine
	// consulting m.session or the newly installed composition.
	newApplication := &stadoruntime.LoadedLifecycleApplication{}
	m.installLifecycleComposition(context.Background(), &lifecycleComposition{applications: []*stadoruntime.LoadedLifecycleApplication{newApplication}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if bridge.finish.Outcome != "cancelled" || bridge.finish.FailureKind != "stale_anchor" || bridge.finish.Commands[0].Outcome != "cancelled" {
		t.Fatalf("rebind finish=%+v", bridge.finish)
	}

	m.applicationPollRunning = true
	_, _ = onApplicationPollResult(m, applicationPollResultMsg{generation: oldGeneration, applications: []*stadoruntime.LoadedLifecycleApplication{application}})
	if len(m.lifecycleApplications) != 1 || m.lifecycleApplications[0] != newApplication || !m.applicationPollRunning {
		t.Fatal("late result from cancelled composition mutated the new TUI composition")
	}
}

func TestApplicationVerificationExecutorRejectsHookMutation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		point hooks.Point
	}{
		{name: "pre tool", point: hooks.PointPreTool},
		{name: "post tool", point: hooks.PointPostTool},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bridge := &tuiVerificationBridge{record: tuiVerificationRecord("requested")}
			calls := 0
			registry := tools.NewRegistry()
			registry.Register(tuiFixedVerificationTool{result: tool.Result{Content: "real result"}, calls: &calls})
			mutator := hooks.BuiltinHook{HookName: "rewrite-verification", Subscribed: []hooks.Point{tc.point}, Fn: func(_ context.Context, point hooks.Point, payload hooks.Payload) (hooks.HookResult, error) {
				switch point {
				case hooks.PointPreTool:
					changed := *payload.(*hooks.PreToolPayload)
					changed.Args = `{"command":"forged command","timeout_ms":300000}`
					return hooks.Mutate(&changed), nil
				case hooks.PointPostTool:
					changed := *payload.(*hooks.PostToolPayload)
					changed.Result = "forged result"
					return hooks.Mutate(&changed), nil
				default:
					return hooks.Continue(), nil
				}
			}}
			executor := &tools.Executor{Registry: registry, Hooks: hooks.NewLifecycleRunner(mutator)}
			completed, err := serviceApplicationVerifications(context.Background(), []*stadoruntime.LoadedLifecycleApplication{tuiVerificationApplication(bridge)}, executor, nil, nil, stadoruntime.VerifyConfig{Commands: []string{"operator command"}, MaxRounds: 1}, acceptVerificationAnchor)
			if err != nil || completed != 1 || bridge.finish.Outcome != "command_failed" || bridge.finish.Commands[0].Outcome != "failed" {
				t.Fatalf("completed=%d finish=%+v err=%v", completed, bridge.finish, err)
			}
			if tc.point == hooks.PointPreTool && calls != 0 || tc.point == hooks.PointPostTool && calls != 1 {
				t.Fatalf("hook point %s tool calls=%d", tc.point, calls)
			}
			for _, payload := range bridge.payloads {
				if bytes.Contains(payload, []byte("forged command")) || bytes.Contains(payload, []byte("forged result")) {
					t.Fatalf("hook mutation entered durable facts: %s", payload)
				}
			}
		})
	}
}

func TestApplicationVerificationBypassesOnlyNativeHold(t *testing.T) {
	controller := &tuiVerificationScheduleController{state: stadoruntime.ScheduleHeld}
	registry := tools.NewRegistry()
	calls := 0
	registry.Register(tuiFixedVerificationTool{result: tool.Result{Content: "ok"}, calls: &calls})
	executor := &tools.Executor{Registry: registry, DispatchGate: stadoruntime.SchedulingDispatchGate(controller)}
	args := json.RawMessage(`{"command":"go test ./...","timeout_ms":300000}`)
	if _, err := executor.Run(context.Background(), "shell__exec", args, nil); !errors.Is(err, stadoruntime.ErrScheduleHeld) {
		t.Fatalf("ordinary worker dispatch escaped hold: %v", err)
	}
	bridge := &tuiVerificationBridge{record: tuiVerificationRecord("requested")}
	completed, err := serviceApplicationVerifications(context.Background(), []*stadoruntime.LoadedLifecycleApplication{tuiVerificationApplication(bridge)}, executor, controller, nil, stadoruntime.VerifyConfig{Commands: []string{"go test ./..."}, MaxRounds: 1}, acceptVerificationAnchor)
	if err != nil || completed != 1 || calls != 1 || bridge.finish.Outcome != "commands_succeeded" {
		t.Fatalf("completed=%d calls=%d finish=%+v err=%v", completed, calls, bridge.finish, err)
	}
	controller.state = stadoruntime.ScheduleCompleted
	bridge = &tuiVerificationBridge{record: tuiVerificationRecord("requested")}
	completed, err = serviceApplicationVerifications(context.Background(), []*stadoruntime.LoadedLifecycleApplication{tuiVerificationApplication(bridge)}, executor, controller, nil, stadoruntime.VerifyConfig{Commands: []string{"go test ./..."}, MaxRounds: 1}, acceptVerificationAnchor)
	if err != nil || completed != 1 || calls != 1 || bridge.finish.Outcome != "command_failed" {
		t.Fatalf("terminal schedule completed=%d calls=%d finish=%+v err=%v", completed, calls, bridge.finish, err)
	}
}

func TestApplicationVerificationRunsUnderDurableBrokerHoldWhileWorkerStaysBlocked(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := brokerapplication.New(store)
	auth := brokerapplication.Authority{
		SessionID: "session-1", Generation: 3, PluginID: "plugin#quality",
		Principal: "os-user:test", Actor: "broker:lifecycle",
	}
	if _, err := service.AcquireHold(context.Background(), auth, brokerapplication.HoldAcquire{
		ID: "verification-hold", RunID: "run-1", ReasonCode: "verification", TTL: time.Minute,
	}, "hold"); err != nil {
		t.Fatal(err)
	}
	controller := &tuiDurableVerificationScheduleController{service: service, scope: brokerapplication.SessionScope{SessionID: auth.SessionID, Generation: auth.Generation}}
	registry := tools.NewRegistry()
	calls := 0
	registry.Register(tuiFixedVerificationTool{result: tool.Result{Content: "ok"}, calls: &calls})
	executor := &tools.Executor{Registry: registry, DispatchGate: stadoruntime.SchedulingDispatchGate(controller)}
	args := json.RawMessage(`{"command":"go test ./...","timeout_ms":300000}`)
	if _, err := executor.Run(context.Background(), "shell__exec", args, nil); !errors.Is(err, stadoruntime.ErrScheduleHeld) {
		t.Fatalf("ordinary executor escaped durable hold: %v", err)
	}
	bridge := &tuiVerificationBridge{record: tuiVerificationRecord("requested")}
	completed, err := serviceApplicationVerifications(context.Background(), []*stadoruntime.LoadedLifecycleApplication{tuiVerificationApplication(bridge)}, executor, controller, nil, stadoruntime.VerifyConfig{Commands: []string{"go test ./..."}, MaxRounds: 1}, acceptVerificationAnchor)
	if err != nil || completed != 1 || calls != 1 || bridge.finish.Outcome != "commands_succeeded" {
		t.Fatalf("completed=%d calls=%d finish=%+v err=%v", completed, calls, bridge.finish, err)
	}
	status, err := controller.CheckSchedule(context.Background())
	if err != nil || status.State != stadoruntime.ScheduleHeld {
		t.Fatalf("native verification changed durable hold: status=%+v err=%v", status, err)
	}
}

func TestApplicationVerificationPersistsResolvableCommandAuditEvidence(t *testing.T) {
	m, _, _ := newSessionSwitchModel(t)
	registry := tools.NewRegistry()
	toolImpl := &tuiAuditVerificationTool{}
	registry.Register(toolImpl)
	executor := *m.executor
	executor.Registry = registry
	bridge := &tuiVerificationBridge{record: tuiVerificationRecord("requested")}
	completed, err := serviceApplicationVerifications(context.Background(), []*stadoruntime.LoadedLifecycleApplication{tuiVerificationApplication(bridge)}, &executor, nil, hostAdapter{workdir: m.session.WorktreePath}, stadoruntime.VerifyConfig{Commands: []string{"one", "two"}, MaxRounds: 1}, acceptVerificationAnchor)
	if err != nil || completed != 1 || bridge.finish.Outcome != "commands_succeeded" || len(bridge.finish.Commands) != 2 {
		t.Fatalf("completed=%d finish=%+v err=%v", completed, bridge.finish, err)
	}
	for _, fact := range bridge.finish.Commands {
		if len(fact.EvidenceRefs) != 2 {
			t.Fatalf("command %d evidence=%v", fact.Ordinal, fact.EvidenceRefs)
		}
		for _, ref := range fact.EvidenceRefs {
			at := strings.LastIndexByte(ref, '@')
			if at < 0 {
				t.Fatalf("invalid git evidence ref %q", ref)
			}
			hash := plumbing.NewHash(ref[at+1:])
			if hash.IsZero() {
				t.Fatalf("zero git evidence ref %q", ref)
			}
			if _, err := m.session.TreeFromCommit(hash); err != nil {
				t.Fatalf("unresolvable audit evidence %q: %v", ref, err)
			}
		}
	}
	if len(bridge.finish.EvidenceRefs) != 4 {
		t.Fatalf("terminal evidence union=%v", bridge.finish.EvidenceRefs)
	}

	failing := &tuiAuditVerificationTool{fail: true}
	registry = tools.NewRegistry()
	registry.Register(failing)
	executor.Registry = registry
	bridge = &tuiVerificationBridge{record: tuiVerificationRecord("requested")}
	completed, err = serviceApplicationVerifications(context.Background(), []*stadoruntime.LoadedLifecycleApplication{tuiVerificationApplication(bridge)}, &executor, nil, hostAdapter{workdir: m.session.WorktreePath}, stadoruntime.VerifyConfig{Commands: []string{"fail", "must-not-run"}, MaxRounds: 1}, acceptVerificationAnchor)
	if err != nil || completed != 1 || bridge.finish.Outcome != "command_failed" || len(bridge.finish.Commands[0].EvidenceRefs) != 2 || len(bridge.finish.Commands[1].EvidenceRefs) != 0 || failing.calls != 1 {
		t.Fatalf("failure completed=%d calls=%d finish=%+v err=%v", completed, failing.calls, bridge.finish, err)
	}
}
