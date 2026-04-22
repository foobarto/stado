#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"

GO_BIN="${GO:-go}"
if ! command -v "$GO_BIN" >/dev/null 2>&1; then
  echo "go toolchain not found on PATH (set GO or PATH before running $0)" >&2
  exit 1
fi

TOOLS=(
  approval_demo
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

tmpdir="$(mktemp -d "$ROOT/.wasm-build.XXXXXX")"
trap 'rm -rf "$tmpdir"' EXIT

mkdir -p wasm

for tool in "${TOOLS[@]}"; do
  echo "building ${tool}.wasm"
  GOOS=wasip1 GOARCH=wasm "$GO_BIN" build -buildmode=c-shared -o "$tmpdir/${tool}.wasm" "./modules/${tool}"
done

rm -f wasm/*.wasm
mv "$tmpdir"/*.wasm wasm/
