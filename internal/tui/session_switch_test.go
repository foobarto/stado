package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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

func TestRenameSessionUpdatesPickerLabel(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)

	if err := m.renameSession(ids.second, "renamed session"); err != nil {
		t.Fatal(err)
	}
	if got := runtime.ReadDescription(filepath.Join(m.cfg.WorktreeDir(), ids.second)); got != "renamed session" {
		t.Fatalf("description = %q, want renamed session", got)
	}
	items, err := m.sessionPickerItems()
	if err != nil {
		t.Fatal(err)
	}
	var saw bool
	for _, it := range items {
		if it.ID == ids.second && it.Label == "renamed session" {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("renamed session missing from picker: %+v", items)
	}
}

func TestDeleteSessionRemovesInactiveSession(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)

	if err := m.deleteSession(ids.second); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.cfg.WorktreeDir(), ids.second)); !os.IsNotExist(err) {
		t.Fatalf("deleted worktree still exists or stat failed unexpectedly: %v", err)
	}
	items, err := m.sessionPickerItems()
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.ID == ids.second {
			t.Fatalf("deleted session still listed: %+v", items)
		}
	}
}

func TestDeleteSessionBlocksActiveSession(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)

	err := m.deleteSession(ids.first)
	if err == nil || !strings.Contains(err.Error(), "active session") {
		t.Fatalf("expected active-session delete error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(m.cfg.WorktreeDir(), ids.first)); err != nil {
		t.Fatalf("active worktree should remain: %v", err)
	}
}

func TestForkAndSwitchSessionCreatesChild(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)

	if err := m.forkAndSwitchSession(ids.second); err != nil {
		t.Fatal(err)
	}
	if m.session.ID == ids.first || m.session.ID == ids.second {
		t.Fatalf("fork reused existing id: %s", m.session.ID)
	}
	if m.executor == nil || m.executor.Session == nil || m.executor.Session.ID != m.session.ID {
		t.Fatalf("executor did not retarget to fork: %+v", m.executor)
	}
	if _, err := os.Stat(m.session.WorktreePath); err != nil {
		t.Fatalf("fork worktree missing: %v", err)
	}
}

func TestSessionPickerModalRenameFlow(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)
	if err := m.openSessionPicker(); err != nil {
		t.Fatal(err)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	if !m.sessionPick.Renaming() {
		t.Fatal("ctrl+r should enter rename mode")
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
	for _, r := range "modal rename" {
		_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := runtime.ReadDescription(filepath.Join(m.cfg.WorktreeDir(), ids.second)); got != "modal rename" {
		t.Fatalf("description = %q, want modal rename", got)
	}
}

func TestSessionPickerRenameModeOwnsShortcuts(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)
	if err := m.openSessionPicker(); err != nil {
		t.Fatal(err)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlR})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	if !m.sessionPick.Renaming() {
		t.Fatal("ctrl+n should not leave rename mode")
	}
	if got := m.session.ID; got != ids.first {
		t.Fatalf("ctrl+n in rename mode changed session: %s", got)
	}
}

func TestSessionPickerModalDeleteFlow(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)
	if err := m.openSessionPicker(); err != nil {
		t.Fatal(err)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlD})
	if !m.sessionPick.Deleting() {
		t.Fatal("ctrl+d should enter delete mode")
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if _, err := os.Stat(filepath.Join(m.cfg.WorktreeDir(), ids.second)); !os.IsNotExist(err) {
		t.Fatalf("deleted worktree still exists or stat failed unexpectedly: %v", err)
	}
	if !m.sessionPick.Visible || m.sessionPick.Deleting() {
		t.Fatal("picker should reopen after delete")
	}
}

func TestSessionPickerModalForkFlow(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)
	if err := m.openSessionPicker(); err != nil {
		t.Fatal(err)
	}
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlF})
	if m.session.ID == ids.first || m.session.ID == ids.second {
		t.Fatalf("fork did not switch to a child session: %s", m.session.ID)
	}
	if m.sessionPick.Visible {
		t.Fatal("picker should close after fork")
	}
}

func TestSwitchToSessionPreservesDraftAndScroll(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)
	if err := runtime.WriteConversation(m.session.WorktreePath, []agent.Message{
		agent.Text(agent.RoleAssistant, strings.Repeat("first session line\n", 24)),
	}); err != nil {
		t.Fatal(err)
	}
	m.vp.Width = 80
	m.vp.Height = 5
	m.LoadPersistedConversation()
	m.renderBlocks()
	m.vp.SetYOffset(3)
	m.input.SetValue("draft for first")

	if err := m.switchToSession(ids.second); err != nil {
		t.Fatal(err)
	}
	if got := m.input.Value(); got != "" {
		t.Fatalf("newly visited session should not inherit draft, got %q", got)
	}
	m.input.SetValue("draft for second")

	if err := m.switchToSession(ids.first); err != nil {
		t.Fatal(err)
	}
	if got := m.input.Value(); got != "draft for first" {
		t.Fatalf("first session draft = %q", got)
	}
	if got := m.vp.YOffset; got != 3 {
		t.Fatalf("first session scroll offset = %d, want 3", got)
	}

	if err := m.switchToSession(ids.second); err != nil {
		t.Fatal(err)
	}
	if got := m.input.Value(); got != "draft for second" {
		t.Fatalf("second session draft = %q", got)
	}
}

func TestSwitchToSessionPreservesProviderAndModelState(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)
	m.providerName = "anthropic"
	m.model = "claude-sonnet"
	m.tokenCounterChecked = true
	m.tokenCounterPresent = true

	if err := m.switchToSession(ids.second); err != nil {
		t.Fatal(err)
	}
	if m.providerName != "anthropic" || m.model != "claude-sonnet" {
		t.Fatalf("new session should inherit current provider/model, got %s/%s", m.providerName, m.model)
	}
	m.providerName = "lmstudio"
	m.model = "qwen3"
	m.provider = scenarioStub{}
	m.tokenCounterChecked = true
	m.tokenCounterPresent = true

	if err := m.switchToSession(ids.first); err != nil {
		t.Fatal(err)
	}
	if m.providerName != "anthropic" || m.model != "claude-sonnet" {
		t.Fatalf("first session provider/model = %s/%s", m.providerName, m.model)
	}
	if m.provider != nil {
		t.Fatalf("provider should be invalidated after provider restore, got %#v", m.provider)
	}
	if m.tokenCounterChecked || m.tokenCounterPresent {
		t.Fatalf("token counter probe should reset after provider restore")
	}

	if err := m.switchToSession(ids.second); err != nil {
		t.Fatal(err)
	}
	if m.providerName != "lmstudio" || m.model != "qwen3" {
		t.Fatalf("second session provider/model = %s/%s", m.providerName, m.model)
	}
}

func TestSwitchToSessionBlocksQueuedPrompt(t *testing.T) {
	m, _, ids := newSessionSwitchModel(t)
	m.queuedPrompt = "queued"

	err := m.switchToSession(ids.second)
	if err == nil || !strings.Contains(err.Error(), "queued prompt") {
		t.Fatalf("expected queued prompt safety error, got %v", err)
	}
	if got := m.session.ID; got != ids.first {
		t.Fatalf("session changed despite queued prompt: %s", got)
	}
}
