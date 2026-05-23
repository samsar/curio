# Decisions

A running log of design decisions, what we picked, and why. New entries go at
the bottom. When a decision is revisited, add a new entry rather than rewriting
the old one — the history is useful.

---

## Language: Go

**Decision:** Go for the daemon, CLI, and MCP sidecar.

**Why:** Single-binary distribution, great concurrency for a crawler, mature
CLI ecosystem (Cobra), strong daemon patterns (PID files, systemd, launchd).

**Tradeoff:** The ML ecosystem is Python-first. We mitigate by treating Ollama
as a sidecar — Go talks HTTP to it for embeddings and LLM calls. No Go ML
libraries needed.

---

## Architecture: daemon + thin clients

**Decision:** A background daemon owns all state and workflows. CLI, MCP, and
future web UI are thin HTTP clients.

**Why:** Crawling and indexing are long-running. The MCP server needs an
always-on backend. Multiple clients (CLI, MCP, future web) should share the
same brain. Modeled after `dockerd` + `docker`.

**Alternative considered:** CLI does everything in-process, no daemon. Rejected
because background jobs and the MCP requirement both need persistent state and
event loops.

---

## Transport: HTTP + JSON

**Decision:** HTTP+JSON between clients and daemon. OpenAPI spec is the source
of truth.

**Why:** Easy to debug with `curl`, MCP speaks JSON anyway, no protobuf
toolchain.

**Alternative considered:** gRPC. Better typing and codegen, but adds toolchain
weight without enough payoff at single-user scale.

---

## MCP server as a sidecar process

**Decision:** `curio-mcp` is a separate binary, not built into the daemon.

**Why:** MCP servers are spawned per-session by clients like Claude Code. Their
lifecycle differs from the always-on daemon. A sidecar adapter (stdin/stdout
MCP → HTTP to daemon) keeps both lifecycles clean.

---

## Storage: SQLite for v1

**Decision:** SQLite with FTS5 for BM25 and sqlite-vec for vectors. Markdown
content on disk under `~/.curio/content/`.

**Why:** Zero ops, fast for single-user, handles the job queue too. Content on
disk keeps the DB small and lets `ripgrep` work against the corpus directly.

**Forward compatibility:** All access goes through `DocumentStore`, `BM25Index`,
`VectorIndex` interfaces. Postgres + pgvector impls land when hosted-mode
demands them.

---

## Job queue: SQLite-backed

**Decision:** A `jobs` table with worker pool polling, not a dedicated queue
(asynq, NATS, Redis, ...).

**Why:** One less moving part. At single-user scale, a few thousand jobs/day is
trivial for SQLite. Polling interval ~1s is fine.

**Forward compatibility:** The `JobQueue` interface lets us swap to asynq or
similar when hosted-mode demands fan-out or stronger durability guarantees.

---

## Embedding model: nomic-embed-text via Ollama

**Decision:** Lock v1 to `nomic-embed-text` (768d) via local Ollama.

**Why:** Runs on CPU, free, good quality, stays local (matches local-first
posture).

### Embedding model swap

Switching embedding models requires a full re-embed because old and new vectors
aren't comparable. The process:

1. Update `embedding.model` and `embedding.dimensions` in `config.yaml`.
2. Run `curio reindex --reason=model-swap`. This:
   - Drops and recreates the `chunks_vec` virtual table at the new dimension.
   - Enqueues `index` jobs for every document (re-embeds all chunks).
3. `.curio-meta.json` is updated with the new model name and dimension.
4. BM25 (FTS5) index is unaffected — keyword search keeps working through the
   re-embed.

To make this swap safe:

- All embedding access goes through the `Embedder` interface
  (`Embed(text) ([]float32, error)` + `Dimensions() int`).
- `chunks_vec` is rebuilt, not migrated — vector dimensions are fixed at table
  creation.
- The daemon refuses to start if `config.yaml`'s embedding model disagrees with
  `.curio-meta.json` and `reindex` hasn't been run.

Future enhancement: support running two embedders side-by-side during a
transition (write to both, read from old, then cut over). Not needed for v1.

---

## Daemon lifecycle: PID file + auto-start

**Decision:** `curio daemon {start|stop|status|logs}` manages the daemon via a
PID file in `~/.curio/daemon.pid`. CLI commands that need the daemon auto-start
it if not running.

**Why:** Most ergonomic for a single-user tool — user never has to think about
it. Skip `launchd`/`systemd` complexity for v0.

**Later:** `curio service install` drops a `launchd` plist (macOS) or
`systemd` unit (Linux) for boot-time auto-start.

---

## Storage location: `~/.curio` with marker file

**Decision:** All state under `$CURIO_HOME` (default `~/.curio`). A
`.curio-meta.json` marker file is required for the daemon to consider the
directory its own.

**Why:** Predictable, easy to back up, easy to nuke. The marker file protects
against unrelated tools that might have created `~/.curio` (unlikely but
defensive).

**Collision handling:** If `~/.curio` exists without the marker, the daemon
refuses to start and prompts the user to set `CURIO_HOME` to a different path.

---

## Data model: documents are universal, references are per-source

**Decision:** Extracted content lives in `documents`, deduplicated by URL.
Bookmarks, history entries, highlights, etc. are separate reference tables
that point at documents.

**Why:** A URL can show up in multiple sources. Sharing the document avoids
re-fetching, re-embedding, and makes "this appears in multiple sources" a
useful signal for the insight layer. See
[data model](./data-model.md#core-idea-separate-references-from-content).

---

## Multi-tenancy: `tenant_id` on reference tables, not child tables

**Decision:** `tenant_id` lives on `bookmarks`, `jobs`, future reference tables,
and (denormalized) on `documents`. `chunks` and `document_extractions` inherit
through their parent.

**Why:** Single-user installs hardcode `tenant_id = "local"`. Hosted mode is a
deployment change, not a schema rewrite. Denormalizing onto `documents` keeps
searches fast without making every chunk row carry the tenant.

User IDs are deferred until team plans are an actual product requirement.

---

## Fetcher selection: data-driven rules file

**Decision:** `fetcher_rules.yaml` lists rules top-to-bottom, first match wins.
Hot-reloadable.

**Why:** Adding domain-specific behavior shouldn't require a recompile. Users
will want to tune this themselves (e.g., switching a paywalled domain to Jina).

---

## Hybrid search: BM25 + vector + RRF

**Decision:** BM25 and vector run in parallel, results merged via Reciprocal
Rank Fusion (RRF, k=60), then chunks collapse to documents.

**Why:** BM25 wins on rare terms and proper nouns; vector wins on conceptual
matches; RRF is the standard, simple, parameter-light fusion method.

**Knobs exposed in config:** BM25/vector weights in RRF, chunk-to-doc collapse
strategy.

---

## API: cursor pagination, not offset

**Decision:** List endpoints use opaque cursors (`?cursor=...` + `next_cursor`
in the response), not offset/limit.

**Why:** Offset is buggy under concurrent writes — rows land between page
fetches and clients silently skip data. Cursors are stable on SQLite via
`WHERE id > :cursor ORDER BY id LIMIT N`. Cost is the same.

---

## API: all long-running operations are async with job IDs

**Decision:** Imports, refetches, and (later) reindex operations return
`202 Accepted` with `{ job_id }`. Clients poll `/v1/jobs/{id}` for status.

**Why:** Imports can take minutes (10k+ bookmarks → 10k+ fetches). Refetches
can take seconds-to-minutes per URL. Sync responses tie up HTTP connections
and force timeout tuning. Polling is simple and observable.

The CLI hides the polling from the user (`curio import chrome` blocks with a
progress bar). The MCP sidecar can either poll or hand the `job_id` back and
let the next turn check.

---

## API: search response exposes BM25 and vector scores per chunk

**Decision:** Each `SearchHit.matches[]` entry includes both `bm25_score` and
`vector_score` (either may be null if that retriever didn't surface the
chunk), alongside the fused score.

**Why:** Cheap to add now, invaluable for tuning later. "BM25 said 0, vector
said 0.87" is a very different story from "both said 0.5" — the per-retriever
view tells you whether the user needs more semantic recall or more keyword
precision. Without this, hybrid search becomes a black box.

---

## API: search knobs are per-request overrides

**Decision:** `weights` (BM25 vs vector RRF mix) and `collapse` (chunk-to-doc
aggregation) are optional fields in the search request body. Defaults come
from server config.

**Why:** Different clients want different tradeoffs. The CLI defaults are fine
for ad-hoc search; the MCP sidecar may want broader recall when providing
context to an LLM; a future "find exact quote" tool wants pure BM25. Avoids
forcing server reconfiguration for client-specific behavior.

---

## API: `tenant_id` is server-side only, never echoed to clients

**Decision:** `tenant_id` exists on every database row but is **not** included
in any API response.

**Why:** The client already authenticated as a tenant; the server is
responsible for filtering. Echoing the tenant back is at best redundant noise,
at worst leaks an internal data-model concern. If we ever build a
cross-tenant admin API, it lives under `/admin/*` with explicit tenant
scoping in the URL — separate from the regular API surface.

---

## API: bulk operations live under named endpoints, not `/batch`

**Decision:** Browser bookmark files import through `POST /v1/bookmarks/import`,
which handles tens of thousands of bookmarks in a single request. No generic
`POST /v1/bookmarks/batch` accepting arrays of bookmark objects.

**Why:** The real bulk use case (browser file import) is covered by the
purpose-built endpoint, which also parses, dedups, and enqueues fetches in
one server-side pass. A generic batch endpoint would be useful only if a
non-file source ever needed to push N pre-parsed bookmarks — speculative for
v1. Easy to add later; non-breaking.

---

## API: `/v1/documents/{id}/references` returns a shape that grows additively

**Decision:** The references endpoint returns
`{ bookmarks: [...], history_entries: [...], highlights: [...] }`. v1 only
populates `bookmarks`. Future reference kinds appear as new top-level fields.

**Why:** Pays off the references-vs-documents split — clients can see *every*
way the user encountered a document, across all sources. Additive shape means
existing clients ignore unknown reference kinds without breaking.

---

## SQLite build tags

**Decision:** All `go build` and `go test` invocations pass
`-tags=sqlite_fts5,sqlite_json` to `mattn/go-sqlite3`.

**Why:** FTS5 (`CREATE VIRTUAL TABLE ... USING fts5`) is not compiled into
mattn's default SQLite build; it requires the `sqlite_fts5` build tag.
`sqlite_json` ensures the `json_valid()` function used in our CHECK
constraints is always available. The Makefile centralizes this so every
build/test/vet invocation gets the same tags; CI uses `make` targets rather
than re-spelling the tag list.

`sqlite-vec` does NOT need a build tag — it's loaded as a runtime extension
via `sqlite_vec.Auto()` from the sqlite-vec-go-bindings package.

---

## SQLite DSN: per-connection pragmas via mattn's query params

**Decision:** `Open()` builds a DSN like
`file:/path/curio.db?_fk=true&_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000`.

**Why:** SQLite's `foreign_keys` PRAGMA is *per-connection* and defaults to
OFF — every connection in `database/sql`'s pool must turn it on, or FK
constraints are silently unenforced. Running `PRAGMA foreign_keys = ON` in
migrations only affects that one connection. The DSN-level params apply to
every new pooled connection.

Note: `_pragma=foreign_keys(1)` syntax is for `modernc.org/sqlite`, NOT
`mattn/go-sqlite3`. They look similar; mixing them silently no-ops.

---

## Migrations do not set PRAGMA journal_mode

**Decision:** Removed `PRAGMA journal_mode = WAL;` from the initial migration.

**Why:** Goose wraps SQL migrations in a transaction, and SQLite refuses to
change journal modes inside a transaction (errors with "cannot change into
wal mode from within a transaction"). The DSN already sets journal_mode at
connection time, so the migration PRAGMA was redundant *and* breaking.

---

## What's deferred from the v1 API

These are intentionally omitted from `api/openapi.yaml`. Each is additive when
it lands — no `/v1` → `/v2` bump required.

- **Insight layer endpoints** (`/v1/clusters`, `/v1/interests`,
  `/v1/suggestions`) — added in milestone M4.
- **Config endpoints** (`GET/PUT /v1/config`) — for now, edit
  `~/.curio/config.yaml` and `SIGHUP` the daemon.
- **Admin reindex endpoint** (`POST /v1/admin/reindex`) — added when an
  embedding model swap is an actual need. Until then, run `curio reindex` CLI.
- **Server-Sent Events for job progress** — polling is fine for v1. If the
  CLI's progress UX gets ugly, add `/v1/jobs/{id}/stream`.
- **Generic batch endpoints** for bookmarks, documents, or jobs. Add only
  when a real non-file source needs them.
- **Authentication scheme** (API keys, OAuth, SSO). Middleware hook is in
  place; the actual mechanism is deferred to hosted-mode work.
- **WebSocket or streaming search** — current `POST /v1/search` is fine.

---

## What's not decided yet

- **Insight layer specifics:** clustering algorithm, summarization prompts,
  trajectory analysis. Deferred until the corpus layer is working and we have
  real data to look at.
- **Authentication for hosted mode:** middleware stub goes in early but the
  actual auth scheme (API keys vs OAuth vs SSO) is deferred.
- **Re-crawl policy:** how often to refetch a given URL. Likely
  domain-rule-driven (news daily, docs monthly, static essays never).
- **Highlight / read-later importers:** schema is ready; importer code is not
  in v1.
