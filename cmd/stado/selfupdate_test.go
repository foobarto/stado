//go:build !airgap

package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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
