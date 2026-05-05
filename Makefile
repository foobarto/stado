# stado — Makefile
#
# Default target builds `./stado`. The release pipeline uses goreleaser
# (see .goreleaser.yaml) for cross-platform + signed artefacts; this
# file is for the local dev loop.

GO       ?= go
GOFLAGS  ?=
# Redirect Go's build temp dir off /tmp — per-user quota is tight on this host.
export GOTMPDIR ?= $(CURDIR)/.tmp
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
LDFLAGS  := -X main.version=$(VERSION)

.DEFAULT_GOAL := build

.PHONY: build
build: ## Compile ./stado (default target)
	$(GO) build $(GOFLAGS) -ldflags='$(LDFLAGS)' -o $(BIN) $(PKG)

.PHONY: install
install: ## Install ./stado into $(GOPATH)/bin
	$(GO) install $(GOFLAGS) -ldflags='$(LDFLAGS)' $(PKG)

.PHONY: test
test: ## Run the full test suite
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
