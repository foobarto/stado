# Bubblewrap proxy/env wiring

When sandboxing subprocesses through `BwrapRunner`, environment handling
has to happen inside the runner before the `bwrap` argv is finalised.

Why:

- `bwrap --setenv NAME VALUE` sets the child env directly from the
  literal `VALUE`.
- Updating `exec.Cmd.Env` after `runner.Command(...)` only changes the
  environment of the outer `bwrap` process, not the already-baked child
  `--setenv` values.

Practical consequence:

- The runner interface now accepts the caller's candidate env slice.
- Runners filter that env against `Policy.Env`.
- Linux `BwrapRunner` injects proxy env (`HTTP_PROXY` / `HTTPS_PROXY`
  and lowercase variants) at the same point it emits `--setenv`.

If MCP or another subprocess surface needs env passthrough or proxying,
feed the env into the runner up front instead of trying to patch
`cmd.Env` afterwards.
