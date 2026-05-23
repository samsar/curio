# Status (handoff)

## What's done

| Step | Package | Description | Tests |
|---|---|---|---|
| 1 | `internal/curiohome` | `$CURIO_HOME` resolution, marker file, atomic writes | 11 |
| 1 | `internal/config` | YAML config with overlay + validation | 14 |
| 3 | `internal/urlutil` | URL normalization (dedup key) | 40+ |
| 4 | `internal/store/sqlite/db.go` + `migrations/` | Open, Migrate, sqlite-vec, embedded FS | 7 |
| 5 | `internal/store/store.go` + `sqlite/documents.go` + `sqlite/extractions.go` + `sqlite/bookmarks.go` + `sqlite/jobs.go` | CRUD impls + interfaces, claim-once job queue | 18 |
| 6 | `internal/store/sqlite/chunks.go` | FTS5 + sqlite-vec writes, BM25 + ANN search | 5 |
| 10 | `internal/indexer/chunker.go` | Paragraph-aware chunker with overlap | 9 |
| 12 | `internal/search/rrf.go` + `search.go` | RRF + hybrid engine + collapse strategies | 11 |
| - | `internal/fetcher/fetcher.go` | Interface + Single dispatcher | - |
| - | `internal/embedder/embedder.go` | Interface | - |

All tests green under `-race` with `make test`. Both `curio` and
`curio-daemon` binaries build clean. The daemon currently exposes only
`/v1/healthz` as a placeholder from the scaffold.

Total: ~4000 LOC across internal/, ~50% tests.

## What's not yet built

Critical path for completing M0:

1. **`internal/embedder/ollama.go`** — HTTP client against
   `http://localhost:11434/api/embeddings`. Tiny surface; needs to live
   against a real Ollama instance to validate.
2. **`internal/fetcher/web2md.go`** — `exec.Command` wrapper around the
   user's `web2md` Node tool. Returns a `Result` with markdown + title.
3. **`internal/indexer/indexer.go`** — orchestrates fetch → chunker →
   embedder → chunks store. Idempotent per document via
   `ChunkStore.ReplaceForDocument`. Pure plumbing once the deps exist.
4. **`internal/jobs/worker.go`** — polls `JobQueue.ClaimNext`, dispatches
   to a handler registry by `kind`. M0 runs one worker. Job handlers for
   `fetch` (calls dispatcher → fetcher → updates document + extraction →
   enqueues `index`) and `index` (calls indexer → marks done).
5. **`internal/api/*`** — HTTP handlers for `POST /v1/bookmarks`,
   `POST /v1/search`, `GET /v1/documents/{id}`, `GET /v1/healthz`,
   `GET /v1/stats`. Mounted via chi. Error responses are RFC 7807.
6. **`internal/client/client.go`** — thin HTTP client used by the CLI.
7. **`internal/cli/*`** — `curio add`, `curio search`, `curio status`,
   `curio daemon {start|stop|status}`.
8. **`internal/daemonctl/*`** — PID file lifecycle + auto-start.
9. **`cmd/curio-daemon/main.go`** — wire all the pieces together: load
   config, open DB, run migrations, construct stores/indexer/engine,
   start API server, start worker.

## Decisions made along the way

Newly logged in `docs/decisions.md`:

- SQLite build tags `sqlite_fts5,sqlite_json` are required and live in the
  Makefile. CI uses `make` so the tag list can't drift.
- DSN uses mattn's `_fk=true&_journal_mode=WAL&_synchronous=NORMAL&
  _busy_timeout=5000` (NOT modernc's `_pragma=...`).
- Migrations don't set `PRAGMA journal_mode` (can't run in a transaction);
  it's set per-connection by the DSN.
- Job claim is one atomic `UPDATE ... WHERE id = (SELECT ...) RETURNING`
  statement, not BEGIN/SELECT/UPDATE. Avoids deadlock under concurrent
  workers; tested with 20 jobs / 8 workers / `-race`.

## How to verify

```sh
make test         # all unit + integration tests
make build        # both binaries
./bin/curio version
./bin/curio-daemon # starts on :8765, /v1/healthz responds
```

## Suggested next-session order

The critical-path order to reach the M0 "done when" demo:

1. Write `internal/embedder/ollama.go` against a running local Ollama with
   `ollama pull nomic-embed-text`. Smallest unknown.
2. Write `internal/fetcher/web2md.go` against the existing
   `~/projects/experiments/web-to-markdown/` tool.
3. Write `internal/indexer/indexer.go` (orchestration only — all the
   subcomponents already exist and are tested).
4. Write `internal/jobs/worker.go` + handler registry + the two handlers.
5. Wire `cmd/curio-daemon/main.go` to construct everything and start.
6. Write the four API handlers (bookmarks create, search, get document,
   stats) and mount them.
7. Write the CLI subcommands (`add`, `search`, `status`, `daemon`).
8. Run the demo end-to-end.

After that, M1 (importers) starts.
