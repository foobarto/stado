package main

import (
	"context"
	"fmt"
	"time"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/telemetry"
)

var startTelemetryRuntime = telemetry.Start

func telemetryConfigFromLoaded(cfg *config.Config) telemetry.Config {
	out := telemetry.Config{
		Enabled:     cfg.OTel.Enabled,
		Endpoint:    cfg.OTel.Endpoint,
		Protocol:    cfg.OTel.Protocol,
		Insecure:    cfg.OTel.Insecure,
		Headers:     cfg.OTel.Headers,
		SampleRate:  cfg.OTel.SampleRate,
		ServiceName: cfg.OTel.ServiceName,
		Version:     collectBuildInfo().Version,
	}
	env := telemetry.ConfigFromEnv()
	if !out.Enabled {
		out.Enabled = env.Enabled
	}
	if out.Endpoint == "" {
		out.Endpoint = env.Endpoint
	}
	return out
}

func withTelemetry(ctx context.Context, cfg *config.Config, run func(context.Context) error) error {
	startCtx, cancelStart := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelStart()

	rt, err := startTelemetryRuntime(startCtx, telemetryConfigFromLoaded(cfg))
	if err != nil {
		return fmt.Errorf("telemetry: %w", err)
	}

	runErr := run(ctx)

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelStop()
	stopErr := rt.Shutdown(stopCtx)
	if runErr != nil {
		return runErr
	}
	if stopErr != nil {
		return fmt.Errorf("telemetry shutdown: %w", stopErr)
	}
	return nil
}
