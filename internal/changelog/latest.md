## v0.74.1 — shell PTY-UX rethink: read modes, no attach, labeled sessions — 2026-06-14

Reworks the bundled `shell` plugin's persistent-PTY surface so agents actually
reach for it: fewer tools, no ceremony, one obvious "get output" verb (EP-0043).

> Ships the EP-0043 content as **v0.74.1**: the `v0.74.0` tag's release run
> failed in GoReleaser's `windows_amd64` cross-compile (the new `shell.signal`
> name map used Unix-only `syscall.SIG*` constants), so v0.74.0 published no
> artifacts. v0.74.1 is that content plus the cross-platform build fix.

### Plugins / Tools

- **`shell.read` gains a `mode`** — `auto` (default) returns the rendered vt100
  screen when a full-screen program is active (vim/htop/less/installer — the
  terminal switched to the alternate screen buffer), otherwise the raw
  incremental byte stream; `stream` and `screen` force either. The response
  carries a `kind` discriminator (`{kind:"stream",data_b64,n}` |
  `{kind:"screen",text,cols,rows,cursor,title,svg?}`). For an ordinary
  line-oriented shell the default behaves exactly as before.
- **`shell.screenshot` is removed** — folded into `shell.read mode:"screen"`.
  The confusing "screenshot" name (it never produced an image agents could use)
  is gone; the rendered-screen path is the same vt100 render.
- **`shell.attach` / `shell.detach` are removed; no attach step.** `read` /
  `write` / `read_until` work directly by session id — `shell.spawn` then
  `shell.write` just works. This eliminates the "you need to attach first" error
  on the `stado tool run shell.*` one-shot path.
- **`shell.spawn` takes a `description`** ("what this shell is for"), surfaced in
  `shell.list` so sessions are self-identifying and stale ones are easy to spot
  + `shell.destroy`. `shell.list` is broad (lists every session — orphans stay
  visible) and drops the now-meaningless `attached` field. `shell.spawn` also
  documents `buffer_bytes` in its schema (ring size; default 64 KiB).
- **`shell.signal` accepts signal names**, not just numbers — `"SIGINT"`,
  `"TERM"` (case-insensitive, with or without the `SIG` prefix) resolve
  host-side, matching what the tool description always advertised.
- **`mode:"auto"` discards the raw byte backlog when it renders a screen**, so
  after a full-screen program (vim/htop) exits the alternate buffer a later
  `read` returns just the current shell output instead of dumping the whole TUI
  escape stream. Explicit `mode:"screen"` stays a non-draining peek, and the
  drain is skipped while a `read`/`read_until` is actively consuming.

### Migration (clean break, pre-1.0)

- `shell.screenshot` → `shell.read mode:"screen"` (or just `read` + `auto`).
- `shell.attach` / `shell.detach` removed — drop them; `read`/`write` need no
  attach. The `read` response now carries a `kind` field.

### Fixes

- `shell.signal`'s name map no longer breaks the Windows build — the Unix-only
  signal constants moved behind a `//go:build unix` file so all release targets
  (windows/linux/darwin × amd64/arm64) cross-compile. (This is what v0.74.0's
  release run tripped on.)

