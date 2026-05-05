package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
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
	runPrompt      string
	runMaxTurns    int
	runNoTurnLimit bool
	runJSON        bool
	runQuiet       bool
	runTools       bool
	runNoTools     bool
	runSandboxFS       bool
	runToolsWhitelist  string // --tools-whitelist
	runToolsAutoload   string // --tools-autoload
	runToolsDisable    string // --tools-disable
	runSessionID       string
	runSkill       string
	// Sampling overrides (EP-0036). Zero value means "use config / provider default".
	runTemperature float64
	runTopP        float64
	runTopK        int
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

Defaults at a glance:

  --tools          ON   (use --no-tools for pure-chat mode)
  --sandbox-fs     OFF  (agent operates on your actual filesystem)

When tools are on (default), bash + read/write/grep/etc. are available
and every call commits to the session's git-native audit log. Pass
--no-tools for pure-chat mode (no tools, no session, no audit).

When --sandbox-fs is set, bash runs inside bwrap (Linux) and writes
are landlock-confined to the session worktree + /tmp. Without it,
tools run as direct subprocesses with full filesystem access — the
agent can ls your home, cd anywhere, etc.

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
		// EP-0036: apply sampling flag overrides on top of config.
		if cmd.Flags().Changed("temperature") {
			cfg.Sampling.Temperature = &runTemperature
		}
		if cmd.Flags().Changed("top-p") {
			cfg.Sampling.TopP = &runTopP
		}
		if cmd.Flags().Changed("top-k") {
			cfg.Sampling.TopK = &runTopK
		}
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

			maxTurns := runMaxTurns
			if runNoTurnLimit {
				// math.MaxInt32 is "effectively unlimited" without
				// risking overflow in the loop counter or downstream
				// arithmetic. Real termination still relies on
				// no-tool-calls-remain or context cancellation.
				maxTurns = math.MaxInt32
			}
			opts := runtime.AgentLoopOptions{
				Provider: prov,
				Config:   cfg,
				Model:    cfg.Defaults.Model,
				Messages: append(priorMsgs, newUserMsg),
				MaxTurns: maxTurns,
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
				TokenCap:             cfg.Budget.HardTokens,
				InputTokenCap:        cfg.Budget.HardInputTokens,
				OutputTokenCap:       cfg.Budget.HardOutputTokens,
				// EP-0036: sampling — flag overrides already patched into cfg.Sampling above.
				Temperature: cfg.Sampling.Temperature,
				TopP:        cfg.Sampling.TopP,
				TopK:        cfg.Sampling.TopK,
			}
			// --no-tools wins over --tools when both are set; the
			// negative flag is the natural opt-out form for users
			// who don't want to type `--tools=false`.
			toolsEnabled := runTools && !runNoTools
			if toolsEnabled {
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
				// EP-0037: CLI flags override [tools] config before building executor.
			if runToolsWhitelist != "" {
				cfg.Tools.Enabled = splitComma(runToolsWhitelist)
			}
			if runToolsAutoload != "" {
				cfg.Tools.Autoload = splitComma(runToolsAutoload)
			}
			if runToolsDisable != "" {
				cfg.Tools.Disabled = append(cfg.Tools.Disabled, splitComma(runToolsDisable)...)
			}
			opts.Executor, err = runtime.BuildExecutor(sess, cfg, "stado-run")
				if err != nil {
					return fmt.Errorf("tools: %w", err)
				}
				// Default sandbox policy for `stado run`: NONE.
				// BuildExecutor seeds Runner with sandbox.Detect()
				// which picks bwrap on Linux, but the run-CLI default
				// is the user-visible "agent operates on my actual
				// filesystem" mode — bwrap-by-default surprised
				// users who expected `ls ~/` to show their real home.
				// Opt back into sandboxing via --sandbox-fs (which
				// also applies landlock for defense-in-depth).
				if !runSandboxFS {
					opts.Executor.Runner = sandbox.NoneRunner{}
					// Tools see the user's launch cwd, not the per-
					// session scratch worktree. Without this override,
					// `ls` in `stado run` lands in an empty directory
					// because the loop defaults to Session.WorktreePath.
					opts.Workdir = cwd
				}
				if runSandboxFS {
					fmt.Fprintf(os.Stderr, "stado run: session %s (sandbox worktree %s)\n", sess.ID, sess.WorktreePath)
				} else {
					fmt.Fprintf(os.Stderr, "stado run: session %s (cwd %s, audit %s)\n", sess.ID, cwd, sess.WorktreePath)
				}

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
	runCmd.Flags().BoolVar(&runNoTurnLimit, "no-turn-limit", false,
		"Disable the max-turn cap entirely; the loop runs until no tool calls remain or the context is cancelled. Beats --max-turns when both set. Useful for long-running multi-step tasks where the cap is the wrong control surface (use --budget hard_usd or context timeout instead).")
	runCmd.Flags().BoolVar(&runJSON, "json", false, "Emit JSON lines instead of raw text (preferred for scripted use; one event per line)")
	runCmd.Flags().BoolVar(&runQuiet, "quiet", false, "Suppress tool-call preview lines on stdout (non-JSON mode); tools still run and still commit")
	runCmd.Flags().BoolVar(&runTools, "tools", true,
		"Enable the bundled toolset with git-native audit (default). Negate with --no-tools.")
	runCmd.Flags().BoolVar(&runNoTools, "no-tools", false,
		"Disable tools — pure-chat mode (no session, no audit). Wins over --tools when both set.")
	runCmd.Flags().BoolVar(&runSandboxFS, "sandbox-fs", false,
		"Sandbox tool execution: bash runs in bwrap (Linux) and writes are landlock-confined to the session worktree + /tmp. Off by default — `stado run` operates on your actual filesystem.")
	// EP-0037: tool surface control flags.
	runCmd.Flags().StringVar(&runToolsWhitelist, "tools-whitelist", "",
		"Comma-separated tool globs: ONLY these tools enabled (e.g. 'fs.*,shell.exec'). Stacks with --tools-disable.")
	runCmd.Flags().StringVar(&runToolsAutoload, "tools-autoload", "",
		"Comma-separated tool globs: always-on surface sent to model every turn. Empty = use [tools.autoload] from config.")
	runCmd.Flags().StringVar(&runToolsDisable, "tools-disable", "",
		"Comma-separated tool globs: remove from surface entirely. Wins over enable and autoload.")
	runCmd.Flags().StringVar(&runSessionID, "session", "",
		"Continue an existing session: prior conversation is loaded, the new prompt appended, and the exchange persisted. Accepts uuid, uuid-prefix (≥8 chars), or description substring.")
	// EP-0036: sampling overrides. Zero value = use config/provider default.
	runCmd.Flags().Float64Var(&runTemperature, "temperature", 0, "Sampling temperature (0 = provider default; 0–2 typical range). Overrides [sampling].temperature in config.")
	runCmd.Flags().Float64Var(&runTopP, "top-p", 0, "Nucleus sampling top-p (0 = provider default). Overrides [sampling].top_p in config.")
	runCmd.Flags().IntVar(&runTopK, "top-k", 0, "Top-k sampling (0 = provider default). Overrides [sampling].top_k in config.")
	rootCmd.AddCommand(runCmd)
}

// splitComma splits a comma-separated flag value into a trimmed non-empty slice.
func splitComma(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
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
