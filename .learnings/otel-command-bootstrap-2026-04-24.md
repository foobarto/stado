The repo already had OTel instrumentation in `runtime`, providers, and session fork/resume code, but the command surfaces were not starting `internal/telemetry.Start(...)`, so all of those spans were effectively no-ops.

Fix pattern:

- add one small command helper that:
  - builds `telemetry.Config` from loaded `config.Config`
  - merges `telemetry.ConfigFromEnv()` as fallback for `STADO_OTEL_ENABLED` and `OTEL_EXPORTER_OTLP_ENDPOINT`
  - starts telemetry once per process
  - flushes shutdown on exit
- wrap only the runtime-facing commands with it:
  - `stado`
  - `stado session resume`
  - `stado run`
  - `stado headless`
  - `stado acp`
  - `stado mcp-server`
  - session fork / revert flows

This keeps version/config-style commands fast and side-effect free while making the shipped spans real where they matter.
