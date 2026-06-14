package plugins

import (
	"strings"
	"testing"
)

// Exact-membership guard for the hardcoded revocation deny-list.
//
// The existing TestRevokedList_count is a shrink-only guard (len >= 12): it
// catches a deny-list that loses entries, but it cannot catch a SWAP — replace
// one revoked fingerprint with a different (attacker-chosen) one and the count
// stays 12, silently un-revoking a previously-denied compromised key while
// still passing the count test. This file pins the EXACT set of fingerprints
// (by value) that must be reported revoked by the public IsRevoked lookup, so
// any silent removal or substitution is caught even when the count is held.
//
// Mirror of the literal map in revoked.go (fingerprint -> source filename).
// If you legitimately add/remove an entry there, update this expected set and
// SECURITY.md's table together — that synchronized edit is the point.
var revnamecheckExpected = map[string]string{
	"6c48b56f20c9c344": "plugins/examples/browser/browser-demo.seed",
	"65eae6fb74279268": "plugins/examples/encode-zig/encode-zig-demo.seed",
	"5bc3855d455e44c4": "plugins/examples/hello/hello-demo.seed",
	"08aa1288d1af3d9a": "plugins/examples/hello-go/hello-go-demo.seed",
	"28f0fa4d25503211": "plugins/examples/http-session/http-session-demo.seed",
	"6c9bf7180872f90c": "plugins/examples/image-info/image-info-demo.seed",
	"effd536ec1e7eb14": "plugins/examples/ls/ls-demo.seed",
	"f701ee55897ada64": "plugins/examples/mcp-client/mcp-client-demo.seed",
	"45016a163a795f9f": "plugins/examples/persistent-shell/persistent-shell-demo.seed",
	"ff8436c9d0ab8450": "plugins/examples/state-dir-info/state-dir-info-demo.seed",
	"33ecd5793539691c": "plugins/examples/webfetch-cached/webfetch-cached-demo.seed",
	"a3128a188d7af698": "plugins/examples/web-search/web-search-demo.seed",
}

// Every pinned fingerprint must be reported revoked by the public lookup,
// by exact value, and the reported source must name the expected seed file.
// A swap (replacing one of these with an attacker-chosen fpr) flips one of
// these IsRevoked calls to false and fails here, even though count stays 12.
func TestRevoked_exactMembershipReportedRevoked(t *testing.T) {
	for fpr, wantSrc := range revnamecheckExpected {
		rev, src := IsRevoked(fpr)
		if !rev {
			t.Errorf("IsRevoked(%q): expected revoked=true (pinned deny-list entry silently removed/swapped?)", fpr)
			continue
		}
		if src != wantSrc {
			t.Errorf("IsRevoked(%q): source = %q, want %q", fpr, src, wantSrc)
		}
	}
}

// Control: a clearly-not-revoked fingerprint must NOT be reported revoked.
// Guards against a degenerate lookup that returns true for everything (which
// would trivially satisfy the membership assertions above).
func TestRevoked_controlNotRevoked(t *testing.T) {
	controls := []string{
		"deadbeefdeadbeef",
		"1111111111111111",
		"ffffffffffffffff",
	}
	for _, fpr := range controls {
		// Sanity: control must not collide with a real pinned entry.
		if _, pinned := revnamecheckExpected[fpr]; pinned {
			t.Fatalf("test bug: control fingerprint %q is actually a pinned revoked entry", fpr)
		}
		if rev, src := IsRevoked(fpr); rev || src != "" {
			t.Errorf("IsRevoked(%q): expected not-revoked, got rev=%v src=%q", fpr, rev, src)
		}
	}
}

// The live deny-list must equal the pinned set EXACTLY — same length, same
// keys, same source values. This is the strict form of the shrink-guard:
// it catches additions-without-test-update AND removals AND swaps. When the
// count differs the message points the maintainer at the synchronized edit
// (revoked.go + this expected set + SECURITY.md).
func TestRevoked_listMatchesPinnedSetExactly(t *testing.T) {
	if got, want := len(revokedFingerprints), len(revnamecheckExpected); got != want {
		t.Errorf("revokedFingerprints: have %d entries, pinned set has %d — update revoked.go, this test, and SECURITY.md together", got, want)
	}
	// Every live entry must be pinned (catches an un-tested addition or a swap
	// that introduced a fingerprint this test doesn't know about).
	for fpr, src := range revokedFingerprints {
		wantSrc, ok := revnamecheckExpected[fpr]
		if !ok {
			t.Errorf("revokedFingerprints contains un-pinned entry %q (%q) — if intentional, add it to the pinned set + SECURITY.md", fpr, src)
			continue
		}
		if src != wantSrc {
			t.Errorf("revokedFingerprints[%q] = %q, pinned set expects %q", fpr, src, wantSrc)
		}
	}
	// Every pinned entry must be live (catches a silent removal).
	for fpr := range revnamecheckExpected {
		if _, ok := revokedFingerprints[fpr]; !ok {
			t.Errorf("pinned fingerprint %q missing from revokedFingerprints — a revoked key was silently un-revoked", fpr)
		}
	}
}

// No duplicate fingerprints once normalized to lowercase canonical hex. The
// live map can't hold duplicate keys, but two keys differing only in case
// would both normalize to the same canonical fingerprint and inflate the
// count — masking a removal behind a case-variant of an existing entry. This
// asserts each canonical fingerprint appears exactly once.
func TestRevoked_noDuplicateCanonicalFingerprints(t *testing.T) {
	seen := make(map[string]string, len(revokedFingerprints))
	for fpr, src := range revokedFingerprints {
		canon := strings.ToLower(fpr)
		if prevSrc, dup := seen[canon]; dup {
			t.Errorf("duplicate canonical fingerprint %q: entries %q and %q", canon, prevSrc, src)
			continue
		}
		seen[canon] = src
	}
	if got, want := len(seen), len(revokedFingerprints); got != want {
		t.Errorf("distinct canonical fingerprints = %d, total entries = %d (case-variant duplicate present)", got, want)
	}
}
