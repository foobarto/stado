//go:build ignore

// fetch-binaries downloads ripgrep + ast-grep release assets for the
// OS/arch matrix stado supports, verifies sha256 against the release
// checksum manifests, and stages them under:
//
//	internal/tools/rg/bundled/rg-<os>-<arch>[.exe]
//	internal/tools/astgrep/bundled/ast-grep-<os>-<arch>[.exe]
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
)

const (
	defaultRipgrepVersion = "14.1.1"
	defaultAstGrepVersion = "0.38.7"
)

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

type githubRelease struct {
	Assets []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	Digest             string `json:"digest"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func main() {
	rgVer := flag.String("ripgrep-version", defaultRipgrepVersion, "ripgrep release tag (without v)")
	sgVer := flag.String("ast-grep-version", defaultAstGrepVersion, "ast-grep release tag (without v)")
	only := flag.String("only", "", "'rg' or 'ast-grep' to limit; default fetches both")
	flag.Parse()

	if *only == "" || *only == "rg" {
		if err := fetchRipgrep(*rgVer); err != nil {
			fatal("ripgrep: %v", err)
		}
	}
	if *only == "" || *only == "ast-grep" {
		if err := fetchAstGrep(*sgVer); err != nil {
			fatal("ast-grep: %v", err)
		}
	}
	fmt.Println("done.")
}

// --- ripgrep ---

func fetchRipgrep(version string) error {
	out := filepath.Join("internal", "tools", "rg", "bundled")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	m := manifest{Version: version, SHA256: map[string]string{}}
	digests, err := fetchReleaseDigests("BurntSushi/ripgrep", version)
	if err != nil {
		return err
	}

	for _, t := range matrix {
		url, archiveKind, innerPath := ripgrepAsset(version, t)
		fmt.Printf("ripgrep %s/%s: %s\n", t.GOOS, t.GOARCH, url)
		b, err := downloadArchiveFile(url, archiveKind, innerPath, digests[filepath.Base(url)])
		if err != nil {
			fmt.Fprintf(os.Stderr, "  skipping %s/%s: %v\n", t.GOOS, t.GOARCH, err)
			continue
		}
		dst := filepath.Join(out, "rg-"+t.GOOS+"-"+t.GOARCH)
		if t.GOOS == "windows" {
			dst += ".exe"
		}
		if err := os.WriteFile(dst, b, 0o755); err != nil {
			return err
		}
		sha := sha256hex(b)
		m.SHA256[filepath.Base(dst)] = sha
		if err := writeEmbedFile(filepath.Join("internal", "tools", "rg"), "rg", "rg", t.GOOS, t.GOARCH, sha); err != nil {
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

func fetchAstGrep(version string) error {
	out := filepath.Join("internal", "tools", "astgrep", "bundled")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	m := manifest{Version: version, SHA256: map[string]string{}}
	digests, err := fetchReleaseDigests("ast-grep/ast-grep", version)
	if err != nil {
		return err
	}

	for _, t := range matrix {
		url, kind := astGrepAsset(version, t)
		fmt.Printf("ast-grep %s/%s: %s\n", t.GOOS, t.GOARCH, url)
		inner := "ast-grep"
		if t.GOOS == "windows" {
			inner = "ast-grep.exe"
		}
		b, err := downloadArchiveFile(url, kind, inner, digests[filepath.Base(url)])
		if err != nil {
			fmt.Fprintf(os.Stderr, "  skipping %s/%s: %v\n", t.GOOS, t.GOARCH, err)
			continue
		}
		dst := filepath.Join(out, "ast-grep-"+t.GOOS+"-"+t.GOARCH)
		if t.GOOS == "windows" {
			dst += ".exe"
		}
		if err := os.WriteFile(dst, b, 0o755); err != nil {
			return err
		}
		sha := sha256hex(b)
		m.SHA256[filepath.Base(dst)] = sha
		if err := writeEmbedFile(filepath.Join("internal", "tools", "astgrep"), "astgrep", "ast-grep", t.GOOS, t.GOARCH, sha); err != nil {
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
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
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
		return readFromZip(bytes.NewReader(body), inner)
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
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("entry %q not found in tar.gz", inner)
}

func readFromZip(r io.Reader, inner string) ([]byte, error) {
	buf, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(bytesReaderAt(buf), int64(len(buf)))
	if err != nil {
		return nil, err
	}
	base := filepath.Base(inner)
	for _, f := range zr.File {
		if f.Name == inner || filepath.Base(f.Name) == base {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer func() { _ = rc.Close() }()
			return io.ReadAll(rc)
		}
	}
	return nil, fmt.Errorf("entry %q not found in zip", inner)
}

// bytesReaderAt is a thin adapter to satisfy zip.NewReader's io.ReaderAt
// requirement without pulling in bytes.Reader's whole surface.
type readerAtBytes []byte

func (r readerAtBytes) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(r)) {
		return 0, io.EOF
	}
	n := copy(p, r[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func bytesReaderAt(b []byte) readerAtBytes { return readerAtBytes(b) }

func fetchReleaseDigests(repo, tag string) (map[string]string, error) {
	req, err := http.NewRequest("GET", "https://api.github.com/repos/"+repo+"/releases/tags/"+tag, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "stado-fetch-binaries")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release metadata HTTP %d", resp.StatusCode)
	}
	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, asset := range rel.Assets {
		if asset.Digest == "" {
			continue
		}
		const prefix = "sha256:"
		if len(asset.Digest) > len(prefix) && asset.Digest[:len(prefix)] == prefix {
			out[asset.Name] = asset.Digest[len(prefix):]
		}
	}
	return out, nil
}

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
	return os.WriteFile(filepath.Join(pkgDir, fname), []byte(content), 0o644)
}

// --- misc ---

func writeManifest(path string, m manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "fetch-binaries: "+format+"\n", args...)
	os.Exit(1)
}
