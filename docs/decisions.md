# Decisions

A running log of design decisions, what we picked, and why. New entries go at
the bottom. When a decision is revisited, add a new entry rather than rewriting
the old one — the history is useful. A decision that still stands but changed
in detail gets a **Revised** note appended to its entry.

Add a line to the index below with each new entry, and mark an entry's line
"(revised)" or "(superseded)" when you append such a note to it. Dates are
when the entry was first committed.

- 2026-05-23 — [Language: Go](#language-go)
- 2026-05-23 — [Architecture: daemon + thin clients](#architecture-daemon--thin-clients)
- 2026-05-23 — [Transport: HTTP + JSON](#transport-http--json)
- 2026-05-23 — [MCP server as a sidecar process](#mcp-server-as-a-sidecar-process)
- 2026-05-23 — [Storage: SQLite for v1](#storage-sqlite-for-v1)
- 2026-05-23 — [Job queue: SQLite-backed](#job-queue-sqlite-backed) (revised)
- 2026-05-23 — [Embedding model: nomic-embed-text via Ollama](#embedding-model-nomic-embed-text-via-ollama) (superseded)
- 2026-05-23 — [Daemon lifecycle: PID file + auto-start](#daemon-lifecycle-pid-file--auto-start) (revised)
- 2026-05-23 — [Storage location: `~/.curio` with marker file](#storage-location-curio-with-marker-file)
- 2026-05-23 — [Data model: documents are universal, references are per-source](#data-model-documents-are-universal-references-are-per-source)
- 2026-05-23 — [Multi-tenancy: `tenant_id` on reference tables, not child tables](#multi-tenancy-tenant_id-on-reference-tables-not-child-tables)
- 2026-05-23 — [Fetcher selection: data-driven rules file](#fetcher-selection-data-driven-rules-file)
- 2026-05-23 — [Hybrid search: BM25 + vector + RRF](#hybrid-search-bm25--vector--rrf) (revised)
- 2026-05-24 — [BM25 query sanitization: OR + stopwords](#bm25-query-sanitization-or--stopwords)
- 2026-05-23 — [API: cursor pagination, not offset](#api-cursor-pagination-not-offset)
- 2026-05-23 — [API: all long-running operations are async with job IDs](#api-all-long-running-operations-are-async-with-job-ids)
- 2026-05-23 — [API: search response exposes BM25 and vector scores per chunk](#api-search-response-exposes-bm25-and-vector-scores-per-chunk)
- 2026-05-23 — [API: search knobs are per-request overrides](#api-search-knobs-are-per-request-overrides)
- 2026-05-23 — [API: `tenant_id` is server-side only, never echoed to clients](#api-tenant_id-is-server-side-only-never-echoed-to-clients)
- 2026-05-23 — [API: bulk operations live under named endpoints, not `/batch`](#api-bulk-operations-live-under-named-endpoints-not-batch)
- 2026-05-23 — [API: `/v1/documents/{id}/references` returns a shape that grows additively](#api-v1documentsidreferences-returns-a-shape-that-grows-additively)
- 2026-05-23 — [SQLite build tags](#sqlite-build-tags)
- 2026-05-23 — [SQLite DSN: per-connection pragmas via mattn's query params](#sqlite-dsn-per-connection-pragmas-via-mattns-query-params)
- 2026-05-23 — [Migrations do not set PRAGMA journal_mode](#migrations-do-not-set-pragma-journal_mode)
- 2026-05-23 — [Job queue claim via atomic UPDATE ... RETURNING](#job-queue-claim-via-atomic-update--returning)
- 2026-05-23 — [Ollama: native install, not containerized](#ollama-native-install-not-containerized)
- 2026-05-23 — [Default fetcher is Go-native, not a Node subprocess](#default-fetcher-is-go-native-not-a-node-subprocess) (revised)
- 2026-05-23 — [Importers: CLI parses, daemon receives lists](#importers-cli-parses-daemon-receives-lists)
- 2026-05-23 — [HTML export is a first-class importer source](#html-export-is-a-first-class-importer-source)
- 2026-05-23 — [HTML parser walks recursively, finds <DL> inside <DT>](#html-parser-walks-recursively-finds-dl-inside-dt)
- 2026-05-23 — [Worker pool: N workers via the same atomic ClaimNext](#worker-pool-n-workers-via-the-same-atomic-claimnext) (revised)
- 2026-05-23 — [Marker file's schema_version is synced from the DB after migrations](#marker-files-schema_version-is-synced-from-the-db-after-migrations)
- 2026-05-23 — [Chunker enforces a 3500-char hard cap (not just 384 words)](#chunker-enforces-a-3500-char-hard-cap-not-just-384-words) (revised)
- 2026-05-23 — [Embedder passes num_ctx=8192 to Ollama, chunker defaults to 384 words](#embedder-passes-num_ctx8192-to-ollama-chunker-defaults-to-384-words) (revised)
- 2026-09-24 — [Indexer: embed in batches of 32; `embedding.timeout_seconds`](#indexer-embed-in-batches-of-32-embeddingtimeout_seconds) (revised)
- 2026-05-24 — [Document state follows job outcome via OnPermanentFailure hook](#document-state-follows-job-outcome-via-onpermanentfailure-hook)
- 2026-05-24 — [Fallback strategy: only Jina for content-came-back cases](#fallback-strategy-only-jina-for-content-came-back-cases)
- 2026-05-24 — [Anti-bot mitigation: browser-thorough headers, not just User-Agent](#anti-bot-mitigation-browser-thorough-headers-not-just-user-agent)
- 2026-06-04 — [Anti-bot mitigation: pluggable TLS/HTTP2 fingerprint backend (uTLS)](#anti-bot-mitigation-pluggable-tlshttp2-fingerprint-backend-utls) (revised)
- 2026-06-04 — [PDF fetcher: two-tier, pure-Go local then Jina](#pdf-fetcher-two-tier-pure-go-local-then-jina)
- 2026-05-24 — [CLI defaults: happy-path views; debug paths are opt-in](#cli-defaults-happy-path-views-debug-paths-are-opt-in)
- 2026-05-24 — [Jobs lifecycle: prune/delete, no nuke-all path](#jobs-lifecycle-prunedelete-no-nuke-all-path)
- 2026-05-24 — [Safari importer: skip Reading List, require Full Disk Access](#safari-importer-skip-reading-list-require-full-disk-access)
- 2026-06-04 — [Firefox importer: copy the live places.sqlite, prefer the install default](#firefox-importer-copy-the-live-placessqlite-prefer-the-install-default)
- 2026-05-24 — [Jobs list: sort by updated_at, show timestamp](#jobs-list-sort-by-updated_at-show-timestamp)
- 2026-05-24 — [`curio status`: CLI version, daemon version, disk usage](#curio-status-cli-version-daemon-version-disk-usage)
- 2026-05-24 — [CLI hides `next_attempt` for terminal-status jobs](#cli-hides-next_attempt-for-terminal-status-jobs)
- 2026-05-23 — [What's deferred from the v1 API](#whats-deferred-from-the-v1-api)
- 2026-05-25 — [PatternDispatcher: host-based fetcher routing](#patterndispatcher-host-based-fetcher-routing) (superseded)
- 2026-05-25 — [YouTube fetcher: yt-dlp over API/scraping](#youtube-fetcher-yt-dlp-over-apiscraping) (revised)
- 2026-05-25 — [GitHub fetcher: REST API, no clone](#github-fetcher-rest-api-no-clone)
- 2026-05-25 — [Per-fetcher rate limiting](#per-fetcher-rate-limiting) (revised)
- 2026-05-25 — [YouTube URL normalization](#youtube-url-normalization) (revised)
- 2026-07-05 — [GitHub issues, PRs, and wiki pages](#github-issues-prs-and-wiki-pages)
- 2026-07-05 — [Dead-link detection: hard 404/410 + soft-404 heuristics](#dead-link-detection-hard-404410--soft-404-heuristics) (revised)
- 2026-07-05 — [fetcher_rules.yaml: mtime-polled hot reload, keep-last-good](#fetcher_rulesyaml-mtime-polled-hot-reload-keep-last-good)
- 2026-07-05 — [find_related: stored-vector mean-pooling, not title search](#find_related-stored-vector-mean-pooling-not-title-search) (revised)
- 2026-07-06 — [Insight layer: kNN-graph clustering + labeled interests (M4)](#insight-layer-knn-graph-clustering--labeled-interests-m4)
- 2026-07-06 — [LLM generation client (`generator.Generator`)](#llm-generation-client-generatorgenerator) (revised)
- 2026-07-06 — [Retrieval eval harness](#retrieval-eval-harness)
- 2026-07-06 — [M6 (planned): RAG / Q&A synthesis + SOTA natural-language search](#m6-planned-rag--qa-synthesis--sota-natural-language-search)
- 2026-07-06 — [nomic-embed-text task prefixes (`search_document:` / `search_query:`)](#nomic-embed-text-task-prefixes-search_document--search_query) (revised)
- 2026-07-06 — [Insight clustering quality: the "general-reading" mega-cluster (known limitation)](#insight-clustering-quality-the-general-reading-mega-cluster-known-limitation)
- 2026-09-09 — [Host-cache hits are permanent failures](#host-cache-hits-are-permanent-failures) (revised)
- 2026-09-24 — [Local API: loopback only, no token, browsers shut out](#local-api-loopback-only-no-token-browsers-shut-out)
- 2026-09-24 — [Single daemon per home: flock on daemon.pid, bind before touching the DB](#single-daemon-per-home-flock-on-daemonpid-bind-before-touching-the-db) (revised)
- 2026-09-24 — [Interrupted vs. orphaned jobs](#interrupted-vs-orphaned-jobs)
- 2026-09-24 — [Config: strict keys, legacy `workers` folded in at load](#config-strict-keys-legacy-workers-folded-in-at-load) (revised)
- 2026-09-24 — [Refetch: state reset and fetch job in one transaction](#refetch-state-reset-and-fetch-job-in-one-transaction)
- 2026-09-24 — [Migrations: rebuilding a table other tables reference](#migrations-rebuilding-a-table-other-tables-reference)
- 2026-09-24 — [Fetcher errors: one typed status model](#fetcher-errors-one-typed-status-model)
- 2026-09-24 — [GitHub: secondary rate limits and a shared cooldown](#github-secondary-rate-limits-and-a-shared-cooldown)
- 2026-09-24 — [Fetchers: one cap on every response body](#fetchers-one-cap-on-every-response-body)
- 2026-09-24 — [Host cache: only host-wide verdicts, under the host that gave them](#host-cache-only-host-wide-verdicts-under-the-host-that-gave-them) (revised)
- 2026-09-24 — [Login-wall heuristic: www and apex are the same site](#login-wall-heuristic-www-and-apex-are-the-same-site) (revised)
- 2026-09-24 — [Fetch politeness: shared Jina pacing, per-host origin gate](#fetch-politeness-shared-jina-pacing-per-host-origin-gate) (revised)
- 2026-09-24 — [URL normalization: fetch-equivalent and idempotent](#url-normalization-fetch-equivalent-and-idempotent)
- 2026-09-24 — [Subprocess fetchers: kill the process group, cap the output](#subprocess-fetchers-kill-the-process-group-cap-the-output)
- 2026-09-24 — [Chrome profiles carry their own User-Agent and sec-ch-ua](#chrome-profiles-carry-their-own-user-agent-and-sec-ch-ua) (revised)
- 2026-09-24 — [Store boundary: consumers see interfaces, depguard enforces it](#store-boundary-consumers-see-interfaces-depguard-enforces-it)
- 2026-09-24 — [Documents: explicit Create and ApplyFetch, no upsert](#documents-explicit-create-and-applyfetch-no-upsert)
- 2026-09-24 — [Bookmark ingest: one transaction, fetch only for new documents](#bookmark-ingest-one-transaction-fetch-only-for-new-documents)
- 2026-09-24 — [Migrate: goose's Provider, and a context all the way down](#migrate-gooses-provider-and-a-context-all-the-way-down) (revised)
- 2026-09-24 — [Folder and host filters: literal input, segment-boundary folders](#folder-and-host-filters-literal-input-segment-boundary-folders)
- 2026-09-24 — [API: handler edge cases found by coverage](#api-handler-edge-cases-found-by-coverage) (revised)
- 2026-09-25 — [updated_at: written by each statement, not by triggers](#updated_at-written-by-each-statement-not-by-triggers)
- 2026-09-25 — [Jobs reference their document through a column](#jobs-reference-their-document-through-a-column)
- 2026-09-25 — [Indexes follow the queries; plans are pinned by tests](#indexes-follow-the-queries-plans-are-pinned-by-tests) (revised)
- 2026-09-25 — [Chunks: external-content FTS, derived rows kept by triggers](#chunks-external-content-fts-derived-rows-kept-by-triggers) (revised)
- 2026-09-25 — [Worker wakeups: an in-process signal, and idle polls that back off](#worker-wakeups-an-in-process-signal-and-idle-polls-that-back-off) (revised)
- 2026-09-25 — [API: request IDs, one error mapping, logged server errors](#api-request-ids-one-error-mapping-logged-server-errors) (revised)
- 2026-09-25 — [API: tolerant responses, strict requests](#api-tolerant-responses-strict-requests)
- 2026-09-25 — [API: absolute content paths, and hydration errors fail the request](#api-absolute-content-paths-and-hydration-errors-fail-the-request) (revised)
- 2026-09-25 — [API: filters are validated, sizing knobs default](#api-filters-are-validated-sizing-knobs-default)
- 2026-09-25 — [Clients: one discovery, an explicit daemon environment, a signal context](#clients-one-discovery-an-explicit-daemon-environment-a-signal-context)
- 2026-09-25 — [Client errors: a typed APIError, and "unreachable" means never connected](#client-errors-a-typed-apierror-and-unreachable-means-never-connected)
- 2026-09-25 — [MCP sidecar: restart an unreachable daemon, retry once](#mcp-sidecar-restart-an-unreachable-daemon-retry-once) (revised)
- 2026-09-25 — [List pagination: keyset on (timestamp, id)](#list-pagination-keyset-on-timestamp-id)
- 2026-09-25 — [API: the spec is the contract, checked by tests](#api-the-spec-is-the-contract-checked-by-tests) (revised)
- 2026-09-25 — [Toolchain: the go directive is the build toolchain, govulncheck gates it](#toolchain-the-go-directive-is-the-build-toolchain-govulncheck-gates-it)
- 2026-09-25 — [Releases: gated on CI, pinned, least privilege](#releases-gated-on-ci-pinned-least-privilege)
- 2026-09-25 — [Lint: a measured linter set, zero issues, explained suppressions](#lint-a-measured-linter-set-zero-issues-explained-suppressions)
- 2026-09-25 — [Ollama: one client, one sentinel pair, a pull that keeps trying](#ollama-one-client-one-sentinel-pair-a-pull-that-keeps-trying) (revised)
- 2026-09-25 — [Insight: skip non-finite document vectors, don't fail the run](#insight-skip-non-finite-document-vectors-dont-fail-the-run)
- 2026-09-25 — [CLI: exit 130 on interrupt, a usage hint on usage errors](#cli-exit-130-on-interrupt-a-usage-hint-on-usage-errors)
- 2026-09-25 — [Daemon startup: a starting API while migrating, clients that wait on progress](#daemon-startup-a-starting-api-while-migrating-clients-that-wait-on-progress) (revised)
- 2026-09-26 — [Chrome backend: plain http carries an explicit :80](#chrome-backend-plain-http-carries-an-explicit-80)
- 2026-09-26 — [TLS certificate failures are permanent, never Jina, never host-cached](#tls-certificate-failures-are-permanent-never-jina-never-host-cached)
- 2026-09-26 — [YouTube: caption tracks by an exact pattern, not `en.*`](#youtube-caption-tracks-by-an-exact-pattern-not-en)
- 2026-09-26 — [YouTube: a failed caption download leaves a partial, not a failed fetch](#youtube-a-failed-caption-download-leaves-a-partial-not-a-failed-fetch)
- 2026-09-26 — [YouTube: a shared cooldown after a 429](#youtube-a-shared-cooldown-after-a-429)
- 2026-09-26 — [Page verdicts: one judge for every page, bot challenges included](#page-verdicts-one-judge-for-every-page-bot-challenges-included) (revised)
- 2026-09-26 — [Jina answers are judged like the origin's pages](#jina-answers-are-judged-like-the-origins-pages) (revised)
- 2026-09-26 — [Search leaves out failed and dead documents](#search-leaves-out-failed-and-dead-documents)
- 2026-09-27 — [Cross-site redirects: judged where they land](#cross-site-redirects-judged-where-they-land)
- 2026-09-27 — [Error pages whose status is hidden](#error-pages-whose-status-is-hidden)
- 2026-09-27 — [Jina requests identify as curio](#jina-requests-identify-as-curio)
- 2026-09-27 — [Queue gate: pause, throttle and schedule, persisted in SQLite](#queue-gate-pause-throttle-and-schedule-persisted-in-sqlite)
- 2026-09-27 — [Embedding model and per-home width](#embedding-model-and-per-home-width)
- 2026-09-27 — [Embedding drift: the marker records the build, healthz reports a change](#embedding-drift-the-marker-records-the-build-healthz-reports-a-change)
- 2026-09-27 — [Embeddings never truncate; an over-long chunk fails at once](#embeddings-never-truncate-an-over-long-chunk-fails-at-once)
- 2026-09-27 — [sqlite-vec: NEON distance kernels on arm64](#sqlite-vec-neon-distance-kernels-on-arm64)
- 2026-09-25 — [Open questions](#open-questions)

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
trivial for SQLite.

**Revised:** a fixed poll stopped being fine once the daemon ran 21 worker
goroutines, each claiming every 500 ms: see "Worker wakeups: an in-process
signal, and idle polls that back off".

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

**Revised (2026-09):** The startup guard exists: `checkMarker` in
`cmd/curio-daemon` refuses to start when `config.yaml`'s `embedding.model` or
`embedding.dim` differs from `.curio-meta.json`, with one error naming both
files and values and the fix (set them back, or use another `CURIO_HOME`).
It refuses *any* change, same dimension included, so `reindex` does not
enable a swap: the daemon won't run under the new model to reindex with it.
`reindex` re-embeds with the configured model, for chunker and prefix
changes and new tags. A supported swap (update the marker, rebuild
`chunks_vec`, reindex, all behind the guard) is future work, tracked in
`docs/roadmap.md`. There is no `--reason` flag.

**Revised (2026-09-27):** the width is now each home's, fixed when the home
is created and recorded in its marker (format 2), and `chunks_vec` is sized
from it by `sqlite.EnsureVectorIndex`. Another embedding model, of any
width, means a new home: `curio up --fresh` moves the old one aside and the
bookmarks are imported again. The guard refuses a mismatch and, before it,
a home from before formats. See "Embedding model and per-home width".

**Superseded (2026-09-27):** new homes embed with `qwen3-embedding:0.6b` at
1024 dimensions; see "Embedding model and per-home width".

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

**Revised (2026-09):** the bound is `store.MaxSearchK` (still 100). It moved
out of `internal/search` because config validation needs it, and importing
the search engine for a constant linked it into the CLI and `curio-mcp`.

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

**Status:** implemented as keyset pages on (timestamp, id); see "List
pagination: keyset on (timestamp, id)" below.

**Decision:** List endpoints use opaque cursors (`?cursor=...` + `next_cursor`
in the response), not offset/limit.

**Why:** Offset is buggy under concurrent writes — rows land between page
fetches and clients silently skip data. Cursors are stable on SQLite via
`WHERE id > :cursor ORDER BY id LIMIT N`. Cost is the same.

---

## API: all long-running operations are async with job IDs

**Status:** imports are synchronous per batch, and `GET /v1/jobs/{id}` is
routed; see "API: the spec is the contract, checked by tests" below.

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

**Status:** not implemented; removed from the spec until a client needs it
(see "API: the spec is the contract, checked by tests").

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
`file:/path/curio.db?_busy_timeout=5000&_fk=true&_journal_mode=WAL&_synchronous=NORMAL&_txlock=immediate`.

**Why:** SQLite's `foreign_keys` PRAGMA is *per-connection* and defaults to
OFF — every connection in `database/sql`'s pool must turn it on, or FK
constraints are silently unenforced. Running `PRAGMA foreign_keys = ON` in
migrations only affects that one connection. The DSN-level params apply to
every new pooled connection.

Note: `_pragma=foreign_keys(1)` syntax is for `modernc.org/sqlite`, NOT
`mattn/go-sqlite3`. They look similar; mixing them silently no-ops.

**Immediate transactions:** the DSN also sets `_txlock=immediate`, so every
`BeginTx` issues `BEGIN IMMEDIATE` and takes the write lock at once.
Without it every transaction was deferred: one that had read and then
wrote, while another connection held the lock, got `SQLITE_BUSY` at once
(measured 5 µs), because SQLite skips the busy handler for that upgrade,
or `SQLITE_BUSY_SNAPSHOT` if a write had committed since its read. The
same transaction under `_txlock=immediate` waited 372 ms and committed.
Every transaction curio opens writes (ingest, refetch, orphan recovery,
chunk replacement, cluster replacement, goose's migrations), so taking the
lock at BEGIN costs nothing, busy_timeout now covers each of them whole,
and statement order inside them no longer matters; the "write first"
rule that comments used to carry is gone. mattn ignores
`sql.TxOptions.ReadOnly`, so reads never open a transaction; as
autocommit statements in WAL mode they never wait on a writer.

**Pool policy:** `SetMaxOpenConns(32)`, `SetMaxIdleConns(32)`,
`SetConnMaxIdleTime(5 * time.Minute)`. database/sql keeps two idle
connections by default, so under the worker pools connections were closed
and reopened constantly: 214 closed in a 0.2 s burst of 21 claimers, each
reopen costing about 0.7 ms (open, pragmas, sqlite-vec) against 2.4 µs for
a query on a pooled connection. A writer waiting out busy_timeout holds
its connection, so the cap sits above the 21 default worker goroutines
with room for the API, and reads don't queue behind waiting writers. The
idle timeout releases connections and their page caches once the daemon
has been idle for five minutes. `Open` rejects `":memory:"`, which would
give each pooled connection its own database.

**No split reader/writer pool:** it would add a routing decision to every
store method and, with a one-connection writer, a self-deadlock risk for
any nested use, for little gain at this scale. Revisit if `SQLITE_BUSY`
shows up in the logs.

**When `SQLITE_BUSY` does happen:** with immediate transactions it means
the lock stayed held for the whole busy_timeout. Handlers' writes already
retry through `MarkFailed`'s backoff. The worker's own bookkeeping
(`MarkDone`, `MarkFailed`, `Requeue` and the permanent-failure hook) is
retried with a backoff of 50 ms doubling to 1 s, within the 10 s
bookkeeping budget, on any error except `store.ErrNotRunning`,
`store.ErrNotFound` and `ErrPermanent`: it used to log the failure and move
on, leaving the job `running` until the next restart. Retrying is safe
because the transitions only move a running job and `MarkDocFailed` is
idempotent (hooks must be). It retries any other error rather than
classifying SQLite error codes across the store boundary; the budget
bounds the cost.

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

**Why:** The transaction approach failed under concurrent workers. The
initial SELECT took a read lock in a deferred transaction, and upgrading it
to a write lock for the UPDATE while another worker held the lock fails
with `SQLITE_BUSY` at once: SQLite skips busy_timeout for that upgrade.
The single-statement form acquires the write lock immediately, serializes
cleanly across workers, and is also shorter code. (Transactions are now
immediate, so a read-then-write transaction would wait instead; see
"SQLite DSN". The single statement stays the simpler claim.)

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

**Revised (2026-09):** a Jina answer is no longer accepted on a 200-char
body alone. It is held to the same 500-byte floor as a page from the origin,
and to every other page verdict, and the login-title and login-path rules
were tightened. See "Page verdicts: one judge for every page, bot
challenges included" and "Jina answers are judged like the origin's pages".

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

**Revised again:** idle goroutines no longer poll on a fixed tick; they
wait for the queue's enqueue signal or an idle poll that backs off. See
"Worker wakeups: an in-process signal, and idle polls that back off".

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

**Revised (2026-09-27):** the table above is for nomic-embed-text's
2048-token ceiling. With `qwen3-embedding:0.6b` the bound is exact rather
than a heuristic: its tokenizer (Qwen2Tokenizer) is a byte-level BPE, at
most one token per byte, so a 3500-byte chunk is at most 3500 tokens plus
end-of-text, against a num_ctx of 8192 that is now binding (next entry).
The cap and the 384-word target are unchanged.

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

**Revised (2026-09-27):** with `qwen3-embedding:0.6b` the 8192 is binding.
Ollama checks each input against the smaller of `num_ctx` and the model's
own context, 32K tokens for Qwen3-Embedding, and with `truncate: false` an
input past it fails instead of being cut (see "Embeddings never truncate;
an over-long chunk fails at once"). Document chunks can't reach it: they
are capped at 3500 bytes and the tokenizer is a byte-level BPE, so a chunk
is at most 3500 tokens plus end-of-text, 2.3x headroom. Chunk sizing (384
words, 3500 bytes) is unchanged.

`num_ctx` 4096 would still cover every chunk and roughly halve the model's
KV cache. At 8192 in f16 that cache is 28 layers × 8 KV heads × 128
(head_dim) × 2 (K and V) × 2 bytes = 112 KiB per token, about 896 MiB:
computed from the model's config, not measured. It is a memory
optimization for the per-machine model tiers to measure, and stays 8192
until then.

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

**Revised (2026-09):** the previous chunks stay searchable only while the
index job is retrying. Once it fails for good, the index pool's
permanent-failure hook marks the document `failed`, and search leaves
failed documents out. A document whose index job gives up after a refetch
or a `curio reindex`, say because Ollama was down for the whole retry
window, drops out of search until it is indexed again; its chunks are
kept. See "Search leaves out failed and dead documents".

**Revised (2026-09-27):** "a few seconds even on CPU-only Ollama" was
measured with nomic-embed-text. `qwen3-embedding:0.6b` has about 4.4 times
its parameters, and with `OLLAMA_NUM_PARALLEL` at its default of 1 every
index worker's request waits behind the others inside the same 60 s
timeout. How long a 32-chunk batch takes with the new model is unmeasured;
`curio up` measures one to estimate an import, and the batch size and the
default timeout are to be re-checked against that number.

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

**Revised (2026-09-26):** now `tls-client v1.16.0` with `fhttp v0.6.9`,
the latest release. It caches one transport per `host:port` and uses
`host:443` for any URL without a port, so `http://` and `https://` on one
host shared a transport and broke each other. The chrome backend gives
plain-http requests and redirect hops an explicit `:80` to keep them
apart; see "Chrome backend: plain http carries an explicit :80".

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

**Revised (2026-09-26):** the default `sub_langs` is now
`en,en-(?-i:[A-Z]{2})` rather than `en.*,en`, which also downloaded one
machine translation per caption language the video was uploaded with. See
"YouTube: caption tracks by an exact pattern, not `en.*`".

yt-dlp now runs with `--ignore-errors`, and its caption `WARNING:` lines
are read: `WARNING: Unable to download video subtitles for '<lang>': …`
is a caption track that failed to download. A video whose only matching
tracks all failed is stored as a partial (description only) whose
extraction `error_message` quotes yt-dlp; recovery is `curio refetch <id>`.
See "YouTube: a failed caption download leaves a partial, not a failed
fetch".

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

**Revised (2026-09):** the checks run in `judgePage`, which Jina's answers
go through too, still before every login-wall check. A 404 or 410 that Jina
reports for the target is a dead link as well. See "Page verdicts: one
judge for every page, bot challenges included" and "Jina answers are judged
like the origin's pages".

**Revised (2026-09-27):**

- A redirect that settles on another site's landing page, a homepage or a
  section that kept nothing of the page asked for, is a soft 404 too
  (`looksLikeLandingPage`), under the same kill switch. It is not
  host-cached either, for either host. This rule and the homepage rule
  judge where a redirect landed whatever that page answered: 2xx, 403 or
  503. A 403/503 there is a dead link, not anti-bot, and never goes to
  Jina. See "Cross-site redirects: judged where they land".
- The homepage rule needs a source that names a page once a trailing index
  document is dropped, the landing rule's reading of the source
  (`pageSegments`): `ocw.mit.edu/index.htm` → `ocw.mit.edu/` is the
  homepage canonicalized, not a page redirected to it. The library holds
  three such bookmarks, fetched on 2026-05-24 and 25, before the homepage
  rule existed: `http://ocw.mit.edu/index.htm` (`cd7f3b2f`),
  `https://ocw.mit.edu/index.htm` (`05d9e332`) and
  `http://www.infragistics.com/default.aspx` (`85ef605b`). Any refetch
  would have marked them dead. Still judged dead: a homepage bookmarked
  under an alias (`vaadin.com/home`, `www.kraken.com/en-us`,
  `www.realmatters.com/home/default.aspx`), which no URL rule tells from a
  deleted page.
- `--force` lifts the 409 and nothing else. A forced refetch runs every
  dead-link rule again, so it recovers a URL whose answer changed (a 404
  that came back, a redirect that was fixed), not a page the heuristics
  misjudge: a not-found title, a homepage redirect or a landing redirect is
  judged the same way again. The refetch policy and kill switch paragraphs
  above take per-document `--force` for the escape hatch from a false
  positive; it is one only once the answer changes. The kill switch is the
  only override, daemon-wide, and it takes a daemon restart (there is no
  config reload).

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

**Revised (2026-09):** `VectorSearch` over-fetches on every query, since
every query now leaves out failed and dead documents, so the exclusion no
longer depends on the filter set being non-empty. Failed and dead
documents are never among the results. See "Search leaves out failed and
dead documents".

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

**Revised (2026-09):** the generator and the embedder now share one
`internal/ollama.Client`: the sentinels are `ollama.ErrUnreachable` and
`ollama.ErrModelNotLoaded`, the pull is `Client.Pull`, and the daemon runs
`Client.KeepPulled`, which retries the pull until the model lands instead of
trying once at startup. See "Ollama: one client, one sentinel pair, a pull
that keeps trying".

**Revised (2026-09-27):**

- Every `/api/generate` body carries `"think": false`, never omitted.
  Ollama turns thinking on for a model that supports it when the field is
  missing (0.34.3's release notes show gemma4 with thinking on by default),
  and the reasoning spends the labeler's 120-token budget and its time,
  leaving an empty or unparseable reply and a term-label fallback. A model
  that can't turn thinking off answers 4xx, which the insight engine meets
  with term labels. There is no option to turn it on: no caller wants it.
- `num_ctx` (8192) stays explicit on every attempt, now pinned by a
  raw-JSON test with `think`. Without it Ollama sizes the context from the
  free VRAM (the macOS app since 0.17), and `qwen3:4b-instruct` has a 256K
  window. Label prompts are about 1K tokens; M6 will need more.
- The default model is `qwen3:4b-instruct`; `llama3.2` is retired. The
  per-machine tiers (16 GB `gemma4:12b`, 32 GB `gemma4:26b-a4b-it-qat`,
  64 GB `gemma4:26b`) are in docs/setup.md.
- `generation.model` is never recorded in the marker: the user changes it
  and restarts the daemon, and nothing needs reindexing. A daemon test
  restarts a home with another writing model and finds the marker
  unchanged but for `updated_at`.

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

**Revised (2026-09-27):** the default model is now `qwen3-embedding:0.6b`,
which is instruction-aware on the query side only: queries get
`config.QwenQueryPrefix` and documents nothing (see "Embedding model and
per-home width" for the exact bytes). The mechanism is unchanged, and
nomic's two prefixes are what a home made for nomic-embed-text would set.
The marker now holds a home to its model and width, and refuses a home from
before formats, but still not to its prefixes: change them only with
`curio reindex --all`.

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

**Revised (2026-09):** `Controller.Stop` returns `(stopped bool, err)`.
`stopped` is true only when a daemon for this home was running when Stop
began and is gone when it returns; no daemon, a stale PID file or another
home's daemon on the port is `(false, nil)`, and `curio daemon stop` then
prints "daemon not running" instead of "daemon stopped". Status probes
healthz after reading the lock, which can take up to the daemon's own
Ollama check, so Stop reads the lock again right before signalling: if
the PID it saw no longer holds it, that daemon has exited and is not
signalled (its PID may already be reused), and `ESRCH` from the signal
likewise means stopped, not a "no such process" error. `EnsureRunning`
waiting on a lock holder it didn't spawn now allows
max(StartTimeout, StopTimeout): the holder may be draining after a stop
(up to 20s) rather than starting (15s), and the timeout error says
"starting up or shutting down" instead of blaming a daemon "already
starting".

**Revised (2026-09-25):** the daemon answers from the bind on, as a
starting daemon until it is ready. Startup order is now signals, home,
lock + PID, config + log level, marker check, bind, serve the starting
API, open + migrate, sync the marker, construct dependencies, orphan
recovery per pool, swap in the full API, workers; a failure after the
bind stops serving before the lock is released. Clients no longer wait a
fixed 15s (or 30s for a holder): they wait while the daemon reports
progress, failing after 15s of silence or at a 30 min ceiling, and they
hold `daemon.start.lock` only while spawning. See "Daemon startup: a
starting API while migrating, clients that wait on progress".

---

## Interrupted vs. orphaned jobs

**Decision:**

- The worker's context decides only whether to claim another job, and it
  bounds the handler. Every queue write after the decision to claim runs
  on a context detached from it and bounded by 10s, longer than SQLite's
  5s busy_timeout. That covers `ClaimNext`, `MarkDone`, `MarkFailed`,
  `Requeue` and the permanent-failure hook; all but the claim are retried
  within that bound when they fail (see "SQLite DSN").
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

**Revised (2026-09-27):** `store.EmbeddingDim` is gone. `embedding.dim`
is the home's width: `Validate` takes any value in [1,
`store.MaxEmbeddingDim`] (8192, sqlite-vec's limit), and the daemon
refuses a value that differs from the one the home's marker records, since
`chunks_vec` is sized from the marker. See "Embedding model and per-home
width".

---

## Refetch: state reset and fetch job in one transaction

**Decision:** `DocumentStore.RequeueFetch` and `RequeueFetchByStates` reset
the document(s) to `pending` and insert the fetch job(s) in one
transaction: the UPDATE, then the INSERTs. Everything commits or nothing
does.

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
documents. Past that, a worker write that waits out the whole
busy_timeout fails as busy and is retried within the worker's 10 s
bookkeeping budget (see "SQLite DSN"); only a bulk transaction longer than
that leaves a job `running` until the next start recovers it as an orphan.
Chunked transactions are the fix if corpora get there.

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

**Revised (2026-09-27):** Native's 403 and 503 are `ErrAntiBot` unless the
request was redirected onto a page the redirect verdicts judge: another
site's login page, or, with dead-link detection on, the site's homepage or
another site's landing page. That verdict (`ErrDeadLink`, or the offsite
login wall) is a `PermanentError` and still carries the origin's
`*HTTPStatusError`. See "Cross-site redirects: judged where they land".

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

**Revised (2026-09):**

- **Jina verdicts never write the cache.** Jina's answers are now judged
  (see "Jina answers are judged like the origin's pages"). A rejected
  answer counts as Jina's verdict, exactly like the thin answer before it,
  so `settle` still applies only the origin's own host-wide verdict. A dead
  link seen through Jina is final and uncached, and a transient status the
  target gave Jina is retryable and uncached.
- **Anti-bot needs the origin's status.** `hostVerdict` caches anti-bot
  only when the chain carries the origin's `*HTTPStatusError` (a 403/503).
  A bot challenge recognized in page content is page-level (see "Page
  verdicts: one judge for every page, bot challenges included").
- **A redirect onto a cached host fails from the cache.** When the
  origin's error carries a host-wide verdict for a host other than the one
  requested, `Fetch` checks that host's entry before calling Jina, and a
  fresh one fails the fetch with the same `PermanentError` that host's own
  URLs get. The first failure for a host still stays retryable. Before,
  `Fetch` checked only the requested host, so a verdict cached under a
  redirect target never stopped the retries of the URL that reached it: on
  2026-09-26 a `mobile.nytimes.com` URL that redirected to another host
  made five origin and Jina attempts (17:58:51, 18:00:01, 18:02:03,
  18:06:03, 18:14:04; `attempts=5`, `last_error` `fetch failed: jina: HTTP
  403 Forbidden (after native: HTTP 403 Forbidden: …)`). Judging Jina's
  challenge pages would have made that common: every Cloudflare-protected
  bookmark whose old URL redirects to another host
  (`nsis.sourceforge.net` → `nsis.sourceforge.io`, `wrapbootstrap.com` →
  `wrapmarket.com`) would have cost five origin and five Jina requests over
  about 15 minutes. It now costs at most one origin request per retry,
  bounded by the per-host gate.

**Revised (2026-09-27):**

- **Jina's own 403 is Jina's trouble,** with 401 and 402: r.jina.ai's CDN
  challenging curio, not a verdict about the target, whose status comes in
  Jina's warning. `jinaAnswered` had taken it for one since the challenges
  began on 2026-08-30: 400 permanent page-level failures, 402 origin-403
  first failures that wrote the host cache, and 428 permanent cache hits
  served from those entries. With `cf-mitigated: challenge` it also pauses
  every Jina call. See "Jina requests identify as curio". Refined the same
  day: a 403 whose reason names the target's host is Jina refusing that
  domain, a verdict like its other 4xx refusals; see "Jina refusing a
  target is a verdict".
- **A redirect onto another site's landing or login page is page-level,
  whatever it answered.** A landing page (a dead link) and a login page (a
  final login wall; after a 403 or 503 only its path counts, since no page
  is read) cache neither the requested nor the answering host, whatever
  that page answered: 2xx, 403 or 503. So the anti-bot verdict above needs
  a 403/503 that no redirect verdict explains: not one after a redirect
  onto another site's login path, nor, with dead-link detection on, onto
  the site's homepage or another site's landing page. A 403/503 from any
  other page on another site is still the anti-bot verdict above, keyed by
  that site. The site-wide login verdict
  needs a redirect that stays on the site.
- **The page-level verdicts above, now:** none is cached. Thin text, no
  article, a login-like title, and a challenge or 403/503 error page
  recognized in content fail permanently once every configured extraction
  path has answered. A redirect onto the homepage or another site's landing
  page (a dead link, detection on) or onto another site's login page
  (`errOffsiteLoginWall`) is final at once, without Jina. An error page
  naming another 5xx (`errServerErrorPage`) is retried and never sent to
  Jina. See "Cross-site redirects: judged where they land" and "Error pages
  whose status is hidden".

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

**Revised (2026-09):** `loginPathRE` matches a whole path segment, `login`,
`signin`, `signup` or `authwall`, optionally with a file extension
(`/login`, `/uas/login`, `//user/login.php`, `/Login.aspx`), not a segment
that merely starts with one. Its `\b` took `-` for a boundary, so a slug
canonicalization such as Stack Overflow's `/questions/63177503` →
`/questions/63177503/login-cognito-…` read as a redirect onto the site's
login page: a site-wide login wall that, with Jina off or rejecting,
cached the whole host for 15 minutes (daemon.log, 2026-05-24T14:04:30;
reproduced). Every real login redirect in the log (`/uas/login`,
`//user/login.php`, `/s/login/`, `/auth/login/`, `/auth/v3/signin`,
`/signup/credentials`) still matches.

**Revised (2026-09-27):** a redirect to another host is no longer flagged
by itself. Only a redirect onto another site's login page (its path or its
title) is a login wall there, final and never cached; another site's landing
page is a dead link, and any other destination is judged like any page. See
"Cross-site redirects: judged where they land".

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

**Revised (2026-09):** `pace` checks the cooldown twice: before queueing
in the limiter, so a cooldown already longer than the inline cap fails
fast, and again once the token is granted, so a 429 that arrived while
the call was queued is still seen. Checking only after the token made
every caller wait its turn for a token it would then not use: at the
keyless 20 a minute the 16th fetch worker waited about 45 s just to fail,
and each of them spent a token a later call needed. A short cooldown (up
to the cap) still queues first, as before. GitHub's calls go through the
same `pace`.

**Revised (2026-09-27):** a Jina 403 carrying `cf-mitigated: challenge`
extends the same cooldown, by its `Retry-After` or else 10 minutes, and logs
one warning; like a long 429 cooldown it fails Jina-bound fetches fast and
retryably. Jina requests send curio's own User-Agent. See "Jina requests
identify as curio".

**Revised (2026-09-27):** the challenge warning is logged once per pause.
`cooldown.extend` reports whether it started a pause (none was in effect),
and only then does `extendJinaCooldown` warn; the challenged answers of
calls already in flight only extend it, silently. GitHub's call ignores the
result. The shared cooldown's end is also what Jina's health reports as
`paused` (see "Fetch upstream health: Jina's calls are tracked and
reported").

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

**Revised (2026-09-27):** the profile's User-Agent and `sec-ch-ua`, and a
`user_agent` override, go to origins only. Jina requests identify as curio,
since r.jina.ai's Cloudflare challenges a browser User-Agent. See "Jina
requests identify as curio".

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
each bookmark through `BookmarkStore.Ingest`, one transaction per
bookmark:

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
machine that re-measured both. The transaction takes the write lock at
BEGIN (see "SQLite DSN"), so concurrent ingests queue on it through
busy_timeout; five writers ingesting the same URLs produced one document
and one job per URL and no `SQLITE_BUSY`. The import handler stops at the
first bookmark after the client has gone; each committed bookmark stands
on its own, so a re-import resumes.

URL normalization and `importer.Indexable` filtering stay in the handlers,
which report failures differently (400 versus `filtered_by`).

---

## Migrate: goose's Provider, and a context all the way down

**Decision:** `sqlite.Open` and `Migrate` take a context (`PingContext`,
`Provider.Up(ctx)`), and the daemon passes its run context. `Migrate`
applies the embedded migrations through `goose.NewProvider`, never goose's
package-level `SetBaseFS` / `SetDialect` / `Up`.

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

**Checkpoint after migrating:** when `Migrate` applied at least one
migration, it runs `PRAGMA wal_checkpoint(TRUNCATE)`. A migration that
rewrites a table writes every page of it through the WAL, and SQLite's
automatic checkpoints copy those pages back but never shrink the file:
without this, migrations 007 and 008 would leave a WAL about the size of
the jobs and chunks tables (580 MB after 008 on a 1 GB database), which
`curio status` reports as the database's WAL.

**Revised (2026-09-25):** `MigrateWithHooks` lists what is pending with
`Provider.Status` (a read, for a database that has a schema) and applies
it one migration at a time with `Provider.UpByOne`, calling hooks once
before the first and around each; `Migrate` is it with no hooks. The
daemon's hooks report progress on its starting healthz answer and log
each migration, so nothing in the store logs on the daemon's behalf. The
checkpoint after migrating and the returned version are unchanged, and a
failed migration's error names its file and version. Should goose apply
a different migration than the one listed (something else migrating the
same database), that is an error too, not a misreported step. See "Daemon
startup: a starting API while migrating, clients that wait on progress".

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

**Revised (2026-09):** the 405 handler probes the path chi routed on, the
escaped one when the URL has it: `PUT /v1/bookmarks/a%2Fb` answered 405 with
an empty `Allow`, because chi matched `a%2Fb` as one `{id}` while the probe
used the decoded `/v1/bookmarks/a/b`, which matches nothing.

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

**Status:** migration 009 appended `id` to the four list indexes and
replaced `idx_bookmarks_tenant_id`; the current table is in "List
pagination: keyset on (timestamp, id)".

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

**Revised (2026-09):** Migration 010 drops `idx_bookmarks_tenant_source`
and `idx_bookmarks_folder`, which no query reads. `Bookmarks.List` walks
`idx_bookmarks_tenant_created` under every filter, `Count` scans it as a
covering index, `TagsForDocument` and the search source filter use
`idx_bookmarks_document`, and the point operations use the primary key.
The `Count` and `TagsForDocument` plans are pinned too now. Every bookmark
insert, and every source or folder update, no longer maintains two unused
indexes.

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

**Revised (2026-09-25):** the Cost paragraph's answer to a migration that
outlasts the CLI's 15s auto-start wait, a `migrating database` line in
the log tail, was not enough: on a 2.4 GB home, migrations 005 to 010
took 37s and the CLI reported a healthy daemon as failed to start. The
daemon now answers as starting while it migrates, clients wait on its
progress rather than a fixed time, and the log names each migration as
it starts and finishes. See "Daemon startup: a starting API while
migrating, clients that wait on progress".

---

## Worker wakeups: an in-process signal, and idle polls that back off

**Decision:** `store.JobQueue` has `Enqueued(kinds) <-chan struct{}`: a
channel closed once a job of one of `kinds` (any kind when empty) is
enqueued, or put back to pending, through that queue in this process, and
committed. `Worker.Run` takes the channel before it drains, then waits for
the channel, its context, or an idle poll timer that starts at
`PollInterval` (500 ms) and doubles up to the new
`WorkerOptions.MaxPollInterval` (5 s). A claimed job resets the timer; a
failed claim backs off like an empty one.

In the SQLite store the signal lives on the `*DB` that `Jobs`,
`Documents` and `Bookmarks` share, since each enqueues. It fires after the
commit (or after an autocommit statement returns) on `Enqueue`, `Ingest`
when it created a fetch job, `RequeueFetch`, `RequeueFetchByStates` when it
enqueued any, `Requeue` (the kind comes from `RETURNING`) and
`RecoverOrphans` when it requeued any. A rollback or `ErrConflict` never
fires it. Each set of kinds has one channel, closed and replaced when one
of its kinds is signalled, so the goroutines of a pool share one entry and
nothing is started per wait.

**Why:** every worker goroutine ran a 500 ms ticker, and the daemon runs
16 fetch, 4 index and 1 cluster goroutine: about 42 claims a second from
an idle daemon. A claim is an UPDATE, which takes SQLite's write lock even
when it matches nothing. With another connection holding the lock, a claim
that matched nothing waited out busy_timeout and failed with
`SQLITE_BUSY`, where the same predicate as a SELECT answered in 0.2 ms. So
idle polling competed with real writers (ingest, index transactions) and
kept an always-on laptop daemon waking 42 times a second, while new work
still waited up to 500 ms to be noticed. Now new work is claimed at once,
and idle claims fall to about 4 a second (21 goroutines at one per 5 s).

**Per kind:** during an import, fetch jobs are enqueued hundreds of times a
second; waking the index and cluster pools on each would bring the no-op
claims back. A signal still wakes every idle goroutine of the pool it is
for, and all but one of their claims find nothing; that is one claim per
goroutine per signal, and busy goroutines aren't waiting.

**Behind the interface:** a hosted queue can implement `Enqueued` with
Postgres `LISTEN/NOTIFY`; the worker only needs a channel.

**Polling stays for `run_after`:** a retry's backoff is at least 60 s, so
the up to 5 s the capped poll adds to it is noise, and computing the next
due time would cost a query and a timer every idle cycle. The poll also
finds jobs another process enqueued, which no in-process signal sees.

**One goroutine per worker still:** a dispatcher handing jobs to a
semaphore-bounded pool would change the shutdown and drain semantics for
little further gain.

**Revised (2026-09-27):** a worker now asks the queue gate before each
claim. While the gate admits none of its kinds, it waits on the gate's
change signal and reopen time instead of on enqueues and idle polls; see
"Queue gate: pause, throttle and schedule, persisted in SQLite".

---

## API: request IDs, one error mapping, logged server errors

**Decision:**

- Every response carries `X-Request-Id` (chi's generator, or the ID the
  client sent in that header), and every problem body repeats it as
  `request_id`, an RFC 7807 extension member, with the request path as
  `instance`. The access log and the Host/Origin rejection warnings carry
  it too.
- Handlers report failures through `Deps.writeError`, the one place errors
  become statuses: a `requestError` (a parameter, cursor or body field the
  handler refuses) is 400, an oversized body 413, `store.ErrNotFound` 404,
  `store.ErrConflict` 409, anything else 500. A handler that loads the
  resource its path names reports a missing one through `writeLookupError`,
  whose 404 names it: `document "x" not found`.
- A 5xx is logged once, at error level, with the request ID, method, path
  and the full error. When the request's own context is done the client has
  gone and the error is almost always that cancellation, so it is logged at
  info with status 499 (nginx's "client closed request"), which is also
  the status the access log records.
- A panicking handler answers a 500 problem and is logged as one structured
  record with the panic value and stack. `recoverProblem` replaces
  `middleware.Recoverer`; `http.ErrAbortHandler` is re-panicked.
- `writeJSON` encodes before it writes the status, so a value that can't be
  encoded (a NaN) is a logged 500, not a 200 with an empty body.
- Middleware order: request ID, its response header, access log, panic
  recovery, then the Host, Origin and body checks, then the routes.

**Why:** The cause of a 500 appeared nowhere. `writeError` logged nothing,
the access log had no request ID, and nothing tied a client's error to a
log line. `middleware.Recoverer` answered a panic with a bare 500 and no
problem body, and printed a colour-coded multi-line stack into the JSON
`daemon.log`. `writeJSON` wrote the status before encoding and discarded
the encode error. Only two sentinels were mapped, and 404 details leaked
wrap chains: `store: not found`, `related: load document: store: not
found`.

**500 details keep the raw error text:** the clients are the local
operator's own tools (see "Local API: loopback only, no token, browsers
shut out"), and the detail plus the request ID is what makes
`curio daemon logs` searchable.

**Revised (2026-09):** A handler that panics after its status line went out
is logged the same way, once, and then aborted with `http.ErrAbortHandler`,
so net/http cuts the connection. Returning instead let net/http finish the
chunked response, and a client read a 200 with half a body and no error.
The access-log line is skipped for such a request; the panic record
carries its request ID, method and path. `GET /v1/documents/{id}/content`
declares `Content-Length` from the file's size for the same reason: a copy
that fails partway is a short body the client detects, not a complete
answer.

---

## API: tolerant responses, strict requests

**Decision:** Within `/v1`:

- **Responses** are read tolerantly. Clients ignore fields and enum values
  they don't know, and the server may add them, and endpoints, without a
  version bump. Unset optional fields are omitted; the API never sends
  `null`. `internal/client` decodes with `encoding/json`'s defaults, which
  ignore unknown fields.
- **Requests** are strict. The server rejects an unknown field with 400.
  Clients send only the optional fields they set (`omitempty`, `omitzero`),
  so an older daemon rejects only a request that uses a feature it lacks.
  The 400 names the field and the daemon's version and says to restart the
  daemon: `curio daemon stop`, and the next command starts the installed
  one.

**Why:** `api/README.md` promised that clients ignore unknown fields while
the request decoder used `DisallowUnknownFields`, and nothing said which
rule applied to which direction. An ignored request field is a filter or
knob silently not applied: `filters.folder` and `filters.tag` on
`POST /v1/search` were accepted and ignored, so a folder-filtered search
returned unfiltered results. They are gone from `api.Filters` and the
spec, so they are 400s like `weights`. After `brew upgrade`, the new CLI
can reach the old daemon still running; a 400 that says to restart it is
the safe failure there, where before it said only `json: unknown field
"x"`. encoding/json has no error type for an unknown field, so
`decodeJSON` recognizes its message.

---

## API: absolute content paths, and hydration errors fail the request

**Decision:** `Deps.contentPath` is the only place the API builds a file
path, with `filepath.Join` of the content directory and the path the store
records. Every `markdown_path` in a response is absolute, `GET
/v1/documents/{id}`'s `current_extraction.markdown_path` included, and the
CLI prints it as given. A lookup that fails while a response is being
built (a document's current extraction, an interest's members or a
member's document, a search hit's markdown path) fails the request with a
500. So does a current extraction or member document that doesn't exist:
the schema guarantees those rows, so their absence is an inconsistency,
not a missing resource, as for bookmarks ("API: handler edge cases found
by coverage").

**Why:** `GET /v1/documents/{id}` returned the path relative to the
daemon's content directory while every other endpoint returned it
absolute, built with `+ "/"`, and the CLI joined the relative one with its
own home, so a client had to know the daemon's layout. The same code
discarded lookup errors, turning a database error into plausible but wrong
data: a document without `current_extraction`, a hit without a path, an
interest without members.

**Revised (2026-09):** one lookup is exempt: a search or related hit whose
document was deleted after the chunk search read it is skipped, and the
next-ranked document takes its place. That is a concurrent delete, not an
inconsistency, and failing on it answered `POST /v1/search` with a 404 and
`/related` with "document <source> not found" about the wrong document.

---

## API: filters are validated, sizing knobs default

**Decision:** A list filter outside its set is a 400 naming the allowed
values: `GET /v1/documents?state`, `GET /v1/jobs?status` and `?kind`, and
`GET /v1/bookmarks?source` (html included), as refetch-all and reindex-all
already did for `state`. `store.JobStatus` and `store.JobKind` gained
`Valid()`, matching the jobs table's CHECK constraints. Sizing parameters
keep the rule "API: handler edge cases found by coverage" set for `limit`:
one helper, `intQuery`, honors a value in range and treats anything else
(absent, malformed, out of range) as the default. It serves list `limit`
(1..500, default 50), interests `limit` (1..500, 50) and `members` (0..100,
default 5; 0..1000, default 100 on `GET /v1/interests/{id}`), related `k`
(1..100, 10) and metrics `window` (1..86400 seconds, 3600).

**Why:** A typo in a filter read as "nothing matches": `curio docs --state
fecthed` printed "no documents match", `curio jobs --status bogus` "no jobs
match", and `?source=bogus` answered `{"items":[]}`. A wrong filter returns
wrong rows, so it is refused; a wrong size still returns the right rows, so
it falls back. The CLI keeps no enum lists of its own: the server's problem
detail reaches the user as it is. Three hand-written parsers had drifted
apart: interests clamped out-of-range values (so `members=-1` meant none),
while related and metrics fell back to their defaults.

---

## Clients: one discovery, an explicit daemon environment, a signal context

**Decision:**

- `daemonctl.Discover(homeOverride, daemonURL)` resolves the home (an
  override is made absolute with `filepath.Abs`; otherwise `$CURIO_HOME`,
  then `~/.curio`), initializes it on first use, loads its config, and
  returns the client and controller for the daemon that serves it. The CLI
  and `curio-mcp` both bootstrap through it.
- The controller hands the daemon it spawns `CURIO_HOME=<its home>`
  explicitly. Nothing calls `os.Setenv`, and errcheck no longer exempts it.
- `cmd/curio` runs the CLI through `cli.Run(ctx, args, stdout, stderr)`
  under `signal.NotifyContext` (interrupt, SIGTERM), so a command that waits
  (`import --follow`, `add --wait`) sees ctrl-c as a cancelled context. The
  first signal restores the default handling, so a second one kills.
- The root command silences cobra's own error printing: `Run` prints
  `Error: <message>` once. A file-open error is returned as the
  `*os.PathError` it is, which already names the path.
- Commands close over the `daemonctl.Env` the root command's
  `PersistentPreRunE` fills in, instead of fetching a value from the
  context and checking it on every call.

**Why:** `--curio-home` reached the daemon only because the CLI exported it
into its own environment for the child to inherit. The CLI and the sidecar
each had a copy of the bootstrap. No signal context was ever installed, so
the documented ctrl-c path of `followProgress` never ran and `waitForFetch`
slept through it. Every error printed twice (cobra, then `main`), with the
path doubled: `open /x.html: open /x.html: no such file or directory`.
Twenty-four call sites repeated `getCtx` and a "no context" error, and
ten checked for a nil home or controller that `buildContext` could no
longer return.

---

## Client errors: a typed APIError, and "unreachable" means never connected

**Decision:**

- `internal/client` returns `*client.APIError{Status, Problem}` for every
  non-2xx answer, `GetDocumentContent` included; callers branch with
  `errors.As` (or `client.IsNotFound`). Its message is the problem's detail,
  or its title; a 5xx adds the request ID and a pointer to
  `curio daemon logs`. A body that isn't `application/problem+json` becomes
  the status text with the body as the detail. Error bodies are read
  through a 64 KiB limit, and every body is drained before it is closed so
  the loopback connection is reused.
- `ErrDaemonUnreachable` wraps only a failure to connect (a `*net.OpError`
  whose `Op` is `dial`, while the caller's context is still live), as
  `fmt.Errorf("%w: %w", ErrDaemonUnreachable, err)`. Any other transport
  error is returned as `http.Client`'s `*url.Error`, which names the method
  and URL and wraps `context.DeadlineExceeded` or `context.Canceled` when
  that is the cause.
- Callers that used to mask errors now look at them: the MCP
  `get_document` tool treats only a content 404 as "no extracted content";
  `curio docs show --content` prints "(no extracted content yet)" for it;
  `curio status` says "not running" only for `ErrDaemonUnreachable`; and
  `curio doctor` tells an unreachable daemon from one whose healthz failed.
- `ImportBookmark.SavedAt` is `omitzero`, so a bookmark without a date is
  sent without `saved_at` instead of as year 1.

**Why:** Every transport failure was reported as "daemon unreachable",
timeouts included, with the cause formatted away (`%v`):
`errors.Is(err, context.DeadlineExceeded)` was false for a deadline.
Non-2xx answers were `HTTP 404: {"type":"about:blank",...}` strings read
with an unbounded `io.ReadAll`, so the CLI printed raw JSON and tests
matched on "404". The MCP sidecar discarded every content error, so a
daemon 500 or timeout reached the model as "(no extracted content
available)". Unreachable has to mean "never connected" because the sidecar
restarts the daemon and resends on it (see the next entry), which is safe
only for a request no daemon received.

---

## MCP sidecar: restart an unreachable daemon, retry once

**Decision:** `curio-mcp` keeps the controller's `EnsureRunning` next to its
client, and every daemon call in every tool goes through one helper,
`call`. When a call fails with `client.ErrDaemonUnreachable`, `call` runs
`EnsureRunning` once and the call once more; if the restart fails, the tool
error carries both the unreachable error and the restart's. Any other
error, a 5xx included, is returned without a restart or a retry. The eager
`EnsureRunning` at startup stays, so a port served by another home still
fails the sidecar where the MCP client shows it.

**Why:** The sidecar lives for a whole Claude session, and the daemon can
stop underneath it: `curio daemon stop` after a config edit (the documented
way to apply one), an upgrade, a crash. The sidecar ensured the daemon once
at startup and then kept only the client, so every tool call failed with
"daemon unreachable" until something else started it.

**Safe to resend:** `ErrDaemonUnreachable` means the connection was never
made (see "Client errors"), so no daemon saw the first attempt. Concurrent
calls that find the daemon gone each ensure it; `EnsureRunning` serializes
on `daemon.start.lock` and re-checks healthz, so one daemon starts. Each
ensure is bounded by the start timeout and the tool call's context.

**Revised (2026-09-25):** `call` also ensures and retries once after
`client.ErrStarting`: a daemon still starting ran nothing, so any request
can be sent again. The sidecar's controller has a `ReadyTimeout` of 30s,
well under the 60s or so after which MCP clients cancel a stdio tool call
(the Claude desktop app does, whatever `MCP_TOOL_TIMEOUT` says); a daemon
still starting after that is a tool error that says so, with its progress,
and to try again in a minute, not "restarting the daemon failed". The
startup ensure is `EnsureStarted`, which returns at the first ready or
verified starting answer: waiting out a migration there would hold the
MCP handshake, which Claude Code gives 30s by default. Concurrent calls
no longer queue on `daemon.start.lock` while they wait; only a spawn
takes it. See "Daemon startup: a starting API while migrating, clients
that wait on progress".

---

## List pagination: keyset on (timestamp, id)

**Decision:** `GET /v1/documents`, `GET /v1/jobs` and `GET /v1/bookmarks`
page with opaque cursors over a keyset:

- Documents and jobs order by `updated_at DESC, id DESC`; bookmarks by
  `created_at DESC, id DESC`, newest first by a key that never changes.
- `store.PageKey{At, ID}` is the last row of a page. `ListDocumentsOpts`
  and `ListJobsOpts` gained `After`, which replaced `ListBookmarksOpts`'
  `Cursor`; a non-zero key restricts the list to rows strictly after it
  with the row-value predicate `(updated_at, id) < (?, ?)`.
- The handlers ask the store for one row more than the page, so
  `next_cursor` is present exactly when another page follows. The cursor
  is base64url of `{"t": <RFC 3339>, "id": ...}`. One that doesn't decode
  to a time and an ID is a 400 "invalid cursor", never ignored.
- `BookmarkStore.List` returns `BookmarkWithState`, the document's state
  read through a `LEFT JOIN` in the same query, instead of the handler
  loading each bookmark's document.
- `curio docs` and `curio jobs` take `--cursor`, and a page that has a
  successor ends with `next page: <the command as run> --cursor=<token>`.
  `--limit` outside 1..500 is a usage error.

**The guarantee:** pages never overlap, and rows inserted during a walk
don't shift it. A row whose `updated_at` changes mid-walk moves ahead of
the cursor and is not revisited, so a walk of an active list can miss a
row that was touched while it ran; that is the cost of keeping the
activity-feed order of "Jobs list: sort by updated_at". Cursors are opaque
and may stop being valid across a daemon upgrade: the 400 means "start the
walk again".

**Why:** Documents and jobs ignored `?cursor` and sent no `next_cursor`,
and their `ORDER BY updated_at DESC LIMIT ?` had no tie-break, so nothing
past the first 500 rows could be seen (`curio docs --all` on a large
corpus) and rows sharing a millisecond came back in no fixed order.
`--limit 1000` silently returned 50. Bookmarks paged `id > ? ORDER BY id`,
which for UUIDv4 is random order, and the handler read every row's
document separately.

**Why these indexes:** on SQLite 3.53, adding `id` to the ORDER BY over
the old `(tenant_id[, state|status], updated_at)` indexes plans a
temporary b-tree for the last term. With `id` as the indexes' last column
(migration 009, inside goose's transaction; no table rebuild) the
row-value predicate becomes a range on the index and the order comes from
it. The expanded `ts < ? OR (ts = ? AND id < ?)` form only seeks
`tenant_id`, which is why `keysetAfter` writes the row value.
`plans_test.go` pins the first page and a cursor page of every filter.

| Index | Serves |
|---|---|
| `idx_jobs_claim (status, kind, run_after, created_at)` | `ClaimNext`; `RecoverOrphans` |
| `idx_jobs_document (document_id, status, updated_at)` | a document's last error (`curio docs`); the FK action when a document is deleted |
| `idx_jobs_tenant_status_updated (tenant_id, status, updated_at, id)` | `ListWithDoc` by status, with or without kind; `CountByStatus`; `MetricsByKind`'s window; `PruneOlderThan`; `DeleteByStatus` |
| `idx_jobs_tenant_updated (tenant_id, updated_at, id)` | `ListWithDoc` unfiltered or by kind only |
| `idx_documents_tenant_state_updated (tenant_id, state, updated_at, id)` | `ListWithLastError` by state; `CountByState`; `ListIDsWithContent`; `DocumentVectors` (now covering); `RequeueFetchByStates` |
| `idx_documents_tenant_updated (tenant_id, updated_at, id)` | `ListWithLastError` unfiltered |
| `idx_bookmarks_tenant_created (tenant_id, created_at, id)` | `Bookmarks.List`, unfiltered or filtered by source or folder (checked per row) |

---

## API: the spec is the contract, checked by tests

**Decision:** `api/openapi.yaml` documents exactly what the daemon serves,
and `internal/api/openapi_test.go` keeps it that way:

- `TestOpenAPI_Valid` loads the spec with kin-openapi, validates it, and
  fails on 3.0's `nullable`, which OpenAPI 3.1 doesn't have. The spec stays
  on 3.1 and marks optional fields by leaving them out of `required`: the
  handlers omit unset fields and never send `null`.
- `TestOpenAPI_RoutesMatchRouter` walks the router `newRouter` builds with
  `chi.Walk` and compares its (method, path) pairs with the spec's, both
  ways.
- `TestOpenAPI_RequestTypesMatchSchemas` compares the JSON fields of the
  request types the strict decoder fills with the request-body schemas,
  recursively.
- `TestOpenAPI_ResponsesMatchSchemas` drives every documented operation
  through the real router over seeded fixtures (a fake embedder, UUID IDs)
  and validates each response: a documented status and content type, and a
  body that passes JSON Schema 2020-12 with `format: uuid` enforced and,
  in the test only, undeclared properties refused. It fails if any
  documented operation goes unexercised, and covers problems for 400, 404,
  409, 405 and 415.
- kin-openapi is a test dependency; the `no-test-deps-in-prod` depguard
  rule denies it to production code.

Reconciling the spec with the router:

- `GET /v1/jobs/{id}` was documented, and it is what the async convention
  tells clients to poll, so it was implemented (`JobStore.GetWithDoc`).
- `POST /v1/jobs/{id}/retry` is removed: retrying a job must also reset
  its document's state, which refetch, reindex and interests rebuild
  already do.
- `POST /v1/bookmarks/{id}/refetch` is removed: it duplicated
  `POST /v1/documents/{id}/refetch`, and every bookmark carries its
  `document_id`.
- `GET /v1/documents/{id}/references` is removed: no client reads it, and
  adding it later is non-breaking.
- `GET /v1/metrics`, `POST /v1/documents/{id}/reindex`,
  `POST /v1/documents/reindex-all` and `DELETE /v1/jobs` were routed but
  undocumented, and are documented now.
- Every operation declares a `default` response referencing the shared
  Problem (403, 405, 413, 415, 500), and lists the 400, 404 and 409 its
  handler produces.

**Imports are synchronous per batch.** `POST /v1/bookmarks/import` answers
200 with what the batch did (`created`, `skipped`, `filtered`,
`filtered_by`, `jobs_enqueued`, the first errors) for a list of parsed
bookmarks, as "Importers: CLI parses, daemon receives lists" decided; the
spec described a file upload answered with 202 and a job ID. A batch (the
CLI sends 500) takes well under a second, and the fetches it enqueues are
jobs like any other. This supersedes "imports answer 202 with job_id" in
"API: all long-running operations are async with job IDs".

**Why:** Nothing checked the spec. Four documented operations weren't
routed, four routed ones weren't documented, the import endpoint was
described as a different API, and validating live responses found drift in
Stats, Extraction, Job, SearchHit, the document list items and
BookmarkCreated (a `job_id` of format uuid that is empty for a known URL,
by design). Seventeen `nullable` keywords meant nothing under 3.1, and
kin-openapi's validator accepted them silently. The docs said the clients
were generated from the spec; they are hand-written, so a test is what
keeps the two in step. Codegen stays deferred.

**Revised (2026-09):** `TestOpenAPI_ResponsesMatchSchemas` now enforces
what its fixtures only claimed: it records, per resolved response schema,
the properties present in the validated responses, and fails listing every
declared property (`Schema.property`) that none carried. A mutation run had
found 16 optional properties the strict validation never saw (among them
`Health.ollama_detail`, `BookmarkList.next_cursor`, `ImportResult.errors`,
`Document.author`, `SearchResponse.degraded` and `Job.doc_title`); the
fixtures and exchanges now produce each one. Removing an optional
property from the spec, or adding one no fixture sets, fails the test.

---

## Toolchain: the go directive is the build toolchain, govulncheck gates it

**Decision:** The `go` line in `go.mod` names the exact toolchain CI and
releases build with (`go 1.26.8`), and there is still no `toolchain`
directive. setup-go installs it from `go-version-file`, and the Makefile
exports `GOTOOLCHAIN=go<directive>`, so every local target runs it too; the
go command downloads it once when a machine's default Go differs.
`make vulncheck` runs govulncheck v1.8.0 with the sqlite build tags. CI runs
it on every push and pull request, and the release gate runs CI.

The policy:

- Bump the patch when govulncheck reports a standard-library finding.
- Move to the next minor before the current line leaves support. Go
  supports its two newest minors, so the release of go1.N+2 ends go1.N.
- golangci-lint must be built with a Go minor at least the directive's, or
  it cannot type-check the standard library it is handed.
- No `toolchain` line. golangci-lint reads it as the target version, so a
  toolchain newer than the language version makes modernize suggest APIs
  that vet's stdversion check then rejects. Move the `go` line instead.

**Why:** The shipped binaries were built with go1.25.7, and govulncheck
found the code reaching 19 known vulnerabilities: 17 in the standard library
(net/http, crypto/tls, crypto/x509, net/url and others, fixed in 1.25.8
through 1.25.13), golang.org/x/text v0.37.0, and cloudflare/circl v1.5.0,
which tls-client pulls in. The daemon fetches arbitrary web pages, so
untrusted input reaches exactly those packages, and nothing ran govulncheck.
Go 1.25 left support when go1.27.0 shipped on 2026-08-19; go1.25.14 was its
last patch, so pinning it would only postpone the first finding with no fix
on that line. Moving to 1.26.8 with x/text v0.39.0 and circl v1.6.3 brings
the count to zero, and the language change from 1.25 needed no code change.

Exporting GOTOOLCHAIN came from a measured failure: the official
golangci-lint v2.12.2 binary, built with go1.26.2, cannot load go1.27's
standard library ("file requires newer Go version go1.27") on a machine
whose default Go is newer than the directive. With the export it loads the
directive's standard library everywhere.

---

## Releases: gated on CI, pinned, least privilege

**Decision:** `release.yml` runs `ci.yml` (`workflow_call`) as a `ci` job,
and the `release` job `needs` it. A tag whose tree fails tidy-check, build,
vet, the unit or end-to-end tests, vulncheck or lint cannot publish. The
workflow's token is read-only at the top level; only the `release` job gets
`contents: write`, and `GITHUB_TOKEN` and `HOMEBREW_TAP_GITHUB_TOKEN` appear
only in the goreleaser step's environment. The gate job inherits the
read-only token and sees no secrets.

- Every third-party action is pinned by full commit SHA with its tag in a
  trailing comment (checkout v7.0.1, setup-go v7.0.0, goreleaser-action
  v7.2.3, golangci-lint-action v9.3.0; all on node24, which replaces the
  deprecated node20 runtime). The local reusable workflow is referenced by
  path, which already means "this commit". goreleaser itself is pinned
  exactly (v2.18.2) instead of `~> v2`.
- The release job checks out with `persist-credentials: false` and builds
  with setup-go's cache off, so the job holding the write token never
  restores a module cache another run wrote.
- CI gains a `test-macos` job on macos-14, the release runner:
  darwin/arm64 is the only platform goreleaser ships and was never built or
  tested before a tag. It runs `make test` with no artifacts or secrets.
- The lint job reads its golangci-lint version from the Makefile
  (`make -s golangci-lint-version`), so the pin lives in one place, and
  runs actionlint over the workflows.
- Dependabot opens one grouped PR per week for Go modules and one for
  actions; for actions it moves the SHA and the tag comment together.
- goreleaser's `before` hook verifies (`go mod tidy -diff`) instead of
  running `go mod tidy` on the release checkout.

**Deferred: `brews` → `homebrew_casks`.** `goreleaser check` v2.18.2 exits
non-zero because `brews` is deprecated, so it is not part of the gate yet.
Migrating turns the tap's Formula into a Cask, which changes how users
install and upgrade; that is a distribution decision of its own, not a
hygiene fix.

**Why:** The release workflow published with a write token and a PAT that
can push to the tap, on any `v*` tag, with no test, vet, lint or
vulnerability gate and nothing requiring the tagged commit to have passed
CI. Every action was a movable tag and goreleaser floated within v2, so the
code holding those secrets could change without a commit here. Making
`ci.yml` reusable instead of copying its steps keeps the gate from
drifting away from what pull requests run.

---

## Lint: a measured linter set, zero issues, explained suppressions

**Decision:** `.golangci.yml` adds gocritic, gosec, exhaustive, noctx,
nilnil, errchkjson, modernize, intrange, usestdlibvars, perfsprint (without
its string-concatenation check), nolintlint and revive with an explicit
rule list to the existing set, and lints the tag-gated files too
(`run.build-tags`: the sqlite tags, `integration`, `e2e`). The tree stays at
zero issues. A hit is fixed, or silenced on its line by a `//nolint` that
names the linter and gives a reason; nolintlint enforces both and rejects
suppressions that no longer suppress anything.

The only config-level exclusions:

- gosec G104 (errcheck owns unchecked errors), G304 and G703 (file paths
  come from the operator's own arguments, config and `$CURIO_HOME`).
- Tests skip errcheck, bodyclose, noctx and gosec.
- `fmt.Fprint*` errors are ignored in `internal/cli/` only, where they are
  writes to the command's own stdout/stderr. The old global exemption and
  the blanket `cmd/` errcheck exclusion are gone (the latter hid an
  unchecked `db.Close`).
- errcheck ignores `(*sql.Tx).Rollback`, a no-op after Commit whose
  failure otherwise loses to the error that caused it; the eight
  `//nolint:errcheck` comments that said so are deleted.

revive runs its defaults minus `exported` and `package-comments`, plus
rules that catch real mistakes (datarace, waitgroup-by-value,
modifies-value-receiver, unconditional-recursion, import-shadowing, defer
in loops, deep-exit) or keep code current (use-any, early-return,
unused-receiver). `redundant-import-alias` stays off: the `fhttp` alias in
`internal/fetcher/transport.go` is required because
github.com/bogdanfinn/fhttp's package name is `http`. exhaustive keeps
`default-signifies-exhaustive` off, so a new enum member has to be handled
on purpose; a default may remain for out-of-range values.

**Why:** The old header dismissed gocritic and gosec as "high-noise".
Measured on this tree, gocritic found 2 hits and gosec 21, all but two of
them the path-taint and unchecked-error classes excluded above; the two
real ones are the subprocess launches, which now carry a reason. The
expanded set found real defects (a Firefox WAL copy whose failure silently
dropped the newest bookmarks, an unchecked `db.Close`, enum switches that
missed members, `net.Listen` and `db.Query` without a context, a
`nil, nil` return) and replaced idioms Go has moved past (`sort` over map
keys, hand-rolled min/max, C-style loops, `os.IsNotExist`, int32 atomics,
`fmt.Errorf` with a constant message). errorlint's `errorf` check is on
again: the last two `%v`-wrapped causes now use `%w`.

---

## Ollama: one client, one sentinel pair, a pull that keeps trying

**Decision:** `internal/ollama.Client` is the one Ollama client.
`embedder.Ollama` and `generator.Ollama` each hold one and keep only their
endpoint's request shape and checks: embed batching and the dimension
check, and generate with its retry policy (unchanged, see "LLM generation
client"). The client owns:

- base-URL validation (http or https, with a host) and trailing-slash
  trimming;
- `Ping`: `GET /api/tags`, matching the model by name or name plus any tag;
- `PostJSON`, which bounds the reply it decodes: 1 MiB for tags and
  generate, and for `/api/embed` a limit sized from the batch (32 bytes of
  JSON per vector component plus 64 KiB);
- `EnsureModel`, `Pull` and `KeepPulled`.

There is one sentinel pair, `ollama.ErrUnreachable` and
`ollama.ErrModelNotLoaded`. A transport failure wraps `ErrUnreachable` and
its cause with `%w`, so `errors.Is` sees `ECONNREFUSED` or
`context.DeadlineExceeded` too. A missing model is `ErrModelNotLoaded`
whether `/api/tags` lacks it or `/api/embed` or `/api/generate` answers
404. `/v1/healthz` maps the pair to advice, whichever client failed.
Error bodies are read with `io.ReadAll` over a 2 KiB `LimitReader`.

`Pull` fails when the stream ends before Ollama's `success` line: progress
lines followed by EOF are a dropped connection or a crashed server, not a
pulled model.

`KeepPulled` replaces the one-shot background `EnsureModel`. It retries
with capped exponential backoff (5 s, doubling to 5 min) until the model is
ready or the daemon shuts down, and the daemon runs it for the embedding
model and, with LLM labels, the generation model. The first attempt logs
its pull at INFO and its failure at WARN, saying it will retry. Retries log
both at DEBUG, so an Ollama that can't reach its registry doesn't add an
INFO line every 5 minutes. Success is INFO, and nothing is logged once the
context is cancelled.

**Why:** The two clients were copies of each other (defaults, validation,
Ping, EnsureModel), with two sentinel pairs that didn't match under
`errors.Is`, and healthz recognized only the embedder's. The copies had
drifted: the embedder formatted its transport cause with `%v`, decoded
`/api/tags` and `/api/embed` without a bound, and quoted error bodies from a
single `Read`, which returns a partial message and leaves the connection
unusable; the pull did the same, and treated EOF before `success` as
success. The auto-pull ran once: with Ollama down when the daemon started,
which is common when the CLI auto-starts the daemon first, the embedding
model was never pulled after Ollama came up, although the log said index
jobs "will retry until it is", and the generation path said outright that
the pull is not retried.

**Revised (2026-09-27):** `Ping`, and `ModelDigest`, the digest lookup the
drift monitor uses, match a model under the name Ollama runs for it:
lower-cased, with `:latest` appended when the last path segment has no tag
(a ':' before the last '/' is a registry host's port), compared exactly
against each `/api/tags` entry's `name` and `model`. The old rule, the name
alone or with any tag, took `qwen3-embedding` as loaded when only
`qwen3-embedding:0.6b` was pulled, while Ollama resolves the untagged name
to `:latest`, the 8B model: `KeepPulled` logged the model ready and never
pulled it, healthz said it was loaded, and every embed answered 404 until
the index jobs gave up. It also reported `qwen3-embedding:latest` missing
next to a pulled `qwen3-embedding`. `Model()` still returns the configured
string, so requests carry what the user wrote, and untagged names aren't
rejected: drift detection catches a `:latest` that moves, and the defaults
are pinned. The client also reads `/api/version` (`Version`).

---

## Insight: skip non-finite document vectors, don't fail the run

**Decision:** `Engine.Rebuild` drops document vectors with any NaN or
infinite component right after reading them, before the empty-corpus
check. It logs one WARN with the count and up to 10 document IDs,
suggesting `curio reindex <id>`, and clusters the rest; the run's
`num_documents` counts only the vectors clustered. If none is left, the
rebuild behaves exactly like one with no vectors: a prior done run is
kept, not replaced by an empty one. `checkUnitVectors` stays as the
clusterer's backstop.

**Why:** With `insight.center_vectors` on (the default), one bad vector
makes the corpus mean NaN, so every residual is NaN and the unit-length
check rejects the first point, a healthy document. The run failed every
time, naming the wrong document, until someone found the real one.
`DocumentVectors` decodes raw float32 bits, so a corrupted or unchecked
stored vector gets that far. One bad vector must not block everyone's
interests; the engine already tolerates an all-zero vector (it falls out
as noise) and a document deleted mid-run the same way. The store and
the indexer are unchanged.

---

## CLI: exit 130 on interrupt, a usage hint on usage errors

**Decision:** `cli.Run` returns 130 and prints nothing when the command
failed after its context (the signal context from `cmd/curio`) was
cancelled. A usage error (an unknown command, a flag that doesn't parse, a
wrong number of arguments) prints `Error: <msg>` and then
`Run '<command path> --help' for usage.`, naming the command that failed
(`curio search`, `curio docs show`), and returns 1. Any other error prints
only the `Error:` line and returns 1.

Usage errors are told apart without matching error text: cobra returns
them before any hook runs, so `Run` marks the root's `PersistentPreRunE`
(the one that runs `Discover`) as reached, and an error from a run that
never reached it is a usage error. curio declares no required flags or
flag groups, whose checks cobra runs after the hooks; the tests pin the
three cases. 130 is 128+SIGINT, what a shell shows for ctrl-c; SIGTERM
gets it too, because the context only says it was cancelled.

**Why:** Interrupting `curio daemon logs -f` printed
`Error: signal: killed` (or `signal: interrupt`) and exited 1: the
cancelled context killed `tail`, and `Run` reported that like a failure.
With `SilenceErrors` on, cobra no longer printed its
"Run 'curio --help' for usage." line, so a mistyped command or flag got a
bare error with no pointer to the usage.

---

## Daemon startup: a starting API while migrating, clients that wait on progress

**Decision:**

- The daemon answers HTTP from the moment it binds. One listener and one
  `http.Server` serve its whole life; until it is ready they route every
  request to a starting router built with no `Deps`, and after orphan
  recovery `api.Server.Ready` swaps in the full router atomically, so a
  keep-alive connection carries on across the swap. The access checks
  are the same middleware in both routers.
- While starting, `/v1/healthz` answers 503 `application/problem+json`
  with `Retry-After: 1`: a problem of type
  `urn:curio:problem:daemon-starting` that also names the daemon (`pid`,
  `home`, `version`) and says how far along it is (`phase`, `initializing`
  or `migrating`, and `migrations: {applied, total}` exactly while
  migrating). Every other request gets the same problem without those
  members. Nothing a starting daemon refused has run.
- Startup order: signals, home, lock + PID, config + log level, marker
  check, bind, serve the starting API, open, migrate, sync the marker,
  build dependencies, recover orphans, swap in the full API, workers. A
  failure after the bind stops serving, which closes the listener, before
  the lock is released.
- `phase` is `migrating` only when the database already had a schema
  (goose version 1 or more): creating a new one takes milliseconds.
- Migrations are applied one at a time (`sqlite.MigrateWithHooks`,
  `Provider.UpByOne`). The daemon's hooks drive both the healthz progress
  and the log: `migrating database` with the pending count and versions,
  then `applying migration` and `migration applied` (with `duration_ms`)
  for each. Nothing is logged when nothing is pending. `curio-daemon
  starting` is logged at the bind and `curio-daemon ready`, with
  `startup_ms`, at the swap.
- Clients (`daemonctl`) wait on our daemon, the child they spawned or the
  lock holder answering for this home, with two budgets. `StartTimeout`
  (15s) is how long it may go without answering, counted again from each
  answer in which it reports it is starting; past it, a child is a
  failed start with the log tail, and a holder we didn't spawn gets
  max(StartTimeout, StopTimeout) as before. `ReadyTimeout` (30 min) caps
  a daemon that keeps reporting it is starting; past it, `EnsureRunning`
  returns `ErrStillStarting`, which names the pid and progress, says the
  daemon keeps running and where to look, and never says "failed to
  start". Nothing is signalled. Each probe is bounded by the time left,
  so a wait can't overrun its budget by healthz's own 2s timeout.
- A starting answer for another home is the same error as a serving one.
  One for this home from a pid that is neither the child nor the lock
  holder is no evidence of progress.
- Probes are 100ms apart while nothing answers or the daemon initializes,
  so a routine start isn't slowed, and 1s apart (its `Retry-After`) while
  it migrates, since each is an access-log line in a log that is never
  rotated. Either way at least two fall in every silence budget.
- `Controller.OnMigrating` is called once per `EnsureRunning` or
  `EnsureStarted` call, the first time our daemon reports it is
  migrating. The CLI prints one line to stderr from it; daemonctl itself
  prints nothing.
- `daemon.start.lock` is held only while spawning: from the decision to
  spawn until the child holds `daemon.pid`, exits, or runs out of silence
  budget. From then on the daemon lock tells every other starter to wait
  rather than spawn. Taking the start lock is a non-blocking attempt
  every 100ms that honours the caller's context.
- `curio-mcp` starts with `EnsureStarted`, which returns at the first
  ready or verified starting answer, and gives each tool call a 30s
  `ReadyTimeout` (see "MCP sidecar: restart an unreachable daemon, retry
  once").
- `curio daemon status`, `curio status` and `curio doctor` show a
  starting daemon as starting, with its phase and progress. doctor makes
  it a warning and leaves Ollama unchecked until the daemon is ready.

**Why:** The daemon bound its port and only built its HTTP server after
migrating, so for a whole migration the port accepted connections and
answered nothing. Clients gave a spawned daemon a fixed 15s (the comment
said it covered migrations on a large database) and a holder 30s, then
reported `curio-daemon failed to start`: on a 2.4 GB home migrations 005
to 010 took 37s, and the daemon was ready about 20s after the CLI gave
up. Migration time grows with the library, so no constant covers it,
while a daemon that has died shows itself by exiting or going silent.
Each probe of the silent port also blocked for healthz's 2s timeout, so
a wait overran its own deadline: a 1s budget ended after 2.1s. The start
lock, taken with a blocking flock and held through the whole wait,
parked a second client in the kernel, deaf to its context and to ctrl-c:
one with a 500ms context returned after 2.7s. And `curio-mcp` ran that
wait before answering the MCP handshake.

**Why 503 problem+json, not 200 with a starting status:** the 200 Health
shape is pinned (see "Marker file's schema_version is synced from the DB
after migrations"). Older clients check only `pid` and `home`, so a 200
would end their wait early and their next request would fail; a 503 keeps
them waiting as they do today. Generic readiness checks treat any 200 as
ready. Every non-2xx answer is RFC 7807, and older clients print a
problem's `detail` readably ("not answering healthz: curio-daemon is
starting: migrating the database, 2 of 6 migrations applied"), where a
Health body on a 503 would print as raw JSON. RFC 7807 reserves `status`
for the HTTP status, so "starting" is the problem type plus `phase`. The
identity members ride on healthz only, so other routes send a plain
Problem.

**Rejected:**

- A longer fixed timeout: whatever it is, a larger library outgrows it,
  and it would also delay reporting a daemon that is wedged.
- Migrating before binding: the rule that a daemon which can't serve
  exits without touching the database would go, and clients would still
  see only a closed port.
- A status file beside `daemon.pid` for clients to read: a second
  protocol to keep in step with healthz, invisible to anything that only
  speaks HTTP, and stale after a crash.

**Compatibility:** new clients talking to a daemon from before this
change (a healthz 200, with or without `pid` and `home`, and never a 503)
behave as before. Old clients talking to a new daemon see a starting
daemon as "not answering healthz", the same wait and failure as before,
never as ready.

**Revised (2026-09-27):** the marker check is now the home's embedding
check (`curiohome.Home.CheckEmbedding`: a legacy home, then a
`config.yaml` that disagrees with the marker), still before the bind. After
migrating, `sqlite.EnsureVectorIndex` sizes `chunks_vec` at the marker's
width before the marker's schema version is synced. The order is now:
signals, home, lock + PID, config + log level, embedding check, bind, serve
the starting API, open, migrate, size the vector index, sync the marker,
build dependencies, recover orphans, swap in the full API, workers and the
drift monitor. See "Embedding model and per-home width".

---

## Chrome backend: plain http carries an explicit :80

**Decision:**

- The chrome backend gives every `http://` request without a port an
  explicit `:80` before sending it (`pinPlainHTTPPort` in
  `internal/fetcher/transport.go`). Every redirect hop gets the same pin
  from the backend's own redirect policy (`chromeCheckRedirect`, installed
  with `tlsclient.WithCustomRedirectFunc`), which keeps fhttp's default
  limit of 10 redirects.
- The pin never shows. The `Host` header stays port-free, and the
  `Referer` fhttp sets on the next hop is rewritten without it. Every hop
  takes its `Host` from its own URL: fhttp carries the previous hop's
  `Host` over to a `Location` without a scheme when that `Host` differs
  from the URL, which the pin makes true, and a scheme-relative
  `Location` (`//www.example.com/post`, an apex → www rule) names another
  host. Carried over, the new host would get the old host's name, and an
  apex → www redirect would loop until the limit. `finalURL`
  and the URL a `*url.Error` names lose any default port through
  `urlutil.StripDefaultPort`, the rule `Normalize` applies. So
  `Result.FinalURL`, `url_canonical`, the base URL Readability resolves
  relative links against, and error text never carry `:80`.
- A URL that names its port is sent as it is. The stock backend is
  unchanged: net/http keys its connections by scheme.
- A URL without a host is never pinned, and a redirect hop without one
  (`Location: http:///x`) is refused with net/http's own `http: no Host
  in request URL`, as the stock backend refuses it. Pinned, it would name
  `:80`; unpinned, tls-client dials an https hop's `:443` before fhttp
  checks the host. Go dials an empty host on the local machine, so the
  fetch could store a local server's page, or record a refused connection
  against the redirecting host as unreachable and fail that host's
  healthy pages from the host cache for 15 minutes.
- tls-client stays at v1.16.0, the latest release. Nothing upstream fixes
  this yet; the report below is ready to file. The pin goes once tls-client
  keys transports by scheme, and `TestChromeRT_PlainAndSecureShareAHost`
  and `TestChromeRT_ConcurrentUpgradeRedirects` then pass without it.

**Why:** tls-client caches one transport per `host:port`, and for a URL
without a port it uses `host:443`, whatever the scheme. `http://h/` and
`https://h/` shared that entry, so on the default backend:

- https, then http: every later http request to the host failed with
  `http2: unsupported scheme`.
- http, then https: the first https request failed with tls-client's
  internal `protocol negotiated`, and http then broke as above.
- A same-host redirect from http to https, which is where most `http://`
  bookmarks lead today, failed with `protocol negotiated` on first
  contact and with `http2: unsupported scheme` on every retry, so the
  document failed all 5 attempts (`http://www.babycenter.ca/…` in a real
  import).
- A redirect from https to http failed with `http2: unsupported scheme`.

The http-first paths also race: tls-client's dial writes the transport map
under a different lock than the one its readers hold. The race detector
reports it with 40 concurrent upgrade redirects, and without it the Go
runtime can abort the whole daemon with `concurrent map read and map
write`, which no `recover` catches. The unit tests never met any of this
because every test URL named its port (`127.0.0.1:PORT`). The new tests
drive the real backend on `example.com` through a dialer that routes by
address, with a test CA's roots trusted.

**Residual:** a URL that names the other scheme's default port
(`http://h:443/`, `https://h:80/`) still shares an entry with that scheme.
Nobody bookmarks those.

**Upstream report (ready to file on bogdanfinn/tls-client):**

- **Title:** RoundTrip keys transports by host:443 for portless http URLs;
  http and https to one host share a transport, and dialTLS writes
  cachedTransports unsynchronized.
- **Versions:** tls-client v1.16.0, fhttp v0.6.9, Go 1.26.8,
  darwin/arm64.
- **Cause:** `getDialTLSAddr` (roundtripper.go:675-682) returns
  `net.JoinHostPort(host, "443")` whenever the URL has no port, regardless
  of scheme. `RoundTrip` (:317-346) caches transports by that key.
  `getTransport`'s http branch (:349-352) stores an HTTP/1 transport and
  records no `cachedKinds` entry for it.
- **Repro** (`WithDialContext` maps `example.com:443` to an httptest
  TLS+h2 server and `example.com:80` to a plain one):
  1. https, then http: every later http request fails with
     `http2: unsupported scheme`.
  2. On a fresh client, http, then https: the https request fails with
     `protocol negotiated`. The cached HTTP/1 transport's
     `DialTLSContext` is `dialTLS` and there is no `cachedKinds` entry,
     so `dialTLS` builds a new transport and returns
     `errProtocolNegotiated`. After that, http fails as in 1.
  3. A same-host 301 from http to https fails with `protocol negotiated`,
     then with `http2: unsupported scheme` on every retry.
  4. An https → http redirect fails with `http2: unsupported scheme`.
- **Race:** `dialTLS` writes `rt.cachedTransports[addr]` (:540) holding
  only `rt.Mutex`, while `RoundTrip` reads the map under
  `cachedTransportsLck` (:325). `-race` flags it; without `-race` the
  runtime can fatal with `concurrent map read and map write`. A reconnect
  after `dropCachedTransport` reaches the same unlocked write.
- **Expected:** http and https never share a transport, and
  `errProtocolNegotiated` never reaches callers.
- **Also:** `RoundTrip` dials before fhttp checks the URL's host, so an
  https URL without one (a redirect to `https:///x`) dials `:443` on the
  local machine instead of failing with `http: no Host in request URL`.
- **Suggested fix:** default the port by scheme (80 for http), or key the
  cache by scheme and address; record a `cachedKinds` entry for the http
  transport; take `cachedTransportsLck` for every write to the map in
  `dialTLS`; refuse a URL without a host before dialing.
- **Workaround:** an explicit `:80` on the request and on each redirect
  hop (through `CheckRedirect`), with the `Host` header left port-free
  and set from each hop's own URL, and a hop without a host refused there.
  Explicitly mismatched ports (`http://h:443/`, `https://h:80/`) still
  collide.

---

## TLS certificate failures are permanent, never Jina, never host-cached

**Decision:**

- A server certificate that fails verification is
  `fetcher.ErrTLSCertificate`. Each backend maps its own TLS stack's
  wrapper at its boundary, so `tryReadability` stays backend-agnostic:
  `stockRT` crypto/tls's `*CertificateVerificationError`, and `chromeRT`
  uTLS's, which is a distinct type that `errors.As` with crypto/tls's
  doesn't match. Classifying on the wrapper covers every chain and
  hostname failure (expired, not yet valid, another name, an unknown
  authority), including the untyped `x509: …` errors macOS's platform
  verifier can produce. The x509 cause and the `*url.Error` stay reachable.
- From the origin it is a `PermanentError`, returned before the Jina and
  host-cache steps. The document goes `failed`, not `dead`.
- A certificate failure talking to Jina stays Jina's trouble: retried like
  any other Jina transport error, and never cached.
- Other TLS failures stay retryable: handshake alerts, protocol-version
  mismatches, EOF or a reset mid-handshake, and a non-TLS answer
  (`RecordHeaderError`). They are ambiguous or transient, and none showed
  up in the import this came from.
- `github.com/bogdanfinn/utls` is now a direct requirement.

**Why:** `https://www.kernel.dk/io_uring.pdf` answered with an expired
certificate (`x509: certificate has expired or is not yet valid:
"brick.kernel.dk" certificate is expired`). That was a generic retryable
transport error, so the document went through all 5 attempts, 60 + 120 +
240 + 480 s of backoff, with the same answer each time.

- **Permanent:** nothing inside the retry window renews a certificate.
- **`failed`, not `dead`:** `markDocFailed` maps only `ErrDeadLink` to
  `dead`. Certificates get fixed; recovery is `curio refetch <id>` or
  `curio refetch --all --state=failed`.
- **No Jina:** Jina would fetch past a check curio refuses to skip and
  store content nobody authenticated. The fallback is for answers that
  came back (see "Fallback strategy: only Jina for content-came-back
  cases").
- **Not host-cached:** a permanent verdict already costs only one
  handshake per URL, bounded by the per-host gate. The cache protects
  retry and Jina budgets, and this spends neither. A cached verdict would
  also make `curio refetch` fail, without sending a request, for up to 15
  minutes after the site fixed its certificate.

**Caveat:** on a network that intercepts TLS (a captive portal, a
corporate proxy whose root the system doesn't trust) or with a badly wrong
local clock, every https fetch fails permanently at once. The error names
the cause (`invalid TLS certificate: … x509: …`), and once the network or
clock is right, `curio refetch --all --state=failed` recovers.

---

## YouTube: caption tracks by an exact pattern, not `en.*`

**Decision:** `fetcher.youtube.sub_langs` defaults to
`en,en-(?-i:[A-Z]{2})`. The value is defined once, as
`fetcher.DefaultYouTubeSubLangs`, and `NewYouTube` applies it when the key
is empty; the config no longer carries a literal. A configured value is
passed to yt-dlp as is.

- `en` is the uploaded English track or, without one, YouTube's automatic
  track: for a video in another language, its captions machine-translated
  into English, as before.
- `en-(?-i:[A-Z]{2})` adds uploaded regional English tracks (`en-GB`,
  `en-US`). The scoped `(?-i:)` keeps the region case-sensitive.

**Why:** yt-dlp full-matches each `--sub-langs` item, case-insensitively,
against the key of every uploaded and automatic track. For each uploaded
track in language L, YouTube's extractor adds an automatic English
machine translation keyed `en-L` (`en-zh`, `en-en-GB`, `en-zh-Hans`), and
it adds `en-orig`, the same URL as the automatic `en`. `en.*` matched
them all, one timedtext request each, and videos with many caption
languages drew HTTP 429s. Track counts from yt-dlp 2026.08.19's own
selector on synthetic layouts (offline, `--load-info-json`):

| Layout | `en.*,en` | default |
|---|---|---|
| uploaded en-GB, zh, ja + automatic | 6 (en-GB, en-orig, en, en-en-GB, en-zh, en-ja) | 2 (en, en-GB) |
| uploaded en + automatic | 3 | 1 |
| automatic only | 2 | 1 |

**What the default gives up:** named uploaded English tracks (`en-<id>`)
and, on a video without automatic captions, the English translation of an
uploaded track in another language. Such a video is stored
description-only (partial). Setting `sub_langs` gets them back.

**Rejected:**

- `en,en-orig`: `en-orig` duplicates the automatic `en`, and uploaded
  `en-GB`/`en-US` tracks lose to automatic captions.
- Omitting `--sub-langs`: yt-dlp then picks one track itself, but its last
  resort is the first uploaded track of any kind. On a stream replay
  without English captions that is `live_chat`, a paginated chat download.
- `--extractor-args youtube:skip=translated_subs`: when the flag is given
  more than once, yt-dlp replaces the whole `youtube:` argument dict
  instead of merging it, and command-line arguments are applied after
  config files (`options.py`, `_dict_from_options_callback`). It would
  silently drop a user's own `player_client` or PO-token settings.
- A literal region list (`en,en-GB,en-US,en-CA,…`): matching is
  case-insensitive, so `en-CA` would also select `en-ca`, the English
  translation of an uploaded Catalan track.

**Migration:** `config.yaml` is never generated, so only a config that sets
`sub_langs` keeps the old value. `docs/setup.md` says to delete the key.

---

## YouTube: a failed caption download leaves a partial, not a failed fetch

**Decision:**

- yt-dlp runs with `--ignore-errors`. A caption track that fails to
  download is then a `WARNING: Unable to download video subtitles for
  '<lang>': <reason>` line, info.json and the other tracks are still
  written, and yt-dlp exits 0. Extraction errors (unavailable, private,
  removed, bot checks, format errors) still exit non-zero and keep their
  classification.
- `YouTube.Fetch` reads those warnings. With no transcript from any track,
  the result is `Partial` and its new `PartialReason` quotes them
  (`transcript not downloaded: yt-dlp: Unable to download video subtitles
  for 'en': HTTP Error 429: Too Many Requests`), capped at 512 bytes like
  any quoted error text. A video with no usable captions says `no usable
  captions for sub_langs "…"`. When another track gave a transcript the
  result is not partial, and the failed track is logged at Warn.
- The fetch handler stores `PartialReason` as the extraction's
  `error_message`, which the API already returns and `curio docs show`
  prints as `err:`. No new API or CLI surface.
- Partial counts as success: the job is done, the index job runs, and the
  document ends `fetched`, searchable by its title and description, with a
  `partial` extraction. `curio refetch <id>` stores a new extraction, `ok`
  once the transcript downloads.

**Why:** yt-dlp writes subtitles before info.json, and under its default
`ignoreerrors='only_download'` a failed caption download raises: exit 1,
no info.json, and the remaining tracks never tried. One 429 on one often
useless track (`en-zh`, a machine translation) threw away the video's
metadata and description, and every retry requested every track again.

**Why partial rather than a retry:** the metadata is already in hand, a
failed fetch indexes nothing, and a retry repeats every caption request
against the limit that failed it. The cost is a transcript missing until a
manual refetch, and "YouTube: a shared cooldown after a 429" bounds how
many videos one throttle turns into partials.

**Known cost:** a refetch replaces the current extraction whatever it
held. Refetching a video that already has its transcript (`curio refetch
<id>`, or `refetch --all`, which includes fetched documents) while YouTube
throttles its captions makes the partial current, and the index job
re-chunks the document without the transcript; the `ok` extraction stays
on disk but is no longer the one searched. A later refetch brings the
transcript back, and the cooldown bounds how many videos one throttle
catches. Keeping an `ok` extraction current over a later partial is left
out: the fetch handler would have to choose between extractions and settle
the document on one it didn't just write, and the partial carries the
video's current title and description.

---

## YouTube: a shared cooldown after a 429

**Decision:**

- Any `HTTP Error 429` in yt-dlp's stderr, on an `ERROR:` line (the
  extraction) or a `WARNING:` line (one caption download), extends a
  cooldown shared by every fetch on the YouTube fetcher to 2 minutes from
  now (`youtubeRateLimitCooldown`), and is logged once at Warn with the
  video ID. Caption 404s, unavailable videos and timeouts extend nothing.
- `Fetch` checks the cooldown through `pace`, with no limiter since the
  daemon's `RateLimited` wrapper already paces yt-dlp starts at 2 a
  second. It checks before queueing for a yt-dlp slot, so a long cooldown
  fails without waiting for one, and again once it holds a slot, so a 429
  that another run met meanwhile stops it too. Up to 30 s left
  (`maxInlineYouTubeWait`, Jina's cap) is sat out; more fails at once,
  without running yt-dlp, as a retryable `*HTTPStatusError{429}` whose
  `RetryAfter` is the time left.
- A failed run that met a 429 returns a retryable error carrying
  `*HTTPStatusError{429}` with the 2-minute step as `RetryAfter`.
  `JobQueue.MarkFailed` takes no delay yet, so the hint rides on the
  error, as for Jina and GitHub.
- `MaxConcurrent` (2 runs at once) and the start pacing are unchanged.

**Why:** YouTube throttles per IP, yet after a 429 every queued video
still started yt-dlp, two at a time, and each 429'd job retried on its own
backoff. Jina and GitHub already shared a cooldown that a 429 extends;
YouTube had none. Now that a failed caption download leaves a partial,
the cooldown also bounds how many videos one throttle leaves without a
transcript.

**Why a fixed 2 minutes:** yt-dlp passes on no `Retry-After`, so the wait
is a fixed step. YouTube's caption and player throttles last minutes,
longer than GitHub's minute without a hint. Against the queue's backoff
(60, 120, 240, 480 s over 5 attempts): a fetch that fails fast at the
start of a cooldown is retried 60 s later, meets its last minute and
fails fast again, then runs on its third attempt 120 s after that. That
leaves two attempts for real failures. A fetch that meets 30 s or less
sits it out and runs on the attempt it is on. A run that meets another
429 starts a fresh 2 minutes.

---

## Page verdicts: one judge for every page, bot challenges included

**Decision:**

- One method, `Native.judgePage`, makes every verdict on a page that came
  back, from the origin (`tryReadability`) or from Jina (`jinaOnce`). It
  looks at a `pageView`: title, text, whether Readability found an article
  (always true for Jina), and the URL the request settled on (nil for
  Jina). Its order, pinned by `TestJudgePage_Order`:
  1. dead link, with detection on: redirected to the homepage (needs the
     final URL), or a not-found title (`soft404TitleRE`);
  2. bot challenge;
  3. login wall by redirect, needs the final URL: onto another site
     (page-level), or onto a login path (site-wide when the redirect stays
     on the site and changes the path);
  4. no article;
  5. thin: under `minArticleBytes` (500 bytes);
  6. a login-page title.
- A bot challenge is `ErrAntiBot`, so it goes to Jina like an origin
  403/503. It is recognized by an anchored, case-insensitive title
  (`challengeTitleRE`: "Just a moment...", "Attention Required! |
  Cloudflare", "Access Denied", "Access to this page has been denied",
  "Pardon Our Interruption", "DDoS-Guard", "Vercel Security Checkpoint",
  "Are you a robot?" and a few more), or by a phrase challenge pages use
  (`challengePhrases`: "checking your browser before accessing", "verify
  you are human", "why have i been blocked?", …). Phrases count only on
  pages of 2 KiB or less, lowercased and with ’ read as ', so an article
  about bot checks can quote them and still be stored.
- A challenge recognized in page content is page-level: `hostVerdict`
  caches anti-bot only when the origin's chain carries its
  `*HTTPStatusError` (a 403/503), keyed by the host of its URL. The
  fallback that cached any other `ErrAntiBot` under the requested host is
  gone; nothing produced it before, and a challenge seen after a redirect
  would have failed a healthy host for 15 minutes.
- `loginTitleRE` requires the verb to end the title, reach a separator
  (`| : · • – — -`), or go on with "to"/"or" ("Sign in to continue",
  "Login | BigCommerce Help Center", "Login or Sign Up"), or to end the
  title after a separator ("Google Docs: Sign-in", "Plaid - Dashboard |
  Signin"). "Sign In With Apple: A Developer's Guide" and "How to log in
  to Grafana with SSO" are articles.

**Why:** the Jina path had none of these checks, which is how challenge,
block, login and not-found pages were stored as articles (see "Jina answers
are judged like the origin's pages"), and neither path recognized a
challenge served with a 2xx. Reproduced: a 200 Imperva "Pardon Our
Interruption" interstitial was stored as a 509-byte article, and a 200
Cloudflare "Just a moment..." page failed as a thin login wall rather than
anti-bot. The old `^(sign in|log in|join now|join linkedin)` title rule
flagged articles such as "Sign In With Apple: …" (sending them to Jina) and
missed the library's real login pages. A Python port of the new rule
matched exactly the 45 login pages stored through Jina and none of the
4,453 titles fetched through readability, the GitHub API or yt-dlp; the
challenge titles match none of those 4,453 either.

**Not done:** Cloudflare's `cf-mitigated: challenge` header. Cloudflare
serves its challenges and blocks with 403 (503 in the legacy under-attack
mode), which `statusFailure` already makes `ErrAntiBot`, host-wide and
Jina-eligible, and no challenge page in the library came through
readability.

**Revised (2026-09-27):** the order is now, pinned by `TestJudgePage_Order`:

1. dead link, with detection on: redirected to the homepage or to another
   site's landing page (both need the final URL), or a not-found title;
2. bot challenge;
3. error page, judged like the status it names (see "Error pages whose
   status is hidden");
4. login wall by redirect, needs the final URL: onto another site's login
   page (final, never cached), or onto the site's own login path
   (site-wide when the path changed);
5. no article; 6. thin; 7. a login-page title, as before.

A redirect onto another site is no longer a login wall by itself: what it
reached is judged like any page (see "Cross-site redirects: judged where
they land"). The one `cf-mitigated: challenge` curio now reads is Jina's own
(see "Jina requests identify as curio"). The "Not done" note's
`statusFailure` now judges the redirect first: a 403/503 after a redirect
onto the homepage or another site's landing page (detection on) or onto
another site's login path is that verdict, never `ErrAntiBot`; only a
403/503 no redirect verdict explains is still host-wide and Jina-eligible.

---

## Jina answers are judged like the origin's pages

**Decision:** Jina's fallback policy is unchanged (`jinaCanHelp`), but a
2xx Jina answer is judged before it is stored (`judgeJinaAnswer`):

1. **The target's status**, from a `Warning: Target URL returned error
   <code>: <text>` line. `parseJina` now keeps every `Warning:` line, in
   order.

   | Target status | Verdict | Fetch result | Host cache |
   |---|---|---|---|
   | 404, 410 (detection on) | `ErrDeadLink` | `PermanentError`, document `dead` | never |
   | 403, 503 | rejected, `ErrAntiBot` | settled like a thin answer | origin's verdict only |
   | 408, 421, 425, 429, 5xx but 501/503/505; 404/410 with detection off | `errJinaTargetTrouble` | retryable | never |
   | any other (400, 401, 451, 501, …) | rejected | settled like a thin answer | origin's verdict only |

2. **Jina's CAPTCHA warning** ("This page maybe requiring CAPTCHA, …"):
   rejected, `ErrAntiBot`. The target's status wins over it, so an answer
   carrying both a target 404 and this warning is a dead link. It is
   matched by its opening words: another warning that merely mentions a
   CAPTCHA is informational.
3. **The page verdicts** every page gets (`judgePage`, see "Page verdicts:
   one judge for every page, bot challenges included"), with no final URL.
   The thin floor is `minArticleBytes`, 500 bytes, instead of 200
   characters. Jina's other warnings (iframes, shadow DOM, a cached
   snapshot, a page maybe not fully loaded) are informational.

- A rejected answer is `errJinaRejected` wrapping its reason, and replaces
  the old thin-answer sentinel: it is Jina's verdict about the target
  (`jinaAnswered`), and `settle` treats it as it treated a thin answer.
- No judged answer is retried within the fetch (`jinaRetryable`): each
  costs one Jina request.
- The target's status is a `targetStatusError`, never an
  `*HTTPStatusError`. That type is Jina's own answer: its 429 extends Jina's
  cooldown by its `Retry-After`, which a status the target gave Jina must
  not.
- A Jina answer's `Result.FinalURL` is the requested URL.

**Host cache: Jina verdicts never write it.** `settle` judges only the
origin's error, and only once Jina gave a verdict of its own, as before. So
an origin 403 plus a Jina rejection caches the origin's answering host, and
an origin thin page plus a Jina rejection caches nothing and fails for good.
A dead link seen through Jina is final and uncached, like `ErrTooLarge`,
even when the origin answered 403. A transient target status is the
target's trouble for now: retryable, uncached.

**Why URL Source is not a final URL:** Jina echoes the request there, as it
re-encodes it, and never where a redirect ended. Of the 1,297 documents
stored through Jina, 1,280 have a URL Source equal to the requested URL and
the other 17 differ only by percent-decoding (`Toronto%2C+ON` →
`Toronto,+ON`), msdn.microsoft.com → learn.microsoft.com included, which
does redirect. Storing it wrote a re-encoded copy of the bookmark into
`documents.url_canonical`.

**Why:** Jina answers 200 for whatever the target served, and only a 2xx
body over 200 characters was checked. In the library, 1,297 documents are
fetched through Jina, and the verdicts above reject 206 of them: 98
challenge titles (84 "Just a moment..." with 200–205-byte bodies, 7
"Attention Required! | Cloudflare", 7 "Access Denied"), 14 pages with a
challenge phrase (8 of them Reddit's untitled "You've been blocked by
network security"), 45 login pages (Atlassian, Google
Docs/Sheets/Drive and Accounts, BigCommerce, Plaid, …), 16 not-found titles
("Page not found | Free local classifieds - Kijiji" ×4, …) and 33 stubs
under 500 bytes. All 123 documents under 500 bytes are challenge, block,
parked-domain, login or stub pages; none is an article. A live probe of
`https://www.python.org/this-page-does-not-exist-xyz-123/` answered HTTP
200, title "Welcome to Python.org", a 13.8 KB body, and the line `Warning:
Target URL returned error 404: Not Found`, which alone gives it away.

**Known limitation: landing pages.** A cross-host redirect to a landing
page (java.sun.com → oracle.com/java/technologies/, forums.aws.amazon.com →
repost.aws/forums) comes back as a real-looking page from the origin and
from Jina alike. The library holds "Java Issue Tracker - Home" ×11, "Oracle
Java Technologies" ×10, "[GWT] Project" ×10 and "Forums" ×8 of these. No
per-page rule tells them from a real page, and a cross-host "redirected to
a root path means dead" rule would mark shortener and redirector links to
homepages dead, which is sticky. 5 of the 8 "Home" titles are genuine
homepage bookmarks. Refetching these would store the same page again.

**Existing documents:** nothing is migrated. `curio refetch <id>` re-judges
a stored Jina answer: junk ends `failed` or `dead` on its first attempt,
keeps its extraction, and leaves search (see "Search leaves out failed and
dead documents"). Clusters change only after `curio interests rebuild`.

**Not done:** 1,043 of the 1,297 were cross-host redirects that Jina simply
followed after curio had already seen them. Judging the redirect target
locally would save most of the Jina budget; it is a separate change.

**Revised (2026-09-27):** that change is made; see "Cross-site redirects:
judged where they land". A redirect onto another site's landing page is a
dead link and one onto another site's login page is final, both without a
Jina request, and any other cross-site redirect is stored from the origin's
answer when it passes. The landing pages above that kept a word of the
source ("Java Issue Tracker - Home" keeps the bug ID) remain a known
limitation. A Jina answer that is an error page without the target-status
warning is now rejected, or left to the job's retry for a server error; see
"Error pages whose status is hidden". Corrected: the `targetStatusError`
bullet above said Jina's `Retry-After` went on to the job. It never did:
the job queue retries on its own backoff (`retryBackoff`) and reads no
`Retry-After`, so Jina's only sets the length of Jina's cooldown. The
bullet now says so.

---

## Search leaves out failed and dead documents

**Decision:** `BM25Search` and `VectorSearch` never return chunks of
documents in state `failed` or `dead` (`AND d.state NOT IN (?, ?)`, bound
parameters, in `searchedDocSQL` next to the tenant scope). `/v1/search`,
`curio search`, MCP `search_bookmarks` and find-related inherit it. Pending
documents are still searched, so a document being refetched stays
searchable until its fetch fails. `VectorSearch` now always over-fetches
(`k = min(limit×10, 1000)`, the rule filters already used), since
sqlite-vec applies `k` before any document predicate and every query now
has one.

**Why:** a refetch keeps the current extraction and its chunks until a new
fetch replaces them, and a permanent failure only changes
`documents.state`. Neither retriever looked at the state, so a document
refetched into `failed` or `dead` kept matching search. 33 failed documents
in the library held 870 chunks of gzip garbage from an old fetch. The
contracts already assumed otherwise: `curio docs` calls the fetched view
"what's actually searchable", and find-related's entry says
pending/failed/dead documents have no indexed chunks.

- **Read-side, not a purge.** Deleting chunks in the permanent-failure hook
  would put a destructive write in `markDocFailed` and a new store method
  behind it, and would leave the existing 33 documents dirty. The filter
  fixes them without a migration, and a refetch that succeeds re-indexes
  the document and brings it back.
- **`NOT IN`, not `IN ('fetched', 'pending')`,** so the planner keeps
  driving from the FTS or vec table. `EXPLAIN QUERY PLAN` is identical with
  and without the predicate, and `TestQueryPlans` now pins the vector plan
  too.
- **Cost:** a synthetic 24k-vector corpus at limit 80 went from 16 ms to
  29 ms per vector query, small next to embedding the query.
- **Whichever job failed.** The index pool runs the same permanent-failure
  hook as the fetch pool, so a document whose index job gives up (Ollama
  down for the whole retry window, say) is `failed` too and leaves search,
  although the chunks from its last successful index are intact. That is
  the price of one rule: the state alone says whether a document is
  searched, and `failed` is what lists it under `curio docs --failed` for
  recovery. While the index job is still retrying, the document keeps its
  state (`pending` after a refetch, `fetched` during a `curio reindex`) and
  stays searchable.

`EmbeddingsForDocument` and `DocumentVectors` are unchanged: a failed or
dead source document still has vectors for find-related, and clustering
already reads fetched documents only.

---

## Cross-site redirects: judged where they land

**Decision:** a redirect onto another site (`crossSite`: both hostnames
known, not the same site by `sameSiteHost`) is no longer a login wall by
itself. `judgePage` judges the page it reached, with no Jina call unless
that page fails a page-level verdict:

- **Another site's landing page is a dead link**, with dead-link detection
  on. `looksLikeLandingPage` runs among the soft-404 checks, after the
  same-site homepage rule, and holds when all of these do:
  1. the destination is on another site;
  2. it is no login page: `loginPathRE` doesn't match its path, nor
     `loginTitleRE` its title;
  3. the source names a page: its decoded path, less a trailing index
     document (`index.*`, `default.*`), holds at least one word, and its
     path and query keys and values together hold at least two;
  4. the destination dropped the source's identity: no source word is a
     word of the destination's hostname, decoded path, or query keys and
     values. A destination query with a value equal to the source's decoded
     path is redirect bookkeeping (`?origin=/message.jspa`) and ignored;
  5. the destination names no page: its path has no segments, fewer than
     the source's (index document removed), a last segment of letters
     alone, or a bookkeeping query.

  A word is a maximal run of letters or digits, lowercased, at least two
  runes long. The verdict is `dead link (redirected to another site's
  landing page: <host><path>)`, a `PermanentError`: document `dead`, no
  Jina request, and no host-cache entry for the requested or the answering
  host.
- **Another site's login page is a final login wall**, whatever dead-link
  detection says: a login path (`loginPathRE`) or a login title
  (`loginTitleRE`) on the destination. `errOffsiteLoginWall` wraps
  `ErrLoginWall` but not `errSiteLoginWall`, and `tryReadability` makes it
  a `PermanentError`: document `failed`, no Jina request, never cached.
- **A 403 or 503 answer gets the same URL rules first.** Before
  `statusFailure` makes a 403/503 `ErrAntiBot`, `judgeRedirect` judges
  where the request settled, with `judgePage`'s own predicates and verdict
  helpers (`deadLink`, `loginWall`) in `judgePage`'s order: with dead-link
  detection on, a redirect onto the site's homepage or another site's
  landing page is the dead link above; whatever detection says, a redirect
  onto another site's login path is the final login wall. The verdict is a
  `PermanentError` wrapping the origin's `*HTTPStatusError` and the
  verdict's sentinel, never `ErrAntiBot`: `native: HTTP 403 Forbidden: dead
  link (redirected to another site's landing page: <host><path>): dead link
  (content is gone)`. `Fetch` returns it before it consults the host cache
  or Jina, so there is no Jina request, neither host is cached, and a fresh
  entry for the destination doesn't override it. The error body is drained
  unread, so there is no title: the not-found-title and login-title rules
  can't fire, and another site's login page counts only by its path.
  - Only 403 and 503, the only origin statuses that go to Jina. 404 and 410
    are dead links already; 408, 421, 425, 429 and the retried 5xx are
    retried without Jina and store nothing; any other status fails at once.
    A landing page that answers one of those keeps its status verdict.
  - A login path on the requested site that answers 403/503 stays
    anti-bot: host-wide and Jina-eligible, like the site-wide login wall it
    is on a 2xx.
  - With dead-link detection off, a homepage or landing destination that
    answers 403/503 stays anti-bot and may be stored through Jina, as a 2xx
    one is stored from the origin: the kill switch's trade.
- **Any other cross-site redirect is judged like any page.** A page that
  passes is stored from the origin's answer (`via: readability`,
  `Result.FinalURL` the destination). One that fails a page-level verdict
  (a challenge, a 403/503 error page, no article, too little text) is
  Jina-eligible and never cached; an error page naming another 5xx is
  retried without Jina (`errServerErrorPage`) and never cached. A 403 or
  503 status from it is anti-bot, cached under the destination (see "Host
  cache: only host-wide verdicts, under the host that gave them").
- A same-site redirect onto a login path is unchanged: site-wide when the
  path changed, host-cached, Jina-eligible.
- Jina answers carry no final URL, so no redirect rule fires on them.
- No new config key: `fetcher.native.dead_link_detection` governs the
  landing verdict with the other soft-404 rules.

**Why each condition:**

- (2) A login page is a wall, not a tombstone; it gets its own verdict
  below.
- (3) Shortener and profile links name a single word (`bit.ly/3xYzAb`,
  `youtu.be/<id>`, `twitter.com/<user>`); a redirect of theirs to a
  homepage says nothing about the link. This is the trap "Jina answers are
  judged like the origin's pages" warned about: a cross-host "root means
  dead" rule would kill them, and a dead verdict is sticky.
- (4) A move keeps something of the page: its slug
  (`farnamstreetblog.com/2013/06/the-work-required-to-have-an-opinion/` →
  `fs.blog/the-work-required-to-have-an-opinion/`), its ID
  (`twitter.com/jack/status/20` → `x.com/jack/status/20`, `youtu.be/<id>` →
  `youtube.com/watch?v=<id>`, `c2.com/cgi/wiki?BurnOut=` →
  `wiki.c2.com/?BurnOut`), or a name that moved into the hostname
  (`computing.llnl.gov/tutorials/pthreads/` → `hpc-tutorials.llnl.gov/posix/`,
  `content.time.com/time/…` → `time.com/archive/…`). The bookkeeping rule
  catches `forums.aws.amazon.com/message.jspa?messageID=284911` →
  `repost.aws/forums?newRedirect=1&origin=/message.jspa&messageID=284911`,
  whose query repeats the ID only to record where the request came from.
- (5) `/articles/`, `/forums`, `/technologies/` are sections; a numeric ID
  (`help.evernote.com/hc/articles/209125877`) or a slug
  (`nngroup.com/articles/why-you-only-need-to-test-with-5-users/`) names a
  page.

**Why:** the cross-host check in `looksLikeLoginWall` flagged every
redirect onto another site as `ErrLoginWall` before any other check. Each
cost a Jina request although the page already in hand was often the
article, and Jina follows the same redirect: judged without a final URL,
its answer was the destination's landing page, stored as the document.
`daemon.log` holds 4,756 such lines over 1,395 URLs, 1,117 of them still
cross-site under today's `sameSiteHost`. Of their documents, 695 were
stored through Jina, junk among them ("Ben Horowitz, Partner at Andreessen
Horowitz" ×11, "Java Issue Tracker - Home" ×11, "Oracle Java Technologies |
Oracle" ×10, "Forums" ×8, "Articles on Self-Knowledge, Relationships and
Calm" ×6, "Getting Real" ×5), and 398 failed. On 2026-09-26 326 failed for
good on a Jina 403 (see "Jina requests identify as curio"), legitimate moves
among them whose origin page was the article (`collabfund.com/blog/<slug>/`,
`fs.blog/<slug>/`, `en.wikipedia.org/wiki/<X>`, `x.com/<user>/status/<id>`).

The rule was built and tested against a live `curl -L` probe of 656 of
those URLs (2026-09-27): 492 end on another site, and the rule marks 78 of
them landing pages. 76 are junk (`java.sun.com/…` →
`oracle.com/java/technologies/`, `ibm.com/developerworks/…` →
`developer.ibm.com/technologies/`, 52 `thebookoflife.org/<slug>/` →
`theschooloflife.com/articles/`, …) and 2 are section-level bookmarks
(`pinterest.com/about/careers/` → `pinterestcareers.com/homepage`,
`store.nike.com/us/en_us/` → `nike.com/w`). No moved article is marked.
`TestLooksLikeLandingPage` pins the probe's cases.

**Why another site's login page is final:** 48 library URLs redirect onto
`accounts.google.com`, `id.atlassian.com`, `authn.edx.org`,
`sentry.io/auth/login`, `data.ai/account/login` or `dropbox.com/login`.
Jina has no session and follows the same redirect: it brought back the
login page (38, rejected since "Jina answers are judged like the origin's
pages"), a challenge (3), or the product's public page, stored as the
document (Gmail, "Google Drive: Share Files Online…", "Shareable Online
Calendar…"), never the bookmarked content. The verdict stays out of the
host cache: it would otherwise take the site-wide branch (the path changed)
and cache the requested host, `docs.google.com` say, for 15 minutes.

**Why a 403 or 503 gets the URL rules:** `statusFailure` classified them by
status alone, so the rules above never saw a redirect whose destination
refused curio. With Jina on, Jina followed the same redirect and, with no
final URL to judge, stored the destination's landing page: the failure this
entry fixes for 2xx pages, which a working Jina (see "Jina requests
identify as curio") would bring back. With Jina off, or once Jina gave its
own verdict, `settle` cached the destination anti-bot for 15 minutes on the
strength of another site's redirect, failing its healthy URLs without a
request, and the document's retry failed from that entry: `failed`, never
`dead`. Of the probe's 78 landing pairs, 14 answered 403: `typepad.com` →
`networksolutions.com/typepad` ×4, `sologig.com` → `careerbuilder.com/` ×3,
`pinterest.com/about/careers/`, `thinkgeek.com` →
`gamestop.com/collectibles`, `ftalphaville.ft.com` → `ft.com/alphaville`,
`jobamatic.com` → `simplyhired.com/`, `horizonsetfs.com` → `globalx.ca/`,
`focus.com` → `telepathy.com/` and `mindware.com` →
`mindware.orientaltrading.com/`. `daemon.log` shows it happening on
2026-09-26 between 22:06 and 22:48: curio's own transport got `HTTP 403
Forbidden` after these redirects and went to Jina, then `fast-fail from
host cache` lines followed for `www.networksolutions.com`,
`www.gamestop.com`, `www.ft.com`, `www.simplyhired.com` and
`www.mindware.orientaltrading.com`. The library holds 7 documents Jina
stored from exactly these redirects (see Existing documents).

- **Neither host is cached** because the verdict is about this URL and
  final at once, as dead links and offsite login walls always are: no
  retry exists for a cache entry to cut short, and the destination's own
  URLs get their own verdict when they are fetched.
- **The same-site homepage rule is included:** it is the same
  `looksLikeSoft404` URL rule on the same junk path, and Jina follows the
  redirect to `/` and stores the homepage. It stays under the kill switch.
- **The offsite login rule applies whatever detection says,** by its path,
  for the reason the 2xx case is final: otherwise the SSO host is cached
  anti-bot and Jina follows the same redirect.

**Known limitations:**

- A destination that keeps a word of the source is judged like any page,
  and stored when it passes: `bugs.java.com/?bug_id=<N>` ("Java Issue
  Tracker - Home", keeps the bug ID), `docs.oracle.com/en/java/javase/27/`
  (keeps "javase" and "docs"), `docs.cloud.google.com/appengine/docs`,
  `developer.ibm.com/languages/java/` (IBM `2edb88f5`, whose
  `…/java/library/…` source keeps "java"). No URL rule tells these from a
  real move without marking real articles dead.
- So is a source of one word or none: `bhorowitz.com/<slug>` (the a16z
  author pages), and a homepage bookmark such as `appdesignvault.com/`,
  now redirected to a gambling site.
- A landing page that answers an error status other than 403 or 503 keeps
  that status's verdict: a 429 or a retried 5xx ends `failed` once its
  retries run out, not `dead`.
- No title is read from a 403 or 503 answer, so another site's login page
  that only its title gives away (`accounts.google.com/ServiceLogin`) is no
  offsite login wall there: it stays anti-bot, or is a landing page when it
  kept none of the source's words.
- No per-document override: `curio refetch <id> --force` only lifts the
  409, and the new fetch applies the same rule to the same redirect, so it
  helps only once the redirect changes. A page that did move there is
  bookmarked at its new address (`curio add <url>`); a corpus the rule
  misjudges needs `fetcher.native.dead_link_detection: false`, daemon-wide
  and after a restart. An override per document would need a flag carried
  from the API through the job payload into the fetcher, and the probe
  found no moved article that the landing rule marks dead.

**Existing documents:** nothing is migrated; a refetch applies the rule.
The IBM notices `08bfd6d8`, `c108cb5b`, `e28b9286`, `1fca7860` and
`54218aa5`, and `appdesignvault.com/portfolio/fitpulse/` (`460c159b`), go
dead. IBM `2edb88f5` is stored as the IBM Developer "Java" page and
`appdesignvault.com/` (`aeab1376`) as the gambling site's, per the
limitations above. The 7 documents Jina stored on 2026-05-24 from a
landing page that answers curio 403 go dead: `9cec217a` ("Typepad |
Network Solutions"), `fb85af76`, `dee26197` and `a11710d5`
("CareerBuilder® - Search Jobs Hiring Now"), `5e331b28` ("Telepathy -
Powering Successful Brands"), `033bfd1a` ("Global X Investments Canada
Inc.") and `ca357865` (`pinterest.com/about/careers/`, a section-level
bookmark).

---

## Error pages whose status is hidden

**Decision:** `judgePage` recognizes an error page by its content, after
the challenge check and before the login-wall checks (`looksLikeErrorPage`):

- **By title** (`errorPageTitleRE`), anchored at both ends: the error
  phrase opens the title, optionally after "Error" and/or a status code
  with a separator, and either ends it or is followed only by a spaced
  separator and a site name ("Access forbidden : Stanford University", "403
  | Forbidden | Axway", "503 Service Unavailable", "Error 503"). Also
  Cloudflare's `<hostname> | 5NN: <reason>` and IBM's `<label>: The page
  you requested cannot be displayed`. "How to fix a 403 Forbidden error",
  "Service Unavailable: lessons from our outage" and "Understanding
  Cloudflare Error 526" are articles. 404 and 410 titles stay with
  `soft404TitleRE`.
- **By body**, on pages of at most 2 KiB like the challenge phrases:
  `<code>accessdenied</code>`, the S3 and Google Cloud Storage XML error,
  is a 403.
- **Judged like the status it names**, the mapping `statusFailure` and
  `targetStatusVerdict` share:
  - 403, 503, and the status-less refusals ("Forbidden", "Access
    forbidden", "Service Unavailable", "cannot be displayed", the XML
    error): `ErrAntiBot`, reason `error page: …`. Page-level: never
    host-cached (no `*HTTPStatusError` in the chain), Jina-eligible at the
    origin, and `errJinaRejected` in a Jina answer.
  - Any other 5xx a title names (500, 502, 504, Cloudflare's 52x and
    530): `errServerErrorPage`, retryable. At the origin it is neither
    Jina-eligible nor cached; in a Jina answer `judgeJinaAnswer` makes it
    `errJinaTargetTrouble`: retryable, uncached, no second Jina request.

**Why:** 12 stored documents are error pages: the IBM notice ×6, "Access
forbidden : Stanford University", Aptana's "403 | Forbidden",
"appdesignvault.com | 526: Invalid SSL certificate" ×2, and two untitled
Google Cloud Storage `AccessDenied` bodies of 588 bytes (`e9a41757`,
`080ce36f`). They were not served with a 2xx, as first thought: their
sites answered 503 or 403 (`daemon.log`, 2026-05-24) or redirected to a
site where Jina met Cloudflare's 526, and the Jina path of the time stored
whatever came back. Run over the stored bodies, today's `judgeJinaAnswer`
rejects every one when Jina's `Target URL returned error` warning is
present, and accepts every one without it; `judgePage` accepts every one as
a 2xx origin page. None matches the challenge, not-found or login titles,
and all but the GCS bodies exceed 500 bytes; Aptana's is 68 KB of cookie
banner, so only its title can tell. This check is defense in depth for the
two ways the status still goes missing: a Jina answer without the warning,
and an origin serving its error page with a 2xx.

Over all 7,982 stored titles, the title rule matches exactly the 11 error
pages (the 10 titled ones above and visage.co's "403 Forbidden"), and the
body phrase matches only the 8 S3 and GCS error bodies.

**Existing documents:** refetching them doesn't exercise the check today:
the IBM URLs now redirect to `developer.ibm.com` landing pages (see
"Cross-site redirects: judged where they land"), Stanford, Aptana and GCS
answer 403 at the origin, and `appdesignvault.com` redirects elsewhere.

---

## Jina requests identify as curio

**Decision:**

- Every Jina request sends a fixed, non-browser User-Agent,
  `jinaUserAgent` (`curio (+https://github.com/samsar/curio)`), with
  `Accept: text/plain` and the bearer key when configured. Never the Chrome
  profile's User-Agent, never `fetcher.native.user_agent`, and none of the
  `sec-ch-ua` / `sec-fetch-*` headers: those are for origins.
- **Jina's own 403 is no verdict about the target.** `jinaAnswered` puts it
  with 401 and 402, trouble with curio's client: the fetch stays retryable
  and nothing is cached, behind a thin page or an origin 403 alike. The
  target's own 403 comes in Jina's warning (`targetStatusVerdict`), and is
  still an anti-bot rejection.
- **A CDN challenge pauses Jina.** A 403 carrying Cloudflare's
  `cf-mitigated: challenge` is `errJinaChallenged` ("r.jina.ai's CDN
  challenged the request", still wrapping Jina's `*HTTPStatusError`). It
  extends the shared Jina cooldown by its `Retry-After`, or by
  `jinaChallengeCooldown` (10 minutes), with one WARN log line per
  challenged answer. Within the pause, a Jina-bound fetch still makes its
  origin request but sends no Jina request: it fails fast and retryably
  through the existing cooldown check (`awaitJina`), uncached. A 403
  without the header extends nothing.

**Why:** the Jina fallback had failed every call since 2026-08-30.
`jinaOnce` sent the origin's Chrome User-Agent, and r.jina.ai's Cloudflare
answers a browser User-Agent from a client that runs no JavaScript with a
managed challenge. Probed live on 2026-09-27 through curio's own transports:

| Backend | Chrome 133 User-Agent | `curio/1.4 (+https://github.com/samsar/curio)` | No User-Agent |
|---|---|---|---|
| chrome | 403, `cf-mitigated: challenge`, "Just a moment..." | 200, Jina's text format | 200 |
| stock | 403, `cf-mitigated: challenge`, "Just a moment..." | 200, Jina's text format | 200 |

`curl` agrees: 403 with a Chrome User-Agent, 200 with its own. `daemon.log`
shows 1,250 failed-job lines with a Jina 403 across 864 jobs since
2026-08-30 (and 5 with 451); the last document stored through Jina dates
from 2026-07-05. So anti-bot and login-wall pages and PDFs the local
extractor can't read have had no fallback at all. No decision ever chose
the browser User-Agent for Jina: "Chrome profiles carry their own
User-Agent and sec-ch-ua" is about origin requests.

The 403 made it worse: `jinaAnswered` took it for Jina's verdict about the
target, against "Host cache: only host-wide verdicts, under the host that
gave them". That produced 400 permanent page-level failures (`permanent
failure: jina: HTTP 403 Forbidden (after native: login wall or thin
content …)`), 402 origin-403 first failures that wrote the host cache, and
428 permanent cache hits served from those entries (`(cached: jina: HTTP
403 Forbidden …)`).

**Why a pause:** while the CDN refuses curio, every Jina call gets the same
answer. Pausing all of them saves each Jina-bound fetch a request that would
only be challenged again. The pause doesn't hold back the job queue's
retries, which follow its own backoff (about 1, 3, 7 and 15 minutes after
the first failure): one that comes due inside the pause fails at once,
retryably, and uses up an attempt, so a document can end `failed` before
Jina answers again; `curio refetch --all --state=failed` recovers it.
`jinaOnce` drops the challenge body, so the error names the challenge
instead.

**Ordering:** this shipped with "Cross-site redirects: judged where they
land". With Jina answering again, cross-site redirects still sent to it
would once more store the destinations' landing pages.

**Revised (2026-09-27):**

- **The 403 bullet, narrowed:** Jina's own 403 is no verdict only when it
  names no target. One whose reason names the target's host is Jina
  refusing that domain ("AbuseAlleviationError: Anonymous access to domain
  … blocked until …"), a verdict like its other 4xx refusals. A bare 403,
  one naming another host and Cloudflare's block page stay no verdict. See
  "Jina refusing a target is a verdict".
- **One warning per pause,** not per challenged answer: the warning is
  logged when a challenge starts a pause. Challenged calls that were
  already in flight only extend it. A probe of 8 concurrent challenged
  calls had logged 8 identical warnings. A challenge that meets the short
  pause of a 429 extends it without a warning; healthz and `curio doctor`
  still show the pause and the challenged call.
- **Jina's health is reported,** so an outage like this one shows in
  healthz, `curio doctor` and `curio status` instead of only in per-job
  lines. See "Fetch upstream health: Jina's calls are tracked and
  reported".

---

## Fetch upstream health: Jina's calls are tracked and reported

**Decision:**

- Every Jina request's outcome is recorded (`healthTracker`, in
  `internal/fetcher/health.go`), in one of nine classes (`CallClass`,
  given by `jinaCallClass`):
  - healthy answers: `ok` (the page), `judged` (a 2xx that is not the
    page: a rejection, the target's error status, a dead link, a body over
    the cap) and `refused` (Jina declined the target; see "Jina refusing a
    target is a verdict");
  - service-level failures (`CallClass.Failed`): `challenged` (the CDN's
    `cf-mitigated: challenge`), `forbidden` (any other 403), `rate_limited`
    (429), `auth` (401, 402), `server_error` (5xx, 408, 421, 425, and
    1xx/3xx) and `network` (a transport error, a timeout, a TLS failure, a
    body cut short).

  A request cut short by curio's own cancellation (shutdown) is not
  recorded, nor is a call never sent (the cooldown's fail-fast, a limiter
  error).
- The tracker's memory is fixed: counts per class for the last 15 minutes
  in 15 one-minute buckets, the last healthy answer's time, the last
  failure's time and class, and the current streak of failures since the
  last healthy answer (its count, its first failure's time and host, and
  whether another host failed). It runs no goroutine or ticker; every
  method takes `now`, which `Native` passes from its clock.
- The state (`UpstreamState`) is the first that holds: `disabled`
  (`fetcher.native.jina_fallback: false`), `failing`, `paused` (the shared
  Jina cooldown ends after now), `idle` (no calls in the window),
  `degraded` (at least a quarter of the window's calls failed), `ok`.
- `failing` holds when the current streak spans 2 or more target hosts and
  either has 5 failures (`failingStreak`) or 30 minutes lie between its
  first failure and its latest (`failingAfter`). It is decided only when a
  call is recorded, and lasts, through an empty window too, until the next
  healthy answer.
- Entering `failing` logs one WARN, `upstream failing`, with `upstream`,
  the latest failure's `class`, the `failures` in a row, the streak's
  start (`since`) and the error capped at 512 bytes (`err`). The next
  healthy answer logs one INFO, `upstream recovered`, with `failed_for`
  and `failures`. Both are decided and logged under the tracker's lock, so
  concurrent fetch workers log each transition once, in order. No other
  state change logs anything.
- Where it shows: `GET /v1/healthz` reports `upstreams`, one element
  (`jina`) from `Native.JinaHealth`, and its `status` stays `ok` whatever
  they say. `curio doctor` adds a `jina` check: ✓ disabled, idle or ok
  (with the window's calls by class, refusals and judged pages included),
  ! degraded or paused, ✗ failing, which fails the command; the last three
  carry a hint for the failure class with the most calls in the window (the
  last failure's class on a tie). `curio status` prints one warning line
  per failing upstream.

**Why:** the fallback failed every call from 2026-08-30 to 2026-09-27 and
nothing said so (see "Jina requests identify as curio"): healthz answered
`ok`, doctor and status had no Jina line, and the only trace was 1,250
`job failed … jina: HTTP 403` lines among thousands of others. The
thresholds come from the daemon log of the day it was fixed:

- **A 15-minute window:** a few pauses' worth of calls at 20 a minute, and
  `degraded` clears soon after the trouble does.
- **Degraded at a quarter:** Jina times out now and then while it works:
  51 of about 1,313 requests since the restart (about 4%) were retried
  after a transport timeout. A quarter is well clear of that.
- **Failing needs two hosts:** one slow target produces long runs of
  failures while Jina serves everything else. bespokeinvest.com timed out
  11 times that day, 4 times per fetch, while Jina served 577 pages. Any
  one host, however long it keeps timing out, can't make Jina failing.
- **5 in a row, or 30 minutes:** five failures across hosts with no
  healthy answer between them is no timeout noise. With a challenge's
  10-minute pause, a persistent challenge lets one Jina request through
  per pause, so the count alone would take 40 minutes to fire; the
  duration rule fires at the fourth challenged call.
- **Failing lasts until an answer:** the WARN is logged exactly when
  healthz starts reporting `failing`, and a daemon that stopped calling
  Jina never "recovers" without evidence.

**Why only failing is logged:** `degraded`, `paused` and `idle` are
derived when read, from the clock. Logging their changes would need a
ticker, and would chatter on every routine 429 pause. The challenge
warning stays, once per pause.

**Per request, not per fetch:** health is about the service's answers, and
the limiter and cooldown count requests too.

**In memory:** the state starts over when the daemon restarts, and Jina
reads `idle` until it is next called. The failures themselves stay in the
job log.

**Not done:** GitHub and yt-dlp don't feed a tracker (the types name
nothing Jina's, and `upstreams` is a list, so they can join without a
schema change); nothing is persisted; and no circuit breaker stops the Jina
calls while it is failing: the pauses Jina asks for (a 429, a challenge)
are already honored, and what is left is for a person to fix.

---

## Jina refusing a target is a verdict

**Decision:**

- `jinaOnce` reads at most `errorBodyDrain` (4 KiB) of a non-2xx Jina
  answer, in one bounded read that also drains it for connection reuse,
  and takes Jina's reason from it (`jinaReason`): a JSON answer's
  `message`, after its `name` when it has one, or the first line of any
  other answer when it names one of Jina's errors
  (`^[A-Z][A-Za-z]*Error: `). An HTML page, such as Cloudflare's challenge
  or block page, gives none. The reason is capped at 512 bytes
  (`snippet`), a configured `jina_api_key` in it is replaced with
  `[redacted]`, and it is quoted after the status in every Jina error:
  `jina: HTTP 401 Unauthorized: …`.
- `errJinaRefused` ("refused the target") marks Jina refusing the target
  (`jinaRefusesTarget`):
  - a 403 without `cf-mitigated: challenge` whose reason names the target's
    host (`namesHost`: one of the reason's hostname-shaped words, less a
    closing dot, equals `hostOf(target)`, case-insensitively, so a reason
    about mobile.twitter.com doesn't name twitter.com). A host inside a URL
    doesn't count: a service-wide 403 that echoes the requested URL would
    otherwise fail every document for good while health read ok;
  - every other deterministic 4xx, all but 401, 402, 403, 408, 421, 425 and
    429: 400, 404, 410, 422 and 451 as before.
- `jinaAnswered` is `errJinaRejected` or `errJinaRefused`, one source of
  truth that Jina's health classifier shares. A refusal is never retried
  within the fetch, extends no cooldown and writes no cache itself.
  `settle` decides the rest, unchanged:
  - behind a page-level origin failure (thin, no article, a login title, a
    challenge or error page, an unreadable PDF) the fetch is a
    `PermanentError` at once, and the document goes `failed`, not `dead`,
    on attempt 1;
  - behind a host-wide origin verdict (an origin 403/503, a redirect onto
    the site's own login page) the origin's answering host is cached and
    the first failure stays retryable; its retry fails from the cache
    without a Jina request, as after any Jina verdict.
- Any other 403 stays no verdict: a bare one, one whose reason names
  another host or none (an IP-wide block), Cloudflare's HTML. It is
  retryable, uncached and pauses nothing, and Jina's health counts it
  `forbidden`, a failure; a refusal counts `refused`, a healthy answer. A
  challenge 403 is unchanged.

A refused 403 behind a thin page ends in `last_error` as:

```
permanent failure: jina: refused the target: HTTP 403 Forbidden: AbuseAlleviationError: Anonymous access to domain … (after native: login wall or thin content (extracted text < 500 bytes))
```

`errors.As` still finds Jina's `*HTTPStatusError` before the origin's, and
`errors.Is` still matches the origin's sentinel.

**Why:** since the restart on 6ffbb77, 167 job failures across 39 jobs
read `fetch failed: jina: HTTP 403 Forbidden (after native: …)`, and no
challenge warning was logged: 34 jobs were mobile.twitter.com profiles
(thin origin pages), 2 LinkedIn `/uas/login` redirects, 1 investing.com
(an origin 403), and bespokeinvest.com. Every such 403 was retryable, so
38 of the 39 documents used all 5 attempts, one Jina request each: the 34
profiles alone took 170 attempts, about 8.5 minutes of the keyless 20 a
minute. And the error dropped Jina's reason. Keyless probes of r.jina.ai
on 2026-09-27 showed what the answers were:

| Target | Status | Content-Type | Body |
|---|---|---|---|
| mobile.twitter.com | 403, no `cf-mitigated` | `text/plain` | `AbuseAlleviationError: Anonymous access to domain mobile.twitter.com blocked until Sun Sep 27 2026 16:40:15 GMT… due to previous abuse found on https://mobile.twitter.com/…: DDoS attack suspected: Too many requests` |
| www.investing.com | 403, no `cf-mitigated` | `text/plain` | the same, blocked until 2039 |
| allrecipes.com | 451 | `application/json` | `{"code": 451, "status": 45101, "message": "This domain is excluded from Jina Reader at the request of its owner, People Inc.", "detail": "The publisher of this URL has instructed Jina AI to cease automated access to its properties. Jina Reader is complying with that request. For programmatic access to this content, please contact People Inc. directly regarding licensing.", "readme": "https://r.jina.ai/docs"}` |

The 403s come from Jina's own application (`via: 1.1 google`), not its
CDN. They are per-domain refusals, not an outage.

**Why the reason must name the target's host:** a 403 can't become a
verdict by exclusion. If r.jina.ai's CDN blocked curio with a plain 403, as
Cloudflare's 1020 block page does, every document would fail for good and
Jina's health would read `ok`, since refusals are healthy answers: the
outage again, unseen. Jina's domain blocks name the domain asked for; a
block of curio itself (Cloudflare's page, an IP ban) names no target. So a
403 that names none fails safe: retryable, and a `forbidden` failure toward
`failing`.

**Why `settle` is unchanged:** a refusal is a Jina verdict like a rejected
page, and "the first failure for a host gets one more real attempt" holds
for it as for any. The refusal itself is never cached: the block is Jina's,
per domain, and it ends.

**Not done:**

- **Pacing Jina calls per target domain.** Jina's abuse check named one of
  the library's own profiles: a burst of about 80 keyless mobile.twitter.com
  reads between 10:10 and 10:29 tripped the block, and it came back after
  a second burst at 11:30. Spacing the calls to one domain would avoid the
  block rather than survive it.
- **Remembering Jina's domain blocks** until the date they give, so later
  documents on a blocked domain skip Jina without a request.

---

## Queue gate: pause, throttle and schedule, persisted in SQLite

**Decision:** workers claim through a gate.

- `jobs.Gate` has one method, `Admit(kind, active, now) Verdict`. A worker
  asks before every claim, once for each kind it claims, under its own
  lock, passing how many jobs it already holds, and claims only the kinds
  admitted. The `Verdict{Closed, Until, Changed}` carries the reason, when
  the gate may reopen by itself, and a channel closed when what it was
  decided from changes; the zero verdict admits. `jobs.All(gates...)`
  gives the first closed verdict, in argument order.
- `jobs.QueueGate` is the daemon's gate. It checks, in order: paused
  (every kind, until a change), outside the daily schedule (every kind,
  until the window's next start), then the throttle's cap for the kind.
  `gentle` caps fetch at 4 and index at 1. Clustering, one job at a time
  already, and kinds without a pool are never throttled; `normal` caps
  nothing.
- The settings are one row of `queue_settings` (migration 011): `paused`,
  `throttle`, and `schedule_start`/`schedule_end` in minutes after local
  midnight, both NULL for no schedule. No row means the defaults.
- `GET /v1/queue` reports the settings, whether the queue is open
  (`state`, `reason`, `opens_at`), and each pool's `limit`, `running` and
  `pending`. `PUT /v1/queue` changes the fields given.
- The CLI has `curio pause`, `resume`, `throttle gentle|normal` and
  `schedule HH:MM-HH:MM|off`. `curio status` shows the queue's state and
  load, and `add --wait` and `import --follow` say when it is closed.

**Why SQLite, read once into memory:**

- A pause must survive a restart, including one launchd makes after a
  crash or a reboot: an overnight import the user paused must not resume
  unasked.
- `config.yaml` needs a restart to take effect, and it is the user's file:
  the daemon doesn't write it.
- The daemon is the only writer, so it reads the row once at startup and
  keeps the settings in memory. `Admit` does no I/O, a claim costs no
  extra read, and there is no hot row: `Update` writes only when a setting
  changes, and a PUT that changes nothing writes nothing.
- `Update` stores before it publishes, so a change the database refused
  never takes effect, and what runs is what the next daemon loads. The
  write runs detached from the request, bounded at 10 s like the workers'
  bookkeeping writes: mattn can report a statement cancelled mid-way as
  failed after it committed.
- A row the daemon can't read stops the start (`load queue settings: …`).
  Starting with the queue open would break the user's pause exactly when
  the database misbehaves.

**Why caps, not nice:** the heavy work is embedding, and Ollama does it in
its own process, on the GPU. `nice`, `renice` or `taskpolicy` on
curio-daemon don't reach it, and nice only reorders CPU time; it doesn't
reduce the work. What cools the machine is fewer concurrent embed
requests: gentle runs one index job instead of four. The fetch cap spares
the network and extraction.

**How pause and schedule combine:** both must allow a claim. Paused is
the reason reported when both hold. `curio resume` never overrides the
schedule: outside the window the queue stays closed until it opens, and
`curio schedule off` runs it now. The window is the daemon's local wall
clock, as the process read its zone at start, from its start up to its
end, wrapping midnight when the end is the smaller.

- `DailyWindow.Contains` goes by the wall clock alone, so a window is open
  twice in a fall-back hour.
- When a spring-forward gap skips the start, `NextStart` opens the window
  as the gap ends; a window wholly inside the gap opens the next day.
- `time.Date` doesn't promise which side of a gap it moves a skipped time
  to: Go 1.26 puts New York's 02:30 before its gap (01:30 EST) and Lord
  Howe's 02:15 after its (02:45). So the gap's end is whichever boundary
  of the zone period `ZoneBounds` reports is nearer.

**How workers wait:**

- A worker reserves a slot under its lock, together with the check, so
  its goroutines never reserve past a cap. The slot is held until the
  job's outcome is recorded, and released at once when the claim finds
  nothing. The reservation is per Worker; the daemon runs one Worker per
  kind, so it is the kind's count.
- While the gate admits none of its kinds, the goroutine waits on the
  verdict's `Changed` and a timer to its `Until`, capped at a minute
  (`maxGateWait`). It doesn't wake for enqueues, poll, or claim.
- The cap is there because Go's timers run on the monotonic clock, which
  on macOS (`mach_absolute_time`) stops while the Mac sleeps and ignores
  changes to the wall clock, while a schedule opens by the wall clock. It
  also catches the opening `NextStart` doesn't report when a fall-back
  hour repeats the start: `time.Date` resolves a repeated wall time to one
  occurrence (the earlier west of UTC, the later east of it, in Go 1.26),
  and the cap opens the gate within a minute of the other.
- A goroutine the throttle holds back waits only for a settings change.
  The goroutines holding the kind's slots claim the next job themselves
  when theirs ends, so whenever one is held, at least the cap's worth are
  running or waiting on enqueues. Waking the held ones on every finish
  would only add churn.
- Each verdict carries the change channel of the settings it was decided
  from, the rule `JobQueue.Enqueued` follows too (take the channel, then
  check), so a change made after the decision still wakes the waiter.
  `All` then composes gates without a goroutine merging their signals:
  while one gate holds the composite closed, only its change can open it,
  and the worker asks again.

**Running jobs are never interrupted:** the gate stops claims, not jobs.
A job claimed at 06:59 in a 22:00-07:00 window finishes after 07:00, and
a pause lets the jobs in hand finish; `curio pause` says how many.

**Daemon-wide:** `ClaimNext` and `RecoverOrphans` work across tenants, so
the settings have no `tenant_id` and `JobStore.QueueCounts` counts every
tenant's jobs. It is a covering walk of `idx_jobs_claim`'s pending and
running ranges with no sort: 2.6 ms with 55k pending and 100k done jobs,
however many finished jobs pile up, so `import --follow` reads it every
tick.

**`curio pause` starts a stopped daemon,** as every command that needs
the daemon does. Whatever that daemon claims before the pause lands
finishes as usual.

**Not done:**

- A gate that holds claims while the Mac runs on battery. It will be one
  more `jobs.Gate`, composed with `jobs.All`, reporting its own reason in
  `GET /v1/queue`.
- Pausing one kind, index only, say.
- Configurable gentle caps.
- A paused-since time in `GET /v1/queue`.

---

## Embedding model and per-home width

**Decision:** a new home embeds with `qwen3-embedding:0.6b` (Ollama's q8_0
build, 639 MB) at 1024 dimensions, and the vector width is the home's:

- `curiohome.Init` records the embedding model and width in the marker,
  with `format: 2` (`curiohome.CurrentFormat`), fixed for the home's life.
  `store.EmbeddingDim`, a process-wide 768, is gone; `store.MaxEmbeddingDim`
  (8192, sqlite-vec's `SQLITE_VEC_VEC0_MAX_DIMENSIONS`) bounds
  `embedding.dim`.
- `config.yaml`'s `embedding.model` and `embedding.dim` must match the
  marker. `curiohome.Home.CheckEmbedding` refuses a marker without format 2
  (`ErrLegacyHome`, checked first) and a mismatch (an
  `EmbeddingMismatchError` carrying both model/dim pairs), naming the
  files, what they record and the fix: `curio up --fresh`, which moves the
  home to `<home>.bak-<YYYYMMDD-HHMMSS>` and deletes nothing, or moving it
  aside by hand, then importing the bookmarks again; for a mismatch,
  setting `embedding.model`/`dim` back is the other fix. The daemon runs it
  after taking the lock and loading the config, before binding the port or
  opening the database, and `curio doctor` fails its home check offline
  with the same text. `curiohome.Open` stays permissive, so doctor and
  `curio up --fresh` can act on a legacy home.
- `sqlite.EnsureVectorIndex(db, dim)` sizes `chunks_vec` right after
  goose: it creates the table when missing, issues no DDL when the width
  matches, drops and recreates it in one `BEGIN IMMEDIATE` transaction
  when the width differs and it holds no vector, and refuses a table that
  holds vectors (`VectorWidthError`, naming the database and both widths,
  nothing changed). The width is parsed from the table's `CREATE`
  statement in `sqlite_master`; text it can't parse is an error, not a
  guess. The daemon sizes the chunk store and the embedder from the
  marker's width too.
- Migration 012 drops `schema_meta`.
- Queries get Qwen3-Embedding's instruction and documents nothing
  (`config.QwenQueryPrefix`, `embedding.document_prefix: ""`):

  ```text
  Instruct: Given a web search query, retrieve relevant passages that answer the query
  Query:
  ```

  That is one line break and nothing after the colon, byte for byte as
  Qwen/Qwen3-Embedding-0.6B's `config_sentence_transformers.json` and its
  `get_detailed_instruct` write it. The model card's TEI curl example puts
  a space after "Query:"; that is not the canonical prompt.

**Why a format, not inference from the model:** an old home whose
`config.yaml` pins `embedding.model: nomic-embed-text` and `dim: 768`
passes a model/dim comparison, and would then run under the new default
prompts: the Qwen instruction on nomic queries, no `search_document:` on
nomic documents. That is a half-working index that fails nowhere. An
explicit format refuses every old home, loudly. No migration code
converts one: its vectors are another model's, and the owner's decision is
a fresh home and a re-import (the owner's library: 130k chunks of
nomic-embed-text at 768).

**Why a Go step after goose, not a migration:** an applied migration never
changes (migrations/README.md), goose SQL takes no parameter, and a goose
Go migration would bake one width into its registration and appear in the
startup hooks without a source file. So 001 still creates `chunks_vec` at
FLOAT[768], and `EnsureVectorIndex` replaces that empty table on a new
database; on a normal start it costs one `sqlite_master` read. Dropping
and recreating the vec0 table inside `BEGIN IMMEDIATE` on a migrated
database works, the chunks delete trigger (`trg_chunks_delete`) still
resolves afterwards, and vec0 then rejects the old width ("Dimension
mismatch … Expected 1024 dimensions but received 768"); tests pin all
three. The DDL is built from the validated int, and the probes are named
constants.

**Why drop `schema_meta`:** nothing read it, and its one row claimed
nomic-embed-text at 768 for every home, which every 1024 home would
contradict. Its Down restores the table as 005 left it, with 001's row, so
the older Downs still run.

**What stays:** chunk sizing (384 words, 3500 bytes) and `num_ctx` 8192;
the re-derivation is in the revised "Embedder passes num_ctx=8192" and
"Chunker enforces a 3500-char hard cap" entries. `insight.min_similarity`
was tuned on nomic-embed-text vectors and is left at 0.5; re-tune it with
`curio eval` once a library is indexed with the new model.

---

## Embedding drift: the marker records the build, healthz reports a change

**Decision:** the daemon fingerprints the build that makes a home's
embeddings, the embedding model's manifest digest and the Ollama version,
and reports when it changes:

- `ollama.Client.ModelDigest` reads the digest from `GET /api/tags`, on the
  same exact-name lookup as `Ping`; `Version` reads `GET /api/version`.
- `internal/drift.Monitor` runs in the daemon. It checks at start, every
  minute and right after a rebaseline, each check bounded by 5 s whatever
  `embedding.timeout_seconds` allows. The first successful check records
  the fingerprint in the marker (`embedding_model_digest`,
  `ollama_version`), keeping every other field. Later checks compare, and
  report each changed part with its recorded and current value.
- `/v1/healthz` carries `embedding_drift` (`changes`, `fix: curio reindex
  --all`, `checked_at`) only while drifted, from the monitor's last check,
  with no Ollama call; `status` stays ok. `curio doctor` warns and lists
  the changes, `curio status` prints one warning line, and the log has one
  WARN per distinct drift, not one per check.
- `POST /v1/documents/reindex-all` for state `fetched` (the default)
  clears the recorded fingerprint once every job is enqueued and asks for
  a check, which records the build serving then. Other states leave the
  baseline alone; a failed reset is a 500 saying all N jobs were enqueued.
- The daemon never reindexes by itself, and the monitor never enqueues a
  job.

**Why:** Ollama 0.30.0 changed nomic-embed-text's vectors silently (its
release note: "nomic-embed-text now converts inputs to lowercase"). After
such an upgrade, or a pull that brings a new build of the model, new
query vectors no longer match the stored ones, and search gets worse with
nothing reporting it.

**Why `/api/tags`, not `/api/show`:** on Ollama 0.34.4, `/api/show`
returns capabilities, details, license, model_info, modelfile,
modified_at, parameters and template, and no digest; its modelfile's
`FROM` line names the weights blob (`sha256-970aa7…` for
nomic-embed-text), not the manifest. `/api/tags` returns each model's
manifest `digest` (`0a109f42…` for nomic-embed-text), the value `ollama
list` shows, which changes when a pull brings a different build. Values
are compared verbatim.

**Why the daemon records it, at its first successful check:** not at
`curiohome.Init`, because the daemon and `daemonctl.Discover` create homes
without Ollama running or the model pulled, and one writer, the daemon
under its lock, is simpler than a second code path in `curio up`; not at
the first embed, which would put marker I/O in the index path. An embed
can precede the record by at most one check interval, which matters only
if Ollama changed inside that minute. After startup the monitor is the
marker's only writer, under its own lock, so a rebaseline and a check
never interleave their writes. With Ollama down or the model not pulled a
check writes nothing, keeps its last report and logs only at DEBUG;
healthz and the pull already say so loudly. So does a check cut short. An
answer the check can't read (a model listed without a digest, a version
reply without a version) is reported nowhere else and keeps drift
detection off, so the first of a run of them is a WARN. A failed marker
write is logged at ERROR and retried at the next check.

**Why a fixed minute:** two small local requests a minute cost nothing,
and catch an Ollama upgrade while the daemon runs, without a backoff or
hooks into reachability changes.

**Why `reindex-all` resets at enqueue time:** the fetched documents are
every searchable one (failed and dead documents are left out of search),
so once their jobs are in, the whole searchable library is being
re-embedded by the build serving now. Search mixes old and new vectors
until the jobs finish.

**Why never reindex automatically:** re-embedding a large library takes
hours of the user's machine and Ollama; the user decides when.

---

## Embeddings never truncate; an over-long chunk fails at once

**Decision:**

- Every `/api/embed` body carries `"truncate": false` and
  `"keep_alive": "30m"` next to `options.num_ctx`. Neither is omitted;
  tests assert them on the raw JSON.
- A 400 whose error mentions the context length is
  `embedder.ErrInputTooLong`, with the `*ollama.StatusError` still in the
  chain, and a vector of the wrong width is `embedder.ErrWrongDimension`.
- The index handler makes both permanent: the job fails on its first
  attempt, the document goes `failed` (not `dead`), and `last_error` names
  the chunk range, the longest chunk's size in bytes and the reason.
- A query too long to embed degrades search to keyword results, with the
  reason in the warning; there is no API length limit.

**Why:** Ollama's `/api/embed` defaults `truncate` to true. On 0.34.4 a
73,889-character input without the field answered 200 with a silently
truncated vector: a vector for text the chunk doesn't hold. With
`truncate: false` the same request answers `HTTP 400
{"error":"the input length exceeds the context length"}`, alone or inside
a 3-input batch: one over-long input fails the whole batch. Older releases
word it "input length exceeds maximum context length"; both match. Both
failures repeat on every attempt, so retrying them only spent the job's
five attempts.

**No bisection:** the indexer doesn't retry halves of a failed batch to
find the long chunk. With chunks capped at 3500 bytes, a chunk past the
context means a misconfigured embedding model, not unusual content, and
the error names the longest chunk.

**keep_alive 30m:** Ollama unloads an idle model after 5 minutes by
default, so a paused, throttled or scheduled import reloaded the 639 MB
model at every burst. 30 minutes is a maximum idle time, not a
reservation: per Ollama's FAQ, an idle model is still unloaded to make
room when another model needs the memory, so it doesn't pin memory on an
8 GB machine. Generation sends none: labeling is a short burst.

---

## sqlite-vec: NEON distance kernels on arm64

**Decision:** curio builds sqlite-vec with its NEON distance kernels on
arm64:

- The Makefile, on arm64 (`uname -m` arm64 or aarch64), exports
  `CGO_CFLAGS` with `-DSQLITE_VEC_ENABLE_NEON`, keeping `-O2 -g` and
  extending a `CGO_CFLAGS` already given. It reaches `build`, `test` and
  `test-e2e`, whose `go build` inherits it. amd64 builds are unchanged.
- Every darwin/arm64 build in `.goreleaser.yaml` sets
  `CGO_CFLAGS=-O2 -g -DSQLITE_VEC_ENABLE_NEON`.
- `sqlite.VectorExtension` reads `vec_version()` and `vec_debug()`'s build
  flags; the daemon's "database ready" log line names both, so a released
  binary can be checked. Failing to read them is a WARN, not a failed
  start: only that line needs them. A test fails on arm64 when the flags
  lack "neon", saying to build through make or set `CGO_CFLAGS`, and
  skips elsewhere.

**Why:** sqlite-vec-go-bindings v0.1.6 compiles `sqlite-vec.c` with
`#cgo CFLAGS: -DSQLITE_CORE` alone, and the NEON L2 kernel is behind
`SQLITE_VEC_ENABLE_NEON`. curio's build reported `Build flags:  `, empty,
from `vec_debug()`: every search ran the scalar loop. With the flag it
reports `neon`. NEON is part of every ARMv8-A core, so nothing checks for
it at run time.

`BenchmarkVectorSearch` on an Apple M4 Max (16 cores, 64 GB), sqlite-vec
v0.1.6, 20,000 random unit vectors of 1024 dimensions in 200 documents,
median of 5 runs of 200 iterations:

| Query | Scalar | NEON | Speedup |
|---|---|---|---|
| vec0 KNN, k = 100 | 18.24 ms | 10.81 ms | 1.69x |
| `Chunks.VectorSearch`, limit 50 (k = 500) | 24.39 ms | 17.12 ms | 1.42x |

A raw vec0 run (k = 100, same machine) agreed: 18.1 against 10.4 ms at
1024 dimensions, 13.2 against 8.0 ms at 768. Scaled to the owner's 130k
chunks that is about 70 ms instead of 117 ms per search, extrapolated, not
measured.

**The `CGO_CFLAGS` trap:** setting `CGO_CFLAGS` replaces Go's default of
`-O2 -g`, so a bare `CGO_CFLAGS=-DSQLITE_VEC_ENABLE_NEON` would compile
SQLite itself, mattn/go-sqlite3's amalgamation, unoptimized. The Makefile
and goreleaser keep `-O2 -g`. A `go test` outside make on arm64 builds
without the flag and fails the NEON test, whose message says what to set;
CLAUDE.md's single-test command sets it.

**Rejected:**

- Vendoring `sqlite-vec.c` into curio with `#cgo arm64 CFLAGS`: NEON would
  be the default in every build, plain `go build` included, but it forks a
  ~9.6k-line C file out of Dependabot's reach.
- AVX on amd64: it needs `-mavx` and AVX hardware at run time, curio ships
  no amd64 build, and nobody asked for it.

---

## Open questions

Choices still open. Those settled since this list was started are at its
end, pointing to the entries that decided them.

- **Insight layer specifics:** trajectory analysis ("new this month"),
  cross-cluster interest merging, and a standalone `interests` table,
  deferred until there's real usage data.
- **Authentication for hosted mode:** the scheme (API keys vs OAuth vs SSO)
  is deferred. Nothing is stubbed in the local daemon, which trusts every
  local process that can reach loopback (see "Local API: loopback only, no
  token, browsers shut out").
- **Re-crawl policy:** how often to refetch a given URL. Likely
  domain-rule-driven (news daily, docs monthly, static essays never).
- **Highlight / read-later importers:** schema is ready; importer code is not
  in v1.
- **An embedding-based "this isn't really an article" classifier**, as a
  refinement of dead-link detection.

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

  The SOTA NL-search work itself is scheduled as **M6**, alongside RAG —
  see "M6 (planned): RAG / Q&A synthesis + SOTA natural-language search".

  **What's NOT under consideration:** building our own tokenizer,
  custom synonym dictionaries, query-classification pipelines. The
  hybrid retriever + RRF was chosen specifically to keep retrieval
  simple; any "smartness" should live in the query-rewriting layer
  above the retriever, not inside it.

Resolved since they were listed here:

- The clustering algorithm and labeling: "Insight layer: kNN-graph
  clustering + labeled interests (M4)".
- "Page Not Found" detection: "Dead-link detection: hard 404/410 +
  soft-404 heuristics" (title patterns and redirect-to-homepage; dead
  documents go to state `dead`).
- The eval harness the natural-language search options need: "Retrieval
  eval harness" (`curio eval --queries <qrels.yaml>`).
