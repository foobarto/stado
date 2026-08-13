package supervise

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
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
	turn        int
	countTokens int
	countErr    error
	fn          func(int, agent.TurnRequest) []agent.Event
}

func (p *scriptedReviewProvider) Name() string { return "review-test" }
func (p *scriptedReviewProvider) Capabilities() agent.Capabilities {
	return agent.Capabilities{SupportsThinking: true, SupportsReasoningEffort: true}
}
func (p *scriptedReviewProvider) CountTokens(context.Context, agent.TurnRequest) (int, error) {
	if p.countErr != nil {
		return 0, p.countErr
	}
	if p.countTokens > 0 {
		return p.countTokens, nil
	}
	return 1, nil
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

type noReasoningReviewProvider struct {
	*scriptedReviewProvider
}

func (p *noReasoningReviewProvider) Capabilities() agent.Capabilities {
	return agent.Capabilities{SupportsThinking: true}
}

type reservedThinkingReviewProvider struct {
	*scriptedReviewProvider
	reserve int
}

func (p *reservedThinkingReviewProvider) Capabilities() agent.Capabilities {
	return agent.Capabilities{SupportsThinking: true, ThinkingOutputReserveTokens: p.reserve}
}

type managedReviewProvider struct {
	*scriptedReviewProvider
	closeErr error
	closes   int
}

func (p *managedReviewProvider) Close() error {
	p.closes++
	return p.closeErr
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

type pagedReviewSource struct {
	anchor   Anchor
	searches []EvidenceQuery
}

func (s *pagedReviewSource) Read(context.Context, EvidenceQuery) (EvidencePage, error) {
	return EvidencePage{}, errors.New("search continuation degraded into read")
}

func (s *pagedReviewSource) Search(_ context.Context, q EvidenceQuery) (EvidencePage, error) {
	s.searches = append(s.searches, q)
	page := EvidencePage{AsOf: s.anchor, Items: []EvidenceItem{{ID: fmt.Sprintf("match:%d", len(s.searches)), Section: q.Section, Summary: "match"}}}
	if q.Cursor == "" {
		page.NextCursor = "source-private:page-2"
	}
	return page, nil
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
	result, err := r.Run(context.Background(), RoleProfile{Model: "watch", Thinking: ThinkingAuto, Effort: EffortXHigh}, RoleBudget{TokenCap: 100_000}, ReviewRequest{Role: RoleWatchdog, Kind: ReviewEvent, State: state})
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

func TestReviewerFollowReplaysExactSearchWithOpaqueCursor(t *testing.T) {
	state := State{RootSessionID: "root-follow", SessionSequence: 2, TreeDigest: "tree", Baseline: testBaseline()}
	source := &pagedReviewSource{anchor: state.Anchor()}
	factory := &reviewFactory{provider: func() agent.Provider {
		return &scriptedReviewProvider{fn: func(turn int, req agent.TurnRequest) []agent.Event {
			switch turn {
			case 1:
				if strings.Contains(string(req.Tools[1].Schema), `"cursor"`) || !strings.Contains(string(req.Tools[2].Schema), `"required":["cursor"]`) {
					t.Fatalf("review tool schemas do not enforce opaque follow: search=%s follow=%s", req.Tools[1].Schema, req.Tools[2].Schema)
				}
				call := agent.ToolUseBlock{ID: "search-1", Name: searchToolName, Input: json.RawMessage(`{"section":"repository","pattern":"needle","path":"internal","kinds":["go"],"limit":7}`)}
				return []agent.Event{{Kind: agent.EvToolCallEnd, ToolCall: &call}, {Kind: agent.EvDone}}
			case 2:
				last := req.Messages[len(req.Messages)-1]
				var page EvidencePage
				if err := json.Unmarshal([]byte(last.Content[0].ToolResult.Content), &page); err != nil {
					t.Fatal(err)
				}
				if page.NextCursor == "" || page.NextCursor == "source-private:page-2" {
					t.Fatalf("source cursor was not replaced by an opaque review cursor: %+v", page)
				}
				input, _ := json.Marshal(EvidenceQuery{Cursor: page.NextCursor})
				call := agent.ToolUseBlock{ID: "follow-1", Name: followToolName, Input: input}
				return []agent.Event{{Kind: agent.EvToolCallEnd, ToolCall: &call}, {Kind: agent.EvDone}}
			default:
				return []agent.Event{{Kind: agent.EvTextDelta, Text: `{"verdict":{"kind":"event","decision":"continue","rationale":"search complete"}}`}, {Kind: agent.EvDone}}
			}
		}}
	}}
	if _, err := (Reviewer{Factory: factory, Source: source}).Run(context.Background(), RoleProfile{Thinking: ThinkingOff}, RoleBudget{}, ReviewRequest{Role: RoleWatchdog, Kind: ReviewEvent, State: state}); err != nil {
		t.Fatal(err)
	}
	if len(source.searches) != 2 {
		t.Fatalf("search calls = %+v", source.searches)
	}
	first, followed := source.searches[0], source.searches[1]
	if followed.Cursor != "source-private:page-2" || followed.Section != first.Section || followed.Pattern != first.Pattern || followed.Path != first.Path || followed.Limit != first.Limit || !reflect.DeepEqual(followed.Kinds, first.Kinds) {
		t.Fatalf("follow query = %+v, want original %+v plus private cursor", followed, first)
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

func TestReviewerOmitsReasoningEffortWhenProviderDoesNotAdvertiseIt(t *testing.T) {
	state := State{RootSessionID: "root", TreeDigest: "tree", Baseline: testBaseline()}
	source := &reviewSource{anchor: state.Anchor()}
	provider := &noReasoningReviewProvider{scriptedReviewProvider: &scriptedReviewProvider{fn: func(_ int, req agent.TurnRequest) []agent.Event {
		if req.ReasoningEffort != "" {
			t.Fatalf("unsupported reasoning effort was forwarded: %q", req.ReasoningEffort)
		}
		return []agent.Event{{Kind: agent.EvTextDelta, Text: "{\"verdict\":{\"kind\":\"event\",\"decision\":\"continue\",\"rationale\":\"aligned\"}}"}, {Kind: agent.EvDone}}
	}}}
	factory := &reviewFactory{provider: func() agent.Provider { return provider }}
	if _, err := (Reviewer{Factory: factory, Source: source}).Run(context.Background(), RoleProfile{Thinking: ThinkingOff, Effort: EffortHigh}, RoleBudget{}, ReviewRequest{Role: RoleWatchdog, Kind: ReviewEvent, State: state}); err != nil {
		t.Fatal(err)
	}
}

func TestReviewerCapsGenerationBeforeDispatch(t *testing.T) {
	state := State{RootSessionID: "root", TreeDigest: "tree", Baseline: testBaseline()}
	source := &reviewSource{anchor: state.Anchor()}
	provider := &scriptedReviewProvider{countTokens: 7, fn: func(_ int, req agent.TurnRequest) []agent.Event {
		if req.MaxTokens != 13 {
			t.Fatalf("max tokens = %d, want remaining 20 - input 7 = 13", req.MaxTokens)
		}
		return []agent.Event{{Kind: agent.EvTextDelta, Text: "{\"verdict\":{\"kind\":\"event\",\"decision\":\"continue\",\"rationale\":\"aligned\"}}"}, {Kind: agent.EvDone, Usage: &agent.Usage{InputTokens: 1, OutputTokens: 2}}}
	}}
	factory := &reviewFactory{provider: func() agent.Provider { return provider }}
	result, err := (Reviewer{Factory: factory, Source: source}).Run(context.Background(), RoleProfile{Thinking: ThinkingOff}, RoleBudget{TokenCap: 20}, ReviewRequest{Role: RoleWatchdog, Kind: ReviewEvent, State: state})
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage.InputTokens != 7 || result.Usage.OutputTokens != 2 {
		t.Fatalf("usage = %+v, want preflight input floor and reported output", result.Usage)
	}
}

func TestReviewerHardTokenBudgetFailsClosedWhenProviderOmitsUsage(t *testing.T) {
	state := State{RootSessionID: "root", TreeDigest: "tree", Baseline: testBaseline()}
	source := &reviewSource{anchor: state.Anchor()}
	provider := &scriptedReviewProvider{countTokens: 3, fn: func(_ int, _ agent.TurnRequest) []agent.Event {
		return []agent.Event{{Kind: agent.EvTextDelta, Text: "{\"verdict\":{\"kind\":\"event\",\"decision\":\"continue\",\"rationale\":\"aligned\"}}"}, {Kind: agent.EvDone}}
	}}
	factory := &reviewFactory{provider: func() agent.Provider { return provider }}
	_, err := (Reviewer{Factory: factory, Source: source}).Run(context.Background(), RoleProfile{Thinking: ThinkingOff}, RoleBudget{TokenCap: 20}, ReviewRequest{Role: RoleWatchdog, Kind: ReviewEvent, State: state})
	if err == nil || !strings.Contains(err.Error(), "omitted usage") || provider.turn != 1 {
		t.Fatalf("missing usage error=%v turns=%d", err, provider.turn)
	}
}

func TestReviewerClosesFreshProvider(t *testing.T) {
	state := State{RootSessionID: "root", TreeDigest: "tree", Baseline: testBaseline()}
	source := &reviewSource{anchor: state.Anchor()}
	resultBody := `{"verdict":{"kind":"event","decision":"continue","rationale":"aligned"}}`

	t.Run("fresh provider is closed", func(t *testing.T) {
		provider := &managedReviewProvider{scriptedReviewProvider: &scriptedReviewProvider{fn: func(_ int, _ agent.TurnRequest) []agent.Event {
			return []agent.Event{{Kind: agent.EvTextDelta, Text: resultBody}, {Kind: agent.EvDone}}
		}}}
		factory := &reviewFactory{provider: func() agent.Provider { return provider }}
		if _, err := (Reviewer{Factory: factory, Source: source}).Run(context.Background(), RoleProfile{Thinking: ThinkingOff}, RoleBudget{}, ReviewRequest{Role: RoleWatchdog, Kind: ReviewEvent, State: state}); err != nil {
			t.Fatal(err)
		}
		if provider.closes != 1 {
			t.Fatalf("provider closes = %d, want 1", provider.closes)
		}
	})

	t.Run("cleanup failure does not discard a valid verdict", func(t *testing.T) {
		provider := &managedReviewProvider{
			closeErr: errors.New("cleanup failed"),
			scriptedReviewProvider: &scriptedReviewProvider{fn: func(_ int, _ agent.TurnRequest) []agent.Event {
				return []agent.Event{{Kind: agent.EvTextDelta, Text: resultBody}, {Kind: agent.EvDone}}
			}},
		}
		factory := &reviewFactory{provider: func() agent.Provider { return provider }}
		result, err := (Reviewer{Factory: factory, Source: source}).Run(context.Background(), RoleProfile{Thinking: ThinkingOff}, RoleBudget{}, ReviewRequest{Role: RoleWatchdog, Kind: ReviewEvent, State: state})
		if err != nil || result.Verdict == nil || provider.closes != 1 {
			t.Fatalf("result=%+v error=%v closes=%d", result, err, provider.closes)
		}
	})

}

func TestReviewerRejectsUnfitTokenBudgetBeforeDispatch(t *testing.T) {
	state := State{RootSessionID: "root", TreeDigest: "tree", Baseline: testBaseline()}
	source := &reviewSource{anchor: state.Anchor()}

	t.Run("request input consumes remaining budget", func(t *testing.T) {
		provider := &scriptedReviewProvider{countTokens: 10, fn: func(_ int, _ agent.TurnRequest) []agent.Event {
			t.Fatal("review request was dispatched despite an unfit token budget")
			return nil
		}}
		factory := &reviewFactory{provider: func() agent.Provider { return provider }}
		_, err := (Reviewer{Factory: factory, Source: source}).Run(context.Background(), RoleProfile{Thinking: ThinkingOff}, RoleBudget{TokenCap: 10}, ReviewRequest{Role: RoleWatchdog, Kind: ReviewEvent, State: state})
		if err == nil || !strings.Contains(err.Error(), "cannot fit") || provider.turn != 0 {
			t.Fatalf("pre-dispatch budget error=%v turns=%d", err, provider.turn)
		}
	})

	t.Run("thinking budget exceeds capped output", func(t *testing.T) {
		provider := &scriptedReviewProvider{countTokens: 1, fn: func(_ int, _ agent.TurnRequest) []agent.Event {
			t.Fatal("review request was dispatched despite an unfit thinking budget")
			return nil
		}}
		factory := &reviewFactory{provider: func() agent.Provider { return provider }}
		_, err := (Reviewer{Factory: factory, Source: source}).Run(context.Background(), RoleProfile{Thinking: ThinkingOn, ThinkingBudgetTokens: 32}, RoleBudget{TokenCap: 32}, ReviewRequest{Role: RoleWatchdog, Kind: ReviewEvent, State: state})
		if err == nil || !strings.Contains(err.Error(), "thinking budget") || provider.turn != 0 {
			t.Fatalf("pre-dispatch thinking error=%v turns=%d", err, provider.turn)
		}
	})
}

func TestReviewerRejectsProviderThinkingReserveBeforeDispatch(t *testing.T) {
	state := State{RootSessionID: "root", TreeDigest: "tree", Baseline: testBaseline()}
	source := &reviewSource{anchor: state.Anchor()}
	base := &scriptedReviewProvider{countTokens: 1_000, fn: func(_ int, _ agent.TurnRequest) []agent.Event {
		t.Fatal("review dispatched after provider thinking reserve exceeded the hard token cap")
		return nil
	}}
	provider := &reservedThinkingReviewProvider{scriptedReviewProvider: base, reserve: 1_024}
	factory := &reviewFactory{provider: func() agent.Provider { return provider }}
	_, err := (Reviewer{Factory: factory, Source: source}).Run(context.Background(), RoleProfile{Thinking: ThinkingOn, ThinkingBudgetTokens: 16_384}, RoleBudget{TokenCap: 18_000}, ReviewRequest{Role: RoleWatchdog, Kind: ReviewEvent, State: state})
	if err == nil || !strings.Contains(err.Error(), "provider thinking reserve") || base.turn != 0 {
		t.Fatalf("thinking reserve error=%v turns=%d", err, base.turn)
	}
}
