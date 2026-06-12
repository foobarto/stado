package tools

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/foobarto/stado/internal/audit"
	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/pkg/tool"
)

// TestExecutor_MutatingWithPostToolMutate_TreeRefNoBrokenLink reproduces the
// Codex P1 finding on PR #125: when a MUTATING-class tool succeeds AND a
// post_tool hook rewrites its result, the executor reused the same CommitMeta
// (with Mutated-By-Hook + Original-Result-SHA set for the trace mutation
// commit) for the TREE commit too. `audit verify` walks both the tree and
// trace refs and treats any commit carrying those two trailers as a mutation
// link, validating its first parent against Original-Result-SHA. On the tree
// ref the first parent is the PREVIOUS tree snapshot (not the original-result
// trace commit), so a legitimate, untampered mutating call was reported as
// MUTATION-LINK-BROKEN and `audit verify` exited non-zero.
//
// Pre-fix this FAILS (the tree commit carries the trailers; BrokenLinks > 0).
// The fix clears the provenance trailers before CommitToTree — the tree ref
// records file state, not tool-result provenance, so it must never carry the
// mutation-chain markers.
func TestExecutor_MutatingWithPostToolMutate_TreeRefNoBrokenLink(t *testing.T) {
	ex, sess, wt := newExecutorFixture(t)
	const original = "wrote secret file"
	ex.Registry.Register(stubTool{
		name:  "stubwrite",
		class: tool.ClassMutating,
		effect: func(workdir string) (tool.Result, error) {
			// A real mutation so BuildTreeFromDir yields a non-zero tree
			// and CommitToTree fires.
			if err := os.WriteFile(filepath.Join(workdir, "new.txt"), []byte("data"), 0o644); err != nil {
				return tool.Result{}, err
			}
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

	if _, err := ex.Run(context.Background(), "stubwrite", json.RawMessage(`{"path":"new.txt"}`), stubHost{workdir: wt}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	treeHead, err := sess.TreeHead()
	if err != nil || treeHead.IsZero() {
		t.Fatalf("tree ref must be set for a successful mutating tool: %v head=%s", err, treeHead)
	}

	// Root invariant: the tree ref records FILE STATE, not tool-result
	// provenance. A tree commit must NEVER carry the mutation-provenance
	// trailers (those belong only to the trace ref's two-commit chain).
	treeCommit, err := object.GetCommit(sess.Sidecar.Repo().Storer, treeHead)
	if err != nil {
		t.Fatalf("GetCommit tree head: %v", err)
	}
	_, trailers := audit.ParseMessage(treeCommit.Message)
	if got := trailers["Mutated-By-Hook"]; got != "" {
		t.Errorf("tree commit must NOT carry Mutated-By-Hook, got %q", got)
	}
	if got := trailers["Original-Result-SHA"]; got != "" {
		t.Errorf("tree commit must NOT carry Original-Result-SHA, got %q", got)
	}

	// End-to-end: the audit Walker over the tree ref must report ZERO broken
	// mutation links. Signature validity is irrelevant to link detection, so
	// a throwaway key suffices (the fixture leaves commits unsigned).
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	res, err := audit.NewWalker(sess.Sidecar.Repo().Storer, pub).Verify("tree", treeHead)
	if err != nil {
		t.Fatalf("walk tree ref: %v", err)
	}
	if n := res.BrokenLinks(); n > 0 {
		t.Errorf("tree ref must have zero broken mutation links, got %d", n)
	}
}
