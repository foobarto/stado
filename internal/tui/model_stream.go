package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/foobarto/stado/internal/compact"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/internal/instructions"
	"github.com/foobarto/stado/internal/personas"
	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/stateprompt"
	"github.com/foobarto/stado/internal/streambudget"
	"github.com/foobarto/stado/internal/subagent"
	"github.com/foobarto/stado/internal/textutil"
	"github.com/foobarto/stado/internal/trajectory"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/foobarto/stado/pkg/tool"
	"github.com/go-git/go-git/v5/plumbing"
)

func (m *Model) turnSystemPrompt(userPrompt string) string {
	system, _, _ := m.turnSystemPromptWithQualityFacts(userPrompt)
	return system
}

// turnSystemPromptWithQualityFacts composes the ordinary prompt and returns
// the two volatile, explicitly untrusted observations made available to a
// TUI-hosted append-only lifecycle contributor (EP-0060/0064). Other surfaces
// do not call this seam and therefore do not imply lifecycle-app parity.
func (m *Model) turnSystemPromptWithQualityFacts(userPrompt string) (string, string, string) {
	sessionID := ""
	if m.session != nil {
		sessionID = m.session.ID
	}
	state, _ := stateprompt.Build(m.cfg.StateDir(), sessionID)
	var sys string
	if m.persona != nil {
		sys = personas.AssembleSystem(m.persona, m.systemPrompt, "", state)
	} else {
		sys = instructions.ComposeSystemPrompt(m.systemPromptTemplate, m.systemPrompt, instructions.RuntimeContext{
			Provider: m.providerDisplayName(),
			Model:    m.model,
		})
		if strings.TrimSpace(state) != "" {
			sys = strings.TrimSpace(sys) + "\n\n" + strings.TrimSpace(state)
		}
	}
	return sys, userPrompt, state
}

func latestUserPrompt(msgs []agent.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != agent.RoleUser {
			continue
		}
		var parts []string
		for _, b := range msgs[i].Content {
			if b.Text != nil {
				parts = append(parts, b.Text.Text)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func (m *Model) appendUser(text string) {
	m.maybeAutoTitleSession(text)
	msg := agent.Text(agent.RoleUser, text)
	m.blocks = append(m.blocks, block{kind: "user", body: text})
	m.msgs = append(m.msgs, msg)
	m.persistMessage(msg)
}

func (m *Model) appendBlock(b block) {
	m.blocks = append(m.blocks, b)
}

// injectStartupNotices renders the launch-time banner — sandbox posture,
// broker session, writable paths, and any startup warnings the caller and
// Run collected — as one system block. Without this the alt-screen TUI
// swallows what the CLI entry points print to stderr before the program
// takes the screen (see internal/sandbox.HostUnsandboxedLines and
// cmd/stado's BrokerSession.AnnounceSandboxMode). No-op when empty.
//
// The block is marked startup:true so it doesn't suppress the empty-session
// landing screen (hasRealBlocks ignores it); the landing screen renders the
// banner above its footer, and active/resumed sessions show it in scrollback.
func (m *Model) injectStartupNotices(notices []string) {
	if len(notices) == 0 {
		return
	}
	m.appendBlock(block{kind: "system", body: strings.Join(notices, "\n"), startup: true})
}

// hasRealBlocks reports whether any block is real conversation/activity
// rather than the launch-time startup banner. Drives View()'s landing
// check so the banner alone doesn't replace the welcome screen.
func (m *Model) hasRealBlocks() bool {
	for i := range m.blocks {
		if !m.blocks[i].startup {
			return true
		}
	}
	return false
}

// startupBannerText returns the joined body of the startup banner block(s),
// or "" if none — the landing screen renders this above its footer.
func (m *Model) startupBannerText() string {
	var parts []string
	for i := range m.blocks {
		if m.blocks[i].startup {
			parts = append(parts, m.blocks[i].body)
		}
	}
	return strings.Join(parts, "\n")
}

const autoSessionTitleMaxRunes = 48

func (m *Model) maybeAutoTitleSession(text string) {
	if m.session == nil || m.session.WorktreePath == "" {
		return
	}
	if runtime.ReadDescription(m.session.WorktreePath) != "" {
		return
	}
	for _, msg := range m.msgs {
		if msg.Role == agent.RoleUser {
			return
		}
	}
	title := autoSessionTitle(text)
	if title == "" {
		return
	}
	_ = runtime.WriteDescription(m.session.WorktreePath, title)
}

func autoSessionTitle(text string) string {
	title := textutil.StripControlChars(text)
	title = strings.Trim(strings.Join(strings.Fields(title), " "), "\"'` ")
	if title == "" {
		return ""
	}
	runes := []rune(title)
	if len(runes) <= autoSessionTitleMaxRunes {
		return title
	}
	title = strings.TrimRight(string(runes[:autoSessionTitleMaxRunes]), " .,;:-")
	if title == "" {
		return ""
	}
	return title + "..."
}

// persistMessage append-writes msg to this session's conversation
// log so a future `stado` boot under the same worktree can resume
// the conversation. Best-effort: a disk error degrades resume but
// shouldn't interrupt the live session, so we swallow errors here
// (they already log through slog via OpenSession's OnCommit).
func (m *Model) persistMessage(msg agent.Message) {
	if m.session == nil {
		return
	}
	_ = runtime.AppendMessage(m.session.WorktreePath, msg)
}

// LoadPersistedConversation seeds m.msgs + m.blocks from whatever
// `runtime.LoadConversation` finds under the session's worktree. No-op
// when the conversation file is absent (fresh session) or the session
// itself is nil (test harness). Callers invoke this once at TUI boot,
// after the session is wired but before the first user input.
//
// Text and thinking blocks are recreated faithfully. Tool-use /
// tool-result / image blocks are summarised with placeholder tags since
// the live execution state is not present on replay. The user sees the
// prior conversation without losing the m.msgs LLM-side prompt prefix.
func (m *Model) LoadPersistedConversation() {
	if m.session == nil {
		return
	}
	loaded, err := runtime.LoadConversation(m.session.WorktreePath)
	if err != nil || len(loaded) == 0 {
		return
	}
	m.msgs = loaded
	m.blocks = append(m.blocks, msgsToBlocks(loaded)...)
	m.appendBlock(block{
		kind: "system",
		body: fmt.Sprintf("resumed session — %d prior message(s) loaded from disk.", len(loaded)),
	})
}

// msgsToBlocks renders a persisted message slice into the TUI's
// block model so the user sees the prior conversation on resume. Text-like
// content is grouped per role, while provider-native thinking is restored as
// separate thinking blocks so display modes still apply after restart.
func msgsToBlocks(msgs []agent.Message) []block {
	out := make([]block, 0, len(msgs))
	for _, msg := range msgs {
		var body string
		kind := "assistant"
		switch msg.Role {
		case agent.RoleUser:
			kind = "user"
		case agent.RoleTool:
			kind = "tool"
		}
		appendLine := func(s string) {
			if body != "" {
				body += "\n"
			}
			body += s
		}
		flush := func() {
			if body == "" {
				return
			}
			out = append(out, block{kind: kind, body: body})
			body = ""
		}
		for _, b := range msg.Content {
			switch {
			case b.Text != nil:
				appendLine(b.Text.Text)
			case b.Thinking != nil:
				flush()
				thinking := b.Thinking.Text
				if strings.TrimSpace(thinking) == "" {
					thinking = "[thinking]"
				}
				out = append(out, block{kind: "thinking", body: thinking})
			case b.ToolUse != nil:
				appendLine("[tool_use " + b.ToolUse.Name + "]")
			case b.ToolResult != nil:
				appendLine("[tool_result]")
			case b.Image != nil:
				appendLine("[image]")
			}
		}
		flush()
	}
	return out
}

// startBtw fires an async BTW query: a StreamTurn that does NOT mutate
// m.msgs.  The conversation history is snapshotted for context; the
// reply is rendered as a "btw" block when it arrives via btwResultMsg.
//
// EP-0033 #098: BTW is the supervisor lane today. When [supervisor]
// is configured, [resolveSupervisorLane] redirects this call to the
// supervisor provider so the transcript doesn't leak to the worker
// endpoint. Lookup failure surfaces as a btw result error rather than
// silent fallback to the worker — silent fallback would defeat the
// documented trust boundary.
func (m *Model) startBtw(question string) tea.Cmd {
	if !m.ensureProvider() {
		return nil
	}

	// Show the user's question immediately as a btw block — happens
	// regardless of supervisor-lane resolution success, so the operator
	// sees their input echoed even when the dispatch fails.
	m.appendBlock(block{kind: "btw", body: question + "\n"})
	m.renderBlocks()

	supProvider, supModel, supErr := resolveSupervisorLane(m.cfg, m.provider, m.model, m.cachedSupervisorLookup)
	if supErr != nil {
		return func() tea.Msg {
			return btwResultMsg{question: question, errMsg: supErr.Error()}
		}
	}

	// Snapshot the conversation for context.  Keep all prior messages
	// (including system/tool) — the model needs enough context to answer.
	msgs := make([]agent.Message, len(m.msgs))
	copy(msgs, m.msgs)
	msgs = append(msgs, agent.Text(agent.RoleUser, question))

	// Build non-mutating tool set (same as Plan mode).
	var tools []agent.ToolDef
	if m.executor != nil {
		for _, t := range m.executor.Registry.All() {
			name := t.Name()
			if m.executor.Registry.ClassOf(name) != tool.ClassNonMutating {
				continue
			}
			schema, _ := json.Marshal(t.Schema())
			tools = append(tools, agent.ToolDef{
				Name:        name,
				Description: t.Description(),
				Schema:      schema,
			})
		}
	}

	go func() {
		ctx, cancel := context.WithTimeout(m.rootCtx, 120*time.Second)
		defer cancel()

		req := agent.TurnRequest{
			Model:    supModel,
			System:   m.turnSystemPrompt(question),
			Messages: msgs,
			Tools:    tools,
		}
		if supProvider.Capabilities().SupportsPromptCache && len(msgs) > 1 {
			req.CacheHints = []agent.CachePoint{{MessageIndex: len(msgs) - 2}}
		}

		ch, err := supProvider.StreamTurn(ctx, req)
		if err != nil {
			m.sendMsg(btwResultMsg{question: question, errMsg: err.Error()})
			return
		}

		var reply strings.Builder
		for ev := range ch {
			switch ev.Kind {
			case agent.EvTextDelta:
				if err := streambudget.CheckAppend("btw reply", reply.Len(), len(ev.Text), streambudget.MaxAssistantTextBytes); err != nil {
					m.sendMsg(btwResultMsg{question: question, errMsg: err.Error()})
					return
				}
				reply.WriteString(ev.Text)
			case agent.EvError:
				if ev.Err != nil {
					m.sendMsg(btwResultMsg{question: question, errMsg: ev.Err.Error()})
					return
				}
			case agent.EvDone:
				goto done
			}
		}
	done:
		m.sendMsg(btwResultMsg{question: question, reply: reply.String()})
	}()
	return nil
}

// startStream fires a non-interactive streaming call to the provider and
// relays events back to the UI via tea.Program.Send.
func (m *Model) startStream() tea.Cmd {
	done := tuiTraceCall("tui.startStream",
		"provider", m.providerDisplayName(),
		"model", m.model,
		"messages", len(m.msgs))
	defer done("state", int(m.state))
	if m.applicationFailure != nil {
		m.state = stateIdle
		m.appendBlock(block{kind: "system", body: "turn blocked by fail-closed lifecycle application: " + m.applicationFailure.Error()})
		m.renderBlocks()
		return nil
	}
	if err := runtime.CheckScheduling(m.rootCtx, m.broker); err != nil {
		m.state = stateIdle
		m.appendBlock(block{kind: "system", body: "turn blocked by broker scheduling: " + err.Error()})
		m.renderBlocks()
		return nil
	}
	if !m.ensureProvider() {
		return nil
	}

	// First-turn capability probe (DESIGN §"Token accounting"). A
	// provider that doesn't satisfy TokenCounter means we can't see
	// how close we are to the context window — surface this as a
	// system message so the user knows the context % is unreliable.
	// No hard-block: the compaction recovery path lands in PR D; until
	// then a loud advisory is the best we can do.
	if !m.tokenCounterChecked {
		m.tokenCounterChecked = true
		_, m.tokenCounterPresent = m.provider.(agent.TokenCounter)
		if !m.tokenCounterPresent {
			m.appendBlock(block{
				kind: "system",
				body: fmt.Sprintf("warning: provider %q doesn't expose a token counter — context-window percentage will be zero until the provider returns usage.",
					m.providerDisplayName()),
			})
		}
	}

	// #19: proactively warn once when the context is filling up, before
	// the turn that might tip it over the hard limit.
	m.maybeEmitContextWarning()

	// Reset per-turn accumulators.
	m.turnText = ""
	m.turnThinking = ""
	m.turnThinkSig = ""
	m.turnToolCalls = nil
	m.turnUsage = agent.Usage{}
	m.turnTreeBefore = plumbing.ZeroHash
	if len(m.lifecycleApplications) > 0 && m.session != nil {
		head, err := m.session.TreeHead()
		if err != nil {
			m.state = stateError
			m.errorMsg = err.Error()
			m.appendBlock(block{kind: "system", body: "application turn anchor failed: " + err.Error()})
			m.renderBlocks()
			return nil
		}
		m.turnTreeBefore = head
	}
	// Codex validated finding (post-#46): clear turnCancelled at
	// the start of every new operator-initiated turn. The actual
	// gate lives in onToolsExecuted (refuses to startStream when
	// set); clearing here ensures a fresh turn isn't pre-aborted
	// by a stale flag from the previous cancelled turn.
	m.turnCancelled = false
	m.turnMode = m.mode
	m.turnModel = m.model
	m.turnProvider = m.providerDisplayName()

	// Span ancestor is m.rootCtx (Background or a cross-process
	// traceparent-enriched context — see Phase 9.4/9.5), so turns
	// inside a forked session link back to the parent's trace tree.
	ctx, cancel := context.WithCancel(m.rootCtx)
	m.streamMu.Lock()
	m.streamCancel = cancel
	m.state = stateStreaming
	m.errorMsg = ""
	m.turnStart = time.Now()
	m.streamMu.Unlock()

	turnTools := m.toolSurfaceForTurn()
	systemPrompt, currentInput, fastContext := m.turnSystemPromptWithQualityFacts(latestUserPrompt(m.msgs))
	req := agent.TurnRequest{
		Model:    m.model,
		Messages: m.msgs,
		Tools:    toolDefsForSurface(turnTools),
		System:   systemPrompt,
		// EP-0036: sampling from config [sampling] section (nil-safe).
		Temperature: func() *float64 {
			if m.cfg != nil {
				return m.cfg.Sampling.Temperature
			}
			return nil
		}(),
		TopP: func() *float64 {
			if m.cfg != nil {
				return m.cfg.Sampling.TopP
			}
			return nil
		}(),
		TopK: func() *int {
			if m.cfg != nil {
				return m.cfg.Sampling.TopK
			}
			return nil
		}(),
	}
	m.turnAllowed = make(map[string]struct{}, len(req.Tools))
	m.turnToolInstances = make(map[string]tool.Tool, len(turnTools))
	m.turnApplicationToolProjectionGeneration = m.applicationToolProjectionGeneration.Load()
	for _, t := range req.Tools {
		m.turnAllowed[t.Name] = struct{}{}
	}
	for _, candidate := range turnTools {
		m.turnToolInstances[candidate.Name()] = candidate
	}
	// Cache-breakpoint placement — DESIGN §"Prompt-cache awareness".
	// One ephemeral breakpoint on the last prior message, so every turn
	// caches the entire history up through the previous turn.
	if m.provider.Capabilities().SupportsPromptCache && len(m.msgs) > 0 {
		req.CacheHints = []agent.CachePoint{{MessageIndex: len(m.msgs) - 1}}
	}

	// pre_llm lifecycle hook seam (F1). The interactive TUI streams the
	// provider call directly (it does NOT go through runtime.AgentLoop), so
	// the agentloop's pre_llm point isn't live here — this is where it
	// fires for interactive turns, mirroring agentloop.go semantics:
	//   - Deny  → abort this turn before any provider call. The reason
	//     surfaces as a system block; we tear down the streaming state set
	//     up above and return idle (no StreamTurn).
	//   - Mutate → rewrite the system prompt and/or model the provider
	//     receives. Message history is intentionally NOT mutable here
	//     (append-only / prompt-cache invariant) — system+model knobs only.
	if m.lifecycleHooks.HasPoint(hooks.PointPreLLM) {
		pre := hooks.PreLLMWithQualityFacts(len(m.msgs), req.Model, req.System, len(req.Messages), len(req.Tools), currentInput, fastContext)
		hookContext := m.rootCtx
		if hookContext == nil {
			hookContext = context.Background()
		}
		// Registry and session-surface imports must observe the exact live TUI
		// ceiling during the callback. The controller remains native; WASM sees
		// only the bounded catalog projection (EP-0066).
		hookContext = tool.WithToolSurfaceController(hookContext, newModelToolSurfaceController(m))
		decision, out := m.lifecycleHooks.Fire(hookContext, hooks.PointPreLLM, pre)
		switch decision.Decision {
		case hooks.DecisionDeny:
			cancel()
			m.streamMu.Lock()
			m.streamCancel = nil
			m.state = stateIdle
			m.streamMu.Unlock()
			m.appendBlock(block{kind: "system", body: "turn denied by pre_llm hook: " + decision.Reason})
			m.renderBlocks()
			return nil
		case hooks.DecisionMutate:
			if mp, ok := out.(*hooks.PreLLMPayload); ok {
				req.System = mp.System
				req.Model = mp.Model
			}
		}
	}
	// Keep the per-turn snapshot aligned with the request actually sent after
	// pre_llm routing. Footers and usage metrics must not charge the configured
	// model when a hook selected another one.
	m.turnModel = req.Model

	// Shared stream buffer — the stream goroutine appends events
	// here under m.streamBufMu; the tea.Tick-driven flush reads them
	// out in batches on the main loop. This decouples the stream's
	// ingestion rate from bubbletea's unbuffered program channel
	// so KeyMsgs never get starved by reasoning-model delta bursts.
	m.streamBufMu.Lock()
	m.streamBuf = m.streamBuf[:0]
	m.streamBufClosed = false
	m.streamBufMu.Unlock()

	go func() {
		defer cancel()
		tuiTrace("provider stream start", "provider", m.turnProvider, "model", m.turnModel)
		ch, err := m.provider.StreamTurn(ctx, req)
		if err != nil {
			tuiTrace("provider stream error", "error", err.Error())
			m.sendMsg(streamErrorMsg{err: err})
			return
		}
		first := true
		var textBytes int
		var thinkingBytes int
		for ev := range ch {
			if first {
				first = false
				tuiTrace("provider stream first event", "kind", int(ev.Kind))
			}
			switch ev.Kind {
			case agent.EvTextDelta:
				if err := streambudget.CheckAppend("assistant text", textBytes, len(ev.Text), streambudget.MaxAssistantTextBytes); err != nil {
					m.sendMsg(streamErrorMsg{err: err})
					return
				}
				textBytes += len(ev.Text)
			case agent.EvThinkingDelta:
				if err := streambudget.CheckAppend("assistant thinking", thinkingBytes, len(ev.Text), streambudget.MaxThinkingTextBytes); err != nil {
					m.sendMsg(streamErrorMsg{err: err})
					return
				}
				thinkingBytes += len(ev.Text)
			}
			m.streamBufMu.Lock()
			m.streamBuf = append(m.streamBuf, ev)
			m.streamBufMu.Unlock()
			if ev.Kind == agent.EvDone || ev.Kind == agent.EvError {
				tuiTrace("provider stream terminal event", "kind", int(ev.Kind))
				break
			}
		}
		m.streamBufMu.Lock()
		m.streamBufClosed = true
		m.streamBufMu.Unlock()
		tuiTrace("provider stream closed")
	}()
	return streamTickCmd()
}

func streamTickCmd() tea.Cmd {
	return tea.Tick(50*time.Millisecond, func(time.Time) tea.Msg {
		return streamTickMsg{}
	})
}

// toolTickCmd reschedules itself every 250ms while a tool is running
// so the elapsed-time pill in the tool block updates live.
func (m *Model) toolTickCmd() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg {
		return toolTickMsg{}
	})
}

func (m *Model) sendMsg(msg tea.Msg) {
	if m.program != nil {
		m.program.Send(msg)
	}
}

func (m *Model) clearQueuedUserBlock(remove bool) {
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind != "user" || !m.blocks[i].queued {
			continue
		}
		if remove {
			m.blocks = append(m.blocks[:i], m.blocks[i+1:]...)
			return
		}
		m.blocks[i].queued = false
		m.invalidateBlockCache(i)
		return
	}
}

func (m *Model) promoteQueuedPrompt() tea.Cmd {
	if m.queuedPrompt == "" {
		return nil
	}
	queued := m.queuedPrompt
	if strings.HasPrefix(queued, "/") {
		m.queuedPrompt = ""
		m.clearQueuedUserBlock(false)
		return m.handleSlash(queued)
	}
	if err := m.setBrokerTaint(runtime.ContextClean); err != nil {
		m.restoreQueuedPromptToInput()
		m.appendBlock(block{kind: "system", body: "broker taint reset failed: " + err.Error()})
		m.renderBlocks()
		return nil
	}
	m.queuedPrompt = ""
	// Unmark only after the broker accepted the operator boundary. Until then,
	// the queued prompt remains recoverable without mutating the transcript.
	m.clearQueuedUserBlock(false)
	m.maybeAutoTitleSession(queued)
	msg := agent.Text(agent.RoleUser, queued)
	m.msgs = append(m.msgs, msg)
	m.persistMessage(msg)
	tuiTrace("queued prompt promoted", "chars", len(queued))
	return m.startStream()
}

func (m *Model) setBrokerTaint(state runtime.ContextTaint) error {
	if m.broker != nil {
		if err := m.broker.SetTaint(m.rootCtx, state); err != nil {
			return err
		}
	}
	if state == runtime.ContextClean {
		m.verifyRounds = 0
	}
	return nil
}

func (m *Model) restoreQueuedPromptToInput() string {
	if m.queuedPrompt == "" {
		return ""
	}
	queued := m.queuedPrompt
	m.queuedPrompt = ""
	m.clearQueuedUserBlock(true)
	draft := m.input.Value()
	restored := queued
	if draft != "" {
		restored += "\n" + draft
	}
	m.input.SetValue(restored)
	tuiTrace("queued prompt restored to input", "chars", len(restored))
	return restored
}

func (m *Model) requestPluginApproval(ctx context.Context, title, body string) (bool, error) {
	if m.program == nil {
		return false, errors.New("approval UI unavailable")
	}
	resp := make(chan bool, 1)
	m.sendMsg(pluginApprovalRequestMsg{
		title:    title,
		body:     body,
		response: resp,
	})
	select {
	case allow := <-resp:
		return allow, nil
	case <-ctx.Done():
		m.sendMsg(pluginApprovalCancelMsg{response: resp})
		return false, ctx.Err()
	}
}

// requestPluginChoice posts a stado_ui_choose request to the TUI loop
// and blocks until the operator answers, cancels, or ctx fires.
// Single-flight enforcement (rejecting concurrent requests) lives in
// the Update handler — see pluginChoiceRequestMsg case. Q3.
//
// F10: per-option Input fields are not supported in multi-select
// mode (the UX of typing into N rows with N input fields is unsolved
// and the spec doesn't address it). The bridge rejects the combo
// here before the modal opens so plugins see a clean structured
// error rather than a half-applied response.
func (m *Model) requestPluginChoice(ctx context.Context, req pluginRuntime.ChoiceRequest) (pluginRuntime.ChoiceResponse, error) {
	// Shape validation runs first (pure logic, no model dependency)
	// so plugins get a precise error rather than the channel-
	// unavailable fallback when the request was already malformed.
	if req.Multi {
		for _, o := range req.Options {
			if o.Input != nil {
				return pluginRuntime.ChoiceResponse{}, errors.New("multi-select choice with per-option input fields is not supported")
			}
		}
	}
	if m.program == nil {
		return pluginRuntime.ChoiceResponse{}, errors.New("choice UI unavailable")
	}
	resp := make(chan pluginRuntime.ChoiceResponse, 1)
	m.sendMsg(pluginChoiceRequestMsg{req: req, response: resp})
	select {
	case r := <-resp:
		return r, nil
	case <-ctx.Done():
		m.sendMsg(pluginChoiceCancelMsg{response: resp})
		return pluginRuntime.ChoiceResponse{Cancelled: true}, ctx.Err()
	}
}

func (m *Model) handleStreamEvent(ev agent.Event) {
	// Drop stray events that arrived after the stream was cancelled
	// (e.g. /clear pressed mid-stream). Compaction state has its own
	// required flow so don't gate it.
	if m.state != stateStreaming && !m.compacting &&
		ev.Kind != agent.EvDone && ev.Kind != agent.EvError {
		return
	}
	switch ev.Kind {
	case agent.EvDone:
		if ev.Usage != nil {
			m.turnUsage = *ev.Usage
			m.metrics.RecordTurnUsage(m.rootCtx, m.turnProvider, m.turnModel,
				ev.Usage.InputTokens, ev.Usage.OutputTokens, ev.Usage.CacheReadTokens)
			m.usage.InputTokens = ev.Usage.InputTokens
			m.cumulativeInputTokens += ev.Usage.InputTokens
			m.usage.OutputTokens += ev.Usage.OutputTokens
			m.usage.CostUSD += ev.Usage.CostUSD
		}
		m.attachTurnFooter(ev.Usage)

	case agent.EvError:
		if ev.Err == nil {
			return
		}
		m.state = stateError
		m.errorMsg = ev.Err.Error()
		m.appendBlock(block{kind: "system", body: "error: " + ev.Err.Error()})

	case agent.EvTextDelta:
		// Compaction streams go into the pending-summary buffer AND the
		// assistant block the caller pre-appended — the user sees the
		// summary materialise, and resolveCompaction has the full text
		// when they accept.
		currentTextBytes := len(m.turnText)
		if m.compacting {
			currentTextBytes = len(m.pendingCompactionSummary)
		}
		if err := streambudget.CheckAppend("assistant text", currentTextBytes, len(ev.Text), streambudget.MaxAssistantTextBytes); err != nil {
			m.failStreamBudget(err)
			return
		}
		if m.compacting {
			m.pendingCompactionSummary += ev.Text
			if len(m.blocks) > 0 && m.blocks[len(m.blocks)-1].kind == "assistant" {
				last := len(m.blocks) - 1
				m.blocks[last].body += textutil.SanitizeForTerminal(ev.Text)
				m.invalidateBlockCache(last)
			}
			return
		}
		m.turnText += ev.Text
		if len(m.blocks) == 0 || m.blocks[len(m.blocks)-1].kind != "assistant" {
			// The model moved on from thinking to its answer — the trailing
			// thinking block is done (auto/collapsed modes collapse it now).
			m.finalizeStreamingThinking()
			m.blocks = append(m.blocks, block{kind: "assistant"})
		}
		last := len(m.blocks) - 1
		m.blocks[last].body += textutil.SanitizeForTerminal(ev.Text)
		m.invalidateBlockCache(last)

	case agent.EvThinkingDelta:
		// Sanitize before the budget guard — the thinking stream is
		// model-prose from the same trust boundary as EvTextDelta
		// (sanitized at lines 632 + 642 above). PR #49's sibling-miss
		// audit (Codex G3/J-a P0) flagged this case as the only one
		// in the EvX switch that appended raw `ev.Text` into the
		// block body, so OSC52 / OSC8 / CSI escapes in the thinking
		// trace reached the renderer unchecked. Sanitize at both the
		// per-turn accumulator (used for in-flight budget +
		// final-trace recording) and the rendered block body.
		//
		// Codex P2 round 1: the budget guard must count what's
		// actually stored (sanitized) — not what arrived (raw) — or
		// an escape-heavy stream gets falsely rejected (current
		// counted on sanitized, delta on raw; the guard ratchets
		// faster than the stored content grows). Hence sanitize
		// first, then `CheckAppend` against sanitized lengths. The
		// signature is not text and stays unsanitized.
		sanitizedThinking := textutil.SanitizeForTerminal(ev.Text)
		if err := streambudget.CheckAppend("assistant thinking", len(m.turnThinking), len(sanitizedThinking), streambudget.MaxThinkingTextBytes); err != nil {
			m.failStreamBudget(err)
			return
		}
		m.turnThinking += sanitizedThinking
		m.turnThinkSig += ev.ThinkingSig
		if sanitizedThinking != "" {
			if len(m.blocks) == 0 || m.blocks[len(m.blocks)-1].kind != "thinking" {
				m.blocks = append(m.blocks, block{kind: "thinking", streaming: true})
			}
			last := len(m.blocks) - 1
			m.blocks[last].body += sanitizedThinking
			m.invalidateBlockCache(last)
		}

	case agent.EvToolCallStart:
		if ev.ToolCall == nil {
			return
		}
		// A tool call after thinking ends the thinking block.
		m.finalizeStreamingThinking()
		m.blocks = append(m.blocks, block{
			kind:   "tool",
			toolID: ev.ToolCall.ID,
			// Sanitize model-supplied tool name + args (same hostile-bytes
			// trust boundary as the tool result and assistant/thinking text):
			// both render raw into message_tool.tmpl, so an OSC/BEL here would
			// rewrite the terminal title, inject a hyperlink, or ring the bell.
			toolName:  textutil.SanitizeForTerminal(ev.ToolCall.Name),
			startedAt: time.Now(),
			// streaming until the result arrives (onToolResult); auto mode
			// renders the running tool full, then collapses it once done.
			streaming: true,
		})

	case agent.EvToolCallArgsDelta:
		if len(m.blocks) == 0 {
			return
		}
		last := &m.blocks[len(m.blocks)-1]
		if last.kind == "tool" {
			// Sanitize each streamed args fragment (SanitizeForTerminal strips
			// control bytes byte-wise, so an escape split across deltas is still
			// neutralized); EvToolCallEnd re-sanitizes the assembled args.
			last.toolArgs += textutil.SanitizeForTerminal(ev.ToolArgsDelta)
			m.invalidateBlockCache(len(m.blocks) - 1)
		}

	case agent.EvToolCallEnd:
		if ev.ToolCall == nil {
			return
		}
		cp := *ev.ToolCall
		m.turnToolCalls = append(m.turnToolCalls, cp)
		for i := len(m.blocks) - 1; i >= 0; i-- {
			if m.blocks[i].kind == "tool" && m.blocks[i].toolID == ev.ToolCall.ID {
				m.blocks[i].toolArgs = textutil.SanitizeForTerminal(string(ev.ToolCall.Input))
				m.blocks[i].endedAt = time.Now()
				m.invalidateBlockCache(i)
				break
			}
		}
	}
}

func (m *Model) attachTurnFooter(usage *agent.Usage) {
	footer := m.turnFooter(usage)
	if footer == "" {
		return
	}
	details := m.turnDetails(usage)
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == "assistant" && strings.TrimSpace(m.blocks[i].body) != "" {
			m.blocks[i].meta = footer
			m.blocks[i].details = details
			m.invalidateBlockCache(i)
			return
		}
	}
}

func (m *Model) turnFooter(usage *agent.Usage) string {
	agentName := m.turnMode.String()
	if agentName == "" {
		agentName = m.mode.String()
	}
	modelName := strings.TrimSpace(m.turnModel)
	if modelName == "" {
		modelName = "model unset"
	}
	providerName := strings.TrimSpace(m.turnProvider)
	modelPart := modelName
	if providerName != "" {
		modelPart += " via " + providerName
	}
	parts := []string{agentName, modelPart}
	if !m.turnStart.IsZero() {
		if elapsed := sidebarDurationString(time.Since(m.turnStart)); elapsed != "" {
			parts = append(parts, elapsed)
		}
	}
	parts = append(parts, fmt.Sprintf("tools %d", len(m.turnToolCalls)))
	if usage != nil {
		if usage.InputTokens > 0 || usage.OutputTokens > 0 {
			parts = append(parts, fmt.Sprintf("in %s out %s", humanize(usage.InputTokens), humanize(usage.OutputTokens)))
		}
		if usage.CostUSD > 0 {
			parts = append(parts, fmt.Sprintf("+$%.4f", usage.CostUSD))
		}
	}
	return strings.Join(parts, " · ")
}

func (m *Model) turnDetails(usage *agent.Usage) string {
	var lines []string
	if usage != nil {
		if usage.InputTokens > 0 || usage.OutputTokens > 0 {
			lines = append(lines, fmt.Sprintf("tokens: input %s, output %s",
				humanize(usage.InputTokens), humanize(usage.OutputTokens)))
		}
		if usage.CacheReadTokens > 0 || usage.CacheWriteTokens > 0 {
			lines = append(lines, fmt.Sprintf("cache: read %s, write %s",
				humanize(usage.CacheReadTokens), humanize(usage.CacheWriteTokens)))
		}
		if usage.CostUSD > 0 {
			lines = append(lines, fmt.Sprintf("cost: +$%.4f", usage.CostUSD))
		}
	}
	if len(m.turnToolCalls) > 0 {
		names := make([]string, 0, len(m.turnToolCalls))
		for _, call := range m.turnToolCalls {
			if strings.TrimSpace(call.Name) != "" {
				names = append(names, call.Name)
			}
		}
		summary := fmt.Sprintf("tools: %d requested", len(m.turnToolCalls))
		if len(names) > 0 {
			summary += " (" + strings.Join(names, ", ") + ")"
		}
		lines = append(lines, summary)
	}
	if m.session != nil && strings.TrimSpace(m.session.ID) != "" {
		lines = append(lines, "trace: stado session tree "+m.session.ID)
	}
	return strings.Join(lines, "\n")
}

func (m *Model) annotateLastAssistantToolResults(results []agent.ToolResultBlock) {
	if len(results) == 0 {
		return
	}
	failed, rejected := toolResultErrorCounts(results)
	if failed == 0 && rejected == 0 {
		return
	}
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind != "assistant" || strings.TrimSpace(m.blocks[i].meta) == "" {
			continue
		}
		requested := len(results)
		base := fmt.Sprintf("tools %d", requested)
		if strings.Contains(m.blocks[i].meta, base+" (") {
			return
		}
		m.blocks[i].meta = strings.Replace(m.blocks[i].meta, base, base+" ("+toolResultErrorSummary(failed, rejected)+")", 1)
		resultLine := fmt.Sprintf("tool results: %d ok, %d failed, %d rejected",
			requested-failed-rejected, failed, rejected)
		if strings.TrimSpace(m.blocks[i].details) == "" {
			m.blocks[i].details = resultLine
		} else if !strings.Contains(m.blocks[i].details, "tool results:") {
			m.blocks[i].details += "\n" + resultLine
		}
		m.invalidateBlockCache(i)
		return
	}
}

func toolResultErrorCounts(results []agent.ToolResultBlock) (failed, rejected int) {
	for _, result := range results {
		if !result.IsError {
			continue
		}
		if isUnavailableToolResult(result) {
			rejected++
			continue
		}
		failed++
	}
	return failed, rejected
}

func isUnavailableToolResult(result agent.ToolResultBlock) bool {
	return strings.Contains(result.Content, " is not available for this turn")
}

func toolResultErrorSummary(failed, rejected int) string {
	var parts []string
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	if rejected > 0 {
		parts = append(parts, fmt.Sprintf("%d rejected", rejected))
	}
	return strings.Join(parts, ", ")
}

// onTurnComplete is called when the provider's stream ends. It persists the
// assistant turn into msgs; if the turn ended on tool calls, it starts the
// approval queue so the user sees each tool before it runs.
func (m *Model) onTurnComplete() tea.Cmd {
	// Compaction turn: the summariser has produced its draft. Park in
	// stateCompactionPending, waiting for y/n. msgs is NOT touched — the
	// replacement only happens after explicit confirmation.
	if m.compacting {
		m.compacting = false
		if strings.TrimSpace(m.pendingCompactionSummary) == "" {
			m.appendBlock(block{kind: "system", body: "compaction: model returned empty summary — aborting."})
			m.state = stateIdle
			return nil
		}
		m.appendBlock(block{
			kind: "system",
			body: "compaction: press 'y' to replace conversation with the summary above, 'n' to discard.",
		})
		m.state = stateCompactionPending
		return nil
	}

	// Build the assistant message from the accumulated turn.
	var asstBlocks []agent.Block
	if m.turnThinking != "" || m.turnThinkSig != "" {
		asstBlocks = append(asstBlocks, agent.Block{Thinking: &agent.ThinkingBlock{
			Text:      m.turnThinking,
			Signature: m.turnThinkSig,
		}})
	}
	if m.turnText != "" {
		asstBlocks = append(asstBlocks, agent.Block{Text: &agent.TextBlock{Text: m.turnText}})
	}
	for i := range m.turnToolCalls {
		tc := m.turnToolCalls[i]
		asstBlocks = append(asstBlocks, agent.Block{ToolUse: &tc})
	}
	if len(asstBlocks) > 0 {
		asstMsg := agent.Message{Role: agent.RoleAssistant, Content: asstBlocks}
		m.msgs = append(m.msgs, asstMsg)
		m.persistMessage(asstMsg)
	}

	if len(m.turnToolCalls) == 0 {
		m.resolveSteeringAtTurnEnd()
		if !m.turnCancelled && m.verifyEnabled && m.verifyConfig.Enabled() {
			return m.startVerification()
		}
		return m.finishTurnWithoutTools()
	}

	m.pendingCalls = append([]agent.ToolUseBlock{}, m.turnToolCalls...)
	m.pendingResults = nil
	return m.advanceToolQueue()
}

func (m *Model) finishTurnWithoutTools() tea.Cmd {
	if command := m.applicationTurnBoundary(nil, applicationBoundaryFinishTurn); command != nil {
		return command
	}
	return m.finishTurnWithoutToolsAfterApplications()
}

func (m *Model) finishTurnWithoutToolsAfterApplications() tea.Cmd {
	m.closeTurnWithoutTools()
	if m.queuedPrompt != "" {
		return m.promoteQueuedPrompt()
	}
	return nil
}

func (m *Model) closeTurnWithoutTools() {
	if m.session != nil {
		if err := m.session.NextTurn(); err != nil {
			m.appendBlock(block{kind: "system", body: "turn boundary failed: " + err.Error()})
		}
	}
	m.applicationIteration = 0
	m.state = stateIdle
	m.finalizeStreamingBlocks()
	m.renderBlocks()
}

func (m *Model) resolveSteeringAtTurnEnd() {
	if m.steeringMsg != "" {
		switch {
		case m.turnCancelled:
		case m.queuedPrompt == "":
			m.queuedPrompt = m.steeringMsg
			m.appendBlock(block{kind: "user", body: m.steeringMsg, queued: true})
		default:
			m.appendBlock(block{kind: "system", body: "steering message dropped — a queued prompt takes priority: " + trimSeed(m.steeringMsg, 60)})
		}
		m.steeringMsg = ""
	}
}

// advanceToolQueue executes pending tool calls one-by-one without an
// automatic approval gate. Plugins can still request approval
// explicitly through the plugin host.
func (m *Model) advanceToolQueue() tea.Cmd {
	for len(m.pendingCalls) > 0 {
		call := m.pendingCalls[0]
		m.pendingCalls = m.pendingCalls[1:]
		if !m.turnAllowsTool(call.Name) {
			m.rejectUnavailableTool(call)
			continue
		}
		return m.executeCallAsync(call)
	}
	// Queue drained — post the results and let the agent loop re-stream.
	results := m.pendingResults
	m.pendingResults = nil
	m.state = stateIdle
	return func() tea.Msg { return toolsExecutedMsg{results: results} }
}

func (m *Model) turnAllowsTool(name string) bool {
	if len(m.turnAllowed) == 0 {
		return false
	}
	if _, ok := m.turnAllowed[name]; !ok {
		return false
	}
	if m.turnToolInstances == nil {
		// Older focused tests construct only turnAllowed. That remains valid
		// for ordinary tools, but application-owned tools always require an
		// exact per-turn adapter binding.
		if m.executor != nil && m.executor.Registry != nil {
			if current, found := m.executor.Registry.Get(name); found && runtime.ToolMetadataFor(current).LifecycleApplication != "" {
				return false
			}
		}
		return true
	}
	expected, ok := m.turnToolInstances[name]
	if !ok || m.executor == nil || m.executor.Registry == nil {
		return false
	}
	current, ok := m.executor.Registry.Get(name)
	if !ok || current != expected {
		return false
	}
	metadata := runtime.ToolMetadataFor(expected)
	if metadata.LifecycleApplication == "" {
		return true
	}
	if m.applicationToolProjectionGeneration.Load() != m.turnApplicationToolProjectionGeneration {
		return false
	}
	if metadata.ApplicationWorker != "" {
		application := m.ownedApplicationWorker()
		if application == nil || metadata.ApplicationWorker != application.Identity.Namespace || !applicationOwnsModelTool(application, expected) {
			return false
		}
		for _, currentlyPermitted := range m.applicationWorkerTools(m.mode) {
			if currentlyPermitted == expected {
				return true
			}
		}
		return false
	}
	if metadata.ApplicationSession != "" {
		if m.applicationSessionToolOwner(expected) == nil {
			return false
		}
		for _, currentlyPermitted := range m.applicationSessionTools(m.mode) {
			if currentlyPermitted == expected {
				return true
			}
		}
	}
	return false
}

func (m *Model) rejectUnavailableTool(call agent.ToolUseBlock) {
	content := unavailableToolContent(call.Name)
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == "tool" && m.blocks[i].toolID == call.ID {
			m.blocks[i].toolResult = content
			m.blocks[i].streaming = false
			if m.blocks[i].endedAt.IsZero() {
				m.blocks[i].endedAt = time.Now()
			}
			m.invalidateBlockCache(i)
			break
		}
	}
	m.pendingResults = append(m.pendingResults, agent.ToolResultBlock{
		ToolUseID: call.ID,
		Content:   content,
		IsError:   true,
	})
}

func (m *Model) failStreamBudget(err error) {
	if err == nil || m.state == stateError {
		return
	}
	if m.streamCancel != nil {
		m.streamCancel()
		m.streamCancel = nil
	}
	m.state = stateError
	m.errorMsg = err.Error()
	m.appendBlock(block{kind: "system", body: "error: " + err.Error()})
}

func unavailableToolContent(name string) string {
	return fmt.Sprintf("tool %q is not available for this turn", name)
}

// executeCallAsync runs a single tool through the Executor on a goroutine
// so long-running tools (e.g. bash sleep 30) never block the UI. The result
// is ferried back via toolResultMsg. A cancellable context lets Ctrl+C stop
// the tool mid-execution; a tick timer updates the elapsed counter live.
func (m *Model) executeCallAsync(call agent.ToolUseBlock) tea.Cmd {
	if m.executor == nil {
		return func() tea.Msg {
			return toolResultMsg{result: agent.ToolResultBlock{
				ToolUseID: call.ID,
				Content:   "tool execution unavailable (no session)",
				IsError:   true,
			}}
		}
	}
	executor := m.executor
	var applicationGuard func(context.Context) error
	var applicationTool tool.Tool
	if expected := m.turnToolInstances[call.Name]; expected != nil && runtime.ToolMetadataFor(expected).LifecycleApplication != "" {
		applicationTool = expected
		projectionGeneration := m.turnApplicationToolProjectionGeneration
		metadata := runtime.ToolMetadataFor(expected)
		if metadata.ApplicationWorker != "" {
			run := runtime.ApplicationWorkerRun{}
			if m.loop != nil {
				run = m.loop.workerRun
			}
			controller, _ := m.broker.(runtime.ApplicationWorkerRunController)
			applicationGuard = func(ctx context.Context) error {
				if m.applicationToolProjectionGeneration.Load() != projectionGeneration {
					return errors.New("application worker tool projection changed after provider turn")
				}
				return validateApplicationWorkerExecution(ctx, executor, controller, call.Name, expected, run)
			}
		} else if metadata.ApplicationSession != "" {
			owner := m.applicationSessionToolOwner(expected)
			var anchor pluginRuntime.ApplicationAnchor
			if owner != nil && owner.Application != nil {
				anchor = owner.Application.Anchor
			}
			applicationGuard = func(context.Context) error {
				if m.applicationToolProjectionGeneration.Load() != projectionGeneration {
					return errors.New("application session tool projection changed after provider turn")
				}
				return validateApplicationSessionExecution(executor, call.Name, expected, owner, anchor)
			}
		}
	}
	// Tools operate on the user's launch CWD, not the session audit
	// worktree. Same model as `stado run` default. The worktree is
	// where turn-boundary tree commits live (m.session.WorktreePath); it
	// is NOT the agent's working directory.
	host := m.newPluginHostAdapter()
	// Create a cancellable context for this tool execution.
	ctx, cancel := context.WithCancel(context.Background())
	m.toolMu.Lock()
	m.toolCancel = cancel
	// Start the tick timer for live elapsed-time updates.
	m.toolTickTimer = time.AfterFunc(250*time.Millisecond, func() {
		if m.program != nil {
			m.program.Send(toolTickMsg{})
		}
	})
	m.toolMu.Unlock()
	var trajectoryRecorder *trajectory.Recorder
	trajectoryTurn := 0
	if m.session != nil {
		if writer, ok := m.broker.(trajectory.Writer); ok {
			rec := trajectory.Recorder{Writer: writer}
			trajectoryRecorder = &rec
			trajectoryTurn = m.session.Turn()
		}
	}
	return func() tea.Msg {
		defer func() {
			// Ensure timer is stopped when tool completes (normally or cancelled).
			m.toolMu.Lock()
			if m.toolTickTimer != nil {
				m.toolTickTimer.Stop()
				m.toolTickTimer = nil
			}
			m.toolMu.Unlock()
		}()
		if applicationGuard != nil {
			if err := applicationGuard(ctx); err != nil {
				return toolResultMsg{result: agent.ToolResultBlock{ToolUseID: call.ID, Content: err.Error(), IsError: true}}
			}
		}
		callCtx := runtime.WithSkillCatalog(ctx, m.skills)
		callCtx = tool.WithToolSurfaceController(callCtx, newModelToolSurfaceController(m))
		var res tool.Result
		var err error
		if applicationTool != nil {
			res, err = executor.RunExpected(callCtx, call.Name, applicationTool, call.Input, host)
		} else {
			res, err = executor.Run(callCtx, call.Name, call.Input, host)
		}
		content := res.Content
		isErr := res.Error != ""
		if err != nil {
			// Distinguish cancellation from other errors.
			if errors.Is(err, context.Canceled) {
				content = "cancelled by user"
			} else {
				content = err.Error()
			}
			isErr = true
		} else if isErr {
			content = res.Error
		}
		resultBlock := agent.ToolResultBlock{
			ToolUseID: call.ID,
			Content:   content,
			IsError:   isErr,
		}
		if trajectoryRecorder != nil {
			trajectoryRecorder.ToolOutcome(trajectoryTurn, call, resultBlock)
		}
		return toolResultMsg{result: resultBlock}
	}
}

func (m *Model) buildSubagentSpawner() func(context.Context, subagent.Request) (subagent.Result, error) {
	runner, ok := m.buildSubagentRunner()
	if !ok {
		return nil
	}
	return runner.SpawnSubagent
}

func (m *Model) buildSubagentRunner() (runtime.SubagentRunner, bool) {
	if m.cfg == nil || m.session == nil || m.provider == nil {
		return runtime.SubagentRunner{}, false
	}
	onEvent := func(ev runtime.SubagentEvent) {
		if m.program != nil {
			m.program.Send(subagentEventMsg{ev: ev})
		}
		// EP-0034 phase B will fan SubagentEvents into
		// runtime.Fleet.UpdateProgress so the /fleet modal
		// shows live LastTool/LastText. Phase A (this
		// release) populates SessionID + terminal Status
		// from Fleet.runGoroutine on goroutine return —
		// adequate for "see what's running, see what
		// finished," misses "see what running agent X is
		// currently doing." See docs/eps/0034 D5.
	}
	if publisher, ok := m.broker.(runtime.ApplicationEventPublisher); ok {
		onEvent = runtime.AgentDownEventCallback(m.session.ID, publisher, onEvent)
	}
	runner := runtime.SubagentRunner{
		Config:                   m.cfg,
		Parent:                   m.session,
		Provider:                 m.provider,
		Model:                    m.model,
		Thinking:                 m.cfg.Agent.Thinking,
		ThinkingBudgetTokens:     m.cfg.Agent.ThinkingBudgetTokens,
		System:                   m.systemPrompt,
		SystemTemplate:           m.systemPromptTemplate,
		AgentName:                "stado-tui-subagent",
		QuietRegistryDiagnostics: true,
		Metrics:                  m.metrics,
		Broker:                   m.broker,
		ResolveSource:            runtime.ResolveTreeSource(m.session, m.cfg.WorktreeDir()),
		ResolveProviderModel: func(_ context.Context, providerName, modelName string) (agent.Provider, string, error) {
			provider, err := buildProviderByName(m.cfg, providerName)
			if err != nil {
				return nil, "", err
			}
			return provider, modelName, nil
		},
		OnEvent: onEvent,
	}
	return runner, true
}

// toolDefs builds the tool-definition list for the current turn request. An
// empty registry (no session) returns nil so the provider runs pure chat.
//
// EP-0037 lazy-load: each turn sends only autoloaded core + tools the model
// has explicitly activated this session via tools.describe / tools.activate
// / plugin.load. Without this filter every installed plugin's schema would
// land in every turn, blowing up the prompt budget on rich plugin sets.
//
// In Plan mode only NonMutating tools are exposed — the model can grep/read/
// look-up-defs to form a plan, but can't edit/write/bash. This is the
// principled enforcement (no approval-loop workaround): the model literally
// doesn't see the mutating tools as available, so it produces analysis
// rather than asking to execute.
func (m *Model) toolDefs() []agent.ToolDef {
	return toolDefsForSurface(m.toolSurfaceForTurn())
}

func toolDefsForSurface(surface []tool.Tool) []agent.ToolDef {
	out := make([]agent.ToolDef, 0, len(surface))
	for _, t := range surface {
		schema, _ := json.Marshal(t.Schema())
		out = append(out, agent.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      schema,
		})
	}
	return out
}

// toolSurfaceForTurn is the per-turn surface: AutoloadedTools(reg, cfg) +
// activatedTools, with Plan-mode and session-override filtering applied.
// Distinct from visibleTools (which returns the full filtered registry for
// `/tool ls` and the status modal).
func (m *Model) toolSurfaceForTurn() []tool.Tool {
	if m.executor == nil {
		return nil
	}
	eff := m.cfg
	if !m.sessionToolOverrides.isZero() && m.cfg != nil {
		c := *m.cfg
		c.Tools = m.sessionToolOverrides.effectiveTools(m.cfg)
		eff = &c
	}
	// Per-persona tool promotion (2026-06-13): the active persona's
	// EffectiveTools() are merged ADDITIVELY into the autoload surface for
	// this turn. Read live off m.persona so a /persona switch re-scopes on
	// the next turn for free — no registry rebuild, no shared-cfg mutation.
	extra := m.persona.EffectiveTools()
	autoloaded := runtime.AutoloadedToolsWithExtra(m.executor.Registry, eff, extra)
	pool := withoutLifecycleApplicationTools(autoloaded)
	m.activatedToolsMu.RLock()
	activated := make(map[string]bool, len(m.activatedTools))
	for name, active := range m.activatedTools {
		activated[name] = active
	}
	m.activatedToolsMu.RUnlock()
	if len(activated) > 0 {
		seen := map[string]bool{}
		for _, t := range autoloaded {
			seen[t.Name()] = true
		}
		for name := range activated {
			if seen[name] {
				continue
			}
			if t, ok := m.executor.Registry.Get(name); ok {
				if runtime.ToolMetadataFor(t).LifecycleApplication != "" {
					continue
				}
				pool = append(pool, t)
				seen[name] = true
			}
		}
		sort.Slice(pool, func(i, j int) bool { return pool[i].Name() < pool[j].Name() })
	}
	if m.mode == modePlan || m.mode == modeBTW {
		out := make([]tool.Tool, 0, len(pool))
		for _, t := range pool {
			if m.executor.Registry.ClassOf(t.Name()) != tool.ClassNonMutating {
				continue
			}
			out = append(out, t)
		}
		pool = out
	}
	if !m.sessionToolOverrides.isZero() {
		out := make([]tool.Tool, 0, len(pool))
		for _, t := range pool {
			if m.sessionToolOverrideHidesTool(t.Name()) {
				continue
			}
			out = append(out, t)
		}
		pool = out
	}
	seen := make(map[string]bool, len(pool))
	for _, candidate := range pool {
		seen[candidate.Name()] = true
	}
	for _, candidate := range m.applicationWorkerTools(m.mode) {
		if !seen[candidate.Name()] {
			pool = append(pool, candidate)
			seen[candidate.Name()] = true
		}
	}
	for _, candidate := range m.applicationSessionTools(m.mode) {
		if !seen[candidate.Name()] {
			pool = append(pool, candidate)
			seen[candidate.Name()] = true
		}
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].Name() < pool[j].Name() })
	return pool
}

func withoutLifecycleApplicationTools(input []tool.Tool) []tool.Tool {
	out := make([]tool.Tool, 0, len(input))
	for _, candidate := range input {
		if runtime.ToolMetadataFor(candidate).LifecycleApplication == "" {
			out = append(out, candidate)
		}
	}
	return out
}

func (m *Model) visibleTools() []tool.Tool {
	if m.executor == nil {
		return nil
	}
	all := withoutLifecycleApplicationTools(m.executor.Registry.All())
	var pool []tool.Tool
	if m.mode != modePlan && m.mode != modeBTW {
		pool = all
	} else {
		pool = make([]tool.Tool, 0, len(all))
		for _, t := range all {
			if m.executor.Registry.ClassOf(t.Name()) != tool.ClassNonMutating {
				continue
			}
			pool = append(pool, t)
		}
	}
	// Apply session-scoped overrides on top of the registry-side filter
	// (which was already applied at executor-build time using the disk
	// config). Overrides only ever subtract from `pool` — they can't
	// expose tools that aren't in the executor's registry.
	if !m.sessionToolOverrides.isZero() {
		out := make([]tool.Tool, 0, len(pool))
		for _, t := range pool {
			if m.sessionToolOverrideHidesTool(t.Name()) {
				continue
			}
			out = append(out, t)
		}
		pool = out
	}
	pool = append(pool, m.applicationWorkerTools(m.mode)...)
	pool = append(pool, m.applicationSessionTools(m.mode)...)
	sort.Slice(pool, func(i, j int) bool { return pool[i].Name() < pool[j].Name() })
	return pool
}

// compactRequest / compactReplace are thin aliases so the code sites
// read in-place (the compact package owns the wire contract, not the TUI).
var (
	compactRequest = compact.BuildRequest
	compactReplace = compact.ReplaceMessages
)

// renderContextStatus summarises what the ctx% in the status bar is
// made of, plus what the user's options are at each threshold. Kept
// terse — one system block, readable in < 1 screen.
// thresholdLabel renders a context threshold as a percentage, or "disabled"
// when set to 0 (R9 — 0 turns the gate off).
func thresholdLabel(t float64) string {
	if t <= 0 {
		return "disabled"
	}
	return fmt.Sprintf("%.0f%%", 100*t)
}

func (m *Model) renderContextStatus() string {
	used := m.usage.InputTokens
	var sb strings.Builder

	caps := m.providerCaps()
	switch {
	case !m.tokenCounterPresent && m.tokenCounterChecked:
		sb.WriteString(fmt.Sprintf("context: unavailable — provider %q doesn't expose a token counter.\n",
			m.providerDisplayName()))
	case caps.MaxContextTokens == 0:
		sb.WriteString("context: unavailable — provider hasn't reported MaxContextTokens.\n")
	case used == 0:
		sb.WriteString(fmt.Sprintf("context: 0 / %d tokens (0%%) — first turn hasn't run yet.\n",
			caps.MaxContextTokens))
	default:
		fraction := float64(used) / float64(caps.MaxContextTokens)
		sb.WriteString(fmt.Sprintf("context: %s / %s tokens (%.1f%%)\n",
			humanize(used), humanize(caps.MaxContextTokens), 100*fraction))
		sb.WriteString(fmt.Sprintf("thresholds: soft %s · hard %s\n",
			thresholdLabel(m.ctxSoftThreshold), thresholdLabel(m.ctxHardThreshold)))
		switch {
		case m.ctxHardThreshold > 0 && fraction >= m.ctxHardThreshold:
			sb.WriteString("status: above hard threshold — consider /compact or `stado session fork <id> --at turns/<N>` in another shell.\n")
		case m.ctxSoftThreshold > 0 && fraction >= m.ctxSoftThreshold:
			sb.WriteString("status: above soft threshold — forking from an earlier turn is the preferred recovery; /compact is the lossy fallback.\n")
		default:
			sb.WriteString("status: healthy.\n")
		}
	}
	sb.WriteString(fmt.Sprintf("turns: %d messages in history\n", len(m.msgs)))

	// Session id (if we're in one) so users can copy-paste into
	// `stado session fork` / `session tree` without a separate /session
	// lookup. Zero-value session fields are tolerated — a TUI running
	// outside a session prints "(no session)".
	if m.session != nil && m.session.ID != "" {
		sb.WriteString(fmt.Sprintf("session: %s\n", m.session.ID))
	}

	// Cost is observational. Budget enforcement below is token-only.
	sb.WriteString(fmt.Sprintf("cost: $%.4f\n", m.usage.CostUSD))
	// Token caps are shown only when configured.
	if m.budgetWarnTokens > 0 || m.budgetHardTokens > 0 ||
		m.budgetWarnInputTokens > 0 || m.budgetHardInputTokens > 0 ||
		m.budgetWarnOutputTokens > 0 || m.budgetHardOutputTokens > 0 {
		sb.WriteString(fmt.Sprintf("tokens: %s in / %s out (%s total)\n",
			formatTokenCount(m.usage.InputTokens), formatTokenCount(m.usage.OutputTokens),
			formatTokenCount(m.totalTokens())))
		writeTokenCap := func(label string, warn, hard int) {
			if warn <= 0 && hard <= 0 {
				return
			}
			w := "(unset)"
			if warn > 0 {
				w = formatTokenCount(warn)
			}
			h := "(unset)"
			if hard > 0 {
				h = formatTokenCount(hard)
			}
			sb.WriteString(fmt.Sprintf("%s: warn=%s · hard=%s\n", label, w, h))
		}
		writeTokenCap("token budget (total)", m.budgetWarnTokens, m.budgetHardTokens)
		writeTokenCap("token budget (input)", m.budgetWarnInputTokens, m.budgetHardInputTokens)
		writeTokenCap("token budget (output)", m.budgetWarnOutputTokens, m.budgetHardOutputTokens)
	}

	// Project-level instructions (AGENTS.md / CLAUDE.md), if loaded.
	if m.systemPromptPath != "" {
		sb.WriteString(fmt.Sprintf("instructions: %s\n", filepath.Base(m.systemPromptPath)))
	}
	// Loaded skills.
	if len(m.skills) > 0 {
		names := make([]string, 0, len(m.skills))
		for _, s := range m.skills {
			names = append(names, s.Name)
		}
		sb.WriteString(fmt.Sprintf("skills: %d loaded — %s\n", len(names), strings.Join(names, ", ")))
	}
	// post_turn hook, if configured.
	if m.hookRunner.PostTurnCmd != "" {
		cmd := m.hookRunner.PostTurnCmd
		if len(cmd) > 60 {
			cmd = cmd[:57] + "..."
		}
		if m.hookRunner.Disabled {
			sb.WriteString(fmt.Sprintf("hook post_turn: %s (disabled: bash tool unavailable)\n", cmd))
		} else {
			sb.WriteString(fmt.Sprintf("hook post_turn: %s\n", cmd))
		}
	}

	sb.WriteString("options: /compact (summarise + confirm)  ·  /retry (regenerate last turn)  ·  session tree / session fork --at turns/<N>")
	return strings.TrimRight(sb.String(), "\n")
}

// startCompaction kicks off a summarisation stream and parks the UI in
// stateCompactionPending once it completes. See DESIGN §"Compaction":
// user-invoked only, explicit confirmation required before msgs is
// replaced.
func (m *Model) startCompaction() tea.Cmd {
	// Block compaction only while a turn is genuinely in flight, or while
	// another summary is already pending. A turn that ERRORED — e.g. a
	// context-overflow 400 from the provider — leaves the model in
	// stateError, and that is exactly when the user needs to compact to
	// recover. The previous gate refused anything != stateIdle, which
	// deadlocked the session: the failed turn left stateError, /compact
	// reported "busy — wait for the current turn to finish" (though nothing
	// was running), and there was no way out. compactRequest truncates tool
	// content, so the summary request fits even when the live context
	// overflowed the model.
	switch m.state {
	case stateStreaming:
		m.appendBlock(block{kind: "system", body: "compaction: busy — wait for the current turn to finish (Ctrl+G interrupts)"})
		return nil
	case stateCompactionPending, stateCompactionEditing:
		m.appendBlock(block{kind: "system", body: "compaction: a summary is already pending — accept or discard it first"})
		return nil
	}
	// stateIdle or stateError: clear any prior error and proceed.
	m.errorMsg = ""
	if !m.ensureProvider() {
		return nil
	}
	if len(m.msgs) == 0 {
		m.appendBlock(block{kind: "system", body: "compaction: conversation is empty — nothing to compact"})
		return nil
	}

	m.appendBlock(block{kind: "system", body: "compacting conversation — streaming proposed summary below..."})
	m.appendBlock(block{kind: "assistant", body: ""})
	// Remember where the streamed summary lives so inline-edit
	// ('e' key) can rewrite the right block when the user revises.
	m.compactionBlockIdx = len(m.blocks) - 1
	m.compacting = true
	m.pendingCompactionSummary = ""

	// Parent-link through rootCtx so the compaction turn's spans
	// thread into the session's trace tree (Phase 9.4/9.5).
	ctx, cancel := context.WithCancel(m.rootCtx)
	m.streamMu.Lock()
	m.streamCancel = cancel
	m.state = stateStreaming
	m.errorMsg = ""
	m.streamMu.Unlock()

	req := compactRequest(m.model, m.msgs)

	go func() {
		defer cancel()
		ch, err := m.provider.StreamTurn(ctx, req)
		if err != nil {
			m.sendMsg(streamErrorMsg{err: err})
			return
		}
		for ev := range ch {
			m.sendMsg(streamEventMsg{ev: ev})
			if ev.Kind == agent.EvDone || ev.Kind == agent.EvError {
				m.sendMsg(streamDoneMsg{})
				return
			}
		}
		m.sendMsg(streamDoneMsg{})
	}()
	return nil
}

// enterSummaryEdit swaps the user's in-flight draft for the proposed
// compaction summary so they can revise it in the main editor. The
// draft is stashed and restored on commit/cancel — DESIGN §"Compaction"
// emphasises the user shouldn't lose their current thought while
// deciding how to recover.
func (m *Model) enterSummaryEdit() {
	if m.state != stateCompactionPending {
		return
	}
	m.savedDraftBeforeEdit = m.input.Value()
	m.input.SetValue(m.pendingCompactionSummary)
	m.state = stateCompactionEditing
	m.appendBlock(block{
		kind: "system",
		body: "editing summary — Enter to save, Esc/n to cancel.",
	})
}

// commitSummaryEdit finalises the edit: the new text becomes
// pendingCompactionSummary AND is written back into the visible
// assistant block so the user sees the revision before pressing y.
func (m *Model) commitSummaryEdit() {
	if m.state != stateCompactionEditing {
		return
	}
	edited := m.input.Value()
	m.pendingCompactionSummary = edited
	if m.compactionBlockIdx >= 0 && m.compactionBlockIdx < len(m.blocks) {
		m.blocks[m.compactionBlockIdx].body = edited
		m.invalidateBlockCache(m.compactionBlockIdx)
	}
	m.input.SetValue(m.savedDraftBeforeEdit)
	m.savedDraftBeforeEdit = ""
	m.state = stateCompactionPending
	m.appendBlock(block{
		kind: "system",
		body: "summary updated — press 'y' to apply, 'n' to discard, 'e' to edit again.",
	})
}

// cancelSummaryEdit restores the original summary + the draft the user
// had in flight. pendingCompactionSummary and the visible block are
// left untouched — we only discard the editor's buffer.
func (m *Model) cancelSummaryEdit() {
	if m.state != stateCompactionEditing {
		return
	}
	m.input.SetValue(m.savedDraftBeforeEdit)
	m.savedDraftBeforeEdit = ""
	m.state = stateCompactionPending
	m.appendBlock(block{
		kind: "system",
		body: "edit cancelled — original summary kept.",
	})
}

// resolveCompaction is called from Update when the user presses 'y' or
// 'n' while in stateCompactionPending. 'y' replaces msgs AND writes a
// dual-ref git commit (tree + trace) recording the compaction event;
// 'n' discards the summary and leaves both sides untouched.
//
// DESIGN §"Compaction" invariant: `tree` commit keeps its parent's
// tree hash (filesystem unchanged — compaction is conversation-scope,
// not file-scope), so `git checkout refs/sessions/<id>/tree~1 -- …`
// restores the pre-compaction file state exactly. The raw conversation
// JSONL is append-only; trace gets a parallel marker for audit.
func (m *Model) resolveCompaction(accept bool) {
	if m.state != stateCompactionPending {
		return
	}
	if accept {
		summary := m.pendingCompactionSummary
		accepted := "compaction accepted — prior conversation replaced with summary."
		if m.session != nil {
			rawLogSHA, err := runtime.ConversationLogSHA(m.session.WorktreePath)
			if err != nil {
				m.appendBlock(block{kind: "system", body: "compaction failed before applying summary: " + err.Error()})
				m.pendingCompactionSummary = ""
				m.state = stateIdle
				return
			}
			fromTurn, toTurn, turnsTotal := m.compactionTurnRange(len(m.msgs))
			title := compactionTitle(summary)
			treeSHA, traceSHA, err := m.session.CommitCompaction(stadogit.CompactionMeta{
				Title:      title,
				Summary:    summary,
				FromTurn:   fromTurn,
				ToTurn:     toTurn,
				TurnsTotal: turnsTotal,
				ByAuthor:   m.providerDisplayName(),
				RawLogSHA:  rawLogSHA,
			})
			if err != nil {
				m.appendBlock(block{kind: "system", body: "compaction failed before applying summary: " + err.Error()})
				m.pendingCompactionSummary = ""
				m.state = stateIdle
				return
			}
			if err := runtime.AppendCompaction(m.session.WorktreePath, runtime.ConversationCompaction{
				Summary:    summary,
				FromTurn:   fromTurn,
				ToTurn:     toTurn,
				TurnsTotal: turnsTotal,
				By:         m.providerDisplayName(),
				TreeSHA:    treeSHA.String(),
				TraceSHA:   traceSHA.String(),
				RawLogSHA:  rawLogSHA,
			}); err != nil {
				m.appendBlock(block{kind: "system", body: "compaction audit marker written, but conversation log update failed; conversation left unchanged: " + err.Error()})
				m.pendingCompactionSummary = ""
				m.state = stateIdle
				return
			}
			accepted += fmt.Sprintf("\ntree: %s  trace: %s",
				treeSHA.String()[:12], traceSHA.String()[:12])
		}
		m.msgs = compactReplace(summary)

		// Also clear the visual chat history so the user sees the
		// replacement happen, not just read about it in a system note
		// below the pre-compact turns. Without this the next user
		// message pushes the old turns further up rather than starting
		// fresh. The raw JSONL is append-only: it now contains the prior
		// messages plus a compaction event, and LoadConversation folds
		// that event into this compacted view on resume.
		m.blocks = nil
		m.appendBlock(block{kind: "assistant", body: summary})
		m.appendBlock(block{kind: "system", body: accepted})
	} else {
		m.appendBlock(block{kind: "system", body: "compaction declined — conversation unchanged."})
	}
	m.pendingCompactionSummary = ""
	m.state = stateIdle
}

func (m *Model) compactionTurnRange(fallbackMessages int) (fromTurn, toTurn, turnsTotal int) {
	if m.session == nil {
		return 0, 0, fallbackMessages
	}
	toTurn = m.session.Turn()
	if markers, err := m.session.Sidecar.ListCompactions(m.session.ID); err == nil && len(markers) > 0 {
		fromTurn = markers[0].ToTurn + 1
	}
	switch {
	case toTurn <= 0:
		turnsTotal = fallbackMessages
	case fromTurn == 0:
		turnsTotal = toTurn
	case toTurn >= fromTurn:
		turnsTotal = toTurn - fromTurn + 1
	default:
		turnsTotal = fallbackMessages
	}
	return fromTurn, toTurn, turnsTotal
}

// compactionTitle derives a short subject line from the summary — the
// first sentence, capped at ~70 chars. The full body lands in the
// commit message under the subject.
func compactionTitle(summary string) string {
	s := strings.TrimSpace(summary)
	if i := strings.IndexAny(s, ".\n"); i > 0 && i < 120 {
		s = s[:i]
	}
	if len(s) > 70 {
		s = s[:69] + "…"
	}
	return s
}

// installedAutoCompact returns the exact canonical source identity selected
// for the sole installed source namespace displaying the auto-compact alias.
// The display name is discovery metadata only: multiple source namespaces
// fail closed, while multiple versions of one source use its exact active
// marker (or highest signed semver).
func (m *Model) installedAutoCompact() string {
	cfg, err := config.Load()
	if err != nil {
		return ""
	}
	byNamespace := make(map[string][]plugins.InstalledPackage)
	rootByNamespace := make(map[string]string)
	for _, root := range cfg.AllPluginDirs() {
		packages, listErr := plugins.ListInstalledPackages(root)
		if listErr != nil {
			return ""
		}
		for _, pkg := range packages {
			if pkg.Manifest.Name != "auto-compact" {
				continue
			}
			byNamespace[pkg.Identity.Namespace] = append(byNamespace[pkg.Identity.Namespace], pkg)
			if previous, ok := rootByNamespace[pkg.Identity.Namespace]; ok && previous != root {
				return ""
			}
			rootByNamespace[pkg.Identity.Namespace] = root
		}
	}
	if len(byNamespace) != 1 {
		return ""
	}
	for namespace, candidates := range byNamespace {
		pkg, ok, pickErr := plugins.PickActivePackage(rootByNamespace[namespace], namespace, candidates)
		if pickErr != nil || !ok {
			return ""
		}
		return pkg.Identity.Canonical
	}
	return ""
}

// aboveHardThreshold reports whether the current turn's running
// context usage has crossed the hard threshold. DESIGN §"Token
// accounting" §11.2.6: new user-initiated turns block above this
// bound; in-flight tool-continuation turns are allowed to finish.
func (m *Model) aboveHardThreshold() bool {
	if m.ctxHardThreshold <= 0 {
		return false
	}
	return m.contextFraction() >= m.ctxHardThreshold
}

// contextFraction returns current input-token usage as a fraction of
// the provider's reported max context. Returns 0 when capacity or
// usage is unknown — callers treat that as "not above threshold".
func (m *Model) contextFraction() float64 {
	cap := m.providerCaps().MaxContextTokens
	used := m.usage.InputTokens
	if cap <= 0 || used == 0 {
		return 0
	}
	return float64(used) / float64(cap)
}

// firePostTurnHook invokes the user-configured post_turn shell
// command (if any) with a JSON payload on stdin, AND fires the
// scriptable post_turn lifecycle hook (F1) if any is configured. No-op
// when neither is configured. Errors / timeouts are logged by the hook
// runners; never propagated — the turn is over.
func (m *Model) firePostTurnHook() {
	duration := time.Duration(0)
	if !m.turnStart.IsZero() {
		duration = time.Since(m.turnStart)
	}
	// Legacy shell notification hook.
	if m.hookRunner.PostTurnCmd != "" && !m.hookRunner.Disabled {
		m.hookRunner.FirePostTurn(m.rootCtx, hooks.NewPostTurnPayload(len(m.msgs), m.usage, m.turnText, duration))
	}
	// Scriptable lifecycle post_turn (deny/mutate-capable seam; the
	// post_turn point is informational — the turn is over).
	if m.lifecycleHooks.HasPoint(hooks.PointPostTurn) {
		pt := hooks.PostTurnLifecycle(len(m.msgs), m.turnText, m.usage.InputTokens, m.usage.OutputTokens, m.usage.CostUSD, duration)
		m.lifecycleHooks.Fire(m.rootCtx, hooks.PointPostTurn, pt)
	}
}

// firePostLLMHook runs the scriptable post_llm lifecycle hook (F1) on the
// completed assistant turn, mirroring agentloop.go's post_llm seam for the
// interactive TUI (which streams directly, not via runtime.AgentLoop).
// Called from onStreamDone AFTER the stream finishes but BEFORE
// onTurnComplete flushes m.turnText into history, so a mutate/deny rewrites
// the text the model history records:
//   - Mutate → rewrite the assistant text (m.turnText). Tool calls are
//     reported for inspection but not mutated here — pre_tool covers
//     per-call arg rewriting.
//   - Deny   → the generation already happened; treat as a request to
//     replace the assistant text with the reason.
//
// No-op when no hook subscribes to post_llm.
func (m *Model) firePostLLMHook() {
	if !m.lifecycleHooks.HasPoint(hooks.PointPostLLM) {
		return
	}
	post := hooks.PostLLM(len(m.msgs), m.turnText, len(m.turnToolCalls),
		m.usage.InputTokens, m.usage.OutputTokens, m.usage.CostUSD)
	decision, out := m.lifecycleHooks.Fire(m.rootCtx, hooks.PointPostLLM, post)
	newText := m.turnText
	switch decision.Decision {
	case hooks.DecisionDeny:
		newText = fmt.Sprintf("[post_llm hook: %s]", decision.Reason)
	case hooks.DecisionMutate:
		if mp, ok := out.(*hooks.PostLLMPayload); ok {
			newText = mp.Text
		}
	}
	if newText == m.turnText {
		return
	}
	m.turnText = newText
	// The assistant text was streamed live into the last rendered block as
	// it arrived; reconcile that block so what the user sees matches the
	// mutated text that onTurnComplete is about to flush into history.
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind != "assistant" {
			continue
		}
		m.blocks[i].body = textutil.SanitizeForTerminal(newText)
		m.invalidateBlockCache(i)
		m.renderBlocks()
		break
	}
}

// maybeEmitBudgetWarning fires a one-time system block once cumulative
// cost OR token usage crosses a configured warn cap, so users don't
// keep seeing the same notice every turn. Called from handleStreamEvent
// on every Usage update.
//
// Token caps matter here because local-runner setups (Ollama / LM
// The warning uses the same token counters on local and remote providers.
// budgetWarnDescription mirrors budgetBreachDescription's precedence so
// the warn block names whichever cap actually fired.
func (m *Model) maybeEmitBudgetWarning() {
	if m.budgetWarned {
		return
	}
	crossed, hint := m.budgetWarnDescription()
	if crossed == "" {
		return
	}
	m.budgetWarned = true
	m.appendBlock(block{
		kind: "system",
		body: fmt.Sprintf("budget warning: %s%s", crossed, hint),
	})
	m.renderBlocks()
}

// budgetWarnDescription names the first warn cap that has been crossed
// and a hint pointing at the matching hard cap, in the same precedence
// order as budgetWarning/budgetBreachDescription (combined tokens, then
// per-direction). Returns ("", "") when nothing
// is over its warn cap. The crossed string is phrased as a full clause
// ("tokens N crossed warn cap M") so
// maybeEmitBudgetWarning can render it verbatim.
func (m *Model) budgetWarnDescription() (crossed, hint string) {
	switch {
	case m.budgetWarnTokens > 0 && m.totalTokens() >= m.budgetWarnTokens:
		crossed = fmt.Sprintf("tokens %s crossed warn cap %s", formatTokenCount(m.totalTokens()), formatTokenCount(m.budgetWarnTokens))
		if m.budgetHardTokens > 0 {
			hint = fmt.Sprintf(" — hard cap at %s tok", formatTokenCount(m.budgetHardTokens))
		}
	case m.budgetWarnInputTokens > 0 && m.usage.InputTokens >= m.budgetWarnInputTokens:
		crossed = fmt.Sprintf("input tokens %s crossed warn cap %s", formatTokenCount(m.usage.InputTokens), formatTokenCount(m.budgetWarnInputTokens))
		if m.budgetHardInputTokens > 0 {
			hint = fmt.Sprintf(" — hard cap at %s tok", formatTokenCount(m.budgetHardInputTokens))
		}
	case m.budgetWarnOutputTokens > 0 && m.usage.OutputTokens >= m.budgetWarnOutputTokens:
		crossed = fmt.Sprintf("output tokens %s crossed warn cap %s", formatTokenCount(m.usage.OutputTokens), formatTokenCount(m.budgetWarnOutputTokens))
		if m.budgetHardOutputTokens > 0 {
			hint = fmt.Sprintf(" — hard cap at %s tok", formatTokenCount(m.budgetHardOutputTokens))
		}
	}
	return crossed, hint
}
