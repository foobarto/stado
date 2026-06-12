package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/hooks"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/tool"
)

// realDataStubTool is a minimal non-mutating tool used to drive the real
// Executor.Run path so a real post_tool mutate / pre_tool deny hook leaves
// genuine provenance trace commits behind (not the synthetic CommitToTrace
// the other provenance tests hand-build).
type realDataStubTool struct {
	name    string
	class   tool.Class
	content string
}

func (s realDataStubTool) Name() string           { return s.name }
func (s realDataStubTool) Description() string    { return "real-data stub" }
func (s realDataStubTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (s realDataStubTool) Class() tool.Class      { return s.class }
func (s realDataStubTool) Run(_ context.Context, _ json.RawMessage, _ tool.Host) (tool.Result, error) {
	return tool.Result{Content: s.content}, nil
}

// realDataHost satisfies tool.Host with a fixed workdir == the session
// worktree (so the executor's live-cwd branch stays off and it audits the
// session worktree, mirroring an isolated/headless session).
type realDataHost struct {
	tools.NullHost
	workdir string
}

func (h realDataHost) Workdir() string { return h.workdir }

// TestBuildForest_RealHookProvenance_TurnBadge drives the ACTUAL executor +
// hook chain (a real post_tool mutate hook + a real pre_tool deny hook) to
// produce real provenance trace commits, then closes the turn the way the
// agent loop does (Session.NextTurn) and runs BuildForest. It asserts the
// /tree `⟳N` / `⊘N` badge counts surface on BOTH the session totals AND the
// turn the operator actually sees in the tree.
//
// This is the never-exercised real-data path: every prior provenance test
// hand-stamped CommitMeta.Turn to MATCH the turn-tag numbering, hiding the
// fact that in production a tool runs at Session.Turn() (0 for the first
// turn) while the turn boundary tag that closes it is turns/1. The per-turn
// badge join in BuildForest keys provenance turn N against TurnNode.Turn ==
// N, so the first turn's mutations/denies land in bucket 0 with no matching
// turns/0 tag — the turn-1 row shows no badge even though the operator's
// "turn 1" is exactly where the hook fired.
func TestBuildForest_RealHookProvenance_TurnBadge(t *testing.T) {
	cfg, sc, _ := forestEnv(t)

	// Create the session the way production does (CreateSession), pinned so
	// ListRepoWorktreeSessionIDs keeps it in scope.
	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), "realprov", plumbing.ZeroHash)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	_ = WriteUserRepoPin(sess.WorktreePath, sc.UserRepoRoot)

	reg := tools.NewRegistry()
	reg.Register(realDataStubTool{name: "redactme", class: tool.ClassNonMutating, content: "raw secret value"})
	reg.Register(realDataStubTool{name: "blockme", class: tool.ClassNonMutating, content: "should not run"})

	ex := &tools.Executor{
		Registry: reg,
		Session:  sess,
		Agent:    "test-agent",
		Model:    "test-model",
		Hooks: hooks.NewLifecycleRunner(
			hooks.BuiltinHook{
				HookName:   "redact",
				Subscribed: []hooks.Point{hooks.PointPostTool},
				Fn: func(_ context.Context, _ hooks.Point, p hooks.Payload) (hooks.HookResult, error) {
					clone := *p.(*hooks.PostToolPayload)
					if !strings.Contains(clone.Result, "secret") {
						return hooks.Continue(), nil
					}
					clone.Result = strings.ReplaceAll(clone.Result, "secret", "[REDACTED]")
					return hooks.Mutate(&clone), nil
				},
			},
			hooks.BuiltinHook{
				HookName:   "guard",
				Subscribed: []hooks.Point{hooks.PointPreTool},
				Fn: func(_ context.Context, _ hooks.Point, p hooks.Payload) (hooks.HookResult, error) {
					if p.(*hooks.PreToolPayload).Tool == "blockme" {
						return hooks.Deny("blocked by policy"), nil
					}
					return hooks.Continue(), nil
				},
			},
		),
	}
	host := realDataHost{workdir: sess.WorktreePath}

	// --- Operator's "turn 1": one real mutation + one real deny. ---
	// Session.Turn() is 0 here (a fresh session), exactly as in the agent
	// loop's first iteration, so these provenance commits get Turn: 0.
	if _, err := ex.Run(context.Background(), "redactme", json.RawMessage(`{"x":1}`), host); err != nil {
		t.Fatalf("run redactme: %v", err)
	}
	if _, err := ex.Run(context.Background(), "blockme", json.RawMessage(`{"x":1}`), host); err != nil {
		t.Fatalf("run blockme: %v", err)
	}
	// The agent loop closes a turn (creates the turns/N tag) when the model
	// stops emitting tool calls. This is turns/1.
	if err := sess.NextTurn(); err != nil {
		t.Fatalf("NextTurn 1: %v", err)
	}

	// --- Operator's "turn 2": one more real mutation. ---
	// Session.Turn() is 1 now, so this provenance commit gets Turn: 1, while
	// the boundary tag that closes it is turns/2.
	if _, err := ex.Run(context.Background(), "redactme", json.RawMessage(`{"x":2}`), host); err != nil {
		t.Fatalf("run redactme turn2: %v", err)
	}
	if err := sess.NextTurn(); err != nil {
		t.Fatalf("NextTurn 2: %v", err)
	}

	f, err := BuildForest(sc, cfg.WorktreeDir(), "")
	if err != nil {
		t.Fatalf("BuildForest: %v", err)
	}
	node := f.Sessions["realprov"]
	if node == nil {
		t.Fatalf("session 'realprov' missing from forest")
	}

	// Session totals are the ground truth: 2 mutations + 1 deny across the
	// whole session. These sum every provenance bucket regardless of whether
	// it matched a turn tag, so they're correct either way.
	if node.MutatedTotal != 2 {
		t.Errorf("session MutatedTotal = %d, want 2", node.MutatedTotal)
	}
	if node.DeniedTotal != 1 {
		t.Errorf("session DeniedTotal = %d, want 1", node.DeniedTotal)
	}

	// The forest exposes two turn rows: turns/1 and turns/2.
	byTurn := map[int]*TurnNode{}
	for _, tn := range node.Turns {
		byTurn[tn.Entry.Turn] = tn
	}

	// The per-turn badge must agree with the session total: every mutation /
	// deny the operator can see in the session total must show up on SOME
	// turn row, not vanish into an unrenderable turn-0 bucket. Sum the
	// rendered per-turn counts and compare with the session totals.
	var sumMut, sumDen int
	for _, tn := range node.Turns {
		sumMut += tn.MutatedCount
		sumDen += tn.DeniedCount
	}
	if sumMut != node.MutatedTotal {
		t.Errorf("per-turn mutated counts sum to %d but session total is %d — provenance was attributed to a turn with no row (operator sees a badge on the session but not on any turn)", sumMut, node.MutatedTotal)
	}
	if sumDen != node.DeniedTotal {
		t.Errorf("per-turn denied counts sum to %d but session total is %d — deny provenance vanished from every turn row", sumDen, node.DeniedTotal)
	}

	// And concretely: the operator's first turn (turns/1) is exactly where
	// the mutation + deny fired, so its row must carry ⟳1 ⊘1.
	if t1 := byTurn[1]; t1 == nil {
		t.Fatal("turn 1 row missing")
	} else {
		if t1.MutatedCount != 1 {
			t.Errorf("turn 1 MutatedCount = %d, want 1 (the redactme mutation fired in the operator's first turn)", t1.MutatedCount)
		}
		if t1.DeniedCount != 1 {
			t.Errorf("turn 1 DeniedCount = %d, want 1 (the blockme deny fired in the operator's first turn)", t1.DeniedCount)
		}
	}
	if t2 := byTurn[2]; t2 == nil {
		t.Fatal("turn 2 row missing")
	} else if t2.MutatedCount != 1 {
		t.Errorf("turn 2 MutatedCount = %d, want 1 (the second redactme mutation)", t2.MutatedCount)
	}
}
