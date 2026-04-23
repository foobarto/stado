package stado_test

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInstallScript_InstallsVerifiedArchiveFromFixture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh targets Linux and macOS")
	}

	root := t.TempDir()
	assetsDir := filepath.Join(root, "assets")
	binDir := filepath.Join(root, "bin")
	fakeBin := filepath.Join(root, "fakebin")
	if err := os.MkdirAll(assetsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}

	assetName := fmt.Sprintf("stado_0.0.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	archivePath := filepath.Join(assetsDir, assetName)
	binaryBody := []byte("#!/bin/sh\necho fixture-stado\n")
	if err := writeTarGz(archivePath, "stado", binaryBody, 0o755); err != nil {
		t.Fatal(err)
	}
	sum, err := fileSHA256(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(assetsDir, "checksums.txt"),
		[]byte(sum+"  "+assetName+"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"checksums.txt.sig", "checksums.txt.cert"} {
		if err := os.WriteFile(filepath.Join(assetsDir, name), []byte("fixture\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(fakeBin, "cosign"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("bash", "install.sh")
	cmd.Env = append(os.Environ(),
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"STADO_INSTALL_BASE_URL=file://"+assetsDir,
		"STADO_INSTALL_DIR="+binDir,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh failed: %v\n%s", err, out)
	}

	installed := filepath.Join(binDir, "stado")
	body, err := os.ReadFile(installed)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(binaryBody) {
		t.Fatalf("installed binary body = %q, want %q", body, binaryBody)
	}
}

func writeTarGz(path, name string, body []byte, mode int64) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	hdr := &tar.Header{
		Name: name,
		Mode: mode,
		Size: int64(len(body)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err = tw.Write(body)
	return err
}

func fileSHA256(path string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}
