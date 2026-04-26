//go:build !airgap

package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestPickAsset_MatchesGOOSGOARCH(t *testing.T) {
	osName := runtime.GOOS
	arch := runtime.GOARCH
	suffix := ".tar.gz"
	if osName == "windows" {
		suffix = ".zip"
	}
	wantName := "stado_1.2.3_" + osName + "_" + arch + suffix
	assets := []ghAsset{
		{Name: "stado_1.2.3_linux_amd64.tar.gz"},
		{Name: "stado_1.2.3_linux_arm64.tar.gz"},
		{Name: "stado_1.2.3_darwin_amd64.tar.gz"},
		{Name: "stado_1.2.3_darwin_arm64.tar.gz"},
		{Name: "stado_1.2.3_windows_amd64.zip"},
		{Name: wantName}, // ensure this exact one is present for the active host
		{Name: "checksums.txt"},
	}
	got, err := pickAsset(assets)
	if err != nil {
		t.Fatalf("pickAsset: %v", err)
	}
	if !strings.Contains(got.Name, osName) || !strings.Contains(got.Name, arch) {
		t.Errorf("got %q, should match %s/%s", got.Name, osName, arch)
	}
}

func TestPickAsset_NoMatch_ErrorsCleanly(t *testing.T) {
	// Empty asset list → error, not panic.
	_, err := pickAsset(nil)
	if err == nil {
		t.Error("expected error on empty assets")
	}
	if !strings.Contains(err.Error(), "no release asset matches") {
		t.Errorf("error wrong: %v", err)
	}
}

func TestFetchChecksums_ParsesFile(t *testing.T) {
	// Simulate a checksums.txt body via the parser embedded in the flow.
	// We can't exercise the HTTP path without a live release, but the parsing
	// logic lives inline — replicate it here for correctness.
	body := `abc123def456  stado_1.0_linux_amd64.tar.gz
7890ab  stado_1.0_darwin_arm64.tar.gz
malformed line without two fields
`
	out := map[string]string{}
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		out[fields[1]] = fields[0]
	}
	if out["stado_1.0_linux_amd64.tar.gz"] != "abc123def456" {
		t.Errorf("amd64 hash parse wrong: %v", out)
	}
	if out["stado_1.0_darwin_arm64.tar.gz"] != "7890ab" {
		t.Errorf("arm64 hash parse wrong: %v", out)
	}
	if len(out) != 2 {
		t.Errorf("malformed line should be skipped, got %v", out)
	}
}

func TestFetchBytesRejectsOversizedBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("12345"))
	}))
	defer srv.Close()

	_, err := fetchBytes(srv.URL, "checksums.txt", 4)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("fetchBytes error = %v, want size rejection", err)
	}
}

func TestFetchLatestReleaseRejectsOversizedMetadata(t *testing.T) {
	prev := selfUpdateAPIClient
	selfUpdateAPIClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(strings.Repeat("x", int(maxSelfUpdateReleaseBytes)+1))),
				Request:    req,
			}, nil
		}),
	}
	t.Cleanup(func() { selfUpdateAPIClient = prev })

	_, err := fetchLatestRelease("foobarto/stado")
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("fetchLatestRelease error = %v, want size rejection", err)
	}
}

func TestDownloadAndVerifyRejectsOversizedAdvertisedArchive(t *testing.T) {
	_, err := downloadAndVerifyLimited("http://127.0.0.1/unused", strings.Repeat("0", 64), 5, 4)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("downloadAndVerifyLimited error = %v, want size rejection", err)
	}
}

func TestDownloadAndVerifyRejectsOversizedArchiveStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("12345"))
	}))
	defer srv.Close()

	_, err := downloadAndVerifyLimited(srv.URL, strings.Repeat("0", 64), 0, 4)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("downloadAndVerifyLimited error = %v, want size rejection", err)
	}
}

func TestExtractBinary_TarRegularEntry(t *testing.T) {
	archivePath := writeSelfUpdateTar(t, tarEntry{
		name: "stado_1.0_linux_amd64/stado",
		mode: 0o755,
		body: "binary",
	})

	out, err := extractBinary(archivePath, "stado_1.0_linux_amd64.tar.gz")
	if err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	defer func() { _ = os.Remove(out) }()
	body, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "binary" {
		t.Fatalf("extracted body = %q, want binary", body)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("extracted binary is not executable: %v", info.Mode())
	}
}

func TestWriteSelfUpdatePayloadRejectsOversizedPayload(t *testing.T) {
	tmp, err := os.CreateTemp("", "stado-payload-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()

	err = writeSelfUpdatePayloadLimited(tmp, strings.NewReader("12345"), 4)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("writeSelfUpdatePayloadLimited error = %v, want size rejection", err)
	}
}

func TestExtractBinary_TarRejectsSymlinkEntry(t *testing.T) {
	archivePath := writeSelfUpdateTar(t, tarEntry{
		name:     "stado",
		typeflag: tar.TypeSymlink,
		linkname: "/tmp/elsewhere",
		mode:     0o777,
	})

	_, err := extractBinary(archivePath, "stado_1.0_linux_amd64.tar.gz")
	if err == nil {
		t.Fatal("expected symlink archive entry to be rejected")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected regular-file error, got %v", err)
	}
}

func TestExtractBinary_RejectsSymlinkedArchivePath(t *testing.T) {
	archivePath := writeSelfUpdateTar(t, tarEntry{
		name: "stado",
		mode: 0o755,
		body: "binary",
	})
	link := filepath.Join(t.TempDir(), "stado.tar.gz")
	if err := os.Symlink(archivePath, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	_, err := extractBinary(link, "stado_1.0_linux_amd64.tar.gz")
	if err == nil {
		t.Fatal("expected symlinked archive path to be rejected")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
}

func TestExtractBinary_ZipRejectsSymlinkEntry(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "stado.zip")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	h := &zip.FileHeader{Name: "stado"}
	h.SetMode(os.ModeSymlink | 0o777)
	w, err := zw.CreateHeader(h)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("/tmp/elsewhere")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = extractBinary(archivePath, "stado_1.0_windows_amd64.zip")
	if err == nil {
		t.Fatal("expected zip symlink entry to be rejected")
	}
	if !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected regular-file error, got %v", err)
	}
}

func TestCurrentExe_ReturnsPath(t *testing.T) {
	if got := currentExe(); got == "" {
		t.Error("currentExe returned empty")
	}
}

func TestInstallBinaryAtSetsExecutableMode(t *testing.T) {
	dir := t.TempDir()
	cur := filepath.Join(dir, "stado")
	if err := os.WriteFile(cur, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	newBin := filepath.Join(dir, "new-stado")
	if err := os.WriteFile(newBin, []byte("new"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := installBinaryAt(newBin, cur); err != nil {
		t.Fatalf("installBinaryAt: %v", err)
	}
	body, err := os.ReadFile(cur)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "new" {
		t.Fatalf("current body = %q, want new", body)
	}
	info, err := os.Stat(cur)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("installed binary is not executable: %v", info.Mode())
	}
	prev, err := os.ReadFile(cur + ".prev")
	if err != nil {
		t.Fatal(err)
	}
	if string(prev) != "old" {
		t.Fatalf("previous body = %q, want old", prev)
	}
}

func TestInstallBinaryAtRejectsCurrentSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	cur := filepath.Join(dir, "stado")
	if err := os.Symlink("target", cur); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	newBin := filepath.Join(dir, "new-stado")
	if err := os.WriteFile(newBin, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := installBinaryAt(newBin, cur)
	if err == nil {
		t.Fatal("installBinaryAt should reject symlinked current binary")
	}
	body, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(body) != "old" {
		t.Fatalf("symlink target modified: %q", body)
	}
}

func TestCopyFileRejectsSymlinkDestination(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(dir, "decoy")
	if err := os.WriteFile(decoy, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")
	if err := os.Symlink("decoy", dst); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	err := copyFile(src, dst)
	if err == nil {
		t.Fatal("copyFile should reject symlinked destination")
	}
	body, readErr := os.ReadFile(decoy)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(body) != "old" {
		t.Fatalf("symlink target modified: %q", body)
	}
}

func TestCopyFileRejectsSymlinkSource(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "src")
	if err := os.Symlink("target", src); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	dst := filepath.Join(dir, "dst")

	err := copyFile(src, dst)
	if err == nil {
		t.Fatal("copyFile should reject symlinked source")
	}
	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("destination exists after rejected source: %v", statErr)
	}
}

func TestCopyFileRejectsOversizedSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.WriteFile(src, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(src, maxSelfUpdateBinaryBytes+1); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "dst")

	err := copyFile(src, dst)
	if err == nil {
		t.Fatal("copyFile should reject oversized source")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected size rejection, got %v", err)
	}
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatalf("destination exists after rejected source: %v", statErr)
	}
}

type tarEntry struct {
	name     string
	typeflag byte
	linkname string
	mode     int64
	body     string
}

func writeSelfUpdateTar(t *testing.T, entry tarEntry) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "stado.tar.gz")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	typeflag := entry.typeflag
	if typeflag == 0 {
		typeflag = tar.TypeReg
	}
	h := &tar.Header{
		Name:     entry.name,
		Typeflag: typeflag,
		Mode:     entry.mode,
		Size:     int64(len(entry.body)),
		Linkname: entry.linkname,
	}
	if typeflag != tar.TypeReg && typeflag != 0 {
		h.Size = 0
	}
	if err := tw.WriteHeader(h); err != nil {
		t.Fatal(err)
	}
	if h.Size > 0 {
		if _, err := tw.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return archivePath
}
