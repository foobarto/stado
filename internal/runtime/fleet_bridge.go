package runtime

// fleetBridge implements plugins/runtime.FleetBridge, wrapping the
// runtime's Fleet + SubagentRunner for the bundled agent plugin. EP-0038c.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	brokerbudget "github.com/foobarto/stado/internal/broker/budget"
	"github.com/foobarto/stado/internal/broker/mailbox"
	"github.com/foobarto/stado/internal/broker/retained"
	"github.com/foobarto/stado/internal/orchestration"
	pluginRuntime "github.com/foobarto/stado/internal/plugins/runtime"
	"github.com/foobarto/stado/internal/subagent"
	"github.com/google/uuid"
)

// FleetBridgeAdapter wires a Fleet + Spawner into the pluginRuntime.FleetBridge
// interface consumed by the bundled agent plugin's stado_agent_* imports.
type FleetBridgeAdapter struct {
	Fleet   *Fleet
	Spawner Spawner
	// RootCtx is the long-running context the Fleet was created with.
	RootCtx           context.Context
	Retained          *orchestration.Coordinator
	RetainedAccountID string
	Principal         string
	ParentSessionID   string
	ResolveForkPoint  func(context.Context, pluginRuntime.AgentSpawnRequest) (retained.ForkPoint, error)
}

var _ pluginRuntime.FleetBridge = (*FleetBridgeAdapter)(nil)

func (a *FleetBridgeAdapter) AgentSpawn(ctx context.Context, req pluginRuntime.AgentSpawnRequest) (pluginRuntime.AgentSpawnResult, error) {
	if req.Prompt == "" {
		return pluginRuntime.AgentSpawnResult{}, fmt.Errorf("prompt is required")
	}
	if a.Fleet == nil {
		return pluginRuntime.AgentSpawnResult{}, fmt.Errorf("agent fleet is unavailable")
	}
	if req.ParentSession != "" && a.ParentSessionID != "" && req.ParentSession != a.ParentSessionID {
		return pluginRuntime.AgentSpawnResult{}, fmt.Errorf("parent_session is host-bound and cannot be changed")
	}
	if req.SandboxProfile != "" {
		return pluginRuntime.AgentSpawnResult{}, fmt.Errorf("sandbox_profile is operator policy and cannot be selected by an agent")
	}
	if len(req.NarrowTools) == 0 && len(req.AllowedTools) > 0 {
		req.NarrowTools = append([]string(nil), req.AllowedTools...)
	}
	req.Provider = strings.TrimSpace(req.Provider)
	req.Model = strings.TrimSpace(req.Model)
	req.Thinking = strings.TrimSpace(req.Thinking)
	req.ReasoningEffort = strings.TrimSpace(req.ReasoningEffort)
	req.Execution = strings.TrimSpace(req.Execution)
	if req.Execution == "" {
		req.Execution = "wait"
	}
	if req.Execution != "wait" && req.Execution != "retained" {
		return pluginRuntime.AgentSpawnResult{}, fmt.Errorf("execution must be wait or retained")
	}
	if req.Ephemeral && req.Execution == "retained" {
		return pluginRuntime.AgentSpawnResult{}, fmt.Errorf("ephemeral and retained execution are mutually exclusive")
	}
	profile := subagent.Request{
		Provider: req.Provider, Model: req.Model, Thinking: req.Thinking,
		ThinkingBudgetTokens: req.ThinkingBudgetTokens, ReasoningEffort: req.ReasoningEffort,
		TokenBudget: req.TokenBudget,
	}
	if profile.TokenBudget == 0 {
		profile.TokenBudget = subagent.DefaultTokenBudget
	}
	if err := subagent.ValidateProviderProfile(profile); err != nil {
		return pluginRuntime.AgentSpawnResult{}, err
	}
	claimScope, requestDigest, err := agentSpawnClaim(req)
	if err != nil {
		return pluginRuntime.AgentSpawnResult{}, err
	}
	opts := SpawnOptions{
		Provider:             req.Provider,
		Model:                req.Model,
		Thinking:             req.Thinking,
		ThinkingBudgetTokens: req.ThinkingBudgetTokens,
		ReasoningEffort:      req.ReasoningEffort,
		ParentSessionID:      a.ParentSessionID,
		Persona:              req.Persona,
		Role:                 req.Role,
		Mode:                 req.Mode,
		Ownership:            req.Ownership,
		WriteScope:           req.WriteScope,
		MaxTurns:             req.MaxTurns,
		TimeoutSeconds:       req.TimeoutSeconds,
		ToolProfile:          req.ToolProfile,
		NarrowTools:          req.NarrowTools,
		TokenBudget:          req.TokenBudget,
		Execution:            req.Execution,
		ChildToolOwner:       req.ChildToolOwner,
	}
	if req.Source != nil {
		opts.Source = &subagent.Source{SessionID: req.Source.SessionID, At: req.Source.At}
	}
	rootCtx := a.RootCtx
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	admission, err := a.Fleet.ClaimSpawn(ctx, claimScope, req.IdempotencyKey, requestDigest, func() (FleetSpawnAdmission, error) {
		if req.Execution == "retained" {
			spawnReq := req
			spawnReq.Async = true
			result, spawnErr := a.spawnRetained(rootCtx, spawnReq, opts)
			return FleetSpawnAdmission{ID: result.ID, SessionID: result.SessionID, Status: result.Status}, spawnErr
		}
		fleetID, spawnErr := a.Fleet.Spawn(rootCtx, a.Spawner, req.Prompt, opts)
		if spawnErr != nil {
			return FleetSpawnAdmission{}, spawnErr
		}
		result := FleetSpawnAdmission{ID: fleetID, Status: string(FleetStatusRunning)}
		if entry, ok := a.Fleet.Get(fleetID); ok {
			result.SessionID = entry.SessionID
		}
		return result, nil
	})
	if err != nil {
		return pluginRuntime.AgentSpawnResult{}, err
	}
	result := pluginRuntime.AgentSpawnResult{
		ID: admission.ID, SessionID: admission.SessionID, Status: admission.Status,
	}

	if req.Async {
		return result, nil
	}
	if req.Execution == "retained" {
		return a.waitRetained(ctx, result)
	}

	// Sync mode: poll until done or context cancelled.
	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		entry, ok := a.Fleet.Get(result.ID)
		if !ok {
			return result, fmt.Errorf("agent %s disappeared from fleet", result.ID)
		}
		result.SessionID = entry.SessionID
		switch entry.Status {
		case FleetStatusCompleted:
			result.Status = string(FleetStatusCompleted)
			result.FinalText = entry.Result
			result.Terminal = pluginAgentTerminal(entry.Terminal)
			return result, nil
		case FleetStatusError:
			return result, fmt.Errorf("agent error: %s", entry.Error)
		case FleetStatusCancelled:
			return result, fmt.Errorf("agent cancelled")
		}
	}
}

func agentSpawnClaim(req pluginRuntime.AgentSpawnRequest) (string, string, error) {
	if req.IdempotencyKey == "" {
		return "", "", nil
	}
	if len(req.IdempotencyKey) > 256 || !utf8.ValidString(req.IdempotencyKey) || strings.TrimSpace(req.IdempotencyKey) == "" {
		return "", "", fmt.Errorf("invalid idempotency_key")
	}
	for _, r := range req.IdempotencyKey {
		if unicode.IsControl(r) {
			return "", "", fmt.Errorf("invalid idempotency_key")
		}
	}
	if req.Caller.PluginID == "" || req.Caller.SessionID == "" || req.Caller.Generation == 0 {
		return "", "", fmt.Errorf("idempotent agent spawn requires authenticated application scope")
	}
	normalized := req
	normalized.IdempotencyKey = ""
	normalized.Caller = pluginRuntime.AgentSpawnCaller{}
	payload, err := json.Marshal(normalized)
	if err != nil {
		return "", "", fmt.Errorf("encode normalized agent spawn: %w", err)
	}
	digest := sha256.Sum256(payload)
	scope := fmt.Sprintf("%s\x00%s\x00%d", req.Caller.PluginID, req.Caller.SessionID, req.Caller.Generation)
	return scope, hex.EncodeToString(digest[:]), nil
}

type spawnerLauncher struct {
	spawner   Spawner
	request   subagent.Request
	mailbox   *mailbox.Broker
	principal string
}

func (l spawnerLauncher) Launch(ctx context.Context, admission retained.Admission) (orchestration.LaunchResult, error) {
	req := l.request
	req.ChildSessionID, req.Execution = admission.ChildSessionID, "wait"
	spawner := l.spawner
	if aware, ok := spawner.(InboxAwareSpawner); ok && l.mailbox != nil {
		spawner = aware.WithInbox(func() []string {
			var inputs []string
			for {
				msg, found, err := l.mailbox.DeliverFrom(context.Background(), admission.ChildSessionID, admission.ParentSessionID, l.principal, "retained-runtime", "retained-deliver:"+uuid.NewString())
				if err != nil || !found {
					break
				}
				var body struct {
					Prompt string `json:"prompt"`
				}
				if json.Unmarshal(msg.Payload, &body) == nil && body.Prompt != "" {
					inputs = append(inputs, body.Prompt)
				}
				_, _ = l.mailbox.CommitReceiverInput(context.Background(), admission.ChildSessionID, msg.ID, msg.DeliveryGeneration, "retained-child-input:"+msg.ID, l.principal, "retained-runtime", "retained-child-ack:"+msg.ID)
			}
			return inputs
		})
	}
	res, err := spawner.SpawnSubagent(ctx, req)
	out := orchestration.LaunchResult{Usage: brokerbudget.Limits{Turns: 1}, FinalText: res.Text, Error: res.Error}
	if err != nil {
		out.Error = err.Error()
		lower := strings.ToLower(err.Error())
		out.Transient = ctx.Err() == nil && (strings.Contains(lower, "temporar") || strings.Contains(lower, "rate limit") || strings.Contains(lower, "connection") || strings.Contains(lower, "provider unavailable"))
	}
	return out, err
}

func (a *FleetBridgeAdapter) spawnRetained(ctx context.Context, req pluginRuntime.AgentSpawnRequest, opts SpawnOptions) (pluginRuntime.AgentSpawnResult, error) {
	if a.Retained == nil || a.ResolveForkPoint == nil || a.RetainedAccountID == "" || a.ParentSessionID == "" {
		return pluginRuntime.AgentSpawnResult{}, fmt.Errorf("retained execution is unavailable on this surface")
	}
	fork, err := a.ResolveForkPoint(ctx, req)
	if err != nil {
		return pluginRuntime.AgentSpawnResult{}, err
	}
	pinner, ok := a.Spawner.(ForkPointSpawner)
	if !ok {
		return pluginRuntime.AgentSpawnResult{}, fmt.Errorf("retained execution requires an exact fork-point spawner")
	}
	pinnedSpawner, err := pinner.PinSpawnForkPoint(ctx, SpawnForkPoint{
		SourceSessionID:    fork.SourceSessionID,
		SourceGeneration:   fork.SourceGeneration,
		CommittedTurn:      fork.CommittedTurn,
		ConversationDigest: fork.ConversationDigest,
		CompactionLineage:  fork.CompactionLineage,
		TreeCommit:         fork.TreeCommit,
		TraceCommit:        fork.TraceCommit,
		EventSequence:      fork.EventSequence,
	})
	if err != nil {
		return pluginRuntime.AgentSpawnResult{}, fmt.Errorf("pin retained fork point: %w", err)
	}
	if pinnedSpawner == nil {
		return pluginRuntime.AgentSpawnResult{}, fmt.Errorf("pin retained fork point returned nil spawner")
	}
	childID := uuid.NewString()
	request := subagent.Request{Prompt: req.Prompt, Role: opts.Role, Mode: opts.Mode, Ownership: opts.Ownership, WriteScope: opts.WriteScope, MaxTurns: opts.MaxTurns, TimeoutSeconds: opts.TimeoutSeconds, Persona: opts.Persona, Source: opts.Source, Provider: opts.Provider, Model: opts.Model, Thinking: opts.Thinking, ThinkingBudgetTokens: opts.ThinkingBudgetTokens, ReasoningEffort: opts.ReasoningEffort, ToolProfile: opts.ToolProfile, NarrowTools: opts.NarrowTools, TokenBudget: opts.TokenBudget, Execution: "retained", ChildToolOwner: opts.ChildToolOwner}
	request, err = prepareSubagentRequest(request)
	if err != nil {
		return pluginRuntime.AgentSpawnResult{}, err
	}
	ceilingRaw, _ := json.Marshal(request)
	ceiling := sha256.Sum256(ceilingRaw)
	idem := "retained-spawn:" + uuid.NewString()
	budget := brokerbudget.Limits{Tokens: uint64(request.TokenBudget), Turns: uint64(request.MaxTurns), WallSeconds: uint64(request.TimeoutSeconds)}
	restartPolicy := retained.RestartPolicy{}
	if req.Supervision == "on_transient_failure" {
		if req.MaxRestarts <= 0 {
			req.MaxRestarts = 2
		}
		if req.MaxRestarts > 5 {
			req.MaxRestarts = 5
		}
		restartPolicy = retained.RestartPolicy{Mode: req.Supervision, MaxRestarts: req.MaxRestarts, Window: 10 * time.Minute, BaseBackoff: 250 * time.Millisecond, MaxBackoff: 10 * time.Second}
	}
	h, err := a.Retained.SpawnRetained(ctx, orchestration.LaunchRequest{AccountID: a.RetainedAccountID, Budget: budget, Principal: a.Principal, Actor: a.ParentSessionID, IdempotencyKey: idem, RestartPolicy: restartPolicy, Launcher: spawnerLauncher{spawner: pinnedSpawner, request: request, mailbox: a.Retained.Mailbox, principal: a.Principal}, Admission: retained.Request{ParentSessionID: a.ParentSessionID, ChildSessionID: childID, Purpose: request.Role, Fork: fork, CeilingDigest: hex.EncodeToString(ceiling[:]), Model: req.Model, ToolProfile: req.ToolProfile, Principal: a.Principal, Actor: a.ParentSessionID, IdempotencyKey: idem + ":admission"}})
	if err != nil {
		return pluginRuntime.AgentSpawnResult{}, err
	}
	result := pluginRuntime.AgentSpawnResult{ID: h.AdmissionID, SessionID: h.ChildSessionID, Status: string(h.Status)}
	if req.Async {
		return result, nil
	}
	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		aState, ok, getErr := a.Retained.Registry.Get(h.AdmissionID)
		if getErr != nil || !ok {
			return result, fmt.Errorf("retained child disappeared: %w", getErr)
		}
		result.Status = string(aState.Status)
		if aState.Status == retained.StatusCompleted {
			msgs, readErr := a.readRetained(ctx, aState, 0)
			if readErr == nil && len(msgs.Messages) > 0 {
				result.FinalText = msgs.Messages[0].Content
			}
			return result, nil
		}
		if aState.Status == retained.StatusFailed || aState.Status == retained.StatusCancelled || aState.Status == retained.StatusDown {
			return result, fmt.Errorf("retained child ended with status %s", aState.Status)
		}
	}
}

func (a *FleetBridgeAdapter) waitRetained(ctx context.Context, result pluginRuntime.AgentSpawnResult) (pluginRuntime.AgentSpawnResult, error) {
	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		aState, ok, getErr := a.Retained.Registry.Get(result.ID)
		if getErr != nil || !ok {
			return result, fmt.Errorf("retained child disappeared: %w", getErr)
		}
		result.Status = string(aState.Status)
		if aState.Status == retained.StatusCompleted {
			msgs, readErr := a.readRetained(ctx, aState, 0)
			if readErr == nil && len(msgs.Messages) > 0 {
				result.FinalText = msgs.Messages[0].Content
			}
			return result, nil
		}
		if aState.Status == retained.StatusFailed || aState.Status == retained.StatusCancelled || aState.Status == retained.StatusDown {
			return result, fmt.Errorf("retained child ended with status %s", aState.Status)
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
	if a.Retained != nil {
		children, err := a.Retained.Registry.List()
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			if child.ParentSessionID != a.ParentSessionID {
				continue
			}
			out = append(out, pluginRuntime.AgentListEntry{ID: child.ID, SessionID: child.ChildSessionID, Status: string(child.Status), Model: child.Model, StartedAt: child.CreatedAt.UTC().Format(time.RFC3339), LastTurnAt: child.UpdatedAt.UTC().Format(time.RFC3339)})
		}
	}
	return out, nil
}

func (a *FleetBridgeAdapter) AgentReadMessages(ctx context.Context, id string, since, timeoutMs int) (pluginRuntime.AgentMessages, error) {
	entry, ok := a.Fleet.Get(id)
	if !ok {
		if a.Retained != nil {
			if child, retainedOK, err := a.Retained.Registry.Get(id); err != nil {
				return pluginRuntime.AgentMessages{}, err
			} else if retainedOK && child.ParentSessionID == a.ParentSessionID {
				return a.readRetained(ctx, child, timeoutMs)
			}
		}
		return pluginRuntime.AgentMessages{}, fmt.Errorf("agent %q not found", id)
	}
	// Best-effort: return current result text as a single assistant message.
	// Full message-inbox polling (offset-based) is future work; this gives
	// the wasm plugin a usable surface now. EP-0038c TODO: wire real inbox.
	msgs := pluginRuntime.AgentMessages{
		Status: string(entry.Status),
		Offset: since,
	}
	if entry.Status != FleetStatusRunning {
		msgs.Terminal = pluginAgentTerminal(entry.Terminal)
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
				msgs.Terminal = pluginAgentTerminal(e.Terminal)
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

func pluginAgentTerminal(in subagent.TerminalMetadata) *pluginRuntime.AgentTerminalMetadata {
	out := &pluginRuntime.AgentTerminalMetadata{Usage: pluginRuntime.AgentTokenUsage{
		InputTokens: in.Usage.InputTokens, OutputTokens: in.Usage.OutputTokens,
		CacheReadTokens: in.Usage.CacheReadTokens, CacheWriteTokens: in.Usage.CacheWriteTokens,
	}, UsageComplete: in.UsageComplete}
	if in.Cleanup != nil {
		out.Cleanup = &pluginRuntime.AgentCleanupDiagnostic{
			Kind: in.Cleanup.Kind, Fingerprint: in.Cleanup.Fingerprint,
		}
	}
	return out
}

func (a *FleetBridgeAdapter) readRetained(ctx context.Context, child retained.Admission, timeoutMs int) (pluginRuntime.AgentMessages, error) {
	deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
	for {
		msg, ok, err := a.Retained.Mailbox.DeliverFrom(ctx, child.ParentSessionID, child.ChildSessionID, a.Principal, child.ParentSessionID, "retained-read:"+child.ID+":"+uuid.NewString())
		if err != nil {
			return pluginRuntime.AgentMessages{}, err
		}
		if ok {
			inputID := "retained-parent-input:" + msg.ID
			if _, err := a.Retained.Mailbox.CommitReceiverInput(ctx, child.ParentSessionID, msg.ID, msg.DeliveryGeneration, inputID, a.Principal, child.ParentSessionID, "retained-ack:"+msg.ID); err != nil {
				return pluginRuntime.AgentMessages{}, err
			}
			var body struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(msg.Payload, &body)
			return pluginRuntime.AgentMessages{Messages: []pluginRuntime.AgentMessage{{Role: "assistant", Content: body.Text, Offset: 0}}, Offset: 1, Status: string(child.Status)}, nil
		}
		if timeoutMs <= 0 || time.Now().After(deadline) {
			return pluginRuntime.AgentMessages{Status: string(child.Status)}, nil
		}
		select {
		case <-ctx.Done():
			return pluginRuntime.AgentMessages{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (a *FleetBridgeAdapter) AgentSendMessage(ctx context.Context, id, msg string) error {
	// Real impl: queue the message into the agent's inbox. The agent's
	// loop drains the inbox at the next turn boundary and prepends
	// pending messages as user-role inputs (see AgentLoopOptions.InboxFn).
	if _, ok := a.Fleet.Get(id); ok {
		return a.Fleet.SendMessage(id, msg)
	}
	if a.Retained != nil {
		if child, ok, err := a.Retained.Registry.Get(id); err != nil {
			return err
		} else if ok && child.ParentSessionID == a.ParentSessionID {
			if child.Status != retained.StatusRunning && child.Status != retained.StatusStarting && child.Status != retained.StatusAdmitted {
				return fmt.Errorf("retained agent %q is %s; resume by spawning a new child with source.session_id=%q", id, child.Status, child.ChildSessionID)
			}
			payload, _ := json.Marshal(map[string]string{"prompt": msg})
			_, err = a.Retained.FollowUp(ctx, child.ParentSessionID, orchestration.Handle{AdmissionID: child.ID, ChildSessionID: child.ChildSessionID, Generation: child.Generation, Status: child.Status}, payload, a.Principal, child.ParentSessionID, "retained-followup:"+uuid.NewString())
			return err
		}
	}
	return fmt.Errorf("agent %q not found", id)
}

func (a *FleetBridgeAdapter) AgentCancel(ctx context.Context, id string) error {
	if _, ok := a.Fleet.Get(id); ok {
		return a.Fleet.Cancel(id)
	}
	if a.Retained != nil {
		if child, ok, err := a.Retained.Registry.Get(id); err != nil {
			return err
		} else if ok && child.ParentSessionID == a.ParentSessionID {
			if a.Retained.Cancel(id) {
				return nil
			}
			return fmt.Errorf("retained agent %q is not running", id)
		}
	}
	return nil
}
