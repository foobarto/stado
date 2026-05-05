package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/foobarto/stado/internal/compact"
	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/internal/instructions"
	"github.com/foobarto/stado/internal/memory"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/streambudget"
	"github.com/foobarto/stado/internal/subagent"
	"github.com/foobarto/stado/internal/textutil"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/foobarto/stado/pkg/tool"
)

func (m *Model) turnSystemPrompt(userPrompt string) string {
	return instructions.ComposeSystemPrompt(m.systemPromptTemplate, m.systemPrompt, instructions.RuntimeContext{
		Provider: m.providerDisplayName(),
		Model:    m.model,
		Memory:   m.turnMemoryContext(userPrompt),
	})
}

func (m *Model) turnMemoryContext(userPrompt string) string {
	if m.cfg == nil || !m.cfg.Memory.Enabled {
		return ""
	}
	sessionID := ""
	if m.session != nil {
		sessionID = m.session.ID
	}
	ctx := m.rootCtx
	if ctx == nil {
		ctx = context.Background()
	}
	body, err := memory.PromptContext(ctx, memory.PromptContextOptions{
		Enabled:      m.cfg.Memory.Enabled,
		StateDir:     m.cfg.StateDir(),
		Workdir:      m.cwd,
		SessionID:    sessionID,
		Prompt:       userPrompt,
		MaxItems:     m.cfg.Memory.EffectiveMaxItems(),
		BudgetTokens: m.cfg.Memory.EffectiveBudgetTokens(),
	})
	if err != nil {
		tuiTrace("memory prompt context failed", "error", err.Error())
		return ""
	}
	return body
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
func (m *Model) startBtw(question string) tea.Cmd {
	if !m.ensureProvider() {
		return nil
	}

	// Show the user's question immediately as a btw block.
	m.appendBlock(block{kind: "btw", body: question + "\n"})
	m.renderBlocks()

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
			Model:    m.model,
			System:   m.turnSystemPrompt(question),
			Messages: msgs,
			Tools:    tools,
		}
		if m.provider.Capabilities().SupportsPromptCache && len(msgs) > 1 {
			req.CacheHints = []agent.CachePoint{{MessageIndex: len(msgs) - 2}}
		}

		ch, err := m.provider.StreamTurn(ctx, req)
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

	// Reset per-turn accumulators.
	m.turnText = ""
	m.turnThinking = ""
	m.turnThinkSig = ""
	m.turnToolCalls = nil
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

	req := agent.TurnRequest{
		Model:    m.model,
		Messages: m.msgs,
		Tools:    m.toolDefs(),
		System:   m.turnSystemPrompt(latestUserPrompt(m.msgs)),
		// EP-0036: sampling from config [sampling] section (nil-safe).
		Temperature: func() *float64 {
			if m.cfg != nil { return m.cfg.Sampling.Temperature }; return nil }(),
		TopP: func() *float64 {
			if m.cfg != nil { return m.cfg.Sampling.TopP }; return nil }(),
		TopK: func() *int {
			if m.cfg != nil { return m.cfg.Sampling.TopK }; return nil }(),
	}
	m.turnAllowed = make(map[string]struct{}, len(req.Tools))
	for _, t := range req.Tools {
		m.turnAllowed[t.Name] = struct{}{}
	}
	// Cache-breakpoint placement — DESIGN §"Prompt-cache awareness".
	// One ephemeral breakpoint on the last prior message, so every turn
	// caches the entire history up through the previous turn.
	if m.provider.Capabilities().SupportsPromptCache && len(m.msgs) > 0 {
		req.CacheHints = []agent.CachePoint{{MessageIndex: len(m.msgs) - 1}}
	}

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
		tuiTrace("provider stream start", "provider", m.providerDisplayName(), "model", m.model)
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
	m.queuedPrompt = ""
	if strings.HasPrefix(queued, "/") {
		return m.handleSlash(queued)
	}
	m.clearQueuedUserBlock(false)
	m.maybeAutoTitleSession(queued)
	msg := agent.Text(agent.RoleUser, queued)
	m.msgs = append(m.msgs, msg)
	m.persistMessage(msg)
	tuiTrace("queued prompt promoted", "chars", len(queued))
	return m.startStream()
}

func (m *Model) restoreQueuedPromptToInput() string {
	if m.queuedPrompt == "" {
		return ""
	}
	queued := m.queuedPrompt
	m.queuedPrompt = ""
	m.clearQueuedUserBlock(true)
	m.input.SetValue(queued)
	tuiTrace("queued prompt restored to input", "chars", len(queued))
	return queued
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
			m.usage.InputTokens = ev.Usage.InputTokens
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
				m.blocks[last].body += ev.Text
				m.invalidateBlockCache(last)
			}
			return
		}
		m.turnText += ev.Text
		if len(m.blocks) == 0 || m.blocks[len(m.blocks)-1].kind != "assistant" {
			m.blocks = append(m.blocks, block{kind: "assistant"})
		}
		last := len(m.blocks) - 1
		m.blocks[last].body += ev.Text
		m.invalidateBlockCache(last)

	case agent.EvThinkingDelta:
		if err := streambudget.CheckAppend("assistant thinking", len(m.turnThinking), len(ev.Text), streambudget.MaxThinkingTextBytes); err != nil {
			m.failStreamBudget(err)
			return
		}
		m.turnThinking += ev.Text
		m.turnThinkSig += ev.ThinkingSig
		if ev.Text != "" {
			if len(m.blocks) == 0 || m.blocks[len(m.blocks)-1].kind != "thinking" {
				m.blocks = append(m.blocks, block{kind: "thinking"})
			}
			last := len(m.blocks) - 1
			m.blocks[last].body += ev.Text
			m.invalidateBlockCache(last)
		}

	case agent.EvToolCallStart:
		if ev.ToolCall == nil {
			return
		}
		m.blocks = append(m.blocks, block{
			kind:      "tool",
			toolID:    ev.ToolCall.ID,
			toolName:  ev.ToolCall.Name,
			startedAt: time.Now(),
		})

	case agent.EvToolCallArgsDelta:
		if len(m.blocks) == 0 {
			return
		}
		last := &m.blocks[len(m.blocks)-1]
		if last.kind == "tool" {
			last.toolArgs += ev.ToolArgsDelta
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
				m.blocks[i].toolArgs = string(ev.ToolCall.Input)
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
		if m.session != nil {
			if err := m.session.NextTurn(); err != nil {
				m.appendBlock(block{kind: "system", body: "turn boundary failed: " + err.Error()})
			}
		}
		m.state = stateIdle
		// Drain any queued follow-up message the user typed while the
		// previous turn was streaming. The block was already appended
		// at queue-time for immediate visual feedback; drain just
		// adds the message to m.msgs (the LLM-facing history) and
		// kicks the next turn. Slash commands route through
		// handleSlash. Queued prompts bypass the hard-threshold gate
		// on the theory that if the user decided to queue something
		// mid-stream, they can react to the block on arrival.
		if m.queuedPrompt != "" {
			return m.promoteQueuedPrompt()
		}
		return nil
	}

	m.pendingCalls = append([]agent.ToolUseBlock{}, m.turnToolCalls...)
	m.pendingResults = nil
	return m.advanceToolQueue()
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
	_, ok := m.turnAllowed[name]
	return ok
}

func (m *Model) rejectUnavailableTool(call agent.ToolUseBlock) {
	content := unavailableToolContent(call.Name)
	for i := len(m.blocks) - 1; i >= 0; i-- {
		if m.blocks[i].kind == "tool" && m.blocks[i].toolID == call.ID {
			m.blocks[i].toolResult = content
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
	// Tools operate on the user's launch CWD, not the session audit
	// worktree. Same model as `stado run` default. The worktree is
	// where turn-boundary tree commits live (m.session.WorktreePath); it
	// is NOT the agent's working directory.
	host := hostAdapter{
		workdir: m.cwd,
		readLog: m.executor.ReadLog,
		runner:  m.executor.Runner,
		approval: tuiApprovalBridge{
			model: m,
		},
		spawn: m.buildSubagentSpawner(),
	}
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
		res, err := m.executor.Run(ctx, call.Name, call.Input, host)
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
		return toolResultMsg{result: agent.ToolResultBlock{
			ToolUseID: call.ID,
			Content:   content,
			IsError:   isErr,
		}}
	}
}

func (m *Model) buildSubagentSpawner() func(context.Context, subagent.Request) (subagent.Result, error) {
	if m.cfg == nil || m.session == nil || m.provider == nil {
		return nil
	}
	runner := runtime.SubagentRunner{
		Config:               m.cfg,
		Parent:               m.session,
		Provider:             m.provider,
		Model:                m.model,
		Thinking:             m.cfg.Agent.Thinking,
		ThinkingBudgetTokens: m.cfg.Agent.ThinkingBudgetTokens,
		System:               m.systemPrompt,
		SystemTemplate:       m.systemPromptTemplate,
		AgentName:            "stado-tui-subagent",
		OnEvent: func(ev runtime.SubagentEvent) {
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
		},
	}
	return runner.SpawnSubagent
}

// toolDefs builds the tool-definition list for the current turn request. An
// empty registry (no session) returns nil so the provider runs pure chat.
//
// In Plan mode only NonMutating tools are exposed — the model can grep/read/
// look-up-defs to form a plan, but can't edit/write/bash. This is the
// principled enforcement (no approval-loop workaround): the model literally
// doesn't see the mutating tools as available, so it produces analysis
// rather than asking to execute.
func (m *Model) toolDefs() []agent.ToolDef {
	visible := m.visibleTools()
	out := make([]agent.ToolDef, 0, len(visible))
	for _, t := range visible {
		schema, _ := json.Marshal(t.Schema())
		out = append(out, agent.ToolDef{
			Name:        t.Name(),
			Description: t.Description(),
			Schema:      schema,
		})
	}
	return out
}

func (m *Model) visibleTools() []tool.Tool {
	if m.executor == nil {
		return nil
	}
	all := m.executor.Registry.All()
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
	if m.sessionToolOverrides.isZero() {
		return pool
	}
	out := make([]tool.Tool, 0, len(pool))
	for _, t := range pool {
		if m.sessionToolOverrideHidesTool(t.Name()) {
			continue
		}
		out = append(out, t)
	}
	return out
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
		sb.WriteString(fmt.Sprintf("thresholds: soft %.0f%% · hard %.0f%%\n",
			100*m.ctxSoftThreshold, 100*m.ctxHardThreshold))
		switch {
		case fraction >= m.ctxHardThreshold:
			sb.WriteString("status: above hard threshold — consider /compact or `stado session fork <id> --at turns/<N>` in another shell.\n")
		case fraction >= m.ctxSoftThreshold:
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

	// Cost / budget. Cost is always shown; budget caps only when set.
	sb.WriteString(fmt.Sprintf("cost: $%.4f\n", m.usage.CostUSD))
	if m.budgetWarnUSD > 0 || m.budgetHardUSD > 0 {
		w := "(unset)"
		if m.budgetWarnUSD > 0 {
			w = fmt.Sprintf("$%.2f", m.budgetWarnUSD)
		}
		h := "(unset)"
		if m.budgetHardUSD > 0 {
			h = fmt.Sprintf("$%.2f", m.budgetHardUSD)
		}
		sb.WriteString(fmt.Sprintf("budget: warn=%s · hard=%s", w, h))
		if m.budgetAcked {
			sb.WriteString(" · ack")
		}
		sb.WriteString("\n")
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
	if m.state != stateIdle {
		m.appendBlock(block{kind: "system", body: "compaction: busy — wait for the current turn to finish"})
		return nil
	}
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

// installedAutoCompact returns the `auto-compact-<version>` directory
// name when a plugin matching that naming pattern is installed under
// $XDG_DATA_HOME/stado/plugins/, or "" otherwise. Used by the
// hard-threshold advisory to offer `/plugin:auto-compact-<ver> compact`
// as a one-click recovery when the plugin is available.
//
// Picks the lexicographically-latest version if multiple are
// installed — simple heuristic that matches install-order in
// practice (version bumps go forward).
func (m *Model) installedAutoCompact() string {
	cfg, err := config.Load()
	if err != nil {
		return ""
	}
	// EP-0035: search all plugin roots (global + project .stado/plugins/).
	var entries []string
	for _, root := range cfg.AllPluginDirs() {
		dirs, err2 := plugins.ListInstalledDirs(root)
		if err2 == nil {
			entries = append(entries, dirs...)
		}
	}
	if err != nil {
		return ""
	}
	latest := ""
	for _, name := range entries {
		if !strings.HasPrefix(name, "auto-compact-") {
			continue
		}
		if name > latest {
			latest = name
		}
	}
	return latest
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
// command (if any) with a JSON payload on stdin. No-op when the
// hook isn't configured. Errors / timeouts are logged by the hook
// runner; never propagated — the turn is over.
func (m *Model) firePostTurnHook() {
	if m.hookRunner.PostTurnCmd == "" || m.hookRunner.Disabled {
		return
	}
	duration := time.Duration(0)
	if !m.turnStart.IsZero() {
		duration = time.Since(m.turnStart)
	}
	m.hookRunner.FirePostTurn(m.rootCtx, hooks.NewPostTurnPayload(len(m.msgs), m.usage, m.turnText, duration))
}

// maybeEmitBudgetWarning fires a one-time system block once cumulative
// cost crosses the warn cap, so users don't keep seeing the same
// notice every turn. Called from handleStreamEvent on every Usage
// update.
func (m *Model) maybeEmitBudgetWarning() {
	if m.budgetWarnUSD <= 0 || m.budgetWarned {
		return
	}
	if m.usage.CostUSD < m.budgetWarnUSD {
		return
	}
	m.budgetWarned = true
	cap := m.budgetWarnUSD
	hint := ""
	if m.budgetHardUSD > 0 {
		hint = fmt.Sprintf(" — hard cap at $%.2f", m.budgetHardUSD)
	}
	m.appendBlock(block{
		kind: "system",
		body: fmt.Sprintf("budget warning: cost $%.2f crossed warn cap $%.2f%s", m.usage.CostUSD, cap, hint),
	})
	m.renderBlocks()
}
