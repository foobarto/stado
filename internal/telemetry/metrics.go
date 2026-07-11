package telemetry

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metrics holds the preconstructed instruments used across stado. Building
// them once avoids map lookups on the hot path and makes instrument names a
// single-file contract.
//
// See PLAN.md §6.3 for the baseline metric set.
type Metrics struct {
	ToolLatency   metric.Float64Histogram // ms; attrs: tool, outcome
	TokensTotal   metric.Int64Counter     // attrs: provider, model, direction (in|out)
	CacheHitRatio metric.Float64Histogram // fraction 0..1; attrs: provider, model
}

// NewMetrics constructs the supported stado metric set for a meter. Keeping
// construction public lets integration tests use an in-memory SDK reader while
// production continues to obtain the same instruments from Start.
func NewMetrics(m metric.Meter) (Metrics, error) {
	var out Metrics
	var err error

	out.ToolLatency, err = m.Float64Histogram("stado_tool_latency_ms",
		metric.WithUnit("ms"),
		metric.WithDescription("Tool-call end-to-end latency."))
	if err != nil {
		return out, fmt.Errorf("metric tool_latency: %w", err)
	}
	out.TokensTotal, err = m.Int64Counter("stado_tokens_total",
		metric.WithDescription("Tokens spent per provider turn."))
	if err != nil {
		return out, fmt.Errorf("metric tokens_total: %w", err)
	}
	out.CacheHitRatio, err = m.Float64Histogram("stado_cache_hit_ratio",
		metric.WithDescription("Prompt-cache hit ratio per turn."))
	if err != nil {
		return out, fmt.Errorf("metric cache_hit_ratio: %w", err)
	}
	return out, nil
}

// RecordTurnUsage records the provider usage instruments at the shared turn
// boundary. Cache hit ratio follows the TUI definition: cache-read tokens over
// cache-read plus uncached input tokens.
func (m Metrics) RecordTurnUsage(ctx context.Context, provider, model string, input, output, cacheRead int) {
	base := []attribute.KeyValue{
		attribute.String("provider", provider),
		attribute.String("model", model),
	}
	if m.TokensTotal != nil {
		m.TokensTotal.Add(ctx, int64(input), metric.WithAttributes(append(base,
			attribute.String("direction", "in"))...))
		m.TokensTotal.Add(ctx, int64(output), metric.WithAttributes(append(base,
			attribute.String("direction", "out"))...))
	}
	if m.CacheHitRatio != nil {
		denominator := cacheRead + input
		if denominator > 0 {
			m.CacheHitRatio.Record(ctx, float64(cacheRead)/float64(denominator),
				metric.WithAttributes(base...))
		}
	}
}

// Span name constants for the PLAN §6.2 hierarchy. Keep these stable so
// dashboards don't break on refactors.
const (
	SpanSession          = "stado.session"
	SpanSessionFork      = "stado.session.fork"   // DESIGN §"Phase 9.4 — supervisory trace across forks"
	SpanSessionResume    = "stado.session.resume" // fires when OpenSession's resume-on-cwd branch reattaches an existing session
	SpanTurn             = "stado.turn"
	SpanToolCall         = "stado.tool_call"
	SpanSandboxExec      = "stado.sandbox.exec"
	SpanProviderStream   = "stado.provider.stream"
	SpanTUIRun           = "stado.tui.run"
	SpanTUIProviderProbe = "stado.tui.provider_probe"
)

// TracerName is the instrumentation-library identifier used for every stado
// span. Call sites fetch the tracer via otel.Tracer(TracerName); the global
// provider returns a no-op tracer until Start() wires up a real one, so
// instrumentation code is safe to call unconditionally.
const TracerName = "github.com/foobarto/stado"
