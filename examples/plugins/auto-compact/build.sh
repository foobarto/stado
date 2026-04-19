#!/usr/bin/env bash
# build.sh — compile main.go to plugin.wasm and re-sign the manifest.
# Same pattern as the other Go plugins in examples/plugins/.
#
# Prerequisites:
#   - Go 1.24+ on $PATH
#   - stado on $PATH (or STADO=/path/to/stado)
#   - auto-compact-demo.seed in CWD
#     (one-time: `stado plugin gen-key auto-compact-demo.seed`)

set -euo pipefail

STADO="${STADO:-stado}"

if [[ ! -f auto-compact-demo.seed ]]; then
  echo "auto-compact-demo.seed not found. Generate with:" >&2
  echo "  $STADO plugin gen-key auto-compact-demo.seed" >&2
  exit 1
fi

echo "→ seeding plugin.manifest.json from template"
cp plugin.manifest.template.json plugin.manifest.json

echo "→ compiling main.go (GOOS=wasip1 -buildmode=c-shared)"
rm -f plugin.wasm
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
echo "  → plugin.wasm ($(stat -c '%s bytes' plugin.wasm))"

echo "→ signing plugin.manifest.json"
"$STADO" plugin sign plugin.manifest.json --key auto-compact-demo.seed
