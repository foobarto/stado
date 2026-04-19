#!/usr/bin/env bash
# build.sh — compile main.go to plugin.wasm and re-sign the manifest.
# Mirrors the pattern from examples/plugins/auto-compact/.
#
# Prerequisites:
#   - Go 1.24+ on $PATH
#   - stado on $PATH (or STADO=/path/to/stado)
#   - session-recorder-demo.seed in CWD
#     (one-time: `stado plugin gen-key session-recorder-demo.seed`)

set -euo pipefail

STADO="${STADO:-stado}"

if [[ ! -f session-recorder-demo.seed ]]; then
  echo "session-recorder-demo.seed not found. Generate with:" >&2
  echo "  $STADO plugin gen-key session-recorder-demo.seed" >&2
  exit 1
fi

echo "→ seeding plugin.manifest.json from template"
cp plugin.manifest.template.json plugin.manifest.json

echo "→ compiling main.go (GOOS=wasip1 -buildmode=c-shared)"
rm -f plugin.wasm
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
echo "  → plugin.wasm ($(stat -c '%s bytes' plugin.wasm))"

echo "→ signing plugin.manifest.json"
"$STADO" plugin sign plugin.manifest.json --key session-recorder-demo.seed
