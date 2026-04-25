package runtime

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/sandbox"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/tasks"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/internal/tools/tasktool"
	"github.com/foobarto/stado/pkg/agent"
)

// BuildDefaultRegistry returns a Registry preloaded with stado's bundled tools
// (bash, fs, webfetch). Separate from Executor so callers can add/remove tools
// before constructing the Executor.
func BuildDefaultRegistry() *tools.Registry {
	return buildBundledPluginRegistry()
}

// ApplyToolFilter trims a registry per cfg.Tools. All tools are on
// by default; Enabled acts as an allowlist (keep only these);
// Disabled removes specific names from the default set. When both
// are set Enabled wins — Disabled is redundant against an explicit
// allowlist.
//
// Unknown tool names in either list log to stderr but don't abort —
// typo-tolerant so a user's config doesn't break stado across
// version upgrades that rename a tool. Mutates the registry in
// place so it's safe to chain after BuildDefaultRegistry.
func ApplyToolFilter(reg *tools.Registry, cfg *config.Config) {
	if cfg == nil {
		return
	}
	if len(cfg.Tools.Enabled) == 0 && len(cfg.Tools.Disabled) == 0 {
		return
	}
	known := map[string]bool{}
	for _, t := range reg.All() {
		known[t.Name()] = true
	}

	// Warn on unknown names so typos surface.
	warnUnknown := func(list []string, label string) {
		for _, n := range list {
			if !known[n] {
				fmt.Fprintf(os.Stderr, "stado: [tools].%s mentions %q — no such bundled tool (ignored)\n", label, n)
			}
		}
	}
	warnUnknown(cfg.Tools.Enabled, "enabled")
	warnUnknown(cfg.Tools.Disabled, "disabled")

	if len(cfg.Tools.Enabled) > 0 {
		allow := map[string]bool{}
		for _, n := range cfg.Tools.Enabled {
			if known[n] {
				allow[n] = true
			}
		}
		if len(allow) == 0 {
			return
		}
		for name := range known {
			if !allow[name] {
				reg.Unregister(name)
			}
		}
		return
	}
	// Disabled-only path.
	for _, n := range cfg.Tools.Disabled {
		reg.Unregister(n)
	}
}

// BuildExecutor wires the tool registry + session + sandbox runner.
//
// Also loads any MCP servers from config and registers their tools. Failed
// MCP connections are logged to stderr, not fatal — stado should boot
// without them if the endpoint is down.
//
// Respects cfg.Tools.Enabled / Disabled — the user's allowlist /
// blocklist is applied AFTER MCP tools land so MCP-sourced names can
// also be trimmed.
func BuildExecutor(sess *stadogit.Session, cfg *config.Config, agentName string) (*tools.Executor, error) {
	reg := BuildDefaultRegistry()
	reg.Register(tasktool.Tool{Path: tasks.StorePath(cfg.StateDir())})

	if len(cfg.MCP.Servers) > 0 {
		if err := attachMCP(reg, cfg.MCP.Servers); err != nil {
			fmt.Fprintf(os.Stderr, "stado: MCP setup: %v\n", err)
		}
	}
	if err := ApplyToolOverrides(reg, cfg); err != nil {
		return nil, err
	}
	ApplyToolFilter(reg, cfg)

	return &tools.Executor{
		Registry: reg,
		Session:  sess,
		Runner:   sandbox.Detect(),
		Agent:    agentName,
		Model:    cfg.Defaults.Model,
		ReadLog:  tools.NewReadLog(),
	}, nil
}

// attachMCP is defined in mcp_glue.go — kept in a separate file so pulling
// the MCP SDK in is a single-file diff and easier to #ifdef out on airgap
// builds later.

// ToolDefs renders the registry as []agent.ToolDef for a TurnRequest.
func ToolDefs(reg *tools.Registry) []agent.ToolDef {
	if reg == nil {
		return nil
	}
	all := reg.All()
	out := make([]agent.ToolDef, 0, len(all))
	for _, t := range all {
		schema, _ := json.Marshal(t.Schema())
		out = append(out, agent.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      schema,
		})
	}
	return out
}

func allowedToolSet(defs []agent.ToolDef) map[string]struct{} {
	out := make(map[string]struct{}, len(defs))
	for _, def := range defs {
		out[def.Name] = struct{}{}
	}
	return out
}

func toolAllowed(allowed map[string]struct{}, name string) bool {
	if len(allowed) == 0 {
		return false
	}
	_, ok := allowed[name]
	return ok
}

func unavailableToolResult(name string) string {
	return fmt.Sprintf("tool %q is not available for this turn", name)
}
