# Status

## M0 — Walking Skeleton

**Code complete. All packages build, all unit tests green under `-race`.**

The only thing standing between today's repo and the M0 "done when" demo is
installing Ollama and pulling `nomic-embed-text`. Everything else is wired.

### Completed packages

| Step | Package | Tests |
|---|---|---|
| 1 | `internal/curiohome` | 11 |
| 1 | `internal/config` | 14 |
| 3 | `internal/urlutil` | 40+ |
| 4 | `internal/store/sqlite/db.go` + migrations | 7 |
| 5 | `internal/store/{store.go, sqlite/*}` CRUD impls | 18 |
| 6 | `internal/store/sqlite/chunks.go` (FTS5 + vec) | 5 |
| 8 | `internal/fetcher/{fetcher.go, web2md.go}` | 10 |
| 9 | `internal/embedder/{embedder.go, ollama.go}` | 6 |
| 10 | `internal/indexer/chunker.go` | 9 |
| 11 | `internal/jobs/{worker.go, handlers.go}` | 7 |
| 12 | `internal/search/{rrf.go, search.go}` | 11 |
| 13 | `internal/api/*` (HTTP handlers) | smoke-tested via curl |
| 14 | `internal/daemonctl/lifecycle.go` | smoke-tested via CLI |
| 15 | `internal/cli/*` + `internal/client/client.go` | smoke-tested via CLI |
| 16 | `cmd/curio-daemon/main.go` daemon wiring + auto-init | runs cleanly |

**Total:** ~6600 LOC across `internal/`, ~50% tests.

## How to demo

```sh
# One-time setup
brew install ollama
ollama serve &
ollama pull nomic-embed-text

# Verify
curl -s http://localhost:11434/api/tags | jq '.models[].name'

# Use curio
cd ~/projects/curio
make build
./bin/curio daemon start

# Configure the web2md path. Edit ~/.curio/config.yaml and add:
#   fetcher:
#     web2md:
#       bin: "/Users/samansartipi/projects/experiments/web-to-markdown/web2md.js"
# Then restart: ./bin/curio daemon stop && ./bin/curio daemon start

./bin/curio add https://martinfowler.com/articles/feature-toggles.html --wait
./bin/curio search "feature flag rollout"
```

`docs/setup.md` has more detail and troubleshooting.

## Known M0 gaps to address later

These are not bugs — they're scope-trimmed pieces deferred from M0 to M1+:

- **`curio init`** — there's no explicit init command; the CLI auto-inits
  `~/.curio` on first run. If a future workflow needs explicit init
  (e.g., to choose a different embedder upfront), adding it is trivial.
- **`/v1/stats` returns version only.** Counter methods on the stores
  would surface document/bookmark/job totals. Add when M1 (importers)
  needs progress reporting.
- **Tags from bookmarks aren't denormalized into `chunks_fts`.** The
  index handler stubs this — would need `BookmarkStore.ListByDocument`.
  Lands when importers do.
- **No reindex CLI yet.** Documented in `docs/decisions.md`; needed when
  someone first wants to swap embedding models.
- **`/v1/documents`** list endpoint isn't wired; individual GET is.
  Trivial to add.
- **Search filters** (`content_type`, `host`, `source`, etc.) are
  accepted by the API but not yet applied. Engine work needed.
- **`curio refetch`** subcommand not wired.

## Decisions logged in `docs/decisions.md`

Worth re-reading before M1:

- SQLite build tags `sqlite_fts5,sqlite_json` centralized in Makefile.
- DSN syntax: mattn's `_fk=true&_journal_mode=WAL&...`.
- Migrations don't set PRAGMA `journal_mode` (transaction conflict).
- Job claim via atomic `UPDATE ... RETURNING` — verified under 20-job /
  8-worker / `-race`.
- Score normalization: BM25 negated, vector distance mapped via
  `1/(1+d)` so RRF is retriever-agnostic.
- Cross-paragraph chunker overlap not implemented; intra-paragraph is.
- Ollama runs natively (not Docker) — Apple Silicon GPU access.
- Embedder interface keeps the door open for Voyage/OpenAI/Bedrock
  without re-architecture.
- API: cursor pagination, RFC 7807 problem responses, no `tenant_id`
  in any response body, async ops return job IDs.
- File-then-row write order for extractions: pre-generate UUID,
  write markdown to disk, then insert the DB row pointing at it.

## Where M1 starts

M1 introduces real bookmark importers (Chrome, Safari, Firefox). The
schema and worker are already shaped for it: importer parses the
browser file → calls `BookmarkStore.Create` for each entry → enqueues
fetch jobs. The same fetch+index pipeline handles them.
