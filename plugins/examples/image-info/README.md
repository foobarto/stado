# image-info — header-only image metadata

Reads just enough of an image file to report its format, dimensions,
color depth, and byte size — no pixel decoding. Useful when an LLM
is staring at a directory of screenshots and wants the cheap "what
is this" answer before deciding whether to actually view the image.

## Tool

```
image_info {path}
  → {path, format, width, height, color_depth?, animated?, size_bytes, note?}
```

Supported formats: PNG, JPEG (JFIF/Exif), GIF87a/89a, WebP
(VP8 / VP8L / VP8X), BMP. Anything else returns `format: "unknown"`
with the byte size only.

## Build + install

```sh
stado plugin gen-key image-info-demo.seed
./build.sh
stado plugin trust "$(cat author.pubkey)" image-info-demo
stado plugin install .
```

## Run

```sh
stado plugin run image-info-0.1.0 image_info \
  '{"path":"assets/screenshots/agent.png"}' \
  --workdir $PWD
```

## Capabilities

```toml
capabilities = ["fs:read:."]
```

If you only want to inspect images in a specific subtree, narrow to
`fs:read:./screenshots` or similar.

## Implementation note: 16 MiB buffer

The plugin allocates a 16 MiB buffer for every call, even though it
only parses the first ~64 KiB. That's because `stado_fs_read` (the
host import) doesn't support partial reads — it returns the whole
file or fails when the file is bigger than the supplied buffer.
The host's hard cap is `maxPluginRuntimeFSFileBytes = 16 << 20`, so
that's what we ask for.

A `stado_fs_read_partial(path, offset, length)` host import would
let this plugin (and any future binary-inspection plugin) stay
small. Filed as feedback in the stado repo.
