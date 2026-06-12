package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/foobarto/stado/internal/audit"
	"github.com/foobarto/stado/internal/hooks"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/pkg/tool"
)

// emptyTreeHash is git's well-known empty tree object id. A SHA-only trace
// commit (the 99% no-mutation path, and the oversized-blob fallback) points at
// it; a blob-backed commit does not.
const emptyTreeHash = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// redactHook returns a post_tool MUTATE hook that replaces `secret` with
// `[REDACTED]` in the tool result.
func redactHook(name string) hooks.BuiltinHook {
	return hooks.BuiltinHook{
		HookName:   name,
		Subscribed: []hooks.Point{hooks.PointPostTool},
		Fn: func(_ context.Context, _ hooks.Point, p hooks.Payload) (hooks.HookResult, error) {
			clone := *p.(*hooks.PostToolPayload)
			clone.Result = strings.ReplaceAll(clone.Result, "secret", "[REDACTED]")
			return hooks.Mutate(&clone), nil
		},
	}
}

// TestExecutor_PostToolMutate_BlobBytesRecoverable is the STAGE 5 core
// acceptance test: BOTH the original and the mutated result bytes are
// recoverable as git blobs from the two-commit mutation trace chain — not just
// their SHAs.
func TestExecutor_PostToolMutate_BlobBytesRecoverable(t *testing.T) {
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
	ex.Hooks = hooks.NewLifecycleRunner(redactHook("redact"))

	res, err := ex.Run(context.Background(), "stubread", json.RawMessage(`{"path":"x"}`), stubHost{workdir: wt})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Content != mutated {
		t.Fatalf("model-facing result should be mutated: %q", res.Content)
	}

	chain := walkTraceChain(t, sess, sess.Sidecar.Repo().Storer)
	if len(chain) < 2 {
		t.Fatalf("expected >=2 trace commits, got %d", len(chain))
	}
	mutationCommit := chain[0]
	originalCommit := chain[1]

	// Both provenance commits must be blob-backed (NOT empty-tree).
	if mutationCommit.TreeHash.String() == emptyTreeHash {
		t.Errorf("mutation commit should be blob-backed, got empty tree")
	}
	if originalCommit.TreeHash.String() == emptyTreeHash {
		t.Errorf("original commit should be blob-backed, got empty tree")
	}

	// The original bytes are recoverable from the original-result commit.
	origBytes, present, err := sess.ReadTraceResultBlob(originalCommit.Hash)
	if err != nil {
		t.Fatalf("read original blob: %v", err)
	}
	if !present {
		t.Fatal("original-result commit should carry a recoverable result blob")
	}
	if string(origBytes) != original {
		t.Errorf("recovered original = %q, want %q", origBytes, original)
	}

	// The mutated bytes are recoverable from the mutation commit.
	mutBytes, present, err := sess.ReadTraceResultBlob(mutationCommit.Hash)
	if err != nil {
		t.Fatalf("read mutated blob: %v", err)
	}
	if !present {
		t.Fatal("mutation commit should carry a recoverable result blob")
	}
	if string(mutBytes) != mutated {
		t.Errorf("recovered mutated = %q, want %q", mutBytes, mutated)
	}

	// Sanity: the recovered original hashes to the Original-Result-SHA the
	// mutation commit records — proving the blob is the audited bytes.
	_, mutTrailers := audit.ParseMessage(mutationCommit.Message)
	if got := mutTrailers["Original-Result-SHA"]; got != sha256OfString(string(origBytes)) {
		t.Errorf("Original-Result-SHA = %q, recovered-blob SHA = %q", got, sha256OfString(string(origBytes)))
	}
}

// TestExecutor_PostToolMutate_OversizedFallsBackToSHAOnly is the STAGE 5
// overflow case: a mutated result larger than the blob cap is NOT stored as a
// blob — the commit falls back to empty-tree (SHA-only) and the Error trailer
// notes the drop. The mutation digest + provenance trailers still verify.
func TestExecutor_PostToolMutate_OversizedFallsBackToSHAOnly(t *testing.T) {
	ex, sess, wt := newExecutorFixture(t)
	// Build a result comfortably over the 4 MiB cap. The hook rewrites a
	// marker so the original differs from the mutated bytes.
	big := strings.Repeat("A", int(stadogit.MaxTraceResultBlobBytes())+1024) + "secret"
	ex.Registry.Register(stubTool{
		name:  "stubread",
		class: tool.ClassNonMutating,
		effect: func(string) (tool.Result, error) {
			return tool.Result{Content: big}, nil
		},
	})
	ex.Hooks = hooks.NewLifecycleRunner(redactHook("redact"))

	if _, err := ex.Run(context.Background(), "stubread", json.RawMessage(`{"path":"x"}`), stubHost{workdir: wt}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	chain := walkTraceChain(t, sess, sess.Sidecar.Repo().Storer)
	if len(chain) < 2 {
		t.Fatalf("expected >=2 trace commits, got %d", len(chain))
	}
	mutationCommit := chain[0]
	originalCommit := chain[1]

	// Both oversized commits fall back to SHA-only (empty tree) — graceful,
	// no error returned to the caller.
	if mutationCommit.TreeHash.String() != emptyTreeHash {
		t.Errorf("oversized mutation commit should fall back to empty tree, got %s", mutationCommit.TreeHash)
	}
	if originalCommit.TreeHash.String() != emptyTreeHash {
		t.Errorf("oversized original commit should fall back to empty tree, got %s", originalCommit.TreeHash)
	}

	// No recoverable bytes, but not an error — present=false.
	if _, present, err := sess.ReadTraceResultBlob(mutationCommit.Hash); err != nil || present {
		t.Errorf("oversized fallback: ReadTraceResultBlob present=%v err=%v, want false,nil", present, err)
	}

	// The drop is noted in the SIGNED chain (Error trailer) so an auditor
	// sees the bytes were not stored. SHA still present.
	_, mutTrailers := audit.ParseMessage(mutationCommit.Message)
	if !strings.Contains(mutTrailers["Error"], "SHA-only fallback") {
		t.Errorf("mutation Error trailer should note the blob fallback, got %q", mutTrailers["Error"])
	}
	if mutTrailers["Result-SHA"] == "" {
		t.Errorf("mutation Result-SHA must still be present on the SHA-only fallback")
	}
	if mutTrailers["Original-Result-SHA"] == "" {
		t.Errorf("mutation Original-Result-SHA must still be present on the SHA-only fallback")
	}
	_, origTrailers := audit.ParseMessage(originalCommit.Message)
	if !strings.Contains(origTrailers["Error"], "SHA-only fallback") {
		t.Errorf("original Error trailer should note the blob fallback, got %q", origTrailers["Error"])
	}
}

// TestExecutor_NoMutation_TraceStaysEmptyTree is the STAGE 5 invariant: a
// normal (no post_tool mutation, no deny) tool call keeps the cheap empty-tree
// trace commit — blob-backing must NOT bloat the 99% path.
func TestExecutor_NoMutation_TraceStaysEmptyTree(t *testing.T) {
	ex, sess, wt := newExecutorFixture(t)
	ex.Registry.Register(stubTool{
		name:  "stubread",
		class: tool.ClassNonMutating,
		effect: func(string) (tool.Result, error) {
			return tool.Result{Content: "plain result, no hook"}, nil
		},
	})

	if _, err := ex.Run(context.Background(), "stubread", json.RawMessage(`{"path":"x"}`), stubHost{workdir: wt}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	head, err := sess.TraceHead()
	if err != nil || head.IsZero() {
		t.Fatalf("trace head: %v %s", err, head)
	}
	commit, err := object.GetCommit(sess.Sidecar.Repo().Storer, head)
	if err != nil {
		t.Fatalf("get commit %s: %v", head, err)
	}
	if commit.TreeHash.String() != emptyTreeHash {
		t.Errorf("no-mutation trace commit should be empty-tree, got %s", commit.TreeHash)
	}
	if _, present, err := sess.ReadTraceResultBlob(head); err != nil || present {
		t.Errorf("no-mutation commit should have no result blob: present=%v err=%v", present, err)
	}
}
