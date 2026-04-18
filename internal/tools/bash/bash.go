package bash

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"

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

	cmd := exec.CommandContext(cmdCtx, "bash", "-c", p.Command)
	cmd.Dir = h.Workdir()

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

	return tool.Result{Content: out.String()}, nil
}

type BashArgs struct {
	Command string `json:"command"`
}
