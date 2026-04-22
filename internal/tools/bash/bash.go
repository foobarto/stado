package bash

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/tools/budget"
	"github.com/foobarto/stado/pkg/tool"
)

type BashTool struct {
	Timeout time.Duration
}

func (BashTool) Name() string        { return "bash" }
func (BashTool) Description() string { return "Execute a shell command" }
func (BashTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string", "description": "Shell command to execute"},
		},
		"required": []string{"command"},
	}
}

func (t BashTool) Run(ctx context.Context, args json.RawMessage, h tool.Host) (tool.Result, error) {
	var p BashArgs
	if err := json.Unmarshal(args, &p); err != nil {
		return tool.Result{Error: err.Error()}, err
	}

	timeout := t.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	approval, err := h.Approve(ctx, tool.ApprovalRequest{
		Tool:    "bash",
		Command: p.Command,
	})
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	if approval == tool.DecisionDeny {
		return tool.Result{Error: "command execution denied by user"}, nil
	}

	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var runner sandbox.Runner
	if rh, ok := h.(interface{ Runner() sandbox.Runner }); ok {
		runner = rh.Runner()
	}
	cmd, err := BuildShellCommand(cmdCtx, runner, h.Workdir(), p.Command)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()

	var out strings.Builder
	if stdout.Len() > 0 {
		out.WriteString(stdout.String())
	}
	if stderr.Len() > 0 {
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		out.WriteString(stderr.String())
	}
	if err != nil {
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		out.WriteString(err.Error())
	}

	return tool.Result{Content: budget.TruncateBashOutput(out.String(), budget.BashBytes)}, nil
}

type BashArgs struct {
	Command string `json:"command"`
}

// BuildShellCommand creates the actual `bash -c` child process. When a runner
// is present, the shell runs through the platform sandbox with access limited
// to the session worktree and /tmp.
func BuildShellCommand(ctx context.Context, runner sandbox.Runner, workdir, command string) (*exec.Cmd, error) {
	if runner == nil {
		cmd := exec.CommandContext(ctx, "bash", "-c", command)
		cmd.Dir = workdir
		return cmd, nil
	}

	policy := sandbox.Policy{
		FSRead:  []string{"/tmp"},
		FSWrite: []string{"/tmp"},
		Exec:    []string{"bash"},
		Net:     sandbox.NetPolicy{Kind: sandbox.NetAllowAll},
		CWD:     workdir,
	}
	if workdir != "" {
		policy.FSRead = append(policy.FSRead, workdir)
		policy.FSWrite = append(policy.FSWrite, workdir)
	}
	return runner.Command(ctx, policy, "bash", []string{"-c", command})
}
