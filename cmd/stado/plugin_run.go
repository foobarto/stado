package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/toolinput"
	"github.com/foobarto/stado/internal/tui"
	"github.com/foobarto/stado/pkg/agent"
)

var (
	pluginRunSession       string
	pluginRunWorkdir       string
	pluginRunBuildProvider = tui.BuildProvider
)

var pluginRunCmd = &cobra.Command{
	Use:   "run <name>-<version> <tool> [json-args]",
	Short: "Run a single tool exported by an installed plugin",
	Long: "Loads the plugin from $XDG_DATA_HOME/stado/plugins/<name>-<version>/,\n" +
		"instantiates the wasm module in a wazero sandbox bound by the\n" +
		"manifest's declared capabilities, then invokes the named tool\n" +
		"with the supplied JSON args (default: empty object).\n\n" +
		"Primarily for local plugin authoring. Pass --session <id> to bind\n" +
		"the run to a persisted session so session-aware capabilities like\n" +
		"session:read, session:fork, and llm:invoke work on the CLI too.",
	Args: cobra.RangeArgs(2, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		toolName := args[1]
		argsJSON := "{}"
		if len(args) >= 3 {
			argsJSON = args[2]
		}
		if err := toolinput.CheckLen(len(argsJSON)); err != nil {
			return err
		}
		dir, err := plugins.InstalledDir(filepath.Join(cfg.StateDir(), "plugins"), args[0])
		if err != nil {
			return err
		}
		if _, err := os.Stat(dir); err != nil {
			return fmt.Errorf("plugin %s not installed (run `stado plugin install <plugin-dir>` after building + signing it)", args[0])
		}

		// Load + verify manifest (signature + wasm sha256 + rollback).
		// The caller is presumably the same user who installed the
		// plugin, so trust-store should already have the signer.
		m, sig, err := plugins.LoadFromDir(dir)
		if err != nil {
			return err
		}
		wasmPath := filepath.Join(dir, "plugin.wasm")
		wasmBytes, err := plugins.ReadVerifiedWASM(m.WASMSHA256, wasmPath)
		if err != nil {
			return fmt.Errorf("verify: %w", err)
		}
		ts := plugins.NewTrustStore(cfg.StateDir())
		if err := ts.VerifyManifest(m, sig); err != nil {
			return fmt.Errorf("verify: %w", err)
		}
		if cfg.Plugins.CRLURL != "" {
			if err := consultCRL(cfg, m); err != nil {
				return fmt.Errorf("run: %w", err)
			}
		}

		ctx := cmd.Context()
		rt, err := pluginRuntime.New(ctx)
		if err != nil {
			return fmt.Errorf("runtime: %w", err)
		}
		defer func() { _ = rt.Close(ctx) }()

		// Resolve plugin Workdir. Default = install dir (backward
		// compat with plugins that scope fs:read:. to their own state
		// directory). Override with --workdir <path> when the plugin
		// is meant to operate against the operator's repo, e.g.
		// `stado plugin run --workdir=$PWD htb-cve-lookup-0.3.0
		// lookup '{"service":"NSClient"}'` from inside that repo.
		workdir := dir
		if pluginRunWorkdir != "" {
			abs, err := filepath.Abs(pluginRunWorkdir)
			if err != nil {
				return fmt.Errorf("--workdir %q: %w", pluginRunWorkdir, err)
			}
			info, err := os.Stat(abs)
			if err != nil {
				return fmt.Errorf("--workdir %q: %w", pluginRunWorkdir, err)
			}
			if !info.IsDir() {
				return fmt.Errorf("--workdir %q: not a directory", pluginRunWorkdir)
			}
			workdir = abs
		}
		host := pluginRuntime.NewHost(*m, workdir, nil)
		attachPluginMemoryBridge(cfg, host, m.Name)
		if host.SessionObserve || host.SessionRead || host.SessionFork || host.LLMInvokeBudget > 0 {
			if pluginRunSession != "" {
				bridge, note, err := buildPluginRunBridge(cmd.Context(), cfg, pluginRunSession, m.Name, host.LLMInvokeBudget > 0)
				if err != nil {
					return err
				}
				host.SessionBridge = bridge
				if note != "" {
					fmt.Fprintln(os.Stderr, note)
				}
			} else {
				bridge := pluginRuntime.NewSessionBridge(nil, nil, "")
				bridge.PluginName = m.Name
				host.SessionBridge = bridge
				fmt.Fprintln(os.Stderr,
					"stado plugin run: session-aware capabilities declared; note that the one-shot CLI has no live session — "+
						"pass --session <id> to attach to a persisted session")
			}
		}
		if err := pluginRuntime.InstallHostImports(ctx, rt, host); err != nil {
			return fmt.Errorf("host imports: %w", err)
		}
		mod, err := rt.Instantiate(ctx, wasmBytes, *m)
		if err != nil {
			return fmt.Errorf("instantiate: %w", err)
		}
		defer func() { _ = mod.Close(ctx) }()

		// Look up the tool in the manifest — must be declared there.
		var tdef *plugins.ToolDef
		for i := range m.Tools {
			if m.Tools[i].Name == toolName {
				tdef = &m.Tools[i]
				break
			}
		}
		if tdef == nil {
			return fmt.Errorf("tool %q not declared in plugin manifest", toolName)
		}
		pt, err := pluginRuntime.NewPluginTool(mod, *tdef)
		if err != nil {
			return err
		}
		res, err := pt.Run(ctx, []byte(argsJSON), nil)
		if err != nil {
			if res.Error != "" {
				fmt.Fprintln(os.Stderr, res.Error)
			}
			return err
		}
		if res.Error != "" {
			return fmt.Errorf("plugin error: %s", res.Error)
		}
		fmt.Println(res.Content)
		return nil
	},
}

func attachPluginMemoryBridge(cfg *config.Config, host *pluginRuntime.Host, pluginName string) {
	if cfg == nil || host == nil || !host.NeedsMemoryBridge() {
		return
	}
	host.MemoryBridge = pluginRuntime.NewLocalMemoryBridge(cfg.StateDir(), "plugin:"+pluginName)
}

func init() {
	pluginRunCmd.Flags().StringVar(&pluginRunSession, "session", "",
		"Bind the plugin run to a persisted session ID so session-aware capabilities work on the CLI")
	_ = pluginRunCmd.RegisterFlagCompletionFunc("session", completeSessionIDs)
	pluginRunCmd.Flags().StringVar(&pluginRunWorkdir, "workdir", "",
		"Override the plugin's Workdir (the path against which fs:read:./fs:write:. capabilities and relative file paths resolve). "+
			"Default is the plugin's install dir for backward compatibility — pass --workdir=$PWD when the plugin is meant to "+
			"read files from the operator's repo.")
}

func buildPluginRunBridge(ctx context.Context, cfg *config.Config, query, pluginName string, needProvider bool) (*pluginRuntime.SessionBridgeImpl, string, error) {
	id, err := resolveSessionID(cfg, query)
	if err != nil {
		return nil, "", fmt.Errorf("plugin run --session: %w", err)
	}
	sc, sess, err := openPersistedSession(cfg, id)
	if err != nil {
		return nil, "", fmt.Errorf("plugin run --session: open %s: %w", id, err)
	}
	msgs, err := runtime.LoadConversation(sess.WorktreePath)
	if err != nil {
		return nil, "", fmt.Errorf("plugin run --session: load conversation: %w", err)
	}

	var (
		prov agent.Provider
		note string
	)
	prov, err = pluginRunBuildProvider(cfg)
	if err != nil {
		if needProvider {
			return nil, "", fmt.Errorf("plugin run --session: provider: %w", err)
		}
		note = "stado plugin run --session: provider unavailable; llm:invoke is disabled and token_count will report 0"
		prov = nil
	}

	bridge := pluginRuntime.NewSessionBridge(sess, prov, cfg.Defaults.Model)
	bridge.PluginName = pluginName
	bridge.MessagesFn = func() []agent.Message {
		return append([]agent.Message(nil), msgs...)
	}
	bridge.TokensFn = func() int {
		return countPluginRunTokens(ctx, prov, cfg.Defaults.Model, msgs)
	}
	bridge.LastTurnRef = func() string {
		return lastPersistedTurnRef(sc, id)
	}
	bridge.ForkFn = func(ctx context.Context, atTurnRef, seed string) (string, error) {
		child, err := runtime.ForkPluginSession(cfg, sess, atTurnRef, seed, pluginName)
		if err != nil {
			return "", err
		}
		return child.ID, nil
	}
	return bridge, note, nil
}

func countPluginRunTokens(ctx context.Context, prov agent.Provider, model string, msgs []agent.Message) int {
	if prov == nil {
		return 0
	}
	tc, ok := prov.(agent.TokenCounter)
	if !ok {
		return 0
	}
	n, err := tc.CountTokens(ctx, agent.TurnRequest{
		Model:    model,
		Messages: msgs,
	})
	if err != nil {
		return 0
	}
	return n
}
