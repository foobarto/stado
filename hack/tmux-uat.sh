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
#   UAT_ROOT    — temp XDG root for test config/state/data (default mktemp)
#   UAT_PROVIDER — provider name for deterministic submit tests
#                  (default stado-uat-none)
#   UAT_MODEL   — model label for deterministic submit tests
#                 (default uat-model)
#
# Commands:
#   smoke   — spawn, assert the input box renders, clean up.
#   input   — type "hi", confirm it lands in the input box (not echoed
#             to the terminal row below).
#   slash   — press '/', confirm the palette opens.
#   escape  — press Escape, confirm no hang + palette closes.
#   sidebar — confirm the landing view stays sidebar-free, submit one
#             message, then confirm the chat view shows and toggles the
#             sidebar.
#   sessions — press ctrl+x l, confirm the session manager opens.
#   agents  — press ctrl+x a, confirm the agent picker opens.
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
UAT_PROVIDER="${UAT_PROVIDER:-stado-uat-none}"
UAT_MODEL="${UAT_MODEL:-uat-model}"

OWN_UAT_ROOT=0
if [[ -z "${UAT_ROOT:-}" ]]; then
  UAT_ROOT="$(mktemp -d)"
  OWN_UAT_ROOT=1
fi
mkdir -p "$UAT_ROOT/config" "$UAT_ROOT/data" "$UAT_ROOT/state"
cleanup_uat_root() {
  if [[ "$OWN_UAT_ROOT" == "1" ]]; then
    rm -rf "$UAT_ROOT"
  fi
}
trap cleanup_uat_root EXIT

if ! command -v tmux >/dev/null 2>&1; then
  echo "tmux-uat: tmux not installed; skipping" >&2
  exit 0
fi
if [[ ! -x "$STADO_BIN" ]]; then
  echo "tmux-uat: $STADO_BIN not executable (try 'go build -o stado ./cmd/stado')" >&2
  exit 1
fi

log()  { printf '[uat] %s\n' "$*" >&2; }
quote() { printf '%q' "$1"; }
fail() {
  printf '[uat] FAIL: %s\n' "$*" >&2
  tmux capture-pane -t "$SESSION" -p 2>/dev/null | sed 's/^/    /' >&2 || true
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  exit 1
}

start_stado() {
  tmux kill-session -t "$SESSION" 2>/dev/null || true
  tmux new-session -d -s "$SESSION" -x "$TMUX_W" -y "$TMUX_H"
  local cmd
  cmd="TERM=xterm-256color"
  cmd+=" XDG_CONFIG_HOME=$(quote "$UAT_ROOT/config")"
  cmd+=" XDG_DATA_HOME=$(quote "$UAT_ROOT/data")"
  cmd+=" XDG_STATE_HOME=$(quote "$UAT_ROOT/state")"
  cmd+=" STADO_DEFAULTS_PROVIDER=$(quote "$UAT_PROVIDER")"
  cmd+=" STADO_DEFAULTS_MODEL=$(quote "$UAT_MODEL")"
  cmd+=" $(quote "$STADO_BIN")"
  tmux send-keys -t "$SESSION" "$cmd" Enter
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
  # The opencode-style landing view intentionally suppresses the
  # sidebar so the first screen stays quiet. The sidebar should appear
  # once the conversation has content.
  local frame; frame=$(capture)
  if grep -qF "Now" <<<"$frame" || grep -qF "Agent" <<<"$frame"; then
    fail "sidebar rendered on landing view"
  fi
  log "OK: landing view starts without sidebar"

  tmux send-keys -t "$SESSION" "sidebar-probe"
  sleep 0.2
  tmux send-keys -t "$SESSION" Enter
  sleep 0.8
  frame=$(capture)
  if ! grep -qF "Now" <<<"$frame" || ! grep -qF "Agent" <<<"$frame"; then
    fail "sidebar not rendered after first message"
  fi
  log "OK: sidebar renders after chat starts"

  # Toggle off + back on — the flag must not latch.
  tmux send-keys -t "$SESSION" "C-t"; sleep 0.2
  tmux send-keys -t "$SESSION" "C-t"; sleep 0.2
  frame=$(capture)
  if ! grep -qF "Now" <<<"$frame" || ! grep -qF "Agent" <<<"$frame"; then
    fail "sidebar did not come back after ctrl+t toggle"
  fi
  log "OK: sidebar survives ctrl+t round-trip"
  stop_stado
}

cmd_sessions() {
  start_stado
  tmux send-keys -t "$SESSION" C-x l
  sleep 0.3
  assert_contains "Sessions" "session manager header"
  assert_contains "ctrl+r rename" "session manager action hints"
  tmux send-keys -t "$SESSION" Escape
  sleep 0.2
  assert_not_contains "Sessions" "session manager after Esc"
  stop_stado
}

cmd_agents() {
  start_stado
  tmux send-keys -t "$SESSION" C-x a
  sleep 0.3
  assert_contains "Select agent" "agent picker header"
  assert_contains "Plan" "agent picker Plan row"
  tmux send-keys -t "$SESSION" Escape
  sleep 0.2
  assert_not_contains "Select agent" "agent picker after Esc"
  stop_stado
}

cmd_banner() {
  start_stado
  # The startup banner renders only when m.blocks is empty. It's
  # built from unicode block-drawing chars; any of ░▒▓█▀▁▂▃▄▅▆▇
  # signals the banner painted something. If JoinHorizontal's
  # sidebar-height mismatch regresses, the top row of banner gets
  # clipped but the middle/bottom usually still show.
  local frame; frame=$(capture)
  if ! grep -qE '[░▒▓█▀▁▂▃▄▅▆▇▖▗▘▙▚▛▜▝▞▟]' <<<"$frame"; then
    fail "banner not visible (no block chars in pane)"
  fi
  log "OK: banner renders (block chars present)"
  stop_stado
}

cmd_user_card() {
  start_stado
  # Submit a message and confirm the user rail-card renders in the
  # chat area. The current opencode-style message treatment is a
  # flat panel with a colored left rail, not a rounded box.
  tmux send-keys -t "$SESSION" "probe"
  sleep 0.3
  tmux send-keys -t "$SESSION" Enter
  sleep 1
  local frame; frame=$(capture)
  if ! grep -qF "│ probe" <<<"$frame"; then
    fail "user rail card missing submitted text"
  fi
  log "OK: user rail card renders submitted text"
  stop_stado
}

cmd_slash_palette_nav() {
  start_stado
  tmux send-keys -t "$SESSION" "/"
  sleep 0.3
  tmux send-keys -t "$SESSION" Down Down Down
  sleep 0.3
  tmux send-keys -t "$SESSION" Escape
  sleep 0.3
  # After Esc, palette must close AND a subsequent keystroke must
  # reach the input (not get swallowed by stale palette state).
  tmux send-keys -t "$SESSION" "xy"
  sleep 0.3
  local frame; frame=$(capture)
  if ! grep -qF "│ xy" <<<"$frame"; then
    fail "typing after palette-close was swallowed"
  fi
  log "OK: palette nav + close + resume typing"
  stop_stado
}

cmd_help_overlay() {
  start_stado
  tmux send-keys -t "$SESSION" "?"
  sleep 0.4
  assert_contains "Slash commands" "help overlay slash section"
  assert_contains "/budget"        "help overlay lists /budget"
  tmux send-keys -t "$SESSION" "?"
  sleep 0.3
  # Overlay should close and return us to the input pane.
  local frame; frame=$(capture)
  if grep -qF "Slash commands" <<<"$frame"; then
    fail "help overlay did not close on ?-toggle"
  fi
  log "OK: help overlay toggles cleanly"
  stop_stado
}

cmd_mode_toggle() {
  start_stado
  # Tab toggles Plan/Do. Input-box bottom row shows the mode:
  # "Do ·" or "Plan ·".
  local frame; frame=$(capture)
  if ! grep -qF "Do ·" <<<"$frame"; then
    fail "default Do mode indicator missing"
  fi
  tmux send-keys -t "$SESSION" Tab
  sleep 0.3
  frame=$(capture)
  if ! grep -qF "Plan ·" <<<"$frame"; then
    fail "Plan mode indicator missing after Tab"
  fi
  tmux send-keys -t "$SESSION" Tab
  sleep 0.3
  frame=$(capture)
  if ! grep -qF "Do ·" <<<"$frame"; then
    fail "Do mode indicator missing after second Tab"
  fi
  log "OK: Tab toggles Plan/Do"
  stop_stado
}

cmd_input_history() {
  start_stado
  # Type + submit, then press Up to recall the previous message
  # from history.
  tmux send-keys -t "$SESSION" "first-prompt"
  sleep 0.2
  tmux send-keys -t "$SESSION" Enter
  sleep 1
  # Clear any streamed content by not waiting for stream completion —
  # history should work regardless of stream state.
  tmux send-keys -t "$SESSION" Up
  sleep 0.3
  local frame; frame=$(capture)
  if ! grep -qF "first-prompt" <<<"$frame"; then
    fail "Up-arrow history recall missing 'first-prompt'"
  fi
  log "OK: history-up recalls last submitted message"
  stop_stado
}

cmd_all() {
  cmd_smoke
  cmd_input
  cmd_slash
  cmd_escape
  cmd_sidebar
  cmd_sessions
  cmd_agents
  cmd_banner
  cmd_user_card
  cmd_slash_palette_nav
  cmd_help_overlay
  cmd_mode_toggle
  cmd_input_history
  log "all tmux UAT checks green"
}

case "${1:-all}" in
  smoke)       cmd_smoke ;;
  input)       cmd_input ;;
  slash)       cmd_slash ;;
  escape)      cmd_escape ;;
  sidebar)     cmd_sidebar ;;
  sessions)    cmd_sessions ;;
  agents)      cmd_agents ;;
  banner)      cmd_banner ;;
  card)        cmd_user_card ;;
  palette)     cmd_slash_palette_nav ;;
  help)        cmd_help_overlay ;;
  mode)        cmd_mode_toggle ;;
  history)     cmd_input_history ;;
  all)         cmd_all ;;
  *)
    echo "usage: $0 {smoke|input|slash|escape|sidebar|sessions|agents|banner|card|palette|help|mode|history|all}" >&2
    exit 2
    ;;
esac
