# Local OTel dev fixture

A single-service Jaeger-all-in-one setup for eyeballing stado's
OpenTelemetry spans without needing a prod collector. Closes Phase 6
verify in PLAN.md.

## Requirements

- `docker` + `docker compose` (or `podman-compose` — the compose file
  is vanilla, nothing Docker-specific)
- Ports 4317, 4318, and 16686 free on localhost

## Run

```sh
cd hack/otel-compose
docker compose up -d          # start Jaeger in the background
```

Wait ~5s for the container to come healthy:

```sh
docker compose ps
# jaeger   healthy   ...
```

## Point stado at it

Two ways: env vars (per-invocation) or config file (persistent).

### Env vars

```sh
export STADO_OTEL_ENABLED=1
export STADO_OTEL_ENDPOINT=localhost:4317
export STADO_OTEL_PROTOCOL=grpc
export STADO_OTEL_INSECURE=1           # Jaeger here isn't TLS
export STADO_OTEL_SERVICE_NAME=stado-dev

stado                                  # or `stado run --prompt hi`
```

### Config (~/.config/stado/config.toml)

```toml
[otel]
enabled      = true
endpoint     = "localhost:4317"
protocol     = "grpc"
insecure     = true
service_name = "stado-dev"
```

`STADO_OTEL_PROTOCOL=http` on port 4318 works too if your host
environment can't reach 4317 for some reason.

## Browse

Open <http://localhost:16686> — the service dropdown will show
"stado-dev" (or whatever `service_name` you set) after stado has
emitted at least one span. Click *Find Traces* to see the hierarchy
`stado.session → stado.turn → stado.tool_call → stado.provider.stream`
covering the expected span names (metrics.go:48-66).

## What to look for

Per PLAN §6.2 the expected hierarchy is:

```
stado.session           ← long-lived (whole TUI lifetime)
└── stado.turn          ← one per (user message, tool loop) cycle
    ├── stado.tool_call ← one per tool execution
    │   └── stado.sandbox.exec
    └── stado.provider.stream ← one per LLM API round trip
```

Plus supplementary spans:
- `stado.session.fork` when `session fork --at` runs
- `stado.session.resume` when `OpenSession` reattaches a worktree
  (the cross-process span-link is what makes forks look continuous
  across shell boundaries)

## Cleanup

```sh
docker compose down -v
```

`-v` drops Jaeger's in-memory trace buffer too — everything you ran
this session disappears.

## Metrics

Jaeger-all-in-one is trace-only. stado's metrics instruments
(`stado_tool_latency_ms`, `stado_tokens_total`,
`stado_cache_hit_ratio`, `stado_approval_rate`,
`stado_sandbox_denials_total`) won't show here — point them at a
Prometheus-compatible OTLP backend (or Grafana Cloud OTLP endpoint)
instead. A production compose with the full
Jaeger + Prometheus + Grafana stack is future work; this fixture
covers the PLAN §6 verify step.
