# Demo plugins

Plugin-author showcases. Each one exists to validate exactly one
host-import surface or approval-flow path. **Not meant for end
users** — they're either trivial greeters or `// Manual test tool
only` modules.

Use these as:
- Copy-modify starting points for your own plugin (`hello-go`).
- Living examples of host-import calling conventions when reading
  [`docs/plugins/host-imports.md`](../../docs/plugins/host-imports.md).
- Test fixtures when changing the approval-flow or any host-side
  capability gating — install one of the matching demos and verify
  end-to-end before merging.

Install procedure is identical to `optional/` — see the parent
[`README.md`](../README.md). Just don't expect these to do anything
useful at the agent level; the value is in their *shape*, not their
output.

## Index

| Demo | Surface validated |
|---|---|
| [`hello/`](hello/) | Minimal Zig wasm32 plugin (~800 B). Freestanding, no runtime. Smallest possible plugin. |
| [`hello-go/`](hello-go/) | Minimal Go reactor plugin (~3 MB) via `-buildmode=c-shared`, WASIp1. Canonical Go starter. |
| [`approval-bash-go/`](approval-bash-go/) | `ui:approval` gate before delegating to the standard `bash` host wrapper. Non-default override pattern. |
| [`approval-write-go/`](approval-write-go/) | `ui:approval` gate before the standard `write` host wrapper. |
| [`approval-edit-go/`](approval-edit-go/) | `ui:approval` gate before the standard `edit` host wrapper. |
| [`approval-ast-grep-go/`](approval-ast-grep-go/) | `ui:approval` only on `ast_grep` rewrite mode (search-only runs directly). Demonstrates conditional approval. |
| [`approval-demo-go/`](approval-demo-go/) | Bare approval-flow exerciser. Use only when verifying the UI surface itself. |
| [`choose-demo-go/`](choose-demo-go/) | `stado_ui_choose` host import. Operator-input wire format. |
| [`expect-demo-go/`](expect-demo-go/) | `stado_terminal_*` (PTY + snapshot) host imports. Expect/snapshot validating. |
| [`render-demo-go/`](render-demo-go/) | `stado_ui_print` / `stado_ui_render` host imports. UI rendering surface. |
| [`state-dir-info/`](state-dir-info/) | `stado_cfg_state_dir` capability. Returns the operator's stado state-dir path. Copy-modify template for plugins that compose paths under it. |

## When to retire a demo

When the surface it validates becomes load-bearing in a real
optional/ plugin (one a user would install for utility), the demo
loses unique value — keep it only if it's still the smallest
example. Otherwise move it to `_retired/` with a one-line note
pointing at the optional/ plugin that supersedes it.
