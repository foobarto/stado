package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

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
	OnEvent   func(SubagentEvent)
}

// SubagentEvent is emitted at child lifecycle boundaries so outer
// orchestration surfaces can notify users without parsing tool JSON.
type SubagentEvent struct {
	Phase           string
	ParentSession   string
	ChildSession    string
	Worktree        string
	Role            string
	Mode            string
	Status          string
	TimeoutSeconds  int
	ForkTree        string
	ChangedFiles    []string
	ScopeViolations []string
	Error           string
}

// AdoptionCommand returns a copy-pasteable command for applying child
// changes into the parent session when the event contains adoptable output.
func (ev SubagentEvent) AdoptionCommand() string {
	return subagentAdoptionCommand(ev.ParentSession, ev.ChildSession, ev.ForkTree, ev.ChangedFiles)
}

func subagentAdoptionCommand(parentID, childID, forkTree string, changedFiles []string) string {
	if parentID == "" || childID == "" || len(changedFiles) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("stado session adopt ")
	b.WriteString(parentID)
	b.WriteString(" ")
	b.WriteString(childID)
	if forkTree != "" {
		b.WriteString(" --fork-tree ")
		b.WriteString(forkTree)
	}
	b.WriteString(" --apply")
	return b.String()
}

func (r SubagentRunner) SpawnSubagent(ctx context.Context, req subagent.Request) (subagent.Result, error) {
	req, err := prepareSubagentRequest(req)
	if err != nil {
		return subagent.Result{}, err
	}
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
	baseTree, err := child.CurrentTree()
	if err != nil {
		return subagent.Result{}, fmt.Errorf("spawn_agent: child base tree: %w", err)
	}
	r.emitSubagentEvent(req, child, "started", "running", "")

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
		err = fmt.Errorf("spawn_agent: seed child conversation: %w", err)
		r.emitSubagentEvent(req, child, "finished", "error", err.Error())
		return subagent.Result{}, err
	}

	exec, err := BuildExecutor(child, r.Config, agentName)
	if err != nil {
		err = fmt.Errorf("spawn_agent: child tools: %w", err)
		r.emitSubagentEvent(req, child, "finished", "error", err.Error())
		return subagent.Result{}, err
	}
	childHost, scopedHost, err := configureSubagentTools(req, exec)
	if err != nil {
		err = fmt.Errorf("spawn_agent: child tools: %w", err)
		r.emitSubagentEvent(req, child, "finished", "error", err.Error())
		return subagent.Result{}, err
	}

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
		Host:                 childHost,
	})
	if appendErr := appendSubagentMessages(child.WorktreePath, msgs, len(seed)); appendErr != nil && err == nil {
		err = appendErr
	}
	if err != nil {
		if errors.Is(childCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			result := subagentResult(req, child, text, msgs)
			result.Status = "timeout"
			result.Error = fmt.Sprintf("child timed out after %d second(s)", req.TimeoutSeconds)
			if detailErr := attachWorkerResultDetails(&result, req, child, baseTree, scopedHost); detailErr != nil {
				result.Error += "; " + detailErr.Error()
			}
			r.attachWorkerAdoptionCommand(&result)
			_, _ = child.CommitToTrace(stadogit.CommitMeta{
				Tool:     subagent.ToolName,
				ShortArg: "timeout",
				Summary:  result.Error,
				Model:    r.Model,
				Agent:    agentName,
				Turn:     child.Turn(),
				Error:    err.Error(),
			})
			r.emitSubagentResultEvent(req, child, result)
			return result, nil
		}
		err = fmt.Errorf("spawn_agent: child %s: %w", child.ID, err)
		r.emitSubagentEvent(req, child, "finished", "error", err.Error())
		return subagent.Result{}, err
	}

	result := subagentResult(req, child, text, msgs)
	if err := attachWorkerResultDetails(&result, req, child, baseTree, scopedHost); err != nil {
		err = fmt.Errorf("spawn_agent: worker result: %w", err)
		r.emitSubagentEvent(req, child, "finished", "error", err.Error())
		return subagent.Result{}, err
	}
	r.attachWorkerAdoptionCommand(&result)
	r.emitSubagentResultEvent(req, child, result)
	return result, nil
}

func prepareSubagentRequest(req subagent.Request) (subagent.Request, error) {
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.Role = strings.TrimSpace(req.Role)
	req.Mode = strings.TrimSpace(req.Mode)
	req.Ownership = strings.TrimSpace(req.Ownership)
	if req.Prompt == "" {
		return subagent.Request{}, fmt.Errorf("spawn_agent: prompt is required")
	}
	req = normalizeSubagentRequest(req)
	writeScope, err := subagent.NormalizeWriteScope(req.WriteScope)
	if err != nil {
		return subagent.Request{}, fmt.Errorf("spawn_agent: write_scope: %w", err)
	}
	req.WriteScope = writeScope
	switch {
	case req.Role == subagent.DefaultRole && req.Mode == subagent.DefaultMode:
		return req, nil
	case req.Role == subagent.WorkerRole && req.Mode == subagent.WorkspaceWriteMode:
		if req.Ownership == "" {
			return subagent.Request{}, fmt.Errorf("spawn_agent: ownership is required for %s", subagent.WorkspaceWriteMode)
		}
		if len(req.WriteScope) == 0 {
			return subagent.Request{}, fmt.Errorf("spawn_agent: write_scope is required for %s", subagent.WorkspaceWriteMode)
		}
		return req, nil
	default:
		return subagent.Request{}, fmt.Errorf("spawn_agent: role %q with mode %q is not supported", req.Role, req.Mode)
	}
}

func (r SubagentRunner) emitSubagentEvent(req subagent.Request, child *stadogit.Session, phase, status, errMsg string) {
	if r.OnEvent == nil || child == nil {
		return
	}
	parentID := ""
	if r.Parent != nil {
		parentID = r.Parent.ID
	}
	r.OnEvent(SubagentEvent{
		Phase:          phase,
		ParentSession:  parentID,
		ChildSession:   child.ID,
		Worktree:       child.WorktreePath,
		Role:           req.Role,
		Mode:           req.Mode,
		Status:         status,
		TimeoutSeconds: req.TimeoutSeconds,
		Error:          errMsg,
	})
}

func (r SubagentRunner) emitSubagentResultEvent(req subagent.Request, child *stadogit.Session, result subagent.Result) {
	if r.OnEvent == nil || child == nil {
		return
	}
	parentID := ""
	if r.Parent != nil {
		parentID = r.Parent.ID
	}
	r.OnEvent(SubagentEvent{
		Phase:           "finished",
		ParentSession:   parentID,
		ChildSession:    child.ID,
		Worktree:        child.WorktreePath,
		Role:            req.Role,
		Mode:            req.Mode,
		Status:          result.Status,
		TimeoutSeconds:  req.TimeoutSeconds,
		ForkTree:        result.ForkTree,
		ChangedFiles:    append([]string(nil), result.ChangedFiles...),
		ScopeViolations: append([]string(nil), result.ScopeViolations...),
		Error:           result.Error,
	})
}

func (r SubagentRunner) attachWorkerAdoptionCommand(result *subagent.Result) {
	if result == nil || r.Parent == nil {
		return
	}
	result.AdoptionCommand = subagentAdoptionCommand(
		r.Parent.ID,
		result.ChildSession,
		result.ForkTree,
		result.ChangedFiles,
	)
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

func configureSubagentTools(req subagent.Request, exec *tools.Executor) (tool.Host, *subagent.ScopedWriteHost, error) {
	if req.Mode == subagent.WorkspaceWriteMode {
		keepWorkspaceWriteTools(exec.Registry)
		scopedHost, err := subagent.NewScopedWriteHost(autoApproveHost{
			workdir: exec.Session.WorktreePath,
			readLog: exec.ReadLog,
			runner:  exec.Runner,
		}, req.WriteScope)
		if err != nil {
			return nil, nil, err
		}
		return scopedHost, scopedHost, nil
	}
	keepReadOnlyTools(exec.Registry)
	return nil, nil, nil
}

func attachWorkerResultDetails(result *subagent.Result, req subagent.Request, child *stadogit.Session, baseTree plumbing.Hash, scopedHost *subagent.ScopedWriteHost) error {
	if req.Mode != subagent.WorkspaceWriteMode {
		return nil
	}
	result.ForkTree = hashString(baseTree)
	if scopedHost != nil {
		result.ScopeViolations = scopedHost.ScopeViolations()
	}
	currentTree, err := child.CurrentTree()
	if err != nil {
		return err
	}
	changed, err := child.ChangedFilesBetween(baseTree, currentTree)
	if err != nil {
		return err
	}
	result.ChangedFiles = reportableWorkerChangedFiles(changed)
	return nil
}

func reportableWorkerChangedFiles(files []string) []string {
	out := make([]string, 0, len(files))
	for _, file := range files {
		switch {
		case file == ".stado-pid" || file == ".stado-span-context":
			continue
		case file == ".git" || strings.HasPrefix(file, ".git/"):
			continue
		case file == ".stado" || strings.HasPrefix(file, ".stado/"):
			continue
		default:
			out = append(out, file)
		}
	}
	return out
}

func keepReadOnlyTools(reg *tools.Registry) {
	if reg == nil {
		return
	}
	for _, t := range reg.All() {
		name := t.Name()
		if reg.ClassOf(name) != tool.ClassNonMutating {
			reg.Unregister(name)
		}
	}
}

func keepWorkspaceWriteTools(reg *tools.Registry) {
	if reg == nil {
		return
	}
	allowed := map[string]struct{}{
		"read":              {},
		"glob":              {},
		"grep":              {},
		"ripgrep":           {},
		"read_with_context": {},
		"find_definition":   {},
		"find_references":   {},
		"document_symbols":  {},
		"hover":             {},
		"write":             {},
		"edit":              {},
	}
	for _, t := range reg.All() {
		if _, ok := allowed[t.Name()]; !ok {
			reg.Unregister(t.Name())
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
	if req.Mode == subagent.WorkspaceWriteMode {
		b.WriteString("You are a scoped worker agent spawned by a parent stado session.\n")
		b.WriteString("Make only the requested changes and keep your final response concise.\n")
		b.WriteString("You may write only paths inside Write scope. Do not run shell commands or edit files outside scope.\n\n")
	} else {
		b.WriteString("You are a read-only sidecar agent spawned by a parent stado session.\n")
		b.WriteString("Return concise findings for the parent. Include file paths, line numbers, and uncertainties when relevant.\n")
		b.WriteString("Do not edit files, run mutating commands, or make recommendations that depend on changes you did not verify.\n\n")
	}
	b.WriteString("Role: ")
	b.WriteString(req.Role)
	b.WriteString("\nMode: ")
	b.WriteString(req.Mode)
	if req.Ownership != "" {
		b.WriteString("\nOwnership: ")
		b.WriteString(req.Ownership)
	}
	if len(req.WriteScope) > 0 {
		b.WriteString("\nWrite scope:")
		for _, scope := range req.WriteScope {
			b.WriteString("\n- ")
			b.WriteString(scope)
		}
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
