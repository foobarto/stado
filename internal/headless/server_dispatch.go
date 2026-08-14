package headless

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/foobarto/stado/internal/acp"
	"github.com/foobarto/stado/internal/runtime"
)

// Serve runs the loop on r/w until the peer disconnects. Loads
// cfg.Plugins.Background plugins before dispatch starts; tears them
// down on exit.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	// EP-0064: headless has neither the persistent per-session application
	// owner nor the complete command/tool/hook/event bridge set. Reject before
	// loading a legacy background instance or accepting session work.
	if err := runtime.RequireLifecycleApplicationSurface(s.Cfg, nil, runtime.ApplicationSurfaceHeadless); err != nil {
		return fmt.Errorf("headless: %w", err)
	}
	s.conn = acp.NewConn(r, w)
	s.loadBackgroundPlugins(ctx)
	defer s.closeBackgroundPlugins(context.Background())
	defer s.closeSessionBrokers()
	return s.conn.Serve(ctx, s.dispatch)
}

func (s *Server) closeSessionBrokers() {
	s.mu.Lock()
	controllers := make([]runtime.BrokerController, 0, len(s.sessions))
	for _, sess := range s.sessions {
		if sess.broker != nil {
			controllers = append(controllers, sess.broker)
			sess.broker = nil
		}
	}
	s.mu.Unlock()
	for _, controller := range controllers {
		_ = controller.Close()
	}
}

func (s *Server) dispatch(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "session.new":
		return s.sessionNew(params)
	case "session.prompt":
		return s.sessionPrompt(ctx, params)
	case "session.list":
		return s.sessionList(), nil
	case "session.cancel":
		return s.sessionCancel(params)
	case "session.delete":
		return s.sessionDelete(params)
	case "session.compact":
		return s.sessionCompact(ctx, params)
	case "tools.list":
		return s.toolsList()
	case "providers.list":
		// `current` reflects what the server actually resolved, not
		// what's written in config — the local-fallback path leaves
		// cfg.Defaults.Provider empty even when a runner is serving.
		// Clients call this to learn which provider they're talking
		// to; blank when neither config nor a resolved runner applies.
		current := s.Cfg.Defaults.Provider
		if s.Provider != nil {
			current = s.Provider.Name()
		}
		return map[string]any{
			"available": availableProviders(s.Cfg),
			"current":   current,
		}, nil
	case "plugin.list":
		return s.pluginList(), nil
	case "plugin.run":
		return s.pluginRun(ctx, params)
	case "shutdown":
		// Wait for every other in-flight dispatch to complete before
		// we reply — otherwise shutdown races ahead of slow calls like
		// plugin.run, and the client sees responses arriving *after*
		// the shutdown ACK. Conn.Close then runs on the background
		// drain path in a fresh goroutine so this dispatch can return
		// + its response can flush before we tear down the pipe.
		s.conn.WaitPendingExceptCaller()
		go s.conn.Close()
		return struct{}{}, nil
	}
	return nil, &acp.RPCError{Code: acp.CodeMethodNotFound, Message: "unknown method: " + method}
}
