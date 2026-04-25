package runtime

import (
	"fmt"
	"sort"

	"github.com/go-git/go-git/v5/plumbing"

	stadogit "github.com/foobarto/stado/internal/state/git"
)

// SubagentAdoptionPlan is a dry-run summary for applying a child worker's
// file changes back to its parent session.
type SubagentAdoptionPlan struct {
	CanAdopt           bool     `json:"can_adopt"`
	ForkTree           string   `json:"fork_tree,omitempty"`
	ParentTree         string   `json:"parent_tree,omitempty"`
	ChildTree          string   `json:"child_tree,omitempty"`
	ChangedFiles       []string `json:"changed_files,omitempty"`
	ParentChangedFiles []string `json:"parent_changed_files,omitempty"`
	Conflicts          []string `json:"conflicts,omitempty"`
}

// PlanSubagentAdoption checks whether a child session's changed files can be
// adopted into the parent without overwriting parent edits made since forkTree.
// It does not modify either worktree or tree ref.
func PlanSubagentAdoption(parent, child *stadogit.Session, forkTree plumbing.Hash) (SubagentAdoptionPlan, error) {
	if parent == nil || parent.Sidecar == nil {
		return SubagentAdoptionPlan{}, fmt.Errorf("subagent adoption: parent session required")
	}
	if child == nil || child.Sidecar == nil {
		return SubagentAdoptionPlan{}, fmt.Errorf("subagent adoption: child session required")
	}
	parentTree, err := parent.CurrentTree()
	if err != nil {
		return SubagentAdoptionPlan{}, fmt.Errorf("subagent adoption: parent tree: %w", err)
	}
	childTree, err := child.CurrentTree()
	if err != nil {
		return SubagentAdoptionPlan{}, fmt.Errorf("subagent adoption: child tree: %w", err)
	}
	childChanged, err := child.ChangedFilesBetween(forkTree, childTree)
	if err != nil {
		return SubagentAdoptionPlan{}, fmt.Errorf("subagent adoption: child changed files: %w", err)
	}
	parentChanged, err := parent.ChangedFilesBetween(forkTree, parentTree)
	if err != nil {
		return SubagentAdoptionPlan{}, fmt.Errorf("subagent adoption: parent changed files: %w", err)
	}
	childChanged = reportableWorkerChangedFiles(childChanged)
	parentChanged = reportableWorkerChangedFiles(parentChanged)
	conflicts := intersectSorted(childChanged, parentChanged)
	return SubagentAdoptionPlan{
		CanAdopt:           len(conflicts) == 0,
		ForkTree:           hashString(forkTree),
		ParentTree:         hashString(parentTree),
		ChildTree:          hashString(childTree),
		ChangedFiles:       childChanged,
		ParentChangedFiles: parentChanged,
		Conflicts:          conflicts,
	}, nil
}

func intersectSorted(a, b []string) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	inB := make(map[string]struct{}, len(b))
	for _, item := range b {
		inB[item] = struct{}{}
	}
	var out []string
	for _, item := range a {
		if _, ok := inB[item]; ok {
			out = append(out, item)
		}
	}
	sort.Strings(out)
	return out
}

func hashString(hash plumbing.Hash) string {
	if hash.IsZero() {
		return ""
	}
	return hash.String()
}
