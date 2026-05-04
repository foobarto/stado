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
	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/internal/instructions"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/skills"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/telemetry"
	"github.com/foobarto/stado/internal/tui"
	"github.com/foobarto/stado/pkg/agent"
)

var (
	runPrompt    string
	runMaxTurns  int
	runJSON      bool
	runQuiet     bool
	runTools     bool
	runSandboxFS bool
	runSessionID string
	runSkill     string
)

var (
	runLoadConfig    = config.Load
	runBuildProvider = tui.BuildProvider
	runAgentLoop     = runtime.AgentLoop
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Non-interactive: run a prompt through the agent loop to completion",
	Long: `Execute a prompt through the configured provider without opening the TUI.

By default, agent text streams to stdout and tool-call previews
("▸ tool(...)" lines) interleave with it. INFO log lines like
"stado.commit ref=..." go to stderr.

For scripted use, two modes strip the noise:

  --json     One JSON object per line on stdout (text / thinking / tool_call).
             The canonical scripted-parse mode — preferred for piping into
             jq, awk, or any structured consumer.
  --quiet    Plain text only on stdout — tool-call previews are suppressed.
             Tools still run and are still committed to the audit log; they
             just don't print. Use when you want the answer body with no
             extra lines.

When --tools is set the model can call stado's bundled toolset, and every
call is committed to the session's git-native audit log regardless of the
output mode.

Exit codes: 0 success; 1 provider/IO error; 2 max-turns reached.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if runPrompt == "" && len(args) > 0 {
			runPrompt = strings.Join(args, " ")
		}
		if err := resolveRunPromptFromFlags(); err != nil {
			return err
		}
		if runPrompt == "" {
			return fmt.Errorf("run: --prompt (or positional) or --skill required")
		}

		cfg, err := runLoadConfig()
		if err != nil {
			return err
		}
		// Root --provider/--model are persistent flags; honour them
		// here too so `stado run --provider ollama-cloud --model
		// kimi-k2.6 --prompt …` works without editing config.toml.
		applyRootProviderOverrides(cfg)
		return withTelemetry(cmd.Context(), cfg, func(runCtx context.Context) error {
			prov, err := runBuildProvider(cfg)
			if err != nil {
				return fmt.Errorf("provider: %w", err)
			}
			hookRunner := hooks.Runner{
				PostTurnCmd: cfg.Hooks.PostTurn,
				Disabled:    hooks.DisabledByToolConfig(cfg),
			}

			var priorMsgs []agent.Message
			var continueSessID string
			var continueWorktree string
			var continueSession *stadogit.Session
			if runSessionID != "" {
				resolved, err := resolveSessionID(cfg, runSessionID)
				if err != nil {
					return fmt.Errorf("run: --session: %w", err)
				}
				_, sess, err := openPersistedSession(cfg, resolved)
				if err != nil {
					return fmt.Errorf("run: open session %s: %w", resolved, err)
				}
				continueSessID = resolved
				continueSession = sess
				continueWorktree = sess.WorktreePath
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
			_ = executor

			sysPrompt := ""
			promptWorkdir := ""
			if cwd, cwdErr := os.Getwd(); cwdErr == nil {
				promptWorkdir = cwd
				if res, err := instructions.Load(cwd); err != nil {
					fmt.Fprintf(os.Stderr, "stado run: instructions load: %v\n", err)
				} else if res.Path != "" {
					sysPrompt = res.Content
					fmt.Fprintf(os.Stderr, "stado run: loaded %s\n", res.Path)
					if !instructions.TemplateInjectsProjectInstructions(cfg.Agent.SystemPromptTemplate) {
						fmt.Fprintf(os.Stderr,
							"stado run: warning — system prompt template at %s does not include {{ .ProjectInstructions }}; project rules from %s will not reach the model. Add the block or delete the file to regenerate the default.\n",
							cfg.Agent.SystemPromptPath, res.Path)
					}
				}
			}
			if continueWorktree != "" {
				promptWorkdir = continueWorktree
			}
			memoryContext := buildMemoryPromptContext(cmd.Context(), cfg, promptWorkdir, continueSessID, runPrompt)

			opts := runtime.AgentLoopOptions{
				Provider: prov,
				Config:   cfg,
				Model:    cfg.Defaults.Model,
				Messages: append(priorMsgs, newUserMsg),
				MaxTurns: runMaxTurns,
				OnEvent:  emitter(runJSON, runQuiet, os.Stdout),
				OnTurnComplete: func(turnIndex int, text string, _ []agent.ToolUseBlock, usage agent.Usage, duration time.Duration) {
					hookRunner.FirePostTurn(runCtx, hooks.NewPostTurnPayload(turnIndex, usage, text, duration))
				},
				Thinking:             cfg.Agent.Thinking,
				ThinkingBudgetTokens: cfg.Agent.ThinkingBudgetTokens,
				System:               sysPrompt,
				SystemTemplate:       cfg.Agent.SystemPromptTemplate,
				MemoryContext:        memoryContext,
				CostCapUSD:           cfg.Budget.HardUSD,
			}
			if runTools {
				cwd, _ := os.Getwd()
				toolWorktree := cwd
				if continueWorktree != "" {
					toolWorktree = continueWorktree
				}
				sess := continueSession
				if sess == nil {
					var err error
					sess, err = runtime.OpenSession(cfg, toolWorktree)
					if err != nil {
						return fmt.Errorf("session: %w", err)
					}
				}
				opts.Executor, err = runtime.BuildExecutor(sess, cfg, "stado-run")
				if err != nil {
					return fmt.Errorf("tools: %w", err)
				}
				fmt.Fprintf(os.Stderr, "stado run: session %s (worktree %s)\n", sess.ID, sess.WorktreePath)

				if runSandboxFS {
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

			cwd, _ := os.Getwd()
			baseCtx, _ := telemetry.LoadParentTraceparent(runCtx, cwd)
			ctx, cancel := context.WithTimeout(baseCtx, 10*time.Minute)
			defer cancel()

			_, finalMsgs, err := runAgentLoop(ctx, opts)
			if err != nil {
				if errors.Is(err, runtime.ErrCostCapExceeded) {
					fmt.Fprintln(os.Stderr, "stado run: "+err.Error())
					fmt.Fprintln(os.Stderr, "  raise [budget].hard_usd in config.toml or pass a larger budget to continue.")
					os.Exit(2)
				}
				if strings.Contains(err.Error(), "exceeded") {
					fmt.Fprintln(os.Stderr, err.Error())
					os.Exit(2)
				}
				return err
			}
			if continueWorktree != "" && continueSessID != "" {
				for i, m := range finalMsgs {
					if i < len(priorMsgs) {
						continue
					}
					if err := runtime.AppendMessage(continueWorktree, m); err != nil {
						fmt.Fprintf(os.Stderr, "stado run: persist message %d: %v\n", i, err)
					}
				}
				if continueSession != nil && opts.Executor == nil {
					if err := continueSession.NextTurn(); err != nil {
						return fmt.Errorf("run: turn boundary for %s: %w", continueSessID, err)
					}
				}
			}
			if !runJSON {
				fmt.Fprintln(os.Stdout)
			}
			return nil
		})
	},
}

// emitter returns an OnEvent callback that streams to out.
//
// jsonOut: emit one JSON object per event (text/thinking/tool_call).
// quiet: in non-JSON mode, suppress the "▸ tool(args)" tool-call preview
// lines so stdout carries only agent text. Has no effect under jsonOut —
// JSON output is already structured and machine-parseable. Tools still
// fire and still commit to the audit log; only the stdout preview is
// elided.
func emitter(jsonOut, quiet bool, out io.Writer) func(agent.Event) {
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
			if ev.ToolCall == nil {
				return
			}
			if jsonOut {
				enc, _ := json.Marshal(map[string]any{
					"type":  "tool_call",
					"name":  ev.ToolCall.Name,
					"input": string(ev.ToolCall.Input),
				})
				fmt.Fprintln(out, string(enc))
			} else if !quiet {
				fmt.Fprintf(out, "\n▸ %s(%s)\n", ev.ToolCall.Name, string(ev.ToolCall.Input))
			}
		}
	}
}

func init() {
	runCmd.Flags().StringVar(&runPrompt, "prompt", "", "Prompt text (or provide as positional argument)")
	runCmd.Flags().StringVar(&runSkill, "skill", "",
		"Load a .stado/skills/<name>.md body as (part of) the prompt — combines with --prompt if both set")
	runCmd.Flags().IntVar(&runMaxTurns, "max-turns", 20, "Maximum agent turns before giving up")
	runCmd.Flags().BoolVar(&runJSON, "json", false, "Emit JSON lines instead of raw text (preferred for scripted use; one event per line)")
	runCmd.Flags().BoolVar(&runQuiet, "quiet", false, "Suppress tool-call preview lines on stdout (non-JSON mode); tools still run and still commit")
	runCmd.Flags().BoolVar(&runTools, "tools", false, "Enable the bundled toolset with git-native audit")
	runCmd.Flags().BoolVar(&runSandboxFS, "sandbox-fs", false, "Apply landlock: confine writes to the session worktree + /tmp (Linux only)")
	runCmd.Flags().StringVar(&runSessionID, "session", "",
		"Continue an existing session: prior conversation is loaded, the new prompt appended, and the exchange persisted. Accepts uuid, uuid-prefix (≥8 chars), or description substring.")
	rootCmd.AddCommand(runCmd)
}

// resolveRunPromptFromFlags mutates runPrompt to reflect --skill
// resolution. Factored out of runCmd.RunE so the resolution logic
// is unit-testable without wiring up a provider. Safe to call even
// when --skill is empty (no-op).
func resolveRunPromptFromFlags() error {
	if runSkill == "" {
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("run: getwd: %w", err)
	}
	sks, err := skills.Load(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "stado run: skills load: %v\n", err)
	}
	var chosen *skills.Skill
	for i := range sks {
		if sks[i].Name == runSkill {
			chosen = &sks[i]
			break
		}
	}
	if chosen == nil {
		names := make([]string, 0, len(sks))
		for _, s := range sks {
			names = append(names, s.Name)
		}
		return fmt.Errorf("run: skill %q not found (available: %s)",
			runSkill, strings.Join(names, ", "))
	}
	if runPrompt == "" {
		runPrompt = chosen.Body
	} else {
		runPrompt = chosen.Body + "\n\n" + runPrompt
	}
	fmt.Fprintf(os.Stderr, "stado run: loaded skill %s (%s)\n", chosen.Name, chosen.Path)
	return nil
}
