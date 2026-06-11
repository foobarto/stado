package tui

// `/provider` modal — the in-TUI counterpart to `stado auth {list,set,
// unset}`. It lists every known provider with its REDACTED credential
// status (env-var NAME + configured/unset marker, never the secret) and
// lets the operator add / modify / remove a credential without dropping to
// a shell.
//
// Storage mirrors the CLI exactly: WriteProviderCredential records the
// env-var NAME (+ optional base_url) in config.toml; StoreProviderSecret
// persists an entered key to the OS keyring when one is available, else
// the form shows the ENV-FIRST export hint. The secret never touches
// config.toml and is never echoed to scrollback — the picker masks the
// key field and the system-block confirmation reports only the env-var
// NAME (redacted) and the backend.

import (
	"fmt"
	"strings"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/tui/providerpicker"
)

// openProviderPicker builds the REDACTED provider rows from
// config.ProviderCredentialStatus and opens the modal. The keyring
// availability gates the form's masked key field vs. the export hint.
func (m *Model) openProviderPicker() error {
	cfg, err := m.sessionActionConfig()
	if err != nil {
		return err
	}
	items := buildProviderItems(cfg)
	if m.providerPick == nil {
		m.providerPick = providerpicker.New()
	}
	m.providerPick.Open(items, config.SecretBackendAvailable(), m.providerName)
	return nil
}

// buildProviderItems turns the known-provider catalogue into REDACTED
// picker rows. The credential status comes from ProviderCredentialStatus
// so the resolution logic matches the runtime; the secret never crosses
// this boundary.
func buildProviderItems(cfg *config.Config) []providerpicker.Item {
	known := config.KnownProviders()
	items := make([]providerpicker.Item, 0, len(known))
	for _, kp := range known {
		st, ok := config.ProviderCredentialStatus(cfg, kp.Name)
		if !ok {
			continue
		}
		items = append(items, providerpicker.Item{
			Provider:      st.Provider,
			Kind:          st.Kind.String(),
			EnvVar:        st.APIKeyEnv,
			BaseURL:       st.BaseURL,
			Configured:    st.Configured,
			Source:        st.Source,
			NeedsKey:      st.NeedsKey,
			AllowsBaseURL: providerKindAllowsBaseURL(st.Kind),
		})
	}
	return items
}

// applyProviderCommand executes a picker Command: write/update the
// credential ref (+ keyring secret) or unset it. It appends a REDACTED
// system block confirming the result and reloads the picker so the new
// status is visible. The secret is zeroed after use.
func (m *Model) applyProviderCommand(cmd providerpicker.Command) error {
	if cmd.Type == providerpicker.CommandNone {
		return nil
	}
	cfg, err := m.sessionActionConfig()
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.ConfigPath) == "" {
		return fmt.Errorf("no config path — run inside a repo or set a config location")
	}
	switch cmd.Type {
	case providerpicker.CommandSave:
		return m.applyProviderSave(cfg, cmd)
	case providerpicker.CommandRemove:
		return m.applyProviderRemove(cfg, cmd)
	default:
		return nil
	}
}

func (m *Model) applyProviderSave(cfg *config.Config, cmd providerpicker.Command) error {
	// Zero the secret as soon as we're done with it — it must not linger
	// on the Command struct after the keyring write.
	secret := cmd.Secret
	defer func() { secret = "" }()

	envVar := strings.TrimSpace(cmd.EnvVar)
	if err := config.WriteProviderCredential(cfg.ConfigPath, cmd.Provider, envVar, "", cmd.BaseURL); err != nil {
		return fmt.Errorf("write credential: %w", err)
	}

	// Recompute the env-var NAME the way the storage layer did (empty
	// falls back to the provider's conventional name) so the confirmation
	// and the keyring write target the same slot.
	if envVar == "" {
		if kp, ok := config.LookupKnownProvider(cmd.Provider); ok {
			envVar = kp.APIKeyEnv
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "provider %s: recorded credential ref (api_key_env=%s)",
		cmd.Provider, redactEnvNameTUI(envVar))
	if strings.TrimSpace(cmd.BaseURL) != "" {
		fmt.Fprintf(&sb, ", base_url=%s", cmd.BaseURL)
	}

	// Store the entered secret in the OS keyring when one is reachable. An
	// empty secret means "keep the current credential, just update the ref"
	// — no keyring write. The secret value is NEVER put in the block.
	if strings.TrimSpace(secret) != "" && envVar != "" {
		backend, persisted, kerr := config.StoreProviderSecret(envVar, secret)
		switch {
		case kerr != nil:
			fmt.Fprintf(&sb, "\n! keyring: could not persist secret (%v)", kerr)
			fmt.Fprintf(&sb, "\n  export %s in your shell instead (secrets are never written to config)", envVar)
		case persisted:
			fmt.Fprintf(&sb, "\n✓ stored secret in the OS keyring (%s)", backend)
		default:
			fmt.Fprintf(&sb, "\n  no OS keyring available — export %s in your shell (the secret was not stored)", envVar)
		}
	}

	m.appendBlock(block{kind: "system", body: sb.String()})
	m.renderBlocks()
	return m.reloadProviderPicker(cmd.Provider)
}

func (m *Model) applyProviderRemove(cfg *config.Config, cmd providerpicker.Command) error {
	// Resolve the env-var NAME (preset override wins) BEFORE removing the
	// preset block, so we clear the matching keyring slot.
	keyEnv := ""
	if kp, ok := config.LookupKnownProvider(cmd.Provider); ok {
		keyEnv = kp.APIKeyEnv
	}
	if cfg.Inference.Presets != nil {
		if pre, ok := cfg.Inference.Presets[cmd.Provider]; ok && strings.TrimSpace(pre.APIKeyEnv) != "" {
			keyEnv = strings.TrimSpace(pre.APIKeyEnv)
		}
	}

	if err := config.RemoveProviderCredential(cfg.ConfigPath, cmd.Provider); err != nil {
		return fmt.Errorf("remove credential: %w", err)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "provider %s: removed credential ref", cmd.Provider)
	if keyEnv != "" {
		if _, removed, derr := config.DeleteProviderSecret(keyEnv); derr != nil {
			fmt.Fprintf(&sb, "\n  ! keyring: could not remove stored secret (%v)", derr)
		} else if removed {
			fmt.Fprintf(&sb, "\n  removed the stored secret from the OS keyring")
		}
		fmt.Fprintf(&sb, "\n  (the %s environment variable, if set, is untouched)", redactEnvNameTUI(keyEnv))
	}
	m.appendBlock(block{kind: "system", body: sb.String()})
	m.renderBlocks()
	return m.reloadProviderPicker(cmd.Provider)
}

func (m *Model) reloadProviderPicker(selected string) error {
	cfg, err := m.sessionActionConfig()
	if err != nil {
		return err
	}
	if m.providerPick == nil {
		m.providerPick = providerpicker.New()
	}
	m.providerPick.Open(buildProviderItems(cfg), config.SecretBackendAvailable(), selected)
	return nil
}

// providerKindAllowsBaseURL mirrors the config-side invariant (only the
// anthropic-compat cloud + OAI-compat kinds accept a base-URL override)
// so the picker enables the base_url field for the right rows. Kept in
// sync with config.providerKindAllowsBaseURL / the CLI's local copy.
func providerKindAllowsBaseURL(k config.ProviderKind) bool {
	switch k {
	case config.ProviderKindAnthropicCompatCloud, config.ProviderKindOAICompatCloud, config.ProviderKindOAICompatLocal:
		return true
	default:
		return false
	}
}

// redactEnvNameTUI is defensive: env-var NAMES are not secret, but if a
// secret is ever fat-fingered into the env slot we don't echo it. Names
// are identifier-like; anything else renders as a placeholder. Mirrors the
// CLI's redactEnvName.
func redactEnvNameTUI(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "(none)"
	}
	for _, r := range name {
		if !(r == '_' || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z')) {
			return "<redacted>"
		}
	}
	return name
}
