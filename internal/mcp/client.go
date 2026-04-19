package mcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	"github.com/foobarto/stado/internal/sandbox"
)

// ServerConfig carries the connection details for one MCP server.
// Runner + Policy are set when the server should be spawned inside a
// sandbox (stdio transport only — HTTP servers run in-process on the
// remote host). DESIGN §"Phase 8.1 — per-MCP-server sandbox".
type ServerConfig struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
	URL     string

	// Runner spawns stdio servers through a sandbox. nil → stdio server
	// runs unwrapped (backwards-compat default). When non-nil, Policy
	// must also be set.
	Runner sandbox.Runner
	Policy sandbox.Policy
}

type MCPClient struct {
	Name   string
	Client *client.Client
	tools  []mcp.Tool
}

type MCPManager struct {
	mu      sync.Mutex
	clients map[string]*MCPClient
}

func NewManager() *MCPManager {
	return &MCPManager{
		clients: make(map[string]*MCPClient),
	}
}

func (m *MCPManager) Connect(ctx context.Context, cfg ServerConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var c *client.Client
	var err error

	if cfg.URL != "" {
		c, err = client.NewStreamableHttpClient(cfg.URL)
	} else {
		env := os.Environ()
		for k, v := range cfg.Env {
			if strings.HasPrefix(v, "@env:") {
				v = os.Getenv(strings.TrimPrefix(v, "@env:"))
			}
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
		var opts []transport.StdioOption
		if cfg.Runner != nil {
			// Route the subprocess spawn through the platform sandbox
			// runner. mcp-go calls our CommandFunc in place of
			// exec.CommandContext when building the stdio transport.
			policy := cfg.Policy
			runner := cfg.Runner
			opts = append(opts, transport.WithCommandFunc(
				func(ctx context.Context, command string, env []string, args []string) (*exec.Cmd, error) {
					cmd, err := runner.Command(ctx, policy, command, args)
					if err != nil {
						return nil, fmt.Errorf("sandbox for MCP server %s: %w", cfg.Name, err)
					}
					// mcp-go passes the merged env through `env`; re-apply
					// on top of the sandbox's filtered env so configured
					// key=value overrides still land.
					cmd.Env = append(cmd.Env, env...)
					return cmd, nil
				},
			))
		}
		c, err = client.NewStdioMCPClientWithOptions(cfg.Command, env, cfg.Args, opts...)
	}

	if err != nil {
		return fmt.Errorf("connect to MCP server %s: %w", cfg.Name, err)
	}

	_, err = c.Initialize(ctx, mcp.InitializeRequest{
		Params: mcp.InitializeParams{
			ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
			ClientInfo: mcp.Implementation{
				Name:    "stado",
				Version: "0.0.0-dev",
			},
		},
	})
	if err != nil {
		c.Close()
		return fmt.Errorf("initialize MCP server %s: %w", cfg.Name, err)
	}

	toolsResult, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		c.Close()
		return fmt.Errorf("list tools on MCP server %s: %w", cfg.Name, err)
	}

	m.clients[cfg.Name] = &MCPClient{
		Name:   cfg.Name,
		Client: c,
		tools:  toolsResult.Tools,
	}

	return nil
}

func (m *MCPManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.clients {
		c.Client.Close()
	}
}

func (m *MCPManager) GetClient(name string) (*MCPClient, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.clients[name]
	return c, ok
}

func (m *MCPClient) Tools() []mcp.Tool {
	return m.tools
}

func (m *MCPManager) AllClients() []*MCPClient {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*MCPClient, 0, len(m.clients))
	for _, c := range m.clients {
		out = append(out, c)
	}
	return out
}
