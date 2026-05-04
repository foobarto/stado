package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

func TestRenderProvidersList_GroupsAndStatus(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	t.Setenv("OPENAI_API_KEY", "")
	var buf bytes.Buffer
	if err := renderProvidersList(&buf); err != nil {
		t.Fatalf("renderProvidersList: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"Native (first-party SDK)",
		"OAI-compatible — cloud",
		"OAI-compatible — local runner",
		"anthropic",
		"deepseek",
		"ollama",
		"✓ ANTHROPIC_API_KEY set",
		"✗ OPENAI_API_KEY unset",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("renderProvidersList output missing %q\nfull:\n%s", want, out)
		}
	}
}

func TestRenderProviderSetup_Native_PrintOnly(t *testing.T) {
	p, ok := config.LookupKnownProvider("anthropic")
	if !ok {
		t.Fatal("anthropic should be a known provider")
	}
	var buf bytes.Buffer
	if err := renderProviderSetup(&buf, p, false, false, ""); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"export ANTHROPIC_API_KEY=",
		`provider = "anthropic"`,
		"--provider anthropic",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("setup output missing %q\nfull:\n%s", want, out)
		}
	}
}

func TestRenderProviderSetup_OAICompat_Write_AddsPreset(t *testing.T) {
	cfg := isolatedHome(t)
	// Prove it starts empty.
	if len(cfg.Inference.Presets) != 0 {
		t.Fatalf("preset map should start empty, got %v", cfg.Inference.Presets)
	}

	p, ok := config.LookupKnownProvider("deepseek")
	if !ok {
		t.Fatal("deepseek should be a known provider")
	}
	var buf bytes.Buffer
	if err := renderProviderSetup(&buf, p, true, false, ""); err != nil {
		t.Fatalf("renderProviderSetup write: %v", err)
	}
	if !strings.Contains(buf.String(), "✓ wrote [inference.presets.deepseek]") {
		t.Errorf("expected write-confirmation line, got:\n%s", buf.String())
	}

	// Reload and verify the preset block landed.
	cfg2, err := config.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	pre, ok := cfg2.Inference.Presets["deepseek"]
	if !ok {
		t.Fatalf("preset deepseek not in reloaded config; got %v", cfg2.Inference.Presets)
	}
	if pre.Endpoint != "https://api.deepseek.com/v1" {
		t.Errorf("endpoint = %q, want https://api.deepseek.com/v1", pre.Endpoint)
	}
	if pre.APIKeyEnv != "DEEPSEEK_API_KEY" {
		t.Errorf("api_key_env = %q, want DEEPSEEK_API_KEY", pre.APIKeyEnv)
	}

	// Second write WITHOUT --force must refuse.
	buf.Reset()
	err = renderProviderSetup(&buf, p, true, false, "")
	if err == nil {
		t.Fatal("expected refusal to overwrite existing preset without --force")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("expected error to mention --force, got %v", err)
	}

	// Second write WITH --force should succeed.
	buf.Reset()
	if err := renderProviderSetup(&buf, p, true, true, ""); err != nil {
		t.Fatalf("--force write: %v", err)
	}
}

func TestRenderProviderSetup_APIKeyInline(t *testing.T) {
	p, ok := config.LookupKnownProvider("groq")
	if !ok {
		t.Fatal("groq should be a known provider")
	}
	var buf bytes.Buffer
	if err := renderProviderSetup(&buf, p, false, false, "gsk_secret"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, `export GROQ_API_KEY="gsk_secret"`) {
		t.Errorf("expected export-with-key line, got:\n%s", out)
	}
}

// isolatedHome is defined in plugin_install_test.go; re-export check:
// this test file lives in the same `package main` so the helper is in scope.
// We just want a sanity check that the helper produced a usable temp HOME.
func TestIsolatedHomeSetsConfigPath(t *testing.T) {
	cfg := isolatedHome(t)
	if cfg.ConfigPath == "" {
		t.Fatal("isolatedHome should produce a cfg with a non-empty ConfigPath")
	}
	// The config dir should exist (created by config.Load → MkdirAllUnderUserConfig).
	if _, err := os.Stat(filepath.Dir(cfg.ConfigPath)); err != nil {
		t.Errorf("config dir should exist: %v", err)
	}
}
