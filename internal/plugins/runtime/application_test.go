package runtime

import (
	"context"
	"errors"
	"io"
	"log/slog"
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
		{Status: "ok", CancelWorkerRunID: "run:cancel-1"},
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
		{Status: "error", CancelWorkerRunID: "run-1"},
		{Status: "ok", CancelWorkerRunID: "bad run"},
		{Status: "ok", WorkerRunID: "run-1", ResumeWorkerRunID: "run-1"},
		{Status: "ok", ResumeWorkerRunID: "run-1", CancelWorkerRunID: "run-1"},
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

func TestCommandResultCancellationHandoffUsesStrictDistinctWireField(t *testing.T) {
	var result CommandResult
	if err := decodeStrictJSON([]byte(`{"status":"ok","cancel_worker_run_id":"run-1"}`), &result); err != nil || result.CancelWorkerRunID != "run-1" || result.WorkerRunID != "" || result.ResumeWorkerRunID != "" {
		t.Fatalf("cancel wire result = %+v, %v", result, err)
	}
	if err := validateCommandResult(result); err != nil {
		t.Fatal(err)
	}
	if err := decodeStrictJSON([]byte(`{"status":"ok","cancel_worker_run_id":"run-1","cancel":true}`), &result); err == nil {
		t.Fatal("unknown cancellation handoff field accepted")
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
		true, false,
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
			_, err := decodeLifecycleDecision(hooks.PointPreLLM, current, []byte(raw), false, false)
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
		[]byte(`{"decision":"mutate","mutation":{"text":"rewritten"}}`), true, false,
	)
	if err == nil || !strings.Contains(err.Error(), "observation-only") {
		t.Fatalf("post_turn mutation err=%v", err)
	}
}

func TestLifecycleContributionAppendsWithoutDecisionAuthority(t *testing.T) {
	current := hooks.PreLLM(2, "model", "base system", 4, 5)
	result, err := decodeLifecycleDecision(
		hooks.PointPreLLM, current,
		[]byte(`{"decision":"contribute","contribution":{"system_append":"bounded guidance"}}`),
		false, true,
	)
	if err != nil || result.Decision != hooks.DecisionMutate {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	next, ok := result.Payload.(*hooks.PreLLMPayload)
	if !ok || next.System != "base system\n\nbounded guidance" || next.Model != current.Model {
		t.Fatalf("contribution=%#v", result.Payload)
	}
	for name, raw := range map[string]string{
		"missing capability":   `{"decision":"contribute","contribution":{"system_append":"x"}}`,
		"replacement mutation": `{"decision":"contribute","mutation":{"system":"x"},"contribution":{"system_append":"y"}}`,
		"deny fields":          `{"decision":"contribute","reason":"stop","contribution":{"system_append":"x"}}`,
		"unknown field":        `{"decision":"contribute","contribution":{"system_append":"x","model":"other"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			canContribute := name != "missing capability"
			if _, err := decodeLifecycleDecision(hooks.PointPreLLM, current, []byte(raw), false, canContribute); err == nil {
				t.Fatal("invalid contribution accepted")
			}
		})
	}
}

func TestFailureOpenContributionCannotBecomeGlobalFailClosedGate(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
		trap   bool
	}{
		{name: "callback trap", trap: true},
		{name: "malformed response", output: `{`},
		{name: "attempted deny", output: `{"decision":"deny","reason":"block unrelated work"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			app := loadContributionTestApplication(t, test.output, test.trap)
			runner := hooks.NewLifecycleRunner(app)
			runner.FailClosed = true
			original := hooks.PreLLM(1, "model", "operator system", 2, 3)
			result, payload := runner.Fire(context.Background(), hooks.PointPreLLM, original)
			if result.Decision != hooks.DecisionContinue || payload != original || original.System != "operator system" {
				t.Fatalf("result=%+v payload=%#v original=%#v", result, payload, original)
			}
		})
	}
}

func TestLifecycleNegativeErrorPayloadIsSurfaced(t *testing.T) {
	app := loadContributionTestApplicationResult(t, "exact guest failure", false, true)
	_, err := app.callJSON(context.Background(), app.lifecycleFn, []byte(`{"probe":true}`))
	if err == nil || err.Error() != "exact guest failure" {
		t.Fatalf("negative lifecycle error=%v", err)
	}
}

type lifecycleTestHook struct {
	name string
	run  func(hooks.Payload) hooks.HookResult
}

func (h lifecycleTestHook) Name() string          { return h.name }
func (h lifecycleTestHook) Points() []hooks.Point { return []hooks.Point{hooks.PointPreLLM} }
func (h lifecycleTestHook) Run(_ context.Context, _ hooks.Point, payload hooks.Payload) (hooks.HookResult, error) {
	return h.run(payload), nil
}

func TestContributionChainsAfterDecideCapableMutation(t *testing.T) {
	decider := lifecycleTestHook{name: "a-decider", run: func(payload hooks.Payload) hooks.HookResult {
		current := payload.(*hooks.PreLLMPayload)
		next := *current
		next.System = "decided system"
		return hooks.Mutate(&next)
	}}
	contributor := loadContributionTestApplication(t, `{"decision":"contribute","contribution":{"system_append":"quality facts guidance"}}`, false)
	probe := hooks.PreLLM(1, "model", "probe", 2, 3)
	probeResult, probeErr := contributor.Run(context.Background(), hooks.PointPreLLM, probe)
	if probeErr != nil || probeResult.Decision != hooks.DecisionMutate {
		raw, rawErr := contributor.callJSON(context.Background(), contributor.lifecycleFn, []byte(`{"probe":true}`))
		t.Fatalf("contributor probe=%+v err=%v raw=%q rawErr=%v", probeResult, probeErr, raw, rawErr)
	}
	runner := hooks.NewLifecycleRunner(decider, contributor)
	original := hooks.PreLLM(1, "model", "base", 2, 3)
	result, payload := runner.Fire(context.Background(), hooks.PointPreLLM, original)
	got := payload.(*hooks.PreLLMPayload)
	if result.Decision != hooks.DecisionMutate || got.System != "decided system\n\nquality facts guidance" {
		t.Fatalf("result=%+v system=%q", result, got.System)
	}
}

func loadContributionTestApplication(t *testing.T, output string, trap bool) *LifecycleApplication {
	return loadContributionTestApplicationResult(t, output, trap, false)
}

func loadContributionTestApplicationResult(t *testing.T, output string, trap, negative bool) *LifecycleApplication {
	t.Helper()
	ctx := context.Background()
	rt, err := New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close(ctx) })
	manifest := plugins.Manifest{
		Name: "guidance-test", Version: "1.0.0",
		Lifecycle:    &plugins.LifecycleDef{Points: []string{"pre_llm"}, Failure: "open"},
		Capabilities: []string{"lifecycle:observe:pre_llm", "lifecycle:contribute:pre_llm"},
	}
	identity, err := plugins.RuntimeIdentityForLocal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	host := NewHost(manifest, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	host.Identity = identity
	app, err := LoadLifecycleApplication(ctx, rt, encodeLifecycleTestModuleResult(output, trap, negative), host, ApplicationAnchor{SessionID: "session-1", SessionGeneration: 1})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

// encodeLifecycleTestModule emits the smallest useful lifecycle ABI module:
// alloc/free plus a callback which either traps or writes one constant result.
// It exercises LifecycleApplication.Run through wazero instead of mocking the
// boundary whose failure posture this test protects.
func encodeLifecycleTestModule(output string, trap bool) []byte {
	return encodeLifecycleTestModuleResult(output, trap, false)
}

func encodeLifecycleTestModuleResult(output string, trap, negative bool) []byte {
	var w wasmWriter
	w.bytes(0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00)
	{
		var s wasmWriter
		s.uleb128(3)
		for _, signature := range []struct{ params, results uint32 }{{1, 1}, {2, 0}, {4, 1}} {
			s.bytes(0x60)
			s.uleb128(signature.params)
			for i := uint32(0); i < signature.params; i++ {
				s.bytes(0x7f)
			}
			s.uleb128(signature.results)
			if signature.results != 0 {
				s.bytes(0x7f)
			}
		}
		w.section(1, s.buf)
	}
	{
		var s wasmWriter
		s.uleb128(3)
		s.uleb128(0)
		s.uleb128(1)
		s.uleb128(2)
		w.section(3, s.buf)
	}
	{
		var s wasmWriter
		s.uleb128(1)
		s.bytes(0x00)
		s.uleb128(17) // enough for the 1 MiB result allocation
		w.section(5, s.buf)
	}
	{
		var s wasmWriter
		s.uleb128(4)
		for _, export := range []struct {
			name  string
			kind  byte
			index uint32
		}{{"memory", 0x02, 0}, {"stado_alloc", 0x00, 0}, {"stado_free", 0x00, 1}, {"stado_plugin_lifecycle", 0x00, 2}} {
			s.name(export.name)
			s.bytes(export.kind)
			s.uleb128(export.index)
		}
		w.section(7, s.buf)
	}
	{
		var s wasmWriter
		s.uleb128(3)
		alloc := wasmWriter{}
		alloc.uleb128(0)
		alloc.bytes(0x41)
		appendSLEB32(&alloc, 1024)
		alloc.bytes(0x0b) // i32.const 1024; end
		appendWasmBody(&s, alloc.buf)
		free := wasmWriter{}
		free.uleb128(0)
		free.bytes(0x0b)
		appendWasmBody(&s, free.buf)
		callback := wasmWriter{}
		callback.uleb128(0)
		if trap {
			callback.bytes(0x00) // unreachable
		} else {
			for offset, value := range []byte(output) {
				callback.bytes(0x20, 0x02, 0x41) // local.get result_ptr; i32.const value
				appendSLEB32(&callback, int32(value))
				callback.bytes(0x3a, 0x00) // i32.store8 align=1
				callback.uleb128(uint32(offset))
			}
			callback.bytes(0x41)
			resultLength := int32(len(output))
			if negative {
				resultLength = -resultLength
			}
			appendSLEB32(&callback, resultLength)
		}
		callback.bytes(0x0b)
		appendWasmBody(&s, callback.buf)
		w.section(10, s.buf)
	}
	return w.buf
}

func appendWasmBody(section *wasmWriter, body []byte) {
	section.uleb128(uint32(len(body)))
	section.buf = append(section.buf, body...)
}

func appendSLEB32(w *wasmWriter, value int32) {
	for {
		b := byte(value & 0x7f)
		value >>= 7
		done := (value == 0 && b&0x40 == 0) || (value == -1 && b&0x40 != 0)
		if !done {
			b |= 0x80
		}
		w.bytes(b)
		if done {
			return
		}
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
