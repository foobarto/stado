# Plugins

This tree is the product-facing plugin catalog.

## Default

| Plugin | Source | Enabled by default | Notes |
|--------|--------|--------------------|-------|
| [`default/auto-compact/`](default/auto-compact/) | Go | yes | Shipped as a bundled background plugin. When the TUI hits the hard context threshold, stado emits a `context_overflow` event, the plugin forks a compacted child session, and the blocked prompt is replayed there. The same source can also be built/signed/installed manually for explicit `plugin run --session` workflows. |

## Examples

Opt-in samples live under [`examples/`](examples/). They are not loaded
automatically and are intended for authoring, signing, installation, and
override experiments.

- [`examples/README.md`](examples/README.md)
- [`examples/hello/`](examples/hello/)
- [`examples/hello-go/`](examples/hello-go/)
- [`examples/session-inspect/`](examples/session-inspect/)
- [`examples/session-recorder/`](examples/session-recorder/)
- approval-wrapper examples for `bash`, `write`, `edit`, and `ast_grep`
