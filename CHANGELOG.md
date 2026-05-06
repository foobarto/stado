# CHANGELOG

Notable changes to stado, reverse-chronological. Pre-1.0; breaking
changes still allowed between tags. Sections: UX / CLI / TUI /
Plugins / Infra / Fixes.

## Unreleased

(no unreleased changes)

## v0.43.1 — Windows build fix

### Fixes

- **goreleaser Windows target.** `syscall.SetsockoptInt` takes
  `syscall.Handle` on Windows but the EP-0038i `setBroadcastFD`
  helper was casting `fd` to `int` (POSIX shape). Split the helper
  into `host_net_setsockopt_unix.go` (`!windows`) and
  `host_net_setsockopt_windows.go` so each platform casts the
  `uintptr` from `SyscallConn.Control` to its own `SetsockoptInt`
  argument type. Cross-verified with `GOOS=windows GOARCH=amd64
  go build` and `GOOS=darwin GOARCH=amd64 go build`.

## v0.43.0 — stado_progress agent-loop integration (closes the EP-0038 backlog)

### Plugin runtime — agent-loop integration

- **`stado_progress` now reaches the model.** v0.38 introduced
  operator-visibility for progress emissions (TUI sidebar, `stado
  plugin run` stderr). v0.42 adds the model-visibility half: while
  a tool runs, emissions are collected per-call via a context-
  threaded `tool.ProgressCollector`; on successful return,
  `Executor.Run` prepends a `[progress] plugin: text` log to the
  tool's result envelope so the model sees the trail. Bounded at
  64 entries per call (FIFO drop on overflow). Suppressed when the
  tool errored (errored results stay clean). This is atomic from
  the model's POV — mid-tool model streaming would need an
  LLM-API streaming-tool-call contract that doesn't exist today;
  closing that gap would shift the agent-loop contract and is
  filed for if/when an upstream provider ships native support.

## v0.42.0 — EP-0038i ICMP echo (closes the network surface)

### Plugin runtime — new host imports

- **`stado_net_icmp_echo(host, timeout_ms?, count?, payload_size?)`** —
  ICMP echo (ping) for plugins doing reachability sweeps where a
  closed TCP port and a dropped IP are different signals. New
  capability `net:icmp`. Tries an unprivileged ICMP socket first
  (Linux `net.ipv4.ping_group_range` covers the running uid; macOS
  supports this without sysctl since 10.10); falls back to raw
  (`SOCK_RAW` + `IPPROTO_ICMP`) which needs `CAP_NET_RAW`. Error
  message names the fix (`sysctl ping_group_range` or `CAP_NET_RAW`).
  Private-IP guard via `NetHTTPRequestPrivate` — without that cap,
  loopback / RFC1918 / link-local destinations are refused at
  resolve. Result includes per-echo RTTs in milliseconds plus
  sent/received counts. Bounds: count ≤ 64, payload ≤ 1500 bytes.
  Uses `golang.org/x/net/icmp` (already a transitive dep).

## v0.41.0 — UDP broadcast/multicast + FleetBridge messaging real impl

### Plugin runtime — new host imports

- **`stado_agent_send_message` real impl.** Was a stub that validated
  the agent ID and silently dropped the body. Now: `Fleet` carries a
  per-agent inbox queue (bounded at 64 messages); `SendMessage` pushes
  onto it; `AgentLoop` drains queued messages at every turn boundary
  and prepends them as user-role inputs in the next turn request.
  Wired through a new optional `InboxAwareSpawner` interface
  (`Spawner` + `WithInbox(fn func() []string) Spawner`); the fleet
  type-asserts and supplies a closure drawing from the right inbox.
  `SubagentRunner` implements both. Effect: the bundled agent
  plugin's `agent.send_message` tool actually delivers messages
  mid-loop instead of being a no-op.

- **`stado_net_setopt(lst_udp, key, value)`** — broadcast / multicast
  setopts on a UDP listener handle. Five keys: `broadcast` (toggles
  `SO_BROADCAST`, required for sendto to broadcast addresses);
  `multicast_join` / `multicast_leave` (join/leave a multicast group
  on an optional named interface); `multicast_loopback` (whether
  multicast we send is looped back to us); `multicast_ttl` (TTL /
  hop limit on outgoing multicast packets, 0..255). All keys gated
  by the new `net:multicast:udp` capability. Group addresses
  validated as multicast (224.0.0.0/4 for IPv4, ff00::/8 for IPv6).
  Multicast wiring uses `golang.org/x/net/ipv4|ipv6` (already a
  transitive dep). Useful for discovery protocols (mDNS, SSDP,
  WS-Discovery, BACnet, NBNS).

## v0.40.0 — TUI tool-expand + mouse + PTY persistence + EP-0038i imports

Bundle release covering UI quality-of-life, two operator-reported
fixes, and three deferred plugin-runtime imports (HTTP upload
streaming, JSON set, AXFR DNS).

### Plugin runtime — new host imports

- **`stado_dns_resolve_axfr(zone, server, timeout_ms?)`** — DNS zone
  transfer (RFC 5936). Useful for security tooling enumerating
  internal zones on misconfigured / permissive infrastructure. New
  capability `dns:axfr` (implies `dns:resolve`). Plugin must name
  the authoritative server explicitly — no recursion. REFUSED rcodes
  land in `result.error` rather than crashing. Adds the
  `github.com/miekg/dns` dependency (single direct dep; the standard
  Go DNS library; binary impact bounded by the imports we use).

- **`stado_json_set(json, path, value) → modified_json`** — companion
  to v0.38.0's `_get` / `_format`. Mutates a value at a dotted path
  in a JSON document and returns the canonical bytes of the modified
  document. The `value` payload is itself parsed as JSON and embedded
  at the target. New object keys are added; out-of-range / non-numeric
  array indices return -1 (no implicit array growth). Walking
  through nil auto-creates intermediate objects so plugins can build
  nested structure incrementally. Empty path replaces the whole doc.
  No capability gating (pure compute, bounded to 256 KB input).

- **`stado_http_upload_create` + `_upload_write` + `_upload_finish`** —
  chunked HTTP request body delivery (EP-0038i, the symmetric
  counterpart to v0.38.0 response streaming). Plugins can now upload
  multi-GB payloads without buffering the whole body in wasm memory.
  New typed handle `httpup:<id>`; `_upload_finish` returns a
  `httpresp:<id>` so the plugin drains the response via the existing
  `stado_http_response_read` / `_close` imports — upload + download
  streaming compose. Reuses `net:http_request[:<host>]` cap; no new
  cap surface. Per-Runtime cap of 8 concurrent in-flight uploads;
  reaped on Runtime shutdown. Args JSON narrows to method/url/
  headers/timeout_ms/content_length — no `body_b64`. Out of scope:
  HTTP/2 server-push, multipart streaming, trailers, true bidi
  duplex.

### Fixes

- **`fs:read:.` / `fs:write:.` on Fedora Atomic / Silverblue / Bazzite.**
  On these distros `/home` is a symlink to `/var/home`. When the
  operator's workdir was the symlink form (`/home/user/repo`), the
  cap parser stored the literal path in `h.FSRead` but `realPath()`
  at file-access time resolved through the symlink to
  `/var/home/user/repo/...`. The cap-glob compare failed, so every
  `fs_read` call silently denied. Fix: at cap-parsing time, also
  append the `EvalSymlinks`-resolved form when it differs from the
  literal — both forms are now in the allowlist. Best-effort:
  missing path or EvalSymlinks failure falls back to the literal.
  Reported by the operator on Fedora Atomic.

- **Bundled `shell.*` / `pty.*` PTY persistence across calls.** Each
  `bundledPluginTool.Run` was creating a fresh `pluginRuntime.New`,
  which in turn created its own `pty.NewManager()`. So
  `shell.spawn` returned an id that the next call's `shell.attach` /
  `read` / `write` couldn't see — every dispatch got a fresh empty
  registry and the second call returned `pty: session not found`.
  Reported on `v0.37.0+`. Fix: new optional `tool.PTYProvider`
  interface; long-lived hosts (TUI session, MCP server, headless
  agent loop) construct one `*pty.Manager` and expose it via
  `PTYManager()`. The bundled-plugin Run path now type-asserts and
  reuses the shared manager when present, falling back to the
  per-call manager otherwise (one-shot `stado plugin run` / `stado
  tool run` are still single-process so the per-call fallback is
  fine for those). Added a regression test
  (`TestBundledPluginTool_HonoursPTYProvider`) that spawns a real
  PTY between two `shell__list` dispatches and confirms the second
  call sees it.

### TUI

- **Expand older tool calls.** `Shift+Tab` previously only toggled
  the latest tool call or assistant turn details. New navigation:
  `Alt+Up` / `Alt+Down` move focus to the previous / next
  expandable block (rendered with a left-edge accent marker);
  `Shift+Tab` then toggles whichever block is focused. With no
  focus, `Shift+Tab` falls back to the previous "latest" behaviour.
- **Mouse left-click expands a tool block.** Click any tool /
  expandable assistant block in the conversation to focus + toggle
  it. Clicks past the conversation pane (sidebar, input) fall
  through to default behaviour.
- **`[tui].mouse_capture` config option** — default `true` (current
  behaviour: app captures mouse events for click-to-expand + scroll
  wheel). Operators who prefer terminal-native click-drag-to-
  select-text can set `false` to disable capture entirely. With
  capture on, holding `Shift` while click-dragging usually bypasses
  app capture in modern terminals.
- **`stado plugin list` PATH column** — each row now shows the
  on-disk path to the plugin's `.wasm` (or `(embedded)` for bundled
  plugins). Useful for `cp` / `file` / `wasm-objdump` / `sha256sum`
  workflows without remembering the state-dir layout.
- **Cleaner text selection.** When the sidebar is hidden (`Ctrl+T`)
  the chat column no longer pads each row to a fixed width —
  click-drag-to-select copies just the visible text instead of a
  trail of pad spaces. The sidebar adjacency problem (rectangular
  terminal selection grabs sidebar text too) remains terminal-side;
  `tui.md` documents three escape hatches: hide sidebar first,
  block-mode `Alt+drag`, or set `[tui].mouse_capture = false`.

## v0.39.0 — session.search plugin + TUI progress surface

### Plugins

- **`session.search`** — new bundled wasm plugin offering grep-style
  search over the current session's message history. Substring
  (default) or RE2 regex (`is_regex: true`); case-insensitive by
  default; optional role filter; bounded results + snippet length.
  Capability: `session:read` (existing — no new host imports).
  Search core lives in `searchcore/` so it builds + tests on the
  host arch alongside the wasip1-only main module.

### TUI

- **`stado_progress` surfaces in the sidebar.** Closes the half-shipped
  EP-0038h piece. Bundled wasm plugins emitting `stado_progress` now
  show as `PROGRESS [plugin] text` entries in the TUI's log-tail
  sidebar — always visible (no `--sidebar-debug` required), styled as
  accent. Wired via a new `tool.ProgressEmitter` optional Host
  extension; `bundled_plugin_tools.Run` type-asserts and populates
  `host.Progress`. Headless runs without an attached operator stay
  silent (nil callback, plugin doesn't fail).

## v0.38.0 — EP-0038h: JSON helpers + UDP stateless + HTTP streaming + stado_progress

Bundle of four deferred items from v0.36–v0.37. Each is small on its
own; releasing them together avoids per-tag overhead.

### Plugin runtime — new host imports

- **`stado_json_get(json, path) → bytes`** + **`stado_json_format(json, indent) → bytes`** —
  host-side JSON conveniences. Plugins extract one value from an
  HTTP response or pretty-print a payload without bundling a 50 KB
  parser into every wasm binary. Path is dotted form (`a.b.0.c`); no
  filters or globs. No capability gating (pure compute, bounded to
  256 KB input).
- **`stado_net_listen("udp", host, port)` + `stado_net_sendto` + `stado_net_recvfrom`** —
  stateless UDP. A UDP listen handle backs both
  send-to-anyone and recv-from-anyone, mirroring Go's
  `net.PacketConn`. New cap `net:listen:udp:<host>:<port>`. Outbound
  peers in `_sendto` are gated by the **same `net:dial:udp:` glob set**
  as connect-mode UDP — a UDP listener can't be a wildcard spray gun.
  `_recvfrom` packs `(body_len << 32) | addr_len` into an i64 return
  with `-1` / `-2` sentinels in the body slot.
- **`stado_http_request_stream` + `stado_http_response_read` + `stado_http_response_close`** —
  chunked HTTP body delivery. Resolves the large-payload OOM in
  `stado_http_request`. New typed handle `httpresp:<id>`. Reuses
  existing `net:http_request[:<host>]` cap; no new cap surface.
  Per-Runtime cap of 8 concurrent open streams; reaped on Runtime
  shutdown. Out of scope: request-body streaming (uploads), HTTP/2
  push, multipart, `proxy_url` (use the non-streaming variant for
  SOCKS pivots).
- **`stado_progress(text_ptr, text_len) → i32`** — operator-visible
  progress emission for long-running tools (>2s). v1 is
  operator-visibility only; the agent / model sees only the final
  tool result. Mid-tool partial output to the model would break
  tool-call atomicity in current LLM contracts and is explicitly
  out of scope. 4 KB max per call. `stado plugin run` prints
  `[plugin] text` to stderr; TUI integration follows.

### Plugin manifest

- New capability vocabulary:
  - `net:listen:udp:<host>:<port>` — bind a UDP socket for
    stateless send/recv (verbatim host-port match like TCP listen)
- Plugin doctor classifies the new UDP listen cap.

### Deferred

ICMP raw sockets (per operator: last), AXFR DNS, FleetBridge
messaging real impl, `stado_json_set` / jq-style queries, UDP
broadcast/multicast set-options, HTTP request body streaming
(uploads), `stado_progress` agent-loop integration (model sees
mid-tool partials).

## v0.37.0 — EP-0038g: net expansion (UDP + Unix sockets + listen/accept)

Continuation of the v0.36.0 EP-0038f TCP work. Plugins can now talk
non-HTTP datagrams, Unix domain sockets, and accept inbound
connections.

### Plugin runtime — new host imports

- **`stado_net_dial("udp", host, port, timeout_ms)`** — connect-mode
  UDP client. Same `conn` handle / read-write-close lifecycle as TCP.
  Capability: `net:dial:udp:<host-glob>:<port-glob>`. Private-IP guard
  shared with TCP (`net:http_request_private` extends to UDP).
  Unblocks NTP, DNS-over-UDP, syslog, custom binary RPC.
- **`stado_net_dial("unix", path, 0, timeout_ms)`** — Unix domain
  socket client. Capability: `net:dial:unix:<path-glob>` (path glob
  via `filepath.Match`). Path validation refuses `..` traversal and
  socket paths > 104 bytes (BSD `sun_path` upper bound).
- **`stado_net_listen` + `stado_net_accept` + `stado_net_close_listener`** —
  server-side networking for `tcp` and `unix` transports. New typed
  handle prefix `listen:<id>`. Per-Runtime cap of 8 listeners; 9th
  bind returns -1. Accept timeout is required, clamped to
  `[default 5s, max 30s]` (DoS guard); -2 distinguishes timeout from
  error. Unix listeners auto-remove their socket file on close and on
  Runtime shutdown.

### Plugin manifest

- New capability vocabulary:
  - `net:dial:udp:<host>:<port>` — outbound UDP
  - `net:dial:unix:<path-glob>` — outbound Unix socket
  - `net:listen:tcp:<host>:<port>` — bind TCP (host = `127.0.0.1` for
    loopback, `0.0.0.0` for any-interface; **verbatim match — no
    implicit widening**, operator must opt in to public binds
    explicitly)
  - `net:listen:unix:<path-glob>` — bind Unix socket
- Plugin doctor (`stado plugin doctor`) classifies each new cap with
  per-transport notes.

### Bug fixes (carried in)

- **`net:dial:tcp:` parser regression from v0.36.0.** The capability
  was silently never reaching the parser block (`SplitN(cap, ":", 3)`
  doesn't expose `parts[2..4]`); it was instead being junk-populated
  into `NetHost`. The v0.36.0 access-layer tests masked the gap.
  Fixed by re-splitting `net:dial:*` / `net:listen:*` caps on every
  colon. v0.36.0 plugins that relied on `net:dial:tcp:*` now actually
  work as documented.

### Deferred

ICMP raw sockets (needs `CAP_NET_RAW`), AXFR DNS, HTTP request
streaming, FleetBridge messaging real impl, `stado_json_*`
conveniences, and UDP stateless `send_to`/`recv_from` remain on the
backlog. Per the architectural-reset spec, `stado_progress` streaming
for partial tool output is also still pending — needs agent-loop
integration design.

## v0.36.0 — Lazy-load realised, host imports doc, EP-0038f TCP, tester-feedback items

This release closes the central architectural-reset gap (lazy-load
not actually filtering the per-turn surface) and ships seven new
plugin-runtime imports addressing concrete tester pain.

### Plugin runtime — new host imports

- **`stado_http_request` proxy_url field** — http(s) + socks5(h)
  schemes route the request through a proxy. Use case: after a
  ligolo-ng pivot every WASM tool reaches inner subnets without
  dropping to bash. Dial guard still applies to the proxy itself;
  `net:http_request_private` covers loopback proxies.
- **`stado_instance_get/set/delete/list`** — process-lifetime in-memory
  KV store with per-plugin namespacing. Resolves multi-step exploit
  chains needing state across tool calls (auth cookies, session
  tokens). Bounded: 1 MB per value, 16 MB per plugin. Capabilities:
  `state:read[:<glob>]`, `state:write[:<glob>]`.
- **`stado_tool_invoke`** — wasm plugins call other registered tools.
  Capability: `tool:invoke[:<name-glob>]`. Recursion depth-limited
  (4). Errors wrapped in JSON envelope.
- **`stado_net_dial / read / write / close`** — Tier 1 TCP raw socket
  primitives (BACKLOG #11 — partial; UDP/Unix/listen/ICMP deferred to
  EP-0038g). Capability: `net:dial:tcp:<host-glob>:<port-glob>`.
  Same private-address dial guard as http_request.
- **`stado_secrets_delete`** — wasm wrapper for the existing
  `secrets.Store.Remove`. Cap-gated by `secrets:write`.
- **Four new meta-tools** (always autoloaded alongside `tools__search`
  / `_describe` / `_categories` / `_in_category`):
  `tools__activate`, `tools__deactivate`, `plugin__load`,
  `plugin__unload`. Lets the agent skip the describe round-trip when
  a parent already named the tool, and bulk-load/unload all of a
  plugin's tools.

### Lazy-load realised (EP-0037 §E)

The architectural reset's central design — "stado stops broadcasting
every tool's schema in the system prompt" — was 80% built but the TUI
wasn't actually filtering per-turn `toolDefs()`. Fixed:

- `Model.activatedTools` map; cleared on `/clear`.
- `toolSurfaceForTurn()` returns autoload ∪ activated, with Plan-mode
  + session-override filters.
- `Host.ActivateTool` / `DeactivateTool` (`pkg/tool.ToolActivator` /
  `ToolDeactivator`) now actually implemented.
- `tools__describe` results parsed and added to activation set after
  every tool turn (via `runtime.AbsorbActivatedFromDescribe`).

Net effect: a session with N installed plugins now sends only autoload
core (~8 tools) + whatever the model has explicitly activated, rather
than every tool's schema every turn.

### Tool-surface configuration

- **`[tools].autoload_categories`** — list of category names. Every
  tool whose `tools[].categories` overlaps joins the autoload set.
  Layered on top of name-based autoload. Lets operators run lean with
  rich plugin sets (declare `["recon"]`; pull exploit_* on demand via
  `tools.activate`).

### Plugin manifest

- **`requires []string`** — plugin dependency declarations.
  `["http-session >= 0.1.0", "secrets-store"]`. `stado plugin install`
  verifies each entry is installed at a satisfying version; install
  fails with a multi-error listing every unsatisfied dep at once.

### Tool surface — operator-collapsed surface

- **`spawn_agent` removed** — was already done in v0.35.0 but worth
  noting again: the canonical agent-spawn surface is `agent.spawn`
  (wasm), part of the unified `agent.*` family. Manifests declaring
  `subagent.Tool` registrations need to drop them.

### Documentation

- **`docs/plugins/host-imports.md`** — comprehensive reference for
  every wasm host import (~70 total). Tier 1/2/3 grouping, capability
  gates, ABI conventions, Patterns + anti-patterns section addressing
  tester feedback ("don't conflate plugin execution with agent
  orchestration"; "use exec:proc:<binary> over exec:bash"). Linked
  from `docs/features/plugin-authoring.md` as the first stop.

### Plugin doctor

- New cap classifications: `state:*`, `tool:invoke:*`, `net:dial:*`.

### Deferred

- **Tier 1 net beyond TCP** — UDP, Unix sockets, listen/accept,
  ICMP. EP-0038g (own design cycle).
- **`stado_progress` streaming** — partial-output channel for tools
  that take >2s. Out of scope this cycle; deserves design care for
  agent-loop integration.
- **`stado_dns_resolve_axfr`**, **`stado_json_*`**, **HTTP streaming**
  — housekeeping items also for EP-0038g.

## v0.35.2 — `.github/dependabot.yml` for explicit example-plugin scans

### Infra

- **`.github/dependabot.yml`** — explicit scan configuration covering
  the parent gomod module, all example-plugin subdirs (`/plugins/default/*`,
  `/plugins/examples/*` — 18 of them total) via the `directories`
  (plural) glob shape, and GitHub Actions in `.github/workflows/`.
  Without this, dependabot's auto-discovery rescanned the plugin
  go.mods on a slow cadence; v0.35.1's fix took longer than expected
  to flip its 4 alerts to "fixed". Explicit weekly schedule + commit-
  message scopes (`chore(deps)`, `chore(plugin-example)`,
  `chore(ci)`) keep future bumps consistent.

## v0.35.1 — Dependabot bumps for golang.org/x/net

### Fixes

- **Browser-plugin go.mods bumped to `golang.org/x/net v0.43.0`** —
  closes 4 dependabot alerts (GHSA-qxp5-gwg8-xv66 HTTP-proxy IPv6
  bypass, GHSA-vvgc-356p-c3xw XSS) on
  `plugins/default/browser/go.mod` and `plugins/examples/browser/go.mod`
  where x/net was pinned at v0.30.0.

### Infra

- `go mod tidy` promoted `github.com/fsnotify/fsnotify v1.7.0` from
  `// indirect` to direct in the parent module — it was always
  consumed directly by the plugin-dev-watch wiring; the indirect
  tag was stale from when the dep was first added.

## v0.35.0 — Plugin bundle, dev watch, Tier 2 (HTTP client + secrets), spawn_agent collapse

### Breaking changes

- **CLI** — `--tools-whitelist` renamed to `--tools` (canonical per
  architectural-reset NOTES §10). No back-compat alias kept; pre-1.0
  means scripts using the old name need updating. The previous bool
  `--tools` (on/off gate) is removed; use `--no-tools` for pure-chat
  mode. `--tools` is now the comma-separated whitelist (empty = all
  installed tools enabled).
- **CLI breaking** — `stado plugin run <plugin-id> <tool> [args]`
  removed. Use `stado tool run <name> [args]` instead — it resolves
  bundled and installed tools uniformly through the live registry.
  Accepts both canonical (`fs.read`) and wire (`fs__read`) names;
  `--session` and `--workdir` carry over; `--force` overrides
  `[tools].disabled` for one-off invocation.
- **Tool surface** — `spawn_agent` (native) removed in favour of
  `agent__spawn` (wasm). Both paths went through `SubagentRunner`;
  the wasm form is a strict superset (adds `agent__list`,
  `read_messages`, `send_message`, `cancel`, async mode). Default
  autoload list rewritten: `spawn_agent` → `agent__spawn`. Manifests
  declaring `subagent.Tool` registrations need to drop them.

### Plugin authoring

- **`stado plugin bundle <ids>... --out=<binary>`** — appends already-
  compiled wasm plugins to the trailing bytes of a stado binary,
  producing a portable customised stado without requiring a Go
  toolchain. Two-level signature verification: per-plugin author
  signature + outer ephemeral-by-default bundler signature seals
  the payload. `--bundling-key=<seed>` for persistent identity,
  `--allow-unsigned` to skip per-plugin trust-store check,
  `--allow-shadow` to override tool-name collisions. Sub-actions:
  `--strip --from=<bundled>` (extract vanilla copy),
  `--info --from=<binary>` (list bundle contents). Runtime escape
  hatch: `--unsafe-skip-bundle-verify` boots a tampered bundle
  with a loud warning + permanent `[unsafe-skip-verify]` marker
  in `--version`. Spec at
  `docs/superpowers/specs/2026-05-06-plugin-bundle-design.md`.
- **`stado plugin dev <dir> --watch`** — file-watch + auto-rebuild
  + auto-reinstall under a `0.0.0-dev` sentinel that gets cleaned
  up on Ctrl+C. 250ms debounce; requires `<dir>/build.sh`.
  Persistence-free in spirit (the sentinel install + active marker
  are removed on watch exit). Reuses the unified-registry slot —
  plugin tools become visible via `stado tool run` / `tool list`
  / mcp-server immediately. Spec at
  `docs/superpowers/specs/2026-05-06-plugin-dev-watch-mode-design.md`.
- **`stado plugin install --autoload`** — persists the newly-installed
  plugin's tools into `[tools].autoload` so they load into every
  session without a separate `stado tool autoload` call.
- **`stado plugin reload <name>`** (CLI) + `/plugin reload [<name>]`
  (TUI) — CLI is advisory (tool calls re-read plugin.wasm per
  invocation); TUI rebuilds the executor's tools registry so
  plugins installed AFTER session start become visible without
  restarting.
- **`stado plugin sign --key-env <ENVVAR>` + `--quiet`** — CI-
  friendly signing flow. The seed is read from an env var (hex or
  base64), eliminating the temp-file dance for runner secrets.
  `--quiet` suppresses informational stdout.

### Plugin runtime — Tier 2 (EP-0038e)

- **Stateful HTTP client** — new `internal/httpclient/Client` with
  cookie jar, redirect cap (default 10, with `follow_subdomain_only`),
  per-host + total connection mux limits, dial guard (RFC1918 /
  loopback / link-local refused unless `AllowPrivate=true`), and
  exact + suffix-glob host allowlist. Wasm imports
  `stado_http_client_create / _close / _request` gated by
  `net:http_client` capability; the existing
  `net:http_request:<host>` allowlist still bounds reachable hosts.
  Per-Runtime cap of 64 open clients prevents resource exhaustion.
- **Operator secret store** — new `internal/secrets/Store` backs
  `<StateDir>/secrets/<name>` with mode-0600 files and refuse-on-
  permission-widening. Wasm imports `stado_secrets_get / _put /
  _list` gated by `secrets:read[:<glob>]` and `secrets:write[:<glob>]`
  capabilities. Every call (allowed or denied) emits a structured
  audit event — names yes, values never. New CLI:
  `stado secrets set/get/list/rm <name>`. Spec at
  `docs/superpowers/specs/2026-05-06-ep-0038e-tier2-stateful-design.md`.
- **`stado plugin doctor`** — added cap-vs-sandbox cross-check that
  flags concrete mismatches between manifest caps and `[sandbox]`
  config. E.g. `net:http_request` with `[sandbox.wrap].network = "off"`
  → error; `fs:read:/etc/passwd` not in `bind_ro` → warn; etc.
- **Unified registry follow-ups** — `tool run gtfobins.lookup`
  (dotted form) now resolves installed plugins whose authors use
  the single-underscore wire convention (`gtfobins_lookup`); tier-4
  fallback in `lookupToolInRegistry`. `plugin info <name>` (bare
  name, no version) resolves via the new
  `runtime.ResolveInstalledPluginDir` helper.

### Tool dispatch

- **`tools__describe`** — accepts `name: "foo"` (single) OR
  `names: ["foo","bar"]` (batched). Both forms can be passed in
  one call; entries are merged and deduped preserving order.
  Replaces the names-array-only schema.

### CLI

- **`stado --unsafe-skip-bundle-verify`** — top-level persistent
  flag for runtime-skip of bundled-payload verification. Loud
  stderr warning; permanent `[unsafe-skip-verify]` marker in
  `--version` output.
- **`stado --version` custom-bundle marker** — when a binary
  contains a user-bundled payload, version output appends
  `(custom: N plugins, bundler=<8-char-fpr>)` for operator
  visibility.
- **`stado secrets set/get/list/rm`** — see Plugin runtime above.

### Plugin metadata

- New capability vocabulary entries (with `plugin doctor`
  classification): `net:http_client` (creates HTTP clients +
  uses existing host allowlist), `secrets:read[:<glob>]`,
  `secrets:write[:<glob>]`.

### Infra

- **Security/PII audit infrastructure** —
  `.gitleaks.toml` extends the default ruleset with project-specific
  allowlists (binary noise, OAI-compat test URI, examples).
  `.pre-commit-config.yaml` runs gitleaks + detect-private-key +
  trailing-whitespace + EOL hooks on every commit.
  `.github/workflows/secret-scan.yml` runs gitleaks-action against
  every PR and push to main.
  Working-tree path-leak strip: 121 `/home/<username>/...`
  occurrences across docs replaced with `~`/`<repo-root>`.
  Editorial pass on `docs/eps/notes/2026-05-05-architectural-reset.md`
  (2018 lines of chat transcript curated to 152-line summary).
- **Sandbox test resilience** — bwrap test now prefers
  `/usr/bin/python3` over `which python3` so linuxbrew-style
  environments (where python lives at a path bwrap doesn't bind)
  don't false-fail.

### Deviation documentation

- **`buildNativeRegistry()` retention** — original EP-0038b Task 5
  called for deletion; documented as a deliberate retention in
  EP-0038's "Deviation" section. Native code stays as the parity-
  test backstop and as the operational fallback when a wasm
  family misbehaves in production.

### Fixes

- **fsnotify integration** — added as a direct dep for plugin dev
  watch mode; previously listed `// indirect`.

## v0.34.1 — Atomic Fedora / Bazzite, multi-tool wasm, exec:proc multi-glob

### Fixes

- **`/home → /var/home` strict-walk regression** — every tool that touched a
  session worktree (`~/.local/state/stado/worktrees/...`) failed on Atomic
  Fedora / Bazzite with `directory component is a symlink: home` because the
  no-symlink walk used by `treebuild`, `subagent_adoption`, audit/minisign,
  several `cmd/stado/*.go` writers, and most TUI / tool readers rejected the
  operator-supplied `/home` link. Fixed by extending the EP-0028 trust-anchor
  model to file/dir Open: new `OpenRegularFileUnderUserConfig` +
  `ReadRegularFileUnderUserConfigNoLimit` plus migration of ~17 call sites
  to the anchored variants. In-userspace attacker boundary preserved (strict
  walk from anchor down). Adversarial paths outside HOME / XDG still get
  the strict from-`/` check via the fallback inside `OpenRoot*UnderUserConfig`.
- **TUI bash / native-tool cwd parity** — TUI tools now operate on the user's
  launch CWD (matching `stado run` default) instead of the session audit
  worktree. Both override paths fixed (`resetForSession` and
  `model_stream.go` host-adapter wiring). The session worktree remains in
  use only for turn-boundary tree commits.
- **Multi-tool wasm "missing ABI exports"** — `newBundledWasmTool` was
  setting `def.Name` to the full export name (`stado_tool_ls`), but the
  dispatcher prepends `stado_tool_` again — `fs.ls`, `shell.spawn/...`,
  `agent.spawn/...`, `web.fetch`, `dns.resolve` all looked up
  `stado_tool_stado_tool_<X>` and failed. Strip the prefix.
- **`exec:proc:<glob>` allows multiple scoped binaries per manifest** —
  `Host.ExecProcGlob` was a single string; the second declaration silently
  overwrote the first. Switched to `[]string` so e.g. `fs.ls` declaring
  both `/bin/ls` and `/usr/bin/ls` works regardless of which is the
  symlink and which the canonical path.
- **`plugin run` exec:bash gate restored to documented behavior** — was
  drifting toward always-refuse; restored EP-0028 D1's warn-loud-run-by-
  default with `[sandbox] refuse_no_runner = true` opt-in for hard refusal.
- **`cobra.Command.Context()` nil-fallback** in `plugin run` (panic surfaced
  under -race in CI when `RunE` invoked without a parent `Execute()`).
- **`spawn_agent` autoload** — restored to the default tool surface so the
  native subagent SubagentEvent path stays reachable without explicit
  `tools.describe` activation (regression from EP-0037 autoload).

### Tool dispatch (EP-0037 §F follow-up)

- **`stado tool enable / disable / autoload / unautoload`** — the locked
  config-mutating verbs. Replaces TOML hand-edits to `[tools]` for
  managing visibility and per-turn surface. Flags: `--global` (writes
  `~/.config/stado/config.toml` instead of project's `.stado/config.toml`),
  `--config <path>` (explicit target), `--dry-run`. Inverse-list cleanup
  on the right side: `tool enable` strips from disabled; `tool disable`
  strips from enabled and autoload.

### Lint / CI

- Cleared five dead-code findings exposed once golangci-lint stopped
  early-exiting (`resolveToolName`, `subagentSpawnerAdapter`,
  `monitorLineMsg`, `loopStatusLabel`, `toolLike`).
- `host_proc.go` receiver name `host` → `h` (ST1016).
- `host_compress.go` defer wrapped to satisfy errcheck.
- `sandbox/wrap.go` error string reflowed (ST1005).

### Docs

- Restored architectural-reset notes (2026-05-05) under
  `docs/eps/notes/2026-05-05-architectural-reset.md` — the design history
  that produced EP-0037/0038/0039.

## v0.34.0 — Tool list UX, terminal aliases, fs.ls, web/dns wasm, remote install

### Tool surface (EP-0037 follow-ups)

- **`stado tool list`** — replaces `tool ls`, adds **PLUGIN** + **CATEGORIES**
  columns and renders dotted canonical names (`fs.read`, `shell.exec`). Bundled
  tool metadata layer maps both wire and bare names. Hidden tools (`approval_demo`,
  superseded natives like `webfetch`/`spawn_agent`) drop out of listings.
- **`plugin list`** — proper table: NAME / VERSION / TOOLS / AUTHOR / FINGERPRINT
  / TRUST.
- **`plugin info`** — defaults to human-readable tool schemas / params /
  capabilities; `--json` for scripting.
- **Autoload fix** — `spawn_agent` re-added to the default autoload set so the
  native `SubagentEvent` path is reachable without explicit `tools.describe`
  activation (regression from EP-0037).

### ABI v2 wasm modules (EP-0038 follow-ups)

- **`stado_terminal_*` aliases** for the existing `stado_pty_*` host imports
  (`open/list/attach/detach/write/read/signal/resize/close`). Capability check
  enforced at call time, so multi-tool wasm modules link cleanly even with
  partial cap grants.
- **`shell.wasm`** — full PTY surface: `shell.spawn/list/attach/detach/read/`
  `write/resize/signal/destroy` plus one-shot `shell.exec/bash/sh/zsh`.
  Capabilities: `terminal:open` (PTY) + `exec:proc` (one-shot).
- **`fs.ls`** — folded into `fs.wasm` via `stado_exec` over `/bin/ls`. Bare `ls`
  binary embedding dropped.
- **`web.wasm`** — `web.fetch` wrapper over `stado_http_get`. Native `webfetch`
  hidden in favour of the wasm version.
- **`dns.wasm`** — `dns.resolve` over `stado_dns_resolve` (A/AAAA/TXT/MX/NS/PTR).
- **`agent.*` tools** — `agent.spawn/list/read_messages/send_message/cancel`
  wired through `tool.AgentFleetProvider` → `FleetBridge`. Native `spawn_agent`
  remains as the authoritative SubagentEvent path; `agent.spawn` is the
  wasm-backed surface.

### Plugin distribution (EP-0039 follow-ups)

- **Remote install** — `stado plugin install github.com/owner/repo@vX.Y.Z`.
  Three-tier resolution: GitHub release artefact → raw tree fetch → source build
  (deferred). Lock file written on success.
- **`plugin update [--check]`** — drift check / re-pin against lock entries.
- **`plugin verify-installed`** — re-verify signatures of installed plugins.
- **Project-walking lock file** — `.stado/plugin-lock.toml` discovered up the
  directory tree.

### Plugin examples

- **`plugins/examples/browser`** — Tier 1 + 2 wasm browser sample.
- **`plugins/examples/image-info`** — image metadata wasm sample.
- **`plugins/examples/ls`**, **`plugins/examples/mcp-client`**, others.

## v0.33.0 — Tool dispatch, ABI v2, plugin distribution, supervisor lane, browser

### Tool dispatch and operator surface (EP-0037)

- **Wire-form naming** — canonical dotted (`fs.read`) + wire `__` form (`fs__read`).
  21-entry frozen category taxonomy, validated at `plugin install` time.
- **Four meta-tools** — `tools__search/describe/categories/in_category` as
  non-disableable dispatch kernel. `tools.describe` activates non-autoloaded schemas
  into the session surface.
- **Autoload dispatch** — only the autoloaded core hits the turn's tool surface
  (default: `read/write/edit/glob/grep/bash` + kernel). Configurable via
  `[tools.autoload]` or `--tools-autoload`.
- **`stado tool ls|info|cats|reload`** subcommand.
- **CLI** — `--tools-whitelist`, `--tools-autoload`, `--tools-disable`.
- **TUI** — `/tool ls|info|cats|reload`, `/session list|show|attach|detach`.

### ABI v2 and bundled wasm tools (EP-0038)

- **New host imports** — `stado_proc_spawn/read/write/wait/kill/close`,
  `stado_exec`, `stado_bundled_bin`, `stado_fs_read_partial` (offset/length partial
  read, D24), `stado_dns_resolve`, `stado_hash/hmac` (md5/sha1/sha256/sha512),
  `stado_compress/decompress` (gzip/zlib). Handle registry (32-bit, collision check).
- **Wasm migration** — bundled wasm plugins: `fs`, `shell`, `rg`, `readctx`,
  `agent`. Per-tool parity flags (`[runtime.use_wasm.*]`). `fs` and `shell` parity
  tests pass. `ApplyWasmMigration()` activated from `BuildExecutor`.
- **Agent surface** — `FleetBridge` + `stado_agent_*` host imports. `FleetBridgeAdapter`
  wraps existing Fleet + SubagentRunner. `agent.spawn/list/read_messages/send_message/cancel`.
- **Sandbox wrap-mode** — `[sandbox] mode = "wrap"` re-execs under
  bwrap/firejail/sandbox-exec. `[sandbox.wrap]` config for binds and network.
  `--with-tool-host` deprecated (ToolHost always wired).
- **TUI** — `/ps`, `/top`, `/kill`, `/stats`, `/sandbox`, `/config`. `[YOU]` marker
  on operator messages in multi-producer sessions.
- **`/session attach` RW** — inject messages into a running agent session.

### Plugin distribution and trust (EP-0039)

- **VCS identity** — `github.com/owner/repo[@subdir]@vX.Y.Z`. Floating refs
  rejected. `plugin install` validates semver/SHA format.
- **Anchor-of-trust** — per-owner TOFU via `AnchorTrustStore`.
- **Lock file** — `.stado/plugin-lock.toml` per project.
- **SHA256 drift detection** — auto-reinstall on wasm sha256 change. `--force` flag.
- **Quality pass** — `plugin trust --pubkey-file`, `plugin use <name>@<ver>`,
  `plugin dev <dir>` (one-step authoring loop).

### Supervisor lane (EP-0033)

- **`[supervisor] enabled`** — when on, input during a streaming turn is classified
  before queuing: questions → `tools.btw` answer, steer phrases → guidance note,
  interrupt phrases → cancel worker.
- **`/supervisor on|off|status`** TUI slash commands.

### Security-research harness (EP-0030)

- **`stado run --mode security`** — activates recon-first discipline, abusability
  filter (PoC-or-it-didn't-happen), candidate vs verified split, engagement folder
  conventions in the system prompt.
- **`stado harness init`** — creates `notes/engagements/` and
  `.stado/harness/security.md` (customisable prompt override).

### Browser plugin

- **`plugins/default/browser`** — two-tier browser, auto-available to all models.
  - **Tier 1** (no deps): `browser_open`, `browser_click`, `browser_query` — HTTP
    fetch, cookie jar, Chrome/Firefox/Safari headers. `needs_js: true` escalation hint.
  - **Tier 2** (requires `chromium`/`google-chrome`): `browser_cdp_open`,
    `browser_cdp_navigate`, `browser_cdp_eval`, `browser_cdp_screenshot`,
    `browser_cdp_click_element`, `browser_cdp_type`, `browser_cdp_scroll`,
    `browser_cdp_close` — real headless Chrome, full JS, real DOM events, keyboard,
    scroll triggers. Anti-detection: `--disable-blink-features=AutomationControlled`.

### Infra / Fixes

- **Makefile** — `GOTMPDIR` redirected off `/tmp` to avoid per-user quota.
- **EP-0032** (ACP client) — phases A+B marked Implemented.

## v0.32.0 — /loop, /monitor, stado schedule, .stado/ project dir, sampling args

### TUI

- **`/loop [duration] <prompt>`** — repeat a prompt automatically. Immediate-repeat
  (fires as soon as each turn finishes) or timed (`/loop 5m "check deploy"`). Agent
  self-terminates by including `[LOOP_DONE]` in its response; operator cancels with
  `/loop stop`. Status bar shows `↻ loop (5m)` while active. EP-0036.

- **`/monitor <cmd>`** — stream a process's stdout into the current session as
  `[monitor]` system blocks. Each stdout line is injected as a notification so the
  agent can react to log events, CI output, or any live stream. `/monitor stop` kills
  the background process. EP-0036.

### CLI

- **`stado schedule`** — persistent scheduled runs. `create --cron "0 9 * * *"
  --prompt "..."` persists entries to `<state-dir>/schedules.json`. Subcommands:
  `list`, `rm`, `run-now`, `install-cron`, `uninstall-cron`. `install-cron` writes
  OS crontab entries for all active schedules; `uninstall-cron` removes them. No
  daemon required — OS cron handles timing. EP-0036.

- **`stado run --temperature / --top-p / --top-k`** — one-shot sampling overrides.
  Also configurable in `config.toml` (or `.stado/config.toml`) under `[sampling]`.
  Wired into TurnRequest for both TUI and headless AgentLoop. Zero/nil = provider
  default. EP-0036.

### Config

- **`.stado/` project-local directory** — commit stado config alongside the repo.
  Three artefacts: `.stado/config.toml` (overlays user config, project wins),
  `.stado/AGENTS.md` (stado-specific agent instructions, sits between `AGENTS.md`
  and `CLAUDE.md` in the walk), `.stado/plugins/` (project-local plugin search
  dir, supplements global state-dir). Discovery walks cwd upward; nearest wins.
  New helpers: `Config.ProjectStadoDir()`, `.ProjectPluginsDir()`, `.AllPluginDirs()`.
  EP-0035.

### Plugins

- **`plugins/examples/http-session`** (Go, ~3.5 MB) — reusable HTTP session wrapper
  on top of `stado_http_request`. Cookie jar + default-header merging + base-URL
  resolution across tool calls. State persisted to disk (wasm instance freshness).

- **`plugins/examples/encode-zig`** (Zig, ~5 KB) — base64/base64url/hex/url/html
  encode+decode. Zig SDK proof: 700× smaller than the Go equivalent; documents the
  2 MiB arena constraint for non-Go plugin authors.

- **`plugins/examples/hash-id-rust`** (Rust, source-only) — hash identification,
  17 types, `#![no_std]`, `wasm32-unknown-unknown`. Rust SDK proof; builds when
  `rustup target add wasm32-unknown-unknown`.

### Fixes

- `stado --version` now shows a readable string across all build paths: `make build`
  → `v0.31.0-N-gabcdef-dirty` (git describe); `go install ...@tag` → `vX.Y.Z`;
  bare `go build` → Go's pseudo-version; fallback → `0.0.0-dev+<hash>`.

## v0.31.0 — net:http_request_private opt-in for lab IPs

### Plugin host imports

- **`net:http_request_private` capability + `tool.HostNetworkPolicy`
  interface.** Loosens the `stado_http_request` dial guard to permit
  RFC1918, loopback, link-local, and CGNAT destinations when the
  manifest declares the cap. Multicast, unspecified, IPv4/IPv6
  reserved, and documentation ranges remain refused — those are
  never valid HTTP destinations regardless of policy. The strict
  public-only path is still the default; opt-in is per-plugin and
  visible in the manifest. Implemented via type-assertion on
  `tool.Host`: hosts return true from `AllowPrivateNetwork()` to
  flip the dial guard. Tests cover cap-granted-loopback-allowed,
  cap-denied-loopback-blocked, and cap-granted-multicast-still-
  refused.

## v0.30.0 — net:http_request capability

### Plugin host imports

- **New `net:http_request` capability + `stado_http_request` host
  import.** Generic HTTP client (GET / POST / PUT / DELETE / PATCH /
  HEAD) with custom request headers and request body. Replaces the
  GET-only / markdown-converting shape of `stado_http_get` for
  plugins that need to drive REST APIs (auth headers, JSON bodies,
  status codes other than 200, response headers like
  `Set-Cookie`). Capability surface: `net:http_request` (broad,
  any public host) and `net:http_request:<hostname>` (narrow,
  per-host allowlist). Request/response bodies are base64 in/out
  for binary-safe JSON transport. Same private-network dial guard
  as `stado_http_get` (RFC1918 / loopback / link-local refused
  before TLS handshake); a future `net:http_request_private` cap
  will gate lab-IP access for plugins that need it. The new
  allowlist (`Host.NetReqHost`) is kept separate from the
  existing `Host.NetHost` (http_get's allowlist) so a manifest
  declaring only one method's hosts can't reach the other.

### Plugins

- **`exec:pty` capability + nine new host imports for persistent
  shell sessions.** `stado_pty_create / list / attach / detach /
  write / read / signal / resize / destroy` expose a runtime-shared
  PTY registry (`internal/plugins/runtime/pty/Manager`) that survives
  plugin instantiation freshness — sessions created in one tool call
  remain reachable from later calls. Per-session ring buffer (default
  64 KiB, configurable 4 KiB-4 MiB, terminal-scrollback semantics)
  captures output while detached so reattach replays the backlog.
  Single-attach-at-a-time per session with `force=true` recovery
  path for "previous attacher crashed without detaching". Reaper hook
  on `Runtime.Close` cleans up orphans.

- **New example plugin: `persistent-shell-0.1.0`.** Wraps the
  `exec:pty` host imports as nine plugin tools (`shell_create`,
  `shell_list`, `shell_attach`, `shell_detach`, `shell_write`,
  `shell_read`, `shell_signal`, `shell_resize`, `shell_destroy`).
  Replaces the fresh-process-per-call shape of `stado_exec_bash` for
  workflows that need interactive stdin/stdout across tool calls —
  driving `ssh` sessions, watching `nc` listeners, running
  `msfconsole` step-by-step, attaching to long-running TUIs.
  Base64-or-string data wire format supports both UTF-8 commands and
  raw bytes (Ctrl-C, terminal escape sequences). Minimal Go-→-wasm
  plugin modeled after `webfetch-cached`. See
  `plugins/examples/persistent-shell/README.md` for workflow
  patterns.

### Providers

- **ACP client — wrap external coding-agent CLIs as stado providers
  (phase A of EP-0032).** stado already speaks ACP as a server (Zed
  drives stado). This is the inverse: stado as ACP **client** wrapping
  an external CLI (`gemini --acp`, `opencode acp`, future
  zed-compatible variants). Configure via:

  ```toml
  [acp.providers.gemini-acp]
  binary = "gemini"
  args   = ["--acp"]
  ```

  Then `stado run --provider gemini-acp --prompt "..."` (or pin in
  `[defaults].provider`). The wrapped agent uses ITS OWN tools
  — phase A's deliberate scope. Phase B will add opt-in tool-host
  capability so wrapped agents can call stado's tool registry; phase
  C adds per-call hybrid. Audit boundary: stado records the
  conversation boundary, not the wrapped agent's internal tool calls
  — that's the trust boundary when handing off to a third-party
  agent. End-to-end tested with `gemini --acp` on a real Google
  account; `opencode acp` should work too (same canonical
  Zed-spec dialect). EP-0032 has the full design + decision log.

### CLI

- **Added `stado integrations`** — detect external coding-agent CLIs
  (claude / gemini / codex / opencode / zed / aider) installed on the
  current host and report what protocols each speaks (ACP / MCP).
  Scans PATH for known binaries + HOME / XDG_*_HOME for config dirs;
  per-agent version probe with a 2s sub-timeout so a hung CLI doesn't
  stall the whole sweep. `--json` emits structured output for piping
  to `jq`. Backed by a new `internal/integrations/` registry — adding
  a new known agent is a one-place change. Same registry surfaces
  detected agents under "Agent: <name>" rows in `stado doctor`.
  Foundation for the future ACP-client-driven dispatch features.
  Operator request.

- **Per-direction token budgets.** `[budget]` accepts the new
  `warn_input_tokens` / `hard_input_tokens` / `warn_output_tokens` /
  `hard_output_tokens` alongside the just-introduced combined
  `warn_tokens` / `hard_tokens`. Output tokens are 3–5× pricier
  than input on most paid providers — an output-only cap is the
  cheap way to constrain spend without restricting context;
  conversely an input-only cap bounds context-window growth without
  limiting generation length. Every cap fires independently;
  whichever crosses first aborts. Agent loop emits per-direction
  telemetry (`turn.tokens_in`, `turn.tokens_out`,
  `loop.cumulative_tokens_in/out`). TUI status pill prefers USD →
  combined → input → output. Operator request — refines the
  combined-cap addition from the same iteration.

- **Added `stado config providers`** — list the bundled provider
  catalogue (3 native + 7 OAI-compat cloud + 4 OAI-compat local)
  with per-provider API-key status (✓ set / ✗ unset) and the
  endpoint each preset points at. `stado config providers setup
  <name>` prints copy-pasteable setup steps; `--write` adds the
  `[inference.presets.<name>]` block to config.toml. Reuses the
  existing `internal/config/provider_registry.go` so adding a new
  provider is a one-place change. Operator request.

- **`[budget]` accepts token thresholds**: `warn_tokens` and
  `hard_tokens` mirror the existing `warn_usd` / `hard_usd`. The
  agent loop returns `ErrTokenCapExceeded` once cumulative
  input+output tokens cross `hard_tokens`; the TUI status pill
  shows `budget 12.3k/100k tok` while between warn and hard.
  Both pairs are independent — set USD-only, tokens-only, both,
  or neither. Useful for local-runner setups (Ollama / LM Studio
  / vLLM) where CostUSD is always zero and the meaningful budget
  is throughput, not dollars. Operator request.

- **Added `stado run --no-turn-limit`** — disables the max-turn
  cap so the agent loop runs until no tool calls remain or the
  context is cancelled. Useful for long-running multi-step tasks
  where the turn cap is the wrong control surface (prefer budget
  caps or context timeouts). Beats `--max-turns` when both set.
  Operator request.

### TUI

- **Animated terminal-tab title.** While the agent is busy
  (streaming or compacting) the OSC 0/2 window title cycles a
  braille spinner glyph: `⠋ stado` → `⠙ stado` → ...; idle
  resets to `stado`. Many emulators (kitty, alacritty, iTerm,
  Ghostty, Windows Terminal, GNOME Terminal) render the title in
  the tab strip, so users get visual "I'm working on it" feedback
  even when they've switched windows. Polled at 5fps via
  `tea.SetWindowTitle`; OSC sequences are deduped so we don't
  spam the terminal when nothing's changed. Operator request.

- **Wider command palette** so long descriptions don't wrap as the
  user moves through the list. Modal scales to 2/3 of screen,
  clamped [64, 110] cols (was [48, 80]); inline cap raised 88 →
  110. Operator-feedback report.

- **Drop category headers from the slash-command popup while
  filtering.** Categories ("Quick", "Session", "View") help
  orient when browsing the full list (empty query) but add
  clutter when the user is searching for a specific command.
  Now: filtered → flat list of matches; browsing → grouped by
  category. Operator-feedback report.

### Plugins

- **`stado plugin run --with-tool-host` now supports `exec:bash`
  plugins** under `sandbox.Detect()` (bwrap on Linux, sandbox-exec
  on macOS) — the same runner the agent loop uses. v0.26.0 refused
  unconditionally because no Runner was wired in; v0.27.0 narrows
  the refusal to: *manifest declares `exec:bash` AND no native
  sandbox is available* (NoneRunner). EP-0005 is preserved — we
  don't substitute the operator's CLI invocation for a real
  syscall/file-access filter, we just stop refusing cases where a
  real one IS available. Resolves EP-0028 D1.

### Fixes

- **Completed the Atomic Fedora boot fix — pass 3 (full audit).** Pass 2
  (v0.26.2) added a multi-probe regression test that surfaced the
  audit-key + sidecar wall. A static audit of every remaining strict
  from-`/` strict-walk caller surfaced 11 more boot-time HOME-rooted
  paths spread across 10 files: config.toml read (loaded only when a
  config exists, so the empty-namespace test missed it), session
  worktree mkdir + open, materialize tree mkdir + open + wipe,
  conversation worktree open, tasks store mkdir + open, theme TOML read,
  model recents mkdir + open, instructions walk-up read,
  binext cache dir mkdir + open, traceparent write + read, session-fork
  worktree mkdir, plugin install destination mkdir + open. Migrated all
  to the trust-anchor variants (same threat model as v0.26.1/v0.26.2).
  Extended the regression test with `config show` (exercises the
  load-existing-config path that fired on real users with config.toml
  present) and added `XDG_CACHE_HOME` to the namespace setenv block so
  binext probes resolve the right anchor. The test now runs 5 probes
  end-to-end and verifies all reach the application logic past every
  known boot-time MkdirAll/OpenRoot/Read wall.

- **Completed the Atomic Fedora boot fix — pass 2.** v0.26.1's
  `hack/test-on-fedora-atomic.sh` test harness only exercised
  `stado config-path`, which leaves three more boot-time surfaces
  unchecked. Fanning the test out to `doctor --no-local --json`,
  `session list`, and `audit verify` surfaced two more strict
  from-`/` walks: `internal/audit/key.go` (the audit signing key
  load+create path, ~`.config/stado/audit/...`) and
  `internal/state/git/sidecar.go` (the sidecar bare repo init +
  alternates dir, under `~/.local/state/stado/sessions/`). Both
  trip on Atomic's `/home → /var/home` symlink whenever a normal
  user runs anything that signs a commit or enumerates sessions —
  i.e. nearly every real workflow. Migrated to the trust-anchor
  variants (`ReadRegularFileUnderUserConfigLimited` /
  `OpenRootUnderUserConfig` / `MkdirAllUnderUserConfig`); same
  threat model as v0.26.1.

- **Reworked `hack/test-on-fedora-atomic.sh` as a multi-probe
  regression suite.** The script now runs four boot-touching
  probes in the bwrap namespace and reports per-probe PASS/REGRESSION,
  so partial regressions surface specifically. Adding new probes
  is one line in the `PROBES=()` array. `make fedora-atomic-test`
  is the entry point.

- **Completed the Atomic Fedora `/home → /var/home` boot fix.** v0.26.0
  migrated three call sites (`config dir`, `audit key dir`, `worktree
  root`) from the strict from-`/` `MkdirAllNoSymlink` to the
  trust-anchor-aware `MkdirAllUnderUserConfig`. Four more call sites in
  `internal/config/config.go` still walked from `/` and tripped the
  same wall: the system-prompt-template ensure (`MkdirAllNoSymlink` →
  `MkdirAllUnderUserConfig`), the system-prompt-template root opener
  (`OpenRootNoSymlink` → `OpenRootUnderUserConfig`), and two read-paths
  for the system-prompt template (a new
  `ReadRegularFileUnderUserConfigLimited` helper, mirroring the
  existing `Mkdir`/`OpenRoot` wrappers). On Atomic the v0.26.0 binary
  booted via `--version` (which prints before any FS work) but failed
  on `stado config-path` and any normal startup that triggered
  `config.Load()`. Added `hack/test-on-fedora-atomic.sh` — a
  `bwrap`-based regression test that simulates the `/home → /var/home`
  symlink layout — plus a `make fedora-atomic-test` target. Threat
  model unchanged: symlinks ABOVE the trust anchor are tolerated as
  operator-supplied OS layout, symlinks UNDER the anchor are still
  rejected by the strict `OpenRootNoSymlinkUnder` walk; see EP-0028.

### CLI

- **Added `stado run --quiet`.** Suppresses `▸ tool(args)` preview lines
  on stdout in non-JSON mode. Tools still execute and still commit to
  the audit log; only the inline preview is elided. Pairs with `--json`
  for scripted use: `--json` for structured event-per-line output, or
  `--quiet` for plain text-only stdout. Dogfood-note item from the
  htb-writeups workflow integration: `stado run --tools` interleaved
  agent text with tool-call previews and INFO log lines, making it
  hard to pipe to `jq` or post-process.
- **Updated `stado run --help` body** to explicitly call out `--json`
  (preferred for scripted use) and `--quiet` (suppress tool-call
  previews). Same dogfood note: `--json` was discoverable via flag
  description but not the command's `Long` description body, so users
  who only read the body missed the canonical scripted-parse mode.
- **`stado doctor` auto-skips local-runner probe when `[defaults].provider`
  pins a remote provider.** Probing four `localhost:*` ports each with
  a 1s TCP timeout adds ~4s on machines without any local runners; the
  probe is informational, not a blocker, so when the user has explicitly
  pinned `provider = "anthropic"` (or any other remote provider, including
  an OAI-compat preset whose endpoint resolves non-local), `doctor` now
  skips the probe and prints a `Local probe: skipped (...)` annotation
  row instead. Explicit `--no-local` still works as before; `provider = ""`
  (auto-detect mode) still triggers the probe. Dogfood-note item.

- **Added `stado --version`.** The `stado version` subcommand has long
  printed `collectBuildInfo().Version`; cobra's standard `--version`
  global flag is now wired to the same source so both surfaces agree.
- **Added `stado plugin run --workdir <path>`.** Lets the operator
  override the plugin's `host.Workdir` (the path that `fs:read:.` /
  `fs:write:.` capabilities and relative file paths resolve against).
  Default unchanged: install dir, for backward compatibility. Pass
  `--workdir=$PWD` when the plugin is meant to read files from the
  operator's repo (the common case for project-specific plugins).
  EP-0027 documents the rationale.
- **Added `stado plugin run --with-tool-host`.** Wires `host.ToolHost`
  so plugins that import bundled tools (`stado_http_get`,
  `stado_fs_tool_*`, `stado_lsp_*`, `stado_search_*`) can be exercised
  end-to-end from the CLI. Without this, `tool_imports.go` returned
  the documented "plugin host has no tool runtime context" error and
  net/exec/lsp paths were only reachable via `stado run`. Refuses
  plugins that declare `exec:bash` because the `sandbox.Runner` the
  agent loop normally provides is not available here — those need to
  run via `stado run` (EP-0005 forbids substituting human approval
  for runtime policy). EP-0028 walks through the design.
- **Added `stado plugin gc [--keep N] [--apply]`.** Sweeps older
  installed plugin versions per (signer fingerprint, manifest name)
  group, keeping the `--keep` newest (default 1). Dry-run by default;
  `--apply` actually deletes. Trust-store entries and rollback pins
  are deliberately untouched, so a freshly-deleted older version
  still cannot be reinstalled. Solves the "`plugin installed` shows
  `htb-cve-lookup-0.1.0`, `-0.2.0`, `-0.3.0` after enough iteration"
  authoring-loop pain.
- **Added `stado plugin doctor <id>`.** Inspects an installed
  plugin's manifest, classifies each declared capability, and prints
  a per-surface compatibility table with the exact `plugin run`
  flag combination needed (or "use the TUI / `stado run`" when the
  plugin requires the full agent loop). Closes the
  "`stado_http_get returned -1` — now what?" first-time-author
  loop: doctor explains which knob to flip without making the
  operator read the source.
- **`--provider` and `--model` flags on every command.** Pass
  `stado --provider ollama-cloud --model kimi-k2.6` (or any subcommand)
  to override `[defaults].provider` / `[defaults].model` for one
  invocation without editing config.toml or pre-exporting
  `STADO_DEFAULTS_*`. Shipped as persistent root flags so `stado run`,
  `stado` (TUI), and other subcommands all honour them.

### Plugins

- **`internal/workdirpath` exports `LooksLikeRepoRoot`,
  `FindRepoRoot`, `FindRepoRootOrEmpty`** as the single source of
  truth for "what counts as a git working tree". The predicate now
  rejects empty `.git/` directories (which previously fooled the
  walker into returning the wrong repo root); every git tree must
  have a HEAD file or a gitfile pointer to be accepted. The 6 inline
  walkers across `cmd/stado/`, `internal/runtime/`, and
  `internal/memory/` now delegate to the shared helper. EP-0027.
- **New bundled example: `plugins/examples/webfetch-cached/`.**
  Drop-in replacement for the bundled `webfetch` tool that adds a
  SHA-256-keyed disk cache. Demonstrates three v0.26.0 plugin-surface
  features in one ~140-line plugin: wrapping a bundled-tool host
  import (`stado_http_get` via `--with-tool-host`), workdir-rooted
  fs capabilities (`fs:read:.cache/stado-webfetch` via `--workdir`),
  and `[tools].overrides` for transparent bundled-tool replacement.
  Solves the "Anthropic WebFetch hard-codes a 15-min TTL" friction
  documented in the round-1 dogfood notes.
- **New `cfg:*` capability vocabulary — first concrete capability
  `cfg:state_dir`.** Introduces a read-only configuration-introspection
  surface for plugins. The `cfg:state_dir` capability gates the
  `stado_cfg_state_dir(buf, cap) → int32` host import, which writes
  the operator's stado state-dir path (`$XDG_DATA_HOME/stado/` or
  fallback) into the caller's buffer. Unblocks the lean-core
  migration of operator-tooling commands (`plugin doctor`,
  `plugin gc`, future `plugin info`) from `cmd/stado/` to bundled
  plugins under `plugins/default/` — those tools all need to learn
  the install dir at `<state-dir>/plugins/`. EP-0029 documents the
  capability vocabulary; future `cfg:config_dir`, `cfg:worktree_dir`,
  etc. follow the same per-field opt-in pattern (no globs).
- **`fs:read:cfg:state_dir/...` path-templating in fs caps.**
  Direct extension of the `cfg:*` vocabulary: manifest fs caps
  can now reference cfg values inline as
  `fs:read:cfg:state_dir/plugins`, `fs:write:cfg:state_dir/scratch`,
  etc. Resolution is at-check time against `host.StateDir`. The
  matching `cfg:state_dir` cap MUST also be declared — missing
  cap, empty value, or unknown cfg name all fail-closed (silently
  filter to no-match → access denied). Unblocks portable plugin
  manifests for any operator-tooling that reads under the state
  dir; before this, plugins had to either hardcode an absolute
  path per-operator or shift the resolution onto the `--workdir`
  invocation flag. EP-0031 walks through the templating shape
  and fail-closed semantics. New bundled examples
  (`plugins/examples/state-dir-info/`,
  `plugins/examples/webfetch-cached/`) demonstrate the cfg:* +
  workdir-rooted patterns.

### Inference

- **Custom OAI-compat presets can now declare an API-key env var.**
  `[inference.presets.<name>].api_key_env = "FOO_API_KEY"` plumbs the
  named env var through the OAI-compat client. When unset, custom
  preset names fall back to `STADO_PRESET_<UPPER>_API_KEY` (hyphens
  normalized to underscores). Previously only the hardcoded list of
  builtin preset names (litellm, groq, etc.) could pick up a key, so
  user-defined presets like `ollama-cloud` would 401.
- **`ollama-cloud` is now a builtin preset.** `[defaults].provider =
  "ollama-cloud"` resolves to `https://ollama.com/v1` with
  `OLLAMA_CLOUD_API_KEY` as the default credential env. No more
  litellm-aliasing workaround required.
- **Anthropic auto-raises `max_tokens` when thinking is enabled.**
  Anthropic enforces `max_tokens > thinking.budget_tokens`. The default
  thinking budget (16K) used to exceed the default `max_tokens` (8K)
  and surface as a 400 from the provider. Stado now widens
  `max_tokens` to `budget + 1024` whenever the caller's ceiling is
  smaller, while never lowering an explicit larger ceiling.

### Fixes

- **`stado` no longer fails to boot when `/home` is a symlink.**
  `MkdirAllNoSymlink` walks from `/` and rejects any symlink in any
  path component — too strict for HOME-rooted system paths on
  Fedora Atomic / Silverblue (`/home` → `/var/home`) and similar
  setups. New helpers `workdirpath.MkdirAllUnderUserConfig` /
  `OpenRootUnderUserConfig` anchor the no-symlink walk at the
  operator's `XDG_*_HOME` / `HOME` environment, so OS-level
  symlinks ABOVE the trust anchor are accepted while symlinks
  WITHIN user space are still rejected. 13 HOME-rooted call sites
  (config dir, state dir, worktree dir, audit keys, memory store,
  plugin install / state files) migrated to the new helpers; the
  strict `MkdirAllNoSymlink` stays for genuinely-untrusted callers
  (in-repo sandbox writes from inside plugin host imports). EP-0028.
- **Empty `/tmp/.git/` no longer fools session GC + lesson document
  paths.** A stray empty `.git/` directory in any parent of CWD was
  enough to make `findRepoRoot` (and its 5 cousins) return the wrong
  path, silently re-pinning sessions and lesson document target dirs
  to that bogus parent. Production code path observed: a user who
  ran `stado run --prompt …` from `/tmp/myproject` (no real `.git`)
  would get sessions pinned to `/tmp` if anything else had previously
  created `/tmp/.git/`. Test impact: this fixed
  `TestSessionGC_ApplyActuallyDeletes` and four `TestLearningCLI_*`
  tests that had been failing on `main` in any CI/dev environment
  with `/tmp/.git/` pollution.
- **Warn when a stale `system-prompt.md` template drops
  `{{ .ProjectInstructions }}`.** When `AGENTS.md` / `CLAUDE.md` is
  loaded but the active template doesn't reference the
  `ProjectInstructions` field, stado now prints a stderr advisory
  pointing at both files so the user can re-add the block (or delete
  the template to regenerate the default). Without this, project rules
  could silently fail to reach the model on installs predating the
  template's `ProjectInstructions` hook.
- **Validated pinned plugin pubkeys.** Manifest verification now re-derives
  the stored signer pubkey fingerprint before trusting a pinned entry, so
  malformed trust-store records cannot authorize the wrong key.
- **Validated OpenAI-compatible endpoints.** Custom OAI-compatible endpoints
  now must use HTTP(S), include a host, and avoid URL-embedded credentials.
- **Hardened prompt and symbol reads.** Project instruction files, project
  skill files, and TUI symbol scans now read regular files through the
  no-symlink opener so repo-controlled symlinks cannot redirect prompt or
  symbol discovery.
- **Capped prompt and template reads.** Project instruction files, project
  skills, system prompt templates, theme files, TUI template overlays, symbol
  scans, and audit key loads now reject oversized regular files before
  parsing.
- **Capped config loads.** `config.toml` now loads through a bounded
  no-symlink reader instead of the koanf file provider, rejecting oversized or
  symlinked user config files before TOML parsing.
- **Capped bundled binary cache verification.** Bundled tool cache hits now
  require an exact byte-size match and hash through a bounded reader before
  reusing an existing executable.
- **Capped plugin install copies.** Plugin installs now reject oversized
  package files during rooted directory copies and remove partial destinations
  if a source grows past the copy ceiling.
- **Bounded plugin install package walks.** Plugin installs now stream package
  directory entries in batches and reject packages that exceed entry-count or
  nesting-depth limits.
- **Bounded installed-plugin listing.** CLI, headless, and TUI plugin listing
  now stream installed-plugin directory entries in batches and reject oversized
  state directories.
- **Capped sidecar tree blob writes.** Session tree snapshots now reject
  oversized worktree files and detect regular files that change size while
  being streamed into sidecar git objects.
- **Bounded sidecar tree snapshot walks.** Session tree snapshots now stream
  directory entries in batches and reject worktrees that exceed entry-count or
  nesting-depth limits.
- **Capped self-update fallback copies.** Cross-device self-update installs
  now reject oversized replacement binaries before streaming through the
  atomic copy fallback.
- **Capped sidecar tree materialization writes.** Materialization now rejects
  oversized regular blobs before writing them back to a worktree and removes
  partial files if a blob stream exceeds the write ceiling.
- **Bounded sidecar tree materialization walks.** Materialization now rejects
  sidecar trees that exceed entry-count or nesting-depth limits before the
  restore path can grow unbounded worktree state.
- **Bounded sidecar materialization cleanup walks.** Replacing or zero-tree
  materialization now streams cleanup discovery in batches and fails before
  deletion if stale worktree traversal exceeds entry-count or depth limits.
- **Bounded worktree session listing.** CLI and TUI session enumeration now
  stream worktree-root entries in batches and reject oversized session state
  directories.
- **Bounded grep tool walks.** The in-process grep tool now streams rooted
  directory traversal in batches and rejects oversized walk depth or entry
  counts before scanning files.
- **Bounded skill discovery.** Project skill loading now streams `.stado/skills`
  directory entries through rooted no-symlink handles and rejects oversized
  skill directories before parsing files.
- **Bounded read-context package discovery.** The `read_with_context` tool now
  streams local Go package directory entries in batches and skips import
  packages that exceed the package-entry cap.
- **Bounded TUI repo scans.** TUI document and symbol pickers now stream repo
  traversal in batches and stop on entry-count or nesting-depth limits.
- **Bounded TUI file picker scans.** The `@` file picker now streams repo file
  discovery in batches and stops on traversal entry-count or depth limits.
- **Bounded TUI template discovery.** Bundled and overlay template discovery
  now reads template directories in batches and rejects oversized template
  entry sets before parsing.
- **Capped model-list decoding.** Local provider detection and
  OpenAI-compatible capability probes now reject oversized model-list
  responses before decoding JSON.
- **Capped self-update release metadata.** Self-update now rejects oversized
  GitHub release API responses before decoding release JSON.
- **Bounded glob tool expansion.** The in-process glob tool now walks through
  rooted bounded traversal, skips symlink directory traversal, and stores only
  the output-budgeted matches while counting total matches.
- **Capped command output capture.** Hooks, bash, ripgrep, and ast-grep now
  capture child-process stdout and stderr through bounded buffers instead of
  unbounded in-memory buffers.
- **Capped command probe captures.** TUI git status checks and Linux pasta
  capability probes now avoid unbounded `Output`/`CombinedOutput` captures.
- **Capped LSP frame reads.** LSP message framing now rejects oversized header
  lines, header blocks, and message bodies before allocation.
- **Capped tool-call inputs.** Providers and the tool executor now reject
  oversized function-call argument payloads before accumulating or replaying
  them into tool execution.
- **Reduced streamed reasoning memory growth.** Anthropic streaming now emits
  thinking and signature deltas without duplicating them in provider-local
  buffers.
- **Hardened TUI stream errors.** Provider stream errors now put the TUI into
  an error state instead of letting partial assistant turns complete normally.
- **Capped direct tool dispatch inputs.** Registry, MCP-server, and plugin
  adapter paths now share the tool-call input ceiling before dispatch.
- **Capped MCP bridge and plugin-run payloads.** Remote MCP tool text is now
  output-budgeted before entering model context, and one-shot plugin runs
  reject oversized JSON arguments before starting a wasm runtime.
- **Capped plugin tool results.** External plugin tool content and tool-side
  errors now share a model-context output budget after the wasm ABI call.
- **Capped streamed assistant output.** Runtime and TUI streams now reject
  oversized assistant text or thinking deltas before unbounded accumulation.
- **Capped subagent and LSP tool results.** `spawn_agent` results and LSP
  lookup output now share explicit model-context budgets before being returned
  to the parent model.
- **Capped task picker input buffers.** Task search, title, and body editing
  now stop at bounded byte limits before oversized pasted input can grow TUI
  memory.
- **Capped TUI prompt input.** The main prompt editor now enforces a byte
  ceiling before oversized pasted input can grow draft and history buffers.
- **Capped memory append logs.** Persistent memory payloads, events, and log
  files now enforce byte ceilings before append or replay.
- **Capped TUI picker inputs.** Agent, model, session, theme, and slash-command
  pickers now bound pasted query and rename input before fuzzy matching.
- **Capped conversation append logs.** Conversation JSONL records and total log
  size are checked before append, with final symlink/non-regular files rejected.
- **Hardened plugin state reads.** Plugin trust and revocation state files now
  reject final symlinks before opening cached JSON.
- **Blocked private webfetch targets.** The `webfetch` tool now denies
  loopback, private, link-local, and reserved IP targets at dial time.
- **Filtered external LSP locations.** LSP definition/reference results now
  drop paths outside the active workdir before rendering tool output.
- **Validated plugin run IDs.** TUI `/plugin:<id>` invocations and tool
  override plugin references now reject traversal before resolving plugin
  directories.
- **Validated background plugin IDs.** TUI background-plugin config entries now
  use the installed-plugin path guard before manifest loading.
- **Preserved plugin rollback pins.** Re-trusting an existing plugin signer now
  keeps its last verified version so inline `plugin install --signer` cannot
  reset rollback protection.
- **Made plugin TOFU pinning atomic.** Inline plugin signer pins are now saved
  only after the signer matches and verifies the manifest, avoiding trust-store
  pollution on failed installs.
- **Streamed task store JSON I/O.** Task store loading and saving now decode
  and encode through the store byte ceiling instead of staging the whole JSON
  document in memory.
- **Streamed git tree materialization.** Session tree materialization now
  streams regular blob contents to destination files, caps symlink blob reads,
  and bounds encoded commit bytes used for SSH signing.
- **Rooted read-context module discovery.** The `read_with_context` tool now
  reads target/import files through workdir-rooted handles and stops Go module
  probing at the tool workdir boundary.
- **Hardened audit key loads.** Existing audit signing keys are now read
  through the no-symlink opener, matching the existing protected key creation
  path.
- **Hardened default system prompt reads.** Auto-managed system prompt
  templates now reject symlinked default files before validation or legacy
  upgrades.
- **Hardened tree blob reads.** Session sidecar tree snapshots now open
  regular file blobs through the no-symlink opener, while preserving symlink
  entries as symlink blobs.
- **Hardened self-update source reads.** Self-update archive extraction and
  cross-device copy fallback now reject symlinked source paths before reading
  release payloads or replacement binaries.
- **Capped self-update downloads.** Self-update checksum manifests, minisig
  signatures, release archives, and extracted binaries now reject oversized
  inputs before unbounded reads or writes.
- **Hardened sandbox proxy CONNECT handling.** The sandbox network proxy now
  rejects malformed CONNECT targets before dialing, applies request-header and
  dial timeouts, and keeps user-controlled text out of status lines.
- **Hardened plugin signing inputs.** Plugin digest and signing commands now
  reject symlinked key, manifest, and WASM source paths before hashing or
  signing plugin artifacts.
- **Capped plugin signing inputs.** Plugin signing now rejects oversized
  manifests and WASM files before hashing or rewriting signed metadata, and
  `plugin digest` applies the same WASM size limit as package verification.
- **Streamed minisign artifact signing.** Release artifact signing now hashes
  files incrementally before writing `.minisig` sidecars instead of reading
  whole artifacts into memory.
- **Capped bundled-binary fetch inputs.** The release helper now uses explicit
  HTTP timeouts and rejects oversized checksum sidecars, release metadata,
  archives, and extracted tool binaries.
- **Bounded file-read tool memory.** The `read` tool now streams full-file and
  ranged reads into the existing output budget instead of loading the whole
  file before truncating.
- **Bounded read-context inputs.** The `read_with_context` tool now enforces a
  hard per-file read ceiling and caps Go import-scan and `go.mod` reads before
  parsing.
- **Capped LSP document opens.** Definition, references, hover, and document
  symbol tools now reject oversized source files before sending document text
  to a language server.
- **Capped edit-tool file loads.** Search/replace edits now reject oversized
  source files and replacement results before loading or writing unbounded
  content.
- **Streamed subagent adoption copies.** Worker adoption now validates child
  file inputs before removing parent targets and streams regular files through
  a capped atomic copy.
- **Capped plugin runtime FS I/O.** Host `fs_read` and `fs_write` now enforce
  path and payload ceilings and read allowed files through bounded
  regular-file reads.
- **Capped plugin host import memory reads.** Runtime host imports now reject
  oversized plugin-controlled strings and byte payloads before copying them
  out of Wasm memory.
- **Capped runtime state reads.** Session metadata and raw conversation log
  reads now verify regular files and enforce byte ceilings before loading
  worktree state into memory.
- **Capped remaining rooted state reads.** Repo pin, config, sidecar
  alternates, TUI model state, git HEAD, and grep reads now use bounded
  regular-file opens instead of direct whole-file root reads.
- **Hardened regular-file open races.** Shared no-symlink regular-file opens
  and plugin package copy reads now verify the opened file still matches the
  pre-open `Lstat` result.
- **Hardened webfetch redirects and reads.** Webfetch now rejects redirects
  that leave the original host and caps raw response reads before markdown
  conversion so plugin host grants cannot be bypassed via cross-host redirects.
- **Capped online plugin metadata responses.** CRL fetches and Rekor online
  checks now reject oversized success bodies before parsing them.
- **Capped plugin package reads.** Plugin manifest, signature, author pubkey,
  and WASM package reads now enforce size limits and verify the opened file
  still matches the pre-open `Lstat` result.
- **Centralized rooted directory reopening.** Plugin capability file access,
  builtin grep traversal, subagent adoption, plugin scaffolding, branch-status
  rendering, release helper writes, and shared workdir file helpers now use the
  no-symlink root opener; direct `os.OpenRoot` use is isolated to that primitive.
- **Hardened explicit output and learning roots.** CLI file-output helpers,
  minisign artifact signing, learning document writes, and learning repo-pin
  reads now reject symlinked parent/root directories before reading or writing.
- **Hardened worktree metadata roots.** Session memory toggles, user-repo pins,
  descriptions, pid files, conversation logs, and traceparent files now reject
  symlinked worktree roots before reading or writing session metadata.
- **Hardened rooted state/cache opens.** Task, memory, plugin-state, model
  state, config, sidecar, bundled-tool cache, plugin scaffolding/install, and
  tree-materialization roots now reopen directories with no-symlink checks.
- **Hardened release helper writes.** The bundled-binary fetch helper now
  creates generated source and asset parent directories through rooted
  no-symlink directory creation.
- **Hardened plugin package verification reads.** Plugin manifest,
  signature, author-pubkey sidecar, and WASM digest reads now reject
  symlinked plugin directory components and package files before
  verification.
- **Hardened destructive directory cleanup.** Session and agent worktree
  deletion, TUI session deletion, failed plugin-install cleanup, and
  zero-tree materialization wipes now reject symlinked directory components
  before removing recursive paths.
- **Rooted memory log reads and appends.** Approved-memory storage now opens
  its append log through `os.Root` scoped to the memory-store directory,
  rejecting symlink escapes for read and append operations.
- **Rooted conversation log access.** Session conversation reads, appends,
  rewrites, and raw-log hashing now stay confined to the session worktree's
  `.stado` directory, rejecting conversation-file and `.stado` symlink escapes.
- **Rooted session metadata files.** Session descriptions and user-repo pins
  now read and write through the session worktree root across runtime, memory,
  and learning paths, rejecting `.stado` and metadata-file symlink escapes.
- **Rooted session pid metadata.** `.stado-pid` reads and writes now stay
  scoped to the session worktree root, preventing pid-file symlink escapes.
- **Rooted traceparent metadata.** Fork traceparent files now read and write
  through the child worktree root, rejecting traceparent symlink escapes.
- **Rooted session memory opt-out metadata.** The per-session memory-disabled
  marker now reads, writes, and removes through the session root, rejecting
  `.stado` symlink escapes.
- **Rooted raw conversation exports.** `stado session export --format jsonl`
  now reads raw logs through the runtime conversation root instead of a direct
  worktree path read.
- **Validated TUI session metadata actions.** Session rename and delete actions
  now use the shared session ID validator, preventing special local IDs from
  writing metadata outside an actual session worktree.
- **Rooted learning document writes.** `stado learning document` now writes
  Markdown notes through rooted `.learnings` handles, rejecting symlink escapes
  before documenting and rejecting a lesson.
- **Rooted session tree materialization.** Fork/revert materialization now
  writes files and directories through a destination root and replaces stale
  destination symlinks instead of following them. Destructive prune/wipe
  cleanup now removes stale paths through the same destination root.
- **Hardened plugin install copies.** Plugin installs now copy package
  contents through rooted source/destination handles, reject destination
  symlinks, and re-check the installed manifest/signature/WASM digest after
  copy so package swaps cannot land unverified bytes.
- **Hardened self-update extraction.** Self-update now writes the release
  binary into its already-open temp file instead of reopening by path and
  rejects tar/zip entries named like the binary unless they are regular files.
- **Hardened plugin state writes.** Plugin CRL and trust-store saves now use
  rooted, exclusive random temp files and reject non-regular state files so
  pre-created temp symlinks cannot redirect writes outside the state directory.
- **Hardened bundled tool cache extraction.** Bundled binary extraction now
  rejects path-like tool names, writes through rooted random temp files, and
  replaces cache symlinks instead of treating them as valid cache hits.
- **Hardened conversation seeding.** Fresh child-session conversation logs now
  seed through rooted, exclusive random temp files so pre-created predictable
  temp symlinks cannot redirect the rewrite.
- **Hardened default prompt template writes.** The automatically managed
  default `system-prompt.md` now creates and upgrades through rooted file
  handles and avoids rewriting legacy defaults through symlinks.
- **Hardened sidecar alternates writes.** Sidecar Git alternates metadata now
  updates through rooted, exclusive random temp files and replaces alternates
  symlinks instead of following them.
- **Hardened session metadata writes.** Session descriptions, repo pins, and
  pid markers now replace through rooted random temp files and reject final
  metadata symlinks instead of following them.
- **Hardened session memory opt-out writes.** The per-session
  memory-disabled marker now writes through rooted random temp files and
  rejects marker symlinks instead of following them.
- **Hardened traceparent writes.** Fork traceparent metadata now writes
  through rooted random temp files and rejects traceparent symlinks instead
  of following them.
- **Hardened config defaults writes.** TUI model, theme, and thinking-display
  preference updates now reject config-file symlinks and save through rooted
  random temp files.
- **Hardened worktree rooted writes.** File tools, plugin filesystem writes,
  and subagent adoption now save through rooted random temp files while
  rejecting final symlink/non-regular write targets.
- **Hardened model picker state writes.** Recent and favorite model state now
  reads regular files only and saves through rooted random temp files instead
  of following state-file symlinks.
- **Hardened private key creation.** Audit signing keys and plugin signing
  seeds now create with exclusive rooted file handles and refuse to overwrite
  existing or symlinked key paths.
- **Hardened config init writes.** `stado config init` now creates config
  templates through rooted exclusive handles and refuses symlinked or
  non-regular config paths, including when `--force` is set.
- **Hardened plugin signing artifact writes.** `stado plugin sign` now writes
  manifests, signatures, and author pubkey sidecars through rooted random temp
  files and rejects final symlink/non-regular targets.
- **Hardened session export writes.** `stado session export -o` now saves
  through rooted random temp files and refuses final symlink/non-regular
  output paths.
- **Hardened plugin scaffold writes.** `stado plugin init --force` now refuses
  symlinked scaffold directories/files and writes generated scripts with the
  intended executable mode through rooted atomic replacement.
- **Hardened minisign sidecar writes.** Release `.minisig` files now save
  through rooted random temp files and refuse final symlink/non-regular
  sidecar paths.
- **Hardened self-update replacement writes.** Self-update now rejects
  symlinked binary replacement paths and uses rooted temp+rename writes for
  copy fallback installation.
- **Hardened release-helper writes.** Bundled release asset refreshes now save
  fetched binaries, generated embed files, and manifests through rooted atomic
  writes instead of following output-path symlinks.
- **Hardened CLI output directory creation.** Session exports, plugin
  scaffolding, and plugin installs now reject symlinked output parent
  directories before creating missing write targets.
- **Hardened state/config directory creation.** Config defaults, default
  prompts, plugin state, task/memory stores, audit keys, model picker state,
  bundled tool caches, and plugin filesystem writes now reject symlinked
  parent directories before creating missing write roots.
- **Hardened session/worktree directory creation.** Session worktree roots,
  sidecar repositories, materialized trees, `.stado` metadata directories,
  learning documents, and subagent adoption paths now reject symlinked
  directory components before creating missing write targets.

## v0.25.7 — 2026-04-26

### Infra

- **Updated SLSA provenance generation to v2.1.0.** Release provenance now
  uses the newer pinned generator workflow to avoid the Node 20 deprecation
  path in future tagged releases.

## v0.25.6 — 2026-04-26

### Fixes

- **Rooted task-store reads and writes.** Shared task data now opens,
  temp-writes, and renames through `os.Root` scoped to the task-store
  directory, rejecting symlink escapes for the persisted store file.

## v0.25.5 — 2026-04-26

### Fixes

- **Rooted the shared task-store lock file.** The cross-process task
  lock now opens, stats, and removes its lock file through `os.Root`
  scoped to the task-store directory, clearing the remaining gosec path
  warning without weakening the lock.

## v0.25.4 — 2026-04-25

### Infra

- **Disabled cosign v3's signing-config path for legacy checksum
  signatures.** Release checksum signing now keeps the existing
  `checksums.txt.sig` and `checksums.txt.cert` artifacts while using
  cosign v3.

## v0.25.3 — 2026-04-25

### Infra

- **Kept release checksum signing on the documented sig/cert artifacts.**
  Cosign v3 signing now disables the new bundle format so releases continue
  publishing `checksums.txt.sig` and `checksums.txt.cert` for `install.sh`
  and documented manual verification.

## v0.25.2 — 2026-04-25

### Infra

- **Updated the release cosign verifier to v3.** The release workflow now
  installs `sigstore/cosign-installer@v4.1.1` and `cosign v3.0.6`, matching
  `goreleaser/goreleaser-action@v7`'s checksum verification requirements.

## v0.25.1 — 2026-04-25

### Infra

- **Updated the GoReleaser GitHub Action to Node 24.** Release builds now
  use `goreleaser/goreleaser-action@v7`, removing the Node 20 deprecation
  annotation from tagged release runs.

## v0.25.0 — 2026-04-25

### TUI

- **Added a shared task manager.** `/tasks` and `Ctrl+X K` open a
  persistent task browser/editor, while `/tasks add <title>` creates an
  open task directly from the input.

### Agent

- **Added the `tasks` tool.** Tool-enabled agents, `stado run --tools`,
  headless/ACP, and MCP server clients can store, list, read, edit, and
  delete shared tasks outside the repo worktree.

### Infra

- **Cleared the gosec backlog.** Runtime state, config, cache, and
  session metadata now use tighter file modes, workdir reads prefer
  rooted helpers where practical, and intentional dynamic path/exec
  cases carry narrow `#nosec` justifications.
- **Hardened shared task state.** The task store now uses cross-process
  locking, validates persisted task files on load, enforces size/count
  limits, and caps model-facing task output before it enters context.

### Fixes

- **Rejected malformed tree entries during session materialization.**
  Session fork/revert materialization now refuses raw Git tree entry
  names that would escape the destination worktree before joining them
  to filesystem paths.
- **Tightened session and Git ref validation.** Session IDs from refs
  and worktree listings are filtered before filesystem probes, turn-ref
  lookups parse numeric `turns/<N>` targets, raw commit lookups require
  valid 40-hex hashes, and `session land` rejects invalid branch names.
- **Normalized copied file permissions in trusted state transitions.**
  Plugin installs now strip group/world permissions from package files,
  and subagent adoption maps child file modes to git-style `0644` or
  `0755` instead of preserving overly broad source modes.

## v0.24.0 — 2026-04-25

### Agent

- **Added a first read-only `spawn_agent` slice.** The parent model can
  fork a bounded child session for read-only repo investigation; the
  child gets only non-mutating tools, cannot recursively spawn, and
  returns a structured result with the child session/worktree.
- **Bounded spawned agents by wall-clock timeout.** `spawn_agent` now
  accepts `timeout_seconds` with default/cap behavior and returns a
  structured timeout result when the child exceeds its budget.
- **Surfaced spawned children in the TUI.** Successful `spawn_agent`
  tool results now add a system notice with child status, session ID,
  worktree, and attach command.
- **Added headless subagent lifecycle notifications.** Headless clients
  now receive `session.update { kind: "subagent", phase: "started" |
  "finished", ... }` when `spawn_agent` creates and completes a child.
- **Pinned parent cancellation for spawned agents.** Runtime and
  headless tests now assert that cancelling the parent operation cancels
  the running child and emits a finished/error subagent event.
- **Documented the write-capable subagent contract.** EP-13 now defines
  worker-mode ownership scopes, write-scope enforcement, conflict
  checks, and explicit adoption semantics.
- **Pinned subagent write-scope validation.** `spawn_agent` request
  decoding now normalizes and rejects unsafe future `write_scope`
  entries before worker mode is exposed.
- **Added a scoped write guard for subagents.** Mutating file tools now
  honor an optional host-level write-path guard, and EP-13's worker host
  has a tested `ScopedWriteHost` implementation.
- **Built the internal worker subagent path.** Runtime tests now exercise
  scoped `workspace_write` children with read/search plus `write`/`edit`,
  while the public `spawn_agent` decoder still rejects worker mode.
- **Reported internal worker outputs.** Worker subagent results now
  include changed files from the child tree diff and collected
  `scope_violations` for rejected scoped writes.
- **Added subagent adoption planning.** Runtime can now dry-run child
  adoption by comparing parent and child changes against the fork tree
  and reporting conflicts without mutating either session.
- **Added internal subagent adoption apply.** Non-conflicting child
  changes can now be copied into the parent worktree and recorded as
  `subagent_adopt` trace/tree commits by an internal runtime helper.
- **Exposed explicit session adoption.** `stado session adopt` now
  dry-runs child-to-parent adoption by default, supports `--apply`, and
  reports conflicts before mutating the parent.
- **Exposed scoped worker subagents.** `spawn_agent` now accepts
  `role=worker`, `mode=workspace_write`, required `ownership`, and
  normalized `write_scope`; TUI/headless surfaces report worker changed
  files and scope violations for explicit adoption.
- **Aligned ACP subagent notifications.** ACP `session/update`
  notifications now surface subagent lifecycle fields, including worker
  changed files and scope violations.
- **Added subagent adoption commands to interop events.** Headless and
  ACP finished-worker notifications now include an `adoptionCommand`
  when child changes are available to review and apply.
- **Added subagent adoption commands to tool results.** Worker
  `spawn_agent` results now include `adoption_command` when child
  changes are available, so the parent model sees the exact review/apply
  command instead of inferring it from session IDs.
- **Closed EP-13 subagent spawn.** The synchronous read-only and scoped
  worker `spawn_agent` contract is now documented as implemented after
  CLI/TUI adoption surfaces and live local-provider dogfood.

### TUI

- **Added session memory opt-out.** `/memory off` and `stado memory
  session off` now disable approved-memory prompt retrieval for the
  current session/worktree; `/memory on` and `stado memory session on`
  re-enable it while leaving `[memory].enabled` as the global gate.
- **Distinguished LM Studio installed vs loaded models.** Local
  detection now uses LM Studio loaded-state data for auto-fallback and
  picker rows, while doctor and `/providers` show installed-but-not-loaded
  remediation.
- **Aligned TUI preference docs.** The TUI and slash-command docs now
  describe `[tui].theme`, `[tui].thinking_display`, and the custom
  `theme.toml` fallback accurately.
- **Added shell symbols to inline `@` completion.** Top-level
  `.sh`/`.bash` functions now appear as symbol rows that insert
  `path:line` locations.
- **Closed EP-20 inline context completion.** The scoped `@` surface now
  covers agents, sessions, skills, docs, files, and repo-shaped symbol
  scanners.
- **Aligned implemented EP index rows.** EP-14 and EP-24 now show
  `Implemented` in the EP README table, matching their frontmatter.
- **Closed EP-22 theme catalog and picker.** The scoped catalog,
  picker, direct mode shortcuts, custom-theme row, markdown style, and
  config persistence goals are documented as implemented.
- **Pinned EP-23 status rows read-only.** Status rows stay read-only
  with inline action hints; active remediation remains in focused
  commands and config files.
- **Closed EP-23 status modal.** `/status` now keeps the modal read-only
  while showing cached background-plugin lifecycle issues and cached MCP
  attach health, including connected/tool counts and the latest attach
  error.
- **Closed EP-21 assistant turn metadata.** Footer metadata is
  documented as display-only while `conversation.jsonl` remains the
  provider-message transcript.
- **Pinned EP-13 subagent concurrency policy.** The current L4 model is
  one active child per parent session/tool queue; higher child
  concurrency is future scheduler work.
- **Closed EP-19 model/provider picker UX.** Favorites and recents stay
  per-machine state, credentials stay outside picker state, and true
  connect/OAuth is left as future provider-specific work.
- **Stored bundled theme selection in config.** `/theme` now persists
  bundled theme ids as `[tui].theme`; custom `theme.toml` remains the
  fallback path when no bundled theme is pinned.
- **Restored thinking blocks on session resume.** Persisted
  provider-native thinking now rehydrates as separate TUI thinking
  blocks so display modes still apply after restart.
- **Annotated failed tool results in assistant details.** Assistant turn
  metadata now marks requested tool counts with failed/rejected result
  counts after tool execution finishes.
- **Persisted thinking display mode.** `/thinking` and `Ctrl+X H` now
  save the display-only thinking mode to `[tui].thinking_display`, and
  the TUI restores it on startup.
- **Finished footer repository density.** The compact status row now
  shows repo-relative cwd segments inside git worktrees and appends `*`
  to the branch or detached SHA when the worktree has uncommitted
  changes, using a short cache to keep renders cheap.
- **Added an in-TUI subagent adoption command.** `/adopt [child]
  [--apply]` now dry-runs the latest adoptable worker child by default
  and applies non-conflicting child changes only when `--apply` is
  explicit.
- **Made inactive-session policy visible.** `/sessions` now states that
  inactive sessions are parked and lists the active-work blockers that
  must clear before switching.
- **Added Python symbols to inline `@` completion.** Top-level Python
  `class`, `def`, and `async def` declarations now appear as symbol rows
  that insert `path:line` locations.
- **Added JS/TS symbols to inline `@` completion.** Top-level
  JavaScript and TypeScript class, function, and variable declarations
  now appear as bounded symbol rows.
- **Expanded the bundled theme catalog.** Added `stado-rose`, a dark
  neutral theme with rose and cyan accents.
- **Surfaced provider credential health in status.** `/status` now
  reports whether the active provider's conventional API key env var is
  set, missing, or not required by a local preset.
- **Added direct provider setup hints.** `/provider <name>` now prints
  the same setup/remediation guidance available from `Ctrl+A` in the
  model picker.
- **Added credential health to providers overview.** `/providers` now
  shows whether the active provider's conventional API key env var is
  missing, set, or not required.
- **Named configured MCP servers in status.** `/status` now summarizes
  configured MCP server names without starting or probing them.
- **Added markdown style control for themes.** `theme.toml` can now set
  `[markdown].style` to `auto`, `light`, or `dark`; `auto` keeps the
  existing background-luminance detection.
- **Showed custom themes in the picker.** When the current
  `theme.toml` does not match a bundled theme, `/theme` now shows it as
  the current custom row and selecting it closes the picker without
  rewriting the override.
- **Surfaced trace IDs in the status modal.** When the TUI has a valid
  OTel span context, `/status` now shows the current trace id for
  copy/paste into a collector UI.
- **Grouped inline slash suggestions.** The `/` suggestion surface now
  shows compact Quick/Session/View group labels, matching the modal
  command palette without leaving the input.
- **Matched markdown rendering to light themes.** Assistant markdown now
  uses Glamour's light style when the active theme background is light,
  and falls back to the dark style for dark/contrast themes.
- **Added local-runner no-model remediation.** `/providers` now shows
  runner-specific next steps when a reachable local backend has no
  models loaded, including the LM Studio `lms load <model>` path.
- **Added a subagent activity overview.** `/subagents` now lists recent
  spawned child sessions with status, worktree, changed-file counts,
  scope violations, and adoption commands.
- **Added expandable assistant turn details.** `Shift+Tab` can now expand
  the latest assistant footer to show token, cache, tool, and trace
  details without making normal transcript rows noisier.
- **Added action hints to the status modal.** Provider, model, tools,
  plugin, MCP, OTel, budget, and context rows now show the focused
  command or config file to open next.
- **Added direct light/dark theme shortcuts.** `/theme light`,
  `/theme dark`, and `/theme toggle` now switch bundled themes without
  opening the theme picker.
- **Toned down the startup landing logo.** The empty-session landing
  view now samples the embedded banner down to a compact fixed-height
  mark so the prompt stays visually primary on large terminals.
- **Added docs to inline `@` completion.** The TUI picker now groups root
  Markdown docs and `docs/**/*.md` before ordinary file matches.
- **Added Go symbols to inline `@` completion.** Top-level Go
  declarations now appear as symbol rows that insert `path:line`
  references.
- **Preserved per-session provider/model selection.** Switching TUI
  sessions now restores each session's selected provider and model, and
  invalidates the live provider when the restored provider differs.
- **Blocked session switches during background plugin ticks.** The TUI
  now waits for running or queued background plugin ticks before
  switching, creating, or forking sessions in the same process.
- **Added subagent activity to the sidebar.** TUI `spawn_agent` lifecycle
  events now populate a recent child-session activity section with
  running/completed status, changed-file counts, scope violations, and
  adoption readiness.

### CLI

- **Added lesson-specific review commands.** `stado learning
  edit|approve|reject|delete|supersede` now wraps the append-only memory
  review flow while preserving lesson fields such as trigger, rationale,
  evidence, tags, scope, and expiry.
- **Added learning document handoff.** `stado learning document <id>`
  now writes a lesson to `.learnings/` without overwriting existing
  notes and rejects the lesson from prompt retrieval.
- **Added stale lesson file checks.** `stado learning stale` now finds
  approved lessons that cite missing evidence files; `--apply` marks
  them candidate for review so they stop being retrieved.
- **Added lesson-only export.** `stado learning export` now emits folded
  lesson items as local JSON for audit and recovery workflows.

### Docs

- **Closed EP-16 learning system.** The local learning workflow is now
  documented as implemented after proposal, review, approval, retrieval,
  document-elsewhere, stale-file review, and lesson-only export shipped.
- **Closed EP-15 memory system.** The implemented standard now records
  the local JSONL baseline, review/edit/supersede/export surfaces, and
  session-local retrieval opt-out.
- **Narrowed the subagent EP open questions.** EP-13 now reflects the
  shipped worker summaries, adoption commands, `/subagents`, and sidebar
  subagent activity, leaving only concurrency policy open.
- **Closed the multi-session TUI EP.** EP-14 now records the
  active-session-only policy, confirmed delete semantics, and
  command-palette/session-overview UI shape as implemented.
- **Closed the inline slash shortcut-hint question.** EP-26 now records
  that inline slash rows show command IDs and secondary keyboard
  shortcuts together, with regression coverage.
- **Refreshed the opencode TUI UAT backlog.** The report now reflects
  the shipped L4 subagent, context completion, landing, status, turn
  metadata, and theme slices and narrows the remaining work.
- **Documented worker subagent update fields.** Headless/ACP docs and
  CLI help now describe `subagent` lifecycle payloads, worker
  `forkTree` / `changedFiles` / `scopeViolations` /
  `adoptionCommand` fields, and the explicit `stado session adopt`
  review flow.

### Fixes

- **Closed rooted file-access security findings.** Native fs tools, plugin
  fs imports, config writes, and subagent adoption now use rooted file
  operations for symlink-safe confinement; gosec no longer reports G703,
  G122, or HIGH findings.
- **Hardened plugin rollback checks.** Plugin trust verification now
  compares semver precedence instead of raw strings, so versions like
  `1.2.0` cannot pass after `1.10.0`.
- **Stabilized tmux TUI UAT startup.** The real-PTY harness now waits for
  the shell to initialize and sends the launch command literally before
  pressing Enter.
- **Kept JS/TS symbol completion top-level only.** Indented nested
  JavaScript and TypeScript declarations are no longer indexed as
  top-level `@` symbol rows.

## v0.23.1 — 2026-04-25

### Docs

- **Saved the L3 checkpoint for future sessions.** Added a dated
  handoff report covering the `v0.21.2` through `v0.23.0` release loop,
  verification state, and next candidate work.

## v0.23.0 — 2026-04-25

### TUI

- **Preserved per-session draft and scroll state.** Switching sessions
  in one TUI process now caches the inactive session's editor draft and
  chat scroll offset, then restores them when switching back.

## v0.22.0 — 2026-04-25

### TUI

- **Added model-picker provider setup actions.** `Ctrl+A` inside the
  model picker now closes the picker and prints selected-provider setup
  guidance for API-key env vars, configured preset endpoints, or local
  runner startup.

## v0.21.2 — 2026-04-25

### Docs

- **Refreshed the opencode TUI UAT report.** The report now separates
  the original `v0.4.2` comparison from current `v0.21.1` status and
  reprioritizes remaining work around subagents, provider remediation,
  session state caching, and docs/symbol context completion.

## v0.21.1 — 2026-04-25

### TUI

- **Kept session identity visible in the footer.** The dense chat footer
  now includes the active session label, or short session id when no
  label exists, alongside cwd, branch, version, usage, cost, and command
  hints.

## v0.21.0 — 2026-04-25

### TUI

- **Extended inline `@` completion to skills.** Loaded project skills
  now appear after agents and sessions; accepting a skill mention
  injects that skill's prompt body into the conversation and removes
  the mention from the draft.

## v0.20.0 — 2026-04-25

### TUI

- **Extended inline `@` completion to sessions.** The editor now shows
  session rows after agents and before files; accepting a session-only
  mention switches to that session, while accepting one inside a longer
  prompt inserts an explicit `session:<id>` reference.

## v0.19.0 — 2026-04-25

### CLI

- **Added explicit learning lesson capture.** `stado learning
  propose/list/show` now records EP-16 lesson candidates in the
  append-only memory store with required trigger and evidence metadata.

### Prompt

- **Separated approved lessons from ordinary memory.** Lessons with
  `memory_kind: "lesson"` are retrieved through the opt-in memory path
  and rendered under an "Operational lessons" prompt section.

## v0.18.0 — 2026-04-25

### CLI

- **Added append-only memory supersession.** `stado memory supersede
  <id>` now replaces an approved memory with a new approved item while
  preserving the original as a `superseded` folded audit tombstone.

### Fixes

- **Fixed folded memory supersede handling.** Supersede events now mark
  the source memory as `superseded` instead of resolving the event
  against the replacement id.

## v0.17.0 — 2026-04-25

### CLI

- **Added append-only memory edits.** `stado memory edit <id>` can now
  update candidate or approved memory summaries, bodies, metadata,
  tags, and expiry while preserving the JSONL audit log as explicit
  `edit` events.

## v0.16.0 — 2026-04-24

### Prompt

- **Added opt-in approved-memory prompt context.** `[memory].enabled =
  true` now injects bounded, scoped, non-secret approved memories into
  TUI, `stado run`, headless, and ACP prompts as labeled untrusted
  context.

### CLI

- **Surfaced memory prompt settings in config.** `stado config init`
  now documents `[memory]`, and `stado config show` prints the resolved
  enable flag plus item/token caps.

## v0.15.0 — 2026-04-24

### CLI

- **Added memory review commands.** `stado memory` now lists, shows,
  approves, rejects, deletes, and exports the local append-only memory
  store used by `memory:*` plugins.

## v0.14.0 — 2026-04-24

### Plugins

- **Added the first memory host API slice.** Plugins can now declare
  `memory:propose`, `memory:read`, and `memory:write` to use a
  capability-gated local append-only memory store for candidate capture,
  approved-memory retrieval, and explicit write mutations.

### Infra

- **Pinned CI and release tool versions.** GitHub workflows now opt into
  Node 24 action execution and pin GoReleaser / golangci-lint versions
  instead of relying on `latest`.

### Docs

- **Accepted EP-15 memory-system design.** The memory plugin standard
  now defines item schema, scopes, host APIs, retrieval, review controls,
  storage, and prompt-injection defenses.
- **Accepted EP-16 learning-plugin design.** The learning standard now
  defines lesson candidates, approval, retrieval, invalidation, and its
  relationship to the EP-15 memory substrate.

## v0.13.1 — 2026-04-24

### Fixes

- **Restored session compaction auditability.** TUI compaction now keeps
  `.stado/conversation.jsonl` append-only, records raw-log digests on
  compaction markers, and creates real turn-boundary refs for pure chat
  and no-file-change turns. `stado run --session` also attaches to the
  persisted session and records a turn boundary when tools are disabled;
  headless and ACP git-backed prompts persist their transcripts before
  later compaction; headless compaction now writes the same raw-log
  audit marker when a git-backed session is attached.

## v0.13.0 — 2026-04-24

### Prompt

- **Aligned the default system prompt with cairn.** New first-run
  `system-prompt.md` templates now include cairn's governing
  principles, six-phase workflow, session artefact discipline, and
  autonomous-work safety rules while keeping stado identity fixed.
  Untouched generated default templates from prior releases are updated
  automatically; customized templates are left alone.

## v0.12.0 — 2026-04-24

### TUI

- **Added thinking display modes.** `Ctrl+X H` and `/thinking` now cycle
  provider-native thinking output between full, tail-only, and hidden
  display without changing what is persisted.
- **Improved model and slash workflows.** Model selection now persists
  the chosen model/provider as the new default, `Ctrl+X M` opens the
  model picker, `/` opens inline fuzzy command suggestions, and `Ctrl+P`
  remains the full command palette.
- **Clarified manual approval demo use.** The `approval_demo` tool spec
  now warns that it is a human-triggered manual test tool only.
- **Added mode-coloured input rails.** Do, Plan, and BTW now use distinct
  left-rail colours in the chat input.

## v0.11.0 — 2026-04-24

### TUI

- **Expanded multi-session management.** The session overlay now supports
  switch/resume, new, rename, fork, and confirmed delete actions without
  leaving the TUI.

## v0.10.0 — 2026-04-24

### TUI

- **Improved footer density.** The chat status row now keeps compact cwd,
  git branch, and version context on the left while preserving usage and
  command hints on the right.

## v0.9.0 — 2026-04-24

### TUI

- **Clarified LSP readiness.** The status modal now explains that LSP
  tools activate when supported files are read and lists detected
  language-server binaries.

## v0.8.0 — 2026-04-24

### TUI

- **Added a status modal.** `/status` and `Ctrl+X S` now show a compact
  provider, model, tool, plugin, MCP, OTel, sandbox, and context summary.

## v0.7.0 — 2026-04-24

### TUI

- **Added a bundled theme picker.** `/theme` and `Ctrl+X T` now open a
  picker for `stado-dark`, `stado-light`, and `stado-contrast`; picking
  one updates the running TUI and persists it to `theme.toml`.

## v0.6.0 — 2026-04-24

### TUI

- **Started unified `@` completion.** The inline `@` picker now shows
  Do, Plan, and BTW agents before repo files; accepting an agent switches
  the active agent and consumes the mention.
- **Added assistant turn footers.** Completed assistant responses now
  show compact metadata for the agent, model/provider, elapsed time,
  tool count, token delta, and cost delta.

## v0.5.0 — 2026-04-24

### TUI

- **Added a first-class agent picker.** `ctrl+x a` and `/agents` now
  open a modal for switching between Do, Plan, and BTW while preserving
  the existing `Tab` Do/Plan toggle and `ctrl+x ctrl+b` BTW shortcut.
- **Made the active agent visible in the sidebar.** The Agent section now
  shows the current Do, Plan, or BTW agent without restoring the old
  noisy mode suffix in the session header.
- **Added the first in-TUI multi-session workflow.** `ctrl+x l` opens a
  searchable session switcher and `ctrl+x n` creates/switches to a
  fresh session in the same TUI process. Switching is blocked while a
  draft, queued prompt, approval, compaction, stream, or tool is active.
- **Improved model picker continuity.** The picker now marks the
  current model and remembers recent model/provider selections under
  stado state so frequently used choices surface first.
- **Added model picker favorites.** Press `ctrl+f` in the model picker
  to favorite or unfavorite the highlighted model; favorites persist in
  stado state and appear before recents.
- **Calmed the default sidebar.** Debug-only diagnostics such as info
  logs, unknown context limits, unbounded budgets, and normal sandbox
  status now stay hidden unless `/debug` enables sidebar diagnostics
  or warnings/errors need attention.
- **Auto-title fresh sessions from the first prompt.** The TUI now writes
  a compact session description from the first user message when no
  manual `/describe` label exists, improving future session lists and
  switchers without overwriting user labels.

### Infra

- **Refreshed the real-PTY TUI UAT harness for the landing view.** The
  tmux harness now isolates config/state, avoids live-provider
  nondeterminism, expects the startup landing view to be sidebar-free,
  and checks the current rail-card message treatment.

## v0.4.2 — 2026-04-24

### Fixes

- **Fixed main CI failures from the `v0.4.1` push.** Cleaned up new
  lint findings and removed the remaining `-race` hazards in TUI trace
  logging and Linux sandbox cleanup.

## v0.4.1 — 2026-04-24

### Docs

- **Documented release versioning policy.** Minor releases now mean new
  features or meaningful behavior adjustments; patch releases mean
  smaller fixes, docs/process updates, dependency bumps, or contained
  internal changes.

## v0.4.0 — 2026-04-24

### TUI

- **Added the first-run landing view.** A new opencode-style startup
  screen centers the stado ANSI logo, model/provider status, command
  hints, and the editable prompt before the first message.
- **Made the chat input taller by default.** The editor now reserves
  three extra visible rows so multi-line prompts do not collapse the
  interaction area immediately.
- **Fixed the first-message freeze path.** TUI trace logging and
  renderer/log-tail fixes keep input responsive while thinking and
  response blocks stream into the chat history.
- **First-turn provider startup is now async instead of blocking the
  UI.** When no default provider is pinned, the TUI now probes local
  runners at startup, queues the first prompt behind that probe if it is
  still in flight, and replays the prompt automatically when the probe
  resolves.
- **Added focused TUI trace logging for startup / first-turn issues.**
  `STADO_TUI_TRACE=1 stado` now emits timestamped trace lines for the
  provider probe, first-submit queueing, provider resolution, and stream
  start into the sidebar log tail.

### CLI / Infra

- **Added a configurable default system prompt template.** First-run
  config creation now writes `system-prompt.md` under the stado config
  directory, and the TUI, run, ACP, headless, MCP, and session-resume
  surfaces all compose prompts from the same template.
- **OpenTelemetry is now actually bootstrapped by the runtime-facing
  command surfaces.** `stado` (TUI), `stado session resume`, `stado run`,
  `stado headless`, `stado acp`, `stado mcp-server`, and session
  fork/revert flows now start the configured OTel runtime and flush it on
  shutdown instead of leaving the shipped spans as no-ops.
- **Release builds now stamp both CLI and TUI version strings.**
  Goreleaser sets the root command version and the sidebar/bundled
  plugin version value from the same tag.

### Docs

- **Added standalone command guides for every shipped top-level
  command.** The docs index now links `agents`, `audit`, `stats`,
  `headless`, `acp`, `mcp-server`, `verify`, `self-update`, and the
  small generated/informational commands.
- **Moved planned work into EP placeholders.** `BUGS.md` now stays
  focused on active bugs, while planned subagents, multi-session TUI,
  memory, learning, tool approval policy, and system-prompt work are
  covered by EPs.
- **Refreshed stale design/config/context docs.** The docs now reflect
  plugin approval cards, current context accounting, bundled
  auto-compact behavior, and the actual `config` command surface.

## v0.3.0 — 2026-04-24

### CLI / Infra

- **Shipped first-install `install.sh`.** Linux/macOS installs can now
  follow a signed-manifest path on day one: the script verifies
  `checksums.txt` with `cosign`, verifies the matching archive against
  that manifest, and installs `stado` to `~/.local/bin` by default.
- **Direct command coverage now includes `agents` and `audit`.**
  `stado agents list/attach/kill` and `stado audit verify/export/pubkey`
  now have dedicated command-level tests instead of depending only on
  lower-level helper coverage.

### TUI

- **Custom template overlays are now live in the shipped app.** Files
  under `$XDG_CONFIG_HOME/stado/templates/*.tmpl` now override the
  bundled renderer templates at boot, matching the long-documented
  `render.NewWithOverlay` contract.

## v0.2.2 — 2026-04-24

### CLI / Infra

- **Provider credential lookup is now centralized.** The direct
  provider constructors, TUI provider builder, and `stado doctor` now
  share one source of truth for provider-name-to-env-var resolution
  under `internal/config` instead of carrying separate maps.
- **Bundled hosted-provider overrides keep their API-key env lookup.**
  If you override a bundled hosted provider name like `groq`,
  `openrouter`, or `deepseek` via `[inference.presets.<name>]`, the TUI
  now still injects the conventional API key for that provider instead
  of silently dropping it.

## v0.2.1 — 2026-04-24

### Security / Hardening

- **Linux `net:<host>` subprocess policies are now real proxy-only
  network sandboxes.** Instead of sharing the host netns and relying on
  proxy env vars alone, the Linux runner now wraps `bwrap` in
  `pasta --splice-only` and forwards only the local proxy port into the
  private netns.

## v0.2.0 — 2026-04-23

+ Plugin-driven context-management release: shipped bundled
auto-compaction by default, promoted session-aware plugin flows on the
CLI/headless surfaces, reorganized the shipped/example plugin catalog,
and tightened several local authority boundaries.

### TUI / Plugins

- **Bundled auto-compaction is now on by default.** Stado ships the
  `auto-compact` plugin source at `plugins/default/auto-compact/`,
  bundles it into the binary as a default background plugin, and loads
  it automatically in the TUI/headless server.
- **Hard-threshold TUI recovery now forks and replays automatically.**
  When the TUI hits the hard context threshold, it emits a
  `context_overflow` event to background plugins; the bundled
  auto-compact plugin responds by forking a compacted child session and
  the blocked prompt is replayed there.
- **Plugin layout is now product-facing.** The repo now uses
  `plugins/default/` for shipped bundled plugin sources and
  `plugins/examples/` for opt-in samples; the old internal
  `builtinplugins` package was renamed to `bundledplugins`.

### CLI / Headless

- **`[hooks].post_turn` now has cross-surface parity.** The same
  notification-only shell hook now fires on completed turns in the TUI,
  `stado run`, and headless `session.prompt`, with the same JSON payload
  shape and the same bash-disable guard when `bash` is removed from the
  active tool set.
- **`stado plugin run` can now attach to persisted sessions.** Pass
  `--session <id>` to give a plugin real `session:read`,
  `session:fork`, and `llm:invoke` access on the CLI path instead of the
  old "zeroed session" fallback.
- **Plugin-created forked sessions now persist their seed summary.**
  Session-aware plugins that fork a child session now write the seeded
  summary into the child's `.stado/conversation.jsonl`, so resuming the
  child picks up with that summary already loaded.
- **`plugin install` no-op output is now explicit.** Reinstalling the
  same plugin version prints a stdout `skipped:` line instead of only a
  stderr advisory, so scripts and users can distinguish "already
  installed" from silent failure.

### Security / Hardening

- **Session/agent/plugin path traversal holes were closed.** Session
  and agent worktree lookups now validate local IDs before joining
  paths, installed plugin IDs are checked before resolving under the
  plugin state dir, and `session:fork` no longer accepts foreign-session
  refs or raw commit hashes.
- **Writable install/update paths now propagate final flush errors.**
  Plugin install and self-update now treat `Sync` / `Close` failures as
  real errors instead of reporting success after a partial write.
- **Timeouts were added to external HTTP control paths.** Self-update,
  CRL/Rekor, and local-provider probe calls now use explicit HTTP
  clients instead of the process-wide default client with no timeout.
- **Capability-less stdio MCP servers are now refused.** Local MCP
  subprocesses must declare `capabilities` in config instead of falling
  back to caller privileges.
- **Sandbox defaults are narrower.** The built-in `bash` tool now uses
  deny-all networking on the sandboxed subprocess path by default, and
  the docs now describe the remaining Linux `net:<host>` limitation
  honestly as proxy-mediated rather than a raw-socket firewall.
- **Several crash/corruption edges were removed.** The LSP client no
  longer panics on bad pending entries, plugin FS reads fail on overflow
  instead of silently truncating, and TUI aggregate usage accounting now
  stays on the Bubble Tea event loop instead of being mutated from the
  stream goroutine.

## v0.1.3 — 2026-04-23

+ Sandbox follow-up release: Linux subprocess host-allowlist policies
now route through the local CONNECT proxy as originally designed, and
the README now distinguishes Linux, macOS, Windows, and WASM tool
sandbox behavior more precisely.

### Infra / Security

- **Linux `net:<host>` subprocess policies now wire through the local
  CONNECT-allowlist proxy.** `BwrapRunner` starts the loopback proxy for
  `NetAllowHosts`, injects `HTTP_PROXY` / `HTTPS_PROXY` into the child,
  and clears `NO_PROXY` so HTTPS-aware subprocesses and MCP stdio
  servers actually honor the configured host allowlist instead of
  bypassing it.
- **Runner env propagation is now handled at the runner boundary.**
  The sandbox runner interface accepts the candidate child environment
  directly so Linux `bwrap`, macOS `sandbox-exec`, Windows passthrough,
  and the fallback runner all perform filtering from the same source of
  truth.

### Docs

- **README sandbox wording now matches the implementation.** The docs
  now call out that Linux has the strongest shipped path, macOS has
  real subprocess sandboxing but not Linux-style whole-process
  narrowing, Windows v1 is still warning-only, and WASM tools are
  sandboxed by `wazero` host-import gates rather than the OS subprocess
  runner.

## v0.1.2 — 2026-04-23

+ Docs + CLI parity release: ships the documented `doctor` automation
surface, refreshes the top-level/docs catalog for the actual shipped
runtime, and lands a large internal source split to make the codebase
easier to read and maintain.

### CLI

- **`stado doctor` now exposes the documented machine/CI flags.**
  `--json` emits newline-delimited JSON (`check`, `status`, `value`,
  `detail`) and `--no-local` skips local-runner probes for faster or
  offline CI preflight. Blocking doctor failures now exit 1, matching
  the command guide.

### Docs

- **README refresh for the current release and CLI surface.** The
  install section now documents the signed `checksums.txt` verification
  flow that releases actually publish, the quick-start plugin commands
  include the missing sign/trust steps, and the configuration / docs /
  roadmap sections now point at shipped guides instead of stale
  placeholder wording.
- **Retroactive EP backfill for the major shipped design decisions.**
  `docs/eps/` now includes accepted records for the provider seam,
  git-native session model, sandboxing, plugin runtime, conversation
  state, repo-local prompt inputs, guardrails, and interop surfaces,
  and the docs indexes now link that catalog directly.
- **Roadmap + command docs now call out the actual remaining product
  gaps.** `PLAN.md`, `README.md`, and the relevant `docs/` guides now
  explicitly describe the unfinished user-visible surfaces: Windows
  sandbox v2, the advisory-only CLI `session compact` shim, the pending
  `install.sh` first-install path, and template-overlay support that
  exists in the renderer but is not yet wired into the TUI entry point.

### Infra

- **Large source files were split by concern without changing package
  boundaries or exported surfaces.** The TUI model, session/plugin CLI,
  plugin host runtime, headless server, runtime loop, and git commit
  internals are now spread across smaller focused files, making the
  codebase easier to review and maintain without changing the shipped
  behavior.

## v0.1.1 — 2026-04-23

+ Release follow-up: fixes the bundle-fetch path that broke the `v0.1.0`
release workflow, and is the first successful 0.1 release build.

### Infra / Release

- **Bundled-tool release fetches no longer depend on GitHub REST asset
  digests.** `hack/fetch-binaries.go` now reads ripgrep checksums from
  upstream `.sha256` sidecars, reads ast-grep checksums from GitHub's
  public `expanded_assets` fragment, and aborts immediately on any
  supported-target fetch failure instead of silently skipping a bundle
  and letting the compiler fail later.

## v0.1.0 — 2026-04-23

+ Built-in tools now ship through the same signed WASM runtime as
third-party plugins, macOS sandboxing is shipped alongside Linux, and
the public plugin workflow is documented end-to-end. Pre-1.0: breaking
changes still allowed between tags.

### Plugins / Tool runtime

- **Built-in tools now load through the plugin runtime.** The default
  `read` / `write` / `edit` / `glob` / `grep` / `bash` / `webfetch` /
  `ripgrep` / `ast_grep` / `read_with_context` / LSP tools are now
  embedded signed WASM modules instantiated through the same wazero host
  surface used for third-party plugins. That removes a large native-vs-
  plugin split, makes override behavior consistent, and keeps the
  bundled tool surface auditable.
- **Approval wrappers moved into plugins.** The old in-process
  "approval tool" path is gone. Approval behavior now lives in explicit
  example plugins (`approval-bash-go`, `approval-write-go`,
  `approval-edit-go`, `approval-ast-grep-go`) plus a bundled
  `approval_demo` module that exercises the `ui:approval` import.

### Docs

- **README refresh for the 0.1.0 surface.** The install section now
  documents release assets and the Homebrew tap that already exists, the
  plugin command examples no longer mention the removed GitHub bot
  workflow generator, and the shipped-status sections now reflect the
  macOS sandbox + WASM plugin runtime that are already live.
- **`stado plugin` now has a command guide.** `docs/README.md` links a
  new `docs/commands/plugin.md` guide covering scaffold → sign → trust
  → verify → install → run, plus the distinction between trusted
  signers (`plugin list`) and installed plugin IDs (`plugin installed`).

### Infra / Security

- **Removed the `stado github` bot workflow generator.** The GitHub
  comment-triggered bot path added a high-risk hosted-runner execution
  surface that was not core to stado's runtime model. The CLI command,
  its workflow template, and related docs references are gone.
- **Plugin FS sandbox now resolves symlinks before capability checks.**
  `stado_fs_read` / `stado_fs_write` used to call `os.ReadFile` / `os.WriteFile`
  directly, which follows symlinks. A plugin with `fs:read:/allowed` could
  create a symlink in `/allowed` pointing outside the tree and read arbitrary
  files. The new `realPath()` helper resolves symlinks before the
  `allowRead` / `allowWrite` check, so escape is caught.
- **Plugin install path traversal prevention.** Manifest `Name` and `Version`
  are validated with `filepath.IsLocal()` plus explicit rejection of `/` and
  `\` characters so a malicious manifest can't write outside the plugins
  directory with fields like `Name: "../../../etc"`.
- **Headless session ID no longer reuses numbers after deletion.**
  `sessionNew` used `len(s.sessions)+1`, so deleting `h-1` and creating a new
  session would overwrite `h-2`. Now uses a monotonic `nextID` counter.
- **Headless session operations are mutex-protected.** `sessionPrompt`,
  `sessionCancel`, and `sessionCompact` all raced on `sess.messages`,
  `sess.cancel`, and `sess.gitSess`. Each `hSession` now carries its own
  `sync.Mutex`.
- **ACP session operations are mutex-protected.** Same race pattern as headless
  — `session/prompt` and `session/cancel` dispatched on separate goroutines
  could corrupt message history or cancel the wrong turn. Fixed with per-
  `acpSession` mutex and monotonic ID counter.
- **MCP client leak on reconnect fixed.** `attachMCP` called from every
  `BuildExecutor` reconnected each configured MCP server without closing the
  previous `client.Client`. After several tool-enabled prompts the process
  leaked stdio MCP subprocesses. `Connect` now closes the old client inside
  the lock after a successful replacement.
- **`run --session --tools` now opens the correct worktree.** When `--session`
  was set, tools still opened a session from the caller's cwd instead of the
  resumed session's worktree. Running from a different directory would
  execute mutating tools against the wrong repo.
- **Host-safe SDK split for bundled WASM modules.** `internal/bundledplugins/sdk`
  now keeps the real pointer-based implementation behind `//go:build wasip1`
  and provides a host-only stub for tests and lint. That stops host-side
  tooling from treating wasm32 offsets as native pointers while preserving
  the ABI used by the embedded modules.

### UX


- **Async tool execution — TUI no longer blocks on long-running tools.**
  `bash sleep 30` (or any slow tool call) used to freeze the entire
  interface until the command returned. Tool calls now run on a
  goroutine and ferry their result back via `toolResultMsg`, so the
  user can keep typing, scroll, or cancel while the tool is in-flight.
  Same pattern already used for `/plugin:...` invocations.
- **Queued prompts get visual feedback.** When you hit Enter while a
  turn is still streaming, the follow-up message is appended to the
  chat immediately with a muted "queued — runs when the current turn
  finishes" pill. Previously the only signal was a tiny status-bar
  label that was easy to miss. Ctrl+C on a queued prompt now also
  removes the block, not just the internal buffer.
- **Render caching eliminates glamour-induced keypress lag.** Long
  conversations used to stutter during streaming because every frame
  re-ran glamour/markdown on every historical block. Two changes fix
  this: (1) `Renderer` now memoises `glamour.TermRenderer` instances
  per width (creating one costs 5–10 ms), and (2) each conversation
  block caches its last rendered output, invalidating only when its
  body, width, expand state, or tool result changes. The live
  assistant block still re-renders every tick (it is growing), but
  everything else is near-free.
- **`[approvals]` allowlist is actually wired to the TUI.** The config
  parser has supported `mode = "allowlist"` and `allowlist = ["read",
  "grep"]` for a while, but `Run()` never called `SetApprovals()`, so
  the allowlist was silently ignored. Now tools named in the allowlist
  auto-execute without the `⚠ y/n` prompt.
- **Gated `?` and `/` keybindings.** Typing a literal `?` or `/` inside
  a non-empty prompt no longer pops the help overlay or command
  palette — the characters insert as text instead. Both shortcuts
  still work when the input box is empty.
- **Tool cancellation actually works.** Approved tool calls run on a
  goroutine so the UI stays responsive. Previously there was no way
  to cancel a long-running tool after approving it. Now Ctrl+C
  propagates cancellation into the tool's context; the goroutine
  exits with "cancelled by user" instead of running to completion.
- **Live "tool running" indicator with elapsed counter.** Tool blocks
  now show `running 3.2s` while the command is active, refreshed
  every 250 ms via `toolTickMsg`. No more silent 30-second waits
  where the user can't tell if stado is working or frozen.
- **Narrow-terminal startup hint.** Terminals narrower than 90 columns
  no longer show a blank empty-state — they get `"Send a message to
  get started — /help for commands"` so first-time users aren't
  staring at empty whitespace.
- **`Ctrl+C` during streaming confirms cancellation.** Cancelling a turn
  now drops a one-time "turn cancelled" system block into the chat,
  so users know the keystroke registered instead of wondering if
  the model finished coincidentally.
- **Collapsed tool cards show an expand affordance.** When a tool
  call has completed but the card is collapsed, a muted
  `shift+tab` hint is appended to the header row so the user
  knows they can expand it.
- **Sidebar placeholder when no model is set.** If `model` is empty in
  config, the sidebar Model field now reads `"no model set — /model"`
  instead of a completely blank line.

### Infra

- **Test coverage: 5 new UAT scenarios** for previously uncovered
  slash commands: `/split`, `/todo`, `/provider` (uninitialised),
  `/tools` (populated + empty paths).

## v0.0.1 — 2026-04-21
+ ACP + plugin ABI + MCP client + MCP server surfaces are all
feature-complete relative to the ranked research list (AGENTS.md
auto-load, `[budget]` cost gate, `.stado/skills/`, `[hooks]`
post_turn, and `stado mcp-server`). Pre-1.0: breaking changes still
allowed between tags.

### Iteration-cycle additions (post-initial-sweep)

- **`stado mcp-server` — expose stado's tools as an MCP server.**
  Every bundled stado tool (read, grep, ripgrep, ast-grep, bash,
  webfetch, file ops, LSP-find) is registered with an MCP v1
  server over stdio. Other MCP-aware agents (Claude Desktop,
  Cursor, etc.) can call stado as a tool backend. Scope is
  tools-only in this release — no resources, no prompts, no
  sampling. `[tools].enabled` / `[tools].disabled` trim the
  exposed surface same as the TUI and `run` paths, so an MCP
  client only sees what stado is currently configured to offer.
  Auto-approve host rooted at process cwd — the MCP client is
  assumed to be the authorization boundary. Closes the last item
  in the ranked research list.
- **`/context` is a one-stop session-state view.** Used to show
  only token + threshold info. Now also renders: session id,
  cost, budget caps (when set), loaded instructions file, skill
  names, configured post_turn hook. Answers "what does this
  session look like to the model?" without bouncing across
  /budget, /skill, sidebar.
- **`/session` slash command** — prints the current session id,
  worktree path, and description label. Copy-paste target for
  `stado session fork`, `session tree`, `session attach` in
  other shells. Explains itself when invoked outside a live
  session instead of silently failing.
- **TUI sidebar surfaces loaded skills.** A new "Skills: N — /skill"
  row renders when skills are loaded from `.stado/skills/`. Users
  no longer have to know the slash command in advance to discover
  the feature — a repo with a skills directory advertises itself.
  The row stays hidden when no skills are loaded so empty repos
  don't see a misleading "0 skills" row.
- **`stado headless --help` documents `plugin.list`/`plugin.run`.**
  Both RPC methods landed months ago but the help text never
  listed them, so CI integrators had to read the server code to
  learn they existed. Added the shape summary for both plus the
  full set of `session.update` notification kinds.
- **`stado run --skill <name>`** — skills are now CLI-usable, not
  just a TUI feature. Resolves `.stado/skills/<name>.md` from cwd
  and uses the body as the prompt. Combines with `--prompt` (skill
  body prepended) so the reusable skill plus a per-invocation ask
  compose naturally. Unknown skill lists the available names in the
  error message so a typo doesn't force a filesystem grep. Useful
  in CI: a repo can ship `.stado/skills/ci-review.md` and pipelines
  invoke `stado run --skill ci-review` instead of inlining the
  full prompt text.
- **`/retry` slash command.** Regenerates the last assistant turn
  from the same user prompt — equivalent to the "regenerate" button
  in ChatGPT/Claude web UIs. Truncates the conversation back to the
  last user message (dropping assistant + tool-role messages) and
  re-streams. No-ops with a hint when there's nothing to retry, the
  last message is already a user prompt, or a stream is already
  running (avoids doubling cost and racing the goroutine).
- **`agents list` hides stale/empty rows by default.** Same problem
  `session list` had pre-dogfood: every aborted run leaves a PID
  file + empty worktree in the agents listing, so the output was
  30+ stale rows with dashes everywhere. Now hidden; `--all`
  restores the full view. A row is worth showing when the process
  is alive OR there's committed content on the tree/trace refs.
- **`stado doctor` now surfaces opt-in feature config** — Budget
  caps, Lifecycle hooks, Tools filter. All render as ✓ regardless
  of whether they're set; the point is to make the features
  discoverable and let users verify that their config.toml took
  effect. "Did I configure the budget cap?" is now a `doctor`-
  answerable question instead of a config-file-read task.
- **Lifecycle hooks — `[hooks]` section (MVP, notification-only).**
  Users can wire a shell command to the `post_turn` event; stado
  runs `/bin/sh -c <cmd>` with a JSON payload on stdin carrying
  turn index, input/output tokens, cost, duration, and a ≤200-char
  excerpt of the assistant text. 5-second wall-clock cap per hook.
  stdout/stderr go to stado's own stderr with a `stado[hook:<event>]`
  prefix so they're distinguishable in shared terminals. Failures
  are logged, never propagated — a broken hook can't poison the
  next turn. MVP scope is deliberate; a richer "approve tool call
  via external policy" form can grow on top.
- **Help overlay (`?`) now lists slash commands.** Users had to
  open the palette separately to learn that `/budget`, `/skill`,
  `/model`, etc. existed. The overlay now appends the palette's
  Commands table below the keybindings section, grouped the same
  way (Quick / Session / View).
- **Skills: `.stado/skills/*.md` auto-loader.** Drop a markdown file
  with frontmatter `name:` / `description:` in a `.stado/skills/`
  directory and stado exposes it as `/skill:<name>` in the TUI.
  Invocation injects the body as a user message so the next turn
  acts on it. `/skill` alone lists what's loaded. Resolution walks
  from cwd upward — nearest-wins for module-level overrides in a
  monorepo. Bodies without frontmatter use the filename stem as
  the name. Matches the emerging cross-vendor convention for
  reusable prompt fragments.
- **`stats --json` now emits a valid empty shape when there are no
  sessions.** Previously stdout was empty and `(no sessions in
  window)` leaked to stderr, which broke `stado stats --json | jq`
  in a fresh repo. Matches the already-valid empty case for
  "sessions exist but no tool calls in window."
- **`config init` template now covers `[budget]` + an AGENTS.md
  pointer.** The generated template was the only docs users saw
  for many knobs; adding budget + pointing at AGENTS.md closes the
  gap between config knobs users can see and features actually
  available.
- **`session list` hides empty rows by default.** Zero-turn +
  zero-message + zero-compaction sessions were cluttering the
  default output — `session list` on a long-lived repo was showing
  50 empties per 3 real rows. Now hidden; `--all` restores the
  full listing. A stderr footer reports how many were hidden with
  a copy-pasteable `session gc --apply` pointer.
- **`stado doctor` stops failing on missing optional tools.** gopls
  is only needed by the `lsp-find` tool; stado works fine without
  it. Now rendered as ✓ with a "not found — optional" detail
  instead of ✗, and the exit code no longer flips to 2 when the
  only missing dep is optional. New `checkOptionalBin` helper
  separates "must-have" from "nice-to-have" checks.
- **`stado config show` now prints `[budget]` and `[tools]`.** Both
  sections were silently absent — users could set them in
  config.toml but couldn't confirm they took effect without
  reading the loader. Budget always renders (with "(unset)"
  labels) so the knob doubles as documentation.
- **`[budget]` cost guardrail.** Two opt-in thresholds:
  `warn_usd` paints a yellow status-bar pill `budget $X/$cap` and
  appends a one-time system block once the cumulative session cost
  crosses it. `hard_usd` blocks further turns with an actionable
  hint; `/budget ack` unblocks for the rest of the session; `/budget
  reset` re-arms the gate. `stado run` maps the hard cap onto
  `AgentLoopOptions.CostCapUSD` and exits 2 with a pointer at the
  config knob. Defaults are 0 (disabled) so local-runner users with
  no cost concerns see no guardrail UI. Misconfigured pairs
  (`hard_usd ≤ warn_usd`) drop the hard cap with a stderr warning.
- **Project-level instructions auto-loader.** Stado now walks up
  from cwd looking for `AGENTS.md` (preferred, cross-vendor
  convention) or `CLAUDE.md` (fallback) and injects the file body
  into every turn as a system prompt. `stado run` prints the
  resolved path to stderr; the TUI sidebar gains an `Instructions`
  row showing the file's basename. Missing file is a silent no-op;
  a broken file becomes a stderr warning — the TUI never fails to
  boot because of an instructions file. Wired into TUI,
  `stado run`, ACP server, and the headless JSON-RPC session.prompt.
  Compaction retains its purpose-specific summarisation prompt —
  only user-facing turns pick up `AGENTS.md`.
- `[tools]` config section lets users trim the bundled tool set.
  `enabled = [...]` acts as an explicit allowlist; `disabled =
  [...]` removes specific tools from the default. When both are
  set, `enabled` wins. Unknown names log a stderr warning and are
  ignored, so configs survive tool renames.
  Applies everywhere the executor is built: TUI, `run`, `headless`,
  and the headless `tools.list` RPC.
- `stado plugin init <name>` — scaffold a new plugin project. Go
  wasip1 template with the full ABI surface (stado_alloc,
  stado_free, stado_tool_*, stado_log import) plus a working demo
  tool. `--dir` relocates; `--force` overwrites. Pairs with
  SECURITY.md's publish cookbook — zero → signed plugin in
  minutes.
- `stado session logs <id> -f` — follow mode live-tails the trace
  ref. Useful for multi-terminal workflows: agent runs in pane 1,
  logs watches in pane 2. `--interval` tunes poll frequency
  (default 500ms).

#### Earlier iterations

Continued polish after the first round of dogfood fixes. Each item
landed independently so the history tells the shape of the
feature set.

- `stado run --session <id>` — continue a long-running session
  from the CLI. Loads the prior conversation, appends the new
  prompt, persists the exchange so the TUI resume picks it up.
  Useful for scripted follow-ups: `stado run --session react
  "what was that hook we extracted?"`. Same id/prefix/description
  resolver as `session resume`.
- `stado session logs <id>` — render the session's trace ref as
  a scannable one-line-per-tool-call feed. Fills the gap between
  `session show` (summary) and `audit export` (JSONL). Shows
  time, tool(arg), summary, tokens, cost, duration, and marks
  errors with ✗. `--limit N` to cap; accepts the same lookup
  resolver.
- `stado config show` — print the resolved effective config
  (TOML + env + defaults merged). Human table by default, `--json`
  for jq. Answers "why is stado using X?" without reading the
  loader. Highlights when `config.toml` doesn't exist yet.
- `stado stats --json` — structured output for dashboards, CI
  gating, jq piping. Shape:
  `{window_days, total{calls,tokens_in,tokens_out,cost_usd},
  total_duration_ms, by_model, by_tool}`. Empty-window case emits
  a valid empty shape so scripts don't special-case.
- Shell-style aliases on frequent subcommands: `session ls` →
  `list`, `session rm` → `delete`, `session cat` → `export`.
- `session list` status column is now colourised — live green,
  idle grey, detached dim. Respects `NO_COLOR` / `FORCE_COLOR` /
  isatty so piped output stays plain.

### UX sweep — dogfood-driven findings (pre-release polish)

**Session management.**

- `stado session describe <id> [text]` — attach a human-readable
  label to a session. Stored in `<worktree>/.stado/description`.
  `--clear` removes; no-text mode prints the current label.
  Surfaces in `session list` (new DESCRIPTION column), `session
  show` (label: line), and the TUI `/sessions` overview.
- `stado session resume <id>` now accepts UUID prefixes (≥8
  chars) and case-insensitive description substrings.
  Ambiguous matches list candidates so you can narrow:
  `stado session resume react` → resolves via description.
- `stado session search <query>` — grep across every session's
  persisted conversation. Case-insensitive substring by default;
  `--regex` switches to RE2. Flags: `--session <id>` to scope,
  `--max N` to cap hits. Output is `session:<id> msg:<n>
  role:<role>  <excerpt>` for easy piping.
- `stado session export <id>` — render the conversation as
  markdown (default) or raw JSONL (`--format jsonl`). `-o
  session.md` writes to a file; otherwise stdout. Markdown has
  per-role headers, fenced tool-call JSON, fenced tool-result
  bodies, thinking blocks as blockquotes (signature stripped).
- `stado session gc [--apply]` — sweep zero-turn, zero-message,
  zero-compaction sessions older than `--older-than` (default
  24h). Dry-run by default; `--apply` to actually delete. Live
  sessions are always skipped.
- `stado session show` now renders a `usage` line summarising
  tool calls, token counts, cost, and wall time for the session.
- `session list` gains a DESCRIPTION column; `Status` values
  refined to `live` / `idle` / `detached` (was `attached`),
  reflecting whether a process actually holds the worktree.

**TUI additions.**

- `@`-file fuzzy autocomplete in the input. Typing `@` opens an
  inline picker of repo files; Up/Down navigate; Tab/Enter
  accepts, replacing the `@query` fragment with the path plus a
  trailing space. Esc closes without changing the buffer. Email-
  style `user@x` deliberately does NOT trigger — the `@` has to
  be at start of input or follow whitespace.
- **Message queuing.** Enter during streaming no longer silently
  drops your message. The user block appears in the chat right
  away; the LLM-facing `msgs` add is deferred to drain so the
  current turn's context isn't mutated. Ctrl+C/Esc with a queue
  pending clears the queue (doesn't also cancel the stream —
  that's the second press).
- Status row surfaces: cumulative cost, cache-hit ratio (when
  non-zero), and a `queued: <excerpt>` pill while something is
  queued.
- `/describe` slash command — mirrors the CLI subcommand:
  `/describe <text>`, `/describe --clear`, or `/describe` alone
  to read back the current label. Sidebar now renders the
  session label under the stado title.
- `/sessions` overview lists sessions with descriptions when set.

**Shell tab-completion** for session IDs on every session
subcommand that takes one: `show`, `attach`, `resume`, `delete`,
`fork`, `describe`, `revert`, `land`, `tree`, `export`.
Descriptions attach as completion hints — `<TAB>` in bash/zsh/fish
shows "id    description" alongside.

### Opencode / Pi gap features

Three features added after researching the top coding-agent CLIs.

- **`stado stats`** — cost + usage dashboard aggregated from the
  git-native audit log (trace-ref trailers). Works offline /
  airgap — no OTel collector required. Flags: `--days` (default
  7), `--session`, `--model`, `--tools` to include a per-tool
  breakdown. Sorted by cost descending.
- **`stado github install`** — writes a
  `.github/workflows/stado-bot.yml` that fires on issue/PR
  comments starting with `@stado`. Runs `stado run --prompt`
  inside the user's Actions runner and posts the reply back via
  `gh api`. `--force` overwrites; `install` / `uninstall` pair is
  idempotent.
- Status-row cache-hit pill. Renders `cache NN%` when the
  provider reports non-zero prompt-cache reads (ratio is
  `CacheReadTokens / (CacheReadTokens + InputTokens)`).

### Plugins + headless

- **Headless plugin surface.** `plugin.list` and `plugin.run`
  JSON-RPC methods; plugin-driven forks emit
  `session.update { kind: "plugin_fork", plugin, reason, child,
  at_turn_ref, childWorktree }`. Background plugins load on
  `Serve()` entry and tick on `session.prompt` completion.
  Closes the deferred K2 line item.
- **Shutdown ordering** in headless. `Conn.WaitPendingExceptCaller`
  lets the `shutdown` handler drain earlier in-flight requests
  before replying, so `plugin.run` responses can't arrive
  *after* the shutdown ACK.
- **`providers.list.current`** now reports the resolved provider
  (previously parroted `cfg.Defaults.Provider` which is blank on
  the local-fallback path).
- **Persistent plugin lifecycle.** Plugins that export
  `stado_plugin_tick` load once per TUI boot via
  `[plugins].background = [...]` and fire on every turn
  boundary.
- Second validating plugin: `plugins/examples/session-recorder/`
  — `session:read` + `fs:read` + `fs:write` + `stado_plugin_tick`.
  Appends JSONL per turn. Proves the ABI generalises beyond
  auto-compaction.
- `stado plugin installed` — list installed plugin IDs (was
  conflated with the trust-store list before).

### Terminal hygiene

- **OSC color-query responses** no longer leak into the
  textarea. Root cause: bubbletea v1.3 has no OSC parser, and
  slow terminals answer `\x1b]11;?` after stado has acquired
  stdin, so the payload lands as Alt-prefixed rune bursts.
  Two-layer fix: byte-level `oscStripReader` that state-machines
  through the response across Read boundaries + `tea.WithFilter`
  backstop for the Alt-wrapped shape. Both removed once
  bubbletea v2 (native OSC parser) lands.
- **Raw-mode regression** from the OSC wrapper — fixed. The
  earlier stripper was a plain `io.Reader`, which made bubbletea's
  `initInput` type assertion (`p.input.(term.File)`) fail: no raw
  mode, no epoll cancel path, so keystrokes echoed to the
  terminal cursor instead of reaching the TUI. New
  `oscStripFile` embeds `*os.File` so `Fd()`/`Write()`/`Close()`/
  `Name()` forward to stdin and bubbletea can still call
  `term.MakeRaw(fd)`. cancelreader's epoll reads via
  `file.Read()` which routes through the filter.
- **Sidebar no longer latches closed** on the first render. View()
  used to flip `sidebarOpen = false` when width was below the
  min threshold — but the very first View() call runs before
  any `WindowSizeMsg` arrives, at width=0, permanently closing
  the sidebar. Now the flag is preserved; only the current-frame
  render is skipped.
- `hack/tmux-uat.sh` — real-PTY harness. Spawns `./stado` in a
  detached tmux session, asserts against the captured pane.
  Orthogonal to teatest: it catches regressions in the termios +
  cancelreader path (the exact path the two fixes above sit on).

### CLI polish

- `stado` in a non-TTY context now exits 1 with an actionable
  pointer to `run --prompt` / `headless` (was exit 0 with a raw
  `/dev/tty: no such device` leak).
- `stado version` and `stado verify` agree — both read the
  shared `collectBuildInfo()`.
- `stado doctor` uses correct pluralisation ("1 check failed"
  vs "2 checks failed").
- `session attach <unknown>` / `session delete <missing>` print
  concise errors (previously wrapped OS stat errors).
- `plugin trust` error explains both unlock paths: out-of-band
  pubkey trust or `plugin verify . --signer <pubkey>` TOFU.
- Provider-fallback message no longer says "no
  ANTHROPIC_API_KEY" — now "no provider configured — using local
  <runner>". Stale from an earlier anthropic-first era.
- Config init template bumped `claude-sonnet-4-5` →
  `claude-sonnet-4-6`.
- Top-level description: "Sandboxed, git-native coding-agent
  runtime" (was "AI CLI harness and editor" — stado is not an
  editor).

### Testing

- 30 UAT scenario tests covering the enumerated user-facing
  flows in `.learnings/UAT_SCENARIOS.md`. 3 in
  `uat_direct_test.go` + 16 in `uat_scenarios_test.go` + 11 in
  `uat_scenarios_extended_test.go`. All direct-Update —
  teatest's virtual terminal fights stado's sidebar+viewport
  layout for reliable snapshot assertions.
- Phase 11.5 PTY harness for interactive `session tree` —
  teatest-backed end-to-end test that navigates + presses `f` to
  fork. Simpler layout, reliable under teatest.

### Infra

- `hack/otel-compose/` — Jaeger-all-in-one compose fixture for
  eyeballing OTel traces during development. Closes Phase 6
  verify.
- Plugin-publish cookbook in SECURITY.md — nine-step guide from
  `gen-key` through rotation.

### Fixes

- Session list's "attached" status now reflects whether a pid is
  actually alive (reads `.stado-pid` + `signal(0)` probe). Was
  "worktree exists on disk" regardless of whether anyone was
  using it.
- Removed the dead `short()` helper from `cmd/stado/session.go`
  that lint caught after an adjacent-file edit triggered a re-
  lint.

---

## Earlier

See `git log --oneline` for pre-changelog history. PLAN.md has the
phase-by-phase roadmap; most ✅ rows there landed before this
changelog started.
