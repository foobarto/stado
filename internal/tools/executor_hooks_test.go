package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/foobarto/stado/internal/audit"
	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/pkg/tool"
)

// TestExecutor_PreToolDeny_BlocksTool: a pre_tool hook that denies must
// prevent the tool from running and surface the reason as an errored
// result. The tool's effect closure must NOT fire. STAGE 3: the denial
// must ALSO write exactly one signed trace commit carrying the deny
// trailers so denials are auditable, not invisible.
func TestExecutor_PreToolDeny_BlocksTool(t *testing.T) {
	ex, sess, wt := newExecutorFixture(t)
	ran := false
	ex.Registry.Register(stubTool{
		name:  "stubexec",
		class: tool.ClassExec,
		effect: func(string) (tool.Result, error) {
			ran = true
			return tool.Result{Content: "should not run"}, nil
		},
	})
	ex.Hooks = hooks.NewLifecycleRunner(hooks.BuiltinHook{
		HookName:   "deny-exec",
		Subscribed: []hooks.Point{hooks.PointPreTool},
		Fn: func(_ context.Context, _ hooks.Point, p hooks.Payload) (hooks.HookResult, error) {
			pt := p.(*hooks.PreToolPayload)
			if pt.Tool == "stubexec" {
				return hooks.Deny("exec blocked by policy"), nil
			}
			return hooks.Continue(), nil
		},
	})

	res, err := ex.Run(context.Background(), "stubexec", json.RawMessage(`{"command":"id"}`), stubHost{workdir: wt})
	if err != nil {
		t.Fatalf("Run returned a Go error; deny should surface as a result error, not a Go error: %v", err)
	}
	if ran {
		t.Fatalf("tool effect ran despite a pre_tool deny")
	}
	if res.Error == "" || !strings.Contains(res.Error, "exec blocked by policy") {
		t.Fatalf("deny reason not surfaced in result error: %q", res.Error)
	}
	if res.Content != "" {
		t.Fatalf("denied tool should have empty content, got %q", res.Content)
	}

	// STAGE 3: exactly one trace commit, carrying the deny provenance.
	chain := walkTraceChain(t, sess, sess.Sidecar.Repo().Storer)
	if len(chain) != 1 {
		t.Fatalf("denied call must write exactly one trace commit, got %d", len(chain))
	}
	denyCommit := chain[0]
	if len(denyCommit.ParentHashes) != 0 {
		t.Errorf("the single deny commit should have no parent, got %d", len(denyCommit.ParentHashes))
	}
	title, trailers := audit.ParseMessage(denyCommit.Message)
	if !strings.Contains(title, "exec [denied]") {
		t.Errorf("deny commit title should read `exec [denied]`, got %q", title)
	}
	if got := trailers["Deny-Reason"]; got != "exec blocked by policy" {
		t.Errorf("Deny-Reason trailer = %q", got)
	}
	if got := trailers["Denied-By-Hook"]; got != "deny-exec" {
		t.Errorf("Denied-By-Hook trailer = %q, want %q", got, "deny-exec")
	}
	if got := trailers["Tool"]; got != "stubexec" {
		t.Errorf("deny commit Tool trailer = %q", got)
	}
	if trailers["Error"] == "" {
		t.Errorf("deny commit should carry the surfaced denial string as Error")
	}
	// The commit must be signed when a signer is configured; the fixture
	// has none, so just assert the message is the canonical rendered form
	// (the signing path is exercised by the audit package's E2E test).
	if _, err := object.GetCommit(sess.Sidecar.Repo().Storer, chain[0].Hash); err != nil {
		t.Fatalf("deny commit not retrievable: %v", err)
	}
}

// TestExecutor_PreToolMutate_RewritesArgs: a pre_tool hook that mutates
// the args must cause the tool to run with the rewritten args.
func TestExecutor_PreToolMutate_RewritesArgs(t *testing.T) {
	ex, _, wt := newExecutorFixture(t)
	var seenArgs json.RawMessage
	ex.Registry.Register(argEchoTool{name: "stubargs", seen: &seenArgs})

	ex.Hooks = hooks.NewLifecycleRunner(hooks.BuiltinHook{
		HookName:   "rewrite",
		Subscribed: []hooks.Point{hooks.PointPreTool},
		Fn: func(_ context.Context, _ hooks.Point, p hooks.Payload) (hooks.HookResult, error) {
			clone := *p.(*hooks.PreToolPayload)
			clone.Args = `{"path":"rewritten"}`
			return hooks.Mutate(&clone), nil
		},
	})

	res, err := ex.Run(context.Background(), "stubargs", json.RawMessage(`{"path":"original"}`), stubHost{workdir: wt})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(seenArgs) != `{"path":"rewritten"}` {
		t.Fatalf("tool did not receive rewritten args: %q", seenArgs)
	}
	// The echo tool reports the args it ran with in its content.
	if !strings.Contains(res.Content, "rewritten") {
		t.Fatalf("result should reflect the mutated args: %q", res.Content)
	}
}

// TestExecutor_PostToolMutate_RewritesResult: a post_tool hook that
// mutates the result must change what the model sees, and the rewritten
// bytes are what flow back from Run.
func TestExecutor_PostToolMutate_RewritesResult(t *testing.T) {
	ex, _, wt := newExecutorFixture(t)
	ex.Registry.Register(stubTool{
		name:  "stubread",
		class: tool.ClassNonMutating,
		effect: func(string) (tool.Result, error) {
			return tool.Result{Content: "raw secret value"}, nil
		},
	})
	ex.Hooks = hooks.NewLifecycleRunner(hooks.BuiltinHook{
		HookName:   "redact",
		Subscribed: []hooks.Point{hooks.PointPostTool},
		Fn: func(_ context.Context, _ hooks.Point, p hooks.Payload) (hooks.HookResult, error) {
			clone := *p.(*hooks.PostToolPayload)
			clone.Result = strings.ReplaceAll(clone.Result, "secret", "[REDACTED]")
			return hooks.Mutate(&clone), nil
		},
	})

	res, err := ex.Run(context.Background(), "stubread", json.RawMessage(`{"path":"x"}`), stubHost{workdir: wt})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Content != "raw [REDACTED] value" {
		t.Fatalf("post_tool mutate not applied to result: %q", res.Content)
	}
}

// TestExecutor_NoHooks_Unaffected: with no Hooks runner, Run behaves
// exactly as before (the seam is a no-op).
func TestExecutor_NoHooks_Unaffected(t *testing.T) {
	ex, _, wt := newExecutorFixture(t)
	ex.Registry.Register(stubTool{
		name:  "stubread",
		class: tool.ClassNonMutating,
		effect: func(string) (tool.Result, error) {
			return tool.Result{Content: "untouched"}, nil
		},
	})
	// ex.Hooks is nil.
	res, err := ex.Run(context.Background(), "stubread", json.RawMessage(`{"path":"x"}`), stubHost{workdir: wt})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Content != "untouched" {
		t.Fatalf("nil hooks runner altered the result: %q", res.Content)
	}
}

// argEchoTool records the args it was invoked with and echoes them back
// in its content, so a test can assert which args reached the tool.
type argEchoTool struct {
	name string
	seen *json.RawMessage
}

func (a argEchoTool) Name() string           { return a.name }
func (a argEchoTool) Description() string    { return "echo args" }
func (a argEchoTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (a argEchoTool) Class() tool.Class      { return tool.ClassNonMutating }
func (a argEchoTool) Run(_ context.Context, args json.RawMessage, _ tool.Host) (tool.Result, error) {
	if a.seen != nil {
		cp := make(json.RawMessage, len(args))
		copy(cp, args)
		*a.seen = cp
	}
	return tool.Result{Content: "ran with " + string(args)}, nil
}
