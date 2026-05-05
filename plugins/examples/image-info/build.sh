#!/usr/bin/env bash
# build.sh — compile main.go to plugin.wasm and re-sign the manifest.
# Run from plugins/examples/image-info/.
#
# Output: plugin.wasm, plugin.manifest.json, plugin.manifest.sig.

set -euo pipefail

STADO="${STADO:-stado}"

if [[ ! -f image-info-demo.seed ]]; then
  echo "image-info-demo.seed not found. Generate it with:" >&2
  echo "  $STADO plugin gen-key image-info-demo.seed" >&2
  exit 1
fi

echo "→ seeding plugin.manifest.json from template"
cp plugin.manifest.template.json plugin.manifest.json

echo "→ compiling main.go (GOOS=wasip1 -buildmode=c-shared)"
rm -f plugin.wasm
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
echo "  → plugin.wasm ($(stat -c '%s bytes' plugin.wasm))"

echo "→ signing plugin.manifest.json"
"$STADO" plugin sign plugin.manifest.json --key image-info-demo.seed
