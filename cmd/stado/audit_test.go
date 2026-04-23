package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
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
