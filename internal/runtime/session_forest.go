package runtime

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/audit"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

// Availability mirrors SessionSummary.Status as a typed enum so the
// treepicker can branch on it without string comparisons. The string
// values match SummariseSession's Status field verbatim ("live" / "idle"
// / "detached") so the two stay in lockstep.
type Availability string

const (
	// AvailLive — worktree present and the owning pid is alive.
	AvailLive Availability = "live"
	// AvailIdle — worktree present but no live owning pid.
	AvailIdle Availability = "idle"
	// AvailDetached — refs only, no worktree on disk. Peek/switch gated off.
	AvailDetached Availability = "detached"
)

// availabilityFromStatus maps a SessionSummary.Status string onto the
// typed enum. Any unexpected value collapses to detached — the safest
// default (gates peek/switch off rather than promising a worktree that
// may not exist).
func availabilityFromStatus(status string) Availability {
	switch status {
	case "live":
		return AvailLive
	case "idle":
		return AvailIdle
	default:
		return AvailDetached
	}
}

// TurnNode is one turn boundary in a session's history. Children is
// reserved for intra-session sub-turn structure; v1 leaves it empty
// (turns are a flat ascending list) but the field exists so the
// treepicker's recursive renderer has a stable shape to walk.
type TurnNode struct {
	Entry    stadogit.TurnEntry
	Children []*TurnNode

	// MutatedCount / DeniedCount aggregate the hook-mutation provenance for
	// THIS turn (spec hooks-audit-mutation-provenance STAGE 7b): how many
	// of this turn's trace commits carried a Mutated-By-Hook trailer (a
	// post_tool hook rewrote a tool result) or a Deny-Reason trailer (a
	// pre_tool hook vetoed a call). Attribution bridges the +1 convention
	// drift between the trace commit's `Turn` trailer (zero-indexed,
	// pre-increment) and this node's turn-boundary tag turns/N — see
	// stampProvenanceCounts. Drive the `⟳N` / `⊘N` /tree turn badge. Zero
	// for every turn in a session that never triggered a mutating or
	// denying hook — the common case, paid by one extra trace-ref walk.
	MutatedCount int
	DeniedCount  int
}

// SessionNode is one session in the forest. A session is either a forest
// root (fresh, or an orphan whose parent refs are gone) or a child edge
// off another session's turn (ParentID set, ParentTurn ≥ 1).
type SessionNode struct {
	ID          string
	Description string
	Avail       Availability
	IsCurrent   bool
	Turns       []*TurnNode
	Summary     SessionSummary

	// ParentID is the id of the session this one forked from, or "" when
	// this node is a forest root (fresh or orphan).
	ParentID string
	// ParentTurn is the parent's turn number this session forked at, or
	// -1 when the fork point matched no turn tag (a mid-turn fork) or the
	// session is unlinked (fresh root / orphan). The -1 sentinel lets the
	// renderer say "⑂ unlinked" instead of "⑂ turn N".
	ParentTurn int
	// Orphan is true when this session carries a real cross-session seed
	// (its tree-root has exactly one parent commit) but that seed matched
	// no live session's turn index — i.e. the parent session's refs were
	// deleted. Orphans render as forest roots with a distinct marker.
	Orphan bool
	// Depth is the BFS depth from the forest root (root == 0).
	Depth int

	// MutatedTotal / DeniedTotal aggregate the session's hook-mutation
	// provenance across ALL its trace commits (spec STAGE 7b) — the sum of
	// every turn's MutatedCount / DeniedCount PLUS any provenance commit
	// whose Turn trailer matched no turn tag (e.g. a turn-0 / pre-first-turn
	// call). Drive the session-line `⟳N` / `⊘N` badge so a collapsed session
	// still surfaces that a hook altered or vetoed something inside it.
	MutatedTotal int
	DeniedTotal  int
}

// Forest is the assembled session graph. Roots are the top-level
// SessionNodes (fresh roots + orphans); every node is also indexed in
// Sessions by id for O(1) edge lookup by the renderer. Total is the
// session count actually built; Truncated is set when enumeration hit
// the hard cap and Total is therefore a floor, not the true count.
type Forest struct {
	Roots     []*SessionNode
	Sessions  map[string]*SessionNode
	Total     int
	Truncated bool
}

// MaxForestSessions is the hard cap on sessions a single BuildForest pass
// will assemble. Above this the forest is truncated (Truncated=true) and
// the caller must surface that to the operator — a 5000-session forest is
// already unbrowsable, and the unbounded case would pin the UI thread.
const MaxForestSessions = 5000

// midTurnSentinel marks a ParentTurn that has no matching turn tag (a
// mid-turn fork) or is simply unlinked (fresh root / orphan).
const midTurnSentinel = -1

// forestRefPasses counts how many times BuildForest has iterated the
// sidecar's full reference set. The benchmark reads it to assert the
// "ONE References() pass per build" invariant — the concrete
// *git.Repository can't be spied on through the Sidecar facade, so the
// counter is the practical seam. Test-only; production never reads it.
var forestRefPasses atomic.Int64

// sessionRefBucket holds the refs found for one session id during the
// single References() pass, before any commit objects are resolved.
type sessionRefBucket struct {
	treeHead  plumbing.Hash
	hasTree   bool
	traceHead plumbing.Hash // trace-ref tip — walked once for mutation/deny provenance
	hasTrace  bool
	turnHash  map[int]plumbing.Hash // turn number -> commit hash
}

// turnRef identifies a turn within the whole forest: which session, which
// turn number. Values of the reverse turn-hash index used for O(1)
// fork-point matching.
type turnRef struct {
	sessionID string
	turn      int
}

// BuildForest assembles the session forest for one repo in a single pass
// over the sidecar's references.
//
// The expensive naive shape — a per-session ListTurnRefs loop — is ~16s at
// 159 sessions because every call re-iterates the entire ref set. Instead
// BuildForest does ONE References() pass (sc.Repo().References()),
// bucketing refs/sessions/<id>/{tree,trace,turns/N} by id, then resolves
// each session's commits from those buckets.
//
// Lineage is EXACT, not approximate. CreateSession seeds a child's tree
// ref directly AT the parent's fork-point commit; once the child commits,
// that fork point becomes an interior node on the child's first-parent
// chain (the chain keeps descending through ALL of the parent's pre-fork
// ancestry to the lineage's zero-parent root — the seed is NOT the chain
// root). The exact fork seed is therefore the youngest commit on a
// session's chain that ALSO lies on another session's chain (two sessions
// share precisely the commits at and below their fork point). A reverse
// turn-hash index (commit hash -> {sessionID, turn}) then names the turn:
// shared+turn-tagged seed -> edge; shared+untagged seed (a mid-turn fork)
// -> unlinked; no shared seed -> fresh root, unless a foreign turn-boundary
// commit survives below this session's own history -> Orphan (parent refs
// deleted).
//
// worktreeRoot is cfg.WorktreeDir(); currentID is the session the caller
// is "in" (pinned + auto-expanded first in the sort), or "" for none.
func BuildForest(sc *stadogit.Sidecar, worktreeRoot, currentID string) (*Forest, error) {
	if sc == nil {
		return &Forest{Sessions: map[string]*SessionNode{}}, nil
	}

	// Phase 1 — ONE References() pass, bucketing by session id.
	buckets, err := bucketSessionRefs(sc)
	if err != nil {
		return nil, err
	}

	// Augment with worktree-backed sessions that have no refs yet (a
	// freshly-created session whose first turn hasn't committed). Scoped
	// to this repo so other projects' worktrees don't leak in — mirrors
	// listSessionIDs in internal/tui/model_sessions.go.
	if ids, err := ListRepoWorktreeSessionIDs(worktreeRoot, sc.UserRepoRoot); err == nil {
		for _, id := range ids {
			if _, ok := buckets[id]; !ok {
				buckets[id] = &sessionRefBucket{turnHash: map[int]plumbing.Hash{}}
			}
		}
	}
	if currentID != "" {
		if _, ok := buckets[currentID]; !ok {
			buckets[currentID] = &sessionRefBucket{turnHash: map[int]plumbing.Hash{}}
		}
	}

	// Hard cap. Deterministic truncation: sort ids first so the cap drops
	// the same sessions on every build rather than a random map-order set.
	ids := make([]string, 0, len(buckets))
	for id := range buckets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	truncated := false
	if len(ids) > MaxForestSessions {
		ids = ids[:MaxForestSessions]
		truncated = true
		// Drop the now-out-of-scope buckets so later phases never see them.
		kept := make(map[string]*sessionRefBucket, len(ids))
		for _, id := range ids {
			kept[id] = buckets[id]
		}
		buckets = kept
	}

	// Phase 2 — build nodes (turns + summary), a reverse turn-hash index for
	// fork-point matching, and each session's first-parent chain.
	//
	// chainMembership maps a commit hash to the set of session ids whose
	// first-parent chain passes through it. Two sessions share exactly the
	// commits at and below their fork point, so it's the precise seam: a
	// session's fork seed is the YOUNGEST commit on its chain that is also
	// on a DIFFERENT session's chain. That holds whether or not the seed
	// carries a turn tag (the turn-index then tells us WHICH turn).
	nodes := make(map[string]*SessionNode, len(ids))
	turnIndex := make(map[plumbing.Hash]turnRef)
	chains := make(map[string][]chainCommit, len(ids))
	chainMembership := make(map[plumbing.Hash]map[string]bool)
	for _, id := range ids {
		b := buckets[id]
		node := &SessionNode{
			ID:         id,
			ParentTurn: midTurnSentinel,
		}
		// Resolve turns from the bucketed hashes FIRST, then derive the
		// summary from them — never via SummariseSession, whose ListTurnRefs
		// does a full References() scan per session (the ~16s O(N²) the
		// single-pass design exists to kill).
		node.Turns = buildTurnNodes(sc, b)
		node.Summary = summariseFromTurns(worktreeRoot, sc, id, node.Turns)
		node.Description = node.Summary.Description
		node.Avail = availabilityFromStatus(node.Summary.Status)
		node.IsCurrent = id == currentID
		for _, tn := range node.Turns {
			turnIndex[tn.Entry.Commit] = turnRef{sessionID: id, turn: tn.Entry.Turn}
		}
		// STAGE 7b: one extra trace-ref walk per session (already O(refs)),
		// folding each mutation/deny provenance commit's Turn trailer into
		// per-turn + session-total counts for the /tree badge.
		stampProvenanceCounts(sc, b, node)
		nodes[id] = node
		if b.hasTree && !b.treeHead.IsZero() {
			chain := firstParentChain(sc, b.treeHead)
			chains[id] = chain
			for _, c := range chain {
				set := chainMembership[c.hash]
				if set == nil {
					set = map[string]bool{}
					chainMembership[c.hash] = set
				}
				set[id] = true
			}
		}
	}

	// Phase 3 — resolve each session's edge from its exact fork seed.
	//
	// Seed = youngest chain commit (closest to HEAD) that another session's
	// chain also contains. If that seed is turn-tagged (turnIndex) → an edge
	// to (parent, turn N). If the seed is a real commit but NOT a turn
	// boundary (a mid-turn fork) → leave it unlinked: ParentTurn stays the
	// −1 sentinel and no edge is drawn (per the design's mid-turn scoping).
	// No shared seed at all → either a genuinely fresh root, or an orphan
	// whose parent's refs are gone (detected structurally via a foreign
	// turn-boundary commit surviving below this session's own floor).
	for _, id := range ids {
		chain := chains[id]
		if len(chain) == 0 {
			continue // no tree ref → fresh root
		}
		ownTurns := ownTurnHashes(nodes[id])
		ownFloor := -1
		seedFound := false
		for i, c := range chain {
			if ownTurns[c.hash] {
				ownFloor = i
			}
			if seedFound {
				continue // keep scanning only to finish computing ownFloor
			}
			if shared := chainMembership[c.hash]; len(shared) > 1 {
				// A commit on another session's chain too → the exact fork
				// seed (youngest such commit).
				seedFound = true
				if ref, ok := turnIndex[c.hash]; ok && ref.sessionID != id {
					nodes[id].ParentID = ref.sessionID
					nodes[id].ParentTurn = ref.turn
				}
				// else: mid-turn fork — seed shared but not turn-tagged.
				// Leave unlinked (no edge, ParentTurn = sentinel).
			}
		}
		if seedFound {
			continue
		}
		// No shared seed. Genuinely fresh, or orphan (parent refs deleted so
		// the seed is no longer on any live session's chain). The orphan
		// fingerprint is a foreign turn-boundary commit surviving on the
		// chain below this session's own deepest turn tag.
		if isOrphanChain(chain, ownTurns, ownFloor) {
			nodes[id].Orphan = true
		}
	}

	// Phase 4 — assemble roots + children, sort, BFS depth.
	forest := &Forest{
		Sessions:  nodes,
		Total:     len(ids),
		Truncated: truncated,
	}
	childrenOf := make(map[string][]*SessionNode, len(ids))
	for _, id := range ids {
		node := nodes[id]
		if node.ParentID != "" {
			if _, parentExists := nodes[node.ParentID]; parentExists {
				childrenOf[node.ParentID] = append(childrenOf[node.ParentID], node)
				continue
			}
			// Parent id was set but the parent node isn't in the forest
			// (truncated out, or a race). Treat as an orphan root so the
			// node is never lost.
			node.Orphan = true
			node.ParentID = ""
			node.ParentTurn = midTurnSentinel
		}
		forest.Roots = append(forest.Roots, node)
	}

	sortNodes(forest.Roots, currentID, nodes)
	for id := range childrenOf {
		sortNodes(childrenOf[id], currentID, nodes)
	}

	// BFS depth assignment from each root.
	assignDepth(forest.Roots, childrenOf)

	return forest, nil
}

// summariseFromTurns builds a SessionSummary WITHOUT the per-session
// References() scan SummariseSession pays via ListTurnRefs. The turns are
// already resolved (from the single bucketing pass), so Turns/LastActive
// come for free; the remaining fields (Status/PID, Description, Msgs,
// Compactions) are each a cheap stat or single-file/single-walk lookup. It
// is otherwise field-for-field identical to SummariseSession — same zero-
// value-on-error collapse — so the forest and `stado session list` render
// the same numbers. (Benchmark: ListTurnRefs alone is ~6.4s/160 sessions;
// everything here is <50ms/160 combined.)
func summariseFromTurns(worktreeRoot string, sc *stadogit.Sidecar, id string, turns []*TurnNode) SessionSummary {
	r := SessionSummary{ID: id, Status: "detached"}
	if stadogit.ValidateSessionID(id) != nil {
		return r
	}
	wt := filepath.Join(worktreeRoot, id)
	if _, err := os.Stat(wt); err == nil {
		r.Status = "idle"
		if pid, alive := liveOwningPID(wt); alive {
			r.Status = "live"
			r.PID = pid
		}
	}
	r.Turns = len(turns)
	if n := len(turns); n > 0 {
		r.LastActive = turns[n-1].Entry.When
	}
	if markers, err := sc.ListCompactions(id); err == nil {
		r.Compactions = len(markers)
	}
	if msgs, err := LoadConversation(wt); err == nil {
		r.Msgs = len(msgs)
	}
	r.Description = ReadDescription(wt)
	return r
}

// bucketSessionRefs does the single References() pass, bucketing every
// refs/sessions/<id>/{tree,trace,turns/N} ref by session id. The trace tip
// is captured so the provenance pass (STAGE 7b) can walk it once per session
// for mutation/deny badges; a trace-only session still registers the id. The
// returned buckets carry raw hashes; commit resolution happens later so a
// corrupt object skips one session, not the whole pass.
func bucketSessionRefs(sc *stadogit.Sidecar) (map[string]*sessionRefBucket, error) {
	forestRefPasses.Add(1)
	iter, err := sc.Repo().References()
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	const prefix = "refs/sessions/"
	buckets := map[string]*sessionRefBucket{}
	_ = iter.ForEach(func(ref *plumbing.Reference) error {
		name := string(ref.Name())
		if !strings.HasPrefix(name, prefix) {
			return nil
		}
		rest := strings.TrimPrefix(name, prefix)
		slash := strings.IndexByte(rest, '/')
		if slash < 0 {
			return nil
		}
		id := rest[:slash]
		if stadogit.ValidateSessionID(id) != nil {
			return nil
		}
		suffix := rest[slash+1:]
		b := buckets[id]
		if b == nil {
			b = &sessionRefBucket{turnHash: map[int]plumbing.Hash{}}
			buckets[id] = b
		}
		switch {
		case suffix == "tree":
			b.treeHead = ref.Hash()
			b.hasTree = true
		case suffix == "trace":
			b.traceHead = ref.Hash()
			b.hasTrace = true
		case strings.HasPrefix(suffix, "turns/"):
			n, err := strconv.Atoi(strings.TrimPrefix(suffix, "turns/"))
			if err != nil {
				return nil // skip unparseable turn ref
			}
			b.turnHash[n] = ref.Hash()
		}
		return nil
	})
	return buckets, nil
}

// buildTurnNodes resolves a session's bucketed turn hashes into ordered
// TurnNodes, skipping any tag whose commit object is missing (a stale tag
// pointing at a pruned commit shouldn't sink the whole session). Turns are
// returned in ascending turn order — matching ListTurnRefs.
func buildTurnNodes(sc *stadogit.Sidecar, b *sessionRefBucket) []*TurnNode {
	if b == nil || len(b.turnHash) == 0 {
		return nil
	}
	turns := make([]int, 0, len(b.turnHash))
	for n := range b.turnHash {
		turns = append(turns, n)
	}
	sort.Ints(turns)
	out := make([]*TurnNode, 0, len(turns))
	for _, n := range turns {
		hash := b.turnHash[n]
		commit, err := sc.Repo().CommitObject(hash)
		if err != nil {
			continue // corrupt/missing commit → skip this turn, keep the rest
		}
		summary, _, _ := strings.Cut(commit.Message, "\n")
		out = append(out, &TurnNode{
			Entry: stadogit.TurnEntry{
				Turn:    n,
				Commit:  hash,
				Author:  commit.Author.Name,
				When:    commit.Author.When,
				Summary: strings.TrimSpace(summary),
			},
		})
	}
	return out
}

// provCounts aggregates the mutation/deny provenance commit counts for a
// single Turn during the trace-ref walk.
type provCounts struct {
	mutated int
	denied  int
}

// maxTraceWalk bounds the per-session trace-ref walk so a corrupt/cyclic
// trace chain can't pin the forest build. The same 1<<20 ceiling
// firstParentChain uses — a session with a million trace commits is already
// pathological.
const maxTraceWalk = 1 << 20

// stampProvenanceCounts walks a session's trace ref ONCE, folding every
// mutation/deny provenance commit into its Turn's per-turn count (matched
// against the session's turn nodes) and the session-level totals. A trace
// commit carrying Mutated-By-Hook is a mutation (a post_tool hook rewrote a
// tool result, recorded as the second of the two-commit pair); one carrying
// Deny-Reason is a pre_tool veto. Both are attributed to the commit's
// `Turn: N` trailer; a commit whose Turn matches no turn tag still counts
// toward the session totals so the session-line badge never undercounts.
//
// CONVENTION DRIFT (the bug this guards against). A tool-call trace commit's
// `Turn: K` trailer is Session.Turn() at dispatch time — the turn IN PROGRESS,
// which is zero-indexed and PRE-increment (a fresh session's first turn runs
// at Turn() == 0; an existing session reopened after turns/N runs its next
// turn at Turn() == N). The turn-boundary TAG that CLOSES that same operator
// turn is turns/K+1 (NextTurn does nextTurn := s.turn + 1 before tagging). So
// the operator-facing turn the tag turns/N represents was executed while
// Session.Turn() == N-1, and its provenance lands in bucket N-1. A naive
// perTurn[tn.Entry.Turn] join is therefore off-by-one: the first turn's
// mutations/denies land in bucket 0, which has no turns/0 tag, so they render
// on NO turn row even though the session total counts them — and every later
// turn's badge shows the NEXT turn's counts. We bridge the +1 here: bucket K
// attributes to the turn node whose tag (turns/K+1) closed it.
//
// Cheap by construction: one git-log-shaped first-parent walk per session,
// only over the trace ref (which BuildForest already enumerated). Sessions
// with no trace ref (or no provenance commits — the common case) pay only
// the walk, which stops at the first non-trace tip / empty ref immediately.
func stampProvenanceCounts(sc *stadogit.Sidecar, b *sessionRefBucket, node *SessionNode) {
	if sc == nil || b == nil || node == nil || !b.hasTrace || b.traceHead.IsZero() {
		return
	}
	perTurn := walkTraceProvenance(sc, b.traceHead)
	if len(perTurn) == 0 {
		return
	}
	for _, tn := range node.Turns {
		// Provenance for the operator turn that tag turns/N closed lives in
		// bucket N-1 (see CONVENTION DRIFT above), so join on Entry.Turn-1.
		if c, ok := perTurn[tn.Entry.Turn-1]; ok {
			tn.MutatedCount = c.mutated
			tn.DeniedCount = c.denied
		}
	}
	for _, c := range perTurn {
		node.MutatedTotal += c.mutated
		node.DeniedTotal += c.denied
	}
}

// walkTraceProvenance does the first-parent trace-ref walk, returning the
// per-Turn mutation/deny counts. Newest-first, bounded by maxTraceWalk, with
// a visited-set cycle guard. A missing/corrupt commit truncates the walk
// (keep what we have) rather than erroring — provenance badges are advisory,
// never load-bearing for navigation. Trailers are parsed with the same
// audit.ParseMessage the verify/export paths use, so the trailer grammar
// stays single-sourced.
func walkTraceProvenance(sc *stadogit.Sidecar, traceHead plumbing.Hash) map[int]provCounts {
	repo := sc.Repo()
	out := map[int]provCounts{}
	seen := map[plumbing.Hash]bool{}
	cur := traceHead
	for i := 0; i < maxTraceWalk && !cur.IsZero(); i++ {
		if seen[cur] {
			break // cycle guard (manual ref surgery / corruption)
		}
		seen[cur] = true
		commit, err := repo.CommitObject(cur)
		if err != nil {
			break // pruned/corrupt — stop, keep what we have
		}
		_, trailers := audit.ParseMessage(commit.Message)
		mutated := trailers["Mutated-By-Hook"] != ""
		denied := trailers["Deny-Reason"] != ""
		if mutated || denied {
			turn, _ := strconv.Atoi(trailers["Turn"]) // unparseable → turn 0 bucket
			c := out[turn]
			if mutated {
				c.mutated++
			}
			if denied {
				c.denied++
			}
			out[turn] = c
		}
		if len(commit.ParentHashes) == 0 {
			break
		}
		cur = commit.ParentHashes[0]
	}
	return out
}

// chainCommit is one commit on a session's first-parent chain, carrying
// just enough to resolve lineage without re-fetching the object: the hash
// and whether it's a turn-boundary commit (which is what a turn tag points
// at — used for the orphan signal).
type chainCommit struct {
	hash         plumbing.Hash
	isTurnMarker bool
}

// turnBoundarySubject is the exact subject NextTurn writes for a turn
// boundary commit. A turn tag always points at one of these. We use the
// subject (not just "is it tagged") so orphan detection survives the
// parent's turn TAGS being deleted while the boundary COMMITS persist in
// the shared object store.
const turnBoundarySubject = "turn_boundary: completed turn"

// firstParentChain walks a session's tree-ref first-parent chain from
// treeHead to the lineage root, newest-first. A missing/corrupt commit
// truncates the walk (we keep what we have) rather than erroring — a
// pruned ancestor shouldn't sink the whole forest. Bounded to guard a
// cyclic/corrupt chain.
func firstParentChain(sc *stadogit.Sidecar, treeHead plumbing.Hash) []chainCommit {
	repo := sc.Repo()
	const maxWalk = 1 << 20
	var out []chainCommit
	cur := treeHead
	for i := 0; i < maxWalk && !cur.IsZero(); i++ {
		commit, err := repo.CommitObject(cur)
		if err != nil {
			break // pruned/corrupt ancestor — stop, keep what we have
		}
		subject, _, _ := strings.Cut(commit.Message, "\n")
		out = append(out, chainCommit{
			hash:         cur,
			isTurnMarker: strings.TrimSpace(subject) == turnBoundarySubject,
		})
		if len(commit.ParentHashes) == 0 {
			break // lineage root
		}
		cur = commit.ParentHashes[0]
	}
	return out
}

// ownTurnHashes returns the set of a session's OWN turn-boundary commit
// hashes, used to anchor where the session's own history sits on its chain.
func ownTurnHashes(node *SessionNode) map[plumbing.Hash]bool {
	set := make(map[plumbing.Hash]bool, len(node.Turns))
	for _, tn := range node.Turns {
		set[tn.Entry.Commit] = true
	}
	return set
}

// isOrphanChain reports whether an unmatched session's chain shows it was
// forked from a now-deleted parent (orphan) rather than being genuinely
// fresh.
//
// Signal: a turn-boundary commit appears on the chain STRICTLY BELOW this
// session's own deepest turn tag (ownFloor) that is NOT one of this
// session's own turns. Turn tags are written in ascending order, so a
// session's own turn boundaries never sit below its own lowest turn tag —
// only a forked-in parent boundary can. When the parent's turn tags still
// exist, that boundary matched in turnIndex (handled before we get here);
// when they've been deleted, the boundary COMMIT survives in the shared
// object store and is the orphan fingerprint.
//
// Limitation (documented, accepted for v1 per the design's mid-turn
// scoping): a session forked MID-TURN (seed is a tool commit, not a turn
// boundary) from a deleted parent collapses to "fresh" — there's no
// surviving boundary commit to fingerprint. Live-parent mid-turn forks are
// correctly left unlinked (no edge, not orphan).
func isOrphanChain(chain []chainCommit, ownTurns map[plumbing.Hash]bool, ownFloor int) bool {
	for i, c := range chain {
		if i <= ownFloor {
			continue // at or above the session's own deepest turn — own history
		}
		if c.isTurnMarker && !ownTurns[c.hash] {
			return true
		}
	}
	return false
}

// sortNodes orders a slice of sibling SessionNodes: the current session's
// lineage first (current node, then any node on the path to/from current),
// then most-recently-active first, then by id for a stable tiebreak. The
// lineage rule keeps the operator's working set pinned to the top of a
// large forest.
func sortNodes(nodes []*SessionNode, currentID string, all map[string]*SessionNode) {
	inCurrentLineage := lineageSet(currentID, all)
	sort.SliceStable(nodes, func(i, j int) bool {
		a, b := nodes[i], nodes[j]
		la, lb := inCurrentLineage[a.ID], inCurrentLineage[b.ID]
		if la != lb {
			return la // lineage members sort first
		}
		if a.IsCurrent != b.IsCurrent {
			return a.IsCurrent
		}
		if !a.Summary.LastActive.Equal(b.Summary.LastActive) {
			return a.Summary.LastActive.After(b.Summary.LastActive)
		}
		return a.ID < b.ID
	})
}

// SessionAncestors returns the ancestor session ids of sessionID within an
// already-open sidecar — nearest parent first, excluding the session itself.
// It is the mechanism behind EP-15 session-scope memory inheritance: a session
// sees the session-scoped memories of every session it forked from. Best
// effort by construction — an unlinked (mid-turn fork) or orphaned session,
// whose ParentID the forest could not recover, yields an empty chain, so
// retrieval safely falls back to exact-session matching.
func SessionAncestors(sc *stadogit.Sidecar, worktreeRoot, sessionID string) ([]string, error) {
	if sc == nil || sessionID == "" {
		return nil, nil
	}
	forest, err := BuildForest(sc, worktreeRoot, sessionID)
	if err != nil {
		return nil, err
	}
	return ancestorChain(forest, sessionID), nil
}

// SessionAncestorsForRepo is SessionAncestors for callers that hold config
// paths but no open sidecar (run/headless/ACP). sessionsDir is
// cfg.StateDir()/sessions, worktreeRoot is cfg.WorktreeDir(), and repoRoot is
// the user repo root (memory.RepoRootFor(workdir)). A missing sidecar yields no
// ancestry rather than creating one.
func SessionAncestorsForRepo(sessionsDir, worktreeRoot, repoRoot, sessionID string) ([]string, error) {
	if sessionID == "" || strings.TrimSpace(repoRoot) == "" {
		return nil, nil
	}
	repoID, err := stadogit.RepoID(repoRoot)
	if err != nil {
		return nil, err
	}
	// Mirror config.SidecarPath: <StateDir>/sessions/<repoID>.git.
	sidecarPath := filepath.Join(sessionsDir, repoID+".git")
	if _, err := os.Stat(sidecarPath); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	sc, err := stadogit.OpenOrInitSidecar(sidecarPath, repoRoot)
	if err != nil {
		return nil, err
	}
	return SessionAncestors(sc, worktreeRoot, sessionID)
}

// ancestorChain walks a session's ParentID links to the root, excluding the
// session itself, with a cycle guard. Order is nearest parent first.
func ancestorChain(forest *Forest, sessionID string) []string {
	if forest == nil {
		return nil
	}
	var out []string
	seen := map[string]bool{sessionID: true}
	node := forest.Sessions[sessionID]
	for node != nil && node.ParentID != "" {
		if seen[node.ParentID] {
			break // cycle guard (corrupt ParentID loop after manual ref edits)
		}
		seen[node.ParentID] = true
		out = append(out, node.ParentID)
		node = forest.Sessions[node.ParentID]
	}
	return out
}

// lineageSet returns the set of session ids on the current session's
// ancestry chain (current + each ParentID up to a root). Used to pin the
// working lineage to the top of the sort. Empty when currentID is "".
func lineageSet(currentID string, all map[string]*SessionNode) map[string]bool {
	set := map[string]bool{}
	if currentID == "" {
		return set
	}
	cur := currentID
	for cur != "" {
		if set[cur] {
			break // cycle guard
		}
		set[cur] = true
		node, ok := all[cur]
		if !ok {
			break
		}
		cur = node.ParentID
	}
	return set
}

// assignDepth walks the forest breadth-first from each root, stamping
// Depth on every reachable node (root == 0). Children come from the
// pre-built childrenOf adjacency so depth assignment is a pure traversal,
// no re-derivation.
func assignDepth(roots []*SessionNode, childrenOf map[string][]*SessionNode) {
	type item struct {
		node  *SessionNode
		depth int
	}
	queue := make([]item, 0, len(roots))
	for _, r := range roots {
		queue = append(queue, item{node: r, depth: 0})
	}
	seen := map[string]bool{}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		if seen[it.node.ID] {
			continue // cycle guard
		}
		seen[it.node.ID] = true
		it.node.Depth = it.depth
		for _, child := range childrenOf[it.node.ID] {
			queue = append(queue, item{node: child, depth: it.depth + 1})
		}
	}
}
