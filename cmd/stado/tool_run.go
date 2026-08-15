package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/plugins/bundled"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/toolinput"
	"github.com/foobarto/stado/internal/tools"
	pkgtool "github.com/foobarto/stado/pkg/tool"
)

var (
	toolRunSession string
	toolRunWorkdir string
	toolRunForce   bool
)

var toolRunCmd = &cobra.Command{
	Use:   "run <name> [json-args]",
	Short: "Run a single tool by canonical (fs.read) or wire (fs__read) name",
	Long: "Looks up the named tool in the live registry — bundled and\n" +
		"installed alike — and invokes it via the wasm runtime under the\n" +
		"manifest's declared capabilities. Accepts both canonical (fs.read)\n" +
		"and wire (fs__read) forms.\n\n" +
		"Bundled tools (fs.*, shell.*, agent.*, etc.) are dispatched from\n" +
		"the binary-embedded wasm; installed plugins are dispatched from\n" +
		"$XDG_DATA_HOME/stado/plugins/. Tools listed in [tools].disabled\n" +
		"are refused unless --force is passed.",
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		applyRootProviderOverrides(cfg)
		argsJSON := "{}"
		if len(args) >= 2 {
			argsJSON = args[1]
		}
		if err := toolinput.CheckLen(len(argsJSON)); err != nil {
			return err
		}
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		return runToolByName(ctx, args[0], argsJSON, toolRunOptions{
			Cfg:     cfg,
			Workdir: toolRunWorkdir,
			Session: toolRunSession,
			Force:   toolRunForce,
			Stdout:  cmd.OutOrStdout(),
			Stderr:  cmd.ErrOrStderr(),
		})
	},
}

type toolRunOptions struct {
	Cfg     *config.Config
	Workdir string // override workdir; "" = use cwd for bundled tools
	Session string
	Force   bool
	Stdout  io.Writer
	Stderr  io.Writer
}

// runToolByName is the testable entry point. Resolves name → registered
// tool, determines bundled vs installed, prepares Manifest + WASM,
// dispatches via runPluginInvocation.
func runToolByName(ctx context.Context, name, argsJSON string, opts toolRunOptions) error {
	cfg := opts.Cfg
	// Build the unfiltered registry: `tool run` is an operator-explicit
	// invocation, so we honour [tools].disabled via the dedicated refusal
	// below (with --force escape) rather than via ApplyToolFilter, which
	// would otherwise hide the tool and produce a misleading "not found".
	reg := runtime.BuildDefaultRegistry(cfg)

	registered, ok := lookupToolInRegistry(reg, name)
	if !ok {
		return fmt.Errorf("tool %q not found — try `stado tool list` to see available tools", name)
	}

	// PTY-bound shell tools (shell.spawn / list / read / write /
	// signal / resize / destroy / read_until) need a host that
	// holds the pty.Manager across calls. The daemon (`stado daemon`)
	// is exactly that host: route PTY-bound tools through the daemon,
	// auto-spawning it when STADO_DAEMON allows.
	//
	// When the daemon is unavailable AND auto-spawn is disabled (or
	// fails), refuse PTY-bound tools with the same actionable message
	// the original B5 fix carried — letting the bundled plugin run
	// in-process would silently produce a fresh empty pty.Manager and
	// the operator would hit the "session not found" path next call.
	if ptyBoundShellTool(registered.Name()) {
		mode := daemonMode()
		if mode == daemonModeOff {
			return errPTYRequiresDaemon(registered.Name(),
				"STADO_DAEMON=off — daemon dispatch disabled; the single-shot CLI cannot host live PTYs.")
		}
		if err := dispatchViaDaemon(ctx, registered.Name(), argsJSON, opts, mode); err != nil {
			return err
		}
		return nil
	}

	// Disabled-tool refusal: check registered name, canonical form, AND
	// the user-typed query against [tools].disabled patterns. Pass
	// --force to bypass.
	//
	// Codex #089: pre-fix only registered + canonical were checked, so
	// `disabled=["gtfobins.lookup"]` did NOT block `stado tool run
	// gtfobins.lookup` — the registered name was `gtfobins_lookup`
	// (single underscore, no canonical wire-name mapping),
	// and the pattern's `.` prevented any match. Matching the
	// user-typed `name` too means the operator's intent ("block this
	// tool by the dotted name I see in /tools") actually fires.
	if !opts.Force && cfg != nil {
		registeredName := registered.Name()
		canonical := runtime.ToolMetadataFor(registered).Canonical
		for _, pat := range cfg.Tools.Disabled {
			if runtime.ToolMatchesGlob(registeredName, pat) ||
				(canonical != "" && runtime.ToolMatchesGlob(canonical, pat)) ||
				runtime.ToolMatchesGlob(name, pat) {
				return fmt.Errorf("tool %q is disabled in [tools].disabled (matched pattern %q); remove it from disabled, or re-run with --force",
					name, pat)
			}
		}
	}

	// Codex #123: symmetric allowlist refusal. When [tools].enabled is
	// non-empty, refuse unless the registered / canonical / query name
	// matches an allow pattern. Pre-fix `tool run` ignored .enabled
	// entirely (only consulted .disabled), so `enabled=["read","grep"]`
	// + `stado tool run bash` ran bash — silently bypassing the
	// operator's allowlist. Same --force escape as the disabled path.
	if !opts.Force && cfg != nil && len(cfg.Tools.Enabled) > 0 {
		registeredName := registered.Name()
		canonical := runtime.ToolMetadataFor(registered).Canonical
		allowed := false
		for _, pat := range cfg.Tools.Enabled {
			if runtime.ToolMatchesGlob(registeredName, pat) ||
				(canonical != "" && runtime.ToolMatchesGlob(canonical, pat)) ||
				runtime.ToolMatchesGlob(name, pat) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("tool %q not in [tools].enabled allowlist %v; add it to enabled, or re-run with --force",
				name, cfg.Tools.Enabled)
		}
	}

	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	// Bundled path. Direct dispatch consumes the same verified manifest
	// projection as the default registry; it never reconstructs a contract
	// from the Go tool object.
	if contract, ok, lookupErr := bundled.LookupToolContract(registered.Name()); lookupErr != nil {
		return lookupErr
	} else if ok {
		identity, err := plugins.RuntimeIdentityForBundledSource(contract.Source, contract.Manifest)
		if err != nil {
			return fmt.Errorf("bundled identity: %w", err)
		}
		wasmBytes, err := bundled.Wasm(contract.Source)
		if err != nil {
			return fmt.Errorf("bundled wasm load: %w", err)
		}
		installDir, _ := os.Getwd()
		return runPluginInvocation(ctx, pluginInvokeArgs{
			Manifest:   contract.Manifest,
			Identity:   identity,
			WasmBytes:  wasmBytes,
			ToolName:   contract.Definition.Name,
			ArgsJSON:   argsJSON,
			Cfg:        cfg,
			WorkdirArg: opts.Workdir,
			InstallDir: installDir,
			SessionID:  opts.Session,
			Stdout:     stdout,
			Stderr:     stderr,
		})
	}

	// Installed-plugin path.
	if mfst, identity, wasmPath, ok := runtime.InstalledModuleForTool(registered); ok {
		// #023: registry construction (registerInstalledPluginTools) only
		// checks the trust-store signature + wasm sha — it does NOT consult
		// the configured CRL or transparency log. Without re-running the
		// full installed-plugin verifier here, a plugin that was trusted at
		// install time but has since been revoked (added to the operator's
		// CRL) would still run via `stado tool run <name>`. Re-verify
		// through the same path the runtime overrides + TUI /plugin use so
		// the revocation/transparency policy is enforced uniformly. The
		// sig isn't carried in the lookup table, so reload it from disk;
		// VerifyInstalledPlugin degrades CRL/Rekor unavailability the same
		// advisory way consultOverrideCRL/Rekor already do (air-gap safe).
		pluginDir := filepath.Dir(wasmPath)
		diskMfst, sig, loadErr := plugins.LoadFromDir(pluginDir)
		if loadErr != nil {
			return fmt.Errorf("verify: load installed plugin %q: %w", mfst.Name, loadErr)
		}
		if err := runtime.VerifyInstalledPlugin(ctx, cfg, pluginDir, diskMfst, sig); err != nil {
			return fmt.Errorf("verify: installed plugin %q: %w", mfst.Name, err)
		}
		wasmBytes, err := plugins.ReadVerifiedWASM(mfst.WASMSHA256, wasmPath)
		if err != nil {
			return fmt.Errorf("verify: %w", err)
		}
		// Find the matching ToolDef in the manifest. Installed plugins
		// use the registered tool name as-is — the plugin author chose
		// the wire-form name in their manifest.
		var bareName string
		for _, td := range mfst.Tools {
			if td.Name == registered.Name() {
				bareName = td.Name
				break
			}
		}
		if bareName == "" {
			return fmt.Errorf("internal: tool %q registered but not in installed manifest %q", registered.Name(), mfst.Name)
		}
		// Default workdir is the operator's CWD — mirrors the bundled-
		// plugin path above. Using filepath.Dir(wasmPath) here would
		// pin relative path args to the plugin install dir
		// (~/.local/share/stado/plugins/<name>-<ver>/), which surprises
		// operators who pass `./subdir` expecting it to resolve against
		// where they ran the command from. --workdir overrides.
		installDir, _ := os.Getwd()
		return runPluginInvocation(ctx, pluginInvokeArgs{
			Manifest:   mfst,
			Identity:   identity,
			WasmBytes:  wasmBytes,
			ToolName:   bareName,
			ArgsJSON:   argsJSON,
			Cfg:        cfg,
			WorkdirArg: opts.Workdir,
			InstallDir: installDir,
			SessionID:  opts.Session,
			Stdout:     stdout,
			Stderr:     stderr,
		})
	}

	return fmt.Errorf("tool %q registered but its source plugin not found — try `stado plugin list`", registered.Name())
}

// ptyBoundShellTool reports whether a registered tool name maps to
// the PTY-binding family of the bundled shell module. These tools
// rely on the runtime's pty.Manager, which is per-Runtime — they
// can't make sense in the single-shot `stado tool run` CLI path.
// Both wire form (`shell__spawn`) and canonical form (`shell.spawn`)
// are checked so the gate trips regardless of how the tool was
// looked up.
func ptyBoundShellTool(name string) bool {
	canonical := runtime.CanonicalToolName(name)
	switch canonical {
	case
		"shell.spawn",
		"shell.list",
		"shell.read",
		"shell.write",
		"shell.signal",
		"shell.resize",
		"shell.destroy",
		// shell.read_until rides the same per-Runtime pty.Manager that
		// the rest of the PTY family does — a one-shot CLI invocation
		// can't reach a session id created in another process.
		"shell.read_until":
		return true
	}
	switch name {
	case
		"shell__spawn",
		"shell__list",
		"shell__read",
		"shell__write",
		"shell__signal",
		"shell__resize",
		"shell__destroy",
		"shell__read_until":
		return true
	}
	return false
}

// lookupToolInRegistry tries (in order): exact name match, canonical
// → wire conversion (double-underscore, bundled convention), canonical-
// metadata fallback, then single-underscore substitution. The last tier
// catches installed plugins whose authors use a single-underscore wire
// form (e.g. gtfobins_lookup) rather than the bundled double-underscore
// convention — `tool run gtfobins.lookup` should resolve to it.
func lookupToolInRegistry(reg *tools.Registry, query string) (pkgtool.Tool, bool) {
	if t, ok := reg.Get(query); ok {
		return t, true
	}
	if dot := strings.Index(query, "."); dot > 0 && dot < len(query)-1 {
		if wire, err := tools.WireForm(query[:dot], query[dot+1:]); err == nil {
			if t, ok := reg.Get(wire); ok {
				return t, true
			}
		}
	}
	for _, candidate := range reg.All() {
		if runtime.ToolMetadataFor(candidate).Canonical == query {
			return candidate, true
		}
	}
	if strings.Contains(query, ".") {
		if t, ok := reg.Get(strings.ReplaceAll(query, ".", "_")); ok {
			return t, true
		}
	}
	return nil, false
}

func init() {
	toolRunCmd.Flags().StringVar(&toolRunSession, "session", "",
		"Bind the tool run to a persisted session ID for session-aware capabilities (audit log, memory, fork). Does NOT persist PTYs — `stado tool run` is single-shot, so shell.spawn / list / read / write / etc. cannot survive across invocations. Use the TUI, MCP server, or agent loop for persistent shells.")
	_ = toolRunCmd.RegisterFlagCompletionFunc("session", completeSessionIDs)
	toolRunCmd.Flags().StringVar(&toolRunWorkdir, "workdir", "",
		"Override the tool's Workdir (default: cwd for bundled tools)")
	toolRunCmd.Flags().BoolVar(&toolRunForce, "force", false,
		"Run even if the tool is disabled in [tools].disabled")
}
