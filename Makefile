# Curio Makefile
# Conventions:
#   - `make` (no target) shows help
#   - All build output goes under ./bin
#   - Test categories are gated by build tags: integration, e2e
#   - Tool versions are pinned here; CI reads them from this file

GO            ?= go
GOLANGCI_LINT ?= golangci-lint
GOOSE         ?= goose

# The go directive in go.mod is the exact toolchain CI and releases build
# with. Exporting it makes every go command below use that toolchain (the go
# command downloads it once when the local default differs), so tidy output,
# vet's stdversion check and the standard library golangci-lint type-checks
# match CI on any machine.
GO_VERSION := $(shell awk '$$1 == "go" { print $$2 }' go.mod)
export GOTOOLCHAIN := go$(GO_VERSION)

# golangci-lint must be built with a Go minor at least the go directive's,
# or it cannot type-check the standard library it is handed.
GOLANGCI_LINT_VERSION := v2.12.2
GOVULNCHECK_VERSION   := v1.8.0
ACTIONLINT_VERSION    := v1.7.12
# goose's CLI tracks the library version the migrations run under.
GOOSE_VERSION         := $(shell awk '$$1 == "github.com/pressly/goose/v3" { print $$2 }' go.mod)

BIN_DIR       := bin

VERSION       ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT        ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE          ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X github.com/samsar/curio/internal/version.Version=$(VERSION) \
	-X github.com/samsar/curio/internal/version.Commit=$(COMMIT) \
	-X github.com/samsar/curio/internal/version.Date=$(DATE)

# Cgo is required (sqlite, sqlite-vec). Force it on so builds fail loudly
# rather than silently producing a binary missing SQLite.
export CGO_ENABLED=1

# sqlite-vec's Go bindings compile sqlite-vec.c with -DSQLITE_CORE alone,
# which leaves its vector distances on a scalar loop. On arm64,
# SQLITE_VEC_ENABLE_NEON compiles in its NEON kernels, about 1.7x faster
# KNN at 1024 dimensions; NEON is part of every ARMv8-A core, so nothing
# checks for it at run time. Setting CGO_CFLAGS replaces Go's default of
# -O2 -g, which would build SQLite itself unoptimized, so those stay, and
# a CGO_CFLAGS already given is extended rather than replaced. The e2e
# test's go build inherits the export. The target's GOARCH decides, not the
# host's: <arm_neon.h> doesn't compile for amd64. See docs/decisions.md "sqlite-vec:
# NEON distance kernels on arm64".
ifeq ($(shell $(GO) env GOARCH),arm64)
override CGO_CFLAGS := $(if $(strip $(CGO_CFLAGS)),$(CGO_CFLAGS),-O2 -g) -DSQLITE_VEC_ENABLE_NEON
export CGO_CFLAGS
endif

# Build tags required by mattn/go-sqlite3:
#   sqlite_fts5     — enables FTS5 for BM25 search
#   sqlite_json     — JSON1 functions used in CHECK constraints (json_valid)
# sqlite-vec is loaded as a runtime extension via sqlite-vec-go-bindings; no
# build tag needed for that.
GOTAGS := sqlite_fts5,sqlite_json

.DEFAULT_GOAL := help

## help: show available targets
.PHONY: help
help:
	@awk 'BEGIN {FS = ": "} /^## [a-zA-Z0-9_-]+:/ {sub(/^## /, ""); printf "  \033[36m%-22s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)

## build: build curio, curio-daemon and curio-mcp into ./bin
# Always runs go build: its cache decides what is stale, including the
# migrations embedded from outside internal/. The three commands are named:
# cmd/clusterreport is a developer's tool (make cluster-report), never built
# into ./bin or shipped.
.PHONY: build
build:
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -tags=$(GOTAGS) -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/ ./cmd/curio ./cmd/curio-daemon ./cmd/curio-mcp

## test: run unit tests under -race (no network, no Ollama)
# The grouping's property tests skip under -race (each runs the whole
# pipeline many times, single-threaded, at ten times the CPU), so the
# insight packages run a second time without it.
.PHONY: test
test:
	$(GO) test -race -count=1 -tags=$(GOTAGS) ./...
	$(GO) test -count=1 -tags=$(GOTAGS) ./internal/insight/...

## test-integration: run integration tests (needs network; fetches live sites)
.PHONY: test-integration
test-integration:
	$(GO) test -race -count=1 -tags=$(GOTAGS),integration ./...

## test-e2e: run the end-to-end test (builds and drives a real curio-daemon), and the daemon's tests in its e2e build
.PHONY: test-e2e
test-e2e:
	$(GO) test -race -count=1 -tags=$(GOTAGS),e2e ./test/e2e/... ./cmd/curio-daemon/...

## cluster-report: measure the interests grouping on a copy of a home's database (DB=<copy> [JSON=<file>] [DRAWS=n] [SEED=n])
# A developer's tool: go run, so nothing lands in ./bin. It migrates the
# database it reads and refuses a home's own (a directory holding
# daemon.pid); take a copy with sqlite3 -readonly ~/.curio/curio.db
# ".backup <copy>". See cmd/clusterreport.
.PHONY: cluster-report
cluster-report:
	@test -n "$(DB)" || { echo 'usage: make cluster-report DB=<copy of curio.db> [JSON=<file>] [DRAWS=n] [SEED=n]' >&2; exit 2; }
	$(GO) run -tags=$(GOTAGS) ./cmd/clusterreport -db "$(DB)" $(if $(JSON),-json "$(JSON)") $(if $(DRAWS),-draws $(DRAWS)) $(if $(SEED),-seed $(SEED))

## vet: go vet
.PHONY: vet
vet:
	$(GO) vet -tags=$(GOTAGS) ./...

## lint: run the pinned golangci-lint
.PHONY: lint
lint:
	@$(GOLANGCI_LINT) version --short 2>/dev/null | grep -qxF '$(GOLANGCI_LINT_VERSION:v%=%)' || \
		{ echo "golangci-lint $(GOLANGCI_LINT_VERSION) required: run make tools" >&2; exit 1; }
	$(GOLANGCI_LINT) run ./...

## golangci-lint-version: print the pinned golangci-lint version (read by CI)
.PHONY: golangci-lint-version
golangci-lint-version:
	@echo $(GOLANGCI_LINT_VERSION)

## vulncheck: report known vulnerabilities reachable from the code
.PHONY: vulncheck
vulncheck:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) -tags=$(GOTAGS) ./...

## actionlint: check the GitHub Actions workflows
.PHONY: actionlint
actionlint:
	$(GO) run github.com/rhysd/actionlint/cmd/actionlint@$(ACTIONLINT_VERSION)

## fmt: gofmt the tree and tidy go.mod
.PHONY: fmt
fmt:
	$(GO) fmt ./...
	$(GO) mod tidy

## tidy-check: fail if go.mod or go.sum is not tidy
.PHONY: tidy-check
tidy-check:
	$(GO) mod tidy -diff

## clean: remove build output
.PHONY: clean
clean:
	rm -rf $(BIN_DIR)

## tools: install the pinned golangci-lint and goose into GOBIN
.PHONY: tools
tools:
	$(GO) install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	$(GO) install github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)

## migrate-up: apply pending migrations to ~/.curio/curio.db
.PHONY: migrate-up
migrate-up:
	$(GOOSE) -dir migrations sqlite3 $${CURIO_HOME:-$$HOME/.curio}/curio.db up

## migrate-status: show migration status
.PHONY: migrate-status
migrate-status:
	$(GOOSE) -dir migrations sqlite3 $${CURIO_HOME:-$$HOME/.curio}/curio.db status
