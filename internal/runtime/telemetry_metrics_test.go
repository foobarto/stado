package runtime

import (
	"context"
	"encoding/json"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/hooks"
	"github.com/foobarto/stado/internal/telemetry"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/pkg/agent"
	"github.com/foobarto/stado/pkg/tool"
)

func TestAgentLoopRecordsExecutorAndProviderMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	metrics, err := telemetry.NewMetrics(provider.Meter(telemetry.TracerName))
	if err != nil {
		t.Fatalf("NewMetrics: %v", err)
	}
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	reg := tools.NewRegistry()
	reg.Register(metricProbeTool{})
	exec := &tools.Executor{Registry: reg, Metrics: metrics}
	cfg := &config.Config{}
	cfg.Tools.Autoload = []string{"metric_probe"}

	_, _, err = AgentLoop(context.Background(), AgentLoopOptions{
		Provider: &metricProvider{},
		Executor: exec,
		Config:   cfg,
		Metrics:  metrics,
		Hooks: hooks.NewLifecycleRunner(hooks.BuiltinHook{
			HookName: "route-model", Subscribed: []hooks.Point{hooks.PointPreLLM},
			Fn: func(_ context.Context, _ hooks.Point, payload hooks.Payload) (hooks.HookResult, error) {
				mutated := *payload.(*hooks.PreLLMPayload)
				mutated.Model = "routed-model"
				return hooks.Mutate(&mutated), nil
			},
		}),
		Model:    "metric-model",
		Messages: []agent.Message{agent.Text(agent.RoleUser, "measure")},
		MaxTurns: 2,
		Workdir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("AgentLoop: %v", err)
	}

	var collected metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &collected); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	byName := map[string]metricdata.Aggregation{}
	for _, scope := range collected.ScopeMetrics {
		for _, m := range scope.Metrics {
			byName[m.Name] = m.Data
		}
	}

	latency, ok := byName["stado_tool_latency_ms"].(metricdata.Histogram[float64])
	if !ok || len(latency.DataPoints) != 1 || latency.DataPoints[0].Count != 1 {
		t.Fatalf("tool latency aggregation = %#v, want one observation", byName["stado_tool_latency_ms"])
	}
	attrs := latency.DataPoints[0].Attributes
	if got, _ := attrs.Value("tool"); got.AsString() != "metric_probe" {
		t.Fatalf("tool attr = %q, want metric_probe", got.AsString())
	}
	if got, _ := attrs.Value("outcome"); got.AsString() != "ok" {
		t.Fatalf("outcome attr = %q, want ok", got.AsString())
	}

	tokens, ok := byName["stado_tokens_total"].(metricdata.Sum[int64])
	if !ok || len(tokens.DataPoints) != 2 {
		t.Fatalf("tokens aggregation = %#v, want input/output series", byName["stado_tokens_total"])
	}
	var total int64
	for _, point := range tokens.DataPoints {
		total += point.Value
		if got, _ := point.Attributes.Value("model"); got.AsString() != "routed-model" {
			t.Fatalf("token model attr = %q, want routed-model", got.AsString())
		}
	}
	if total != 21 {
		t.Fatalf("token total = %d, want 21", total)
	}

	cache, ok := byName["stado_cache_hit_ratio"].(metricdata.Histogram[float64])
	if !ok || len(cache.DataPoints) != 1 || cache.DataPoints[0].Count != 2 {
		t.Fatalf("cache aggregation = %#v, want two observations", byName["stado_cache_hit_ratio"])
	}
}

type metricProbeTool struct{}

func (metricProbeTool) Name() string           { return "metric_probe" }
func (metricProbeTool) Description() string    { return "metric probe" }
func (metricProbeTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (metricProbeTool) Class() tool.Class      { return tool.ClassNonMutating }
func (metricProbeTool) Run(context.Context, json.RawMessage, tool.Host) (tool.Result, error) {
	return tool.Result{Content: "measured"}, nil
}

type metricProvider struct{ turn int }

func (*metricProvider) Name() string                     { return "metric-provider" }
func (*metricProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (p *metricProvider) StreamTurn(context.Context, agent.TurnRequest) (<-chan agent.Event, error) {
	p.turn++
	events := make(chan agent.Event, 2)
	if p.turn == 1 {
		events <- agent.Event{Kind: agent.EvToolCallEnd, ToolCall: &agent.ToolUseBlock{
			ID: "call-1", Name: "metric_probe", Input: json.RawMessage(`{}`),
		}}
		events <- agent.Event{Kind: agent.EvDone, Usage: &agent.Usage{
			InputTokens: 10, OutputTokens: 4, CacheReadTokens: 5,
		}}
	} else {
		events <- agent.Event{Kind: agent.EvDone, Usage: &agent.Usage{
			InputTokens: 5, OutputTokens: 2,
		}}
	}
	close(events)
	return events, nil
}
