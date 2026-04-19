package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/tui"
	"github.com/foobarto/stado/pkg/agent"
)

var (
	runPrompt   string
	runMaxTurns int
	runJSON     bool
	runTools    bool
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Non-interactive: run a prompt through the agent loop to completion",
	Long: `Execute a prompt through the configured provider without opening the TUI.

Text streams to stdout (or JSON lines with --json). When --tools is set the
model can call stado's bundled tools (bash / fs / webfetch), and every call
is committed to the session's git-native audit log.

Exit codes: 0 success; 1 provider/IO error; 2 max-turns reached.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if runPrompt == "" && len(args) > 0 {
			runPrompt = strings.Join(args, " ")
		}
		if runPrompt == "" {
			return fmt.Errorf("run: --prompt (or positional) required")
		}

		cfg, err := config.Load()
		if err != nil {
			return err
		}
		prov, err := tui.BuildProvider(cfg)
		if err != nil {
			return fmt.Errorf("provider: %w", err)
		}

		var executor *runtime.AgentLoopOptions
		_ = executor // silence unused warning when --tools is off
		opts := runtime.AgentLoopOptions{
			Provider: prov,
			Model:    cfg.Defaults.Model,
			Messages: []agent.Message{agent.Text(agent.RoleUser, runPrompt)},
			MaxTurns: runMaxTurns,
			OnEvent:  emitter(runJSON, os.Stdout),
		}
		if runTools {
			cwd, _ := os.Getwd()
			sess, err := runtime.OpenSession(cfg, cwd)
			if err != nil {
				return fmt.Errorf("session: %w", err)
			}
			opts.Executor = runtime.BuildExecutor(sess, cfg, "stado-run")
			fmt.Fprintf(os.Stderr, "stado run: session %s (worktree %s)\n", sess.ID, sess.WorktreePath)
		}

		ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Minute)
		defer cancel()

		_, _, err = runtime.AgentLoop(ctx, opts)
		if err != nil {
			if strings.Contains(err.Error(), "exceeded") {
				fmt.Fprintln(os.Stderr, err.Error())
				os.Exit(2)
			}
			return err
		}
		if !runJSON {
			fmt.Fprintln(os.Stdout)
		}
		return nil
	},
}

// emitter returns an OnEvent callback that streams to out.
func emitter(jsonOut bool, out io.Writer) func(agent.Event) {
	return func(ev agent.Event) {
		switch ev.Kind {
		case agent.EvTextDelta:
			if jsonOut {
				enc, _ := json.Marshal(map[string]any{"type": "text", "text": ev.Text})
				fmt.Fprintln(out, string(enc))
			} else {
				fmt.Fprint(out, ev.Text)
			}
		case agent.EvThinkingDelta:
			if jsonOut {
				enc, _ := json.Marshal(map[string]any{"type": "thinking", "text": ev.Text})
				fmt.Fprintln(out, string(enc))
			}
		case agent.EvToolCallEnd:
			if ev.ToolCall != nil && jsonOut {
				enc, _ := json.Marshal(map[string]any{
					"type":  "tool_call",
					"name":  ev.ToolCall.Name,
					"input": string(ev.ToolCall.Input),
				})
				fmt.Fprintln(out, string(enc))
			} else if ev.ToolCall != nil {
				fmt.Fprintf(out, "\n▸ %s(%s)\n", ev.ToolCall.Name, string(ev.ToolCall.Input))
			}
		}
	}
}

func init() {
	runCmd.Flags().StringVar(&runPrompt, "prompt", "", "Prompt text (or provide as positional argument)")
	runCmd.Flags().IntVar(&runMaxTurns, "max-turns", 20, "Maximum agent turns before giving up")
	runCmd.Flags().BoolVar(&runJSON, "json", false, "Emit JSON lines instead of raw text")
	runCmd.Flags().BoolVar(&runTools, "tools", false, "Enable tool-calling (bash/fs/webfetch) with git-native audit")
	rootCmd.AddCommand(runCmd)
}
