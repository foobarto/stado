package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/sandbox"
)

// pluginInvokeArgs is the input to runPluginInvocation. The caller
// (tool_run.go for bundled plugins; future installed-plugin invokers)
// is responsible for loading + verifying the manifest and the wasm
// bytes; this helper handles the wasm instantiation, host-import
// wiring, and tool dispatch.
type pluginInvokeArgs struct {
	Manifest   plugins.Manifest // already loaded + verified by the caller
	WasmBytes  []byte           // already verified against Manifest.WASMSHA256
	ToolName   string           // tool def name (matches Manifest.Tools[i].Name)
	ArgsJSON   string           // JSON args; "{}" when omitted
	Cfg        *config.Config
	WorkdirArg string    // raw --workdir arg ("" = default to InstallDir)
	InstallDir string    // for default workdir + caller logging
	SessionID  string    // raw --session arg ("" = no session)
	Stdout     io.Writer // typically cmd.OutOrStdout()
	Stderr     io.Writer // typically cmd.ErrOrStderr()
}

// runPluginInvocation is the shared invoke body called from
// tool_run. Returns nil on success; an error on any failure.
// Prints res.Content to Stdout on success, res.Error to Stderr on a
// plugin-reported error.
func runPluginInvocation(ctx context.Context, in pluginInvokeArgs) error {
	cfg := in.Cfg

	// Resolve workdir: default to install dir; --workdir overrides.
	workdir := in.InstallDir
	if in.WorkdirArg != "" {
		abs, err := filepath.Abs(in.WorkdirArg)
		if err != nil {
			return fmt.Errorf("--workdir %q: %w", in.WorkdirArg, err)
		}
		info, err := os.Stat(abs)
		if err != nil {
			return fmt.Errorf("--workdir %q: %w", in.WorkdirArg, err)
		}
		if !info.IsDir() {
			return fmt.Errorf("--workdir %q: not a directory", in.WorkdirArg)
		}
		workdir = abs
	}

	host := pluginRuntime.NewHost(in.Manifest, workdir, nil)
	host.StateDir = cfg.StateDir()

	runner := sandbox.Detect()
	if host.ExecBash && !host.ExecProc && runner.Name() == "none" {
		if cfg.Sandbox.RefuseNoRunner {
			return fmt.Errorf(
				"plugin run: plugin %s declares exec:bash but no native sandbox runner is available on this host. Install bubblewrap (Linux: `apt install bubblewrap` / `dnf install bubblewrap`) or sandbox-exec (macOS: bundled with Xcode CLT), or set [sandbox] refuse_no_runner = false to run unsandboxed",
				in.Manifest.Name)
		}
		fmt.Fprintf(in.Stderr,
			"stado: warn: plugin %s declares exec:bash but no native sandbox runner is available — running unsandboxed. Set [sandbox] refuse_no_runner = true to hard-fail instead.\n",
			in.Manifest.Name)
	}

	rt, err := pluginRuntime.New(ctx)
	if err != nil {
		return fmt.Errorf("runtime: %w", err)
	}
	defer func() { _ = rt.Close(ctx) }()

	attachPluginMemoryBridge(cfg, host, in.Manifest.Name)
	host.ToolHost = newPluginRunToolHost(workdir, runner, host.NetHTTPRequestPrivate)

	if host.SessionObserve || host.SessionRead || host.SessionFork || host.LLMInvokeBudget > 0 {
		if in.SessionID != "" {
			bridge, note, err := buildPluginRunBridge(ctx, cfg, in.SessionID, in.Manifest.Name, host.LLMInvokeBudget > 0)
			if err != nil {
				return err
			}
			host.SessionBridge = bridge
			if note != "" {
				fmt.Fprintln(in.Stderr, note)
			}
		} else {
			bridge := pluginRuntime.NewSessionBridge(nil, nil, "")
			bridge.PluginName = in.Manifest.Name
			host.SessionBridge = bridge
			fmt.Fprintln(in.Stderr,
				"stado plugin run: session-aware capabilities declared; note that the one-shot CLI has no live session — pass --session <id> to attach to a persisted session")
		}
	}

	if err := pluginRuntime.InstallHostImports(ctx, rt, host); err != nil {
		return fmt.Errorf("host imports: %w", err)
	}
	mod, err := rt.Instantiate(ctx, in.WasmBytes, in.Manifest)
	if err != nil {
		return fmt.Errorf("instantiate: %w", err)
	}
	defer func() { _ = mod.Close(ctx) }()

	var tdef *plugins.ToolDef
	for i := range in.Manifest.Tools {
		if in.Manifest.Tools[i].Name == in.ToolName {
			tdef = &in.Manifest.Tools[i]
			break
		}
	}
	if tdef == nil {
		return fmt.Errorf("tool %q not declared in plugin manifest %q", in.ToolName, in.Manifest.Name)
	}
	pt, err := pluginRuntime.NewPluginTool(mod, *tdef)
	if err != nil {
		return err
	}
	res, err := pt.Run(ctx, []byte(in.ArgsJSON), nil)
	if err != nil {
		if res.Error != "" {
			fmt.Fprintln(in.Stderr, res.Error)
		}
		return err
	}
	if res.Error != "" {
		return fmt.Errorf("plugin error: %s", res.Error)
	}
	fmt.Fprintln(in.Stdout, res.Content)
	return nil
}
