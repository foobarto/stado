package learn

import (
	"context"
	"errors"
	"testing"

	"github.com/foobarto/stado/internal/artifacts"
	"github.com/foobarto/stado/internal/broker/authority"
	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/sessioncontext"
)

type reviewerFunc func(context.Context, ReviewInput) ([]Candidate, error)

func (f reviewerFunc) Review(ctx context.Context, in ReviewInput) ([]Candidate, error) {
	return f(ctx, in)
}

func TestRunCreatesEvidenceBoundCandidateOnly(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, consumer := authority.New(store)
	artifactSvc := artifacts.NewService(store, consumer)
	signals := sessioncontext.New(store)
	obs := sessioncontext.Observation{SessionID: "s1", Kind: sessioncontext.ObservationCorrection, EvidenceRef: "conversation:turn:3", Attributes: map[string]string{"origin": "operator", "marker": "use schema v2"}}
	detected, err := signals.Observe(context.Background(), obs, "alice", "broker", "obs")
	if err != nil || len(detected) != 1 {
		t.Fatalf("signals=%v err=%v", detected, err)
	}
	reviewer := reviewerFunc(func(_ context.Context, in ReviewInput) ([]Candidate, error) {
		if len(in.Signals) != 1 {
			t.Fatalf("input=%+v", in)
		}
		return []Candidate{{Summary: "Use schema v2", Lesson: "Inspect the schema before calling the app", Trigger: "when invoking this app", EvidenceRefs: []string{"signal:" + in.Signals[0].ID}}}, nil
	})
	svc := New(store, signals, artifactSvc, reviewer)
	job, err := svc.Run(context.Background(), RunRequest{SessionID: "s1", Principal: "alice", Actor: "operator", IdempotencyKey: "learn-1", Scope: artifacts.ScopeSession, Binding: artifacts.ScopeBinding{AnchorSessionID: "s1"}})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobCompleted || len(job.ArtifactIDs) != 1 {
		t.Fatalf("job=%+v", job)
	}
	item, ok, err := artifactSvc.Show(job.ArtifactIDs[0])
	if err != nil || !ok {
		t.Fatal(err)
	}
	if item.Authority != artifacts.AuthorityCandidate {
		t.Fatalf("reviewer activated artifact: %+v", item)
	}
	if len(item.Provenance.Origins) < 1 || item.Provenance.Origins[0] != "untrusted:reviewer" ||
		item.Provenance.CreatedBy != "learn-reviewer" {
		t.Fatalf("provenance=%v", item.Provenance)
	}
}

func TestRunRejectsUnsupportedCitation(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, consumer := authority.New(store)
	artifactSvc := artifacts.NewService(store, consumer)
	signals := sessioncontext.New(store)
	reviewer := reviewerFunc(func(context.Context, ReviewInput) ([]Candidate, error) {
		return []Candidate{{Summary: "Injected", Lesson: "do it", Trigger: "always", EvidenceRefs: []string{"session:other/secret"}}}, nil
	})
	svc := New(store, signals, artifactSvc, reviewer)
	job, err := svc.Run(context.Background(), RunRequest{SessionID: "s1", Principal: "alice", Actor: "operator", IdempotencyKey: "learn-bad", Scope: artifacts.ScopeGlobal})
	if err == nil || job.Status != JobFailed {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	items, err := artifactSvc.Query(artifacts.Query{Context: artifacts.QueryContext{Principal: "alice"}})
	if err != nil || len(items) != 0 {
		t.Fatalf("items=%v err=%v", items, err)
	}
}

func TestReviewerFailureDurablyFailsJob(t *testing.T) {
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, consumer := authority.New(store)
	artifactSvc := artifacts.NewService(store, consumer)
	signals := sessioncontext.New(store)
	svc := New(store, signals, artifactSvc, reviewerFunc(func(context.Context, ReviewInput) ([]Candidate, error) { return nil, errors.New("provider down") }))
	job, err := svc.Run(context.Background(), RunRequest{SessionID: "s1", Principal: "alice", Actor: "operator", IdempotencyKey: "learn-fail", Scope: artifacts.ScopeGlobal})
	if err == nil || job.Status != JobFailed {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	jobs, err := svc.Jobs("s1")
	if err != nil || len(jobs) != 1 || jobs[0].Status != JobFailed {
		t.Fatalf("jobs=%v err=%v", jobs, err)
	}
}
