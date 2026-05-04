# stado — Makefile
#
# Default target builds `./stado`. The release pipeline uses goreleaser
# (see .goreleaser.yaml) for cross-platform + signed artefacts; this
# file is for the local dev loop.

GO       ?= go
GOFLAGS  ?= -buildvcs=false
PKG      ?= ./cmd/stado
BIN      ?= stado
STATICCHECK ?= staticcheck

.DEFAULT_GOAL := build

.PHONY: build
build: ## Compile ./stado (default target)
	$(GO) build $(GOFLAGS) -o $(BIN) $(PKG)

.PHONY: install
install: ## Install ./stado into $(GOPATH)/bin
	$(GO) install $(GOFLAGS) $(PKG)

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
