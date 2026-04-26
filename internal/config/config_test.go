package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/instructions"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	// Provider + Model are intentionally empty on a fresh config —
	// stado probes local runners at provider-build time rather than
	// assuming a specific hosted provider as a default.
	if cfg.Defaults.Provider != "" {
		t.Errorf("Defaults.Provider = %q, want empty (probe-at-build)", cfg.Defaults.Provider)
	}
	if cfg.Defaults.Model != "" {
		t.Errorf("Defaults.Model = %q, want empty", cfg.Defaults.Model)
	}
	if cfg.Approvals.Mode != "prompt" {
		t.Errorf("Approvals.Mode = %q, want %q", cfg.Approvals.Mode, "prompt")
	}
	if cfg.TUI.ThinkingDisplay != "show" {
		t.Errorf("TUI.ThinkingDisplay = %q, want show", cfg.TUI.ThinkingDisplay)
	}
	if cfg.TUI.Theme != "" {
		t.Errorf("TUI.Theme = %q, want empty", cfg.TUI.Theme)
	}
	if cfg.Agent.SystemPromptPath == "" {
		t.Fatal("Agent.SystemPromptPath should default to a config-dir template")
	}
	if cfg.Agent.SystemPromptTemplate == "" {
		t.Fatal("Agent.SystemPromptTemplate should be loaded")
	}
	if !strings.Contains(cfg.Agent.SystemPromptTemplate, "Cairn workflow defaults") {
		t.Fatalf("default system prompt template should include cairn workflow defaults")
	}
	if _, err := os.Stat(cfg.Agent.SystemPromptPath); err != nil {
		t.Fatalf("default system prompt template not created: %v", err)
	}
}

func TestLoadCustomTUIThinkingDisplay(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	configDir := filepath.Join(cfgHome, "stado")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[tui]\nthinking_display = \"tail\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.TUI.ThinkingDisplay != "tail" {
		t.Fatalf("TUI.ThinkingDisplay = %q, want tail", cfg.TUI.ThinkingDisplay)
	}
}

func TestEnvOverride(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("STADO_DEFAULTS_PROVIDER", "openai")
	t.Setenv("STADODEFAULTS_MODEL", "gpt-4o")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Defaults.Provider != "openai" {
		t.Errorf("Defaults.Provider = %q, want %q", cfg.Defaults.Provider, "openai")
	}
}

func TestConfigPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	expected := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "stado", "config.toml")
	if cfg.ConfigPath != expected {
		t.Errorf("ConfigPath = %q, want %q", cfg.ConfigPath, expected)
	}
}

func TestLoadRejectsSymlinkedConfigDir(t *testing.T) {
	cfgHome := t.TempDir()
	target := filepath.Join(cfgHome, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("target", filepath.Join(cfgHome, "stado")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	_, err := Load()
	if err == nil {
		t.Fatal("Load should reject symlinked config dir")
	}
	if _, statErr := os.Stat(filepath.Join(target, defaultSystemPromptFilename)); !os.IsNotExist(statErr) {
		t.Fatalf("symlink target was modified, stat err = %v", statErr)
	}
}

func TestLoadCustomSystemPromptPath(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	customPath := filepath.Join(cfgHome, "custom-system.md")
	if err := os.WriteFile(customPath, []byte("model={{ .Model }} project={{ .ProjectInstructions }}"), 0o600); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(cfgHome, "stado")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("agent.system_prompt_path = "+quoteTOML(customPath)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Agent.SystemPromptPath != customPath {
		t.Fatalf("SystemPromptPath = %q, want %q", cfg.Agent.SystemPromptPath, customPath)
	}
	if !strings.Contains(cfg.Agent.SystemPromptTemplate, "{{ .Model }}") {
		t.Fatalf("custom template not loaded: %q", cfg.Agent.SystemPromptTemplate)
	}
}

func TestLoadRejectsOversizedSystemPromptTemplate(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	customPath := filepath.Join(cfgHome, "huge-system.md")
	body := strings.Repeat("x", int(maxSystemPromptTemplateBytes)+1)
	if err := os.WriteFile(customPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(cfgHome, "stado")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configDir, "config.toml")
	if err := os.WriteFile(configPath, []byte("agent.system_prompt_path = "+quoteTOML(customPath)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected oversized system prompt error, got %v", err)
	}
}

func TestLoadRejectsInvalidSystemPromptTemplate(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	configDir := filepath.Join(cfgHome, "stado")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "system-prompt.md"), []byte("{{ .Missing }}"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "validate [agent].system_prompt_path") {
		t.Fatalf("expected template validation error, got %v", err)
	}
}

func TestLoadUpdatesUntouchedLegacyDefaultSystemPromptTemplate(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	configDir := filepath.Join(cfgHome, "stado")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	promptPath := filepath.Join(configDir, "system-prompt.md")
	if err := os.WriteFile(promptPath, []byte(legacyDefaultSystemPromptTemplateForTest), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Agent.SystemPromptTemplate != instructions.DefaultSystemPromptTemplate {
		t.Fatalf("legacy generated prompt was not updated")
	}
}

func TestLoadRejectsDefaultSystemPromptSymlink(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	configDir := filepath.Join(cfgHome, "stado")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outsidePrompt := filepath.Join(t.TempDir(), "outside-system-prompt.md")
	if err := os.WriteFile(outsidePrompt, []byte(legacyDefaultSystemPromptTemplateForTest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePrompt, filepath.Join(configDir, "system-prompt.md")); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "default system prompt template is a symlink") {
		t.Fatalf("expected default system prompt symlink rejection, got %v", err)
	}
	data, err := os.ReadFile(outsidePrompt)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != legacyDefaultSystemPromptTemplateForTest {
		t.Fatal("default system prompt upgrade rewrote through a symlink")
	}
}

func TestLoadLeavesCustomSystemPromptTemplateUntouched(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	configDir := filepath.Join(cfgHome, "stado")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	custom := "custom model={{ .Model }} project={{ .ProjectInstructions }}"
	promptPath := filepath.Join(configDir, "system-prompt.md")
	if err := os.WriteFile(promptPath, []byte(custom), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Agent.SystemPromptTemplate != custom {
		t.Fatalf("custom prompt was overwritten: %q", cfg.Agent.SystemPromptTemplate)
	}
}

func TestStateDir(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	expected := filepath.Join(os.Getenv("XDG_DATA_HOME"), "stado")
	if cfg.StateDir() != expected {
		t.Errorf("StateDir() = %q, want %q", cfg.StateDir(), expected)
	}
}

func quoteTOML(s string) string {
	return `"` + strings.ReplaceAll(s, `\`, `\\`) + `"`
}

const legacyDefaultSystemPromptTemplateForTest = `You are stado, an AI coding agent running in the stado terminal or CLI.

Identity:
- Identify as stado when asked what you are.
- Do not claim to be Claude Code, Anthropic Claude, OpenCode, Cursor, Aider, or another client.
- If asked which model you are, report the active provider/model metadata below when present; otherwise say that the host did not provide a model id.

Active runtime:
{{- if .Provider }}
- provider: {{ .Provider }}
{{- end }}
{{- if .Model }}
- model: {{ .Model }}
{{- end }}
{{- if and (not .Provider) (not .Model) }}
- provider/model: not provided by host
{{- end }}

Problem-solving defaults:
- First understand the user's goal and the current state. Inspect relevant files, config, logs, tests, and command output before changing behavior.
- Prefer the smallest coherent fix that solves the actual problem. Avoid speculative rewrites and unrelated cleanup.
- Preserve user work. Do not discard, revert, overwrite, or reset changes unless the user explicitly asks.
- When requirements are ambiguous, make a conservative assumption and state it. Ask only when a wrong assumption would be expensive or unsafe.
- Use tools deliberately. Prefer fast local search (rg when available), structured parsers, existing project helpers, and the repository's current patterns.
- Verify changes with the narrowest useful check first, then broader tests when the blast radius warrants it. If verification cannot run, say exactly why.
- Be honest about uncertainty. Do not invent command output, file contents, citations, test results, or capabilities.
- Keep communication concise and actionable. Lead with what changed, what was verified, and what remains.

Coding-agent behavior:
- Treat project instructions as additional guidance, not as a replacement for the stado identity above.
- Follow security and sandbox boundaries. Avoid destructive commands and risky filesystem operations unless explicitly requested.
- For code changes, prefer surgical patches, readable names, focused tests, and behavior-preserving refactors only when needed.
- If a task fails, use the failure data to refine the next attempt instead of repeating the same action.

{{- if .ProjectInstructions }}
Project instructions:
{{ .ProjectInstructions }}
{{- end }}
`
