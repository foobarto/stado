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
	"github.com/foobarto/stado/internal/tui/sessionpicker"
	"github.com/foobarto/stado/pkg/agent"
)

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
		if id != "" {
			ids[id] = struct{}{}
		}
		return nil
	})
	if entries, err := os.ReadDir(worktreeRoot); err == nil {
		for _, e := range entries {
			if e.IsDir() {
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

func (m *Model) ensureSessionSwitchAllowed() error {
	if strings.TrimSpace(m.input.Value()) != "" {
		return fmt.Errorf("session switch: submit or clear the draft first")
	}
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
	m.executor = exec
	m.resetForSession(sess)
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
	m.agentPick.Close()
	m.modelPicker.Close()
	m.sessionPick.Close()
	m.filePicker.Close()
	m.vp.SetContent("")
	m.activityVP.SetContent("")
	m.LoadPersistedConversation()
	m.renderBlocks()
}
