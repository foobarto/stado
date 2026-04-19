package tui

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/sandbox"
	"github.com/foobarto/stado/internal/tools"
	"github.com/foobarto/stado/internal/tools/bash"
	"github.com/foobarto/stado/internal/tools/fs"
	"github.com/foobarto/stado/internal/tools/rg"
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

// TestBuildProvider_LocalPresetsNeedNoKey sanity-checks that the bundled
// local-runner presets don't hit any env-var check. (Hosted presets
// like groq DO need a key and are tested for that in app_test.go.)
func TestBuildProvider_LocalPresetsNeedNoKey(t *testing.T) {
	for _, name := range []string{"ollama", "llamacpp", "vllm", "lmstudio"} {
		t.Run(name, func(t *testing.T) {
			ep, keyEnv, ok := builtinPreset(name)
			if !ok {
				t.Fatalf("%s preset should exist", name)
			}
			if keyEnv != "" {
				t.Errorf("%s should not require a key env, got %q", name, keyEnv)
			}
			if ep == "" {
				t.Errorf("%s endpoint empty", name)
			}
			if !strings.HasPrefix(ep, "http://localhost:") {
				t.Errorf("%s should default to localhost, got %q", name, ep)
			}
		})
	}
}

// TestPlanMode_FiltersMutatingTools proves the Plan/Do toggle actually
// removes exec + mutating tools from the TurnRequest, so the model
// genuinely cannot act in Plan mode (not just a post-hoc approval loop).
func TestPlanMode_FiltersMutatingTools(t *testing.T) {
	rnd, _ := render.New(theme.Default())
	reg := tools.NewRegistry()
	reg.Register(fs.ReadTool{})     // NonMutating
	reg.Register(fs.WriteTool{})    // Mutating
	reg.Register(fs.EditTool{})     // Mutating
	reg.Register(rg.Tool{})         // NonMutating
	reg.Register(bash.BashTool{})   // Exec
	exec := &tools.Executor{Registry: reg, Runner: sandbox.NoneRunner{}}

	m := NewModel("/tmp", "m", "anthropic",
		func() (agent.Provider, error) { return nil, nil },
		rnd, keys.NewRegistry())
	m.executor = exec

	// Do mode — all five tools visible.
	m.mode = modeDo
	defs := m.toolDefs()
	if len(defs) != 5 {
		t.Errorf("Do mode: want 5 tools, got %d", len(defs))
	}

	// Plan mode — only NonMutating (read + ripgrep).
	m.mode = modePlan
	defs = m.toolDefs()
	if len(defs) != 2 {
		t.Errorf("Plan mode: want 2 tools (read, ripgrep), got %d", len(defs))
	}
	for _, d := range defs {
		if d.Name == "write" || d.Name == "edit" || d.Name == "bash" {
			t.Errorf("Plan mode leaked mutating tool %q", d.Name)
		}
	}
}
