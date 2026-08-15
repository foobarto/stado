package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/foobarto/stado/internal/brokercredential"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/harness"
	"github.com/foobarto/stado/internal/headless"
	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/internal/instructions"
	"github.com/foobarto/stado/internal/personas"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/skills"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/telemetry"
	"github.com/foobarto/stado/internal/trajectory"
	"github.com/foobarto/stado/internal/tui"
	"github.com/foobarto/stado/pkg/agent"
)

var (
	runPrompt         string
	runMaxTurns       int
	runNoTurnLimit    bool
	runJSON           bool
	runQuiet          bool
	runNoTools        bool
	runMode           string // --mode harness mode (EP-0030)
	runTools          string // --tools (whitelist; comma-separated globs)
	runToolsAutoload  string // --tools-autoload
	runToolsDisable   string // --tools-disable
	runSessionID      string
	runSkill          string
	runPersona        string // --persona; empty = config default → bundled default
	runHeadless       bool   // --headless: serve the JSON-RPC daemon instead of a one-shot prompt (was `stado headless`)
	runVerifyCommands []string
	runNoVerify       bool
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

  --tools          ""   (empty = all installed tools enabled; pass globs to whitelist)
  --no-tools       OFF  (pure-chat mode — no session, no audit)
  --no-sandbox     OFF  (v1 default: sandboxed; pass to disable bwrap + Landlock)

By default, bash + read/write/grep/etc. are available and every call
commits to the session's git-native audit log. Pass --no-tools for
pure-chat mode (no tools, no session, no audit). Pass --tools with a
comma-separated glob list to whitelist a subset (e.g. --tools=fs.*).

By default in v1, bash runs inside bwrap (Linux) and writes are
landlock-confined to the launch cwd + /tmp; the broker's mount table
masks credential-bearing paths. Pass --no-sandbox to opt out — tools
run as direct subprocesses with full filesystem access, intended for
development scenarios and explicit operator override.

Daemon mode: pass --headless to serve a line-delimited JSON-RPC 2.0 daemon
over stdio (multi-session; for CI and editor integration) instead of running
a single prompt. This replaces the former ` + "`stado headless`" + ` command;
see docs/commands/headless.md for the method reference.

Exit codes: 0 success; 1 provider/IO error; 2 max-turns, budget cap, or verification exhaustion.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// --headless turns `run` into the JSON-RPC daemon (the surface
		// formerly exposed as `stado headless`): a persistent multi-session
		// stdio server, not a one-shot prompt. Branch before prompt
		// resolution / MaybeRewrap / the 10-minute timeout so the daemon
		// keeps its own lifecycle exactly as the standalone command had it.
		if runHeadless {
			return runHeadlessMode(cmd, args)
		}
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
		// EP-0064: non-interactive run does not own a persistent lifecycle-
		// application composition. Resolve the launch persona before doing any
		// provider/session work so its additive plugin declarations participate
		// in the same fail-closed surface check as global background plugins.
		persona, personaErr := resolvePersona(runPersona, cfg)
		if personaErr != nil {
			fmt.Fprintf(os.Stderr, "stado run: %v\n", personaErr)
		}
		var personaPlugins []string
		if persona != nil {
			personaPlugins = persona.Plugins
		}
		if err := runtime.RequireLifecycleApplicationSurface(cfg, personaPlugins, runtime.ApplicationSurfaceRun); err != nil {
			return fmt.Errorf("stado run: %w", err)
		}
		// EP-0038d: sandbox wrap-mode — re-exec under bwrap/firejail
		// when [sandbox] mode = "wrap" and not already inside a wrapper.
		if err := sandbox.MaybeRewrap(sandbox.WrapConfig{
			Mode:           cfg.Sandbox.Mode,
			BindRO:         cfg.Sandbox.Wrap.BindRO,
			BindRW:         cfg.Sandbox.Wrap.BindRW,
			Network:        cfg.Sandbox.Wrap.Network,
			HTTPProxy:      cfg.Sandbox.HTTPProxy,
			AllowEnv:       cfg.Sandbox.AllowEnv,
			RefuseNoRunner: cfg.Sandbox.RefuseNoRunner,
			Runner:         cfg.Sandbox.Wrap.Runner,
		}); err != nil {
			return err
		}
		// EP-0042 follow-up: emit a once-per-process warning when running
		// without process-containment. After MaybeRewrap, so wrapped
		// children (RewrappedEnvVar=1) stay silent and unwrapped parents
		// see the warning.
		sandbox.WarnIfHostUnsandboxed(sandbox.WrapConfig{Mode: cfg.Sandbox.Mode})
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
		if cmd.Flags().Changed("verify") {
			cfg.Verify.Commands = append([]string(nil), runVerifyCommands...)
		}
		if runNoVerify {
			cfg.Verify.Commands = nil
		}
		return withTelemetry(cmd.Context(), cfg, func(runCtx context.Context, rt *telemetry.Runtime) error {
			// F1: scriptable deny/mutate lifecycle hooks (Lua). Built
			// once and wired into BOTH the agent loop (LLM-side points)
			// and the executor (tool-side points) below.
			//
			// C3: build the lifecycle runner — and emit its skip-warnings
			// for any broken/unloadable hook — BEFORE the provider build.
			// A first-run user with no API key errors out at provider build;
			// gating the warning behind a successful provider build meant a
			// broken hook was dropped with zero feedback. Surface it first so
			// the warning reaches the user regardless of provider config.
			lifecycleHooks, hookWarnings := hooks.BuildLifecycleRunnerWithWarnings(cfg)
			for _, w := range hookWarnings {
				fmt.Fprintln(os.Stderr, w)
			}

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
			var activeSession *stadogit.Session
			persistWorktree := ""
			persistedViewLen := 0
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
				persistWorktree = sess.WorktreePath
				priorMsgs, err = runtime.LoadConversation(continueWorktree)
				if err != nil {
					return fmt.Errorf("run: load conversation for %s: %w", resolved, err)
				}
				persistedViewLen = len(priorMsgs)
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
			// EP-0030: security harness — prepend to the system prompt in
			// security mode. Shared helper so run/TUI/ACP/headless inject it
			// identically.
			sysPrompt = harness.Prepend(sysPrompt, promptWorkdir, cfg.Harness.Mode)
			if continueWorktree != "" {
				promptWorkdir = continueWorktree
			}
			maxTurns := runMaxTurns
			if runNoTurnLimit {
				// math.MaxInt32 is "effectively unlimited" without
				// risking overflow in the loop counter or downstream
				// arithmetic. Real termination still relies on
				// no-tool-calls-remain or context cancellation.
				maxTurns = math.MaxInt32
			}
			effectiveSkills, skErr := runtime.EffectiveSkills(promptWorkdir, persona)
			if skErr != nil {
				fmt.Fprintf(os.Stderr, "stado run: skills load: %v\n", skErr)
			}
			if inert := skills.InertSkills(effectiveSkills); len(inert) > 0 {
				fmt.Fprintf(os.Stderr, "stado run: skills %v are unreachable (disable-model-invocation + user-invocable: false)\n", inert)
			}
			opts := runtime.AgentLoopOptions{
				Provider:      prov,
				Config:        cfg,
				Metrics:       rt.M(),
				Model:         cfg.Defaults.Model,
				Messages:      append(priorMsgs, newUserMsg),
				MaxTurns:      maxTurns,
				Persona:       persona,
				Skills:        effectiveSkills,
				Hooks:         lifecycleHooks,
				OnEvent:       emitter(runJSON, runQuiet, os.Stdout),
				OnVerifyEvent: verifyEmitter(runJSON, os.Stdout, os.Stderr),
				OnToolOutcome: trajectory.Recorder{StateDir: cfg.StateDir(), SessionID: continueSessID, Principal: trajectory.LocalPrincipal()}.ToolOutcome,
				OnTurnComplete: func(turnIndex int, text string, _ []agent.ToolUseBlock, usage agent.Usage, duration time.Duration) {
					hookRunner.FirePostTurn(runCtx, hooks.NewPostTurnPayload(turnIndex, usage, text, duration))
				},
				Thinking:             cfg.Agent.Thinking,
				ThinkingBudgetTokens: cfg.Agent.ThinkingBudgetTokens,
				System:               sysPrompt,
				SystemTemplate:       cfg.Agent.SystemPromptTemplate,
				TokenCap:             cfg.Budget.HardTokens,
				InputTokenCap:        cfg.Budget.HardInputTokens,
				OutputTokenCap:       cfg.Budget.HardOutputTokens,
				// EP-0036: sampling — flag overrides already patched into cfg.Sampling above.
				Temperature: cfg.Sampling.Temperature,
				TopP:        cfg.Sampling.TopP,
				TopK:        cfg.Sampling.TopK,
			}
			// --no-tools is the gate: pure-chat mode disables the
			// executor entirely. --tools (string) is a whitelist of
			// globs applied below if non-empty; empty = all enabled.
			toolsEnabled := !runNoTools
			if !toolsEnabled && runtime.VerifyConfigFrom(cfg).Enabled() {
				return errors.New("stado run: verification commands require tools; remove --no-tools or pass --no-verify")
			}
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
				activeSession = sess
				recorder := trajectory.Recorder{StateDir: cfg.StateDir(), SessionID: sess.ID, Principal: trajectory.LocalPrincipal()}
				recorder.EnsureObjective(runPrompt)
				opts.OnToolOutcome = recorder.ToolOutcome
				persistWorktree = sess.WorktreePath
				persistedViewLen = len(priorMsgs)
				// EP-0030: harness mode flag overrides config.
				if runMode != "" {
					cfg.Harness.Mode = runMode
				}
				// EP-0037: CLI flags override [tools] config before building executor.
				if runTools != "" {
					cfg.Tools.Enabled = splitComma(runTools)
				}
				if runToolsAutoload != "" {
					cfg.Tools.Autoload = splitComma(runToolsAutoload)
				}
				if runToolsDisable != "" {
					cfg.Tools.Disabled = append(cfg.Tools.Disabled, splitComma(runToolsDisable)...)
				}
				opts.Executor, err = runtime.BuildExecutor(sess, cfg, "stado-run", rt.M())
				if err != nil {
					return fmt.Errorf("tools: %w", err)
				}
				// F1: same lifecycle runner drives the tool-side
				// (pre/post-tool) deny/mutate seam.
				opts.Executor.Hooks = lifecycleHooks
				// The broker decision is applied below after attach. Landlock is
				// likewise delayed until the broker daemon has had a chance to
				// start, so both layers use one explicit sandbox decision.
				//
				// Reverses an earlier UX-pressured retreat documented
				// in DESIGN.md §Sandbox → "Reversal of an earlier UX
				// retreat". The launch cwd is the writable boundary
				// in BOTH modes — operators expect `cd ~/projects/foo
				// && stado run` to operate on ~/projects/foo, not on
				// a per-session scratch worktree.
				opts.Workdir = cwd
				if noSandbox {
					fmt.Fprintf(os.Stderr, "stado run: session %s (cwd %s, audit %s) [--no-sandbox]\n", sess.ID, cwd, sess.WorktreePath)
				} else {
					fmt.Fprintf(os.Stderr, "stado run: session %s (cwd %s, audit %s) [sandboxed]\n", sess.ID, cwd, sess.WorktreePath)
				}
			}
			cwd, _ := os.Getwd()
			baseCtx, _ := telemetry.LoadParentTraceparent(runCtx, cwd)
			ctx, cancel := context.WithTimeout(baseCtx, 10*time.Minute)
			defer cancel()

			// v1 broker attach. MUST happen before ApplyLandlock —
			// daemon.EnsureRunning auto-spawns `stado daemon start`
			// when the socket is absent (fresh host, post-idle), and
			// Landlock survives fork+exec by design. If Landlock fires
			// first the spawned daemon inherits the cwd+/tmp write
			// confinement and cannot mkdir $XDG_RUNTIME_DIR/stado/ or
			// bind its socket. Cloud-review bug_011 / PR #71.
			// brokerProfileFromFlags() now honours --no-sandbox itself.
			brokerSession, brokerErr := attachToBroker(ctx, brokerPurposeFromFlags(), brokerProfileFromFlags(), cwd)
			if brokerErr != nil {
				return fmt.Errorf("stado run: %w", brokerErr)
			}
			brokerSession.AnnounceSandboxMode(os.Stderr, "stado run")
			opts.Broker = brokerSession
			defer func() {
				if closeErr := brokerSession.Close(); closeErr != nil {
					fmt.Fprintf(os.Stderr, "stado run: broker session.terminate: %v\n", closeErr)
				}
			}()
			if activeSession != nil {
				credentialStore, credentialErr := brokercredential.New(cfg.StateDir())
				if credentialErr != nil {
					return fmt.Errorf("stado run: durable broker session credentials: %w", credentialErr)
				}
				brokerSession.logicalCredentials = credentialStore
				logical, logicalErr := brokerSession.OpenLogicalSession(ctx, cwd, activeSession.ID)
				if logicalErr != nil {
					return fmt.Errorf("stado run: durable broker session: %w", logicalErr)
				}
				opts.Broker = logical
				defer func() {
					if closeErr := logical.Close(); closeErr != nil {
						fmt.Fprintf(os.Stderr, "stado run: durable broker session close: %v\n", closeErr)
					}
				}()
			}

			// Apply the same broker decision used by every other executor-owning
			// surface. This preserves skipped attaches and makes --no-sandbox an
			// explicit NoneRunner selection.
			executorSandbox := brokerExecutorSandbox(brokerSession, noSandbox)
			executorSandbox.Apply(opts.Executor)
			opts.DefaultSandboxPolicy = executorSandbox.DefaultSandboxPolicy(cwd)

			// Apply Landlock LAST so the broker auto-spawn above ran
			// unrestricted but every subsequent in-process write is
			// now confined.
			if toolsEnabled && !noSandbox {
				if err := sandbox.ApplyLandlock(runLandlockPolicy(cwd, activeSession)); err != nil {
					if errors.Is(err, sandbox.ErrLandlockUnavailable) {
						fmt.Fprintln(os.Stderr, "stado run: Landlock unavailable on this kernel; continuing without in-process write confinement (bwrap still applies at the runner layer)")
					} else {
						return fmt.Errorf("sandbox: %w", err)
					}
				} else {
					fmt.Fprintln(os.Stderr, "stado run: Landlock applied (writes confined to cwd + /tmp + session audit state)")
				}
			}

			_, finalMsgs, loopErr := runAgentLoop(ctx, opts)
			if persistWorktree != "" {
				if _, err := runtime.AppendMessagesFrom(persistWorktree, finalMsgs, persistedViewLen); err != nil {
					fmt.Fprintf(os.Stderr, "stado run: persist conversation: %v\n", err)
				}
				if continueSession != nil && opts.Executor == nil {
					if err := continueSession.NextTurn(); err != nil {
						return fmt.Errorf("run: turn boundary for %s: %w", continueSessID, err)
					}
				}
			}
			if loopErr != nil {
				if runLoopUsesExitCode2(loopErr) {
					return &exitCodeError{Code: 2, Err: loopErr}
				}
				return loopErr
			}
			if !runJSON {
				fmt.Fprintln(os.Stdout)
			}
			return nil
		})
	},
}

func runLandlockPolicy(cwd string, sess *stadogit.Session) sandbox.Policy {
	policy := sandbox.WorktreeWrite(cwd)
	if sess == nil {
		return policy
	}
	policy.FSWrite = append(policy.FSWrite, sess.WorktreePath)
	if sess.Sidecar != nil {
		policy.FSWrite = append(policy.FSWrite, sess.Sidecar.Path)
	}
	return policy
}

func runLoopUsesExitCode2(err error) bool {
	return errors.Is(err, runtime.ErrTokenCapExceeded) ||
		errors.Is(err, runtime.ErrMaxTurnsExceeded) ||
		errors.Is(err, runtime.ErrVerifyExhausted)
}

func verifyEmitter(jsonOut bool, out, errOut io.Writer) func(runtime.VerifyEvent) {
	return func(ev runtime.VerifyEvent) {
		if jsonOut {
			encoded, _ := json.Marshal(map[string]any{
				"type": "verify", "status": ev.Status, "round": ev.Round,
				"command": ev.Command, "output": ev.Output,
			})
			fmt.Fprintln(out, string(encoded))
			return
		}
		switch ev.Status {
		case runtime.VerifyPending:
			fmt.Fprintln(errOut, "stado verify: candidate output buffered until verification")
		case runtime.VerifyStarted:
			fmt.Fprintf(errOut, "stado verify: round %d: %s\n", ev.Round, ev.Command)
		case runtime.VerifyFailed, runtime.VerifyInfrastructure, runtime.VerifyCancelled, runtime.VerifyGenerationError, runtime.VerifyExhausted:
			fmt.Fprintf(errOut, "stado verify: %s: %s\n", ev.Status, strings.TrimSpace(ev.Output))
		}
	}
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

// runHeadlessMode serves the line-delimited JSON-RPC 2.0 daemon over stdio —
// the surface formerly exposed as `stado headless`. It is a persistent
// multi-session server, not a one-shot run, so it deliberately does NOT call
// sandbox.MaybeRewrap or impose run's 10-minute context timeout; per-call
// inputs arrive through the session.* RPC methods rather than run's flags.
func runHeadlessMode(cmd *cobra.Command, args []string) error {
	// Reject one-shot-only inputs so a misuse like
	// `stado run --headless --prompt hi` fails loudly instead of silently
	// dropping the prompt. Per-call prompts go through session.prompt.
	if runPrompt != "" || len(args) > 0 || runSkill != "" || runSessionID != "" {
		return fmt.Errorf("run --headless is a JSON-RPC daemon and takes no prompt/--skill/--session; drive it with the session.* RPC methods")
	}
	incompatible := incompatibleHeadlessFlags(cmd)
	if len(incompatible) > 0 {
		return fmt.Errorf("run --headless does not accept one-shot run flags: %s; configure daemon sessions through config and the session.* RPC methods", strings.Join(incompatible, ", "))
	}
	cfg, err := runLoadConfig()
	if err != nil {
		return err
	}
	applyRootProviderOverrides(cfg)
	// The daemon runs unwrapped under mode=off and mode=wrap alike (no
	// MaybeRewrap), so warn once — an integrator wiring stado into another
	// harness sees the containment posture on stderr.
	sandbox.WarnIfHostUnsandboxed(sandbox.WrapConfig{Mode: cfg.Sandbox.Mode})
	var defaultPersona *personas.Persona
	if runPersona != "" {
		p, err := resolvePersona(runPersona, cfg)
		if err != nil {
			return fmt.Errorf("run --headless: %w", err)
		}
		defaultPersona = p
	}
	var personaPlugins []string
	if defaultPersona != nil {
		personaPlugins = defaultPersona.Plugins
	}
	if err := runtime.RequireLifecycleApplicationSurface(cfg, personaPlugins, runtime.ApplicationSurfaceHeadless); err != nil {
		return fmt.Errorf("stado run --headless: %w", err)
	}
	return withTelemetry(cmd.Context(), cfg, func(ctx context.Context, rt *telemetry.Runtime) error {
		cwd, _ := os.Getwd()
		brokerSession, brokerErr := attachToBroker(ctx, brokerPurposeFromFlags(), brokerProfileFromFlags(), cwd)
		if brokerErr != nil {
			return fmt.Errorf("stado run --headless: %w", brokerErr)
		}
		brokerSession.AnnounceSandboxMode(os.Stderr, "stado run --headless")
		defer func() {
			if closeErr := brokerSession.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "stado run --headless: broker session.terminate: %v\n", closeErr)
			}
		}()

		prov, provErr := runBuildProvider(cfg)
		if provErr != nil {
			// Non-fatal: the daemon still serves non-LLM RPCs (tools.list,
			// plugin.*) so an integrator can wire up before credentials land.
			fmt.Fprintf(os.Stderr, "stado run --headless: provider unavailable: %v\n", provErr)
		}
		personaTag := ""
		if defaultPersona != nil {
			personaTag = " (persona=" + defaultPersona.Name + ")"
		}
		fmt.Fprintf(os.Stderr, "stado run --headless: ready (JSON-RPC 2.0, stdio)%s\n", personaTag)
		srv := headless.NewServer(cfg, prov)
		srv.Metrics = rt.M()
		srv.BrokerFactory = brokerSession.CreatePeer
		srv.DefaultPersona = defaultPersona
		srv.ExecutorSandbox = brokerExecutorSandbox(brokerSession, noSandbox)
		return srv.Serve(ctx, os.Stdin, os.Stdout)
	})
}

func incompatibleHeadlessFlags(cmd *cobra.Command) []string {
	var incompatible []string
	seen := make(map[string]struct{})
	visitChanged := func(flag *pflag.Flag) {
		if _, ok := seen[flag.Name]; ok {
			return
		}
		seen[flag.Name] = struct{}{}
		switch flag.Name {
		case "headless", "persona", "provider", "model", "no-sandbox":
			// These flags are consumed by daemon setup or broker attachment.
		default:
			incompatible = append(incompatible, "--"+flag.Name)
		}
	}
	cmd.Flags().Visit(visitChanged)
	cmd.InheritedFlags().Visit(visitChanged)
	sort.Strings(incompatible)
	return incompatible
}

func init() {
	runCmd.Flags().StringVar(&runPrompt, "prompt", "", "Prompt text (or provide as positional argument)")
	runCmd.Flags().StringVar(&runSkill, "skill", "",
		"Load a .stado/skills/<name>.md body as (part of) the prompt — combines with --prompt if both set")
	runCmd.Flags().IntVar(&runMaxTurns, "max-turns", 20, "Maximum agent turns before giving up")
	runCmd.Flags().BoolVar(&runNoTurnLimit, "no-turn-limit", false,
		"Disable the max-turn cap entirely; the loop runs until no tool calls remain or the context is cancelled. Beats --max-turns when both set. Use token budgets or context timeout for bounded long-running work.")
	runCmd.Flags().BoolVar(&runJSON, "json", false, "Emit JSON lines instead of raw text (preferred for scripted use; one event per line)")
	runCmd.Flags().BoolVar(&runQuiet, "quiet", false, "Suppress tool-call preview lines on stdout (non-JSON mode); tools still run and still commit")
	runCmd.Flags().BoolVar(&runNoTools, "no-tools", false,
		"Disable tools — pure-chat mode (no session, no audit).")
	runCmd.Flags().StringArrayVar(&runVerifyCommands, "verify", nil,
		"Run this sandboxed command before accepting completion (repeatable; overrides [verify].commands)")
	runCmd.Flags().BoolVar(&runNoVerify, "no-verify", false,
		"Disable configured verification commands for this run")
	// --no-sandbox is a persistent root flag (see main.go), so it works for the
	// TUI/acp/headless/mcp-server too, not just `run`. Do not re-register it
	// here — a duplicate would panic at init.
	// EP-0030: harness mode.
	runCmd.Flags().StringVar(&runMode, "mode", "",
		"Harness mode: \"\" (general, default) or \"security\" (security-research harness with recon discipline and abusability filters).")
	// EP-0037: tool surface control flags (canonical names per NOTES §10).
	runCmd.Flags().StringVar(&runTools, "tools", "",
		"Comma-separated tool globs: ONLY these tools enabled (e.g. 'fs.*,shell.exec'). Empty = all installed tools enabled. Stacks with --tools-disable.")
	runCmd.Flags().StringVar(&runToolsAutoload, "tools-autoload", "",
		"Comma-separated tool globs: always-on surface sent to model every turn. Empty = use [tools.autoload] from config.")
	runCmd.Flags().StringVar(&runToolsDisable, "tools-disable", "",
		"Comma-separated tool globs: remove from surface entirely. Wins over enable and autoload.")
	runCmd.Flags().StringVar(&runPersona, "persona", "",
		"Persona name to use as the operating manual (system prompt). "+
			"Empty = [defaults].persona from config, or bundled default. "+
			"Resolution: {cwd}/.stado/personas → ~/.stado/personas → bundled.")
	runCmd.Flags().BoolVar(&runHeadless, "headless", false,
		"Serve a line-delimited JSON-RPC 2.0 daemon over stdio (multi-session; for CI and editor integration) instead of running a one-shot prompt. Prompt/skill/session inputs are rejected; drive sessions via the session.* RPC methods. --persona sets the default persona for new sessions.")
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
		avail := "(none installed — add .md files under .stado/skills/)"
		if len(names) > 0 {
			avail = strings.Join(names, ", ")
		}
		return fmt.Errorf("run: skill %q not found (available: %s)", runSkill, avail)
	}
	if runPrompt == "" {
		runPrompt = chosen.RenderedBody()
	} else {
		runPrompt = chosen.RenderedBody() + "\n\n" + runPrompt
	}
	fmt.Fprintf(os.Stderr, "stado run: loaded skill %s (%s)\n", chosen.Name, chosen.Path)
	return nil
}
