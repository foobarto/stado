#!/usr/bin/env bash
set -euo pipefail

repo="${STADO_INSTALL_REPO:-foobarto/stado}"
install_dir="${STADO_INSTALL_DIR:-$HOME/.local/bin}"
version="${STADO_INSTALL_VERSION:-latest}"
base_url_override="${STADO_INSTALL_BASE_URL:-}"
dry_run=0

log() {
  printf 'stado-install: %s\n' "$*" >&2
}

die() {
  log "$*"
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || die "missing required command: $1"
}

usage() {
  cat <<'EOF'
Usage: install.sh [--dir PATH] [--repo owner/name] [--version TAG] [--dry-run]

Downloads the matching stado release archive, verifies the signed
checksums.txt manifest with cosign, verifies the chosen archive against
that manifest, and installs the stado binary.

Defaults:
  --dir      ~/.local/bin
  --repo     foobarto/stado
  --version  latest

Environment overrides:
  STADO_INSTALL_DIR
  STADO_INSTALL_REPO
  STADO_INSTALL_VERSION

Testing-only override:
  STADO_INSTALL_BASE_URL
EOF
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dir)
      [[ $# -ge 2 ]] || die "--dir requires a value"
      install_dir="$2"
      shift 2
      ;;
    --repo)
      [[ $# -ge 2 ]] || die "--repo requires a value"
      repo="$2"
      shift 2
      ;;
    --version)
      [[ $# -ge 2 ]] || die "--version requires a value"
      version="$2"
      shift 2
      ;;
    --dry-run)
      dry_run=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

need curl
need cosign
need tar
need install
need awk
need grep

detect_os() {
  case "$(uname -s)" in
    Linux) printf 'linux\n' ;;
    Darwin) printf 'darwin\n' ;;
    *)
      die "install.sh currently supports Linux and macOS only"
      ;;
  esac
}

detect_arch() {
  case "$(uname -m)" in
    x86_64|amd64) printf 'amd64\n' ;;
    arm64|aarch64) printf 'arm64\n' ;;
    *)
      die "unsupported architecture: $(uname -m)"
      ;;
  esac
}

release_base_url() {
  if [[ -n "$base_url_override" ]]; then
    printf '%s\n' "${base_url_override%/}"
    return
  fi
  if [[ "$version" == "latest" ]]; then
    printf 'https://github.com/%s/releases/latest/download\n' "$repo"
    return
  fi
  local tag="$version"
  if [[ "$tag" != v* ]]; then
    tag="v$tag"
  fi
  printf 'https://github.com/%s/releases/download/%s\n' "$repo" "$tag"
}

verify_manifest() {
  cosign verify-blob \
    --certificate "$cert_path" \
    --certificate-identity-regexp "https://github.com/${repo}/.github/workflows/" \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com \
    --signature "$sig_path" \
    "$checksums_path" >/dev/null
}

verify_archive_sha() {
  if command -v sha256sum >/dev/null 2>&1; then
    printf '%s  %s\n' "$archive_sha" "$archive_path" | sha256sum -c - >/dev/null
    return
  fi
  if command -v shasum >/dev/null 2>&1; then
    local got
    got="$(shasum -a 256 "$archive_path" | awk '{print $1}')"
    [[ "$got" == "$archive_sha" ]] || die "sha256 mismatch for $asset_name"
    return
  fi
  die "missing required command: sha256sum or shasum"
}

extract_binary() {
  tar -xzf "$archive_path" -C "$extract_dir"
  binary_path="$(find "$extract_dir" -type f -name stado | head -n1)"
  [[ -n "$binary_path" ]] || die "archive did not contain a stado binary"
}

os_name="$(detect_os)"
arch_name="$(detect_arch)"
base_url="$(release_base_url)"
asset_pattern="_${os_name}_${arch_name}.tar.gz"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

checksums_path="$tmpdir/checksums.txt"
sig_path="$tmpdir/checksums.txt.sig"
cert_path="$tmpdir/checksums.txt.cert"
extract_dir="$tmpdir/extracted"
mkdir -p "$extract_dir"

log "downloading signed manifest from $base_url"
curl -fsSL "$base_url/checksums.txt" -o "$checksums_path"
curl -fsSL "$base_url/checksums.txt.sig" -o "$sig_path"
curl -fsSL "$base_url/checksums.txt.cert" -o "$cert_path"
verify_manifest
log "manifest signature verified"

asset_name="$(awk '{print $2}' "$checksums_path" | grep -F "$asset_pattern" | head -n1 || true)"
[[ -n "$asset_name" ]] || die "no release asset matches ${os_name}/${arch_name}"
archive_sha="$(awk -v name="$asset_name" '$2 == name { print $1; exit }' "$checksums_path")"
[[ -n "$archive_sha" ]] || die "no checksum found for $asset_name"

archive_path="$tmpdir/$asset_name"
log "downloading $asset_name"
curl -fsSL "$base_url/$asset_name" -o "$archive_path"
verify_archive_sha
log "archive checksum verified"

target_path="$install_dir/stado"
if [[ "$dry_run" -eq 1 ]]; then
  log "dry-run: would install $asset_name to $target_path"
  exit 0
fi

extract_binary
mkdir -p "$install_dir"
install -m 0755 "$binary_path" "$target_path"
log "installed $target_path"
if [[ ":$PATH:" != *":$install_dir:"* ]]; then
  log "add $install_dir to PATH to run stado directly"
fi
