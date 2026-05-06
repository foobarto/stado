package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/foobarto/stado/internal/plugins"
	"github.com/foobarto/stado/internal/secrets"
)

// --- SecretsAccess unit tests ---

func TestSecretsAccess_CanRead_Broad(t *testing.T) {
	s := &SecretsAccess{} // empty ReadGlobs → broad
	if !s.CanRead("anything") {
		t.Error("broad read should allow any name")
	}
	if !s.CanRead("api_token") {
		t.Error("broad read should allow api_token")
	}
}

func TestSecretsAccess_CanRead_Glob(t *testing.T) {
	s := &SecretsAccess{ReadGlobs: []string{"api_*"}}
	if !s.CanRead("api_token") {
		t.Error("api_* should match api_token")
	}
	if !s.CanRead("api_key") {
		t.Error("api_* should match api_key")
	}
	if s.CanRead("db_password") {
		t.Error("api_* should NOT match db_password")
	}
	if s.CanRead("napi_key") {
		t.Error("api_* should NOT match napi_key (no leading wildcard)")
	}
}

func TestSecretsAccess_CanWrite_Broad(t *testing.T) {
	s := &SecretsAccess{} // empty WriteGlobs → broad
	if !s.CanWrite("anything") {
		t.Error("broad write should allow any name")
	}
}

func TestSecretsAccess_CanWrite_Glob(t *testing.T) {
	s := &SecretsAccess{WriteGlobs: []string{"cache_*"}}
	if !s.CanWrite("cache_token") {
		t.Error("cache_* should match cache_token")
	}
	if s.CanWrite("api_token") {
		t.Error("cache_* should NOT match api_token")
	}
}

func TestSecretsAccess_CanList_Broad(t *testing.T) {
	s := &SecretsAccess{} // empty ReadGlobs → broad
	if !s.CanList() {
		t.Error("broad read should allow list")
	}
}

func TestSecretsAccess_CanList_StarGlob(t *testing.T) {
	s := &SecretsAccess{ReadGlobs: []string{"*"}}
	if !s.CanList() {
		t.Error("secrets:read:* should allow list")
	}
}

func TestSecretsAccess_CanList_NarrowGlob(t *testing.T) {
	s := &SecretsAccess{ReadGlobs: []string{"api_*"}}
	if s.CanList() {
		t.Error("narrow glob api_* should NOT allow list")
	}
}

func TestSecretsAccess_Audit_NilEmitter(t *testing.T) {
	s := &SecretsAccess{} // AuditEmitter nil — must not panic
	s.Audit(SecretsAuditEvent{Plugin: "test", Op: "get", Secret: "x", Allowed: true})
}

func TestSecretsAccess_Audit_CallsEmitter(t *testing.T) {
	var got SecretsAuditEvent
	s := &SecretsAccess{
		AuditEmitter: func(ev SecretsAuditEvent) { got = ev },
	}
	s.Audit(SecretsAuditEvent{Plugin: "myplugin", Op: "put", Secret: "key", Allowed: false, Reason: "denied"})
	if got.Plugin != "myplugin" || got.Op != "put" || got.Secret != "key" || got.Allowed || got.Reason != "denied" {
		t.Errorf("audit emitter received unexpected event: %+v", got)
	}
}

// --- NewHost capability parsing tests ---

func TestNewHost_SecretsCapParsing(t *testing.T) {
	tests := []struct {
		name        string
		caps        []string
		wantNil     bool
		readGlobs   []string
		writeGlobs  []string
	}{
		{
			name:    "no secrets caps → Secrets nil",
			caps:    []string{"fs:read:/tmp"},
			wantNil: true,
		},
		{
			name:       "secrets:read broad",
			caps:       []string{"secrets:read"},
			readGlobs:  []string{},
			writeGlobs: nil,
		},
		{
			name:       "secrets:read:<glob>",
			caps:       []string{"secrets:read:api_*"},
			readGlobs:  []string{"api_*"},
			writeGlobs: nil,
		},
		{
			name:       "secrets:write broad",
			caps:       []string{"secrets:write"},
			readGlobs:  nil,
			writeGlobs: []string{},
		},
		{
			name:       "secrets:write:<glob>",
			caps:       []string{"secrets:write:cache_*"},
			readGlobs:  nil,
			writeGlobs: []string{"cache_*"},
		},
		{
			name:       "both read and write globs",
			caps:       []string{"secrets:read:api_*", "secrets:write:cache_*"},
			readGlobs:  []string{"api_*"},
			writeGlobs: []string{"cache_*"},
		},
		{
			name:       "multiple read globs",
			caps:       []string{"secrets:read:api_*", "secrets:read:db_*"},
			readGlobs:  []string{"api_*", "db_*"},
			writeGlobs: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHost(plugins.Manifest{Name: "test", Capabilities: tc.caps}, "/tmp", nil)
			if tc.wantNil {
				if h.Secrets != nil {
					t.Errorf("expected Secrets nil, got %+v", h.Secrets)
				}
				return
			}
			if h.Secrets == nil {
				t.Fatal("expected Secrets non-nil")
			}
			if tc.readGlobs != nil {
				if len(h.Secrets.ReadGlobs) != len(tc.readGlobs) {
					t.Errorf("ReadGlobs = %v, want %v", h.Secrets.ReadGlobs, tc.readGlobs)
				} else {
					for i, g := range tc.readGlobs {
						if h.Secrets.ReadGlobs[i] != g {
							t.Errorf("ReadGlobs[%d] = %q, want %q", i, h.Secrets.ReadGlobs[i], g)
						}
					}
				}
			}
			if tc.writeGlobs != nil {
				if len(h.Secrets.WriteGlobs) != len(tc.writeGlobs) {
					t.Errorf("WriteGlobs = %v, want %v", h.Secrets.WriteGlobs, tc.writeGlobs)
				} else {
					for i, g := range tc.writeGlobs {
						if h.Secrets.WriteGlobs[i] != g {
							t.Errorf("WriteGlobs[%d] = %q, want %q", i, h.Secrets.WriteGlobs[i], g)
						}
					}
				}
			}
		})
	}
}

// --- Integration: StoreGet/Put/List via SecretsAccess + backing Store ---

func makeSecretsStore(t *testing.T) (*secrets.Store, string) {
	t.Helper()
	dir := t.TempDir()
	store := secrets.NewStore(dir)
	return store, dir
}

func putSecret(t *testing.T, store *secrets.Store, stateDir, name, value string) {
	t.Helper()
	// Write directly via Store.Put (which handles secrets/ subdir creation).
	if err := store.Put(name, []byte(value)); err != nil {
		t.Fatalf("put secret %q: %v", name, err)
	}
	// Store.Put writes to <stateDir>/secrets/<name>; fix permissions.
	p := filepath.Join(stateDir, "secrets", name)
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatalf("chmod secret %q: %v", name, err)
	}
}

func TestSecretsAccess_Integration_ReadAllowed(t *testing.T) {
	store, stateDir := makeSecretsStore(t)
	putSecret(t, store, stateDir, "api_token", "tok123")

	var events []SecretsAuditEvent
	sa := &SecretsAccess{
		Store:      store,
		ReadGlobs:  []string{"api_*"},
		PluginName: "myplugin",
		AuditEmitter: func(ev SecretsAuditEvent) {
			events = append(events, ev)
		},
	}

	if !sa.CanRead("api_token") {
		t.Fatal("expected CanRead true for api_token with api_* glob")
	}
	val, err := store.Get("api_token")
	if err != nil {
		t.Fatalf("store.Get: %v", err)
	}
	sa.Audit(SecretsAuditEvent{Plugin: sa.PluginName, Op: "get", Secret: "api_token", Allowed: true})
	if string(val) != "tok123" {
		t.Errorf("got %q, want tok123", val)
	}
	if len(events) != 1 || !events[0].Allowed {
		t.Errorf("expected one allowed audit event, got %+v", events)
	}
}

func TestSecretsAccess_Integration_ReadDenied(t *testing.T) {
	store, stateDir := makeSecretsStore(t)
	putSecret(t, store, stateDir, "db_password", "s3cr3t")

	var events []SecretsAuditEvent
	sa := &SecretsAccess{
		Store:      store,
		ReadGlobs:  []string{"api_*"},
		PluginName: "myplugin",
		AuditEmitter: func(ev SecretsAuditEvent) {
			events = append(events, ev)
		},
	}

	if sa.CanRead("db_password") {
		t.Fatal("expected CanRead false for db_password with api_* glob")
	}
	sa.Audit(SecretsAuditEvent{Plugin: sa.PluginName, Op: "get", Secret: "db_password", Allowed: false, Reason: "glob mismatch"})
	if len(events) != 1 || events[0].Allowed || events[0].Secret != "db_password" {
		t.Errorf("expected one denied audit event, got %+v", events)
	}
}

func TestSecretsAccess_Integration_NoCapability(t *testing.T) {
	// h.Secrets is nil — every call should return denial.
	h := NewHost(plugins.Manifest{Name: "nocap"}, "/tmp", nil)
	if h.Secrets != nil {
		t.Fatal("expected Secrets nil")
	}
}

func TestSecretsAccess_Integration_WriteDenied(t *testing.T) {
	store, _ := makeSecretsStore(t)
	var events []SecretsAuditEvent
	sa := &SecretsAccess{
		Store:      store,
		WriteGlobs: []string{"cache_*"}, // only cache_* writable
		PluginName: "myplugin",
		AuditEmitter: func(ev SecretsAuditEvent) {
			events = append(events, ev)
		},
	}

	if sa.CanWrite("api_token") {
		t.Fatal("expected CanWrite false for api_token with cache_* glob")
	}
	sa.Audit(SecretsAuditEvent{Plugin: sa.PluginName, Op: "put", Secret: "api_token", Allowed: false, Reason: "glob mismatch"})
	if len(events) != 1 || events[0].Allowed {
		t.Errorf("expected denied event, got %+v", events)
	}
}

func TestSecretsAccess_Integration_BroadRead(t *testing.T) {
	store, stateDir := makeSecretsStore(t)
	putSecret(t, store, stateDir, "anything", "val")

	sa := &SecretsAccess{
		Store:      store,
		ReadGlobs:  nil, // broad
		PluginName: "myplugin",
	}
	if !sa.CanRead("anything") {
		t.Error("broad read should allow any name")
	}
	if !sa.CanRead("db_password") {
		t.Error("broad read should allow db_password")
	}
}
