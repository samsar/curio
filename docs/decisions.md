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

**Forward compatibility:** All access goes through the `internal/store`
interfaces (`DocumentStore`, `ChunkStore`, `JobStore`, ...), and a lint rule
keeps it that way (see "Store boundary" below). Postgres + pgvector impls land
when hosted-mode demands them.

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
aren't comparable.

**Implemented today:** `curio reindex <id>` and `curio reindex --all` enqueue
`index` jobs that re-chunk and re-embed documents from their *existing*
extraction — no re-fetch (`POST /v1/documents/{id}/reindex`, `/reindex-all`;
`--all` defaults to `state=fetched`). This covers a **same-dimension** model
swap, chunker-setting changes, and picking up newly-added bookmark tags. BM25
(FTS5) is unaffected — keyword search keeps working through the re-embed.

**Not yet implemented** — a *different-dimension* swap additionally needs:

1. Update `embedding.model` + `embedding.dim` in `config.yaml`.
2. Drop and recreate the `chunks_vec` virtual table at the new dimension
   (vec dims are fixed at table creation — rebuild, not migrate).
3. Update `.curio-meta.json` with the new model + dimension.
4. A startup guard that refuses to run if `config.yaml`'s model disagrees with
   `.curio-meta.json` and a reindex hasn't happened.

A `--reason` flag and steps 2–4 are future work; today `reindex` assumes the
dimension is unchanged. All embedding access already goes through the
`Embedder` interface, so the swap stays tractable. Future enhancement: run two
embedders side-by-side during a transition. Not needed for v1.

---

## Daemon lifecycle: PID file + auto-start

**Decision:** `curio daemon {start|stop|status|logs}` manages the daemon via a
PID file in `~/.curio/daemon.pid`. CLI commands that need the daemon auto-start
it if not running.

**Why:** Most ergonomic for a single-user tool — user never has to think about
it. Skip `launchd`/`systemd` complexity for v0.

**Revised:** the PID file is now the daemon's own single-instance lock (an
`flock` it holds for its lifetime), written by the daemon, not the CLI, and
liveness is decided by the lock rather than `kill(pid, 0)`. See "Single
daemon per home: flock on daemon.pid, bind before touching the DB" below.

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

**Decision:** BM25 and vector run concurrently, results merged via Reciprocal
Rank Fusion (RRF, k=60), then chunks collapse to documents.

**Why:** BM25 wins on rare terms and proper nouns; vector wins on conceptual
matches; RRF is the standard, simple, parameter-light fusion method.

**Degradation is asymmetric.** BM25 is local SQLite and always available, so a
BM25 failure is a bug or corruption: the search fails (500) and the in-flight
vector leg is canceled. The vector leg depends on an embedding model in another
process — Ollama may be down, not started yet, or hung — which is legitimately
optional at query time. When it fails for any reason (embed error or timeout,
no or wrong-sized vector, ANN error), search returns the BM25 results with
`degraded: true` and a warning ("semantic search unavailable (…); keyword-only
results") and logs one WARN. Previously the embed error discarded the BM25 hits
already in hand, so `curio search`, MCP `search_bookmarks` and `curio eval`
failed outright whenever Ollama was down. Two edges: if the caller's own
context is canceled or past its deadline, that's an error, never a degraded
success; and if BM25 had no terms to search (all stopwords) and the vector leg
fails, the result is empty but degraded, with the warning explaining why.
There's no circuit breaker: a refused connection fails instantly, and the
embed deadline bounds the hung case.

**Query embed deadline:** `search.embed_timeout_seconds` (default 10) bounds
embedding the query, separately from the embedder's per-request timeout
(`embedding.timeout_seconds`, sized for index batches). Before, the only bound
was that 60 s client timeout. Keep it well under 30 s: the CLI and MCP give up
on a daemon request after 30 s, so a longer deadline turns a hung Ollama back
into a client-side timeout instead of a degraded result.

**Fanout scales with k:** each retriever returns `max(50, 8·k)` chunks, the same
rule `find_related` uses — hits are chunk-level, and one long document can fill
dozens of slots, so a fixed 50-chunk pool could never return k=60 documents.
Because the fanout is a SQL LIMIT, k is bounded: 1..100 (`search.MaxK`), 400
outside it; omitted means `search.default_k`, which is now actually applied
(the engine used to hardcode 10, and the CLI and MCP always sent 10).

**Knobs exposed in config:** BM25/vector weights in RRF (finite, not negative,
not both zero; a single zero switches that retriever's contribution off),
chunk-to-doc collapse strategy, `default_k`, `embed_timeout_seconds`.

---

## BM25 query sanitization: OR + stopwords

**Decision:** Before handing a user query to FTS5's `MATCH`, run it through
`sanitizeBM25Query` in `internal/search/search.go`:

  1. Extract word-like tokens (letters, digits, apostrophes, hyphens).
  2. Drop common English stopwords (small curated list, ~80 entries).
  3. Wrap each remaining token in `"..."` so FTS5 treats it as a literal
     phrase — escapes punctuation and reserved keywords (AND, OR, NOT, NEAR).
  4. Join with ` OR ` so any content-bearing token can hit.
  5. If nothing survives, return empty; caller skips BM25 and runs only the
     vector leg.

**Why:** Two real failures on natural-language queries forced this.

  - **Crash:** "Find me articles about computer science, data structures, and
    algorithms" — bare commas are an FTS5 syntax error, raw query went
    straight to MATCH, daemon returned HTTP 500.
  - **Zero results:** even after wrapping tokens in quotes, FTS5's default
    AND semantics required every token to be present. No real chunk has all
    13 words of a long query, so BM25 returned 0 on every long query —
    silently halving the hybrid pipeline.

OR + stopwords mirrors what Elasticsearch / OpenSearch / Vespa ship by default
for natural-language queries. BM25's role in hybrid search is to catch exact
lexical hits the vector misses (proper names, identifiers, jargon, version
numbers); it does not need to "understand" the query — that's the vector
leg's job.

**Stopword list is intentionally short.** Over-aggressive stopwording hurts
recall on technical terms that overlap English ("set", "map", "list" in CS
contexts). We drop only obvious function words plus a handful of
query-framing verbs ("find", "show", "tell", "give") that flood
natural-language queries without carrying retrieval signal.

**What we deliberately did NOT add:**

  - **Stemming / lemmatization.** Would need a stemmer dependency and the
    FTS5 index would need to be rebuilt with a stemming tokenizer. Defer
    until evidence of "I searched for `algorithms` and missed docs that say
    `algorithm`" actually matters.
  - **Synonym expansion.** Same reason: not worth the complexity for v1.
  - **`minimum_should_match` tuning.** FTS5 doesn't expose it natively; would
    need post-filtering. Pure OR is the simplest thing that could work.

**Trade-off acknowledged:** OR over many tokens can surface noisy results
(any chunk with "data" ranks). The RRF fusion with vector search and the
stopword filter together keep this manageable. See "Natural-language search
is provisional" in the deferred section for what we may revisit.

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

The bulk `refetch-all` and `reindex-all` answer `202` with `{ jobs_enqueued }`
instead: each document gets its own job and there is no parent job to poll
(see "Refetch: state reset and fetch job in one transaction").

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

**Status: not implemented.** Only the server config sets `weights` and
`collapse` today, and the request decoder rejects unknown fields, so sending
them is a 400. `api/openapi.yaml` no longer advertises them (nor the
`saved_after`/`saved_before` filters). The design below stands for when they
land.

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

## Job queue claim via atomic UPDATE ... RETURNING

**Decision:** `ClaimNext` uses a single `UPDATE jobs SET status='running' WHERE
id = (SELECT id ... LIMIT 1) RETURNING ...` statement, not a `BEGIN; SELECT;
UPDATE; COMMIT;` transaction.

**Why:** The transaction approach deadlocks under concurrent workers. The
initial SELECT takes a SHARED lock; when each worker tries to upgrade to a
RESERVED lock for the UPDATE, they all block on each other and SQLite's
busy_timeout doesn't save us under load. The single-statement form acquires
the write lock immediately, serializes cleanly across workers, and is also
shorter code.

Tested with 20 jobs / 8 workers / `-race`: every job claimed exactly once,
no duplicates, no errors. The test lives in jobs_test.go specifically so the
multi-worker semantics are locked in before the M1 worker-pool expansion.

**Claim order and index:** the claim takes the pending job that became
runnable first: `ORDER BY run_after, created_at`, served by
`idx_jobs_claim (status, kind, run_after, created_at)`. Every daemon pool
claims one kind, and for one kind the subquery is a seek on
`(status=? AND kind=? AND run_after<?)` and the first row, with no sort
(pinned in `internal/store/sqlite/plans_test.go`). A claim for several
kinds, or any kind, still works but sorts.

The claim used to be `ORDER BY created_at` over `idx_jobs_dispatch
(status, run_after, created_at)`, which served the `run_after` range but
not the order, so every claim read and sorted all runnable jobs, under the
write lock: 0.43 ms per claim with 5k runnable fetch jobs, 4.94 ms at 50k,
so the claims of a large import cost the square of its size. A claim for a
kind with nothing runnable (the index pool during a fetch backlog) walked
every runnable job of the other kinds first, 3.1 ms at 50k. With
`idx_jobs_claim` every single-kind claim measured 5-6 µs at every size.

Fresh jobs are unaffected by the new order, since insert sets `run_after`
to the insert time. A retried or requeued job is placed by when it came
due, not by when it was first created.

---

## Ollama: native install, not containerized

**Decision:** Curio expects Ollama to run on the host (`brew install ollama`
on macOS, package or systemd on Linux). No `docker-compose.dev.yml`.

**Why:** Docker Desktop on macOS can't expose the Apple Silicon GPU to
containers, so dockered Ollama is materially slower than native. Since
this is a local-first tool and dev/prod for v1 are both single-user host
installs, the native path is plainly better. If hosted deployment ever
needs containerized Ollama, we'll add a compose/Helm chart at that point
with GPU passthrough for the relevant platform.

The daemon connects via `embedding.base_url` in config; default
`http://localhost:11434` works for any native Ollama install on the same
host.

---

## Default fetcher is Go-native, not a Node subprocess

**Decision:** The default `Fetcher` impl is `Native` — Go code using
`codeberg.org/readeck/go-readability/v2` for article extraction and
`github.com/JohannesKaufmann/html-to-markdown/v2` for HTML→Markdown. The
existing `Web2MD` fetcher stays as an opt-in backend
(`fetcher.default: web2md` in config).

**Why:** "Baked in" means users don't think about runtime deps. Requiring
Node + an `npm install` step to use curio is real install friction; a
single Go binary is the standard for tools in this space. The Go
Readability port is mature and produces extraction that's close enough
to Mozilla Readability for our use case.

**What was preserved verbatim from the JS impl:**
- Login-wall heuristics: extracted text < 500 chars, title matching
  "sign in"/"log in"/"join now"/"join linkedin", redirect to a different
  host, or redirect to a `/login`, `/authwall`, `/signin`, `/signup`
  path. Each condition produces a distinct diagnostic reason.
- Jina Reader fallback: same retry policy (4 attempts, exponential
  backoff on 429/5xx), same response parser (`Title:`, `URL Source:`,
  `Published Time:`, `Markdown Content:` block format), same 200-char
  minimum body length to consider the fallback successful.
- User-Agent header matching the JS version.
- `via: readability` vs `via: jina` metadata key.

**Revised:** the thin-content threshold is 500 UTF-8 *bytes*, not
characters, and that is deliberate: about 500 Latin or 170 CJK
characters tracks how much a page says, and counting runes would send
short CJK pages to Jina three times as often. The code now says so
(`minArticleBytes`, `trimmedByteLen`). The redirect checks also moved
first and treat `www.` as the same site; see "Host cache: only host-wide
verdicts, under the host that gave them" and "Login-wall heuristic: www
and apex are the same site".

**Operator note:** to compare extraction quality between the two
backends on a specific URL, point your config at `web2md` and refetch.
We don't yet have an A/B comparison mode but it'd be a natural M2 add.

**Internal abstraction:** we deliberately did NOT add nested interfaces
for the Readability impl or the HTML→Markdown converter. We have one Go
lib for each in production use; abstracting them now would be the
classic "interface with one implementation" trap. The `Fetcher`
interface at the pipeline level is the right place to swap behavior;
the rest is direct lib calls.

---

## Importers: CLI parses, daemon receives lists

**Decision:** Bookmark file parsing happens in the `curio` CLI. The daemon
exposes `POST /v1/bookmarks/import` which accepts an array of
`{url, title, folder_path, tags, saved_at}` objects + a source label,
dedups, and enqueues fetch jobs.

**Why:**
- The daemon never needs filesystem access to user-owned files
  (browser bookmark paths, exports, etc.). Hosted mode falls out free.
- Profile discovery is a per-user concern; the CLI is where user-side
  context lives.
- Easier to test: parser-and-poster vs. file-reading-daemon-endpoint.
- Network payload is bounded: even 10k bookmarks is a few MB JSON, and
  we batch in 500s.

**Implication:** when hosted mode wants browser-side imports without
shipping a CLI, a small JS shim can do the same parsing client-side and
POST to the same endpoint.

---

## HTML export is a first-class importer source

**Decision:** `curio import html <file>` accepts the Netscape Bookmark
File Format that every major browser (and most read-later tools) exports
to. The schema's `source` column gets a new value `html` via migration 002.

**Why:**
- Cross-browser by default — one parser, all browsers via export.
- No "is the browser running?" concern, no TCC permission prompts (Safari).
- Same format as Pocket/Instapaper/Raindrop exports; future read-later
  ingestion comes free.
- User curation: they can edit the file before importing.

Live browser readers (Chrome JSON, future Safari plist, future Firefox
SQLite) are convenience layers on top of this. HTML is the workhorse.

**`ADD_DATE` units:** exporters disagree on the unit — seconds (browsers),
microseconds (Firefox), milliseconds (several read-later tools), even
nanoseconds — so the parser infers it from the magnitude: ≥ 1e17 is
nanoseconds, ≥ 1e14 microseconds, ≥ 1e11 milliseconds, else seconds. Each
threshold is about 1973 in the finer unit and about year 5138 in the coarser
one, so no plausible date is ambiguous. Integers are parsed exactly; decimals
and scientific notation keep their sub-second part. The earlier rule (divide
by 10⁶ above ~9.6e10) turned milliseconds into 1970-01-20 and nanoseconds into
year 55840. Bookmarks already imported with a wrong date are not corrected:
re-import skips existing rows, and there is no data migration.

---

## HTML parser walks recursively, finds <DL> inside <DT>

**Decision:** The Netscape format implies sibling layout
(`<DT><H3>Folder</H3>` then `<DL>...</DL>` as a sibling DT), but HTML5
parsers nest aggressively — they put the `<DL>` *inside* the `<DT>`
containing the preceding `<H3>`, alongside the `<H3>`. The parser walks
pre-order and, when entering a `<DT>`, looks for an `<H3>` child to use
as the folder name for any nested `<DL>`.

**Why:** Discovered the hard way when tests with deeply-nested input
returned zero bookmarks while the sample with one nesting level worked
partially. The sample worked only because the top-level URLs were
sibling-of-folder, which my walker handled by accident.

---

## Worker pool: N workers via the same atomic ClaimNext

**Decision:** `daemon.workers` config (default 4) spawns N goroutines all
calling `Worker.Run` on the same Worker struct. The JobQueue's
`UPDATE ... WHERE id = (SELECT ...) RETURNING` claim already guarantees
one worker per job; tested at 20 jobs / 8 workers under `-race`.

**Why not more?** Embedding throughput is usually the bottleneck. Ollama
on Metal handles ~4 concurrent embed requests well; cranking workers
higher just makes them sit in `Embed`. If the user runs on GPU-heavy
hardware or switches to a cloud embedder, raising the config is one
edit.

**Revised:** the single pool was split into `daemon.fetch_workers`
(default 16, network-bound) and `daemon.index_workers` (default 4,
Ollama-bound) after FIFO claiming let fetches starve indexing, plus a
one-worker cluster pool. `daemon.workers` survives only as a deprecated
alias: see "Config: strict keys, legacy `workers` folded in at load" below.

---

## Marker file's schema_version is synced from the DB after migrations

**Decision:** goose's `goose_db_version` table is the only record of the
schema version. `sqlite.Migrate` returns the version goose leaves the
database at (`Provider.GetDBVersion` after `Up`), and the daemon writes it
into `.curio-meta.json` so `/v1/healthz`, `curio version` and
`curio doctor` show it. The marker's `schema_version` is a cache;
`curiohome.CurrentSchemaVersion` is only the placeholder `Init` writes
before the daemon's first start.

**Why:** The marker is written at `curiohome.Init` time with a constant,
so after migration 002 ran, `curio status` still reported "schema: v1";
the daemon has to refresh it from the database.

It used to refresh it from `schema_meta.schema_version`, a copy every
migration had to bump by hand, next to goose's own record of the same
number. A migration that forgot the bump would have made every display
wrong. Migration 005 drops the column (its Down restores it at 4, so the
older Downs that set it still run), and no migration records the version
any more. Goose versions 1 through 4 equal the old `schema_version`
values, so existing installs continue without a jump. The marker field
stays because offline commands read it and the healthz response shape
must not change.

Returning the version from `Migrate` reads it on the same Provider after
`Up`. A separate read function would have to be called on a database
that may not be migrated yet, where `GetDBVersion` creates goose's table
as a side effect.

---

## Chunker enforces a 3500-char hard cap (not just 384 words)

**Decision:** `ChunkOptions.SizeChars` (default 3500) is a hard upper bound
on chunk byte length applied AFTER the word-count chunking pass. Chunks
that exceed the limit are split at word boundaries with a small overlap.
A single whitespace-free token longer than the cap (a long URL, a hex
blob, or a CJK paragraph, which has no spaces at all) is split into
consecutive pieces on rune boundaries — never truncated. Truncating it
used to drop everything past the cut (about 60% of a 9000-byte Chinese
paragraph never reached BM25 or the vector index) and cut multi-byte
runes in half, producing invalid UTF-8.

**Headings stay with their section:** the paragraph splitter prefixes a
markdown heading to the paragraph that follows it (consecutive headings
all join it; a trailing heading stands alone), so the word packer can't
leave a heading at the tail of the previous chunk, apart from the text it
names. Both changes apply to documents as they are next indexed; run
`curio reindex --all` to re-chunk the existing corpus.

**Why:** Word count is a bad proxy for BPE token count on URL- or
code-heavy content. A single URL like
`https://ontariocourtforms.on.ca/static/media/uploads/courtforms/scc/01a/scr-1a-jan21-en-fil.docx`
counts as one whitespace-word but tokenizes into 30+ BPE pieces.
During the first real import, an Ontario court forms page with ~6 URLs
per KB blew past the 2048-token model context even with 384-word
chunks. The 6000-char first attempt was still too loose for URL-dense
content. 3500 keeps even worst-case content under the limit:

| Content type      | 3500 chars ≈ tokens | Safe under 2048? |
|---|---|---|
| Plain prose       | ~875                | Yes (huge margin) |
| Code-heavy        | ~1200               | Yes |
| URL-heavy markdown | ~1500-1800         | Yes |
| Pathological (all URLs) | ~2000          | Borderline; rare |

**Hard model context:** Ollama's `nomic-embed-text` clamps at 2048 tokens
regardless of the modelfile's `PARAMETER num_ctx 8192` — the GGUF model
metadata declares `nomic-bert.context_length: 2048`. Verified by reading
`/api/show`. The `num_ctx` option we send is therefore advisory only;
the char cap is what actually saves us.

**Real tokenizer later:** counting BPE tokens directly (via
huggingface-tokenizers or similar) would let us pack closer to the
limit and produce fewer, more semantic chunks. Defer until quality
issues from the conservative byte heuristic surface.

---

## Embedder passes num_ctx=8192 to Ollama, chunker defaults to 384 words

**Decision:** The Ollama embedder sets `options.num_ctx = 8192` on every
`/api/embed` request (configurable via `embedding.num_ctx` later). The
chunker's default `size_tokens` drops from 512 → 384 words with a
proportionally smaller overlap (48 instead of 64).

**Why:** During the first real import we saw Ollama return
`HTTP 400: input length exceeds the context length` on ~30% of index
jobs. Two factors compounded:

1. Ollama defaults `num_ctx` to 2048 even for models like
   `nomic-embed-text` whose declared context is 8192.
2. The chunker counts whitespace-words, not BPE tokens. For prose,
   words≈tokens, but dense markdown (long URLs, code blocks, dense
   tables) tokenizes 2-5x. A 512-word chunk with many URLs could be
   well over 2048 BPE tokens.

Setting `num_ctx=8192` gives us the model's full window. Dropping to
384 words gives a safety margin even for the worst content
(384 × 5 = 1920 tokens, still under 2048; comfortably under 8192).

**Real tokenizer later:** the proper fix is to count BPE tokens before
chunking. Costs a tokenizer dep (e.g., tiktoken-go or sugarme/tokenizer)
and adds latency. Not worth it until we see chunk-quality issues from
the conservative word-based heuristic.

**Later finding:** nomic-embed-text's real ceiling is 2048 tokens (its GGUF
`context_length`), and Ollama clamps `num_ctx` to it, so the 8192 we send
is advisory. The 3500-byte chunk cap (entry above) is what bounds inputs.

---

## Indexer: embed in batches of 32; `embedding.timeout_seconds`

**Decision:** `indexer.Index` embeds a document's chunks in consecutive
`/api/embed` requests of at most 32 chunks (≤ 32 × 3500 B ≈ 112 KB each), in
order, and writes nothing until every batch has succeeded. The embedder's
per-request timeout is configurable as `embedding.timeout_seconds` (default
60, previously a hard-coded 60 s).

**Why:** one request per document let a long document — a book-length PDF, a
long docs page, or a moderate one queued inside Ollama behind the other index
workers' requests (`daemon.index_workers` = 4) — run past the fixed timeout.
The job then retried from scratch up to five times and ended `failed`: never
searchable. A 32-chunk request takes a few seconds even on CPU-only Ollama, so
a timeout now means Ollama is in trouble, not that the document is long. It
also stops one huge request from holding search's query embedding behind it.

**Where it lives:** batching is orchestration policy, so it's in the indexer
(`Options.EmbedBatchSize`, not a config key), independent of the embedder
client. The job-level retry is still the recovery mechanism; batching bounds
how much work each attempt repeats. A failure names the chunk range
("embed chunks 64-95 of 180"), and because nothing is written until the end,
the document's previous chunks stay searchable.

---

## Document state follows job outcome via OnPermanentFailure hook

**Decision:** Documents have their own state machine
(`pending → fetched | failed`) and the failed transition is driven by the
worker's `OnPermanentFailure` hook firing after a fetch or index job hits
`MaxAttempts`. `JobQueue.MarkFailed` returns `(permanent bool, error)` so
the worker can distinguish retry-going-back-to-pending from
terminal-failed.

**Why:** Before this, jobs went to `failed` cleanly but docs stayed
`pending` forever once their job gave up. `curio status` couldn't
distinguish "still in flight" from "permanently broken." Now the doc
state mirrors job outcome:

- `pending`: just created OR has an in-flight job
- `fetched`: fetch + index both succeeded
- `failed`: a fetch or index job for this doc permanently failed

`refetch` flips a doc back to `pending` and enqueues a new fetch job.
The old failed job stays in the history table as audit; the new run is
a fresh attempts counter.

`dead` is reserved in the constants but unused. Eventually it'd mean
"don't even allow refetch" — saved for when we have a retention or
give-up-permanently policy.

---

## Fallback strategy: only Jina for content-came-back cases

**Decision:** The Native fetcher's pass-2 Jina fallback fires only when
the original error wraps `ErrLoginWall` or `ErrAntiBot`. For 404, DNS
failures, timeouts, and other 4xx/5xx-other, we return the original
error directly.

**Why:** The first real import showed 88% of Jina fallbacks were on URLs
Jina couldn't fix either (mostly dead links and DNS failures). The
fallback was burning rate-limit budget on hopeless URLs and getting us
429'd on the calls that *would* have benefited. The classification:

- `ErrLoginWall` — readability got real content but it was thin /
  paywalled / login-walled. Jina's infrastructure often beats this.
- `ErrAntiBot` — origin returned HTTP 403 or 503; commonly Cloudflare /
  WAF bot blocks. Jina's residential infra often gets through.
- Everything else — page truly missing, network broken, or origin
  reachable but rejecting for non-bot reasons. Jina won't help.

After this fix the Jina fallback rate dropped ~10×.

---

## Anti-bot mitigation: browser-thorough headers, not just User-Agent

**Decision:** The Native fetcher sends a Chrome-like header set covering
`Sec-Fetch-*`, `Sec-Ch-Ua-*`, `Upgrade-Insecure-Requests`, and a richer
`Accept` value — not just the UA string.

**Why:** CDNs like Cloudflare cross-check the UA against several other
headers; a Chrome UA without `Sec-Fetch-User`, `Sec-Ch-Ua-Platform`, etc.
is a known automation fingerprint and triggers 403/503. Won't beat JS
challenges or TLS-fingerprint checks, but eliminates the cheapest
blocks. Sites that were 403-ing for us in the first import (inc.com
style) start returning 200 with this change.

If we need more, the next escalation is the TLS/HTTP2 fingerprint backend
(see below), and beyond that a headless-Chrome fetcher (via chromedp) for
JS challenges — deferred until the cheap fixes stop being enough.

---

## Anti-bot mitigation: pluggable TLS/HTTP2 fingerprint backend (uTLS)

**Decision:** The Native fetcher issues requests through a `roundTripper`
abstraction (`internal/fetcher/transport.go`) with two backends, selected
by `fetcher.native.backend`:

- `chrome` (default) — uTLS + a forked HTTP/2 stack
  (`github.com/bogdanfinn/tls-client`, which wraps `bogdanfinn/utls` and
  `bogdanfinn/fhttp`). Parrots a real Chrome TLS ClientHello and HTTP/2
  SETTINGS/pseudo-header order. `chrome_120|124|131|133` pin a profile;
  default is the latest (`Chrome_133`).
- `stock` — Go's `net/http`. Zero extra network behavior to reason about,
  but trivially fingerprinted as a bot.

**Why:** The browser-thorough headers above only address the L0 (header)
layer. Go's `crypto/tls` emits a *fixed* ClientHello — no GREASE,
distinctive cipher/extension ordering — that Cloudflare/Akamai/DataDome
JA3/JA4-blocklist on sight, and its HTTP/2 stack has an equally
distinctive Akamai fingerprint. Both are checked *before* a single header
byte is read, so no amount of header spoofing helps. uTLS + the forked h2
stack make the whole network footprint Chrome's.

Measured against a reflector (`tls.peet.ws`), the two backends are night
and day:

| | JA4 | Akamai h2 (pseudo-header order) | GREASE |
|---|---|---|---|
| `chrome` | `t13d1516h2_8daaf6152771_…` | `…\|m,a,s,p` (Chrome) | yes |
| `stock` | `t13d1312h2_f57a46bbacb6_…` | `…\|a,m,p,s` (Go) | no |

**Design notes:**
- `tryReadability` / `tryJina` are backend-agnostic — they build an ordered
  `[]header` and call `rt.do`. `fhttp.Header.Add` records header order as
  it goes, so the chrome backend reproduces Chrome's header order;
  pseudo-header order and H2 SETTINGS come from the profile.
- `NewNative` never errors: if the chrome backend fails to initialize it
  logs and degrades to `stock`. That's the "fallback if the dep is
  unavailable" path.
- The default UA and `sec-ch-ua` were bumped to Chrome 133 to stay
  coherent with the default profile — a JA3 that says 133 paired with a UA
  that says something else is itself a tell. **Override `backend` and
  `user_agent` together.** (Revised: both headers now come from the
  selected profile; see "Chrome profiles carry their own User-Agent and
  sec-ch-ua" below.)
- This also fixed a latent `net/http` gotcha: setting `Accept-Encoding` by
  hand *disables* net/http's transparent gzip (it only decompresses when
  the transport added the header). The `stock` backend now omits
  `Accept-Encoding` (auto-gzip); the `chrome` backend sends a faithful
  `gzip, deflate, br, zstd`.
- **Decompression is NOT uniform across protocols in fhttp.** Its h2 and h1
  paths auto-decompress by `Content-Encoding` (gzip/br/zstd), but its
  **HTTP/3 (QUIC)** path does not — and the Chrome profile negotiates h3
  with CDNs that advertise it (e.g. MDN/Cloudflare). The first cut shipped
  binary garbage to disk for those pages. Fix: `chromeRT.do` decompresses
  defensively when the transport left it compressed (`!resp.Uncompressed &&
  Content-Encoding != ""`); on h2/h1-gzip `Uncompressed` is already true so
  there's no double-decompress. Regression coverage: the live test
  `TestNative_LiveSites_RenderMarkdown` (build tag `integration`) fetches a
  real h3 site and asserts readable markdown — httptest can't reproduce it
  because it's h1/h2 only.

**Limits / next escalations:** still won't beat JS challenges
(Turnstile/managed challenge), canvas/behavioral fingerprinting, or IP
reputation. Those need a headless browser (chromedp) and/or residential
proxies respectively — both deferred. The Jina fallback (above) still
mops up the stubborn `ErrAntiBot` / `ErrLoginWall` tail.

**Cost:** pulls in `bogdanfinn/{tls-client,fhttp,utls}` plus brotli, circl,
and a quic-utls dep. Pinned at `tls-client v1.11.0`. Acceptable for the
block-rate win; revisit if it bloats build time or the dep goes stale.

---

## PDF fetcher: two-tier, pure-Go local then Jina

**Decision:** The Native fetcher detects PDFs by Content-Type
(`application/pdf`, or a `.pdf` URL when the server is vague) and handles
them in two tiers: **(1)** pure-Go local extraction on the downloaded bytes
(`github.com/ledongthuc/pdf`); **(2)** if that yields too little text,
errors, or panics, fall back to **Jina** (which renders PDFs server-side).
Other non-HTML, non-PDF content (images, octet-stream) is a permanent
failure — see the content-type guard above.

**Why pure-Go for tier 1 (not pdftotext/MuPDF/UniDoc):** keeping curio a
single binary with no runtime deps is the whole reason we left the Node
`web2md` behind — a `pdftotext`/poppler subprocess would reintroduce that
friction. The two genuinely high-quality embeddable options, `go-fitz`
(MuPDF, cgo) and `unidoc/unipdf` (pure Go), are both **AGPL-or-commercial** —
a problem for a Homebrew-distributed binary. `ledongthuc/pdf` (BSD-3,
descended from `rsc.io/pdf`) is mediocre but permissive and dependency-free.

**Why that mediocrity is acceptable:** tier 2 is the quality backstop. Jina
uses a high-quality server-side extractor, so tier 1 only needs to cheaply
nail the easy PDFs locally (avoiding a network round-trip + Jina rate
limit); anything it botches falls back. The arXiv "Attention Is All You
Need" PDF extracts cleanly via tier 1 (~33k chars); messier PDFs route to
Jina. If local quality ever matters more, an optional `pdftotext` tier
(used only when poppler is present) is the cleanest upgrade — no hard dep,
no AGPL.

**Robustness:** `ledongthuc` can panic on malformed input — recovered into
an error so the caller falls back. A `minPDFChars` floor catches the
"parsed but produced garbage" case. PDFs over 32 MiB skip local extraction
(memory) and go straight to Jina.

**Gotcha (cost me a retry loop):** `Result.ContentType` must be one of the
values the `documents` CHECK constraint allows —
`article | repo | video | pdf | thread | unknown`. PDFs use `pdf`. An
out-of-set value (I first used `"document"`) fails the DB write *after* a
successful fetch+extract, and that write error is retryable — so it loops
silently re-fetching. The fetcher's content-type vocabulary is coupled to
the migration's CHECK; keep them in sync.

---

## CLI defaults: happy-path views; debug paths are opt-in

**Decision:** `curio docs` defaults to `state=fetched`, `curio jobs`
defaults to `status=done`. `--failed` is a shortcut for the debug view;
`--all` shows everything. Explicit `--state` / `--status` flags always
win.

**Why:** Once a corpus has a few thousand bookmarks, the table is
dominated by audit rows (every fetch + every index becomes a row,
mostly `done`). Showing all of them by default buries the signal in
noise. The "happy-path corpus view" is what users want most often; the
debug view is opt-in.

Precedence: `--status`/`--state` > `--failed` > `--all` > default
(`fetched` / `done`). The explicit flag always wins so scripts that
already passed `--state=""` continue working.

---

## Jobs lifecycle: prune/delete, no nuke-all path

**Decision:** Two operations for managing the jobs table:

- `curio jobs prune --older-than 30d` — time-based retention; deletes
  finished (`done` / `failed`) jobs whose `updated_at` is older than the
  duration. Accepts Go duration syntax plus `Nd` for days.
- `curio jobs delete --status failed` — exact-status delete; status is
  required and must be `done` or `failed`.

Both only ever remove **finished** jobs. A `pending` or `running` job is
work in flight: deleting it leaves its document in `pending` with no job
and no permanent-failure hook to move it on — indistinguishable from real
progress, forever — and a running job's later `MarkDone` finds no row.
`DELETE /v1/jobs?status=pending|running` is a 400. Cancelling queued work,
if it's ever wanted, is a separate feature that also settles the document.

There is deliberately **no "delete all jobs"** path. If that's what's
wanted, `rm ~/.curio/curio.db` is faster and more explicit.

**Why:** The jobs table accumulates monotonically: every fetch + index
attempt leaves a row, retries multiply it. After a real corpus import
you'll have 10k+ rows where 99% are stale `done` entries. Without a
prune story the audit trail eventually crowds out the debugging signal.
Splitting into two commands (one time-based, one status-based) keeps
the intent visible at the CLI; a unified `--all` flag would be too easy
to misuse.

**Important:** Deleting jobs does NOT change document state. A failed
doc stays failed (still visible in `curio docs --failed`, still
refetchable). The jobs table is audit/history; doc state is current
truth.

---

## Safari importer: skip Reading List, require Full Disk Access

**Decision:** The Safari parser reads `~/Library/Safari/Bookmarks.plist`
(binary or XML plist via `howett.net/plist`), walks the bookmark tree,
and emits `ParsedBookmark`s. Reading List entries
(`com.apple.ReadingList`) are excluded. The CLI surfaces a clear error
with remediation when macOS denies access due to TCC restrictions.

**Why:**
- Reading List is ephemeral by design — items are saved temporarily for
  offline reading, not curated bookmarks. Including them would inflate
  the corpus with transient content the user may have already dismissed.
- macOS requires Full Disk Access for any process reading Safari data.
  Rather than silently failing or producing a confusing `os.Open` error,
  the CLI detects permission errors and tells the user exactly which
  System Settings pane to visit.

**Structure mirrors Chrome:** `SafariBookmarksPath()` auto-discovers the
plist (with `CURIO_SAFARI_DIR` env override for tests), `ParseSafari()`
accepts an `io.ReadSeeker`, folder hierarchy is preserved in
`FolderPath`. Root folders are labeled "Favorites" (BookmarksBar) and
"Bookmarks Menu" rather than their internal identifiers. A bookmark saved
directly under the root (not in any folder) is imported with an empty
`FolderPath`; the parser used to treat every root child as a folder and
silently drop these. Bookmarks already imported are not updated (re-import
skips existing rows), but the ones that were dropped are new, so re-running
`curio import safari` adds them.

---

## Firefox importer: copy the live places.sqlite, prefer the install default

**Decision:** The Firefox parser reads `places.sqlite` (a SQLite DB, not a
flat file). It **copies the DB plus its `-wal`/`-shm` sidecars to a temp
dir and reads the copy**, walks `moz_bookmarks` (joined to `moz_places`),
skips the Tags subtree and separators, and labels root containers by GUID.
Profile discovery prefers the **`[Install*]` default** in `profiles.ini`.

**Why copy (incl. the WAL):**
- Firefox holds `places.sqlite` open in WAL mode while running. Opening it
  directly fights for locks; opening with `immutable=1` avoids locks but
  **ignores the WAL** — so a bookmark added seconds ago (still in the
  `-wal`, not yet checkpointed) would be invisible. Copying the main file
  *and* the sidecars lets SQLite replay the WAL on the copy, so recent
  writes are seen and the user doesn't have to quit Firefox. The 2.4 MB WAL
  observed on a fresh profile confirmed this isn't theoretical.

**Why the `[Install*]` default:**
- Post-67 Firefox is per-install. `profiles.ini` can list several profiles
  (`default`, `default-release`) and a legacy `[Profile*] Default=1` that
  is NOT what the running browser uses. The authoritative choice is the
  `[Install<hash>]` section's `Default=`. We prefer it, then fall back to
  `Default=1`, then the first profile.

**Schema notes:** `moz_bookmarks.type` 1 = bookmark, 2 = folder, 3 =
separator. `dateAdded` is **microseconds since the Unix epoch** (unlike
Chrome's 1601 epoch). The Tags root (`tags________`) contains tag
pseudo-bookmarks, not real folders — its subtree is not emitted, so tagged
URLs don't double-count. But its tag *names* are kept: each folder directly
under the Tags root is a tag, holding one row per tagged place, so the parser
maps place id (`moz_bookmarks.fk`) → tags and attaches them, sorted and
de-duplicated, to every real bookmark of that place. They then flow into
`bookmarks.tags` and the search index like HTML-export `TAGS=`; before, Firefox
users lost them entirely. Bookmarks imported earlier don't gain their tags:
re-import skips existing rows, and updating them is out of scope. Root GUIDs
map to friendly labels ("Bookmarks Menu", "Bookmarks Toolbar", "Other
Bookmarks", "Mobile Bookmarks").

**Shape deviation:** `ParseFirefox` takes a *path*, not an `io.Reader` like
the other parsers — SQLite needs a real file to open. The CLI passes the
discovered (or `--file`) path straight through. This is the only importer
that links the sqlite driver (already in the binary via the store).

---

## Jobs list: sort by updated_at, show timestamp

**Decision:** `ListWithDoc` sorts by `j.updated_at DESC` (not
`created_at DESC`). The CLI prints the `updated_at` timestamp on every
job row.

**Why:** For terminal-status jobs (done, failed), `updated_at` is when
the job actually completed or failed — the timestamp the user cares
about when triaging. `created_at` is when the job was enqueued, which
can be minutes or hours earlier for large imports. Sorting by
`updated_at` puts the most recently resolved jobs first regardless of
when they were originally queued.

**Trade-off:** for pending jobs, `updated_at` and `created_at` are
usually identical (or nearly so), so sorting by either produces the same
order. No status-conditional ORDER BY needed.

---

## `curio status`: CLI version, daemon version, disk usage

**Decision:** `curio status` shows the CLI binary's version (from
`internal/version`, stamped at build time) independently of the daemon's
version (from `/v1/healthz`). It also shows disk usage: database size,
WAL size, content directory size + file count, and logs directory size.

**Why:**
- CLI and daemon can be different versions if the user rebuilt one but
  not the other, or if a release upgraded the CLI first. Showing both
  makes version drift visible immediately.
- Disk usage gives the user a feel for corpus growth without running
  `du -sh` manually. The database, WAL, and content directory are the
  three things that grow with import volume; surfacing them in status
  makes "how big is my curio" a zero-effort question.

**Formatting:** map breakdowns (documents by state, jobs by status) are
rendered as `key=val  key=val` sorted alphabetically, not Go's default
`map[...]` representation.

---

## CLI hides `next_attempt` for terminal-status jobs

**Decision:** `curio jobs` only prints the `next attempt:` line when
status is `pending` or `running`. For `done` / `failed`, the row is
omitted.

**Why:** `MarkFailed` updates `status` and `last_error` when the job
hits terminal-failed but leaves `run_after` alone. The stored value is
"the time the next retry *would have* happened" — but it'll never fire
because the status is now terminal. Displaying it as "next attempt" on
a failed row is misleading. Also: timestamps now include the timezone
abbreviation so `11:54 EDT` is unambiguous.

---

## What's deferred from the v1 API

These are intentionally omitted from `api/openapi.yaml`. Each is additive when
it lands — no `/v1` → `/v2` bump required.

- **Insight layer endpoints** — `/v1/interests` (+ `/{id}` and `/rebuild`)
  ✅ landed in M4 (see "Insight layer: kNN-graph clustering + labeled
  interests" below). `/v1/suggestions` arrives with M5; a separate
  `/v1/clusters` was folded into `/v1/interests` — an interest *is* a
  labeled cluster.
- **Config endpoints** (`GET/PUT /v1/config`) — for now, edit
  `~/.curio/config.yaml` and restart the daemon (`curio daemon stop`; the
  next command auto-starts it). The daemon has no reload path: SIGHUP, like
  SIGTERM, shuts it down cleanly.
- **Admin reindex endpoint** (`POST /v1/admin/reindex`) — added when an
  embedding model swap is an actual need. Until then, run `curio reindex` CLI.
- **Server-Sent Events for job progress** — polling is fine for v1. If the
  CLI's progress UX gets ugly, add `/v1/jobs/{id}/stream`.
- **Generic batch endpoints** for bookmarks, documents, or jobs. Add only
  when a real non-file source needs them.
- **Authentication scheme** (API keys, OAuth, SSO) — deferred to
  hosted-mode work. The local daemon has no credentials at all; it binds
  loopback only and refuses browser-originated requests. See "Local API:
  loopback only, no token, browsers shut out" below for the threat model.
- **WebSocket or streaming search** — current `POST /v1/search` is fine.

---

## What's not decided yet

- **Insight layer specifics:** clustering algorithm and labeling ✅ decided
  in M4 — kNN-graph clustering + term/LLM labels (see the entry below). Still
  open: trajectory analysis ("new this month"), cross-cluster interest
  merging, and a standalone `interests` table, deferred until there's real
  usage data.
- **Authentication for hosted mode:** the scheme (API keys vs OAuth vs SSO)
  is deferred. Nothing is stubbed in the local daemon, which trusts every
  local process that can reach loopback (see "Local API: loopback only, no
  token, browsers shut out").
- **Re-crawl policy:** how often to refetch a given URL. Likely
  domain-rule-driven (news daily, docs monthly, static essays never).
- **Highlight / read-later importers:** schema is ready; importer code is not
  in v1.
- ~~**"Page Not Found" detection**~~ ✅ implemented — see the
  "Dead-link detection: hard 404/410 + soft-404 heuristics" entry
  below (title patterns + redirect-to-homepage; dead docs go to
  state `dead`). The embedding-based "this isn't really an article"
  classifier remains a possible future refinement.

- **Natural-language search is provisional.** Current BM25 sanitization
  (OR + small stopword list — see decision above) is the production
  default of mid-2010s search engines, not the leading edge. We should
  revisit when retrieval quality starts feeling weak or when corpus
  size makes the noise from pure-OR matching surface. Options in rough
  order of effort:

    1. **Stemming + `minimum_should_match` post-filter.** Add a Porter
       or Snowball stemmer to the tokenizer side AND require ~50-75% of
       non-stopword tokens to match (FTS5 doesn't support this natively,
       so we'd post-filter in Go). Cheap; modest recall + precision
       boost.

    2. **LLM query rewriting via Ollama.** Send the natural-language
       query to a small local model with a system prompt like "extract
       3-7 keyword phrases from this query." Use those for BM25 (vector
       still uses the original). This is the "Perplexity / You.com"
       pattern. Adds ~200-1000ms per query; quality jump can be big.
       We already have Ollama running so the infrastructure cost is
       zero. Right move if a search-quality eval shows BM25 is dragging
       the hybrid score down.

    3. **Learned sparse retrieval (SPLADE / ColBERT).** Replace BM25
       entirely with a transformer-produced sparse vector indexed in an
       inverted index. This is what Vespa, Qdrant, Weaviate are pushing
       as "the next BM25." Best-in-class for natural-language queries,
       but requires deploying another model, embedding every chunk at
       index time, and embedding queries at search time. Massive
       complexity jump for what's still a single-user local system.
       Only worth it if curio outgrows hobby scale.

  **Prerequisite for any of these:** a tiny eval harness — 10-20
  representative queries with expected docs, scored on NDCG@10 or
  recall@10. Without it we'll be guessing about whether each change
  actually moved retrieval quality. Build the eval BEFORE the
  improvement.

  ✅ The eval harness now exists — `curio eval --queries <qrels.yaml>`
  (`internal/eval`: recall@k / precision@k / NDCG@k / MRR). The SOTA
  NL-search work itself is scheduled as **M6**, alongside RAG — see
  "M6 (planned): RAG / Q&A synthesis + SOTA natural-language search" below.

  **What's NOT under consideration:** building our own tokenizer,
  custom synonym dictionaries, query-classification pipelines. The
  hybrid retriever + RRF was chosen specifically to keep retrieval
  simple; any "smartness" should live in the query-rewriting layer
  above the retriever, not inside it.

---

## PatternDispatcher: host-based fetcher routing

**Decision:** Replace the M0 `Single` dispatcher with
`PatternDispatcher` — a list of `Rule{Hosts, Fetcher}` checked
top-to-bottom, with a fallback to the default (Native) fetcher.

**Why:** M2 introduces YouTube and GitHub fetchers that only make
sense for their respective hostnames. A code-based dispatcher is
simpler than the roadmap's `fetcher_rules.yaml` while there are
only 2-3 content-type-specific fetchers. The YAML rules file adds
parsing, hot-reload, and a config schema — worthy work but not
needed until there are enough fetchers to justify user-facing config.

**Wiring:** The daemon always registers GitHub (pure Go, no external
dep). YouTube is registered conditionally on `exec.LookPath("yt-dlp")`.
Unmatched URLs fall through to Native.

**Superseded** by `fetcher_rules.yaml` (see "fetcher_rules.yaml:
mtime-polled hot reload, keep-last-good"). `RulesDispatcher` does the
routing, the same host lists are its built-in defaults, and
`PatternDispatcher` has been deleted.

---

## YouTube fetcher: yt-dlp over API/scraping

**Decision:** Shell out to `yt-dlp` for YouTube metadata and
transcript extraction. No YouTube API key required.

**Why:**
- YouTube Data API v3's `captions.download` requires OAuth 2.0 and
  video ownership — useless for indexing third-party bookmarked
  videos.
- `yt-dlp` handles both manual and auto-generated captions, manages
  YouTube's anti-bot measures, and is maintained by 400+ contributors.
- Same shell-out pattern as Web2MD, but yt-dlp is a single binary
  (no Node runtime), so install friction is lower.
- Innertube API (undocumented, used by `youtube-transcript-api` in
  Python) deferred — no Go library, maintenance burden of tracking
  YouTube's internal API changes.

**`--write-info-json` not `--dump-json`:** `--dump-json` suppresses
all file downloads including subtitles. Discovered during first real
test — metadata came back but no VTT file was written. Switched to
`--write-info-json` which writes JSON to disk alongside the subtitle
files.

**VTT parsing:** Inline (~60 lines) rather than a dependency.
Strips timestamps, `<c>` tags, cue IDs; deduplicates rolling-window
lines from auto-captions; groups into paragraphs by line count.

**No inline timestamps in markdown:** Timestamps waste BPE tokens
without adding semantic value for embedding/search. Stored in the
Meta map if needed later (e.g., deep-linking into videos).

**Transcript fallback chain:** manual English → auto English → any
language → description-only (`status=partial`). When no yt-dlp is
installed, YouTube URLs fall through to Native (extracts whatever
the page HTML yields).

**Revised:** the chain above overstated what was built. The file was
picked by shortest name and nearly always labeled `manual`, and nothing
ever set `status=partial`. What is implemented now: among the languages
in `fetcher.youtube.sub_langs`, uploaded captions before automatic ones
(both kinds are written as `<id>.<lang>.vtt`, so a track counts as
uploaded when info.json's `subtitles` lists its language), then the
shortest language tag. With no usable track the document is the
description alone: `Result.Partial` is set, `transcript_source` is
`none`, and the extraction is stored with status `partial`. There is no
"any language" step: it would take a second yt-dlp run per video, and
more requests to YouTube, for little gain.

**yt-dlp stderr handling:** On failure (`cmd.Run` returns error),
extract only `ERROR:` lines from stderr. Ignore `WARNING:` lines
(e.g., "ffmpeg not found", impersonation warnings) that are noisy
but harmless.

---

## GitHub fetcher: REST API, no clone

**Decision:** Fetch GitHub repos via the REST API (`/repos/{owner}/{repo}`
for metadata, `/repos/{owner}/{repo}/readme` for README content). No
`git clone`, no GraphQL.

**Why:** For bookmarked repos, what you want to recall is *what the
project is and why you saved it*, not grep through source code. The
README is the primary content (it's what you read when you bookmarked
it). Repo metadata (description, topics, stars, language, license)
provides rich search signals without storing megabytes of source per
repo.

**File URLs** (`github.com/owner/repo/blob/main/docs/arch.md`):
fetch the specific file as primary content, plus repo metadata in
the markdown header for context. The ref (branch/tag) is extracted
from the URL path.

**Auth:** Optional `CURIO_GITHUB_TOKEN` env var or config field.
Without a token: 60 req/hr (anonymous). With a fine-grained PAT
(no scopes needed): 5,000 req/hr. The token is resolved at startup:
config value → env var → empty.

**What's not supported yet:** issues, PRs, wiki pages, gist URLs,
org-level pages (`github.com/bitwarden`). These return a
`PermanentError` with a clear message. Future work can add handlers
or fall through to Native.

**Native fetcher produced garbage for GitHub:** Verified on
`perplexityai/bumblebee` — the Native fetcher got binary/encoded
data from GitHub's page (likely compressed response). This was the
motivating failure for the dedicated fetcher.

---

## Per-fetcher rate limiting

**Decision:** Two layers of rate limiting for API-backed fetchers.

1. **`RateLimited` wrapper** (`internal/fetcher/fetcher.go`): a
   generic `Fetcher` decorator using `golang.org/x/time/rate` token
   bucket. Applied to YouTube (2 req/s, burst 3 — limits concurrent
   yt-dlp starts).

2. **Internal `apiGet` limiter** (GitHub fetcher): 1.5 API calls/s,
   burst 1. Applied at the individual HTTP call level, not the
   Fetch level, because each GitHub fetch makes 2 API calls (repo
   metadata + README). An outer-only limiter under-counts by 2×.

**Why two layers:** The outer wrapper is generic and works for any
fetcher. But GitHub's abuse detection counts individual API calls,
not logical "fetch a repo" operations. The first real bulk refetch
(251 repos, 16 workers) hit GitHub's secondary rate limit (~100
req/min) despite the outer limiter pacing fetches at 1.5/s — because
each fetch was 2 calls, the actual API rate was 3/s.

**Retry-After support** (GitHub): When the API returns HTTP 429 or
403-with-rate-limit, the fetcher parses the `Retry-After` header
(or falls back to `X-RateLimit-Reset` epoch) and sleeps internally
before retrying — up to 3 attempts per API call. This prevents
transient rate limits from burning through the job system's 5-attempt
retry budget, where the exponential backoff (2^N seconds, max 32s)
is too short for GitHub's 60-second rate limit windows.

**Revised:** the queue backoff above was misdescribed; `MarkFailed` waits
30·2^attempts seconds (60s after the first attempt), capped at 1 hour.
Only the primary limit (`X-RateLimit-Remaining: 0`) was treated as a rate
limit, so the secondary-limit 403s this entry was written about failed
permanently, and one call's back-off didn't pause the other workers. See
"GitHub: secondary rate limits and a shared cooldown" below. The YouTube
token bucket limits how fast yt-dlp processes start, not how many run;
see "Subprocess fetchers: kill the process group, cap the output".

---

## YouTube URL normalization

**Decision:** `Normalize()` canonicalizes all YouTube URL variants
to `https://www.youtube.com/watch?v=<ID>`, stripping all other
query parameters.

**Variants collapsed:** `youtu.be/ID`, `m.youtube.com/watch?v=ID`,
`youtube.com/shorts/ID`, `youtube.com/live/ID`,
`youtube.com/embed/ID`, and any URL with tracking params (`si`,
`list`, `index`, `t`, `pp`, `feature`, `ab_channel`).

**Why:** A single video can be bookmarked via many URL forms
(mobile, short link, embedded in a playlist, with share tracking).
Without canonicalization, the same video creates multiple documents
that compete in search results. The `YouTubeVideoID` helper is
shared between the normalizer and the fetcher.

**Playlist-only URLs** (`youtube.com/playlist?list=...`) are not
canonicalized — they don't have a video ID and are rejected by the
YouTube fetcher with a `PermanentError`.

**Revised:** only IDs matching `^[A-Za-z0-9_-]+$` are canonicalized; the
ID used to be pasted into the query unescaped. See "URL normalization:
fetch-equivalent and idempotent" below.

---

## GitHub issues, PRs, and wiki pages

**Decision:** The GitHub fetcher handles three more URL shapes.
`/owner/repo/issues/N` and `/owner/repo/pull/N` fetch via the REST
API (issue/PR metadata + conversation comments) and store as
content_type `thread` — the enum value that was allocated-but-unused
since M0, so no migration. `/owner/repo/wiki[/Page]` fetches the raw
page from `raw.githubusercontent.com/wiki/o/r/Page.md` and stores as
`article`.

**Mechanics worth remembering:**

- Web URLs say `/pull/456` (singular); the REST path is
  `/repos/o/r/pulls/456` (plural). The API path is built explicitly,
  never echoed from the URL.
- `GET /repos/o/r/issues/N` returns PRs too (a PR *is* an issue); a
  `pull_request` key marks them, and `fetchIssue` delegates to
  `fetchPull` in that case — mirroring GitHub's own browser redirect.
- Conversation comments for BOTH issues and PRs live at
  `/repos/o/r/issues/N/comments`. `/repos/o/r/pulls/N/comments` is
  review (diff) comments — deliberately not fetched.
- Comments are capped at one page of 100 (`maxIssueComments`) to keep
  the per-fetch API call count at 2 — the apiGet limiter paces
  individual calls (see "Per-fetcher rate limiting"), and a paginated
  mega-thread would starve a bulk import. Truncation is recorded in
  the markdown ("_N more comments not shown_").
- Wikis have no REST content API (they're separate git repos). The
  raw host serves public wikis without auth; private/disabled wikis
  404 → PermanentError. The raw fetch still routes through apiGet,
  so it's limiter-paced like everything else.
- `ParseGitHubURL` gained a `Number` field; non-numeric tails
  (`/issues/new`) classify as "other" → PermanentError.

**Note for existing corpora:** issue/PR/wiki docs bookmarked before
this feature are in state `failed` (the fetcher used to reject them
permanently). `curio refetch --all --state=failed` gives them a
fresh chance.

---

## Dead-link detection: hard 404/410 + soft-404 heuristics

**Decision:** The Native fetcher now classifies dead links and the
system records them in the (previously reserved) document state
`dead`:

1. **Hard dead:** HTTP 404 and 410 return a `PermanentError` wrapping
   the new `ErrDeadLink` sentinel. Previously these were retryable
   and burned all 5 attempts (~7.5 min of backoff) per dead URL.
2. **Soft 404:** an HTTP 200 whose extracted title reads like a
   not-found template (`soft404TitleRE`: "404 …", "Page not found",
   "page has been removed", …) or whose request for a specific path
   settled on the site's homepage (same host, non-trivial source
   path, final path "/", no query). Also `ErrDeadLink`, also
   permanent.

**Ordering matters:** the soft-404 check runs BEFORE the login-wall
heuristics in `tryReadability`. A tombstone page is usually thin, and
the thin-content check would classify it `ErrLoginWall` → Jina
fallback — exactly the wasted-Jina-budget failure mode the fallback
policy exists to prevent. Dead links never touch Jina.

**State plumbing:** the jobs bridge now double-wraps
(`fmt.Errorf("%w: %w", ErrPermanent, pe.Err)`) so the fetcher's
sentinel chain survives to the worker; `OnPermanentFailure` hooks
receive the cause error (`PermFailHook`), and `MarkDocFailed` picks
`dead` over `failed` when `errors.Is(cause, fetcher.ErrDeadLink)`.
The `documents.state` CHECK constraint already allowed 'dead' — no
migration.

**Refetch policy** (the reason `dead` exists as a distinct state):
single-doc refetch of a dead doc returns 409 unless `?force=1`
(CLI: `curio refetch <id> --force`) — the soft-404 heuristic can
false-positive, so the escape hatch is cheap and explicit. Bulk
`refetch --all` *skips* dead docs; `--state=dead` is the deliberate
bulk escape hatch (refetch resets state to pending either way).

**Not host-cached:** `ErrDeadLink` is deliberately absent from
`hostFailureFromError` — a dead path says nothing about the rest of
the host.

**Kill switch:** `fetcher.native.dead_link_detection: false` disables
the whole classifier (404/410 go back to plain retryable errors, the
soft-404 heuristics are skipped). Default true. Exists because a new
always-on heuristic needs an escape hatch bigger than per-doc
`--force` if a corpus turns out to false-positive systematically.

**No archive.org fallback (yet):** the roadmap sketched a Wayback
fallback for dead links; scoped out of this pass to keep detection
observable on its own. If added later, it belongs in `Native.Fetch`
next to the Jina gate, modeled on `tryJina`.

---

## fetcher_rules.yaml: mtime-polled hot reload, keep-last-good

**Decision:** the data-driven fetcher routing designed in
architecture.md is now real. `$CURIO_HOME/fetcher_rules.yaml` lists
rules top-to-bottom, first match wins; matchers are `host` (exact),
`host_suffix` (label-boundary suffix: "youtube.com" matches
"m.youtube.com" and "youtube.com" but not "evilyoutube.com"),
`host_in` (list of exact hosts), or `{}` (catch-all). Rules bind to
fetchers by `Fetcher.Name()` against a registry the daemon builds at
startup: `native` always (pure Go, constructed even when web2md is the
default so rules can bind it), the configured default, `github`, and
`youtube` when yt-dlp is present.

**Parsing is strict** (`yaml` `KnownFields`): an unknown key anywhere in
the file is a validation error. Non-strict decoding would turn a typo'd
matcher key (`host_sufix:`) into an all-empty match block — which is the
catch-all — and one misspelled rule would silently route every URL to
the wrong fetcher on the next reload. Strict + keep-last-good turns that
into a logged warning and no behavior change instead.

**Hot reload = stat-on-dispatch, not fsnotify/SIGHUP:** the
`RulesDispatcher` re-stats the file on `For()` calls, throttled to
one stat per 2s. No new dependency, no signal-handling plumbing, no
watcher goroutine to babysit — and a fetch-heavy import amortizes
the stat to noise. Reload swaps an immutable compiled snapshot under
a mutex, so the 16 fetch workers never see a half-applied rule set.

**Failure posture (all degrade, none crash):**

- file absent → built-in default rules (the pre-feature hardcoded
  wiring); file deleted later → revert to the same.
- file invalid (YAML error, unknown matcher combination,
  `content_type` matcher) → keep the last good rules, log a warning.
  The broken file's stat is recorded so it isn't re-parsed (and
  re-warned) every 2 seconds.
- rule names an unavailable fetcher (e.g. `youtube` without yt-dlp,
  or a typo) → skip that rule with a logged warning listing what IS
  available; other rules still apply.

**`content_type` matching rejected explicitly:** the architecture.md
sketch showed `match: { content_type: application/pdf }`, but
dispatch happens before any response exists — there's nothing to
match against. The validator names the reason; PDFs stay handled
inside the Native fetcher. A post-fetch re-dispatch layer could
revisit this.

**Per-fetcher options stay in config.yaml** — the rules file routes;
it does not configure fetchers.

---

## find_related: stored-vector mean-pooling, not title search

**Decision:** `find_related` is now a real vector-neighbor lookup:
`GET /v1/documents/{id}/related?k=N`. The M3 sidecar shipped with a
stopgap (re-search using the doc's *title* as query text); the MCP
tool and the new `curio related` command now hit the daemon endpoint.

**Algorithm:** read the document's stored chunk vectors back out of
`chunks_vec` (new `ChunkStore.EmbeddingsForDocument`; vec0 point-reads
return the raw little-endian float32 blob, decoded by hand — the Go
bindings ship no deserializer), mean-pool up to the first 64 chunks
in float64, run ONE ANN query with the source document excluded, and
collapse chunk hits to documents through the same
collapse-strategy/hydration path as hybrid search (factored into
`collapseAndHydrate`). No query text, no embedder call, no Ollama
dependency at related-time.

**Why mean-pooling over per-chunk KNN + RRF:** one ANN query instead
of N, and for a personal corpus the doc-level topic centroid is what
"related" should mean. Per-chunk fusion (via the existing `Fuse`)
would surface multi-topic documents better; revisit if mean-vector
results feel muddy on long documents.

**Self-exclusion is an over-fetch problem:** sqlite-vec applies
non-MATCH predicates AFTER the KNN k-cutoff (verified empirically on
v0.1.6: k=1 plus `chunk_id != <nearest>` returns zero rows). The
exclusion therefore rides through `SearchFilters.ExcludeDocumentID`,
which makes the filter set non-empty and routes `VectorSearch`
through its existing k×10 over-fetch path. A bespoke query would
have to replicate that dance — don't.

**Fanout scales with K:** hits are chunk-level, so the ANN pool is
`max(preFanout, K*8)` — a fixed 50-chunk pool could be crowded out by
one long near-duplicate document and could never yield more than 50
distinct documents despite the API accepting k up to 100.

**Contract edges:** unknown doc → 404; known doc with no indexed
chunks (pending/failed/dead) → 200 with empty items (the document
exists; it just has no vectors yet — kinder to MCP callers than a
409). Scores are raw vector similarities (1/(1+L2), 0..1) and NOT
comparable with /v1/search's RRF-fused scores; the openapi
description says so.

---

## Insight layer: kNN-graph clustering + labeled interests (M4)

**Decision:** M4 groups documents into topic "interests" by clustering their
mean-pooled chunk embeddings with a **kNN-graph + deterministic label
propagation** clusterer (`internal/insight`), keeping an explicit noise bucket
for one-off saves. The clusterer sits behind a small `insight.Clusterer`
interface (points in → per-point label out, `-1` = noise), so the algorithm is
swappable without touching storage, API, CLI, or MCP.

**Why not HDBSCAN (which the roadmap originally named):** there is no mature,
trustworthy pure-Go HDBSCAN, and a hand-rolled one (mutual-reachability → MST →
condensed tree → stability) is subtle enough that a bug silently degrades
clusters rather than failing loudly. The kNN-graph approach captures HDBSCAN's
essentials — auto-k, tolerance of varying-density clusters (it's rank-based, not
a single global ε), and a noise bucket — using vectors we already store, at a
fraction of the risk. It's the standard way to cluster embeddings in, e.g.,
single-cell genomics, so it's a different well-trodden path, not a hack. Because
the choice lives behind the interface and we now have an eval harness, swapping
in real HDBSCAN later (if measurement justifies it) is a contained change.

**Graph:** the union of every document's top-K list — an edge joins two
documents when *either* lists the other among its K most similar (at or above
`min_similarity`), weighted by the larger of the two similarities. (A mutual-kNN
graph, where both must list each other, is sparser; it isn't offered because
nothing measures whether it would help — see the mega-cluster entry for the
candidates that would.)

**Determinism:** fully deterministic — weighted-majority vote with a
smallest-label tie-break, stable cluster ordering by size. Label propagation is
order-sensitive (nodes are visited in sequence, ties go to the smallest label),
so the clusterer sorts points by document ID internally and maps labels back:
the result depends on the set of documents, not the order they arrive in. (On
the seeded 1200-point overlapping corpus in `cluster_test.go`, 5 of 5 shuffles
used to change the partition; production only escaped this because
`DocumentVectors` happens to `ORDER BY document_id`.)

**Vector preparation happens once:** the engine mean-centers (when
`insight.center_vectors`) and normalizes the document vectors, then hands the
same unit vectors to the clusterer and to the cohesion/similarity summary, so
both work in the same space and the corpus is held once, not three times. The
clusterer requires unit (or zero) vectors and says so with an error rather than
silently renormalizing. `center` is still recorded in the run's params.

**Performance:** building the kNN graph is O(n²·d) and was essentially the
whole cost (the union step and label propagation take tens of milliseconds even
at 20k documents). Rows are independent, so they run in parallel across
GOMAXPROCS workers that share only a row counter, and each row keeps its top K
in a small heap instead of sorting every candidate above the threshold. Tie
order (similarity desc, index asc) is unchanged, and a test pins the neighbor
lists to a serial full-sort reference. `BenchmarkKNNGraphClusterer` (5000
documents × 768 dims, Apple M4 Max, 16 cores): **15.0 s → 1.29 s**. A 4-way
unrolled dot product would roughly halve that again, but it changes float
summation order and so shifts similarities in the last bits; we kept the
existing dot so an upgrade doesn't reshuffle anyone's interests. Computing
each pair once (filling both rows from one dot product) was also left out: it
needs cross-worker synchronization on the row heaps for at most a 2× gain.

**Labels:** LLM labels by default (`LLMLabeler`, `insight.labeling = "llm"`) for
richer topic names + summaries. The generation model is auto-pulled on startup
(see below); until it's ready, unavailable, or if `generation.auto_pull` is off,
the engine falls back to deterministic term labels (`TermLabeler`) — so the
layer still works with zero setup. Set `insight.labeling = "terms"` to force the
deterministic labeler.

The fallback is decided per run, not at startup. The daemon used to ping Ollama
once when it started and, if that failed, never wire the LLM labeler — and
since the CLI auto-starts the daemon, often before the Ollama app is up, every
rebuild quietly used term labels until a restart. Now the labeler is always
wired with `labeling = "llm"`, and model auto-pull runs in the background
either way. The pull is attempted once: if Ollama is down at startup and
doesn't already have the model, labels stay on terms until `ollama pull` or a
daemon restart. Within a run:

- Clusters are labeled **largest first**, whatever numbering the clusterer
  used, so the budget goes to the interests that matter most.
- The first LLM failure that would repeat — unreachable, an HTTP error, a
  timeout — **switches the LLM off for the rest of that run**: the remaining
  clusters get term labels at once, and one WARN reports how many clusters got
  term labels (counting those never offered to the model) and the last LLM
  error. Before, every cluster waited out the same failure (with generator
  retries, ≈6 min each), so a hung Ollama could hold the single cluster worker
  for hours.
- `insight.labeling_timeout_seconds` (default 900) caps the total time one run
  waits on the LLM; when it runs out the rest get term labels.
- An unparseable reply (below) costs only that cluster its LLM label.
- Labeling stays sequential: a local Ollama serializes generation anyway and
  would compete with index embeddings, and the budget plus ordering bound the
  wait.
- If the run's own context ends (daemon shutdown), the run fails instead of
  finishing with fallback labels.

The model's reply must carry an explicit `NAME:` field (case-insensitive;
`-`, `=`, en/em-dash separators and markdown emphasis are tolerated) of at most
six words. Anything else — a preamble ("Sure! Here you go:"), a bare line, only
a `SUMMARY:` — is rejected as `ErrUnparseableLabel` and that one cluster gets a
term label; guessing a name from some other line used to persist preambles as
interest names. Term labels split titles on Unicode letters and digits (not
just ASCII), so accented and CJK titles yield whole words rather than
fragments like "Montr Caf".

**Storage / lifecycle:** clustering fully recomputes each run. A `cluster_runs`
row records the attempt (`running` → `done`/`failed`); the current interests are
the `clusters` of the latest done run, and older runs are pruned (keeping
history for trajectory analysis is deferred). It runs on a dedicated
single-worker `cluster` job pool so it neither starves nor is starved by
fetch/index. Knobs: `insight.{enabled,knn,min_similarity,min_cluster_size,
labeling,labeling_timeout_seconds}`. `min_similarity` is the main granularity
dial and is corpus-dependent — tune it with the eval harness.

The guards that protect the last good run treat only `ErrNotFound` as "there is
no prior run". An empty corpus with an unreadable `LatestRun` (say, `database
is locked`) fails the rebuild instead of recording an empty run and pruning the
good one; after a failed run, pruning is skipped with a WARN when the latest
done run can't be read, because cleanup is best-effort and must not delete on a
guess. Marking a run failed (and that cleanup) runs detached from the run's
context with a 10 s timeout, so a run cut short by daemon shutdown still ends
as `failed` rather than `running` forever. `min_similarity` is validated
NaN-safely: YAML `.nan` used to pass, reject every edge, and replace the
interests with an empty run.

**API surface:** an interest *is* a labeled cluster, so there is one surface —
`GET /v1/interests`, `GET /v1/interests/{id}`, `POST /v1/interests/rebuild`
(202 + job_id; 409 when disabled) — rather than the separately-sketched
`/v1/clusters`.

---

## LLM generation client (`generator.Generator`)

**Decision:** Introduce a provider-agnostic text-generation interface
(`internal/generator`: `Generate` / `Model` / `Ping`), separate from
`internal/embedder`. The v1 implementation targets Ollama's `/api/generate`
(non-streaming, bounded retries, explicit `num_ctx`), configured by a new
`generation` block (`provider`, `model` — default `llama3.2`, `base_url`,
`timeout_seconds`).

**Why:** embeddings and generation are different models on different endpoints,
and most of curio needs neither — so generation is its own small, optional
dependency. One interface means M4 (cluster labels) and M6 (RAG synthesis + LLM
query rewriting) share the same seam, and an Anthropic/Claude implementation can
drop in later without touching callers. The embedding model (nomic-embed-text)
can't generate, so a generation model must be pulled separately.

**Retry policy:** `Generate` retries only what a retry can fix, with a short
linear backoff (0.5 s, 1 s) and `retries` = 2 by default (negative = none):

| Failure | Retried? | Why |
|---|---|---|
| HTTP 5xx | yes | the model may still be loading, or Ollama is restarting |
| connection refused / reset, EOF mid-response | yes | Ollama starting or restarting |
| per-attempt timeout (`generation.timeout_seconds`) | **no** | at 120 s per attempt a timeout isn't a blip; retrying tripled the stall (≈6 min per cluster label) |
| HTTP 404 | no | the model isn't pulled; wraps `ErrModelNotLoaded` |
| other 4xx, undecodable or oversized (> 1 MiB) reply | no | the same request gets the same answer |
| caller's context done | no | returned at once, wrapping `context.Canceled` / `DeadlineExceeded` |

Every non-200 is a typed `*StatusError{Code, Body}` (body capped at 2 KiB), and
transport errors are wrapped `%w: %w` so both `ErrOllamaUnreachable` and the
cause (e.g. `ECONNREFUSED`, `context.DeadlineExceeded`) stay matchable — the old
`%w: %v` wrapping hid the timeout, which is why timeouts were being retried.

**Model auto-pull:** because a required model being absent is a poor
first-run experience, the daemon pulls missing models on startup via Ollama's
`/api/pull` (shared `internal/ollama.PullModel`; both the embedding and
generation clients get an `EnsureModel`). It runs in the background so startup
isn't blocked — index jobs retry until the embedding model lands, and cluster
labeling uses the term fallback until the generation model lands. Gated by
`embedding.auto_pull` / `generation.auto_pull` (default true; turn off for
metered/offline setups). This is why LLM labeling can be the default without
making a 2 GB download a hard prerequisite.

---

## Retrieval eval harness

**Decision:** Ship `curio eval --queries <qrels.yaml>` (`internal/eval`) — a
pure, deterministic scorer for recall@k, precision@k, NDCG@k, and MRR over a
labeled query set. The qrels file lists, per query, the relevant document URLs
(stable across machines); the CLI runs each through `/v1/search` and scores the
returned ranking. Example: `docs/eval.example.yaml`.

**Why:** the "Natural-language search is provisional" note makes an eval harness
the explicit prerequisite for any search change — *build the eval before the
improvement*. This is that harness. It also de-risks M4 (measure whether a
`min_similarity` change helps retrieval) and is the ground truth for the M6
build-vs-buy RAG decision. The metrics package has no HTTP or store dependency,
so it's easy to test and reuse.

**Scoring rules** (the harness approves search changes, so it must not reward
the wrong thing):

- **P@k is hits / k** (trec_eval's definition): ranks past the end of a short
  result list count as misses. It used to divide by the number retrieved, so
  `[a]` with `a` relevant scored 1.0 at k=10 instead of 0.1 and a change that
  returned fewer results scored better. Precision numbers recorded before this
  fix aren't comparable with new ones; recall, NDCG and MRR are unaffected.
- **Relevant URLs are normalized on load** with `urlutil.Normalize`, the same
  canonicalization stored document URLs went through, and de-duplicated
  afterwards; an unparseable URL fails loading and names the query. Verbatim
  comparison silently scored a browser-pasted URL (fragment, `utm_*`,
  `youtu.be`, a bare origin without the trailing `/`) as never retrieved.
  `urlutil` is pure, so the package still has no store/HTTP/search dependency.
- **A degraded search is refused:** if any query's response comes back
  keyword-only (the vector leg failed, see "Hybrid search"), `curio eval`
  exits non-zero naming the query and the warning instead of scoring BM25 as
  if it were the hybrid pipeline. `--k` must be at least 1 (checked locally);
  above 100 the API's 400 surfaces.

---

## M6 (planned): RAG / Q&A synthesis + SOTA natural-language search

**Decision (direction, not yet built):** M6 will (1) answer natural-language
questions over saved content via retrieval-augmented generation — retrieve with
the existing hybrid engine, synthesize a cited answer through
`generator.Generator`, likely surfaced as `curio ask` / `POST /v1/ask` — and (2)
upgrade NL search beyond the current OR+stopword BM25 using the options logged
above (stemming + `minimum_should_match`, LLM query rewriting via Ollama, or
SPLADE/ColBERT), every change gated on the eval harness.

**Open decision — build vs. buy (resolve before implementing RAG):** decide
whether to build curio's own RAG loop or adopt an existing local-first tool such
as [tobi/qmd](https://github.com/tobi/qmd). The eval harness makes this a
measurable comparison (qmd vs. our retriever on the same qrels) rather than a
taste call. Do not start building the RAG loop until this is settled.

**Why M6, not M5:** M5 (suggestions & digest) is unchanged and stays ahead of
M6; RAG/search is the larger, more foundational body of work, and suggestions
build on good retrieval regardless. This renumber pushed the former "Hosted
mode" milestone to M7.

---

## nomic-embed-text task prefixes (`search_document:` / `search_query:`)

**Decision:** Prepend nomic-embed-text's required task-instruction prefixes
before embedding — `search_document: ` for indexed chunks, `search_query: ` for
queries (config `embedding.document_prefix` / `embedding.query_prefix`). The
prefix is applied ONLY to the text sent to the embedder; the stored chunk text
stays raw, so BM25 and snippets are unaffected. find_related and clustering
reuse the stored (already-prefixed) document vectors, so they need no prefix of
their own.

**Why:** nomic-embed-text is a *prefixed* model — it was trained with task
instructions and its card requires them. Without prefixes its embedding space is
badly anisotropic (vectors collapse into a narrow cone), which surfaced as a
single "interest" swallowing ~60% of a real corpus and also drags on search
quality. Adding the prefixes de-collapses the space at the source — higher
leverage than any downstream clustering trick (mean-centering was a partial
mitigation of the same anisotropy, kept as a complementary safety net).

**Reindex required:** stored document vectors and query vectors must share one
prefix scheme, so changing either prefix means re-embedding the whole corpus
(`curio reindex`). There is no automatic marker check for the prefix yet — the
`.curio-meta.json` cross-check covers only embedding model + dim, so a prefix
change won't warn; enforcing it is a deliberate follow-up. For now, reindex
after any prefix change. Set both prefixes to "" for a model that takes none.

---

## Insight clustering quality: the "general-reading" mega-cluster (known limitation)

**State at M4 ship:** `curio interests` produces genuinely good *niche* interests
(Kubernetes, Ontario regulations, remote-work tools, Canadian investment news,
Cloudflare/web-dev, …) plus one large "general-reading" cluster that absorbs
~60% of a real 5.6k-doc corpus. The niches are useful; the mega-cluster is the
open problem. M4 is considered done with this documented.

**What was ruled out empirically** (recorded so a future effort doesn't repeat
the same experiments):

| Hypothesis | Test | Result |
|---|---|---|
| kNN threshold too low | `min_similarity` 0.5 → 0.65 | inert — byte-identical output |
| single-axis anisotropy | mean-centering (shipped) | helped granularity + added a noise bucket, blob persists |
| wrong embedding usage | nomic `search_document:` / `search_query:` prefixes + full reindex (shipped) | labels/cohesion shifted (embeddings did improve), blob still ~60% |
| thin / boilerplate content | chunk-count profile of the blob | ruled out — ~90% of blob docs are substantial (>3 chunks), same profile as the good clusters |

**Diagnosed cause:** doc-level **mean-pooling** of nomic embeddings. Averaging
every chunk of a general-interest article lands it near a common "general prose"
centroid; only documents that are *entirely* about one narrow topic keep a
distinctive mean vector. This is a doc-representation + embedding-resolution
limit, not a tunable parameter — which is why no threshold / centering / prefix
change fixes it.

**Candidate fixes (deferred), rough leverage/effort order:**
1. **Recursively split oversized clusters** — re-run the clusterer on just the
   mega-cluster's members; their own mean-centering removes *their* shared
   direction and exposes sub-topics. Cheapest, reuses the existing `Clusterer`,
   targets the symptom directly. Try this first.
2. **Leiden clusterer with a resolution knob** — drop in behind the existing
   `insight.Clusterer` interface; Leiden can subdivide dense regions where label
   propagation collapses them into one community.
3. **Better doc representation than mean-pooling** — lead/salient-chunk embedding
   or TF-IDF-weighted pooling to sharpen topical signal before clustering.
4. **Chunk-level clustering** — cluster chunks and derive doc interests; more
   faithful, but a doc can then belong to several interests (a UX/model change).

The `curio eval` harness and the swappable `Clusterer` interface exist precisely
so any of these can be measured and swapped without touching storage / API /
CLI / MCP. Note that the prefix fix is worthwhile regardless of clustering — it
corrects genuinely wrong embedding usage and improves *search* quality (the
primary use case).

---

## Host-cache hits are permanent failures

**Decision:** When `Native.Fetch` short-circuits on a fresh
`hostFailureCache` entry (unreachable / anti-bot / login-wall) it
returns a `PermanentError` wrapping the same sentinel it always did,
so the jobs bridge marks the job `failed` (and the doc `failed`) on
the spot. The *first* failure for a host is unchanged — a plain
retryable error that populates the cache.

**Why:** Observed during an import: ten jobs sat `pending` for ~15
minutes each. Attempt 1 did real work (origin, then Jina), the host
got cached, and attempts 2–5 each hit the cache, returned a retryable
`ErrLoginWall (cached: …)`, and slept 60 → 120 → 240 → 480s. Four
retries that could only ever re-read a cache entry. The cache TTL
(15 min) roughly equals the total backoff, so at best the fifth
attempt did one more real fetch with the same result. Meanwhile
`curio import`'s progress line showed `eta≈0s` — its rate is jobs
finished between ticks, and nothing finishes while every remaining
job is asleep.

**Effect:** a URL on a freshly-bad host now fails after one real
attempt plus one 60s backoff (~1 min instead of ~15). Later URLs on
the same host inside the TTL fail instantly, with no origin request.

**Tradeoff:** a transient host-wide blip (a 503 during someone's
deploy) now fails every URL on that host for the rest of the TTL
window without a second real attempt — previously they'd have retried
into the same cached verdict anyway, so little is actually lost.
Recovery is `curio refetch --all --state=failed` (or per-doc
`curio refetch <id>`) once the host is back: cheap and explicit,
same posture as dead links. The
`(cached: …)` suffix survives into `last_error` so
`curio jobs --failed` shows why.

**Not changed:** the first failure stays retryable (one genuine retry
per host after the TTL is still useful for flaky origins),
`ErrDeadLink` is still not host-cached, and MaxAttempts / backoff are
untouched for everything that isn't a cache hit.

**Revised:** which failures are cached, and under which host, changed.
Thin pages and Jina-side failures are no longer cached, and a page-level
login wall is final on its own. See "Host cache: only host-wide verdicts,
under the host that gave them" below.

---

## Local API: loopback only, no token, browsers shut out

**Decision:** The daemon API stays unauthenticated and defends its
perimeter instead:

- `daemon.listen` must be a loopback host (`127.0.0.0/8`, `::1`,
  `localhost`) with a fixed port in 1–65535; anything else fails config
  validation.
- Router-level middleware (it runs before routing, so it also covers
  404/405) answers 403 unless `Host` is `localhost:P` (any case) or an
  address equal to 127.0.0.1, ::1 or the bound address, on port `P`,
  where `P` is the port the listener actually bound. Addresses compare
  by value, so `[::ffff:127.0.0.1]:P` passes: clients send `daemon.listen`
  as written.
- A request that carries an `Origin` gets 403 unless it is exactly
  `http://127.0.0.1:P`, `http://localhost:P` or `http://[::1]:P`. That
  includes `Origin: null` and localhost on another port. The daemon never
  emits CORS headers, so no preflight is ever approved.
- Request bodies must be `application/json` (415 otherwise). Body-less
  POSTs (refetch, reindex, interests/rebuild) need no Content-Type. Bodies
  are capped at 1 MiB, or 32 MiB for `POST /v1/bookmarks/import`; bigger
  ones get 413. A body holds exactly one JSON value; the decoder reads to
  the end, so padding after the value counts toward the cap.
- The server sets read-header (5s), read (30s), write (2m, longer than the
  slowest synchronous handler) and idle (2m) timeouts.

**Why:** Reproduced before the change. A web page's `text/plain` POST
to `/v1/bookmarks` created a bookmark and a fetch job for a LAN URL of the
page's choosing (201). A body-less `refetch-all` with `Origin: null`
returned 202. A DNS-rebinding page, whose hostname re-resolves to
127.0.0.1, could read `/v1/search` and document content, meaning the whole
reading history. Browsers attach `Origin` to every cross-origin POST, and
a page can only send a JSON body after a CORS preflight. A rebinding page
still sends its own hostname in `Host`.

**Threat model:** browser-originated requests are blocked. Other local
processes, and other OS users who can reach loopback, are trusted. There
is no token. The long-lived MCP sidecar would have to re-read a rotating
token after every daemon restart, and for same-user processes it adds
nothing, because anything that can read a token file under `$CURIO_HOME`
can read `curio.db`. A token would keep *other* OS users on the same
machine out. That is accepted for a single-user tool and revisited with
hosted-mode auth. Browsers' Private Network Access protections are not
relied on because they don't ship everywhere.

---

## Single daemon per home: flock on daemon.pid, bind before touching the DB

**Decision:**

- The daemon takes an exclusive `flock(2)` on `$CURIO_HOME/daemon.pid`
  right after resolving the home, before it loads config or opens the DB.
  It retries non-blocking for about 2s, so a client's momentary
  status-probe lock can't make it give up. It then writes its PID into
  that file through the locked descriptor and truncates it on clean exit.
  It never unlinks or replaces the file: a new daemon locking a fresh
  inode while a prober holds the old one would break mutual exclusion.
- It binds the API port before migrations, orphan recovery or workers.
  A second daemon (lock held) and one that can't bind both exit without
  writing to the DB.
- Startup order: signals, home, lock + PID, config + log level, marker
  check, bind, open + migrate, construct dependencies, orphan recovery
  per pool, workers, serve.
- Clients (`internal/daemonctl`) decide liveness with a shared-lock probe
  and identify the daemon through `/v1/healthz`, which now reports `pid`
  and `home`. They only ever signal the PID recorded by the current lock
  holder, and only when healthz (if it answers) reports the same pid and
  home. Auto-starts are serialized by an exclusive lock on
  `daemon.start.lock`, so the CLI and the MCP sidecar can't both spawn.
  The spawner watches the child with `cmd.Wait`. A daemon that dies during
  startup is reported at once, with its exit status and the tail of
  `daemon.log`. A starter that finds the lock held with nothing serving
  waits for that daemon, and spawns its own if the holder releases the
  lock without ever answering (a `curio daemon stop` still draining).
  Whatever a free lock file contains is only informational: a PID there
  is reported as stale, and anything unparsable is ignored.
- Every healthz probe (`client.Healthz`) waits up to 2s. The handler's
  only wait on another service is its Ollama check, capped at 500ms, so a
  daemon answers well inside the probe's limit whatever state Ollama is
  in, and a stalled Ollama shows up as `ollama_reachable: false`. With
  equal limits on both sides the probe gave up first, and clients took a
  running daemon for a missing one.
- SIGINT, SIGTERM and SIGHUP all shut down gracefully. Shutdown is
  bounded: 5s for in-flight HTTP requests, then 15s for running jobs.
  Jobs still running after that are logged by ID and left for orphan
  recovery. `curio daemon stop` waits up to 30s for the signalled daemon
  to release the lock (or for the lock to pass to a newer daemon).

**Why:** Nothing enforced one daemon per home. Startup reset every
`running` job to `pending` with raw SQL before proving it was alone, and
started workers before binding. A second daemon therefore requeued the
first one's in-flight jobs, which then ran twice, claimed jobs itself,
and died on EADDRINUSE with those jobs left `running`. The CLI wrote the
PID file and trusted `kill(pid, 0)`. After a reboot or PID reuse it
reported an unrelated process as the daemon, refused to start one, and
`curio daemon stop` sent that process SIGTERM.

**Why flock, not fcntl:** an fcntl lock is released when the process
closes *any* descriptor for the file. An flock belongs to one open file
description and is released only when that is closed or the process
dies, SIGKILL and crashes included. That is what makes the lock
trustworthy where a PID isn't. Both darwin and linux have it in stdlib
`syscall`.

**Upgrade path:** a daemon from before this change holds no lock and
reports no identity. Clients treat it as running, so an upgrade doesn't
spawn a second daemon that can't bind. `curio daemon status` shows it as
legacy, and `curio daemon stop` refuses to signal it and prints how to
stop it by hand.

**Not done:** job leases or heartbeats. The lock makes "one daemon per
database" true, and that is the assumption the queue relies on.

---

## Interrupted vs. orphaned jobs

**Decision:**

- The worker's context decides only whether to claim another job, and it
  bounds the handler. Every queue write after the decision to claim runs
  on a context detached from it and bounded by 10s, longer than SQLite's
  5s busy_timeout. That covers `ClaimNext`, `MarkDone`, `MarkFailed`,
  `Requeue` and the permanent-failure hook.
- **Interrupted:** a handler returns while the worker's context is done
  (shutdown). `Requeue` puts the job back to `pending`, runnable now, with
  the attempt refunded and `last_error` untouched. This keys off the
  worker's context, not the error. A handler cut short by shutdown can
  return anything: `context.Canceled`, a subprocess's `signal: killed`,
  even an `ErrPermanent` the cancellation caused. A handler that returns
  nil during shutdown is still marked `done`.
- **Orphaned:** a job left `running` by a daemon that died (crash,
  SIGKILL, drain timeout). At startup, while holding the lock and before
  any worker runs, each pool's `Worker.RecoverOrphans` requeues its kinds'
  orphans with the used attempt kept. An orphan with no attempts left is
  failed instead, and its kind's permanent-failure hook runs, so the
  document goes `failed` rather than staying `pending`. The hook runs on
  the detached context too: the job is already committed as failed, so a
  stop that lands mid-startup mustn't skip it.
- A panic in a handler or hook is recovered and logged with its stack. The
  job fails permanently with `last_error` starting `panic:`, and the
  worker moves on to the next job.
- `MarkDone`, `MarkFailed` and `Requeue` only transition a `running` row.
  Otherwise they change nothing and return `store.ErrNotRunning` (or
  `ErrNotFound`).

**Why:** On SIGTERM the worker did its bookkeeping on its already-cancelled
context, so every outcome recorded during shutdown failed with
`context.Canceled`. Finished jobs stayed `running` and ran again, producing
a duplicate extraction and a duplicate index job. The startup reset didn't
refund the claim, so every restart cost a job one of its five attempts.
Separately, a panic anywhere in a handler killed the daemon along with
every in-flight job's bookkeeping. `ClaimNext` never checks attempts, so a
job that crashed the daemon was re-claimed after every restart, forever.
Keeping the attempt on orphans, but refunding it on interrupts, is what
ends that loop. The detached claim context also sidesteps a driver quirk:
mattn's `Rows.Next` can report cancellation for an `UPDATE ... RETURNING`
that has already committed.

**Not done:** graceful drain of in-flight handlers. Interrupted work is
simply redone.

---

## Config: strict keys, legacy `workers` folded in at load

**Decision:**

- `config.yaml` is decoded strictly (`KnownFields`). An unknown key at any
  depth is a load error that names the key. Empty and comment-only files
  still mean "all defaults".
- `Load` translates the deprecated `daemon.workers`, keyed on whether the
  key is present in the file rather than on its value:
  - On its own it splits 75/25 into fetch and index, at least 1 each
    (`workers: 8` gives 6/2).
  - Combined with `fetch_workers` or `index_workers` it is an error.
  - `workers <= 0` is an error, and so are `fetch_workers <= 0` and
    `index_workers <= 0`.
  - `Validate` no longer mutates anything.
- `daemon.log_level` takes effect, through a `slog.LevelVar` set right
  after load.
- `embedding.dim` must equal `store.EmbeddingDim` (768, the width
  `chunks_vec` was created with). `embedding.provider` and
  `generation.provider` must be `ollama`.

**Why:**

- The legacy split ran in `Validate` on a value receiver, so its result
  was computed on a copy and thrown away.
- `Load` decodes on top of `Default()`, so `workers: 8` alone silently ran
  16/4, and `workers: 8, fetch_workers: 0, index_workers: 0` ran zero
  workers.
- A typo'd section (`embeding:`) silently fell back to defaults, the same
  failure `fetcher_rules.yaml` already parses strictly to avoid.
- `dim: 1024` loaded fine and then failed every insert into the 768-wide
  vector table.
- A provider value was never read, so any value was silently accepted.

---

## Refetch: state reset and fetch job in one transaction

**Decision:** `DocumentStore.RequeueFetch` and `RequeueFetchByStates` reset
the document(s) to `pending` and insert the fetch job(s) in one
write-first transaction: the UPDATE comes first, then the INSERTs.
Everything commits or nothing does.

- `refetch-all` rejects any `?state=` other than pending, fetched, failed
  or dead with 400. With no `?state=` it defaults to pending, fetched and
  failed.
- `reindex-all` stops at the first enqueue failure and reports how many
  jobs it enqueued.
- Every job insert goes through one helper (`insertJob` in
  `internal/store/sqlite`), inside a transaction or not.

**Why:** The handlers flipped the document to `pending` and then
enqueued, discarding errors. A failed enqueue left the document `pending`
with no job, the stuck state the permanent-failure hook exists to
prevent. `refetch-all` returned 202 with a count that hid the failures.

**One transaction, not batches:** 50k documents took 1.5–2s (measured),
and about 2.4s since migration 007 added the jobs indexes and the
`document_id` check (see "Indexes follow the queries"). Nearly all of it
is the job INSERTs, and a prepared statement saved only about 8%. That is
inside the 5s busy_timeout other writers wait on up to roughly 100k
documents. Past that, a worker write that lands during
the bulk transaction fails as busy; its job stays `running` and is
recovered as an orphan on the next start. Chunked transactions are the
fix if corpora get there.

**Not done:** skipping documents that already have a queued fetch job.

---

## Migrations: rebuilding a table other tables reference

**Decision:** Table rebuilds follow the recipe in `migrations/README.md`:

- The file starts with `-- +goose NO TRANSACTION`.
- The whole rebuild is one `StatementBegin` block: `PRAGMA foreign_keys =
  OFF; BEGIN IMMEDIATE;`, then the rebuild, an enforcing foreign-key
  guard, `COMMIT; PRAGMA foreign_keys = ON;`.

Migration 002 was rewritten into this form in place. That is a narrow
exception to "never edit an applied migration", allowed because the
resulting schema is identical; a test compares it against the original
file.

**Why:** 002 set `PRAGMA foreign_keys = OFF` inside goose's per-migration
transaction, where SQLite ignores it, and the README recommended that
recipe. It was harmless for `bookmarks`, which nothing references. Used on
`documents`, `DROP TABLE` would have run the ON DELETE actions: every
extraction, chunk and cluster membership deleted, and every
`bookmarks.document_id` nulled.

Two more traps shaped the recipe:

- goose Execs a bare `PRAGMA foreign_key_check`, and the driver discards
  its rows, so it can't abort anything.
- In NO TRANSACTION mode goose's global API runs each statement on an
  arbitrary pooled connection, so the pragma, BEGIN and COMMIT must be one
  statement.

A failed rebuild leaves its connection mid-transaction with foreign keys
off. The daemon therefore never reuses the DB after a `Migrate` error; it
exits.

---

## Fetcher errors: one typed status model

**Decision:**

- Every HTTP failure a fetcher returns carries a
  `*fetcher.HTTPStatusError{StatusCode, URL, RetryAfter}`. `URL` is the
  URL that answered, after redirects. `RetryAfter` comes from the
  `Retry-After` header, as delta-seconds or an HTTP-date.
- One rule, `retryableStatus`, decides retry vs. permanent: 408, 421, 425,
  429 and every 5xx except 501 and 505 are retried. Every other status is
  a `PermanentError`.
- The Native fetcher applies its fetch policy before that rule: 403 and
  503 are `ErrAntiBot` and stay retryable, because Jina may get through.
  404 and 410 are `ErrDeadLink` permanents with dead-link detection on and
  retryable with it off. GitHub treats 404 as permanent and tags rate
  limits for `apiGet`; everything else follows the rule.
- `PermanentError` and the sentinels live in `internal/fetcher/errors.go`.
  Error wraps use `%w`, twice when there are two causes.
- Error text quotes at most 512 bytes of a response body.

**Why:** Native retried 401, 402 and 451 five times over about 15
minutes, like a 500. GitHub made every status it didn't list retryable, so
a deleted issue (410) or a DMCA-blocked repo (451) burned the whole retry
budget. Jina kept a status map of its own. Errors built with `%v` or plain
strings couldn't be matched with `errors.Is`/`errors.As`, and GitHub
errors pasted whole response bodies into `jobs.last_error`.

---

## GitHub: secondary rate limits and a shared cooldown

**Decision:**

- A 429 is always a rate limit. A 403 is one when it carries
  `Retry-After`, `X-RateLimit-Remaining: 0`, or a body mentioning
  "secondary rate limit" or "abuse detection". Any other 403 is a real
  permission answer and fails permanently.
- The wait is `Retry-After` when given, else until `X-RateLimit-Reset` for
  the primary limit, else one minute. GitHub asks for at least a minute
  before retrying a secondary limit, and a reset time already in the past
  is no reason to retry at once.
- A rate-limit answer extends a cooldown shared by every GitHub call. Each
  call waits out a remaining cooldown of up to 2 minutes before it goes
  out. A longer one fails the call at once, without a request, as a
  retryable `*HTTPStatusError{429}` whose `RetryAfter` is the time left,
  and the job queue's backoff covers the rest. The call that got the long
  `Retry-After` fails the same way. Still at most 3 attempts per call.
- The cooldown is checked after the shared limiter grants a call its
  token, not before. Workers already queued in the limiter when the
  rate-limit answer arrives would otherwise pass a check made before the
  answer and then send their requests into the limit: 6 of 6 did in a
  probe. A call that sat a cooldown out queues for a fresh token, so the
  held-up calls resume at the limiter's pace instead of all at once.
- A README or comment thread that doesn't exist (404) is left out of the
  document. Any other failure of those calls (5xx, a rate limit, a
  timeout) fails the fetch retryably. The README is a repo document's
  primary content, so storing the document without it hid the failure
  for good.

**Why:** The secondary limit answers 403 while `X-RateLimit-Remaining` is
still positive, so the 251-repo refetch this was built for failed those
repos permanently on attempt 1. When one call backed off, the other
workers kept calling through the shared limiter, although GitHub counts
the limit per account and IP.

**Beyond 2 minutes:** sleeping longer inline would hold a fetch worker
for the whole wait. `JobQueue.MarkFailed` takes no delay, so the queue
can't honor the hint yet. `RetryAfter` travels on the error for when it
can.

---

## Fetchers: one cap on every response body

**Decision:**

- Every response body a fetcher reads is capped at 32 MiB
  (`maxResponseBytes`), counted after decompression. Past the cap a read
  fails with `ErrTooLarge` rather than quietly ending, so a cut-off body
  can never pass for a complete one. `ErrTooLarge` is always permanent,
  never goes to Jina and is never host-cached.
- For the Native fetcher the cap is one decorator around the transport
  (`limitBodies`), so it covers both backends and both the origin and
  Jina requests. GitHub reads through the same limiter, and Web2MD's
  stdout has the same cap.
- A PDF over the cap skips local extraction and goes to Jina, as before,
  but now without reading past the cap.
- `text/event-stream` is refused on its Content-Type: it never ends.
- A Jina body cut off mid-transfer is a transport failure and is retried.
  It used to be stored as a short article.

**Why:** Nothing bounded a body. Readability's parser, Jina and GitHub
all read to EOF, and decompression is lazy on both backends, so a gzip
or brotli bomb multiplied whatever came over the wire. `text/*` let an
endless event stream through. The only limit was the 30s client timeout,
times 16 fetch workers. The Jina path dropped the read error, and an
overlay probe stored a truncated Jina answer as a 629-character
"success" that was never retried.

**Detection detail:** go-readability flattens reader errors with `%v`, so
`errors.Is` can't find `ErrTooLarge` through it. `tryReadability` asks
the capped body whether it overflowed instead of pre-buffering, since the
parser already copies the whole body.

---

## Host cache: only host-wide verdicts, under the host that gave them

Builds on "Host-cache hits are permanent failures": a hit fails every URL
on the host for 15 minutes without a request, so a wrong entry is
expensive.

**Decision:**

- Only three verdicts speak for a whole host, and only they are cached:
  - **unreachable**: the name doesn't exist (`net.DNSError.IsNotFound`),
    or the host refuses connections (`ECONNREFUSED`) or has no route
    (`EHOSTUNREACH`);
  - **anti-bot**: a 403 or 503 answer;
  - **login wall**: a redirect onto the requested site's own login page
    (`loginPathRE`, same site ignoring a leading `www.`).
- Everything else is about one page (thin text, no article, a login-like
  title, a redirect to another site) or transient (DNS timeouts and
  temporary failures, `ENETUNREACH`, which is our own network), and is
  never cached.
- A verdict is cached under the host that gave it: the redirect target's
  host from `*url.Error.URL` or `HTTPStatusError.URL`, not the host that
  was requested. Keys are lowercased hostnames.
- Nothing is cached when Jina was tried and failed for its own reasons:
  429, 5xx, 408, timeouts, transport errors, 401/402 (our account) or
  its own cooldown. Only a Jina verdict about the target counts: a 2xx with
  too little content, or another non-retryable 4xx.
- A page-level verdict Jina could have helped with is final once every
  configured extraction path has answered: with Jina off, or after Jina
  gave its own verdict, the fetch fails with a `PermanentError` on
  attempt 1. The document goes `failed`, not `dead`. If Jina only had
  trouble, the error stays retryable.
- The login-wall heuristics check redirects first, so a thin login page
  reached by redirect still counts as the site-wide wall it is.
- Error chains are kept whole. Both causes are wrapped with `%w`, so the
  error for "origin and Jina both failed" matches the origin's sentinel
  and Jina's `*HTTPStatusError`. Jina's failure leads the chain: a retry
  depends on Jina now, so `errors.As` finds its status and `Retry-After`
  before an origin 403/503. An unreachable host keeps its `*url.Error`
  and `*net.DNSError`.

**Why:** Four kinds of wrong entry, each confirmed by a probe:

1. Every `ErrLoginWall` was cached as host-wide, although most are about
   one thin page. With Jina off, one short page failed the whole site.
2. A Jina outage or 429 cached every host that needed Jina.
3. Verdicts were keyed by the requested host. A shortener (bit.ly, t.co,
   lnkd.in) redirecting one link to a site that answered 403, or to a dead
   host, got the shortener cached, and its healthy links then failed with
   zero requests.
4. Any `*net.DNSError`, including resolver timeouts, and "network is
   unreachable" were cached as "unreachable", so a Wi-Fi blip mid-import
   cached every host it touched.

**Why page-level verdicts are final:** without the cache, attempts 2–5
would each repeat the origin fetch and up to four Jina requests for the
same thin page, spending the budget the fallback policy protects and
bringing back the "~15 minutes pending" symptom. The page answered the
same way on every path that exists.

---

## Login-wall heuristic: www and apex are the same site

**Decision:** The cross-host check in `looksLikeLoginWall` and the
same-host check in the soft-404 "redirected to homepage" rule compare
hosts with `sameSiteHost`: case-insensitive, ignoring one leading `www.`.

**Why:** `example.com` → `www.example.com` (and old `http://` bookmarks
upgraded to `https://www.`) is canonicalization, not a login wall. The
rule came from the JS implementation, where it was meant to catch
redirects to login/SSO hosts. Every such full article was sent to Jina,
which allows 20 requests a minute without a key, and failed outright
with Jina off. A deleted post redirecting to the `www` homepage skipped
the soft-404 check and landed in the login-wall path instead of `dead`.
Redirects to any other host are still flagged, and never host-cached.

---

## Fetch politeness: shared Jina pacing, per-host origin gate

**Decision:**

- All Jina calls go through one limiter per Native fetcher, and all 16
  fetch workers share that fetcher: 20 requests a minute without an API
  key, 200 with one. Jina publishes 20 and 500.
- A Jina 429 extends a cooldown shared by every Jina call. The wait is its
  `Retry-After`, or the current backoff step when it gave none. A later
  call waits out up to 30 seconds of cooldown inline. A longer one fails
  at once, without a request, as a retryable `*HTTPStatusError{429}`
  whose `RetryAfter` is the time left. The host cache is never written
  for it. 5xx and transport errors keep the 2/4/8 s backoff, now through
  the injectable clock, with 4 attempts in all. Limiter and cooldown are
  combined the same way as GitHub's (`pace`): the cooldown is checked once
  the token is granted, and a call that sat one out queues for a fresh
  token.
- `fetcher.native.jina_api_key` (or `CURIO_JINA_API_KEY`) is sent as
  `Authorization: Bearer <key>`, and never appears in logs or errors.
- At most 2 origin requests per host are in flight
  (`originRequestsPerHost`). A fetch waits for a slot, honoring its
  context. The host cache is checked again once the slot is acquired, so
  fetches queued behind the ones that got a host cached as anti-bot fail
  from the cache instead of sending the request. The slot is released as
  soon as the origin has answered, and is never held while waiting on or
  calling Jina.

**Why:** Every worker called `r.jina.ai` on its own, slept 2/4/8 s
between attempts, ignored `Retry-After` and sent no key, although Jina
429s were the original reason for the fallback policy. Origin fetches had
no per-host limit, so an import heavy on one site sent it up to 16
concurrent requests. That provokes the 403/503s which then get the whole
host cached as anti-bot.

**Waiting, not failing:** local limits block, bounded by the job's context,
instead of returning an error. Failing would spend job attempts on our own
throttling. Only an upstream cooldown longer than the inline cap fails
fast, because sleeping it out would hold a fetch worker. `JobQueue` can't
take a delay yet, so the hint stays on the error.

**Cost:** a worker waiting for a host slot can't pick up another job, so
an import dominated by one site proceeds at roughly that site's pace
(two requests at a time) instead of sixteen. That is the point for the
site, and mixed imports barely notice.

---

## URL normalization: fetch-equivalent and idempotent

**Decision:** `urlutil.Normalize` output is both the dedup key and the URL
the fetcher requests, so every rule keeps the URL pointing at the same
resource, and normalizing twice changes nothing.

- Only absolute `http`/`https` URLs with a host are accepted; anything
  else is `ErrInvalidURL`. That covers `POST /v1/bookmarks` (400), the
  import endpoint (counted under its filter reasons) and MCP, which goes
  through the API.
- Host: lowercased; IPv6 literals keep their brackets; a host containing
  `:` must be an IP literal. The default port is dropped. An empty path
  becomes `/`. The fragment is dropped.
- Query: split on `&` only. Empty pairs and tracking parameters are
  dropped. Well-formed pairs are re-encoded exactly as `url.Values.Encode`
  writes them. A pair that doesn't decode, or contains `;`, is kept as is,
  with only its spaces and non-ASCII bytes percent-encoded the way a
  browser sends them. A key without `=` stays without one. Pairs are
  stable-sorted by decoded key.
- `ref` is no longer a tracking parameter: it is also a branch or version
  selector.
- YouTube URLs are canonicalized only when the ID matches
  `^[A-Za-z0-9_-]+$`; the YouTube fetcher rejects other IDs permanently.
- `FuzzNormalize` asserts that every accepted output is an http(s) URL
  with a host and a fixed point of `Normalize`.

**Why:** the old normalizer dropped data and wasn't idempotent. `?a=1;b=2`
lost its whole query and `?q=%zz` lost that pair, because `url.Query()`
discards what it can't parse. `?ref=main` lost the branch, and `?flag`
became `?flag=`. `v=abc%26list%3Dx` was pasted unescaped into the watch
URL, and a second pass shortened it again. `https://[::1]:443/x` lost its
brackets. `javascript:`, `file:`, `mailto:`, `https:example.com/x` and
`https:///x` were all accepted, so `curio add` created a document whose
fetch failed five times. The CLI importers normalize before the daemon
does it again, so every non-idempotent step split one bookmark into two
documents.

**No original-URL column:** after these rules the stored URL requests the
same resource as the input. The two differ only in fragment, default
port, the case of scheme and host, parameter order, canonical
percent-encoding and tracking parameters, none of which change what a
server returns. So there is no schema change.

**One-time key change:** a few URL shapes normalize differently now: a
bare origin (`https://example.com` → `https://example.com/`), valueless
parameters, queries with `;` or undecodable pairs, and `ref=`. A bookmark
of one of those shapes that is imported again after the upgrade creates a
second document under the new key. Every other key is unchanged.

---

## Subprocess fetchers: kill the process group, cap the output

**Decision:**

- Web2MD and YouTube run their tool through `runCapped`
  (`internal/fetcher/exec.go`). On Unix the tool gets its own process
  group, and when the timeout or the job's context ends the whole group
  gets SIGKILL (`exec_unix.go`). Elsewhere only the tool is killed; the
  package still builds there, but only darwin/arm64 ships. `WaitDelay`
  (2 s) bounds how long a descendant that left the group can keep the
  output pipes open.
- A run cut short fails with an error wrapping `ctx.Err()` that names the
  timeout, so a timeout stays retryable and a shutdown lets the worker
  requeue the job.
- Web2MD's stdout is capped at `maxResponseBytes`. Past the cap the pipe
  write fails, which stops the tool, and the fetch fails permanently with
  `ErrTooLarge`. Stderr keeps its first 64 KiB for error messages.
- At most `YouTubeOptions.MaxConcurrent` (default 2) yt-dlp processes run
  at once. A fetch queues for a slot, honoring its context, before its
  timeout starts. The daemon's `RateLimited` wrapper still limits how fast
  processes start; it never limited how many run.
- Tests run the fakes by re-executing the test binary (`TestMain` switches
  on `CURIO_FAKE_TOOL`) instead of writing shell scripts at test time.

**Why:** `exec.CommandContext` kills only the direct child. A helper it
spawned (yt-dlp and web2md's Node process both can) inherited the output
pipes, and `Wait` blocked until that helper exited: a probe with a 1 s
timeout returned after 8 s and left the helper running. That also
stretched the daemon's bounded shutdown. Output went into unbounded
buffers, and a timeout surfaced as `signal: killed`. The token bucket in
front of YouTube let up to 16 yt-dlp processes run at once.

---

## Chrome profiles carry their own User-Agent and sec-ch-ua

**Decision:** One table in `transport.go` (`chromeProfiles`) maps each
`fetcher.native.backend` profile to its TLS/HTTP2 fingerprint, its Chrome
major version, its User-Agent and its `sec-ch-ua`. The Native fetcher
sends the selected profile's User-Agent and `sec-ch-ua`; the stock
backend sends the latest profile's. A `user_agent` override is still sent
as is, but one that doesn't name the profile's Chrome version logs a
warning when the fetcher is built. A test checks that every entry names
one version throughout.

**Why:** The note above asked users to keep `backend` and `user_agent`
coherent, but only prose enforced it. `sec-ch-ua` was hard-coded to Chrome
133 and couldn't be configured at all, so `backend: chrome_120` sent a
Chrome 120 TLS fingerprint with Chrome 133 headers. The `sec-ch-ua` values
are copied from real Chrome of each version, because the GREASE brand and
the brand order change from one version to the next. The old 133 value
had its brands in the wrong order.

---

## Store boundary: consumers see interfaces, depguard enforces it

**Decision:**

- Everything the API needs from storage is on the `internal/store`
  interfaces. `DocumentStore` has `ListWithLastError`,
  `ListIDsWithContent` and `CountByState`; `BookmarkStore` has `Count`; and
  `JobStore` embeds `JobQueue` and adds `ListWithDoc`, `CountByStatus`,
  `MetricsByKind`, `DeleteByStatus` and `PruneOlderThan`.
- The queue interface is split by role. Workers (`jobs.Worker`,
  `jobs.Deps`) take `JobQueue`, which only claims and transitions jobs; the
  API takes `JobStore`.
- List methods take options structs (`ListDocumentsOpts`, `ListJobsOpts`),
  so a cursor can be added without another signature change.
- depguard's `store-boundary` rule denies `internal/store/sqlite` to every
  non-test file outside `cmd/curio-daemon`, the sqlite package itself and
  the test-support packages (`internal/store/sqlite/sqlitetest`,
  `internal/api/apitest`). Its `no-test-deps-in-prod` rule denies
  `testing`, testify and those test-support packages to production code.
- `/v1/stats` counts through these methods (`SELECT count(*)`, not a
  listing) and answers 500 when a count fails.

**Why:** `internal/api` imported the SQLite package and type-asserted the
stores to concrete types in seven places to reach methods the interfaces
lacked. Five fell back to a 501 that the one implementation never hit, and
`/v1/stats` silently dropped the fields it couldn't count. A second
implementation would have compiled and then served 501s. `/v1/stats` also
counted bookmarks by decoding up to 100000 rows, on an endpoint
`curio import --follow` polls every 2 s. Separately, `testutil.go` was a
non-test file, so the daemon linked testify. Nothing stopped the next
violation; the lint rules do.

**Interfaces in `store`, not consumer-side interfaces in `api`:** every
consumer (search, insight, jobs, api) already takes `store.*` interfaces,
and a hosted implementation has to provide these methods to serve the API
anyway.

---

## Documents: explicit Create and ApplyFetch, no upsert

**Decision:** `DocumentStore` has no upsert. `Create` is a plain INSERT
that returns `ErrConflict` for an existing `(tenant_id, url)`; ingest's
get-or-create stays private to the store (see "Bookmark ingest" below).
`ApplyFetch` is the only way a fetch result reaches the documents row: one
UPDATE by id that points `current_extraction_id` at the new extraction,
writes `content_type`, `url_canonical`, `title`, `author`, `language` and
`published_at` exactly as given (nil writes NULL), and sets the state to
`pending` until the index step marks it `fetched`.

**Why:** `Upsert` served two callers with different needs. Its ON CONFLICT
branch COALESCEd every nullable column, so a refetch could never clear an
author or canonical URL left by an earlier extraction, yet it always
overwrote `content_type` and `state` and quietly defaulted empty values;
used as get-or-create, it could rewrite the state of a row another request
had just created.

**Verbatim writes:** those columns describe `current_extraction_id`, so a
value the new extraction lacks must not survive from the old one. Clients
already fall back to the URL or the bookmark title when `title` is NULL.
`word_count` is left alone because no fetcher sets it.

---

## Bookmark ingest: one transaction, fetch only for new documents

**Decision:** `POST /v1/bookmarks` and `POST /v1/bookmarks/import` save
each bookmark through `BookmarkStore.Ingest`, one write-first transaction
per bookmark:

1. `INSERT INTO documents ... ON CONFLICT (tenant_id, url) DO NOTHING
   RETURNING id, state`; no row back means the document exists, so it is
   read.
2. The bookmark is inserted linked to that document. `ON CONFLICT
   (tenant_id, url, source) DO NOTHING` returning no row is `ErrConflict`,
   and the rollback takes the document insert with it.
3. A fetch job is inserted (through `insertJob`) only when step 1 created
   the document.

Either everything commits or nothing does. The create endpoint answers
409 for a duplicate bookmark and returns `job_id: ""` with the existing
document's `document_state` for a known URL; the import endpoint counts a
duplicate as skipped and only new documents in `jobs_enqueued`. Fetch and
index payloads are one type, `store.DocumentJobPayload`, built by
`store.NewDocumentJob`.

**Why:** the handlers ran GetByURL, Upsert, bookmark insert and enqueue as
separate autocommit writes, and enqueued whenever the document was
`pending`. Two failures followed:

- An enqueue that failed after the bookmark committed left the document
  `pending` with no job, for good: a retry of the create answered 409
  before reaching the enqueue, and a re-import counted the row as skipped.
  That is the stuck state the permanent-failure hook and `RequeueFetch`
  exist to prevent.
- One URL imported from Chrome, Safari and Firefox and then added by hand
  got four fetch jobs, so every bookmark shared by synced browsers was
  fetched and embedded again.

**Fetch only when created:** no lookup of queued jobs is needed. A
`pending` document already has its fetch or index job; a `fetched` one has
its content; a `failed` or `dead` one is left to `curio refetch`, which
knows the dead-link rules. Documents stranded `pending` by the old path are
healed with `curio refetch --all --state=pending`.

**One transaction per bookmark, not per batch:** measured at about 145 µs
per bookmark, the same as the five autocommit statements it replaces, and
it lets fetch and index workers interleave with a 500-bookmark batch. The
indexes of migration 007 raised it to about 245 µs, from 190 µs on the
machine that re-measured both. The
write comes first so concurrent ingests queue on the write lock through
busy_timeout (see "Job queue claim via atomic UPDATE ... RETURNING"); five
writers ingesting the same URLs produced one document and one job per URL
and no `SQLITE_BUSY`. The import handler stops at the first bookmark after
the client has gone; each committed bookmark stands on its own, so a
re-import resumes.

URL normalization and `importer.Indexable` filtering stay in the handlers,
which report failures differently (400 versus `filtered_by`).

---

## Migrate: goose's Provider, and a context all the way down

**Decision:** `sqlite.Open` and `Migrate` take a context (`PingContext`,
`Provider.Up(ctx)`), and the daemon passes its run context. `Migrate` applies the embedded migrations
through `goose.NewProvider`, never goose's package-level `SetBaseFS` /
`SetDialect` / `Up`.

**Why:** the package-level API reads and writes process globals, so two
databases migrating at once race: four parallel test subtests that each
built a database failed under `-race`. That kept every DB-backed test
serial. The Provider holds its state per instance and is quiet unless
asked (`WithVerbose`), which also ends goose's per-migration `OK` lines in
test output. Both APIs use the same `goose_db_version` table, so existing
homes migrate unchanged.

**Shutdown during a migration:** a cancelled context fails `Migrate`, and
the daemon exits as on any migration error, without reusing the handle
(see "Migrations: rebuilding a table other tables reference").

---

## Folder and host filters: literal input, segment-boundary folders

**Decision:**

- The bookmark folder filter matches the folder itself or any folder under
  it, on path segments and case-sensitively. It compares BINARY ranges
  instead of using LIKE: `folder_path = p OR (folder_path >= p || '/' AND
  folder_path < p || '0')`, where `p` is the input without a trailing `/`.
  `'0'` is the byte after `/`, so the half-open range holds exactly the
  paths under `p/`. `/` alone means no folder filter.
- The search host filter keeps LIKE, since everything after the host is a
  wildcard and DNS names are case-insensitive, but escapes the host's `%`,
  `_` and `\` with `ESCAPE '\'` (`escapeLike` in `internal/store/sqlite`).

**Why:** both filters pasted user input into a LIKE pattern. `/Tech/AI`
matched `/Tech/AIRPLANES`, `/Tech/AI_x` and `/Tech/AI0`, and, because LIKE
folds ASCII case, `/tech/ai/lower`; `/100% Reading` matched `/100X Reading`.
The host filter is reachable from `curio search --host` and from the MCP
`search_bookmarks` tool, where a model supplies the value: `_` matched any
character and a host of `%` matched every document. The obvious fix,
`folder_path = ? OR folder_path LIKE ? ESCAPE ...`, is still wrong: `=` is
case-sensitive and LIKE is not, so `/tech/ai` would match
`/Tech/AI/Agents` but not `/Tech/AI`. The range needs no escaping. (The OR
keeps SQLite from seeking `idx_bookmarks_folder` on it; see "Indexes follow
the queries" for how a filtered page is read.)

---

## API: handler edge cases found by coverage

**Decision:**

- **Paging:** bookmarks, documents and jobs share one page-size rule:
  `?limit` of 1 to 500 is honored, anything else means 50. The bookmark
  list asks the store for one row more than the page and sets
  `next_cursor` only when that row exists.
- **Router errors are problems too:** an unknown route answers 404 and a
  wrong method 405, both `application/problem+json`; the 405 carries an
  `Allow` header. chi fills `Allow` only in its own 405 handler, and its
  `Match` reports every method for a mount point such as
  `/v1/bookmarks`, so the server keeps a mount-free copy of its routes
  (built with `chi.Walk`) to answer which methods a path takes.
- **Bookmark document lookups fail loudly:** listing or getting a
  bookmark whose document can't be read is a 500, not a blank
  `document_state`. `bookmarks.document_id` is `ON DELETE SET NULL`, so a
  dangling ID is an inconsistency.
- **Missing content is a 404:** `GET /v1/documents/{id}/content` for a
  markdown file deleted from disk (which docs/data-model.md presents as
  supported) answers 404 "refetch the document" instead of a 500 carrying
  the absolute path.
- **reindex-all only reindexes documents with content:** it validates
  `?state` as refetch-all does (400 for an unknown state) and enqueues index
  jobs only for documents in that state with a current extraction
  (`DocumentStore.ListIDsWithContent`).

**Why reindex-all needed the second half:** an index job for a document
with no extraction fails permanently, and the permanent-failure hook then
marks the document failed. So `curio reindex --all --state=pending` turned
documents whose first fetch was still in flight into failed ones.
Single-document reindex already refused such a document with 409.

---

## updated_at: written by each statement, not by triggers

**Decision:** Every UPDATE sets `updated_at` in the same statement:
`updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now')` (the `sqlNow`
fragment in `internal/store/sqlite`, the expression the column DEFAULTs
use), or the formatted Go time the statement already binds for another
column (`ClaimNext`, `Requeue`, `RecoverOrphans`, `MarkFailed`'s retry).
Migration 006 drops the `trg_*_updated_at` triggers on documents,
bookmarks, jobs, cluster_runs and clusters; its Down recreates them
verbatim.

**Why:** Each AFTER UPDATE trigger ran a second UPDATE of the row, so every
one-row write cost two (`total_changes()` moved by 2). Worse, RETURNING
reports the row as the statement left it, before the trigger runs:
`ClaimNext` handed back the enqueue time as `UpdatedAt` while the stored
row had the claim time, and so did every job `RecoverOrphans` returned.
The triggers also made `updated_at` impossible to pin in a test, which is
why several tests inserted rows by hand and one comment claimed the claim
was the last write to touch it.

The triggers migration 008 adds on `chunks` are a different kind (see
"Chunks: external-content FTS"): they keep derived tables in step with
their source, and nothing reads those back through RETURNING.

---

## Jobs reference their document through a column

**Decision:** `jobs.document_id TEXT REFERENCES documents(id) ON DELETE SET
NULL` (migration 007) holds the document a fetch or index job works on.
`insertJob` fills it in the INSERT itself with
`json_extract(<payload>, '$.document_id')`, the rule the migration
backfilled existing rows with. `ListWithLastError` finds a document's last
error with `document_id = d.id AND status = 'failed' ORDER BY updated_at
DESC LIMIT 1` on `idx_jobs_document (document_id, status, updated_at)`, and
`ListWithDoc` joins `d.id = j.document_id`. No store read uses
`json_extract`. Enqueueing a job whose payload names a missing document is
an error wrapping `store.ErrNotFound`.

**Why:** Jobs named their document only inside the payload JSON, which no
index can serve. `curio docs` ran, for every document of the tenant before
its sort and LIMIT, a correlated subquery that walked every failed job:
59 ms at 2k documents and 200 failed jobs, 756 ms at 5k and 1k, growing
with documents × failed jobs. `ListWithDoc` joined through `json_extract`
for every tenant job. Nothing defined what deleting a document did to its
jobs.

**SET NULL, not CASCADE:** jobs are the audit trail. Their `last_error`
explains a failure and their durations feed `/v1/metrics`; deleting a
document must not erase that, the rule `bookmarks.document_id` already
follows. SET NULL is also what `curio jobs` already showed for a vanished
document (the job listed with an empty URL), and the state the backfill
gives a payload naming a document that no longer exists, so existing
databases pass `PRAGMA foreign_key_check`.

**The payload keeps `document_id`:** the API returns payloads verbatim and
the handlers decode them; the column is a projection written once at
insert. Deriving it in SQL rather than decoding in Go keeps one rule for
old and new rows, lets a payload that isn't a JSON object still enqueue
(its column is NULL), and means `store.Job` needs no field no Go code
reads.

**In goose's transaction:** `ALTER TABLE ADD COLUMN` with a REFERENCES
clause is allowed when the default is NULL, so no table is rebuilt; Down
drops the index and then the column.

---

## Indexes follow the queries; plans are pinned by tests

**Decision:** Migration 007 builds the index set around the queries the
store runs:

| Index | Serves |
|---|---|
| `idx_jobs_claim (status, kind, run_after, created_at)` | `ClaimNext`; `RecoverOrphans` |
| `idx_jobs_document (document_id, status, updated_at)` | a document's last error (`curio docs`); the FK action when a document is deleted |
| `idx_jobs_tenant_status_updated (tenant_id, status, updated_at)` | `ListWithDoc` by status (`curio jobs`, `--failed`); `CountByStatus`; `MetricsByKind`'s window; `PruneOlderThan`; `DeleteByStatus` |
| `idx_jobs_tenant_updated (tenant_id, updated_at)` | `ListWithDoc` unfiltered (`--all`) or by kind only |
| `idx_documents_tenant_state_updated (tenant_id, state, updated_at)` | `ListWithLastError` by state (`curio docs`, `--failed`); `CountByState`; `ListIDsWithContent`; `DocumentVectors`; `RequeueFetchByStates` |
| `idx_documents_tenant_updated (tenant_id, updated_at)` | `ListWithLastError` unfiltered (`--all`) |
| `idx_bookmarks_tenant_id (tenant_id, id)` | `Bookmarks.List` pages |

They replace `idx_jobs_dispatch`, `idx_jobs_kind` and
`idx_documents_tenant_state`. `idx_documents_tenant_ctype` and
`idx_documents_url_canonical` are dropped: no query read them (the search
`content_type` filter is checked on each hit's document after a
primary-key join), and each cost a write on every document update.

The lists walk their index in `updated_at` (or `id`) order and stop at the
LIMIT, where they used to sort every tenant row first: `ListWithDoc` took
2.3 ms at 4.2k jobs and 6.3 ms at 11k and grew until someone pruned, and a
page of bookmarks sorted every bookmark, so paging through N cost
O(N²/page size). `CountByStatus`, behind the `/v1/stats` that
`curio import --follow` polls every 2 s, no longer builds a temporary
b-tree for its GROUP BY. A bookmark page filtered by source or folder
walks `idx_bookmarks_tenant_id` too, checking the filter per row; the
folder filter's OR never let SQLite seek `idx_bookmarks_folder` anyway.

**Pinned by tests:** curio never runs ANALYZE, so SQLite plans from its
heuristics and the schema alone, and the plans are stable.
`internal/store/sqlite/plans_test.go` runs EXPLAIN QUERY PLAN on the SQL
the store runs, built by the same constants and builders, and asserts the
index and its constraints, and the absence of a temporary b-tree wherever
the order should come from the index. A query or index change that loses
a plan fails there.

**Write cost:** `jobs` now carries four secondary indexes, and every job
insert checks its document. On the machine that measured 1.75 s before
this change, `RequeueFetchByStates` over 50k documents takes 2.4 s, nearly
all of it the job INSERTs (the `document_id` check about 0.3 s of it), and
a bookmark `Ingest` about 245 µs instead of 190 µs. Reads that were
proportional to the table are now proportional to the page, which is the
trade `curio docs` and `curio jobs` need.

---

## Chunks: external-content FTS, derived rows kept by triggers

**Decision:** Migration 008 makes `chunks_fts` an FTS5 index with `chunks`
as its external content: `fts5(text, title, tags, content='chunks',
content_rowid='seq', ...)`.

- `chunks` gains `seq INTEGER PRIMARY KEY`, the index's rowid; `id` stays
  the public TEXT ID, `NOT NULL UNIQUE`. It also gains `title` and `tags`,
  exactly the strings indexed for the chunk.
- AFTER INSERT, DELETE and UPDATE triggers on `chunks` mirror every change
  into the index, deletes supplying the old values. The delete trigger
  also runs `DELETE FROM chunks_vec WHERE chunk_id = old.id`.
- `ReplaceForDocument` runs one `DELETE FROM chunks WHERE document_id = ?`
  and inserts through two statements prepared once per transaction (chunk
  row, vector). `BM25Search` joins `chunks c ON c.seq = chunks_fts.rowid`
  and takes both IDs from `chunks`.

**Why:** Every index job started with two deletes that read the whole
corpus under the write lock. `DELETE FROM chunks_fts WHERE document_id = ?`
filtered on an UNINDEXED column, a full scan. `DELETE FROM chunks_vec
WHERE chunk_id IN (SELECT ...)` made vec0 take its full-scan plan: vec0
only has a point plan for `chunk_id = ?` and never accepts `IN
(subquery)` as a lookup. A 10-chunk `ReplaceForDocument` took 4.1, 7.0
and 10.3 ms at 2k, 8k and 16k chunks, so indexing an import cost the
square of its size. The regular FTS table also stored a second copy of
every chunk's text (`chunks_fts_content`), a title column nothing read and
copies of both IDs, and chunks deleted by a foreign-key cascade from
documents or extractions left their FTS and vector rows behind.

**Why `seq`:** SQLite only guarantees that an INTEGER PRIMARY KEY keeps its
value across VACUUM; an implicit rowid may be renumbered, and so would one
after a future table rebuild. Either would silently detach the index from
its rows.

**Why `title` and `tags` on the chunk:** an external-content delete must
supply exactly the values that were indexed, and a document's title can
change after its chunks are indexed. The UNINDEXED FTS columns are gone:
nothing read `title`, and both IDs come from `chunks` through the rowid.
bm25 only counts tokens in indexed columns, so scores are unchanged; the
migration test compares IDs, order, snippets and scores with the old
query, before and after, both ways.

**Why triggers:** it is the pattern the FTS5 documentation gives for
external content, and it makes every delete path clean up, cascades
included, which the store alone could not. Unlike the `updated_at`
triggers migration 006 dropped, these maintain derived tables, not the row
being written. Inserting the vector stays in Go, since the embedding is
not a `chunks` column. `plans_test.go` pins that the trigger's vector
delete is a vec0 point lookup and that the chunk delete uses
`idx_chunks_document`. With 250-word chunks, a 10-chunk
`ReplaceForDocument` measured 4.4, 10.0 and 17.3 ms at 2k, 8k and 16k
chunks before, and 4.9, 6.0 and 6.3 ms after.

**Never `INSERT OR REPLACE` into `chunks`:** REPLACE deletes the
conflicting row without firing delete triggers (`recursive_triggers` is
off), which would leave its index entry and vector behind.

**Inside goose's transaction:** no foreign key references `chunks` (a test
checks `pragma_foreign_key_list`), so dropping the old table runs no
ON DELETE actions even with foreign keys on, and the version bump commits
with the rebuild. The NO TRANSACTION recipe could not meet its "safe to
run twice" rule here anyway: a rerun would read FTS columns the first run
dropped.

**Cost:** 200k chunks of 250 words (a 1 GB database) migrate in about 9 s
on an Apple M4 Max, most of it re-tokenizing into the new index (the FTS
`'rebuild'` command). The daemon logs `migrating database` before it
starts, so a migration that outlasts the CLI's 15 s auto-start wait shows
in the log tail. The dropped copy of the text (about 400 MB there) goes to
SQLite's freelist and is reused as the database grows. The migration does
not VACUUM, which would rewrite the whole file; to return the space to the
OS now, stop the daemon and run `sqlite3 ~/.curio/curio.db VACUUM`.
