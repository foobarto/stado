package runtime

import (
	"errors"
	"testing"

	"github.com/foobarto/stado/internal/secrets"
)

// host_secrets_isolation_test.go — regression coverage for per-plugin secrets
// isolation at the HOST capability layer (internal/plugins/runtime). The store
// layer is exercised in internal/secrets; this file pins the load-bearing
// security properties of the host wiring:
//
//  1. CanRead/CanWrite glob boundaries — exact vs. prefix, no implicit leading
//     wildcard, broad (empty+declared) matches all, undeclared denies all.
//  2. Cross-plugin isolation end-to-end through the Store using two distinct
//     PluginName values: plugin A's PutScoped secret is NOT visible to plugin B
//     via GetScoped even with broad read; operator-shared (flat Put) IS visible
//     to both via the shared-keyspace fallback.
//  3. CanList requires broad read (empty globs or a literal "*").
//
// Helpers are prefixed `seciso` to avoid identifier clashes with sibling files
// in package runtime.

// secisoStore returns a fresh, isolated secrets.Store backed by a temp dir.
func secisoStore(t *testing.T) *secrets.Store {
	t.Helper()
	return secrets.NewStore(t.TempDir())
}

// --- 1. CanRead / CanWrite glob boundaries ----------------------------------

func TestSecIso_CanRead_GlobBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		declared bool
		globs    []string
		probe    string
		want     bool
	}{
		// Narrow prefix glob: matches the prefix, nothing else.
		{"prefix matches own family", true, []string{"foo_*"}, "foo_x", true},
		{"prefix matches longer member", true, []string{"foo_*"}, "foo_token_123", true},
		{"prefix rejects unrelated", true, []string{"foo_*"}, "bar", false},
		{"prefix rejects sibling family", true, []string{"foo_*"}, "barfoo_x", false},
		// No implicit leading wildcard: a name that merely *contains* the prefix
		// must not match — this is the cross-namespace leak guard.
		{"no leading wildcard", true, []string{"foo_*"}, "xfoo_y", false},
		// The wildcard is required: "foo_*" must not match the bare prefix
		// "foo_" minus the suffix unless the suffix is present (empty is OK).
		{"prefix matches empty suffix", true, []string{"foo_*"}, "foo_", true},
		// Broad read: empty globs + declared = match-all.
		{"broad matches anything", true, nil, "anything_at_all", true},
		{"broad matches bar", true, nil, "bar", true},
		{"broad empty-slice matches", true, []string{}, "whatever", true},
		// Undeclared: nothing matches, regardless of globs present.
		{"undeclared denies all", false, nil, "anything", false},
		{"undeclared denies even with glob", false, []string{"*"}, "anything", false},
		// Multiple globs: union semantics — match any.
		{"multi-glob matches second", true, []string{"foo_*", "db_*"}, "db_pass", true},
		{"multi-glob no match", true, []string{"foo_*", "db_*"}, "cache_x", false},
		// Exact (no wildcard) glob is an EXACT match only.
		{"exact glob matches exactly", true, []string{"api_token"}, "api_token", true},
		{"exact glob rejects prefix sibling", true, []string{"api_token"}, "api_token_2", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sa := &SecretsAccess{ReadDeclared: tc.declared, ReadGlobs: tc.globs}
			if got := sa.CanRead(tc.probe); got != tc.want {
				t.Errorf("CanRead(%q) with declared=%v globs=%v = %v, want %v",
					tc.probe, tc.declared, tc.globs, got, tc.want)
			}
		})
	}
}

func TestSecIso_CanWrite_GlobBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		declared bool
		globs    []string
		probe    string
		want     bool
	}{
		{"prefix matches own family", true, []string{"cache_*"}, "cache_token", true},
		{"prefix rejects unrelated", true, []string{"cache_*"}, "api_token", false},
		{"no leading wildcard", true, []string{"cache_*"}, "xcache_y", false},
		{"broad matches anything", true, nil, "anything", true},
		{"broad empty-slice matches", true, []string{}, "whatever", true},
		{"undeclared denies all", false, nil, "anything", false},
		{"undeclared denies even with glob", false, []string{"*"}, "anything", false},
		{"exact glob matches exactly", true, []string{"deploy_key"}, "deploy_key", true},
		{"exact glob rejects sibling", true, []string{"deploy_key"}, "deploy_key2", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sa := &SecretsAccess{WriteDeclared: tc.declared, WriteGlobs: tc.globs}
			if got := sa.CanWrite(tc.probe); got != tc.want {
				t.Errorf("CanWrite(%q) with declared=%v globs=%v = %v, want %v",
					tc.probe, tc.declared, tc.globs, got, tc.want)
			}
		})
	}
}

// Read and write gates are independent: a read-only grant must not confer write,
// and vice-versa. (Regression guard for #029 — empty globs once meant match-all
// even when the cap was never declared.)
func TestSecIso_ReadWriteGatesIndependent(t *testing.T) {
	readOnly := &SecretsAccess{ReadDeclared: true} // broad read, no write
	if !readOnly.CanRead("x") {
		t.Error("read-only broad grant should allow read")
	}
	if readOnly.CanWrite("x") {
		t.Error("read-only grant must NOT confer write")
	}

	writeOnly := &SecretsAccess{WriteDeclared: true} // broad write, no read
	if !writeOnly.CanWrite("x") {
		t.Error("write-only broad grant should allow write")
	}
	if writeOnly.CanRead("x") {
		t.Error("write-only grant must NOT confer read")
	}
}

// --- 2. CanList requires broad read ----------------------------------------

func TestSecIso_CanList_RequiresBroadRead(t *testing.T) {
	tests := []struct {
		name     string
		declared bool
		globs    []string
		want     bool
	}{
		{"broad (empty) read allows list", true, nil, true},
		{"broad (empty-slice) read allows list", true, []string{}, true},
		{"literal star allows list", true, []string{"*"}, true},
		{"star among others allows list", true, []string{"foo_*", "*"}, true},
		{"narrow glob denies list", true, []string{"foo_*"}, false},
		{"exact glob denies list", true, []string{"api_token"}, false},
		{"undeclared denies list", false, nil, false},
		{"undeclared denies even with star", false, []string{"*"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sa := &SecretsAccess{ReadDeclared: tc.declared, ReadGlobs: tc.globs}
			if got := sa.CanList(); got != tc.want {
				t.Errorf("CanList() with declared=%v globs=%v = %v, want %v",
					tc.declared, tc.globs, got, tc.want)
			}
		})
	}
}

// --- 3. Cross-plugin isolation end-to-end through the Store -----------------

// Two SecretsAccess instances, distinct PluginName, share one backing Store.
// Plugin A writes a scoped secret; plugin B — even with broad read — must not
// see it. An operator-shared (flat Put) secret must be readable by BOTH.
//
// This is the security property the whole layer exists to enforce: the
// per-plugin keyspace must not leak across PluginName, while the shared
// operator keyspace stays the deliberate common ground.
func TestSecIso_CrossPlugin_ScopedSecretNotLeaked(t *testing.T) {
	store := secisoStore(t)

	// SecretsAccess for plugin A and plugin B. Both have broad read so the only
	// thing standing between B and A's secret is PluginName scoping in the Store.
	saA := &SecretsAccess{
		Store:        store,
		ReadDeclared: true, WriteDeclared: true, // broad read+write
		PluginName: "plugin-a",
	}
	saB := &SecretsAccess{
		Store:        store,
		ReadDeclared: true, WriteDeclared: true, // broad read+write
		PluginName: "plugin-b",
	}

	const name = "api_token"

	// The capability gate passes for both (broad read/write), so the isolation
	// can only come from the Store's PluginName scoping — exactly what we test.
	if !saA.CanWrite(name) || !saB.CanRead(name) {
		t.Fatal("precondition: both plugins should pass the broad cap gate")
	}

	// Plugin A writes its scoped secret (mirrors stado_secrets_put: PutScoped
	// with host.Secrets.PluginName).
	if err := store.PutScoped(saA.PluginName, name, []byte("A-private")); err != nil {
		t.Fatalf("plugin A PutScoped: %v", err)
	}

	// Plugin A can read back its own scoped secret.
	gotA, err := store.GetScoped(saA.PluginName, name)
	if err != nil {
		t.Fatalf("plugin A GetScoped own secret: %v", err)
	}
	if string(gotA) != "A-private" {
		t.Errorf("plugin A read back %q, want %q", gotA, "A-private")
	}

	// Plugin B, despite broad read, must NOT see A's scoped secret. With no
	// shared (operator) secret of that name, GetScoped must fall through to
	// ErrNotFound — NOT return A's bytes.
	gotB, err := store.GetScoped(saB.PluginName, name)
	if !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("plugin B GetScoped of A's secret: got (%q, %v), want ErrNotFound — CROSS-PLUGIN LEAK", gotB, err)
	}
	if gotB != nil {
		t.Errorf("plugin B GetScoped returned bytes %q for A's scoped secret — LEAK", gotB)
	}
}

// Plugin B writing its OWN scoped secret of the same name must not clobber or
// reveal A's, and each plugin reads back only its own bytes.
func TestSecIso_CrossPlugin_SameNameDistinctValues(t *testing.T) {
	store := secisoStore(t)
	const name = "token"

	if err := store.PutScoped("plugin-a", name, []byte("A-val")); err != nil {
		t.Fatalf("A PutScoped: %v", err)
	}
	if err := store.PutScoped("plugin-b", name, []byte("B-val")); err != nil {
		t.Fatalf("B PutScoped: %v", err)
	}

	gotA, err := store.GetScoped("plugin-a", name)
	if err != nil {
		t.Fatalf("A GetScoped: %v", err)
	}
	gotB, err := store.GetScoped("plugin-b", name)
	if err != nil {
		t.Fatalf("B GetScoped: %v", err)
	}
	if string(gotA) != "A-val" {
		t.Errorf("plugin A sees %q, want A-val (B clobbered A?)", gotA)
	}
	if string(gotB) != "B-val" {
		t.Errorf("plugin B sees %q, want B-val", gotB)
	}

	// Plugin A removing its own secret must not affect B's.
	if err := store.RemoveScoped("plugin-a", name); err != nil {
		t.Fatalf("A RemoveScoped: %v", err)
	}
	if _, err := store.GetScoped("plugin-a", name); !errors.Is(err, secrets.ErrNotFound) {
		t.Errorf("after A removed its own secret, A GetScoped err = %v, want ErrNotFound", err)
	}
	stillB, err := store.GetScoped("plugin-b", name)
	if err != nil || string(stillB) != "B-val" {
		t.Errorf("A's RemoveScoped touched B's secret: got (%q, %v), want (B-val, nil)", stillB, err)
	}
}

// An operator-provisioned secret (flat Put — the shared keyspace) is the
// deliberate common ground: every plugin granted read should see it via the
// GetScoped shared-keyspace fallback, regardless of PluginName.
func TestSecIso_OperatorSharedSecretReadableByAllPlugins(t *testing.T) {
	store := secisoStore(t)
	const name = "shared_api_key"

	// Operator provisions via the flat (shared) Put. Put already writes the file
	// 0600 (it chmods after write, before rename) so readSecretFile's 0600 gate
	// is satisfied without further fixup.
	if err := store.Put(name, []byte("operator-key")); err != nil {
		t.Fatalf("operator Put: %v", err)
	}

	for _, plugin := range []string{"plugin-a", "plugin-b", "github.com/owner/repo"} {
		got, err := store.GetScoped(plugin, name)
		if err != nil {
			t.Errorf("plugin %q GetScoped shared secret: %v", plugin, err)
			continue
		}
		if string(got) != "operator-key" {
			t.Errorf("plugin %q read shared secret = %q, want operator-key", plugin, got)
		}
	}
}

// A plugin's PutScoped must never write into (and thus never overwrite) the
// shared operator keyspace: after a plugin writes a secret of the same name as
// an operator secret, the operator's flat Get must still return the operator's
// value, and the plugin's GetScoped must prefer its own scoped copy.
func TestSecIso_PluginPutScopedDoesNotClobberOperator(t *testing.T) {
	store := secisoStore(t)
	const name = "deploy_token"

	if err := store.Put(name, []byte("operator-value")); err != nil {
		t.Fatalf("operator Put: %v", err)
	}

	// Plugin writes its own scoped secret of the SAME name.
	if err := store.PutScoped("plugin-a", name, []byte("plugin-value")); err != nil {
		t.Fatalf("plugin PutScoped: %v", err)
	}

	// Operator's flat keyspace is untouched.
	op, err := store.Get(name)
	if err != nil {
		t.Fatalf("operator Get after plugin write: %v", err)
	}
	if string(op) != "operator-value" {
		t.Errorf("operator secret clobbered: got %q, want operator-value", op)
	}

	// Plugin sees its own scoped copy, shadowing the shared one.
	pv, err := store.GetScoped("plugin-a", name)
	if err != nil {
		t.Fatalf("plugin GetScoped: %v", err)
	}
	if string(pv) != "plugin-value" {
		t.Errorf("plugin GetScoped = %q, want plugin-value (own scope should shadow shared)", pv)
	}

	// A DIFFERENT plugin with no scoped copy falls back to the shared operator
	// secret — not plugin-a's scoped value.
	other, err := store.GetScoped("plugin-b", name)
	if err != nil {
		t.Fatalf("plugin-b GetScoped: %v", err)
	}
	if string(other) != "operator-value" {
		t.Errorf("plugin-b saw %q, want operator-value (must not see plugin-a's scoped copy)", other)
	}
}
