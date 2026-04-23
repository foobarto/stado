GoReleaser's `dist/artifacts.json` may contain metadata rows where
`type` is `null`. Release workflow `jq` that assumes every `.type` is a
string will fail with:

`jq: error ... null (null) cannot be matched, as it is not a string`

For the SLSA subjects list in `.github/workflows/release.yml`, guard the
query with defaults:

- `(.type // "") | test("Archive|Binary")`
- `(.extra.Checksum // "") | startswith("sha256:")`

That keeps the workflow focused on archive/binary digests and ignores
non-artifact metadata rows.
