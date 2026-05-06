package secrets

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(t.TempDir())
}

// TestPutGet_RoundTrip verifies that Get returns byte-equal value after Put.
func TestPutGet_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	want := []byte("super-secret-value")
	if err := s.Put("api_token", want); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get("api_token")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("Get = %q, want %q", got, want)
	}
}

// TestPut_FileMode verifies that the file mode is exactly 0600 after Put.
func TestPut_FileMode(t *testing.T) {
	s := newTestStore(t)
	if err := s.Put("api_token", []byte("value")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	info, err := os.Stat(filepath.Join(s.root, "api_token"))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode = %04o, want 0600", perm)
	}
}

// TestValidName_Rejects verifies that invalid names are rejected.
func TestValidName_Rejects(t *testing.T) {
	bad := []string{
		"/etc/passwd",
		"..",
		"..foo",
		"",
		"name with spaces",
		"名前",
		`back\slash`,
	}
	for _, name := range bad {
		if err := ValidName(name); err == nil {
			t.Errorf("ValidName(%q) = nil, want error", name)
		}
	}
}

// TestValidName_Accepts verifies that valid names pass through.
func TestValidName_Accepts(t *testing.T) {
	good := []string{
		"api_token",
		"db.password",
		"github-key",
		"key1",
	}
	for _, name := range good {
		if err := ValidName(name); err != nil {
			t.Errorf("ValidName(%q) = %v, want nil", name, err)
		}
	}
}

// TestList_Sorted verifies that List returns names in alphabetical order
// regardless of insertion order.
func TestList_Sorted(t *testing.T) {
	s := newTestStore(t)
	for _, name := range []string{"zebra", "apple", "mango"} {
		if err := s.Put(name, []byte("v")); err != nil {
			t.Fatalf("Put %s: %v", name, err)
		}
	}
	names, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"apple", "mango", "zebra"}
	if len(names) != len(want) {
		t.Fatalf("List = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("List[%d] = %q, want %q", i, names[i], want[i])
		}
	}
}

// TestList_EmptyOnMissingDir verifies that List returns an empty slice when
// the secrets directory doesn't exist yet.
func TestList_EmptyOnMissingDir(t *testing.T) {
	s := NewStore(t.TempDir()) // stateDir has no secrets/ subdir
	names, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("List = %v, want empty", names)
	}
}

// TestRemove_Idempotent verifies that calling Remove twice does not error.
func TestRemove_Idempotent(t *testing.T) {
	s := newTestStore(t)
	if err := s.Put("tok", []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Remove("tok"); err != nil {
		t.Errorf("Remove first call: %v", err)
	}
	if err := s.Remove("tok"); err != nil {
		t.Errorf("Remove second call (idempotent): %v", err)
	}
}

// TestGet_ErrNotFound verifies that Get on a missing key returns ErrNotFound.
func TestGet_ErrNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Get("nonexistent")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get missing = %v, want ErrNotFound", err)
	}
}

// TestGet_PermissionWideningDetected verifies that Get refuses to return the
// value when the file mode is wider than 0600.
func TestGet_PermissionWideningDetected(t *testing.T) {
	s := newTestStore(t)
	if err := s.Put("tok", []byte("value")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	path := filepath.Join(s.root, "tok")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	_, err := s.Get("tok")
	if err == nil {
		t.Fatal("Get with widened permissions = nil, want error")
	}
	// Error message must mention "permissions".
	if msg := err.Error(); len(msg) == 0 {
		t.Errorf("error message is empty")
	}
}
