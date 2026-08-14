package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/internal/plugins"
)

func TestValidateCommandResultWorkerRunHandoff(t *testing.T) {
	for _, result := range []CommandResult{
		{Status: "ok"},
		{Status: "ok", WorkerRunID: "run:supervise-1"},
		{Status: "ok", ResumeWorkerRunID: "run:resume-1"},
	} {
		if err := validateCommandResult(result); err != nil {
			t.Fatalf("valid result %+v: %v", result, err)
		}
	}
	for _, result := range []CommandResult{
		{Status: "error", WorkerRunID: "run-1"},
		{Status: "ok", WorkerRunID: "bad run"},
		{Status: "ok", WorkerRunID: "-leading"},
		{Status: "error", ResumeWorkerRunID: "run-1"},
		{Status: "ok", ResumeWorkerRunID: "bad run"},
		{Status: "ok", WorkerRunID: "run-1", ResumeWorkerRunID: "run-1"},
	} {
		if err := validateCommandResult(result); err == nil {
			t.Fatalf("invalid result accepted: %+v", result)
		}
	}
}

func TestCommandResultResumeHandoffUsesStrictDistinctWireField(t *testing.T) {
	var result CommandResult
	if err := decodeStrictJSON([]byte(`{"status":"ok","resume_worker_run_id":"run-1"}`), &result); err != nil || result.ResumeWorkerRunID != "run-1" || result.WorkerRunID != "" {
		t.Fatalf("resume wire result = %+v, %v", result, err)
	}
	if err := decodeStrictJSON([]byte(`{"status":"ok","resume_worker_run_id":"run-1","worker_run_id":"run-1"}`), &result); err != nil {
		t.Fatal(err)
	}
	if err := validateCommandResult(result); err == nil {
		t.Fatal("ambiguous initial/resume handoff accepted")
	}
	if err := decodeStrictJSON([]byte(`{"status":"ok","resume_worker_run_id":"run-1","resume":true}`), &result); err == nil {
		t.Fatal("unknown resume handoff field accepted")
	}
}

func TestApplicationSerializationContentionIsCancellable(t *testing.T) {
	gate := newSerializedCallGate()
	if err := gate.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := gate.lock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended lock err=%v", err)
	}
	gate.unlock()
	if err := gate.lock(context.Background()); err != nil {
		t.Fatalf("gate did not recover after cancelled contention: %v", err)
	}
	gate.unlock()
}

func TestLifecycleDecisionPreservesHostOwnedFields(t *testing.T) {
	current := hooks.PreTool(7, "shell", "exec", `{"cmd":"old"}`)
	result, err := decodeLifecycleDecision(
		hooks.PointPreTool,
		current,
		[]byte(`{"decision":"mutate","mutation":{"args":"{\"cmd\":\"safe\"}"}}`),
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	next, ok := result.Payload.(*hooks.PreToolPayload)
	if !ok || next.Args != `{"cmd":"safe"}` {
		t.Fatalf("mutation=%+v", result)
	}
	if next.Tool != current.Tool || next.Class != current.Class || next.TurnIndex != current.TurnIndex || next.Timestamp != current.Timestamp {
		t.Fatalf("plugin changed host-owned lifecycle facts: before=%+v after=%+v", current, next)
	}
}

func TestLifecycleDecisionNeedsDecideAuthorityAndStrictShape(t *testing.T) {
	current := hooks.PreLLM(2, "model", "system", 4, 5)
	for name, raw := range map[string]string{
		"deny without authority": `{"decision":"deny","reason":"stop"}`,
		"unknown field":          `{"decision":"continue","extra":true}`,
		"trailing value":         `{"decision":"continue"} {}`,
		"continue with reason":   `{"decision":"continue","reason":"no"}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := decodeLifecycleDecision(hooks.PointPreLLM, current, []byte(raw), false)
			if err == nil {
				t.Fatal("invalid lifecycle decision accepted")
			}
		})
	}
}

func TestLifecyclePostTurnCannotMutate(t *testing.T) {
	current := hooks.PostTurnLifecycle(3, "done", 10, 20, 0, 0)
	_, err := decodeLifecycleDecision(
		hooks.PointPostTurn, current,
		[]byte(`{"decision":"mutate","mutation":{"text":"rewritten"}}`), true,
	)
	if err == nil || !strings.Contains(err.Error(), "observation-only") {
		t.Fatalf("post_turn mutation err=%v", err)
	}
}

func TestLoadLifecycleApplicationRequiresDeclaredExport(t *testing.T) {
	ctx := context.Background()
	rt, err := New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rt.Close(ctx) }()
	manifest := plugins.Manifest{
		Name: "watcher", Version: "1.0.0",
		Lifecycle:    &plugins.LifecycleDef{Points: []string{"post_turn"}},
		Capabilities: []string{"lifecycle:observe:post_turn"},
	}
	identity, err := plugins.RuntimeIdentityForLocal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	host := NewHost(manifest, t.TempDir(), nil)
	host.Identity = identity
	_, err = LoadLifecycleApplication(ctx, rt, minimalWasm, host, ApplicationAnchor{SessionID: "session-1", SessionGeneration: 1})
	if err == nil || !strings.Contains(err.Error(), "stado_plugin_lifecycle") {
		t.Fatalf("missing lifecycle export err=%v", err)
	}
}

func TestLoadLifecycleApplicationRequiresDeclaredCommandExport(t *testing.T) {
	ctx := context.Background()
	rt, err := New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rt.Close(ctx) }()
	manifest := plugins.Manifest{
		Name: "watcher", Version: "1.0.0",
		Lifecycle: &plugins.LifecycleDef{},
		Commands:  []plugins.CommandDef{{Name: "watch", Description: "Watch work"}},
	}
	identity, err := plugins.RuntimeIdentityForLocal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	host := NewHost(manifest, t.TempDir(), nil)
	host.Identity = identity
	_, err = LoadLifecycleApplication(ctx, rt, minimalWasm, host, ApplicationAnchor{SessionID: "session-1", SessionGeneration: 1})
	if err == nil || !strings.Contains(err.Error(), "stado_plugin_command") {
		t.Fatalf("missing command export err=%v", err)
	}
}

func TestApplicationAnchorRejectsUnadmittedSession(t *testing.T) {
	for _, anchor := range []ApplicationAnchor{{}, {SessionID: "session"}, {SessionGeneration: 1}} {
		if err := anchor.Validate(); err == nil {
			t.Fatalf("invalid anchor accepted: %+v", anchor)
		}
	}
}
