package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/pelletier/go-toml"
	"github.com/zalando/go-keyring"
)

// Provider-credential storage. ENV-FIRST: a provider credential is
// referenced in config.toml by the NAME of an environment variable
// (api_key_env), never by the secret itself. The secret lives in the
// process environment (the existing APIKeyEnv convention) and, in a
// future increment, an OS keyring fronted by keyringStore.
//
// This file owns the config-side of `stado auth {set,unset,list}`:
//   - WriteProviderCredential writes/updates the [inference.presets.<name>]
//     block recording the env-var NAME (+ optional endpoint / base_url).
//   - RemoveProviderCredential deletes that block.
//   - ProviderCredentialStatus reports whether a credential is resolvable
//     for display, REDACTED — it never returns or echoes the secret.
//
// Secrets themselves are mediated by keyringStore so the OS-keyring
// backend can be slotted in without touching callers. See keyringStore.

// ErrKeyringUnavailable is returned by a keyringStore backend that is not
// usable in the current build / on the current platform.
var ErrKeyringUnavailable = errors.New("keyring backend unavailable")

// keyringStore abstracts where a provider secret is persisted. The
// ENV-FIRST policy means the default backend never persists the secret
// to disk — it reports the env var the operator must export. A real
// OS-keyring backend (Keychain / Secret Service / WinCred) is a future
// increment; it slots in behind this interface with no caller changes.
//
// Key is the env-var NAME (e.g. "ANTHROPIC_API_KEY"); backends key on the
// name so a stored secret and the env-fallback resolve to the same slot.
type keyringStore interface {
	// Name identifies the backend for diagnostics ("env" / "os-keyring").
	Name() string
	// Available reports whether the backend can be used right now.
	Available() bool
	// Get returns the secret for key, or ("", false) when absent.
	Get(key string) (string, bool)
	// Set persists secret under key. Returns ErrKeyringUnavailable when
	// the backend cannot persist (the env backend cannot — secrets stay
	// in the environment, not on disk).
	Set(key, secret string) error
	// Delete removes any persisted secret for key. No-op when absent.
	Delete(key string) error
}

// envKeyringStore is the ENV-FIRST default backend. It resolves secrets
// from the process environment (read-only) and refuses to persist — by
// design, secrets are never written to disk by stado. `auth set --key`
// therefore tells the operator the export line to add to their shell rc.
type envKeyringStore struct{}

func (envKeyringStore) Name() string { return "env" }

func (envKeyringStore) Available() bool { return true }

func (envKeyringStore) Get(key string) (string, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", false
	}
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

func (envKeyringStore) Set(key, secret string) error {
	return fmt.Errorf("%w: the env backend does not persist secrets; export %s in your shell instead", ErrKeyringUnavailable, key)
}

func (envKeyringStore) Delete(key string) error { return nil }

// keyringService is the service name stado registers its provider secrets
// under in the OS keyring. The env-var NAME (e.g. "ANTHROPIC_API_KEY") is
// the per-secret account/user within this service, so a stored secret and
// the env-fallback resolve to the same logical slot. Stable on purpose:
// changing it would orphan previously-stored secrets.
const keyringService = "stado"

// keyringProbeAccount is a reserved account name used only to probe whether
// the OS keyring backend is reachable, without depending on (or mutating)
// any real provider secret. A Get of it returns keyring.ErrNotFound on a
// working backend and a platform/transport error on a broken one.
const keyringProbeAccount = "__stado_probe__"

// osKeyringStore persists provider secrets in the OS-native secret store
// via github.com/zalando/go-keyring (cgo-free): macOS Keychain, freedesktop
// Secret Service / libsecret on Linux, Windows Credential Manager. Secrets
// are keyed by env-var NAME within the "stado" service so the keyring slot
// and the env fallback line up.
//
// KEYRING-FIRST: when Available() reports the backend is reachable,
// `auth set --key` persists the secret here instead of only printing an
// export hint. When the backend is unavailable (headless CI, no Secret
// Service running, unsupported platform), resolveKeyringStore falls through
// to envKeyringStore and the ENV-FIRST behavior is unchanged.
type osKeyringStore struct{}

func (osKeyringStore) Name() string { return "os-keyring" }

// Available probes the backend with a side-effect-free Get of a reserved
// probe account. A working keyring answers with keyring.ErrNotFound (the
// probe was never stored); an unsupported platform or an unreachable Secret
// Service answers with a transport/platform error. Treating only the
// "not found" outcome (or an unexpected success) as available keeps us from
// claiming a keyring we cannot actually write to.
func (osKeyringStore) Available() bool {
	_, err := keyring.Get(keyringService, keyringProbeAccount)
	return err == nil || errors.Is(err, keyring.ErrNotFound)
}

func (osKeyringStore) Get(key string) (string, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", false
	}
	secret, err := keyring.Get(keyringService, key)
	if err != nil || secret == "" {
		return "", false
	}
	return secret, true
}

func (osKeyringStore) Set(key, secret string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return fmt.Errorf("%w: empty key", ErrKeyringUnavailable)
	}
	if err := keyring.Set(keyringService, key, secret); err != nil {
		return fmt.Errorf("%w: %v", ErrKeyringUnavailable, err)
	}
	return nil
}

func (osKeyringStore) Delete(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	if err := keyring.Delete(keyringService, key); err != nil {
		// A missing secret is not an error — Delete is idempotent, matching
		// the env backend's no-op semantics.
		if errors.Is(err, keyring.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("%w: %v", ErrKeyringUnavailable, err)
	}
	return nil
}

// resolveKeyringStore returns the active secret backend in priority order:
// the OS keyring when it is reachable, else the ENV-FIRST fallback. On a
// desktop with a running secret service this returns the os-keyring backend
// (KEYRING-FIRST); on a headless / unsupported host it returns the env
// backend so behavior degrades gracefully.
func resolveKeyringStore() keyringStore {
	if ks := (osKeyringStore{}); ks.Available() {
		return ks
	}
	return envKeyringStore{}
}

// SecretBackendAvailable reports whether the OS keyring can hold provider
// secrets right now. When false, callers should fall back to the ENV-FIRST
// export-hint flow (the env backend never persists to disk). This is the
// CLI's signal for whether `auth set --key` will persist the secret.
func SecretBackendAvailable() bool {
	return resolveKeyringStore().Name() != envKeyringStore{}.Name()
}

// resolveSecretSource reports whether a secret for the env-var NAME keyEnv
// resolves, and from which backend ("os-keyring" / "env"). The keyring is
// consulted first when available; the environment is ALWAYS consulted as a
// fallback so an exported var still counts even on a keyring-equipped host.
// It never returns the secret itself — only presence + source.
func resolveSecretSource(keyEnv string) (source string, present bool) {
	if ks := (osKeyringStore{}); ks.Available() {
		if _, ok := ks.Get(keyEnv); ok {
			return ks.Name(), true
		}
	}
	if _, ok := (envKeyringStore{}).Get(keyEnv); ok {
		return envKeyringStore{}.Name(), true
	}
	return "", false
}

// StoreProviderSecret persists secret under the env-var NAME keyEnv using the
// active backend. It returns the backend name ("os-keyring") and true when
// the secret was persisted. When only the ENV-FIRST backend is available it
// returns ("env", false, nil) WITHOUT error and WITHOUT touching disk — the
// caller is expected to fall back to the export-hint flow. keyEnv and secret
// must both be non-empty; the secret never reaches config.toml.
func StoreProviderSecret(keyEnv, secret string) (backend string, persisted bool, err error) {
	keyEnv = strings.TrimSpace(keyEnv)
	if keyEnv == "" {
		return "", false, fmt.Errorf("empty env-var name")
	}
	if secret == "" {
		return "", false, fmt.Errorf("empty secret")
	}
	store := resolveKeyringStore()
	if store.Name() == (envKeyringStore{}).Name() {
		return store.Name(), false, nil
	}
	if err := store.Set(keyEnv, secret); err != nil {
		return store.Name(), false, err
	}
	return store.Name(), true, nil
}

// DeleteProviderSecret removes any keyring-persisted secret for the env-var
// NAME keyEnv. It is idempotent (a missing secret is not an error) and a
// no-op when the OS keyring is unavailable — the env backend holds nothing
// to delete. The operator's environment variable, if exported, is untouched.
func DeleteProviderSecret(keyEnv string) (backend string, removed bool, err error) {
	keyEnv = strings.TrimSpace(keyEnv)
	if keyEnv == "" {
		return "", false, nil
	}
	store := resolveKeyringStore()
	if store.Name() == (envKeyringStore{}).Name() {
		return store.Name(), false, nil
	}
	// Only report removed when a secret was actually present, so the CLI can
	// phrase its confirmation honestly.
	_, present := store.Get(keyEnv)
	if err := store.Delete(keyEnv); err != nil {
		return store.Name(), false, err
	}
	return store.Name(), present, nil
}

// ProviderCredentialState is the REDACTED, display-safe view of a single
// provider's credential wiring for `stado auth list`. It never carries the
// secret — only the env-var NAME and whether a value is currently
// resolvable, plus where it would resolve from.
type ProviderCredentialState struct {
	Provider string
	Kind     ProviderKind
	// APIKeyEnv is the env-var NAME stado reads for this provider's
	// secret. Empty for local-runner providers that need no key.
	APIKeyEnv string
	// Configured is true when the credential is resolvable right now
	// (the env var / keyring slot holds a non-empty value), or when the
	// provider needs no key at all.
	Configured bool
	// Source names where the secret resolved from ("env", "os-keyring"),
	// or "" when unconfigured. Never the secret itself.
	Source string
	// Endpoint / BaseURL reflect any [inference.presets.<name>] override
	// written by `auth set`. Non-secret; safe to display.
	Endpoint string
	BaseURL  string
	// NeedsKey is false for local-runner providers (no key expected).
	NeedsKey bool
}

// ProviderCredentialStatus builds the REDACTED display state for one
// provider against the loaded config. It consults the configured env-var
// NAME (preset override wins over the registry convention), then asks the
// active keyringStore whether a value resolves — WITHOUT returning the
// value. Callers render the result via the auth list command; the secret
// never crosses this boundary.
func ProviderCredentialStatus(cfg *Config, provider string) (ProviderCredentialState, bool) {
	kp, ok := LookupKnownProvider(provider)
	if !ok {
		return ProviderCredentialState{}, false
	}
	st := ProviderCredentialState{
		Provider: kp.Name,
		Kind:     kp.Kind,
		NeedsKey: kp.APIKeyEnv != "",
	}

	// Preset override (if any) wins for endpoint / base_url / api_key_env.
	var preset InferencePreset
	if cfg != nil && cfg.Inference.Presets != nil {
		preset = cfg.Inference.Presets[kp.Name]
	}
	st.Endpoint = preset.Endpoint
	if st.Endpoint == "" {
		st.Endpoint = kp.Endpoint
	}
	st.BaseURL = preset.BaseURL

	// Resolve the env-var NAME the same way the runtime would.
	keyEnv := strings.TrimSpace(preset.APIKeyEnv)
	if keyEnv == "" {
		keyEnv = kp.APIKeyEnv
	}
	st.APIKeyEnv = keyEnv

	if !st.NeedsKey {
		// Local runner — no key expected; "configured" means reachable
		// config-wise, which for our purposes is always true.
		st.Configured = true
		return st, true
	}

	// A credential is "configured" if it resolves from EITHER the OS keyring
	// OR the environment — they are independent slots and the operator may
	// have used either. Keyring wins for Source when both hold a value
	// (it's what the runtime would resolve first), but a bare exported env
	// var still reports configured even when a keyring is present. This is
	// why we don't short-circuit on resolveKeyringStore() alone.
	if src, present := resolveSecretSource(keyEnv); present {
		st.Configured = true
		st.Source = src
	}
	return st, true
}

// WriteProviderCredential records a provider credential reference in
// config.toml's [inference.presets.<name>] block, ENV-FIRST: it writes the
// env-var NAME (apiKeyEnv) and, for base-URL-overridable kinds, the
// endpoint / base_url overrides. It NEVER writes the secret — the secret
// stays in the environment (or, later, the OS keyring). Empty apiKeyEnv
// falls back to the provider's conventional env var so a bare
// `auth set <provider>` still records the right NAME.
//
// baseURL is accepted ONLY for ProviderKindAnthropicCompatCloud and the
// OAI-compat preset kinds; passing it for any other kind is an error
// (the caller validates kind up front, but this enforces the invariant at
// the storage boundary too).
func WriteProviderCredential(configPath, provider, apiKeyEnv, endpoint, baseURL string) error {
	if strings.TrimSpace(configPath) == "" {
		return fmt.Errorf("config path is empty")
	}
	kp, ok := LookupKnownProvider(provider)
	if !ok {
		return fmt.Errorf("unknown provider %q", provider)
	}
	apiKeyEnv = strings.TrimSpace(apiKeyEnv)
	if apiKeyEnv == "" {
		apiKeyEnv = kp.APIKeyEnv
	}
	endpoint = strings.TrimSpace(endpoint)
	baseURL = strings.TrimSpace(baseURL)
	if baseURL != "" && !providerKindAllowsBaseURL(kp.Kind) {
		return fmt.Errorf("base_url override is not allowed for provider %q (kind %s); only anthropic-compat-cloud and OAI-compat providers accept one", kp.Name, kp.Kind)
	}

	return updateConfig(configPath, func(tree *toml.Tree) {
		if apiKeyEnv != "" {
			tree.SetPath([]string{"inference", "presets", kp.Name, "api_key_env"}, apiKeyEnv)
		}
		if endpoint != "" {
			tree.SetPath([]string{"inference", "presets", kp.Name, "endpoint"}, endpoint)
		}
		if baseURL != "" {
			tree.SetPath([]string{"inference", "presets", kp.Name, "base_url"}, baseURL)
		}
	})
}

// RemoveProviderCredential deletes the [inference.presets.<name>] block for
// a provider. No-op when absent so `auth unset` is idempotent. The secret
// in the environment is untouched — stado does not own the operator's
// shell rc; it only removes the config-side reference.
func RemoveProviderCredential(configPath, provider string) error {
	if strings.TrimSpace(configPath) == "" {
		return fmt.Errorf("config path is empty")
	}
	kp, ok := LookupKnownProvider(provider)
	if !ok {
		return fmt.Errorf("unknown provider %q", provider)
	}
	return updateConfig(configPath, func(tree *toml.Tree) {
		_ = tree.DeletePath([]string{"inference", "presets", kp.Name})
	})
}

// providerKindAllowsBaseURL reports whether a base-URL override is
// meaningful for the given provider kind. Per the design decision, only
// the Anthropic-compat cloud (native SDK + WithBaseURL) and the
// OAI-compat kinds accept one. Native first-party-SDK providers
// (anthropic / openai / google) hit fixed vendor endpoints, so an
// override there is rejected.
func providerKindAllowsBaseURL(k ProviderKind) bool {
	switch k {
	case ProviderKindAnthropicCompatCloud, ProviderKindOAICompatCloud, ProviderKindOAICompatLocal:
		return true
	default:
		return false
	}
}
