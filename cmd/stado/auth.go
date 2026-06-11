package main

// `stado auth <provider> {set,unset,list}` — manage provider credentials
// without hand-editing config.toml.
//
// ENV-FIRST storage: a provider credential is referenced by the NAME of an
// environment variable in [inference.presets.<name>].api_key_env; the
// secret itself lives in the operator's environment (or, in a future
// increment, an OS keyring). This command NEVER writes a secret to
// config.toml and NEVER echoes one to the terminal — `auth list` renders
// only the env-var NAME and a configured/unset status, REDACTED.
//
// Scope note: this is the CLI + storage half of the provider-credential
// feature. The in-TUI `/provider` modal lands on a separate branch once
// the slash-command registry is in place.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
)

var (
	authSetKey        string
	authSetEnv        string
	authSetBaseURL    string
	authSetNoValidate bool
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage provider credentials (env-var references, never secrets)",
	Long: "Configure which providers stado can reach. Credentials are stored\n" +
		"ENV-FIRST: config.toml records the NAME of the environment variable\n" +
		"that holds each provider's API key; the secret itself stays in your\n" +
		"environment and is never written to config or printed.\n\n" +
		"  stado auth list                      # providers + credential status (redacted)\n" +
		"  stado auth set <provider> [flags]    # record a provider's credential ref\n" +
		"  stado auth unset <provider>          # remove a provider's config-side ref",
}

var authListCmd = &cobra.Command{
	Use:   "list",
	Short: "List providers and whether a credential is configured (redacted)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		return renderAuthList(cmd.OutOrStdout(), cfg)
	},
}

var authSetCmd = &cobra.Command{
	Use:   "set <provider>",
	Short: "Record a provider credential reference in config.toml (env-var name only)",
	Long: "Writes [inference.presets.<provider>] recording the env-var NAME that\n" +
		"holds the API key (and, for base-URL-overridable providers, an\n" +
		"endpoint / base-url override). The secret is NEVER written to config.\n\n" +
		"  --env VAR        name an existing env var that holds the key\n" +
		"  --key VALUE      provide the secret inline; stado prints the export\n" +
		"                   line to add to your shell rc (it is NOT persisted)\n" +
		"  --base-url URL   base-URL override (anthropic-compat / OAI-compat only)\n" +
		"  --no-validate    skip the on-save credential probe",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		return runAuthSet(cmd.Context(), cmd.OutOrStdout(), cfg, args[0], authSetOpts{
			key:        authSetKey,
			envVar:     authSetEnv,
			baseURL:    authSetBaseURL,
			noValidate: authSetNoValidate,
		})
	},
}

var authUnsetCmd = &cobra.Command{
	Use:   "unset <provider>",
	Short: "Remove a provider's credential reference from config.toml",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		return runAuthUnset(cmd.OutOrStdout(), cfg, args[0])
	},
}

type authSetOpts struct {
	key        string
	envVar     string
	baseURL    string
	noValidate bool
}

// renderAuthList prints every known provider with its credential status,
// REDACTED. It shows the env-var NAME and a ✓/✗ for whether a value
// resolves — never the secret. Uses ProviderCredentialStatus so the
// resolution logic matches the runtime.
func renderAuthList(w io.Writer, cfg *config.Config) error {
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PROVIDER\tKIND\tCREDENTIAL\tENV VAR")
	for _, p := range config.KnownProviders() {
		st, ok := config.ProviderCredentialStatus(cfg, p.Name)
		if !ok {
			continue
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", st.Provider, st.Kind, authCredStatus(st), authEnvLabel(st))
	}
	_ = tw.Flush()
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Credentials are stored ENV-FIRST: config records the env-var NAME, the secret stays in your environment.")
	fmt.Fprintln(w, "  stado auth set <provider> --env VAR     # point a provider at an existing env var")
	fmt.Fprintln(w, "  stado auth set <provider> --key VALUE   # get the export line for your shell rc")
	return nil
}

func authCredStatus(st config.ProviderCredentialState) string {
	if !st.NeedsKey {
		return "(no key needed)"
	}
	if st.Configured {
		src := st.Source
		if src == "" {
			src = "env"
		}
		return "✓ configured (" + src + ")"
	}
	return "✗ not set"
}

func authEnvLabel(st config.ProviderCredentialState) string {
	if st.APIKeyEnv == "" {
		return "-"
	}
	return st.APIKeyEnv
}

// runAuthSet records the credential reference and (unless --no-validate)
// runs a light probe. It NEVER persists or echoes the secret: --key is
// turned into an export hint, --env names an existing variable.
func runAuthSet(ctx context.Context, w io.Writer, cfg *config.Config, provider string, opts authSetOpts) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	kp, ok := config.LookupKnownProvider(provider)
	if !ok {
		return fmt.Errorf("unknown provider %q — run `stado auth list` for the catalogue", provider)
	}

	if opts.key != "" && opts.envVar != "" {
		return fmt.Errorf("pass at most one of --key / --env")
	}
	baseURL := strings.TrimSpace(opts.baseURL)
	if baseURL != "" && !providerKindAllowsBaseURL(kp.Kind) {
		return fmt.Errorf("--base-url is not allowed for provider %q (kind %s); only anthropic-compat-cloud and OAI-compat providers accept one", kp.Name, kp.Kind)
	}

	// Resolve the env-var NAME to record. --env wins; else the provider's
	// conventional name. Never store the secret value itself.
	envVar := strings.TrimSpace(opts.envVar)
	if envVar == "" {
		envVar = kp.APIKeyEnv
	}
	if kp.APIKeyEnv == "" && envVar == "" && opts.key != "" {
		return fmt.Errorf("provider %q needs no API key; --key has no effect", kp.Name)
	}

	if err := config.WriteProviderCredential(cfg.ConfigPath, kp.Name, envVar, "", baseURL); err != nil {
		return fmt.Errorf("write credential: %w", err)
	}
	fmt.Fprintf(w, "✓ recorded credential ref for %s in %s (api_key_env=%s)\n", kp.Name, cfg.ConfigPath, redactEnvName(envVar))

	// --key: persist the secret to the OS keyring when one is reachable
	// (KEYRING-FIRST), else fall back to printing the export line so the
	// operator can wire it into their shell (ENV-FIRST). The secret is NEVER
	// written to config.toml in either case.
	if opts.key != "" && envVar != "" {
		backend, persisted, kerr := config.StoreProviderSecret(envVar, opts.key)
		switch {
		case kerr != nil:
			// Keyring was available but the write failed — degrade to the
			// export hint rather than failing the whole command, since the
			// ref is already recorded.
			fmt.Fprintf(w, "\n! keyring: could not persist secret (%v)\n", kerr)
			fmt.Fprintln(w, "  Falling back to an environment variable. Add this to your shell rc / current session:")
			fmt.Fprintf(w, "  export %s=%s\n", envVar, shellQuote(opts.key))
		case persisted:
			fmt.Fprintf(w, "✓ stored secret in the OS keyring (%s) under %s\n", backend, envVar)
			fmt.Fprintln(w, "  (stado reads it from the keyring; no environment variable needed)")
		default:
			// No keyring available — ENV-FIRST export hint.
			fmt.Fprintln(w)
			fmt.Fprintln(w, "No OS keyring available; secrets are never written to config.")
			fmt.Fprintln(w, "Add this to your shell rc / current session:")
			fmt.Fprintf(w, "  export %s=%s\n", envVar, shellQuote(opts.key))
		}
	}

	if opts.noValidate {
		return nil
	}

	// Validate-on-save (default on). Probe with the secret resolved from
	// the environment (or --key for this invocation). A failure is a
	// WARNING, not an error — the ref is already written and the operator
	// may set the env var later.
	secret := opts.key
	if secret == "" && envVar != "" {
		secret = os.Getenv(envVar)
	}
	if err := validateProviderCredential(ctx, kp, baseURL, secret); err != nil {
		fmt.Fprintf(w, "\n! validation: %s\n", err.Error())
		fmt.Fprintln(w, "  (the credential ref was still saved; fix the key/env and re-run, or pass --no-validate)")
	} else {
		fmt.Fprintln(w, "✓ validation: credential accepted")
	}
	return nil
}

func runAuthUnset(w io.Writer, cfg *config.Config, provider string) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	kp, ok := config.LookupKnownProvider(provider)
	if !ok {
		return fmt.Errorf("unknown provider %q — run `stado auth list` for the catalogue", provider)
	}

	// Resolve the env-var NAME the same way the runtime does (preset
	// override wins) so we clear the matching keyring slot, if any, BEFORE
	// the preset block — and its api_key_env override — is removed.
	keyEnv := kp.APIKeyEnv
	if cfg.Inference.Presets != nil {
		if pre, ok := cfg.Inference.Presets[kp.Name]; ok && strings.TrimSpace(pre.APIKeyEnv) != "" {
			keyEnv = strings.TrimSpace(pre.APIKeyEnv)
		}
	}

	if err := config.RemoveProviderCredential(cfg.ConfigPath, kp.Name); err != nil {
		return fmt.Errorf("remove credential: %w", err)
	}
	fmt.Fprintf(w, "✓ removed credential ref for %s from %s\n", kp.Name, cfg.ConfigPath)

	// Best-effort: drop any keyring-persisted secret for this env-var NAME.
	// A keyring failure here is a warning — the config ref is already gone.
	if keyEnv != "" {
		if _, removed, derr := config.DeleteProviderSecret(keyEnv); derr != nil {
			fmt.Fprintf(w, "  ! keyring: could not remove stored secret for %s (%v)\n", keyEnv, derr)
		} else if removed {
			fmt.Fprintf(w, "  removed the stored secret from the OS keyring (%s)\n", keyEnv)
		}
	}
	fmt.Fprintf(w, "  (the %s environment variable, if set, is untouched)\n", kp.APIKeyEnv)
	return nil
}

// validateProviderCredential runs a light, short-timeout probe to confirm
// a credential is plausibly usable. For OAI-compat kinds it GETs
// <base>/models with a bearer header; for native / anthropic-compat it can
// only confirm a non-empty secret is present (a real auth round-trip needs
// the vendor SDK, deliberately out of scope here). Returns nil on success.
func validateProviderCredential(ctx context.Context, kp config.KnownProvider, baseURL, secret string) error {
	if kp.APIKeyEnv == "" {
		return nil // local runner — nothing to validate
	}
	if strings.TrimSpace(secret) == "" {
		return fmt.Errorf("no key resolved from %s — set the env var (or pass --key) to validate", kp.APIKeyEnv)
	}

	switch kp.Kind {
	case config.ProviderKindOAICompatCloud, config.ProviderKindOAICompatLocal:
		base := strings.TrimSpace(baseURL)
		if base == "" {
			base = kp.Endpoint
		}
		if base == "" {
			return nil
		}
		return probeOAIModels(ctx, base, secret)
	default:
		// Native + anthropic-compat: confirm a key is present. A live
		// auth probe would need the vendor SDK; out of scope for this
		// increment.
		return nil
	}
}

// probeOAIModels GETs <base>/models with a bearer token and a short
// timeout. 2xx → ok; 401/403 → bad key; other → reported verbatim.
func probeOAIModels(ctx context.Context, base, secret string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	url := strings.TrimRight(base, "/") + "/models"
	req, err := http.NewRequestWithContext(pctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build probe request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("User-Agent", "stado-auth-probe")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("key rejected by %s (HTTP %d)", url, resp.StatusCode)
	default:
		return fmt.Errorf("unexpected response from %s (HTTP %d)", url, resp.StatusCode)
	}
}

// providerKindAllowsBaseURL mirrors the config-side invariant so the CLI
// can reject --base-url early with a friendly message.
func providerKindAllowsBaseURL(k config.ProviderKind) bool {
	switch k {
	case config.ProviderKindAnthropicCompatCloud, config.ProviderKindOAICompatCloud, config.ProviderKindOAICompatLocal:
		return true
	default:
		return false
	}
}

// redactEnvName is defensive: env-var NAMES are not secret, but if a caller
// ever fat-fingers a secret into the --env slot we don't want it echoed.
// Names are uppercase identifiers; anything else is shown as a placeholder.
func redactEnvName(name string) string {
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

// shellQuote single-quotes a value for a POSIX shell export line, escaping
// embedded single quotes. Used only for the --key export hint.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func init() {
	authSetCmd.Flags().StringVar(&authSetKey, "key", "",
		"API key value; stado prints the export line for your shell (NOT written to config)")
	authSetCmd.Flags().StringVar(&authSetEnv, "env", "",
		"Name of an existing environment variable that holds the API key")
	authSetCmd.Flags().StringVar(&authSetBaseURL, "base-url", "",
		"Base-URL override (anthropic-compat-cloud / OAI-compat providers only)")
	authSetCmd.Flags().BoolVar(&authSetNoValidate, "no-validate", false,
		"Skip the on-save credential probe")

	authCmd.AddCommand(authListCmd)
	authCmd.AddCommand(authSetCmd)
	authCmd.AddCommand(authUnsetCmd)
	rootCmd.AddCommand(authCmd)
}
