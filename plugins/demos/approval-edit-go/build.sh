#!/usr/bin/env bash
set -euo pipefail

STADO="${STADO:-stado}"
GO_BIN="${GO:-go}"
SEED="${SEED:-approval-edit-go.seed}"

if ! command -v "$GO_BIN" >/dev/null 2>&1; then
  echo "go toolchain not found on PATH (set GO or PATH before running $0)" >&2
  exit 1
fi

if [[ ! -f "$SEED" ]]; then
  echo "$SEED not found. Generate with:" >&2
  echo "  $STADO plugin gen-key $SEED" >&2
  exit 1
fi

echo "→ seeding plugin.manifest.json from template"
cp plugin.manifest.template.json plugin.manifest.json

echo "→ compiling main.go (GOOS=wasip1 -buildmode=c-shared)"
rm -f plugin.wasm
GOOS=wasip1 GOARCH=wasm "$GO_BIN" build -buildmode=c-shared -o plugin.wasm .
echo "  → plugin.wasm ($(stat -c '%s bytes' plugin.wasm 2>/dev/null || stat -f '%z bytes' plugin.wasm))"

echo "→ signing plugin.manifest.json"
"$STADO" plugin sign plugin.manifest.json --key "$SEED"
