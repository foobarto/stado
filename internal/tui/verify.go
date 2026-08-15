package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/foobarto/stado/internal/runtime"
	"github.com/foobarto/stado/internal/textutil"
	"github.com/foobarto/stado/pkg/agent"
)

func (m *Model) startVerification() tea.Cmd {
	m.verifyRounds++
	m.verifying = true
	m.verifyGeneration++
	m.state = stateStreaming
	commands := strings.Join(m.verifyConfig.Commands, " -> ")
	m.appendBlock(block{kind: "system", body: fmt.Sprintf(
		"verifying (round %d/%d): %s", m.verifyRounds, m.verifyConfig.MaxRounds,
		textutil.TruncateRunes(commands, 300))})
	m.finalizeStreamingBlocks()
	m.renderBlocks()

	executor := m.executor
	host := hostAdapter{workdir: m.cwd, executorSandbox: m.executorSandbox}
	if executor != nil {
		host.readLog = executor.ReadLog
		host.runner = executor.Runner
	}
	cfg := m.verifyConfig
	round := m.verifyRounds
	generation := m.verifyGeneration
	ctx, cancel := context.WithCancel(m.rootCtx)
	m.toolMu.Lock()
	m.toolCancel = cancel
	m.toolMu.Unlock()
	return func() tea.Msg {
		defer cancel()
		return verifyResultMsg{
			outcome:    runtime.RunVerificationRound(ctx, executor, host, cfg, round, nil),
			generation: generation,
		}
	}
}

func onVerifyResult(m *Model, msg verifyResultMsg) (tea.Model, tea.Cmd) {
	if !m.verifying || msg.generation != m.verifyGeneration {
		return m, nil
	}
	m.toolMu.Lock()
	m.toolCancel = nil
	m.toolMu.Unlock()
	m.verifying = false
	out := msg.outcome
	safeOutput := textutil.TruncateRunes(textutil.SanitizeForTerminal(strings.TrimSpace(out.Output)), 4000)
	if !m.verifyEnabled {
		m.turnCancelled = false
		m.appendBlock(block{kind: "system", body: "verification disabled; completion accepted without gate"})
		return m.finishVerifiedTurn()
	}
	if errors.Is(out.Err, context.Canceled) ||
		(out.Status == runtime.VerifyInfrastructure && strings.Contains(strings.ToLower(out.Output), "context canceled")) {
		m.turnCancelled = false
		m.appendBlock(block{kind: "system", body: "verification cancelled by operator; completion left unverified"})
		if m.loop != nil {
			m.loop = nil
			m.appendBlock(block{kind: "system", body: "loop stopped - verification was cancelled"})
		}
		return m.finishVerifiedTurn()
	}
	switch out.Status {
	case runtime.VerifyPassed:
		m.appendBlock(block{kind: "system", body: fmt.Sprintf("verification passed (round %d)", out.Round)})
		return m.finishVerifiedTurn()
	case runtime.VerifyInfrastructure:
		if !m.verifyConfig.Strict {
			m.appendBlock(block{kind: "system", body: "verification unavailable; accepting completion (fail-open): " + safeOutput})
			return m.finishVerifiedTurn()
		}
		return m.finishVerificationError("verification infrastructure error (strict): " + safeOutput)
	case runtime.VerifyFailed:
		m.appendBlock(block{kind: "system", body: fmt.Sprintf(
			"verification failed (round %d/%d):\n%s", out.Round, m.verifyConfig.MaxRounds, safeOutput)})
		if err := m.setBrokerTaint(runtime.ContextTainted); err != nil {
			return m.finishVerificationError("broker taint update failed: " + err.Error())
		}
		feedback := agent.Text(agent.RoleUser, out.Feedback)
		m.msgs = append(m.msgs, feedback)
		m.persistMessage(feedback)
		if out.Round >= m.verifyConfig.MaxRounds {
			return m.finishVerificationError(fmt.Sprintf("verify_exhausted after %d round(s): %s", out.Round, safeOutput))
		}
		m.renderBlocks()
		return m, m.startStream()
	default:
		return m.finishVerificationError("verification returned unknown status: " + string(out.Status))
	}
}

func (m *Model) finishVerificationError(message string) (tea.Model, tea.Cmd) {
	if m.session != nil {
		if err := m.session.NextTurn(); err != nil {
			message += "; turn boundary failed: " + err.Error()
		}
	}
	m.verifying = false
	loopStop := m.stopLoopOnError()
	m.state = stateError
	m.errorMsg = message
	m.steeringMsg = ""
	m.restoreQueuedPromptToInput()
	m.appendBlock(block{kind: "system", body: message})
	m.finalizeStreamingBlocks()
	m.renderBlocks()
	return m, loopStop
}

func (m *Model) finishVerifiedTurn() (tea.Model, tea.Cmd) {
	cmd := m.finishTurnWithoutTools()
	if m.state == stateStreaming || m.loop == nil {
		return m, cmd
	}
	if m.loopCheckDone(m.lastAssistantText()) {
		return m, cmd
	}
	var loopCmd tea.Cmd
	if m.loop.interval > 0 {
		loopCmd = m.loopTick()
	} else {
		loopCmd = m.loopIterate()
	}
	return m, tea.Batch(cmd, loopCmd)
}

func (m *Model) handleVerifySlash(args []string) tea.Cmd {
	action := "toggle"
	if len(args) > 0 {
		action = strings.ToLower(strings.TrimSpace(args[0]))
	}
	switch action {
	case "on":
		if !m.verifyConfig.Enabled() {
			m.appendBlock(block{kind: "system", body: "verify: no commands configured in user [verify].commands"})
			return nil
		}
		m.verifyEnabled = true
	case "off":
		m.verifyEnabled = false
	case "status":
	case "toggle":
		if !m.verifyEnabled && !m.verifyConfig.Enabled() {
			m.appendBlock(block{kind: "system", body: "verify: no commands configured in user [verify].commands"})
			return nil
		}
		m.verifyEnabled = !m.verifyEnabled
	default:
		m.appendBlock(block{kind: "system", body: "verify: usage /verify [on|off|status]"})
		return nil
	}
	cancelling := false
	if !m.verifyEnabled && m.verifying {
		cancelling = m.cancelRunningTool()
	}
	state := "off"
	if m.verifyEnabled {
		state = "on"
	}
	body := fmt.Sprintf("verify: %s (%d command(s), max %d round(s), strict=%v)", state,
		len(m.verifyConfig.Commands), m.verifyConfig.MaxRounds, m.verifyConfig.Strict)
	if cancelling {
		body += "; cancelling active verification"
	}
	m.appendBlock(block{kind: "system", body: body})
	return nil
}
