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

func TestLoadOrCreateKey_CreatesWithCorrectPerms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "key")
	k, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(k) != ed25519.PrivateKeySize {
		t.Errorf("key size = %d", len(k))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key perms = %o, want 0600", info.Mode().Perm())
	}
}

func TestLoadOrCreateKey_ReusesExistingKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	k1, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(k1, k2) {
		t.Error("LoadOrCreateKey should return the same key on second call")
	}
}

func TestLoadOrCreateKeyRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	decoy := filepath.Join(dir, "decoy")
	if err := os.WriteFile(decoy, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "key")
	if err := os.Symlink("decoy", path); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if _, err := LoadOrCreateKey(path); err == nil {
		t.Fatal("LoadOrCreateKey should reject symlinked key path")
	}
	data, err := os.ReadFile(decoy)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "not a key" {
		t.Fatalf("symlink target modified: %q", data)
	}
}

func TestLoadOrCreateKeyRejectsParentSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "keys-link")
	if err := os.Symlink("target", link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if _, err := LoadOrCreateKey(filepath.Join(link, "agent.ed25519")); err == nil {
		t.Fatal("LoadOrCreateKey should reject symlinked key parent dirs")
	}
	if _, err := os.Stat(filepath.Join(target, "agent.ed25519")); !os.IsNotExist(err) {
		t.Fatalf("symlink target was modified, stat err = %v", err)
	}
}

func TestLoadOrCreateKeyDoesNotOverwriteMalformedExistingKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte("not a key"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadOrCreateKey(path); err == nil {
		t.Fatal("LoadOrCreateKey should reject malformed existing key")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "not a key" {
		t.Fatalf("malformed key was overwritten: %q", data)
	}
}

func TestSignAndVerify_RoundTrip(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := NewSigner(priv)
	body := "tool(path): summary\n\nTool: write\nTurn: 1\n"
	sig := signer.Sign("deadbeef", []string{"parent1"}, body)
	if sig == "" {
		t.Fatal("empty sig from non-nil signer")
	}
	withSig := AppendTrailer(body, sig)
	if err := Verify(signer.Public(), "deadbeef", []string{"parent1"}, withSig); err != nil {
		t.Errorf("verify: %v", err)
	}
}

func TestVerify_DetectsTamperedBody(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := NewSigner(priv)
	body := "write(a.go): added a.go\n\nTool: write\n"
	withSig := AppendTrailer(body, signer.Sign("tree1", nil, body))

	// Tamper: change trailer value.
	tampered := strings.Replace(withSig, "Tool: write", "Tool: read", 1)
	if err := Verify(signer.Public(), "tree1", nil, tampered); err == nil {
		t.Error("verify should reject tampered body")
	}
}

func TestVerify_DetectsTamperedTreeHash(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := NewSigner(priv)
	body := "read(foo.go): read\n\nTool: read\n"
	withSig := AppendTrailer(body, signer.Sign("tree1", nil, body))
	if err := Verify(signer.Public(), "tree2", nil, withSig); err == nil {
		t.Error("verify should reject mismatched tree hash")
	}
}

func TestVerify_DetectsTamperedParents(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := NewSigner(priv)
	body := "bash(make): built\n\nTool: bash\n"
	withSig := AppendTrailer(body, signer.Sign("t", []string{"p1"}, body))
	if err := Verify(signer.Public(), "t", []string{"p2"}, withSig); err == nil {
		t.Error("verify should reject mismatched parents")
	}
}

func TestVerify_MissingSignatureReturnsError(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	if err := Verify(priv.Public().(ed25519.PublicKey), "t", nil, "no trailer"); err == nil {
		t.Error("verify should fail when no signature present")
	}
}

func TestAppendTrailer_ReplacesExisting(t *testing.T) {
	body := "title\n\nSignature: ed25519:AAAA\n"
	out := AppendTrailer(body, "ed25519:BBBB")
	// There should be exactly one Signature line ending with BBBB.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	sigLines := 0
	for _, l := range lines {
		if strings.HasPrefix(l, "Signature:") {
			sigLines++
			if !strings.Contains(l, "BBBB") {
				t.Errorf("signature line kept old value: %q", l)
			}
		}
	}
	if sigLines != 1 {
		t.Errorf("expected 1 signature line, got %d: %q", sigLines, out)
	}
}

func TestFingerprint_Stable(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	f1 := Fingerprint(pub)
	f2 := Fingerprint(pub)
	if f1 != f2 || len(f1) != 16 {
		t.Errorf("fingerprint not stable/16-hex: %q %q", f1, f2)
	}
}

func TestSigner_NilIsNoop(t *testing.T) {
	var s *Signer
	if got := s.Sign("t", nil, "body"); got != "" {
		t.Errorf("nil signer should return empty, got %q", got)
	}
	if pub := s.Public(); pub != nil {
		t.Errorf("nil signer pub should be nil, got %v", pub)
	}
}

func TestExportJSONL_ParseMessageTrailers(t *testing.T) {
	title, trailers := parseMessage("write(x.go): added x\n\nTool: write\nTurn: 3\nSignature: ed25519:ZZZZ\n")
	if title != "write(x.go): added x" {
		t.Errorf("title = %q", title)
	}
	if trailers["Tool"] != "write" || trailers["Turn"] != "3" {
		t.Errorf("trailers = %v", trailers)
	}
	if _, present := trailers["Signature"]; present {
		t.Error("signature trailer should NOT leak into the export record")
	}
}
