# `stado verify`

Print build provenance and embedded release trust roots.

## What it does

`stado verify` reports what the running binary knows about itself:
version, VCS commit, build time, dirty-bit, Go toolchain, target
platform, module path, and optionally the embedded minisign release
verification key.

It does not verify a downloaded asset by path. Release assets are
verified against the signed `checksums.txt` manifest; see
[SECURITY.md](../../SECURITY.md).

## Usage

```sh
stado verify
stado verify --json
stado verify --show-builtin-keys
```

## Flags

| Flag | Meaning |
|------|---------|
| `--json` | Emit the build info object as JSON |
| `--show-builtin-keys` | Include embedded minisign pubkey/key id |

## Gotchas

- Dev/source builds usually report `0.0.0-dev` and may not have an
  embedded minisign pubkey.
- `vcs modified: true` means the binary was built from a dirty
  worktree.

## See also

- [self-update.md](self-update.md) — update verification path.
- [../../SECURITY.md](../../SECURITY.md) — manual release verification.
- [../eps/0012-release-integrity-and-distribution.md](../eps/0012-release-integrity-and-distribution.md)
