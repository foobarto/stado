# auto-compact

This is the source for stado's shipped bundled auto-compaction plugin.

What it does:

1. Observes session events via `session:observe`
2. Reads token count/history via `session:read`
3. Shapes a summarisation request in WASM and sends it through the generic
   `provider:invoke` primitive
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
- `provider:invoke:30000`

The signed capability is the cumulative token ceiling for this plugin
instance. Provider construction, credentials, authenticated plugin identity,
and accounting stay in the native host. Prompt shape, summary policy, and the
model-facing result stay in this plugin. The guest checks the returned buffer
length before slicing and rejects provider facts whose total exceeds the signed
30,000-token ceiling.

## Manual build/install

The same source can still be built and installed as a normal signed
plugin when you want to experiment with it directly:

```sh
stado plugin gen-key auto-compact-demo.seed
./build.sh
stado plugin trust <pubkey-hex> "stado example"
stado plugin install .
stado tool run --session <session-id> compact '{}'
```

That manual path is useful for authoring or explicit persisted-session
CLI compaction. The bundled default background plugin remains separate.

## Notes

- The bundled runtime ID is `auto-compact`.
- The installable demo from this directory still uses the manifest
  version in `plugin.manifest.template.json`. `plugin install` assigns a
  source-derived store key; the manifest name remains display metadata.
- The plugin reacts to event payloads queued by stado; today the
  meaningful kinds are `turn_complete` and `context_overflow`.
