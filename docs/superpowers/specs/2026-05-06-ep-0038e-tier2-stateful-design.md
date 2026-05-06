# EP-0038e — Tier 2 stateful HTTP client + secrets store

**Status:** approved 2026-05-06; awaiting writing-plans pass.
**Author:** Bartosz Ptaszynski (autonomous design judgment).
**Branch:** `feat/ep-0038e-tier2-stateful`.

## Problem

Two missing host imports from EP-0038's Tier 2 surface:

1. **Stateful HTTP client.** Today `stado_http_request` is one-shot
   per call. A plugin that needs to log into a service and follow
   the resulting session has to re-thread auth state every request,
   parse Set-Cookie headers manually, and replay them on the next
   call. Cookie jar + redirect policy + connection mux belong on
   the host side.

2. **Operator secret store.** Today plugins that need API tokens
   either embed them (a security disaster), read them from
   environment variables (no audit trail, leaks via `env` listings),
   or rely on the operator to wire them through capability-restricted
   filesystem reads (clunky). A first-class `stado_secrets_*` import
   with audited fetch + capability gating + storage outside the
   project tree solves all three.

Both are listed in EP-0038 §B Tier 2 and BACKLOG #8 as the next
blocker for plugins that integrate with real services.

## Locked decisions

| # | Topic | Decision | Reason |
|---|---|---|---|
| Q1 | HTTP client lifecycle | **Plugin creates clients explicitly** via `stado_http_client_create(opts)` returning a typed handle (`http:abc123`). Plugins close them via `stado_http_client_close(handle)` or rely on plugin-instance close-out. Handles are runtime-scoped (one Runtime = one handle table). | Per-tool-call clients defeat the cookie jar's purpose. Per-session shared clients couple HTTP state to session lifetime, which doesn't match the cookie semantics plugins want. Explicit creation = predictable. |
| Q2 | Cookie jar storage | **In-memory per-client.** No on-disk persistence in v0. | Disk persistence requires answering "where" (under StateDir? under session dir?), "who can read it" (cap?), "encryption?" (likely yes). Out of scope; can land separately if real plugins need it. |
| Q3 | Redirect policy | Configurable per-client at create time: `max_redirects` (default 10), `follow_subdomain_only` (default false). Per-request override allowed. | Matches Go's `http.Client.CheckRedirect` shape. Most plugins use defaults; the few that need stricter (auth-token forwarding concerns) get the override. |
| Q4 | Connection mux limits | Per-client `max_connections_per_host` (default 4), `max_total_connections` (default 32). Enforced via `http.Transport.MaxConnsPerHost` + `MaxIdleConns`. | Bounded resource use; prevents a misbehaving plugin from exhausting host fds. |
| Q5 | Capabilities (HTTP client) | `net:http_client` (creates clients + makes requests). The existing host allowlist (`net:http_request:<host>`) governs which hosts can be reached; HTTP client requests honour the same allowlist. | Single-cap surface keeps manifests clean. Reuse existing host allowlist machinery. |
| Q6 | Secrets storage location | `<StateDir>/secrets/<name>` — single value per file, mode 0600, owner-only. Names match `[a-zA-Z0-9_.-]+` (no path separators). | Filesystem-backed = simple, durable, auditable via existing audit framework. No new database. |
| Q7 | Secrets capabilities | `secrets:read:<glob>` for read; `secrets:write:<glob>` for write. Glob is a name-pattern (`api_token_*`); empty pattern means broad access (`secrets:read` matches everything). | Matches `fs:read:<glob>` shape — operator already understands the pattern. Per-secret granularity prevents one plugin from reading another's tokens. |
| Q8 | Audit | Every `stado_secrets_get` / `stado_secrets_put` / `stado_secrets_list` writes an entry to the existing audit log: `{plugin, secret_name, op}` — **never the value**. | Operator must be able to detect a malicious plugin reading secrets it doesn't need. Value-leak in logs would defeat the storage's purpose. |
| Q9 | Secret-value handling on the host | Read from disk into a heap byte slice, copy into wasm memory via `stado_alloc`, then `runtime.KeepAlive` until the wasm call returns. Don't keep them in long-lived host caches; re-read on each `get` so config edits propagate immediately. | Simple. Disk read is fast. Caching introduces stale-value bugs and a longer attack window for memory disclosure. |
| Q10 | CLI surface for operator secret management | `stado secrets set <name>` (reads from stdin or `--from-stdin`/`--from-file`), `stado secrets get <name>` (prints to stdout — operator's choice; not via plugin), `stado secrets list`, `stado secrets rm <name>`. All under a top-level `secrets` cobra command. | First-class operator surface. Plugins access via host imports; operators provision via CLI. Same separation as `plugin install` vs `stado_plugin_*`. |

## Architecture

### `internal/secrets/` — operator secret store

```go
package secrets

// Store is the on-disk operator secret store. Files live at
// <stateDir>/secrets/<name>; each file holds the raw bytes for one
// secret. Mode 0600, owner-only.
type Store struct{ root string }

func NewStore(stateDir string) *Store

// Get reads the named secret. Returns (nil, ErrNotFound) when the
// secret doesn't exist.
func (s *Store) Get(name string) ([]byte, error)

// Put writes the named secret atomically (write-then-rename) and
// chmods to 0600. Idempotent.
func (s *Store) Put(name string, value []byte) error

// List returns the sorted set of secret names. Does not return
// values.
func (s *Store) List() ([]string, error)

// Remove deletes the named secret. Idempotent — missing secret is
// not an error.
func (s *Store) Remove(name string) error

// ValidName returns nil when the name is acceptable
// ([a-zA-Z0-9_.-]+, no path separators, length 1..128).
func ValidName(name string) error
```

### `cmd/stado/secrets.go` — CLI

```
stado secrets set <name> [--from-stdin | --from-file=<path>]
stado secrets get <name>
stado secrets list
stado secrets rm <name>
```

`set` defaults to reading the value from stdin when neither flag is
passed, matching `pass`/`gpg`-style secret tools. `get` writes to
stdout (operator's choice — they're authenticated to the host).

### `internal/plugins/runtime/host_secrets.go` — wasm imports

```go
// stado_secrets_get(name_ptr, name_len, out_ptr, out_max) -> int
// Returns the actual byte count written, or -1 on error.
// Cap-gated: secrets:read:<name-glob> must match `name`.
func registerSecretsGet(builder wazero.HostModuleBuilder, host *Host)

// stado_secrets_put(name_ptr, name_len, value_ptr, value_len) -> int
// Returns 0 on success, -1 on error.
// Cap-gated: secrets:write:<name-glob>.
func registerSecretsPut(builder wazero.HostModuleBuilder, host *Host)

// stado_secrets_list(out_ptr, out_max) -> int
// Writes \n-separated names. Returns total byte count or -1 on error.
// Cap-gated: secrets:read:* (broad-read).
func registerSecretsList(builder wazero.HostModuleBuilder, host *Host)
```

The Host struct gains:
```go
type Host struct {
    // ... existing fields ...

    // Secrets gates stado_secrets_* host imports. Populated when the
    // plugin manifest declares secrets:read:* or secrets:write:*.
    Secrets *SecretsAccess
}

type SecretsAccess struct {
    Store        *secrets.Store
    ReadGlobs    []string // patterns from secrets:read:<glob>; empty = broad
    WriteGlobs   []string // patterns from secrets:write:<glob>; empty = broad
    AuditEmitter func(event SecretsAuditEvent)
}

type SecretsAuditEvent struct {
    Plugin   string // manifest.Name
    Op       string // "get" | "put" | "list" | "remove"
    Secret   string // empty for list
    Allowed  bool
    Reason   string // when !Allowed
}
```

### `internal/httpclient/` — runtime HTTP client implementation

```go
package httpclient

// Client is a stateful HTTP client with cookie jar + bounded mux.
type Client struct {
    inner *http.Client  // http.Transport.MaxConnsPerHost / MaxIdleConns
    jar   http.CookieJar
    opts  ClientOptions
}

type ClientOptions struct {
    MaxRedirects        int
    FollowSubdomainOnly bool
    MaxConnsPerHost     int
    MaxTotalConns       int
    Timeout             time.Duration
    AllowedHosts        []string  // from net:http_request:<host> caps
    AllowPrivate        bool      // from net:http_request_private cap
}

// Request executes one request through the client, applying the
// configured redirect policy + cookie jar. Returns body bytes,
// status code, headers map.
func (c *Client) Request(ctx context.Context, method, url string, headers map[string]string, body []byte) (*Response, error)
```

### `internal/plugins/runtime/host_http_client.go` — wasm imports

Mirrors the existing `stado_http_request` shape but with handle
parameter:

```
stado_http_client_create(opts_ptr, opts_len) -> handle
stado_http_client_close(handle) -> int
stado_http_client_request(handle, method_ptr, method_len, url_ptr, url_len, headers_ptr, headers_len, body_ptr, body_len, resp_out_ptr, resp_max) -> int
```

Cap-gated by `net:http_client` (creates + uses clients). Existing
`net:http_request:<host>` allowlist applies; `net:http_request_private`
applies for RFC1918/loopback. The handle uses the typed-prefix format
`http:<id>` (per BACKLOG #7's typed-handle convention, now landed).

The Host struct gains:
```go
type Host struct {
    // ... existing ...
    NetHTTPClient bool // gates stado_http_client_* family
    httpClients   *httpClientRegistry  // keyed by typed handle, runtime-scoped
}
```

### Capability parsing

Add to `internal/plugins/manifest.go`'s capability classifier:

```go
// secrets:read[:<name-glob>]
// secrets:write[:<name-glob>]
// net:http_client (creates http clients; requests still honour net:http_request:<host>)
```

These slot in alongside the existing `net:*`, `fs:*` etc. cap
parsing. The doctor command (`internal/cmd/plugin_doctor.go`'s
`classifyCapability`) gains entries for them so operators see what
they require.

## File map

| Action | Path | Net lines |
|---|---|---|
| Create | `internal/secrets/store.go` | ~120 |
| Create | `internal/secrets/store_test.go` | ~150 |
| Create | `internal/httpclient/client.go` | ~200 |
| Create | `internal/httpclient/client_test.go` | ~200 |
| Create | `cmd/stado/secrets.go` | ~180 |
| Create | `cmd/stado/secrets_test.go` | ~120 |
| Create | `internal/plugins/runtime/host_secrets.go` | ~150 |
| Create | `internal/plugins/runtime/host_http_client.go` | ~200 |
| Modify | `internal/plugins/runtime/host.go` (add fields) | ~30 |
| Modify | `internal/plugins/runtime/host_imports.go` (wire registrations) | ~10 |
| Modify | `internal/plugins/runtime/cap.go` or wherever caps parse | ~30 |
| Modify | `cmd/stado/plugin_doctor.go` (classify new caps) | ~25 |
| Modify | `cmd/stado/main.go` (register secretsCmd) | ~3 |

Total: ~1400 net (slightly above the 1200 estimate; the audit-event
plumbing through both surfaces takes more glue than expected).

## Testing strategy

### Unit — `internal/secrets/`

- Store happy path: Put → Get round-trips bytes
- Permissions: file mode is 0600 after Put
- ValidName: reject names with `/`, `..`, leading dots, empty,
  excessive length
- List sort order: alphabetical
- Remove idempotent: missing secret is no-op
- Get on missing: ErrNotFound

### Unit — `internal/httpclient/`

- Cookie jar: server returns Set-Cookie; subsequent request to same
  host carries the cookie (httptest.Server)
- Redirect cap: server 302→302→302... stops at MaxRedirects with a
  clear error
- Subdomain redirect rejection: when `FollowSubdomainOnly=true`, a
  redirect to an unrelated host is refused
- Allowed-hosts denial: request to a host not in `AllowedHosts`
  fails with capability error
- Private-IP guard: dial to 127.0.0.1 / 10.x.x.x is blocked unless
  `AllowPrivate=true`
- Timeout: long-running request honours `ctx.Deadline`

### Integration — host imports

- Plugin with `secrets:read:api_*` cap can read `api_token` but
  refused for `db_password`
- Plugin without `secrets:write` cap is refused on Put with audit
  event recorded
- Plugin with `net:http_client` + `net:http_request:example.com` can
  GET example.com via the client; subsequent request reuses cookies
- Plugin without `net:http_client` cannot create a client (host
  import returns -1; audit reason recorded)
- Audit emitter receives expected events for each operation

### CLI smoke

- `stado secrets set test_token` (read from stdin) → file at
  `<state>/secrets/test_token` with mode 0600
- `stado secrets get test_token` → prints raw value
- `stado secrets list` → shows test_token
- `stado secrets rm test_token` → file gone
- `stado secrets get missing` → exit code 1, stderr "not found"

## Risks + mitigations

- **Risk:** secret value leaks via stderr/stdout during plugin
  errors.
  - *Mitigation:* host import never echoes value in error messages.
    The audit log records the operation but never the value. Plugins
    that misuse the value (e.g. log it themselves) are out of
    stado's enforcement scope but documented as "do not log the
    secret value" in the SDK docs.

- **Risk:** cookie-jar leakage across plugins.
  - *Mitigation:* clients are per-handle, handles are per-Runtime.
    A plugin can't access another plugin's client even within the
    same Runtime — wasm memory isolation handles that.

- **Risk:** secret file mode getting widened by accident (e.g.
  operator chmod, restored backup).
  - *Mitigation:* Get re-checks mode on read; refuses to return the
    value if mode > 0600. Logs a warning to stderr telling the
    operator how to fix. Ugly but safe — hard-fail on unexpected
    permissions is the conservative choice.

- **Risk:** HTTP client follows redirect to internal host even
  when allowlisted to public.
  - *Mitigation:* the allowlist applies to BOTH the initial dial
    AND every redirect target. The httpclient's CheckRedirect runs
    the same dial guard as the initial request.

- **Risk:** Handle leaks (plugin creates clients in a loop without
  closing).
  - *Mitigation:* per-Runtime cap on total open clients (e.g. 64
    per Runtime). Beyond that, `stado_http_client_create` returns
    -1. Plugin instance close-out (Runtime close) frees everything.

## Out of scope

- Cookie persistence across stado restarts (in-memory only)
- HTTP/2 stream concurrency tuning beyond defaults
- WebSockets / Server-Sent Events / streaming response body
- Per-secret encryption at rest (file mode 0600 + filesystem
  permissions are the only protection in v0; can layer encryption
  later)
- Multi-user secret stores (single-operator stado; OS user is the
  trust boundary)
- Secret rotation reminders / TTLs
- Per-request HTTP-client config overrides at request time (use
  multiple clients with different ClientOptions instead)

## Verification plan

1. `go test ./... -count=1` clean (modulo pre-existing env failures).
2. `go vet ./...` clean.
3. CLI smoke: `stado secrets set/get/list/rm` round-trip works.
4. Manual smoke with a fixture plugin that reads a secret + makes
   a stateful HTTP request — observe audit events emitted, observe
   cookie reuse across requests.
5. Negative tests: plugin without `secrets:read` cap is refused;
   plugin without `net:http_client` cap is refused.

## Handoff (2026-05-06)

### What shipped

Four commits on `feat/ep-0038e-tier2-stateful`:

1. `6576b70` — secrets store + CLI (`stado secrets set/get/list/rm`).
2. `985b78d` — secrets host imports (`stado_secrets_get/put/list`) + cap parsing.
3. `9dc2c8b` — `internal/httpclient` package (stateful client, cookie jar, dial guard).
4. This commit — wasm host imports `stado_http_client_create / _close / _request`,
   `NetHTTPClient` cap field + parser, `httpClientCount` on Runtime, cleanup in
   `Runtime.Close`, `plugin_doctor` classification for `net:http_client`.

Tests: 131 passing in the `runtime` package (262 RUN lines, covering both subtests
and top-level). New tests in `host_http_client_test.go`: 11 test functions covering
cap denied, happy path create+close, AllowPrivate gate, allowed-host intersection,
per-Runtime cap (64), idempotent close, Runtime.Close cleanup, and end-to-end
httptest request.

### What's left

- **Cookie persistence** — jar state lives in-process only; survives plugin
  instance restarts within a Runtime but not across process restarts. A follow-up
  spec can add optional JSON serialisation of the jar to the secrets store.
- **Streaming responses** — body is fully buffered and base64'd in the JSON
  envelope. Suitable for API payloads; unsuitable for large binary downloads.
  A streaming variant with separate `stado_http_client_read_body` calls is a
  Tier 3 follow-up.
- **Cookie-jar introspection** — no `stado_http_client_get_cookies /
  _set_cookies` yet; plugins can't seed a jar from a known session token.

### Spec deviations

- `HandleTypeHTTPClient` was not added to the public `HandleType` constants in
  `handles.go` — the `"http"` tag is internal to the registry. The wasm SDK
  formats typed handles at the language layer; no public constant is needed until
  a CLI surface exposes http-client handle IDs.
- `_request` signature: 11 parameters (handle + 5×ptr/len pairs + resp_out+resp_max).
  Spec sketched a simpler shape; the 11-arg form matches the existing multi-param
  convention in `host_proc.go` and fits in wasm32 i32-only imports.
- Response JSON uses `body_b64` (base64-encoded body in the envelope) as chosen.
  Confirmed simpler than the multi-accessor alternative.

### What to watch

- The `httpClientCount` counter uses `sync/atomic` and the registry mutex
  separately — a very tight race between _create and _close could briefly
  show count=-1 if close wins; harmless (just allows one extra create), but
  monitor for negative values in production logs.
- Idle-connection cleanup in `closeAllHTTPClients` holds the registry mutex
  briefly to collect, then releases before calling `Close()`. Watch for
  shutdown latency if plugins open many connections to slow hosts.
