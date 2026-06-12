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

func TestLoadOrCreateKeyRejectsValidKeySymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target-key")
	if _, err := LoadOrCreateKey(target); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "key")
	if err := os.Symlink("target-key", path); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	if _, err := LoadOrCreateKey(path); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("LoadOrCreateKey error = %v, want symlink rejection", err)
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

// v2 sign + verify roundtrip — author/committer/timestamps are part
// of the signed payload.
func TestSignV2AndVerifyV2_RoundTrip(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := NewSigner(priv)
	body := "tool(path): summary\n\nTool: write\nTurn: 1\n"
	const (
		authorName    = "Bartosz Ptaszynski"
		authorEmail   = "bartosz@foobarto.me"
		authorUnix    = int64(1779600000)
		committerName = "Bartosz Ptaszynski"
		committerEmail = "bartosz@foobarto.me"
		committerUnix  = int64(1779600005)
	)
	sig := signer.SignV2("deadbeef", []string{"parent1"}, body,
		authorName, authorEmail, authorUnix,
		committerName, committerEmail, committerUnix)
	if sig == "" {
		t.Fatal("empty sig from non-nil signer")
	}
	withSig := AppendTrailer(body, sig)
	ident := SignedIdentity{
		AuthorName: authorName, AuthorEmail: authorEmail, AuthorUnix: authorUnix,
		CommitterName: committerName, CommitterEmail: committerEmail, CommitterUnix: committerUnix,
	}
	if err := VerifyV2(signer.Public(), "deadbeef", []string{"parent1"}, withSig, ident); err != nil {
		t.Errorf("verify v2: %v", err)
	}
}

// Codex #138 regression: tampering the author identity must invalidate
// a v2 signature even when tree/parents/body are unchanged.
func TestVerifyV2_DetectsTamperedAuthorIdentity(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := NewSigner(priv)
	body := "tool(path): summary\n\nTool: write\nTurn: 1\n"
	sig := signer.SignV2("tree", []string{"p"}, body,
		"alice", "alice@example.com", 1779600000,
		"alice", "alice@example.com", 1779600000)
	withSig := AppendTrailer(body, sig)

	// Verify with a DIFFERENT author identity — must fail.
	bobIdent := SignedIdentity{
		AuthorName: "bob", AuthorEmail: "bob@example.com", AuthorUnix: 1779600000,
		CommitterName: "alice", CommitterEmail: "alice@example.com", CommitterUnix: 1779600000,
	}
	if err := VerifyV2(signer.Public(), "tree", []string{"p"}, withSig, bobIdent); err == nil {
		t.Error("v2 verify should reject tampered author identity")
	}
}

// Backdated author/committer timestamps must invalidate v2.
func TestVerifyV2_DetectsBackdatedTimestamps(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := NewSigner(priv)
	body := "tool(path): summary\n\nTool: write\nTurn: 1\n"
	sig := signer.SignV2("tree", nil, body,
		"alice", "alice@example.com", 1779600000,
		"alice", "alice@example.com", 1779600000)
	withSig := AppendTrailer(body, sig)

	backdated := SignedIdentity{
		AuthorName: "alice", AuthorEmail: "alice@example.com", AuthorUnix: 1000000000,
		CommitterName: "alice", CommitterEmail: "alice@example.com", CommitterUnix: 1000000000,
	}
	if err := VerifyV2(signer.Public(), "tree", nil, withSig, backdated); err == nil {
		t.Error("v2 verify should reject backdated timestamps")
	}
}

// V1 downgrade is now REJECTED (decision 2026-06-12 clean-break). A v1
// signature binds only tree+parents+body, NOT the author/committer/timestamps,
// so VerifyV2's old v1 fallback let an attacker with sidecar write copy a
// genuine v1 commit's (tree, parents, body, sig) into a new commit with a
// rewritten identity and still verify. VerifyV2 no longer falls back to v1, so
// a v1 signature presented with any identity must fail. IsV1Signature still
// recognizes it as a legacy v1 sig (for reporting) without granting trust.
func TestVerifyV2_RejectsV1Downgrade(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := NewSigner(priv)
	body := "legacy(commit): pre-v2 audit entry\n\nTool: legacy\n"

	v1sig := signer.Sign("tree", []string{"p"}, body)
	withSig := AppendTrailer(body, v1sig)

	// The downgrade: present the v1 sig with an arbitrary (forged) identity.
	forged := SignedIdentity{
		AuthorName: "anybody", AuthorEmail: "any@x", AuthorUnix: 42,
		CommitterName: "anybody", CommitterEmail: "any@x", CommitterUnix: 42,
	}
	if err := VerifyV2(signer.Public(), "tree", []string{"p"}, withSig, forged); err == nil {
		t.Error("VerifyV2 must reject a v1 signature (downgrade), got nil error")
	}
	// It is still recognizable as a legacy v1 sig (reporting, not acceptance).
	if !IsV1Signature(signer.Public(), "tree", []string{"p"}, withSig) {
		t.Error("IsV1Signature should recognize a genuine v1 signature")
	}
	// A tampered/garbage signature is NOT classified as legacy v1.
	if IsV1Signature(signer.Public(), "tree", []string{"different"}, withSig) {
		t.Error("IsV1Signature must not classify a non-matching v1 sig as legacy")
	}
}

// ExtractSignature rejects a body carrying more than one Signature trailer:
// a well-formed signed commit has exactly one, and picking the first of
// several lets an attacker prepend a trailer (anti trailer-injection).
func TestExtractSignature_RejectsMultipleTrailers(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer := NewSigner(priv)
	body := "tool(x): y\n\nTool: write\n"
	sig := signer.Sign("tree", nil, body)
	one := AppendTrailer(body, sig)
	if _, ok := ExtractSignature(one); !ok {
		t.Fatal("exactly one Signature trailer should be extractable")
	}
	// Append a second trailer line directly (AppendTrailer strips first, so it
	// can't produce a duplicate — an injection would bypass it anyway). sig
	// already carries the "ed25519:" prefix.
	two := one + "Signature: " + sig + "\n"
	if _, ok := ExtractSignature(two); ok {
		t.Error("ExtractSignature must reject a body with multiple Signature trailers")
	}
}

// V2 signature with the SAME canonical-bytes-formattable identity
// produced by SignV2 vs. CanonicalBytesV2 must be byte-identical —
// this is the implementation contract between Signer and verifier.
func TestCanonicalBytesV2_StableShape(t *testing.T) {
	ident := SignedIdentity{
		AuthorName: "alice", AuthorEmail: "alice@x", AuthorUnix: 1779600000,
		CommitterName: "alice", CommitterEmail: "alice@x", CommitterUnix: 1779600000,
	}
	got := CanonicalBytesV2("treehash", []string{"p1"}, "body\n", ident)
	want := "stado-audit-v2\n" +
		"tree treehash\n" +
		"parent p1\n" +
		"author alice <alice@x> 1779600000\n" +
		"committer alice <alice@x> 1779600000\n" +
		"\n" +
		"body\n"
	if string(got) != want {
		t.Errorf("CanonicalBytesV2 mismatch:\ngot:  %q\nwant: %q", got, want)
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
