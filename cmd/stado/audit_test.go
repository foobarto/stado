package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"

	auditpkg "github.com/foobarto/stado/internal/audit"
	"github.com/foobarto/stado/internal/runtime"
	stadogit "github.com/foobarto/stado/internal/state/git"
)

func TestAuditVerify_NoSessions(t *testing.T) {
	_, _, restore := statsEnv(t)
	defer restore()

	stdout, stderr := captureOutput(t, func() {
		if err := auditVerifyCmd.RunE(auditVerifyCmd, nil); err != nil {
			t.Fatalf("audit verify: %v", err)
		}
	})

	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("expected no stdout for empty verify, got %q", stdout)
	}
	if !strings.Contains(stderr, "(no sessions)") {
		t.Fatalf("expected no sessions message, got %q", stderr)
	}
}

// B8: an explicitly-named unknown id must error, not silently exit 0.
func TestAuditVerify_UnknownIDErrors(t *testing.T) {
	_, _, restore := statsEnv(t)
	defer restore()

	err := auditVerifyCmd.RunE(auditVerifyCmd, []string{"no-such-session-id"})
	if err == nil {
		t.Fatal("expected an error for an unknown explicit session id, got nil (exit 0)")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should say 'not found', got %q", err.Error())
	}
}

// B8 sibling: an explicitly-named unknown id passed to `audit export`
// must error rather than silently exit 0 with empty output — for a
// SIEM-ingestion tool, a typoed/nonexistent session id producing zero
// records under a success exit code is a silent data-loss footgun (the
// operator believes they captured an audit trail when they captured
// nothing). Mirrors TestAuditVerify_UnknownIDErrors. The no-args sweep
// keeps its lenient skip (nothing to export → exit 0 is fine there).
func TestAuditExport_UnknownIDErrors(t *testing.T) {
	_, _, restore := statsEnv(t)
	defer restore()

	err := auditExportCmd.RunE(auditExportCmd, []string{"no-such-session-id"})
	if err == nil {
		t.Fatal("expected an error for an unknown explicit session id, got nil (exit 0)")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should say 'not found', got %q", err.Error())
	}
}

// A2: `audit export` must NOT swallow a genuine git-storage error during
// ref resolution. Earlier the resolve loop did a blanket `continue` on ANY
// ResolveRef error, so a corrupt refs backing-store was misreported as
// "session not found (no tree/trace refs)" — the operator believes the
// session simply has no audit trail when in fact the store is unreadable.
// `audit verify` already classifies plumbing.ErrReferenceNotFound (benign,
// skip) apart from a real storage error (return it); this asserts export
// now mirrors that. Reproduced by corrupting the sidecar's packed-refs so
// go-git's Storer.Reference returns "malformed packed-ref" (a non-NotFound
// error) on lookup of an explicit, valid-format session id.
func TestAuditExport_StorageErrorNotSwallowed(t *testing.T) {
	_, sc, restore := statsEnv(t)
	defer restore()

	// Corrupt the refs backing-store of the sidecar bare repo. go-git
	// consults packed-refs during ref resolution, so a malformed file
	// surfaces a real storage error (not ErrReferenceNotFound).
	if err := os.WriteFile(filepath.Join(sc.Path, "packed-refs"),
		[]byte("garbage not a ref line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := auditExportCmd.RunE(auditExportCmd, []string{"some-session-id"})
	if err == nil {
		t.Fatal("expected a storage error to surface, got nil (swallowed)")
	}
	// It must be the storage error, NOT the benign "not found" misreport.
	if strings.Contains(err.Error(), "not found") {
		t.Fatalf("storage error misreported as 'not found': %q", err.Error())
	}
	if !strings.Contains(err.Error(), "resolve") {
		t.Errorf("expected a resolve-classified storage error, got %q", err.Error())
	}
}

func TestAuditExport_EmitsJSONL(t *testing.T) {
	cfg, sc, restore := statsEnv(t)
	defer restore()

	const id = "audit-export"
	sess, err := stadogit.CreateSession(sc, cfg.WorktreeDir(), id, plumbing.ZeroHash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.CommitToTrace(stadogit.CommitMeta{Tool: "grep", Summary: "export fixture"}); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := auditExportCmd.RunE(auditExportCmd, []string{id}); err != nil {
			t.Fatalf("audit export: %v", err)
		}
	})

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected one exported record, got %d:\n%s", len(lines), out)
	}
	var rec auditpkg.Record
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatalf("exported line is not JSON: %v\n%s", err, out)
	}
	if rec.Ref != string(stadogit.TraceRef(id)) {
		t.Fatalf("record ref = %q, want %q", rec.Ref, stadogit.TraceRef(id))
	}
	if rec.Commit == "" {
		t.Fatal("record commit hash should not be empty")
	}
	if rec.Signed {
		t.Fatal("fixture trace commit should be unsigned")
	}
}

func TestAuditPubkey_PrintsFingerprintAndHex(t *testing.T) {
	cfg, _, restore := statsEnv(t)
	defer restore()

	out := captureStdout(t, func() {
		if err := auditPubkeyCmd.RunE(auditPubkeyCmd, nil); err != nil {
			t.Fatalf("audit pubkey: %v", err)
		}
	})

	priv, err := auditpkg.LoadOrCreateKey(runtime.SigningKeyPath(cfg))
	if err != nil {
		t.Fatal(err)
	}
	pub := priv.Public().(ed25519.PublicKey)
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) != 2 {
		t.Fatalf("expected fingerprint + hex public key, got %q", out)
	}
	if fields[0] != auditpkg.Fingerprint(pub) {
		t.Fatalf("fingerprint = %q, want %q", fields[0], auditpkg.Fingerprint(pub))
	}
	if fields[1] != hex.EncodeToString(pub) {
		t.Fatalf("pubkey hex = %q, want %q", fields[1], hex.EncodeToString(pub))
	}
}
