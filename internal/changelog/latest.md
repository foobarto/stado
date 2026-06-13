## v0.66.0 — per-persona scope + modal vim + ssh-agent passthrough — 2026-06-13

Three user-facing features — per-persona skill/tool/plugin scoping, a modal
vim keybinding schema, and ssh-agent forwarding with SSH-key masking for
sandboxed sessions — plus an agent-facing repository map and a docs quality
pass.

### Security

- **ssh-agent passthrough + SSH-key masking (default-on).** A sandboxed
  session can no longer read the SSH private-key directory (no key
  exfiltration), while git-over-ssh keeps working via the forwarded agent
  socket — the key never enters the sandbox. `sandbox.Policy` gains `Mask`
  (paths shadowed with a `tmpfs`, then `known_hosts` / `config` re-bound
  read-only on top) and `Sockets` (the host `$SSH_AUTH_SOCK` bound in +
  re-exported). Wired into the broker ceiling, the bare TUI (which now
  enforces the ceiling like `stado run`), `mcp-server`, and `daemon`.
  - **Accepted residual:** a forwarded session can sign git operations for
    its lifetime; the key itself is never exposed. The fetch-only,
    approval/taint-gated git-sub-agent (EP-0050 phase 7) is the eventual
    stronger model. `session resume` does not yet enforce the ceiling
    (follow-up).

### TUI

- **Per-persona skills / tools / plugins (additive).** A persona `.md` can
  declare `tools:` / `skills:` / `plugins:` in frontmatter; when the persona
  is active they layer on top of the global surface (extend, never hide).
  Tools and skills re-scope live on a `/persona` switch; background plugins
  apply at launch. The previously-dormant `recommended_tools` field is now
  honored as a tools source.
- **Modal vim keybinding schema.** Opt in with `[keymap] schema = "vim"` for
  NORMAL / INSERT / VISUAL editing of the chat input: motions (`h j k l`,
  `w b e`, `0 ^ $`, `gg G`) with counts, insert-entry (`i a I A o O`), edits
  (`x D C s r`, line-wise `dd cc yy`, operator+motion), an unnamed register
  with `p` / `P`, and visual selection. ESC enters NORMAL (vim-schema-only);
  `Ctrl+G` still interrupts in every mode. Starts in INSERT so it is never a
  launch trap.

### CLI

- **`usage` rejects an inverted time window.** `stado usage --since 1d
  --until 7d` (an inverted window) now errors instead of silently printing a
  reversed-window header with no rows.

### Docs

- **Agent-facing repository map** in the README — a dense source-tree
  navigation packet (subsystem map, surfaces, a "where do I change X?"
  task → location table, and pointers to the EPs / DESIGN / PLAN / `.agent`
  state).
- **Docs quality pass** across 25 `.md` files: corrected stale references to
  match the current code (the all-tools-as-wasm bundled-tool surface in
  DESIGN, the shipped v1-security rollout in PLAN, the plugin ABI docs, the
  `run` / `tui` command refs, the persona config path), fixed broken links,
  and removed decorative emoji. (Flagged for a separate decision: the
  minisign self-update path is documented but not yet wired into the release
  pipeline.)

