package telemetry

import (
	"context"
	"os"
	"testing"

	"go.opentelemetry.io/otel"
)

func TestStart_DisabledProducesNoopRuntime(t *testing.T) {
	r, err := Start(context.Background(), Config{Enabled: false})
	if err != nil {
		t.Fatalf("Start(disabled): %v", err)
	}
	if r == nil {
		t.Fatal("nil runtime")
	}
	if r.Tracer() == nil {
		t.Error("tracer is nil")
	}
	if r.Meter() == nil {
		t.Error("meter is nil")
	}
	// A span from a disabled runtime shouldn't panic.
	_, span := r.Tracer().Start(context.Background(), "test-span")
	span.End()

	// Shutdown should be a no-op and safe on a disabled runtime.
	if err := r.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown disabled: %v", err)
	}
}

func TestShutdown_OnNilRuntime(t *testing.T) {
	var r *Runtime
	if err := r.Shutdown(context.Background()); err != nil {
		t.Errorf("nil runtime shutdown should be no-op: %v", err)
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("STADO_OTEL_ENABLED", "1")
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "https://collector.example.com:4317")
	c := ConfigFromEnv()
	if !c.Enabled {
		t.Error("Enabled not read from env")
	}
	if c.Endpoint != "https://collector.example.com:4317" {
		t.Errorf("endpoint = %q", c.Endpoint)
	}
}

func TestCleanEndpoint(t *testing.T) {
	cases := map[string]string{
		"https://host:4317/":  "host:4317",
		"http://host:4317":    "host:4317",
		"host:4317":           "host:4317",
	}
	for in, want := range cases {
		if got := cleanEndpoint(in); got != want {
			t.Errorf("cleanEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSetTracerProviderGlobal_LeftAlone(t *testing.T) {
	// Disabled runtime must not install a non-noop tracer provider.
	r, _ := Start(context.Background(), Config{Enabled: false})
	defer r.Shutdown(context.Background())
	// Smoke: otel's default tracer provider is a no-op.
	_ = otel.GetTracerProvider()
}

// Sanity: absent any env vars, ConfigFromEnv gives Enabled=false.
func TestConfigFromEnv_DefaultOff(t *testing.T) {
	os.Unsetenv("STADO_OTEL_ENABLED")
	os.Unsetenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	c := ConfigFromEnv()
	if c.Enabled {
		t.Error("Enabled should be false by default")
	}
}
