---
ep: 40
title: Bundled local inference — managed llama-server sidecar
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Draft
type: Standards
created: 2026-05-07
see-also: [12, 19]
history:
  - date: 2026-05-07
    status: Draft
    note: Initial draft.
---

## Problem

stado already supports four LLM provider families: native Anthropic /
OpenAI / Google SDKs plus a generic OpenAI-compatible (OAI-compat)
client used to talk to local runners. `internal/providers/localdetect/`
probes ollama / llamacpp / vllm / lmstudio at known localhost ports, and
the OAI-compat client wires up to whichever ones answer.

What stado does **not** ship is an inference engine. Operators who want
to route any traffic to a local model must install ollama, start
`llama-server`, or stand up vllm themselves. Two consequences:

1. Operators who would otherwise route cheap, latency-tolerant calls
   to a local model — to push down on Anthropic / OpenAI spend — pay
   the setup tax and often skip it.
2. The "OAI-compat preset" surface is correct for users who already
   run a local engine, but useless on a fresh install.

This EP introduces a managed local engine: stado downloads, supervises,
and updates a pinned `llama-server` build, picks (or accepts) a model,
and exposes the result as a normal provider. No part of stado's loop
auto-routes to it; users opt in via the same provider-pick path that
selects between Anthropic and OpenAI today.

## Goals

- Reduce hosted-API spend for operators willing to take a quality
  trade-off for sessions where a local model is sufficient.
- Keep stado's release artifact size, signing chain, and reproducible-
  build posture unchanged. The engine is downloaded post-install and
  verified against SHA256s pinned in the (already cosign-signed)
  release manifest.
- Treat "managed local" as just another provider preset. No router,
  no traffic classification, no per-call magic.
- Provide a complete operator surface (`stado inference …`) for
  install / lifecycle / model management / diagnostics.

## Non-goals

1. **No router or traffic classification.** Managed-local is selected
   the same way Anthropic or OpenAI is — explicitly.
2. **No automatic model selection per request.** The active model is
   what `inference up` was started with (or `default_model` from
   config).
3. **No multi-model concurrent serving.** `llama-server` is single-
   model-per-process, like ollama and LM Studio. A future EP can add
   a multi-process / proxy story if it earns its way in.
4. **No automatic restart on engine crash.** Surfaced in `doctor`;
   operator runs `up` again. Auto-restart hides correlated failures
   and tempts retry storms.
5. **No remote management.** All endpoints bind 127.0.0.1.
6. **No fancy GPU detection.** v1 picks Metal on macOS, Vulkan on
   Linux/Windows by default, CPU as fallback, CUDA opt-in via config
   when the catalog has a matching binary. ROCm/HIP, multi-GPU, driver-
   version pickiness are future work.
7. **No supervised resource limits.** The operator sets `--ctx`,
   `--parallel`, `kv_cache_type`. If the model OOMs, llama-server
   fails to start; stado surfaces the failure.
8. **No stado-side sandboxing of `llama-server`.** It is a pure-compute
   binary listening on loopback; it does not act on the host on stado's
   behalf. `internal/sandbox/` exists to mediate that class of access,
   not to wrap arbitrary third-party engines.
9. **No telemetry / phone-home** about local inference usage.
10. **No bundled model weights in stado release.** Curation is a SHA-
    pinned download list; weights ship over HTTPS on `inference pull`.

## Design

### Package layout

```
internal/inference/
  inference.go          # Manager: concrete struct
  catalog/
    catalog.go
    models.json         # curated GGUF entries
    engine.json         # pinned llama-server per (os, arch, backend)
  store/
    bins.go             # engine install / uninstall / glibc precheck
    models.go           # pull / register-byo / ls / rm / gc / prune
    state.go            # JSON state + flock + identity verify
    extract.go          # safe archive extraction
  lifecycle.go          # spawn detached, identity-verified stop, port rotation
  backend_detect.go     # v1 simple

cmd/stado/inference.go         # subcommand wiring
internal/config/inference.go   # [inference] section parsing
```

`internal/providers/oaicompat/` and `internal/providers/localdetect/`
are **not modified**. Managed-local appears as a provider preset whose
endpoint resolves once `Manager.Endpoint()` returns ready; it pulls
`MaxContextTokens` from the active model's catalog `context_window`.

### `Manager`

Concrete struct, no interface. Five public methods:

```go
func New(cfg *config.Inference, paths xdg.Paths) *Manager
func (m *Manager) Status(ctx) Status
func (m *Manager) Up(ctx, opts UpOpts) error           // honors --single-model
func (m *Manager) Down(ctx) error                      // identity-verified stop
func (m *Manager) Load(ctx, modelID string) error      // model swap (port rotation)
func (m *Manager) Endpoint() (url, token string, ok bool)
```

### Catalog schemas

`models.json` — curated GGUFs:
```json
{
  "id": "qwen2.5-coder-7b-q4-k-m",
  "name": "Qwen 2.5 Coder 7B (Q4_K_M)",
  "hf_repo": "Qwen/Qwen2.5-Coder-7B-Instruct-GGUF",
  "hf_revision": "<commit-sha>",
  "filename": "qwen2.5-coder-7b-instruct-q4_k_m.gguf",
  "sha256": "...",
  "size_bytes": 4683074336,
  "license_spdx": "Apache-2.0",
  "context_window": 32768,
  "min_ram_gb": 8,
  "tags": ["coder", "general"]
}
```

HF revision is the **commit SHA**, not a branch — the filename–at-tip
contract is too weak.

`engine.json` — pinned llama-server:
```json
{
  "version": "b4400",
  "binaries": [
    { "os": "linux", "arch": "amd64", "backend": "cpu",
      "url": "...", "sha256": "...",
      "archive_path": "llama-server", "min_glibc": "2.31" },
    { "os": "linux", "arch": "amd64", "backend": "vulkan", ... },
    { "os": "darwin", "arch": "arm64", "backend": "metal", ... }
  ]
}
```

One stado release pins exactly one llama.cpp release across all `(os,
arch, backend)` rows.

### State file

`$XDG_STATE_HOME/stado/inference/server.json` (0600):

```json
{
  "pid": 12345,
  "started_at": "2026-05-07T17:30:00Z",
  "exe_path": ".../bin/b4400/vulkan/llama-server",
  "exe_sha256": "...",
  "argv_marker": "stado-managed-<uuid>",
  "host": "127.0.0.1",
  "port": 38271,
  "token_path": ".../server.token",
  "engine_version": "b4400",
  "backend": "vulkan",
  "model_id": "qwen2.5-coder-7b-q4-k-m",
  "single_model": false,
  "draining": []
}
```

`server.token` is 32 random bytes, 0600. `.activity` is a zero-byte
sentinel touched via `os.Chtimes` on every managed-local request
(see Activity tracking below).

`manager.lock` (flock, exclusive) wraps every mutating verb: `Up`,
`Down`, `Load`, `Install`, `Uninstall`, `Pull`, `Rm`, `Gc`, `Prune`.
Read-only verbs (`Status`, `Endpoint`) take a shared lock.

### CLI surface

```
stado inference install   [--version <pin>] [--backend <b>] [--accept-downloads]
stado inference uninstall [--version <pin>]
stado inference up        [--single-model <id>] [--ctx <N>] [--parallel <N>]
                          [--keep-alive <dur>] [--accept-host-process]
stado inference down      [--force]
stado inference status    [--json]
stado inference pull      <id|hf-spec> [--accept-downloads]
stado inference load      <id> [--force]
stado inference ls        [--json]
stado inference rm        <id>
stado inference gc        [--retain N] [--dry-run]
stado inference prune     [--dry-run]
stado inference doctor
```

`hf-spec` form: `<repo>@<rev>:<filename>`. Revision is required — no
implicit `main`. `--force` on `down` and `load` skips the drain wait.

### Configuration

```toml
[inference]
auto_up_on_session = false       # if true, sessions selecting managed-local
                                 # trigger Up; off by default
keep_alive       = "15m"         # "always" / "session" / Go duration
default_model    = ""            # required by `up` if no --single-model

[inference.engine]
version = ""                     # empty = stado-pinned
backend = ""                     # empty = autodetect (metal/vulkan/cpu)

[inference.runtime]
max_context_tokens = 0           # 0 = derive from model + RAM
parallel_slots     = 0           # 0 = derive from cores
kv_cache_type      = "f16"       # f16 / q8_0 / q4_0
sleep_idle_seconds = 60          # llama-server flag — VRAM unload only
load_grace_seconds = 30          # drain budget on Load
model_quant        = ""          # advisory for unqualified pulls

[inference.paths]
data_dir  = ""                   # $XDG_DATA_HOME/stado/inference
state_dir = ""                   # $XDG_STATE_HOME/stado/inference
```

Project `.stado/config.toml` may **read** any of these but **cannot**
trigger an install or pull. Mutating verbs require explicit CLI
invocation; `--accept-downloads` is the audit knob for non-interactive
contexts (CI scripts, headless setups).

### Lifecycle data flow

**`install`**:
1. Refuse if `sandbox.IsRewrapped()` returns true or `[sandbox] mode
   = "external"`, unless `--accept-host-process`.
2. `gpudetect.Pick()` → backend.
3. Look up `engine.json` row; verify host glibc against `min_glibc`.
4. flock exclusive. HTTPS download to `*.partial`, verify SHA256,
   atomic rename, safely extract under `<data_dir>/bin/<version>/
   <backend>/`. Final perms 0755 on engine, 0644 on data, 0700 on
   dirs.

**`up`** (`--single-model X` optional):
1. flock exclusive. If `state.json` exists and identity-verifies,
   return "already up at port N".
2. If `--single-model X`, ensure model X is in store.
3. Pick port: `net.Listen("tcp4", "127.0.0.1:0")` → record →
   `Close`. Generate token → write `server.token`.
4. Spawn `llama-server --host 127.0.0.1 --port <p> --api-key-file
   <path> --alias <model_id> --model <gguf-path> --ctx-size <c>
   --parallel <n> --cache-type-k <q> --cache-type-v <q>
   --sleep-idle-seconds <s>` detached (`setsid` Unix /
   `CREATE_NEW_PROCESS_GROUP` Windows), with env
   `STADO_MANAGED_UUID=<uuid>` for identity-verify.
5. On bind error within ~3s, retry up to 3× with new port.
6. Tail server log or poll `/v1/models` with our token until ready.
7. Atomic write of `state.json`.

**Per-request** (provider plumbing → `Endpoint()` → oaicompat client):
1. Manager wraps the oaicompat client's `http.RoundTripper`. The
   wrapper touches `<state_dir>/.activity` (`os.Chtimes`) on request
   start, on stream end, and at most every ~20s during a long stream.
2. `state.json` is **not** rewritten on the hot path.

**`load X`** (model swap, no service interruption):
1. flock exclusive. Identity-verify current. Refuse with
   `ErrPinnedSingleModel` if pinned.
2. Spawn new engine on fresh random port + token (steps 3–6 from
   `up`).
3. Wait for new engine `/v1/models` to return `id == X` under our
   token.
4. Atomic write of new `state.json` pointing at new port/token. Old
   PID + endpoint moved into `state.draining[]` with `started_at`.
5. Manager's in-flight counter for the old endpoint drains as
   sessions finish their streams. When count hits 0 or
   `load_grace_seconds` elapses → SIGTERM old PID, then 10s
   → SIGKILL. `--force` skips the wait.
6. Sessions that started after step 4 see only the new engine. In-
   flight streams against old continue uninterrupted.

**Idle / lazy cleanup** (every `stado` invocation, very early):
1. If `state.json` exists, `stat .activity`. If `now - mtime >
   keep_alive`, run `Down()` silently.
2. Catches "left it running and forgot" without a supervisor process.
   Trade-off: an idle sidecar sits with VRAM unloaded (`--sleep-idle-
   seconds`) but a live process until the next stado invocation, which
   is acceptable for the operator class this serves.

**`down`**:
1. flock exclusive. Identity-verify. SIGTERM, wait 10s, SIGKILL.
2. Remove `state.json` and `.activity`. Keep `server.log` (rotated).

### Backend detection (v1)

- macOS → `metal`.
- Linux + `nvidia-smi` exits 0 + `engine.json` has CUDA pinned for
  `(amd64, current-glibc)` + config opt-in → `cuda`. Otherwise →
  `vulkan` if `vulkaninfo` available, else `cpu`.
- Windows → same idea (cuda → vulkan → cpu).
- User override: `[inference.engine] backend`.

### Trust model

| Artifact | Verification |
|---|---|
| stado release | cosign + minisign (existing). |
| `llama-server` binary | SHA256 pinned in stado-shipped `engine.json`. HTTPS-only `http.Client`, `CheckRedirect` rejects non-HTTPS redirects. Refused on mismatch. |
| Curated GGUF | SHA256 + HF commit SHA pinned in `models.json`. Same HTTPS rules. |
| BYO GGUF | SHA256 computed at registration, displayed; `path` + `mtime` recorded; `tainted` flag in `status`/`ls` if mtime changes. Not enforced — operator owns the call. |
| Bearer token | 32 random bytes, 0600, passed to llama-server as `--api-key-file <path>` (never in argv). |
| Glibc | `ldd --version` parsed at install; refuses with actionable error if `min_glibc` exceeds host. |

### Identity-verified stop

Before any `kill(2)` against the recorded PID:

1. PID is alive (`Signal(0)` on Unix, `OpenProcess` on Windows).
2. `/proc/<pid>/exe` symlink target (Linux) or equivalent matches
   `state.exe_path`.
3. Process environment includes `STADO_MANAGED_UUID == state.argv_marker`
   (read-only inspection where supported; on platforms where it isn't,
   fall back to argv prefix.).
4. `/v1/models` under the recorded port + token returns the expected
   `model_id` (and the `--alias` we set).

Any mismatch → refuse to kill, surface in `doctor`. `--force-cleanup-
state` removes stado's state file but does **not** kill the foreign
process.

## Migration / rollout

stado has no prior managed-inference subsystem. There is nothing to
migrate. Operators who already point stado at ollama or LM Studio via
existing OAI-compat presets are unaffected — `localdetect` keeps
finding their server, and the new managed-local provider is a
separate preset entry.

The first stado release shipping this EP carries:

- Embedded `engine.json` with one pinned llama.cpp release across the
  supported `(os, arch, backend)` matrix.
- Embedded `models.json` with a small curated catalog (target: 2–4
  entries, picked for permissive licenses + reasonable RAM tiers).
- New `[inference]` config section with the defaults above.
- New `stado inference` subcommand.

Subsequent stado releases that bump the pinned llama.cpp version do
**not** auto-upgrade an installed engine. `stado inference status`
surfaces the version skew; `stado inference install --version
<new>` is the upgrade verb. `prune` removes orphaned old versions.

## Failure modes

- **Download SHA mismatch.** Refuse install/pull, surface URL +
  expected vs. actual SHA. No "trust this once" override — the
  operator can edit catalog or use BYO if they know what they're
  doing.
- **Glibc too old.** `ErrGlibcTooOld` at install with a clear
  message and the catalog row's `min_glibc`. Doctor explains.
- **Bind race / port in use.** Up to 3 retries with new port. After
  3, surface the bind error and the port stado tried.
- **llama-server fails to start** (OOM, malformed model, missing
  GPU runtime). stado tails the server log; on missing-readiness
  within a ~30s timeout, kills the process, removes state, surfaces
  the last 50 log lines.
- **Identity mismatch on stop.** Refuse to `kill`. `doctor`
  surfaces the mismatch in detail. `--force-cleanup-state` deletes
  stado's state without killing.
- **llama-server crash mid-session.** oaicompat client gets
  connect-refused; manager's `Status` notices port stopped
  responding, marks state `crashed`, runs cleanup. No auto-
  restart.
- **`load` swap, new engine fails health.** Old engine remains
  authoritative. New engine PID killed. State unchanged.
- **`load` swap, drain timeout.** SIGTERM old after
  `load_grace_seconds`; in-flight streams see connection drop.
  Operator can set `--force` to skip the wait deliberately.
- **Stado runs under restrictive sandbox-wrap.** `Up` / `Install`
  / `Pull` refuse with `ErrSandboxRefused` and a hint to run
  outside the sandbox or pass `--accept-host-process`.
- **Two stado invocations race on `up`.** flock serializes them;
  the second sees "already up at port N".

## Test strategy

- **Unit, no network.** Catalog parsing; `PickDefault`;
  `extract.go` path-traversal / symlink rejects; `state.go` JSON
  round-trip; identity-verify mismatch matrix; port-bind retry;
  sandbox refusal triggers; `keep_alive` / lazy-cleanup math;
  flock contention.
- **Integration with a fake llama-server.** A small Go test binary
  pretends to be `llama-server`: responds to `/v1/models`, accepts
  `--api-key-file`, `--alias`, `--model`, `--port`, simulates
  controllable startup latency and health states. Drives full
  `Up` → `Load` (port rotation) → `Down` flows; in-flight counter
  drain; identity verify against fake exe; lazy cleanup. Lives
  under `internal/inference/lifecycle_test.go` with `//go:build
  integration`.
- **End-to-end, opt-in.** A separate `make test-inference-e2e`
  target (excluded from CI default) downloads a tiny curated model
  and a pinned llama.cpp build, runs a single chat completion.
  For local manual verification + occasional CI matrix runs.
- **Fuzz.** Catalog parser; archive extractor.

## Open questions

- **Catalog provenance.** Models pinned in `models.json` need a
  process for adding / updating entries (license review, SHA
  computation, HF-revision capture, size measurement). Probably a
  small `hack/inference-catalog/` script invoked manually before a
  release. Not worth a new EP, but worth a contributor doc.
- **Windows lifecycle.** Windows lacks `setsid` and `os.Chtimes`
  has different mtime semantics on FAT/exFAT. The state-file
  lifecycle is straightforward; the detached spawn and identity
  verify need Windows-specific paths under build tags. Detail
  deferred to implementation; the `inference` package will isolate
  it behind one OS-specific file per platform.
- **`auto_up_on_session = true` UX.** The default is `false` —
  the operator picks the managed-local provider explicitly and
  runs `up` themselves. If we later flip the default, sessions
  that select managed-local trigger `Up` transparently; this needs
  a UX story for "downloading 5GB model, please wait" that doesn't
  exist today. Out of scope for v1.
- **Future multi-model serving.** llama-server is single-model.
  Operators wanting to keep an embedding-sized model warm
  alongside a chat model will eventually want it. The cheapest
  path is a thin process pool + port-multiplexer; that is a
  separate EP.

## Decision log

### D1. Manage, don't ship

- **Decided:** stado downloads `llama-server` from upstream
  llama.cpp releases at `inference install` time, verified against
  SHAs pinned in stado's own (cosign-signed) release manifest.
- **Alternatives:** ship the engine inside the stado tarball; static-
  link via cgo into the stado binary; use a pure-Go inference
  runtime; refuse to install anything (BYO).
- **Why:** keeps the stado release artifact size, signing path, and
  reproducibility unchanged. The trust chain stays "verify stado
  with cosign → stado verifies engine against pinned SHA," which is
  no weaker than the existing supply chain.

### D2. Engine: llama-server only

- **Decided:** stado manages exactly `llama-server` from upstream
  llama.cpp. Operators who already run ollama or LM Studio keep
  using `localdetect` against those.
- **Alternatives:** manage ollama instead; manage both with a flag.
- **Why:** smallest abstraction; ollama is itself a managed thing
  and "stado manages a thing that manages a thing" is more rope
  than the value justifies. Coexistence with existing ollama / LM
  Studio installs is solved at the `localdetect` layer.

### D3. Curated default + BYO

- **Decided:** `models.json` ships a small curated catalog (per-
  RAM-tier defaults, SHA + HF-revision pinned, license vetted).
  `inference pull <id|hf-spec>` adds catalog entries; `pull <local-
  path>` registers BYO GGUFs.
- **Alternatives:** BYO only; curated only; defer the model question
  to a separate EP.
- **Why:** the operator class this EP serves expects a working
  default ("just give me something reasonable") *and* the freedom
  to bring their own.

### D4. Long-running detached sidecar

- **Decided:** `llama-server` runs as a process that survives
  stado exits. Lifetime bounded by an idle timeout (`keep_alive`,
  default 15m) enforced via lazy cleanup at the next stado
  invocation, plus explicit `inference down`.
- **Alternatives:** tie engine lifetime to the stado parent
  process; per-session spawn; explicit-only (no idle timeout); a
  persistent supervisor process.
- **Why:** cold-starting a 7B/8B Q4 model is 5–10s; per-session is
  user-visible latency. A persistent supervisor adds a process
  to manage; lazy cleanup at session start covers the "left it
  running and forgot" case without one. The `--sleep-idle-
  seconds` flag handles VRAM pressure during the gap.

### D5. No router; managed-local is just another provider

- **Decided:** managed-local is selected the same way Anthropic or
  OpenAI is — via the existing provider-pick path. No traffic
  classification, no per-call routing.
- **Alternatives:** route auxiliary calls (commit messages,
  summaries) automatically; route by token/complexity heuristic;
  route subagent loops only.
- **Why:** automatic routing means hidden quality regressions when
  the local model is wrong-shaped for the call. The operator owns
  the trade-off.

### D6. Single-model at a time

- **Decided:** one active model per running sidecar. `inference
  load <id>` swaps via port rotation; `inference up --single-model
  <id>` pins the sidecar so swap requests fail loudly. Matches
  ollama and LM Studio's effective behaviour.
- **Alternatives:** multi-model with hot-swap (llama-server doesn't
  support); pool of sidecars (defers to a future EP).
- **Why:** the v1 surface that delivers the most spend reduction
  per line of code shipped. Pooling is real complexity (port
  multiplexing, model selection per request, routing); not earning
  its way in yet.

### D7. No stado-side sandbox of llama-server

- **Decided:** the engine runs as a regular host child process.
  `internal/sandbox/` is not applied to it.
- **Alternatives:** wrap llama-server in Landlock + seccomp + a
  private netns via pasta, similar to tool subprocesses.
- **Why:** stado's sandbox mediates host access on stado's behalf
  (file edits, command exec). A pure-compute binary listening on
  loopback isn't in that class. Upstream security bugs are
  upstream's responsibility.

### D8. 127.0.0.1 + bearer token via `--api-key-file`

- **Decided:** bind loopback only; generate a 32-byte token at
  startup; pass via `--api-key-file <0600 path>`, not `--api-key`,
  to keep the secret out of argv. oaicompat client wired with the
  bearer.
- **Alternatives:** unauthenticated localhost; unix socket; pass
  via env.
- **Why:** localhost ports are reachable by any local user / sibling
  process. Bearer auth + `--api-key-file` is no weaker than ollama's
  default and avoids `ps`-visible secrets.

### D9. Port-rotation `Load` swap

- **Decided:** model swap spawns a new engine on a fresh random
  port + token, waits for `/v1/models` to confirm `id`, atomic
  state write, then drains old engine via stado-tracked in-flight
  count with bounded grace.
- **Alternatives:** SIGTERM old then start new (downtime); rely on
  503 retry from the oaicompat client.
- **Why:** stado's oaicompat path streams tokens out to the user;
  there is no replay boundary for partial output, so 503 mid-stream
  is user-visible breakage. Port rotation gives new requests
  immediate continuity and lets old streams finish.

### D10. Identity-verified stop

- **Decided:** before any `kill`, verify pid alive + exe path
  matches + argv marker matches + `/v1/models` health-check under
  our token.
- **Alternatives:** trust pidfile; check exe-path only.
- **Why:** PID reuse on Unix is real. The cost of these checks is
  microseconds on the rare lifecycle path; the cost of a mistake
  is killing a foreign process the operator started by hand.

### D11. flock around mutating verbs

- **Decided:** `manager.lock` (file-lock, exclusive) wraps `Up`,
  `Down`, `Load`, `Install`, `Uninstall`, `Pull`, `Rm`, `Gc`,
  `Prune`. `Status` and `Endpoint` take a shared lock.
- **Alternatives:** advisory lock in a single TUI process only;
  no lock (assume serial CLI use).
- **Why:** stado is increasingly multi-surface (TUI, ACP, headless,
  background agents). Two `inference up` invocations otherwise race
  into corrupt partial downloads or two servers fighting for the
  same port.

### D12. `.activity` sentinel + `os.Chtimes`

- **Decided:** activity tracking via mtime updates on a zero-byte
  sentinel file, touched on request start / stream end / every ~20s
  during long streams. State-file rewrites only on lifecycle changes.
- **Alternatives:** rewrite `state.json` on every request; in-
  memory only (no idle timeout); separate per-request audit log.
- **Why:** mtime updates are metadata-only and cheap; rewriting
  state.json on every request takes the lifecycle flock onto the
  hot path. Lazy cleanup tolerates losing a few seconds of
  precision.

### D13. Project config can configure but not trigger

- **Decided:** project `.stado/config.toml` may set `[inference]`
  values but cannot cause downloads or installs. Mutating verbs
  require explicit CLI invocation or `--accept-downloads`.
- **Alternatives:** project config triggers on first session.
- **Why:** `cd`-ing into a project should never silently start a
  multi-GB download. The project-overlay path
  (`internal/config/config.go:631`) is otherwise a footgun for any
  surface that integrates with project config.

### D14. v1 GPU detection: simple

- **Decided:** Metal on macOS, Vulkan on Linux/Windows by default,
  CPU fallback, CUDA opt-in via config. ROCm / multi-GPU / driver
  picks are future work.
- **Alternatives:** ship per-platform detection logic for every
  supported backend; ship a "fat" binary.
- **Why:** the catalog matrix is bounded by what stado releases pin.
  Operators who need CUDA flip a config flag; everyone else gets
  Vulkan, which llama.cpp supports broadly. Detection complexity
  earns its way in only when the catalog grows.

### D15. Sandbox-wrap interplay

- **Decided:** `inference up` / `install` / `pull` refuse if
  `sandbox.IsRewrapped()` is true or `[sandbox] mode = "external"`,
  unless `--accept-host-process` is passed. Doctor explains.
- **Alternatives:** silently allow; require manual unwrap.
- **Why:** under restrictive wrap, network unshare or FS-scope
  lock means the install/download path will fail in confusing
  ways. Refusing early with a clear message beats partial state
  + cryptic failure.

## Related

- **EP-12 — Release integrity and distribution.** Defines the
  cosign + minisign chain that this EP's pinned-SHA approach plugs
  into.
- **EP-19 — Model / provider picker UX.** The TUI surface where
  managed-local appears as another provider preset.
- **`internal/providers/oaicompat/`** — wire protocol; reused
  unchanged.
- **`internal/providers/localdetect/`** — independent path for
  operator-installed ollama / LM Studio / vllm / external
  llama.cpp; reused unchanged.
- **`internal/sandbox/wrap.go`** — provides
  `RewrappedEnvVar = "STADO_REWRAPPED"`; the helper
  `sandbox.IsRewrapped()` is added by this EP.
