#!/usr/bin/env bash
# build.sh — compile src/lib.rs to plugin.wasm and re-sign the manifest.
# Run from plugins/examples/hash-id-rust/.
#
# Prerequisites:
#   - rustup with wasm32-unknown-unknown target:
#       rustup target add wasm32-unknown-unknown
#   - stado on $PATH OR pass STADO=/path/to/stado
#   - hash-id-rust-demo.seed in the current directory
#       (generate with `stado plugin gen-key hash-id-rust-demo.seed` once)
#
# Expected output size: ~50-200 KB wasm (no_std, aggressive LTO/strip).
# Compare: Go equivalent (hash plugin, htb-toolkit) ~3.5 MB.

set -euo pipefail

STADO="${STADO:-stado}"

if [[ ! -f hash-id-rust-demo.seed ]]; then
  echo "hash-id-rust-demo.seed not found. Generate it with:" >&2
  echo "  $STADO plugin gen-key hash-id-rust-demo.seed" >&2
  exit 1
fi

if ! rustup target list --installed 2>/dev/null | grep -q wasm32-unknown-unknown; then
  echo "wasm32-unknown-unknown target not installed. Run:" >&2
  echo "  rustup target add wasm32-unknown-unknown" >&2
  exit 1
fi

echo "→ seeding plugin.manifest.json from template"
cp plugin.manifest.template.json plugin.manifest.json

echo "→ compiling src/lib.rs (wasm32-unknown-unknown, release)"
cargo build --target wasm32-unknown-unknown --release 2>&1
cp target/wasm32-unknown-unknown/release/hash_id_rust.wasm plugin.wasm
echo "  → plugin.wasm ($(stat -c '%s bytes' plugin.wasm))"

echo "→ signing plugin.manifest.json"
"$STADO" plugin sign plugin.manifest.json --key hash-id-rust-demo.seed
