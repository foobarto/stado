#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

TOOLS=(
  read
  write
  edit
  glob
  grep
  bash
  webfetch
  ripgrep
  ast_grep
  read_with_context
  find_definition
  find_references
  document_symbols
  hover
)

mkdir -p wasm
rm -f wasm/*.wasm

for tool in "${TOOLS[@]}"; do
  echo "building ${tool}.wasm"
  GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o "wasm/${tool}.wasm" "./modules/${tool}"
done
