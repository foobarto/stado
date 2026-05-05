package main

import (
	"context"
	"fmt"

	"github.com/foobarto/stado/internal/config"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/tui"
	"github.com/foobarto/stado/pkg/agent"
)

// pluginRunBuildProvider is the provider-builder injection point used by
// buildPluginRunBridge below and overridden in tests. Kept as a package-level
// var so test code can substitute a stub before exercising the bridge path.
var pluginRunBuildProvider = tui.BuildProvider

// attachPluginMemoryBridge wires a local-disk memory bridge onto the plugin
// runtime host when the plugin's manifest declares it needs one. Shared
// between every code path that constructs a pluginRuntime.Host (CLI tool run
// and TUI plugin invocations alike).
func attachPluginMemoryBridge(cfg *config.Config, host *pluginRuntime.Host, pluginName string) {
	if cfg == nil || host == nil || !host.NeedsMemoryBridge() {
		return
	}
	host.MemoryBridge = pluginRuntime.NewLocalMemoryBridge(cfg.StateDir(), "plugin:"+pluginName)
}

// buildPluginRunBridge constructs the SessionBridge that session-aware plugin
// capabilities (session:read, session:fork, llm:invoke) rely on. The query
// string is either a session ID or a partial-prefix; resolution + opening
// happen here so callers receive a ready-to-use bridge.
func buildPluginRunBridge(ctx context.Context, cfg *config.Config, query, pluginName string, needProvider bool) (*pluginRuntime.SessionBridgeImpl, string, error) {
	id, err := resolveSessionID(cfg, query)
	if err != nil {
		return nil, "", fmt.Errorf("plugin run --session: %w", err)
	}
	sc, sess, err := openPersistedSession(cfg, id)
	if err != nil {
		return nil, "", fmt.Errorf("plugin run --session: open %s: %w", id, err)
	}
	msgs, err := runtime.LoadConversation(sess.WorktreePath)
	if err != nil {
		return nil, "", fmt.Errorf("plugin run --session: load conversation: %w", err)
	}

	var (
		prov agent.Provider
		note string
	)
	prov, err = pluginRunBuildProvider(cfg)
	if err != nil {
		if needProvider {
			return nil, "", fmt.Errorf("plugin run --session: provider: %w", err)
		}
		note = "stado plugin run --session: provider unavailable; llm:invoke is disabled and token_count will report 0"
		prov = nil
	}

	bridge := pluginRuntime.NewSessionBridge(sess, prov, cfg.Defaults.Model)
	bridge.PluginName = pluginName
	bridge.MessagesFn = func() []agent.Message {
		return append([]agent.Message(nil), msgs...)
	}
	bridge.TokensFn = func() int {
		return countPluginRunTokens(ctx, prov, cfg.Defaults.Model, msgs)
	}
	bridge.LastTurnRef = func() string {
		return lastPersistedTurnRef(sc, id)
	}
	bridge.ForkFn = func(ctx context.Context, atTurnRef, seed string) (string, error) {
		child, err := runtime.ForkPluginSession(cfg, sess, atTurnRef, seed, pluginName)
		if err != nil {
			return "", err
		}
		return child.ID, nil
	}
	return bridge, note, nil
}

// countPluginRunTokens returns the provider's token count for the supplied
// message slice when the provider implements TokenCounter, else 0. Used by
// the SessionBridge TokensFn so plugins can read the live conversation cost.
func countPluginRunTokens(ctx context.Context, prov agent.Provider, model string, msgs []agent.Message) int {
	if prov == nil {
		return 0
	}
	tc, ok := prov.(agent.TokenCounter)
	if !ok {
		return 0
	}
	n, err := tc.CountTokens(ctx, agent.TurnRequest{
		Model:    model,
		Messages: msgs,
	})
	if err != nil {
		return 0
	}
	return n
}
