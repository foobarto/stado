package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/instructions"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/telemetry"
	"github.com/foobarto/stado/internal/tui"
	"github.com/foobarto/stado/pkg/agent"
)

var (
	runPrompt    string
	runMaxTurns  int
	runJSON      bool
	runTools     bool
	runSandboxFS bool
	runSessionID string
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

		// Session-continuation path: --session <id-or-label> loads the
		// existing conversation and appends the new prompt. The reply
		// gets persisted back, so `stado` resume + TUI see the
		// exchange. Useful for scripted follow-ups on a long-running
		// session: `stado run --session react "what was that hook
		// we extracted?"`.
		var priorMsgs []agent.Message
		var continueSessID string
		var continueWorktree string
		if runSessionID != "" {
			resolved, err := resolveSessionID(cfg, runSessionID)
			if err != nil {
				return fmt.Errorf("run: --session: %w", err)
			}
			continueSessID = resolved
			continueWorktree = cfgWorktreeDirPath(cfg, resolved)
			priorMsgs, err = runtime.LoadConversation(continueWorktree)
			if err != nil {
				return fmt.Errorf("run: load conversation for %s: %w", resolved, err)
			}
			fmt.Fprintf(os.Stderr,
				"stado run: continuing session %s (%d prior message(s))\n",
				resolved, len(priorMsgs))
		}

		newUserMsg := agent.Text(agent.RoleUser, runPrompt)
		var executor *runtime.AgentLoopOptions
		_ = executor // silence unused warning when --tools is off

		// Project-level instructions (AGENTS.md / CLAUDE.md) resolved
		// from the current working directory. A missing file is fine;
		// a broken one is surfaced as a stderr warning and the run
		// proceeds without a system prompt rather than aborting.
		sysPrompt := ""
		if cwd, cwdErr := os.Getwd(); cwdErr == nil {
			if res, err := instructions.Load(cwd); err != nil {
				fmt.Fprintf(os.Stderr, "stado run: instructions load: %v\n", err)
			} else if res.Path != "" {
				sysPrompt = res.Content
				fmt.Fprintf(os.Stderr, "stado run: loaded %s\n", res.Path)
			}
		}

		opts := runtime.AgentLoopOptions{
			Provider:             prov,
			Model:                cfg.Defaults.Model,
			Messages:             append(priorMsgs, newUserMsg),
			MaxTurns:             runMaxTurns,
			OnEvent:              emitter(runJSON, os.Stdout),
			Thinking:             cfg.Agent.Thinking,
			ThinkingBudgetTokens: cfg.Agent.ThinkingBudgetTokens,
			System:               sysPrompt,
		}
		if runTools {
			cwd, _ := os.Getwd()
			sess, err := runtime.OpenSession(cfg, cwd)
			if err != nil {
				return fmt.Errorf("session: %w", err)
			}
			opts.Executor = runtime.BuildExecutor(sess, cfg, "stado-run")
			fmt.Fprintf(os.Stderr, "stado run: session %s (worktree %s)\n", sess.ID, sess.WorktreePath)

			if runSandboxFS {
				// Narrow our own process so mutating tools can only touch
				// the worktree + /tmp. Read-anywhere stays permitted so
				// globs/greps/reads still work across the repo.
				if err := sandbox.ApplyLandlock(sandbox.WorktreeWrite(sess.WorktreePath)); err != nil {
					if errors.Is(err, sandbox.ErrLandlockUnavailable) {
						fmt.Fprintln(os.Stderr, "stado run: --sandbox-fs requested but landlock unavailable on this kernel; continuing unsandboxed")
					} else {
						return fmt.Errorf("sandbox: %w", err)
					}
				} else {
					fmt.Fprintln(os.Stderr, "stado run: landlock applied (writes confined to worktree + /tmp)")
				}
			}
		}

		// Wrap the CLI's context with any `.stado-span-context`
		// present in cwd (cross-process span link, Phase 9.4/9.5).
		// Non-forked cwd is a no-op.
		cwd, _ := os.Getwd()
		baseCtx, _ := telemetry.LoadParentTraceparent(cmd.Context(), cwd)
		ctx, cancel := context.WithTimeout(baseCtx, 10*time.Minute)
		defer cancel()

		_, finalMsgs, err := runtime.AgentLoop(ctx, opts)
		if err != nil {
			if strings.Contains(err.Error(), "exceeded") {
				fmt.Fprintln(os.Stderr, err.Error())
				os.Exit(2)
			}
			return err
		}
		// Persist the session-continuation exchange. priorMsgs was
		// the prefix; finalMsgs is that prefix + the new user msg +
		// whatever assistant/tool turns came back. Slice off the
		// prefix and append each new message so the TUI replay sees
		// the full flow next time it resumes.
		if continueWorktree != "" && continueSessID != "" {
			for i, m := range finalMsgs {
				if i < len(priorMsgs) {
					continue
				}
				if err := runtime.AppendMessage(continueWorktree, m); err != nil {
					fmt.Fprintf(os.Stderr, "stado run: persist message %d: %v\n", i, err)
				}
			}
		}
		if !runJSON {
			fmt.Fprintln(os.Stdout)
		}
		return nil
	},
}

// cfgWorktreeDirPath is a shorthand used only by run.go's session
// continuation path. Inlined helper keeps the main command body
// readable without pulling filepath into the import list here
// (it's already in session.go).
func cfgWorktreeDirPath(cfg *config.Config, id string) string {
	return cfg.WorktreeDir() + "/" + id
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
	runCmd.Flags().BoolVar(&runSandboxFS, "sandbox-fs", false, "Apply landlock: confine writes to the session worktree + /tmp (Linux only)")
	runCmd.Flags().StringVar(&runSessionID, "session", "",
		"Continue an existing session: prior conversation is loaded, the new prompt appended, and the exchange persisted. Accepts uuid, uuid-prefix (≥8 chars), or description substring.")
	rootCmd.AddCommand(runCmd)
}
