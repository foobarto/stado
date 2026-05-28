package broker

// gitsubagent.go — phase 7 substrate for the ssh-agent + git
// sub-agent flow described in DESIGN.md §"Sessions and sub-agents"
// → "Git sub-agent". The substrate-level pieces wired here:
//
//   - Role constants for "git-fetch" / "git-sub-agent" (reserved
//     by isElevatedSubagentRole in phase 6).
//   - GitSubagentSpec: the declared task that drives the
//     mechanical ceiling projection — declared hosts, declared key
//     paths, declared egress mode.
//   - ProjectGitSubagentCeiling: takes a parent Policy + a
//     GitSubagentSpec and produces the child ceiling. The
//     projection is mechanical — the model doesn't negotiate; it
//     declares hosts + key paths and the substrate produces the
//     ceiling.
//   - IsForbiddenForGitSubagent: dispatch-side helper the runtime
//     consults to refuse `git push` (and other write-direction
//     commands) from a fetch-purposed sub-agent.
//
// What this DOESN'T do yet (deferred runtime wiring — flagged
// in EP-0050 and DESIGN.md as phase 7 follow-up):
//
//   - Bind-mount the ssh-agent socket into the sub-agent's
//     namespace at bwrap-build time. The Policy.Env machinery
//     plumbs SSH_AUTH_SOCK through but the actual mount needs
//     bwrap-layer code that doesn't exist yet.
//   - The approval-once prompt + taint gating at session-create.
//     The substrate (EvaluateWithTaint reserving git-fetch in
//     phase 6) is in place; the orchestrator-side prompt UI is
//     the missing piece.
//   - Synthesised minimal ssh config for the hardened profile.
//     Phase 3's mount table reserves the path; phase 7+ would
//     emit the minimal config at broker startup into a
//     broker-owned temp dir + bind-mount it.

import (
	"sort"
	"strings"

	"github.com/foobarto/stado/internal/sandbox"
)

// Role constants for git sub-agents. Match the reserved set in
// isElevatedSubagentRole — when the substrate is wired into
// spawn_agent, these names trigger the taint overlay + the
// special ceiling projection in ProjectGitSubagentCeiling.
const (
	RoleGitFetch    = "git-fetch"
	RoleGitSubagent = "git-sub-agent"
)

// gitFetchableCommands is the allowlist of git operations that
// fetch-purposed sub-agents may perform. Anything not on this
// list is denied by IsForbiddenForGitSubagent regardless of what
// the ssh-agent socket would sign.
var gitFetchableCommands = map[string]struct{}{
	"clone":     {},
	"fetch":     {},
	"pull":      {},
	"ls-remote": {},
	// Read-only inspect operations.
	"log":  {},
	"show": {},
	"diff": {},
}

// gitPushCommands explicitly enumerated for clarity — these are
// the write-direction operations a fetch-purposed sub-agent must
// not perform. Other git verbs that aren't in either list (e.g.
// "branch -D", "config --global") default to forbidden via the
// "not in fetchable list" fall-through.
var gitPushCommands = map[string]struct{}{
	"push":      {},
	"send-pack": {},
}

// GitSubagentSpec is the declared task that drives the
// mechanical ceiling projection for a git sub-agent. The
// spawn_agent request (when its role is RoleGitFetch /
// RoleGitSubagent) carries these fields; the broker projects
// them into the ceiling.
type GitSubagentSpec struct {
	// Hosts is the declared set of remote hosts the sub-agent
	// may reach (e.g. "github.com", "gitlab.example.com"). Other
	// hosts are denied at the network-namespace level.
	Hosts []string

	// KeyPaths is the declared set of host -> key-path mappings.
	// The ssh-agent socket bind-mounted into the sub-agent's
	// namespace will only contain keys for these declared hosts
	// (loaded by the broker from the operator's ssh-agent
	// outside the sandbox).
	//
	// Phase 7 runtime wiring (not in this iteration): the
	// broker reads the operator's ssh-agent, filters to the
	// declared host -> key mapping, exposes only those entries
	// over the bind-mounted socket. Substrate today just
	// records the declared paths.
	KeyPaths map[string]string

	// WriteScope is the declared subset of the parent's writable
	// paths the sub-agent may write to (build outputs, fetched
	// module caches, etc.). Anything outside this scope is
	// dropped at projection time.
	WriteScope []string

	// AllowEgressOnlyToDeclaredHosts, when true (the default for
	// fetch-purposed sub-agents), produces Net = NetAllowHosts(Hosts).
	// When false, no Net narrowing is applied — used for tests
	// that want to exercise the FS/Env projection without the
	// netns implications.
	AllowEgressOnlyToDeclaredHosts bool
}

// ProjectGitSubagentCeiling produces a sandbox.Policy that
// represents the git sub-agent's ceiling, mechanically projected
// from the parent Policy + the declared spec. Guarantees:
//
//   - FSRead inherits from parent (the sub-agent can read what
//     the parent could read; it doesn't gain new read access).
//   - FSWrite is the intersection of spec.WriteScope and the
//     parent's writable paths (subpath semantics from
//     SubagentCeiling apply).
//   - Net is NetAllowHosts(spec.Hosts) when
//     AllowEgressOnlyToDeclaredHosts is true; otherwise
//     inherited from parent.
//   - Env gains SSH_AUTH_SOCK (the bind-mounted socket the
//     broker provides; phase 7 runtime work mounts it).
//
// The projection NEVER widens beyond the parent ceiling — even
// if spec.Hosts has hosts the parent's Net.AllowHosts doesn't
// include, the intersection drops them. The dropped slice (paths
// + hosts) the caller can surface in the broker-decision log.
func ProjectGitSubagentCeiling(parent sandbox.Policy, spec GitSubagentSpec) (sandbox.Policy, []string) {
	// Start with the worker/workspace_write semantics from
	// SubagentCeiling — that handles the WriteScope projection.
	child, dropped := SubagentCeiling(parent, "worker", "workspace_write", spec.WriteScope)

	// Egress narrowing. NetAllowHosts intersected with parent's
	// Net via the standard rules in Policy.Merge.
	if spec.AllowEgressOnlyToDeclaredHosts {
		netRequested := sandbox.NetPolicy{
			Kind:  sandbox.NetAllowHosts,
			Hosts: cleanHosts(spec.Hosts),
		}
		// Manual intersection: parent's Net beats the request when
		// parent is DenyAll; otherwise intersect host lists.
		switch parent.Net.Kind {
		case sandbox.NetDenyAll:
			child.Net = sandbox.NetPolicy{Kind: sandbox.NetDenyAll}
		case sandbox.NetAllowAll:
			child.Net = netRequested
		case sandbox.NetAllowHosts:
			child.Net = sandbox.NetPolicy{
				Kind:  sandbox.NetAllowHosts,
				Hosts: intersectHosts(parent.Net.Hosts, netRequested.Hosts),
			}
		}
		// Track dropped hosts (requested but parent doesn't allow).
		dropped = append(dropped, droppedHosts(spec.Hosts, child.Net.Hosts)...)
	}

	// Env: add SSH_AUTH_SOCK if not already there. This signals
	// the runtime wiring (when it lands) to bind-mount the
	// broker-prepared filtered ssh-agent socket at the path the
	// env var points to.
	if !containsString(child.Env, "SSH_AUTH_SOCK") {
		child.Env = append([]string(nil), child.Env...)
		child.Env = append(child.Env, "SSH_AUTH_SOCK")
	}

	return child, dropped
}

// IsForbiddenForGitSubagent returns true when the given argv
// (typically `git <verb> ...`) is a write-direction operation
// that fetch-purposed sub-agents must not perform, regardless
// of what the ssh-agent socket would sign.
//
// The runtime tool-dispatch hook (phase 7 wiring) consults this
// before invoking the shell/bash tool from a sub-agent whose
// role is RoleGitFetch or RoleGitSubagent.
//
// Recognises both bare git (`git push origin main`) and
// piped/redirected forms (`git push 2>&1`); the verb-extraction
// strips common shell decoration to find the actual git verb.
func IsForbiddenForGitSubagent(argv []string) bool {
	verb := extractGitVerb(argv)
	if verb == "" {
		return false // not a git command — defer to other guards
	}
	if _, ok := gitPushCommands[verb]; ok {
		return true
	}
	if _, ok := gitFetchableCommands[verb]; ok {
		return false
	}
	// Unknown git verb: default to forbidden (fail-safe). The
	// allowlist is small enough that this is the right default —
	// new git operations should be explicitly considered before
	// being allowed in a fetch-purposed sub-agent.
	return true
}

// extractGitVerb finds the git subcommand verb in argv. Returns
// "" if argv doesn't look like a git invocation. Handles:
//
//   - ["git", "fetch", ...]
//   - ["git", "-c", "key=val", "fetch", ...]
//   - ["/usr/bin/git", "push", ...]
func extractGitVerb(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	prog := argv[0]
	// Allow /usr/bin/git, git.exe, etc.
	base := prog
	if i := strings.LastIndexAny(prog, "/\\"); i >= 0 {
		base = prog[i+1:]
	}
	base = strings.TrimSuffix(base, ".exe")
	if base != "git" {
		return ""
	}
	// Skip leading -c options (git -c key=val verb args).
	for i := 1; i < len(argv); i++ {
		switch argv[i] {
		case "-c":
			i++ // skip the key=val
			continue
		case "-C":
			i++ // skip the path
			continue
		}
		if strings.HasPrefix(argv[i], "-") {
			continue // any other flag before the verb
		}
		return argv[i]
	}
	return ""
}

func cleanHosts(hosts []string) []string {
	if len(hosts) == 0 {
		return nil
	}
	out := make([]string, 0, len(hosts))
	seen := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

func intersectHosts(parent, child []string) []string {
	if len(parent) == 0 || len(child) == 0 {
		return nil
	}
	parentSet := make(map[string]struct{}, len(parent))
	for _, p := range parent {
		parentSet[p] = struct{}{}
	}
	var out []string
	for _, c := range child {
		if _, ok := parentSet[c]; ok {
			out = append(out, c)
		}
	}
	return out
}

func droppedHosts(requested, kept []string) []string {
	keptSet := make(map[string]struct{}, len(kept))
	for _, k := range kept {
		keptSet[k] = struct{}{}
	}
	var out []string
	for _, r := range requested {
		if _, ok := keptSet[r]; !ok {
			out = append(out, r)
		}
	}
	return out
}

func containsString(slice []string, target string) bool {
	for _, s := range slice {
		if s == target {
			return true
		}
	}
	return false
}
