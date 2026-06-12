package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"

	"github.com/foobarto/stado/internal/audit"
	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/pkg/tool"
)

// sha256OfString mirrors executor.sha256Of for an arbitrary string so the
// test can compute the expected SHA of the ORIGINAL (pre-mutation) result
// without reaching into the post-mutation res.
func sha256OfString(s string) string {
	if len(s) == 0 {
		return ""
	}
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// walkTraceChain returns the trace commits newest→oldest starting from the
// trace ref head. Used to assert the two-commit mutation provenance model:
// the mutation commit parents the original-result commit.
func walkTraceChain(t *testing.T, sess interface {
	TraceHead() (plumbing.Hash, error)
}, st storer.EncodedObjectStorer) []*object.Commit {
	t.Helper()
	head, err := sess.TraceHead()
	if err != nil {
		t.Fatalf("TraceHead: %v", err)
	}
	var chain []*object.Commit
	h := head
	for !h.IsZero() {
		c, err := object.GetCommit(st, h)
		if err != nil {
			t.Fatalf("GetCommit %s: %v", h, err)
		}
		chain = append(chain, c)
		if len(c.ParentHashes) == 0 {
			break
		}
		h = c.ParentHashes[0]
	}
	return chain
}

// TestExecutor_PostToolMutate_OriginalSHARecoverable is the STAGE 0
// reproduce test (bugfix posture: reproduce first). A post_tool hook
// mutates a tool result; the original result's SHA must be recoverable
// from the signed trace chain.
//
// Pre-fix (v0.63.0) this FAILS: the single trace commit hashes the
// MUTATED bytes into Result-SHA and the original is lost. After STAGE 4
// the executor writes TWO linked trace commits — an original-result
// commit (Result-SHA = original) then a mutation commit (Result-SHA =
// mutated, Original-Result-SHA = original, Mutated-By-Hook = the
// deciding hook) parenting it — so the original SHA is recoverable.
func TestExecutor_PostToolMutate_OriginalSHARecoverable(t *testing.T) {
	ex, sess, wt := newExecutorFixture(t)
	const original = "raw secret value"
	const mutated = "raw [REDACTED] value"
	ex.Registry.Register(stubTool{
		name:  "stubread",
		class: tool.ClassNonMutating,
		effect: func(string) (tool.Result, error) {
			return tool.Result{Content: original}, nil
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
	// The model-facing result stays the MUTATED bytes (unchanged contract).
	if res.Content != mutated {
		t.Fatalf("model-facing result should be mutated: %q", res.Content)
	}

	wantOriginalSHA := sha256OfString(original)
	wantMutatedSHA := sha256OfString(mutated)

	chain := walkTraceChain(t, sess, sess.Sidecar.Repo().Storer)
	if len(chain) < 2 {
		t.Fatalf("expected at least 2 trace commits (original + mutation), got %d", len(chain))
	}

	// chain[0] is the newest: the mutation commit. chain[1] is its
	// parent: the original-result commit.
	mutationCommit := chain[0]
	originalCommit := chain[1]

	_, mutTrailers := audit.ParseMessage(mutationCommit.Message)
	_, origTrailers := audit.ParseMessage(originalCommit.Message)

	// The mutation commit carries the mutated Result-SHA as canonical and
	// records the original SHA + the mutating hook's identity.
	if got := mutTrailers["Result-SHA"]; got != wantMutatedSHA {
		t.Errorf("mutation commit Result-SHA = %q, want mutated %q", got, wantMutatedSHA)
	}
	if got := mutTrailers["Original-Result-SHA"]; got != wantOriginalSHA {
		t.Errorf("mutation commit Original-Result-SHA = %q, want %q", got, wantOriginalSHA)
	}
	if got := mutTrailers["Mutated-By-Hook"]; got != "redact" {
		t.Errorf("mutation commit Mutated-By-Hook = %q, want %q", got, "redact")
	}

	// The original-result commit preserves the ORIGINAL SHA and carries
	// NO mutation trailers — it's the untouched provenance entry.
	if got := origTrailers["Result-SHA"]; got != wantOriginalSHA {
		t.Errorf("original commit Result-SHA = %q, want original %q", got, wantOriginalSHA)
	}
	if origTrailers["Original-Result-SHA"] != "" {
		t.Errorf("original-result commit must NOT carry Original-Result-SHA, got %q", origTrailers["Original-Result-SHA"])
	}
	if origTrailers["Mutated-By-Hook"] != "" {
		t.Errorf("original-result commit must NOT carry Mutated-By-Hook, got %q", origTrailers["Mutated-By-Hook"])
	}

	// The whole point: the original result's SHA is recoverable from the
	// signed trace chain even though the model only ever saw the mutated
	// bytes.
	if wantOriginalSHA == wantMutatedSHA {
		t.Fatal("precondition: original and mutated SHAs must differ")
	}
}
