package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/pkg/agent"
)

// fakeProvider is a minimal agent.Provider for identity-comparison in
// tests — we only need to assert which provider value came back, not
// to actually stream anything. The Name() field makes the identity
// easy to read in test failures.
type fakeProvider struct{ name string }

func (f *fakeProvider) Name() string                 { return f.name }
func (f *fakeProvider) Capabilities() agent.Capabilities { return agent.Capabilities{} }
func (f *fakeProvider) StreamTurn(_ context.Context, _ agent.TurnRequest) (<-chan agent.Event, error) {
	return nil, errors.New("fake: not implemented")
}

func TestResolveSupervisorLane_NilCfg_ReturnsFallback(t *testing.T) {
	worker := &fakeProvider{name: "worker"}
	gotP, gotM, err := resolveSupervisorLane(nil, worker, "worker-model", nil)
	if err != nil {
		t.Fatalf("nil cfg: unexpected error: %v", err)
	}
	if gotP != worker {
		t.Errorf("provider = %v, want worker fallback", gotP)
	}
	if gotM != "worker-model" {
		t.Errorf("model = %q, want worker-model", gotM)
	}
}

func TestResolveSupervisorLane_Disabled_ReturnsFallback(t *testing.T) {
	cfg := &config.Config{Supervisor: config.Supervisor{Enabled: false, Provider: "sup", Model: "sup-model"}}
	worker := &fakeProvider{name: "worker"}
	gotP, gotM, err := resolveSupervisorLane(cfg, worker, "worker-model", nil)
	if err != nil {
		t.Fatalf("disabled: unexpected error: %v", err)
	}
	if gotP != worker || gotM != "worker-model" {
		t.Errorf("disabled supervisor should return fallback; got provider=%v model=%q", gotP, gotM)
	}
}

// Enabled but no provider configured (only Enabled=true) — the supervisor
// is effectively un-configured; fall back rather than fail loudly.
func TestResolveSupervisorLane_EnabledNoProvider_ReturnsFallback(t *testing.T) {
	cfg := &config.Config{Supervisor: config.Supervisor{Enabled: true, Provider: ""}}
	worker := &fakeProvider{name: "worker"}
	gotP, gotM, err := resolveSupervisorLane(cfg, worker, "worker-model", nil)
	if err != nil {
		t.Fatalf("enabled-no-provider: unexpected error: %v", err)
	}
	if gotP != worker || gotM != "worker-model" {
		t.Errorf("enabled-but-unconfigured should return fallback; got provider=%v model=%q", gotP, gotM)
	}
}

func TestResolveSupervisorLane_EnabledWithProvider_LookupReturnsSupervisor(t *testing.T) {
	cfg := &config.Config{Supervisor: config.Supervisor{
		Enabled:  true,
		Provider: "ollama-local",
		Model:    "llama3.2",
	}}
	worker := &fakeProvider{name: "worker"}
	sup := &fakeProvider{name: "supervisor"}

	var lookupCalled bool
	var lookupName string
	lookup := func(c *config.Config, name string) (agent.Provider, error) {
		lookupCalled = true
		lookupName = name
		if c != cfg {
			t.Errorf("lookup got cfg %p, want %p", c, cfg)
		}
		return sup, nil
	}

	gotP, gotM, err := resolveSupervisorLane(cfg, worker, "worker-model", lookup)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !lookupCalled {
		t.Error("expected lookup to be called when supervisor configured")
	}
	if lookupName != "ollama-local" {
		t.Errorf("lookup called with name %q, want ollama-local", lookupName)
	}
	if gotP != sup {
		t.Errorf("provider = %v, want supervisor", gotP)
	}
	if gotM != "llama3.2" {
		t.Errorf("model = %q, want llama3.2", gotM)
	}
}

// Per the config contract (`Supervisor.Model` doc: "Empty = use the
// provider's default"), the helper must return "" when Model is empty
// so the supervisor provider applies its own default. Returning the
// worker model would force the supervisor to honor a model name
// chosen for a DIFFERENT provider, usually a mismatch. Copilot caught
// the prior fallback-to-worker behavior on round 1.
func TestResolveSupervisorLane_EmptySupervisorModel_ReturnsEmptyForProviderDefault(t *testing.T) {
	cfg := &config.Config{Supervisor: config.Supervisor{
		Enabled:  true,
		Provider: "ollama-local",
		Model:    "",
	}}
	sup := &fakeProvider{name: "supervisor"}
	lookup := func(_ *config.Config, _ string) (agent.Provider, error) { return sup, nil }

	_, gotM, err := resolveSupervisorLane(cfg, &fakeProvider{}, "worker-model", lookup)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotM != "" {
		t.Errorf("model = %q, want empty so the supervisor provider applies its default", gotM)
	}
}

// Defensive: nil lookup must produce an error (not a panic) when
// supervisor is configured. Caller bug surfaces predictably.
// Sanity-checked at the bottom: nil lookup with supervisor DISABLED
// stays the supported "no supervisor here" sentinel and short-circuits
// to fallback before dereferencing.
func TestResolveSupervisorLane_NilLookup_ReturnsError(t *testing.T) {
	cfg := &config.Config{Supervisor: config.Supervisor{
		Enabled:  true,
		Provider: "ollama-local",
		Model:    "llama3.2",
	}}
	gotP, gotM, err := resolveSupervisorLane(cfg, &fakeProvider{}, "worker-model", nil)
	if err == nil {
		t.Fatal("expected error for nil lookup with configured supervisor, got nil")
	}
	if gotP != nil || gotM != "" {
		t.Errorf("on error, expected zero values; got provider=%v model=%q", gotP, gotM)
	}
	if !strings.Contains(err.Error(), "nil lookup") {
		t.Errorf("error should mention nil lookup; got %v", err)
	}

	off := &config.Config{Supervisor: config.Supervisor{Enabled: false}}
	worker := &fakeProvider{name: "worker"}
	if p, m, err := resolveSupervisorLane(off, worker, "worker-model", nil); err != nil || p != worker || m != "worker-model" {
		t.Errorf("disabled+nil-lookup should return fallback cleanly; got p=%v m=%q err=%v", p, m, err)
	}
}

// Defensive: lookup returning (nil, nil) is a buggy lookup impl, but
// propagating nil to startBtw would crash on Capabilities() /
// StreamTurn downstream. Fail closed here with a descriptive error
// instead of pushing the nil into the call site.
func TestResolveSupervisorLane_LookupReturnsNilProvider_ReturnsError(t *testing.T) {
	cfg := &config.Config{Supervisor: config.Supervisor{
		Enabled:  true,
		Provider: "ollama-local",
		Model:    "llama3.2",
	}}
	lookup := func(_ *config.Config, _ string) (agent.Provider, error) { return nil, nil }

	gotP, gotM, err := resolveSupervisorLane(cfg, &fakeProvider{}, "worker-model", lookup)
	if err == nil {
		t.Fatal("expected error for (nil, nil) lookup return, got nil")
	}
	if gotP != nil || gotM != "" {
		t.Errorf("on error, expected zero values; got provider=%v model=%q", gotP, gotM)
	}
	if !strings.Contains(err.Error(), "nil provider") {
		t.Errorf("error should mention nil provider; got %v", err)
	}
}

// Lookup failure is the load-bearing case: silent fallback to the
// worker would defeat the supervisor trust boundary (transcript ships
// to the wrong provider). Must propagate the error so the caller can
// surface it to the operator.
func TestResolveSupervisorLane_LookupFails_ReturnsError(t *testing.T) {
	cfg := &config.Config{Supervisor: config.Supervisor{
		Enabled:  true,
		Provider: "bad-provider-name",
		Model:    "irrelevant",
	}}
	worker := &fakeProvider{name: "worker"}
	lookup := func(_ *config.Config, _ string) (agent.Provider, error) {
		return nil, errors.New("no such provider preset")
	}

	gotP, gotM, err := resolveSupervisorLane(cfg, worker, "worker-model", lookup)
	if err == nil {
		t.Fatal("expected error from lookup failure, got nil")
	}
	if gotP != nil || gotM != "" {
		t.Errorf("on error, provider/model should be zero values; got %v / %q", gotP, gotM)
	}
	if !strings.Contains(err.Error(), "bad-provider-name") {
		t.Errorf("error should name the misconfigured provider, got %v", err)
	}
	if !strings.Contains(err.Error(), "no such provider preset") {
		t.Errorf("error should wrap lookup error, got %v", err)
	}
}
