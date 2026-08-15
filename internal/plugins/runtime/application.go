package runtime

// Persistent WASM lifecycle applications implement EP-0064's generic
// application seam. This file deliberately knows nothing about supervise:
// manifests select hook points and durable event kinds, while the host only
// serializes one instance, authenticates its session anchor, validates narrow
// decisions, and enforces time/output bounds.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tetratelabs/wazero/api"

	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/internal/plugins"
)

const (
	lifecycleSchemaV1        = "stado.dev/lifecycle/v1"
	maxLifecyclePayloadBytes = 1 << 20
	maxSystemContribution    = 16 << 10
)

var applicationWorkerRunIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)

// ApplicationAnchor is supplied by the native session admission path and is
// injected into every callback. A guest cannot select a different session or
// generation by writing fields into its response.
type ApplicationAnchor struct {
	SessionID         string `json:"session_id"`
	SessionGeneration uint64 `json:"session_generation"`
	CanonicalRepoID   string `json:"canonical_repo_id,omitempty"`
}

func (a ApplicationAnchor) Validate() error {
	if a.SessionID == "" || a.SessionGeneration == 0 {
		return errors.New("lifecycle application requires an admitted session id and generation")
	}
	return nil
}

type lifecycleCallEnvelope struct {
	Schema      string            `json:"schema"`
	Point       hooks.Point       `json:"point"`
	Application string            `json:"application"`
	Anchor      ApplicationAnchor `json:"anchor"`
	Sequence    uint64            `json:"sequence"`
	Payload     hooks.Payload     `json:"payload"`
}

// ApplicationEvent is a broker-projected durable event. Data is bounded opaque
// event-specific JSON; the host, not the guest, supplies sequence and evidence
// references. Delivery/ack durability belongs to the dispatcher above this
// runtime primitive.
type ApplicationEvent struct {
	Kind         string          `json:"kind"`
	BrokerSeq    uint64          `json:"broker_sequence"`
	EvidenceRefs []string        `json:"evidence_refs,omitempty"`
	Data         json.RawMessage `json:"data"`
}

// ApplicationEventTransport is the native-held durable dispatcher bridge.
// Pending event kinds come from the signed manifest stored at broker admission;
// WASM receives individual bounded events only through stado_plugin_event and
// never sees the opaque binding or cursor mutation calls.
type ApplicationEventTransport interface {
	Pending(context.Context, int) ([]ApplicationEvent, error)
	Acknowledge(context.Context, uint64) error
}

type applicationEventEnvelope struct {
	Schema      string            `json:"schema"`
	Application string            `json:"application"`
	Anchor      ApplicationAnchor `json:"anchor"`
	Sequence    uint64            `json:"sequence"`
	Event       ApplicationEvent  `json:"event"`
}

type applicationCommandEnvelope struct {
	Schema      string            `json:"schema"`
	Application string            `json:"application"`
	Anchor      ApplicationAnchor `json:"anchor"`
	Sequence    uint64            `json:"sequence"`
	Command     string            `json:"command"`
	Args        string            `json:"args,omitempty"`
}

// EventDisposition tells the durable dispatcher whether this delivery was
// consumed or the application deliberately unregistered. Transient call
// errors are returned as errors and therefore remain unacknowledged.
type EventDisposition string

const (
	EventAcknowledged EventDisposition = "ack"
	EventUnregister   EventDisposition = "unregister"
)

type lifecycleDecisionWire struct {
	Decision     string          `json:"decision"`
	Reason       string          `json:"reason,omitempty"`
	Mutation     json.RawMessage `json:"mutation,omitempty"`
	Contribution json.RawMessage `json:"contribution,omitempty"`
}

type eventResultWire struct {
	Status EventDisposition `json:"status"`
}

// CommandResult is the bounded result of an operator-invoked application
// command. UI-heavy commands normally render through stado_ui_* imports; the
// optional message is a concise fallback for text/headless surfaces.
type CommandResult struct {
	Status            string `json:"status"`
	Message           string `json:"message,omitempty"`
	WorkerRunID       string `json:"worker_run_id,omitempty"`
	ResumeWorkerRunID string `json:"resume_worker_run_id,omitempty"`
	CancelWorkerRunID string `json:"cancel_worker_run_id,omitempty"`
}

// serializedCallGate is a context-aware binary semaphore. A plain sync.Mutex
// can wedge forever when a plugin recursively invokes one of its own tools:
// the inner call waits for the outer callback while the outer callback waits
// for the import to return. The gate makes that contention cancellable by the
// existing per-callback deadline.
type serializedCallGate struct {
	token chan struct{}
}

func newSerializedCallGate() *serializedCallGate {
	gate := &serializedCallGate{token: make(chan struct{}, 1)}
	gate.token <- struct{}{}
	return gate
}

func (g *serializedCallGate) lock(ctx context.Context) error {
	if g == nil {
		return errors.New("application serialization gate is unavailable")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-g.token:
		return nil
	}
}

func (g *serializedCallGate) unlock() {
	g.token <- struct{}{}
}

// LifecycleApplication is one serialized, persistent WASM module for an exact
// (plugin identity, session, generation) tuple. Calls to hooks, events, ticks,
// and plugin tools must share callMu; WASM is never re-entered concurrently.
type LifecycleApplication struct {
	Module   *Module
	Host     *Host
	Manifest plugins.Manifest
	Identity plugins.RuntimeIdentity
	Anchor   ApplicationAnchor

	callGate    *serializedCallGate
	lifecycleFn api.Function
	eventFn     api.Function
	commandFn   api.Function
	tickFn      api.Function
	caps        plugins.LifecycleCapabilities
	points      []hooks.Point
	timeout     time.Duration
	sequence    uint64
	closed      bool
}

// LoadLifecycleApplication instantiates a manifest-declared application. Each
// application must receive its own Runtime because wazero host imports close
// over a particular Host; sharing one "stado" module across different plugin
// authorities would collapse their capability boundaries.
func LoadLifecycleApplication(ctx context.Context, rt *Runtime, wasmBytes []byte, host *Host, anchor ApplicationAnchor) (*LifecycleApplication, error) {
	if rt == nil || host == nil {
		return nil, errors.New("lifecycle: runtime and host are required")
	}
	if err := anchor.Validate(); err != nil {
		return nil, err
	}
	if err := host.Identity.Validate(); err != nil {
		return nil, fmt.Errorf("lifecycle identity: %w", err)
	}
	if host.Manifest.Lifecycle == nil {
		return nil, errors.New("lifecycle: manifest has no lifecycle declaration")
	}
	if err := host.Manifest.ValidateExtensions(); err != nil {
		return nil, fmt.Errorf("lifecycle manifest: %w", err)
	}
	caps, err := host.Manifest.ParseLifecycleCapabilities()
	if err != nil {
		return nil, err
	}
	if err := InstallHostImports(ctx, rt, host); err != nil {
		return nil, fmt.Errorf("lifecycle: host imports: %w", err)
	}
	mod, err := rt.InstantiateWithIdentity(ctx, wasmBytes, host.Manifest, host.Identity)
	if err != nil {
		return nil, fmt.Errorf("lifecycle: instantiate: %w", err)
	}
	closeOnError := func(err error) (*LifecycleApplication, error) {
		_ = mod.Close(ctx)
		return nil, err
	}
	lifecycleFn := mod.wasmMod.ExportedFunction("stado_plugin_lifecycle")
	if len(host.Manifest.Lifecycle.Points) > 0 && lifecycleFn == nil {
		return closeOnError(errors.New("lifecycle: subscribed points require stado_plugin_lifecycle export"))
	}
	eventFn := mod.wasmMod.ExportedFunction("stado_plugin_event")
	if len(host.Manifest.Lifecycle.Events) > 0 && eventFn == nil {
		return closeOnError(errors.New("lifecycle: subscribed events require stado_plugin_event export"))
	}
	commandFn := mod.wasmMod.ExportedFunction("stado_plugin_command")
	if len(host.Manifest.Commands) > 0 && commandFn == nil {
		return closeOnError(errors.New("lifecycle: declared commands require stado_plugin_command export"))
	}
	points := make([]hooks.Point, len(host.Manifest.Lifecycle.Points))
	for i, point := range host.Manifest.Lifecycle.Points {
		points[i] = hooks.Point(point)
	}
	return &LifecycleApplication{
		Module: mod, Host: host, Manifest: host.Manifest, Identity: host.Identity,
		Anchor: anchor, lifecycleFn: lifecycleFn, eventFn: eventFn, commandFn: commandFn,
		tickFn: mod.wasmMod.ExportedFunction("stado_plugin_tick"), caps: caps,
		points: points, timeout: time.Duration(host.Manifest.Lifecycle.EffectiveTimeoutMS()) * time.Millisecond,
		callGate: newSerializedCallGate(),
	}, nil
}

// RunCommand routes one host-selected, signed manifest command into this
// persistent instance. It shares the application serialization lock with
// lifecycle callbacks, durable events, ticks, and model tools.
func (a *LifecycleApplication) RunCommand(ctx context.Context, name, args string) (CommandResult, error) {
	if a == nil || a.commandFn == nil {
		return CommandResult{}, errors.New("application command callback is unavailable")
	}
	var declared *plugins.CommandDef
	for _, command := range a.Manifest.Commands {
		if command.Name == name {
			copy := command
			declared = &copy
			break
		}
	}
	if declared == nil {
		return CommandResult{}, fmt.Errorf("application command %q is not declared", name)
	}
	if len(args) > maxLifecyclePayloadBytes/4 {
		return CommandResult{}, errors.New("application command arguments exceed 256 KiB")
	}
	if err := a.callGate.lock(ctx); err != nil {
		return CommandResult{}, err
	}
	defer a.callGate.unlock()
	if a.closed {
		return CommandResult{}, errors.New("lifecycle application is closed")
	}
	a.sequence++
	input, err := json.Marshal(applicationCommandEnvelope{
		Schema: "stado.dev/application-command/v1", Application: a.Identity.Canonical,
		Anchor: a.Anchor, Sequence: a.sequence, Command: name, Args: args,
	})
	if err != nil {
		return CommandResult{}, err
	}
	commandTimeout := time.Duration(declared.EffectiveTimeoutMS(int(a.timeout/time.Millisecond))) * time.Millisecond
	out, err := a.callJSONWithTimeout(ctx, a.commandFn, input, commandTimeout)
	if err != nil {
		return CommandResult{}, err
	}
	var result CommandResult
	if err := decodeStrictJSON(out, &result); err != nil {
		return CommandResult{}, err
	}
	if err := validateCommandResult(result); err != nil {
		return CommandResult{}, err
	}
	return result, nil
}

func validateCommandResult(result CommandResult) error {
	if result.Status != "ok" && result.Status != "error" {
		return fmt.Errorf("invalid application command status %q", result.Status)
	}
	if len(result.Message) > 16<<10 {
		return errors.New("application command message exceeds 16 KiB")
	}
	if result.WorkerRunID != "" && (result.Status != "ok" || len(result.WorkerRunID) > 256 || !applicationWorkerRunIDPattern.MatchString(result.WorkerRunID)) {
		return errors.New("application command worker_run_id is invalid")
	}
	if result.ResumeWorkerRunID != "" && (result.Status != "ok" || len(result.ResumeWorkerRunID) > 256 || !applicationWorkerRunIDPattern.MatchString(result.ResumeWorkerRunID)) {
		return errors.New("application command resume_worker_run_id is invalid")
	}
	if result.CancelWorkerRunID != "" && (result.Status != "ok" || len(result.CancelWorkerRunID) > 256 || !applicationWorkerRunIDPattern.MatchString(result.CancelWorkerRunID)) {
		return errors.New("application command cancel_worker_run_id is invalid")
	}
	handoffs := 0
	for _, runID := range []string{result.WorkerRunID, result.ResumeWorkerRunID, result.CancelWorkerRunID} {
		if runID != "" {
			handoffs++
		}
	}
	if handoffs > 1 {
		return errors.New("application command cannot request multiple worker handoffs together")
	}
	return nil
}

func (a *LifecycleApplication) Name() string {
	if a == nil {
		return "wasm:lifecycle"
	}
	return a.Identity.Canonical
}

func (a *LifecycleApplication) Points() []hooks.Point {
	if a == nil {
		return nil
	}
	return append([]hooks.Point(nil), a.points...)
}

// Run implements hooks.HookScript. Only fields EP-51 permits at the current
// point can be mutated; timestamps, turn numbers, tool identity/class, token
// facts, and session anchors always remain host-owned.
func (a *LifecycleApplication) Run(ctx context.Context, point hooks.Point, payload hooks.Payload) (hooks.HookResult, error) {
	if a == nil || a.lifecycleFn == nil {
		return a.lifecycleFault(point, "lifecycle application is unavailable", errors.New("callback export is unavailable"))
	}
	if !a.caps.CanObserve(string(point)) {
		return a.lifecycleFault(point, "lifecycle application is not authorized", fmt.Errorf("point %q is not authorized", point))
	}
	if err := a.callGate.lock(ctx); err != nil {
		return a.lifecycleFault(point, "lifecycle application callback failed", err)
	}
	defer a.callGate.unlock()
	if a.closed {
		return a.lifecycleFault(point, "lifecycle application callback failed", errors.New("application is closed"))
	}
	a.sequence++
	input, err := json.Marshal(lifecycleCallEnvelope{
		Schema: lifecycleSchemaV1, Point: point, Application: a.Identity.Canonical,
		Anchor: a.Anchor, Sequence: a.sequence, Payload: payload,
	})
	if err != nil {
		return a.lifecycleFault(point, "lifecycle application callback failed", err)
	}
	out, err := a.callJSON(ctx, a.lifecycleFn, input)
	if err != nil {
		return a.lifecycleFault(point, "lifecycle application failed closed", err)
	}
	result, err := decodeLifecycleDecision(point, payload, out, a.caps.CanDecide(string(point)), a.caps.CanContribute(string(point)))
	if err != nil {
		return a.lifecycleFault(point, "lifecycle application returned an invalid decision", err)
	}
	return result, nil
}

// lifecycleFault makes the signed application's own failure posture
// authoritative before the generic lifecycle runner sees the callback result.
// In particular, a global fail-closed setting for operator policy hooks must
// not turn a failure-open, contribute-only application into an unrelated turn
// gate. Failure-open applications log the fault and contribute nothing.
func (a *LifecycleApplication) lifecycleFault(point hooks.Point, closedReason string, err error) (hooks.HookResult, error) {
	if a != nil && a.Manifest.Lifecycle != nil && a.Manifest.Lifecycle.Failure == "closed" {
		return hooks.Deny(closedReason + ": " + err.Error()), nil
	}
	if a != nil && a.Host != nil && a.Host.Logger != nil {
		a.Host.Logger.Warn("lifecycle application failed open; contribution ignored", "point", point, "err", err)
	}
	return hooks.Continue(), nil
}

func decodeLifecycleDecision(point hooks.Point, current hooks.Payload, raw []byte, canDecide, canContribute bool) (hooks.HookResult, error) {
	var wire lifecycleDecisionWire
	if err := decodeStrictJSON(raw, &wire); err != nil {
		return hooks.Continue(), err
	}
	switch wire.Decision {
	case "continue":
		if wire.Reason != "" || len(wire.Mutation) != 0 || len(wire.Contribution) != 0 {
			return hooks.Continue(), errors.New("continue decision cannot carry reason, mutation, or contribution")
		}
		return hooks.Continue(), nil
	case "deny":
		if !canDecide {
			return hooks.Continue(), errors.New("lifecycle:decide capability required for deny")
		}
		if wire.Reason == "" || len(wire.Reason) > 4096 || len(wire.Mutation) != 0 || len(wire.Contribution) != 0 {
			return hooks.Continue(), errors.New("deny requires a bounded reason and no mutation or contribution")
		}
		return hooks.Deny(wire.Reason), nil
	case "mutate":
		if !canDecide {
			return hooks.Continue(), errors.New("lifecycle:decide capability required for mutation")
		}
		if wire.Reason != "" || len(wire.Mutation) == 0 || len(wire.Contribution) != 0 {
			return hooks.Continue(), errors.New("mutate requires only a mutation object")
		}
		next, err := applyLifecycleMutation(point, current, wire.Mutation)
		if err != nil {
			return hooks.Continue(), err
		}
		return hooks.Mutate(next), nil
	case "contribute":
		if !canContribute {
			return hooks.Continue(), errors.New("lifecycle:contribute capability required")
		}
		if wire.Reason != "" || len(wire.Mutation) != 0 || len(wire.Contribution) == 0 {
			return hooks.Continue(), errors.New("contribute requires only a contribution object")
		}
		next, err := applyLifecycleContribution(point, current, wire.Contribution)
		if err != nil {
			return hooks.Continue(), err
		}
		return hooks.Mutate(next), nil
	default:
		return hooks.Continue(), fmt.Errorf("unknown lifecycle decision %q", wire.Decision)
	}
}

// applyLifecycleContribution is intentionally separate from mutation. An
// application holding only lifecycle:contribute:pre_llm can add one bounded
// low-priority context section, but cannot echo/replace the system prompt,
// select a model, rewrite history, or deny the provider turn (EP-0060/0064).
func applyLifecycleContribution(point hooks.Point, current hooks.Payload, raw json.RawMessage) (hooks.Payload, error) {
	if point != hooks.PointPreLLM {
		return nil, fmt.Errorf("lifecycle contribution is unsupported at %q", point)
	}
	base, ok := current.(*hooks.PreLLMPayload)
	if !ok {
		return nil, errors.New("pre_llm contribution payload type mismatch")
	}
	var contribution struct {
		SystemAppend *string `json:"system_append"`
	}
	if err := decodeStrictJSON(raw, &contribution); err != nil || contribution.SystemAppend == nil {
		return nil, errors.New("pre_llm contribution requires only system_append")
	}
	appendix := strings.TrimSpace(*contribution.SystemAppend)
	if appendix == "" || len(appendix) > maxSystemContribution {
		return nil, fmt.Errorf("pre_llm system_append must be 1..%d bytes", maxSystemContribution)
	}
	if len(base.System)+2+len(appendix) > maxLifecyclePayloadBytes {
		return nil, errors.New("pre_llm contributed system exceeds lifecycle payload bound")
	}
	next := *base
	if strings.TrimSpace(next.System) == "" {
		next.System = appendix
	} else {
		next.System += "\n\n" + appendix
	}
	return &next, nil
}

func applyLifecycleMutation(point hooks.Point, current hooks.Payload, raw json.RawMessage) (hooks.Payload, error) {
	switch point {
	case hooks.PointPreTool:
		base, ok := current.(*hooks.PreToolPayload)
		if !ok {
			return nil, errors.New("pre_tool payload type mismatch")
		}
		var change struct {
			Args *string `json:"args"`
		}
		if err := decodeStrictJSON(raw, &change); err != nil || change.Args == nil || !json.Valid([]byte(*change.Args)) {
			return nil, errors.New("pre_tool mutation requires valid JSON args")
		}
		next := *base
		next.Args = *change.Args
		return &next, nil
	case hooks.PointPostTool:
		base, ok := current.(*hooks.PostToolPayload)
		if !ok {
			return nil, errors.New("post_tool payload type mismatch")
		}
		var change struct {
			Result *string `json:"result"`
			Error  *string `json:"error"`
		}
		if err := decodeStrictJSON(raw, &change); err != nil || (change.Result == nil && change.Error == nil) {
			return nil, errors.New("post_tool mutation requires result and/or error")
		}
		next := *base
		if change.Result != nil {
			next.Result = *change.Result
		}
		if change.Error != nil {
			next.Error = *change.Error
		}
		return &next, nil
	case hooks.PointPreLLM:
		base, ok := current.(*hooks.PreLLMPayload)
		if !ok {
			return nil, errors.New("pre_llm payload type mismatch")
		}
		var change struct {
			Model  *string `json:"model"`
			System *string `json:"system"`
		}
		if err := decodeStrictJSON(raw, &change); err != nil || (change.Model == nil && change.System == nil) {
			return nil, errors.New("pre_llm mutation requires model and/or system")
		}
		next := *base
		if change.Model != nil {
			next.Model = *change.Model
		}
		if change.System != nil {
			next.System = *change.System
		}
		return &next, nil
	case hooks.PointPostLLM:
		base, ok := current.(*hooks.PostLLMPayload)
		if !ok {
			return nil, errors.New("post_llm payload type mismatch")
		}
		var change struct {
			Text *string `json:"text"`
		}
		if err := decodeStrictJSON(raw, &change); err != nil || change.Text == nil {
			return nil, errors.New("post_llm mutation requires text")
		}
		next := *base
		next.Text = *change.Text
		return &next, nil
	case hooks.PointPostTurn:
		return nil, errors.New("post_turn is observation-only")
	default:
		return nil, fmt.Errorf("unsupported lifecycle point %q", point)
	}
}

// DeliverEvent invokes the durable event callback. The caller must persist its
// cursor and acknowledge broker sequence only after EventAcknowledged.
func (a *LifecycleApplication) DeliverEvent(ctx context.Context, event ApplicationEvent) (EventDisposition, error) {
	if a == nil || a.eventFn == nil {
		return "", errors.New("lifecycle event callback is unavailable")
	}
	if !a.caps.CanObserve(event.Kind) {
		return "", fmt.Errorf("lifecycle event %q is not authorized", event.Kind)
	}
	if event.BrokerSeq == 0 || len(event.Data) == 0 || !json.Valid(event.Data) || len(event.Data) > maxLifecyclePayloadBytes {
		return "", errors.New("invalid lifecycle event envelope")
	}
	if err := a.callGate.lock(ctx); err != nil {
		return "", err
	}
	defer a.callGate.unlock()
	if a.closed {
		return "", errors.New("lifecycle application is closed")
	}
	a.sequence++
	input, err := json.Marshal(applicationEventEnvelope{
		Schema: lifecycleSchemaV1, Application: a.Identity.Canonical, Anchor: a.Anchor,
		Sequence: a.sequence, Event: event,
	})
	if err != nil {
		return "", err
	}
	out, err := a.callJSON(ctx, a.eventFn, input)
	if err != nil {
		return "", err
	}
	var result eventResultWire
	if err := decodeStrictJSON(out, &result); err != nil {
		return "", err
	}
	if result.Status != EventAcknowledged && result.Status != EventUnregister {
		return "", fmt.Errorf("invalid lifecycle event status %q", result.Status)
	}
	return result.Status, nil
}

func (a *LifecycleApplication) Tick(ctx context.Context) (unregister bool, err error) {
	if a == nil || a.tickFn == nil {
		return false, nil
	}
	if err := a.callGate.lock(ctx); err != nil {
		return true, err
	}
	defer a.callGate.unlock()
	if a.closed {
		return true, errors.New("lifecycle application is closed")
	}
	cctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	ret, err := a.tickFn.Call(cctx)
	if err != nil {
		return true, fmt.Errorf("lifecycle tick: %w", err)
	}
	return len(ret) > 0 && api.DecodeI32(ret[0]) != 0, nil
}

func (a *LifecycleApplication) callJSON(ctx context.Context, fn api.Function, input []byte) ([]byte, error) {
	return a.callJSONWithTimeout(ctx, fn, input, a.timeout)
}

func (a *LifecycleApplication) callJSONWithTimeout(ctx context.Context, fn api.Function, input []byte, timeout time.Duration) ([]byte, error) {
	if len(input) == 0 || len(input) > maxLifecyclePayloadBytes {
		return nil, errors.New("lifecycle input exceeds 1 MiB")
	}
	alloc := a.Module.wasmMod.ExportedFunction("stado_alloc")
	freeFn := a.Module.wasmMod.ExportedFunction("stado_free")
	if alloc == nil || freeFn == nil || fn == nil {
		return nil, errors.New("lifecycle ABI requires stado_alloc, stado_free, and callback export")
	}
	if timeout <= 0 {
		return nil, errors.New("lifecycle callback timeout must be positive")
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	inLen := uint32(len(input))
	inPtr, err := callAlloc(cctx, alloc, inLen)
	if err != nil {
		return nil, err
	}
	defer callFree(cctx, freeFn, inPtr, inLen)
	if !a.Module.wasmMod.Memory().Write(inPtr, input) {
		return nil, errors.New("lifecycle input memory write failed")
	}
	outPtr, err := callAlloc(cctx, alloc, maxLifecyclePayloadBytes)
	if err != nil {
		return nil, err
	}
	defer callFree(cctx, freeFn, outPtr, maxLifecyclePayloadBytes)
	ret, err := fn.Call(cctx, api.EncodeU32(inPtr), api.EncodeU32(inLen), api.EncodeU32(outPtr), api.EncodeU32(maxLifecyclePayloadBytes))
	if err != nil {
		return nil, err
	}
	if len(ret) == 0 {
		return nil, errors.New("lifecycle callback returned no length")
	}
	n := api.DecodeI32(ret[0])
	if n < 0 {
		errorLength := -int64(n)
		if errorLength <= 0 || errorLength > maxLifecyclePayloadBytes {
			return nil, fmt.Errorf("lifecycle callback returned invalid error length %d", n)
		}
		out, ok := a.Module.wasmMod.Memory().Read(outPtr, uint32(errorLength))
		if !ok {
			return nil, errors.New("lifecycle error memory read failed")
		}
		if !utf8.Valid(out) {
			return nil, errors.New("lifecycle callback returned invalid UTF-8 error payload")
		}
		return nil, errors.New(string(out))
	}
	if n == 0 || n > maxLifecyclePayloadBytes {
		return nil, fmt.Errorf("lifecycle callback returned invalid length %d", n)
	}
	out, ok := a.Module.wasmMod.Memory().Read(outPtr, uint32(n))
	if !ok {
		return nil, errors.New("lifecycle output memory read failed")
	}
	return append([]byte(nil), out...), nil
}

func (a *LifecycleApplication) Close(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if err := a.callGate.lock(ctx); err != nil {
		return err
	}
	defer a.callGate.unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	if a.Module == nil {
		return nil
	}
	return a.Module.Close(ctx)
}

func decodeStrictJSON(raw []byte, target any) error {
	if len(raw) == 0 || len(raw) > maxLifecyclePayloadBytes {
		return errors.New("lifecycle JSON size must be 1..1 MiB")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err == nil {
		return errors.New("lifecycle JSON contains trailing value")
	} else if !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

var _ hooks.HookScript = (*LifecycleApplication)(nil)
