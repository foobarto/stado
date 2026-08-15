package application

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/broker/wal"
)

var testNow = time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

type fixture struct {
	service *Service
	store   *wal.Store
	now     time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	return newFixtureWithLimits(t, DefaultLimits())
}

func newFixtureWithLimits(t *testing.T, limits Limits) *fixture {
	t.Helper()
	store, err := wal.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service, err := NewWithLimits(store, limits)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{service: service, store: store, now: testNow}
	service.now = func() time.Time { return f.now }
	return f
}

func testAuthority(plugin string) Authority {
	return Authority{
		SessionID:  "session-1",
		Generation: 3,
		PluginID:   plugin,
		Principal:  "os-user:1000",
		Actor:      "broker:lifecycle",
	}
}

func TestJournalIsNamespacedBoundedPersistentAndIdempotent(t *testing.T) {
	f := newFixture(t)
	auth := testAuthority("github.com/foobarto/stado-plugins/supervise#supervise")
	input := JournalAppend{RunID: "run-1", Kind: "watchdog.review", Summary: "review completed", Data: json.RawMessage(`{"verdict":"continue"}`), EvidenceRefs: []string{"artifact:one"}}

	first, err := f.service.AppendJournal(context.Background(), auth, input, "review-1")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := f.service.AppendJournal(context.Background(), auth, input, "review-1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, retry) || first.ID == "" || first.Sequence != 1 || first.WALSequence != 1 {
		t.Fatalf("idempotent append mismatch: first=%+v retry=%+v", first, retry)
	}
	if len(f.store.Records()) != 1 {
		t.Fatalf("WAL records = %d, want 1", len(f.store.Records()))
	}
	changed := input
	changed.Summary = "different"
	if _, err := f.service.AppendJournal(context.Background(), auth, changed, "review-1"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed retry error = %v", err)
	}
	second, err := f.service.AppendJournal(context.Background(), auth, JournalAppend{ID: "entry-2", RunID: "run-1", Kind: "worker.turn", Summary: "turn completed"}, "review-2")
	if err != nil || second.Sequence != 2 {
		t.Fatalf("second append = %+v, %v", second, err)
	}

	other := testAuthority("example.org/other#watcher")
	otherEntry, err := f.service.AppendJournal(context.Background(), other, JournalAppend{ID: first.ID, RunID: "run-1", Kind: "worker.turn", Summary: "isolated"}, "review-1")
	if err != nil || otherEntry.Sequence != 1 {
		t.Fatalf("other namespace append = %+v, %v", otherEntry, err)
	}
	projection, err := f.service.Project(context.Background(), auth, ProjectionOptions{JournalLimit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !projection.JournalTruncated || len(projection.Journal) != 1 || projection.Journal[0].ID != second.ID || projection.AsOfSequence != second.WALSequence {
		t.Fatalf("projection = %+v", projection)
	}
	otherProjection, err := f.service.Project(context.Background(), other, ProjectionOptions{})
	if err != nil || len(otherProjection.Journal) != 1 || otherProjection.Journal[0].Summary != "isolated" {
		t.Fatalf("other projection = %+v, %v", otherProjection, err)
	}

	reloaded := New(f.store)
	reloaded.now = f.service.now
	reloadedProjection, err := reloaded.Project(context.Background(), auth, ProjectionOptions{JournalLimit: 1})
	if err != nil || !reflect.DeepEqual(projection, reloadedProjection) {
		t.Fatalf("reload projection = %+v, %v", reloadedProjection, err)
	}
}

func TestJournalValidationAndLimits(t *testing.T) {
	auth := testAuthority("plugin#one")
	tests := []JournalAppend{
		{RunID: "bad run", Kind: "event", Summary: "ok"},
		{RunID: "run", Kind: "event", Summary: "   "},
		{RunID: "run", Kind: "event", Summary: "ok", Data: json.RawMessage(`{`)},
	}
	for i, input := range tests {
		f := newFixture(t)
		if _, err := f.service.AppendJournal(context.Background(), auth, input, "invalid"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("case %d error = %v", i, err)
		}
	}
	f := newFixture(t)
	refs := make([]string, DefaultLimits().MaxEvidenceRefs+1)
	for i := range refs {
		refs[i] = "ref"
	}
	if _, err := f.service.AppendJournal(context.Background(), auth, JournalAppend{RunID: "run", Kind: "event", Summary: "ok", EvidenceRefs: refs}, "too-many"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("evidence limit error = %v", err)
	}

	limits := DefaultLimits()
	limits.MaxJournalEntries = 1
	f = newFixtureWithLimits(t, limits)
	for i := 0; i < 2; i++ {
		_, err := f.service.AppendJournal(context.Background(), auth, JournalAppend{RunID: "run", Kind: "event", Summary: "ok"}, "entry-"+string(rune('a'+i)))
		if i == 0 && err != nil {
			t.Fatal(err)
		}
		if i == 1 && !errors.Is(err, ErrLimit) {
			t.Fatalf("journal bound error = %v", err)
		}
	}
}

func TestDurableEventDeliveryCursorAndSubscriptionIsolation(t *testing.T) {
	f := newFixture(t)
	scope := SessionScope{SessionID: "session-1", Generation: 3}
	turn, err := f.service.PublishEvent(context.Background(), scope, EventInput{
		ID: "turn-1", Kind: "session.turn_committed", Data: json.RawMessage(`{"turn":1}`), EvidenceRefs: []string{"trace:1"},
	}, "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if turn.WALSequence == 0 || turn.SessionID != scope.SessionID || turn.Generation != scope.Generation {
		t.Fatalf("published event = %+v", turn)
	}
	if _, err := f.service.PublishEvent(context.Background(), scope, EventInput{ID: "noise-1", Kind: "session.noise", Data: json.RawMessage(`{}`)}, "noise-1"); err != nil {
		t.Fatal(err)
	}
	auth := testAuthority("plugin#watcher")
	pending, cursor, err := f.service.PendingEvents(context.Background(), auth, []string{"session.turn_committed"}, 10)
	if err != nil || len(pending) != 1 || pending[0].ID != turn.ID || cursor.Sequence != 0 {
		t.Fatalf("pending=%+v cursor=%+v err=%v", pending, cursor, err)
	}
	acked, err := f.service.AcknowledgeEvent(context.Background(), auth, EventAck{Sequence: turn.WALSequence}, "ack-turn-1")
	if err != nil || acked.Sequence != turn.WALSequence || acked.WALSequence <= turn.WALSequence {
		t.Fatalf("ack=%+v err=%v", acked, err)
	}
	pending, cursor, err = f.service.PendingEvents(context.Background(), auth, []string{"session.turn_committed"}, 10)
	if err != nil || len(pending) != 0 || cursor.Sequence != turn.WALSequence {
		t.Fatalf("post-ack pending=%+v cursor=%+v err=%v", pending, cursor, err)
	}

	other := testAuthority("plugin#other")
	pending, _, err = f.service.PendingEvents(context.Background(), other, []string{"session.turn_committed"}, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("independent cursor pending=%+v err=%v", pending, err)
	}
	reloaded := New(f.store)
	reloaded.now = f.service.now
	pending, cursor, err = reloaded.PendingEvents(context.Background(), auth, []string{"session.turn_committed"}, 10)
	if err != nil || len(pending) != 0 || cursor.Sequence != turn.WALSequence {
		t.Fatalf("reloaded pending=%+v cursor=%+v err=%v", pending, cursor, err)
	}
}

func TestDueTimerBecomesTargetedDurableEvent(t *testing.T) {
	f := newFixture(t)
	auth := testAuthority("plugin#timer")
	timer, err := f.service.ScheduleTimer(context.Background(), auth, TimerSchedule{
		ID: "review-1", RunID: "run-1", Name: "poll-reviewer", DueAt: f.now.Add(time.Minute), Payload: json.RawMessage(`{"agent":"a1"}`),
	}, "schedule-review")
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(2 * time.Minute)
	if err := f.service.PromoteDueTimers(context.Background(), auth, 10); err != nil {
		t.Fatal(err)
	}
	pending, _, err := f.service.PendingEvents(context.Background(), auth, []string{"timer.due"}, 10)
	if err != nil || len(pending) != 1 || pending[0].TargetPlugin != auth.PluginID || pending[0].WALSequence <= timer.WALSequence {
		t.Fatalf("timer pending=%+v err=%v", pending, err)
	}
	other := testAuthority("plugin#other")
	pending, _, err = f.service.PendingEvents(context.Background(), other, []string{"timer.due"}, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("targeted timer leaked: %+v err=%v", pending, err)
	}
}

func TestHoldLifecycleCASExpiryAndIsolation(t *testing.T) {
	f := newFixture(t)
	auth := testAuthority("plugin#scheduler")
	acquire := HoldAcquire{ID: "hold-1", RunID: "run-1", ReasonCode: "watchdog.review", Reason: "reviewing", TTL: 10 * time.Minute}
	hold, err := f.service.AcquireHold(context.Background(), auth, acquire, "acquire")
	if err != nil {
		t.Fatal(err)
	}
	if hold.Version != 1 || hold.Owner != auth.PluginID || hold.Status != HoldActive || hold.WALSequence == 0 {
		t.Fatalf("hold = %+v", hold)
	}

	other := testAuthority("plugin#other")
	isolated, err := f.service.AcquireHold(context.Background(), other, acquire, "acquire")
	if err != nil || isolated.ID != hold.ID {
		t.Fatalf("same ID in other namespace = %+v, %v", isolated, err)
	}
	f.now = f.now.Add(time.Minute)
	renew := acquire
	renew.ExpectedVersion = 1
	renew.TTL = 20 * time.Minute
	hold, err = f.service.AcquireHold(context.Background(), auth, renew, "renew")
	if err != nil || hold.Version != 2 || !hold.LeaseUntil.Equal(f.now.Add(20*time.Minute)) {
		t.Fatalf("renew = %+v, %v", hold, err)
	}
	if _, err := f.service.ReleaseHold(context.Background(), auth, HoldCAS{ID: hold.ID, RunID: hold.RunID, ExpectedVersion: 1}, "stale"); !errors.Is(err, ErrVersion) {
		t.Fatalf("stale CAS error = %v", err)
	}
	if _, err := f.service.ReleaseHold(context.Background(), auth, HoldCAS{ID: hold.ID, RunID: "other-run", ExpectedVersion: 2}, "wrong-run"); !errors.Is(err, ErrScope) {
		t.Fatalf("wrong run error = %v", err)
	}
	if _, err := f.service.ExpireHold(context.Background(), auth, HoldCAS{ID: hold.ID, RunID: hold.RunID, ExpectedVersion: 2}, "early"); !errors.Is(err, ErrNotDue) {
		t.Fatalf("early expiry error = %v", err)
	}

	f.now = hold.LeaseUntil
	if _, err := f.service.ReleaseHold(context.Background(), auth, HoldCAS{ID: hold.ID, RunID: hold.RunID, ExpectedVersion: 2}, "late-release"); !errors.Is(err, ErrLeaseExpired) {
		t.Fatalf("late release error = %v", err)
	}
	due, err := f.service.DueHolds(context.Background(), auth, 10)
	if err != nil || len(due) != 1 || due[0].ID != hold.ID {
		t.Fatalf("due holds = %+v, %v", due, err)
	}
	expired, err := f.service.ExpireHold(context.Background(), auth, HoldCAS{ID: hold.ID, RunID: hold.RunID, ExpectedVersion: 2}, "expire")
	if err != nil || expired.Status != HoldExpired || expired.Version != 3 {
		t.Fatalf("expired = %+v, %v", expired, err)
	}
	retry, err := f.service.ExpireHold(context.Background(), auth, HoldCAS{ID: hold.ID, RunID: hold.RunID, ExpectedVersion: 2}, "expire")
	if err != nil || !reflect.DeepEqual(expired, retry) {
		t.Fatalf("expiry retry = %+v, %v", retry, err)
	}
	if _, err := f.service.AcquireHold(context.Background(), auth, renew, "renew-terminal"); !errors.Is(err, ErrTerminal) {
		t.Fatalf("terminal renew error = %v", err)
	}

	projection, err := f.service.Project(context.Background(), auth, ProjectionOptions{})
	if err != nil || len(projection.Holds) != 0 {
		t.Fatalf("active projection = %+v, %v", projection.Holds, err)
	}
	projection, err = f.service.Project(context.Background(), auth, ProjectionOptions{IncludeTerminal: true})
	if err != nil || len(projection.Holds) != 1 || projection.Holds[0].Status != HoldExpired {
		t.Fatalf("terminal projection = %+v, %v", projection.Holds, err)
	}
}

func TestHoldCASSerializesConcurrentWriters(t *testing.T) {
	f := newFixture(t)
	auth := testAuthority("plugin#scheduler")
	base, err := f.service.AcquireHold(context.Background(), auth, HoldAcquire{ID: "hold", RunID: "run", ReasonCode: "review", TTL: time.Minute}, "base")
	if err != nil {
		t.Fatal(err)
	}
	f.now = f.now.Add(time.Second)
	const writers = 12
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = f.service.AcquireHold(context.Background(), auth, HoldAcquire{ID: base.ID, RunID: base.RunID, ExpectedVersion: base.Version, ReasonCode: "review", TTL: time.Minute}, "writer-"+string(rune('a'+i)))
		}()
	}
	wg.Wait()
	success, conflicts := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrVersion):
			conflicts++
		default:
			t.Fatalf("unexpected CAS error: %v", err)
		}
	}
	if success != 1 || conflicts != writers-1 {
		t.Fatalf("success/conflicts = %d/%d", success, conflicts)
	}
}

func TestControlRequestsAndBrokerEnforcementProjection(t *testing.T) {
	f := newFixture(t)
	firstAuth := testAuthority("plugin#one")
	secondAuth := testAuthority("plugin#two")
	firstHold, err := f.service.AcquireHold(context.Background(), firstAuth, HoldAcquire{ID: "hold", RunID: "run", ReasonCode: "review", TTL: 10 * time.Minute}, "hold")
	if err != nil {
		t.Fatal(err)
	}
	secondHold, err := f.service.AcquireHold(context.Background(), secondAuth, HoldAcquire{ID: "hold", RunID: "run", ReasonCode: "review", TTL: time.Minute}, "hold")
	if err != nil {
		t.Fatal(err)
	}
	pause, err := f.service.RequestPause(context.Background(), firstAuth, ControlInput{RunID: "run", ReasonCode: "stale-stop", Reason: "recheck", HoldID: firstHold.ID, EvidenceRefs: []string{"journal:1"}}, "pause")
	if err != nil {
		t.Fatal(err)
	}
	stop, err := f.service.RequestStop(context.Background(), secondAuth, ControlInput{RunID: "run", ReasonCode: "policy", Reason: "stop now"}, "stop")
	if err != nil {
		t.Fatal(err)
	}
	if pause.PluginID != firstAuth.PluginID || stop.PluginID != secondAuth.PluginID || pause.WALSequence == 0 || stop.WALSequence <= pause.WALSequence {
		t.Fatalf("requests = %+v %+v", pause, stop)
	}
	completion, err := f.service.CompleteSession(context.Background(), firstAuth, CompletionInput{RunID: "run", Summary: "finished successfully", EvidenceRefs: []string{"journal:2"}}, "complete")
	if err != nil || completion.WALSequence <= stop.WALSequence {
		t.Fatalf("completion = %+v err=%v", completion, err)
	}
	retry, err := f.service.CompleteSession(context.Background(), firstAuth, CompletionInput{RunID: "run", Summary: "finished successfully", EvidenceRefs: []string{"journal:2"}}, "complete")
	if err != nil || !reflect.DeepEqual(completion, retry) {
		t.Fatalf("completion retry = %+v err=%v", retry, err)
	}
	if _, err := f.service.CompleteSession(context.Background(), firstAuth, CompletionInput{RunID: "run", Summary: "again"}, "complete-again"); !errors.Is(err, ErrTerminal) {
		t.Fatalf("second completion transition error = %v", err)
	}
	if _, err := f.service.RequestPause(context.Background(), secondAuth, ControlInput{RunID: "run", ReasonCode: "bad-ref", HoldID: "missing"}, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-scope/missing hold error = %v", err)
	}

	guestProjection, err := f.service.Project(context.Background(), firstAuth, ProjectionOptions{})
	if err != nil || len(guestProjection.ControlRequests) != 1 || guestProjection.ControlRequests[0].PluginID != firstAuth.PluginID || len(guestProjection.Completions) != 1 || guestProjection.Completions[0].ID != completion.ID {
		t.Fatalf("guest projection leaked namespace: %+v, %v", guestProjection, err)
	}
	enforcement, err := f.service.ProjectEnforcement(context.Background(), SessionScope{SessionID: firstAuth.SessionID, Generation: firstAuth.Generation})
	if err != nil {
		t.Fatal(err)
	}
	if len(enforcement.ActiveHolds) != 2 || enforcement.LatestPause == nil || enforcement.LatestPause.ID != pause.ID || enforcement.LatestStop == nil || enforcement.LatestStop.ID != stop.ID || enforcement.LatestCompletion == nil || enforcement.LatestCompletion.ID != completion.ID || enforcement.AsOfSequence != completion.WALSequence {
		t.Fatalf("enforcement projection = %+v", enforcement)
	}
	reloaded := New(f.store)
	reloaded.now = f.service.now
	restarted, err := reloaded.ProjectEnforcement(context.Background(), SessionScope{SessionID: firstAuth.SessionID, Generation: firstAuth.Generation})
	if err != nil || !reflect.DeepEqual(enforcement, restarted) {
		t.Fatalf("restarted enforcement = %+v err=%v", restarted, err)
	}
	if got := []string{enforcement.ActiveHolds[0].PluginID, enforcement.ActiveHolds[1].PluginID}; !sort.StringsAreSorted(got) && enforcement.ActiveHolds[0].LeaseUntil.Equal(enforcement.ActiveHolds[1].LeaseUntil) {
		t.Fatalf("holds not deterministic: %+v", enforcement.ActiveHolds)
	}
	f.now = secondHold.LeaseUntil
	enforcement, err = f.service.ProjectEnforcement(context.Background(), SessionScope{SessionID: firstAuth.SessionID, Generation: firstAuth.Generation})
	if err != nil || len(enforcement.ActiveHolds) != 1 || enforcement.ActiveHolds[0].PluginID != firstAuth.PluginID {
		t.Fatalf("expired lease included: %+v, %v", enforcement.ActiveHolds, err)
	}
	if _, err := f.service.ProjectEnforcement(context.Background(), SessionScope{SessionID: firstAuth.SessionID}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid broker scope error = %v", err)
	}
}

func TestCompletionTransitionSerializesConcurrentWriters(t *testing.T) {
	f := newFixture(t)
	auth := testAuthority("plugin#completion-race")
	const writers = 16
	errs := make(chan error, writers)
	for i := range writers {
		go func() {
			_, err := f.service.CompleteSession(context.Background(), auth, CompletionInput{
				ID: "done-" + string(rune('a'+i)), RunID: "run", Summary: "success",
			}, "writer-"+string(rune('a'+i)))
			errs <- err
		}()
	}
	success, terminal := 0, 0
	for range writers {
		switch err := <-errs; {
		case err == nil:
			success++
		case errors.Is(err, ErrTerminal):
			terminal++
		default:
			t.Fatalf("completion writer error = %v", err)
		}
	}
	if success != 1 || terminal != writers-1 {
		t.Fatalf("completion writers success=%d terminal=%d", success, terminal)
	}
	projection, err := f.service.Project(context.Background(), auth, ProjectionOptions{})
	if err != nil || len(projection.Completions) != 1 {
		t.Fatalf("completion projection=%+v err=%v", projection.Completions, err)
	}
}

func TestTimerScheduleRescheduleCancelAndDue(t *testing.T) {
	f := newFixture(t)
	auth := testAuthority("plugin#timer")
	first, err := f.service.ScheduleTimer(context.Background(), auth, TimerSchedule{ID: "timer-b", RunID: "run", Name: "review", DueAt: f.now.Add(2 * time.Minute), Payload: json.RawMessage(`{"attempt":1}`)}, "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.service.ScheduleTimer(context.Background(), auth, TimerSchedule{ID: "timer-a", RunID: "run", Name: "heartbeat", DueAt: f.now.Add(time.Minute)}, "second")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.MarkTimerDue(context.Background(), auth, TimerCAS{ID: second.ID, RunID: second.RunID, ExpectedVersion: second.Version}, "early"); !errors.Is(err, ErrNotDue) {
		t.Fatalf("early due error = %v", err)
	}
	f.now = f.now.Add(30 * time.Second)
	first, err = f.service.ScheduleTimer(context.Background(), auth, TimerSchedule{ID: first.ID, RunID: first.RunID, ExpectedVersion: first.Version, Name: "review-again", DueAt: f.now.Add(30 * time.Second)}, "reschedule")
	if err != nil || first.Version != 2 {
		t.Fatalf("reschedule = %+v, %v", first, err)
	}
	f.now = first.DueAt
	due, err := f.service.DueTimers(context.Background(), auth, 10)
	if err != nil || len(due) != 2 || due[0].ID != "timer-a" || due[1].ID != "timer-b" {
		t.Fatalf("due timers = %+v, %v", due, err)
	}
	marked, err := f.service.MarkTimerDue(context.Background(), auth, TimerCAS{ID: first.ID, RunID: first.RunID, ExpectedVersion: first.Version}, "due")
	if err != nil || marked.Status != TimerDue || marked.Version != 3 {
		t.Fatalf("mark due = %+v, %v", marked, err)
	}
	markedRetry, err := f.service.MarkTimerDue(context.Background(), auth, TimerCAS{ID: first.ID, RunID: first.RunID, ExpectedVersion: first.Version}, "due")
	if err != nil || !reflect.DeepEqual(marked, markedRetry) {
		t.Fatalf("due retry = %+v, %v", markedRetry, err)
	}
	cancelled, err := f.service.CancelTimer(context.Background(), auth, TimerCAS{ID: second.ID, RunID: second.RunID, ExpectedVersion: second.Version}, "cancel")
	if err != nil || cancelled.Status != TimerCancelled {
		t.Fatalf("cancel = %+v, %v", cancelled, err)
	}
	if _, err := f.service.ScheduleTimer(context.Background(), auth, TimerSchedule{ID: second.ID, RunID: second.RunID, ExpectedVersion: cancelled.Version, Name: "again", DueAt: f.now.Add(time.Minute)}, "terminal"); !errors.Is(err, ErrTerminal) {
		t.Fatalf("terminal timer error = %v", err)
	}
	projection, err := f.service.Project(context.Background(), auth, ProjectionOptions{IncludeTerminal: true})
	if err != nil || len(projection.Timers) != 2 || projection.AsOfSequence != cancelled.WALSequence {
		t.Fatalf("timer projection = %+v, %v", projection, err)
	}
}

func TestTimerValidationAndActiveBound(t *testing.T) {
	f := newFixture(t)
	auth := testAuthority("plugin#timer")
	tests := []TimerSchedule{
		{RunID: "run", Name: "timer", DueAt: f.now.Add(-time.Second)},
		{RunID: "run", Name: "timer", DueAt: f.now.Add(DefaultLimits().MaxTimerHorizon + time.Second)},
		{RunID: "run", Name: "timer", DueAt: f.now.Add(time.Second), Payload: json.RawMessage(`{`)},
	}
	for i, input := range tests {
		if _, err := f.service.ScheduleTimer(context.Background(), auth, input, "invalid-"+string(rune('a'+i))); !errors.Is(err, ErrInvalid) {
			t.Fatalf("case %d error = %v", i, err)
		}
	}
	limits := DefaultLimits()
	limits.MaxActiveTimers = 1
	f = newFixtureWithLimits(t, limits)
	if _, err := f.service.ScheduleTimer(context.Background(), auth, TimerSchedule{ID: "one", RunID: "run", Name: "timer", DueAt: f.now.Add(time.Second)}, "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ScheduleTimer(context.Background(), auth, TimerSchedule{ID: "two", RunID: "run", Name: "timer", DueAt: f.now.Add(time.Second)}, "two"); !errors.Is(err, ErrLimit) {
		t.Fatalf("active timer bound error = %v", err)
	}
}

func TestGuestRequestsCannotSupplyAuthority(t *testing.T) {
	for _, value := range []any{JournalAppend{}, HoldAcquire{}, HoldCAS{}, ControlInput{}, CompletionInput{}, TimerSchedule{}, TimerCAS{}} {
		typeOf := reflect.TypeOf(value)
		for _, forbidden := range []string{"SessionID", "Generation", "PluginID", "Principal", "Actor", "Owner"} {
			if _, ok := typeOf.FieldByName(forbidden); ok {
				t.Fatalf("guest request %s contains authority field %s", typeOf.Name(), forbidden)
			}
		}
	}
}

func TestStateSurvivesWALReopenAndIdempotencyRetry(t *testing.T) {
	dir := t.TempDir()
	store, err := wal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	auth := testAuthority("plugin#persistent")
	service := New(store)
	service.now = func() time.Time { return testNow }
	journalInput := JournalAppend{RunID: "run", Kind: "checkpoint", Summary: "persisted"}
	journal, err := service.AppendJournal(context.Background(), auth, journalInput, "journal")
	if err != nil {
		t.Fatal(err)
	}
	hold, err := service.AcquireHold(context.Background(), auth, HoldAcquire{ID: "hold", RunID: "run", ReasonCode: "review", TTL: time.Minute}, "hold")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.RequestPause(context.Background(), auth, ControlInput{RunID: "run", ReasonCode: "review", HoldID: hold.ID}, "pause"); err != nil {
		t.Fatal(err)
	}
	completionInput := CompletionInput{RunID: "run", Summary: "success"}
	completion, err := service.CompleteSession(context.Background(), auth, completionInput, "complete")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ScheduleTimer(context.Background(), auth, TimerSchedule{ID: "timer", RunID: "run", Name: "wake", DueAt: testNow.Add(time.Minute)}, "timer"); err != nil {
		t.Fatal(err)
	}
	recordCount := len(store.Records())
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = wal.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service = New(store)
	service.now = func() time.Time { return testNow }
	projection, err := service.Project(context.Background(), auth, ProjectionOptions{})
	if err != nil || len(projection.Journal) != 1 || len(projection.Holds) != 1 || len(projection.ControlRequests) != 1 || len(projection.Completions) != 1 || len(projection.Timers) != 1 {
		t.Fatalf("reopened projection = %+v, %v", projection, err)
	}
	retry, err := service.AppendJournal(context.Background(), auth, journalInput, "journal")
	if err != nil || !reflect.DeepEqual(journal, retry) || len(store.Records()) != recordCount {
		t.Fatalf("reopened retry = %+v, %v, records=%d", retry, err, len(store.Records()))
	}
	completionRetry, err := service.CompleteSession(context.Background(), auth, completionInput, "complete")
	if err != nil || !reflect.DeepEqual(completion, completionRetry) || len(store.Records()) != recordCount {
		t.Fatalf("reopened completion retry = %+v, %v, records=%d", completionRetry, err, len(store.Records()))
	}
}

func TestHoldAndControlBounds(t *testing.T) {
	auth := testAuthority("plugin#bounded")
	f := newFixture(t)
	if _, err := f.service.AcquireHold(context.Background(), auth, HoldAcquire{RunID: "run", ReasonCode: "review", TTL: DefaultLimits().MaxHoldTTL + time.Second}, "long"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("hold TTL error = %v", err)
	}
	limits := DefaultLimits()
	limits.MaxActiveHolds = 1
	limits.MaxControlRequests = 1
	limits.MaxCompletions = 1
	f = newFixtureWithLimits(t, limits)
	if _, err := f.service.AcquireHold(context.Background(), auth, HoldAcquire{ID: "one", RunID: "run", ReasonCode: "review", TTL: time.Minute}, "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.AcquireHold(context.Background(), auth, HoldAcquire{ID: "two", RunID: "run", ReasonCode: "review", TTL: time.Minute}, "two"); !errors.Is(err, ErrLimit) {
		t.Fatalf("active hold bound error = %v", err)
	}
	if _, err := f.service.RequestStop(context.Background(), auth, ControlInput{ID: "stop-one", RunID: "run", ReasonCode: "review"}, "stop-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RequestStop(context.Background(), auth, ControlInput{ID: "stop-two", RunID: "run", ReasonCode: "review"}, "stop-two"); !errors.Is(err, ErrLimit) {
		t.Fatalf("control bound error = %v", err)
	}
	if _, err := f.service.CompleteSession(context.Background(), auth, CompletionInput{ID: "done-one", RunID: "run-one", Summary: "done"}, "done-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.CompleteSession(context.Background(), auth, CompletionInput{ID: "done-two", RunID: "run-two", Summary: "done"}, "done-two"); !errors.Is(err, ErrLimit) {
		t.Fatalf("completion bound error = %v", err)
	}
	if _, err := newFixture(t).service.CompleteSession(context.Background(), auth, CompletionInput{RunID: "bad run", Summary: "done"}, "invalid-completion"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("invalid completion error = %v", err)
	}
}

func TestAuthorityContextAndProjectionValidation(t *testing.T) {
	f := newFixture(t)
	auth := testAuthority("plugin#one")
	bad := auth
	bad.PluginID = "plugin\nforged"
	if _, err := f.service.Project(context.Background(), bad, ProjectionOptions{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("authority error = %v", err)
	}
	var nilContext context.Context
	if _, err := f.service.Project(nilContext, auth, ProjectionOptions{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil context error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.service.Project(cancelled, auth, ProjectionOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context error = %v", err)
	}
	if _, err := f.service.Project(context.Background(), auth, ProjectionOptions{JournalLimit: DefaultLimits().MaxProjectionItems + 1}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("projection limit error = %v", err)
	}
	if _, err := NewWithLimits(nil, DefaultLimits()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil WAL error = %v", err)
	}
	badLimits := DefaultLimits()
	badLimits.MaxActiveHolds = badLimits.MaxHoldRecords + 1
	if _, err := NewWithLimits(f.store, badLimits); !errors.Is(err, ErrInvalid) {
		t.Fatalf("limits error = %v", err)
	}
}

func TestFoldRejectsMalformedAndImpossibleTransitions(t *testing.T) {
	auth := testAuthority("plugin#one")
	base := Hold{ID: "hold", SessionID: auth.SessionID, Generation: auth.Generation, PluginID: auth.PluginID, Owner: auth.PluginID, RunID: "run", Version: 1, ReasonCode: "review", Status: HoldActive, LeaseUntil: testNow.Add(time.Minute), CreatedAt: testNow, UpdatedAt: testNow}
	meta := eventMeta{Schema: eventSchema, SessionID: auth.SessionID, Generation: auth.Generation, PluginID: auth.PluginID, RequestDigest: strings.Repeat("a", 64)}
	makeRecord := func(sequence uint64, eventType string, envelope eventEnvelope) wal.Record {
		data, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		return wal.Record{Sequence: sequence, Transaction: wal.Transaction{Events: []wal.Event{{Store: storeName, Type: eventType, Session: auth.SessionID, Data: data}}}}
	}
	valid := makeRecord(1, "hold.acquired", eventEnvelope{Meta: meta, Hold: &base})
	completion := Completion{ID: "done", SessionID: auth.SessionID, Generation: auth.Generation, PluginID: auth.PluginID, RunID: "run", CreatedAt: testNow}
	otherCompletion := completion
	otherCompletion.ID = "done-again"
	badStatus := base
	badStatus.Version = 2
	badStatus.Status = HoldReleased
	badStatus.UpdatedAt = testNow.Add(time.Second)
	tests := []struct {
		name    string
		records []wal.Record
	}{
		{"unknown type", []wal.Record{makeRecord(1, "made.up", eventEnvelope{Meta: meta, Hold: &base})}},
		{"multiple payloads", []wal.Record{makeRecord(1, "hold.acquired", eventEnvelope{Meta: meta, Hold: &base, Timer: &Timer{}})}},
		{"wrong session envelope", []wal.Record{{Sequence: 1, Transaction: wal.Transaction{Events: []wal.Event{{Store: storeName, Type: "hold.acquired", Session: "other", Data: valid.Transaction.Events[0].Data}}}}}},
		{"event status mismatch", []wal.Record{valid, makeRecord(2, "hold.renewed", eventEnvelope{Meta: meta, Hold: &badStatus})}},
		{"duplicate initial event", []wal.Record{valid, makeRecord(2, "hold.acquired", eventEnvelope{Meta: meta, Hold: &base})}},
		{"duplicate completion run", []wal.Record{makeRecord(1, "session.completed", eventEnvelope{Meta: meta, Completion: &completion}), makeRecord(2, "session.completed", eventEnvelope{Meta: meta, Completion: &otherCompletion})}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := fold(test.records); err == nil {
				t.Fatal("fold accepted malformed records")
			}
		})
	}
}
