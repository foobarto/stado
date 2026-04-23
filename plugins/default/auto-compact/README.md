# auto-compact

This is the source for stado's shipped bundled auto-compaction plugin.

What it does:

1. Observes session events via `session:observe`
2. Reads token count/history via `session:read`
3. Summarises through `llm:invoke`
4. Recovers by `session:fork` into a compacted child session

The parent session is never rewritten in place.

## Default behavior

Stado bundles this plugin into the binary and loads it automatically as
a background plugin in the TUI and headless server.

- On `turn_complete`, it checks the current token count and may fork a
  compacted child session when the session is already over threshold.
- On the TUI-specific `context_overflow` event, it runs immediate
  recovery so the blocked prompt can be replayed in the compacted child
  session.

That default load is built into stado; you do not need to add anything
to `[plugins].background` for this plugin.

## Capabilities

The bundled manifest declares:

- `session:observe`
- `session:read`
- `session:fork`
- `llm:invoke:30000`

## Manual build/install

The same source can still be built and installed as a normal signed
plugin when you want to experiment with it directly:

```sh
stado plugin gen-key auto-compact-demo.seed
./build.sh
stado plugin trust <pubkey-hex> "stado example"
stado plugin install .
stado plugin run --session <session-id> auto-compact-0.1.0 compact '{}'
```

That manual path is useful for authoring or explicit persisted-session
CLI compaction. The bundled default background plugin remains separate.

## Notes

- The bundled runtime ID is `auto-compact`.
- The installable demo from this directory still uses the manifest
  version in `plugin.manifest.template.json`, so its on-disk plugin ID
  remains `<name>-<version>` after `plugin install`.
- The plugin reacts to event payloads queued by stado; today the
  meaningful kinds are `turn_complete` and `context_overflow`.
