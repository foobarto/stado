package hooks

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// builtin hook helpers ----------------------------------------------------

// denyOnTool denies any pre_tool whose tool name matches.
func denyOnTool(name, target, reason string) BuiltinHook {
	return BuiltinHook{HookName: name, Subscribed: []Point{PointPreTool}, Fn: func(_ context.Context, _ Point, p Payload) (HookResult, error) {
		pt := p.(*PreToolPayload)
		if pt.Tool == target {
			return Deny(reason), nil
		}
		return Continue(), nil
	}}
}

// rewriteArgs mutates a pre_tool payload's args to newArgs.
func rewriteArgs(name, newArgs string) BuiltinHook {
	return BuiltinHook{HookName: name, Subscribed: []Point{PointPreTool}, Fn: func(_ context.Context, _ Point, p Payload) (HookResult, error) {
		clone := *p.(*PreToolPayload)
		clone.Args = newArgs
		return Mutate(&clone), nil
	}}
}

// recordOrder appends its name to *log every time it runs.
func recordOrder(name string, log *[]string) BuiltinHook {
	return BuiltinHook{HookName: name, Fn: func(context.Context, Point, Payload) (HookResult, error) {
		*log = append(*log, name)
		return Continue(), nil
	}}
}

// tests -------------------------------------------------------------------

func TestLifecycleRunner_NoHooks_Continue(t *testing.T) {
	var r *LifecycleRunner // nil receiver
	res, out := r.Fire(context.Background(), PointPreTool, PreTool(0, "x", "exec", "{}"))
	if res.Decision != DecisionContinue {
		t.Fatalf("nil runner should Continue, got %v", res.Decision)
	}
	if out == nil {
		t.Fatalf("Fire must return a non-nil payload")
	}

	empty := NewLifecycleRunner()
	res, _ = empty.Fire(context.Background(), PointPreTool, PreTool(0, "x", "exec", "{}"))
	if res.Decision != DecisionContinue {
		t.Fatalf("empty runner should Continue, got %v", res.Decision)
	}
}

func TestLifecycleRunner_Deny_ShortCircuits(t *testing.T) {
	var ran []string
	r := NewLifecycleRunner(
		recordOrder("first", &ran),
		denyOnTool("policy", "shell__bash", "bash blocked"),
		recordOrder("after-deny", &ran),
	)
	res, _ := r.Fire(context.Background(), PointPreTool, PreTool(0, "shell__bash", "exec", "{}"))
	if res.Decision != DecisionDeny {
		t.Fatalf("expected Deny, got %v", res.Decision)
	}
	if res.Reason != "bash blocked" {
		t.Fatalf("reason lost: %q", res.Reason)
	}
	// The hook AFTER the deny must not have run (short-circuit).
	for _, n := range ran {
		if n == "after-deny" {
			t.Fatalf("hook after a deny ran; deny did not short-circuit: %v", ran)
		}
	}
	if len(ran) != 1 || ran[0] != "first" {
		t.Fatalf("expected only 'first' to run before deny, got %v", ran)
	}
}

func TestLifecycleRunner_Mutate_RewritesPayload(t *testing.T) {
	r := NewLifecycleRunner(rewriteArgs("rw", `{"path":"safe"}`))
	res, out := r.Fire(context.Background(), PointPreTool, PreTool(0, "fs__read", "non-mutating", `{"path":"secret"}`))
	if res.Decision != DecisionMutate {
		t.Fatalf("expected Mutate, got %v", res.Decision)
	}
	pt, ok := out.(*PreToolPayload)
	if !ok {
		t.Fatalf("returned payload is not *PreToolPayload: %T", out)
	}
	if pt.Args != `{"path":"safe"}` {
		t.Fatalf("args not rewritten: %q", pt.Args)
	}
}

// A mutate is threaded into the NEXT hook, and a later deny that
// inspects the mutated value wins.
func TestLifecycleRunner_Mutate_ChainsIntoNextHook(t *testing.T) {
	// hook 1 rewrites args to contain "BLOCK"; hook 2 denies if args
	// contains "BLOCK". Proves the mutation was visible to hook 2.
	rw := rewriteArgs("rw", `{"cmd":"BLOCK"}`)
	denyIfBlock := BuiltinHook{HookName: "deny-block", Subscribed: []Point{PointPreTool}, Fn: func(_ context.Context, _ Point, p Payload) (HookResult, error) {
		pt := p.(*PreToolPayload)
		if strings.Contains(pt.Args, "BLOCK") {
			return Deny("saw BLOCK"), nil
		}
		return Continue(), nil
	}}
	r := NewLifecycleRunner(rw, denyIfBlock)
	res, _ := r.Fire(context.Background(), PointPreTool, PreTool(0, "shell__bash", "exec", `{"cmd":"ls"}`))
	if res.Decision != DecisionDeny || res.Reason != "saw BLOCK" {
		t.Fatalf("mutation not threaded into next hook: %+v", res)
	}
}

func TestLifecycleRunner_FailOpen_OnError(t *testing.T) {
	var log bytes.Buffer
	boom := BuiltinHook{HookName: "boom", Fn: func(context.Context, Point, Payload) (HookResult, error) {
		return Continue(), errors.New("kaboom")
	}}
	var ran []string
	r := &LifecycleRunner{hooks: []HookScript{boom, recordOrder("after", &ran)}, Logger: &log}
	res, _ := r.Fire(context.Background(), PointPreTool, PreTool(0, "x", "exec", "{}"))
	if res.Decision != DecisionContinue {
		t.Fatalf("erroring hook must fail-open to Continue, got %v", res.Decision)
	}
	// The hook after the erroring one still runs (fail-open != short-circuit).
	if len(ran) != 1 || ran[0] != "after" {
		t.Fatalf("fail-open should keep running later hooks, ran=%v", ran)
	}
	if !strings.Contains(log.String(), "fail-open") {
		t.Fatalf("expected fail-open log line, got %q", log.String())
	}
}

func TestLifecycleRunner_FailOpen_OnPanic(t *testing.T) {
	var log bytes.Buffer
	panicker := BuiltinHook{HookName: "panicker", Fn: func(context.Context, Point, Payload) (HookResult, error) {
		panic("hook exploded")
	}}
	r := &LifecycleRunner{hooks: []HookScript{panicker}, Logger: &log}
	res, _ := r.Fire(context.Background(), PointPreTool, PreTool(0, "x", "exec", "{}"))
	if res.Decision != DecisionContinue {
		t.Fatalf("panicking hook must fail-open to Continue, got %v", res.Decision)
	}
	if !strings.Contains(log.String(), "fail-open") {
		t.Fatalf("expected fail-open log for panic, got %q", log.String())
	}
}

func TestLifecycleRunner_Ordering(t *testing.T) {
	var ran []string
	r := NewLifecycleRunner(
		recordOrder("a", &ran),
		recordOrder("b", &ran),
		recordOrder("c", &ran),
	)
	r.Fire(context.Background(), PointPreTool, PreTool(0, "x", "exec", "{}"))
	if strings.Join(ran, ",") != "a,b,c" {
		t.Fatalf("hooks ran out of config order: %v", ran)
	}
}

func TestLifecycleRunner_Timeout(t *testing.T) {
	var log bytes.Buffer
	slow := BuiltinHook{HookName: "slow", Fn: func(ctx context.Context, _ Point, _ Payload) (HookResult, error) {
		// Block until the per-hook timeout cancels ctx, then report it.
		select {
		case <-ctx.Done():
			return Continue(), ctx.Err()
		case <-time.After(5 * time.Second):
			return Continue(), nil
		}
	}}
	r := &LifecycleRunner{hooks: []HookScript{slow}, Logger: &log, Timeout: 50 * time.Millisecond}
	start := time.Now()
	res, _ := r.Fire(context.Background(), PointPreTool, PreTool(0, "x", "exec", "{}"))
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Fatalf("hook ran past the timeout cap: %v", elapsed)
	}
	// Timeout surfaces as a fail-open Continue (the ctx deadline error is
	// absorbed and logged).
	if res.Decision != DecisionContinue {
		t.Fatalf("timed-out hook should fail-open to Continue, got %v", res.Decision)
	}
	if !strings.Contains(log.String(), "fail-open") {
		t.Fatalf("expected fail-open log on timeout, got %q", log.String())
	}
}

func TestLifecycleRunner_WrongPointMutation_FailsOpen(t *testing.T) {
	var log bytes.Buffer
	// A hook that returns a payload for the WRONG point on a mutate.
	wrong := BuiltinHook{HookName: "wrong", Subscribed: []Point{PointPreTool}, Fn: func(context.Context, Point, Payload) (HookResult, error) {
		return Mutate(PostLLM(0, "oops", 0, 0, 0, 0)), nil
	}}
	r := &LifecycleRunner{hooks: []HookScript{wrong}, Logger: &log}
	res, out := r.Fire(context.Background(), PointPreTool, PreTool(0, "x", "exec", `{"a":1}`))
	if res.Decision != DecisionContinue {
		t.Fatalf("invalid mutation must be ignored (fail-open), got %v", res.Decision)
	}
	// The original payload is preserved.
	if pt, ok := out.(*PreToolPayload); !ok || pt.Args != `{"a":1}` {
		t.Fatalf("original payload not preserved after invalid mutation: %#v", out)
	}
	if !strings.Contains(log.String(), "invalid mutation") {
		t.Fatalf("expected invalid-mutation log, got %q", log.String())
	}
}

// fail-closed posture ----------------------------------------------------

// TestLifecycleRunner_FailClosed_ErrorDenies: with FailClosed set, a hook
// that errors becomes a Deny (the inverse of the fail-open default) and
// short-circuits the remaining hooks, just like an explicit deny.
func TestLifecycleRunner_FailClosed_ErrorDenies(t *testing.T) {
	var log bytes.Buffer
	boom := BuiltinHook{HookName: "boom", Fn: func(context.Context, Point, Payload) (HookResult, error) {
		return Continue(), errors.New("kaboom")
	}}
	var ran []string
	r := &LifecycleRunner{hooks: []HookScript{boom, recordOrder("after", &ran)}, Logger: &log, FailClosed: true}
	res, _ := r.Fire(context.Background(), PointPreTool, PreTool(0, "x", "exec", "{}"))
	if res.Decision != DecisionDeny {
		t.Fatalf("fail-closed: erroring hook must Deny, got %v", res.Decision)
	}
	if !strings.Contains(res.Reason, "boom") || !strings.Contains(res.Reason, "kaboom") {
		t.Fatalf("deny reason should name the hook + error, got %q", res.Reason)
	}
	// Deny short-circuits: the hook after the erroring one must NOT run.
	if len(ran) != 0 {
		t.Fatalf("fail-closed deny should short-circuit later hooks, ran=%v", ran)
	}
	if !strings.Contains(log.String(), "fail-closed") {
		t.Fatalf("expected fail-closed log line, got %q", log.String())
	}
}

// TestLifecycleRunner_FailClosed_PanicDenies: a panicking hook denies under
// fail-closed (the panic is recovered into an error, then converted).
func TestLifecycleRunner_FailClosed_PanicDenies(t *testing.T) {
	var log bytes.Buffer
	panicker := BuiltinHook{HookName: "panicker", Fn: func(context.Context, Point, Payload) (HookResult, error) {
		panic("hook exploded")
	}}
	r := &LifecycleRunner{hooks: []HookScript{panicker}, Logger: &log, FailClosed: true}
	res, _ := r.Fire(context.Background(), PointPreTool, PreTool(0, "x", "exec", "{}"))
	if res.Decision != DecisionDeny {
		t.Fatalf("fail-closed: panicking hook must Deny, got %v", res.Decision)
	}
}

// TestLifecycleRunner_FailClosed_TimeoutDenies: a hook that blocks past its
// per-hook timeout denies under fail-closed.
func TestLifecycleRunner_FailClosed_TimeoutDenies(t *testing.T) {
	var log bytes.Buffer
	slow := BuiltinHook{HookName: "slow", Fn: func(ctx context.Context, _ Point, _ Payload) (HookResult, error) {
		select {
		case <-ctx.Done():
			return Continue(), ctx.Err()
		case <-time.After(5 * time.Second):
			return Continue(), nil
		}
	}}
	r := &LifecycleRunner{hooks: []HookScript{slow}, Logger: &log, Timeout: 50 * time.Millisecond, FailClosed: true}
	res, _ := r.Fire(context.Background(), PointPreTool, PreTool(0, "x", "exec", "{}"))
	if res.Decision != DecisionDeny {
		t.Fatalf("fail-closed: timed-out hook must Deny, got %v", res.Decision)
	}
}

// TestLifecycleRunner_FailClosed_InvalidMutationDenies: a hook that mutates
// to the wrong payload type is a fault; under fail-closed it denies rather
// than being silently ignored.
func TestLifecycleRunner_FailClosed_InvalidMutationDenies(t *testing.T) {
	var log bytes.Buffer
	wrong := BuiltinHook{HookName: "wrong", Subscribed: []Point{PointPreTool}, Fn: func(context.Context, Point, Payload) (HookResult, error) {
		return Mutate(PostLLM(0, "oops", 0, 0, 0, 0)), nil
	}}
	r := &LifecycleRunner{hooks: []HookScript{wrong}, Logger: &log, FailClosed: true}
	res, _ := r.Fire(context.Background(), PointPreTool, PreTool(0, "x", "exec", `{"a":1}`))
	if res.Decision != DecisionDeny {
		t.Fatalf("fail-closed: invalid mutation must Deny, got %v", res.Decision)
	}
	if !strings.Contains(log.String(), "fail-closed") {
		t.Fatalf("expected fail-closed log on invalid mutation, got %q", log.String())
	}
}

// TestLifecycleRunner_FailClosed_HealthyHooksUnaffected: fail-closed only
// changes the *fault* path. A hook that runs cleanly (Continue / Mutate)
// behaves identically to fail-open.
func TestLifecycleRunner_FailClosed_HealthyHooksUnaffected(t *testing.T) {
	r := NewLifecycleRunner(rewriteArgs("rw", `{"path":"safe"}`))
	r.FailClosed = true
	res, out := r.Fire(context.Background(), PointPreTool, PreTool(0, "fs__read", "non-mutating", `{"path":"secret"}`))
	if res.Decision != DecisionMutate {
		t.Fatalf("fail-closed must not disturb a healthy mutate, got %v", res.Decision)
	}
	if pt, ok := out.(*PreToolPayload); !ok || pt.Args != `{"path":"safe"}` {
		t.Fatalf("healthy mutate result wrong under fail-closed: %#v", out)
	}
}

func TestLifecycleRunner_PointSubscriptionFiltering(t *testing.T) {
	var ran []string
	preOnly := BuiltinHook{HookName: "pre-only", Subscribed: []Point{PointPreTool}, Fn: func(context.Context, Point, Payload) (HookResult, error) {
		ran = append(ran, "pre-only")
		return Continue(), nil
	}}
	r := NewLifecycleRunner(preOnly)
	// Firing at post_llm must NOT invoke a pre_tool-only hook.
	if r.HasPoint(PointPostLLM) {
		t.Fatalf("HasPoint(post_llm) true for a pre_tool-only hook")
	}
	r.Fire(context.Background(), PointPostLLM, PostLLM(0, "hi", 0, 0, 0, 0))
	if len(ran) != 0 {
		t.Fatalf("pre_tool-only hook fired at post_llm: %v", ran)
	}
	if !r.HasPoint(PointPreTool) {
		t.Fatalf("HasPoint(pre_tool) should be true")
	}
}

// TestLifecycleRunner_Append: Append adds hooks after the existing set
// (preserving order), and works on a nil receiver — the case where
// BuildLifecycleRunner returned nil (no Lua hooks) but a built-in still
// needs registering.
func TestLifecycleRunner_Append(t *testing.T) {
	var ran []string

	// nil receiver → Append constructs a fresh runner carrying just the
	// appended hook.
	var nilRunner *LifecycleRunner
	r := nilRunner.Append(recordOrder("builtin", &ran))
	if r == nil {
		t.Fatal("Append on nil receiver returned nil")
	}
	r.Fire(context.Background(), PointPreTool, PreTool(0, "x", "exec", "{}"))
	if strings.Join(ran, ",") != "builtin" {
		t.Fatalf("nil-receiver Append did not run the appended hook: %v", ran)
	}

	// Non-nil receiver → appended hooks run AFTER the existing ones.
	ran = nil
	r2 := NewLifecycleRunner(recordOrder("lua", &ran)).Append(recordOrder("builtin", &ran))
	r2.Fire(context.Background(), PointPreTool, PreTool(0, "x", "exec", "{}"))
	if strings.Join(ran, ",") != "lua,builtin" {
		t.Fatalf("Append order wrong (built-ins must run last): %v", ran)
	}

	// Empty Append is a no-op (returns the same runner unchanged).
	if got := r2.Append(); got != r2 {
		t.Fatal("empty Append should return the receiver unchanged")
	}
}

// mutateResult is a post_tool hook that rewrites the result, used to test
// last-winning-mutator attribution.
func mutateResult(name, newResult string) BuiltinHook {
	return BuiltinHook{HookName: name, Subscribed: []Point{PointPostTool}, Fn: func(_ context.Context, _ Point, p Payload) (HookResult, error) {
		clone := *p.(*PostToolPayload)
		clone.Result = newResult
		return Mutate(&clone), nil
	}}
}

// TestLifecycleRunner_Deny_AttributesDecidingHook (STAGE 2): the winning
// Deny carries the deciding hook's Name() so the audit trace can record
// Denied-By-Hook. A Continue hook before it must NOT be credited.
func TestLifecycleRunner_Deny_AttributesDecidingHook(t *testing.T) {
	var ran []string
	r := NewLifecycleRunner(
		recordOrder("observer", &ran),
		denyOnTool("the-gate", "shell__bash", "bash blocked"),
		denyOnTool("never-reached", "shell__bash", "second deny"),
	)
	res, _ := r.Fire(context.Background(), PointPreTool, PreTool(0, "shell__bash", "exec", "{}"))
	if res.Decision != DecisionDeny {
		t.Fatalf("expected Deny, got %v", res.Decision)
	}
	if res.HookName != "the-gate" {
		t.Fatalf("deny attribution wrong: got %q, want %q", res.HookName, "the-gate")
	}
	if res.Reason != "bash blocked" {
		t.Fatalf("deny reason from the wrong hook: %q", res.Reason)
	}
}

// TestLifecycleRunner_Mutate_AttributesLastWinningHook (STAGE 2): when
// multiple hooks mutate, the LAST mutation is the one the action runs
// with, so the aggregate result attributes the LAST mutator.
func TestLifecycleRunner_Mutate_AttributesLastWinningHook(t *testing.T) {
	r := NewLifecycleRunner(
		mutateResult("first-redactor", "v1"),
		mutateResult("second-redactor", "v2"),
	)
	res, out := r.Fire(context.Background(), PointPostTool, PostTool(0, "stubread", "non-mutating", "{}", "raw", ""))
	if res.Decision != DecisionMutate {
		t.Fatalf("expected Mutate, got %v", res.Decision)
	}
	if res.HookName != "second-redactor" {
		t.Fatalf("mutate attribution must be the LAST winning hook: got %q, want %q", res.HookName, "second-redactor")
	}
	if pt := out.(*PostToolPayload); pt.Result != "v2" {
		t.Fatalf("final payload should reflect the last mutation: %q", pt.Result)
	}
}

// TestLifecycleRunner_FailClosed_AttributesFaultingHook (STAGE 2): a
// fail-closed deny synthesized from a hook error/invalid-mutation also
// attributes the faulting hook.
func TestLifecycleRunner_FailClosed_AttributesFaultingHook(t *testing.T) {
	boom := BuiltinHook{HookName: "broken-policy", Subscribed: []Point{PointPreTool}, Fn: func(context.Context, Point, Payload) (HookResult, error) {
		return Continue(), errors.New("script fault")
	}}
	r := &LifecycleRunner{hooks: []HookScript{boom}, FailClosed: true}
	res, _ := r.Fire(context.Background(), PointPreTool, PreTool(0, "x", "exec", "{}"))
	if res.Decision != DecisionDeny {
		t.Fatalf("fail-closed should deny on hook error, got %v", res.Decision)
	}
	if res.HookName != "broken-policy" {
		t.Fatalf("fail-closed deny must attribute the faulting hook: got %q", res.HookName)
	}
}
