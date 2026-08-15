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
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/tool"
)

// RunArgs is the input to Run. Manifest + WasmBytes must already be
// verified by the caller (signature, sha256, trust store) — pluginrun
// trusts these inputs and instantiates them directly.
type RunArgs struct {
	// Manifest is the plugin manifest (declares tools, caps, version).
	Manifest plugins.Manifest

	// Identity is the loader-authenticated canonical source identity. Every
	// caller, including local development, must populate it explicitly.
	Identity plugins.RuntimeIdentity

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
	// declares session-aware caps (session:read, session:fork) or the generic
	// provider:invoke capability. Optional; nil means the plugin sees a no-op bridge
	// that fails gracefully when the cap is exercised. The bool reports
	// whether the plugin needs an operator-configured provider.
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

	// InvokeExecutor, when non-nil, routes stado_tool_invoke through
	// Executor.Run so nested calls get the same audit trailers, hooks,
	// and progress collection as top-level tool dispatch (Codex #043).
	// When set, takes precedence over InvokeRegistry.
	InvokeExecutor *tools.Executor

	// RegistryCatalog is bound by the concrete runtime adapter to the exact
	// config-filtered registry instance and authenticated caller namespace.
	// It exposes only generic catalog facts and digest-fenced surface edits.
	RegistryCatalog *pluginRuntime.RegistryCatalogAccess

	// ContextResources is bound to the exact outer tool-dispatch context. It
	// exposes generic, digest-fenced host facts; nil means this invocation has
	// no context-resource surface.
	ContextResources *pluginRuntime.ContextResourceAccess

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
	effectiveCapabilities, err := args.Manifest.EffectiveToolCapabilities(*tdef)
	if err != nil {
		err = fmt.Errorf("tool %q capabilities: %w", args.ToolName, err)
		return tool.Result{Error: err.Error()}, err
	}

	// Caller-cap inheritance: when this run is a nested stado_tool_invoke, gate
	// the inner tool with the CALLER's capabilities, not its own manifest's,
	// so a plugin can't escalate by invoking a more-powerful tool (#021).
	mf := args.Manifest
	mf.Capabilities = effectiveCapabilities
	if inherited, ok := pluginRuntime.InheritedCaps(ctx); ok {
		mf.Capabilities = intersectCapabilities(effectiveCapabilities, inherited)
	}
	identity := args.Identity
	if identityErr := identity.ValidateManifest(args.Manifest); identityErr != nil {
		return tool.Result{Error: identityErr.Error()}, fmt.Errorf("pluginrun: identity: %w", identityErr)
	}
	rtHost := pluginRuntime.NewHostWithIdentity(mf, identity, args.Workdir, nil)
	if args.Cfg != nil {
		rtHost.StateDir = args.Cfg.StateDir()
	}
	rtHost.AttachToolHost(h)
	rtHost.RegistryCatalog = args.RegistryCatalog
	rtHost.ContextResources = args.ContextResources

	// EP-0028: the exec:bash refuse-no-runner guard that used to live here is
	// gone. The exec:bash capability was dropped in EP-no-internal-tools
	// Step 4 (bash now routes through exec:proc:<glob> + the sandbox), so
	// Host.ExecBash could never be set and the branch was unreachable. Removed
	// with the field rather than left as documented-dead code.

	rt, err := pluginRuntime.New(ctx)
	if err != nil {
		return tool.Result{Error: err.Error()}, fmt.Errorf("pluginrun: runtime: %w", err)
	}
	defer func() { _ = rt.Close(ctx) }()

	stateDir := ""
	if args.Cfg != nil {
		stateDir = args.Cfg.StateDir()
	}
	rtHost.AttachAuthorityStores(stateDir, rt.InstanceStore(), args.SecretsAudit)

	if err := attachLifecycleBridges(ctx, rtHost, h, identity, args.Manifest, args.ToolName); err != nil {
		return tool.Result{Error: err.Error()}, fmt.Errorf("pluginrun: attach broker bridge: %w", err)
	}

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

	if rtHost.ToolInvoke != nil {
		if args.InvokeExecutor != nil {
			rtHost.ToolInvoke.Invoke = makeInvokeCallback(args.InvokeExecutor.Registry, args.InvokeExecutor, h)
		} else if args.InvokeRegistry != nil {
			rtHost.ToolInvoke.Invoke = makeInvokeCallback(args.InvokeRegistry, nil, h)
		}
	}

	needsSessionBridge := rtHost.SessionObserve || rtHost.SessionRead || rtHost.SessionFork || (args.SessionID != "" && rtHost.ProviderInvokeBudget > 0)
	if needsSessionBridge {
		if args.SessionBridgeBuilder != nil {
			bridge, note, berr := args.SessionBridgeBuilder(ctx, args.SessionID, identity.Canonical, rtHost.ProviderInvokeBudget > 0)
			if berr != nil {
				return tool.Result{Error: berr.Error()}, berr
			}
			rtHost.SessionBridge = bridge
			if note != "" && args.SessionBridgeNote != nil {
				args.SessionBridgeNote(note)
			}
		} else {
			// Fallback: minimal session bridge that gracefully fails on
			// session:* calls. Provider-only plugins instead use the concrete
			// tool host's provider bridge and do not need a synthetic session.
			bridge := pluginRuntime.NewSessionBridge(nil, nil, "")
			bridge.PluginName = identity.Canonical
			rtHost.SessionBridge = bridge
		}
	}

	if err := pluginRuntime.InstallHostImports(ctx, rt, rtHost); err != nil {
		return tool.Result{Error: err.Error()}, fmt.Errorf("pluginrun: host imports: %w", err)
	}
	mod, err := rt.InstantiateWithIdentity(ctx, args.WasmBytes, args.Manifest, args.Identity)
	if err != nil {
		return tool.Result{Error: err.Error()}, fmt.Errorf("pluginrun: instantiate: %w", err)
	}
	defer func() { _ = mod.Close(ctx) }()

	pt, err := pluginRuntime.NewPluginTool(mod, *tdef)
	if err != nil {
		return tool.Result{Error: err.Error()}, err
	}
	return pt.Run(ctx, args.Args, h)
}

func intersectCapabilities(selected, inherited []string) []string {
	allowed := make(map[string]struct{}, len(inherited))
	for _, capability := range inherited {
		allowed[capability] = struct{}{}
	}
	intersection := make([]string, 0, len(selected))
	for _, capability := range selected {
		if _, ok := allowed[capability]; ok {
			intersection = append(intersection, capability)
		}
	}
	return intersection
}

// attachLifecycleBridges pulls FleetBridge, PTYManager, ApprovalBridge
// off the caller's tool.Host via interface assertions and wires them
// into the plugin runtime host. Each is optional — host that lacks the
// interface leaves the bridge nil, which the host imports treat as
// "feature unavailable for this dispatch." Same pattern bundledPluginTool.Run
// has used since EP-0038c.
type artifactBrokerProvider interface {
	ArtifactBrokerBinding(context.Context, plugins.RuntimeIdentity, plugins.Manifest, string) (pluginRuntime.ArtifactBridgeBinding, error)
}

type evidenceBrokerProvider interface {
	EvidenceBrokerBinding(context.Context, plugins.RuntimeIdentity, plugins.Manifest, string) (pluginRuntime.EvidenceBridgeBinding, error)
}

func attachLifecycleBridges(ctx context.Context, rtHost *pluginRuntime.Host, h tool.Host, identity plugins.RuntimeIdentity, manifest plugins.Manifest, toolName string) error {
	if rtHost.NeedsArtifactBridge() {
		provider, ok := h.(artifactBrokerProvider)
		if !ok {
			return fmt.Errorf("artifact capabilities declared but no broker binding provider is attached")
		}
		binding, err := provider.ArtifactBrokerBinding(ctx, identity, manifest, toolName)
		if err != nil {
			return err
		}
		rtHost.ArtifactBridge = binding.Bridge
		rtHost.ArtifactCaller = binding.Caller
	}
	if rtHost.NeedsEvidenceBridge() {
		provider, ok := h.(evidenceBrokerProvider)
		if !ok {
			return fmt.Errorf("evidence capabilities declared but no broker binding provider is attached")
		}
		binding, err := provider.EvidenceBrokerBinding(ctx, identity, manifest, toolName)
		if err != nil {
			return err
		}
		rtHost.EvidenceBridge = binding.Bridge
	}
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
	if bridge, ok := h.(pluginRuntime.RenderBridge); ok {
		rtHost.RenderBridge = bridge
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
	return nil
}

// makeInvokeCallback returns the stado_tool_invoke dispatch closure.
// The closure routes inner tool calls through the supplied executor when
// present (audit + hooks + progress), otherwise through the registry.
// Today's CLI implementation built a fresh BuildDefaultRegistry per
// call — codex's review flagged that as bypassing active filters /
// overrides / MCP-attached tools. Routing through the active executor's
// registry instead means inner calls see the same surface the outer
// caller did.
func makeInvokeCallback(reg *tools.Registry, exec *tools.Executor, h tool.Host) func(context.Context, string, json.RawMessage) (string, error) {
	return func(ctx context.Context, name string, args json.RawMessage) (string, error) {
		var result tool.Result
		var err error
		if exec != nil {
			result, err = exec.Run(ctx, name, args, h)
		} else {
			result, err = reg.Run(ctx, name, args, h)
		}
		if err != nil {
			return "", err
		}
		if result.Error != "" {
			return "", fmt.Errorf("%s: %s", name, result.Error)
		}
		return result.Content, nil
	}
}
