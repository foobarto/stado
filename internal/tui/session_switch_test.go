package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

func newSessionSwitchModel(t *testing.T) (*Model, *config.Config, *runtimeSessionPair) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	first, err := runtime.NewSession(cfg, repo)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.NewSession(cfg, repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.WriteDescription(second.WorktreePath, "second label"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.WriteConversation(second.WorktreePath, []agent.Message{
		agent.Text(agent.RoleUser, "hello from session two"),
		agent.Text(agent.RoleAssistant, "reply from session two"),
	}); err != nil {
		t.Fatal(err)
	}

	rnd, _ := render.New(theme.Default())
	reg := keys.NewRegistry()
	m := NewModel(repo, "m", "p", func() (agent.Provider, error) { return nil, nil }, rnd, reg)
	m.cfg = cfg
	m.session = first
	m.executor, err = runtime.BuildExecutor(first, cfg, "stado-tui")
	if err != nil {
		t.Fatal(err)
	}
	m.width, m.height = 100, 30
	return m, cfg, &runtimeSessionPair{first: first.ID, second: second.ID}
}

type runtimeSessionPair struct {
	first  string
	second string
}

func TestSessionPickerItemsIncludeCurrentAndDescriptions(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)

	items, err := m.sessionPickerItems()
	if err != nil {
		t.Fatal(err)
	}
	var sawCurrent, sawSecond bool
	for _, it := range items {
		if it.ID == ids.first && it.Current {
			sawCurrent = true
		}
		if it.ID == ids.second && it.Label == "second label" {
			sawSecond = true
		}
	}
	if !sawCurrent {
		t.Fatalf("current session not marked in picker: %+v", items)
	}
	if !sawSecond {
		t.Fatalf("described session missing from picker: %+v", items)
	}
}

func TestSwitchToSessionLoadsPersistedConversation(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)

	if err := m.switchToSession(ids.second); err != nil {
		t.Fatal(err)
	}
	if got := m.session.ID; got != ids.second {
		t.Fatalf("session id = %s, want %s", got, ids.second)
	}
	if m.executor == nil || m.executor.Session == nil || m.executor.Session.ID != ids.second {
		t.Fatalf("executor did not retarget: %+v", m.executor)
	}
	if len(m.msgs) != 2 {
		t.Fatalf("loaded messages = %d, want 2", len(m.msgs))
	}
	if got := m.blocks[0].body; !strings.Contains(got, "hello from session two") {
		t.Fatalf("conversation not rendered after switch: %+v", m.blocks)
	}
}

func TestCreateAndSwitchSessionStartsLandingState(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)

	if err := m.createAndSwitchSession(); err != nil {
		t.Fatal(err)
	}
	if m.session.ID == ids.first || m.session.ID == ids.second {
		t.Fatalf("new session reused existing id: %s", m.session.ID)
	}
	if len(m.blocks) != 0 || len(m.msgs) != 0 {
		t.Fatalf("new session should start empty; blocks=%d msgs=%d", len(m.blocks), len(m.msgs))
	}
}

func TestSwitchToSessionBlocksDraft(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)
	m.input.SetValue("unsent draft")

	err := m.switchToSession(ids.second)
	if err == nil || !strings.Contains(err.Error(), "draft") {
		t.Fatalf("expected draft safety error, got %v", err)
	}
	if got := m.session.ID; got != ids.first {
		t.Fatalf("session changed despite draft: %s", got)
	}
}
