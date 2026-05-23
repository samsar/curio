# Curio Makefile
# Conventions:
#   - `make` (no target) shows help
#   - All build output goes under ./bin
#   - Test categories are gated by build tags: integration, e2e

GO            ?= go
GOLANGCI_LINT ?= golangci-lint
GOOSE         ?= goose

BIN_DIR       := bin
CURIO_BIN     := $(BIN_DIR)/curio
DAEMON_BIN    := $(BIN_DIR)/curio-daemon

VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT        ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE          ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X github.com/samansartipi/curio/internal/version.Version=$(VERSION) \
	-X github.com/samansartipi/curio/internal/version.Commit=$(COMMIT) \
	-X github.com/samansartipi/curio/internal/version.Date=$(DATE)

# Cgo is required (sqlite, sqlite-vec). Force it on so builds fail loudly
# rather than silently producing a binary missing SQLite.
export CGO_ENABLED=1

.DEFAULT_GOAL := help

## help: show available targets
.PHONY: help
help:
	@awk 'BEGIN {FS = ": "} /^## [a-zA-Z0-9_-]+:/ {sub(/^## /, ""); printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

## build: build both binaries
.PHONY: build
build: $(CURIO_BIN) $(DAEMON_BIN)

$(CURIO_BIN): $(shell find cmd/curio internal -name '*.go' 2>/dev/null) go.mod go.sum
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $@ ./cmd/curio

$(DAEMON_BIN): $(shell find cmd/curio-daemon internal -name '*.go' 2>/dev/null) go.mod go.sum
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $@ ./cmd/curio-daemon

## test: run unit tests (fast; no external services required)
.PHONY: test
test:
	$(GO) test -race -count=1 ./...

## test-integration: run integration tests (requires local SQLite + Ollama + web2md)
.PHONY: test-integration
test-integration:
	$(GO) test -race -count=1 -tags=integration ./...

## test-e2e: run end-to-end tests (boots full daemon)
.PHONY: test-e2e
test-e2e: build
	$(GO) test -race -count=1 -tags=e2e ./test/e2e/...

## lint: run golangci-lint
.PHONY: lint
lint:
	$(GOLANGCI_LINT) run ./...

## fmt: format and tidy
.PHONY: fmt
fmt:
	$(GO) fmt ./...
	$(GO) mod tidy

## vet: go vet
.PHONY: vet
vet:
	$(GO) vet ./...

## clean: remove build output
.PHONY: clean
clean:
	rm -rf $(BIN_DIR)

## tools: install dev tools (golangci-lint, goose, oapi-codegen)
.PHONY: tools
tools:
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	$(GO) install github.com/pressly/goose/v3/cmd/goose@latest
	$(GO) install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest

## migrate-up: apply pending migrations to ~/.curio/curio.db
.PHONY: migrate-up
migrate-up:
	$(GOOSE) -dir migrations sqlite3 $${CURIO_HOME:-$$HOME/.curio}/curio.db up

## migrate-status: show migration status
.PHONY: migrate-status
migrate-status:
	$(GOOSE) -dir migrations sqlite3 $${CURIO_HOME:-$$HOME/.curio}/curio.db status
