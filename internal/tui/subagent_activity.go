package tui

import (
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/subagent"
)

const maxSubagentActivities = 5

type subagentEventMsg struct {
	ev runtime.SubagentEvent
}

type subagentActivity struct {
	ChildSession    string
	Worktree        string
	Role            string
	Mode            string
	Phase           string
	Status          string
	ForkTree        string
	ChangedFiles    int
	ScopeViolations int
	AdoptionCommand string
	StartedAt       time.Time
	UpdatedAt       time.Time
}

func (m *Model) recordSubagentEvent(ev runtime.SubagentEvent) {
	child := strings.TrimSpace(ev.ChildSession)
	if child == "" {
		return
	}
	now := time.Now()
	act := m.upsertSubagentActivity(child, now)
	if ev.Worktree != "" {
		act.Worktree = ev.Worktree
	}
	if ev.Role != "" {
		act.Role = ev.Role
	}
	if ev.Mode != "" {
		act.Mode = ev.Mode
	}
	if ev.Phase != "" {
		act.Phase = ev.Phase
	}
	if ev.Status != "" {
		act.Status = ev.Status
	}
	if ev.ForkTree != "" {
		act.ForkTree = ev.ForkTree
	}
	if len(ev.ChangedFiles) > 0 {
		act.ChangedFiles = len(ev.ChangedFiles)
	}
	if len(ev.ScopeViolations) > 0 {
		act.ScopeViolations = len(ev.ScopeViolations)
	}
	if cmd := ev.AdoptionCommand(); cmd != "" {
		act.AdoptionCommand = cmd
	}
	act.UpdatedAt = now
}

func (m *Model) recordSubagentResult(res subagent.Result) {
	child := strings.TrimSpace(res.ChildSession)
	if child == "" {
		return
	}
	now := time.Now()
	act := m.upsertSubagentActivity(child, now)
	if res.Worktree != "" {
		act.Worktree = res.Worktree
	}
	if res.Role != "" {
		act.Role = res.Role
	}
	if res.Mode != "" {
		act.Mode = res.Mode
	}
	act.Phase = "finished"
	act.Status = res.Status
	if act.Status == "" {
		act.Status = "completed"
	}
	act.ForkTree = res.ForkTree
	act.ChangedFiles = len(res.ChangedFiles)
	act.ScopeViolations = len(res.ScopeViolations)
	parent := ""
	if m.session != nil {
		parent = m.session.ID
	}
	act.AdoptionCommand = runtime.SubagentEvent{
		ParentSession: parent,
		ChildSession:  child,
		ForkTree:      res.ForkTree,
		ChangedFiles:  res.ChangedFiles,
	}.AdoptionCommand()
	act.UpdatedAt = now
}

func (m *Model) upsertSubagentActivity(child string, now time.Time) *subagentActivity {
	for i := range m.subagents {
		if m.subagents[i].ChildSession == child {
			return &m.subagents[i]
		}
	}
	m.subagents = append(m.subagents, subagentActivity{
		ChildSession: child,
		StartedAt:    now,
		UpdatedAt:    now,
	})
	if len(m.subagents) > maxSubagentActivities {
		m.subagents = append([]subagentActivity(nil), m.subagents[len(m.subagents)-maxSubagentActivities:]...)
	}
	return &m.subagents[len(m.subagents)-1]
}

func (m *Model) sidebarSubagentLines() []sidebarLine {
	if len(m.subagents) == 0 {
		return nil
	}
	out := make([]sidebarLine, 0, len(m.subagents)*3)
	for i := len(m.subagents) - 1; i >= 0; i-- {
		act := m.subagents[i]
		status := act.Status
		if status == "" {
			status = act.Phase
		}
		if status == "" {
			status = "running"
		}
		text := shortSessionID(act.ChildSession) + " " + status
		if roleMode := subagentRoleMode(act); roleMode != "" {
			text += " " + roleMode
		}
		if status == "running" && !act.StartedAt.IsZero() {
			if elapsed := sidebarDurationString(time.Since(act.StartedAt)); elapsed != "" {
				text += " " + elapsed
			}
		}
		out = append(out, sidebarLine{Text: text, Tone: subagentActivityTone(act)})
		if act.ChangedFiles > 0 {
			changed := fmt.Sprintf("%d changed", act.ChangedFiles)
			if act.AdoptionCommand != "" {
				changed += " · adopt ready"
			}
			out = append(out, sidebarLine{Text: changed, Tone: "accent"})
		}
		if act.ScopeViolations > 0 {
			label := "scope violation"
			if act.ScopeViolations != 1 {
				label = "scope violations"
			}
			out = append(out, sidebarLine{
				Text: fmt.Sprintf("%d %s", act.ScopeViolations, label),
				Tone: "warning",
			})
		}
	}
	return out
}

func (m *Model) renderSubagentsOverview() string {
	if len(m.subagents) == 0 {
		return "/subagents: no subagent activity yet."
	}
	var b strings.Builder
	b.WriteString("Subagents:\n")
	for i := len(m.subagents) - 1; i >= 0; i-- {
		act := m.subagents[i]
		status := act.Status
		if status == "" {
			status = act.Phase
		}
		if status == "" {
			status = "running"
		}
		fmt.Fprintf(&b, "\n- %s  %s", act.ChildSession, status)
		if roleMode := subagentRoleMode(act); roleMode != "" {
			fmt.Fprintf(&b, "  %s", roleMode)
		}
		if act.Worktree != "" {
			fmt.Fprintf(&b, "\n  worktree: %s", act.Worktree)
		}
		if !act.StartedAt.IsZero() {
			if elapsed := sidebarDurationString(time.Since(act.StartedAt)); elapsed != "" {
				fmt.Fprintf(&b, "\n  started: %s ago", elapsed)
			}
		}
		if act.ChangedFiles > 0 {
			fmt.Fprintf(&b, "\n  changed files: %d", act.ChangedFiles)
		}
		if act.ScopeViolations > 0 {
			fmt.Fprintf(&b, "\n  scope violations: %d", act.ScopeViolations)
		}
		if act.AdoptionCommand != "" {
			fmt.Fprintf(&b, "\n  adopt: %s", act.AdoptionCommand)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Model) handleSubagentAdoptSlash(parts []string) {
	body, err := m.runSubagentAdoptSlash(parts)
	if err != nil {
		if body != "" {
			body += "\nerror: " + err.Error()
		} else {
			body = err.Error()
		}
	}
	m.appendBlock(block{kind: "system", body: body})
}

func (m *Model) runSubagentAdoptSlash(parts []string) (string, error) {
	if m.session == nil || m.session.Sidecar == nil || strings.TrimSpace(m.session.ID) == "" {
		return "", fmt.Errorf("/adopt: no live parent session")
	}
	target, apply, err := parseSubagentAdoptArgs(parts)
	if err != nil {
		return "", err
	}
	act, err := m.resolveSubagentAdoptionTarget(target)
	if err != nil {
		return "", err
	}
	forkTree, err := parseSubagentForkTree(act.ForkTree)
	if err != nil {
		return "", err
	}
	child, err := stadogit.OpenSession(m.session.Sidecar, filepath.Dir(m.session.WorktreePath), act.ChildSession)
	if err != nil {
		return "", fmt.Errorf("/adopt: open child session: %w", err)
	}

	var plan runtime.SubagentAdoptionPlan
	if apply {
		plan, err = runtime.AdoptSubagentChanges(m.session, child, forkTree, "stado-tui-adopt", m.model)
	} else {
		plan, err = runtime.PlanSubagentAdoption(m.session, child, forkTree)
	}
	if apply && err == nil && plan.Applied {
		if tracked := m.findSubagentActivity(act.ChildSession); tracked != nil {
			tracked.AdoptionCommand = ""
			tracked.UpdatedAt = time.Now()
		}
	}
	body := renderSubagentAdoptionPlan(act.ChildSession, plan, apply)
	if errors.Is(err, runtime.ErrSubagentAdoptionConflict) {
		return body, fmt.Errorf("conflicts: %s", strings.Join(plan.Conflicts, ", "))
	}
	return body, err
}

func parseSubagentAdoptArgs(parts []string) (target string, apply bool, err error) {
	for _, part := range parts[1:] {
		switch part {
		case "--apply":
			apply = true
		case "--dry-run":
			apply = false
		default:
			if strings.HasPrefix(part, "-") {
				return "", false, fmt.Errorf("usage: /adopt [child-session] [--apply]")
			}
			if target != "" {
				return "", false, fmt.Errorf("usage: /adopt [child-session] [--apply]")
			}
			target = part
		}
	}
	return target, apply, nil
}

func (m *Model) resolveSubagentAdoptionTarget(target string) (subagentActivity, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		for i := len(m.subagents) - 1; i >= 0; i-- {
			act := m.subagents[i]
			if act.AdoptionCommand != "" {
				return act, nil
			}
		}
		return subagentActivity{}, fmt.Errorf("/adopt: no adoptable subagent activity")
	}

	var matches []subagentActivity
	for i := len(m.subagents) - 1; i >= 0; i-- {
		act := m.subagents[i]
		if act.ChildSession == target || strings.HasPrefix(act.ChildSession, target) {
			matches = append(matches, act)
		}
	}
	switch len(matches) {
	case 0:
		return subagentActivity{}, fmt.Errorf("/adopt: unknown child session %q", target)
	case 1:
		return matches[0], nil
	default:
		return subagentActivity{}, fmt.Errorf("/adopt: child session prefix %q is ambiguous", target)
	}
}

func (m *Model) findSubagentActivity(child string) *subagentActivity {
	child = strings.TrimSpace(child)
	for i := range m.subagents {
		if m.subagents[i].ChildSession == child {
			return &m.subagents[i]
		}
	}
	return nil
}

func parseSubagentForkTree(raw string) (plumbing.Hash, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return plumbing.ZeroHash, nil
	}
	if len(raw) != 40 {
		return plumbing.ZeroHash, fmt.Errorf("/adopt: fork tree must be a 40-character hash")
	}
	if _, err := hex.DecodeString(raw); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("/adopt: fork tree must be hex: %w", err)
	}
	return plumbing.NewHash(raw), nil
}

func renderSubagentAdoptionPlan(child string, plan runtime.SubagentAdoptionPlan, apply bool) string {
	status := "blocked"
	switch {
	case plan.Applied:
		status = "applied"
	case plan.CanAdopt:
		status = "ready"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "subagent adoption: %s", status)
	if child != "" {
		fmt.Fprintf(&b, "\nchild: %s", child)
	}
	if plan.ForkTree != "" {
		fmt.Fprintf(&b, "\nfork_tree: %s", plan.ForkTree)
	}
	writeSubagentPlanList(&b, "changed_files", plan.ChangedFiles)
	writeSubagentPlanList(&b, "parent_changed_files", plan.ParentChangedFiles)
	writeSubagentPlanList(&b, "conflicts", plan.Conflicts)
	if plan.Applied {
		writeSubagentPlanList(&b, "adopted_files", plan.AdoptedFiles)
		if plan.AdoptedTree != "" {
			fmt.Fprintf(&b, "\nadopted_tree: %s", plan.AdoptedTree)
		}
	} else if !apply {
		b.WriteString("\ndry_run: true")
		if plan.CanAdopt && len(plan.ChangedFiles) > 0 {
			fmt.Fprintf(&b, "\nrerun: /adopt %s --apply", shortSessionID(child))
		}
	}
	return b.String()
}

func writeSubagentPlanList(b *strings.Builder, title string, items []string) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "\n%s:", title)
	for _, item := range items {
		fmt.Fprintf(b, "\n  %s", item)
	}
}

func subagentRoleMode(act subagentActivity) string {
	role := strings.TrimSpace(act.Role)
	mode := strings.TrimSpace(act.Mode)
	switch {
	case role != "" && mode != "":
		return role + "/" + mode
	case role != "":
		return role
	default:
		return mode
	}
}

func subagentActivityTone(act subagentActivity) string {
	switch {
	case act.Status == "error":
		return "error"
	case act.Status == "running":
		return "warning"
	case act.ScopeViolations > 0:
		return "warning"
	case act.ChangedFiles > 0:
		return "accent"
	default:
		return "muted"
	}
}
