#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"
REPO_ROOT="$(cd "$ROOT/../.." && pwd)"

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

# EP-0038b: new wasm tool plugins with wire-name exports.
# Output names use the alias (e.g. fs.wasm, shell.wasm).
EP38_TOOLS=(
  fs
  shell
  rg
  agent
)

# EP-0038b readctx replacement (renamed module dir to avoid conflict with
# existing read_with_context module).
EP38_RENAMED=(
  "readctx-ng:readctx"
)

tmpdir="$(mktemp -d "$ROOT/.wasm-build.XXXXXX")"
trap 'rm -rf "$tmpdir"' EXIT

mkdir -p wasm

for tool in "${TOOLS[@]}"; do
  echo "building ${tool}.wasm"
  GOOS=wasip1 GOARCH=wasm "$GO_BIN" build -buildmode=c-shared -o "$tmpdir/${tool}.wasm" "./modules/${tool}"
done

for tool in "${EP38_TOOLS[@]}"; do
  echo "building ${tool}.wasm (ep-0038b)"
  GOOS=wasip1 GOARCH=wasm "$GO_BIN" build -buildmode=c-shared -o "$tmpdir/${tool}.wasm" "./modules/${tool}"
done

for entry in "${EP38_RENAMED[@]}"; do
  srcdir="${entry%%:*}"
  outname="${entry##*:}"
  echo "building ${outname}.wasm (ep-0038b, from modules/${srcdir})"
  GOOS=wasip1 GOARCH=wasm "$GO_BIN" build -buildmode=c-shared -o "$tmpdir/${outname}.wasm" "./modules/${srcdir}"
done

echo "building auto-compact.wasm"
(
  cd "$REPO_ROOT/plugins/default/auto-compact"
  GOOS=wasip1 GOARCH=wasm "$GO_BIN" build -buildmode=c-shared -o "$tmpdir/auto-compact.wasm"
)

rm -f wasm/*.wasm
mv "$tmpdir"/*.wasm wasm/
