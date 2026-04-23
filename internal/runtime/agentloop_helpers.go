package runtime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"hash/fnv"
	"log/slog"

	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/foobarto/stado/pkg/tool"
)

// wantThinking resolves the agent-loop Thinking knob against the
// provider's capability. Empty / "auto" → enable iff supported. "on"
// → enable unconditionally. "off" → always disabled.
func wantThinking(mode string, supported bool) bool {
	switch mode {
	case "on":
		return true
	case "off":
		return false
	default: // "", "auto"
		return supported
	}
}

// stripImageBlocks removes Image blocks from every message. Logs a
// slog.Warn per drop so callers notice when vision-laden input is
// being sent to a non-vision provider — better than a silent pass
// through that fails at provider-side with a less-specific error.
func stripImageBlocks(msgs []agent.Message, providerName string) []agent.Message {
	dropped := 0
	out := make([]agent.Message, len(msgs))
	for i, m := range msgs {
		if !hasImage(m.Content) {
			out[i] = m
			continue
		}
		filtered := make([]agent.Block, 0, len(m.Content))
		for _, b := range m.Content {
			if b.Image != nil {
				dropped++
				continue
			}
			filtered = append(filtered, b)
		}
		out[i] = agent.Message{Role: m.Role, Content: filtered}
	}
	if dropped > 0 {
		slog.Warn("stado.runtime.vision_not_supported",
			slog.String("provider", providerName),
			slog.Int("image_blocks_dropped", dropped),
		)
	}
	return out
}

func hasImage(blocks []agent.Block) bool {
	for _, b := range blocks {
		if b.Image != nil {
			return true
		}
	}
	return false
}

// hashMessagesPrefix returns a short, stable fingerprint of msgs[:n]. Used by
// the append-only guardrail to detect in-place mutation of prior turns
// between StreamTurn calls. Hashes the JSON encoding; Go's encoding/json
// sorts map keys so ordering within Block/Message is deterministic.
func hashMessagesPrefix(msgs []agent.Message, n int) string {
	if n > len(msgs) {
		n = len(msgs)
	}
	h := fnv.New64a()
	enc := json.NewEncoder(h)
	for i := 0; i < n; i++ {
		_ = enc.Encode(msgs[i])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// collectTurn drains an event stream into (assistant_text, tool_calls,
// usage, err). usage is the final EvDone.Usage on providers that
// report it; zero value if the provider emits neither EvDone nor a
// Usage payload.
func collectTurn(ch <-chan agent.Event, onEvent func(agent.Event)) (string, []agent.ToolUseBlock, agent.Usage, error) {
	var text string
	var calls []agent.ToolUseBlock
	var usage agent.Usage
	for ev := range ch {
		if onEvent != nil {
			onEvent(ev)
		}
		switch ev.Kind {
		case agent.EvTextDelta:
			text += ev.Text
		case agent.EvToolCallEnd:
			if ev.ToolCall != nil {
				calls = append(calls, *ev.ToolCall)
			}
		case agent.EvDone:
			if ev.Usage != nil {
				usage = *ev.Usage
			}
		case agent.EvError:
			return text, calls, usage, ev.Err
		}
	}
	return text, calls, usage, nil
}

type autoApproveHost struct {
	workdir string
	readLog *tools.ReadLog
	runner  sandbox.Runner
}

func (h autoApproveHost) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}

func (h autoApproveHost) Workdir() string        { return h.workdir }
func (h autoApproveHost) Runner() sandbox.Runner { return h.runner }

func (h autoApproveHost) PriorRead(key tool.ReadKey) (tool.PriorReadInfo, bool) {
	if h.readLog == nil {
		return tool.PriorReadInfo{}, false
	}
	return h.readLog.PriorRead(key)
}

func (h autoApproveHost) RecordRead(key tool.ReadKey, info tool.PriorReadInfo) {
	if h.readLog == nil {
		return
	}
	h.readLog.RecordRead(key, info)
}
