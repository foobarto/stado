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

// RevokedError returns the user-facing error for a revoked fingerprint.
// Exported because non-plugins packages (internal/runtime's
// verifyPluginOverride, etc.) also need to produce the same consistent
// error message — keeping the deny-list non-bypassable across every
// trust-verification entry point.
//
// Defensive: if called with a non-revoked fpr (a caller bug — they should
// IsRevoked-check first), the message reports the internal error rather
// than falsely claiming revocation with an empty source.
//
// Follows this package's `<stage>:` error-prefix convention so users can
// tell at a glance which stage rejected — `verify:` for the user-facing
// revoked error (matching VerifyManifest / TrustVerified); `plugins:` for
// the internal-error caller-bug message (matching requires.go's
// ParseRequire). Different stages, same convention. (Note: the rest of
// this package uses the singular `plugin:` for many other errors —
// trust.go's parsePubkey, manifest.go's load/parse — so the convention
// isn't perfectly uniform across the file.)
func RevokedError(fpr string) error {
	src, ok := revokedFingerprints[fpr]
	if !ok {
		return fmt.Errorf("plugins: RevokedError called for non-revoked fingerprint %s (internal error — caller should IsRevoked() first)", fpr)
	}
	return fmt.Errorf("verify: author fingerprint %s is revoked — corresponding Ed25519 seed leaked in git history (%s); see SECURITY.md. Treat as compromised; the plugin author must rotate keys before manifests under this fingerprint will verify",
		fpr, src)
}
