#!/usr/bin/env bash
set -euo pipefail

STADO="${STADO:-stado}"

if [[ ! -f browser-demo.seed ]]; then
  echo "browser-demo.seed not found. Generate it with:" >&2
  echo "  $STADO plugin gen-key browser-demo.seed" >&2
  exit 1
fi

echo "→ seeding plugin.manifest.json from template"
cp plugin.manifest.template.json plugin.manifest.json

echo "→ go mod tidy"
GOOS=wasip1 GOARCH=wasm go mod tidy

echo "→ compiling main.go (GOOS=wasip1 -buildmode=c-shared)"
rm -f plugin.wasm
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
echo "  → plugin.wasm ($(stat -c '%s bytes' plugin.wasm))"

echo "→ signing plugin.manifest.json"
"$STADO" plugin sign plugin.manifest.json --key browser-demo.seed
