# state-dir-info — `cfg:state_dir` capability example

The minimal plugin demonstrating the EP-0029 `cfg:*` capability
vocabulary. Calls `stado_cfg_state_dir` and returns the resolved
path as JSON.

## What this example demonstrates

- Declaring a `cfg:*` capability in the manifest.
- Calling the corresponding `stado_cfg_<name>` host import.
- Handling the three response shapes: success (n > 0), empty
  value (n == 0, host caller didn't populate), and error
  (n == -1, value exceeds buffer or — at link time — the
  capability wasn't declared).

## Build + install

```sh
stado plugin gen-key state-dir-info-demo.seed       # one-time
./build.sh                                            # compile + sign
stado plugin trust <pubkey-hex> "stado example"       # one-time per signer
stado plugin install .
```

## Run

```sh
stado plugin run state-dir-info-0.1.0 state_dir '{}'
```

Output (example, paths vary by operator):

```json
{"state_dir":"/home/you/.local/share/stado"}
```

On Atomic Fedora / Silverblue (where `/home → /var/home`) the
returned path will look like `/var/home/you/.local/share/stado`
because stado follows OS-level symlinks above HOME (EP-0028).

## Use as a template

Plugins that need to compose paths under stado's state dir
(operator-tooling for installed plugins, a memory-store inspector,
a worktree-list tool) start here. Add the additional capabilities
you need:

```json
{
  "capabilities": [
    "cfg:state_dir",
    "fs:read:/abs/path"
  ]
}
```

The `fs:read` capability still needs an absolute path — the
plugin can't compose `<state-dir>/plugins` at install time. That
operator-friendliness gap is captured in the EP-0029 §"Future
capabilities" + §"Open questions" sections; expect a follow-up
EP that introduces a path-template syntax so a plugin can declare
`fs:read:cfg:state_dir/plugins` and have the parser substitute
the resolved value at install time.

## See also

- [`docs/eps/0029-config-introspection-host-imports.md`](../../../docs/eps/0029-config-introspection-host-imports.md)
  — the design doc.
- [`docs/features/plugin-authoring.md`](../../../docs/features/plugin-authoring.md)
  — first-time-author walkthrough.
- [`plugins/examples/webfetch-cached/`](../webfetch-cached/) —
  the other v0.26.0 surface demo (wraps a bundled-tool host
  import with a cache).
