# choose-demo-go

Minimal example exercising the `ui:choice` capability + the
`stado_ui_choose` host primitive end-to-end. The tool prompts the
operator to pick from a list (single or multi). In the TUI a drawer
renders below the input with `↑`/`↓` to navigate, Space to toggle in
multi mode, Enter to confirm, Esc to cancel. On ACP it routes through
`session/update kind=choice` + `session/choice_response`.

Marked "manual test tool only" in its description so the model
won't try to invoke it on its own — install it only when you want
to manually exercise the choose UI.

## Build, sign, install

```sh
cd plugins/optional/choose-demo-go
stado plugin gen-key choose-demo-go.seed
./build.sh
stado plugin trust <pubkey-hex-from-gen-key>
stado plugin install .
```

## Use

In the TUI, run with the default option set (Alpha / Bravo / Charlie):

```
/tool run choose_demo
```

Or with an explicit option list and multi-select:

```
/tool run choose_demo {"prompt":"Pick targets","multi":true,"options":[{"id":"a","label":"Alpha"},{"id":"b","label":"Bravo"}]}
```

Result is `selected: <id1>,<id2>,...` on confirm, or `cancelled` on
Esc / dismissal.

## Why an example, not bundled

stado deliberately doesn't ship demos as bundled tools — the model
sees the tool surface and shouldn't be tempted to call test tools.
This example layout is the canonical home for "implementation
references": clone, study, and either install as-is for manual UI
smoke testing or fork as a starting point for your own
choose-flow plugin.
