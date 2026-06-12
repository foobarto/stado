package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
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
	keyring.MockInit() // empty, available keyring → env fallback resolves
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
	keyring.MockInit() // empty keyring → neither backend resolves
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
	keyring.MockInit() // empty keyring → env fallback under the preset name
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

func TestOSKeyringStore_SetGetDelete(t *testing.T) {
	keyring.MockInit() // hermetic in-memory backend
	store := osKeyringStore{}

	if !store.Available() {
		t.Fatal("mocked keyring should report available")
	}

	// Unset key resolves to absent.
	if _, ok := store.Get("ANTHROPIC_API_KEY"); ok {
		t.Fatal("Get of an unset key should report absent")
	}

	// Set then Get round-trips the secret, keyed by env-var NAME.
	if err := store.Set("ANTHROPIC_API_KEY", "sk-stored-in-keyring"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, ok := store.Get("ANTHROPIC_API_KEY")
	if !ok || got != "sk-stored-in-keyring" {
		t.Fatalf("Get = (%q,%v), want (sk-stored-in-keyring,true)", got, ok)
	}

	// Delete removes it; a second Delete is a no-op (idempotent).
	if err := store.Delete("ANTHROPIC_API_KEY"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := store.Get("ANTHROPIC_API_KEY"); ok {
		t.Error("Get after Delete should report absent")
	}
	if err := store.Delete("ANTHROPIC_API_KEY"); err != nil {
		t.Errorf("second Delete should be a no-op, got %v", err)
	}
}

func TestResolveKeyringStore_PrefersOSKeyringWhenAvailable(t *testing.T) {
	keyring.MockInit()
	if got := resolveKeyringStore().Name(); got != "os-keyring" {
		t.Errorf("resolveKeyringStore = %q, want os-keyring when backend is available", got)
	}
}

func TestStoreProviderSecret_PersistsToKeyring(t *testing.T) {
	keyring.MockInit()
	backend, persisted, err := StoreProviderSecret("MINIMAX_API_KEY", "mm-secret")
	if err != nil {
		t.Fatalf("StoreProviderSecret: %v", err)
	}
	if !persisted || backend != "os-keyring" {
		t.Fatalf("StoreProviderSecret = (%q,%v), want (os-keyring,true)", backend, persisted)
	}
	// The persisted secret then resolves through the status path.
	t.Setenv("MINIMAX_API_KEY", "") // prove it resolves from keyring, not env
	cfg := &Config{}
	st, ok := ProviderCredentialStatus(cfg, "minimax")
	if !ok || !st.Configured {
		t.Fatalf("status after keyring store: ok=%v configured=%v", ok, st.Configured)
	}
	if st.Source != "os-keyring" {
		t.Errorf("source = %q, want os-keyring", st.Source)
	}
}

func TestResolveProviderSecret_EnvFirstThenKeyring(t *testing.T) {
	keyring.MockInit() // hermetic in-memory keyring

	// 1. Env set → env value wins (most explicit source).
	t.Setenv("RESOLVE_TEST_ENV", "from-env")
	if got := ResolveProviderSecret("RESOLVE_TEST_ENV"); got != "from-env" {
		t.Errorf("env-set resolve = %q, want from-env", got)
	}

	// 2. Env UNSET but keyring holds the secret → keyring value resolves.
	// This is the core of finding #1: a `stado auth set --key`-stored secret
	// must authenticate even when the operator never exports the env var.
	t.Setenv("RESOLVE_TEST_KEYRING", "") // prove env is empty
	if _, _, err := StoreProviderSecret("RESOLVE_TEST_KEYRING", "from-keyring"); err != nil {
		t.Fatalf("seed keyring: %v", err)
	}
	if got := ResolveProviderSecret("RESOLVE_TEST_KEYRING"); got != "from-keyring" {
		t.Errorf("keyring-only resolve = %q, want from-keyring", got)
	}

	// 3. Neither env nor keyring → empty.
	if got := ResolveProviderSecret("RESOLVE_TEST_ABSENT_XYZ"); got != "" {
		t.Errorf("absent resolve = %q, want empty", got)
	}

	// 4. Empty key name → empty (defensive).
	if got := ResolveProviderSecret("  "); got != "" {
		t.Errorf("blank key resolve = %q, want empty", got)
	}
}

func TestWriteProviderCredential_ClearsBaseURLWhenBlank(t *testing.T) {
	path := tempConfigPath(t)
	// Record a base_url override first (deepseek is OAI-compat cloud).
	if err := WriteProviderCredential(path, "deepseek", "DEEPSEEK_API_KEY", "", "https://override.test/v1"); err != nil {
		t.Fatalf("initial write: %v", err)
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "base_url") {
		t.Fatalf("expected base_url stored after first write:\n%s", data)
	}
	// Re-save with a BLANK base_url — the stale override must be removed.
	if err := WriteProviderCredential(path, "deepseek", "DEEPSEEK_API_KEY", "", ""); err != nil {
		t.Fatalf("clearing write: %v", err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "base_url") {
		t.Errorf("base_url should have been cleared, still present:\n%s", data)
	}
	// The rest of the preset block (api_key_env) must survive the clear.
	if !strings.Contains(string(data), `api_key_env = "DEEPSEEK_API_KEY"`) {
		t.Errorf("api_key_env should survive a base_url clear:\n%s", data)
	}
}

func TestStoreProviderSecret_RejectsEmpty(t *testing.T) {
	keyring.MockInit()
	if _, _, err := StoreProviderSecret("", "s"); err == nil {
		t.Error("empty env name should error")
	}
	if _, _, err := StoreProviderSecret("K", ""); err == nil {
		t.Error("empty secret should error")
	}
}

func TestDeleteProviderSecret_RemovesAndIdempotent(t *testing.T) {
	keyring.MockInit()
	if _, _, err := StoreProviderSecret("XAI_API_KEY", "xs"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	backend, removed, err := DeleteProviderSecret("XAI_API_KEY")
	if err != nil {
		t.Fatalf("DeleteProviderSecret: %v", err)
	}
	if backend != "os-keyring" || !removed {
		t.Errorf("DeleteProviderSecret = (%q,%v), want (os-keyring,true)", backend, removed)
	}
	// Deleting again reports removed=false (nothing there) without error.
	if _, removed, err := DeleteProviderSecret("XAI_API_KEY"); err != nil || removed {
		t.Errorf("second delete = (removed=%v, err=%v), want (false, nil)", removed, err)
	}
}
