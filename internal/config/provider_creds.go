package config

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/pelletier/go-toml"
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

// osKeyringStore is a STUB for the OS-native secret store
// (macOS Keychain, freedesktop Secret Service / libsecret, Windows
// Credential Manager).
//
// TODO(provider-creds): implement against an OS keyring. The keyring
// DEPENDENCY decision is deferred deliberately — rather than blindly
// pulling in github.com/zalando/go-keyring (the usual pick) or
// 99designs/keyring, this increment ships the interface + env backend
// and leaves the concrete OS backend behind this stub so the dependency
// is a reviewed, separate change. Until then Available() is false and
// every method reports ErrKeyringUnavailable, so the resolver falls
// through to envKeyringStore.
type osKeyringStore struct{}

func (osKeyringStore) Name() string { return "os-keyring" }

func (osKeyringStore) Available() bool { return false }

func (osKeyringStore) Get(string) (string, bool) { return "", false }

func (osKeyringStore) Set(key, _ string) error {
	return fmt.Errorf("%w: os-keyring backend not implemented yet (TODO provider-creds)", ErrKeyringUnavailable)
}

func (osKeyringStore) Delete(string) error {
	return fmt.Errorf("%w: os-keyring backend not implemented yet (TODO provider-creds)", ErrKeyringUnavailable)
}

// resolveKeyringStore returns the active secret backend in priority order:
// OS keyring when available, else the ENV-FIRST fallback. Today the OS
// keyring is a stub (Available()==false), so this always returns the env
// backend — the structure is what lets the OS backend slot in later.
func resolveKeyringStore() keyringStore {
	if os := (osKeyringStore{}); os.Available() {
		return os
	}
	return envKeyringStore{}
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

	store := resolveKeyringStore()
	if _, present := store.Get(keyEnv); present {
		st.Configured = true
		st.Source = store.Name()
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
