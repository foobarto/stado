package main

// plugin_remote.go — fetch a plugin from a remote source URL into a local
// temp dir, ready for the existing install pipeline. EP-0039 §A/§E.
//
// Identity format: <host>/<owner>/<repo>[/<plugin-subdir>]@<version>
// Resolution order:
//   1. GitHub Release attached to tag — download wasm + manifest + sig as
//      release assets (preferred — no source build, no toolchain needed).
//   2. Files at <plugin-subdir>/dist/ in the tagged tree.
//   3. Source build (--build flag, opt-in).

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/plugins"
)

// looksLikeRemoteIdentity returns true when src is a remote plugin identity
// (host/owner/repo@version) rather than a local directory.
func looksLikeRemoteIdentity(src string) bool {
	if _, err := os.Stat(src); err == nil {
		return false // it's a real local path
	}
	// Identity must contain @ and at least one /
	return strings.Contains(src, "@") && strings.Count(src, "/") >= 2
}

// fetchRemotePlugin resolves the identity, downloads artefacts to a local
// temp dir, and returns the directory path. Caller is responsible for
// cleanup (defer os.RemoveAll on the returned path).
func fetchRemotePlugin(rawIdentity string) (localDir string, err error) {
	id, err := plugins.ParseIdentity(rawIdentity)
	if err != nil {
		return "", fmt.Errorf("parse identity: %w", err)
	}

	cacheDir, err := pluginTarballCacheDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", err
	}
	stagingDir := filepath.Join(cacheDir, id.Key())
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return "", err
	}

	// Try Tier 1: GitHub Release tagged with the version.
	if strings.HasPrefix(id.Host, "github.com") {
		if err := tryGitHubRelease(id, stagingDir); err == nil {
			return stagingDir, nil
		}
	}

	// Tier 2: raw files at <plugin-subdir>/dist/<file>
	if err := tryRawTreeFetch(id, stagingDir); err != nil {
		return "", fmt.Errorf("remote install: no release or dist/ tree found at %s: %w",
			id.Canonical(), err)
	}
	return stagingDir, nil
}

// pluginTarballCacheDir returns ~/.cache/stado/plugin-tarballs/.
func pluginTarballCacheDir() (string, error) {
	xdg := os.Getenv("XDG_CACHE_HOME")
	if xdg == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		xdg = filepath.Join(home, ".cache")
	}
	return filepath.Join(xdg, "stado", "plugin-tarballs"), nil
}

// tryGitHubRelease tries to download the three artefacts as release assets.
// URL format: https://github.com/<owner>/<repo>/releases/download/<version>/<filename>
func tryGitHubRelease(id plugins.Identity, dst string) error {
	prefix := fmt.Sprintf("https://%s/%s/%s/releases/download/%s",
		id.Host, id.Owner, id.Repo, id.Version)
	for _, file := range []string{"plugin.wasm", "plugin.manifest.json", "plugin.manifest.sig"} {
		// Honor subdir if specified (e.g. monorepo with multiple plugins).
		filename := file
		if id.Subdir != "" {
			// Convention: assets named <subdir>-<file> for monorepo cases.
			filename = strings.ReplaceAll(id.Subdir, "/", "-") + "-" + file
		}
		url := prefix + "/" + filename
		if err := downloadFile(url, filepath.Join(dst, file)); err != nil {
			// Fall back to non-prefixed name for monorepo flat releases.
			if id.Subdir != "" {
				url = prefix + "/" + file
				if err2 := downloadFile(url, filepath.Join(dst, file)); err2 != nil {
					return fmt.Errorf("github release: %s: %w", file, err)
				}
			} else {
				return fmt.Errorf("github release: %s: %w", file, err)
			}
		}
	}
	// Optionally fetch author.pubkey from the well-known anchor.
	_ = downloadFile(id.AnchorURL(), filepath.Join(dst, "author.pubkey"))
	return nil
}

// tryRawTreeFetch downloads from the raw tree at <plugin-subdir>/dist/.
func tryRawTreeFetch(id plugins.Identity, dst string) error {
	// GitHub raw URL: https://raw.githubusercontent.com/<owner>/<repo>/<version>/<subdir>/dist/<file>
	var prefix string
	switch id.Host {
	case "github.com":
		prefix = fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s",
			id.Owner, id.Repo, id.Version)
	case "gitlab.com":
		prefix = fmt.Sprintf("https://gitlab.com/%s/%s/-/raw/%s",
			id.Owner, id.Repo, id.Version)
	default:
		// Generic: try gitiles-style raw
		prefix = fmt.Sprintf("https://%s/%s/%s/raw/%s",
			id.Host, id.Owner, id.Repo, id.Version)
	}
	subdir := ""
	if id.Subdir != "" {
		subdir = "/" + id.Subdir
	}
	for _, file := range []string{"plugin.wasm", "plugin.manifest.json", "plugin.manifest.sig"} {
		url := prefix + subdir + "/dist/" + file
		if err := downloadFile(url, filepath.Join(dst, file)); err != nil {
			return fmt.Errorf("raw tree: %s: %w", file, err)
		}
	}
	_ = downloadFile(id.AnchorURL(), filepath.Join(dst, "author.pubkey"))
	return nil
}

func downloadFile(url, dst string) error {
	cl := &http.Client{Timeout: 60 * time.Second}
	resp, err := cl.Get(url) //nolint:noctx
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d for %s", resp.StatusCode, url)
	}
	f, err := os.Create(dst) //nolint:gosec
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	const maxArtefactBytes = 64 << 20 // 64 MiB
	_, err = io.Copy(f, io.LimitReader(resp.Body, maxArtefactBytes))
	return err
}
