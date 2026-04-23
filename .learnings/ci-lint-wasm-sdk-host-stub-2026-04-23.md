# CI lint: wasm-only SDK on host linters

The recurring GitHub Actions failures on `main` were not test or build
failures. `test` and the full cross-platform `build` matrix were green;
only `lint` was red.

Root cause:

- `internal/bundledplugins/sdk` was written with wasm32 pointer
  assumptions but had no build tags, so host-side linting analyzed it
  as ordinary linux/amd64 code.
- That exposed `govet` `unsafeptr` issues, and host-side tests would
  also crash because `int32` pointer handles are meaningless outside
  wasm linear memory.

Fix pattern:

- Keep the real pointer-based implementation behind `//go:build wasip1`
- Add a host-only stub implementation for `!wasip1` so lint/tests can
  load the package safely
- Reproduce CI lint locally with the same version GitHub Actions used:
  `golangci-lint v2.11.4`

Verification that matched CI:

- `golangci-lint run --timeout=5m`
- `/usr/local/go/bin/go test ./...`
- `GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared ./internal/bundledplugins/modules/approval_demo`
