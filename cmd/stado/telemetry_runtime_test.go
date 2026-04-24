package main

import (
	"context"
	"errors"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/telemetry"
)

func TestTelemetryConfigFromLoaded_MergesEnvFallbacks(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "collector.example:4317")
	t.Setenv("STADO_OTEL_ENABLED", "1")

	cfg := &config.Config{}
	got := telemetryConfigFromLoaded(cfg)

	if !got.Enabled {
		t.Fatal("expected env fallback to enable telemetry")
	}
	if got.Endpoint != "collector.example:4317" {
		t.Fatalf("endpoint = %q, want collector.example:4317", got.Endpoint)
	}
	if got.Version != collectBuildInfo().Version {
		t.Fatalf("version = %q, want %q", got.Version, collectBuildInfo().Version)
	}
}

func TestWithTelemetry_PropagatesStartError(t *testing.T) {
	old := startTelemetryRuntime
	startTelemetryRuntime = func(context.Context, telemetry.Config) (*telemetry.Runtime, error) {
		return nil, errors.New("boom")
	}
	t.Cleanup(func() { startTelemetryRuntime = old })

	err := withTelemetry(context.Background(), &config.Config{}, func(context.Context) error { return nil })
	if err == nil || err.Error() != "telemetry: boom" {
		t.Fatalf("err = %v, want telemetry: boom", err)
	}
}
