package main

// plugin_update_verify.go — plugin update / verify / untrust subcommands.
// EP-0039 §G, §K.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/config"
	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/runtime"
	"golang.org/x/mod/semver"
)

// ── plugin update ────────────────────────────────────────────────────────

var pluginUpdateCheck bool

var pluginUpdateCmd = &cobra.Command{
	Use:   "update [[project:|global:]<canonical-source|store-key>|all]",
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
		target := ""
		if len(args) > 0 {
			target = args[0]
		}

		anyLock := false
		anyUpdates := false
		var updateFailures []string
		processedNamespaces := make(map[string]struct{})
		targetNamespace := ""
		targetPluginsRoot := ""
		if target != "" && target != "all" {
			pkg, root, resolveErr := resolveManagedInstalledPackage(cfg, target)
			if resolveErr != nil {
				return fmt.Errorf("plugin update: resolve %q: %w", target, resolveErr)
			}
			targetNamespace = pkg.Identity.Namespace
			targetPluginsRoot = filepath.Clean(root.Dir)
		}
		for _, lockTarget := range pluginLockTargets(cfg) {
			pluginsRoot := filepath.Join(filepath.Dir(lockTarget.Path), "plugins")
			if targetPluginsRoot != "" && filepath.Clean(pluginsRoot) != targetPluginsRoot {
				continue
			}
			installed, listErr := plugins.ListInstalledPackages(pluginsRoot)
			if listErr != nil {
				return fmt.Errorf("plugin update: enumerate source-keyed installs for %s: %w", lockTarget.Path, listErr)
			}
			groups := make(map[string][]plugins.InstalledPackage)
			for _, pkg := range installed {
				if pkg.Record.Kind == plugins.InstallRemote {
					groups[pkg.Identity.Namespace] = append(groups[pkg.Identity.Namespace], pkg)
				}
			}
			activeStoreKeys := make(map[string]string)
			for namespace, candidates := range groups {
				selected, ok, selectErr := plugins.PickActivePackage(pluginsRoot, namespace, candidates)
				if selectErr != nil {
					return fmt.Errorf("plugin update: select active %s: %w", namespace, selectErr)
				}
				if ok {
					activeStoreKeys[namespace] = selected.Record.StoreKey
				}
			}
			lock, lockErr := plugins.ReadLock(lockTarget.Path)
			if lockErr != nil {
				if os.IsNotExist(lockErr) {
					continue
				}
				return lockErr
			}
			anyLock = true
			for _, entry := range lock.Entries {
				id, parseErr := plugins.ParseIdentity(entry.Identity)
				if parseErr != nil {
					continue
				}
				if targetNamespace != "" && id.Namespace() != targetNamespace {
					continue
				}
				if activeStoreKeys[id.Namespace()] != entry.StoreKey {
					continue
				}
				processKey := lockTarget.Path + "\x00" + id.Namespace()
				if _, done := processedNamespaces[processKey]; done {
					continue
				}
				processedNamespaces[processKey] = struct{}{}
				if !allowsImplicitPluginUpdate(id) {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s: pinned to commit; install an explicit new identity to move\n", entry.Identity)
					continue
				}
				latest, err := fetchLatestTag(id)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "  %s: latest-tag lookup failed: %v\n", entry.Identity, err)
					continue
				}
				alreadyCurrent := latest == id.Version
				if !alreadyCurrent && !id.IsCommit() {
					latestIsOlder, compareErr := plugins.VersionLess(latest, id.Version)
					if compareErr != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "  %s: package-version comparison failed: %v\n", entry.Identity, compareErr)
						continue
					}
					alreadyCurrent = latestIsOlder
				}
				if alreadyCurrent {
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
				installErr := withPluginInstallScope(lockTarget.Local, func() error {
					return pluginInstallCmd.RunE(pluginInstallCmd, []string{newID})
				})
				if installErr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "    install failed: %v\n", installErr)
					updateFailures = append(updateFailures, entry.Identity+": "+installErr.Error())
					continue
				}
			}
		}
		if !anyLock {
			fmt.Fprintln(cmd.OutOrStdout(), "no plugin-lock.toml — install plugins via 'stado plugin install <identity>' to enable updates")
			return nil
		}
		if !anyUpdates && !pluginUpdateCheck {
			fmt.Fprintln(cmd.OutOrStdout(), "all plugins up to date")
		}
		if len(updateFailures) > 0 {
			return fmt.Errorf("%d plugin update(s) failed", len(updateFailures))
		}
		return nil
	},
}

func allowsImplicitPluginUpdate(id plugins.Identity) bool { return !id.IsCommit() }

func withPluginInstallScope(local bool, fn func() error) error {
	oldLocal := pluginInstallLocal
	pluginInstallLocal = local
	defer func() { pluginInstallLocal = oldLocal }()
	return fn()
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
			"https://api.github.com/repos/%s/%s/releases?per_page=100", id.Owner, id.Repo))
		if err != nil {
			return "", err
		}
		var releases []packageRelease
		if err := json.Unmarshal(body, &releases); err != nil {
			return "", fmt.Errorf("parse github releases: %w", err)
		}
		return selectLatestPackageRelease(id, releases)
	case "gitlab.com":
		if id.Subdir != "" {
			return "", fmt.Errorf("package-aware latest lookup is not available for gitlab monorepos; install an exact pinned identity instead")
		}
		proj := url.PathEscape(id.Owner + "/" + id.Repo)
		body, err := httpGetReleaseJSON(ctx, fmt.Sprintf(
			"https://gitlab.com/api/v4/projects/%s/releases/permalink/latest", proj))
		if err != nil {
			return "", err
		}
		tag, err := parseReleaseTagName(body, "gitlab")
		if err != nil {
			return "", err
		}
		if !semver.IsValid(tag) {
			return "", fmt.Errorf("gitlab latest release tag %q is not canonical semver", tag)
		}
		return tag, nil
	default:
		return "", fmt.Errorf("latest-tag lookup unsupported for host %q (github.com / gitlab.com only)", id.Host)
	}
}

type packageRelease struct {
	TagName    string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
	} `json:"assets"`
}

// selectLatestPackageRelease returns a logical vX.Y.Z for exactly id's
// package. A monorepo release qualifies either by the EP-39 package tag
// `<subdir>/vX.Y.Z`, or by a repository-wide vX.Y.Z release carrying all three
// package-prefixed assets. Sibling releases and ambiguous flat assets cannot
// advance this package.
func selectLatestPackageRelease(id plugins.Identity, releases []packageRelease) (string, error) {
	best := ""
	packagePrefix := id.Subdir + "/"
	assetPrefix := strings.ReplaceAll(id.Subdir, "/", "-") + "-"
	for _, release := range releases {
		// Match GitHub's former /releases/latest semantics: unapproved drafts
		// and prereleases never become an implicit stable update. Operators can
		// still install either by exact pinned identity.
		if release.Draft || release.Prerelease {
			continue
		}
		candidate := release.TagName
		if id.Subdir != "" {
			switch {
			case strings.HasPrefix(candidate, packagePrefix):
				candidate = strings.TrimPrefix(candidate, packagePrefix)
			case semver.IsValid(candidate) && releaseHasPackageAssets(release, assetPrefix):
				// Repository-wide release with an exact package asset set.
			default:
				continue
			}
		}
		if !semver.IsValid(candidate) {
			continue
		}
		if best == "" || semver.Compare(candidate, best) > 0 {
			best = candidate
		}
	}
	if best == "" {
		return "", fmt.Errorf("no package-specific semver release found for %s", id.Namespace())
	}
	return best, nil
}

func releaseHasPackageAssets(release packageRelease, prefix string) bool {
	want := map[string]bool{
		prefix + "plugin.wasm":          false,
		prefix + "plugin.manifest.json": false,
		prefix + "plugin.manifest.sig":  false,
	}
	for _, asset := range release.Assets {
		if _, ok := want[asset.Name]; ok {
			want[asset.Name] = true
		}
	}
	for _, found := range want {
		if !found {
			return false
		}
	}
	return true
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
	return readBoundedRemoteBody(resp.Body, 1<<20, "plugin release metadata")
}

// ── plugin verify ────────────────────────────────────────────────────────

var pluginVerifyInstalledCmd = &cobra.Command{
	Use:   "verify-installed [project:|global:]<canonical-source|store-key>",
	Short: "Re-verify the signature of an installed plugin against the trust store",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		pkg, _, err := resolveManagedInstalledPackage(cfg, args[0])
		if err != nil {
			if !os.IsNotExist(err) {
				return err
			}
			return fmt.Errorf("plugin %q is not installed", args[0])
		}
		dir, mf, sig := pkg.Dir, &pkg.Manifest, pkg.Signature
		if err := runtime.VerifyInstalledPlugin(cmd.Context(), cfg, dir, mf, sig); err != nil {
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
