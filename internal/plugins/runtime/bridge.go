package runtime

// Bridge implementations that plug real stado subsystems (the git-native
// Session + an agent.Provider + the session event stream) into the
// SessionBridge interface host imports call through. Kept in its own
// file so a future in-memory / testing bridge can sit alongside without
// churning host.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/pkg/agent"
)

// SessionBridgeImpl is the production SessionBridge: pulls conversation
// history + metadata from a live *stadogit.Session, forks via the same
// Session primitive stado's own CLI uses, and calls the active provider
// for LLM invocations. Event notifications arrive over a channel the
// caller (runtime / TUI) fills as turns complete.
//
// One instance per plugin instantiation — the token-usage counter in
// the Host enforces per-session budgets, and events are scoped to the
// session this bridge observes.
type SessionBridgeImpl struct {
	Session     *stadogit.Session
	Provider    agent.Provider
	Model       string
	PluginName  string                 // for Plugin: audit trailer
	MessagesFn  func() []agent.Message // snapshot of current conversation
	TokensFn    func() int             // current input-token count
	LastTurnRef func() string          // ref like refs/sessions/<id>/turns/N
	ForkFn      func(ctx context.Context, atTurn, seed string) (string, error)

	// Events is the channel the runtime pushes turn-boundary +
	// plugin-visible events into. NextEvent pops one per call; when
	// empty it returns ([]byte{}, nil) so the plugin sees the
	// "yield" signal (import returns 0, plugin backs off).
	Events chan []byte
}

// NewSessionBridge wires a SessionBridgeImpl against real subsystems.
// MessagesFn / TokensFn / LastTurnRef / ForkFn are callbacks because
// this file stays out of the TUI and runtime import graphs — those
// packages construct the bridge and pass closures that read their
// own state.
func NewSessionBridge(sess *stadogit.Session, prov agent.Provider, model string) *SessionBridgeImpl {
	return &SessionBridgeImpl{
		Session:  sess,
		Provider: prov,
		Model:    model,
		Events:   make(chan []byte, 16),
	}
}

// Emit enqueues one event payload to the bridge's Events channel.
// Non-blocking — dropped when the buffer is full so a wedged plugin
// can't pressure-stall the host. Caller typically wraps this in a
// helper like emitTurnBoundary(ev) that formats the payload.
func (b *SessionBridgeImpl) Emit(payload []byte) {
	select {
	case b.Events <- payload:
	default:
		// Buffer full — drop. A slog.Warn inside the host import
		// would be too noisy; the bridge's own caller tracks drops.
	}
}

// NextEvent implements SessionBridge. Non-blocking pop — returns
// ([]byte{}, nil) when no event is ready so the wasm plugin sees
// "0 = yield" rather than blocking on a system call.
func (b *SessionBridgeImpl) NextEvent(ctx context.Context) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case ev := <-b.Events:
		return ev, nil
	default:
		return nil, nil
	}
}

// ReadField implements SessionBridge. Field names match DESIGN
// §"Plugin extension points for context management":
//
//	message_count   → decimal ASCII int
//	token_count     → decimal ASCII int
//	session_id      → session ID string
//	last_turn_ref   → turn tag ref
//	history         → JSON array of {role, text}
//
// Unknown fields return an error so plugins get a deterministic
// "field X is not a valid session attribute" signal rather than
// silently-empty bytes.
func (b *SessionBridgeImpl) ReadField(name string) ([]byte, error) {
	switch name {
	case "message_count":
		if b.MessagesFn == nil {
			return nil, errors.New("message_count: no MessagesFn wired on bridge")
		}
		return []byte(strconv.Itoa(len(b.MessagesFn()))), nil
	case "token_count":
		if b.TokensFn == nil {
			return []byte("0"), nil
		}
		return []byte(strconv.Itoa(b.TokensFn())), nil
	case "session_id":
		if b.Session == nil {
			return nil, errors.New("session_id: no session")
		}
		return []byte(b.Session.ID), nil
	case "last_turn_ref":
		if b.LastTurnRef == nil {
			return []byte{}, nil
		}
		return []byte(b.LastTurnRef()), nil
	case "history":
		if b.MessagesFn == nil {
			return nil, errors.New("history: no MessagesFn wired on bridge")
		}
		return marshalHistory(b.MessagesFn())
	default:
		return nil, fmt.Errorf("session_read: unknown field %q (see DESIGN §\"Plugin extension points\")", name)
	}
}

// Fork implements SessionBridge via the caller-supplied ForkFn. This
// indirection keeps the bridge out of runtime/session import loops —
// the caller (which already knows how to build a new session rooted
// at a turn tag) supplies a closure.
func (b *SessionBridgeImpl) Fork(ctx context.Context, atTurnRef, seedMessage string) (string, error) {
	if b.ForkFn == nil {
		return "", errors.New("session_fork: no ForkFn wired on bridge")
	}
	return b.ForkFn(ctx, atTurnRef, seedMessage)
}

// InvokeLLM implements SessionBridge. One-shot completion: builds a
// minimal TurnRequest with the plugin's prompt as a single user
// message, drains the provider's event stream into an aggregated
// reply, and returns (reply, tokens, err). Tokens are estimated from
// reported usage when the provider emits it; falls back to a byte
// count / 4 heuristic when not reported so budget enforcement still
// pushes back on runaway plugins.
//
// On success, commits a trace-ref record with a `Plugin:` trailer
// attributing the call to PluginName — DESIGN invariant 3
// ("plugin-triggered actions are audited"). Commit failures are
// logged via stadogit's OnCommit hook but do not fail the call — a
// degraded audit trail is preferable to a plugin that can't invoke
// the model at all.
func (b *SessionBridgeImpl) InvokeLLM(ctx context.Context, prompt string, opts LLMInvokeOpts) (string, int, error) {
	if b.Provider == nil {
		return "", 0, errors.New("llm_invoke: no provider on bridge")
	}
	model := b.Model
	if opts.Model != "" {
		model = opts.Model
	}
	req := agent.TurnRequest{
		Model:    model,
		Messages: []agent.Message{agent.Text(agent.RoleUser, prompt)},
		System:   opts.System, // persona resolution lives one layer up; bridge takes the raw system body
	}
	if opts.MaxTokens > 0 {
		req.MaxTokens = opts.MaxTokens
	}
	if opts.Temperature > 0 {
		t := opts.Temperature
		req.Temperature = &t
	}
	ch, err := b.Provider.StreamTurn(ctx, req)
	if err != nil {
		return "", 0, fmt.Errorf("llm_invoke: %w", err)
	}
	var reply, tokens, inTokens, outTokens = "", 0, 0, 0
	for ev := range ch {
		switch ev.Kind {
		case agent.EvTextDelta:
			reply += ev.Text
		case agent.EvUsage:
			if ev.Usage != nil {
				inTokens = ev.Usage.InputTokens
				outTokens = ev.Usage.OutputTokens
				tokens = inTokens + outTokens
			}
		case agent.EvDone:
			// stream-complete marker — nothing to do, loop will exit
		}
	}
	if tokens == 0 {
		// Fallback estimate: ~4 bytes/token is the rule of thumb for
		// English prose. Enough to push back on runaway loops; real
		// providers report exact numbers via EvUsage.
		tokens = (len(prompt) + len(reply) + 3) / 4
		inTokens = (len(prompt) + 3) / 4
		outTokens = tokens - inTokens
	}

	// Trace-audit the invocation. Best-effort: errors are swallowed
	// because the LLM call itself already succeeded, and a
	// conversation-level failure shouldn't kill the plugin.
	if b.Session != nil && b.PluginName != "" {
		_, _ = b.Session.CommitToTrace(stadogit.CommitMeta{
			Tool:      "llm_invoke",
			ShortArg:  trimForCommit(prompt, 40),
			Summary:   "plugin LLM call",
			TokensIn:  inTokens,
			TokensOut: outTokens,
			Model:     b.Model,
			Agent:     "plugin:" + b.PluginName,
			Plugin:    b.PluginName,
			Turn:      b.Session.Turn(),
		})
	}

	return reply, tokens, nil
}

// trimForCommit truncates a string for the commit title's ShortArg
// column. Single-line, runes-bounded, ellipsis-terminated — matches
// the style of other shortArgOf callers so `git log --oneline`
// alignment stays sane.
func trimForCommit(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max < 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
}

// marshalHistory flattens the live conversation into a compact JSON
// array the plugin can deserialise. Each element is {role, text}
// where text concatenates every TextBlock in the message's Content.
// Tool-use / tool-result / image / thinking blocks are summarised as
// placeholder tags so the plugin sees they existed without having to
// parse stado's internal block shape.
func marshalHistory(msgs []agent.Message) ([]byte, error) {
	type entry struct {
		Role string `json:"role"`
		Text string `json:"text"`
	}
	out := make([]entry, 0, len(msgs))
	for _, m := range msgs {
		var text string
		for _, b := range m.Content {
			switch {
			case b.Text != nil:
				if text != "" {
					text += "\n"
				}
				text += b.Text.Text
			case b.ToolUse != nil:
				text += "[tool_use " + b.ToolUse.Name + "]"
			case b.ToolResult != nil:
				text += "[tool_result]"
			case b.Image != nil:
				text += "[image]"
			case b.Thinking != nil:
				text += "[thinking]"
			}
		}
		out = append(out, entry{Role: string(m.Role), Text: text})
	}
	return json.Marshal(out)
}
