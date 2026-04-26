package runtime

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"

	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/workdirpath"
)

// SubagentAdoptionPlan is a dry-run summary for applying a child worker's
// file changes back to its parent session.
type SubagentAdoptionPlan struct {
	CanAdopt           bool     `json:"can_adopt"`
	Applied            bool     `json:"applied,omitempty"`
	ForkTree           string   `json:"fork_tree,omitempty"`
	ParentTree         string   `json:"parent_tree,omitempty"`
	ChildTree          string   `json:"child_tree,omitempty"`
	AdoptedTree        string   `json:"adopted_tree,omitempty"`
	ChangedFiles       []string `json:"changed_files,omitempty"`
	AdoptedFiles       []string `json:"adopted_files,omitempty"`
	ParentChangedFiles []string `json:"parent_changed_files,omitempty"`
	Conflicts          []string `json:"conflicts,omitempty"`
}

var ErrSubagentAdoptionConflict = errors.New("subagent adoption: conflicts")

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
	parentTree, err := parent.BuildTreeFromDir(parent.WorktreePath)
	if err != nil {
		return SubagentAdoptionPlan{}, fmt.Errorf("subagent adoption: parent tree: %w", err)
	}
	childTree, err := child.BuildTreeFromDir(child.WorktreePath)
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

// AdoptSubagentChanges applies a conflict-free child adoption plan to the
// parent worktree and records trace/tree commits. It never mutates the child.
func AdoptSubagentChanges(parent, child *stadogit.Session, forkTree plumbing.Hash, agentName, model string) (SubagentAdoptionPlan, error) {
	plan, err := PlanSubagentAdoption(parent, child, forkTree)
	if err != nil {
		return plan, err
	}
	if !plan.CanAdopt {
		return plan, ErrSubagentAdoptionConflict
	}
	if len(plan.ChangedFiles) == 0 {
		return plan, nil
	}
	for _, file := range plan.ChangedFiles {
		if err := copyChildChange(parent.WorktreePath, child.WorktreePath, file); err != nil {
			return plan, fmt.Errorf("subagent adoption: apply %s: %w", file, err)
		}
	}
	adoptedTree, err := parent.BuildTreeFromDir(parent.WorktreePath)
	if err != nil {
		return plan, fmt.Errorf("subagent adoption: build adopted tree: %w", err)
	}
	if agentName == "" {
		agentName = "stado-subagent-adopt"
	}
	meta := stadogit.CommitMeta{
		Tool:     "subagent_adopt",
		ShortArg: child.ID,
		Summary:  fmt.Sprintf("adopt %d file(s) from child %s", len(plan.ChangedFiles), shortSessionID(child.ID)),
		Model:    model,
		Agent:    agentName,
		Turn:     parent.Turn(),
	}
	if _, err := parent.CommitToTrace(meta); err != nil {
		return plan, fmt.Errorf("subagent adoption: commit trace: %w", err)
	}
	if _, err := parent.CommitToTree(adoptedTree, meta); err != nil {
		return plan, fmt.Errorf("subagent adoption: commit tree: %w", err)
	}
	plan.Applied = true
	plan.AdoptedTree = hashString(adoptedTree)
	plan.AdoptedFiles = append([]string(nil), plan.ChangedFiles...)
	return plan, nil
}

func copyChildChange(parentWorktree, childWorktree, rel string) error {
	parentRootPath, parentRel, err := workdirpath.RootRelForWrite(parentWorktree, rel)
	if err != nil {
		return err
	}
	parentRoot, err := os.OpenRoot(parentRootPath)
	if err != nil {
		return err
	}
	defer func() { _ = parentRoot.Close() }()

	childPath, err := safeWorktreeRel(childWorktree, rel)
	if err != nil {
		return err
	}
	info, err := os.Lstat(childPath)
	if errors.Is(err, os.ErrNotExist) {
		if rmErr := parentRoot.RemoveAll(parentRel); rmErr != nil {
			return rmErr
		}
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("directories are not supported adoption targets")
	}
	if dir := filepath.Dir(parentRel); dir != "." {
		if err := parentRoot.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := parentRoot.RemoveAll(parentRel); err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(childPath)
		if err != nil {
			return err
		}
		if !safeAdoptionSymlinkTarget(parentRel, target) {
			return fmt.Errorf("unsafe symlink target %q for %q", target, rel)
		}
		return parentRoot.Symlink(target, parentRel)
	}
	data, err := workdirpath.ReadFile(childWorktree, rel)
	if err != nil {
		return err
	}
	mode := info.Mode().Perm()
	return workdirpath.WriteRootFileAtomic(parentRoot, parentRel, data, adoptedFileMode(mode))
}

func adoptedFileMode(mode os.FileMode) os.FileMode {
	if mode&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

func safeWorktreeRel(root, rel string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("worktree root is required")
	}
	if rel == "" || rel == "." || filepath.IsAbs(rel) || strings.Contains(rel, "\x00") {
		return "", fmt.Errorf("unsafe relative path %q", rel)
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe relative path %q", rel)
	}
	return filepath.Join(root, clean), nil
}

func safeAdoptionSymlinkTarget(linkRel, target string) bool {
	if target == "" || filepath.IsAbs(target) || strings.Contains(target, "\x00") {
		return false
	}
	linkDir := filepath.Dir(filepath.FromSlash(linkRel))
	cleanTarget := filepath.Clean(filepath.Join(linkDir, filepath.FromSlash(target)))
	return cleanTarget != "." && cleanTarget != ".." && !strings.HasPrefix(cleanTarget, ".."+string(filepath.Separator))
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

func shortSessionID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
