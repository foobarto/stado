package plugins

import "fmt"

// revokedFingerprints names plugin-author Ed25519 fingerprints whose
// corresponding *private seeds* were committed to this repo's git history
// before the `.seed` gitignore landed (untracked in v0.51.1, but history
// retains them forever). Anyone with a clone or a mirror has the seeds, so
// they can forge a manifest signature matching these fingerprints.
//
// The trust path refuses to verify any manifest carrying a revoked
// fingerprint, even if the operator has previously trusted it via
// `stado plugin trust <pubkey>`. This is a hard deny — the only resolution
// is for the (former) author to rotate to a new key, sign with that, and
// have operators trust the new fingerprint. See SECURITY.md for the full
// list, rationale, and remediation.
//
// Value is map-of-fingerprint → source filename (so error messages point
// operators at the leak that caused the revocation).
var revokedFingerprints = map[string]string{
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

// IsRevoked reports whether the fingerprint is on the deny-list. The second
// return is the source filename of the leaked seed (empty when not revoked).
func IsRevoked(fpr string) (bool, string) {
	src, ok := revokedFingerprints[fpr]
	return ok, src
}

// errRevoked returns a user-facing error for a revoked fingerprint.
func errRevoked(fpr string) error {
	return fmt.Errorf("author fingerprint %s is revoked: corresponding Ed25519 seed leaked in git history (%s). See SECURITY.md; treat as compromised. The plugin author must rotate keys before manifests under this fingerprint will verify",
		fpr, revokedFingerprints[fpr])
}
