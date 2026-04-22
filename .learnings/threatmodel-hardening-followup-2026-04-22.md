Threat-model follow-up fixes landed on 2026-04-22:

- Plugin tool classes cannot be trusted from the manifest alone. Plugin capabilities are plugin-wide, so any tool in a plugin with `fs:read`, `net:*`, `session:*`, or `llm:*` must be treated as `Exec` for safety-gating/audit purposes; `fs:write:*` must be at least `Mutating`.
- JSON-RPC over line-delimited stdin/stdout needs both a per-frame size cap and bounded dispatch concurrency. Limiting only one side still leaves an easy local DoS path.
- `bash` must execute through the sandbox runner when a runner is available. Carrying a `Runner` on the executor is meaningless unless the host path used by exec-class tools actually consults it.
- Post-turn hooks should not bypass a configuration that removed `bash`. Treat hooks as disabled when the active tool registry no longer exposes shell execution.
- Self-update needs a real trust root. Checksums fetched from the same release are not enough; require an embedded minisign pubkey and a release-side minisig.
