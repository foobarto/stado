package runtime

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/mcp"
	"github.com/foobarto/stado/internal/mcpbridge"
	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/tools"
)

// mcpMgr is a process-lifetime MCP manager kept as a package singleton so
// multiple runtime.BuildExecutor calls reuse the same connections.
var (
	mcpMgr   *mcp.MCPManager
	mcpOnce  sync.Once
)

func attachMCP(reg *tools.Registry, servers map[string]config.MCPServer) error {
	mcpOnce.Do(func() { mcpMgr = mcp.NewManager() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	runner := sandbox.Detect()

	var errs []error
	for name, cfg := range servers {
		scfg := mcp.ServerConfig{
			Name:    name,
			Command: cfg.Command,
			Args:    cfg.Args,
			Env:     cfg.Env,
			URL:     cfg.URL,
		}
		// Only stdio servers (Command set) participate in the sandbox;
		// HTTP servers run on a remote host and can't be wrapped.
		if cfg.Command != "" && len(cfg.Capabilities) > 0 {
			policy, err := mcp.ParseCapabilities(cfg.Capabilities)
			if err != nil {
				errs = append(errs, fmt.Errorf("%s capabilities: %w", name, err))
				continue
			}
			scfg.Policy = policy
			scfg.Runner = runner
		} else if cfg.Command != "" {
			// No capabilities declared — keep the unsandboxed default
			// but emit a one-line advisory so operators notice. Lines
			// up with DESIGN §"Phase 8.1 — per-MCP-server sandbox":
			// out-of-manifest syscalls fail visibly only when a
			// manifest exists.
			fmt.Fprintf(os.Stderr,
				"stado: MCP server %s has no capabilities declared — runs with calling process privileges\n", name)
		}
		if err := mcpMgr.Connect(ctx, scfg); err != nil {
			errs = append(errs, fmt.Errorf("connect %s: %w", name, err))
			continue
		}
	}

	for _, c := range mcpMgr.AllClients() {
		for _, t := range c.Tools() {
			reg.Register(mcpbridge.MCPTool{ServerName: c.Name, Tool: t, Client: c})
		}
	}

	if len(errs) > 0 {
		return joinErrors(errs)
	}
	return nil
}

func joinErrors(errs []error) error {
	if len(errs) == 1 {
		return errs[0]
	}
	var s string
	for i, e := range errs {
		if i > 0 {
			s += "; "
		}
		s += e.Error()
	}
	return fmt.Errorf("%s", s)
}
