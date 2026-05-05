package runtime

// fleetBridge implements plugins/runtime.FleetBridge, wrapping the
// runtime's Fleet + SubagentRunner for the bundled agent plugin. EP-0038c.

import (
	"context"
	"fmt"
	"time"

	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
)

// FleetBridgeAdapter wires a Fleet + Spawner into the pluginRuntime.FleetBridge
// interface consumed by the bundled agent plugin's stado_agent_* imports.
type FleetBridgeAdapter struct {
	Fleet   *Fleet
	Spawner Spawner
	// RootCtx is the long-running context the Fleet was created with.
	RootCtx context.Context
}

var _ pluginRuntime.FleetBridge = (*FleetBridgeAdapter)(nil)

func (a *FleetBridgeAdapter) AgentSpawn(ctx context.Context, req pluginRuntime.AgentSpawnRequest) (pluginRuntime.AgentSpawnResult, error) {
	if req.Prompt == "" {
		return pluginRuntime.AgentSpawnResult{}, fmt.Errorf("prompt is required")
	}
	opts := SpawnOptions{
		Model: req.Model,
	}
	fleetID, err := a.Fleet.Spawn(a.RootCtx, a.Spawner, req.Prompt, opts)
	if err != nil {
		return pluginRuntime.AgentSpawnResult{}, err
	}
	result := pluginRuntime.AgentSpawnResult{
		ID:     fleetID,
		Status: string(FleetStatusRunning),
	}
	// Populate session ID once the goroutine has created it.
	if entry, ok := a.Fleet.Get(fleetID); ok {
		result.SessionID = entry.SessionID
	}

	if req.Async {
		return result, nil
	}

	// Sync mode: poll until done or context cancelled.
	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		entry, ok := a.Fleet.Get(fleetID)
		if !ok {
			return result, fmt.Errorf("agent %s disappeared from fleet", fleetID)
		}
		result.SessionID = entry.SessionID
		switch entry.Status {
		case FleetStatusCompleted:
			result.Status = string(FleetStatusCompleted)
			result.FinalText = entry.Result
			return result, nil
		case FleetStatusError:
			return result, fmt.Errorf("agent error: %s", entry.Error)
		case FleetStatusCancelled:
			return result, fmt.Errorf("agent cancelled")
		}
	}
}

func (a *FleetBridgeAdapter) AgentList(ctx context.Context) ([]pluginRuntime.AgentListEntry, error) {
	entries := a.Fleet.List()
	out := make([]pluginRuntime.AgentListEntry, len(entries))
	for i, e := range entries {
		out[i] = pluginRuntime.AgentListEntry{
			ID:        e.FleetID,
			SessionID: e.SessionID,
			Status:    string(e.Status),
			Model:     e.Model,
			StartedAt: e.StartedAt.UTC().Format(time.RFC3339),
		}
		if !e.LastActivity.IsZero() {
			out[i].LastTurnAt = e.LastActivity.UTC().Format(time.RFC3339)
		}
	}
	return out, nil
}

func (a *FleetBridgeAdapter) AgentReadMessages(ctx context.Context, id string, since, timeoutMs int) (pluginRuntime.AgentMessages, error) {
	entry, ok := a.Fleet.Get(id)
	if !ok {
		return pluginRuntime.AgentMessages{}, fmt.Errorf("agent %q not found", id)
	}
	// Best-effort: return current result text as a single assistant message.
	// Full message-inbox polling (offset-based) is future work; this gives
	// the wasm plugin a usable surface now. EP-0038c TODO: wire real inbox.
	msgs := pluginRuntime.AgentMessages{
		Status: string(entry.Status),
		Offset: since,
	}
	if entry.Result != "" {
		msgs.Messages = []pluginRuntime.AgentMessage{
			{Role: "assistant", Content: entry.Result, Offset: since},
		}
		msgs.Offset = since + 1
	}
	// If the agent is still running and caller wants to wait, poll briefly.
	if entry.Status == FleetStatusRunning && timeoutMs > 0 {
		deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
		for time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return msgs, nil
			case <-time.After(100 * time.Millisecond):
			}
			if e, ok := a.Fleet.Get(id); ok && e.Status != FleetStatusRunning {
				msgs.Status = string(e.Status)
				if e.Result != "" {
					msgs.Messages = []pluginRuntime.AgentMessage{
						{Role: "assistant", Content: e.Result, Offset: since},
					}
					msgs.Offset = since + 1
				}
				break
			}
		}
	}
	return msgs, nil
}

func (a *FleetBridgeAdapter) AgentSendMessage(ctx context.Context, id, msg string) error {
	// TODO: inject into session inbox when multi-producer inbox is wired.
	// For now, verify the agent exists and return success.
	if _, ok := a.Fleet.Get(id); !ok {
		return fmt.Errorf("agent %q not found", id)
	}
	return nil
}

func (a *FleetBridgeAdapter) AgentCancel(ctx context.Context, id string) error {
	return a.Fleet.Cancel(id)
}

