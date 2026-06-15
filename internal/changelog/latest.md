## v0.75.1 — deferred Codex triage hardening — 2026-06-15

Patch follow-up to v0.75.0: closes the remaining **medium/low FIX_NOW** items
from the post-v0.74.1 Codex export that did not need operator decisions. No
new features — security hardening, resource caps, and correctness fixes across
the TUI, ACP, plugin host, and CLI.

### Security

- **Plugin manifest descriptions are sanitized** in TUI `/plugin` and `/tool ls`
  listings (terminal escape injection via repo-local plugin text).
- **ACP choice/approval no longer hang on client disconnect** — `Conn.Serve`
  signals peer disconnect before waiting for in-flight handlers.
- **`config providers setup --api-key`** export hints use POSIX-safe
  single-quoting (not Go `%q`, which leaves shell metacharacters active).
- **HTTP stream requests enforce same-host redirects** (parity with non-streaming
  `stado_http_request`; blocks cross-host redirect bypass).
- **Plugin install validates categories/requires before trust-store writes** (a
  category failure no longer leaves a pinned signer).
- **Malformed HTTP-request URLs** no longer panic on the denial path.
- **Doc picker skips control-character paths** (parity with the file picker).

### Fixes

- **`--provider` / `--model` root flags** now apply on `stado acp`, `stado
  headless`, `stado mcp-server`, and `stado tool run` (previously ignored).
- **`stado uninstall`** compares resolved paths for the self-binary guard but
  removes the install symlink — not its target (Fedora Atomic `$HOME` symlink).
- **Daemon `listTools`** snapshots the registry under lock (data-race fix).
- **Invalid plugin tool class** keeps the conservative `exec` fallback instead
  of downgrading to `non-mutating`.
- **`/spawn`** guards against a nil subagent spawner (process panic).
- **`plugin dev --watch`** cleans up on Ctrl+C via `signal.NotifyContext`.
- **Progress-log prepend** is capped against `budget.PluginBytes`; activated-tool
  surfaces are sorted for stable prompt-cache keys.
- **PTY cols/rows capped at 1000**; **ICMP `timeout_ms` capped at 30s**.

