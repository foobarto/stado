package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/config"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/subagent"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/foobarto/stado/pkg/tool"
)

// SubagentRunner is the runtime-side implementation behind the spawn_agent
// tool. It deliberately runs children synchronously from the parent tool call:
// the parent session is not re-entered, and the child has its own forked
// worktree, conversation log, trace commits, and turn boundary.
type SubagentRunner struct {
	Config *config.Config
	Parent *stadogit.Session

	Provider agent.Provider
	Model    string

	Thinking             string
	ThinkingBudgetTokens int
	System               string
	SystemTemplate       string

	AgentName string
}

func (r SubagentRunner) SpawnSubagent(ctx context.Context, req subagent.Request) (subagent.Result, error) {
	req = normalizeSubagentRequest(req)
	if r.Config == nil {
		return subagent.Result{}, fmt.Errorf("spawn_agent: config required")
	}
	if r.Parent == nil || r.Parent.Sidecar == nil {
		return subagent.Result{}, fmt.Errorf("spawn_agent: parent session required")
	}
	if r.Provider == nil {
		return subagent.Result{}, fmt.Errorf("spawn_agent: provider required")
	}
	child, err := ForkSession(r.Config, r.Parent)
	if err != nil {
		return subagent.Result{}, fmt.Errorf("spawn_agent: fork child session: %w", err)
	}

	agentName := r.AgentName
	if agentName == "" {
		agentName = "stado-subagent"
	}
	_, _ = child.CommitToTrace(stadogit.CommitMeta{
		Tool:     subagent.ToolName,
		ShortArg: req.Role,
		Summary:  trimForSubagentCommit(req.Prompt, 72),
		Model:    r.Model,
		Agent:    agentName,
		Turn:     child.Turn(),
	})

	seed := []agent.Message{agent.Text(agent.RoleUser, renderSubagentPrompt(req))}
	if err := WriteConversation(child.WorktreePath, seed); err != nil {
		return subagent.Result{}, fmt.Errorf("spawn_agent: seed child conversation: %w", err)
	}

	exec, err := BuildExecutor(child, r.Config, agentName)
	if err != nil {
		return subagent.Result{}, fmt.Errorf("spawn_agent: child tools: %w", err)
	}
	keepReadOnlyTools(exec.Registry)

	childCtx, cancel := context.WithTimeout(ctx, time.Duration(req.TimeoutSeconds)*time.Second)
	defer cancel()

	text, msgs, err := AgentLoop(childCtx, AgentLoopOptions{
		Provider:             r.Provider,
		Executor:             exec,
		Model:                r.Model,
		Messages:             seed,
		MaxTurns:             req.MaxTurns,
		Thinking:             r.Thinking,
		ThinkingBudgetTokens: r.ThinkingBudgetTokens,
		System:               r.System,
		SystemTemplate:       r.SystemTemplate,
	})
	if appendErr := appendSubagentMessages(child.WorktreePath, msgs, len(seed)); appendErr != nil && err == nil {
		err = appendErr
	}
	if err != nil {
		if errors.Is(childCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			result := subagentResult(req, child, text, msgs)
			result.Status = "timeout"
			result.Error = fmt.Sprintf("child timed out after %d second(s)", req.TimeoutSeconds)
			_, _ = child.CommitToTrace(stadogit.CommitMeta{
				Tool:     subagent.ToolName,
				ShortArg: "timeout",
				Summary:  result.Error,
				Model:    r.Model,
				Agent:    agentName,
				Turn:     child.Turn(),
				Error:    err.Error(),
			})
			return result, nil
		}
		return subagent.Result{}, fmt.Errorf("spawn_agent: child %s: %w", child.ID, err)
	}

	return subagentResult(req, child, text, msgs), nil
}

func normalizeSubagentRequest(req subagent.Request) subagent.Request {
	if req.Role == "" {
		req.Role = subagent.DefaultRole
	}
	if req.Mode == "" {
		req.Mode = subagent.DefaultMode
	}
	if req.MaxTurns <= 0 {
		req.MaxTurns = subagent.DefaultTurns
	}
	if req.MaxTurns > subagent.MaxTurns {
		req.MaxTurns = subagent.MaxTurns
	}
	if req.TimeoutSeconds <= 0 {
		req.TimeoutSeconds = subagent.DefaultTimeoutSeconds
	}
	if req.TimeoutSeconds > subagent.MaxTimeoutSeconds {
		req.TimeoutSeconds = subagent.MaxTimeoutSeconds
	}
	return req
}

func subagentResult(req subagent.Request, child *stadogit.Session, text string, msgs []agent.Message) subagent.Result {
	return subagent.Result{
		Status:         "completed",
		Role:           req.Role,
		Mode:           req.Mode,
		ChildSession:   child.ID,
		Worktree:       child.WorktreePath,
		Text:           strings.TrimSpace(text),
		MessageCount:   len(msgs),
		TimeoutSeconds: req.TimeoutSeconds,
	}
}

func keepReadOnlyTools(reg *tools.Registry) {
	if reg == nil {
		return
	}
	for _, t := range reg.All() {
		name := t.Name()
		if name == subagent.ToolName || reg.ClassOf(name) != tool.ClassNonMutating {
			reg.Unregister(name)
		}
	}
}

func appendSubagentMessages(worktree string, msgs []agent.Message, persisted int) error {
	if len(msgs) <= persisted {
		return nil
	}
	_, err := AppendMessagesFrom(worktree, msgs, persisted)
	return err
}

func renderSubagentPrompt(req subagent.Request) string {
	var b strings.Builder
	b.WriteString("You are a read-only sidecar agent spawned by a parent stado session.\n")
	b.WriteString("Return concise findings for the parent. Include file paths, line numbers, and uncertainties when relevant.\n")
	b.WriteString("Do not edit files, run mutating commands, or make recommendations that depend on changes you did not verify.\n\n")
	b.WriteString("Role: ")
	b.WriteString(req.Role)
	b.WriteString("\nMode: ")
	b.WriteString(req.Mode)
	if req.Ownership != "" {
		b.WriteString("\nOwnership: ")
		b.WriteString(req.Ownership)
	}
	b.WriteString("\n\nTask:\n")
	b.WriteString(req.Prompt)
	return b.String()
}

func trimForSubagentCommit(s string, max int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 0 {
		return "..."
	}
	if max <= 3 {
		return string(r[:max])
	}
	return string(r[:max-3]) + "..."
}
