package runtime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"sync"

	"github.com/foobarto/stado/internal/plugins"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/plugins/runtime/pty"
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
func collectTurn(ch <-chan agent.Event, onEvent func(agent.Event) error, cancel func()) (string, []agent.ToolUseBlock, agent.Usage, error) {
	var text string
	var calls []agent.ToolUseBlock
	var usage agent.Usage
	var thinkingBytes int
	var collectErr error
	for ev := range ch {
		if collectErr != nil {
			continue
		}
		switch ev.Kind {
		case agent.EvTextDelta:
			if err := streambudget.CheckAppend("assistant text", len(text), len(ev.Text), streambudget.MaxAssistantTextBytes); err != nil {
				collectErr = err
			}
		case agent.EvThinkingDelta:
			if err := streambudget.CheckAppend("assistant thinking", thinkingBytes, len(ev.Text), streambudget.MaxThinkingTextBytes); err != nil {
				collectErr = err
			} else {
				thinkingBytes += len(ev.Text)
			}
		}
		if collectErr == nil && onEvent != nil {
			collectErr = onEvent(ev)
		}
		if collectErr != nil {
			if cancel != nil {
				cancel()
			}
			continue
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
			collectErr = ev.Err
			if cancel != nil {
				cancel()
			}
		}
	}
	return text, calls, usage, collectErr
}

type autoApproveHost struct {
	workdir     string
	readLog     *tools.ReadLog
	runner      sandbox.Runner
	spawn       func(context.Context, subagent.Request) (subagent.Result, error)
	fleetBridge *FleetBridgeAdapter
	// pty is the agent-loop-lifetime PTY manager shared with bundled
	// shell.* / pty.* tools so spawn / read / write across
	// dispatches see the same registry.
	pty *pty.Manager
	// defaultSandboxPolicy, when non-nil, is returned by DefaultSandboxPolicy
	// so bash/exec tool calls run confined by it. Callers set it through
	// AgentLoopOptions.DefaultSandboxPolicy; explicit sandbox opt-out leaves it
	// nil. Model A (decision 2026-06-13).
	defaultSandboxPolicy any
	provider             agent.Provider
	defaultModel         string
	broker               BrokerController
}

// sessionToolSurface is the workflow-neutral per-loop implementation behind
// the digest-fenced WASM surface-edit primitive.
type sessionToolSurface struct {
	mu        sync.RWMutex
	activated map[string]bool
	ceiling   map[string]bool
}

func (s *sessionToolSurface) AllowsToolSurface(name string) bool {
	return s != nil && s.ceiling[name]
}

func (s *sessionToolSurface) ApplyToolSurface(edit tool.ToolSurfaceEdit) error {
	if s == nil {
		return errors.New("session tool surface unavailable")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[string]string, len(edit.Activate)+len(edit.Deactivate))
	for _, group := range []struct {
		label string
		names []string
	}{{"activate", edit.Activate}, {"deactivate", edit.Deactivate}} {
		for _, name := range group.names {
			if !s.AllowsToolSurface(name) {
				return fmt.Errorf("tool %q is outside the session ceiling", name)
			}
			if prior := seen[name]; prior != "" {
				return fmt.Errorf("tool %q occurs more than once (%s, %s)", name, prior, group.label)
			}
			seen[name] = group.label
		}
	}
	if s.activated == nil {
		s.activated = make(map[string]bool)
	}
	for _, name := range edit.Activate {
		s.activated[name] = true
	}
	for _, name := range edit.Deactivate {
		delete(s.activated, name)
	}
	return nil
}

func (s *sessionToolSurface) names() map[string]bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]bool, len(s.activated))
	for name, active := range s.activated {
		out[name] = active
	}
	return out
}

func (h autoApproveHost) Approve(context.Context, tool.ApprovalRequest) (tool.Decision, error) {
	return tool.DecisionAllow, nil
}

func (h autoApproveHost) Workdir() string        { return h.workdir }
func (h autoApproveHost) Runner() sandbox.Runner { return h.runner }

func (h autoApproveHost) PluginProviderBridge(identityCanonical string) (pluginRuntime.ProviderBridge, error) {
	if identityCanonical == "" || h.provider == nil {
		return nil, errors.New("agent-loop provider bridge unavailable")
	}
	return &pluginRuntime.NativeProviderBridge{Provider: h.provider, DefaultModel: h.defaultModel}, nil
}

func (h autoApproveHost) EvidenceBrokerBinding(ctx context.Context, identity plugins.RuntimeIdentity, manifest plugins.Manifest, toolName string) (pluginRuntime.EvidenceBridgeBinding, error) {
	controller, ok := h.broker.(EvidenceBrokerController)
	if !ok || controller == nil {
		return pluginRuntime.EvidenceBridgeBinding{}, errors.New("evidence broker binding unavailable")
	}
	return controller.BindEvidence(ctx, identity, manifest, toolName)
}

// DefaultSandboxPolicy implements tool.SandboxPolicyProvider. The caller owns
// the policy decision; a nil value means explicit or legacy direct execution.
// Model A (decision 2026-06-13).
func (h autoApproveHost) DefaultSandboxPolicy() any { return h.defaultSandboxPolicy }

func (h autoApproveHost) SpawnSubagent(ctx context.Context, req subagent.Request) (subagent.Result, error) {
	if h.spawn == nil {
		return subagent.Result{}, errors.New("spawn_agent unavailable: current host does not support subagents")
	}
	return h.spawn(ctx, req)
}

// AgentFleetBridge implements tool.AgentFleetProvider so the wasm agent.*
// tools can reach the FleetBridgeAdapter when the loop auto-constructs a host.
func (h autoApproveHost) AgentFleetBridge() any {
	if h.fleetBridge == nil {
		return nil
	}
	return h.fleetBridge
}

// PTYManager implements pkg/tool.PTYProvider so bundled shell.* / pty.*
// wasm tools share a long-lived PTY registry across dispatches in the
// agent loop. Without this, shell.spawn → shell.read/write across calls
// would fail with "session not found" because each bundled-plugin Run
// would otherwise build a fresh pluginRuntime with its own manager.
func (h autoApproveHost) PTYManager() any { return h.pty }

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
