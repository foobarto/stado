package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tempConfigPath returns a writable config.toml path under a temp dir.
func tempConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "config.toml")
}

func TestWriteProviderCredential_RecordsEnvNameAndEndpoint(t *testing.T) {
	path := tempConfigPath(t)

	// deepseek is OAI-compat cloud — base_url override allowed.
	if err := WriteProviderCredential(path, "deepseek", "MY_DEEPSEEK_KEY", "https://example.test/v1", "https://override.test/v1"); err != nil {
		t.Fatalf("WriteProviderCredential: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	got := string(data)
	for _, want := range []string{
		"[inference.presets.deepseek]",
		`api_key_env = "MY_DEEPSEEK_KEY"`,
		`endpoint = "https://example.test/v1"`,
		`base_url = "https://override.test/v1"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config missing %q\nfull:\n%s", want, got)
		}
	}
}

func TestWriteProviderCredential_DefaultsToConventionalEnv(t *testing.T) {
	path := tempConfigPath(t)
	// Empty apiKeyEnv → fall back to the provider's conventional name.
	if err := WriteProviderCredential(path, "groq", "", "", ""); err != nil {
		t.Fatalf("WriteProviderCredential: %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `api_key_env = "GROQ_API_KEY"`) {
		t.Errorf("expected conventional env var recorded, got:\n%s", data)
	}
}

func TestWriteProviderCredential_NeverWritesSecret(t *testing.T) {
	path := tempConfigPath(t)
	// The API contract: only NAMES, never secrets, reach the function.
	// Confirm the written file contains no obvious secret material.
	if err := WriteProviderCredential(path, "minimax", "MINIMAX_API_KEY", "", ""); err != nil {
		t.Fatalf("WriteProviderCredential: %v", err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "sk-") || strings.Contains(string(data), "secret") {
		t.Errorf("config unexpectedly contains secret-looking material:\n%s", data)
	}
}

func TestWriteProviderCredential_BaseURLRejectedForNative(t *testing.T) {
	path := tempConfigPath(t)
	// anthropic is native (first-party SDK) — base_url override not allowed.
	err := WriteProviderCredential(path, "anthropic", "ANTHROPIC_API_KEY", "", "https://nope.test")
	if err == nil {
		t.Fatal("expected base_url to be rejected for native provider")
	}
	if !strings.Contains(err.Error(), "base_url") {
		t.Errorf("error = %v, want it to mention base_url", err)
	}
	// And nothing should have been written.
	if _, statErr := os.Stat(path); statErr == nil {
		t.Error("config file should not have been created on a rejected write")
	}
}

func TestWriteProviderCredential_BaseURLAllowedForAnthropicCompat(t *testing.T) {
	path := tempConfigPath(t)
	// minimax-anthropic is anthropic-compat-cloud — override allowed.
	if err := WriteProviderCredential(path, "minimax-anthropic", "MINIMAX_API_KEY", "", "https://api.minimax.io/anthropic"); err != nil {
		t.Fatalf("expected base_url allowed for anthropic-compat-cloud, got %v", err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `base_url = "https://api.minimax.io/anthropic"`) {
		t.Errorf("base_url not written:\n%s", data)
	}
}

func TestWriteProviderCredential_UnknownProvider(t *testing.T) {
	path := tempConfigPath(t)
	if err := WriteProviderCredential(path, "nope-not-real", "X", "", ""); err == nil {
		t.Fatal("expected unknown provider error")
	}
}

func TestRemoveProviderCredential_Idempotent(t *testing.T) {
	path := tempConfigPath(t)
	if err := WriteProviderCredential(path, "groq", "GROQ_API_KEY", "https://api.groq.com/openai/v1", ""); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := RemoveProviderCredential(path, "groq"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "[inference.presets.groq]") {
		t.Errorf("preset block should be gone:\n%s", data)
	}
	// Second remove is a no-op, not an error.
	if err := RemoveProviderCredential(path, "groq"); err != nil {
		t.Fatalf("second remove should be no-op, got %v", err)
	}
}

func TestProviderCredentialStatus_ConfiguredFromEnv(t *testing.T) {
	t.Setenv("GROQ_API_KEY", "gsk_present")
	cfg := &Config{}
	st, ok := ProviderCredentialStatus(cfg, "groq")
	if !ok {
		t.Fatal("groq should be a known provider")
	}
	if !st.Configured {
		t.Error("expected configured=true when env var is set")
	}
	if st.Source != "env" {
		t.Errorf("source = %q, want env", st.Source)
	}
	if st.APIKeyEnv != "GROQ_API_KEY" {
		t.Errorf("APIKeyEnv = %q, want GROQ_API_KEY", st.APIKeyEnv)
	}
}

func TestProviderCredentialStatus_UnsetWhenEnvMissing(t *testing.T) {
	t.Setenv("XAI_API_KEY", "")
	cfg := &Config{}
	st, ok := ProviderCredentialStatus(cfg, "xai")
	if !ok {
		t.Fatal("xai should be a known provider")
	}
	if st.Configured {
		t.Error("expected configured=false when env var is empty")
	}
	if st.Source != "" {
		t.Errorf("source should be empty when unconfigured, got %q", st.Source)
	}
}

func TestProviderCredentialStatus_LocalRunnerNeedsNoKey(t *testing.T) {
	cfg := &Config{}
	st, ok := ProviderCredentialStatus(cfg, "ollama")
	if !ok {
		t.Fatal("ollama should be a known provider")
	}
	if st.NeedsKey {
		t.Error("ollama is a local runner; NeedsKey should be false")
	}
	if !st.Configured {
		t.Error("local runner should report configured (no key needed)")
	}
}

func TestProviderCredentialStatus_PresetEnvNameWins(t *testing.T) {
	// A preset api_key_env override should be the resolved name, beating
	// the registry convention.
	t.Setenv("CUSTOM_MINIMAX_KEY", "present")
	t.Setenv("MINIMAX_API_KEY", "")
	cfg := &Config{}
	cfg.Inference.Presets = map[string]InferencePreset{
		"minimax": {APIKeyEnv: "CUSTOM_MINIMAX_KEY", Endpoint: "https://api.minimax.io/v1"},
	}
	st, ok := ProviderCredentialStatus(cfg, "minimax")
	if !ok {
		t.Fatal("minimax should be known")
	}
	if st.APIKeyEnv != "CUSTOM_MINIMAX_KEY" {
		t.Errorf("APIKeyEnv = %q, want the preset override CUSTOM_MINIMAX_KEY", st.APIKeyEnv)
	}
	if !st.Configured {
		t.Error("should resolve via the preset env name")
	}
}

func TestEnvKeyringStore_DoesNotPersist(t *testing.T) {
	store := envKeyringStore{}
	if err := store.Set("ANY_KEY", "secret"); err == nil {
		t.Fatal("env backend must refuse to persist secrets")
	}
	t.Setenv("PROBE_KEY", "v")
	if v, ok := store.Get("PROBE_KEY"); !ok || v != "v" {
		t.Errorf("Get = (%q,%v), want (v,true)", v, ok)
	}
	if _, ok := store.Get("DEFINITELY_UNSET_KEY_XYZ"); ok {
		t.Error("Get of unset key should report absent")
	}
}

func TestOSKeyringStore_StubUnavailable(t *testing.T) {
	store := osKeyringStore{}
	if store.Available() {
		t.Fatal("os-keyring stub should report unavailable until implemented")
	}
	if err := store.Set("K", "s"); err == nil {
		t.Error("stub Set should error")
	}
}

func TestResolveKeyringStore_FallsBackToEnv(t *testing.T) {
	// With the OS keyring stubbed unavailable, the resolver returns env.
	if got := resolveKeyringStore().Name(); got != "env" {
		t.Errorf("resolveKeyringStore = %q, want env (os-keyring stubbed)", got)
	}
}
