package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/foobarto/stado/internal/broker/wal"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/supervise"
	"github.com/foobarto/stado/internal/tasks"
	"github.com/foobarto/stado/internal/textutil"
	"github.com/foobarto/stado/internal/trajectory"
	"github.com/foobarto/stado/internal/tui/supervisepicker"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/go-git/go-git/v5/plumbing"
)

type superviseRuntime struct {
	service            *supervise.Service
	store              *wal.Store
	state              supervise.State
	detector           *supervise.Detector
	draft              supervisepicker.Draft
	generation         uint64
	cancel             context.CancelFunc
	sequence           uint64
	gateActive         bool
	verifierGeneration uint64
	pivotGeneration    uint64
	reviewGeneration   uint64
	reviewing          bool
	pendingTrigger     *supervise.Trigger
	correctionPending  bool
	riskCall           *agent.ToolUseBlock
	followupQueue      []supervise.Followup
	followupCurrent    supervise.Followup
	followupReviewing  bool
	followupGeneration uint64
	followupResume     bool
	followupRelated    bool
}

type superviseProviderFactory struct {
	cfgProvider func(string) (agent.Provider, error)
}

func (f superviseProviderFactory) Build(_ context.Context, profile supervise.RoleProfile) (agent.Provider, error) {
	return f.cfgProvider(profile.Provider)
}

type superviseBaselineResultMsg struct {
	generation uint64
	result     supervise.ReviewResult
	err        error
}

type superviseBaselineDecisionMsg struct {
	generation uint64
	approved   bool
}

type superviseVerifierResultMsg struct {
	generation uint64
	result     supervise.ReviewResult
	err        error
}

type supervisePivotReviewMsg struct {
	generation uint64
	result     supervise.ReviewResult
	err        error
}

type supervisePivotDecisionMsg struct {
	generation uint64
	approved   bool
	verdict    supervise.Verdict
}

type superviseEventReviewMsg struct {
	generation uint64
	trigger    *supervise.Trigger
	result     supervise.ReviewResult
	err        error
}

type superviseRiskDecisionMsg struct {
	generation uint64
	approved   bool
	boundary   string
}

type superviseFollowupReviewMsg struct {
	generation uint64
	followup   supervise.Followup
	result     supervise.ReviewResult
	err        error
}

func (m *Model) openSuperviseWizard(objective string) {
	if m.supervision != nil && !superviseTerminal(m.supervision.state.Status) {
		m.appendBlock(block{kind: "system", body: "supervise: this worker session already has active supervised work; use `/supervise status`"})
		return
	}
	if m.state != stateIdle {
		m.appendBlock(block{kind: "system", body: "supervise: wait for the current turn or tool to finish before starting supervised work"})
		return
	}
	if m.supervisePick == nil {
		m.supervisePick = supervisepicker.New()
	}
	m.supervisePick.Open(objective, m.providerName, m.model)
	m.layout()
}

func (m *Model) loadSupervision() error {
	if m.cfg == nil || m.session == nil {
		return nil
	}
	store, err := wal.OpenShared(filepath.Join(m.cfg.StateDir(), "broker", "events"))
	if err != nil {
		return fmt.Errorf("restore supervision state: %w", err)
	}
	svc := supervise.New(store)
	st, err := svc.StateBySession(m.session.ID)
	if errors.Is(err, supervise.ErrNotFound) {
		_ = store.Close()
		return nil
	}
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("restore supervision state: %w", err)
	}
	detector := supervise.RestoreDetector(st.Detector)
	m.supervision = &superviseRuntime{
		service: svc, store: store, state: st, detector: detector,
		sequence: st.SessionSequence, generation: 1,
		followupQueue: append([]supervise.Followup(nil), st.PendingFollowups...),
	}
	if st.Status != supervise.StatusSetup && st.Status != supervise.StatusAwaitingApproval && !superviseTerminal(st.Status) {
		m.registerSuperviseControlTools()
	}
	m.appendBlock(block{kind: "system", body: fmt.Sprintf("supervise: restored durable run %s · status %s · plan v%d · sequence %d", st.ID, st.Status, st.PlanVersion, st.SessionSequence)})
	return nil
}

func (m *Model) detachSupervision() {
	if m.executor != nil && m.executor.Registry != nil {
		for _, name := range []string{superviseProgressTool, supervisePivotTool, superviseCompletionTool} {
			m.executor.Registry.Unregister(name)
		}
	}
	if m.supervision == nil {
		return
	}
	if m.supervision.cancel != nil {
		m.supervision.cancel()
		m.supervision.cancel = nil
	}
	m.supervision.generation++
	m.supervision.verifierGeneration++
	m.supervision.pivotGeneration++
	m.supervision.reviewGeneration++
	m.supervision.followupGeneration++
	if m.supervision.store != nil {
		_ = m.supervision.store.Close()
		m.supervision.store = nil
	}
	m.supervision = nil
}

func (m *Model) handleSuperviseSlash(parts []string) tea.Cmd {
	verb := ""
	if len(parts) > 1 {
		verb = strings.ToLower(parts[1])
	}
	switch verb {
	case "status":
		if m.supervision == nil {
			m.appendBlock(block{kind: "system", body: "supervise: no run is attached to this worker session"})
			return nil
		}
		m.syncSuperviseState()
		m.appendBlock(block{kind: "system", body: renderSuperviseStatus(m.supervision.state)})
		return nil
	case "cancel":
		rt := m.supervision
		if rt == nil || superviseTerminal(rt.state.Status) {
			m.appendBlock(block{kind: "system", body: "supervise: no active run to cancel"})
			return nil
		}
		m.cancelSupervisedWorker()
		st, err := rt.service.Cancel(m.rootCtx, rt.state.ID, rt.state.Version, supervise.RoleOperator, trajectory.LocalPrincipal(), "operator", "supervise-cancel:"+rt.state.ID+":"+strconvVersionUI(rt.state.Version))
		if err != nil {
			m.appendBlock(block{kind: "system", body: "supervise: cancel failed: " + err.Error()})
			return nil
		}
		rt.state = st
		m.state = stateIdle
		m.appendBlock(block{kind: "system", body: "supervise: run cancelled by operator"})
		return nil
	case "resume":
		return m.resumeSupervision()
	case "", "start":
		objective := ""
		if verb == "start" && len(parts) > 2 {
			objective = strings.TrimSpace(strings.Join(parts[2:], " "))
		} else if verb == "" && len(parts) > 1 {
			objective = strings.TrimSpace(strings.Join(parts[1:], " "))
		}
		m.openSuperviseWizard(objective)
		return nil
	default:
		// Preserve the ergonomic `/supervise <objective>` form. Only a
		// recognized lifecycle verb is consumed as a subcommand.
		m.openSuperviseWizard(strings.TrimSpace(strings.Join(parts[1:], " ")))
		return nil
	}
}

func (m *Model) resumeSupervision() tea.Cmd {
	rt := m.supervision
	if rt == nil {
		m.appendBlock(block{kind: "system", body: "supervise: no durable run to resume"})
		return nil
	}
	if rt.cancel != nil {
		m.appendBlock(block{kind: "system", body: "supervise: a watchdog or verifier review is already active"})
		return nil
	}
	m.syncSuperviseState()
	switch rt.state.Status {
	case supervise.StatusSetup:
		if rt.state.ObjectiveSeed == "" {
			m.appendBlock(block{kind: "system", body: "supervise: the interrupted setup predates durable objective seeds; cancel it and start again"})
			return nil
		}
		return m.startSuperviseBaselineReview(rt.state.ObjectiveSeed)
	case supervise.StatusAwaitingApproval:
		return m.requestSuperviseBaselineApproval()
	case supervise.StatusPivotPending:
		return m.startSupervisePivotReview()
	case supervise.StatusVerifying:
		return m.startSuperviseCompletionFlow()
	case supervise.StatusPaused:
		anchor := rt.state.Anchor()
		anchor.TreeDigest = m.superviseTreeDigest()
		st, err := rt.service.Resume(m.rootCtx, rt.state.ID, rt.state.Version, supervise.RoleOperator, anchor, trajectory.LocalPrincipal(), "operator", "supervise-resume:"+rt.state.ID+":"+strconvVersionUI(rt.state.Version))
		if err != nil {
			m.appendBlock(block{kind: "system", body: "supervise: resume failed: " + err.Error()})
			return nil
		}
		rt.state = st
		rt.detector = supervise.RestoreDetector(st.Detector)
		m.registerSuperviseControlTools()
		m.appendBlock(block{kind: "system", body: "supervise: run resumed by operator"})
		m.state = stateIdle
		switch st.Status {
		case supervise.StatusPivotPending:
			return m.startSupervisePivotReview()
		case supervise.StatusVerifying:
			if len(rt.followupQueue) > 0 {
				invalidated, invalidateErr := rt.service.InvalidateCompletion(m.rootCtx, st.ID, st.Version, supervise.RoleHost, "completion invalidated on resume because durable operator follow-ups must be routed first", trajectory.LocalPrincipal(), "tui-host", "supervise-resume-inbox:"+st.ID+":"+strconvVersionUI(st.Version))
				if invalidateErr != nil {
					m.appendBlock(block{kind: "system", body: "supervise: resume could not route the durable inbox: " + invalidateErr.Error()})
					return nil
				}
				rt.state = invalidated
				rt.followupResume = true
				return m.startNextSuperviseFollowupReview()
			}
			return m.startSuperviseCompletionFlow()
		}
		feedback := "[Host-authenticated supervision decision]\nThe operator resumed supervised work. Re-read the enforced baseline and continue from the active step; do not repeat failed tactics without new evidence."
		workerMsg := agent.Text(agent.RoleUser, feedback)
		m.msgs = append(m.msgs, workerMsg)
		m.persistMessage(workerMsg)
		if len(rt.followupQueue) > 0 {
			rt.followupResume = true
			return m.startNextSuperviseFollowupReview()
		}
		return m.startStream()
	case supervise.StatusRunning:
		if len(rt.followupQueue) > 0 {
			m.appendBlock(block{kind: "system", body: "supervise: resuming durable follow-up inbox classification"})
			return m.startNextSuperviseFollowupReview()
		}
		m.appendBlock(block{kind: "system", body: "supervise: run is already active; send the worker its next instruction or use `/supervise status`"})
	default:
		m.appendBlock(block{kind: "system", body: "supervise: run is " + string(rt.state.Status) + " and cannot be resumed"})
	}
	return nil
}

func (m *Model) beginSupervise(draft supervisepicker.Draft) tea.Cmd {
	if m.cfg == nil || m.session == nil {
		m.appendBlock(block{kind: "system", body: "supervise: a persisted session and configuration are required"})
		return nil
	}
	m.detachSupervision()
	store, err := wal.OpenShared(filepath.Join(m.cfg.StateDir(), "broker", "events"))
	if err != nil {
		m.appendBlock(block{kind: "system", body: "supervise: open durable state: " + err.Error()})
		return nil
	}
	svc := supervise.New(store)
	seed := renderWizardSeed(draft)
	st, err := svc.CreateWithSeed(m.rootCtx, m.session.ID, seed, draft.Config, trajectory.LocalPrincipal(), "tui-host", "supervise-create:"+m.session.ID+":"+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err != nil {
		_ = store.Close()
		m.appendBlock(block{kind: "system", body: err.Error()})
		return nil
	}
	tree := m.superviseTreeDigest()
	st, err = svc.UpdateRuntimeAnchor(m.rootCtx, st.ID, st.Version, supervise.RoleHost, supervise.Anchor{RootSessionID: m.session.ID, TreeDigest: tree}, trajectory.LocalPrincipal(), "tui-host", "supervise-anchor:"+st.ID)
	if err != nil {
		_ = store.Close()
		m.appendBlock(block{kind: "system", body: err.Error()})
		return nil
	}
	rt := &superviseRuntime{service: svc, store: store, state: st, detector: supervise.NewDetector(st.Config.Mode), draft: draft, generation: 1}
	m.supervision = rt
	return m.startSuperviseBaselineReview(seed)
}

func (m *Model) startSuperviseBaselineReview(seed string) tea.Cmd {
	rt := m.supervision
	if rt == nil || rt.state.Status != supervise.StatusSetup {
		return nil
	}
	st := rt.state
	profile := st.Config.Watchdog
	if profile.Provider == "" {
		profile.Provider = m.providerName
	}
	if profile.Model == "" {
		profile.Model = m.model
	}
	source := m.superviseEvidenceSnapshot(st)
	reviewer := supervise.Reviewer{Factory: superviseProviderFactory{cfgProvider: func(name string) (agent.Provider, error) {
		if strings.TrimSpace(name) == "" {
			name = m.providerName
		}
		return buildProviderByName(m.cfg, name)
	}}, Source: source}
	ctx, cancel := context.WithCancel(m.rootCtx)
	rt.cancel = cancel
	generation := rt.generation
	m.state = stateStreaming
	m.appendBlock(block{kind: "system", body: fmt.Sprintf("supervise: gathering requirements with a fresh watchdog (%s mode); execution will not begin until you approve its baseline", st.Config.Mode)})
	return func() tea.Msg {
		result, runErr := runSuperviseWatchdogReview(ctx, reviewer, st.Config, profile, st.Config.WatchdogBudget, supervise.ReviewRequest{Role: supervise.RoleWatchdog, Kind: supervise.ReviewBaseline, State: st, ObjectiveSeed: seed})
		return superviseBaselineResultMsg{generation: generation, result: result, err: runErr}
	}
}

func onSuperviseBaselineResult(m *Model, msg superviseBaselineResultMsg) (tea.Model, tea.Cmd) {
	rt := m.supervision
	if rt == nil || msg.generation != rt.generation {
		return m, nil
	}
	rt.cancel = nil
	if msg.err != nil {
		m.state = stateIdle
		m.appendBlock(block{kind: "system", body: "supervise: requirements review failed: " + msg.err.Error()})
		m.renderBlocks()
		return m, nil
	}
	if msg.result.Baseline == nil {
		m.state = stateIdle
		m.appendBlock(block{kind: "system", body: "supervise: watchdog returned no baseline"})
		return m, nil
	}
	st, err := rt.service.ProposeBaseline(m.rootCtx, rt.state.ID, rt.state.Version, *msg.result.Baseline, supervise.RoleWatchdog, rt.state.Anchor(), trajectory.LocalPrincipal(), "watchdog", "supervise-baseline:"+rt.state.ID)
	if err != nil {
		m.state = stateIdle
		m.appendBlock(block{kind: "system", body: err.Error()})
		return m, nil
	}
	rt.state = st
	return m, m.requestSuperviseBaselineApproval()
}

func (m *Model) requestSuperviseBaselineApproval() tea.Cmd {
	rt := m.supervision
	if rt == nil || rt.state.Status != supervise.StatusAwaitingApproval {
		return nil
	}
	proposal := renderSuperviseBaseline(rt.state.Baseline)
	m.appendBlock(block{kind: "system", body: textutil.SanitizeForTerminal("Proposed supervised-work baseline (not active until approved):\n\n" + proposal)})
	response := make(chan bool, 1)
	m.approval = &approvalRequest{title: "Approve supervised-work baseline", body: "Review the full proposal in the conversation. Allow activates this objective, criteria, plan, definition of done, and verification contract.", response: response}
	m.approvalFocused, m.approvalAllowSelected, m.state = true, true, stateApproval
	m.renderBlocks()
	generation := rt.generation
	return func() tea.Msg { return superviseBaselineDecisionMsg{generation: generation, approved: <-response} }
}

func onSuperviseBaselineDecision(m *Model, msg superviseBaselineDecisionMsg) (tea.Model, tea.Cmd) {
	rt := m.supervision
	if rt == nil || msg.generation != rt.generation {
		return m, nil
	}
	if !msg.approved {
		st, err := rt.service.Cancel(m.rootCtx, rt.state.ID, rt.state.Version, supervise.RoleOperator, trajectory.LocalPrincipal(), "operator", "supervise-baseline-rejected:"+rt.state.ID)
		if err == nil {
			rt.state = st
		}
		m.appendBlock(block{kind: "system", body: "supervise: baseline rejected; no worker execution started"})
		m.renderBlocks()
		return m, nil
	}
	st, err := rt.service.ApproveBaseline(m.rootCtx, rt.state.ID, rt.state.Version, supervise.RoleOperator, trajectory.LocalPrincipal(), "operator", "supervise-baseline-approved:"+rt.state.ID)
	if err != nil {
		m.appendBlock(block{kind: "system", body: err.Error()})
		return m, nil
	}
	rt.state = st
	m.registerSuperviseControlTools()
	m.appendBlock(block{kind: "system", body: fmt.Sprintf("supervise: baseline approved and enforced · mode %s · plan v%d · active step %s", st.Config.Mode, st.PlanVersion, st.ActiveStep)})
	if err := m.setBrokerTaint(runtime.ContextClean); err != nil {
		m.appendBlock(block{kind: "system", body: "supervise: broker taint reset failed: " + err.Error()})
		return m, nil
	}
	m.appendUser(st.Baseline.Objective)
	m.renderBlocks()
	return m, m.startStream()
}

func (m *Model) startSuperviseCompletionFlow() tea.Cmd {
	rt := m.supervision
	if rt == nil || rt.state.Status != supervise.StatusVerifying {
		return nil
	}
	m.invalidateSuperviseEventReview()
	if m.verifyEnabled && m.verifyConfig.Enabled() {
		rt.gateActive = true
		return m.startVerification()
	}
	st, err := rt.service.RecordVerificationGate(m.rootCtx, rt.state.ID, rt.state.Version, supervise.RoleHost, true, "No configured Verify Work commands; final verifier must assess the baseline-specified evidence directly.", nil, trajectory.LocalPrincipal(), "tui-host", "supervise-gate-none:"+rt.state.ID+":"+strconvVersionUI(rt.state.Version))
	if err != nil {
		m.appendBlock(block{kind: "system", body: err.Error()})
		return nil
	}
	rt.state = st
	return m.startSuperviseVerifier()
}

func onSuperviseGateResult(m *Model, msg verifyResultMsg) (tea.Model, tea.Cmd) {
	rt := m.supervision
	if rt == nil || !rt.gateActive || !m.verifying || msg.generation != m.verifyGeneration {
		return m, nil
	}
	rt.gateActive = false
	m.toolMu.Lock()
	m.toolCancel = nil
	m.toolMu.Unlock()
	m.verifying = false
	out := msg.outcome
	passed := out.Status == runtime.VerifyPassed
	summary := strings.TrimSpace(out.Output)
	if summary == "" {
		summary = string(out.Status)
	}
	summary = textutil.TruncateRunes(summary, 2048)
	st, err := rt.service.RecordVerificationGate(m.rootCtx, rt.state.ID, rt.state.Version, supervise.RoleHost, passed, summary, []string{"verify-work:round:" + strconv.Itoa(out.Round)}, trajectory.LocalPrincipal(), "verify-work", "supervise-gate:"+rt.state.ID+":"+strconvVersionUI(rt.state.Version))
	if err != nil {
		m.appendBlock(block{kind: "system", body: err.Error()})
		m.state = stateError
		return m, nil
	}
	rt.state = st
	if passed {
		m.appendBlock(block{kind: "system", body: fmt.Sprintf("supervise: Verify Work gates passed (round %d); starting fresh independent verifier", out.Round)})
		// A passed gate must not advance the anchor: the completion request
		// and verifier verdict are deliberately bound to the exact worker tree
		// that requested verification.
		return m, m.startSuperviseVerifier()
	}
	review := m.observeSupervise(superviseVerificationEvent(false))
	feedback := "Supervised completion was rejected by deterministic verification.\n" + textutil.TruncateRunes(textutil.SanitizeForTerminal(summary), 4000)
	m.appendBlock(block{kind: "system", body: feedback})
	if errors.Is(out.Err, context.Canceled) {
		m.state = stateIdle
		return m, nil
	}
	if err := m.setBrokerTaint(runtime.ContextTainted); err != nil {
		m.appendBlock(block{kind: "system", body: err.Error()})
		return m, nil
	}
	msgToWorker := agent.Text(agent.RoleUser, feedback)
	m.msgs = append(m.msgs, msgToWorker)
	m.persistMessage(msgToWorker)
	m.state = stateIdle
	return m, tea.Batch(review, m.startStream())
}

func (m *Model) startSuperviseVerifier() tea.Cmd {
	rt := m.supervision
	if rt == nil || rt.state.Status != supervise.StatusVerifying {
		return nil
	}
	m.invalidateSuperviseEventReview()
	rt.verifierGeneration++
	generation := rt.verifierGeneration
	state := rt.state
	source := m.superviseEvidenceSnapshot(state)
	cfg := m.cfg
	defaultProvider, defaultModel := m.providerName, m.model
	profile := state.Config.Verifier
	if profile.Provider == "" {
		profile.Provider = defaultProvider
	}
	if profile.Model == "" {
		profile.Model = defaultModel
	}
	reviewer := supervise.Reviewer{Factory: superviseProviderFactory{cfgProvider: func(name string) (agent.Provider, error) {
		if name == "" {
			name = defaultProvider
		}
		return buildProviderByName(cfg, name)
	}}, Source: source}
	ctx, cancel := context.WithCancel(m.rootCtx)
	rt.cancel = cancel
	m.state = stateStreaming
	m.appendBlock(block{kind: "system", body: "supervise: independent verifier is checking every acceptance criterion, definition-of-done item, gate, diff, and documentation obligation"})
	m.renderBlocks()
	return func() tea.Msg {
		var result supervise.ReviewResult
		var err error
		for attempt := 0; attempt < state.Config.EventReviewRetries; attempt++ {
			result, err = reviewer.Run(ctx, profile, state.Config.VerifierBudget, supervise.ReviewRequest{Role: supervise.RoleVerifier, Kind: supervise.ReviewCompletion, State: state})
			if err == nil {
				break
			}
			select {
			case <-ctx.Done():
				attempt = state.Config.EventReviewRetries
			case <-time.After(time.Duration(250*(1<<attempt)) * time.Millisecond):
			}
		}
		return superviseVerifierResultMsg{generation: generation, result: result, err: err}
	}
}

func onSuperviseVerifierResult(m *Model, msg superviseVerifierResultMsg) (tea.Model, tea.Cmd) {
	rt := m.supervision
	if rt == nil || msg.generation != rt.verifierGeneration {
		return m, nil
	}
	rt.cancel = nil
	if msg.err != nil {
		if !rt.state.Config.VerifierRequired && rt.state.Config.AllowAdvisoryFallback {
			reason := textutil.TruncateRunes("independent verifier unavailable; optional-verifier policy returned the completion request to work without accepting it: "+msg.err.Error(), 2048)
			st, invalidateErr := rt.service.InvalidateCompletion(m.rootCtx, rt.state.ID, rt.state.Version, supervise.RoleHost, reason, trajectory.LocalPrincipal(), "tui-host", "supervise-verifier-fallback:"+rt.state.ID+":"+strconvVersionUI(rt.state.Version))
			if invalidateErr == nil {
				rt.state = st
				feedback := "Independent verifier unavailable. Completion was not accepted; optional-verifier fallback returned the run to work."
				workerMsg := agent.Text(agent.RoleUser, feedback)
				m.msgs = append(m.msgs, workerMsg)
				m.persistMessage(workerMsg)
				m.appendBlock(block{kind: "system", body: "supervise: " + feedback})
				m.state = stateIdle
				if len(rt.followupQueue) > 0 {
					rt.followupResume = true
					return m, m.startNextSuperviseFollowupReview()
				}
				return m, m.startStream()
			}
		}
		pauseReason := textutil.TruncateRunes("independent verifier unavailable after retries: "+msg.err.Error(), 4096)
		st, pauseErr := rt.service.Pause(m.rootCtx, rt.state.ID, rt.state.Version, supervise.RoleHost, pauseReason, trajectory.LocalPrincipal(), "tui-host", "supervise-verifier-pause:"+rt.state.ID)
		if pauseErr == nil {
			rt.state = st
		}
		m.state = stateIdle
		m.appendBlock(block{kind: "system", body: "supervise paused: independent verifier failed after retries; completion was not accepted: " + msg.err.Error()})
		m.renderBlocks()
		return m, nil
	}
	if msg.result.Verdict == nil {
		st, pauseErr := rt.service.Pause(m.rootCtx, rt.state.ID, rt.state.Version, supervise.RoleHost, "independent verifier returned no verdict", trajectory.LocalPrincipal(), "tui-host", "supervise-verifier-empty:"+rt.state.ID)
		if pauseErr == nil {
			rt.state = st
		}
		m.state = stateIdle
		m.appendBlock(block{kind: "system", body: "supervise paused: verifier returned no verdict"})
		return m, nil
	}
	currentTree := m.superviseTreeDigest()
	if currentTree != rt.state.TreeDigest || len(rt.state.PendingFollowups) > 0 {
		reason := "completion invalidated before verdict: "
		if currentTree != rt.state.TreeDigest {
			reason += "the audited worker tree changed during verification"
		} else {
			reason += "operator follow-ups arrived during verification and must be routed first"
		}
		st, invalidateErr := rt.service.InvalidateCompletion(m.rootCtx, rt.state.ID, rt.state.Version, supervise.RoleHost, reason, trajectory.LocalPrincipal(), "tui-host", "supervise-verifier-invalidated:"+rt.state.ID+":"+strconvVersionUI(rt.state.Version))
		if invalidateErr != nil {
			m.state = stateIdle
			m.appendBlock(block{kind: "system", body: "supervise: could not invalidate stale completion: " + invalidateErr.Error()})
			return m, nil
		}
		rt.state = st
		feedback := "[Host-authenticated supervision decision]\n" + reason + ". Reconcile the new state and request completion again."
		workerMsg := agent.Text(agent.RoleUser, feedback)
		m.msgs = append(m.msgs, workerMsg)
		m.persistMessage(workerMsg)
		m.appendBlock(block{kind: "system", body: feedback})
		m.state = stateIdle
		var review tea.Cmd
		if currentTree != st.TreeDigest {
			patch, paths := m.superviseDiffSnapshot(st)
			review = m.observeSupervise(supervise.WorkerEvent{Kind: supervise.WorkerTreeChanged, TreeDigest: currentTree, DiffBytes: int64(len(patch)), ChangedPaths: paths})
		}
		if len(rt.followupQueue) > 0 {
			rt.followupResume = true
			return m, tea.Batch(review, m.nextSuperviseHostAction())
		}
		return m, tea.Batch(review, m.startStream())
	}
	st, err := rt.service.ResolveCompletion(m.rootCtx, rt.state.ID, rt.state.Version, supervise.RoleVerifier, *msg.result.Verdict, trajectory.LocalPrincipal(), "verifier", "supervise-verdict:"+rt.state.ID+":"+strconvVersionUI(rt.state.Version))
	if err != nil {
		m.state = stateIdle
		m.appendBlock(block{kind: "system", body: "supervise: verifier verdict rejected by host: " + err.Error()})
		return m, nil
	}
	rt.state = st
	if st.Status == supervise.StatusCompleted {
		m.appendBlock(block{kind: "system", body: "supervise: completion approved by the independent verifier\n" + textutil.SanitizeForTerminal(msg.result.Verdict.Rationale)})
		if store, storeErr := m.taskStore(); storeErr != nil {
			m.appendBlock(block{kind: "system", body: "supervise: completed, but deferred tasks could not be reopened automatically: " + storeErr.Error()})
		} else if deferred, listErr := superviseDeferredTasksForRun(store, st.ID); listErr != nil {
			m.appendBlock(block{kind: "system", body: "supervise: completed, but deferred tasks could not be reopened automatically: " + listErr.Error()})
		} else if len(deferred) > 0 {
			prompt := renderSuperviseDeferredContinuation(st.ID, deferred)
			m.queuedPrompt = prompt
			m.appendBlock(block{kind: "user", body: prompt, queued: true, source: "supervise-deferred"})
			m.appendBlock(block{kind: "system", body: fmt.Sprintf("supervise: %d deferred task(s) are ready; continuing with the oldest open item", len(deferred))})
		}
		m.state = stateIdle
		return m, m.finishTurnWithoutTools()
	}
	feedback := "Independent verifier rejected completion: " + textutil.SanitizeForTerminal(msg.result.Verdict.Rationale)
	if msg.result.Verdict.Correction != "" {
		feedback += "\nCorrection: " + textutil.SanitizeForTerminal(msg.result.Verdict.Correction)
	}
	m.appendBlock(block{kind: "system", body: feedback})
	if err := m.setBrokerTaint(runtime.ContextTainted); err != nil {
		m.state = stateIdle
		return m, nil
	}
	workerMsg := agent.Text(agent.RoleUser, feedback)
	m.msgs = append(m.msgs, workerMsg)
	m.persistMessage(workerMsg)
	m.state = stateIdle
	review := m.observeSupervise(superviseVerificationEvent(false))
	return m, tea.Batch(review, m.startStream())
}

func superviseVerificationEvent(passed bool) supervise.WorkerEvent {
	return supervise.WorkerEvent{Kind: supervise.WorkerVerification, VerificationPassed: &passed}
}

func (m *Model) startSupervisePivotReview() tea.Cmd {
	rt := m.supervision
	if rt == nil || rt.state.Status != supervise.StatusPivotPending || rt.state.PendingPivot == nil {
		return nil
	}
	m.invalidateSuperviseEventReview()
	rt.pivotGeneration++
	generation := rt.pivotGeneration
	state := rt.state
	source := m.superviseEvidenceSnapshot(state)
	profile := state.Config.Watchdog
	if profile.Provider == "" {
		profile.Provider = m.providerName
	}
	if profile.Model == "" {
		profile.Model = m.model
	}
	cfg, defaultProvider := m.cfg, m.providerName
	reviewer := supervise.Reviewer{Factory: superviseProviderFactory{cfgProvider: func(name string) (agent.Provider, error) {
		if name == "" {
			name = defaultProvider
		}
		return buildProviderByName(cfg, name)
	}}, Source: source}
	ctx, cancel := context.WithCancel(m.rootCtx)
	rt.cancel = cancel
	m.state = stateStreaming
	m.appendBlock(block{kind: "system", body: "supervise: a fresh watchdog is reviewing the requested plan/contract pivot against the approved objective and current evidence"})
	m.renderBlocks()
	return func() tea.Msg {
		result, err := runSuperviseWatchdogReview(ctx, reviewer, state.Config, profile, state.Config.WatchdogBudget, supervise.ReviewRequest{Role: supervise.RoleWatchdog, Kind: supervise.ReviewPivot, State: state})
		return supervisePivotReviewMsg{generation: generation, result: result, err: err}
	}
}

func onSupervisePivotReview(m *Model, msg supervisePivotReviewMsg) (tea.Model, tea.Cmd) {
	rt := m.supervision
	if rt == nil || msg.generation != rt.pivotGeneration || rt.state.Status != supervise.StatusPivotPending || rt.state.PendingPivot == nil {
		return m, nil
	}
	rt.cancel = nil
	verdict := supervise.Verdict{Kind: supervise.ReviewPivot, Decision: supervise.VerdictReject, Anchor: rt.state.Anchor(), Rationale: "watchdog review unavailable; operator decision required"}
	if msg.err == nil && msg.result.Verdict != nil && (msg.result.Verdict.Decision == supervise.VerdictApprove || msg.result.Verdict.Decision == supervise.VerdictReject) {
		verdict = *msg.result.Verdict
	}
	if msg.err == nil && rt.state.Config.PivotApproval == supervise.PivotByWatchdog && rt.state.PendingPivot.Kind == supervise.PivotPlan {
		return m, m.resolveSupervisePivot(supervise.RoleWatchdog, verdict)
	}
	proposal := renderSupervisePivot(*rt.state.PendingPivot)
	review := "Watchdog recommendation: " + string(verdict.Decision) + " — " + verdict.Rationale
	if msg.err != nil {
		review = "Watchdog unavailable after retries: " + msg.err.Error() + ". The host will not infer approval."
	}
	m.appendBlock(block{kind: "system", body: textutil.SanitizeForTerminal("Supervised-work pivot awaiting operator decision:\n\n" + proposal + "\n\n" + review)})
	response := make(chan bool, 1)
	m.approval = &approvalRequest{title: "Approve supervised-work pivot", body: "Review the full proposal and watchdog recommendation in the conversation. Allow replaces the approved plan/contract; deny keeps the current baseline.", response: response}
	m.approvalFocused, m.approvalAllowSelected, m.state = true, false, stateApproval
	m.renderBlocks()
	return m, func() tea.Msg {
		return supervisePivotDecisionMsg{generation: msg.generation, approved: <-response, verdict: verdict}
	}
}

func onSupervisePivotDecision(m *Model, msg supervisePivotDecisionMsg) (tea.Model, tea.Cmd) {
	rt := m.supervision
	if rt == nil || msg.generation != rt.pivotGeneration || rt.state.Status != supervise.StatusPivotPending {
		return m, nil
	}
	msg.verdict.Kind = supervise.ReviewPivot
	msg.verdict.Anchor = rt.state.Anchor()
	if msg.approved {
		msg.verdict.Decision = supervise.VerdictApprove
		msg.verdict.Rationale = "operator approved the requested pivot after reviewing the proposal and watchdog recommendation"
	} else {
		msg.verdict.Decision = supervise.VerdictReject
		msg.verdict.Rationale = "operator rejected the requested pivot"
	}
	return m, m.resolveSupervisePivot(supervise.RoleOperator, msg.verdict)
}

func (m *Model) resolveSupervisePivot(role supervise.ActorRole, verdict supervise.Verdict) tea.Cmd {
	rt := m.supervision
	if rt == nil {
		return nil
	}
	currentTree := m.superviseTreeDigest()
	if verdict.Decision == supervise.VerdictApprove && currentTree != rt.state.TreeDigest {
		verdict.Decision = supervise.VerdictReject
		verdict.Rationale = "host rejected stale pivot approval because the audited worker tree changed during review"
	}
	st, err := rt.service.ResolvePivot(m.rootCtx, rt.state.ID, rt.state.Version, role, verdict, trajectory.LocalPrincipal(), string(role), "supervise-pivot-verdict:"+rt.state.ID+":"+strconvVersionUI(rt.state.Version))
	if err != nil {
		m.state = stateIdle
		m.appendBlock(block{kind: "system", body: "supervise: pivot verdict rejected by host: " + err.Error()})
		m.renderBlocks()
		return nil
	}
	rt.state = st
	decision := "rejected"
	if verdict.Decision == supervise.VerdictApprove {
		decision = "approved; plan v" + strconvVersionUI(st.PlanVersion) + " is now enforced"
	}
	feedback := "[Host-authenticated supervision decision]\nPivot " + decision + ".\nReason: " + textutil.SanitizeForTerminal(verdict.Rationale)
	m.appendBlock(block{kind: "system", body: "supervise: " + feedback})
	workerMsg := agent.Text(agent.RoleUser, feedback)
	m.msgs = append(m.msgs, workerMsg)
	m.persistMessage(workerMsg)
	m.state = stateIdle
	m.renderBlocks()
	var review tea.Cmd
	if currentTree != st.TreeDigest {
		patch, paths := m.superviseDiffSnapshot(st)
		review = m.observeSupervise(supervise.WorkerEvent{Kind: supervise.WorkerTreeChanged, TreeDigest: currentTree, DiffBytes: int64(len(patch)), ChangedPaths: paths})
	}
	if len(rt.followupQueue) > 0 {
		rt.followupResume = true
		return tea.Batch(review, m.nextSuperviseHostAction())
	}
	return tea.Batch(review, m.startStream())
}

func (m *Model) enqueueSuperviseFollowup(text string) tea.Cmd {
	rt := m.supervision
	text = strings.TrimSpace(text)
	if rt == nil || text == "" {
		return nil
	}
	st, followup, err := rt.service.EnqueueFollowup(m.rootCtx, rt.state.ID, rt.state.Version, supervise.RoleOperator, text, trajectory.LocalPrincipal(), "operator", "supervise-followup:"+rt.state.ID+":"+strconvVersionUI(rt.state.Version)+":"+digestBytes([]byte(text)))
	if err != nil {
		m.appendBlock(block{kind: "system", body: "supervise inbox: could not durably queue follow-up: " + err.Error()})
		m.renderBlocks()
		return nil
	}
	rt.state = st
	rt.followupQueue = append(rt.followupQueue, followup)
	m.appendBlock(block{kind: "user", body: text, queued: true, source: "supervise-inbox"})
	m.appendBlock(block{kind: "system", body: "supervise inbox: follow-up held until the next safe boundary; the watchdog will route related input to the active step and persist unrelated work as a task"})
	m.renderBlocks()
	if m.state == stateIdle && rt.state.Status == supervise.StatusRunning {
		return m.startNextSuperviseFollowupReview()
	}
	return nil
}

func (m *Model) startNextSuperviseFollowupReview() tea.Cmd {
	rt := m.supervision
	if rt == nil || rt.reviewing || rt.followupReviewing || len(rt.followupQueue) == 0 || rt.state.Status != supervise.StatusRunning {
		return nil
	}
	rt.followupCurrent = rt.followupQueue[0]
	rt.followupQueue = rt.followupQueue[1:]
	rt.followupReviewing = true
	rt.followupGeneration++
	generation, followup, state := rt.followupGeneration, rt.followupCurrent, rt.state
	profile := state.Config.Watchdog
	if profile.Provider == "" {
		profile.Provider = m.providerName
	}
	if profile.Model == "" {
		profile.Model = m.model
	}
	cfg, defaultProvider := m.cfg, m.providerName
	reviewer := supervise.Reviewer{Factory: superviseProviderFactory{cfgProvider: func(name string) (agent.Provider, error) {
		if name == "" {
			name = defaultProvider
		}
		return buildProviderByName(cfg, name)
	}}, Source: m.superviseEvidenceSnapshot(state)}
	ctx, cancel := context.WithCancel(m.rootCtx)
	rt.cancel = cancel
	m.state = stateStreaming
	return func() tea.Msg {
		result, err := runSuperviseWatchdogReview(ctx, reviewer, state.Config, profile, state.Config.WatchdogBudget, supervise.ReviewRequest{Role: supervise.RoleWatchdog, Kind: supervise.ReviewFollowup, State: state, Followup: followup.Text})
		return superviseFollowupReviewMsg{generation: generation, followup: followup, result: result, err: err}
	}
}

func onSuperviseFollowupReview(m *Model, msg superviseFollowupReviewMsg) (tea.Model, tea.Cmd) {
	rt := m.supervision
	if rt == nil || msg.generation != rt.followupGeneration || msg.followup.ID != rt.followupCurrent.ID {
		return m, nil
	}
	rt.cancel, rt.followupReviewing, rt.followupCurrent = nil, false, supervise.Followup{}
	related := msg.err == nil && msg.result.Verdict != nil && msg.result.Verdict.Kind == supervise.ReviewFollowup && msg.result.Verdict.Decision == supervise.VerdictApprove && msg.result.Verdict.Anchor == rt.state.Anchor()
	rationale := "watchdog classification unavailable; deferred conservatively"
	if msg.result.Verdict != nil {
		rationale = msg.result.Verdict.Rationale
	}
	deferred := !related || rt.state.Status != supervise.StatusRunning
	if related && rt.state.Status == supervise.StatusRunning {
		marker := "[supervise-followup:" + msg.followup.ID + "]"
		if !superviseFollowupDelivered(m.msgs, marker) {
			workerText := marker + "\n[Operator follow-up classified as directly related to the active supervised step]\n" + msg.followup.Text
			workerMsg := agent.Text(agent.RoleUser, workerText)
			m.msgs = append(m.msgs, workerMsg)
			m.persistMessage(workerMsg)
		}
		rt.followupRelated = true
		m.appendBlock(block{kind: "system", body: "supervise inbox: routed follow-up to the active step — " + textutil.SanitizeForTerminal(rationale)})
	} else {
		if store, err := m.taskStore(); err != nil {
			m.appendBlock(block{kind: "system", body: "supervise inbox: could not persist deferred task: " + err.Error()})
			rt.followupQueue = append([]supervise.Followup{msg.followup}, rt.followupQueue...)
			m.state = stateIdle
			return m, nil
		} else {
			marker := "[supervise-followup:" + msg.followup.ID + "]"
			title, body := superviseDeferredTaskText(rt.state, msg.followup, rationale)
			task, taskErr := existingSuperviseFollowupTask(store, marker)
			if taskErr == nil && task.ID == "" {
				task, taskErr = store.Create(title, body, tasks.StatusOpen)
			}
			if taskErr != nil {
				m.appendBlock(block{kind: "system", body: "supervise inbox: could not persist deferred task: " + taskErr.Error()})
				rt.followupQueue = append([]supervise.Followup{msg.followup}, rt.followupQueue...)
				m.state = stateIdle
				return m, nil
			} else {
				m.appendBlock(block{kind: "system", body: "supervise inbox: deferred unrelated request as task " + task.ID + " — " + task.Title})
			}
		}
	}
	st, err := rt.service.ResolveFollowup(m.rootCtx, rt.state.ID, rt.state.Version, supervise.RoleHost, msg.followup.ID, trajectory.LocalPrincipal(), "tui-host", "supervise-followup-resolved:"+msg.followup.ID)
	if err != nil {
		m.appendBlock(block{kind: "system", body: "supervise inbox: routed follow-up but could not acknowledge durable inbox state: " + err.Error()})
		rt.followupQueue = append([]supervise.Followup{msg.followup}, rt.followupQueue...)
		m.state = stateIdle
		m.renderBlocks()
		return m, nil
	}
	rt.state = st
	m.resolveSuperviseFollowupBlock(msg.followup.Text, deferred)
	m.renderBlocks()
	m.state = stateIdle
	return m, m.nextSuperviseHostAction()
}

func superviseDeferredTaskText(st supervise.State, followup supervise.Followup, rationale string) (string, string) {
	title := textutil.TruncateRunes(strings.Join(strings.Fields(followup.Text), " "), 120)
	title = strings.TrimSpace(textutil.AppendWithinBytes("", title, tasks.MaxTitleBytes))
	marker := "[supervise-followup:" + followup.ID + "]"
	body := marker + "\nDeferred by /supervise while run " + st.ID + " was focused on: " + st.Baseline.Objective + "\n\nWatchdog routing rationale: " + rationale + "\n\nOriginal request:\n" + followup.Text
	return title, textutil.AppendWithinBytes("", body, tasks.MaxBodyBytes)
}

func superviseFollowupDelivered(msgs []agent.Message, marker string) bool {
	for _, msg := range msgs {
		if msg.Role != agent.RoleUser {
			continue
		}
		for _, block := range msg.Content {
			if block.Text != nil && strings.Contains(block.Text.Text, marker) {
				return true
			}
		}
	}
	return false
}

func existingSuperviseFollowupTask(store tasks.Store, marker string) (tasks.Task, error) {
	all, err := store.List("")
	if err != nil {
		return tasks.Task{}, err
	}
	for _, task := range all {
		if strings.Contains(task.Body, marker) {
			return task, nil
		}
	}
	return tasks.Task{}, nil
}

func superviseDeferredTasksForRun(store tasks.Store, runID string) ([]tasks.Task, error) {
	all, err := store.List("")
	if err != nil {
		return nil, err
	}
	marker := "Deferred by /supervise while run " + runID + " was focused on:"
	deferred := make([]tasks.Task, 0)
	for _, task := range all {
		if task.Status != tasks.StatusDone && strings.Contains(task.Body, marker) {
			deferred = append(deferred, task)
		}
	}
	sort.SliceStable(deferred, func(i, j int) bool { return deferred[i].CreatedAt.Before(deferred[j].CreatedAt) })
	if len(deferred) > 64 {
		deferred = deferred[:64]
	}
	return deferred, nil
}

func renderSuperviseDeferredContinuation(runID string, deferred []tasks.Task) string {
	const maxPromptBytes = 16 << 10
	var out strings.Builder
	out.WriteString("[Host-authenticated supervised-work closeout]\n")
	out.WriteString("The supervised goal for run " + runID + " is complete. Revisit only these deferred tasks, in order, without claiming unrelated global backlog items. Start the oldest still-open item now; finish one active task before beginning the next:\n")
	for _, task := range deferred {
		line := "- " + task.ID + " — " + strings.Join(strings.Fields(task.Title), " ") + "\n"
		if out.Len()+len(line) > maxPromptBytes {
			out.WriteString("- [remaining deferred task IDs omitted from this prompt; query the task store by the run marker]\n")
			break
		}
		out.WriteString(line)
	}
	return out.String()
}

func (m *Model) resolveSuperviseFollowupBlock(text string, deferred bool) {
	for i := range m.blocks {
		if m.blocks[i].kind == "user" && m.blocks[i].queued && m.blocks[i].source == "supervise-inbox" && m.blocks[i].body == text {
			m.blocks[i].queued = false
			if deferred {
				m.blocks[i].source = "deferred-task"
			} else {
				m.blocks[i].source = "operator"
			}
			m.invalidateBlockCache(i)
			return
		}
	}
}

func (m *Model) observeSupervise(ev supervise.WorkerEvent) tea.Cmd {
	rt := m.supervision
	if rt == nil || rt.state.Status != supervise.StatusRunning {
		return nil
	}
	rt.sequence++
	ev.Sequence, ev.StepID = rt.sequence, rt.state.ActiveStep
	anchor := rt.state.Anchor()
	anchor.SessionSequence = rt.sequence
	anchor.TreeDigest = m.superviseTreeDigest()
	st, err := rt.service.UpdateRuntimeAnchor(m.rootCtx, rt.state.ID, rt.state.Version, supervise.RoleHost, anchor, trajectory.LocalPrincipal(), "tui-host", "supervise-event:"+rt.state.ID+":"+strconv.FormatUint(rt.sequence, 10))
	if err != nil {
		m.appendBlock(block{kind: "system", body: "supervise event state: " + err.Error()})
		return nil
	}
	rt.state = st
	trigger := rt.detector.Observe(ev, st.Anchor())
	st, err = rt.service.RecordDetectorSnapshot(m.rootCtx, st.ID, st.Version, supervise.RoleHost, rt.detector.Snapshot(), trajectory.LocalPrincipal(), "tui-host", "supervise-detector:"+st.ID+":"+strconv.FormatUint(rt.sequence, 10))
	if err != nil {
		m.appendBlock(block{kind: "system", body: "supervise detector state: " + err.Error()})
		return nil
	}
	rt.state = st
	if trigger == nil || st.Status != supervise.StatusRunning {
		return nil
	}
	if rt.reviewing || rt.followupReviewing {
		rt.pendingTrigger = coalesceSuperviseTriggers(rt.pendingTrigger, trigger)
		return nil
	}
	return m.startSuperviseEventReview(trigger)
}

// recordSuperviseControlEvent adds a native pivot/completion fact to durable
// detector history without advancing the worker anchor. Successful control
// requests already froze that anchor for a direct authority review, so an
// ordinary observation transition here would make every verdict stale.
func (m *Model) recordSuperviseControlEvent(kind supervise.WorkerEventKind) {
	rt := m.supervision
	if rt == nil || rt.service == nil || superviseTerminal(rt.state.Status) {
		return
	}
	ev := supervise.WorkerEvent{
		ID:       fmt.Sprintf("control-event:%s:%d", kind, rt.state.Version),
		Kind:     kind,
		Sequence: rt.state.SessionSequence,
		StepID:   rt.state.ActiveStep,
		At:       time.Now().UTC(),
	}
	_ = rt.detector.Observe(ev, rt.state.Anchor())
	st, err := rt.service.RecordDetectorSnapshot(m.rootCtx, rt.state.ID, rt.state.Version, supervise.RoleHost, rt.detector.Snapshot(), trajectory.LocalPrincipal(), "tui-host", "supervise-control-event:"+rt.state.ID+":"+strconvVersionUI(rt.state.Version))
	if err != nil {
		m.appendBlock(block{kind: "system", body: "supervise detector state: " + err.Error()})
		return
	}
	rt.state = st
}

func (m *Model) startSuperviseEventReview(trigger *supervise.Trigger) tea.Cmd {
	rt := m.supervision
	if rt == nil || trigger == nil || rt.reviewing || rt.followupReviewing || rt.state.Status != supervise.StatusRunning {
		return nil
	}
	rt.reviewing = true
	rt.reviewGeneration++
	generation := rt.reviewGeneration
	state := rt.state
	source := m.superviseEvidenceSnapshot(state)
	cfg := m.cfg
	defaultProvider, defaultModel := m.providerName, m.model
	profile := state.Config.Watchdog
	if profile.Provider == "" {
		profile.Provider = defaultProvider
	}
	if profile.Model == "" {
		profile.Model = defaultModel
	}
	reviewer := supervise.Reviewer{Factory: superviseProviderFactory{cfgProvider: func(name string) (agent.Provider, error) {
		if name == "" {
			name = defaultProvider
		}
		return buildProviderByName(cfg, name)
	}}, Source: source}
	ctx, cancel := context.WithCancel(m.rootCtx)
	rt.cancel = cancel
	return func() tea.Msg {
		result, err := runSuperviseWatchdogReview(ctx, reviewer, state.Config, profile, state.Config.WatchdogBudget, supervise.ReviewRequest{Role: supervise.RoleWatchdog, Kind: supervise.ReviewEvent, State: state, Trigger: trigger})
		return superviseEventReviewMsg{generation: generation, trigger: trigger, result: result, err: err}
	}
}

func onSuperviseEventReview(m *Model, msg superviseEventReviewMsg) (tea.Model, tea.Cmd) {
	rt := m.supervision
	if rt == nil || msg.generation != rt.reviewGeneration {
		return m, nil
	}
	rt.reviewing = false
	rt.cancel = nil
	startPending := func() tea.Cmd {
		return m.nextSuperviseHostAction()
	}
	if msg.err != nil {
		reason := textutil.TruncateRunes(msg.err.Error(), 4096)
		st, err := rt.service.RecordReviewFailure(m.rootCtx, rt.state.ID, rt.state.Version, supervise.RoleHost, reason, trajectory.LocalPrincipal(), "tui-host", "supervise-review-failed:"+rt.state.ID+":"+strconvVersionUI(rt.state.Version))
		if err == nil {
			rt.state = st
		}
		if rt.state.Status == supervise.StatusPaused {
			m.cancelSupervisedWorker()
			m.appendBlock(block{kind: "system", body: "supervise paused: watchdog failed repeatedly: " + msg.err.Error()})
		}
		return m, startPending()
	}
	if msg.result.Verdict == nil {
		return m, startPending()
	}
	if msg.result.Verdict.Anchor != rt.state.Anchor() {
		// The worker is allowed to keep moving while an event review runs.
		// Preserve the reviewed signal and coalesce it with newer signals so
		// an ordinary tool boundary cannot silently erase an intervention.
		rt.pendingTrigger = coalesceSuperviseTriggers(msg.trigger, rt.pendingTrigger)
		return m, startPending()
	}
	if rt.correctionPending && (msg.result.Verdict.Decision == supervise.VerdictContinue || msg.result.Verdict.Decision == supervise.VerdictApprove) {
		if st, err := rt.service.RecordCorrectionResult(m.rootCtx, rt.state.ID, rt.state.Version, supervise.RoleHost, true, trajectory.LocalPrincipal(), "tui-host", "supervise-correction-ok:"+rt.state.ID+":"+strconvVersionUI(rt.state.Version)); err == nil {
			rt.state = st
			rt.correctionPending = false
		}
	}
	if rt.correctionPending && msg.result.Verdict.Decision == supervise.VerdictCorrect {
		if st, err := rt.service.RecordCorrectionResult(m.rootCtx, rt.state.ID, rt.state.Version, supervise.RoleHost, false, trajectory.LocalPrincipal(), "tui-host", "supervise-correction-failed:"+rt.state.ID+":"+strconvVersionUI(rt.state.Version)); err == nil {
			rt.state = st
		}
		if rt.state.Status == supervise.StatusPaused {
			m.cancelSupervisedWorker()
			m.appendBlock(block{kind: "system", body: "supervise paused: watchdog corrections did not restore alignment"})
			return m, nil
		}
	}
	st, err := rt.service.RecordEventVerdict(m.rootCtx, rt.state.ID, rt.state.Version, supervise.RoleWatchdog, *msg.result.Verdict, msg.trigger, trajectory.LocalPrincipal(), "watchdog", "supervise-event-verdict:"+rt.state.ID+":"+strconvVersionUI(rt.state.Version))
	if err != nil {
		return m, startPending()
	}
	rt.state = st
	if msg.result.Verdict.Decision == supervise.VerdictApprove && superviseTriggerHas(msg.trigger, supervise.TriggerStepCompletion) {
		if advanced, advanceErr := rt.service.AdvanceStep(m.rootCtx, rt.state.ID, rt.state.Version, supervise.RoleWatchdog, rt.state.Anchor(), trajectory.LocalPrincipal(), "watchdog", "supervise-step-approved:"+rt.state.ID+":"+strconvVersionUI(rt.state.Version)); advanceErr == nil {
			rt.state = advanced
			m.appendBlock(block{kind: "system", body: "supervise: watchdog accepted step evidence; active step is now " + advanced.ActiveStep})
		} else {
			m.appendBlock(block{kind: "system", body: "supervise: step transition rejected by host: " + advanceErr.Error()})
		}
	}
	switch msg.result.Verdict.Decision {
	case supervise.VerdictCorrect:
		rt.correctionPending = true
		m.injectWatchdogCorrection(msg.result.Verdict.Correction, msg.result.Verdict.Rationale)
	case supervise.VerdictPause, supervise.VerdictStop:
		m.cancelSupervisedWorker()
		m.appendBlock(block{kind: "system", body: "supervise paused by watchdog: " + textutil.SanitizeForTerminal(msg.result.Verdict.Rationale)})
	}
	m.renderBlocks()
	return m, startPending()
}

func (m *Model) nextSuperviseHostAction() tea.Cmd {
	rt := m.supervision
	if rt == nil || rt.state.Status != supervise.StatusRunning || rt.reviewing || rt.followupReviewing {
		return nil
	}
	if m.state == stateIdle && m.queuedPrompt != "" {
		return m.promoteQueuedPrompt()
	}
	if rt.pendingTrigger != nil {
		pending := rt.pendingTrigger
		rt.pendingTrigger = nil
		pending.Anchor = rt.state.Anchor()
		return m.startSuperviseEventReview(pending)
	}
	if len(rt.followupQueue) > 0 {
		return m.startNextSuperviseFollowupReview()
	}
	if m.state == stateIdle && (rt.followupResume || rt.followupRelated) {
		rt.followupResume, rt.followupRelated = false, false
		return m.startStream()
	}
	return nil
}

func (m *Model) invalidateSuperviseEventReview() {
	rt := m.supervision
	if rt == nil || !rt.reviewing {
		return
	}
	if rt.cancel != nil {
		rt.cancel()
	}
	rt.cancel = nil
	rt.reviewing = false
	rt.pendingTrigger = nil
	rt.reviewGeneration++
}

func (m *Model) injectWatchdogCorrection(correction, rationale string) {
	text := "[Host-authenticated stado watchdog correction]\n" + textutil.SanitizeForTerminal(correction)
	if strings.TrimSpace(rationale) != "" {
		text += "\nReason: " + textutil.SanitizeForTerminal(rationale)
	}
	m.cancelRunningStream()
	m.cancelRunningTool()
	m.clearPendingToolQueue()
	m.queuedPrompt = text
	m.appendBlock(block{kind: "user", body: text, queued: true, source: "watchdog"})
}

func (m *Model) cancelSupervisedWorker() {
	if m.supervision != nil {
		if m.supervision.cancel != nil {
			m.supervision.cancel()
			m.supervision.cancel = nil
		}
		// Late provider messages cannot mutate a cancelled run or reopen a
		// trusted drawer after the operator has ended supervision.
		m.supervision.generation++
		m.supervision.verifierGeneration++
		m.supervision.pivotGeneration++
		m.supervision.reviewGeneration++
		m.supervision.followupGeneration++
		m.supervision.reviewing = false
		m.supervision.followupReviewing = false
	}
	m.cancelRunningStream()
	m.cancelRunningTool()
	m.clearPendingToolQueue()
	m.queuedPrompt = ""
	m.steeringMsg = ""
}

func (m *Model) requestSuperviseRiskApproval(call agent.ToolUseBlock, boundary string) tea.Cmd {
	rt := m.supervision
	if rt == nil {
		return m.executeCallAsync(call)
	}
	rt.riskCall = &call
	response := make(chan bool, 1)
	m.approval = &approvalRequest{title: "Supervised work: operator authority required", body: "The worker requested a human-only boundary (" + boundary + ") via " + call.Name + ". Allow executes this exact tool call; deny returns an error to the worker.", response: response}
	m.approvalFocused = true
	m.approvalAllowSelected = false
	m.state = stateApproval
	m.renderBlocks()
	generation := rt.generation
	return func() tea.Msg {
		return superviseRiskDecisionMsg{generation: generation, approved: <-response, boundary: boundary}
	}
}

func onSuperviseRiskDecision(m *Model, msg superviseRiskDecisionMsg) (tea.Model, tea.Cmd) {
	rt := m.supervision
	if rt == nil || msg.generation != rt.generation || rt.riskCall == nil {
		return m, nil
	}
	call := *rt.riskCall
	rt.riskCall = nil
	review := m.observeSupervise(supervise.WorkerEvent{Kind: supervise.WorkerRiskBoundary, Boundary: msg.boundary, Succeeded: msg.approved})
	if msg.approved {
		return m, tea.Batch(review, m.executeCallAsync(call))
	}
	m.pendingResults = append(m.pendingResults, agent.ToolResultBlock{ToolUseID: call.ID, Content: "denied by operator at supervised human-only boundary: " + msg.boundary, IsError: true})
	return m, tea.Batch(review, m.advanceToolQueue())
}

func superviseRiskBoundary(call agent.ToolUseBlock) string {
	name := strings.ToLower(call.Name)
	args := strings.ToLower(string(call.Input))
	normalized := strings.NewReplacer(".", " ", "_", " ", "-", " ", "/", " ").Replace(name)
	words := strings.Fields(normalized)
	wordSet := map[string]bool{}
	for _, word := range words {
		wordSet[word] = true
	}
	for _, word := range words {
		switch word {
		case "merge":
			return "merge"
		case "push":
			return "push"
		case "release":
			return "release"
		case "publish":
			return "publish"
		case "deploy":
			return "deploy"
		case "delete", "remove", "destroy":
			return "destructive operation"
		}
	}
	externalNamespace := wordSet["email"] || wordSet["gmail"] || wordSet["slack"] || wordSet["discord"] || wordSet["calendar"] || wordSet["github"] || wordSet["gitlab"] || wordSet["jira"] || wordSet["linear"] || wordSet["notion"] || wordSet["http"] || wordSet["web"]
	externalAction := wordSet["send"] || wordSet["post"] || wordSet["create"] || wordSet["update"] || wordSet["comment"] || wordSet["message"] || wordSet["invite"]
	if externalNamespace && externalAction {
		return "external commitment"
	}
	authorityNoun := wordSet["permission"] || wordSet["permissions"] || wordSet["budget"] || wordSet["grant"] || wordSet["trust"] || wordSet["authority"]
	authorityAction := wordSet["set"] || wordSet["update"] || wordSet["write"] || wordSet["create"] || wordSet["increase"] || wordSet["grant"] || wordSet["approve"] || wordSet["authorize"]
	if authorityNoun && authorityAction {
		return "permission or budget change"
	}
	// For generic command runners, inspect command text as well as the tool
	// name. Keeping this branch behind an execution-tool check avoids treating
	// documentation written by fs.write as an actual merge or deployment.
	executionTool := false
	for _, word := range words {
		switch word {
		case "bash", "shell", "exec", "execute", "run", "command", "terminal":
			executionTool = true
		}
	}
	if executionTool {
		for _, command := range superviseExecutionCommands(call.Input) {
			if superviseDestructiveCommand(command, 0) {
				return "destructive operation"
			}
		}
		checks := []struct{ needle, label string }{
			{"gh pr merge", "merge"}, {"git merge", "merge"},
			{"git push", "push"},
			{"gh release", "release"}, {"npm publish", "publish"}, {"cargo publish", "publish"},
			{"kubectl apply", "deploy"}, {"terraform apply", "deploy"},
			{"drop table", "destructive operation"}, {"truncate table", "destructive operation"},
		}
		for _, check := range checks {
			if strings.Contains(args, check.needle) {
				return check.label
			}
		}
		externalCommands := []string{
			"gh issue create", "gh issue comment", "gh pr create", "gh pr comment", "gh api --method post", "gh api -x post",
			"glab issue create", "glab mr create", "curl -x post", "curl --request post", "wget --post", "sendmail ",
		}
		for _, command := range externalCommands {
			if strings.Contains(args, command) {
				return "external commitment"
			}
		}
		for _, word := range strings.FieldsFunc(args, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		}) {
			switch word {
			case "deploy":
				return "deploy"
			case "release":
				return "release"
			case "publish":
				return "publish"
			}
		}
	}
	return ""
}

func superviseExecutionCommands(input json.RawMessage) []string {
	var direct string
	if json.Unmarshal(input, &direct) == nil && strings.TrimSpace(direct) != "" {
		return []string{direct}
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(input, &envelope) != nil {
		return nil
	}
	commands := make([]string, 0, 3)
	for _, key := range []string{"command", "cmd", "script"} {
		raw, ok := envelope[key]
		if !ok {
			continue
		}
		var command string
		if json.Unmarshal(raw, &command) == nil && strings.TrimSpace(command) != "" {
			commands = append(commands, command)
		}
	}
	for _, key := range []string{"argv", "args"} {
		raw, ok := envelope[key]
		if !ok {
			continue
		}
		var argv []string
		if json.Unmarshal(raw, &argv) == nil && len(argv) > 0 {
			commands = append(commands, strings.Join(argv, " "))
		}
	}
	return commands
}

func superviseDestructiveCommand(command string, depth int) bool {
	if depth > 4 {
		return true
	}
	words := superviseShellWords(command)
	for i, word := range words {
		base := path.Base(strings.ToLower(word))
		switch {
		case base == "rm" || base == "rmdir" || base == "unlink" || base == "shred" || base == "truncate" || base == "wipefs" || base == "dd":
			return true
		case base == "mkfs" || strings.HasPrefix(base, "mkfs."):
			return true
		case base == "find" && superviseWordsContain(words[i+1:], "-delete"):
			return true
		case base == "git" && superviseDestructiveGitArgs(words[i+1:]):
			return true
		case (base == "kubectl" || base == "oc") && superviseWordsContain(words[i+1:], "delete"):
			return true
		case (base == "docker" || base == "podman") && superviseWordsContainAny(words[i+1:], "rm", "rmi", "prune"):
			return true
		case base == "drop" && superviseWordsContainAny(words[i+1:], "table", "database", "schema"):
			return true
		case base == "bash" || base == "dash" || base == "sh" || base == "zsh":
			for j := i + 1; j < len(words); j++ {
				flag := strings.ToLower(words[j])
				if !strings.HasPrefix(flag, "-") {
					break
				}
				if strings.Contains(flag, "c") {
					payload := j + 1
					if payload < len(words) && words[payload] == "--" {
						payload++
					}
					if payload < len(words) && superviseDestructiveCommand(words[payload], depth+1) {
						return true
					}
					break
				}
			}
		}
	}
	return false
}

func superviseDestructiveGitArgs(args []string) bool {
	if superviseWordsContain(args, "clean") {
		return true
	}
	if superviseWordsContain(args, "reset") && superviseWordsContainAny(args, "--hard", "--merge", "--keep") {
		return true
	}
	if superviseWordsContain(args, "branch") && superviseWordsContain(args, "-d") {
		return true
	}
	if superviseWordsContain(args, "stash") && superviseWordsContainAny(args, "drop", "clear") {
		return true
	}
	return false
}

func superviseWordsContain(words []string, want string) bool {
	want = strings.ToLower(want)
	for _, word := range words {
		if strings.ToLower(word) == want {
			return true
		}
	}
	return false
}

func superviseWordsContainAny(words []string, wants ...string) bool {
	for _, want := range wants {
		if superviseWordsContain(words, want) {
			return true
		}
	}
	return false
}

func superviseShellWords(command string) []string {
	words := make([]string, 0, 8)
	var current strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if current.Len() > 0 {
			words = append(words, current.String())
			current.Reset()
		}
	}
	for _, r := range command {
		if escaped {
			current.WriteRune(r)
			escaped = false
			continue
		}
		if quote == '\'' {
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
			continue
		}
		switch {
		case r == '\'' || r == '"':
			quote = r
		case unicode.IsSpace(r) || strings.ContainsRune(";|&()<>", r):
			flush()
		default:
			current.WriteRune(r)
		}
	}
	if escaped {
		current.WriteRune('\\')
	}
	flush()
	return words
}

func superviseTriggerHas(trigger *supervise.Trigger, want supervise.TriggerType) bool {
	if trigger == nil {
		return false
	}
	for _, signal := range trigger.Signals {
		if signal.Type == want {
			return true
		}
	}
	return false
}

func superviseTurnUsesControl(calls []agent.ToolUseBlock, name string) bool {
	for _, call := range calls {
		if call.Name == name {
			return true
		}
	}
	return false
}

func superviseClaimsCompletion(text string) bool {
	text = strings.ToLower(textutil.SanitizeForTerminal(text))
	for _, negative := range []string{"not done", "not complete", "not completed", "isn't complete", "is not complete", "still need", "remaining work", "before completion"} {
		if strings.Contains(text, negative) {
			return false
		}
	}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.Trim(strings.TrimSpace(raw), "#*`_-:!. ")
		switch line {
		case "done", "complete", "completed", "finished":
			return true
		}
		for _, prefix := range []string{"implementation is complete", "the implementation is complete", "work is complete", "the work is complete", "all requested work is complete", "i have completed", "i've completed", "ready to merge", "ready for review", "all tests pass"} {
			if strings.HasPrefix(line, prefix) {
				return true
			}
		}
	}
	return false
}

func coalesceSuperviseTriggers(first, second *supervise.Trigger) *supervise.Trigger {
	if first == nil {
		return second
	}
	if second == nil {
		return first
	}
	out := &supervise.Trigger{Anchor: second.Anchor, CreatedAt: first.CreatedAt}
	if out.CreatedAt.IsZero() || !second.CreatedAt.IsZero() && second.CreatedAt.Before(out.CreatedAt) {
		out.CreatedAt = second.CreatedAt
	}
	index := map[string]int{}
	for _, trigger := range []*supervise.Trigger{first, second} {
		for _, signal := range trigger.Signals {
			raw, _ := json.Marshal(struct {
				Type       supervise.TriggerType `json:"type"`
				Attributes map[string]string     `json:"attributes,omitempty"`
			}{signal.Type, signal.Attributes})
			key := string(raw)
			if at, ok := index[key]; ok {
				seen := map[string]bool{}
				for _, ref := range out.Signals[at].EvidenceRefs {
					seen[ref] = true
				}
				for _, ref := range signal.EvidenceRefs {
					if !seen[ref] && len(out.Signals[at].EvidenceRefs) < 64 {
						out.Signals[at].EvidenceRefs = append(out.Signals[at].EvidenceRefs, ref)
						seen[ref] = true
					}
				}
				if superviseSeverityRank(signal.Severity) > superviseSeverityRank(out.Signals[at].Severity) {
					out.Signals[at].Severity = signal.Severity
				}
				continue
			}
			if len(out.Signals) >= 32 {
				continue
			}
			index[key] = len(out.Signals)
			signal.EvidenceRefs = append([]string(nil), signal.EvidenceRefs...)
			out.Signals = append(out.Signals, signal)
		}
	}
	out.ID = "trigger_coalesced_" + digestBytes([]byte(first.ID+"\x00"+second.ID))
	return out
}

func superviseSeverityRank(severity string) int {
	switch severity {
	case "critical":
		return 4
	case "high":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	default:
		return 0
	}
}

func superviseReviewRetryDelay(cfg supervise.Config, attempt int) time.Duration {
	if cfg.Mode != supervise.ModeLive {
		return 250 * time.Millisecond
	}
	ms := cfg.LiveRetryBaseMillis
	for i := 0; i < attempt && ms < cfg.LiveRetryMaxMillis; i++ {
		if ms > cfg.LiveRetryMaxMillis/2 {
			ms = cfg.LiveRetryMaxMillis
		} else {
			ms *= 2
		}
	}
	if ms > cfg.LiveRetryMaxMillis {
		ms = cfg.LiveRetryMaxMillis
	}
	return time.Duration(ms) * time.Millisecond
}

// runSuperviseWatchdogReview applies one retry contract to every watchdog
// role, including requirements, follow-up routing, pivots, and detector
// events. Event mode exhausts a bounded review; live mode remains active with
// capped backoff until it succeeds or the operator cancels its context.
func runSuperviseWatchdogReview(ctx context.Context, reviewer supervise.Reviewer, cfg supervise.Config, profile supervise.RoleProfile, budget supervise.RoleBudget, request supervise.ReviewRequest) (supervise.ReviewResult, error) {
	var result supervise.ReviewResult
	var err error
	for attempt := 0; ; attempt++ {
		result, err = reviewer.Run(ctx, profile, budget, request)
		if err == nil {
			return result, nil
		}
		if cfg.Mode == supervise.ModeEvent && attempt+1 >= cfg.EventReviewRetries {
			return result, err
		}
		timer := time.NewTimer(superviseReviewRetryDelay(cfg, attempt))
		select {
		case <-ctx.Done():
			timer.Stop()
			return result, ctx.Err()
		case <-timer.C:
		}
	}
}

func strconvVersionUI(v uint64) string { return strconv.FormatUint(v, 10) }

func (m *Model) superviseSystemContext() string {
	if m.supervision == nil || m.supervision.state.Status != supervise.StatusRunning && m.supervision.state.Status != supervise.StatusPivotPending {
		return ""
	}
	st := m.supervision.state
	raw, _ := json.Marshal(struct {
		RunID          string                  `json:"run_id"`
		Status         supervise.Status        `json:"status"`
		Mode           supervise.Mode          `json:"watchdog_mode"`
		PivotApproval  supervise.PivotApproval `json:"pivot_approval"`
		PlanVersion    uint64                  `json:"plan_version"`
		ActiveStep     string                  `json:"active_step"`
		CompletedSteps []string                `json:"completed_steps,omitempty"`
		Baseline       supervise.Baseline      `json:"approved_baseline"`
	}{st.ID, st.Status, st.Config.Mode, st.Config.PivotApproval, st.PlanVersion, st.ActiveStep, st.CompletedSteps, st.Baseline})
	return "Stado supervised-work contract (host-owned and operator-approved; higher priority than model-authored plans):\n" + string(raw) + "\n\n" +
		"Work against exactly one active step and preserve direct evidence. Operator follow-ups are held at a host boundary: related input is explicitly labeled and delivered here; unrelated input is persisted in the shared tasks backlog and must not displace this work. Use supervise__report_progress for evidence and step-completion claims. Tactical implementation adjustments are free. Before changing the plan, use supervise__request_pivot. These boundaries are human-only: objective, acceptance criteria, permissions, budgets, destructive actions, merge/release/deploy, and external commitments always require the operator. Never announce completion as accepted in prose: use supervise__request_completion with criterion-linked evidence, then wait for the independent verifier. The root worker remains accountable for every child; nested supervision is unavailable."
}

func (m *Model) superviseTreeDigest() string {
	return superviseSessionTreeDigest(m.session)
}

func superviseSessionTreeDigest(session *stadogit.Session) string {
	if session == nil {
		return "unavailable"
	}
	head, err := session.TreeHead()
	if err != nil || head.IsZero() {
		return "empty"
	}
	return head.String()
}

func (m *Model) superviseDiffSnapshot(st supervise.State) (string, []string) {
	if m.session == nil || st.InitialTreeDigest == "" {
		return "", nil
	}
	parse := func(v string) plumbing.Hash {
		if v == "" || v == "empty" || v == "unavailable" {
			return plumbing.ZeroHash
		}
		return plumbing.NewHash(v)
	}
	from, to := parse(st.InitialTreeDigest), parse(m.superviseTreeDigest())
	patch, err := m.session.PatchBetweenHeads(from, to, 64<<10)
	if err != nil {
		return "", nil
	}
	fromTree, toTree := plumbing.ZeroHash, plumbing.ZeroHash
	if !from.IsZero() {
		fromTree, _ = m.session.TreeFromCommit(from)
	}
	if !to.IsZero() {
		toTree, _ = m.session.TreeFromCommit(to)
	}
	paths, _ := m.session.ChangedFilesBetween(fromTree, toTree)
	return patch, paths
}

func renderWizardSeed(d supervisepicker.Draft) string {
	parts := []string{"Objective: " + strings.TrimSpace(d.Objective)}
	if strings.TrimSpace(d.AcceptanceHints) != "" {
		parts = append(parts, "Operator acceptance/gate hints: "+strings.TrimSpace(d.AcceptanceHints))
	}
	if strings.TrimSpace(d.DefinitionHints) != "" {
		parts = append(parts, "Operator definition-of-done hints: "+strings.TrimSpace(d.DefinitionHints))
	}
	if strings.TrimSpace(d.VerificationHints) != "" {
		parts = append(parts, "Operator verification hints: "+strings.TrimSpace(d.VerificationHints))
	}
	return strings.Join(parts, "\n")
}

func renderSuperviseBaseline(b supervise.Baseline) string {
	var out strings.Builder
	out.WriteString("Objective\n  " + b.Objective + "\n")
	writeList := func(title string, values []string) {
		if len(values) == 0 {
			return
		}
		out.WriteString("\n" + title + "\n")
		for _, v := range values {
			out.WriteString("  - " + v + "\n")
		}
	}
	writeList("Constraints", b.Constraints)
	writeList("Non-goals", b.NonGoals)
	writeList("Acceptance criteria", b.AcceptanceCriteria)
	out.WriteString("\nPlan\n")
	for _, step := range b.Plan {
		out.WriteString(fmt.Sprintf("  %s. %s — done when %s\n", step.ID, step.Title, step.DoneWhen))
	}
	writeList("Definition of done", b.DefinitionOfDone)
	writeList("Verification", b.Verification)
	writeList("Risks", b.Risks)
	return strings.TrimSpace(out.String())
}

func renderSupervisePivot(req supervise.PivotRequest) string {
	var out strings.Builder
	out.WriteString("Kind: " + string(req.Kind) + "\nReason: " + req.Reason + "\n")
	if req.ProposedBaseline != nil {
		out.WriteString("\nProposed replacement baseline\n\n" + renderSuperviseBaseline(*req.ProposedBaseline))
	} else {
		out.WriteString("\nProposed replacement plan\n")
		for _, step := range req.ProposedPlan {
			out.WriteString(fmt.Sprintf("  %s. %s — done when %s\n", step.ID, step.Title, step.DoneWhen))
		}
	}
	return strings.TrimSpace(out.String())
}

func renderSuperviseStatus(st supervise.State) string {
	var out strings.Builder
	attached := st.AttachedSessionID
	if attached == "" {
		attached = st.RootSessionID
	}
	fmt.Fprintf(&out, "supervise %s\nstatus: %s\nroot: %s · worker session: %s\nmode: %s\npivot approval: %s\nplan: v%d · active step: %s\nworker sequence: %d\nwatchdog failures: %d/%d · correction failures: %d/%d",
		st.ID, st.Status, st.RootSessionID, attached, st.Config.Mode, st.Config.PivotApproval, st.PlanVersion, st.ActiveStep, st.SessionSequence, st.FailedEventStreak, st.Config.FailedEventLimit, st.FailedCorrectionCount, st.Config.CorrectionLimit)
	if st.PauseReason != "" {
		out.WriteString("\npause reason: " + st.PauseReason)
	}
	if len(st.WatchdogHandoff.OpenConcerns) > 0 {
		out.WriteString("\nopen concerns: " + strings.Join(st.WatchdogHandoff.OpenConcerns, "; "))
	}
	if st.PendingPivot != nil {
		out.WriteString("\npending pivot: " + string(st.PendingPivot.Kind) + " — " + st.PendingPivot.Reason)
	}
	if len(st.PendingFollowups) > 0 {
		fmt.Fprintf(&out, "\npending follow-ups: %d (durable inbox)", len(st.PendingFollowups))
	}
	return textutil.SanitizeForTerminal(out.String())
}

func superviseTerminal(status supervise.Status) bool {
	return status == supervise.StatusCompleted || status == supervise.StatusCancelled
}

func superviseAcceptsFollowup(status supervise.Status) bool {
	return status == supervise.StatusRunning || status == supervise.StatusPivotPending || status == supervise.StatusVerifying || status == supervise.StatusPaused
}

func (m *Model) cancelActiveSuperviseReview() bool {
	if m.supervision == nil || m.supervision.cancel == nil {
		return false
	}
	m.supervision.cancel()
	return true
}

type staticSuperviseEvidence struct {
	anchor     supervise.Anchor
	bySection  map[supervise.EvidenceSection][]supervise.EvidenceItem
	repository *superviseRepositorySnapshot
}

type superviseRepositorySnapshot struct {
	session *stadogit.Session
	head    plumbing.Hash
}

func (m *Model) superviseEvidenceSnapshot(st supervise.State) *staticSuperviseEvidence {
	s := &staticSuperviseEvidence{anchor: st.Anchor(), bySection: map[supervise.EvidenceSection][]supervise.EvidenceItem{}}
	if m.session != nil && st.TreeDigest != "" && st.TreeDigest != "empty" && st.TreeDigest != "unavailable" {
		s.repository = &superviseRepositorySnapshot{session: m.session, head: plumbing.NewHash(st.TreeDigest)}
	}
	addJSON := func(section supervise.EvidenceSection, id, summary string, value any) {
		raw, _ := json.Marshal(value)
		s.bySection[section] = append(s.bySection[section], supervise.EvidenceItem{ID: id, Section: section, Summary: summary, Content: boundedSuperviseEvidenceContent(string(raw), 64<<10)})
	}
	addJSON(supervise.EvidenceState, "state", "Current host-owned supervision state", st)
	addJSON(supervise.EvidenceContract, "contract", "Approved or proposed contract", st.Baseline)
	addJSON(supervise.EvidencePlan, "plan", "Current plan", st.Baseline.Plan)
	addJSON(supervise.EvidenceBudgets, "budgets", "Worker and review budgets", map[string]any{"worker_tokens": m.totalTokens(), "worker_hard_tokens": m.budgetHardTokens, "watchdog": st.Config.WatchdogBudget, "verifier": st.Config.VerifierBudget})
	for _, ev := range st.Detector.History {
		addJSON(supervise.EvidenceEvents, ev.ID, string(ev.Kind), ev)
	}
	for i, ev := range st.Evidence {
		section := supervise.EvidenceState
		if ev.Kind == "verification_gate" || strings.HasPrefix(ev.Kind, "verification") {
			section = supervise.EvidenceVerification
		}
		addJSON(section, fmt.Sprintf("recorded-evidence:%d", i), ev.Kind+": "+ev.Summary, ev)
	}
	if patch, paths := m.superviseDiffSnapshot(st); patch != "" || len(paths) > 0 {
		content := boundedSuperviseEvidenceContent(patch, 64<<10)
		s.bySection[supervise.EvidenceDiff] = append(s.bySection[supervise.EvidenceDiff], supervise.EvidenceItem{ID: "worker-diff", Section: supervise.EvidenceDiff, Summary: strings.Join(paths, ", "), Content: content})
	}
	for i, b := range m.blocks {
		if b.kind == "thinking" {
			continue
		}
		item := supervise.EvidenceItem{ID: fmt.Sprintf("block:%d", i), Section: supervise.EvidenceTranscript, Sequence: uint64(i + 1), Summary: b.kind, Content: textutil.SanitizeForTerminal(strings.TrimSpace(b.body))}
		if b.kind == "tool" {
			item.Section = supervise.EvidenceTools
			item.Summary = b.toolName
			item.Content = textutil.SanitizeForTerminal(strings.TrimSpace(b.toolArgs + "\n" + b.toolResult))
		}
		if len(item.Content) > 64<<10 {
			item.Content = boundedSuperviseEvidenceContent(item.Content, 64<<10)
		}
		s.bySection[item.Section] = append(s.bySection[item.Section], item)
	}
	if m.fleet != nil {
		addJSON(supervise.EvidenceChildren, "children:fleet", "Root-owned background-subworker lifecycle and bounded progress/results", m.fleet.List())
	}
	if len(m.subagents) > 0 {
		addJSON(supervise.EvidenceChildren, "children:recent", "Recent root-owned synchronous subworker lifecycle and scope summary", m.subagents)
	}
	return s
}

func boundedSuperviseEvidenceContent(content string, maxBytes int) string {
	if len(content) <= maxBytes {
		return content
	}
	const marker = "\n[evidence truncated by host boundary]"
	return textutil.AppendWithinBytes("", content, maxBytes-len(marker)) + marker
}

func (s *staticSuperviseEvidence) Read(_ context.Context, q supervise.EvidenceQuery) (supervise.EvidencePage, error) {
	return s.page(q, false)
}
func (s *staticSuperviseEvidence) Search(_ context.Context, q supervise.EvidenceQuery) (supervise.EvidencePage, error) {
	return s.page(q, true)
}
func (s *staticSuperviseEvidence) Follow(_ context.Context, q supervise.EvidenceQuery) (supervise.EvidencePage, error) {
	return s.page(q, false)
}
func (s *staticSuperviseEvidence) page(q supervise.EvidenceQuery, search bool) (supervise.EvidencePage, error) {
	if q.Section == supervise.EvidenceRepository {
		return s.repositoryPage(q, search)
	}
	items := append([]supervise.EvidenceItem(nil), s.bySection[q.Section]...)
	if search {
		needle := strings.ToLower(q.Pattern)
		filtered := items[:0]
		for _, item := range items {
			if strings.Contains(strings.ToLower(item.Summary+"\n"+item.Content), needle) {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	if len(q.Kinds) > 0 {
		allowed := map[string]bool{}
		for _, kind := range q.Kinds {
			allowed[kind] = true
		}
		filtered := items[:0]
		for _, item := range items {
			if allowed[item.Summary] {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Sequence < items[j].Sequence })
	start := 0
	if q.Cursor != "" {
		var err error
		start, err = strconv.Atoi(q.Cursor)
		if err != nil || start < 0 || start > len(items) {
			return supervise.EvidencePage{}, errors.New("supervise: invalid evidence cursor")
		}
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	end := min(len(items), start+limit)
	page := supervise.EvidencePage{AsOf: s.anchor, Items: append([]supervise.EvidenceItem(nil), items[start:end]...)}
	if end < len(items) {
		page.NextCursor = strconv.Itoa(end)
	}
	return page, nil
}

func (s *staticSuperviseEvidence) repositoryPage(q supervise.EvidenceQuery, search bool) (supervise.EvidencePage, error) {
	page := supervise.EvidencePage{AsOf: s.anchor}
	if s.repository == nil || s.repository.session == nil || s.repository.head.IsZero() {
		return page, nil
	}
	start, err := superviseEvidenceCursor(q.Cursor, 10_000)
	if err != nil {
		return supervise.EvidencePage{}, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 20
	}
	want := start + limit + 1
	if search {
		matches, partial, searchErr := s.repository.session.SearchFilesAtHead(s.repository.head, q.Path, q.Pattern, want, 10_000, 8<<20)
		if searchErr != nil {
			return supervise.EvidencePage{}, searchErr
		}
		if start > len(matches) {
			return supervise.EvidencePage{}, errors.New("supervise: invalid repository evidence cursor")
		}
		end := min(len(matches), start+limit)
		for _, match := range matches[start:end] {
			line := "path"
			if match.Line > 0 {
				line = strconv.Itoa(match.Line)
			}
			page.Items = append(page.Items, supervise.EvidenceItem{ID: fmt.Sprintf("repository:%s:%s", match.Path, line), Section: supervise.EvidenceRepository, Summary: match.Path, Content: match.Text, Attributes: map[string]string{"line": line}})
		}
		if end < len(matches) {
			page.NextCursor = strconv.Itoa(end)
		}
		if partial {
			if len(page.Items) == 0 {
				page.Items = append(page.Items, supervise.EvidenceItem{ID: "repository:scan-limit", Section: supervise.EvidenceRepository, Summary: "Repository search reached its scan ceiling; narrow path or pattern."})
			} else {
				page.Items[len(page.Items)-1].Attributes["partial"] = "true"
			}
		}
		return page, nil
	}
	if strings.TrimSpace(q.Path) != "" {
		if start != 0 {
			return supervise.EvidencePage{}, errors.New("supervise: file reads do not use cursors")
		}
		data, readErr := s.repository.session.ReadFileAtHead(s.repository.head, q.Path, 64<<10)
		if readErr != nil {
			return supervise.EvidencePage{}, readErr
		}
		content := string(data)
		if !utf8.Valid(data) || strings.IndexByte(content, 0) >= 0 {
			content = "[binary or non-UTF-8 content omitted]"
		}
		page.Items = []supervise.EvidenceItem{{ID: "repository:" + q.Path, Section: supervise.EvidenceRepository, Summary: q.Path, Content: content}}
		return page, nil
	}
	files, more, listErr := s.repository.session.ListFilesAtHead(s.repository.head, "", want)
	if listErr != nil {
		return supervise.EvidencePage{}, listErr
	}
	if start > len(files) {
		return supervise.EvidencePage{}, errors.New("supervise: invalid repository evidence cursor")
	}
	end := min(len(files), start+limit)
	for _, name := range files[start:end] {
		page.Items = append(page.Items, supervise.EvidenceItem{ID: "repository:" + name, Section: supervise.EvidenceRepository, Summary: name})
	}
	if end < len(files) || more {
		page.NextCursor = strconv.Itoa(end)
	}
	return page, nil
}

func superviseEvidenceCursor(raw string, maxValue int) (int, error) {
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 || value > maxValue {
		return 0, errors.New("supervise: invalid evidence cursor")
	}
	return value, nil
}
