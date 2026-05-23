# M0 — Walking Skeleton

**Goal:** one URL goes in, searchable text comes out, end to end.

**Done when:**

```sh
$ curio daemon start
$ curio add https://martinfowler.com/articles/feature-toggles.html
   added bookmark <uuid> (job <uuid> enqueued)
$ # wait ~5s
$ curio search "feature flag rollout"
   1. Feature Toggles (martinfowler.com)
      "...the key idea is that the toggle should be a temporary measure..."
```

M0 is deliberately narrow:

- **One fetcher** (web2md, shelling out to the existing Node tool)
- **One embedder** (Ollama + `nomic-embed-text`)
- **One reference table** (bookmarks)
- **One importer** (manual `curio add` only — no Chrome/Safari yet)
- **One worker** (single-threaded job loop)
- **No MCP sidecar** (that's M3)
- **No insight layer** (that's M4)

But every interface that we know we'll swap is wired through interfaces from day one, so M1+ is additive.

---

## Module setup

Module path: `github.com/samansartipi/curio` (or whatever the repo URL ends up being).

```sh
cd ~/projects/curio
go mod init github.com/samansartipi/curio
```

### Direct dependencies

| Dep | Why |
|---|---|
| `github.com/spf13/cobra` | CLI framework |
| `github.com/go-chi/chi/v5` | HTTP router (lightweight, idiomatic) |
| `github.com/mattn/go-sqlite3` | SQLite driver with extension loading (needed for sqlite-vec) |
| `github.com/asg017/sqlite-vec-go-bindings` | sqlite-vec extension binding |
| `github.com/pressly/goose/v3` | Migrations |
| `github.com/google/uuid` | UUIDs |
| `github.com/ollama/ollama/api` | Ollama client |
| `gopkg.in/yaml.v3` | Config files |
| `github.com/stretchr/testify` | Test assertions |

Cgo is required (sqlite + sqlite-vec). Document `CGO_ENABLED=1` in the build
instructions. Cross-compile becomes harder but is not on the M0 critical path.

---

## Package layout (M0 only)

```
curio/
├── cmd/
│   ├── curio/                      # CLI binary
│   │   └── main.go
│   └── curio-daemon/               # daemon binary
│       └── main.go
├── internal/
│   ├── api/                        # HTTP server (handlers + router)
│   │   ├── server.go
│   │   ├── bookmarks.go
│   │   ├── search.go
│   │   ├── system.go
│   │   └── errors.go               # RFC 7807 mapping
│   ├── client/                     # HTTP client used by CLI
│   │   └── client.go
│   ├── cli/                        # cobra commands
│   │   ├── root.go
│   │   ├── add.go
│   │   ├── search.go
│   │   ├── daemon.go               # start/stop/status
│   │   └── status.go
│   ├── config/                     # config.yaml loader
│   │   └── config.go
│   ├── curiohome/                  # CURIO_HOME + marker file
│   │   └── home.go
│   ├── daemonctl/                  # PID file, start/stop, auto-start
│   │   ├── pidfile.go
│   │   └── lifecycle.go
│   ├── store/                      # storage interfaces + sqlite impls
│   │   ├── interfaces.go
│   │   └── sqlite/
│   │       ├── db.go               # open, pragmas, migrate, sqlite-vec load
│   │       ├── documents.go
│   │       ├── bookmarks.go
│   │       ├── chunks.go
│   │       ├── jobs.go
│   │       └── testutil.go         # ephemeral DB for tests
│   ├── fetcher/
│   │   ├── fetcher.go              # interface + dispatcher
│   │   └── web2md.go               # shells out to web2md
│   ├── embedder/
│   │   ├── embedder.go             # interface
│   │   └── ollama.go               # Ollama client
│   ├── indexer/
│   │   ├── chunker.go              # markdown → chunks (token-aware)
│   │   └── indexer.go              # fetch → chunk → embed → store
│   ├── search/
│   │   ├── search.go               # orchestrates BM25 + vector + RRF
│   │   └── rrf.go                  # pure RRF fusion
│   ├── jobs/
│   │   ├── queue.go                # SQLite-backed queue
│   │   ├── worker.go               # single-worker loop (multi later)
│   │   └── handlers.go             # fetch/index handlers
│   ├── urlutil/
│   │   └── normalize.go
│   └── version/
│       └── version.go              # build-time version info
├── api/
│   └── openapi.yaml
├── migrations/
│   └── 001_initial.sql
├── docs/
├── go.mod
├── Makefile                        # build / test / lint
└── README.md
```

`pkg/` is empty in M0. Add it only if external consumers need a Go SDK.

---

## File-by-file

Roughly in build order. Each file's purpose is one sentence.

### Layer 0: foundations

- **`internal/curiohome/home.go`** — resolve `$CURIO_HOME` (defaults to
  `~/.curio`), read/write `.curio-meta.json`, refuse to operate on a directory
  without our marker file.
- **`internal/version/version.go`** — `var Version = "dev"`, overridden at
  build time via `-ldflags`. Used in `/healthz`.
- **`internal/config/config.go`** — load `config.yaml`, env override
  (`CURIO_HOME`), provide typed access to: daemon port, embedding model,
  embedding dim, search defaults, fetcher rules path.

### Layer 1: storage

- **`internal/store/interfaces.go`** — `DocumentStore`, `BookmarkStore`,
  `ChunkStore`, `JobQueue`, `BM25Index`, `VectorIndex` interfaces. Pure Go
  types; no SQLite leakage.
- **`internal/store/sqlite/db.go`** — `Open(path) (*DB, error)`, sets PRAGMAs,
  loads sqlite-vec extension, runs goose migrations. Single `*sql.DB` shared
  by all impls.
- **`internal/store/sqlite/documents.go`** — implements `DocumentStore`.
- **`internal/store/sqlite/bookmarks.go`** — implements `BookmarkStore`.
- **`internal/store/sqlite/chunks.go`** — implements `ChunkStore` + serves as
  the `BM25Index` and `VectorIndex` impl (chunks_fts and chunks_vec virtual
  tables live here).
- **`internal/store/sqlite/jobs.go`** — implements `JobQueue`. Has the hot
  dispatch query.
- **`internal/store/sqlite/testutil.go`** — `NewEphemeralDB(t)` returns an
  in-memory SQLite with migrations applied; used by every storage test.

### Layer 2: urlutil + jobs

- **`internal/urlutil/normalize.go`** — `Normalize(rawURL string) (string,
  error)`. Lowercase host, strip fragment, drop tracking params (`utm_*`,
  `fbclid`, `gclid`, `ref`, `mc_*`), sort remaining params. Exhaustively unit
  tested.
- **`internal/jobs/queue.go`** — wraps the `JobQueue` storage interface with
  enqueue helpers (`EnqueueFetch(bookmarkID)`, `EnqueueIndex(documentID)`).
- **`internal/jobs/worker.go`** — `Worker.Run(ctx)` loop. Polls every N ms,
  claims one job at a time (UPDATE ... WHERE id = ? AND status='pending'),
  dispatches to handlers, marks done/failed with exponential backoff. M0 runs
  a single worker; the pool comes later.
- **`internal/jobs/handlers.go`** — `handleFetch` and `handleIndex`. Both
  receive a job payload (JSON), do the work, and enqueue the next job in the
  chain (`fetch` enqueues `index` on success).

### Layer 3: fetcher + embedder

- **`internal/fetcher/fetcher.go`** — `Fetcher` interface:
  `Fetch(ctx, url) (FetchResult, error)`. `FetchResult` carries markdown,
  content_type, status, metadata.
- **`internal/fetcher/web2md.go`** — implementation that shells out to the
  existing `web2md` Node tool. Locates it via config; logs stderr on failure;
  returns a `FetchResult` with `fetcher: "web2md"` set.
- **`internal/fetcher/dispatcher.go`** — for M0 always returns the web2md
  fetcher; structured this way so M2's rules engine drops in cleanly.
- **`internal/embedder/embedder.go`** — `Embedder` interface:
  `Embed(ctx, []string) ([][]float32, error)` and `Dimensions() int` and
  `Model() string`. Batch API even with one impl, because batching matters.
- **`internal/embedder/ollama.go`** — Ollama client targeting
  `http://localhost:11434/api/embeddings`. Reads model + base URL from config.
  Returns informative error if Ollama is unreachable.

### Layer 4: indexer + search

- **`internal/indexer/chunker.go`** — `Chunk(markdown, size=512, overlap=64)
  []Chunk`. Token-aware: respects paragraph and heading boundaries when
  possible. Uses a simple word-count approximation for tokens in M0 (real
  tokenizer later).
- **`internal/indexer/indexer.go`** — `Index(ctx, documentID)`: load doc, run
  chunker, batch-embed, write to chunks table + chunks_fts + chunks_vec.
  Idempotent — re-running deletes old chunks for the doc first.
- **`internal/search/rrf.go`** — pure function:
  `Fuse(ranked [][]ScoredID, weights []float64, k=60) []ScoredID`. Easy to
  unit-test.
- **`internal/search/search.go`** — `Search(ctx, req SearchRequest)
  (SearchResponse, error)`: parallel BM25 + vector queries, RRF fusion,
  chunk-to-doc collapse, filter application, top-k.

### Layer 5: daemon

- **`internal/daemonctl/pidfile.go`** — read/write/clean `~/.curio/daemon.pid`,
  verify the PID is actually our process.
- **`internal/daemonctl/lifecycle.go`** — `Start()`, `Stop()`, `Status()`,
  `EnsureRunning()` (auto-start helper called by CLI commands).
- **`internal/api/errors.go`** — `WriteProblem(w, status, title, detail)` per
  RFC 7807; helper for handlers.
- **`internal/api/system.go`** — `/v1/healthz` (checks Ollama reachability),
  `/v1/stats` (counts via storage).
- **`internal/api/bookmarks.go`** — `POST /v1/bookmarks`, `GET
  /v1/bookmarks`, `GET /v1/bookmarks/{id}`, `DELETE /v1/bookmarks/{id}`,
  `POST /v1/bookmarks/{id}/refetch`. (Import endpoint deferred to M1.)
- **`internal/api/search.go`** — `POST /v1/search`.
- **`internal/api/server.go`** — wires chi router, mounts handlers, owns the
  `*http.Server` lifecycle, exposes `Run(ctx)`.
- **`cmd/curio-daemon/main.go`** — load config, open DB, run migrations,
  construct stores/fetcher/embedder/indexer/search/queue/worker, start API
  server, start worker loop. Signal handling for clean shutdown.

### Layer 6: CLI

- **`internal/client/client.go`** — thin HTTP client over the daemon API.
  Generated types from `api/openapi.yaml` if we end up using `oapi-codegen`;
  hand-written for M0 is also fine.
- **`internal/cli/root.go`** — `curio` root command, global flags
  (`--daemon-url`, `--curio-home`), persistent setup (load config, init
  client).
- **`internal/cli/add.go`** — `curio add <url> [--folder PATH] [--tag X]`.
  Calls `POST /v1/bookmarks`. Optionally `--wait` to poll the resulting
  fetch job to completion.
- **`internal/cli/search.go`** — `curio search "query" [-k N] [--type pdf]`.
  Calls `POST /v1/search`, pretty-prints results with chunk snippets and
  scores.
- **`internal/cli/daemon.go`** — `curio daemon {start|stop|status|logs}`.
  Manages via PID file. `start` forks/execs `curio-daemon`; on macOS we may
  want to detach properly using `setsid`.
- **`internal/cli/status.go`** — `curio status`. Hits `/v1/stats` and
  `/v1/healthz`.
- **`cmd/curio/main.go`** — wires cobra root and calls `Execute()`.

---

## Build order

A linear path from nothing to working end-to-end. Each step ends with
something testable.

1. **Module + skeleton**: `go.mod`, empty `cmd/curio/main.go`, empty
   `cmd/curio-daemon/main.go`, `Makefile` with `build`/`test`/`lint`.
   *Verify:* `make build` produces two binaries.

2. **`curiohome` + `config`**: enough to read `~/.curio/config.yaml` and
   refuse missing marker file.
   *Verify:* unit tests + a hand-run that hits both error paths.

3. **`urlutil/normalize.go`**: with exhaustive table-driven tests.
   *Verify:* `go test ./internal/urlutil/...` passes.

4. **`store/sqlite/db.go` + migrations**: open DB, run migrations, sqlite-vec
   loaded.
   *Verify:* integration test opens an in-memory DB, runs migrations, queries
   `schema_meta`.

5. **`store/sqlite/{documents,bookmarks,jobs}.go`**: CRUD against real
   SQLite.
   *Verify:* per-impl integration tests using `testutil.NewEphemeralDB`.

6. **`store/sqlite/chunks.go`** including FTS5 and vec writes: a chunk
   round-trips, FTS5 returns it for a keyword query, vec returns it for a
   nearest-neighbor query against a known vector.
   *Verify:* integration test with a fake fixed-length embedding.

7. **`jobs/queue.go` + `worker.go`** with a stub handler that just marks done.
   *Verify:* enqueue a job, run the worker for one tick, see it `done`.

8. **`fetcher/web2md.go`**: shells out, parses output, returns
   `FetchResult`.
   *Verify:* integration test against a real local web2md run, plus a unit
   test with a fake `exec.Command` for the error paths.

9. **`embedder/ollama.go`**: hits a real local Ollama (or a mock server).
   *Verify:* integration test against the local Ollama running
   `nomic-embed-text`; unit test against an `httptest.Server` for error
   paths.

10. **`indexer/`**: chunker + indexer. End-to-end inside the process: given
    a document with markdown on disk, produce chunks → embed → write.
    *Verify:* integration test that builds the doc, runs `Index`, queries the
    chunks store directly.

11. **`jobs/handlers.go`**: real fetch + index handlers wired up.
    *Verify:* enqueue a `fetch` job for a real URL, run the worker, see the
    chain complete (`fetch` → enqueues `index` → `index` runs → chunks
    written).

12. **`search/`**: RRF + orchestration.
    *Verify:* seed three documents, run a query, assert ordering. RRF unit
    tests separately.

13. **`api/`** handlers and server.
    *Verify:* `httptest`-based handler tests + one end-to-end test that boots
    the full daemon in-process and exercises `POST /v1/bookmarks` →
    `POST /v1/search`.

14. **`daemonctl/`** + `cli/daemon.go`: PID file lifecycle.
    *Verify:* spawn daemon, hit `/healthz`, send `Stop()`, PID file cleaned
    up.

15. **`cli/{add,search,status}.go`**: thin HTTP clients.
    *Verify:* end-to-end shell test (Makefile target `make e2e`) that runs
    `curio daemon start` → `curio add <url>` → poll → `curio search`.

16. **Auto-start**: CLI commands that need the daemon call
    `daemonctl.EnsureRunning()` first.
    *Verify:* after `curio daemon stop`, running `curio add ...` starts the
    daemon transparently.

Working in this order means every step ends with green tests and a useful
artifact. Nothing depends on layers above it.

---

## Testing

Three test categories. Keep them separable so CI can run them independently
when we get there.

| Kind | Lives in | Runs against | Build tag |
|---|---|---|---|
| Unit | `*_test.go` next to source | Pure Go, no external services | (none) |
| Integration | `*_integration_test.go` | Real SQLite (in-memory), real Ollama (local), real web2md | `//go:build integration` |
| E2E | `test/e2e/` shell scripts + a Go runner | Full daemon + CLI on disk | `//go:build e2e` |

Specific tests worth having from day one:

- **`urlutil`**: 40+ cases. The dedup story depends on this being right.
- **`indexer/chunker`**: edge cases — empty input, code blocks, headings,
  unicode, very long single paragraph.
- **`search/rrf`**: known-result examples from the RRF paper.
- **`jobs/worker`**: claim-once semantics under concurrent workers (even
  though M0 runs one — write the test now, it'll save you later).
- **`store/sqlite/chunks`**: FTS5 + vec round-trip together (the most
  failure-prone integration point).

---

## Configuration

`~/.curio/config.yaml`:

```yaml
daemon:
  listen: "127.0.0.1:8765"
  log_level: "info"

embedding:
  provider: "ollama"
  model:    "nomic-embed-text"
  dim:      768
  base_url: "http://localhost:11434"

fetcher:
  web2md:
    bin: "/usr/local/bin/web2md"   # or wherever the user's tool lives
    timeout_seconds: 30

search:
  default_k: 10
  rrf_k: 60
  bm25_weight: 1.0
  vector_weight: 1.0
  collapse: "max"

chunking:
  size_tokens: 512
  overlap_tokens: 64
```

The config loader applies defaults; missing fields are fine.

---

## Non-goals for M0

Explicitly out of scope, listed so they don't accumulate in the M0 PR:

- Chrome / Safari / Firefox bookmark importers (M1)
- Multiple fetchers and the rules engine (M2)
- MCP sidecar (M3)
- Insight layer: clustering, summarization, suggestions (M4)
- Web UI
- Postgres / pgvector implementations
- Authentication
- `systemd` / `launchd` integration
- Cross-compile / release packaging
- Streaming search results
- `curio reindex` (deferred to first model swap)

---

## Definition of done

```sh
# Fresh machine. Ollama running with nomic-embed-text pulled. web2md installed.
make build
./bin/curio daemon start
./bin/curio add https://martinfowler.com/articles/feature-toggles.html --wait
./bin/curio search "feature flag rollout"
# Returns the Fowler article with a relevant chunk snippet.
./bin/curio status
# Shows: 1 bookmark, 1 document (fetched), 0 jobs pending, embedding=nomic-embed-text dim=768
./bin/curio daemon stop
```

Plus: all unit tests green, integration tests green when Ollama + web2md are
available, the one e2e test green.

---

## Estimated scope

Not a commitment, just a sanity check. About 2,500–4,000 lines of Go for M0
including tests, spread roughly:

- Storage layer: ~600 LOC
- Fetcher + embedder: ~300 LOC
- Indexer + search: ~500 LOC
- Job queue + worker: ~350 LOC
- HTTP API: ~400 LOC
- CLI: ~400 LOC
- Daemon lifecycle: ~200 LOC
- Tests: similar to source code size

Realistic chunk for a focused weekend or two, depending on how many of the
above layers you've built in Go before.
