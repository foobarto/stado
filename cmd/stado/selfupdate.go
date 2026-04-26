//go:build !airgap

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
	"github.com/foobarto/stado/internal/workdirpath"
)

var (
	selfUpdateAPIClient      = &http.Client{Timeout: 30 * time.Second}
	selfUpdateDownloadClient = &http.Client{Timeout: 5 * time.Minute}
)

const (
	maxSelfUpdateChecksumsBytes int64 = 64 << 10
	maxSelfUpdateMinisigBytes   int64 = 16 << 10
	maxSelfUpdateArchiveBytes   int64 = 128 << 20
	maxSelfUpdateBinaryBytes    int64 = 256 << 20
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
		"Integrity: self-update requires a build with an embedded minisign\n" +
		"pubkey and a release that publishes checksums.txt.minisig. The\n" +
		"signature is verified before checksums.txt is trusted, and the\n" +
		"archive sha256 must then match that signed manifest.",
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

	body, err := downloadAndVerify(asset.BrowserDownloadURL, want, asset.Size)
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
	TagName     string    `json:"tag_name"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []ghAsset `json:"assets"`
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
	resp, err := selfUpdateAPIClient.Do(req)
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
// a map. The release manifest must be verified through
// checksums.txt.minisig before the checksums are trusted — self-update
// refuses builds without an embedded minisign pubkey and releases that do
// not publish the signature.
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
	data, err := fetchBytes(checksumsURL, "checksums.txt", maxSelfUpdateChecksumsBytes)
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

// fetchBytes is a thin GET helper that reads a bounded response body.
func fetchBytes(url, label string, maxBytes int64) ([]byte, error) {
	resp, err := selfUpdateAPIClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s HTTP %d", label, resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, maxBytes)
	}
	return readLimitedSelfUpdateBody(resp.Body, label, maxBytes)
}

func readLimitedSelfUpdateBody(r io.Reader, label string, maxBytes int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, maxBytes)
	}
	return data, nil
}

// verifyChecksumsMinisig enforces the release trust root for self-update.
// Self-update is only permitted from builds that embed the project's minisign
// pubkey and from releases that publish a matching checksums.txt.minisig.
func verifyChecksumsMinisig(checksums []byte, minisigURL string) error {
	pinned := audit.EmbeddedMinisignPubkey
	switch {
	case pinned == "":
		return fmt.Errorf("self-update: this build has no embedded minisign pubkey; refusing unsigned release verification")
	case minisigURL == "":
		return fmt.Errorf("self-update: minisign pubkey pinned but release has no checksums.txt.minisig")
	}

	// Both present — enforce.
	pub, err := base64.StdEncoding.DecodeString(pinned)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("self-update: embedded minisign pubkey malformed: %w", err)
	}
	sigBytes, err := fetchBytes(minisigURL, "checksums.txt.minisig", maxSelfUpdateMinisigBytes)
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
func downloadAndVerify(url, wantHex string, advertisedSize int64) (*os.File, error) {
	return downloadAndVerifyLimited(url, wantHex, advertisedSize, maxSelfUpdateArchiveBytes)
}

func downloadAndVerifyLimited(url, wantHex string, advertisedSize, maxBytes int64) (*os.File, error) {
	if advertisedSize > maxBytes {
		return nil, fmt.Errorf("download exceeds %d bytes", maxBytes)
	}
	resp, err := selfUpdateDownloadClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return nil, fmt.Errorf("download exceeds %d bytes", maxBytes)
	}

	tmp, err := os.CreateTemp("", "stado-selfupdate-*.tar.gz")
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	tee := io.TeeReader(io.LimitReader(resp.Body, maxBytes+1), h)
	n, err := io.Copy(tmp, tee)
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, err
	}
	if n > maxBytes {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, fmt.Errorf("download exceeds %d bytes", maxBytes)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != wantHex {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, fmt.Errorf("sha256 mismatch: got %s, want %s", got, wantHex)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
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
	out := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(out)
	}
	f, err := workdirpath.OpenRegularFileNoSymlink(archivePath)
	if err != nil {
		cleanup()
		return "", err
	}
	defer func() { _ = f.Close() }()

	if strings.HasSuffix(assetName, ".zip") {
		if err := extractZipBinary(f, tmp); err != nil {
			cleanup()
			return "", err
		}
	} else {
		gz, err := gzip.NewReader(f)
		if err != nil {
			cleanup()
			return "", err
		}
		defer func() { _ = gz.Close() }()
		tr := tar.NewReader(gz)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				cleanup()
				return "", fmt.Errorf("stado binary not found in archive")
			}
			if err != nil {
				cleanup()
				return "", err
			}
			base := filepath.Base(hdr.Name)
			if base == "stado" || base == "stado.exe" {
				if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != 0 {
					cleanup()
					return "", fmt.Errorf("archive entry %s is not a regular file", hdr.Name)
				}
				if hdr.Size > maxSelfUpdateBinaryBytes {
					cleanup()
					return "", fmt.Errorf("archive entry %s exceeds %d bytes", hdr.Name, maxSelfUpdateBinaryBytes)
				}
				if err := writeSelfUpdatePayload(tmp, tr); err != nil {
					cleanup()
					return "", err
				}
				if err := tmp.Chmod(0o755); err != nil { // #nosec G302 -- extracted self-update payload must remain executable.
					cleanup()
					return "", err
				}
				if err := tmp.Close(); err != nil {
					_ = os.Remove(out)
					return "", err
				}
				return out, nil
			}
		}
	}
	if err := tmp.Chmod(0o755); err != nil { // #nosec G302 -- extracted self-update payload must remain executable.
		cleanup()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(out)
		return "", err
	}
	return out, nil
}

func extractZipBinary(archive *os.File, out *os.File) error {
	info, err := archive.Stat()
	if err != nil {
		return err
	}
	r, err := zip.NewReader(archive, info.Size())
	if err != nil {
		return err
	}
	for _, zf := range r.File {
		base := filepath.Base(zf.Name)
		if base == "stado" || base == "stado.exe" {
			if !zf.FileInfo().Mode().IsRegular() {
				return fmt.Errorf("zip entry %s is not a regular file", zf.Name)
			}
			if zf.UncompressedSize64 > uint64(maxSelfUpdateBinaryBytes) {
				return fmt.Errorf("zip entry %s exceeds %d bytes", zf.Name, maxSelfUpdateBinaryBytes)
			}
			rc, err := zf.Open()
			if err != nil {
				return err
			}
			defer func() { _ = rc.Close() }()
			return writeSelfUpdatePayload(out, rc)
		}
	}
	return fmt.Errorf("stado binary not found in zip")
}

func writeSelfUpdatePayload(out *os.File, in io.Reader) error {
	return writeSelfUpdatePayloadLimited(out, in, maxSelfUpdateBinaryBytes)
}

func writeSelfUpdatePayloadLimited(out *os.File, in io.Reader, maxBytes int64) error {
	if err := out.Truncate(0); err != nil {
		return err
	}
	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return err
	}
	n, err := io.Copy(out, io.LimitReader(in, maxBytes+1))
	if err != nil {
		return err
	}
	if n > maxBytes {
		return fmt.Errorf("self-update payload exceeds %d bytes", maxBytes)
	}
	return out.Sync()
}

// installBinary moves the new binary over the current one, saving the
// previous version to <path>.prev.
func installBinary(newBin string) error {
	cur := currentExe()
	if cur == "" {
		return fmt.Errorf("cannot locate current executable")
	}
	return installBinaryAt(newBin, cur)
}

func installBinaryAt(newBin, cur string) error {
	if err := prepareInstallBinary(newBin); err != nil {
		return err
	}
	prev := cur + ".prev"
	if info, err := os.Lstat(cur); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("current binary is a symlink: %s", cur)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("current binary is not regular: %s", cur)
		}
		if err := os.Rename(cur, prev); err != nil {
			return fmt.Errorf("save previous binary: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat current binary: %w", err)
	}
	if err := os.Rename(newBin, cur); err != nil {
		// Try copy fallback (cross-device rename).
		if err2 := copyFile(newBin, cur); err2 != nil {
			// Best-effort restore of previous.
			_ = os.Rename(prev, cur)
			return fmt.Errorf("install: %w (copy fallback: %v)", err, err2)
		}
	}
	return nil
}

func prepareInstallBinary(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat new binary: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("new binary is a symlink: %s", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("new binary is not regular: %s", path)
	}
	return os.Chmod(path, 0o755) // #nosec G302 -- installed command binary must remain executable.
}

func copyFile(src, dst string) error {
	in, err := workdirpath.OpenRegularFileNoSymlink(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if info.Size() > maxSelfUpdateBinaryBytes {
		return fmt.Errorf("self-update binary exceeds %d bytes: %s", maxSelfUpdateBinaryBytes, src)
	}
	return writeReaderToPath(dst, 0o755, in, maxSelfUpdateBinaryBytes) // #nosec G302 -- installed command binary must remain executable.
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
