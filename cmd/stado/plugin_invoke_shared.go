package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/runtime/pluginrun"
	"github.com/foobarto/stado/internal/sandbox"
)

// pluginInvokeArgs is the input to runPluginInvocation. The caller
// (tool_run.go for bundled plugins; future installed-plugin invokers)
// is responsible for loading + verifying the manifest and the wasm
// bytes; this helper handles workdir resolution + CLI-flavoured
// stdout/stderr formatting around the shared pluginrun.Run dispatch.
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

// runPluginInvocation is the CLI's wrapper around pluginrun.Run. It
// resolves the --workdir arg, sets up CLI-flavoured Progress / Secrets
// audit / SessionBridge note callbacks (all of which stream to Stderr),
// dispatches via pluginrun, and prints the result envelope to Stdout.
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

	// CLI-side host: minimal mock providing workdir, runner, and
	// auto-approval. Agent loop / MCP server callers of pluginrun.Run
	// pass their real tool.Host carrying lifecycle bridges.
	runner := sandbox.Detect()
	host := newPluginRunToolHost(workdir, runner, false)

	// stado_tool_invoke dispatch from this CLI invocation routes
	// against the live BuildDefaultRegistry — same set the agent loop
	// would see. Per-call construction is fine for the CLI single-shot
	// path (no surrounding executor with filters).
	invokeReg := runtime.BuildDefaultRegistry(cfg)

	args := pluginrun.RunArgs{
		Manifest:  in.Manifest,
		WasmBytes: in.WasmBytes,
		ToolName:  in.ToolName,
		Args:      json.RawMessage(in.ArgsJSON),
		Cfg:       cfg,
		Workdir:   workdir,
		SessionID: in.SessionID,
		// Progress / SecretsAudit go to Stderr in CLI mode. Agent loop
		// and MCP server pass nil here; they wire progress via the
		// host's ProgressEmitter interface that pluginrun assertion
		// pulls off the host.
		SecretsAudit: func(ev pluginRuntime.SecretsAuditEvent) {
			fmt.Fprintf(in.Stderr,
				"stado-audit: secrets op=%s secret=%q plugin=%s allowed=%v reason=%s\n",
				ev.Op, ev.Secret, ev.Plugin, ev.Allowed, ev.Reason)
		},
		InvokeRegistry: invokeReg,
		SessionBridgeNote: func(note string) {
			fmt.Fprintln(in.Stderr, note)
		},
		// SessionBridgeBuilder is wired only when the CLI was invoked
		// with --session OR the manifest declares a session-aware cap
		// without --session (in which case we want the CLI's
		// "pass --session to attach" note rather than nil-bridge silent
		// failure). pluginrun's nil-bridge path also surfaces the cap
		// gracefully — but the CLI message is more helpful, so we
		// always supply the builder.
		SessionBridgeBuilder: func(ctx context.Context, sessionID, pluginName string, withLLM bool) (pluginRuntime.SessionBridge, string, error) {
			if sessionID != "" {
				bridge, note, err := buildPluginRunBridge(ctx, cfg, sessionID, pluginName, withLLM)
				return bridge, note, err
			}
			bridge := pluginRuntime.NewSessionBridge(nil, nil, "")
			bridge.PluginName = pluginName
			return bridge, "stado plugin run: session-aware capabilities declared; note that the one-shot CLI has no live session — pass --session <id> to attach to a persisted session", nil
		},
	}

	// Compute exec:bash refuse-no-runner check before pluginrun (which
	// also enforces it) so the CLI's error message is the rich one with
	// install hints instead of pluginrun's generic copy. After Step 4
	// migrates bash to exec:proc, this check disappears.
	probeHost := pluginRuntime.NewHost(in.Manifest, workdir, nil)
	if probeHost.ExecBash && !probeHost.ExecProc && runner.Name() == "none" {
		if cfg.Sandbox.RefuseNoRunner {
			return fmt.Errorf(
				"plugin run: plugin %s declares exec:bash but no native sandbox runner is available on this host. Install bubblewrap (Linux: `apt install bubblewrap` / `dnf install bubblewrap`) or sandbox-exec (macOS: bundled with Xcode CLT), or set [sandbox] refuse_no_runner = false to run unsandboxed",
				in.Manifest.Name)
		}
		fmt.Fprintf(in.Stderr,
			"stado: warn: plugin %s declares exec:bash but no native sandbox runner is available — running unsandboxed. Set [sandbox] refuse_no_runner = true to hard-fail instead.\n",
			in.Manifest.Name)
	}

	res, err := pluginrun.Run(ctx, args, host)
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

// runPluginInvocationCaptured is the same as runPluginInvocation but
// returns the result content as a string instead of writing to Stdout.
// Used by the stado_tool_invoke recursive path so it can capture inner
// plugin output and return it to the calling wasm guest.
//
// Kept here (not in pluginrun) because it's a CLI-affordance: the
// agent loop and MCP server callers of pluginrun call pluginrun.Run
// directly and use its tool.Result return value without this wrapper.
func runPluginInvocationCaptured(ctx context.Context, in pluginInvokeArgs) (string, error) {
	var buf bytes.Buffer
	in.Stdout = &buf
	if in.Stderr == nil {
		in.Stderr = io.Discard
	}
	if err := runPluginInvocation(ctx, in); err != nil {
		return "", err
	}
	// runPluginInvocation Fprintln's the content; trim the trailing
	// newline so callers see the same shape they'd get from a direct
	// reg.Run() call.
	out := buf.String()
	if len(out) > 0 && out[len(out)-1] == '\n' {
		out = out[:len(out)-1]
	}
	return out, nil
}
