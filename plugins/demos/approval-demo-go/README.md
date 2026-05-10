# approval-demo-go

Minimal example exercising the `ui:approval` capability + the
`stado_ui_approve` host primitive end-to-end. The tool prompts the
operator with a yes/no modal in the TUI; on ACP it routes through
`session/update kind=approval` + `session/approval_response`.

Marked "manual test tool only" in its description so the model
won't try to invoke it on its own — install it only when you want
to manually exercise the approval UI.

## Build, sign, install

```sh
cd plugins/demos/approval-demo-go
stado plugin gen-key approval-demo-go.seed
./build.sh
stado plugin trust <pubkey-hex-from-gen-key>
stado plugin install .
```

## Use

In the TUI, run:

```
/tool run approval_demo {"title":"Test","body":"Continue?"}
```

The drawer pops up below the input. Press Y/N or use ←/→ + Enter to
choose. The tool result is `approved` / `denied` / (on errors)
`approval UI unavailable`.

## Why an example, not bundled

stado deliberately doesn't ship demos as bundled tools — the model
sees the tool surface and shouldn't be tempted to call test tools.
This example layout is the canonical home for "implementation
references": clone, study, and either install as-is for manual UI
smoke testing or fork as a starting point for your own
approval-flow plugin.
