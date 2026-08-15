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
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/workdirpath"
)

const maxRemotePluginArtifactBytes int64 = 64 << 20

// looksLikeRemoteIdentity returns true when src is a remote plugin identity
// (host/owner/repo@version) rather than a local directory.
func looksLikeRemoteIdentity(src string) bool {
	if _, err := os.Stat(src); err == nil {
		return false // it's a real local path
	}
	// Identity must contain @ and at least one /
	return strings.Contains(src, "@") && strings.Count(src, "/") >= 2
}

type fetchedRemotePlugin struct {
	Dir            string
	SourceRevision string
	ResolvedCommit string
}

var remoteSourceMetadataGet = httpGetReleaseJSON
var remotePluginArtifactDownload = downloadFile

// fetchRemotePlugin resolves the identity, downloads artefacts to a local
// staging dir, and returns the exact bounded source revision used. A
// monorepo package prefers its EP-39 `<subdir>/vX.Y.Z` release/tag and only
// then tries a repository-wide tag.
func fetchRemotePlugin(rawIdentity string) (fetchedRemotePlugin, error) {
	id, err := plugins.ParseIdentity(rawIdentity)
	if err != nil {
		return fetchedRemotePlugin{}, fmt.Errorf("parse identity: %w", err)
	}

	cacheDir, err := pluginTarballCacheDir()
	if err != nil {
		return fetchedRemotePlugin{}, err
	}
	cachePaths := workdirpath.NewUserConfigResolver()
	if err := cachePaths.MkdirAll(cacheDir, 0o755); err != nil {
		return fetchedRemotePlugin{}, err
	}
	stagingDir := filepath.Join(cacheDir, id.Key())
	if err := cachePaths.MkdirAll(stagingDir, 0o755); err != nil {
		return fetchedRemotePlugin{}, err
	}

	var lastErr error
	for _, sourceRevision := range id.SourceRevisions() {
		resolvedCommit, resolveErr := resolveRemoteSourceCommit(id, sourceRevision)
		if resolveErr != nil {
			lastErr = resolveErr
			continue
		}
		// Tier 1: GitHub Release attached to the exact resolved source ref.
		if id.Host == "github.com" {
			if err := tryGitHubRelease(id, sourceRevision, stagingDir); err == nil {
				confirmed, confirmErr := resolveRemoteSourceCommit(id, sourceRevision)
				if confirmErr != nil || confirmed != resolvedCommit {
					return fetchedRemotePlugin{}, fmt.Errorf("remote install: source ref %s changed while fetching", sourceRevision)
				}
				return fetchedRemotePlugin{Dir: stagingDir, SourceRevision: sourceRevision, ResolvedCommit: resolvedCommit}, nil
			}
		}

		// Tier 2: signed dist files from the already-dereferenced commit. Do
		// not ask the raw endpoint to resolve mutable tag text a second time.
		if err := tryRawTreeFetch(id, resolvedCommit, stagingDir); err == nil {
			confirmed, confirmErr := resolveRemoteSourceCommit(id, sourceRevision)
			if confirmErr != nil || confirmed != resolvedCommit {
				return fetchedRemotePlugin{}, fmt.Errorf("remote install: source ref %s changed while fetching", sourceRevision)
			}
			return fetchedRemotePlugin{Dir: stagingDir, SourceRevision: sourceRevision, ResolvedCommit: resolvedCommit}, nil
		} else {
			lastErr = err
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no source revisions available")
	}
	return fetchedRemotePlugin{}, fmt.Errorf("remote install: no release or dist/ tree found at %s: %w", id.Canonical(), lastErr)
}

// resolveRemoteSourceCommit dereferences one already-bounded source selector
// to the commit that supplied it. Full-commit identities are self-authenticating
// selectors; semver refs require a host API that returns the underlying commit.
// Unsupported hosts fail closed rather than recording only mutable tag text.
func resolveRemoteSourceCommit(id plugins.Identity, sourceRevision string) (string, error) {
	if id.IsCommit() {
		if sourceRevision != id.Version || !validFullGitCommit(sourceRevision) {
			return "", fmt.Errorf("commit identity source revision mismatch")
		}
		return sourceRevision, nil
	}
	if err := id.ValidateSourceRevision(sourceRevision); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), latestTagHTTPTimeout)
	defer cancel()
	switch id.Host {
	case "github.com":
		return resolveGitHubTagCommit(ctx, id, sourceRevision)
	case "gitlab.com":
		return resolveGitLabTagCommit(ctx, id, sourceRevision)
	default:
		return "", fmt.Errorf("source-commit resolution unsupported for semver refs on host %q; use a full commit identity", id.Host)
	}
}

func resolveGitHubTagCommit(ctx context.Context, id plugins.Identity, sourceRevision string) (string, error) {
	base := fmt.Sprintf("https://api.github.com/repos/%s/%s/git", id.Owner, id.Repo)
	body, err := remoteSourceMetadataGet(ctx, base+"/ref/tags/"+url.PathEscape(sourceRevision))
	if err != nil {
		return "", fmt.Errorf("resolve github tag ref %s: %w", sourceRevision, err)
	}
	var ref struct {
		Object gitHubGitObject `json:"object"`
	}
	if err := json.Unmarshal(body, &ref); err != nil {
		return "", fmt.Errorf("parse github tag ref %s: %w", sourceRevision, err)
	}
	object := ref.Object
	for depth := 0; depth < 8; depth++ {
		if !validFullGitCommit(object.SHA) {
			return "", fmt.Errorf("github tag %s returned invalid object id", sourceRevision)
		}
		switch object.Type {
		case "commit":
			return object.SHA, nil
		case "tag":
			body, err = remoteSourceMetadataGet(ctx, base+"/tags/"+object.SHA)
			if err != nil {
				return "", fmt.Errorf("dereference github tag %s: %w", sourceRevision, err)
			}
			var tag struct {
				Object gitHubGitObject `json:"object"`
			}
			if err := json.Unmarshal(body, &tag); err != nil {
				return "", fmt.Errorf("parse github tag object %s: %w", object.SHA, err)
			}
			object = tag.Object
		default:
			return "", fmt.Errorf("github tag %s resolves to unsupported object type %q", sourceRevision, object.Type)
		}
	}
	return "", fmt.Errorf("github tag %s exceeds annotation dereference limit", sourceRevision)
}

type gitHubGitObject struct {
	Type string `json:"type"`
	SHA  string `json:"sha"`
}

func resolveGitLabTagCommit(ctx context.Context, id plugins.Identity, sourceRevision string) (string, error) {
	project := url.PathEscape(id.Owner + "/" + id.Repo)
	endpoint := fmt.Sprintf("https://gitlab.com/api/v4/projects/%s/repository/tags/%s", project, url.PathEscape(sourceRevision))
	body, err := remoteSourceMetadataGet(ctx, endpoint)
	if err != nil {
		return "", fmt.Errorf("resolve gitlab tag %s: %w", sourceRevision, err)
	}
	var tag struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := json.Unmarshal(body, &tag); err != nil {
		return "", fmt.Errorf("parse gitlab tag %s: %w", sourceRevision, err)
	}
	if !validFullGitCommit(tag.Commit.ID) {
		return "", fmt.Errorf("gitlab tag %s returned invalid commit id", sourceRevision)
	}
	return tag.Commit.ID, nil
}

func validFullGitCommit(value string) bool {
	if len(value) != 40 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
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
func tryGitHubRelease(id plugins.Identity, sourceRevision, dst string) error {
	prefix := fmt.Sprintf("https://%s/%s/%s/releases/download/%s",
		id.Host, id.Owner, id.Repo, url.PathEscape(sourceRevision))
	dedicatedSubdirRelease := id.Subdir != "" && sourceRevision != id.Version
	for _, file := range []string{"plugin.wasm", "plugin.manifest.json", "plugin.manifest.sig"} {
		// A subdir-prefixed release belongs to one package and uses flat asset
		// names. Repository-wide monorepo releases must use prefixed names;
		// falling back to a flat asset could install a sibling package.
		filename := file
		if id.Subdir != "" && !dedicatedSubdirRelease {
			filename = strings.ReplaceAll(id.Subdir, "/", "-") + "-" + file
		}
		url := prefix + "/" + filename
		if err := remotePluginArtifactDownload(url, filepath.Join(dst, file)); err != nil {
			return fmt.Errorf("github release %s: %s: %w", sourceRevision, file, err)
		}
	}
	return nil
}

// tryRawTreeFetch downloads from the raw tree at <plugin-subdir>/dist/.
func tryRawTreeFetch(id plugins.Identity, resolvedCommit, dst string) error {
	// GitHub raw URL: https://raw.githubusercontent.com/<owner>/<repo>/<version>/<subdir>/dist/<file>
	var prefix string
	switch id.Host {
	case "github.com":
		prefix = fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s",
			id.Owner, id.Repo, url.PathEscape(resolvedCommit))
	case "gitlab.com":
		prefix = fmt.Sprintf("https://gitlab.com/%s/%s/-/raw/%s",
			id.Owner, id.Repo, url.PathEscape(resolvedCommit))
	default:
		// Generic: try gitiles-style raw
		prefix = fmt.Sprintf("https://%s/%s/%s/raw/%s",
			id.Host, id.Owner, id.Repo, url.PathEscape(resolvedCommit))
	}
	subdir := ""
	if id.Subdir != "" {
		subdir = "/" + id.Subdir
	}
	for _, file := range []string{"plugin.wasm", "plugin.manifest.json", "plugin.manifest.sig"} {
		url := prefix + subdir + "/dist/" + file
		if err := remotePluginArtifactDownload(url, filepath.Join(dst, file)); err != nil {
			return fmt.Errorf("raw tree: %s: %w", file, err)
		}
	}
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
	return writeDownloadedPluginArtifact(dst, resp.Body)
}

// writeDownloadedPluginArtifact bounds the response before changing install
// cache state and replaces the destination through a directory handle. The
// cache is persistent and same-UID writable, so os.Create would follow a
// pre-planted symlink and turn a plugin download into an arbitrary user-file
// overwrite. RootResolver also keeps the replacement atomic: a cancelled or
// short response cannot leave half of a signed package behind for a later
// fallback attempt.
func writeDownloadedPluginArtifact(dst string, source io.Reader) error {
	data, err := readBoundedPluginArtifact(source, maxRemotePluginArtifactBytes)
	if err != nil {
		return err
	}
	root, err := workdirpath.NewUserConfigResolver().OpenRoot(filepath.Dir(dst))
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err := workdirpath.NewRootResolver(root).WriteFileAtomic(filepath.Base(dst), data, 0o644); err != nil {
		return fmt.Errorf("write downloaded plugin artifact: %w", err)
	}
	return nil
}

func readBoundedPluginArtifact(source io.Reader, maxBytes int64) ([]byte, error) {
	return readBoundedRemoteBody(source, maxBytes, "remote plugin artifact")
}

func readBoundedRemoteBody(source io.Reader, maxBytes int64, label string) ([]byte, error) {
	if source == nil || maxBytes < 1 {
		return nil, fmt.Errorf("%s reader or bound is invalid", label)
	}
	data, err := io.ReadAll(io.LimitReader(source, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, maxBytes)
	}
	return data, nil
}
