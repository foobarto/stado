package bash

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/limitedio"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/tools/budget"
	"github.com/foobarto/stado/pkg/tool"
)

const maxBashCapturedOutputBytes = budget.BashBytes * 2

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

	stdout := limitedio.NewBuffer(maxBashCapturedOutputBytes)
	stderr := limitedio.NewBuffer(maxBashCapturedOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	err = cmd.Run()

	var out strings.Builder
	if stdout.Len() > 0 || stdout.Truncated() {
		appendCommandOutput(&out, stdout, "stdout")
	}
	if stderr.Len() > 0 || stderr.Truncated() {
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		appendCommandOutput(&out, stderr, "stderr")
	}
	if err != nil {
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		out.WriteString(err.Error())
	}

	return tool.Result{Content: budget.TruncateBashOutput(out.String(), budget.BashBytes)}, nil
}

func appendCommandOutput(out *strings.Builder, buf *limitedio.Buffer, label string) {
	out.WriteString(buf.String())
	if buf.Truncated() {
		if out.Len() > 0 && !strings.HasSuffix(out.String(), "\n") {
			out.WriteString("\n")
		}
		out.WriteString(fmt.Sprintf("[truncated: command %s exceeded %d bytes]\n", label, maxBashCapturedOutputBytes))
	}
}

type BashArgs struct {
	Command string `json:"command"`
}

// BuildShellCommand creates the actual `bash -c` child process. When a runner
// is present, the shell runs through the platform sandbox with access limited
// to the session worktree and /tmp. Network is denied by default for the
// sandboxed path.
func BuildShellCommand(ctx context.Context, runner sandbox.Runner, workdir, command string) (*exec.Cmd, error) {
	if runner == nil {
		cmd := exec.CommandContext(ctx, "bash", "-c", command) // #nosec G204 -- bash tool intentionally runs user-supplied shell commands.
		cmd.Dir = workdir
		return cmd, nil
	}

	policy := sandbox.Policy{
		FSRead:  []string{"/tmp"},
		FSWrite: []string{"/tmp"},
		Exec:    []string{"bash"},
		Net:     sandbox.NetPolicy{Kind: sandbox.NetDenyAll},
		CWD:     workdir,
	}
	if workdir != "" {
		policy.FSRead = append(policy.FSRead, workdir)
		policy.FSWrite = append(policy.FSWrite, workdir)
	}
	return runner.Command(ctx, policy, "bash", []string{"-c", command}, nil)
}
