package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Hardening: readSecretFile gated the 0600 perm via os.Stat, which FOLLOWS a
// symlink — so a symlinked secret pointing at a 0600 file elsewhere passed the
// gate. It now opens O_NOFOLLOW + fstats the opened fd (regular + 0600) + reads
// that fd (closing the Stat/ReadFile TOCTOU).

func TestReadSecretFile_ReadsRegular0600(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s")
	if err := os.WriteFile(p, []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readSecretFile(p, "s")
	if err != nil {
		t.Fatalf("regular 0600 secret should read: %v", err)
	}
	if string(got) != "value" {
		t.Errorf("got %q; want %q", got, "value")
	}
}

func TestReadSecretFile_RefusesSymlink(t *testing.T) {
	// A real 0600 target outside the store, and a symlink to it.
	target := filepath.Join(t.TempDir(), "real.txt")
	if err := os.WriteFile(target, []byte("attacker-readable"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	got, err := readSecretFile(link, "linked")
	if err == nil {
		t.Fatalf("symlinked secret must be refused (not followed); got %q", got)
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("symlink refusal should not surface as ErrNotFound; got %v", err)
	}
}

func TestReadSecretFile_RefusesWidePerms(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s")
	if err := os.WriteFile(p, []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecretFile(p, "s"); err == nil {
		t.Error("0644 secret must be refused")
	}
}

func TestReadSecretFile_NotFound(t *testing.T) {
	if _, err := readSecretFile(filepath.Join(t.TempDir(), "nope"), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing secret: want ErrNotFound, got %v", err)
	}
}

func TestReadSecretFile_RefusesDirectory(t *testing.T) {
	d := filepath.Join(t.TempDir(), "adir")
	if err := os.Mkdir(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecretFile(d, "adir"); err == nil {
		t.Error("a directory must not be read as a secret")
	}
}
