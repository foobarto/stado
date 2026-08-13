package supervise

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/foobarto/stado/pkg/agent"
)

type reviewFactory struct {
	mu       sync.Mutex
	builds   int
	provider func() agent.Provider
}

func (f *reviewFactory) Build(context.Context, RoleProfile) (agent.Provider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.builds++
	return f.provider(), nil
}

type scriptedReviewProvider struct {
	turn int
	fn   func(int, agent.TurnRequest) []agent.Event
}

func (p *scriptedReviewProvider) Name() string { return "review-test" }
func (p *scriptedReviewProvider) Capabilities() agent.Capabilities {
	return agent.Capabilities{SupportsThinking: true}
}
func (p *scriptedReviewProvider) StreamTurn(_ context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	p.turn++
	events := p.fn(p.turn, req)
	ch := make(chan agent.Event, len(events))
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

type reviewSource struct {
	anchor Anchor
	reads  int
}

func (s *reviewSource) Read(_ context.Context, q EvidenceQuery) (EvidencePage, error) {
	s.reads++
	return EvidencePage{AsOf: s.anchor, Items: []EvidenceItem{{ID: "test:1", Section: q.Section, Summary: "full suite passed"}}}, nil
}
func (s *reviewSource) Search(context.Context, EvidenceQuery) (EvidencePage, error) {
	return EvidencePage{}, errors.New("unexpected search")
}
func (s *reviewSource) Follow(context.Context, EvidenceQuery) (EvidencePage, error) {
	return EvidencePage{}, errors.New("unexpected follow")
}

func TestReviewerUsesOnlyFilteredToolsAndReturnsAnchoredVerdict(t *testing.T) {
	state := State{RootSessionID: "root", SessionSequence: 4, PlanVersion: 2, ActiveStep: "build", TreeDigest: "tree", Baseline: testBaseline()}
	source := &reviewSource{anchor: state.Anchor()}
	factory := &reviewFactory{provider: func() agent.Provider {
		return &scriptedReviewProvider{fn: func(turn int, req agent.TurnRequest) []agent.Event {
			if len(req.Tools) != 3 || req.Tools[0].Name != readToolName || !strings.Contains(string(req.Tools[0].Schema), `"repository"`) || req.ReasoningEffort != "xhigh" || req.Thinking == nil {
				t.Fatalf("review request = %+v", req)
			}
			if turn == 1 {
				call := agent.ToolUseBlock{ID: "read-1", Name: readToolName, Input: json.RawMessage(`{"section":"verification","limit":10}`)}
				return []agent.Event{{Kind: agent.EvToolCallEnd, ToolCall: &call}, {Kind: agent.EvDone, Usage: &agent.Usage{InputTokens: 10, OutputTokens: 2}}}
			}
			last := req.Messages[len(req.Messages)-1]
			if last.Role != agent.RoleTool || !strings.Contains(last.Content[0].ToolResult.Content, "test:1") {
				t.Fatalf("tool evidence not returned: %+v", last)
			}
			body := `{"verdict":{"kind":"event","decision":"correct","rationale":"the worker claimed success without documentation evidence","evidence_refs":["test:1"],"correction":"reconcile documentation before completion","handoff":{"open_concerns":["documentation"]}}}`
			return []agent.Event{{Kind: agent.EvTextDelta, Text: body}, {Kind: agent.EvDone, Usage: &agent.Usage{InputTokens: 20, OutputTokens: 10}}}
		}}
	}}
	r := Reviewer{Factory: factory, Source: source}
	result, err := r.Run(context.Background(), RoleProfile{Model: "watch", Thinking: ThinkingAuto, Effort: EffortXHigh}, RoleBudget{TokenCap: 1000}, ReviewRequest{Role: RoleWatchdog, Kind: ReviewEvent, State: state})
	if err != nil {
		t.Fatal(err)
	}
	if source.reads != 1 || factory.builds != 1 {
		t.Fatalf("reads=%d builds=%d", source.reads, factory.builds)
	}
	if result.Verdict == nil || result.Verdict.Anchor != state.Anchor() || result.Verdict.Decision != VerdictCorrect {
		t.Fatalf("verdict = %+v", result.Verdict)
	}
	if result.Usage.InputTokens != 30 || result.Usage.OutputTokens != 12 {
		t.Fatalf("usage = %+v", result.Usage)
	}
}

func TestRepositoryEvidenceQueryUsesPathOnlyForRepository(t *testing.T) {
	got, err := validateEvidenceQuery(EvidenceQuery{Section: EvidenceRepository, Path: "internal/supervise/service.go"}, false)
	if err != nil || got.Limit != 20 {
		t.Fatalf("repository query=%+v err=%v", got, err)
	}
	if _, err := validateEvidenceQuery(EvidenceQuery{Section: EvidenceTranscript, Path: "secret"}, false); err == nil {
		t.Fatal("non-repository path query succeeded")
	}
	if _, err := validateEvidenceQuery(EvidenceQuery{Section: EvidenceRepository, Path: "../secret"}, false); err == nil {
		t.Fatal("repository traversal query succeeded")
	}
}

func TestReviewerBuildsFreshProviderForEachEvent(t *testing.T) {
	state := State{RootSessionID: "root", SessionSequence: 1, TreeDigest: "tree", Baseline: testBaseline()}
	source := &reviewSource{anchor: state.Anchor()}
	factory := &reviewFactory{provider: func() agent.Provider {
		return &scriptedReviewProvider{fn: func(_ int, _ agent.TurnRequest) []agent.Event {
			return []agent.Event{{Kind: agent.EvTextDelta, Text: `{"verdict":{"kind":"event","decision":"continue","rationale":"aligned"}}`}, {Kind: agent.EvDone}}
		}}
	}}
	r := Reviewer{Factory: factory, Source: source}
	for i := 0; i < 2; i++ {
		if _, err := r.Run(context.Background(), RoleProfile{Thinking: ThinkingOff, Effort: EffortLow}, RoleBudget{}, ReviewRequest{Role: RoleWatchdog, Kind: ReviewEvent, State: state}); err != nil {
			t.Fatal(err)
		}
	}
	if factory.builds != 2 {
		t.Fatalf("provider builds = %d, want fresh provider per review", factory.builds)
	}
}

func TestBaselineReviewerCannotApproveItsOwnProposal(t *testing.T) {
	state := State{RootSessionID: "root", TreeDigest: "tree"}
	source := &reviewSource{anchor: state.Anchor()}
	raw, _ := json.Marshal(struct {
		Baseline Baseline `json:"baseline"`
	}{testBaseline()})
	factory := &reviewFactory{provider: func() agent.Provider {
		return &scriptedReviewProvider{fn: func(_ int, _ agent.TurnRequest) []agent.Event {
			return []agent.Event{{Kind: agent.EvTextDelta, Text: string(raw)}, {Kind: agent.EvDone}}
		}}
	}}
	r := Reviewer{Factory: factory, Source: source}
	result, err := r.Run(context.Background(), RoleProfile{Thinking: ThinkingOff, Effort: EffortMedium}, RoleBudget{}, ReviewRequest{Role: RoleWatchdog, Kind: ReviewBaseline, State: state, ObjectiveSeed: "build it"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Baseline == nil || result.Verdict != nil {
		t.Fatalf("baseline result = %+v", result)
	}
}

func TestVerifierCannotReviewOrdinaryEvent(t *testing.T) {
	r := Reviewer{Factory: &reviewFactory{}, Source: &reviewSource{}}
	_, err := r.Run(context.Background(), RoleProfile{}, RoleBudget{}, ReviewRequest{Role: RoleVerifier, Kind: ReviewEvent})
	if err == nil {
		t.Fatal("verifier ordinary event review succeeded")
	}
}

func TestReviewerRejectsTrailingJSONAndDisablesThinking(t *testing.T) {
	state := State{RootSessionID: "root", TreeDigest: "tree", Baseline: testBaseline()}
	source := &reviewSource{anchor: state.Anchor()}
	factory := &reviewFactory{provider: func() agent.Provider {
		return &scriptedReviewProvider{fn: func(_ int, req agent.TurnRequest) []agent.Event {
			if req.Thinking != nil || req.ReasoningEffort != "low" {
				t.Fatalf("thinking/effort request = %+v", req)
			}
			return []agent.Event{{Kind: agent.EvTextDelta, Text: `{"verdict":{"kind":"event","decision":"continue","rationale":"aligned"}} {}`}, {Kind: agent.EvDone}}
		}}
	}}
	_, err := (Reviewer{Factory: factory, Source: source}).Run(context.Background(), RoleProfile{Thinking: ThinkingOff, Effort: EffortLow}, RoleBudget{}, ReviewRequest{Role: RoleWatchdog, Kind: ReviewEvent, State: state})
	if err == nil || !strings.Contains(err.Error(), "multiple JSON") {
		t.Fatalf("trailing JSON error = %v", err)
	}
}

func TestReviewerRejectsInvalidProviderUsage(t *testing.T) {
	state := State{RootSessionID: "root", TreeDigest: "tree", Baseline: testBaseline()}
	source := &reviewSource{anchor: state.Anchor()}
	factory := &reviewFactory{provider: func() agent.Provider {
		return &scriptedReviewProvider{fn: func(_ int, _ agent.TurnRequest) []agent.Event {
			return []agent.Event{{Kind: agent.EvTextDelta, Text: `{"verdict":{"kind":"event","decision":"continue","rationale":"aligned"}}`}, {Kind: agent.EvDone, Usage: &agent.Usage{InputTokens: 1, CostUSD: math.NaN()}}}
		}}
	}}
	_, err := (Reviewer{Factory: factory, Source: source}).Run(context.Background(), RoleProfile{Thinking: ThinkingOff, Effort: EffortLow}, RoleBudget{}, ReviewRequest{Role: RoleWatchdog, Kind: ReviewEvent, State: state})
	if err == nil || !strings.Contains(err.Error(), "invalid usage") {
		t.Fatalf("invalid provider usage error = %v", err)
	}
}

func TestFollowupReviewPacketIncludesOnlyClassifiedPrompt(t *testing.T) {
	state := State{RootSessionID: "root", TreeDigest: "tree", Baseline: testBaseline(), ActiveStep: "build", PlanVersion: 1}
	source := &reviewSource{anchor: state.Anchor()}
	factory := &reviewFactory{provider: func() agent.Provider {
		return &scriptedReviewProvider{fn: func(_ int, req agent.TurnRequest) []agent.Event {
			body := req.Messages[0].Content[0].Text.Text
			if !strings.Contains(body, `"operator_followup":"also redesign the website"`) || !strings.Contains(req.System, "separate task") {
				t.Fatalf("followup request body=%s system=%s", body, req.System)
			}
			return []agent.Event{{Kind: agent.EvTextDelta, Text: `{"verdict":{"kind":"followup","decision":"reject","rationale":"separate objective"}}`}, {Kind: agent.EvDone}}
		}}
	}}
	result, err := (Reviewer{Factory: factory, Source: source}).Run(context.Background(), RoleProfile{Thinking: ThinkingOff, Effort: EffortMedium}, RoleBudget{}, ReviewRequest{Role: RoleWatchdog, Kind: ReviewFollowup, State: state, Followup: "also redesign the website"})
	if err != nil || result.Verdict == nil || result.Verdict.Decision != VerdictReject {
		t.Fatalf("followup result=%+v err=%v", result, err)
	}
}

func TestReviewerRejectsDecisionThatHostCannotApplyForReviewKind(t *testing.T) {
	state := State{RootSessionID: "root", TreeDigest: "tree", Baseline: testBaseline()}
	source := &reviewSource{anchor: state.Anchor()}
	factory := &reviewFactory{provider: func() agent.Provider {
		return &scriptedReviewProvider{fn: func(_ int, _ agent.TurnRequest) []agent.Event {
			return []agent.Event{{Kind: agent.EvTextDelta, Text: `{"verdict":{"kind":"event","decision":"reject","rationale":"cannot map reject to an event transition"}}`}, {Kind: agent.EvDone}}
		}}
	}}
	_, err := (Reviewer{Factory: factory, Source: source}).Run(context.Background(), RoleProfile{Thinking: ThinkingOff, Effort: EffortLow}, RoleBudget{}, ReviewRequest{Role: RoleWatchdog, Kind: ReviewEvent, State: state})
	if err == nil || !strings.Contains(err.Error(), "invalid for event review") {
		t.Fatalf("event reject error = %v", err)
	}
}

func TestReviewerRejectsAuthorityBearingApprovalWithoutEvidence(t *testing.T) {
	state := State{RootSessionID: "root", TreeDigest: "tree", Baseline: testBaseline()}
	source := &reviewSource{anchor: state.Anchor()}
	factory := &reviewFactory{provider: func() agent.Provider {
		return &scriptedReviewProvider{fn: func(_ int, _ agent.TurnRequest) []agent.Event {
			return []agent.Event{{Kind: agent.EvTextDelta, Text: `{"verdict":{"kind":"completion","decision":"approve","rationale":"looks done"}}`}, {Kind: agent.EvDone}}
		}}
	}}
	_, err := (Reviewer{Factory: factory, Source: source}).Run(context.Background(), RoleProfile{Thinking: ThinkingOff, Effort: EffortLow}, RoleBudget{}, ReviewRequest{Role: RoleVerifier, Kind: ReviewCompletion, State: state})
	if err == nil || !strings.Contains(err.Error(), "requires evidence references") {
		t.Fatalf("evidence-free completion approval error = %v", err)
	}
}

func TestReviewerRejectsCitationThatHostDidNotServe(t *testing.T) {
	state := State{RootSessionID: "root", TreeDigest: "tree", Baseline: testBaseline()}
	source := &reviewSource{anchor: state.Anchor()}
	factory := &reviewFactory{provider: func() agent.Provider {
		return &scriptedReviewProvider{fn: func(turn int, _ agent.TurnRequest) []agent.Event {
			if turn == 1 {
				call := agent.ToolUseBlock{ID: "read-1", Name: readToolName, Input: json.RawMessage(`{"section":"verification"}`)}
				return []agent.Event{{Kind: agent.EvToolCallEnd, ToolCall: &call}, {Kind: agent.EvDone}}
			}
			return []agent.Event{{Kind: agent.EvTextDelta, Text: `{"verdict":{"kind":"completion","decision":"approve","rationale":"done","evidence_refs":["hallucinated:1"]}}`}, {Kind: agent.EvDone}}
		}}
	}}
	_, err := (Reviewer{Factory: factory, Source: source}).Run(context.Background(), RoleProfile{Thinking: ThinkingOff, Effort: EffortLow}, RoleBudget{}, ReviewRequest{Role: RoleVerifier, Kind: ReviewCompletion, State: state})
	if err == nil || !strings.Contains(err.Error(), "was not served") {
		t.Fatalf("hallucinated citation error = %v", err)
	}
}
