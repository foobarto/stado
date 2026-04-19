package telemetry

import (
	"fmt"

	"go.opentelemetry.io/otel/metric"
)

// Metrics holds the preconstructed instruments used across stado. Building
// them once avoids map lookups on the hot path and makes instrument names a
// single-file contract.
//
// See PLAN.md §6.3 for the baseline metric set.
type Metrics struct {
	ToolLatency       metric.Float64Histogram // ms; attrs: tool, outcome
	TokensTotal       metric.Int64Counter     // attrs: provider, model, direction (in|out)
	CacheHitRatio     metric.Float64Histogram // fraction 0..1; attrs: provider, model
	ApprovalRate      metric.Int64Counter     // attrs: tool, decision (allow|deny)
	SandboxDenials    metric.Int64Counter     // attrs: tool, reason
	SessionsActive    metric.Int64UpDownCounter
}

func newMetrics(m metric.Meter) (Metrics, error) {
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
	out.ApprovalRate, err = m.Int64Counter("stado_approval_rate",
		metric.WithDescription("Approval decisions by tool."))
	if err != nil {
		return out, fmt.Errorf("metric approval_rate: %w", err)
	}
	out.SandboxDenials, err = m.Int64Counter("stado_sandbox_denials_total",
		metric.WithDescription("Sandbox policy denials by tool + reason."))
	if err != nil {
		return out, fmt.Errorf("metric sandbox_denials: %w", err)
	}
	out.SessionsActive, err = m.Int64UpDownCounter("stado_sessions_active",
		metric.WithDescription("Active stado sessions."))
	if err != nil {
		return out, fmt.Errorf("metric sessions_active: %w", err)
	}
	return out, nil
}

// Span name constants for the PLAN §6.2 hierarchy. Keep these stable so
// dashboards don't break on refactors.
const (
	SpanSession       = "stado.session"
	SpanTurn          = "stado.turn"
	SpanToolCall      = "stado.tool_call"
	SpanSandboxExec   = "stado.sandbox.exec"
	SpanProviderStream = "stado.provider.stream"
)

// TracerName is the instrumentation-library identifier used for every stado
// span. Call sites fetch the tracer via otel.Tracer(TracerName); the global
// provider returns a no-op tracer until Start() wires up a real one, so
// instrumentation code is safe to call unconditionally.
const TracerName = "github.com/foobarto/stado"
