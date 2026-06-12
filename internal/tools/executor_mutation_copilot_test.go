package tools

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"

	"github.com/foobarto/stado/internal/audit"
	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/pkg/tool"
)

// TestExecutor_PostToolMutate_EmptyOriginal_LinkValidated reproduces the
// Copilot finding on PR #125: when a post_tool hook mutates a result whose
// ORIGINAL content was empty (e.g. injects content where the tool produced
// none), originalResultSHA = sha256Of("") = "", so the mutation commit's
// Original-Result-SHA trailer is omitted. verify's mutation-link guard
// required BOTH Mutated-By-Hook AND Original-Result-SHA to be present, so the
// link was silently skipped — the mutation was recorded (Mutated-By-Hook is
// on the commit, the badge + session logs count it) but `audit verify` never
// validated the original->mutated linkage, weakening the "mutation is signed
// evidence" guarantee for this case.
//
// Pre-fix this FAILS: verify records ZERO mutation links for the empty-origin
// pair. After relaxing the guard to key on Mutated-By-Hook alone (matching
// the badge + logs detection), verify records exactly one INTACT link — the
// empty original Result-SHA == empty Original-Result-SHA is a valid match,
// and tamper detection is unaffected (a non-empty parent mismatch still
// breaks).
func TestExecutor_PostToolMutate_EmptyOriginal_LinkValidated(t *testing.T) {
	ex, sess, wt := newExecutorFixture(t)
	ex.Registry.Register(stubTool{
		name:  "stubempty",
		class: tool.ClassNonMutating,
		effect: func(string) (tool.Result, error) {
			return tool.Result{Content: ""}, nil // empty original result
		},
	})
	ex.Hooks = hooks.NewLifecycleRunner(hooks.BuiltinHook{
		HookName:   "inject",
		Subscribed: []hooks.Point{hooks.PointPostTool},
		Fn: func(_ context.Context, _ hooks.Point, p hooks.Payload) (hooks.HookResult, error) {
			clone := *p.(*hooks.PostToolPayload)
			clone.Result = "injected by hook" // empty -> non-empty
			return hooks.Mutate(&clone), nil
		},
	})

	res, err := ex.Run(context.Background(), "stubempty", json.RawMessage(`{"path":"x"}`), stubHost{workdir: wt})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Content != "injected by hook" {
		t.Fatalf("model-facing result should be mutated, got %q", res.Content)
	}

	traceHead, err := sess.TraceHead()
	if err != nil || traceHead.IsZero() {
		t.Fatalf("trace ref must be set: %v head=%s", err, traceHead)
	}

	// verify's mutation-link validator must RECORD this mutation (keyed on
	// Mutated-By-Hook, which is present) and validate it as INTACT — the
	// empty original SHA legitimately matches the empty parent Result-SHA.
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	walkRes, err := audit.NewWalker(sess.Sidecar.Repo().Storer, pub).Verify("trace", traceHead)
	if err != nil {
		t.Fatalf("walk trace ref: %v", err)
	}
	if len(walkRes.MutationChain) != 1 {
		t.Fatalf("expected exactly 1 recorded mutation link for the empty-origin pair, got %d", len(walkRes.MutationChain))
	}
	if walkRes.BrokenLinks() != 0 {
		t.Errorf("empty-origin mutation link must be INTACT, got %d broken", walkRes.BrokenLinks())
	}
	link := walkRes.MutationChain[0]
	if link.ByHook != "inject" {
		t.Errorf("link ByHook = %q, want %q", link.ByHook, "inject")
	}
}
