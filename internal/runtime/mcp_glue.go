package runtime

import (
	"context"
	"fmt"
	"sort"
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
	mcpMgr      *mcp.MCPManager
	mcpOnce     sync.Once
	mcpStatusMu sync.Mutex
	mcpStatuses map[string]MCPServerStatus
)

// MCPServerStatus is the latest attach snapshot for one configured MCP
// server. It is informational only; rendering it must not trigger probes.
type MCPServerStatus struct {
	Name      string
	Connected bool
	ToolCount int
	Error     string
}

// MCPStatusSnapshot returns the latest MCP attach snapshot in stable order.
func MCPStatusSnapshot() []MCPServerStatus {
	mcpStatusMu.Lock()
	defer mcpStatusMu.Unlock()
	out := make([]MCPServerStatus, 0, len(mcpStatuses))
	for _, st := range mcpStatuses {
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func attachMCP(reg *tools.Registry, servers map[string]config.MCPServer) error {
	mcpOnce.Do(func() { mcpMgr = mcp.NewManager() })
	resetMCPStatuses(servers)

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
		// Only stdio servers (Command set without URL) participate in the
		// local sandbox; HTTP servers run on a remote host and can't be
		// wrapped. Refuse capability-less stdio servers rather than
		// silently running them with caller privileges.
		if cfg.Command != "" && cfg.URL == "" {
			if len(cfg.Capabilities) == 0 {
				err := fmt.Errorf("%s stdio MCP server: capabilities are required", name)
				recordMCPStatus(MCPServerStatus{Name: name, Error: err.Error()})
				errs = append(errs, err)
				continue
			}
			policy, err := mcp.ParseCapabilities(cfg.Capabilities)
			if err != nil {
				err := fmt.Errorf("%s capabilities: %w", name, err)
				recordMCPStatus(MCPServerStatus{Name: name, Error: err.Error()})
				errs = append(errs, err)
				continue
			}
			scfg.Policy = policy
			scfg.Runner = runner
		}
		if err := mcpMgr.Connect(ctx, scfg); err != nil {
			err := fmt.Errorf("connect %s: %w", name, err)
			recordMCPStatus(MCPServerStatus{Name: name, Error: err.Error()})
			errs = append(errs, err)
			continue
		}
		toolCount := 0
		if c, ok := mcpMgr.GetClient(name); ok {
			toolCount = len(c.Tools())
		}
		recordMCPStatus(MCPServerStatus{Name: name, Connected: true, ToolCount: toolCount})
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

func resetMCPStatuses(servers map[string]config.MCPServer) {
	mcpStatusMu.Lock()
	defer mcpStatusMu.Unlock()
	mcpStatuses = make(map[string]MCPServerStatus, len(servers))
	for name := range servers {
		mcpStatuses[name] = MCPServerStatus{Name: name}
	}
}

func recordMCPStatus(st MCPServerStatus) {
	mcpStatusMu.Lock()
	defer mcpStatusMu.Unlock()
	if mcpStatuses == nil {
		mcpStatuses = make(map[string]MCPServerStatus)
	}
	mcpStatuses[st.Name] = st
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
