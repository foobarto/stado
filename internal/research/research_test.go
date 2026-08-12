package research

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/artifacts"
	"github.com/foobarto/stado/internal/broker/authority"
	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/pkg/agent"
)

type scriptedProvider struct {
	turns    [][]agent.Event
	requests []agent.TurnRequest
}

func (p *scriptedProvider) Name() string                     { return "scripted" }
func (p *scriptedProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (p *scriptedProvider) StreamTurn(_ context.Context, req agent.TurnRequest) (<-chan agent.Event, error) {
	p.requests = append(p.requests, req)
	events := p.turns[0]
	p.turns = p.turns[1:]
	ch := make(chan agent.Event, len(events))
	for _, e := range events {
		ch <- e
	}
	close(ch)
	return ch, nil
}

func TestMemoryResearchReturnsOnlyValidatedBoundedCitation(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	issuer, consumer := authority.New(store)
	svc := artifacts.NewService(store, consumer)
	ctx := context.Background()
	item, err := svc.Create(ctx, artifacts.Artifact{Kind: artifacts.KindLesson, Scope: artifacts.ScopeRepo, Binding: artifacts.ScopeBinding{CanonicalRepoID: "repo"}, Summary: "Valid JSON arguments", Content: "Inspect the schema and retry with valid JSON.", Trigger: "tool reports malformed input"}, "alice", "agent", "create")
	if err != nil {
		t.Fatal(err)
	}
	action, _ := artifacts.ActivationAction(item, "alice")
	grant, err := issuer.Issue(ctx, action, "operator", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	item, err = svc.SetAuthority(ctx, item.ID, 1, artifacts.AuthorityActive, grant.ID, "alice", "broker", "activate")
	if err != nil {
		t.Fatal(err)
	}
	corpus := MemoryCorpus{Service: svc, Context: artifacts.QueryContext{Principal: "alice", CanonicalRepoID: "repo"}, Kinds: []artifacts.Kind{artifacts.KindMemory, artifacts.KindLesson}}
	opened, err := corpus.Open(ctx, makeRef(item))
	if err != nil {
		t.Fatal(err)
	}
	var openedJSON map[string]any
	if err := json.Unmarshal([]byte(opened.Body), &openedJSON); err != nil {
		t.Fatal(err)
	}
	call := agent.ToolUseBlock{ID: "1", Name: "corpus_open", Input: mustJSON(map[string]any{"ref": makeRef(item)})}
	result := Result{Answer: "Use valid JSON.", Citations: []Citation{{Ref: makeRef(item), Claim: "schema inspection is advised", Excerpt: "Inspect the schema and retry with valid JSON."}}}
	raw, _ := json.Marshal(result)
	p := &scriptedProvider{turns: [][]agent.Event{{{Kind: agent.EvToolCallEnd, ToolCall: &call}, {Kind: agent.EvDone}}, {{Kind: agent.EvTextDelta, Text: string(raw)}, {Kind: agent.EvDone}}}}
	got, err := (Agent{Provider: p, Model: "test", Corpus: corpus, Kind: "memory"}).Run(ctx, "how should I retry?")
	if err != nil || len(got.Citations) != 1 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if got.Citations[0].EntailmentVerified {
		t.Fatal("citation incorrectly claims entailment")
	}
	if len(p.requests[0].Tools) != 3 {
		t.Fatalf("tools=%v", p.requests[0].Tools)
	}
}

func TestResearchRejectsUnopenedCitation(t *testing.T) {
	p := &scriptedProvider{turns: [][]agent.Event{{{Kind: agent.EvTextDelta, Text: `{"answer":"x","citations":[{"ref":{"kind":"memory","id":"secret","version":1,"digest":"x"},"claim":"x","excerpt":"secret"}]}`}, {Kind: agent.EvDone}}}}
	_, err := (Agent{Provider: p, Model: "test", Corpus: emptyCorpus{}, Kind: "memory"}).Run(context.Background(), "q")
	if err == nil {
		t.Fatal("unopened citation accepted")
	}
}

type emptyCorpus struct{}

func (emptyCorpus) Catalog(context.Context, int) ([]CatalogItem, error)        { return nil, nil }
func (emptyCorpus) Search(context.Context, string, int) ([]CatalogItem, error) { return nil, nil }
func (emptyCorpus) Open(context.Context, Ref) (Opened, error)                  { return Opened{}, nil }
func mustJSON(v any) json.RawMessage                                           { b, _ := json.Marshal(v); return b }
