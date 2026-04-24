// Package headless is stado's editor-neutral JSON-RPC daemon surface.
//
// Reuses the line-delimited JSON-RPC transport from internal/acp so one
// implementation covers both the Zed-specific ACP server and this general
// one. Method set differs: headless uses dot-cased method names
// (session.new, tools.list, …) and is intended for scripting + editor
// integrations that aren't Zed.
package headless

import (
	"context"
	"sync"

	"github.com/foobarto/stado/internal/acp"
	"github.com/foobarto/stado/internal/config"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/pkg/agent"
)

// Server is the headless JSON-RPC daemon.
type Server struct {
	Cfg      *config.Config
	Provider agent.Provider

	conn     *acp.Conn
	mu       sync.Mutex
	sessions map[string]*hSession
	nextID   uint64 // monotonic counter so deleting sessions doesn't reuse IDs

	// Background-plugin state. See plugins.go. Populated on Serve()
	// entry from cfg.Plugins.Background and torn down on exit.
	bgRuntime *pluginRuntime.Runtime
	bgPlugins []*pluginRuntime.BackgroundPlugin
}

type hSession struct {
	id               string
	mu               sync.Mutex
	messages         []agent.Message
	cancel           context.CancelFunc
	workdir          string
	gitSess          *stadogit.Session // lazy, set by ensureGitSession
	persistedViewLen int               // folded conversation messages persisted to conversation.jsonl
	lastInputTokens  int               // most recent input-token observation
	busy             bool
}

func NewServer(cfg *config.Config, prov agent.Provider) *Server {
	return &Server{Cfg: cfg, Provider: prov, sessions: map[string]*hSession{}}
}
