package application

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func workerRequest(runID string) WorkerRunRequest {
	return WorkerRunRequest{
		RunID:     runID,
		Objective: "finish the admitted task",
		Prompt:    "Continue the task and report durable evidence.",
		Conflict:  WorkerRunRejectOperatorLoop,
	}
}

func TestWorkerRunLifecycleIsIdempotentScopedAndRestartSafe(t *testing.T) {
	f := newFixture(t)
	auth := testAuthority("plugin#worker")
	requested, err := f.service.RequestWorkerRun(context.Background(), auth, workerRequest("run-1"), "request")
	if err != nil {
		t.Fatal(err)
	}
	retry, err := f.service.RequestWorkerRun(context.Background(), auth, workerRequest("run-1"), "request")
	if err != nil || !reflect.DeepEqual(requested, retry) {
		t.Fatalf("request retry = %+v, %v", retry, err)
	}
	if requested.Status != WorkerRunRequested || requested.Version != 1 || requested.Owner != auth.PluginID {
		t.Fatalf("requested run = %+v", requested)
	}
	changed := workerRequest("run-1")
	changed.Prompt = "different"
	if _, err := f.service.RequestWorkerRun(context.Background(), auth, changed, "request"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("changed retry error = %v", err)
	}

	other := testAuthority("plugin#other")
	if _, err := f.service.WorkerRunByID(context.Background(), other, requested.RunID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-plugin read error = %v", err)
	}
	active, err := f.service.ActivateWorkerRun(context.Background(), auth, WorkerRunCAS{RunID: requested.RunID, ExpectedVersion: requested.Version}, "activate")
	if err != nil || active.Status != WorkerRunActive || active.Version != 2 {
		t.Fatalf("activated run = %+v, %v", active, err)
	}
	activeRetry, err := f.service.ActivateWorkerRun(context.Background(), auth, WorkerRunCAS{RunID: requested.RunID, ExpectedVersion: requested.Version}, "activate")
	if err != nil || !reflect.DeepEqual(active, activeRetry) {
		t.Fatalf("activation retry = %+v, %v", activeRetry, err)
	}

	otherRun, err := f.service.RequestWorkerRun(context.Background(), other, workerRequest("run-2"), "request")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ActivateWorkerRun(context.Background(), other, WorkerRunCAS{RunID: otherRun.RunID, ExpectedVersion: otherRun.Version}, "activate"); !errors.Is(err, ErrVersion) {
		t.Fatalf("second active application run error = %v", err)
	}

	projection, err := f.service.Project(context.Background(), auth, ProjectionOptions{})
	if err != nil || len(projection.WorkerRuns) != 1 || projection.WorkerRuns[0].Status != WorkerRunActive {
		t.Fatalf("worker projection = %+v, %v", projection.WorkerRuns, err)
	}
	enforcement, err := f.service.ProjectEnforcement(context.Background(), SessionScope{SessionID: auth.SessionID, Generation: auth.Generation})
	if err != nil || enforcement.ActiveWorkerRun == nil || enforcement.ActiveWorkerRun.RunID != active.RunID || enforcement.LatestWorkerRun == nil {
		t.Fatalf("enforcement = %+v, %v", enforcement, err)
	}

	reloaded := New(f.store)
	reloaded.now = f.service.now
	restarted, err := reloaded.ProjectEnforcement(context.Background(), SessionScope{SessionID: auth.SessionID, Generation: auth.Generation})
	if err != nil || !reflect.DeepEqual(enforcement, restarted) {
		t.Fatalf("restarted enforcement = %+v, %v", restarted, err)
	}
}

func TestWorkerRunCompletionIsDerivedFromSuccessfulHandoff(t *testing.T) {
	f := newFixture(t)
	auth := testAuthority("plugin#completion")
	requested, err := f.service.RequestWorkerRun(context.Background(), auth, workerRequest("run"), "request")
	if err != nil {
		t.Fatal(err)
	}
	active, err := f.service.ActivateWorkerRun(context.Background(), auth, WorkerRunCAS{RunID: requested.RunID, ExpectedVersion: requested.Version}, "activate")
	if err != nil {
		t.Fatal(err)
	}
	completion, err := f.service.CompleteSession(context.Background(), auth, CompletionInput{RunID: active.RunID, Summary: "all acceptance criteria passed"}, "complete")
	if err != nil {
		t.Fatal(err)
	}
	projected, err := f.service.WorkerRunByID(context.Background(), auth, active.RunID)
	if err != nil || projected.Status != WorkerRunCompleted || projected.TerminalSequence != completion.WALSequence || projected.TerminalReason != completion.Summary {
		t.Fatalf("completed worker run = %+v, %v", projected, err)
	}
	projection, err := f.service.Project(context.Background(), auth, ProjectionOptions{})
	if err != nil || len(projection.WorkerRuns) != 0 {
		t.Fatalf("terminal run in active projection = %+v, %v", projection.WorkerRuns, err)
	}
	projection, err = f.service.Project(context.Background(), auth, ProjectionOptions{IncludeTerminal: true})
	if err != nil || len(projection.WorkerRuns) != 1 || projection.WorkerRuns[0].Status != WorkerRunCompleted {
		t.Fatalf("terminal worker projection = %+v, %v", projection.WorkerRuns, err)
	}
	enforcement, err := f.service.ProjectEnforcement(context.Background(), SessionScope{SessionID: auth.SessionID, Generation: auth.Generation})
	if err != nil || enforcement.ActiveWorkerRun != nil || enforcement.LatestWorkerRun == nil || enforcement.LatestWorkerRun.Status != WorkerRunCompleted {
		t.Fatalf("completed enforcement = %+v, %v", enforcement, err)
	}
}

func TestWorkerRunCannotBeRequestedAfterSameRunCompleted(t *testing.T) {
	f := newFixture(t)
	auth := testAuthority("plugin#completion-first")
	if _, err := f.service.CompleteSession(context.Background(), auth, CompletionInput{RunID: "run", Summary: "done"}, "complete"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RequestWorkerRun(context.Background(), auth, workerRequest("run"), "request"); !errors.Is(err, ErrTerminal) {
		t.Fatalf("completed run request error = %v", err)
	}
}

func TestWorkerRunPauseStopConsumptionIsDurablyTerminal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status WorkerRunStatus
	}{
		{name: "pause", status: WorkerRunInterrupted},
		{name: "stop", status: WorkerRunStopped},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			auth := testAuthority("plugin#control")
			requested, err := f.service.RequestWorkerRun(context.Background(), auth, workerRequest("run"), "request")
			if err != nil {
				t.Fatal(err)
			}
			active, err := f.service.ActivateWorkerRun(context.Background(), auth, WorkerRunCAS{RunID: requested.RunID, ExpectedVersion: requested.Version}, "activate")
			if err != nil {
				t.Fatal(err)
			}
			terminal, err := f.service.TerminalizeWorkerRun(context.Background(), auth, WorkerRunTerminal{
				RunID: active.RunID, ExpectedVersion: active.Version, Status: tc.status,
				Reason: "scheduler consumed " + tc.name, ControlSequence: 41,
			}, "consume-41")
			if err != nil || terminal.Status != tc.status || terminal.TerminalSequence != 41 || terminal.TerminalReason == "" {
				t.Fatalf("terminal run = %+v, %v", terminal, err)
			}
			retry, err := f.service.TerminalizeWorkerRun(context.Background(), auth, WorkerRunTerminal{
				RunID: active.RunID, ExpectedVersion: active.Version, Status: tc.status,
				Reason: "scheduler consumed " + tc.name, ControlSequence: 41,
			}, "consume-41")
			if err != nil || !reflect.DeepEqual(terminal, retry) {
				t.Fatalf("terminal retry = %+v, %v", retry, err)
			}
			reloaded := New(f.store)
			reloaded.now = f.service.now
			enforcement, err := reloaded.ProjectEnforcement(context.Background(), SessionScope{SessionID: auth.SessionID, Generation: auth.Generation})
			if err != nil || enforcement.ActiveWorkerRun != nil || enforcement.LatestWorkerRun == nil || enforcement.LatestWorkerRun.Status != tc.status || enforcement.LatestWorkerRun.TerminalSequence != 41 {
				t.Fatalf("restarted terminal projection = %+v, %v", enforcement, err)
			}
		})
	}
}

func interruptWorkerRun(t *testing.T, f *fixture, auth Authority, runID string) WorkerRun {
	t.Helper()
	requested, err := f.service.RequestWorkerRun(context.Background(), auth, workerRequest(runID), "request-"+runID)
	if err != nil {
		t.Fatal(err)
	}
	active, err := f.service.ActivateWorkerRun(context.Background(), auth, WorkerRunCAS{RunID: runID, ExpectedVersion: requested.Version}, "activate-"+runID)
	if err != nil {
		t.Fatal(err)
	}
	pause, err := f.service.RequestPause(context.Background(), auth, ControlInput{RunID: runID, ReasonCode: "operator-review", Reason: "wait for operator review"}, "pause-"+runID)
	if err != nil {
		t.Fatal(err)
	}
	interrupted, err := f.service.TerminalizeWorkerRun(context.Background(), auth, WorkerRunTerminal{
		RunID: runID, ExpectedVersion: active.Version, Status: WorkerRunInterrupted,
		Reason: pause.Reason, ControlSequence: pause.WALSequence,
	}, "consume-"+runID)
	if err != nil {
		t.Fatal(err)
	}
	return interrupted
}

func TestWorkerRunResumeRequestAndActivationAreDurableAndIdempotent(t *testing.T) {
	f := newFixture(t)
	auth := testAuthority("plugin#resume")
	interrupted := interruptWorkerRun(t, f, auth, "run")

	resume, err := f.service.RequestWorkerRunResume(context.Background(), auth, WorkerRunCAS{
		RunID: interrupted.RunID, ExpectedVersion: interrupted.Version,
	}, "resume-request")
	if err != nil {
		t.Fatal(err)
	}
	if resume.Status != WorkerRunResumeRequested || resume.Version != interrupted.Version+1 ||
		resume.TerminalReason != interrupted.TerminalReason || resume.TerminalSequence != interrupted.TerminalSequence ||
		resume.RunID != interrupted.RunID || resume.Objective != interrupted.Objective || resume.Prompt != interrupted.Prompt {
		t.Fatalf("resume request = %+v, interrupted = %+v", resume, interrupted)
	}
	recordsAfterRequest := len(f.store.Records())
	retry, err := f.service.RequestWorkerRunResume(context.Background(), auth, WorkerRunCAS{
		RunID: interrupted.RunID, ExpectedVersion: interrupted.Version,
	}, "resume-request")
	if err != nil || !reflect.DeepEqual(retry, resume) {
		t.Fatalf("resume request replay = %+v, %v", retry, err)
	}
	if len(f.store.Records()) != recordsAfterRequest {
		t.Fatalf("resume request replay appended WAL: %d -> %d", recordsAfterRequest, len(f.store.Records()))
	}
	if _, err := f.service.RequestWorkerRunResume(context.Background(), auth, WorkerRunCAS{
		RunID: interrupted.RunID, ExpectedVersion: interrupted.Version,
	}, "resume-request-stale"); !errors.Is(err, ErrVersion) {
		t.Fatalf("stale resume request error = %v", err)
	}

	reloaded := New(f.store)
	reloaded.now = f.service.now
	recovered, err := reloaded.WorkerRunByID(context.Background(), auth, resume.RunID)
	if err != nil || !reflect.DeepEqual(recovered, resume) {
		t.Fatalf("reloaded resume request = %+v, %v", recovered, err)
	}
	reloadedRetry, err := reloaded.RequestWorkerRunResume(context.Background(), auth, WorkerRunCAS{
		RunID: interrupted.RunID, ExpectedVersion: interrupted.Version,
	}, "resume-request")
	if err != nil || !reflect.DeepEqual(reloadedRetry, resume) || len(f.store.Records()) != recordsAfterRequest {
		t.Fatalf("reloaded resume replay = %+v, %v records=%d", reloadedRetry, err, len(f.store.Records()))
	}
	active, err := reloaded.ActivateResumedWorkerRun(context.Background(), auth, WorkerRunCAS{
		RunID: resume.RunID, ExpectedVersion: resume.Version,
	}, "resume-activate")
	if err != nil || active.Status != WorkerRunActive || active.Version != resume.Version+1 ||
		active.TerminalReason != "" || active.TerminalSequence != 0 || active.Objective != resume.Objective || active.Prompt != resume.Prompt {
		t.Fatalf("resumed active run = %+v, %v", active, err)
	}
	activeRetry, err := reloaded.ActivateResumedWorkerRun(context.Background(), auth, WorkerRunCAS{
		RunID: resume.RunID, ExpectedVersion: resume.Version,
	}, "resume-activate")
	if err != nil || !reflect.DeepEqual(activeRetry, active) {
		t.Fatalf("resume activation replay = %+v, %v", activeRetry, err)
	}
}

func TestWorkerRunResumePreservesJournalInputAndDeferredOwnership(t *testing.T) {
	f := newFixture(t)
	auth := testAuthority("plugin#resume-state")
	scope := SessionScope{SessionID: auth.SessionID, Generation: auth.Generation}
	requested, err := f.service.RequestWorkerRun(context.Background(), auth, workerRequest("run"), "request")
	if err != nil {
		t.Fatal(err)
	}
	active, err := f.service.ActivateWorkerRun(context.Background(), auth, WorkerRunCAS{RunID: "run", ExpectedVersion: requested.Version}, "activate")
	if err != nil {
		t.Fatal(err)
	}
	journal, err := f.service.AppendJournal(context.Background(), auth, JournalAppend{RunID: "run", Kind: "quality.checkpoint", Summary: "state is recoverable"}, "journal")
	if err != nil {
		t.Fatal(err)
	}
	captured, err := f.service.CaptureOperatorInput(context.Background(), scope, "handle this after the workflow", "capture")
	if err != nil {
		t.Fatal(err)
	}
	deferred, err := f.service.RouteOperatorInput(context.Background(), auth, OperatorInputRoute{
		InputID: captured.ID, RunID: "run", ExpectedVersion: captured.Version,
		Disposition: OperatorInputDefer, Label: "follow-up",
	}, "defer")
	if err != nil {
		t.Fatal(err)
	}
	pause, err := f.service.RequestPause(context.Background(), auth, ControlInput{RunID: "run", ReasonCode: "review", Reason: "pause"}, "pause")
	if err != nil {
		t.Fatal(err)
	}
	interrupted, err := f.service.TerminalizeWorkerRun(context.Background(), auth, WorkerRunTerminal{
		RunID: "run", ExpectedVersion: active.Version, Status: WorkerRunInterrupted,
		Reason: pause.Reason, ControlSequence: pause.WALSequence,
	}, "consume")
	if err != nil {
		t.Fatal(err)
	}
	resume, err := f.service.RequestWorkerRunResume(context.Background(), auth, WorkerRunCAS{RunID: "run", ExpectedVersion: interrupted.Version}, "resume")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ActivateResumedWorkerRun(context.Background(), auth, WorkerRunCAS{RunID: "run", ExpectedVersion: resume.Version}, "resume-active"); err != nil {
		t.Fatal(err)
	}
	projection, err := f.service.Project(context.Background(), auth, ProjectionOptions{JournalLimit: 8, DeferredTaskLimit: 8})
	if err != nil || len(projection.Journal) != 1 || projection.Journal[0].ID != journal.ID || len(projection.DeferredTasks) != 1 ||
		projection.DeferredTasks[0].InputID != deferred.ID || projection.DeferredTasks[0].RunID != "run" || projection.DeferredTasks[0].Status != DeferredTaskOpen {
		t.Fatalf("resumed state projection = %+v, %v", projection, err)
	}
}

func TestCancelledAndStoppedWorkerRunsNeverResume(t *testing.T) {
	t.Run("cancelled", func(t *testing.T) {
		f := newFixture(t)
		auth := testAuthority("plugin#cancelled")
		requested, err := f.service.RequestWorkerRun(context.Background(), auth, workerRequest("run"), "request")
		if err != nil {
			t.Fatal(err)
		}
		cancelled, err := f.service.CancelWorkerRun(context.Background(), auth, WorkerRunCAS{RunID: "run", ExpectedVersion: requested.Version, Reason: "cancel"}, "cancel")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.RequestWorkerRunResume(context.Background(), auth, WorkerRunCAS{RunID: "run", ExpectedVersion: cancelled.Version}, "resume"); !errors.Is(err, ErrTerminal) {
			t.Fatalf("cancelled resume error = %v", err)
		}
	})
	t.Run("stopped", func(t *testing.T) {
		f := newFixture(t)
		auth := testAuthority("plugin#stopped")
		requested, err := f.service.RequestWorkerRun(context.Background(), auth, workerRequest("run"), "request")
		if err != nil {
			t.Fatal(err)
		}
		active, err := f.service.ActivateWorkerRun(context.Background(), auth, WorkerRunCAS{RunID: "run", ExpectedVersion: requested.Version}, "activate")
		if err != nil {
			t.Fatal(err)
		}
		stopped, err := f.service.TerminalizeWorkerRun(context.Background(), auth, WorkerRunTerminal{
			RunID: "run", ExpectedVersion: active.Version, Status: WorkerRunStopped,
			Reason: "stop", ControlSequence: 41,
		}, "stop")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.RequestWorkerRunResume(context.Background(), auth, WorkerRunCAS{RunID: "run", ExpectedVersion: stopped.Version}, "resume"); !errors.Is(err, ErrTerminal) {
			t.Fatalf("stopped resume error = %v", err)
		}
	})
}

func TestWorkerRunResumeRechecksPauseStopCompletionHoldsAndOwner(t *testing.T) {
	t.Run("pause race requires a fresh exact request", func(t *testing.T) {
		f := newFixture(t)
		auth := testAuthority("plugin#pause-race")
		interrupted := interruptWorkerRun(t, f, auth, "run")
		resume, err := f.service.RequestWorkerRunResume(context.Background(), auth, WorkerRunCAS{RunID: "run", ExpectedVersion: interrupted.Version}, "resume")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.RequestPause(context.Background(), auth, ControlInput{RunID: "run", ReasonCode: "new-review", Reason: "new pause"}, "new-pause"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.ActivateResumedWorkerRun(context.Background(), auth, WorkerRunCAS{RunID: "run", ExpectedVersion: resume.Version}, "activate-stale"); !errors.Is(err, ErrVersion) {
			t.Fatalf("activation after racing pause error = %v", err)
		}
		stillRequested, err := f.service.WorkerRunByID(context.Background(), auth, "run")
		if err != nil || stillRequested.Status != WorkerRunResumeRequested || stillRequested.Version != resume.Version ||
			stillRequested.TerminalReason != interrupted.TerminalReason || stillRequested.TerminalSequence != interrupted.TerminalSequence {
			t.Fatalf("pause race changed resume request = %+v, %v", stillRequested, err)
		}
		refreshed, err := f.service.RequestWorkerRunResume(context.Background(), auth, WorkerRunCAS{RunID: "run", ExpectedVersion: resume.Version}, "resume-refreshed")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.ActivateResumedWorkerRun(context.Background(), auth, WorkerRunCAS{RunID: "run", ExpectedVersion: refreshed.Version}, "activate-refreshed"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("stop and completion never resume", func(t *testing.T) {
		for _, terminal := range []string{"stop", "completion"} {
			t.Run(terminal, func(t *testing.T) {
				f := newFixture(t)
				auth := testAuthority("plugin#" + terminal)
				interrupted := interruptWorkerRun(t, f, auth, "run")
				switch terminal {
				case "stop":
					if _, err := f.service.RequestStop(context.Background(), auth, ControlInput{RunID: "run", ReasonCode: "operator-stop", Reason: "stop forever"}, "stop"); err != nil {
						t.Fatal(err)
					}
				case "completion":
					if _, err := f.service.CompleteSession(context.Background(), auth, CompletionInput{RunID: "run", Summary: "done"}, "complete"); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := f.service.RequestWorkerRunResume(context.Background(), auth, WorkerRunCAS{RunID: "run", ExpectedVersion: interrupted.Version}, "resume"); !errors.Is(err, ErrTerminal) {
					t.Fatalf("%s resume error = %v", terminal, err)
				}
			})
		}
	})

	t.Run("stop and completion racing after request block activation", func(t *testing.T) {
		for _, terminal := range []string{"stop", "completion"} {
			t.Run(terminal, func(t *testing.T) {
				f := newFixture(t)
				auth := testAuthority("plugin#activation-" + terminal)
				interrupted := interruptWorkerRun(t, f, auth, "run")
				resume, err := f.service.RequestWorkerRunResume(context.Background(), auth, WorkerRunCAS{RunID: "run", ExpectedVersion: interrupted.Version}, "resume")
				if err != nil {
					t.Fatal(err)
				}
				switch terminal {
				case "stop":
					if _, err := f.service.RequestStop(context.Background(), auth, ControlInput{RunID: "run", ReasonCode: "operator-stop", Reason: "stop forever"}, "stop"); err != nil {
						t.Fatal(err)
					}
				case "completion":
					if _, err := f.service.CompleteSession(context.Background(), auth, CompletionInput{RunID: "run", Summary: "done"}, "complete"); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := f.service.ActivateResumedWorkerRun(context.Background(), auth, WorkerRunCAS{RunID: "run", ExpectedVersion: resume.Version}, "activate"); !errors.Is(err, ErrTerminal) {
					t.Fatalf("activation after %s error = %v", terminal, err)
				}
				if terminal == "stop" {
					stillRequested, err := f.service.WorkerRunByID(context.Background(), auth, "run")
					if err != nil || stillRequested.Status != WorkerRunResumeRequested || stillRequested.Version != resume.Version ||
						stillRequested.TerminalReason != interrupted.TerminalReason || stillRequested.TerminalSequence != interrupted.TerminalSequence {
						t.Fatalf("stop race changed resume request = %+v, %v", stillRequested, err)
					}
				}
			})
		}
	})

	t.Run("conflicting broker owner blocks activation", func(t *testing.T) {
		f := newFixture(t)
		auth := testAuthority("plugin#resume-owner")
		interrupted := interruptWorkerRun(t, f, auth, "run")
		resume, err := f.service.RequestWorkerRunResume(context.Background(), auth, WorkerRunCAS{RunID: "run", ExpectedVersion: interrupted.Version}, "resume")
		if err != nil {
			t.Fatal(err)
		}
		otherAuth := testAuthority("plugin#other-owner")
		other, err := f.service.RequestWorkerRun(context.Background(), otherAuth, workerRequest("other"), "other-request")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.ActivateWorkerRun(context.Background(), otherAuth, WorkerRunCAS{RunID: other.RunID, ExpectedVersion: other.Version}, "other-active"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.ActivateResumedWorkerRun(context.Background(), auth, WorkerRunCAS{RunID: "run", ExpectedVersion: resume.Version}, "resume-active"); !errors.Is(err, ErrVersion) {
			t.Fatalf("conflicting owner activation error = %v", err)
		}
		otherActive, err := f.service.WorkerRunByID(context.Background(), otherAuth, other.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.CancelWorkerRun(context.Background(), otherAuth, WorkerRunCAS{RunID: other.RunID, ExpectedVersion: otherActive.Version, Reason: "release recurrence"}, "other-cancel"); err != nil {
			t.Fatal(err)
		}
		resumed, err := f.service.ActivateResumedWorkerRun(context.Background(), auth, WorkerRunCAS{RunID: "run", ExpectedVersion: resume.Version}, "resume-active-after-release")
		if err != nil || resumed.Status != WorkerRunActive {
			t.Fatalf("resume after owner release = %+v, %v", resumed, err)
		}
	})

	t.Run("own hold blocks across restart then exact retry succeeds after release", func(t *testing.T) {
		f := newFixture(t)
		auth := testAuthority("plugin#own-hold")
		interrupted := interruptWorkerRun(t, f, auth, "run")
		resume, err := f.service.RequestWorkerRunResume(context.Background(), auth, WorkerRunCAS{RunID: "run", ExpectedVersion: interrupted.Version}, "resume")
		if err != nil {
			t.Fatal(err)
		}
		hold, err := f.service.AcquireHold(context.Background(), auth, HoldAcquire{ID: "review", RunID: "run", ReasonCode: "review", TTL: time.Minute}, "hold")
		if err != nil {
			t.Fatal(err)
		}
		activation := WorkerRunCAS{RunID: "run", ExpectedVersion: resume.Version}
		if _, err := f.service.ActivateResumedWorkerRun(context.Background(), auth, activation, "resume-active"); !errors.Is(err, ErrVersion) || !strings.Contains(err.Error(), "active scheduling hold") {
			t.Fatalf("own hold activation error = %v", err)
		}
		stillRequested, err := f.service.WorkerRunByID(context.Background(), auth, "run")
		if err != nil || stillRequested.Status != WorkerRunResumeRequested || stillRequested.Version != resume.Version {
			t.Fatalf("held resume request = %+v, %v", stillRequested, err)
		}
		reloaded := New(f.store)
		reloaded.now = f.service.now
		if _, err := reloaded.ActivateResumedWorkerRun(context.Background(), auth, activation, "resume-active"); !errors.Is(err, ErrVersion) {
			t.Fatalf("restart ignored active hold: %v", err)
		}
		if _, err := reloaded.ReleaseHold(context.Background(), auth, HoldCAS{ID: hold.ID, RunID: hold.RunID, ExpectedVersion: hold.Version}, "release"); err != nil {
			t.Fatal(err)
		}
		resumed, err := reloaded.ActivateResumedWorkerRun(context.Background(), auth, activation, "resume-active")
		if err != nil || resumed.Status != WorkerRunActive {
			t.Fatalf("exact activation retry after release = %+v, %v", resumed, err)
		}
	})

	t.Run("other plugin hold blocks but expired hold does not", func(t *testing.T) {
		f := newFixture(t)
		auth := testAuthority("plugin#resume-held")
		interrupted := interruptWorkerRun(t, f, auth, "run")
		resume, err := f.service.RequestWorkerRunResume(context.Background(), auth, WorkerRunCAS{RunID: "run", ExpectedVersion: interrupted.Version}, "resume")
		if err != nil {
			t.Fatal(err)
		}
		other := testAuthority("plugin#holding")
		hold, err := f.service.AcquireHold(context.Background(), other, HoldAcquire{ID: "other-review", RunID: "other-run", ReasonCode: "review", TTL: time.Minute}, "hold")
		if err != nil {
			t.Fatal(err)
		}
		activation := WorkerRunCAS{RunID: "run", ExpectedVersion: resume.Version}
		if _, err := f.service.ActivateResumedWorkerRun(context.Background(), auth, activation, "activate-other-held"); !errors.Is(err, ErrVersion) || !strings.Contains(err.Error(), "active scheduling hold") {
			t.Fatalf("other hold activation error = %v", err)
		}
		if _, err := f.service.ReleaseHold(context.Background(), other, HoldCAS{ID: hold.ID, RunID: hold.RunID, ExpectedVersion: hold.Version}, "release-other"); err != nil {
			t.Fatal(err)
		}
		if _, err := f.service.AcquireHold(context.Background(), other, HoldAcquire{ID: "expired", RunID: "other-run", ReasonCode: "review", TTL: time.Second}, "expired"); err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(2 * time.Second)
		resumed, err := f.service.ActivateResumedWorkerRun(context.Background(), auth, activation, "activate-after-expiry")
		if err != nil || resumed.Status != WorkerRunActive {
			t.Fatalf("expired hold blocked activation = %+v, %v", resumed, err)
		}
	})

	t.Run("hold release and activation serialize", func(t *testing.T) {
		f := newFixture(t)
		auth := testAuthority("plugin#hold-race")
		interrupted := interruptWorkerRun(t, f, auth, "run")
		resume, err := f.service.RequestWorkerRunResume(context.Background(), auth, WorkerRunCAS{RunID: "run", ExpectedVersion: interrupted.Version}, "resume")
		if err != nil {
			t.Fatal(err)
		}
		hold, err := f.service.AcquireHold(context.Background(), auth, HoldAcquire{ID: "review", RunID: "run", ReasonCode: "review", TTL: time.Minute}, "hold")
		if err != nil {
			t.Fatal(err)
		}
		activation := WorkerRunCAS{RunID: "run", ExpectedVersion: resume.Version}
		var releaseErr, activateErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, releaseErr = f.service.ReleaseHold(context.Background(), auth, HoldCAS{ID: hold.ID, RunID: hold.RunID, ExpectedVersion: hold.Version}, "release")
		}()
		go func() {
			defer wg.Done()
			_, activateErr = f.service.ActivateResumedWorkerRun(context.Background(), auth, activation, "activate")
		}()
		wg.Wait()
		if releaseErr != nil || activateErr != nil && !errors.Is(activateErr, ErrVersion) {
			t.Fatalf("release/activate race errors = %v / %v", releaseErr, activateErr)
		}
		if activateErr != nil {
			if _, err := f.service.ActivateResumedWorkerRun(context.Background(), auth, activation, "activate"); err != nil {
				t.Fatalf("activation retry after raced release = %v", err)
			}
		}
	})
}

func TestWorkerRunActivationCASSerializesAcrossApplications(t *testing.T) {
	f := newFixture(t)
	const writers = 12
	type candidate struct {
		auth Authority
		run  WorkerRun
	}
	candidates := make([]candidate, writers)
	for i := range candidates {
		auth := testAuthority("plugin#worker-" + string(rune('a'+i)))
		run, err := f.service.RequestWorkerRun(context.Background(), auth, workerRequest("run"), "request")
		if err != nil {
			t.Fatal(err)
		}
		candidates[i] = candidate{auth: auth, run: run}
	}
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := range candidates {
		wg.Add(1)
		go func() {
			defer wg.Done()
			candidate := candidates[i]
			_, errs[i] = f.service.ActivateWorkerRun(context.Background(), candidate.auth, WorkerRunCAS{RunID: candidate.run.RunID, ExpectedVersion: candidate.run.Version}, "activate")
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
			t.Fatalf("activation error = %v", err)
		}
	}
	if success != 1 || conflicts != writers-1 {
		t.Fatalf("activation success/conflicts = %d/%d", success, conflicts)
	}
}

func TestWorkerRunValidationAndBound(t *testing.T) {
	f := newFixture(t)
	auth := testAuthority("plugin#worker")
	tests := []WorkerRunRequest{
		{RunID: "bad run", Objective: "objective", Prompt: "prompt", Conflict: WorkerRunRejectOperatorLoop},
		{RunID: "run", Objective: " ", Prompt: "prompt", Conflict: WorkerRunRejectOperatorLoop},
		{RunID: "run", Objective: "objective", Prompt: " ", Conflict: WorkerRunRejectOperatorLoop},
		{RunID: "run", Objective: "objective", Prompt: "prompt", Conflict: "replace_anything"},
		{RunID: "run", Objective: "objective", Prompt: strings.Repeat("x", DefaultLimits().MaxWorkerPromptBytes+1), Conflict: WorkerRunRejectOperatorLoop},
	}
	for i, input := range tests {
		if _, err := f.service.RequestWorkerRun(context.Background(), auth, input, "invalid-"+string(rune('a'+i))); !errors.Is(err, ErrInvalid) {
			t.Fatalf("case %d error = %v", i, err)
		}
	}
	limits := DefaultLimits()
	limits.MaxWorkerRuns = 1
	f = newFixtureWithLimits(t, limits)
	if _, err := f.service.RequestWorkerRun(context.Background(), auth, workerRequest("one"), "one"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RequestWorkerRun(context.Background(), auth, workerRequest("two"), "two"); !errors.Is(err, ErrLimit) {
		t.Fatalf("worker run bound error = %v", err)
	}
}

func TestWorkerRunProjectionIsBoundedAndReportsTruncation(t *testing.T) {
	f := newFixture(t)
	auth := testAuthority("plugin#projection")
	for _, id := range []string{"one", "two", "three"} {
		if _, err := f.service.RequestWorkerRun(context.Background(), auth, workerRequest(id), "request-"+id); err != nil {
			t.Fatal(err)
		}
		f.now = f.now.Add(1)
	}
	projection, err := f.service.Project(context.Background(), auth, ProjectionOptions{WorkerLimit: 1})
	if err != nil || !projection.WorkerRunsTruncated || len(projection.WorkerRuns) != 1 || projection.WorkerRuns[0].RunID != "three" {
		t.Fatalf("bounded projection = %+v, %v", projection, err)
	}
}
