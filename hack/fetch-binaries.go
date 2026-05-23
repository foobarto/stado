//go:build ignore

// fetch-binaries downloads ripgrep + ast-grep release assets for the
// OS/arch matrix stado supports, verifies sha256 against the release
// checksum manifests, and stages them under:
//
//	internal/rg/bundled/rg-<os>-<arch>[.exe]
//	internal/astgrep/bundled/ast-grep-<os>-<arch>[.exe]
//
// Also writes a `manifest.json` sidecar per tool with per-file sha256
// so the embed-time verification in internal/tools/binext can pin the
// digest without re-deriving it at build time.
//
// Intended to run from CI (release workflow) or locally before cutting
// a build that should ship bundled binaries. The default build without
// running this script has empty placeholder files, which the binext
// extractor treats as "not bundled" → PATH fallback.
//
// Usage:
//
//	go run hack/fetch-binaries.go            # all (os, arch) pairs
//	go run hack/fetch-binaries.go -only rg   # just ripgrep
//	go run hack/fetch-binaries.go -ripgrep-version 14.1.0
//
// Run from the repo root. Flags are documented by the command itself.
package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/foobarto/stado/internal/workdirpath"
)

const (
	defaultRipgrepVersion = "14.1.1"
	defaultAstGrepVersion = "0.38.7"

	maxFetchArchiveBytes int64 = 128 << 20
	maxFetchBinaryBytes  int64 = 256 << 20
	fetchHTTPTimeout           = 30 * time.Second
)

var fetchHTTPClient = &http.Client{Timeout: fetchHTTPTimeout}

type target struct {
	GOOS, GOARCH string
}

var matrix = []target{
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"darwin", "amd64"},
	{"darwin", "arm64"},
	{"windows", "amd64"},
}

type manifest struct {
	Version string            `json:"version"`
	SHA256  map[string]string `json:"sha256"` // filename → hex digest
}

// pinnedDigests is the shape of hack/binary-pins.json. Per-version,
// per-asset SHA256 hex digests for the upstream native binaries embedded
// into release builds. Committed to source so digests are independently
// reviewable — codex validated finding (HIGH attack-path): pre-fix the
// expected digests were fetched live from the SAME upstream release
// source as the binaries (ripgrep `.sha256` sidecars + ast-grep
// `expanded_assets` HTML), giving zero protection against upstream
// account/release compromise.
type pinnedDigests struct {
	Ripgrep map[string]map[string]string `json:"ripgrep"`
	AstGrep map[string]map[string]string `json:"ast-grep"`
}

func main() {
	pins, err := loadPinnedDigests()
	if err != nil {
		fatal("load pinned digests: %v", err)
	}

	rgVer := flag.String("ripgrep-version", defaultRipgrepVersion, "ripgrep release tag (without v)")
	sgVer := flag.String("ast-grep-version", defaultAstGrepVersion, "ast-grep release tag (without v)")
	only := flag.String("only", "", "'rg' or 'ast-grep' to limit; default fetches both")
	flag.Parse()

	if *only == "" || *only == "rg" {
		if err := fetchRipgrep(*rgVer, pins); err != nil {
			fatal("ripgrep: %v", err)
		}
	}
	if *only == "" || *only == "ast-grep" {
		if err := fetchAstGrep(*sgVer, pins); err != nil {
			fatal("ast-grep: %v", err)
		}
	}
	fmt.Println("done.")
}

// loadPinnedDigests reads hack/binary-pins.json. Allows a leading
// `_comment` JSON field (skipped by the unmarshaler since it's not a
// known field on pinnedDigests). Returns an error rather than
// defaulting to empty pins — a missing pin file should hard-fail the
// release build rather than silently downgrade to no pinning.
func loadPinnedDigests() (pinnedDigests, error) {
	b, err := os.ReadFile(filepath.Join("hack", "binary-pins.json"))
	if err != nil {
		return pinnedDigests{}, err
	}
	var pins pinnedDigests
	if err := json.Unmarshal(b, &pins); err != nil {
		return pinnedDigests{}, err
	}
	return pins, nil
}

// pinnedDigestFor returns the committed digest for an asset basename
// at a specific version. Hard-errors when the digest is missing —
// the release build refuses to proceed rather than fall back to a
// live-fetched value.
func pinnedDigestFor(d map[string]map[string]string, version, asset string) (string, error) {
	assets, ok := d[version]
	if !ok {
		return "", fmt.Errorf("no pinned digests for version %q in hack/binary-pins.json", version)
	}
	digest, ok := assets[asset]
	if !ok || digest == "" {
		return "", fmt.Errorf("no pinned digest for %s@%s in hack/binary-pins.json", asset, version)
	}
	return digest, nil
}

// --- ripgrep ---

func fetchRipgrep(version string, pins pinnedDigests) error {
	out := filepath.Join("internal", "rg", "bundled")
	m := manifest{Version: version, SHA256: map[string]string{}}

	for _, t := range matrix {
		url, archiveKind, innerPath := ripgrepAsset(version, t)
		fmt.Printf("ripgrep %s/%s: %s\n", t.GOOS, t.GOARCH, url)
		wantDigest, err := pinnedDigestFor(pins.Ripgrep, version, filepath.Base(url))
		if err != nil {
			return fmt.Errorf("%s/%s digest: %w", t.GOOS, t.GOARCH, err)
		}
		b, err := downloadArchiveFile(url, archiveKind, innerPath, wantDigest)
		if err != nil {
			return fmt.Errorf("%s/%s: %w", t.GOOS, t.GOARCH, err)
		}
		dst := filepath.Join(out, "rg-"+t.GOOS+"-"+t.GOARCH)
		if t.GOOS == "windows" {
			dst += ".exe"
		}
		if err := writeRepoFileAtomic(dst, b, 0o755); err != nil {
			return err
		}
		sha := sha256hex(b)
		m.SHA256[filepath.Base(dst)] = sha
		if err := writeEmbedFile(filepath.Join("internal", "rg"), "rg", "rg", t.GOOS, t.GOARCH, sha); err != nil {
			return err
		}
	}
	return writeManifest(filepath.Join(out, "manifest.json"), m)
}

// ripgrepAsset returns (url, archive-kind, inner-path) for one target.
func ripgrepAsset(v string, t target) (string, string, string) {
	base := "https://github.com/BurntSushi/ripgrep/releases/download/" + v
	switch {
	case t.GOOS == "linux" && t.GOARCH == "amd64":
		name := "ripgrep-" + v + "-x86_64-unknown-linux-musl"
		return base + "/" + name + ".tar.gz", "tar.gz", name + "/rg"
	case t.GOOS == "linux" && t.GOARCH == "arm64":
		name := "ripgrep-" + v + "-aarch64-unknown-linux-gnu"
		return base + "/" + name + ".tar.gz", "tar.gz", name + "/rg"
	case t.GOOS == "darwin" && t.GOARCH == "amd64":
		name := "ripgrep-" + v + "-x86_64-apple-darwin"
		return base + "/" + name + ".tar.gz", "tar.gz", name + "/rg"
	case t.GOOS == "darwin" && t.GOARCH == "arm64":
		name := "ripgrep-" + v + "-aarch64-apple-darwin"
		return base + "/" + name + ".tar.gz", "tar.gz", name + "/rg"
	case t.GOOS == "windows" && t.GOARCH == "amd64":
		name := "ripgrep-" + v + "-x86_64-pc-windows-msvc"
		return base + "/" + name + ".zip", "zip", name + "/rg.exe"
	}
	return "", "", ""
}

// --- ast-grep ---

func fetchAstGrep(version string, pins pinnedDigests) error {
	out := filepath.Join("internal", "astgrep", "bundled")
	m := manifest{Version: version, SHA256: map[string]string{}}

	for _, t := range matrix {
		url, kind := astGrepAsset(version, t)
		fmt.Printf("ast-grep %s/%s: %s\n", t.GOOS, t.GOARCH, url)
		inner := "ast-grep"
		if t.GOOS == "windows" {
			inner = "ast-grep.exe"
		}
		wantDigest, err := pinnedDigestFor(pins.AstGrep, version, filepath.Base(url))
		if err != nil {
			return fmt.Errorf("%s/%s digest: %w", t.GOOS, t.GOARCH, err)
		}
		b, err := downloadArchiveFile(url, kind, inner, wantDigest)
		if err != nil {
			return fmt.Errorf("%s/%s: %w", t.GOOS, t.GOARCH, err)
		}
		dst := filepath.Join(out, "ast-grep-"+t.GOOS+"-"+t.GOARCH)
		if t.GOOS == "windows" {
			dst += ".exe"
		}
		if err := writeRepoFileAtomic(dst, b, 0o755); err != nil {
			return err
		}
		sha := sha256hex(b)
		m.SHA256[filepath.Base(dst)] = sha
		if err := writeEmbedFile(filepath.Join("internal", "astgrep"), "astgrep", "ast-grep", t.GOOS, t.GOARCH, sha); err != nil {
			return err
		}
	}
	return writeManifest(filepath.Join(out, "manifest.json"), m)
}

func astGrepAsset(v string, t target) (string, string) {
	base := "https://github.com/ast-grep/ast-grep/releases/download/" + v
	switch {
	case t.GOOS == "linux" && t.GOARCH == "amd64":
		return base + "/app-x86_64-unknown-linux-gnu.zip", "zip"
	case t.GOOS == "linux" && t.GOARCH == "arm64":
		return base + "/app-aarch64-unknown-linux-gnu.zip", "zip"
	case t.GOOS == "darwin" && t.GOARCH == "amd64":
		return base + "/app-x86_64-apple-darwin.zip", "zip"
	case t.GOOS == "darwin" && t.GOARCH == "arm64":
		return base + "/app-aarch64-apple-darwin.zip", "zip"
	case t.GOOS == "windows" && t.GOARCH == "amd64":
		return base + "/app-x86_64-pc-windows-msvc.zip", "zip"
	}
	return "", ""
}

// --- archive helpers ---

// downloadArchiveFile GETs url, walks the archive, and returns the
// contents of the file whose path matches inner (exact, or basename
// when the archive has flat structure).
func downloadArchiveFile(url, kind, inner, wantDigest string) ([]byte, error) {
	if url == "" {
		return nil, fmt.Errorf("no asset URL for this target")
	}
	body, err := fetchURLLimited(url, filepath.Base(url), maxFetchArchiveBytes)
	if err != nil {
		return nil, err
	}
	if wantDigest == "" {
		return nil, fmt.Errorf("missing published digest for %s", filepath.Base(url))
	}
	if got := sha256hex(body); got != wantDigest {
		return nil, fmt.Errorf("digest mismatch for %s: got %s want %s", filepath.Base(url), got, wantDigest)
	}
	switch kind {
	case "tar.gz":
		return readFromTarGz(bytes.NewReader(body), inner)
	case "zip":
		return readFromZip(body, inner)
	}
	return nil, fmt.Errorf("unknown archive kind %q", kind)
}

func readFromTarGz(r io.Reader, inner string) ([]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	base := filepath.Base(inner)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		if h.Name == inner || filepath.Base(h.Name) == base {
			if h.Size < 0 {
				return nil, fmt.Errorf("tar entry %s has invalid size %d", h.Name, h.Size)
			}
			if h.Size > maxFetchBinaryBytes {
				return nil, fmt.Errorf("tar entry %s exceeds %d bytes", h.Name, maxFetchBinaryBytes)
			}
			return readLimitedFetchBody(tr, "tar entry "+h.Name, maxFetchBinaryBytes)
		}
	}
	return nil, fmt.Errorf("entry %q not found in tar.gz", inner)
}

func readFromZip(body []byte, inner string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, err
	}
	base := filepath.Base(inner)
	for _, f := range zr.File {
		if f.Name == inner || filepath.Base(f.Name) == base {
			if f.UncompressedSize64 > uint64(maxFetchBinaryBytes) {
				return nil, fmt.Errorf("zip entry %s exceeds %d bytes", f.Name, maxFetchBinaryBytes)
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer func() { _ = rc.Close() }()
			return readLimitedFetchBody(rc, "zip entry "+f.Name, maxFetchBinaryBytes)
		}
	}
	return nil, fmt.Errorf("entry %q not found in zip", inner)
}

func fetchURLLimited(url, label string, maxBytes int64) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "stado-fetch-binaries")
	resp, err := fetchHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s HTTP %d", label, resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, maxBytes)
	}
	return readLimitedFetchBody(resp.Body, label, maxBytes)
}

func readLimitedFetchBody(r io.Reader, label string, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, maxBytes)
	}
	return data, nil
}

// (helpers fetchSHA256Sidecar + fetchGitHubExpandedAssetDigests
// removed — Cluster T closed the supply-chain regression by
// switching to committed pins in hack/binary-pins.json. Live-fetched
// digests from the same upstream source as the binaries provided no
// independent protection against upstream account/release compromise.)

// --- embed generator ---

// writeEmbedFile emits a per-platform Go source file that `//go:embed`s
// the bundled binary + pins its sha256. Guarded by
// `//go:build stado_embed_binaries && <goos> && <goarch>` so it only
// participates in release builds that pass `-tags stado_embed_binaries`;
// dev builds compile the bundled_stub.go fallback instead (empty bytes
// → PATH resolution).
func writeEmbedFile(pkgDir, pkgName, binBase, goos, goarch, sha string) error {
	binFile := binBase + "-" + goos + "-" + goarch
	if goos == "windows" {
		binFile += ".exe"
	}
	content := fmt.Sprintf(`//go:build stado_embed_binaries && %s && %s

// Generated by hack/fetch-binaries.go — DO NOT EDIT.
// Embedded %s %s/%s binary + sha256 for stado's first-use extractor.

package %s

import (
	_ "embed"
	"runtime"
)

//go:embed bundled/%s
var bundledBytes []byte

var bundledSHA256 = %q

func isWindows() bool { return runtime.GOOS == "windows" }
`, goos, goarch, binBase, goos, goarch, pkgName, binFile, sha)
	fname := fmt.Sprintf("bundled_%s_%s.go", goos, goarch)
	return writeRepoFileAtomic(filepath.Join(pkgDir, fname), []byte(content), 0o644)
}

// --- misc ---

func writeRepoFileAtomic(path string, data []byte, perm os.FileMode) error {
	r, err := workdirpath.New(".")
	if err != nil {
		return err
	}
	rootPath, rel, err := r.RootRelForWrite(path)
	if err != nil {
		return err
	}
	root, err := workdirpath.NewStrictResolver().OpenRoot(rootPath)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	rr := workdirpath.NewRootResolver(root)
	if dir := filepath.Dir(rel); dir != "." {
		if err := rr.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return rr.WriteFileAtomicExactMode(rel, data, perm)
}

func writeManifest(path string, m manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeRepoFileAtomic(path, append(b, '\n'), 0o644)
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "fetch-binaries: "+format+"\n", args...)
	os.Exit(1)
}
