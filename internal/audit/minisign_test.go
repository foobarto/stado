package audit

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMinisign_RoundTrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	message := []byte("hello stado")

	sig, err := MinisignSign(priv, 0xdeadbeef, message,
		"test untrusted", "release-v1.0.0")
	if err != nil {
		t.Fatal(err)
	}

	trusted, err := MinisignVerify(pub, message, sig)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if trusted != "release-v1.0.0" {
		t.Errorf("trusted = %q", trusted)
	}
}

func TestMinisign_TamperedMessageDetected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sig, _ := MinisignSign(priv, 0, []byte("original"), "", "")

	_, err := MinisignVerify(pub, []byte("tampered"), sig)
	if err == nil {
		t.Error("verify should fail for tampered message")
	}
}

func TestMinisign_TamperedTrustedCommentDetected(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	sig, _ := MinisignSign(priv, 0, []byte("msg"), "", "original-comment")

	// Replace "original-comment" → "evil-comment" in the signature file.
	tampered := bytes.Replace(sig, []byte("original-comment"), []byte("evil-comment"), 1)
	_, err := MinisignVerify(pub, []byte("msg"), tampered)
	if err == nil {
		t.Error("verify should fail for mutated trusted comment")
	}
}

func TestMinisign_WrongPubkey(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	sig, _ := MinisignSign(priv, 0, []byte("msg"), "", "")
	_, err := MinisignVerify(otherPub, []byte("msg"), sig)
	if err == nil {
		t.Error("verify should fail with different public key")
	}
}

func TestMinisign_SignFile(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	src := filepath.Join(dir, "artifact.tar.gz")
	if err := os.WriteFile(src, bytes.Repeat([]byte{0xaa}, 1024), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := MinisignSignFile(priv, 0x1234, src, "stado release", "stado v1.0.0"); err != nil {
		t.Fatal(err)
	}
	sig, err := os.ReadFile(src + ".minisig")
	if err != nil {
		t.Fatalf("minisig not written: %v", err)
	}
	body, _ := os.ReadFile(src)
	trusted, err := MinisignVerify(pub, body, sig)
	if err != nil {
		t.Errorf("verify: %v", err)
	}
	if trusted != "stado v1.0.0" {
		t.Errorf("trusted = %q", trusted)
	}
}

func TestMinisignSignFileRejectsSidecarSymlink(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	src := filepath.Join(dir, "artifact.tar.gz")
	if err := os.WriteFile(src, []byte("artifact"), 0o644); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(dir, "decoy.minisig")
	if err := os.WriteFile(decoy, []byte("do not replace"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("decoy.minisig", src+".minisig"); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	err := MinisignSignFile(priv, 0x1234, src, "stado release", "stado v1.0.0")
	if err == nil {
		t.Fatal("MinisignSignFile should reject symlinked sidecar path")
	}
	data, readErr := os.ReadFile(decoy)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "do not replace" {
		t.Fatalf("symlink target modified: %q", data)
	}
}

func TestMinisign_FormatShape(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	sig, _ := MinisignSign(priv, 0, []byte("x"), "top", "bottom")
	lines := strings.Split(strings.TrimRight(string(sig), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("want 4 lines, got %d: %q", len(lines), string(sig))
	}
	if !strings.HasPrefix(lines[0], "untrusted comment:") {
		t.Errorf("line 0: %q", lines[0])
	}
	if !strings.HasPrefix(lines[2], "trusted comment:") {
		t.Errorf("line 2: %q", lines[2])
	}
}

func TestParseMinisignFile_BadFormat(t *testing.T) {
	_, _, _, _, err := parseMinisignFile([]byte("too few lines"))
	if err == nil {
		t.Error("expected error on short input")
	}
	_, _, _, _, err = parseMinisignFile([]byte("no header:\nx\ntrusted comment: c\ny\n"))
	if err == nil {
		t.Error("expected error when first line isn't untrusted comment")
	}
}
