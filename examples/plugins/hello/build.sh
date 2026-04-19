#!/usr/bin/env bash
# build.sh — compile src/hello.zig to plugin.wasm and re-sign the
# manifest. Run from examples/plugins/hello/.
#
# Prerequisites:
#   - zig on $PATH (tested with 0.15.x)
#   - stado on $PATH OR pass STADO=/path/to/stado
#   - hello-demo.seed in the current directory
#     (generate with `stado plugin gen-key hello-demo.seed` once)
#
# Output:
#   plugin.wasm              — compiled module
#   plugin.manifest.json     — rewritten with computed digest + signer fpr
#   plugin.manifest.sig      — base64 Ed25519 signature

set -euo pipefail

STADO="${STADO:-stado}"

if [[ ! -f hello-demo.seed ]]; then
  echo "hello-demo.seed not found. Generate it with:" >&2
  echo "  $STADO plugin gen-key hello-demo.seed" >&2
  exit 1
fi

echo "→ seeding plugin.manifest.json from template"
cp plugin.manifest.template.json plugin.manifest.json

echo "→ compiling src/hello.zig"
rm -f hello.wasm plugin.wasm
zig build-exe src/hello.zig \
  -target wasm32-freestanding \
  -fno-entry \
  -OReleaseSmall \
  --export=stado_alloc \
  --export=stado_free \
  --export=stado_tool_greet
mv hello.wasm plugin.wasm
echo "  → plugin.wasm ($(stat -c '%s bytes' plugin.wasm))"

echo "→ signing plugin.manifest.json"
"$STADO" plugin sign plugin.manifest.json --key hello-demo.seed
