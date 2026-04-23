package audit

// EmbeddedMinisignPubkey is the project's release-signing public key,
// compiled into release builds so `stado self-update` can validate the
// release manifest offline and `stado verify --show-builtin-keys` can
// expose the embedded trust roots (airgap-friendly).
//
// Empty by default — release builds seed the real key via ldflags.
// Without that embedded trust root, `stado verify --show-builtin-keys`
// reports "(not pinned)" and `stado self-update` refuses to run.
//
// When a real key replaces "" here, the expected encoding is base64
// of the raw 32-byte Ed25519 public key (same shape as a
// `minisign -G` key file's trusted_comment payload).
//
// Kept as a var (not const) so release builds can seed it via
// `-ldflags "-X github.com/foobarto/stado/internal/audit.EmbeddedMinisignPubkey=..."`
// without editing source, and so tests can swap it for the duration
// of a test case.
var EmbeddedMinisignPubkey = ""

// EmbeddedMinisignKeyID is the signer's 64-bit key id (minisign's own
// anti-mixup identifier). Empty default pairs with the empty pubkey.
// Release builds can seed via ldflags alongside the pubkey.
var EmbeddedMinisignKeyID uint64 = 0
