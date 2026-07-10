package main

// plugin_update_verify.go — plugin update / verify / untrust subcommands.
// EP-0039 §G, §K.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
)

// ── plugin update ────────────────────────────────────────────────────────

var pluginUpdateCheck bool

var pluginUpdateCmd = &cobra.Command{
	Use:   "update [<name>|all]",
	Short: "Update an installed plugin to its latest tagged version (EP-0039)",
	Long: `update fetches the latest semver tag for a plugin and installs it
side-by-side with the existing version. Use --check to see available
updates without installing.

Without arguments lists currently-installed plugins eligible for update.
With "all", attempts to update every installed plugin tracked by lock file.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		// Look for project lock file.
		lockPath := pluginLockPath(cfg)
		lock, err := plugins.ReadLock(lockPath)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintln(cmd.OutOrStdout(), "no plugin-lock.toml — install plugins via 'stado plugin install <identity>' to enable updates")
				return nil
			}
			return err
		}

		target := ""
		if len(args) > 0 {
			target = args[0]
		}

		anyUpdates := false
		for _, entry := range lock.Entries {
			if target != "" && target != "all" && !strings.Contains(entry.Identity, target) {
				continue
			}
			id, parseErr := plugins.ParseIdentity(entry.Identity)
			if parseErr != nil {
				continue
			}
			latest, err := fetchLatestTag(id)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "  %s: latest-tag lookup failed: %v\n", entry.Identity, err)
				continue
			}
			if latest == id.Version {
				fmt.Fprintf(cmd.OutOrStdout(), "  %s: up to date (%s)\n", entry.Identity, id.Version)
				continue
			}
			anyUpdates = true
			fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s → %s\n", entry.Identity, id.Version, latest)
			if pluginUpdateCheck {
				continue
			}
			// Run install with the new version. Invoke the install RunE
			// directly rather than pluginInstallCmd.Execute() — Execute() on a
			// child walks up and runs the ROOT command (which launches the TUI),
			// so the latter would never actually install. (Latent until now: the
			// old fetchLatestTag stub returned the current version, so this
			// branch was unreachable.)
			newID := strings.Replace(entry.Identity, "@"+id.Version, "@"+latest, 1)
			fmt.Fprintf(cmd.ErrOrStderr(), "    installing %s...\n", newID)
			if err := pluginInstallCmd.RunE(pluginInstallCmd, []string{newID}); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "    install failed: %v\n", err)
			}
		}
		if !anyUpdates && !pluginUpdateCheck {
			fmt.Fprintln(cmd.OutOrStdout(), "all plugins up to date")
		}
		return nil
	},
}

// latestTagHTTPTimeout bounds each release-API lookup.
const latestTagHTTPTimeout = 15 * time.Second

// fetchLatestTag resolves the latest release tag for an installed plugin's
// owner/repo via the host's release API. github.com and gitlab.com are
// supported; other hosts return an error so `plugin update` reports a clear
// "lookup failed" rather than silently no-op'ing (the prior stub returned the
// current version, so update never updated anything — EP-0039 §G).
func fetchLatestTag(id plugins.Identity) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), latestTagHTTPTimeout)
	defer cancel()
	switch id.Host {
	case "github.com":
		body, err := httpGetReleaseJSON(ctx, fmt.Sprintf(
			"https://api.github.com/repos/%s/%s/releases/latest", id.Owner, id.Repo))
		if err != nil {
			return "", err
		}
		return parseReleaseTagName(body, "github")
	case "gitlab.com":
		proj := url.PathEscape(id.Owner + "/" + id.Repo)
		body, err := httpGetReleaseJSON(ctx, fmt.Sprintf(
			"https://gitlab.com/api/v4/projects/%s/releases/permalink/latest", proj))
		if err != nil {
			return "", err
		}
		return parseReleaseTagName(body, "gitlab")
	default:
		return "", fmt.Errorf("latest-tag lookup unsupported for host %q (github.com / gitlab.com only)", id.Host)
	}
}

// parseReleaseTagName extracts tag_name from a GitHub/GitLab release JSON
// object. Both APIs expose the field under the same key. Pure (testable).
func parseReleaseTagName(body []byte, provider string) (string, error) {
	var r struct {
		TagName string `json:"tag_name"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("parse %s release: %w", provider, err)
	}
	if r.TagName == "" {
		return "", fmt.Errorf("%s release has no tag_name", provider)
	}
	return r.TagName, nil
}

// httpGetReleaseJSON GETs a release-API URL and returns the (size-capped) body.
func httpGetReleaseJSON(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: latestTagHTTPTimeout}).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

func pluginLockPath(cfg *config.Config) string {
	// Config discovery is the source of truth for project scope. Return this
	// path before the lock exists so a first remote install in a project does
	// not silently fall back to user-global state.
	if projectDir := cfg.ProjectStadoDir(); projectDir != "" {
		return filepath.Join(projectDir, "plugin-lock.toml")
	}
	return filepath.Join(cfg.StateDir(), "plugin-lock.toml")
}

// ── plugin verify ────────────────────────────────────────────────────────

var pluginVerifyInstalledCmd = &cobra.Command{
	Use:   "verify-installed <plugin-id>",
	Short: "Re-verify the signature of an installed plugin against the trust store",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		pluginsDir := filepath.Join(cfg.StateDir(), "plugins")
		dir, err := plugins.InstalledDir(pluginsDir, args[0])
		if err != nil {
			return err
		}
		mf, sig, err := plugins.LoadFromDir(dir)
		if err != nil {
			return fmt.Errorf("read manifest: %w", err)
		}
		ts := plugins.NewTrustStore(cfg.StateDir())
		if err := ts.VerifyManifest(mf, sig); err != nil {
			return fmt.Errorf("verify failed: %w", err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "✓ %s v%s — signature verified (fingerprint %s)\n",
			mf.Name, mf.Version, mf.AuthorPubkeyFpr)
		return nil
	},
}

func init() {
	pluginUpdateCmd.Flags().BoolVar(&pluginUpdateCheck, "check", false, "Show available updates without installing")
	pluginCmd.AddCommand(pluginUpdateCmd, pluginVerifyInstalledCmd, pluginRemoveCmd)
}
