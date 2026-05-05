#!/usr/bin/env bash
set -euo pipefail
# Build the bundled browser plugin (tier 1 HTTP + tier 2 Chrome CDP).
# Requires: Go 1.24+, GOOS=wasip1 target.
cd "$(dirname "$0")"

GO="${GO:-go}"
GOTMPDIR="${GOTMPDIR:-$(mktemp -d)}"
export GOTMPDIR

echo "building browser.wasm"
GOOS=wasip1 GOARCH=wasm "$GO" build \
  -buildmode=c-shared \
  -o browser.wasm \
  .

echo "done: $(du -sh browser.wasm | cut -f1) browser.wasm"
