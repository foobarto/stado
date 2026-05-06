# EP-0038h — Quad chunk: stado_json_*, UDP stateless, HTTP response streaming, stado_progress

**Status:** drafted 2026-05-06; autonomous design.
**Author:** Bartosz Ptaszynski.
**Branches:** `feat/ep-0038h-{json,udp-stateless,http-streaming,progress}`.

## Problem

Four deferred items from v0.36.0/v0.37.0 changelogs that share a
release cycle. None are large individually; bundling them keeps the
release-cut overhead low.

## Item 1 — `stado_json_get` + `stado_json_format`

**Why:** plugins parse JSON in wasm today by bundling a parser
(~50KB gzipped per plugin) and paying its CPU cost on every call.
Most uses just want one field out of an HTTP response. Host-side
JSON saves binary size and runs at native speed.

| # | Topic | Decision | Reason |
|---|---|---|---|
| Q1 | Surface | **Two imports:** `stado_json_get(json, path) → bytes` and `stado_json_format(json, indent) → bytes`. No `_parse`, no `_set`, no `_path` (jsonpath/jq). | The 80% case is "extract one field, then format the result." `_set` is rare; `_parse` is implicit in `_get` (returns -1 on malformed input). |
| Q2 | Path syntax | **Dotted form** (`a.b.c`) plus numeric array index (`a.0.b`). No filters, no globs, no recursive descent. | Trivially parseable, no tokenizer needed. Plugins that need jq-style queries can compose multiple `_get` calls. |
| Q3 | Capability | **None.** Pure compute, no side effects. | Cap-gating CPU work is friction without benefit. |
| Q4 | Output of `_get` | **Canonical JSON bytes** of the extracted value (so `_get` on a number returns `42`, on a string returns `"hello"` with quotes). | Plugin can re-feed the output into another `_get` call. Round-trippable. Quoted strings are unambiguous. |
| Q5 | `_get` on missing path | Return -1. | -1 is the standard error sentinel. Plugin checks return code; doesn't conflate "not present" with `null` (which is valid JSON). |
| Q6 | `_format` indent | int32 indent param: 0 = compact, N>0 = N-space indent. | Simple; matches `encoding/json` `MarshalIndent`. |

**ABI:**

```
stado_json_get(json_ptr i32, json_len i32, path_ptr i32, path_len i32,
               out_ptr i32, out_max i32) → i32
   // Returns: bytes written on success, -1 on malformed JSON / missing
   // path / out_max too small.

stado_json_format(json_ptr i32, json_len i32, indent i32,
                  out_ptr i32, out_max i32) → i32
   // Returns: bytes written, or -1 on malformed JSON / out_max too
   // small.
```

## Item 2 — UDP stateless

**Why:** v0.37.0 ships connect-mode UDP only (one peer per socket).
Plugins that probe arbitrary peers (DNS reflection scanners, NTP
mode 6 sweepers, network-discovery tools) need to send to many peers
and receive replies from anywhere.

| # | Topic | Decision | Reason |
|---|---|---|---|
| Q1 | Handle reuse | **Reuse the `listen` handle type** — `stado_net_listen("udp", host, port)` returns a `lst` handle backed by `net.PacketConn`. The same handle accepts both `_sendto` and `_recvfrom`. | Mirrors Go's `PacketConn` shape. Avoids new "udpsock" handle prefix. |
| Q2 | Capability | **`net:listen:udp:<host>:<port>`** for the bind. Outbound `_sendto` peers are governed by the **same `net:dial:udp:<peer-host>:<peer-port>` glob set** — sending to a peer the plugin couldn't dial is denied. | Single, recognisable capability surface. Per-peer gating prevents a UDP listener from becoming a net-wide spray gun. |
| Q3 | `_sendto` ABI | `stado_net_sendto(lst, host_ptr, host_len, port, data_ptr, data_len) → i32` | Synchronous; returns bytes written or -1. |
| Q4 | `_recvfrom` ABI | `stado_net_recvfrom(lst, timeout_ms, body_ptr, body_max, addr_ptr, addr_max) → i64` packed: `high32 = body_len_or_sentinel`, `low32 = addr_len`. addr is written as `host:port`. -2 = timeout, -1 = error. | Packed return delivers two outputs in one host call. The wasm caller un-packs with `(ret >> 32) & 0xFFFFFFFF` / `ret & 0xFFFFFFFF`. |
| Q5 | Listen on `0.0.0.0`/`::` | Allowed if the cap permits (verbatim match). UDP listen has no private-IP guard analogous to TCP dial — the cap string is the whole control. | Consistent with TCP listen. |
| Q6 | Resource caps | UDP listen handles count toward `maxNetListenersPerRuntime` (8). Outbound `_sendto` doesn't allocate handles. | UDP packet sockets are cheap to send through; only the bind is the resource. |
| Q7 | Private peer guard on `_sendto` | **Same `NetHTTPRequestPrivate` guard.** Sending to RFC1918 / loopback / link-local without that cap returns -1. | Consistent with dial. |

## Item 3 — HTTP response streaming

**Why:** today `stado_http_request` reads the entire body into memory
before returning. A 1 GB download OOMs the wasm instance. Plugins
that fetch large payloads (firmware blobs, log archives) need
chunked reads.

| # | Topic | Decision | Reason |
|---|---|---|---|
| Q1 | Direction | **Response streaming only.** Request streaming (large uploads) deferred. | Most security tools fetch big payloads; few upload them. v2 if a plugin actually needs upload streaming. |
| Q2 | Surface | **Three imports:** `stado_http_request_stream(args)` returns a response handle; `_response_read(handle, out, max) → i32`; `_response_close(handle) → i32`. | One stateful resource (the open body); three operations on it. |
| Q3 | Handle type | **New typed prefix `httpresp`.** Distinct from `conn` (different read semantics — chunked, EOF-terminated). | Type-tag dispatch keeps the lifecycle clear. |
| Q4 | Capability | **Reuse existing `net:http_request[:<host>]`.** Streaming just changes how the body is delivered, not which hosts can be reached. | Friction-free for plugins that already have HTTP caps. |
| Q5 | Args shape | Same `args_json` as `stado_http_request`. Headers/method/body/timeout/proxy_url all carry over. The response object is now: `{status, headers, body_handle: i32}` — the body is *not* in `args` result. | One JSON shape, easy to learn. The `body_handle` field tells the plugin to drain via `_response_read`. |
| Q6 | Resource cap | `maxHTTPStreamsPerRuntime = 8`. New `httpStreamCount` atomic counter on Runtime. | Open response bodies hold connections; bound them. |
| Q7 | `_response_read` semantics | Returns bytes written into out_ptr. **0 = EOF**. -1 = error. Read deadline configurable per-call via timeout_ms (0 = no deadline). | Mirrors `stado_net_read`. |
| Q8 | `_response_close` | Idempotent. Closes the body even if not fully drained. | Plugin may bail mid-download. |
| Q9 | Reaper | `Runtime.Close` reaps open response handles via a new `closeAllHTTPStreams`. | Symmetry with `closeAllHTTPClients` / `closeAllNetConns`. |

## Item 4 — `stado_progress`

**Why:** tools that take more than ~2s today appear silent to the
operator until the final result lands. Tester #4: a long probe
should be able to emit "checking host 17/256" so the operator (and
soon, the agent's pulse signal) can tell it's making progress.

| # | Topic | Decision | Reason |
|---|---|---|---|
| Q1 | Audience | **Operator only in v1.** The agent/model sees only the final tool result. Agent-loop integration (model sees mid-tool partials) is a separate ABI design and explicitly out of scope. | Mid-tool partial-output to the model breaks tool-call atomicity in current LLM contracts. The operator-visibility variant is the practical, ship-it-now answer. |
| Q2 | ABI | `stado_progress(text_ptr i32, text_len i32) → i32`. Returns 0 on success, -1 on malformed text / channel full / no executor wired. | One-shot string emit. No structured-event variant. |
| Q3 | Capability | **None.** Progress events are a UX channel, not a privilege. Spammy plugins are bounded by Q4's rate limit. | Capping a debug stream behind a cap is friction. |
| Q4 | Rate / size limits | **Per call: 4 KB max payload.** Per Runtime: ring-buffer of 64 most recent entries (older entries dropped silently). | Bounds blast radius. A tight loop that calls `_progress` 1M times can't OOM the host or flood the TUI. |
| Q5 | Wiring | `Host.Progress` is a `func(plugin, text string)` callback. Populated by the executor caller (TUI / headless) when a session is live; nil = no-op (drop). | Decouples host-imports from any specific UI. The TUI plumbs it into a TUI event; headless prints to stderr. |
| Q6 | Audit | Progress events do **not** go through the audit log. They're a debug stream. | Cap-gated imports go through audit; progress isn't cap-gated. |
| Q7 | Per-call format | Plugin emits raw text. The host caller decides how to render (prefix with plugin name, add timestamp, etc.). | Keep ABI dumb; presentation is the host's call. |

## Out of scope (filed for follow-ups)

- ICMP raw sockets (per operator: last).
- AXFR DNS.
- HTTP request streaming (large uploads).
- FleetBridge messaging real impl.
- `stado_json_set` / jq-style queries.
- UDP broadcast/multicast set-options.
- stado_progress agent-loop integration (model sees mid-tool
  partials) — needs LLM-API streaming-tool-call design.

## Risk and self-critique

- **Stateful UDP via shared listen handle** could surprise plugins
  that dial connect-mode UDP and expect symmetric sendto/recvfrom on
  the same handle. We don't expose that; only `lst`-handle UDP
  supports stateless. Document.
- **HTTP streaming + proxy_url** interaction: the proxy still
  applies, but the response body ships through the proxy too. Test
  with the local-mock SOCKS proxy.
- **Progress channel full** is silent — plugin doesn't know its
  message was dropped. Acceptable for a debug stream; documented.
- **JSON path syntax** doesn't escape dots in keys. A key literally
  containing `.` is unreachable via `_get`. Plugins use `_format`
  + walk-and-rewrite; rare enough not to block v1.
- **Recvfrom packed-i64 ABI** is non-obvious. The host-imports.md
  must document the `>>32` / `& 0xFFFFFFFF` extraction clearly with
  a code example.

## Done definition

- All four items shipped as separate commits with their own tests.
- `host-imports.md` updated for each.
- `plugin doctor` classifies new caps (UDP listen).
- CHANGELOG `v0.38.0` entry covering all four.
- Tag `v0.38.0` pushed; `./stado` rebuilt.
