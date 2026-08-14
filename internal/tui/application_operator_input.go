package tui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"

	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/pkg/agent"
)

type applicationInputAfter uint8

const (
	applicationInputAfterNone applicationInputAfter = iota
	applicationInputAfterWorkerTurn
	applicationInputAfterToolResults
	applicationInputAfterCompletion
	applicationInputAfterContinuation
)

const (
	applicationFailureInputCapture   = "operator-input:capture"
	applicationFailureInputState     = "operator-input:state"
	applicationFailureInputDelivery  = "operator-input:delivery"
	applicationFailureWorkerRecovery = "worker-run:recovery"
	applicationFailureWorkerHandoff  = "worker-run:handoff"
	applicationFailureSessionHandoff = "session:handoff"
	applicationFailureEventPoll      = "events:poll"
	applicationFailureTurnBoundary   = "turn:boundary"
)

type applicationInputCaptureMsg struct {
	requestID string
	text      string
	input     runtime.ApplicationOperatorInput
	err       error
}

type applicationInputStateMsg struct {
	after applicationInputAfter
	state runtime.ApplicationInputState
	err   error
}

type applicationInputDeliveryMsg struct {
	after        applicationInputAfter
	input        *runtime.ApplicationOperatorInput
	continuation *runtime.ApplicationContinuation
	recovery     bool
	inputIDs     []string
	messages     []agent.Message
	appended     bool
	err          error
}

func (m *Model) applicationOwnsOperatorInput() bool {
	return m.loop != nil && m.loop.application != nil
}

func (m *Model) setApplicationFailureSource(source string, err error) {
	if err == nil {
		m.clearApplicationFailureSource(source)
		return
	}
	if m.applicationFailureSources == nil {
		m.applicationFailureSources = make(map[string]error)
	}
	m.applicationFailureSources[source] = err
	m.applicationFailure = err
}

func (m *Model) clearApplicationFailureSource(source string) {
	previous := m.applicationFailureSources[source]
	delete(m.applicationFailureSources, source)
	if previous != nil && m.applicationFailure == previous {
		m.applicationFailure = m.persistentApplicationFailure()
	}
}

func (m *Model) persistentApplicationFailure() error {
	priority := []string{
		applicationFailureSessionHandoff,
		applicationFailureWorkerRecovery,
		applicationFailureWorkerHandoff,
		applicationFailureInputDelivery,
		applicationFailureInputState,
		applicationFailureInputCapture,
		applicationFailureTurnBoundary,
		applicationFailureEventPoll,
	}
	seen := make(map[string]struct{}, len(priority))
	for _, source := range priority {
		seen[source] = struct{}{}
		if err := m.applicationFailureSources[source]; err != nil {
			return err
		}
	}
	var remaining []string
	for source, err := range m.applicationFailureSources {
		if err == nil {
			continue
		}
		if _, known := seen[source]; !known {
			remaining = append(remaining, source)
		}
	}
	sort.Strings(remaining)
	if len(remaining) > 0 {
		return m.applicationFailureSources[remaining[0]]
	}
	return m.applicationAdmissionFailure
}

func (m *Model) captureApplicationOperatorInput(text string) tea.Cmd {
	controller, ok := m.broker.(runtime.ApplicationOperatorInputController)
	if !ok || controller == nil {
		m.appendBlock(block{kind: "system", body: "application input capture blocked: authenticated broker controller unavailable"})
		m.renderBlocks()
		return nil
	}
	if m.applicationInputCaptureRunning {
		m.appendBlock(block{kind: "system", body: "application input capture is still pending; the new draft remains in the editor"})
		m.renderBlocks()
		return nil
	}
	if len(text) > runtime.MaxApplicationOperatorInputBytes {
		m.appendBlock(block{kind: "system", body: fmt.Sprintf("application input capture blocked: input exceeds %d UTF-8 bytes", runtime.MaxApplicationOperatorInputBytes)})
		m.renderBlocks()
		return nil
	}
	requestID := m.applicationInputCaptureRetryID
	if requestID == "" || m.applicationInputCaptureRetryText != text {
		requestID = "operator-input:" + uuid.NewString()
	}
	m.applicationInputCaptureRetryID = requestID
	m.applicationInputCaptureRetryText = text
	m.applicationInputCaptureRunning = true
	m.applicationInputCaptureText = text
	m.input.Reset()
	return func() tea.Msg {
		input, err := controller.CaptureApplicationOperatorInput(context.Background(), text, requestID)
		return applicationInputCaptureMsg{requestID: requestID, text: text, input: input, err: err}
	}
}

func onApplicationInputCapture(m *Model, msg applicationInputCaptureMsg) (tea.Model, tea.Cmd) {
	if !m.applicationInputCaptureRunning || msg.text != m.applicationInputCaptureText || msg.requestID != m.applicationInputCaptureRetryID {
		return m, nil
	}
	m.applicationInputCaptureRunning = false
	m.applicationInputCaptureText = ""
	if msg.err != nil {
		m.input.SetValue(mergeRecoveryInput(msg.text, "", m.input.Value()))
		failure := fmt.Errorf("durable application input capture: %w", msg.err)
		m.setApplicationFailureSource(applicationFailureInputCapture, failure)
		m.appendBlock(block{kind: "system", body: failure.Error() + "; your draft was restored and was not submitted unsupervised"})
		m.renderBlocks()
		return m, nil
	}
	if msg.input.Text != msg.text || msg.input.ID == "" || msg.input.Status != "queued" {
		m.input.SetValue(mergeRecoveryInput(msg.text, "", m.input.Value()))
		failure := errors.New("durable application input capture returned a mismatched record")
		m.setApplicationFailureSource(applicationFailureInputCapture, failure)
		m.appendBlock(block{kind: "system", body: failure.Error() + "; your draft was restored"})
		m.renderBlocks()
		return m, nil
	}
	m.applicationInputCaptureRetryID = ""
	m.applicationInputCaptureRetryText = ""
	m.clearApplicationFailureSource(applicationFailureInputCapture)
	m.input.History.Push(msg.text)
	m.appendBlock(block{kind: "user", body: msg.text, queued: true, source: "application-input", deliveryID: msg.input.ID})
	m.appendBlock(block{kind: "system", body: "lifecycle application is routing the captured input"})
	m.renderBlocks()
	return m, m.reconcileApplicationOperatorInput(applicationInputAfterNone)
}

func (m *Model) reconcileApplicationOperatorInput(after applicationInputAfter) tea.Cmd {
	if m.applicationInputDeliveryRunning {
		m.rememberApplicationInputAfter(after)
		return nil
	}
	// Classification may finish while the current provider stream is still
	// producing its assistant message. Keep the routed record durable but wait
	// for the turn boundary, otherwise receiver persistence would order the new
	// user message before the assistant response it followed.
	if after == applicationInputAfterNone && m.state == stateStreaming && m.applicationOwnsOperatorInput() {
		return nil
	}
	controller, ok := m.broker.(runtime.ApplicationOperatorInputController)
	if !ok || controller == nil || m.session == nil {
		if after != applicationInputAfterNone && m.applicationOwnsOperatorInput() {
			m.rememberApplicationInputAfter(after)
			failure := errors.New("application input delivery blocked: authenticated broker controller or exact session unavailable")
			m.setApplicationFailureSource(applicationFailureInputState, failure)
			m.appendBlock(block{kind: "system", body: failure.Error()})
			m.renderBlocks()
		}
		return nil
	}
	m.applicationInputDeliveryRunning = true
	return func() tea.Msg {
		state, err := controller.ApplicationOperatorInputState(context.Background())
		return applicationInputStateMsg{after: after, state: state, err: err}
	}
}

func onApplicationInputState(m *Model, msg applicationInputStateMsg) (tea.Model, tea.Cmd) {
	m.applicationInputDeliveryRunning = false
	msg.after = m.takeApplicationInputAfter(msg.after)
	if msg.err != nil {
		m.rememberApplicationInputAfter(msg.after)
		failure := fmt.Errorf("recover durable application input state: %w", msg.err)
		m.setApplicationFailureSource(applicationFailureInputState, failure)
		m.appendBlock(block{kind: "system", body: failure.Error()})
		m.renderBlocks()
		return m, nil
	}
	controller, ok := m.broker.(runtime.ApplicationOperatorInputController)
	if !ok || controller == nil || m.session == nil {
		m.rememberApplicationInputAfter(msg.after)
		return m, nil
	}
	m.clearApplicationFailureSource(applicationFailureInputState)
	m.surfaceOpenApplicationDeferredTasks(msg.state.OpenDeferredTasks, msg.state.OpenDeferredTaskCount, msg.state.OpenDeferredTruncated)
	if len(msg.state.RecoveryInputs) > 0 {
		input := msg.state.RecoveryInputs[0]
		m.applicationInputDeliveryRunning = true
		return m, applicationInputDeliveryCmd(controller, m.session, msg.after, &input, nil, true)
	}
	if len(msg.state.ReviewingInputs) > 0 {
		// A durable async claim lets the application acknowledge the original
		// mandatory event, but it does not settle the immutable input. Preserve
		// the pending turn continuation and let the ordinary application poll
		// retry after the same binding routes every claimed record. Provider and
		// tool dispatch are independently fenced by the aggregate broker schedule.
		m.rememberApplicationInputAfter(msg.after)
		return m, nil
	}
	if len(msg.state.ReadyInputs) > 0 {
		input := msg.state.ReadyInputs[0]
		m.applicationInputDeliveryRunning = true
		return m, applicationInputDeliveryCmd(controller, m.session, msg.after, &input, nil, false)
	}
	if msg.state.PendingContinuation != nil {
		continuation := *msg.state.PendingContinuation
		m.applicationInputDeliveryRunning = true
		return m, applicationInputDeliveryCmd(controller, m.session, msg.after, nil, &continuation, false)
	}
	return m, m.resumeAfterApplicationInput(msg.after)
}

func applicationInputDeliveryCmd(controller runtime.ApplicationOperatorInputController, session *stadogit.Session, after applicationInputAfter, input *runtime.ApplicationOperatorInput, continuation *runtime.ApplicationContinuation, recovery bool) tea.Cmd {
	return func() tea.Msg {
		result := applicationInputDeliveryMsg{after: after, recovery: recovery}
		if input != nil {
			copy := *input
			result.input = &copy
			result.inputIDs = []string{input.ID}
			result.messages = []agent.Message{agent.Text(agent.RoleUser, input.Text)}
			result.appended, result.err = runtime.AppendConversationMessageDelivery(session, input.ID, []string{input.Digest}, result.messages)
			if result.err == nil {
				committed, err := controller.CommitApplicationOperatorInput(context.Background(), *input, recovery)
				result.input, result.err = &committed, err
			}
			return result
		}
		if continuation == nil || continuation.DeliveryID == "" || len(continuation.Inputs) == 0 || len(continuation.Inputs) != len(continuation.InputIDs) {
			result.err = errors.New("application continuation projection is incomplete")
			return result
		}
		copy := *continuation
		result.continuation = &copy
		digests := make([]string, 0, len(continuation.Inputs))
		for i, item := range continuation.Inputs {
			if item.ID != continuation.InputIDs[i] {
				result.err = errors.New("application continuation order mismatch")
				return result
			}
			result.inputIDs = append(result.inputIDs, item.ID)
			result.messages = append(result.messages, agent.Text(agent.RoleUser, item.Text))
			digests = append(digests, item.Digest)
		}
		result.appended, result.err = runtime.AppendConversationMessageDelivery(session, continuation.DeliveryID, digests, result.messages)
		if result.err == nil {
			committed, err := controller.CommitApplicationContinuation(context.Background(), *continuation)
			result.continuation, result.err = &committed, err
		}
		return result
	}
}

func onApplicationInputDelivery(m *Model, msg applicationInputDeliveryMsg) (tea.Model, tea.Cmd) {
	m.applicationInputDeliveryRunning = false
	msg.after = m.takeApplicationInputAfter(msg.after)
	if msg.appended {
		for i, message := range msg.messages {
			m.msgs = append(m.msgs, message)
			text, _ := plainApplicationUserText(message)
			inputID := ""
			if i < len(msg.inputIDs) {
				inputID = msg.inputIDs[i]
			}
			if !m.resolveApplicationInputBlock(inputID, text, msg.recovery) {
				source := "operator"
				if msg.recovery {
					source = "application-recovery"
				}
				m.appendBlock(block{kind: "user", body: text, source: source, deliveryID: inputID})
			}
		}
		m.renderBlocks()
	}
	if msg.err != nil {
		m.rememberApplicationInputAfter(msg.after)
		failure := fmt.Errorf("durable application input delivery: %w", msg.err)
		m.setApplicationFailureSource(applicationFailureInputDelivery, failure)
		m.appendBlock(block{kind: "system", body: failure.Error() + "; provider dispatch remains blocked until retry"})
		m.renderBlocks()
		return m, nil
	}
	if (msg.input != nil && msg.input.Status != "delivered") || (msg.continuation != nil && msg.continuation.Status != "delivered") {
		m.rememberApplicationInputAfter(msg.after)
		failure := errors.New("durable application input delivery returned a non-delivered record")
		m.setApplicationFailureSource(applicationFailureInputDelivery, failure)
		m.appendBlock(block{kind: "system", body: failure.Error()})
		m.renderBlocks()
		return m, nil
	}
	m.clearApplicationFailureSource(applicationFailureInputDelivery)
	if msg.recovery {
		m.appendBlock(block{kind: "system", body: "application worker ended; captured operator input was recovered into the exact session"})
		m.renderBlocks()
	}
	nextAfter := msg.after
	if msg.continuation != nil {
		nextAfter = applicationInputAfterContinuation
	}
	return m, m.reconcileApplicationOperatorInput(nextAfter)
}

func (m *Model) surfaceOpenApplicationDeferredTasks(tasks []runtime.ApplicationDeferredTask, count int, truncated bool) {
	if count == 0 {
		m.applicationDeferredTaskNotice = ""
		return
	}
	ids := make([]string, 0, len(tasks))
	titles := make([]string, 0, min(len(tasks), 3))
	for _, task := range tasks {
		ids = append(ids, task.ID+":"+task.Status)
		if len(titles) < 3 {
			titles = append(titles, task.Title)
		}
	}
	noticeID := fmt.Sprintf("%d:%t:%s", count, truncated, strings.Join(ids, "\x00"))
	if noticeID == m.applicationDeferredTaskNotice {
		return
	}
	m.applicationDeferredTaskNotice = noticeID
	body := fmt.Sprintf("lifecycle application has %d open deferred operator task(s) in the durable broker projection", count)
	if len(titles) > 0 {
		body += ": " + strings.Join(titles, "; ")
		if count > len(titles) {
			body += fmt.Sprintf("; and %d more", count-len(titles))
		}
	}
	m.appendBlock(block{kind: "system", body: body})
	m.renderBlocks()
}

func (m *Model) resolveApplicationInputBlock(inputID, text string, recovery bool) bool {
	for i := range m.blocks {
		if m.blocks[i].deliveryID != inputID || m.blocks[i].kind != "user" {
			continue
		}
		m.blocks[i].queued = false
		m.blocks[i].source = "operator"
		if recovery {
			m.blocks[i].source = "application-recovery"
		}
		return true
	}
	return false
}

func (m *Model) resumeAfterApplicationInput(after applicationInputAfter) tea.Cmd {
	switch after {
	case applicationInputAfterWorkerTurn:
		if m.applicationOwnsOperatorInput() {
			return m.loopIterate()
		}
	case applicationInputAfterToolResults:
		if m.applicationOwnsOperatorInput() {
			return m.startStream()
		}
	case applicationInputAfterContinuation:
		return m.startStream()
	case applicationInputAfterCompletion:
		if m.queuedPrompt != "" {
			return m.promoteQueuedPrompt()
		}
	}
	return nil
}

func (m *Model) rememberApplicationInputAfter(after applicationInputAfter) {
	if after != applicationInputAfterNone && m.applicationInputPendingAfter == applicationInputAfterNone {
		m.applicationInputPendingAfter = after
	}
}

func (m *Model) takeApplicationInputAfter(current applicationInputAfter) applicationInputAfter {
	if current == applicationInputAfterNone && m.applicationInputPendingAfter != applicationInputAfterNone {
		current = m.applicationInputPendingAfter
	}
	m.applicationInputPendingAfter = applicationInputAfterNone
	return current
}

func plainApplicationUserText(message agent.Message) (string, bool) {
	if message.Role != agent.RoleUser || len(message.Content) != 1 || message.Content[0].Text == nil {
		return "", false
	}
	return message.Content[0].Text.Text, true
}
