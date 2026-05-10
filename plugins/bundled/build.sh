#!/usr/bin/env bash
set -euo pipefail

# Build script for the wasm plugins bundled INTO the stado binary.
#
# Sources live next to this script (one subdirectory per plugin); the
# host-side embed.FS that picks them up at compile time still lives at
# internal/plugins/bundled/wasm/. //go:embed only sees siblings of the
# importing Go file, so the compiled artefacts have to land there even
# though the sources moved here.

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT"
REPO_ROOT="$(cd "$ROOT/../.." && pwd)"
WASM_OUT="$REPO_ROOT/internal/plugins/bundled/wasm"

GO_BIN="${GO:-go}"
if ! command -v "$GO_BIN" >/dev/null 2>&1; then
  echo "go toolchain not found on PATH (set GO or PATH before running $0)" >&2
  exit 1
fi

TOOLS=(
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
  web
  dns
)

# EP-no-internal-tools renamed modules: rebuild from a renamed source
# dir so the wire-form output name matches the registered tool.
EP38_RENAMED=(
  "ast_grep:astgrep"
)

# Bundled wasm plugins beyond the EP-0038b core.
EXTRA_WASM=(
  session_search
)

tmpdir="$(mktemp -d "$ROOT/.wasm-build.XXXXXX")"
trap 'rm -rf "$tmpdir"' EXIT

mkdir -p "$WASM_OUT"

for tool in "${TOOLS[@]}"; do
  echo "building ${tool}.wasm"
  GOOS=wasip1 GOARCH=wasm "$GO_BIN" build -buildmode=c-shared -o "$tmpdir/${tool}.wasm" "./${tool}"
done

for tool in "${EP38_TOOLS[@]}"; do
  echo "building ${tool}.wasm (ep-0038b)"
  GOOS=wasip1 GOARCH=wasm "$GO_BIN" build -buildmode=c-shared -o "$tmpdir/${tool}.wasm" "./${tool}"
done

for entry in "${EP38_RENAMED[@]}"; do
  srcdir="${entry%%:*}"
  outname="${entry##*:}"
  echo "building ${outname}.wasm (ep-0038b, from ${srcdir}/)"
  GOOS=wasip1 GOARCH=wasm "$GO_BIN" build -buildmode=c-shared -o "$tmpdir/${outname}.wasm" "./${srcdir}"
done

for tool in "${EXTRA_WASM[@]}"; do
  echo "building ${tool}.wasm"
  GOOS=wasip1 GOARCH=wasm "$GO_BIN" build -buildmode=c-shared -o "$tmpdir/${tool}.wasm" "./${tool}"
done

echo "building auto-compact.wasm"
(
  cd "$ROOT/auto-compact"
  GOOS=wasip1 GOARCH=wasm "$GO_BIN" build -buildmode=c-shared -o "$tmpdir/auto-compact.wasm"
)

rm -f "$WASM_OUT"/*.wasm
mv "$tmpdir"/*.wasm "$WASM_OUT/"
