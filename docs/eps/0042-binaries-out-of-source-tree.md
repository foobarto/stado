---
ep: 42
title: Binaries out of the source tree — bundled wasm built at release, plugins distributed via anchor repo
author: Bartosz Ptaszynski <bartosz@foobarto.me>
status: Implemented
type: Standards
created: 2026-05-22
implemented-in: v0.53.0
see-also: ["[EP-0006](./0006-signed-wasm-plugin-runtime.md)", "[EP-0012](./0012-release-integrity-and-distribution.md)", "[EP-0038](./0038-abi-v2-bundled-wasm-and-runtime.md)", "[EP-0039](./0039-plugin-distribution-and-trust.md)"]
history:
  - date: 2026-05-22
    status: Draft
    note: >
      Drafted after a code-scanning review: 25 committed .wasm (~76 MB) trip
      35 OpenSSF Scorecard BinaryArtifacts alerts and freeze stale deps that
      osv-scanner flags as 20 live vulnerabilities (Scorecard VulnerabilitiesID).
      Splits the fix in two: optional/demo plugins move to an anchor repo and
      install via the already-implemented EP-39 path; the 13 go:embed'd bundled
      wasm are built at build/release time and gitignored.
  - date: 2026-06-13
    status: Implemented
    version: v0.53.0
    note: >
      Both parts landed (shipped v0.53.0). Part A — optional/demo plugins moved
      to the anchor repo foobarto/stado-plugins, installed via EP-39's
      `stado plugin install` path (PR #25). Part B — the bundled wasm is built
      from source at build/release time (`make wasm` / build.sh, run by CI and
      goreleaser) and gitignored, no `.wasm` committed; `go install` dropped in
      favour of clone+make.
---

# EP-0042: Binaries out of the source tree

## Problem

The main repo commits **25 `.wasm` artefacts totalling ~76 MB**:

- **13 bundled** under `internal/plugins/bundled/wasm/`, `//go:embed`'d into
  the `stado` binary (`internal/plugins/bundled/embed.go`). Sources live at
  `plugins/bundled/<plugin>/`; `plugins/bundled/build.sh` compiles them back
  into `internal/plugins/bundled/wasm/`. The goreleaser before-hook does **not**
  rebuild them — the committed bytes are what ship.
- **12 optional / demo** under `plugins/optional/*` and `plugins/demos/*`,
  installed by users (not embedded).

This causes three concrete problems:

1. **Scorecard `BinaryArtifacts` (35 alerts, high).** Every committed binary
   is flagged. Binaries in source are unreviewable and unreproducible from the
   diff.
2. **Frozen stale dependencies → `VulnerabilitiesID` (20 vulns).** A committed
   `.wasm` embeds whatever dependency versions it was built with. osv-scanner
   reads those frozen module versions and reports live advisories
   (GO-2026-5005/5006/5013-17, …) even when the *source* would build clean.
   This is the same class as GO-2026-5026 (EP-trigger for issue #23): the root
   module was patched, but the committed plugin binaries still carry old
   `x/net`/`crypto`.
3. **In-tree rebuild is broken or risky.** `browser-minimal` pins its
   `wasm_sha256` in a signed manifest and re-signs with `browser-demo.seed` —
   a private key that is **not present in the repo** (gitignored), so the wasm
   cannot be rebuilt + re-signed in-tree at all. Bundled-wasm rebuilds have
   historically been non-deterministic and have corrupted `//go:wasmexport`
   ABI arity under a mismatched Go toolchain.

## Goals

- No committed `.wasm` (or other compiled binaries) in the main repo tree.
- Bundled wasm built **from source** at build/release time, embedded into the
  `stado` binary, never committed.
- Optional/demo plugins distributed from a separate **anchor repo** and
  installed via the EP-39 remote-install path (already implemented in v0.33.0).
- Plugin/bundled vulnerability surface tracks **live source dependencies**, not
  frozen blobs — so a `go get` + rebuild is the whole fix, and CI catches drift.
- Reproducible wasm builds: a pinned toolchain so release artefacts are
  deterministic.

## Non-goals

- Changing EP-39's install/trust model. This EP **reuses** it for optional
  plugins; it does not redesign identity, anchors, TOFU, or the lock file.
- Changing EP-12's release-integrity contract for the `stado` binary itself
  (signed `checksums.txt`, cosign + minisign, self-update). The binary still
  ships the embedded bundled wasm; only their *provenance* changes from
  "committed" to "built at release".
- A central plugin registry (EP-6 / EP-39 non-goal preserved).
- Rewriting git history to purge the existing committed `.wasm`. HEAD goes
  clean; history retention is a separate, optional operator decision.

## Design

The split has two independent parts with different risk profiles.

### Part A — Optional + demo plugins → anchor repo (low risk)

**Every** non-bundled / non-embedded plugin moves out of the main repo —
source *and* compiled `.wasm` — into one new home. EP-39 already provides
everything needed to install them remotely:

- New repo **`github.com/foobarto/stado-plugins`** — also the EP-39 §C
  *anchor repo*, carrying `.stado/author.pub` (one ed25519 signing key for the
  owner, as EP-39 §C requires). Per EP-39 §A monorepo subdir tags, each plugin
  versions independently (`<plugin>/v0.1.0`).
- The full current set (`plugins/optional/*`, `plugins/demos/*`) relocates
  there — their sources, manifests, build scripts, and the `.wasm` artefacts
  themselves. Binaries legitimately live in the distribution repo: EP-39 §E
  resolves a plugin from either **tier 1** (CI builds the wasm deterministically,
  the operator signs the manifest **offline**, signed manifest + wasm published
  as GitHub Release assets — preferred) or **tier 2** (`<plugin>/dist/` with the
  committed `.sig`, signed offline). Tier 1 keeps even `stado-plugins` free of
  committed binaries; tier 2 is the simpler fallback. Either way the wasm is no
  longer in the main `stado` repo.
- **Signing is offline (D5).** The anchor private key is never placed in
  `stado-plugins` Actions secrets. CI only *builds*; the operator signs locally
  with the key that never leaves their machine, mirroring EP-12's minisign trust
  root. This keeps the one-key-per-owner anchor — the highest-value key in the
  plugin trust model — out of GitHub's trust boundary.
- The plugins are **removed from the main tree**. Docs/READMEs switch to
  `stado plugin install github.com/foobarto/stado-plugins/<plugin>@<version>`.
- No UX regression: these were already opt-in (`optional`/`demos`), never
  enabled by default. Operators who used them gain versioning, lock-file
  reproducibility, and one-TOFU-per-owner trust.

This supersedes **issue #23**: `browser`/`browser-minimal` get rebuilt from
current source in the `stado-plugins` CI, so the stale `x/net` and the missing
`browser-demo.seed` both move into that repo's controlled release pipeline.

### Part B — Bundled wasm built at build/release time (higher risk)

The 13 embedded wasm stop being committed and become build artefacts.

The constraint that makes this non-trivial: `//go:embed wasm/*.wasm` **requires
the files to exist at compile time**. Today every `go build`, `make`, `go test`,
and CI run works only because the bytes are committed. Removing them means a
wasm-build step must precede any compile that needs the embed.

**Consequence — `go install` is dropped (D6).** The embed is unconditional and
has no PATH fallback (unlike the native rg/ast-grep, which are build-tag-gated).
`go install …/cmd/stado@latest` builds from the module cache with no hook to run
`build.sh`, so it cannot produce the wasm and the embed fails. The wasm are
first-party core tools, so signing them is theatre (they ride the binary's
cosign/minisign signature) and EP-39's trust model does not apply; the only
reason to commit them was `go install`. We accept dropping `go install` — the
"from source" path becomes `git clone && make`; release binaries (install.sh /
brew, via the goreleaser before-hook) are unaffected. Fetching the wasm from
`stado-plugins` was considered and rejected: it would obscure provenance and
bolt needless trust machinery onto first-party code.

Mechanism:

1. **`make wasm`** (new target) runs `plugins/bundled/build.sh`, writing the 13
   `.wasm` into `internal/plugins/bundled/wasm/` and manifest templates into
   `internal/plugins/bundled/manifests/` (matching the current script). `make build`,
   `make test`, and `make install` depend on it.
2. **`//go:generate`** directive next to `embed.go` invokes the same build, so
   `go generate ./...` produces them for contributors who don't use `make`.
3. **goreleaser before-hook** gains the wasm build (alongside `go mod tidy` +
   `go run ./hack/fetch-binaries.go`) so release artefacts embed freshly-built wasm.
4. **`.gitignore`** covers `internal/plugins/bundled/wasm/*.wasm`. The 13
   committed files are deleted from the tree.
5. **A compile-time guard**: a tiny `//go:build` sentinel or a generated
   `wasm/.gitkeep` + a clear error path so a bare `go build ./...` with no wasm
   present fails with *"run `make wasm` (or `go generate ./...`) first"* rather
   than a cryptic embed error.
6. **Toolchain pin**: the wasm build pins `GOTOOLCHAIN` (per the
   project's toolchain-pin discipline) so the embedded bytes are deterministic
   across machines and CI — directly mitigating the historical ABI-arity
   corruption.
7. **CI rebuild-verify** job: build the wasm twice (or build + compare against a
   recorded sha) to assert determinism, and run `govulncheck` inside
   `plugins/bundled/` so embedded-dependency drift is caught at PR time, not by
   an external scanner months later.

Bundled wasm need **no separate signing**: they are embedded into the `stado`
binary and covered by EP-12's binary-level cosign/minisign signature. Their
trust derives from the signed `stado` release, not a per-plugin manifest sig.

### What clears

- 13 bundled + 12 optional/demo committed `.wasm` removed → all **35
  BinaryArtifacts** alerts clear at HEAD.
- Embedded + plugin deps rebuilt from current source → **VulnerabilitiesID**
  (the 20 frozen-blob advisories) clears as source is patched; CI `govulncheck`
  keeps it clear.
- Repo shrinks by ~76 MB of churning binaries; diffs stop carrying blob noise.

## Risk and self-critique

- **Dev-loop friction is the real cost.** `go build ./...` alone no longer
  works on a fresh checkout — contributors must `make wasm` / `go generate`
  first. This is the strongest argument *against* Part B; it's accepted because
  the alternative (committing 76 MB of stale-dep binaries forever) is worse, and
  the friction is one documented command, gated by a clear error message.
- **CI gets slower** (13 wasm compiles per run). Mitigate by caching the wasm
  build keyed on `plugins/bundled/**` source hash.
- **Reproducibility/ABI risk.** If the pinned toolchain isn't honoured
  everywhere, embedded bytes differ or the ABI corrupts. Mitigated by the pin +
  the rebuild-verify CI gate; this is also why bundled wasm don't need
  byte-stable signing (binary-level signature covers them).
- **External-repo custody.** `stado-plugins` and its anchor key are
  operator-owned. Standing up the repo, generating/escrowing the anchor key, and
  wiring its release CI are operator steps this EP cannot perform unilaterally.
- **Assumption to verify before Part B lands:** that `plugins/bundled/build.sh`
  builds cleanly under the pinned toolchain on CI's OS/arch and the resulting
  embed loads at runtime (smoke-test `stado tool ls` showing all bundled tools).

## Migration / rollout

Phased; each phase ships independently and leaves the tree working.

- **Phase 1 (operator-owned setup).** Create `github.com/foobarto/stado-plugins`,
  add `.stado/author.pub`, wire a release workflow that builds + signs each
  plugin from source and publishes per-subdir releases.
- **Phase 2 (Part A).** Relocate the entire `plugins/optional/*` +
  `plugins/demos/*` set — sources, manifests, build scripts, and `.wasm`
  artefacts — into `stado-plugins`; publish first releases (tier 1) or commit
  `dist/` (tier 2); update main-repo docs/READMEs to the `stado plugin install`
  form; delete them from the main tree.
- **Phase 3 (Part B).** Add `make wasm` + `go:generate` + goreleaser hook +
  `.gitignore` + compile guard + toolchain pin + CI rebuild-verify; delete the
  13 committed bundled `.wasm`. Verify a clean `make build` and `stado tool ls`.
- **Phase 4 (verify).** Confirm Scorecard BinaryArtifacts + VulnerabilitiesID
  clear on the next scan; dismiss any residual as fixed.

## Decision log

### D1. Build bundled wasm at build/release time; do not commit them

- **Decided:** the 13 embedded wasm are build artefacts, gitignored, produced
  by `make wasm` / `go generate` / goreleaser hook from source.
- **Alternatives:** keep committed and dismiss the alerts as by-design; hybrid
  commit + CI rebuild-verify.
- **Why:** committing binaries freezes stale deps (live vuln surface) and is
  unreproducible from the diff. Building from source makes a dep bump the whole
  fix and lets CI catch drift. Cost (dev-loop step) is one documented command.

### D2. Distribute optional/demo plugins via the EP-39 anchor repo

- **Decided:** move them to `github.com/foobarto/stado-plugins`; install via the
  implemented EP-39 remote path; remove from the main tree.
- **Alternatives:** per-plugin repos; keep in-tree; a registry.
- **Why:** EP-39 already designed and shipped this exact mechanism (anchor,
  subdir tags, three-tier resolution, lock file). Reuse it; don't reinvent.

### D3. Pin the wasm build toolchain for determinism

- **Decided:** the wasm build pins `GOTOOLCHAIN`; a CI gate verifies
  determinism + runs govulncheck inside the bundled module set.
- **Alternatives:** build with whatever local Go is present.
- **Why:** prior bundled-wasm rebuilds were non-deterministic and corrupted
  `//go:wasmexport` ABI arity under a mismatched toolchain. A pin makes
  embedded bytes reproducible and lets the binary-level signature cover them.

### D4. Do not rewrite history to purge existing committed wasm

- **Decided:** HEAD goes clean; the ~76 MB of historical blobs stay in git
  history unless the operator separately decides to rewrite.
- **Alternatives:** `git filter-repo` to excise them.
- **Why:** history rewrite is disruptive (invalidates clones, signatures, the
  EP-12 release contract's commit references) and the Scorecard checks evaluate
  HEAD. Purging history is an optional, separate operator decision.

### D5. Sign the anchor offline; never put the key in CI secrets

- **Decided:** `stado-plugins` CI builds wasm but does not sign. The EP-39
  anchor private key signs manifests offline on the operator's machine; CI never
  holds it.
- **Alternatives:** store the anchor key as an Environment-gated Actions secret
  and sign in CI; sigstore keyless/OIDC (needs an EP-39 verification extension).
- **Why:** the anchor is one-key-per-owner — its compromise lets an attacker
  sign any plugin as the owner for every operator trusting `<owner>/*`. Putting
  a long-lived copy in Actions secrets widens exposure to every workflow step,
  unpinned action, and workflow-edit path. EP-12 already keeps the analogous
  minisign trust root offline; the plugin anchor gets the same posture. Keyless
  OIDC is stronger still but is deferred future work (EP-39 §C item 2).

### D6. Bundled wasm built from source; `go install` dropped

- **Decided:** the 13 bundled wasm are built from source at build time
  (`make wasm` / `//go:generate` / goreleaser before-hook / CI step),
  gitignored, never committed, never signed, never fetched. `go install
  …/cmd/stado@latest` is dropped; the "from source" path is `git clone && make`.
- **Alternatives:** keep them committed + dismiss the BinaryArtifacts alerts as
  by-design (preserves `go install`); fetch pre-built wasm from `stado-plugins`.
- **Why:** committing embed assets obscures provenance (the binary embeds an
  opaque blob, not auditably the source's output) and is the *only* reason
  `go install` worked. Signing is theatre (the wasm ride the signed binary;
  no author→installer boundary, so EP-39 does not apply). Fetching from
  `stado-plugins` was rejected — same obscuring, plus needless trust machinery
  on first-party code. Building from source makes the embed provably == source,
  removes the binaries from the tree (clears 13 BinaryArtifacts), and a dep bump
  is the whole fix. Cost: `go install` no longer works (accepted) and a
  wasm-build step precedes every compile (Makefile/CI/goreleaser carry it).
- **Settled 2026-05-22 (operator, twice). Do not re-litigate** — see the
  surfaced decision index. Contrast: the native rg/ast-grep are third-party,
  cannot be built from stado source, so they are fetched + sha256-verified.

## Related

- [EP-6: Signed WASM Plugin Runtime](./0006-signed-wasm-plugin-runtime.md)
- [EP-12: Release Integrity and Distribution](./0012-release-integrity-and-distribution.md)
- [EP-38: ABI v2, Bundled WASM and Runtime](./0038-abi-v2-bundled-wasm-and-runtime.md)
- [EP-39: Plugin Distribution and Trust](./0039-plugin-distribution-and-trust.md)
- Issue #23 (plugin-module x/net) — superseded by Part A.
