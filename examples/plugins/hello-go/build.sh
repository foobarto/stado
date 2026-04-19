#!/usr/bin/env bash
# build.sh — compile main.go to plugin.wasm and re-sign the manifest.
# Run from examples/plugins/hello-go/.
#
# Prerequisites:
#   - Go 1.24+ on $PATH (1.25+ recommended; earlier versions lack
#     `-buildmode=c-shared` for wasip1)
#   - stado on $PATH OR pass STADO=/path/to/stado
#   - hello-go-demo.seed in the current directory
#     (generate once with `stado plugin gen-key hello-go-demo.seed`)
#
# Output: plugin.wasm, plugin.manifest.json, plugin.manifest.sig
# (all gitignored — regenerate via this script).

set -euo pipefail

STADO="${STADO:-stado}"

if [[ ! -f hello-go-demo.seed ]]; then
  echo "hello-go-demo.seed not found. Generate it with:" >&2
  echo "  $STADO plugin gen-key hello-go-demo.seed" >&2
  exit 1
fi

echo "→ seeding plugin.manifest.json from template"
cp plugin.manifest.template.json plugin.manifest.json

echo "→ compiling main.go (GOOS=wasip1 -buildmode=c-shared)"
rm -f plugin.wasm
GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
echo "  → plugin.wasm ($(stat -c '%s bytes' plugin.wasm))"

echo "→ signing plugin.manifest.json"
"$STADO" plugin sign plugin.manifest.json --key hello-go-demo.seed
