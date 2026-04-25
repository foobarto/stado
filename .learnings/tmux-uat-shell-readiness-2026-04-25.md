# tmux UAT must wait for shell readiness

- A detached `tmux new-session` can accept `send-keys` before the login
  shell has drawn and is ready to submit a command. The command may appear in
  the pane, but the trailing Enter is lost, leaving the harness to assert
  against an idle shell.
- In `hack/tmux-uat.sh`, wait briefly after `new-session`, send the launch
  command with `send-keys -l`, then send Enter separately.
- When the pane shows the full launch command and a fresh shell prompt but no
  TUI frame, reproduce with a tiny `echo hello` tmux smoke before debugging
  TUI startup itself.
