# EP-0038g — Net expansion: UDP + Unix sockets + listen/accept

**Status:** drafted 2026-05-06; autonomous design call.
**Author:** Bartosz Ptaszynski.
**Branch:** `feat/ep-0038g-net-expansion`.

## Problem

EP-0038f shipped TCP-only raw socket dial in v0.36.0. The deferred
sub-items in the v0.36.0 CHANGELOG list UDP, Unix sockets,
listen/accept, ICMP, AXFR, HTTP-streaming, FleetBridge messaging,
and `stado_json_*`. This cycle ships the bounded, lowest-risk
subset:

1. **`stado_net_dial("udp", host, port, …)`** — connect-mode UDP
   client. Unblocks NTP, custom binary protocols, DNS-over-UDP
   probing, syslog senders.
2. **`stado_net_dial("unix", path, …)`** — Unix domain socket
   client. Unblocks Docker daemon, systemd, Postgres-via-socket,
   any local IPC.
3. **`stado_net_listen` + `stado_net_accept` (TCP and Unix)** —
   server-side networking. Unblocks webhook receivers, ad-hoc
   proxies, multi-process IPC.

Out of scope this cycle (filed for follow-ups):

- **ICMP** — needs `CAP_NET_RAW` and a different syscall path; can
  wait until last per operator direction.
- **AXFR** — niche; separate import.
- **HTTP request streaming** — needs agent-loop integration design
  (same blocker as `stado_progress`).
- **FleetBridge messaging real impl** — architectural piece.
- **`stado_json_*`** — plugins can JSON-parse in wasm today.
- **UDP stateless** (`send_to`/`recv_from`) — connect-mode covers
  ~95% of real plugin patterns; revisit if a plugin actually needs
  it.

## Locked decisions

| # | Topic | Decision | Reason |
|---|---|---|---|
| Q1 | UDP shape | **Stateful: same `dial`/`read`/`write`/`close` quartet** as TCP, just `transport == "udp"`. Connect-mode only. | One handle type, one API. Stateless mode is a separate import surface; defer until a plugin actually needs it. |
| Q2 | Unix dial transport | **`stado_net_dial("unix", path, port=0, …)`** — `path` carried in the `host` parameter, port ignored. Same `conn` handle type. | Reuses dial+read+write+close. No new imports for unix client. |
| Q3 | Capability vocabulary | `net:dial:udp:<host>:<port>`, `net:dial:unix:<path-glob>`, `net:listen:tcp:<host>:<port>`, `net:listen:unix:<path-glob>`. | Continues `net:dial:tcp:*` shape. Path globs use `filepath.Match`. |
| Q4 | Listener handle type | **New prefix `lst`** (`HandleTypeListener`). | Listener and conn APIs differ — conflating them breaks type-tag dispatch + accept-on-conn would be undefined. |
| Q5 | Accept timeout | **Required parameter**, max 30000ms (`30s`). 0 or negative = use default (5s). Returns -1 on error, -2 on timeout. | Infinite-block accept holds the runtime forever — DoS vector. Plugins re-call in a loop. -2 distinguishes timeout (retry) from error (not). |
| Q6 | Per-Runtime listener cap | **`maxNetListenersPerRuntime = 8`**, separate counter from `netConnCount`. | Listeners hold port/socket-file resources. 8 is generous for real use, prevents ephemeral-port exhaustion. |
| Q7 | TCP listen address policy | **Capability matches verbatim** — `net:listen:tcp:127.0.0.1:8080` allows only loopback; `net:listen:tcp:0.0.0.0:8080` allows any-interface. No implicit fallback. | Operator must explicitly opt-in to public binds. Default-deny on accidental exposure. |
| Q8 | UDP private-IP guard | **Same `NetHTTPRequestPrivate` gate as TCP dial.** Refuses RFC1918/loopback/link-local without the cap. | Consistency. Threat model unchanged across L4 protocols. |
| Q9 | Unix listener cleanup | **Remove socket file on `close_listener`.** Idempotent — already-gone is fine. Also remove on `Runtime.Close` reaper path. | Stale socket files block re-bind on next plugin run; cleanup is the friendly default. |
| Q10 | Unix path constraints | **Refuse paths containing `..`; refuse paths longer than 104 bytes** (BSD `sun_path` limit; conservative across platforms). | Path traversal into other plugins' socket dirs; OS sockaddr_un upper bound. |
| Q11 | UDP read semantics | **Each `stado_net_read` returns one datagram** (or up to `outMax` if truncated; truncation flag not signalled in v1 — plugins use a generous buffer). | `net.Conn.Read` over connect-mode UDP returns one datagram per call. Truncation signalling is post-v1 surface bloat. |
| Q12 | Accept ABI | `stado_net_accept(lst_handle, timeout_ms i32) → i64`. On success returns the new conn-handle ID (uint32 promoted to i64). On error -1; on timeout -2. | Mirrors dial's i64 return. |

## Architecture

### `internal/plugins/runtime/host.go`

Extend `NetDialAccess` with UDP + Unix patterns and add
`NetListenAccess`. Both are nil when no relevant cap was granted.

```go
type NetDialAccess struct {
    TCPGlobs  []NetDialPattern
    UDPGlobs  []NetDialPattern // NEW
    UnixGlobs []string         // NEW (path globs)
}

func (a *NetDialAccess) CanDialUDP(host, port string) bool
func (a *NetDialAccess) CanDialUnix(path string) bool

type NetListenAccess struct {
    TCPGlobs  []NetDialPattern
    UnixGlobs []string
}

func (a *NetListenAccess) CanListenTCP(host, port string) bool
func (a *NetListenAccess) CanListenUnix(path string) bool
```

Capability parser block (mirrors existing `net:dial:tcp:` block,
parsed after the `case` switch because of 5-part splits):

```go
if parts[0] == "net" && len(parts) >= 2 && parts[1] == "dial" {
    if len(parts) >= 5 && parts[2] == "tcp" { /* existing */ }
    if len(parts) >= 5 && parts[2] == "udp" {
        if h.NetDial == nil { h.NetDial = &NetDialAccess{} }
        h.NetDial.UDPGlobs = append(h.NetDial.UDPGlobs, NetDialPattern{
            Host: parts[3], Port: parts[4],
        })
    }
    if len(parts) >= 4 && parts[2] == "unix" {
        if h.NetDial == nil { h.NetDial = &NetDialAccess{} }
        // unix path may contain colons → re-join from parts[3:]
        path := strings.Join(parts[3:], ":")
        h.NetDial.UnixGlobs = append(h.NetDial.UnixGlobs, path)
    }
}
if parts[0] == "net" && len(parts) >= 2 && parts[1] == "listen" {
    // tcp:host:port (5 parts); unix:path (4+ parts)
    // … analogous shape to dial above …
}
```

### `internal/plugins/runtime/host_net.go`

Three additions on top of v0.36.0:

```go
const maxNetListenersPerRuntime = 8

// HandleTypeListener — new typed handle prefix "lst".
//
// netListener is the host-side state for a stado_net_listen-allocated
// handle. Records kind so close_listener knows whether to remove a
// socket file.
type netListener struct {
    l    net.Listener
    kind string // "tcp" | "unix"
    path string // unix only
}

// Imports added:
//   stado_net_listen(transport_ptr, transport_len,
//                    host_ptr, host_len, port_i32) → i64
//   stado_net_accept(lst_handle, timeout_ms_i32) → i64
//   stado_net_close_listener(lst_handle) → i32
```

`stado_net_dial` gains UDP + unix transport branches.
`stado_net_read`/`stado_net_write`/`stado_net_close` work
transparently for all three transports (`net.Conn` interface).

`Runtime.closeAllNetConns` is renamed `closeAllNetResources` and
also reaps listener handles + removes their socket files.

### `cmd/stado/plugin_doctor.go`

Extend `classifyCapability` cases:

```go
case strings.HasPrefix(cap, "net:dial:udp:"):
    return "Dial outbound UDP", "warn"
case strings.HasPrefix(cap, "net:dial:unix:"):
    return "Dial Unix domain socket", "warn"
case strings.HasPrefix(cap, "net:listen:tcp:"):
    return "Listen on TCP port", "warn"
case strings.HasPrefix(cap, "net:listen:unix:"):
    return "Listen on Unix domain socket", "warn"
```

### `docs/plugins/host-imports.md`

Append new section under existing Tier 1 net entries listing the
new transport variants + listen/accept imports + capability
vocabulary.

## Risk and self-critique

- **UDP connect-mode is restrictive.** Doesn't cover broadcast,
  multicast, or "respond to whoever last sent us a packet"
  patterns. Plugins needing those wait for stateless mode. Most
  real protocols (NTP, DNS-over-UDP, syslog client, custom binary
  RPC) use connect-mode → acceptable for v1.
- **8 listeners per runtime might bite a multi-protocol service.**
  Mitigation: test with `for i := 0; i < 9; i++` to confirm clean
  rejection; raise the cap if a real plugin needs more.
- **Unix socket paths on Windows** behave differently. stado
  doesn't target Windows for the runtime today; document, don't
  gate.
- **Accept timeout cap = 30s** could surprise plugins polling a
  slow service. Mitigation: the wasm caller re-loops; for-loop
  pattern is the standard Go pattern. Document.
- **Capability syntax for `net:listen:tcp:0.0.0.0:*`** is verbose.
  Could add `net:listen:tcp:*:8080` shorthand later if friction
  shows up. YAGNI for now.
- **Unix dial doesn't have a private-IP equivalent guard.** Unix
  sockets are inherently local; the path glob is the entire access
  control. Confirmed safe.

## Done definition

- All four new caps parse and gate correctly (`host_test.go`).
- UDP dial round-trip works against a local UDP echo server
  (`host_net_test.go`).
- Unix dial round-trip works against a temp `unixpacket` server
  (`host_net_test.go`).
- TCP `listen`+`accept` round-trip works (`host_net_test.go`).
- Unix `listen`+`accept` round-trip works (`host_net_test.go`).
- Per-Runtime listener cap enforced (`host_net_test.go`).
- Listener-close removes the socket file (`host_net_test.go`).
- `Runtime.Close` reaps stale listeners (`host_net_test.go`).
- Doctor classifies all four new caps (existing test pattern).
- `docs/plugins/host-imports.md` documents the new imports + caps.
- `CHANGELOG.md` v0.37.0 entry.
- `v0.37.0` tagged and pushed.
