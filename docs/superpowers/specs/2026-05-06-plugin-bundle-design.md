# `stado plugin bundle` — append wasm plugins to the stado binary

**Status:** approved 2026-05-06; awaiting writing-plans pass.
**Author:** Bartosz Ptaszynski (with brainstorming assistance).
**Branch:** `feat/plugin-bundle` (off `main` after the plugin-dev-watch-mode merge).

## Problem

A power-user wants to ship "stado + my preferred plugins" as a single
self-contained binary — for offline work, for distributing a custom
preset to teammates, for security-research bundles that include
specific recon/payload plugins out of the box.

Constraints from the user discussion:
- The plugins are **already compiled to wasm**. They may be third-
  party blobs the user downloaded, not something the user can rebuild.
- The user **does not necessarily have a Go development environment**
  set up. A bundling workflow that requires `go build` defeats the
  whole point.
- Bundling should be reversible — operator should be able to start
  from a vanilla stado, customize it, then strip back to vanilla.

## Locked decisions

From the 2026-05-06 brainstorming session:

| # | Decision | Rationale |
|---|---|---|
| Q1 | **No `go build`.** Bundling is a binary-manipulation tool: copy source → append payload → done. | Removes Go-toolchain requirement. Bundling completes in ~100ms regardless of plugin count. |
| Q2 | **Append to the binary tail; runtime detects via trailing magic.** No source-tree code generation, no `embed.FS`. `internal/userbundled/` contains only the runtime-detection code; the "custom-ness" is purely a runtime property of the binary's bytes. | One source-tree shape regardless of whether the binary has a payload. Reusable across vanilla and customized binaries. |
| Q3 | **Both CLI args and `--from-file=bundle.toml`.** Bare-name plugin IDs resolve via the just-landed `runtime.ResolveInstalledPluginDir`. | One-off + reproducible team builds. |
| Q4 | **Two-level trust verification, with explicit runtime escape hatch.** (1) Bundle-time verifies each plugin against the operator's trust store. (2) Bundle-time signs the whole payload-body sha256 with a bundling key (ephemeral by default, persistent via `--bundling-key=path/to/seed`). (3) Runtime verifies the bundler's payload signature FIRST (catches any post-bundle tampering across entries), THEN per-entry author signatures. `--allow-unsigned` skips bundle-time per-plugin verification (the bundler signature still seals the result). (4) **Runtime skip:** `--unsafe-skip-bundle-verify` (or `STADO_UNSAFE_SKIP_BUNDLE_VERIFY=1`) bypasses BOTH runtime checks for diagnostics; loud stderr warning; `[unsafe-skip-verify]` marker in `--version`. | Per-entry sig defends against per-entry tampering but not whole-entry substitution; the bundler signature locks the payload to exactly what was bundled. The verbose `--unsafe-skip-bundle-verify` name + permanent version-string marker make it operationally obvious when verification was bypassed. |
| Q5 | **Deterministic payload.** Plugins sorted by name. No timestamps. Byte-identical output for the same input + source binary. | Operators can diff bundled binaries; teams can sign bundles. |
| Q6 | **`stado --version` shows `(custom: N plugins)` when payload present.** New `stado bundle info` parses + lists. | Discoverability + structured introspection. |

Strip subcommand (`stado plugin bundle --strip <binary> --out=...`) is included in v0 — it's ~30 lines and round-trips cleanly with bundle.

## Architecture

### Payload format

Appended to the end of the stado binary:

```
<existing binary bytes>
<STADO_BUNDLE_v1 magic, 16 bytes>
<payload-body-bytes>
<bundler-pubkey, 32 bytes>          ← Ed25519 pubkey of the bundling identity
<bundler-sig, 64 bytes>             ← Ed25519 sig over sha256(payload-body) using that key
<payload-size uint64 LE, 8 bytes>   ← size of payload-body + bundler-pubkey + bundler-sig
<STADO_BUNDLE_END magic, 16 bytes>
```

`STADO_BUNDLE_v1` and `STADO_BUNDLE_END` are 16-byte ASCII magics
chosen so they don't appear in normal Go binaries (they're not
valid UTF-8 strings the compiler would emit).

**The bundler signature is the outer trust anchor.** At runtime,
stado verifies it FIRST against the embedded bundler-pubkey before
parsing entries. Any tampering with payload-body — even substituting
a complete entry with attacker-signed material — invalidates this
signature and stado refuses to load the bundle.

The bundler key has two modes:
- **Ephemeral** (default): each `bundle` invocation generates a fresh
  Ed25519 keypair, signs the payload, embeds the pubkey, discards
  the privkey. The bundle is self-attesting; tampering is detectable
  but the bundle has no persistent identity.
- **Persistent** (`--bundling-key=path/to/seed`): operator supplies
  a seed file (created via `stado plugin gen-key`). The same key
  can re-bundle, giving teams a stable bundle identity. `bundle info`
  shows the bundler pubkey fingerprint so teams can confirm
  authorship.

**Payload-body format:**

```
<entry-count uint32 LE, 4 bytes>
<entry 1>
<entry 2>
...
```

**Per-entry format:**

```
<pubkey-len uint16 LE, 2 bytes>  <pubkey-bytes>          ← 32-byte Ed25519 pubkey
<manifest-len uint32 LE, 4 bytes> <manifest-json>         ← UTF-8 JSON
<sig-len uint16 LE, 2 bytes>     <signature-bytes>        ← 64-byte Ed25519 sig
<wasm-len uint32 LE, 4 bytes>    <wasm-bytes>             ← raw wasm
```

All entries are length-prefixed so a corrupted entry doesn't desync
the parser — we either validate the whole payload or reject it.

### Runtime detection — `internal/bundlepayload/`

```go
package bundlepayload

// LoadFromBinary opens the running executable, looks for the
// trailing STADO_BUNDLE_END magic, parses the payload, verifies
// the outer bundler signature against the embedded bundler pubkey,
// then verifies each entry's signature against its embedded
// per-plugin pubkey, and returns the verified entries.
//
// Returns (nil, nil) when no payload is present (vanilla stado).
// Returns an error only when a payload IS present but malformed
// or signature-invalid — operators should never see a stado that
// silently boots with a tampered payload (unless skipVerify is true).
//
// skipVerify is wired from the top-level --unsafe-skip-bundle-verify
// flag / STADO_UNSAFE_SKIP_BUNDLE_VERIFY env var. When true, BOTH
// signature checks are bypassed and a loud stderr warning is
// emitted by the caller; the function itself still parses payload
// structure (so a structurally-corrupt payload still errors).
func LoadFromBinary(skipVerify bool) (Bundle, error)

type Bundle struct {
    BundlerPubkey ed25519.PublicKey  // for `stado bundle info` / --version display
    Entries       []Entry
    SkipVerified  bool                // true when skipVerify was honoured
}

type Entry struct {
    Pubkey   ed25519.PublicKey
    Manifest plugins.Manifest
    Sig      []byte
    Wasm     []byte
}
```

Implementation:
1. `os.Executable()` → open file.
2. Seek to `EOF - 24`. Read 24 bytes.
3. If trailing 16 ≠ `STADO_BUNDLE_END`, return `(nil, nil)` — vanilla.
4. Read uint64 size from bytes [-24..-16].
5. Seek to `EOF - 24 - size - 16`. Expect 16-byte `STADO_BUNDLE_v1` magic. If not present, return `ErrCorrupt`.
6. Read `size` bytes (= payload-body + bundler-pubkey + bundler-sig).
7. Split: last 96 bytes = bundler-pubkey (32) + bundler-sig (64); leading bytes = payload-body.
8. **Verify bundler signature first**: `ed25519.Verify(bundlerPubkey, sha256(payloadBody), bundlerSig)`. If invalid, return `ErrBundlerSigInvalid` — DO NOT parse entries. Tampering anywhere in the payload-body fails this check.
9. Parse entry-count + entries from the verified payload-body.
10. For each entry: `ed25519.Verify(entry.Pubkey, canonicalize(manifest)+entry.Wasm, entry.Sig)`. If verification fails, return `ErrSigInvalid` — refuses individual entries that don't match their claimed author. Note: at this point, the bundler has already attested to the payload's integrity, so per-entry sig failure indicates the operator bundled a corrupt-at-source plugin (a bundler bug or a deliberately-broken bundle).
11. Return verified entries + bundlerPubkey (so callers can show the bundle's identity in `--version` / `bundle info`).

### Runtime registration — `internal/userbundled/`

```go
package userbundled

import (
    "github.com/foobarto/stado/internal/bundledplugins"
    "github.com/foobarto/stado/internal/bundlepayload"
)

// Bundler exposes the verified bundler pubkey to the rest of the
// process (for --version + bundle info displays). Set during init();
// nil if no payload or if loading failed.
var Bundler ed25519.PublicKey

// SkipVerifyApplied is true when the runtime honoured
// --unsafe-skip-bundle-verify / STADO_UNSAFE_SKIP_BUNDLE_VERIFY.
// Surfaced in --version output and bundle info as a marker.
var SkipVerifyApplied bool

func init() {
    skip := os.Getenv("STADO_UNSAFE_SKIP_BUNDLE_VERIFY") == "1"
    // Top-level --unsafe-skip-bundle-verify flag is parsed before
    // cobra dispatch and sets the env var so this init() picks it
    // up regardless of subcommand path.
    bundle, err := bundlepayload.LoadFromBinary(skip)
    if err != nil {
        // Catastrophic — corrupt or tampered payload (and skip is
        // false). Print to stderr and refuse to register any
        // user-bundled plugins; vanilla bundled plugins (auto-compact
        // etc.) still come up via their own init() chain.
        fmt.Fprintf(os.Stderr, "stado: ERROR: user-bundled payload invalid: %v\n", err)
        fmt.Fprintf(os.Stderr, "stado: hint: re-bundle with `stado plugin bundle`, or boot with --unsafe-skip-bundle-verify to bypass (loses tamper-evidence)\n")
        return
    }
    Bundler = bundle.BundlerPubkey
    SkipVerifyApplied = bundle.SkipVerified
    if SkipVerifyApplied {
        fmt.Fprintln(os.Stderr, "stado: WARNING: bundle signature verification skipped via --unsafe-skip-bundle-verify")
    }
    for _, e := range bundle.Entries {
        bundledplugins.RegisterModule(bundledplugins.Info{
            Name:         strings.TrimPrefix(e.Manifest.Name, bundledplugins.ManifestNamePrefix+"-"),
            Version:      e.Manifest.Version,
            Author:       e.Manifest.Author,
            Capabilities: e.Manifest.Capabilities,
            Tools:        toolNames(e.Manifest.Tools),
            // WasmBytes carries the verified payload; bundled
            // plugins.Wasm() must consult this for user-bundled
            // entries (see Component below).
            WasmSource: e.Wasm,
        })
    }
}
```

### `bundledplugins.Info` extension

The existing `Info` struct in `internal/bundledplugins/list.go` is
populated by upstream-shipped plugins via `init()` calls that
reference embed.FS-backed wasm. User-bundled entries don't have
an embed.FS — they have raw bytes from the appended payload.

**Modification:** add a `WasmSource []byte` field to `Info`. When
non-nil, `bundledplugins.Wasm(name)` returns those bytes directly
instead of looking them up in the embed.FS.

```go
type Info struct {
    Name         string
    Version      string
    Author       string
    Capabilities []string
    Tools        []string
    WasmSource   []byte  // NEW: non-nil for user-bundled entries
}

// Existing:
func Wasm(name string) ([]byte, error) {
    info, _, ok := LookupByName(name)
    if !ok { return nil, fmt.Errorf("unknown bundled module: %s", name) }
    if info.WasmSource != nil {
        return info.WasmSource, nil  // user-bundled
    }
    // existing embed.FS path for upstream-shipped
    return wasmFS.ReadFile(...)
}
```

This keeps user-bundled plugins indistinguishable from upstream-
shipped bundled plugins from the runtime's perspective.

### Bundle subcommand — `cmd/stado/plugin_bundle.go`

```
stado plugin bundle [flags] <plugin-id>...
stado plugin bundle [flags] --from-file=bundle.toml
stado plugin bundle --strip --from=<bundled-binary> --out=<vanilla-output>
stado plugin bundle --info --from=<binary>
```

Subcommand structure:
- `pluginBundleCmd` — top-level wrapper for the `bundle` action
- `pluginBundleStripFlag bool` — switches behavior to strip mode
- `pluginBundleInfoFlag bool` — switches behavior to info mode
- `pluginBundleFromFile string` — TOML manifest path
- `pluginBundleAllowUnsigned bool` — skip bundle-time per-plugin signature verification (the bundler signature is always written; this just relaxes the per-plugin trust-store check)
- `pluginBundleAllowShadow bool` — allow tool-name collisions with already-bundled plugins
- `pluginBundleFrom string` — source binary (default: `os.Executable()`)
- `pluginBundleOut string` — output path (default: `<source-name>-custom`)
- `pluginBundleBundlingKey string` — path to a persistent bundling-key seed (default: ephemeral key generated per-invocation)

Bundle-action flow:
1. Resolve plugin IDs to install dirs via `runtime.ResolveInstalledPluginDir`.
2. For each: read manifest, sig, wasm; recover the verifying pubkey from the trust store (look up `manifest.AuthorPubkeyFpr`) or from `<install-dir>/author.pubkey`. Refuse with clear error if neither yields a pubkey AND `--allow-unsigned` is not set.
3. Verify each manifest's signature against its recovered pubkey (skip per-plugin verification if `--allow-unsigned`).
4. Check for tool-name collisions with already-bundled tools (refuse unless `--allow-shadow`).
5. Sort entries by name (deterministic).
6. Compose `payload-body` bytes (entry count + sorted entries).
7. **Bundler key**: if `--bundling-key` set, load seed from path; else generate a fresh ephemeral Ed25519 keypair for this invocation.
8. **Sign the payload-body**: `bundlerSig = ed25519.Sign(bundlerPriv, sha256(payloadBody))`.
9. If `--from` == `--out`, refuse with clear error.
10. Copy `--from` to `--out` (using `io.Copy`, preserving exec perms).
11. Append: `STADO_BUNDLE_v1` magic + payload-body + bundlerPubkey + bundlerSig + size + `STADO_BUNDLE_END` magic.
12. Print `bundled <N> plugins → <out>` plus the bundler-pubkey fingerprint (so operators can record/share the bundle's identity).

Strip-action flow:
1. Open `--from`, look for trailing `STADO_BUNDLE_END` magic.
2. If absent, print "no bundle to strip; <from> is already vanilla" and exit 0.
3. Compute payload-start offset from the trailer's size field.
4. Copy `--from` to `--out` truncated at the payload-start offset.
5. Print `stripped <N> plugins → <out>`.

Info-action flow:
1. Open `--from` (default: running stado), parse payload via the same `bundlepayload.LoadFromBinary` (modified to take an `io.ReaderAt` so it works on arbitrary files).
2. Print: per-plugin name, version, author, fingerprint, tools, wasm size.
3. Exit 0 (or 1 if no payload).

### Pubkey recovery for bundling

The bundling step needs the Ed25519 pubkey for each plugin to embed
alongside the signature. Two sources:

- **Operator's trust store** (`internal/plugins.NewTrustStore(stateDir)`):
  the trust store maps fingerprint → pubkey. Each manifest carries
  `AuthorPubkeyFpr`; the trust store entry for that fingerprint
  yields the raw pubkey bytes.
- **`<install-dir>/author.pubkey`** if present (existing convention from `plugin dev`).

Bundle-time:
1. Compute the fingerprint from the manifest's `AuthorPubkeyFpr`.
2. Look up in trust store. If found, use that pubkey.
3. If not in trust store, fall back to `<install-dir>/author.pubkey` if present.
4. If neither yields a pubkey, refuse unless `--allow-unsigned` is set.
5. Embed pubkey + manifest + sig + wasm.

Runtime trust chain:
1. Verify the bundler signature against the embedded bundler pubkey
   (the outer trust anchor — locks the entire payload-body to what
   the bundler signed).
2. For each entry, re-verify `ed25519.Verify(entry.Pubkey,
   canonicalize(manifest)+wasm, entry.Sig)` — confirms each plugin
   is signed by its claimed author.

Both signatures must verify before any user-bundled plugin is
registered. The runtime never trusts payload bytes without the
outer bundler signature first attesting to them.

### Runtime skip-verify escape hatch

For diagnostics, debugging, or scenarios where an operator
explicitly accepts the risk (e.g. inspecting a bundle whose
bundler pubkey is unknown to the runtime context, or running a
transferred bundle whose authorship can't be independently
verified), stado supports skipping runtime verification:

- **Flag:** `--unsafe-skip-bundle-verify` (top-level flag on
  `stado` itself, applied before any subcommand).
- **Env var equivalent:** `STADO_UNSAFE_SKIP_BUNDLE_VERIFY=1`.

When set:
- Both the bundler signature AND per-entry signatures are
  skipped at runtime.
- The bundled plugins still register normally (trust is YOLO'd).
- A loud warning is printed to stderr at startup:
  `stado: WARNING: bundle signature verification skipped via --unsafe-skip-bundle-verify`
- `stado --version` output gains a `[unsafe-skip-verify]` marker so
  it's obvious in transcripts and logs.

The flag name is deliberately verbose and includes "unsafe" so
operators can't claim they didn't know what they were doing. There
is **no shorter alias.**

### `stado --version` output

Modify `cmd/stado/version.go` (or wherever the version is printed):

```go
func formatVersion() string {
    base := fmt.Sprintf("stado %s", version.Stado)
    // userbundled.init() has already run by the time --version
    // formats; no need to re-parse the payload.
    if userbundled.Bundler != nil {
        fpr := plugins.FingerprintPubkey(userbundled.Bundler)
        n := len(bundledplugins.List()) - upstreamBundledCount()
        base += fmt.Sprintf(" (custom: %d plugins, bundler=%s)", n, fpr[:8])
    }
    if userbundled.SkipVerifyApplied {
        base += " [unsafe-skip-verify]"
    }
    return base
}
```

### TOML bundle manifest format

```toml
# bundle.toml
output = "stado-htb-toolkit"
allow_unsigned = false

[[plugin]]
name = "htb-lab"

[[plugin]]
name = "gtfobins"
# Optional version pin
version = "0.1.0"

[[plugin]]
name = "kerberos"
```

Loading:
- `koanf` (already in deps) reads the file.
- Plugin entries become positional args at the bundle action.
- File-level options (`output`, `allow_unsigned`) become flag overrides if present.

## File map

| Action | Path | Net lines |
|---|---|---|
| Create | `internal/bundlepayload/payload.go` | ~150 |
| Create | `internal/bundlepayload/payload_test.go` | ~150 |
| Create | `internal/userbundled/init.go` | ~80 |
| Create | `internal/userbundled/init_test.go` | ~60 |
| Create | `cmd/stado/plugin_bundle.go` | ~250 |
| Create | `cmd/stado/plugin_bundle_test.go` | ~250 |
| Modify | `internal/bundledplugins/list.go` (add `WasmSource` field) | ~10 |
| Modify | `internal/bundledplugins/wasm.go` (consult `WasmSource`) | ~5 |
| Modify | `cmd/stado/version.go` (custom-bundled suffix + skip-verify marker) | ~15 |
| Modify | `cmd/stado/plugin.go` (register pluginBundleCmd) | ~3 |
| Modify | `cmd/stado/main.go` or root-cmd (top-level `--unsafe-skip-bundle-verify` flag → env var) | ~10 |

Total: ~1050 net (a bit larger than the rough estimate; the round-
trip + signature-recovery + bundler-trust tests pull weight, and
the two-level trust adds ~80 lines vs the single-level model).

## Testing strategy

### Unit — `internal/bundlepayload/`

- **Round-trip:** compose a payload with 3 entries + bundler key → write to a buffer → parse back → bundler sig verifies, all entries identical.
- **EOF detection:** parse a buffer with no trailing magic → returns (nil, nil).
- **Corruption refusal:** flip a byte mid-payload-body → bundler sig verify fails → returns ErrBundlerSigInvalid.
- **Whole-entry substitution refusal:** replace one entry with a self-consistently-signed entry from a different keypair → bundler sig verify fails → ErrBundlerSigInvalid (this is the threat the outer signature defends against).
- **Per-entry signature failure:** corrupt the bundler-sealed payload's per-entry sig (would require regenerating the bundler sig too, but verify the runtime catches both): refuses with ErrSigInvalid.
- **Boundary cases:** zero entries (empty payload), one giant entry (multi-MB wasm), persistent-key vs ephemeral-key bundles.
- **Bundler-key mismatch:** parse a payload signed with key A but provide key B as the verifying pubkey → ErrBundlerSigInvalid.
- **skipVerify=true with corrupt payload:** parse a tampered payload → no error from sig checks, but structural parse still works and entries are returned.
- **skipVerify=true with structurally-broken payload:** parse a truncated payload → still errors (structural validation always happens, only signature checks are skipped).

### Unit — `internal/userbundled/`

- **No-payload init:** test binary has no payload → init() registers nothing, no panic.
- **Valid-payload init (mocked):** test setup that fakes the LoadFromBinary path → confirms entries flow through `bundledplugins.RegisterModule`.

### Integration — `cmd/stado/`

- **Bundle happy path:** use a fixture stado binary + a fixture installed plugin → run bundle command → verify output binary has trailing magic + payload parses + signatures verify.
- **Bundle + run:** bundle a fixture plugin → exec the resulting binary with `tool list` → verify the bundled plugin appears.
- **Bundle + strip round-trip:** bundle → strip → resulting bytes match the source vanilla binary byte-for-byte.
- **Bundle refuses unsigned:** use a plugin not in the trust store, no `--allow-unsigned` → exits non-zero with clear error.
- **Bundle with `--allow-unsigned`:** same scenario succeeds; the embedded pubkey is read from `<install-dir>/author.pubkey` instead of the trust store.
- **Same `--from` and `--out` refused.**
- **Version string:** running a bundled binary's `--version` includes "(custom: N plugins, bundler=<fpr>)".
- **Info action:** parses the trailer correctly on bundled binary; prints "no bundle" on vanilla.
- **Skip-verify happy path:** corrupt a bundled binary's payload, run with `STADO_UNSAFE_SKIP_BUNDLE_VERIFY=1` → boots, prints loud warning, plugins register, `--version` includes `[unsafe-skip-verify]`.
- **Skip-verify refused on structural corruption:** truncate a binary mid-payload, even with skip-verify the bundle fails to load (structural integrity required regardless).
- **Persistent bundler key:** bundle with `--bundling-key=k1.seed` twice → both binaries' `--version` show identical bundler fingerprint (proves stable identity).

### Manual smoke

1. `go build -o /tmp/stado-base ./cmd/stado`
2. `/tmp/stado-base plugin bundle htb-lab gtfobins --out=/tmp/stado-htb`
3. `/tmp/stado-htb --version` → shows `(custom: 2 plugins)`
4. `/tmp/stado-htb tool list | grep htb-lab` → succeeds
5. `/tmp/stado-htb plugin bundle --info` → lists htb-lab, gtfobins
6. `/tmp/stado-htb plugin bundle --strip --from=/tmp/stado-htb --out=/tmp/stado-stripped`
7. `cmp /tmp/stado-base /tmp/stado-stripped` → identical

## Risks + mitigations

- **Risk:** Code-signed binaries (macOS Authenticode, Windows). Appending to a signed binary invalidates the signature.
  - *Mitigation:* documented in spec output. Operators on signed platforms must re-sign their custom binary or accept it unsigned. Linux unaffected.

- **Risk:** Self-modification — operator runs `bundle` from the same binary they want to modify.
  - *Mitigation:* `--from` and `--out` resolve to absolute paths; refuse if equal. The OS prevents modifying a running binary anyway (ETXTBSY); we get a cleaner UX by refusing earlier.

- **Risk:** Trailing magic appears coincidentally in a vanilla stado.
  - *Mitigation:* the magic is 16 bytes of ASCII (`STADO_BUNDLE_END`) — astronomically unlikely to appear in compiled Go code as 16 contiguous bytes. We also validate the size field decodes to a sensible offset before trusting the leading magic.

- **Risk:** Pubkey not in operator's trust store at bundle time but plugin is legitimately signed.
  - *Mitigation:* fall back to reading `<install-dir>/author.pubkey` when present. If still missing, `--allow-unsigned` is the escape hatch — the user takes responsibility for the per-plugin trust, but the bundler signature still seals the payload.

- **Risk:** Whole-entry substitution (attacker replaces (pubkey, manifest, sig, wasm) with their own self-consistently-signed material).
  - *Mitigation:* the outer bundler signature locks the payload-body. Any substitution invalidates `ed25519.Verify(bundlerPubkey, sha256(payloadBody), bundlerSig)`, and stado refuses to load the bundle. The trust chain at runtime is: verify bundler sig → trust the embedded payload-body bytes → verify per-entry author sigs. The chain only breaks if the attacker has the bundler's privkey, which (in ephemeral mode) was discarded after bundle, and (in persistent mode) is the operator's own key.

- **Risk:** Operator can't recover from a tampered or version-mismatched bundle (e.g. binary transferred between machines whose contexts differ; bundle signed by an unknown party).
  - *Mitigation:* `--unsafe-skip-bundle-verify` flag + `STADO_UNSAFE_SKIP_BUNDLE_VERIFY=1` env var bypass runtime verification entirely. Loud stderr warning at startup; permanent `[unsafe-skip-verify]` marker in `--version` output so it's obvious in transcripts. The flag name is verbose by design; no shorter alias.

- **Risk:** Future stado versions break the payload format.
  - *Mitigation:* the magic includes `_v1`. Future versions can add `STADO_BUNDLE_v2` and refuse to load v1 with a clear "rebuild your bundle" error. The payload format is internal — operators rebuild on upgrade.

- **Risk:** Tool-name collision between user-bundled plugins (two bundled plugins both export `fs__read`).
  - *Mitigation:* bundle-time refusal with `--allow-shadow`. The init() ordering of generated entries doesn't disambiguate, so we catch this at bundle time before it matters.

## Out of scope

- Cross-arch bundling (a stado for ARM produced from an x86 source still needs an ARM source binary; wasm payload is portable, but the host binary isn't).
- Plugin update / refresh mechanism (bundle = snapshot; rebuild to update).
- Auto-strip on `plugin install` (operators manage their own custom binaries).
- Modifying the running stado in place (always writes to a new path).
- Multi-version disambiguation in the bundle (one bundle = one active version per plugin name).
- Bundling installed plugins from a different stado state-dir (operator copies them locally first if they want).

## Verification plan

1. `go test ./... -count=1` passes.
2. `go vet ./...` clean.
3. Manual smoke per "Testing strategy" above.
4. Round-trip determinism: bundle the same plugins twice → resulting binaries byte-identical (modulo source binary).
5. Tampering test: bundle, then `dd` overwrite a byte mid-payload → resulting binary refuses to start (or starts with payload-load warning + no user-bundled plugins).

## Handoff (2026-05-06)

- **What shipped:** `stado plugin bundle <ids>... --out=<bin>` (and `--strip`, `--info`) appends already-compiled wasm plugins to the trailing bytes of a stado binary. No Go toolchain needed — bundling is a copy + write, completes in ~100ms for any plugin count. Two-level signature verification: per-plugin author sigs (carried from the operator's installed plugins) + outer bundler sig (ephemeral by default; `--bundling-key=path` for persistent identity). Runtime verifies both before any plugin registers; tampering refuses with a clear stderr hint pointing at `--unsafe-skip-bundle-verify`. 9 commits on `feat/plugin-bundle`.
- **Smoke verified end-to-end** with a real installed plugin (gtfobins-0.1.0 / 3.3 MB wasm): bundle produces a self-contained binary; `--version` shows `(custom: 1 plugins, bundler=<8-char-fpr>)`; `plugin bundle --info` lists contents; `tool list` shows the bundled plugin's tools registered alongside upstream-shipped ones; strip round-trip produces byte-identical output (`cmp` passes); `dd` tampering is caught with `ErrBundlerSigInvalid`; `--unsafe-skip-bundle-verify` bypasses with loud WARNING + permanent `[unsafe-skip-verify]` version-string marker.
- **Tests:** 12 in `internal/bundlepayload/` (encode/decode/append/strip/load round-trips + sig-invalid + skip-verify + structural-corruption), 2 in `internal/userbundled/` (happy path + vanilla-no-op), plus integration tests in `cmd/stado/` (TOML manifest parse + strip round-trip).
- **What surprised me:** The first end-to-end smoke failed with `ErrEntrySigInvalid`. Root cause: the plan's encoder/decoder signed/verified `canonical(manifest)+wasm`, but the actual plugin signature flow signs only `canonical(manifest)` — the wasm binding goes through `manifest.WASMSHA256`. Fixed by: (1) per-entry verification uses `canon` only, (2) a separate `sha256(wasm) == manifest.WASMSHA256` check so wasm tampering is still caught. Test helpers across three files updated to match.
- **What's left:** TUI live-reload of bundled plugins (handled by the unified-registry path; nothing extra needed for bundled plugins specifically). Cross-arch bundling (operator copies a target-arch source binary first). `plugin bundle --update` / `--refresh` (operators rebuild; bundled = snapshot).
- **What to watch:** the `--unsafe-skip-bundle-verify` env var path is detected by walking `os.Args` in `userbundled/init.go` because the package-level init runs before cobra flag parsing. If anyone refactors the flag scan, ensure the env var fallback still works (it's the canonical mechanism for non-CLI contexts like systemd units).
