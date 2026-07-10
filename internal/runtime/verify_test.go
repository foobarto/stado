package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/foobarto/stado/pkg/tool"
)

func TestAgentLoopVerificationFailureFeedsBackThenPasses(t *testing.T) {
	gate := &scriptedVerifyTool{failures: 1}
	reg := tools.NewRegistry()
	reg.Register(gate)
	provider := &verifyProvider{}
	var events []VerifyEvent

	var published []agent.Event
	final, msgs, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: provider,
		Executor: &tools.Executor{Registry: reg},
		Model:    "m",
		Messages: []agent.Message{agent.Text(agent.RoleUser, "fix it")},
		MaxTurns: 3,
		Verify: VerifyConfig{
			Commands: []string{"go test ./..."}, MaxRounds: 2,
		},
		OnVerifyEvent: func(event VerifyEvent) { events = append(events, event) },
		OnEvent:       func(event agent.Event) { published = append(published, event) },
	})
	if err != nil {
		t.Fatalf("AgentLoop: %v", err)
	}
	if provider.turns != 2 || gate.calls != 2 {
		t.Fatalf("provider turns=%d gate calls=%d, want 2/2", provider.turns, gate.calls)
	}
	if final != "candidate-2" {
		t.Fatalf("accepted final text = %q, want only second candidate", final)
	}
	var publishedText string
	for _, event := range published {
		if event.Kind == agent.EvTextDelta {
			publishedText += event.Text
		}
	}
	if publishedText != "candidate-2" {
		t.Fatalf("published text = %q, want only accepted candidate", publishedText)
	}
	foundFeedback := false
	for _, msg := range msgs {
		for _, block := range msg.Content {
			if block.Text != nil && strings.Contains(block.Text.Text, "Verification command failed") {
				foundFeedback = true
			}
		}
	}
	if !foundFeedback {
		t.Fatalf("verification feedback missing from messages: %+v", msgs)
	}
	if len(events) < 6 || events[0].Status != VerifyPending || events[1].Status != VerifyStarted || events[2].Status != VerifyFailed {
		t.Fatalf("verify events = %+v", events)
	}
}

func TestAgentLoopVerificationExhausted(t *testing.T) {
	gate := &scriptedVerifyTool{failures: 10}
	reg := tools.NewRegistry()
	reg.Register(gate)
	_, _, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: &verifyProvider{},
		Executor: &tools.Executor{Registry: reg},
		Model:    "m",
		Messages: []agent.Message{agent.Text(agent.RoleUser, "fix it")},
		MaxTurns: 4,
		Verify:   VerifyConfig{Commands: []string{"false"}, MaxRounds: 2},
	})
	if !errors.Is(err, ErrVerifyExhausted) {
		t.Fatalf("error = %v, want ErrVerifyExhausted", err)
	}
	if gate.calls != 2 {
		t.Fatalf("gate calls = %d, want 2", gate.calls)
	}
}

func TestAgentLoopVerificationInfrastructurePosture(t *testing.T) {
	base := AgentLoopOptions{
		Provider: &verifyProvider{}, Model: "m",
		Messages: []agent.Message{agent.Text(agent.RoleUser, "done")}, MaxTurns: 1,
		Verify: VerifyConfig{Commands: []string{"test"}, MaxRounds: 1},
	}
	if _, _, err := AgentLoop(context.Background(), base); err != nil {
		t.Fatalf("default infrastructure posture must fail open: %v", err)
	}
	base.Provider = &verifyProvider{}
	base.Verify.Strict = true
	final, _, err := AgentLoop(context.Background(), base)
	if err == nil || !strings.Contains(err.Error(), "verification infrastructure") {
		t.Fatalf("strict infrastructure error = %v", err)
	}
	if final != "" {
		t.Fatalf("strict infrastructure returned unaccepted candidate %q", final)
	}
}

func TestAgentLoopVerificationCancellationNeverFailsOpen(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reg := tools.NewRegistry()
	reg.Register(cancelVerifyTool{cancel: cancel})
	var published []agent.Event
	final, _, err := AgentLoop(ctx, AgentLoopOptions{
		Provider: &verifyProvider{}, Executor: &tools.Executor{Registry: reg},
		Model: "m", Messages: []agent.Message{agent.Text(agent.RoleUser, "done")},
		MaxTurns: 1, Verify: VerifyConfig{Commands: []string{"wait"}, MaxRounds: 1},
		OnEvent: func(event agent.Event) { published = append(published, event) },
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v, want context.Canceled", err)
	}
	if final != "" {
		t.Fatalf("cancelled verification returned unaccepted candidate %q", final)
	}
	for _, event := range published {
		if event.Kind == agent.EvTextDelta {
			t.Fatalf("cancelled candidate was published: %+v", published)
		}
	}
}

func TestAgentLoopPublishesAcceptedCandidateBeforeVerifyPassed(t *testing.T) {
	gate := &scriptedVerifyTool{}
	reg := tools.NewRegistry()
	reg.Register(gate)
	var timeline []string

	_, _, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: &verifyProvider{}, Executor: &tools.Executor{Registry: reg},
		Model: "m", Messages: []agent.Message{agent.Text(agent.RoleUser, "done")},
		MaxTurns: 1, Verify: VerifyConfig{Commands: []string{"go test ./..."}, MaxRounds: 1},
		OnEvent: func(event agent.Event) {
			kind := "other"
			switch event.Kind {
			case agent.EvTextDelta:
				kind = "text_delta"
			case agent.EvDone:
				kind = "done"
			}
			timeline = append(timeline, "agent:"+kind)
		},
		OnVerifyEvent: func(event VerifyEvent) {
			timeline = append(timeline, "verify:"+string(event.Status))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"verify:pending_candidate", "verify:started", "agent:text_delta", "agent:done", "verify:passed"}
	if strings.Join(timeline, ",") != strings.Join(want, ",") {
		t.Fatalf("event order = %v, want %v", timeline, want)
	}
}

func TestAgentLoopClosesPendingVerificationOnGenerationError(t *testing.T) {
	var statuses []VerifyStatus
	_, _, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: verifyErrorProvider{}, Model: "m",
		Messages: []agent.Message{agent.Text(agent.RoleUser, "done")}, MaxTurns: 1,
		Verify:        VerifyConfig{Commands: []string{"go test ./..."}, MaxRounds: 1},
		OnVerifyEvent: func(event VerifyEvent) { statuses = append(statuses, event.Status) },
	})
	if err == nil || !strings.Contains(err.Error(), "provider failed") {
		t.Fatalf("generation error=%v", err)
	}
	want := []VerifyStatus{VerifyPending, VerifyGenerationError}
	if !reflect.DeepEqual(statuses, want) {
		t.Fatalf("verify statuses=%v, want %v", statuses, want)
	}
}

func TestAgentLoopClosesPendingVerificationAtCostCap(t *testing.T) {
	var statuses []VerifyStatus
	var published []agent.Event
	final, _, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: verifyCostProvider{}, Model: "m",
		Messages: []agent.Message{agent.Text(agent.RoleUser, "done")}, MaxTurns: 1,
		CostCapUSD: 1, Verify: VerifyConfig{Commands: []string{"go test ./..."}, MaxRounds: 1},
		OnEvent:       func(event agent.Event) { published = append(published, event) },
		OnVerifyEvent: func(event VerifyEvent) { statuses = append(statuses, event.Status) },
	})
	if !errors.Is(err, ErrCostCapExceeded) || final != "" || len(published) != 0 {
		t.Fatalf("cap final=%q published=%v error=%v", final, published, err)
	}
	want := []VerifyStatus{VerifyPending, VerifyGenerationError}
	if !reflect.DeepEqual(statuses, want) {
		t.Fatalf("verify statuses=%v, want %v", statuses, want)
	}
}

func TestAgentLoopCoalescesFragmentedCandidateBeforeVerification(t *testing.T) {
	gate := &scriptedVerifyTool{}
	reg := tools.NewRegistry()
	reg.Register(gate)
	var published []agent.Event
	final, _, err := AgentLoop(context.Background(), AgentLoopOptions{
		Provider: fragmentedVerifyProvider{}, Executor: &tools.Executor{Registry: reg},
		Model: "m", Messages: []agent.Message{agent.Text(agent.RoleUser, "done")},
		MaxTurns: 1, Verify: VerifyConfig{Commands: []string{"go test ./..."}, MaxRounds: 1},
		OnEvent: func(event agent.Event) { published = append(published, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(final) != maxBufferedVerifyEvents+1 {
		t.Fatalf("coalesced final length=%d", len(final))
	}
	if len(published) != 2 || published[0].Kind != agent.EvTextDelta || published[1].Kind != agent.EvDone {
		t.Fatalf("coalesced events=%d %+v", len(published), published)
	}
}

func TestRunVerificationRoundUsesBundledSandboxedShellExitStatus(t *testing.T) {
	reg := BuildDefaultRegistry(nil)
	exec := &tools.Executor{Registry: reg, Runner: sandbox.NoneRunner{}}
	host := bundledToolHost{workdir: t.TempDir(), runner: sandbox.NoneRunner{}}
	out := RunVerificationRound(context.Background(), exec, host,
		VerifyConfig{Commands: []string{"printf actual-gate; exit 9"}, MaxRounds: 1}, 1, nil)
	if out.Status != VerifyFailed || !strings.Contains(out.Output, "code 9") || !strings.Contains(out.Output, "actual-gate") {
		t.Fatalf("bundled verification outcome = %+v", out)
	}
}

type scriptedVerifyTool struct {
	calls    int
	failures int
}

type cancelVerifyTool struct{ cancel context.CancelFunc }

func (cancelVerifyTool) Name() string           { return verifyToolName }
func (cancelVerifyTool) Description() string    { return "cancel verify test" }
func (cancelVerifyTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (cancelVerifyTool) Class() tool.Class      { return tool.ClassExec }
func (t cancelVerifyTool) Run(ctx context.Context, _ json.RawMessage, _ tool.Host) (tool.Result, error) {
	t.cancel()
	return tool.Result{}, ctx.Err()
}

func (*scriptedVerifyTool) Name() string           { return verifyToolName }
func (*scriptedVerifyTool) Description() string    { return "verify test" }
func (*scriptedVerifyTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (*scriptedVerifyTool) Class() tool.Class      { return tool.ClassExec }
func (t *scriptedVerifyTool) Run(_ context.Context, args json.RawMessage, _ tool.Host) (tool.Result, error) {
	t.calls++
	if !strings.Contains(string(args), "go test") && !strings.Contains(string(args), "false") {
		return tool.Result{Error: "unexpected command"}, nil
	}
	if t.calls <= t.failures {
		return tool.Result{Error: "command exited with code 1\ntest failed"}, nil
	}
	return tool.Result{Content: "ok"}, nil
}

type verifyProvider struct{ turns int }

func (*verifyProvider) Name() string                     { return "verify-provider" }
func (*verifyProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (p *verifyProvider) StreamTurn(context.Context, agent.TurnRequest) (<-chan agent.Event, error) {
	p.turns++
	events := make(chan agent.Event, 2)
	events <- agent.Event{Kind: agent.EvTextDelta, Text: fmt.Sprintf("candidate-%d", p.turns)}
	events <- agent.Event{Kind: agent.EvDone}
	close(events)
	return events, nil
}

type verifyErrorProvider struct{}

func (verifyErrorProvider) Name() string                     { return "verify-error" }
func (verifyErrorProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (verifyErrorProvider) StreamTurn(context.Context, agent.TurnRequest) (<-chan agent.Event, error) {
	return nil, errors.New("provider failed")
}

type verifyCostProvider struct{}

func (verifyCostProvider) Name() string                     { return "verify-cost" }
func (verifyCostProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (verifyCostProvider) StreamTurn(context.Context, agent.TurnRequest) (<-chan agent.Event, error) {
	events := make(chan agent.Event, 2)
	events <- agent.Event{Kind: agent.EvTextDelta, Text: "unaccepted candidate"}
	events <- agent.Event{Kind: agent.EvDone, Usage: &agent.Usage{CostUSD: 2}}
	close(events)
	return events, nil
}

type fragmentedVerifyProvider struct{}

func (fragmentedVerifyProvider) Name() string                     { return "verify-fragmented" }
func (fragmentedVerifyProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (fragmentedVerifyProvider) StreamTurn(context.Context, agent.TurnRequest) (<-chan agent.Event, error) {
	events := make(chan agent.Event)
	go func() {
		defer close(events)
		for range maxBufferedVerifyEvents + 1 {
			events <- agent.Event{Kind: agent.EvTextDelta, Text: "x"}
		}
		events <- agent.Event{Kind: agent.EvDone}
	}()
	return events, nil
}
