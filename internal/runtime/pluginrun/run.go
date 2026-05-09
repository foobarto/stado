// Package pluginrun is the unified wasm-plugin invoker.
//
// Every plugin invocation in stado — agent loop, MCP server, CLI
// `stado tool run`, in-plugin `stado_tool_invoke` — funnels through
// pluginrun.Run. Before this package existed the body lived in
// cmd/stado/plugin_invoke_shared.go (package main), which made it
// unreachable from internal/runtime. Lifting the function out resolved
// that layering bug and let installedPluginTool.Run, bundledPluginTool.Run,
// and pluginOverrideTool.Run all dispatch the same way (no more
// "sentinel error returning Tool.Run for installed plugins" trap).
//
// The function is shaped like a tool.Tool method:
//
//	pluginrun.Run(ctx, args, host) (tool.Result, error)
//
// Caller responsibilities:
//   - Verify manifest + wasm before calling. pluginrun trusts the inputs.
//   - Provide a tool.Host. Lifecycle bridges (FleetBridge, PTYManager,
//     ApprovalBridge, ProgressEmitter) are pulled off the host via
//     interface assertions when present. CLI callers without a real host
//     pass a minimal mock that supplies workdir + runner.
//   - Provide optional callbacks for SessionBridge construction +
//     SecretsAuditEmitter when those caps are declared. nil = the
//     corresponding bridge is unwired (plugin sees a denied result if
//     it tries to use the cap).
//   - Provide an InvokeRegistry when the manifest declares tool:invoke.
//     Without it, stado_tool_invoke calls return -1 to the plugin.
package pluginrun

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/plugins/runtime/pty"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/secrets"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/tool"
)

// RunArgs is the input to Run. Manifest + WasmBytes must already be
// verified by the caller (signature, sha256, trust store) — pluginrun
// trusts these inputs and instantiates them directly.
type RunArgs struct {
	// Manifest is the plugin manifest (declares tools, caps, version).
	Manifest plugins.Manifest

	// WasmBytes is the verified wasm module body.
	WasmBytes []byte

	// ToolName is the bare tool name from the manifest (matches one of
	// Manifest.Tools[i].Name).
	ToolName string

	// Args is the JSON-encoded tool arguments. Passed to the wasm
	// export as-is.
	Args json.RawMessage

	// Cfg is the loaded stado config. Required for state dir, secrets
	// store path, etc.
	Cfg *config.Config

	// Workdir is the absolute path the plugin runs against. Caller
	// resolved any --workdir overrides before calling.
	Workdir string

	// SessionID identifies the persisted session for session-aware
	// caps. Empty means "no session attached." Used by SessionBridgeBuilder.
	SessionID string

	// SessionBridgeBuilder constructs a SessionBridge when the plugin
	// declares session-aware caps (session:read, session:fork,
	// llm:invoke). Optional; nil means the plugin sees a no-op bridge
	// that fails gracefully when the cap is exercised. The bool reports
	// whether the plugin needs an LLM provider (llm:invoke budget > 0).
	// Returns the bridge, an optional informational note, and an error.
	SessionBridgeBuilder func(ctx context.Context, sessionID, pluginName string, withLLM bool) (pluginRuntime.SessionBridge, string, error)

	// SecretsAudit, when set, receives one event per secrets:* host
	// import call (allowed and denied alike). Used by the CLI to print
	// audit lines to stderr.
	SecretsAudit func(pluginRuntime.SecretsAuditEvent)

	// InvokeRegistry, when non-nil, is the dispatch target for
	// stado_tool_invoke calls from this plugin. Pass the active
	// executor's registry from the agent loop / MCP server / CLI so
	// the inner call sees the same tool surface the outer caller did.
	// nil = stado_tool_invoke disabled (returns -1).
	InvokeRegistry *tools.Registry

	// SessionBridgeNote, when non-nil, receives any informational note
	// produced by SessionBridgeBuilder (e.g., "session-aware capabilities
	// declared; pass --session to attach"). Caller decides whether to
	// surface it. nil = drop the note.
	SessionBridgeNote func(note string)
}

// Run instantiates the plugin's wasm, wires host imports, dispatches
// the named tool, and returns its result. Reuses the caller-supplied
// tool.Host for lifecycle bridges (PTY, fleet, approvals, progress);
// caller must always pass a non-nil Host.
func Run(ctx context.Context, args RunArgs, h tool.Host) (tool.Result, error) {
	if h == nil {
		return tool.Result{Error: "pluginrun: nil host"}, fmt.Errorf("pluginrun: nil host")
	}

	rtHost := pluginRuntime.NewHost(args.Manifest, args.Workdir, nil)
	if args.Cfg != nil {
		rtHost.StateDir = args.Cfg.StateDir()
	}
	rtHost.ToolHost = h

	// Refuse exec:bash plugins on hosts without a sandbox runner — the
	// existing safety check from the CLI invocation path. Preserved
	// here so every dispatch path enforces it. After Step 4 (bash
	// migrates to exec:proc:bash with optional sandbox), exec:bash
	// disappears as a manifest cap and this branch becomes dead — fine.
	// nil cfg = test path; skip the refuse check since there's no
	// configured policy to enforce.
	runner := sandbox.Detect()
	if args.Cfg != nil && rtHost.ExecBash && !rtHost.ExecProc && runner.Name() == "none" {
		if args.Cfg.Sandbox.RefuseNoRunner {
			return tool.Result{
				Error: fmt.Sprintf(
					"plugin %s declares exec:bash but no native sandbox runner is available on this host. Install bubblewrap (Linux) or sandbox-exec (macOS), or set [sandbox] refuse_no_runner = false to run unsandboxed",
					args.Manifest.Name),
			}, fmt.Errorf("pluginrun: refuse_no_runner")
		}
	}

	rt, err := pluginRuntime.New(ctx)
	if err != nil {
		return tool.Result{Error: err.Error()}, fmt.Errorf("pluginrun: runtime: %w", err)
	}
	defer func() { _ = rt.Close(ctx) }()

	attachMemoryBridge(args.Cfg, rtHost, args.Manifest.Name)

	if rtHost.Secrets != nil && args.Cfg != nil {
		rtHost.Secrets.Store = secrets.NewStore(args.Cfg.StateDir())
		rtHost.Secrets.PluginName = args.Manifest.Name
		if args.SecretsAudit != nil {
			rtHost.Secrets.AuditEmitter = args.SecretsAudit
		}
	}
	if rtHost.State != nil {
		rtHost.State.Store = rt.InstanceStore()
		rtHost.State.PluginName = args.Manifest.Name
	}

	attachLifecycleBridges(rtHost, h)

	// Per-call progress collector lives in ctx (Executor.Run installs
	// it); the host's ProgressEmitter is already wired by
	// attachLifecycleBridges. Combine: emit to the operator surface
	// (TUI / stderr) AND append to the result-envelope collector so
	// the model sees plugin progress as part of the tool result.
	if progCollector := tool.ProgressFromContext(ctx); progCollector != nil {
		emitter := rtHost.Progress
		rtHost.Progress = func(plugin, text string) {
			if emitter != nil {
				emitter(plugin, text)
			}
			progCollector.Append(plugin, text)
		}
	}

	if rtHost.ToolInvoke != nil && args.InvokeRegistry != nil {
		rtHost.ToolInvoke.Invoke = makeInvokeCallback(args.InvokeRegistry, h)
	}

	if rtHost.SessionObserve || rtHost.SessionRead || rtHost.SessionFork || rtHost.LLMInvokeBudget > 0 {
		if args.SessionBridgeBuilder != nil {
			bridge, note, berr := args.SessionBridgeBuilder(ctx, args.SessionID, args.Manifest.Name, rtHost.LLMInvokeBudget > 0)
			if berr != nil {
				return tool.Result{Error: berr.Error()}, berr
			}
			rtHost.SessionBridge = bridge
			if note != "" && args.SessionBridgeNote != nil {
				args.SessionBridgeNote(note)
			}
		} else {
			// Fallback: minimal session bridge that gracefully fails on
			// session:* / llm:invoke calls. Same shape the CLI uses
			// today when --session is not provided.
			bridge := pluginRuntime.NewSessionBridge(nil, nil, "")
			bridge.PluginName = args.Manifest.Name
			rtHost.SessionBridge = bridge
		}
	}

	if err := pluginRuntime.InstallHostImports(ctx, rt, rtHost); err != nil {
		return tool.Result{Error: err.Error()}, fmt.Errorf("pluginrun: host imports: %w", err)
	}
	mod, err := rt.Instantiate(ctx, args.WasmBytes, args.Manifest)
	if err != nil {
		return tool.Result{Error: err.Error()}, fmt.Errorf("pluginrun: instantiate: %w", err)
	}
	defer func() { _ = mod.Close(ctx) }()

	var tdef *plugins.ToolDef
	for i := range args.Manifest.Tools {
		if args.Manifest.Tools[i].Name == args.ToolName {
			tdef = &args.Manifest.Tools[i]
			break
		}
	}
	if tdef == nil {
		err := fmt.Errorf("tool %q not declared in plugin manifest %q", args.ToolName, args.Manifest.Name)
		return tool.Result{Error: err.Error()}, err
	}

	pt, err := pluginRuntime.NewPluginTool(mod, *tdef)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	return pt.Run(ctx, args.Args, h)
}

// attachMemoryBridge wires a local-disk memory bridge onto the
// plugin runtime host when the manifest declares it needs one.
// Mirrors cmd/stado/plugin_run.go:attachPluginMemoryBridge — kept
// in this package so callers don't need to remember the wiring.
func attachMemoryBridge(cfg *config.Config, host *pluginRuntime.Host, pluginName string) {
	if cfg == nil || host == nil || !host.NeedsMemoryBridge() {
		return
	}
	host.MemoryBridge = pluginRuntime.NewLocalMemoryBridge(cfg.StateDir(), "plugin:"+pluginName)
}

// attachLifecycleBridges pulls FleetBridge, PTYManager, ApprovalBridge
// off the caller's tool.Host via interface assertions and wires them
// into the plugin runtime host. Each is optional — host that lacks the
// interface leaves the bridge nil, which the host imports treat as
// "feature unavailable for this dispatch." Same pattern bundledPluginTool.Run
// has used since EP-0038c.
func attachLifecycleBridges(rtHost *pluginRuntime.Host, h tool.Host) {
	if afp, ok := h.(tool.AgentFleetProvider); ok {
		if fb, ok := afp.AgentFleetBridge().(pluginRuntime.FleetBridge); ok {
			rtHost.FleetBridge = fb
		}
	}
	if pp, ok := h.(tool.PTYProvider); ok {
		if pm, ok := pp.PTYManager().(*pty.Manager); ok && pm != nil {
			rtHost.PTYManager = pm
		}
	}
	if bridge, ok := h.(pluginRuntime.ApprovalBridge); ok {
		rtHost.ApprovalBridge = bridge
	}
	if bridge, ok := h.(pluginRuntime.ChoiceBridge); ok {
		rtHost.ChoiceBridge = bridge
	}
	if bridge, ok := h.(pluginRuntime.PrintBridge); ok {
		rtHost.PrintBridge = bridge
	}
	// SandboxPolicyProvider plumbs a host-default sandbox policy into
	// stado_exec / stado_proc_spawn. mcp-server / daemon set this so
	// guest plugins that don't supply their own `sandbox` field still
	// get bwrap / sandbox-exec confinement. Pre-2026-05-09: the
	// mcp-server header comment claimed this happened; it didn't,
	// because there was no plumbing. The plumbing is now here.
	if pp, ok := h.(tool.SandboxPolicyProvider); ok {
		rtHost.DefaultSandboxPolicy = pp.DefaultSandboxPolicy()
	}
	// Progress emitter has two routes: the host's tool.ProgressEmitter
	// interface (TUI / headless run / stderr) and any per-call collector
	// installed in ctx by Executor.Run. The collector path runs at the
	// agent-loop call site, not here — pluginrun is invoked under whatever
	// ctx the caller supplied, and the bundledPluginTool.Run pattern
	// continues to install the collector pre-call. Here we only wire
	// the EmitProgress route.
	if pe, ok := h.(tool.ProgressEmitter); ok {
		rtHost.Progress = func(plugin, text string) {
			pe.EmitProgress(plugin, text)
		}
	}
}

// makeInvokeCallback returns the stado_tool_invoke dispatch closure.
// The closure routes inner tool calls through the supplied registry.
// Today's CLI implementation built a fresh BuildDefaultRegistry per
// call — codex's review flagged that as bypassing active filters /
// overrides / MCP-attached tools. Routing through the active executor's
// registry instead means inner calls see the same surface the outer
// caller did.
func makeInvokeCallback(reg *tools.Registry, h tool.Host) func(context.Context, string, json.RawMessage) (string, error) {
	return func(ctx context.Context, name string, args json.RawMessage) (string, error) {
		result, err := reg.Run(ctx, name, args, h)
		if err != nil {
			return "", err
		}
		if result.Error != "" {
			return "", fmt.Errorf("%s: %s", name, result.Error)
		}
		return result.Content, nil
	}
}
