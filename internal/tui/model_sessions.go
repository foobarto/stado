package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/internal/tui/filepicker"
	"github.com/foobarto/stado/internal/tui/sessionpicker"
	"github.com/foobarto/stado/pkg/agent"
)

type sessionUIState struct {
	Draft               string
	ViewportYOffset     int
	ActivityYOffset     int
	ProviderName        string
	Model               string
	TokenCounterChecked bool
	TokenCounterPresent bool
	HasProviderState    bool
}

func (m *Model) openSessionPicker() error {
	if m.session == nil || m.session.Sidecar == nil {
		return fmt.Errorf("session switch: no live session")
	}
	items, err := m.sessionPickerItems()
	if err != nil {
		return err
	}
	m.sessionPick.Open(items, m.session.ID)
	return nil
}

func (m *Model) sessionPickerItems() ([]sessionpicker.Item, error) {
	if m.session == nil || m.session.Sidecar == nil {
		return nil, fmt.Errorf("session switch: no live session")
	}
	worktreeRoot := filepath.Dir(m.session.WorktreePath)
	ids, err := listSessionIDs(worktreeRoot, m.session.Sidecar)
	if err != nil {
		return nil, err
	}
	if m.session.ID != "" {
		ids[m.session.ID] = struct{}{}
	}

	rows := make([]runtime.SessionSummary, 0, len(ids))
	for id := range ids {
		rows = append(rows, runtime.SummariseSession(worktreeRoot, m.session.Sidecar, id))
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].ID == m.session.ID {
			return true
		}
		if rows[j].ID == m.session.ID {
			return false
		}
		if !rows[i].LastActive.Equal(rows[j].LastActive) {
			return rows[i].LastActive.After(rows[j].LastActive)
		}
		return rows[i].ID < rows[j].ID
	})

	items := make([]sessionpicker.Item, 0, len(rows))
	for _, r := range rows {
		label := shortSessionID(r.ID)
		if r.Description != "" {
			label = r.Description
		}
		meta := fmt.Sprintf("%s  turns=%d msgs=%d", r.Status, r.Turns, r.Msgs)
		if !r.LastActive.IsZero() {
			meta = r.LastActive.UTC().Format("2006-01-02 15:04") + "  " + meta
		}
		if r.Description != "" {
			meta += "  " + shortSessionID(r.ID)
		}
		items = append(items, sessionpicker.Item{
			ID:      r.ID,
			Label:   label,
			Meta:    meta,
			Current: r.ID == m.session.ID,
		})
	}
	return items, nil
}

func (m *Model) filePickerSessionItems() []filepicker.Item {
	items, err := m.sessionPickerItems()
	if err != nil {
		return nil
	}
	out := make([]filepicker.Item, 0, len(items))
	for _, item := range items {
		meta := item.Meta
		if item.Current {
			if meta != "" {
				meta = "current  " + meta
			} else {
				meta = "current"
			}
		}
		out = append(out, filepicker.Item{
			Kind:    filepicker.KindSession,
			ID:      item.ID,
			Display: item.Label,
			Meta:    meta,
			Insert:  "session:" + item.ID,
		})
	}
	return out
}

func listSessionIDs(worktreeRoot string, sc *stadogit.Sidecar) (map[string]struct{}, error) {
	ids := map[string]struct{}{}
	iter, err := sc.Repo().References()
	if err != nil {
		return nil, fmt.Errorf("session list: %w", err)
	}
	defer iter.Close()
	_ = iter.ForEach(func(ref *plumbing.Reference) error {
		name := string(ref.Name())
		const prefix = "refs/sessions/"
		if !strings.HasPrefix(name, prefix) {
			return nil
		}
		rest := strings.TrimPrefix(name, prefix)
		id := strings.Split(rest, "/")[0]
		if stadogit.ValidateSessionID(id) == nil {
			ids[id] = struct{}{}
		}
		return nil
	})
	if entries, err := os.ReadDir(worktreeRoot); err == nil {
		for _, e := range entries {
			if e.IsDir() && stadogit.ValidateSessionID(e.Name()) == nil {
				ids[e.Name()] = struct{}{}
			}
		}
	}
	return ids, nil
}

func (m *Model) switchToSession(id string) error {
	if m.session != nil && id == m.session.ID {
		return nil
	}
	if err := m.ensureSessionSwitchAllowed(); err != nil {
		return err
	}
	cfg, err := m.sessionActionConfig()
	if err != nil {
		return err
	}
	sess, err := runtime.OpenSessionByID(cfg, m.sessionActionCWD(), id)
	if err != nil {
		return fmt.Errorf("session switch: %w", err)
	}
	exec, err := runtime.BuildExecutor(sess, cfg, "stado-tui")
	if err != nil {
		return fmt.Errorf("session switch tools: %w", err)
	}
	m.activateSession(sess, exec)
	return nil
}

func (m *Model) createAndSwitchSession() error {
	if err := m.ensureSessionSwitchAllowed(); err != nil {
		return err
	}
	cfg, err := m.sessionActionConfig()
	if err != nil {
		return err
	}
	sess, err := runtime.NewSession(cfg, m.sessionActionCWD())
	if err != nil {
		return fmt.Errorf("new session: %w", err)
	}
	exec, err := runtime.BuildExecutor(sess, cfg, "stado-tui")
	if err != nil {
		return fmt.Errorf("new session tools: %w", err)
	}
	m.activateSession(sess, exec)
	return nil
}

func (m *Model) renameSession(id, label string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("session rename: no session selected")
	}
	if filepath.Base(id) != id {
		return fmt.Errorf("session rename: invalid session id %q", id)
	}
	cfg, err := m.sessionActionConfig()
	if err != nil {
		return err
	}
	wt := filepath.Join(cfg.WorktreeDir(), id)
	if err := runtime.WriteDescription(wt, label); err != nil {
		return fmt.Errorf("session rename: %w", err)
	}
	return nil
}

func (m *Model) deleteSession(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("session delete: no session selected")
	}
	if m.session != nil && id == m.session.ID {
		return fmt.Errorf("session delete: cannot delete the active session")
	}
	if filepath.Base(id) != id {
		return fmt.Errorf("session delete: invalid session id %q", id)
	}
	cfg, err := m.sessionActionConfig()
	if err != nil {
		return err
	}
	sc := (*stadogit.Sidecar)(nil)
	if m.session != nil {
		sc = m.session.Sidecar
	}
	if sc == nil {
		return fmt.Errorf("session delete: no live sidecar")
	}
	if err := sc.DeleteSessionRefs(id); err != nil {
		return fmt.Errorf("session delete refs: %w", err)
	}
	if err := os.RemoveAll(filepath.Join(cfg.WorktreeDir(), id)); err != nil {
		return fmt.Errorf("session delete worktree: %w", err)
	}
	return nil
}

func (m *Model) forkAndSwitchSession(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("session fork: no session selected")
	}
	if err := m.ensureSessionSwitchAllowed(); err != nil {
		return err
	}
	cfg, err := m.sessionActionConfig()
	if err != nil {
		return err
	}
	parent, err := runtime.OpenSessionByID(cfg, m.sessionActionCWD(), id)
	if err != nil {
		return fmt.Errorf("session fork: %w", err)
	}
	child, err := runtime.ForkSession(cfg, parent)
	if err != nil {
		return err
	}
	exec, err := runtime.BuildExecutor(child, cfg, "stado-tui")
	if err != nil {
		return fmt.Errorf("session fork tools: %w", err)
	}
	m.activateSession(child, exec)
	return nil
}

func (m *Model) ensureSessionSwitchAllowed() error {
	if m.queuedPrompt != "" {
		return fmt.Errorf("session switch: wait for queued prompt to run or clear it")
	}
	if m.state == stateStreaming || m.compacting {
		return fmt.Errorf("session switch: wait for the current turn to finish")
	}
	if m.state == stateApproval {
		return fmt.Errorf("session switch: resolve the approval first")
	}
	if m.state == stateCompactionPending || m.state == stateCompactionEditing {
		return fmt.Errorf("session switch: resolve compaction first")
	}
	if m.backgroundTickRunning || m.backgroundTickQueued {
		return fmt.Errorf("session switch: wait for background plugins to finish")
	}
	m.toolMu.Lock()
	defer m.toolMu.Unlock()
	if m.toolCancel != nil {
		return fmt.Errorf("session switch: wait for the running tool to finish")
	}
	return nil
}

func (m *Model) sessionActionConfig() (*config.Config, error) {
	if m.cfg != nil {
		return m.cfg, nil
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("session config: %w", err)
	}
	m.cfg = cfg
	return cfg, nil
}

func (m *Model) sessionActionCWD() string {
	if m.session != nil && m.session.WorktreePath != "" {
		return m.session.WorktreePath
	}
	return m.cwd
}

func (m *Model) activateSession(sess *stadogit.Session, exec *tools.Executor) {
	m.saveActiveSessionUIState()
	m.executor = exec
	m.resetForSession(sess)
}

func (m *Model) saveActiveSessionUIState() {
	if m.session == nil || strings.TrimSpace(m.session.ID) == "" {
		return
	}
	if m.sessionUIStates == nil {
		m.sessionUIStates = make(map[string]sessionUIState)
	}
	draft := ""
	if m.input != nil {
		draft = m.input.Value()
	}
	m.sessionUIStates[m.session.ID] = sessionUIState{
		Draft:               draft,
		ViewportYOffset:     m.vp.YOffset,
		ActivityYOffset:     m.activityVP.YOffset,
		ProviderName:        m.providerName,
		Model:               m.model,
		TokenCounterChecked: m.tokenCounterChecked,
		TokenCounterPresent: m.tokenCounterPresent,
		HasProviderState:    true,
	}
}

func (m *Model) restoreActiveSessionUIState() {
	if m.session == nil || m.sessionUIStates == nil {
		return
	}
	state, ok := m.sessionUIStates[m.session.ID]
	if !ok {
		return
	}
	if m.input != nil {
		m.input.SetValue(state.Draft)
	}
	m.vp.SetYOffset(state.ViewportYOffset)
	m.activityVP.SetYOffset(state.ActivityYOffset)
	m.restoreSessionProviderState(state)
}

func (m *Model) restoreSessionProviderState(state sessionUIState) {
	if !state.HasProviderState {
		return
	}
	providerChanged := state.ProviderName != m.providerName
	m.providerName = state.ProviderName
	m.model = state.Model
	if providerChanged {
		m.provider = nil
		m.tokenCounterChecked = false
		m.tokenCounterPresent = false
		return
	}
	m.tokenCounterChecked = state.TokenCounterChecked
	m.tokenCounterPresent = state.TokenCounterPresent
}

func (m *Model) resetForSession(sess *stadogit.Session) {
	m.session = sess
	m.cwd = sess.WorktreePath
	m.blocks = nil
	m.msgs = nil
	m.todos = nil
	m.usage = agent.Usage{}
	m.state = stateIdle
	m.errorMsg = ""
	m.queuedPrompt = ""
	m.recoveryPrompt = ""
	m.recoveryPluginName = ""
	m.recoveryPluginActive = false
	m.pendingCompactionSummary = ""
	m.savedDraftBeforeEdit = ""
	m.compactionBlockIdx = 0
	m.compacting = false
	m.turnText = ""
	m.turnThinking = ""
	m.turnThinkSig = ""
	m.turnToolCalls = nil
	m.turnAllowed = nil
	m.pendingCalls = nil
	m.pendingResults = nil
	m.input.Reset()
	m.slash.Close()
	m.slashInline = false
	m.agentPick.Close()
	m.modelPicker.Close()
	m.sessionPick.Close()
	m.filePicker.Close()
	m.vp.SetContent("")
	m.activityVP.SetContent("")
	m.LoadPersistedConversation()
	m.renderBlocks()
	m.restoreActiveSessionUIState()
}
