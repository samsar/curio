# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build, test, lint

```sh
make build               # produces ./bin/curio, ./bin/curio-daemon, ./bin/curio-mcp
make test                # unit tests under -race
make test-integration    # needs Ollama + web2md available
make test-e2e            # boots the daemon end-to-end
make lint                # golangci-lint v2 with the project's .golangci.yml
make fmt                 # gofmt + go mod tidy
make help                # full target list
```

Run a single test:
```sh
go test -race -count=1 -tags=sqlite_fts5,sqlite_json -run TestParseHTML_Basic ./internal/importer/...
```

The build tags are mandatory; without them SQLite FTS5 is missing and `chunks_fts` virtual table creation fails. `make` already passes them. Never invoke `go build` / `go test` directly without `-tags=sqlite_fts5,sqlite_json` — the Makefile is the source of truth.

Cgo is required (sqlite, sqlite-vec). `CGO_ENABLED=1` is forced in the Makefile.

## Tooling traps

- **Go version**: `go.mod` declares `go 1.25.7` because deps (`pressly/goose`, `html-to-markdown/v2`, others) require it. `go mod tidy` run with a different Go version produces a different `go.sum` — CI uses the version in `go.mod` via `go-version-file`. Always tidy with the matching toolchain: `GOTOOLCHAIN=go1.25.7 go mod tidy`.
- **No `toolchain` directive**: removed because golangci-lint v2 sees it as the targeted version. Don't add it back unless you also bump the linter to a version built with a newer Go.
- **golangci-lint v2.12.2** is the pinned version; older v2.0.x was built with go1.24 and rejected our modules. The action is `golangci/golangci-lint-action@v7` (v6 doesn't pull v2.x).

## Architecture in one screen

Three binaries:

```
curio     (CLI, cobra)   ──HTTP+JSON──►  curio-daemon  ──►  SQLite (~/.curio/curio.db)
curio-mcp (MCP sidecar)  ──HTTP+JSON──►       │             ├ FTS5 (chunks_fts)
                                              │             └ sqlite-vec (chunks_vec)
                                              │
                                              ├─► Ollama at http://localhost:11434  (embeddings)
                                              ├─► Native fetcher (Go-native HTTP + Readability)
                                              └─► Optional web2md (Node subprocess)
```

`curio-mcp` (`cmd/curio-mcp`) speaks MCP over stdio to clients like Claude Code and forwards tool calls to the daemon over the same HTTP API the CLI uses; it auto-starts the daemon. See `docs/mcp.md`.

- Storage state lives under `$CURIO_HOME` (default `~/.curio`): `curio.db`, `content/<doc_id>/<extraction_id>.md`, `daemon.pid`, `daemon.log`, `.curio-meta.json`.
- The CLI auto-starts the daemon when needed via `internal/daemonctl`. PID file at `~/.curio/daemon.pid`. The auto-discovery picks `curio-daemon` from the same directory as the `curio` binary, override with `CURIO_DAEMON_BIN`.
- The daemon listens on `127.0.0.1:8765` (local-only).

## Where the design lives

**Read these before making non-trivial changes:**

- `docs/decisions.md` — running log of design choices and *why*. The single most important doc; consult before second-guessing anything that looks weird (e.g., why the chunker has a 3500-char cap, why we don't use `toolchain` in go.mod, why `MarkFailed` returns `(permanent bool, error)`).
- `docs/architecture.md` — components, transports, data flow.
- `docs/data-model.md` — schema and the "documents vs references" split.
- `docs/setup.md` — Ollama + web2md installation flow.
- `docs/roadmap.md` and `docs/status.md` — what's done vs. deferred per milestone.
- `api/openapi.yaml` — HTTP contract.

## State machine, briefly

This catches people:

**Jobs**: `pending ↔ running → done | failed`. `failed` is terminal — won't retry. The `run_after` column on a failed row is stale data from the last retry cycle (we update status + last_error but not run_after at terminal transition). The CLI hides `next_attempt` for failed/done rows for that reason.

**Documents**: `pending → fetched | failed`. The `failed` transition is driven by `jobs.OnPermanentFailure` hook — when a fetch or index job permanently fails, the worker calls the hook which sets `doc.state = failed`. Without this hook, docs would stay `pending` forever even after their jobs gave up. `refetch` flips the doc back to `pending` and enqueues a fresh job (new attempts counter).

`dead` exists in constants but is unused — reserved for a future "don't allow refetch" policy.

## Fetcher fallback policy

`internal/fetcher/native.go` falls back to Jina Reader (`r.jina.ai`) only when the original error wraps `ErrLoginWall` or `ErrAntiBot`. Hard errors (404, DNS failures, timeouts) return directly — Jina can't help and burning rate-limit budget there gets us 429'd on the calls that would actually benefit. If you're tempted to widen the fallback, read `docs/decisions.md` under "Fallback strategy" first.

`ErrAntiBot` wraps HTTP 403 and 503. The Native fetcher also sends Chrome-like headers (`Sec-Fetch-*`, `Sec-Ch-Ua-*`) to reduce false-positive bot blocks.

## Code layout pointers

- `internal/store/` — interfaces (`store.go`) + sqlite impls (`sqlite/`). The interface boundary is real; other packages should never import `internal/store/sqlite` directly except `cmd/curio-daemon/main.go` and tests.
- `internal/jobs/` — Worker loop + handlers. `OnPermanentFailure` hooks are wired in `Register`.
- `internal/api/` — HTTP handlers, RFC 7807 errors, chi router.
- `internal/cli/` — Cobra commands. Pattern: each command file (`add.go`, `docs.go`, etc.) exports `newXxxCmd()` and `root.go` adds them.
- `internal/fetcher/` — Native (Go) and Web2MD (subprocess) backends behind the same `Fetcher` interface.
- `internal/indexer/` — Chunker (paragraph-aware with hard char cap) + orchestrator (chunk → embed → store).
- `migrations/` — Goose SQL migrations, embedded into the binary via `embed.go`.

## Conventions to preserve

- The CLI never echoes `tenant_id` to clients; tenant scoping is server-side. Single-tenant local installs hardcode `"local"`.
- API list endpoints use cursor pagination (`?cursor=...`), not offset. Stable under concurrent writes.
- Long-running ops (import, refetch-all) return `202 + job_id`; clients poll.
- Errors over the wire are RFC 7807 (`application/problem+json`).
- `curio docs` and `curio jobs` default to the happy-path view (`state=fetched`, `status=done`). `--failed`, `--all`, and explicit `--state`/`--status` widen.
- Both list views include the on-disk markdown path under `doc_id` so `cat`, `curio docs show`, and `curio refetch` are copy/paste-ready.

## CI release flow

`v*` git tags trigger `.github/workflows/release.yml` → goreleaser → publishes a binary tarball to GitHub releases and writes `Formula/curio.rb` to `samsar/homebrew-tap`. cgo limits us to `darwin/arm64` for now (single macos-14 runner). Adding amd64 or linux means matrix runners + `goreleaser --split`/`--merge`; deliberately deferred.
