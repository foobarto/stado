package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/foobarto/stado/internal/audit"
)

var (
	selfUpdateDryRun bool
	selfUpdateForce  bool
	selfUpdateRepo   string
)

var selfUpdateCmd = &cobra.Command{
	Use:   "self-update",
	Short: "Download + install the latest stado release for this OS/arch",
	Long: "Queries the GitHub Releases API of --repo (default foobarto/stado),\n" +
		"picks the archive matching this host's GOOS/GOARCH, downloads it,\n" +
		"verifies the sha256 from the release's checksums.txt, extracts the\n" +
		"stado binary, and atomically swaps it into place.\n\n" +
		"Keeps the previous binary at <bin>.prev so you can roll back.\n\n" +
		"Integrity: sha256 from checksums.txt always checked. When the build\n" +
		"has an embedded minisign pubkey AND the release publishes a\n" +
		"checksums.txt.minisig, the signature is verified before the\n" +
		"checksums are trusted. Cosign verification lands alongside the\n" +
		"full sigstore wiring in a follow-up.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSelfUpdate()
	},
}

func init() {
	selfUpdateCmd.Flags().BoolVar(&selfUpdateDryRun, "dry-run", false, "Show what would happen without touching the binary")
	selfUpdateCmd.Flags().BoolVar(&selfUpdateForce, "force", false, "Upgrade even if the current binary is already the latest version")
	selfUpdateCmd.Flags().StringVar(&selfUpdateRepo, "repo", "foobarto/stado", "GitHub owner/repo to update from")
	rootCmd.AddCommand(selfUpdateCmd)
}

func runSelfUpdate() error {
	rel, err := fetchLatestRelease(selfUpdateRepo)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "latest release: %s (%s)\n", rel.TagName, rel.PublishedAt)

	current := strings.TrimPrefix(version, "v")
	available := strings.TrimPrefix(rel.TagName, "v")
	if !selfUpdateForce && current == available {
		fmt.Fprintf(os.Stderr, "already at %s (use --force to reinstall)\n", current)
		return nil
	}

	asset, err := pickAsset(rel.Assets)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "picked asset: %s (%d bytes)\n", asset.Name, asset.Size)

	checksums, err := fetchChecksums(rel.Assets)
	if err != nil {
		return fmt.Errorf("checksums: %w", err)
	}
	want, ok := checksums[asset.Name]
	if !ok {
		return fmt.Errorf("no sha256 for %s in checksums.txt", asset.Name)
	}

	if selfUpdateDryRun {
		fmt.Fprintf(os.Stderr, "dry-run: would download %s\n", asset.BrowserDownloadURL)
		fmt.Fprintf(os.Stderr, "dry-run: would verify sha256 %s\n", want)
		fmt.Fprintf(os.Stderr, "dry-run: would install into current executable path\n")
		return nil
	}

	body, err := downloadAndVerify(asset.BrowserDownloadURL, want)
	if err != nil {
		return err
	}
	defer os.Remove(body.Name())

	binPath, err := extractBinary(body.Name(), asset.Name)
	if err != nil {
		return err
	}
	defer os.Remove(binPath)

	if err := installBinary(binPath); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "installed %s. Previous binary saved at %s\n",
		rel.TagName, currentExe()+".prev")
	return nil
}

// --- GitHub releases API ---

type ghRelease struct {
	TagName     string      `json:"tag_name"`
	PublishedAt time.Time   `json:"published_at"`
	Assets      []ghAsset   `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func fetchLatestRelease(repo string) (*ghRelease, error) {
	url := "https://api.github.com/repos/" + repo + "/releases/latest"
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "stado-self-update")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github api: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("github api HTTP %d: %s", resp.StatusCode, body)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// pickAsset chooses the archive matching this host's GOOS/GOARCH.
// Goreleaser names archives "stado_<ver>_<os>_<arch>.tar.gz" (.zip on windows).
func pickAsset(assets []ghAsset) (ghAsset, error) {
	suffix := ".tar.gz"
	if runtime.GOOS == "windows" {
		suffix = ".zip"
	}
	osName := runtime.GOOS
	arch := runtime.GOARCH
	for _, a := range assets {
		if !strings.HasSuffix(a.Name, suffix) {
			continue
		}
		n := strings.ToLower(a.Name)
		if strings.Contains(n, "_"+osName+"_") && strings.Contains(n, "_"+arch+".") {
			return a, nil
		}
	}
	return ghAsset{}, fmt.Errorf("no release asset matches %s/%s", osName, arch)
}

// fetchChecksums parses checksums.txt (one "sha256  filename" per line) into
// a map. When the release also publishes checksums.txt.minisig AND stado
// was built with a pinned EmbeddedMinisignPubkey, the minisig is verified
// against the embedded key before the checksums are trusted — DESIGN
// §"Phase 10.8b: signature verification on self-update". No embedded key
// = advisory-only, sha256 remains the integrity proof.
func fetchChecksums(assets []ghAsset) (map[string]string, error) {
	var checksumsURL, minisigURL string
	for _, a := range assets {
		switch a.Name {
		case "checksums.txt":
			checksumsURL = a.BrowserDownloadURL
		case "checksums.txt.minisig":
			minisigURL = a.BrowserDownloadURL
		}
	}
	if checksumsURL == "" {
		return nil, fmt.Errorf("no checksums.txt in release assets")
	}

	// Download checksums.txt.
	data, err := fetchBytes(checksumsURL, "checksums.txt")
	if err != nil {
		return nil, err
	}

	// Verify the minisig when we have both (a) a pinned embedded pubkey
	// and (b) a minisig asset on the release. Otherwise log the state
	// so the user knows what the integrity guarantee actually is.
	if err := verifyChecksumsMinisig(data, minisigURL); err != nil {
		return nil, err
	}

	out := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		out[fields[1]] = fields[0]
	}
	return out, nil
}

// fetchBytes is a thin GET helper that reads the whole body.
func fetchBytes(url, label string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s HTTP %d", label, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// verifyChecksumsMinisig enforces the signature contract when both sides
// are present. Returns nil (+ stderr advisory) for the degraded cases so
// existing environments without a pinned key keep working.
func verifyChecksumsMinisig(checksums []byte, minisigURL string) error {
	pinned := audit.EmbeddedMinisignPubkey
	switch {
	case pinned == "" && minisigURL == "":
		fmt.Fprintln(os.Stderr,
			"self-update: no minisign pubkey pinned and no .minisig asset — sha256 is the only integrity check.")
		return nil
	case pinned == "":
		fmt.Fprintln(os.Stderr,
			"self-update: no minisign pubkey pinned; release publishes a .minisig but we can't verify it. (PR O wires the ceremony.)")
		return nil
	case minisigURL == "":
		fmt.Fprintln(os.Stderr,
			"self-update: minisign pubkey pinned but release has no checksums.txt.minisig — falling back to sha256 only.")
		return nil
	}

	// Both present — enforce.
	pub, err := base64.StdEncoding.DecodeString(pinned)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("self-update: embedded minisign pubkey malformed: %w", err)
	}
	sigBytes, err := fetchBytes(minisigURL, "checksums.txt.minisig")
	if err != nil {
		return err
	}
	trusted, err := audit.MinisignVerify(ed25519.PublicKey(pub), checksums, sigBytes)
	if err != nil {
		return fmt.Errorf("self-update: minisign verification failed: %w", err)
	}
	fmt.Fprintf(os.Stderr, "self-update: minisign verified (trusted comment: %s)\n", trusted)
	return nil
}

// downloadAndVerify streams url to a temp file, computing sha256 as it goes.
// Returns the temp file (caller must remove) or an error if the digest doesn't
// match wantHex.
func downloadAndVerify(url, wantHex string) (*os.File, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download HTTP %d", resp.StatusCode)
	}

	tmp, err := os.CreateTemp("", "stado-selfupdate-*.tar.gz")
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	tee := io.TeeReader(resp.Body, h)
	if _, err := io.Copy(tmp, tee); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != wantHex {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, fmt.Errorf("sha256 mismatch: got %s, want %s", got, wantHex)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, err
	}
	return tmp, nil
}

// extractBinary opens archivePath (.tar.gz or .zip) and writes the `stado`
// entry into a new temp file, returning its path.
func extractBinary(archivePath, assetName string) (string, error) {
	tmp, err := os.CreateTemp("", "stado-bin-*")
	if err != nil {
		return "", err
	}
	tmp.Close()
	out := tmp.Name()
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	if strings.HasSuffix(assetName, ".zip") {
		if err := extractZipBinary(archivePath, out); err != nil {
			os.Remove(out)
			return "", err
		}
	} else {
		gz, err := gzip.NewReader(f)
		if err != nil {
			os.Remove(out)
			return "", err
		}
		defer gz.Close()
		tr := tar.NewReader(gz)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				os.Remove(out)
				return "", fmt.Errorf("stado binary not found in archive")
			}
			if err != nil {
				os.Remove(out)
				return "", err
			}
			base := filepath.Base(hdr.Name)
			if base == "stado" || base == "stado.exe" {
				w, err := os.OpenFile(out, os.O_WRONLY|os.O_TRUNC, 0o755)
				if err != nil {
					os.Remove(out)
					return "", err
				}
				if _, err := io.Copy(w, tr); err != nil {
					w.Close()
					os.Remove(out)
					return "", err
				}
				w.Close()
				if err := os.Chmod(out, 0o755); err != nil {
					return "", err
				}
				return out, nil
			}
		}
	}
	return out, nil
}

func extractZipBinary(archivePath, outPath string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, zf := range r.File {
		base := filepath.Base(zf.Name)
		if base == "stado" || base == "stado.exe" {
			rc, err := zf.Open()
			if err != nil {
				return err
			}
			defer rc.Close()
			w, err := os.OpenFile(outPath, os.O_WRONLY|os.O_TRUNC, 0o755)
			if err != nil {
				return err
			}
			defer w.Close()
			if _, err := io.Copy(w, rc); err != nil {
				return err
			}
			return os.Chmod(outPath, 0o755)
		}
	}
	return fmt.Errorf("stado binary not found in zip")
}

// installBinary moves the new binary over the current one, saving the
// previous version to <path>.prev.
func installBinary(newBin string) error {
	cur := currentExe()
	if cur == "" {
		return fmt.Errorf("cannot locate current executable")
	}
	prev := cur + ".prev"
	if _, err := os.Stat(cur); err == nil {
		if err := os.Rename(cur, prev); err != nil {
			return fmt.Errorf("save previous binary: %w", err)
		}
	}
	if err := os.Rename(newBin, cur); err != nil {
		// Try copy fallback (cross-device rename).
		if err2 := copyFile(newBin, cur); err2 != nil {
			// Best-effort restore of previous.
			_ = os.Rename(prev, cur)
			return fmt.Errorf("install: %w (copy fallback: %v)", err, err2)
		}
	}
	return os.Chmod(cur, 0o755)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func currentExe() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved
	}
	return exe
}
