package audit

// EmbeddedMinisignPubkey is the project's release-signing public key,
// compiled into every stado binary so `stado verify` and `stado
// self-update` can validate releases offline (airgap-friendly).
//
// Empty by default — a real key lands via PR O (Phase 10.3b offline
// minisign ceremony). Until then, any code that depends on a pinned
// pubkey falls back to advisory-only behaviour: sha256 stays the
// integrity proof, minisign verification logs a warning and skips.
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
