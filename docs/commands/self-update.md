# `stado self-update`

Download, verify, and atomically replace the current stado binary.

## What it does

`self-update` queries GitHub Releases, chooses the archive for the
current OS/arch, verifies the signed checksum manifest, verifies the
archive sha256 against that manifest, extracts `stado`, and swaps it
into the current executable path. The previous binary is kept as
`<bin>.prev`.

Release builds must embed the minisign trust root, and the release must
publish `checksums.txt.minisig`.

## Usage

```sh
stado self-update --dry-run
stado self-update
stado self-update --force
stado self-update --repo foobarto/stado
```

## Flags

| Flag | Meaning |
|------|---------|
| `--dry-run` | Resolve release and asset, but do not download/install |
| `--force` | Reinstall even when current version matches latest |
| `--repo owner/name` | Read releases from another GitHub repo |

## Gotchas

- Airgap builds compile this command as disabled and print an offline
  install hint.
- Dev builds without embedded minisign roots cannot complete strict
  self-update verification.
- The command updates the binary path that is currently executing; make
  sure that is the install you intend to replace.

## See also

- [verify.md](verify.md) — inspect embedded trust roots.
- [../../SECURITY.md](../../SECURITY.md) — manual and airgapped
  verification.
- [../eps/0012-release-integrity-and-distribution.md](../eps/0012-release-integrity-and-distribution.md)
