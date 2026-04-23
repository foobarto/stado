package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/pkg/agent"
)

type pluginRunProviderStub struct{}

func (pluginRunProviderStub) Name() string                     { return "plugin-run-test" }
func (pluginRunProviderStub) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (pluginRunProviderStub) StreamTurn(context.Context, agent.TurnRequest) (<-chan agent.Event, error) {
	ch := make(chan agent.Event)
	close(ch)
	return ch, nil
}
func (pluginRunProviderStub) CountTokens(context.Context, agent.TurnRequest) (int, error) {
	return 123, nil
}

func TestBuildPluginRunBridge_SessionAwareForkPersistsSeed(t *testing.T) {
	cfg, restore := resolveEnv(t, []string{"plugin-session"}, nil)
	defer restore()

	oldBuildProvider := pluginRunBuildProvider
	pluginRunBuildProvider = func(*config.Config) (agent.Provider, error) {
		return pluginRunProviderStub{}, nil
	}
	defer func() { pluginRunBuildProvider = oldBuildProvider }()

	sc, sess, err := openPersistedSession(cfg, "plugin-session")
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.AppendMessage(sess.WorktreePath, agent.Text(agent.RoleUser, "first ask")); err != nil {
		t.Fatal(err)
	}
	if err := runtime.AppendMessage(sess.WorktreePath, agent.Text(agent.RoleAssistant, "first answer")); err != nil {
		t.Fatal(err)
	}
	tree, err := sess.BuildTreeFromDir(sess.WorktreePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.CommitToTree(tree, stadogit.CommitMeta{Tool: "write", Summary: "seed"}); err != nil {
		t.Fatal(err)
	}
	if err := sess.NextTurn(); err != nil {
		t.Fatal(err)
	}

	bridge, note, err := buildPluginRunBridge(context.Background(), cfg, "plugin-session", "auto-compact", true)
	if err != nil {
		t.Fatalf("buildPluginRunBridge: %v", err)
	}
	if note != "" {
		t.Fatalf("unexpected note: %q", note)
	}
	if got := bridge.TokensFn(); got != 123 {
		t.Fatalf("TokensFn = %d, want 123", got)
	}
	if got := bridge.LastTurnRef(); !strings.HasSuffix(got, "/turns/1") {
		t.Fatalf("LastTurnRef = %q, want suffix /turns/1", got)
	}

	childID, err := bridge.Fork(context.Background(), bridge.LastTurnRef(), "condensed summary")
	if err != nil {
		t.Fatalf("bridge.Fork: %v", err)
	}
	child, err := stadogit.OpenSession(sc, cfg.WorktreeDir(), childID)
	if err != nil {
		t.Fatalf("open child: %v", err)
	}
	msgs, err := runtime.LoadConversation(child.WorktreePath)
	if err != nil {
		t.Fatalf("load child conversation: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("child messages = %d, want 1", len(msgs))
	}
	body := msgs[0].Content[0].Text.Text
	if !strings.Contains(body, "[compaction summary") || !strings.Contains(body, "condensed summary") {
		t.Fatalf("unexpected child seed body: %q", body)
	}
	if _, err := sc.ResolveRef(stadogit.TraceRef(childID)); err != nil {
		t.Fatalf("child trace ref missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(child.WorktreePath, ".stado", "user-repo")); err != nil {
		t.Fatalf("child missing user-repo pin: %v", err)
	}
}
