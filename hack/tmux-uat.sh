#!/usr/bin/env bash
#
# tmux-uat.sh — real-PTY UAT harness for the stado TUI.
#
# Runs ./stado inside a detached tmux session so assertions can be made
# against the actual rendered frame. Complements the in-process teatest
# integration tests by exercising the path through real termios +
# cancelreader — the path where the lone-ESC + OSC-wrapper regressions
# shipped. Example uses:
#
#   # Interactive smoke
#   hack/tmux-uat.sh smoke
#
#   # Feature matrix (used in CI when CI has tmux)
#   hack/tmux-uat.sh all
#
# Env knobs:
#   STADO_BIN   — path to the stado binary (default: ./stado)
#   TMUX_W      — tmux pane width     (default 200)
#   TMUX_H      — tmux pane height    (default 50)
#   SESSION     — tmux session name   (default stado-uat)
#   WAIT        — seconds to wait after spawning stado (default 2)
#
# Commands:
#   smoke   — spawn, assert the input box renders, clean up.
#   input   — type "hi", confirm it lands in the input box (not echoed
#             to the terminal row below).
#   slash   — press '/', confirm the palette opens.
#   escape  — press Escape, confirm no hang + palette closes.
#   sidebar — press ctrl+t twice, confirm the sidebar is visible on the
#             right both before and after the toggles.
#   all     — run every subcommand above.
#
# The script exits nonzero on the first failing assertion and prints
# the tmux pane so the failure mode is visible.

set -euo pipefail

STADO_BIN="${STADO_BIN:-./stado}"
TMUX_W="${TMUX_W:-200}"
TMUX_H="${TMUX_H:-50}"
SESSION="${SESSION:-stado-uat}"
WAIT="${WAIT:-2}"

if ! command -v tmux >/dev/null 2>&1; then
  echo "tmux-uat: tmux not installed; skipping" >&2
  exit 0
fi
if [[ ! -x "$STADO_BIN" ]]; then
  echo "tmux-uat: $STADO_BIN not executable (try 'go build -o stado ./cmd/stado')" >&2
  exit 1
fi

log()  { printf '[uat] %s\n' "$*" >&2; }
fail() {
  printf '[uat] FAIL: %s\n' "$*" >&2
  tmux capture-pane -t "$SESSION" -p 2>/dev/null | sed 's/^/    /' >&2 || true
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  exit 1
}

start_stado() {
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  tmux new-session -d -s "$SESSION" -x "$TMUX_W" -y "$TMUX_H"
  tmux send-keys -t "$SESSION" "TERM=xterm-256color $STADO_BIN" Enter
  sleep "$WAIT"
}
stop_stado() {
  tmux send-keys -t "$SESSION" "C-d" 2>/dev/null || true
  sleep 0.2
  tmux kill-session -t "$SESSION" 2>/dev/null || true
}
capture() { tmux capture-pane -t "$SESSION" -p; }
contains() { capture | grep -qF -- "$1"; }

assert_contains() {
  local needle="$1" label="${2:-$needle}"
  if ! contains "$needle"; then
    fail "expected pane to contain $label"
  fi
  log "OK: pane contains $label"
}
assert_not_contains() {
  local needle="$1" label="${2:-$needle}"
  if contains "$needle"; then
    fail "pane unexpectedly contains $label"
  fi
  log "OK: pane does not contain $label"
}

cmd_smoke() {
  start_stado
  assert_contains "Type a message..." "input placeholder"
  assert_contains "ctrl+p commands"   "status-bar hint"
  stop_stado
}

cmd_input() {
  start_stado
  tmux send-keys -t "$SESSION" "hi"
  sleep 0.3
  # The 'hi' must appear inside the bordered input region (somewhere
  # between the two horizontal borders). If raw mode is broken, the
  # string echoes on the shell prompt line below the status bar.
  local frame; frame=$(capture)
  if ! grep -qF "│ hi" <<<"$frame"; then
    fail "typed 'hi' did not appear inside the input frame"
  fi
  log "OK: 'hi' lands inside input frame"
  stop_stado
}

cmd_slash() {
  start_stado
  tmux send-keys -t "$SESSION" "/"
  sleep 0.3
  assert_contains "Commands" "palette header"
  tmux send-keys -t "$SESSION" "Escape"
  sleep 0.2
  assert_not_contains "Commands" "palette after Esc"
  stop_stado
}

cmd_escape() {
  start_stado
  tmux send-keys -t "$SESSION" "abc"
  sleep 0.2
  tmux send-keys -t "$SESSION" "Escape"
  sleep 0.2
  # The TUI should still respond after Esc. If the lone-ESC regression
  # comes back, the next keystroke is silently dropped.
  tmux send-keys -t "$SESSION" "z"
  sleep 0.2
  assert_contains "abcz" "keystrokes after Escape"
  stop_stado
}

cmd_sidebar() {
  start_stado
  # Sidebar is open by default; the vertical divider pipes (││) and a
  # second right-edge pipe are the tell that the right-column sidebar
  # is actually laid out.
  local frame; frame=$(capture)
  if ! grep -qF "││" <<<"$frame"; then
    fail "sidebar not rendered on startup (missing '││' divider)"
  fi
  log "OK: sidebar rendered on startup"
  # Toggle off + back on — the flag must not latch.
  tmux send-keys -t "$SESSION" "C-t"; sleep 0.2
  tmux send-keys -t "$SESSION" "C-t"; sleep 0.2
  if ! capture | grep -qF "││"; then
    fail "sidebar did not come back after ctrl+t toggle"
  fi
  log "OK: sidebar survives ctrl+t round-trip"
  stop_stado
}

cmd_all() {
  cmd_smoke
  cmd_input
  cmd_slash
  cmd_escape
  cmd_sidebar
  log "all tmux UAT checks green"
}

case "${1:-all}" in
  smoke)   cmd_smoke ;;
  input)   cmd_input ;;
  slash)   cmd_slash ;;
  escape)  cmd_escape ;;
  sidebar) cmd_sidebar ;;
  all)     cmd_all ;;
  *)
    echo "usage: $0 {smoke|input|slash|escape|sidebar|all}" >&2
    exit 2
    ;;
esac
