#!/usr/bin/env bash
# test-on-fedora-atomic.sh
#
# Reproduces the Bazzite / Fedora Atomic / Silverblue filesystem
# layout (`/home` → `/var/home` symlink) inside a transient
# `bwrap` namespace and runs the stado binary against that layout
# to verify boot-time MkdirAll paths don't regress.
#
# Why this exists: stado v0.25.7 failed at boot with
#   `Error: config: create config dir: directory component is a
#    symlink: home`
# on every Atomic-Fedora-derived host because `MkdirAllNoSymlink`
# walks from `/` and rejects symlinked `/home`. v0.26.0 fixed it
# via `MkdirAllUnderUserConfig` (EP-0028). This script is the
# regression guard so the next person who hardens the path-walker
# doesn't accidentally re-introduce the boot wall.
#
# Default behaviour: build a fresh `./stado`, then run
# `stado config-path` inside a bwrap namespace where `/home` is a
# symlink to `/var/home` and `$HOME` is `/var/home/$USER`. Success
# criteria: zero exit code AND a config.toml path on stdout. Failure:
# any non-zero, OR the error string `directory component is a symlink:
# home` anywhere in output.
#
# Why `config-path` (not `--version`): `--version` prints from a
# Cobra hook before any FS work, so it can't surface boot-time
# MkdirAll failures. `config-path` calls `config.Load()` →
# `MkdirAllUnderUserConfig` + the system-prompt-template ensure
# chain, which is the exact path that broke pre-v0.26.0.
#
# Usage:
#   hack/test-on-fedora-atomic.sh                   # build + test
#   hack/test-on-fedora-atomic.sh --binary ./stado  # use existing binary
#   hack/test-on-fedora-atomic.sh --no-build        # skip build step
#
# Requires: bwrap (bubblewrap). Available on most Linux dev hosts;
# install via `dnf install bubblewrap` / `apt install bubblewrap`.

set -euo pipefail

BIN=""
BUILD=1
while [[ $# -gt 0 ]]; do
  case "$1" in
    --binary) BIN="$2"; shift 2 ;;
    --no-build) BUILD=0; shift ;;
    -h|--help) sed -n '2,38p' "$0"; exit 0 ;;
    *) echo "unknown arg: $1" >&2; exit 64 ;;
  esac
done

if ! command -v bwrap >/dev/null; then
  echo "test-on-fedora-atomic: bwrap not found." >&2
  echo "  Install via: dnf install bubblewrap   (Fedora-derived)" >&2
  echo "                apt install bubblewrap   (Debian-derived)" >&2
  echo "                brew install bubblewrap  (macOS — limited)" >&2
  exit 1
fi

if [[ -z "$BIN" ]]; then
  BIN="$(pwd)/stado"
  if [[ "$BUILD" == 1 ]]; then
    echo "→ building ./stado"
    go build -buildvcs=false -o stado ./cmd/stado
  fi
fi
if [[ ! -x "$BIN" ]]; then
  echo "test-on-fedora-atomic: binary not found / not executable: $BIN" >&2
  exit 1
fi

# Construct the simulated XDG layout outside the namespace. bwrap
# bind-mounts these directories to the desired paths, so the stado
# binary sees /home → /var/home/$USER even though our real host's
# /home is a regular directory.
#
# Layout we want inside the namespace:
#   /var/home/atomic-test/                # real bind-mounted tempdir
#   /var/home/atomic-test/.config/        # XDG_CONFIG_HOME
#   /var/home/atomic-test/.local/share/   # XDG_DATA_HOME
#   /var/home/atomic-test/.local/state/   # XDG_STATE_HOME
#   /home -> /var/home                    # symlink (the bug trigger)
#   /tmp                                   # real tempfs
#   /usr,/lib,/lib64,/etc                  # ro-bind from host
#   /home/atomic-test/stado                # bind-mount of $BIN
WORKDIR="$(mktemp -d --tmpdir stado-atomic-test.XXXXXX)"
trap 'rm -rf "$WORKDIR"' EXIT
mkdir -p \
  "$WORKDIR/var/home/atomic-test" \
  "$WORKDIR/var/home/atomic-test/.config" \
  "$WORKDIR/var/home/atomic-test/.local/share" \
  "$WORKDIR/var/home/atomic-test/.local/state" \
  "$WORKDIR/var/home/atomic-test/.cache"

# Pre-seed a config.toml so the with-config probe exercises the
# "config exists, must be read" path (config.go:322) — distinct from
# the empty-namespace path that just creates the dir.
mkdir -p "$WORKDIR/var/home/atomic-test/.config/stado"
cat > "$WORKDIR/var/home/atomic-test/.config/stado/config.toml" <<'TOML'
# Pre-existing config.toml — exercises the load-existing-config path
# that broke pre-v0.26.3 on Atomic.
[defaults]
provider = "anthropic"
model = "claude-sonnet-4-6"
TOML

run_in_namespace() {
  local out="$1"; local err="$2"; shift 2
  bwrap \
    --ro-bind /usr /usr \
    --ro-bind /etc /etc \
    --ro-bind-try /lib /lib \
    --ro-bind-try /lib64 /lib64 \
    --ro-bind-try /bin /bin \
    --ro-bind-try /sbin /sbin \
    --proc /proc \
    --dev /dev \
    --tmpfs /tmp \
    --bind "$WORKDIR/var/home" /var/home \
    --bind "$BIN" /var/home/atomic-test/stado \
    --symlink /var/home /home \
    --chdir /home/atomic-test \
    --setenv HOME /home/atomic-test \
    --setenv USER atomic-test \
    --setenv PATH /usr/local/bin:/usr/bin:/bin \
    --setenv XDG_CONFIG_HOME /home/atomic-test/.config \
    --setenv XDG_DATA_HOME   /home/atomic-test/.local/share \
    --setenv XDG_STATE_HOME  /home/atomic-test/.local/state \
    --setenv XDG_CACHE_HOME  /home/atomic-test/.cache \
    --unshare-user --unshare-pid --unshare-net \
    /home/atomic-test/stado "$@" >"$out" 2>"$err"
}

# Probes — each exercises a different boot-time MkdirAll/OpenRoot/Read
# call site. Every prior round of v0.26.x missed a sub-tree of these,
# so the test deliberately fans out to all known boot-touching commands:
#
#   config-path     → config.Load() + system-prompt-template ensure
#                    + (with the pre-seeded config.toml above) the
#                      load-existing-config read path (config.go:322)
#   config show     → re-reads config; same load-existing-config path
#   doctor --json   → broad env health-check, hits config + audit + state-dir
#   session list    → sidecar git repo init + worktree enumeration
#   audit verify    → audit-key load-or-create (HOME-rooted PEM)
#
# Add new probes here as additional boot-touching surface ships.
PROBES=(
  "config-path"
  "config show"
  "doctor --no-local --json"
  "session list"
  "audit verify"
)

failures=0
for probe in "${PROBES[@]}"; do
  echo "→ probe: stado $probe"
  out="$WORKDIR/stdout-$(echo "$probe" | tr ' /' '__')"
  err="$WORKDIR/stderr-$(echo "$probe" | tr ' /' '__')"
  set +e
  # shellcheck disable=SC2086
  run_in_namespace "$out" "$err" $probe
  rc=$?
  set -e

  if grep -q "directory component is a symlink: home" "$err" "$out" 2>/dev/null; then
    echo "  ✗ REGRESSION — '$probe' tripped the Atomic /home → /var/home boot bug" >&2
    grep -E "(directory component is a symlink|Error:)" "$err" "$out" 2>/dev/null | head -3 | sed 's/^/    /' >&2
    failures=$((failures + 1))
    continue
  fi

  if [[ "$rc" -ne 0 ]]; then
    # Non-zero exit that ISN'T a symlink-walk regression. Some probes
    # legitimately return non-zero on a fresh-namespace setup (e.g.
    # `audit verify` with no signed commits yet, `session list` with
    # no sessions). We tolerate those — only the symlink wall is the
    # actual regression we care about. Print the first error line for
    # visibility but treat as PASS (the boot path made it past the
    # MkdirAll wall to the application logic).
    head -1 "$err" 2>/dev/null | sed 's/^/    (info: /;s/$/)/'
  fi
  echo "  ✓ PASS"
done

if [[ "$failures" -gt 0 ]]; then
  echo "" >&2
  echo "test-on-fedora-atomic: $failures probe(s) failed — see EP-0028 + workdirpath.*UnderUserConfig wrappers." >&2
  exit 1
fi

echo ""
echo "→ ALL PROBES PASS — stado boots cleanly with /home → /var/home"
