package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/runtime"
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
