# Release bundling: don't trust GitHub API asset digests

The `v0.1.0` release workflow failed after `go run ./hack/fetch-binaries.go`
appeared to succeed, then the later GoReleaser build for `darwin_amd64`
could not find `internal/tools/rg`'s generated `bundled_*` symbols.

Root cause:

- `hack/fetch-binaries.go` relied on GitHub REST release-asset
  `digest` metadata from `releases/tags/<tag>`.
- ripgrep's release assets did not expose the needed digests there, so
  the script silently skipped supported targets and returned success.
- The build then failed much later because `stado_embed_binaries`
  removed the stub file but no generated per-target embed file existed.

Working pattern:

- For ripgrep, read checksums from the published `.sha256` sidecar next
  to each asset. Support both `sha256sum` output and the Windows
  `CertUtil` format used by some sidecars.
- For ast-grep, parse the public GitHub
  `/releases/expanded_assets/<tag>` fragment, which includes inline
  `sha256:<hex>` digests for each binary asset.
- For the supported release matrix, treat any fetch/digest mismatch as
  fatal. Do not `continue` on errors or the failure will be deferred
  into a confusing compile error.

Verification that matched the release path:

- `go test ./internal/releaseassets`
- `go run ./hack/fetch-binaries.go -only rg`
- `go run ./hack/fetch-binaries.go -only ast-grep`
- `GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -tags stado_embed_binaries ./cmd/stado`
- `go run github.com/goreleaser/goreleaser/v2@v2.15.4 build --snapshot --clean`
