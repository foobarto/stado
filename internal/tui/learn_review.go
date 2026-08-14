package tui

import (
	"encoding/json"
	"fmt"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/foobarto/stado/internal/artifacts"
	"github.com/foobarto/stado/internal/broker/wal"
	learnpkg "github.com/foobarto/stado/internal/learn"
	"github.com/foobarto/stado/internal/sessioncontext"
)

type learnResultMsg struct {
	job learnpkg.Job
	err error
}

type learnManageResultMsg struct {
	body string
	err  error
}

func (m *Model) startLearnReview(focus string) tea.Cmd {
	if m.session == nil || m.session.ID == "" {
		m.appendBlock(block{kind: "system", body: "/learn requires a live session"})
		return nil
	}
	if m.state != stateIdle {
		captured := strings.TrimSpace(focus)
		m.pendingLearnFocus = &captured
		m.appendBlock(block{kind: "system", body: "/learn queued for the next completed turn boundary"})
		return nil
	}
	if !m.ensureProvider() {
		return nil
	}
	sessionID := m.session.ID
	provider := m.provider
	model := m.model
	stateDir := m.cfg.StateDir()
	principal := "local"
	if current, err := user.Current(); err == nil && current.Uid != "" {
		principal = "os-user:" + current.Uid
	}
	m.appendBlock(block{kind: "system", body: "learning review queued for the completed trajectory; generated lessons remain candidates until operator approval"})
	return func() tea.Msg {
		store, err := wal.OpenShared(filepath.Join(stateDir, "broker", "events"))
		if err != nil {
			return learnResultMsg{err: fmt.Errorf("open broker events: %w", err)}
		}
		defer func() { _ = store.Close() }()
		artifactSvc := artifacts.NewService(store, nil)
		signalSvc := sessioncontext.New(store)
		reviewer := learnpkg.LLMReviewer{Provider: provider, Model: model}
		svc := learnpkg.New(store, signalSvc, artifactSvc, reviewer)
		job, err := svc.Run(m.rootCtx, learnpkg.RunRequest{SessionID: sessionID, Focus: strings.TrimSpace(focus), Principal: principal, Actor: "operator-tui", IdempotencyKey: "learn-tui:" + sessionID + ":" + time.Now().UTC().Format(time.RFC3339Nano), Scope: artifacts.ScopeSession, Binding: artifacts.ScopeBinding{AnchorSessionID: sessionID}, MaxCandidates: 5})
		return learnResultMsg{job: job, err: err}
	}
}

func (m *Model) runPendingLearn() tea.Cmd {
	if m.pendingLearnFocus == nil {
		return nil
	}
	focus := *m.pendingLearnFocus
	m.pendingLearnFocus = nil
	return m.startLearnReview(focus)
}

func onLearnResult(m *Model, msg learnResultMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.appendBlock(block{kind: "system", body: "/learn failed: " + msg.err.Error()})
	} else if len(msg.job.ArtifactIDs) == 0 {
		m.appendBlock(block{kind: "system", body: "/learn completed: no evidence-supported lesson candidates"})
	} else {
		m.appendBlock(block{kind: "system", body: fmt.Sprintf("/learn completed: %d candidate(s) created\n%s", len(msg.job.ArtifactIDs), strings.Join(msg.job.ArtifactIDs, "\n"))})
	}
	m.renderBlocks()
	return m, nil
}

func (m *Model) manageLearn(parts []string) tea.Cmd {
	if m.session == nil || m.session.ID == "" {
		m.appendBlock(block{kind: "system", body: "/learn requires a live session"})
		return nil
	}
	verb := "candidates"
	if len(parts) > 1 {
		verb = parts[1]
	}
	id := ""
	if len(parts) > 2 {
		id = parts[2]
	}
	stateDir := m.cfg.StateDir()
	sessionID := m.session.ID
	principal := "local"
	if current, err := user.Current(); err == nil && current.Uid != "" {
		principal = "os-user:" + current.Uid
	}
	return func() tea.Msg {
		store, err := wal.OpenShared(filepath.Join(stateDir, "broker", "events"))
		if err != nil {
			return learnManageResultMsg{err: err}
		}
		defer func() { _ = store.Close() }()
		svc := artifacts.NewService(store, nil)
		qctx := artifacts.QueryContext{Principal: principal, SessionID: sessionID}
		switch verb {
		case "candidates", "list":
			items, err := svc.Query(artifacts.Query{Context: qctx, Kinds: []artifacts.Kind{artifacts.KindLesson}, MaxItems: 50})
			if err != nil {
				return learnManageResultMsg{err: err}
			}
			var rows []string
			for _, a := range items {
				if a.Authority == artifacts.AuthorityCandidate || a.Authority == artifacts.AuthorityLegacyActive {
					rows = append(rows, fmt.Sprintf("%s  %-13s  %s", a.ID, a.Authority, a.Title()))
				}
			}
			if len(rows) == 0 {
				return learnManageResultMsg{body: "(no lesson candidates)"}
			}
			return learnManageResultMsg{body: strings.Join(rows, "\n")}
		case "show":
			if id == "" {
				return learnManageResultMsg{err: fmt.Errorf("usage: /learn show <artifact-id>")}
			}
			a, ok, err := svc.Show(id)
			if err != nil || !ok {
				return learnManageResultMsg{err: fmt.Errorf("lesson %q not found", id)}
			}
			b, _ := json.MarshalIndent(a, "", "  ")
			return learnManageResultMsg{body: string(b)}
		default:
			return learnManageResultMsg{err: fmt.Errorf("usage: /learn [focus] | /learn candidates | /learn show <id>; activation requires a separately trusted presenter or broker policy")}
		}
	}
}
func onLearnManageResult(m *Model, msg learnManageResultMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.appendBlock(block{kind: "system", body: "/learn: " + msg.err.Error()})
	} else {
		m.appendBlock(block{kind: "system", body: msg.body})
	}
	m.renderBlocks()
	return m, nil
}
