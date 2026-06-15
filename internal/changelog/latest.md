## v0.75.0 — repo-config trust boundary + PTY/exec sandbox hardening — 2026-06-15

A security-hardening release closing a cluster of findings around **untrusted
repository content** (the threat model treats repo contents as
attacker-controlled). Most changes tighten what a checked-in `.stado/` can do on
a bare `cd repo && stado`; a few are standalone path-safety / sandbox fixes. No
new features. EP-0044 has the full design.

### Security — repo-config trust boundary (EP-0044)

- **Project `.stado/config.toml` no longer honors operator-domain keys.** A
  repo-committed project config now has these stripped (they belong in your
  user/global config): `[keymap]` (couldn't neutralize the Esc/Ctrl+G interrupt
  or swap the input model), `defaults.persona` + `agent.system_prompt_path`
  (system-prompt injection), `plugins.background` (repo wasm autostart),
  `[acp]` (register_mcp backdoor / inherit_env secret passthrough / max_turns),
  `mcp.providers` + `mcp.servers` (inherit_env + repo-declared subprocess
  servers), `[tui.sidebar]`/`[tui.footer]` (hiding safety chrome), `[sandbox]`
  (weakening containment), `[runtime]` (native↔wasm tool swap), `[inference]`
  (API-key exfil endpoints). Stripping is case-insensitive. `defaults.model`/
  `provider` and other project overrides still apply.
- **Project personas are opt-in.** A repo `.stado/personas/*.md` no longer
  silently shadows your persona; set `[defaults] allow_project_persona = true`
  (user config) to honor them.
- **Project-local plugin autoload is opt-in.** A repo's `.stado/plugins/` is no
  longer auto-registered into the agent; set `[plugins] allow_project_plugins =
  true` (user config) to enable it. (`plugin run` / explicit ops unaffected.)
- **Auto-LSP diagnostics are opt-in.** Set `[lsp] auto_diagnostics = true` —
  previously the TUI auto-spawned an unsandboxed language server after every
  mutating edit. All three opt-in keys are themselves stripped from project
  config (a repo can't self-enable its own bypass).
- **Cross-repo memory leak fixed.** A repo-committed `.stado/user-repo` pin is
  honored only when it's an ancestor/descendant of the workdir (or a
  stado-managed session worktree).

### Security — sandbox / path-safety

- **`shell.spawn` PTYs are sandboxed** on the confining surfaces (daemon /
  mcp-server / acp / headless), parity with `shell.exec`. `run`/TUI/`resume`
  stay on the operator's own filesystem by design.
- **`exec:proc`/`exec:pty` basename caps** (e.g. `exec:proc:git`) no longer
  authorize a path-containing argv[0] like `/tmp/evil/git` (incl. Windows
  backslash/volume forms).
- **`.git` write-guard** closes a `.git`-symlink-to-other-dir bypass.
- **`net:dial:unix` caps** canonicalize the socket path before authorizing
  (symlink-redirect fix).
- **`plugin remove`** no longer interprets glob metacharacters in the project
  path. **`shell.signal`** accepts signal names (`SIGINT`) host-side.

### Fixes

- ACP choice input-validation could be bypassed with extra/unknown selections;
  it now requires exactly one known option and clears stray input.
- Headless sessions sandboxed against the real checkout instead of the sidecar
  worktree.

### Notes

- A `post_tool` redaction hook is documented as context-hygiene, **not**
  secret-erasure — the original bytes remain in the sidecar audit trail.

