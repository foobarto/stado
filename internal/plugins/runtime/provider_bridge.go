package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/foobarto/stado/pkg/agent"
)

const ProviderInvokeFactsSchemaV1 = "stado.dev/provider-invoke-facts/v1"

const maxProviderInvokeTextBytes = (1 << 20) - (16 << 10)
const maxProviderInvokeFactNameBytes = 512

// ProviderInvokeRequest is the provider-neutral request accepted by the native
// provider primitive. It deliberately contains no plugin identity, principal,
// session, credential, budget, usage, or audit fields: those are host facts and
// are never accepted from guest memory.
type ProviderInvokeRequest struct {
	System          string                  `json:"system,omitempty"`
	Messages        []ProviderInvokeMessage `json:"messages"`
	Model           string                  `json:"model,omitempty"`
	MaxOutputTokens int                     `json:"max_output_tokens,omitempty"`
	Temperature     *float64                `json:"temperature,omitempty"`
}

type ProviderInvokeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ProviderInvokeUsage struct {
	InputTokens      int  `json:"input_tokens"`
	OutputTokens     int  `json:"output_tokens"`
	CacheReadTokens  int  `json:"cache_read_tokens"`
	CacheWriteTokens int  `json:"cache_write_tokens"`
	TotalTokens      int  `json:"total_tokens"`
	Estimated        bool `json:"estimated"`
}

// ProviderInvokeDiagnostic is safe to expose to an untrusted plugin. Raw
// provider and cleanup errors may contain credentials, URLs, or prompt text;
// the stable fingerprint supports comparison without disclosing that detail.
type ProviderInvokeDiagnostic struct {
	Kind        string `json:"kind"`
	Fingerprint string `json:"fingerprint"`
}

// ProviderInvokeFacts are host-observed facts, not a tool result. A WASM
// application decides how to turn these facts into model-facing output.
type ProviderInvokeFacts struct {
	Schema     string                    `json:"schema"`
	Status     string                    `json:"status"`
	Text       string                    `json:"text,omitempty"`
	Provider   string                    `json:"provider,omitempty"`
	Model      string                    `json:"model,omitempty"`
	Usage      ProviderInvokeUsage       `json:"usage"`
	Diagnostic *ProviderInvokeDiagnostic `json:"diagnostic,omitempty"`
	Cleanup    *ProviderInvokeDiagnostic `json:"cleanup,omitempty"`
}

// ProviderBridge is the smallest native seam needed by a WASM application to
// use an operator-configured provider. tokenCeiling is derived from the signed
// manifest capability by the host; it is never read from guest JSON.
type ProviderBridge interface {
	InvokeProvider(context.Context, string, ProviderInvokeRequest, int) (ProviderInvokeFacts, error)
}

// ToolHostProviderBridge lets a generic tool host supply provider construction
// without registering a native model tool. The exact loader-authenticated
// identity is injected by the runtime and cannot be replaced by guest input.
type ToolHostProviderBridge interface {
	PluginProviderBridge(identityCanonical string) (ProviderBridge, error)
}

// NativeProviderBridge adapts agent.Provider to ProviderBridge. A bridge may
// either borrow a live session provider or construct an owned provider for one
// invocation. Owned providers are closed after the semantic result is known;
// cleanup failure is attached separately and never discards a valid result.
type NativeProviderBridge struct {
	Provider        agent.Provider
	ProviderFactory func() (agent.Provider, error)
	DefaultModel    string
	OwnProvider     bool
}

func NewOwnedProviderBridge(factory func() (agent.Provider, error), defaultModel string) *NativeProviderBridge {
	return &NativeProviderBridge{ProviderFactory: factory, DefaultModel: defaultModel, OwnProvider: true}
}

func (b *SessionBridgeImpl) InvokeProvider(ctx context.Context, identityCanonical string, req ProviderInvokeRequest, tokenCeiling int) (ProviderInvokeFacts, error) {
	bridge := NativeProviderBridge{
		Provider: b.Provider, DefaultModel: b.Model,
	}
	return bridge.InvokeProvider(ctx, identityCanonical, req, tokenCeiling)
}

func (b *NativeProviderBridge) InvokeProvider(ctx context.Context, identityCanonical string, req ProviderInvokeRequest, tokenCeiling int) (facts ProviderInvokeFacts, err error) {
	facts.Schema = ProviderInvokeFactsSchemaV1
	facts.Status = "failed"
	// Defense in depth around every exit path: a provider adapter may ignore
	// TurnRequest.MaxTokens, report corrected usage only on its terminal event,
	// or fail after emitting partial text. Actual/estimated usage remains in the
	// facts for native accounting, but over-ceiling text never crosses the ABI.
	defer func() { enforceProviderTokenCeiling(&facts, tokenCeiling) }()
	if strings.TrimSpace(identityCanonical) == "" {
		return facts, errors.New("provider invocation requires authenticated plugin identity")
	}

	provider := b.Provider
	if provider == nil && b.ProviderFactory != nil {
		provider, err = b.ProviderFactory()
		if err != nil {
			facts.Diagnostic = providerDiagnostic("provider_construct", err)
			return facts, nil
		}
	}
	if provider == nil {
		facts.Diagnostic = providerDiagnostic("provider_unavailable", errors.New("provider unavailable"))
		return facts, nil
	}
	if b.OwnProvider {
		defer func() {
			closer, ok := provider.(io.Closer)
			if !ok {
				return
			}
			if closeErr := closer.Close(); closeErr != nil {
				facts.Cleanup = providerDiagnostic("provider_close", closeErr)
			}
		}()
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = b.DefaultModel
	}
	facts.Provider = boundedProviderFactName(provider.Name())
	facts.Model = boundedProviderFactName(model)

	turnRequest := providerTurnRequest(req, model)
	inputTokens, estimated := countProviderInput(ctx, provider, turnRequest, req)
	if inputTokens >= tokenCeiling {
		// Counting is preflight rather than provider consumption. Because the
		// request is not dispatched, the host releases its reservation and
		// commits zero usage.
		facts.Usage = ProviderInvokeUsage{}
		facts.Diagnostic = providerDiagnostic("token_budget", fmt.Errorf("input tokens exceed provider token ceiling"))
		return facts, nil
	}
	outputCeiling := tokenCeiling - inputTokens
	if req.MaxOutputTokens > 0 && req.MaxOutputTokens < outputCeiling {
		outputCeiling = req.MaxOutputTokens
	}
	turnRequest.MaxTokens = outputCeiling

	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, streamErr := provider.StreamTurn(streamCtx, turnRequest)
	if streamErr != nil {
		facts.Usage = ProviderInvokeUsage{InputTokens: inputTokens, TotalTokens: inputTokens, Estimated: true}
		facts.Diagnostic = providerDiagnostic(providerFailureKind(ctx, streamErr), streamErr)
		if errors.Is(streamErr, context.Canceled) || errors.Is(streamErr, context.DeadlineExceeded) || ctx.Err() != nil {
			facts.Status = "cancelled"
		}
		return facts, nil
	}
	if stream == nil {
		facts.Usage = ProviderInvokeUsage{InputTokens: inputTokens, TotalTokens: inputTokens, Estimated: estimated}
		facts.Diagnostic = providerDiagnostic("provider_stream", errors.New("provider returned a nil event stream"))
		return facts, nil
	}

	var text strings.Builder
	var reported *agent.Usage
	done := false

streamLoop:
	for {
		var event agent.Event
		var open bool
		select {
		case <-ctx.Done():
			facts.Text = text.String()
			facts.Usage = providerUsage(req, inputTokens, facts.Text, reported, estimated)
			facts.Status = "cancelled"
			facts.Diagnostic = providerDiagnostic("context_cancelled", ctx.Err())
			return facts, nil
		case event, open = <-stream:
			if !open {
				break streamLoop
			}
		}
		switch event.Kind {
		case agent.EvTextDelta:
			if len(event.Text) > maxProviderInvokeTextBytes-text.Len() {
				cancel()
				facts.Text = text.String()
				facts.Diagnostic = providerDiagnostic("response_limit", errors.New("provider response exceeds host byte ceiling"))
				// The over-limit delta was generated even though it cannot cross
				// the ABI. Include it in estimated/reported commitment so output
				// truncation cannot make consumed tokens disappear.
				estimatedOutput := saturatingTokenSum(text.Len(), len(event.Text))
				facts.Usage = mergeProviderUsage(inputTokens, estimatedOutput, reported, estimated)
				return facts, nil
			}
			text.WriteString(event.Text)
		case agent.EvUsage, agent.EvDone:
			if event.Usage != nil {
				copyUsage := *event.Usage
				reported = &copyUsage
				observed := providerUsage(req, inputTokens, text.String(), reported, estimated)
				if observed.TotalTokens > tokenCeiling {
					cancel()
					facts.Usage = observed
					facts.Diagnostic = providerDiagnostic("token_budget", errors.New("provider usage exceeds signed token ceiling"))
					return facts, nil
				}
			}
			if event.Kind == agent.EvDone {
				done = true
				break streamLoop
			}
		case agent.EvError:
			facts.Text = text.String()
			facts.Usage = providerUsage(req, inputTokens, facts.Text, reported, estimated)
			facts.Diagnostic = providerDiagnostic(providerFailureKind(ctx, event.Err), event.Err)
			if errors.Is(event.Err, context.Canceled) || errors.Is(event.Err, context.DeadlineExceeded) || ctx.Err() != nil {
				facts.Status = "cancelled"
			}
			return facts, nil
		}
	}

	facts.Text = text.String()
	facts.Usage = providerUsage(req, inputTokens, facts.Text, reported, estimated)
	if ctx.Err() != nil {
		facts.Status = "cancelled"
		facts.Diagnostic = providerDiagnostic("context_cancelled", ctx.Err())
		return facts, nil
	}
	if !done {
		facts.Diagnostic = providerDiagnostic("provider_stream_incomplete", errors.New("provider stream closed without done event"))
		return facts, nil
	}
	facts.Status = "completed"

	return facts, nil
}

func providerTurnRequest(req ProviderInvokeRequest, model string) agent.TurnRequest {
	messages := make([]agent.Message, 0, len(req.Messages))
	for _, message := range req.Messages {
		messages = append(messages, agent.Text(agent.Role(message.Role), message.Content))
	}
	return agent.TurnRequest{
		Model: model, System: req.System, Messages: messages,
		Temperature: req.Temperature,
	}
}

func countProviderInput(ctx context.Context, provider agent.Provider, turnRequest agent.TurnRequest, wireRequest ProviderInvokeRequest) (int, bool) {
	if counter, ok := provider.(agent.TokenCounter); ok {
		if count, err := counter.CountTokens(ctx, turnRequest); err == nil && count > 0 {
			return count, false
		}
	}
	// A four-bytes-per-token heuristic is useful telemetry but is not an
	// authority ceiling. The strict request's encoded byte length includes the
	// content plus role/framing fields and is a conservative one-byte-per-token
	// upper bound for the supported text providers. If a future provider uses a
	// tokenizer outside that bound, its terminal usage is still enforced below.
	return conservativeProviderInputTokens(wireRequest), true
}

func providerUsage(_ ProviderInvokeRequest, inputTokens int, text string, reported *agent.Usage, inputEstimated bool) ProviderInvokeUsage {
	// Without provider usage, one UTF-8 byte per token is deliberately
	// conservative. This may reject an unmetered response that would have fit
	// under a provider tokenizer, but it cannot turn uncertainty into authority.
	estimatedOutput := len(text)
	return mergeProviderUsage(inputTokens, estimatedOutput, reported, inputEstimated)
}

func enforceProviderTokenCeiling(facts *ProviderInvokeFacts, tokenCeiling int) bool {
	if facts == nil || tokenCeiling <= 0 || facts.Usage.TotalTokens <= tokenCeiling {
		return true
	}
	facts.Status = "failed"
	facts.Text = ""
	facts.Diagnostic = providerDiagnostic("token_budget", errors.New("provider usage exceeds signed token ceiling"))
	return false
}

func mergeProviderUsage(inputTokens, estimatedOutput int, reported *agent.Usage, inputEstimated bool) ProviderInvokeUsage {
	usage := ProviderInvokeUsage{InputTokens: inputTokens, OutputTokens: estimatedOutput, Estimated: true}
	if reported == nil {
		usage.TotalTokens = saturatingTokenSum(usage.InputTokens, usage.OutputTokens)
		return usage
	}
	usage.Estimated = false
	if reported.InputTokens > 0 {
		usage.InputTokens = reported.InputTokens
	} else if inputEstimated {
		usage.Estimated = true
	}
	if reported.OutputTokens > 0 {
		usage.OutputTokens = reported.OutputTokens
	} else {
		usage.Estimated = true
	}
	usage.CacheReadTokens = nonNegativeTokenCount(reported.CacheReadTokens)
	usage.CacheWriteTokens = nonNegativeTokenCount(reported.CacheWriteTokens)
	// Cache read/write are factual subdivisions of provider usage, not extra
	// billable context tokens. The hard token contract is input+output across
	// AgentLoop, TUI, and lifecycle facts; counting cache fields again would
	// double-charge the same tokens.
	usage.TotalTokens = saturatingTokenSum(usage.InputTokens, usage.OutputTokens)
	return usage
}

func nonNegativeTokenCount(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func boundedProviderFactName(value string) string {
	if len(value) > maxProviderInvokeFactNameBytes {
		return ""
	}
	return value
}

func saturatingTokenSum(values ...int) int {
	total := 0
	for _, value := range values {
		value = nonNegativeTokenCount(value)
		if value > math.MaxInt-total {
			return math.MaxInt
		}
		total += value
	}
	return total
}

func providerFailureKind(ctx context.Context, err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return "context_cancelled"
	}
	return "provider_stream"
}

func providerDiagnostic(kind string, err error) *ProviderInvokeDiagnostic {
	if err == nil {
		err = errors.New(kind)
	}
	sum := sha256.Sum256([]byte(err.Error()))
	return &ProviderInvokeDiagnostic{Kind: kind, Fingerprint: "sha256:" + hex.EncodeToString(sum[:])}
}
