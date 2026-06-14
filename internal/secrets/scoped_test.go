package secrets

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
)

// TestScoped_NoEncodingCollision regresses the Codex P1: a plugin must not be
// able to land in another plugin's scope by naming itself the sha256-hex of the
// victim's (separator-containing) identity. The disjoint name-/hash- prefixes
// must keep the two scopes distinct.
func TestScoped_NoEncodingCollision(t *testing.T) {
	s := newTestStore(t)
	victim := "github.com/owner/repo" // hashed (contains separators)
	attacker := hex.EncodeToString(func() []byte { h := sha256.Sum256([]byte(victim)); return h[:] }())

	if err := s.PutScoped(victim, "secret", []byte("victim-data")); err != nil {
		t.Fatal(err)
	}
	// The attacker's name is a valid single segment (64 hex chars), so without
	// disjoint prefixes it would map to the same dir as victim's hash.
	if got, err := s.GetScoped(attacker, "secret"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("attacker read victim's secret via hash collision: got %q err %v; want ErrNotFound", got, err)
	}
}

// EP-0038 D19: plugin secrets must be namespaced by plugin identity so one
// plugin can't read, overwrite, or delete another's secret — while
// operator-provisioned (shared) secrets stay readable by any granted plugin.

// TestScoped_IsolationBetweenPlugins: plugin A's written secret is invisible to
// plugin B, and readable by A.
func TestScoped_IsolationBetweenPlugins(t *testing.T) {
	s := newTestStore(t)
	if err := s.PutScoped("plugin-a", "token", []byte("a-secret")); err != nil {
		t.Fatalf("PutScoped(A): %v", err)
	}

	if got, err := s.GetScoped("plugin-a", "token"); err != nil || string(got) != "a-secret" {
		t.Fatalf("A reading own: got %q err %v; want a-secret", got, err)
	}
	if _, err := s.GetScoped("plugin-b", "token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("B reading A's token: err %v; want ErrNotFound (isolation breach)", err)
	}
}

// TestScoped_OperatorSharedFallback: an operator-provisioned secret (flat Put)
// is readable by a plugin via GetScoped (fallback to the shared keyspace).
func TestScoped_OperatorSharedFallback(t *testing.T) {
	s := newTestStore(t)
	if err := s.Put("apikey", []byte("operator-key")); err != nil { // operator CLI path
		t.Fatalf("Put(shared): %v", err)
	}
	for _, plugin := range []string{"plugin-a", "plugin-b"} {
		if got, err := s.GetScoped(plugin, "apikey"); err != nil || string(got) != "operator-key" {
			t.Fatalf("%s reading shared apikey: got %q err %v; want operator-key", plugin, got, err)
		}
	}
}

// TestScoped_ShadowsSharedPerPlugin: a plugin's own scoped secret shadows the
// shared one for THAT plugin, but other plugins + the operator still see the
// shared value.
func TestScoped_ShadowsSharedPerPlugin(t *testing.T) {
	s := newTestStore(t)
	if err := s.Put("k", []byte("shared")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutScoped("plugin-a", "k", []byte("a-private")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetScoped("plugin-a", "k"); string(got) != "a-private" {
		t.Errorf("A should see its own scoped value; got %q", got)
	}
	if got, _ := s.GetScoped("plugin-b", "k"); string(got) != "shared" {
		t.Errorf("B should fall back to shared; got %q", got)
	}
	if got, _ := s.Get("k"); string(got) != "shared" {
		t.Errorf("operator should still see shared; got %q", got)
	}
}

// TestScoped_PutCannotOverwriteOperatorSecret: a plugin's PutScoped writes into
// its own scope, never the operator keyspace.
func TestScoped_PutCannotOverwriteOperatorSecret(t *testing.T) {
	s := newTestStore(t)
	if err := s.Put("op", []byte("operator")); err != nil {
		t.Fatal(err)
	}
	if err := s.PutScoped("plugin-a", "op", []byte("hijacked")); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get("op"); string(got) != "operator" {
		t.Fatalf("operator secret overwritten by plugin Put! got %q; want operator", got)
	}
}

// TestScoped_RemoveCannotDeleteOperatorSecret: RemoveScoped only deletes the
// plugin's own copy; an operator secret survives.
func TestScoped_RemoveCannotDeleteOperatorSecret(t *testing.T) {
	s := newTestStore(t)
	if err := s.Put("op", []byte("operator")); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveScoped("plugin-a", "op"); err != nil {
		t.Fatalf("RemoveScoped: %v", err)
	}
	if got, err := s.Get("op"); err != nil || string(got) != "operator" {
		t.Fatalf("operator secret deleted by plugin Remove! got %q err %v", got, err)
	}
	// A plugin removing its OWN scoped secret works.
	if err := s.PutScoped("plugin-a", "mine", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveScoped("plugin-a", "mine"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetScoped("plugin-a", "mine"); !errors.Is(err, ErrNotFound) {
		t.Errorf("own scoped secret should be gone after RemoveScoped; err %v", err)
	}
}

// TestScoped_ListUnionDeduped: ListScoped returns shared ∪ scoped, deduped.
func TestScoped_ListUnionDeduped(t *testing.T) {
	s := newTestStore(t)
	_ = s.Put("shared1", []byte("x"))
	_ = s.Put("common", []byte("x"))
	_ = s.PutScoped("plugin-a", "private1", []byte("x"))
	_ = s.PutScoped("plugin-a", "common", []byte("y")) // overlaps shared name

	names, err := s.ListScoped("plugin-a")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"shared1": true, "common": true, "private1": true}
	if len(names) != len(want) {
		t.Fatalf("ListScoped = %v; want exactly %v (deduped)", names, want)
	}
	for _, n := range names {
		if !want[n] {
			t.Errorf("unexpected name %q", n)
		}
	}
	// plugin-b only sees the shared ones.
	bNames, _ := s.ListScoped("plugin-b")
	if len(bNames) != 2 {
		t.Errorf("plugin-b ListScoped = %v; want shared1+common only", bNames)
	}
}

// TestScoped_PathUnsafePluginName: an identity with separators (e.g. a canonical
// github path) is path-safe and isolated from a different identity.
func TestScoped_PathUnsafePluginName(t *testing.T) {
	s := newTestStore(t)
	idA := "github.com/owner/repo-a"
	idB := "github.com/owner/repo-b"
	if err := s.PutScoped(idA, "token", []byte("a")); err != nil {
		t.Fatalf("PutScoped(canonical id): %v", err)
	}
	if got, err := s.GetScoped(idA, "token"); err != nil || string(got) != "a" {
		t.Fatalf("A canonical read own: got %q err %v", got, err)
	}
	if _, err := s.GetScoped(idB, "token"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("B canonical reading A's: err %v; want ErrNotFound", err)
	}
}

// TestScoped_LegacySharedSecretReadable: a secret written via the flat Put
// (as older stado versions did before scoping) is still readable via GetScoped
// — the fallback covers migration.
func TestScoped_LegacySharedSecretReadable(t *testing.T) {
	s := newTestStore(t)
	if err := s.Put("legacy", []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetScoped("plugin-a", "legacy"); err != nil || string(got) != "v1" {
		t.Fatalf("legacy shared secret unreadable via GetScoped: got %q err %v", got, err)
	}
}

// TestScoped_OperatorListSkipsPluginScope: the operator-facing List must not
// surface the dot-prefixed plugin scope dir as a secret.
func TestScoped_OperatorListSkipsPluginScope(t *testing.T) {
	s := newTestStore(t)
	_ = s.Put("op", []byte("x"))
	_ = s.PutScoped("plugin-a", "priv", []byte("y"))
	names, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if n == ".plugins" || n == "priv" {
			t.Errorf("operator List leaked plugin scope entry %q; got %v", n, names)
		}
	}
	if len(names) != 1 || names[0] != "op" {
		t.Errorf("operator List = %v; want [op] only", names)
	}
}
