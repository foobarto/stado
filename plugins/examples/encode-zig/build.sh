#!/usr/bin/env bash
# build.sh — compile src/encode.zig to plugin.wasm and re-sign manifest.
# Run from plugins/examples/encode-zig/.
#
# Prerequisites:
#   - zig >= 0.13.0 on $PATH
#   - stado on $PATH OR pass STADO=/path/to/stado
#   - encode-zig-demo.seed in the current directory
#     (generate with `stado plugin gen-key encode-zig-demo.seed` once)
#
# Output: plugin.wasm (~3 KB), plugin.manifest.json, plugin.manifest.sig

set -euo pipefail

STADO="${STADO:-stado}"

if [[ ! -f encode-zig-demo.seed ]]; then
  echo "encode-zig-demo.seed not found. Generate it with:" >&2
  echo "  $STADO plugin gen-key encode-zig-demo.seed" >&2
  exit 1
fi

echo "→ seeding plugin.manifest.json from template"
cp plugin.manifest.template.json plugin.manifest.json

echo "→ compiling src/encode.zig (wasm32-freestanding, -OReleaseSmall)"
rm -f encode.wasm plugin.wasm
zig build-exe src/encode.zig \
  -target wasm32-freestanding \
  -fno-entry \
  -OReleaseSmall \
  --export=stado_alloc \
  --export=stado_free \
  --export=stado_tool_encode
mv encode.wasm plugin.wasm
echo "  → plugin.wasm ($(stat -c '%s bytes' plugin.wasm))"

echo "→ signing plugin.manifest.json"
"$STADO" plugin sign plugin.manifest.json --key encode-zig-demo.seed
