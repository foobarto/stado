# stado — Makefile
#
# Default target builds `./stado`. The release pipeline uses goreleaser
# (see .goreleaser.yaml) for cross-platform + signed artefacts; this
# file is for the local dev loop.

GO       ?= go
GOFLAGS  ?=
# Go roots `t.TempDir()` at GOTMPDIR. Several suites walk parent dirs
# looking for .git / .env / AGENTS.md / CLAUDE.md, and the fs/grep walk
# guards reject symlinked path components — so GOTMPDIR must sit where no
# such marker appears in any ancestor (NOT inside this repo, whose root
# carries .git/.env/CLAUDE.md) and on a real, non-symlinked path (NOT
# under $HOME on Fedora Atomic, where /home -> /var/home). It must also
# stay off /tmp (per-user quota is tight on this host). /var/tmp fits on
# every count. Override with `make GOTMPDIR=/path ...`.
GOTMPDIR ?= /var/tmp/stado-gotmp-$(shell id -u)
# An inherited GOTMPDIR inside the repo would make those parent-walk
# suites escape into the repo's own files; force it back out.
ifneq (,$(findstring $(CURDIR),$(GOTMPDIR)))
GOTMPDIR := /var/tmp/stado-gotmp-$(shell id -u)
endif
export GOTMPDIR
_ := $(shell mkdir -p $(GOTMPDIR))
PKG      ?= ./cmd/stado
BIN      ?= stado
STATICCHECK ?= staticcheck

# `git describe`-derived version for ldflags injection. Falls through
# to "0.0.0-dev" (matching the package-level default) when we're not
# in a git checkout. `--tags --always --dirty` produces:
#   v0.31.0                            (on a tagged commit, clean tree)
#   v0.31.0-3-gabc1234                 (3 commits past v0.31.0, clean)
#   v0.31.0-3-gabc1234-dirty           (... with uncommitted changes)
#   abc1234-dirty                      (no tag reachable, dirty)
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
# Set BOTH version vars: cmd/stado's `main.version` (used by `stado --version`)
# and internal/version.Version (used by the TUI status bar / landing / sidebar).
# Previously only main.version was set here, so `make`-built binaries showed the
# right `--version` but 0.0.0-dev in the TUI. goreleaser already sets both.
LDFLAGS  := -X main.version=$(VERSION) -X github.com/foobarto/stado/internal/version.Version=$(VERSION)

.DEFAULT_GOAL := build

# Bundled wasm are built from source (EP-0042 Part B), not committed. The
# embed at internal/plugins/bundled/embed.go needs ALL of them present at
# compile time, so build/install/test depend on `wasm`. The target rebuilds
# when: (a) any output is missing (partial/interrupted build — a single-file
# sentinel would miss this and let `go build` succeed only to panic at
# startup in MustWasm), or (b) any source changed — ANYTHING under the bundled
# source trees (build.sh, plugin .go, nested go.mod/go.sum, manifest
# templates), the shared wasm SDK, or the root module graph. Conservative;
# the right default for embedded artefacts.
WASM_DIR        := internal/plugins/bundled/wasm
WASM_STAMP      := $(WASM_DIR)/.wasm.stamp
# Count of wasm build.sh produces — keep in sync with its TOOLS/EP38_TOOLS/
# EP38_RENAMED/EXTRA_WASM lists + auto-compact (4+6+1+1+1).
WASM_COUNT      := 13
WASM_SRC        := $(shell find plugins/bundled internal/plugins/bundled/sdk -type f -not -path '*/.wasm-build*' 2>/dev/null) go.mod go.sum

# Pass GO through so `make GO=/path/to/go` and the wasm build use the same
# toolchain (build.sh reads $GO, defaulting to `go` on PATH). Stamp is touched
# only after a successful build (build.sh is `set -e`, so it builds all or fails).
.PHONY: wasm
wasm: ## Build the bundled wasm if any output is missing or any source changed
	@if [ ! -f $(WASM_STAMP) ] \
	   || [ "$$(ls $(WASM_DIR)/*.wasm 2>/dev/null | wc -l)" -lt $(WASM_COUNT) ] \
	   || [ -n "$$(find $(WASM_SRC) -newer $(WASM_STAMP) 2>/dev/null)" ]; then \
		GO=$(GO) bash plugins/bundled/build.sh && touch $(WASM_STAMP); \
	fi

.PHONY: changelog
changelog: ## Regenerate internal/changelog/latest.md from CHANGELOG.md (drift-guarded by a test)
	@awk '/^## v/{c++} c==1' CHANGELOG.md > internal/changelog/latest.md.tmp \
	  && mv internal/changelog/latest.md.tmp internal/changelog/latest.md

.PHONY: build
build: wasm changelog ## Compile ./stado (default target)
	$(GO) build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o $(BIN) $(PKG)

.PHONY: install
install: wasm changelog ## Install ./stado into $(GOPATH)/bin
	$(GO) install $(GOFLAGS) -ldflags='$(LDFLAGS)' $(PKG)

.PHONY: test
test: wasm ## Run the full test suite
	$(GO) test -count=1 -timeout 180s ./...

.PHONY: lint
lint: ## Run staticcheck with the same checks as CI's golangci config
	$(STATICCHECK) -checks "all,-S1011,-S1025,-S1039,-ST1000,-ST1020,-ST1022,-QF1001,-QF1012" ./...

.PHONY: check
check: lint test ## Run lint + test (the local pre-push gate)

.PHONY: tidy
tidy: ## Run go mod tidy
	$(GO) mod tidy

.PHONY: fetch-binaries
fetch-binaries: ## Run hack/fetch-binaries.go (mirrors the goreleaser before-hook)
	$(GO) run hack/fetch-binaries.go

.PHONY: fedora-atomic-test
fedora-atomic-test: build ## Regression-test the Atomic Fedora /home → /var/home boot path (needs bwrap)
	./hack/test-on-fedora-atomic.sh --no-build

.PHONY: clean
clean: ## Remove the local binary
	rm -f $(BIN)

.PHONY: help
help: ## Show this help
	@awk 'BEGIN {FS = ":.*?## "} /^[a-zA-Z_-]+:.*?## / {printf "  %-18s %s\n", $$1, $$2}' $(MAKEFILE_LIST)
