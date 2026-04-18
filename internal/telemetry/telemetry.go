// Package telemetry is stado's OpenTelemetry setup.
//
// Off by default. Enabled by setting [otel] in config.toml or env
// STADO_OTEL_ENABLED=true. Supports OTLP over gRPC (default) or HTTP.
//
// Span hierarchy (PLAN.md §6.2):
//
//	stado.session
//	 └─ stado.turn
//	     └─ stado.tool_call
//	         └─ stado.sandbox.exec
//	     └─ stado.provider.stream
package telemetry

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// Config is the loadable telemetry config (maps to [otel] in config.toml).
type Config struct {
	Enabled   bool              `koanf:"enabled"`
	Endpoint  string            `koanf:"endpoint"`
	Protocol  string            `koanf:"protocol"` // "grpc" (default) | "http"
	Insecure  bool              `koanf:"insecure"`
	Headers   map[string]string `koanf:"headers"`
	Timeout   time.Duration     `koanf:"timeout"`
	SampleRate float64          `koanf:"sample_rate"` // 0.0..1.0
	ServiceName string          `koanf:"service_name"`
	Version     string          `koanf:"version"`
}

// Runtime is the started-up telemetry state. Shutdown must be called on exit.
type Runtime struct {
	tp         *sdktrace.TracerProvider
	mp         *sdkmetric.MeterProvider
	tracer     trace.Tracer
	meter      metric.Meter
	metrics    Metrics
}

// Tracer returns the stado tracer (no-op if disabled).
func (r *Runtime) Tracer() trace.Tracer { return r.tracer }

// Meter returns the stado meter (no-op if disabled).
func (r *Runtime) Meter() metric.Meter { return r.meter }

// M returns the preconstructed metric instruments.
func (r *Runtime) M() Metrics { return r.metrics }

// Shutdown flushes exporters and tears down providers. Safe to call when
// telemetry was never started (nil *Runtime).
func (r *Runtime) Shutdown(ctx context.Context) error {
	if r == nil {
		return nil
	}
	var firstErr error
	if r.tp != nil {
		if err := r.tp.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("trace shutdown: %w", err)
		}
	}
	if r.mp != nil {
		if err := r.mp.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("meter shutdown: %w", err)
		}
	}
	return firstErr
}

// Start initialises tracing + metrics if enabled. When disabled returns a
// Runtime whose Tracer/Meter are no-ops — callers can unconditionally use
// r.Tracer()/r.Meter().
func Start(ctx context.Context, cfg Config) (*Runtime, error) {
	if !cfg.Enabled {
		return noop()
	}

	if cfg.ServiceName == "" {
		cfg.ServiceName = "stado"
	}
	if cfg.Version == "" {
		cfg.Version = "0.0.0-dev"
	}
	if cfg.Protocol == "" {
		cfg.Protocol = "grpc"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.SampleRate == 0 {
		cfg.SampleRate = 1.0
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(cfg.ServiceName),
			semconv.ServiceVersion(cfg.Version),
		),
		resource.WithHost(),
		resource.WithProcess(),
		resource.WithOS(),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: resource: %w", err)
	}

	tp, err := buildTracerProvider(ctx, cfg, res)
	if err != nil {
		return nil, err
	}
	mp, err := buildMeterProvider(ctx, cfg, res)
	if err != nil {
		tp.Shutdown(ctx)
		return nil, err
	}

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)

	tracer := tp.Tracer("github.com/foobarto/stado")
	meter := mp.Meter("github.com/foobarto/stado")
	m, err := newMetrics(meter)
	if err != nil {
		tp.Shutdown(ctx)
		mp.Shutdown(ctx)
		return nil, err
	}
	return &Runtime{
		tp:      tp,
		mp:      mp,
		tracer:  tracer,
		meter:   meter,
		metrics: m,
	}, nil
}

func noop() (*Runtime, error) {
	// A Runtime with nil tp/mp; the no-op tracer/meter come from the otel
	// global providers which default to no-op until SetTracerProvider runs.
	return &Runtime{
		tracer:  otel.Tracer("github.com/foobarto/stado"),
		meter:   otel.Meter("github.com/foobarto/stado"),
		metrics: Metrics{},
	}, nil
}

func buildTracerProvider(ctx context.Context, cfg Config, res *resource.Resource) (*sdktrace.TracerProvider, error) {
	var exp sdktrace.SpanExporter
	var err error
	switch strings.ToLower(cfg.Protocol) {
	case "http":
		exp, err = otlptrace.New(ctx, otlptracehttp.NewClient(httpTraceOpts(cfg)...))
	default:
		exp, err = otlptrace.New(ctx, otlptracegrpc.NewClient(grpcTraceOpts(cfg)...))
	}
	if err != nil {
		return nil, fmt.Errorf("telemetry: trace exporter: %w", err)
	}
	return sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(cfg.SampleRate)),
	), nil
}

func buildMeterProvider(ctx context.Context, cfg Config, res *resource.Resource) (*sdkmetric.MeterProvider, error) {
	var reader sdkmetric.Reader
	switch strings.ToLower(cfg.Protocol) {
	case "http":
		exp, err := otlpmetrichttp.New(ctx, httpMetricOpts(cfg)...)
		if err != nil {
			return nil, fmt.Errorf("telemetry: metric exporter: %w", err)
		}
		reader = sdkmetric.NewPeriodicReader(exp)
	default:
		exp, err := otlpmetricgrpc.New(ctx, grpcMetricOpts(cfg)...)
		if err != nil {
			return nil, fmt.Errorf("telemetry: metric exporter: %w", err)
		}
		reader = sdkmetric.NewPeriodicReader(exp)
	}
	return sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(reader),
	), nil
}

// ConfigFromEnv fills defaults from OTEL_EXPORTER_OTLP_ENDPOINT if set, so
// stado picks up a locally-running collector without explicit config.
func ConfigFromEnv() Config {
	c := Config{}
	if v := os.Getenv("STADO_OTEL_ENABLED"); v == "true" || v == "1" {
		c.Enabled = true
	}
	if v := os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"); v != "" {
		c.Endpoint = v
	}
	return c
}

// --- exporter option builders ---

func grpcTraceOpts(cfg Config) []otlptracegrpc.Option {
	opts := []otlptracegrpc.Option{otlptracegrpc.WithTimeout(cfg.Timeout)}
	if cfg.Endpoint != "" {
		opts = append(opts, otlptracegrpc.WithEndpoint(cleanEndpoint(cfg.Endpoint)))
	}
	if cfg.Insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}
	if len(cfg.Headers) > 0 {
		opts = append(opts, otlptracegrpc.WithHeaders(cfg.Headers))
	}
	return opts
}

func httpTraceOpts(cfg Config) []otlptracehttp.Option {
	opts := []otlptracehttp.Option{otlptracehttp.WithTimeout(cfg.Timeout)}
	if cfg.Endpoint != "" {
		opts = append(opts, otlptracehttp.WithEndpoint(cleanEndpoint(cfg.Endpoint)))
	}
	if cfg.Insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	if len(cfg.Headers) > 0 {
		opts = append(opts, otlptracehttp.WithHeaders(cfg.Headers))
	}
	return opts
}

func grpcMetricOpts(cfg Config) []otlpmetricgrpc.Option {
	opts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithTimeout(cfg.Timeout)}
	if cfg.Endpoint != "" {
		opts = append(opts, otlpmetricgrpc.WithEndpoint(cleanEndpoint(cfg.Endpoint)))
	}
	if cfg.Insecure {
		opts = append(opts, otlpmetricgrpc.WithInsecure())
	}
	if len(cfg.Headers) > 0 {
		opts = append(opts, otlpmetricgrpc.WithHeaders(cfg.Headers))
	}
	return opts
}

func httpMetricOpts(cfg Config) []otlpmetrichttp.Option {
	opts := []otlpmetrichttp.Option{otlpmetrichttp.WithTimeout(cfg.Timeout)}
	if cfg.Endpoint != "" {
		opts = append(opts, otlpmetrichttp.WithEndpoint(cleanEndpoint(cfg.Endpoint)))
	}
	if cfg.Insecure {
		opts = append(opts, otlpmetrichttp.WithInsecure())
	}
	if len(cfg.Headers) > 0 {
		opts = append(opts, otlpmetrichttp.WithHeaders(cfg.Headers))
	}
	return opts
}

func cleanEndpoint(s string) string {
	// OTLP exporters want bare host:port. Strip scheme if present.
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	return strings.TrimSuffix(s, "/")
}
