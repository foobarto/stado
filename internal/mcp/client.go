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
	// Build the new client entirely outside the lock so transient
	// failures (network blip, stdio spawn failure, bad handshake)
	// don't block other callers of AllClients / GetClient.
	newClient, toolsList, err := m.connectAndProbe(ctx, cfg)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Replace only after the new connection is fully healthy. If the
	// replacement fails for any reason, the old connection stays alive
	// so tools don't become dead handles.
	if old, ok := m.clients[cfg.Name]; ok {
		_ = old.Client.Close()
	}
	m.clients[cfg.Name] = &MCPClient{
		Name:   cfg.Name,
		Client: newClient,
		tools:  toolsList,
	}
	return nil
}

// connectAndProbe builds, initializes, and probes one MCP client.
// Returns the live client + its tool list, or an error that may wrap
// connection / initialization / list-tools failures.
func (m *MCPManager) connectAndProbe(ctx context.Context, cfg ServerConfig) (*client.Client, []mcp.Tool, error) {
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
			policy := cfg.Policy
			runner := cfg.Runner
			opts = append(opts, transport.WithCommandFunc(
				func(ctx context.Context, command string, env []string, args []string) (*exec.Cmd, error) {
					cmd, pErr := runner.Command(ctx, policy, command, args)
					if pErr != nil {
						return nil, fmt.Errorf("sandbox for MCP server %s: %w", cfg.Name, pErr)
					}
					cmd.Env = append(cmd.Env, env...)
					return cmd, nil
				},
			))
		}
		c, err = client.NewStdioMCPClientWithOptions(cfg.Command, env, cfg.Args, opts...)
	}

	if err != nil {
		return nil, nil, fmt.Errorf("connect to MCP server %s: %w", cfg.Name, err)
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
		_ = c.Close()
		return nil, nil, fmt.Errorf("initialize MCP server %s: %w", cfg.Name, err)
	}

	toolsResult, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		_ = c.Close()
		return nil, nil, fmt.Errorf("list tools on MCP server %s: %w", cfg.Name, err)
	}

	return c, toolsResult.Tools, nil
}

func (m *MCPManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.clients {
		_ = c.Client.Close()
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
