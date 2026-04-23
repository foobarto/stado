package bash

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/tools/budget"
	"github.com/foobarto/stado/pkg/tool"
)

type nullHost struct{}

func (nullHost) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}
func (nullHost) Workdir() string { return "" }
func (nullHost) PriorRead(tool.ReadKey) (tool.PriorReadInfo, bool) {
	return tool.PriorReadInfo{}, false
}
func (nullHost) RecordRead(tool.ReadKey, tool.PriorReadInfo) {}

type runnerHost struct {
	nullHost
	runner sandbox.Runner
}

func (h runnerHost) Runner() sandbox.Runner { return h.runner }

type fakeRunner struct {
	called bool
	name   string
	args   []string
	cwd    string
}

func (f *fakeRunner) Name() string    { return "fake" }
func (f *fakeRunner) Available() bool { return true }
func (f *fakeRunner) Command(ctx context.Context, p sandbox.Policy, name string, args []string, env []string) (*exec.Cmd, error) {
	f.called = true
	f.name = name
	f.args = append([]string{}, args...)
	f.cwd = p.CWD
	return exec.CommandContext(ctx, "bash", "-c", "printf runner-path"), nil
}

// TestBashTruncatesLargeOutput fires a command that prints >BashBytes and
// asserts the head+tail elision shape from TruncateBashOutput lands.
func TestBashTruncatesLargeOutput(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	// Produce a block well over the budget. `yes` is fast and portable
	// enough on Linux/macOS runners; if it's missing, skip.
	if _, err := exec.LookPath("yes"); err != nil {
		t.Skip("yes not on PATH")
	}

	raw, _ := json.Marshal(map[string]any{
		// 80K of "y\n" → well over the 32K cap.
		"command": "yes y | head -c 80000",
	})
	res, err := BashTool{}.Run(context.Background(), raw, nullHost{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Content) > budget.BashBytes+256 {
		t.Errorf("result exceeds budget: %d > %d", len(res.Content), budget.BashBytes+256)
	}
	if !strings.Contains(res.Content, "bytes elided from the middle") {
		t.Errorf("bash middle-elide marker missing: %q",
			res.Content[max0(len(res.Content)-400):])
	}
	// Head+tail shape — the start and end of the output should both
	// carry content ("y"s) rather than one end being lost.
	if !strings.HasPrefix(res.Content, "y") {
		t.Errorf("head not retained: %q", res.Content[:40])
	}
}

// TestBashNoTruncationSmallOutput checks the no-op path.
func TestBashNoTruncationSmallOutput(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	raw, _ := json.Marshal(map[string]any{"command": "echo hi"})
	res, err := BashTool{}.Run(context.Background(), raw, nullHost{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(res.Content, "bytes elided") {
		t.Errorf("unexpected truncation marker on small output: %q", res.Content)
	}
	if !strings.Contains(res.Content, "hi") {
		t.Errorf("expected 'hi' in output: %q", res.Content)
	}
}

func TestBashUsesRunnerWhenHostProvidesOne(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	raw, _ := json.Marshal(map[string]any{"command": "echo ignored"})
	r := &fakeRunner{}
	res, err := BashTool{}.Run(context.Background(), raw, runnerHost{runner: r})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !r.called {
		t.Fatal("expected runner to be used")
	}
	if r.name != "bash" {
		t.Fatalf("runner name = %q, want bash", r.name)
	}
	if len(r.args) != 2 || r.args[0] != "-c" || r.args[1] != "echo ignored" {
		t.Fatalf("runner args = %v", r.args)
	}
	if !strings.Contains(res.Content, "runner-path") {
		t.Fatalf("runner output missing from result: %q", res.Content)
	}
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
