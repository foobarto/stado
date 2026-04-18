package mcp

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

type ServerConfig struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
	URL     string
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
		c, err = client.NewStdioMCPClient(cfg.Command, env, cfg.Args...)
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
