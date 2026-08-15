package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteDownloadedPluginArtifactRejectsSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "operator-file")
	if err := os.WriteFile(outside, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "plugin.wasm")
	if err := os.Symlink(outside, destination); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if err := writeDownloadedPluginArtifact(destination, strings.NewReader("attacker")); err == nil {
		t.Fatal("download followed or replaced a pre-planted symlink")
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep" {
		t.Fatalf("outside file changed through cache symlink: %q", got)
	}
}

func TestReadBoundedPluginArtifactRejectsRatherThanTruncates(t *testing.T) {
	if got, err := readBoundedPluginArtifact(strings.NewReader("12345"), 4); err == nil || got != nil {
		t.Fatalf("oversized artifact = %q, err=%v; want rejection", got, err)
	}
	got, err := readBoundedPluginArtifact(bytes.NewBufferString("1234"), 4)
	if err != nil || string(got) != "1234" {
		t.Fatalf("bounded artifact = %q, err=%v", got, err)
	}
}
