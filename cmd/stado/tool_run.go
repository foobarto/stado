package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/bundledplugins"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
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

	// Disabled-tool refusal: check both registered name and canonical
	// form against [tools].disabled patterns. Pass --force to bypass.
	if !opts.Force && cfg != nil {
		registeredName := registered.Name()
		canonical := runtime.LookupToolMetadata(registeredName).Canonical
		for _, pat := range cfg.Tools.Disabled {
			if runtime.ToolMatchesGlob(registeredName, pat) ||
				(canonical != "" && runtime.ToolMatchesGlob(canonical, pat)) {
				return fmt.Errorf("tool %q is disabled in [tools].disabled (matched pattern %q); remove it from disabled, or re-run with --force",
					name, pat)
			}
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

	// Bundled path.
	if info, ok := bundledplugins.LookupModuleByToolName(registered.Name()); ok {
		pluginName := bundledplugins.ManifestNamePrefix + "-" + info.Name
		bareToolDef := toolDefFromRegistered(registered)
		manifest := plugins.Manifest{
			Name:         pluginName,
			Version:      info.Version,
			Author:       info.Author,
			Capabilities: info.Capabilities,
			Tools:        []plugins.ToolDef{bareToolDef},
		}
		wasmBytes, err := bundledplugins.Wasm(info.Name)
		if err != nil {
			return fmt.Errorf("bundled wasm load: %w", err)
		}
		installDir, _ := os.Getwd()
		return runPluginInvocation(ctx, pluginInvokeArgs{
			Manifest:   manifest,
			WasmBytes:  wasmBytes,
			ToolName:   bareToolDef.Name,
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
	if mfst, wasmPath, ok := runtime.LookupInstalledModule(registered.Name()); ok {
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
		return runPluginInvocation(ctx, pluginInvokeArgs{
			Manifest:   mfst,
			WasmBytes:  wasmBytes,
			ToolName:   bareName,
			ArgsJSON:   argsJSON,
			Cfg:        cfg,
			WorkdirArg: opts.Workdir,
			InstallDir: filepath.Dir(wasmPath),
			SessionID:  opts.Session,
			Stdout:     stdout,
			Stderr:     stderr,
		})
	}

	return fmt.Errorf("tool %q registered but its source plugin not found — try `stado plugin list`", registered.Name())
}

// lookupToolInRegistry tries (in order): exact name match, canonical
// → wire conversion, canonical-metadata fallback. Mirrors the lookup
// pattern in `stado tool info`.
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
		if runtime.LookupToolMetadata(candidate.Name()).Canonical == query {
			return candidate, true
		}
	}
	return nil, false
}

// toolDefFromRegistered builds a plugins.ToolDef from a registered
// tool. The Name field uses the bare suffix from a wire-form name
// (e.g. fs__read → "read") because the wasm dispatcher in
// internal/plugins/runtime/tool.go prepends "stado_tool_" to def.Name
// to resolve the export. Tools registered with non-wire-form names
// (legacy bare names like "read", "write") use the registered name
// as-is — ParseWireForm returns ok=false for those.
func toolDefFromRegistered(t pkgtool.Tool) plugins.ToolDef {
	registered := t.Name()
	bare := registered
	if alias, sub, ok := tools.ParseWireForm(registered); ok && alias != "" {
		bare = sub
	}
	return plugins.ToolDef{
		Name:        bare,
		Description: t.Description(),
		Schema:      marshalSchemaJSON(t.Schema()),
	}
}

// marshalSchemaJSON serializes a schema map as JSON. Returns "{}" on
// error so the wasm dispatcher receives a parseable empty schema.
func marshalSchemaJSON(schema map[string]any) string {
	if schema == nil {
		return `{"type":"object"}`
	}
	b, err := json.Marshal(schema)
	if err != nil {
		return `{"type":"object"}`
	}
	return string(b)
}

func init() {
	toolRunCmd.Flags().StringVar(&toolRunSession, "session", "",
		"Bind the tool run to a persisted session ID so session-aware capabilities work on the CLI")
	_ = toolRunCmd.RegisterFlagCompletionFunc("session", completeSessionIDs)
	toolRunCmd.Flags().StringVar(&toolRunWorkdir, "workdir", "",
		"Override the tool's Workdir (default: cwd for bundled tools)")
	toolRunCmd.Flags().BoolVar(&toolRunForce, "force", false,
		"Run even if the tool is disabled in [tools].disabled")
}
