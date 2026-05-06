package tui

import (
	"errors"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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

func TestLocalFallbackReadyMsgSeedsProviderAndSingleModel(t *testing.T) {
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	reg := keys.NewRegistry()
	stub := scenarioStub{}
	m := NewModel("/tmp", "", "", func() (agent.Provider, error) { return stub, nil }, rnd, reg)

	_, _ = m.Update(localFallbackReadyMsg{
		provider:     stub,
		providerName: "lmstudio",
		models:       []string{"qwen3"},
	})
	if m.provider == nil || m.provider.Name() != "scenario-stub" {
		t.Fatalf("provider not seeded from local fallback msg: %#v", m.provider)
	}
	if m.providerName != "lmstudio" {
		t.Fatalf("providerName = %q, want lmstudio", m.providerName)
	}
	if m.model != "qwen3" {
		t.Fatalf("model = %q, want qwen3", m.model)
	}
}

func TestLocalFallbackReadyMsgDoesNotOverwriteExistingModel(t *testing.T) {
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	reg := keys.NewRegistry()
	stub := scenarioStub{}
	m := NewModel("/tmp", "already-set", "", func() (agent.Provider, error) { return stub, nil }, rnd, reg)

	_, _ = m.Update(localFallbackReadyMsg{
		provider:     stub,
		providerName: "lmstudio",
		models:       []string{"qwen3"},
	})
	if m.model != "already-set" {
		t.Fatalf("existing model overwritten: %q", m.model)
	}
}

func TestSubmit_QueuesBehindStartupProviderProbe(t *testing.T) {
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	reg := keys.NewRegistry()
	stub := scenarioStub{}
	m := NewModel("/tmp", "", "", func() (agent.Provider, error) { return stub, nil }, rnd, reg)
	m.width, m.height = 120, 30
	m.providerProbePending = true
	m.tokenCounterChecked = true
	m.tokenCounterPresent = true

	m.input.SetValue("hello")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if m.queuedPrompt != "hello" {
		t.Fatalf("queuedPrompt = %q, want hello", m.queuedPrompt)
	}
	if m.input.Value() != "" {
		t.Fatalf("input should clear while waiting for startup probe, got %q", m.input.Value())
	}
	if len(m.blocks) == 0 || !m.blocks[len(m.blocks)-1].queued {
		t.Fatalf("expected queued user block, got %+v", m.blocks)
	}

	_, _ = m.Update(localFallbackReadyMsg{
		provider:     stub,
		providerName: "lmstudio",
		models:       []string{"qwen3"},
	})

	if m.provider == nil || m.provider.Name() != "scenario-stub" {
		t.Fatalf("provider not seeded after startup probe: %#v", m.provider)
	}
	if m.queuedPrompt != "" {
		t.Fatalf("queuedPrompt should be drained after startup probe, got %q", m.queuedPrompt)
	}
	if m.state != stateStreaming {
		t.Fatalf("state = %v, want streaming after startup probe replay", m.state)
	}
	if len(m.msgs) != 1 || m.msgs[0].Role != agent.RoleUser {
		t.Fatalf("expected queued prompt to be promoted into msgs, got %+v", m.msgs)
	}
	if m.blocks[len(m.blocks)-1].queued {
		t.Fatal("queued marker should clear once the replayed turn starts")
	}
}

func TestStartupProbeFailureRestoresQueuedPrompt(t *testing.T) {
	rnd, err := render.New(theme.Default())
	if err != nil {
		t.Fatal(err)
	}
	reg := keys.NewRegistry()
	m := NewModel("/tmp", "", "", nil, rnd, reg)
	m.width, m.height = 120, 30
	m.providerProbePending = true

	m.input.SetValue("hello")
	_, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.queuedPrompt != "hello" {
		t.Fatalf("queuedPrompt = %q, want hello", m.queuedPrompt)
	}

	_, _ = m.Update(localFallbackReadyMsg{})

	if m.input.Value() != "hello" {
		t.Fatalf("input should be restored after startup probe failure, got %q", m.input.Value())
	}
	if m.queuedPrompt != "" {
		t.Fatalf("queuedPrompt should clear after restore, got %q", m.queuedPrompt)
	}
	if m.state != stateError {
		t.Fatalf("state = %v, want error after startup probe failure", m.state)
	}
	for _, blk := range m.blocks {
		if blk.kind == "user" && blk.queued {
			t.Fatalf("queued user block should be removed after restore: %+v", blk)
		}
	}
}

// TestPlanMode_FiltersMutatingTools proves the Plan/Do toggle actually
// removes exec + mutating tools from the TurnRequest, so the model
// genuinely cannot act in Plan mode (not just a post-hoc approval loop).
//
// EP-0037 lazy-load note: toolDefs() now returns only autoloaded core +
// session-activated tools. The test activates the 5 fixture tools
// explicitly so the Plan/Do filter is the variable under test, not the
// activation surface.
func TestPlanMode_FiltersMutatingTools(t *testing.T) {
	rnd, _ := render.New(theme.Default())
	reg := tools.NewRegistry()
	reg.Register(fs.ReadTool{})   // NonMutating
	reg.Register(fs.WriteTool{})  // Mutating
	reg.Register(fs.EditTool{})   // Mutating
	reg.Register(rg.Tool{})       // NonMutating
	reg.Register(bash.BashTool{}) // Exec
	exec := &tools.Executor{Registry: reg, Runner: sandbox.NoneRunner{}}

	m := NewModel("/tmp", "m", "anthropic",
		func() (agent.Provider, error) { return nil, nil },
		rnd, keys.NewRegistry())
	m.executor = exec
	// Activate the fixture tools so they reach the per-turn surface.
	m.activatedTools = map[string]bool{
		"read":    true,
		"write":   true,
		"edit":    true,
		"ripgrep": true,
		"bash":    true,
	}

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
