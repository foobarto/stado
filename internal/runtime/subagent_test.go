package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/sandbox"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/subagent"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/foobarto/stado/pkg/tool"
)

func TestSubagentRunnerForksReadOnlyChild(t *testing.T) {
	cfg, parent, _ := forkPluginEnv(t)
	cfg.Verify.Commands = []string{"false"}
	cfg.Verify.Strict = true
	prov := &subagentCaptureProvider{}
	brokerCtl := &recordingBrokerController{worktree: filepath.Join(cfg.WorktreeDir(), "broker-child")}
	var events []SubagentEvent

	res, err := (SubagentRunner{
		Config:    cfg,
		Parent:    parent,
		Provider:  prov,
		Model:     "test-model",
		AgentName: "test-subagent",
		Broker:    brokerCtl,
		OnEvent: func(ev SubagentEvent) {
			events = append(events, ev)
		},
	}).SpawnSubagent(context.Background(), subagent.Request{
		Prompt:   "Inspect runtime subagent code.",
		Role:     subagent.DefaultRole,
		Mode:     subagent.DefaultMode,
		MaxTurns: 1,
	})
	if err != nil {
		t.Fatalf("SpawnSubagent: %v", err)
	}
	if res.Status != "completed" || res.ChildSession == "" || res.ChildSession == parent.ID {
		t.Fatalf("bad result: %+v", res)
	}
	if !strings.Contains(res.Text, "child findings") {
		t.Fatalf("text = %q", res.Text)
	}
	if !containsString(prov.toolNames, "fs__read") {
		t.Fatalf("child tools missing read: %v", prov.toolNames)
	}
	for _, forbidden := range []string{"fs__write", "fs__edit", "bash", subagent.ToolName} {
		if containsString(prov.toolNames, forbidden) {
			t.Fatalf("child tools exposed %q in read-only mode: %v", forbidden, prov.toolNames)
		}
	}

	msgs, err := LoadConversation(res.Worktree)
	if err != nil {
		t.Fatalf("LoadConversation: %v", err)
	}
	if len(msgs) != res.MessageCount {
		t.Fatalf("conversation messages = %d, result count = %d", len(msgs), res.MessageCount)
	}
	if len(msgs) == 0 || msgs[0].Role != agent.RoleUser {
		t.Fatalf("seed message missing: %+v", msgs)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want started+finished: %+v", len(events), events)
	}
	if events[0].Phase != "started" || events[0].Status != "running" {
		t.Fatalf("started event = %+v", events[0])
	}
	if events[1].Phase != "finished" || events[1].Status != "completed" {
		t.Fatalf("finished event = %+v", events[1])
	}
	if !reflect.DeepEqual(events[1].Terminal, res.Terminal) {
		t.Fatalf("finished terminal metadata = %+v, result=%+v", events[1].Terminal, res.Terminal)
	}
	if events[0].ChildSession != res.ChildSession || events[1].ChildSession != res.ChildSession {
		t.Fatalf("event child ids = %q/%q, result=%q", events[0].ChildSession, events[1].ChildSession, res.ChildSession)
	}
	brokerCtl.mu.Lock()
	requests := append([]BrokerSubagentRequest(nil), brokerCtl.requests...)
	taints := append([]ContextTaint(nil), brokerCtl.taints...)
	closed := brokerCtl.closed
	brokerCtl.mu.Unlock()
	if len(requests) != 1 || requests[0].Role != subagent.DefaultRole {
		t.Fatalf("broker child requests = %+v", requests)
	}
	if closed != 1 {
		t.Fatalf("broker child close count = %d, want 1", closed)
	}
	if len(taints) == 0 || taints[0] != ContextTainted {
		t.Fatalf("child initial taint = %v, want tainted", taints)
	}
}

func TestSubagentRunnerPinsParentTreeHeadForAsyncSpawn(t *testing.T) {
	_, parent, _ := forkPluginEnv(t)
	writeAndCommitTree(t, parent, "review anchor", map[string]string{"anchor.txt": "one"})
	want, err := parent.TreeHead()
	if err != nil || want.IsZero() {
		t.Fatalf("tree head=%s err=%v", want, err)
	}
	pinnedSpawner, err := (SubagentRunner{Parent: parent}).PinSpawnSource(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pinned, ok := pinnedSpawner.(SubagentRunner)
	if !ok || pinned.pinnedSourceHead != want {
		t.Fatalf("pinned spawner=%T head=%s want=%s", pinnedSpawner, pinned.pinnedSourceHead, want)
	}
	writeAndCommitTree(t, parent, "later worker state", map[string]string{"anchor.txt": "two"})
	after, err := parent.TreeHead()
	if err != nil || after == want {
		t.Fatalf("later tree head=%s original=%s err=%v", after, want, err)
	}
	if pinned.pinnedSourceHead != want {
		t.Fatalf("pinned source moved with parent: got=%s want=%s", pinned.pinnedSourceHead, want)
	}
}

func TestSubagentRunnerPinsExactApplicationTurnRefBeforeWorkerMoves(t *testing.T) {
	cfg, parent, _ := forkPluginEnv(t)
	writeAndCommitTree(t, parent, "review anchor", map[string]string{"anchor.txt": "one"})
	want, err := parent.TreeHead()
	if err != nil || want.IsZero() {
		t.Fatalf("tree head=%s err=%v", want, err)
	}
	turnRef := "git:" + stadogit.TreeRef(parent.ID).String() + "@" + want.String() + "#turn-1-iteration-1"
	runner := SubagentRunner{
		Config: cfg, Parent: parent, Provider: &subagentCaptureProvider{}, Model: "test-model",
		ResolveSource: ResolveTreeSource(parent, cfg.WorktreeDir()),
	}
	pinnedSpawner, err := runner.PinSpawnRequestSource(context.Background(), &subagent.Source{At: turnRef})
	if err != nil {
		t.Fatal(err)
	}
	writeAndCommitTree(t, parent, "later worker state", map[string]string{"anchor.txt": "two"})
	result, err := pinnedSpawner.SpawnSubagent(context.Background(), subagent.Request{
		Prompt: "Review only the authenticated anchor.", Source: &subagent.Source{At: turnRef}, MaxTurns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(result.Worktree, "anchor.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "one" {
		t.Fatalf("reviewer inspected moving worker tip: anchor.txt=%q", got)
	}
	conversation, err := LoadConversation(result.Worktree)
	if err != nil {
		t.Fatal(err)
	}
	if len(conversation) == 0 || conversation[0].Role != agent.RoleUser ||
		!strings.Contains(conversation[0].Content[0].Text.Text, "Review only") {
		t.Fatalf("exact reviewer did not start from a fresh prompt: %#v", conversation)
	}
}

func TestSubagentRunnerRejectsNonCanonicalApplicationTurnRefCommit(t *testing.T) {
	_, parent, _ := forkPluginEnv(t)
	writeAndCommitTree(t, parent, "review anchor", map[string]string{"anchor.txt": "one"})
	head, err := parent.TreeHead()
	if err != nil || head.IsZero() {
		t.Fatalf("tree head=%s err=%v", head, err)
	}
	turnRef := "git:" + stadogit.TreeRef(parent.ID).String() + "@" + strings.ToUpper(head.String()) + "#turn-1-iteration-1"
	if _, err := parseApplicationTurnRef(parent, turnRef); err == nil || !strings.Contains(err.Error(), "lowercase hex") {
		t.Fatalf("parse uppercase exact source err=%v", err)
	}
}

func TestSubagentRunnerRetainedForkPointSurvivesParentMovement(t *testing.T) {
	cfg, parent, _ := forkPluginEnv(t)
	cfg.Verify.Commands = []string{"false"}
	cfg.Verify.Strict = true
	writeAndCommitTree(t, parent, "retained anchor", map[string]string{"anchor.txt": "one"})
	wantTree, err := parent.TreeHead()
	if err != nil || wantTree.IsZero() {
		t.Fatalf("tree head=%s err=%v", wantTree, err)
	}
	seed, err := historicalSeed(parent, "turns/0")
	if err != nil {
		t.Fatal(err)
	}
	seedBytes, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(seedBytes)
	runner := SubagentRunner{
		Config:    cfg,
		Parent:    parent,
		Provider:  &subagentCaptureProvider{},
		Model:     "test-model",
		AgentName: "retained-test",
	}
	pinnedSpawner, err := runner.PinSpawnForkPoint(context.Background(), SpawnForkPoint{
		SourceSessionID:    parent.ID,
		SourceGeneration:   1,
		CommittedTurn:      0,
		ConversationDigest: hex.EncodeToString(digest[:]),
		TreeCommit:         wantTree.String(),
		TraceCommit:        "empty",
	})
	if err != nil {
		t.Fatal(err)
	}
	writeAndCommitTree(t, parent, "later parent state", map[string]string{"anchor.txt": "two"})

	result, err := pinnedSpawner.SpawnSubagent(context.Background(), subagent.Request{
		Prompt:   "Inspect the admitted snapshot.",
		Role:     subagent.DefaultRole,
		Mode:     subagent.DefaultMode,
		MaxTurns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(result.Worktree, "anchor.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "one" {
		t.Fatalf("child used moving parent tip: anchor.txt=%q", got)
	}
}

func TestSubagentRunnerWorkerUsesScopedWriteTools(t *testing.T) {
	cfg, parent, _ := forkPluginEnv(t)
	cfg.Verify.Commands = []string{"false"}
	cfg.Verify.Strict = true
	prov := &workerSubagentProvider{}

	res, err := (SubagentRunner{
		Config:    cfg,
		Parent:    parent,
		Provider:  prov,
		Model:     "test-model",
		AgentName: "test-worker",
	}).SpawnSubagent(context.Background(), subagent.Request{
		Prompt:     "Write the allowed file and report blocked attempts.",
		Role:       subagent.WorkerRole,
		Mode:       subagent.WorkspaceWriteMode,
		Ownership:  "allowed directory only",
		WriteScope: []string{"allowed/**"},
		MaxTurns:   2,
	})
	if err != nil {
		t.Fatalf("SpawnSubagent: %v", err)
	}
	if res.Status != "completed" || res.Mode != subagent.WorkspaceWriteMode {
		t.Fatalf("bad result: %+v", res)
	}
	for _, want := range []string{"fs__read", "fs__write", "fs__edit"} {
		if !containsString(prov.toolNames, want) {
			t.Fatalf("worker tools missing %q: %v", want, prov.toolNames)
		}
	}
	for _, forbidden := range []string{"bash", "ast_grep", "webfetch", subagent.ToolName} {
		if containsString(prov.toolNames, forbidden) {
			t.Fatalf("worker tools exposed %q: %v", forbidden, prov.toolNames)
		}
	}
	written := filepath.Join(res.Worktree, "allowed", "new.txt")
	data, err := os.ReadFile(written)
	if err != nil {
		t.Fatalf("read child write: %v", err)
	}
	if string(data) != "child write" {
		t.Fatalf("child write = %q", data)
	}
	if want := []string{"allowed/new.txt"}; !reflect.DeepEqual(res.ChangedFiles, want) {
		t.Fatalf("changed_files = %#v, want %#v", res.ChangedFiles, want)
	}
	wantAdoptionCommand := "stado session adopt " + parent.ID + " " + res.ChildSession + " --apply"
	if res.AdoptionCommand != wantAdoptionCommand {
		t.Fatalf("adoption_command = %q, want %q", res.AdoptionCommand, wantAdoptionCommand)
	}
	if _, err := os.Stat(filepath.Join(parent.WorktreePath, "allowed", "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("parent worktree was modified, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(res.Worktree, "blocked", "new.txt")); !os.IsNotExist(err) {
		t.Fatalf("blocked write created file, stat err = %v", err)
	}
	if got := prov.toolResult("allow"); got == nil || got.IsError {
		t.Fatalf("allow tool result = %+v", got)
	}
	blocked := prov.toolResult("block")
	if blocked == nil || !blocked.IsError || !strings.Contains(blocked.Content, "outside write_scope") {
		t.Fatalf("blocked tool result = %+v", blocked)
	}
	if len(res.ScopeViolations) != 1 || !strings.Contains(res.ScopeViolations[0], "blocked/new.txt") {
		t.Fatalf("scope_violations = %#v, want blocked/new.txt", res.ScopeViolations)
	}
}

func TestConfigureWorkerToolsPreservesBrokerSandboxPolicy(t *testing.T) {
	_, parent, _ := forkPluginEnv(t)
	policy := &struct{ name string }{name: "broker-child"}
	runner := sandbox.NoneRunner{}
	exec := &tools.Executor{Registry: BuildDefaultRegistry(nil), Session: parent, Runner: runner}

	host, scoped, err := configureSubagentTools(subagent.Request{
		Mode: subagent.WorkspaceWriteMode, WriteScope: []string{"allowed/**"},
	}, exec, policy)
	if err != nil {
		t.Fatal(err)
	}
	if host != scoped || scoped == nil {
		t.Fatalf("worker host=%T scoped=%p", host, scoped)
	}
	provider, ok := host.(tool.SandboxPolicyProvider)
	if !ok {
		t.Fatalf("worker host %T does not expose sandbox policy", host)
	}
	if got := provider.DefaultSandboxPolicy(); got != policy {
		t.Fatalf("worker sandbox policy=%#v, want broker policy", got)
	}
	runnerProvider, ok := host.(interface{ Runner() sandbox.Runner })
	if !ok {
		t.Fatalf("worker host %T does not expose sandbox runner", host)
	}
	if _, ok := runnerProvider.Runner().(sandbox.NoneRunner); !ok {
		t.Fatalf("worker sandbox runner=%T, want sandbox.NoneRunner", runnerProvider.Runner())
	}
}

func TestSubagentRunnerRejectsWorkerWithoutScope(t *testing.T) {
	cfg, parent, _ := forkPluginEnv(t)
	_, err := (SubagentRunner{
		Config:    cfg,
		Parent:    parent,
		Provider:  &subagentCaptureProvider{},
		Model:     "test-model",
		AgentName: "test-worker",
	}).SpawnSubagent(context.Background(), subagent.Request{
		Prompt:    "Write files.",
		Role:      subagent.WorkerRole,
		Mode:      subagent.WorkspaceWriteMode,
		Ownership: "missing scope",
		MaxTurns:  1,
	})
	if err == nil {
		t.Fatal("expected missing write_scope error")
	}
	if !strings.Contains(err.Error(), "write_scope is required") {
		t.Fatalf("error = %v", err)
	}
}

func TestSubagentRunnerReturnsTimeoutResult(t *testing.T) {
	cfg, parent, _ := forkPluginEnv(t)

	res, err := (SubagentRunner{
		Config:    cfg,
		Parent:    parent,
		Provider:  blockingSubagentProvider{},
		Model:     "test-model",
		AgentName: "test-subagent",
	}).SpawnSubagent(context.Background(), subagent.Request{
		Prompt:         "This child blocks.",
		Role:           subagent.DefaultRole,
		Mode:           subagent.DefaultMode,
		MaxTurns:       1,
		TimeoutSeconds: 1,
	})
	if err != nil {
		t.Fatalf("SpawnSubagent: %v", err)
	}
	if res.Status != "timeout" {
		t.Fatalf("status = %q, want timeout", res.Status)
	}
	if res.ChildSession == "" || res.Worktree == "" {
		t.Fatalf("timeout result missing child identity: %+v", res)
	}
	if !strings.Contains(res.Error, "timed out after 1") {
		t.Fatalf("error = %q", res.Error)
	}
}

func TestSubagentRunnerPropagatesParentCancellation(t *testing.T) {
	cfg, parent, _ := forkPluginEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	var events []SubagentEvent

	type terminalResult struct {
		result subagent.Result
		err    error
	}
	done := make(chan terminalResult, 1)
	go func() {
		result, err := (SubagentRunner{
			Config:    cfg,
			Parent:    parent,
			Provider:  blockingSubagentProvider{},
			Model:     "test-model",
			AgentName: "test-subagent",
			OnEvent: func(ev SubagentEvent) {
				events = append(events, ev)
			},
		}).SpawnSubagent(ctx, subagent.Request{
			Prompt:         "This child waits for parent cancellation.",
			Role:           subagent.DefaultRole,
			Mode:           subagent.DefaultMode,
			MaxTurns:       1,
			TimeoutSeconds: 60,
		})
		done <- terminalResult{result: result, err: err}
	}()

	cancel()
	got := <-done
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", got.err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want started+finished: %+v", len(events), events)
	}
	if events[1].Phase != "finished" || events[1].Status != "error" {
		t.Fatalf("finished event = %+v", events[1])
	}
	if !strings.Contains(events[1].Error, "context canceled") {
		t.Fatalf("finished event error = %q", events[1].Error)
	}
	if got.result.Status != "error" || got.result.Error != events[1].Error || !reflect.DeepEqual(got.result.Terminal, events[1].Terminal) {
		t.Fatalf("terminal error result diverged from finished event: result=%+v event=%+v", got.result, events[1])
	}
}

type subagentCaptureProvider struct {
	toolNames []string
}

func (p *subagentCaptureProvider) Name() string {
	return "capture"
}

func (p *subagentCaptureProvider) Capabilities() agent.Capabilities {
	return agent.Capabilities{}
}

func (p *subagentCaptureProvider) StreamTurn(_ context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	p.toolNames = p.toolNames[:0]
	for _, def := range req.Tools {
		p.toolNames = append(p.toolNames, def.Name)
	}
	ch := make(chan agent.Event, 2)
	ch <- agent.Event{Kind: agent.EvTextDelta, Text: "child findings"}
	ch <- agent.Event{Kind: agent.EvDone}
	close(ch)
	return ch, nil
}

type terminalFactsProvider struct {
	closes   int
	closeErr error
}

type subagentProfileProvider struct {
	request agent.TurnRequest
}

func (p *subagentProfileProvider) Name() string { return "review-provider" }
func (p *subagentProfileProvider) Capabilities() agent.Capabilities {
	return agent.Capabilities{SupportsThinking: true, SupportsReasoningEffort: true}
}
func (p *subagentProfileProvider) StreamTurn(_ context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	p.request = req
	ch := make(chan agent.Event, 2)
	ch <- agent.Event{Kind: agent.EvTextDelta, Text: "approved"}
	ch <- agent.Event{Kind: agent.EvDone}
	close(ch)
	return ch, nil
}

func TestSubagentProviderProfileIsResolvedAndForwarded(t *testing.T) {
	cfg, parent, _ := forkPluginEnv(t)
	profileProvider := &subagentProfileProvider{}
	runner := SubagentRunner{
		Config: cfg, Parent: parent, Provider: &subagentCaptureProvider{}, Model: "worker",
		Thinking: "off", ThinkingBudgetTokens: 1000,
		ResolveProviderModel: func(_ context.Context, providerName, modelName string) (agent.Provider, string, error) {
			if providerName != "review-provider" || modelName != "review-model" {
				t.Fatalf("resolver input = %q/%q", providerName, modelName)
			}
			return profileProvider, modelName, nil
		},
	}
	_, err := runner.SpawnSubagent(t.Context(), subagent.Request{
		Prompt: "review", Provider: "review-provider", Model: "review-model",
		Thinking: "on", ThinkingBudgetTokens: 8000, ReasoningEffort: "xhigh",
		TokenBudget: 20_000, MaxTurns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if profileProvider.request.Model != "review-model" || profileProvider.request.Thinking == nil ||
		profileProvider.request.Thinking.BudgetTokens != 8000 || profileProvider.request.ReasoningEffort != "xhigh" {
		t.Fatalf("provider request = %#v", profileProvider.request)
	}
}

func TestSubagentProviderProfileRejectsUnsupportedControlsAndClosesResolvedProvider(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request subagent.Request
		wantErr string
	}{
		{
			name:    "thinking",
			request: subagent.Request{Prompt: "review", Provider: "terminal-facts", Model: "reviewer", Thinking: "on", MaxTurns: 1},
			wantErr: "thinking is unsupported",
		},
		{
			name:    "reasoning effort",
			request: subagent.Request{Prompt: "review", Provider: "terminal-facts", Model: "reviewer", ReasoningEffort: "high", MaxTurns: 1},
			wantErr: "reasoning effort is unsupported",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, parent, _ := forkPluginEnv(t)
			provider := &terminalFactsProvider{}
			runner := SubagentRunner{
				Config: cfg, Parent: parent, Provider: &subagentCaptureProvider{}, Model: "worker",
				ResolveProviderModel: func(context.Context, string, string) (agent.Provider, string, error) {
					return provider, "reviewer", nil
				},
			}
			_, err := runner.SpawnSubagent(t.Context(), tc.request)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
			if provider.closes != 1 {
				t.Fatalf("provider closes = %d, want 1", provider.closes)
			}
		})
	}
}

func TestSubagentProviderProfileRejectsResolverModelSubstitution(t *testing.T) {
	cfg, parent, _ := forkPluginEnv(t)
	provider := &terminalFactsProvider{}
	runner := SubagentRunner{
		Config: cfg, Parent: parent, Provider: &subagentCaptureProvider{}, Model: "worker",
		ResolveProviderModel: func(context.Context, string, string) (agent.Provider, string, error) {
			return provider, "silent-substitute", nil
		},
	}
	_, err := runner.SpawnSubagent(t.Context(), subagent.Request{
		Prompt: "review", Provider: "terminal-facts", Model: "requested", MaxTurns: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "was not resolved exactly") {
		t.Fatalf("error = %v", err)
	}
	if provider.closes != 1 {
		t.Fatalf("provider closes = %d, want 1", provider.closes)
	}
}

func (p *terminalFactsProvider) Name() string                     { return "terminal-facts" }
func (p *terminalFactsProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (p *terminalFactsProvider) StreamTurn(_ context.Context, _ agent.TurnRequest) (<-chan agent.Event, error) {
	ch := make(chan agent.Event, 2)
	ch <- agent.Event{Kind: agent.EvTextDelta, Text: "valid reviewer verdict"}
	ch <- agent.Event{Kind: agent.EvDone, Usage: &agent.Usage{
		InputTokens: 21, OutputTokens: 8, CacheReadTokens: 5, CacheWriteTokens: 3,
	}}
	close(ch)
	return ch, nil
}
func (p *terminalFactsProvider) Close() error {
	p.closes++
	return p.closeErr
}

func TestSubagentTerminalFactsKeepResultAcrossCleanupFailure(t *testing.T) {
	cfg, parent, _ := forkPluginEnv(t)
	provider := &terminalFactsProvider{closeErr: errors.New("transport cleanup failed at secret endpoint")}
	result, err := (SubagentRunner{
		Config: cfg, Parent: parent, Provider: &subagentCaptureProvider{}, Model: "worker",
		ResolveProviderModel: func(context.Context, string, string) (agent.Provider, string, error) {
			return provider, "reviewer", nil
		},
	}).SpawnSubagent(context.Background(), subagent.Request{
		Prompt: "review", Model: "reviewer", MaxTurns: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "valid reviewer verdict" || provider.closes != 1 {
		t.Fatalf("result=%#v closes=%d", result, provider.closes)
	}
	if !result.Terminal.UsageComplete || result.Terminal.Usage != (subagent.TokenUsage{
		InputTokens: 21, OutputTokens: 8, CacheReadTokens: 5, CacheWriteTokens: 3,
	}) {
		t.Fatalf("terminal usage = %#v", result.Terminal)
	}
	wantHash := sha256.Sum256([]byte(provider.closeErr.Error()))
	if result.Terminal.Cleanup == nil || result.Terminal.Cleanup.Kind != "provider_close" ||
		result.Terminal.Cleanup.Fingerprint != "sha256:"+hex.EncodeToString(wantHash[:]) {
		t.Fatalf("cleanup diagnostic = %#v", result.Terminal.Cleanup)
	}
	if strings.Contains(result.Terminal.Cleanup.Fingerprint, "secret") {
		t.Fatal("raw cleanup detail crossed the terminal metadata boundary")
	}
}

type workerSubagentProvider struct {
	turn        int
	toolNames   []string
	toolResults []agent.ToolResultBlock
}

func (p *workerSubagentProvider) Name() string {
	return "worker-capture"
}

func (p *workerSubagentProvider) Capabilities() agent.Capabilities {
	return agent.Capabilities{}
}

func (p *workerSubagentProvider) StreamTurn(_ context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	if p.turn == 0 {
		p.toolNames = p.toolNames[:0]
		for _, def := range req.Tools {
			p.toolNames = append(p.toolNames, def.Name)
		}
		p.turn++
		ch := make(chan agent.Event, 3)
		ch <- agent.Event{Kind: agent.EvToolCallEnd, ToolCall: &agent.ToolUseBlock{
			ID:    "allow",
			Name:  "fs__write",
			Input: json.RawMessage(`{"path":"allowed/new.txt","content":"child write"}`),
		}}
		ch <- agent.Event{Kind: agent.EvToolCallEnd, ToolCall: &agent.ToolUseBlock{
			ID:    "block",
			Name:  "fs__write",
			Input: json.RawMessage(`{"path":"blocked/new.txt","content":"should not land"}`),
		}}
		ch <- agent.Event{Kind: agent.EvDone}
		close(ch)
		return ch, nil
	}
	for _, msg := range req.Messages {
		for _, block := range msg.Content {
			if block.ToolResult != nil {
				p.toolResults = append(p.toolResults, *block.ToolResult)
			}
		}
	}
	ch := make(chan agent.Event, 2)
	ch <- agent.Event{Kind: agent.EvTextDelta, Text: "worker done"}
	ch <- agent.Event{Kind: agent.EvDone}
	close(ch)
	return ch, nil
}

func (p *workerSubagentProvider) toolResult(id string) *agent.ToolResultBlock {
	for i := range p.toolResults {
		if p.toolResults[i].ToolUseID == id {
			return &p.toolResults[i]
		}
	}
	return nil
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

type blockingSubagentProvider struct{}

func (blockingSubagentProvider) Name() string {
	return "blocking"
}

func (blockingSubagentProvider) Capabilities() agent.Capabilities {
	return agent.Capabilities{}
}

func (blockingSubagentProvider) StreamTurn(ctx context.Context, _ agent.TurnRequest) (<-chan agent.Event, error) {
	ch := make(chan agent.Event, 1)
	go func() {
		defer close(ch)
		<-ctx.Done()
		ch <- agent.Event{Kind: agent.EvError, Err: ctx.Err()}
	}()
	return ch, nil
}
