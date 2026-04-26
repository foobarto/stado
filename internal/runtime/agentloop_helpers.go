package runtime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/fnv"
	"log/slog"

	"github.com/foobarto/stado/internal/sandbox"
	stadogit "github.com/foobarto/stado/internal/state/git"
	"github.com/foobarto/stado/internal/streambudget"
	"github.com/foobarto/stado/internal/subagent"
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
	var thinkingBytes int
	for ev := range ch {
		switch ev.Kind {
		case agent.EvTextDelta:
			if err := streambudget.CheckAppend("assistant text", len(text), len(ev.Text), streambudget.MaxAssistantTextBytes); err != nil {
				return text, calls, usage, err
			}
		case agent.EvThinkingDelta:
			if err := streambudget.CheckAppend("assistant thinking", thinkingBytes, len(ev.Text), streambudget.MaxThinkingTextBytes); err != nil {
				return text, calls, usage, err
			}
			thinkingBytes += len(ev.Text)
		}
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
	spawn   func(context.Context, subagent.Request) (subagent.Result, error)
}

func (h autoApproveHost) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}

func (h autoApproveHost) Workdir() string        { return h.workdir }
func (h autoApproveHost) Runner() sandbox.Runner { return h.runner }

func (h autoApproveHost) SpawnSubagent(ctx context.Context, req subagent.Request) (subagent.Result, error) {
	if h.spawn == nil {
		return subagent.Result{}, errors.New("spawn_agent unavailable: current host does not support subagents")
	}
	return h.spawn(ctx, req)
}

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

func sessionFromExecutor(exec *tools.Executor) *stadogit.Session {
	if exec == nil {
		return nil
	}
	return exec.Session
}

func buildLoopSubagentSpawner(r SubagentRunner) func(context.Context, subagent.Request) (subagent.Result, error) {
	if r.Config == nil || r.Parent == nil || r.Provider == nil {
		return nil
	}
	return r.SpawnSubagent
}
