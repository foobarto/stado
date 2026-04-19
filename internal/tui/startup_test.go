package tui

import (
	"errors"
	"os"
	"testing"

	"github.com/foobarto/stado/internal/tui/keys"
	"github.com/foobarto/stado/internal/tui/render"
	"github.com/foobarto/stado/internal/tui/theme"
	"github.com/foobarto/stado/pkg/agent"
)

// TestStartup_NoProviderKey proves stado's TUI boot path does NOT require
// any provider API key. The Model is constructed with a deferred builder;
// the provider is only resolved on the first user prompt.
//
// Regression guard: earlier iterations of stado called provider.New() in
// Run() and failed-loudly at startup when ANTHROPIC_API_KEY wasn't set.
// See commit a54ad9a (TUI: lazy provider init).
func TestStartup_NoProviderKey(t *testing.T) {
	// Wipe every relevant env var for the duration of this test.
	for _, k := range []string{
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY",
		"GROQ_API_KEY", "OPENROUTER_API_KEY", "DEEPSEEK_API_KEY",
		"XAI_API_KEY", "MISTRAL_API_KEY", "CEREBRAS_API_KEY", "LITELLM_API_KEY",
	} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}

	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	reg := keys.NewRegistry()

	builderCalled := false
	builder := func() (agent.Provider, error) {
		builderCalled = true
		return nil, errors.New("anthropic: ANTHROPIC_API_KEY not set")
	}

	m := NewModel("/tmp", "claude-sonnet-4-5", "anthropic", builder, rnd, reg)
	if m == nil {
		t.Fatal("NewModel returned nil")
	}
	if builderCalled {
		t.Error("startup MUST NOT invoke the provider builder — lazy init is the contract")
	}

	// Cosmetic rendering paths must work without a provider (status bar,
	// sidebar). The sidebar uses providerDisplayName which falls back to
	// the configured name when the real provider isn't instantiated yet.
	if got := m.providerDisplayName(); got != "anthropic" {
		t.Errorf("providerDisplayName before lazy init = %q, want 'anthropic'", got)
	}

	caps := m.providerCaps()
	if caps.MaxContextTokens != 0 {
		t.Errorf("providerCaps before lazy init should be zero-value, got %+v", caps)
	}

	// ensureProvider on a builder that errors must NOT panic; it should
	// transition the model to stateError and append a system block.
	if ok := m.ensureProvider(); ok {
		t.Error("ensureProvider should return false when builder errors")
	}
	if !builderCalled {
		t.Error("ensureProvider should call the builder")
	}
	if m.state != stateError {
		t.Errorf("state after failed ensureProvider = %v, want stateError", m.state)
	}
	var hasSystem bool
	for _, b := range m.blocks {
		if b.kind == "system" {
			hasSystem = true
		}
	}
	if !hasSystem {
		t.Error("expected a system-role block describing the provider error")
	}
}

// TestBuildProvider_LocalOllamaNeedsNoKey sanity-checks that the bundled
// local presets don't hit any env-var check. (Hosted presets like groq DO
// need a key and are tested for that in app_test.go.)
func TestBuildProvider_LocalOllamaNeedsNoKey(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "")
	// builtinPreset lookup for "ollama" should resolve without a key env.
	ep, keyEnv, ok := builtinPreset("ollama")
	if !ok {
		t.Fatal("ollama preset should exist")
	}
	if keyEnv != "" {
		t.Errorf("ollama should not require a key env, got %q", keyEnv)
	}
	if ep == "" {
		t.Error("ollama endpoint empty")
	}
}
