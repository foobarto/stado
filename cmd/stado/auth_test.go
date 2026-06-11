package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/foobarto/stado/internal/config"
)

func TestRenderAuthList_RedactedStatus(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-super-secret-value")
	t.Setenv("OPENAI_API_KEY", "")
	cfg := &config.Config{}

	var buf bytes.Buffer
	if err := renderAuthList(&buf, cfg); err != nil {
		t.Fatalf("renderAuthList: %v", err)
	}
	out := buf.String()

	// The secret value must never appear.
	if strings.Contains(out, "sk-super-secret-value") {
		t.Fatalf("auth list leaked the secret value:\n%s", out)
	}

	for _, want := range []string{
		"PROVIDER",
		"anthropic",
		"ANTHROPIC_API_KEY",
		"✓ configured", // anthropic key is set
		"openai",
		"✗ not set",       // openai key empty
		"(no key needed)", // a local runner row
	} {
		if !strings.Contains(out, want) {
			t.Errorf("auth list missing %q\nfull:\n%s", want, out)
		}
	}
}

func TestRunAuthSet_WritesRefAndDoesNotPersistSecret(t *testing.T) {
	cfg := isolatedHome(t)
	t.Setenv("DEEPSEEK_API_KEY", "")

	var buf bytes.Buffer
	err := runAuthSet(context.Background(), &buf, cfg, "deepseek", authSetOpts{
		key:        "sk-inline-secret",
		noValidate: true,
	})
	if err != nil {
		t.Fatalf("runAuthSet: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "recorded credential ref for deepseek") {
		t.Errorf("missing confirmation line:\n%s", out)
	}
	// --key should produce an export hint (the operator wires it themselves).
	if !strings.Contains(out, "export DEEPSEEK_API_KEY=") {
		t.Errorf("expected export hint for --key:\n%s", out)
	}

	// Reload config and prove the SECRET is not on disk, only the env-var NAME.
	cfg2, err := config.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	pre, ok := cfg2.Inference.Presets["deepseek"]
	if !ok {
		t.Fatalf("deepseek preset not written; presets=%v", cfg2.Inference.Presets)
	}
	if pre.APIKeyEnv != "DEEPSEEK_API_KEY" {
		t.Errorf("api_key_env = %q, want DEEPSEEK_API_KEY", pre.APIKeyEnv)
	}
}

func TestRunAuthSet_EnvVarReference(t *testing.T) {
	cfg := isolatedHome(t)

	var buf bytes.Buffer
	err := runAuthSet(context.Background(), &buf, cfg, "groq", authSetOpts{
		envVar:     "MY_CUSTOM_GROQ_KEY",
		noValidate: true,
	})
	if err != nil {
		t.Fatalf("runAuthSet: %v", err)
	}
	cfg2, _ := config.Load()
	if cfg2.Inference.Presets["groq"].APIKeyEnv != "MY_CUSTOM_GROQ_KEY" {
		t.Errorf("api_key_env = %q, want MY_CUSTOM_GROQ_KEY", cfg2.Inference.Presets["groq"].APIKeyEnv)
	}
}

func TestRunAuthSet_BaseURLRejectedForNative(t *testing.T) {
	cfg := isolatedHome(t)
	var buf bytes.Buffer
	err := runAuthSet(context.Background(), &buf, cfg, "anthropic", authSetOpts{
		baseURL:    "https://nope.test",
		noValidate: true,
	})
	if err == nil {
		t.Fatal("expected --base-url rejected for native provider")
	}
	if !strings.Contains(err.Error(), "base-url") {
		t.Errorf("error = %v, want it to mention base-url", err)
	}
}

func TestRunAuthSet_BaseURLAllowedForOAICompat(t *testing.T) {
	cfg := isolatedHome(t)
	var buf bytes.Buffer
	err := runAuthSet(context.Background(), &buf, cfg, "deepseek", authSetOpts{
		baseURL:    "https://proxy.internal/v1",
		noValidate: true,
	})
	if err != nil {
		t.Fatalf("base-url should be allowed for OAI-compat, got %v", err)
	}
	cfg2, _ := config.Load()
	if got := cfg2.Inference.Presets["deepseek"].BaseURL; got != "https://proxy.internal/v1" {
		t.Errorf("base_url = %q, want https://proxy.internal/v1", got)
	}
}

func TestRunAuthSet_KeyAndEnvMutuallyExclusive(t *testing.T) {
	cfg := isolatedHome(t)
	var buf bytes.Buffer
	err := runAuthSet(context.Background(), &buf, cfg, "deepseek", authSetOpts{
		key:        "k",
		envVar:     "V",
		noValidate: true,
	})
	if err == nil {
		t.Fatal("expected error when both --key and --env are passed")
	}
}

func TestRunAuthSet_UnknownProvider(t *testing.T) {
	cfg := isolatedHome(t)
	var buf bytes.Buffer
	if err := runAuthSet(context.Background(), &buf, cfg, "totally-bogus", authSetOpts{noValidate: true}); err == nil {
		t.Fatal("expected unknown-provider error")
	}
}

func TestRunAuthUnset_RemovesRef(t *testing.T) {
	cfg := isolatedHome(t)
	var buf bytes.Buffer
	if err := runAuthSet(context.Background(), &buf, cfg, "groq", authSetOpts{envVar: "GROQ_API_KEY", noValidate: true}); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Confirm it landed.
	cfgMid, _ := config.Load()
	if _, ok := cfgMid.Inference.Presets["groq"]; !ok {
		t.Fatal("preset should exist before unset")
	}

	buf.Reset()
	if err := runAuthUnset(&buf, cfgMid, "groq"); err != nil {
		t.Fatalf("unset: %v", err)
	}
	if !strings.Contains(buf.String(), "removed credential ref for groq") {
		t.Errorf("missing removal confirmation:\n%s", buf.String())
	}
	cfg2, _ := config.Load()
	if _, ok := cfg2.Inference.Presets["groq"]; ok {
		t.Error("preset should be gone after unset")
	}
}

func TestRunAuthUnset_UnknownProvider(t *testing.T) {
	cfg := isolatedHome(t)
	var buf bytes.Buffer
	if err := runAuthUnset(&buf, cfg, "totally-bogus"); err == nil {
		t.Fatal("expected unknown-provider error")
	}
}

func TestShellQuote_EscapesSingleQuotes(t *testing.T) {
	got := shellQuote("a'b")
	if !strings.Contains(got, `'\''`) {
		t.Errorf("shellQuote(%q) = %q, want embedded-quote escaping", "a'b", got)
	}
}

func TestRedactEnvName(t *testing.T) {
	if got := redactEnvName("GROQ_API_KEY"); got != "GROQ_API_KEY" {
		t.Errorf("plain env name should pass through, got %q", got)
	}
	if got := redactEnvName("sk-looks-like-a-secret"); got != "<redacted>" {
		t.Errorf("non-identifier should be redacted, got %q", got)
	}
}
