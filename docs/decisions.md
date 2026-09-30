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
- 2026-05-23 — [API: cursor pagination, not offset](#api-cursor-pagination-not-offset) (revised)
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
- 2026-07-06 — [Insight layer: kNN-graph clustering + labeled interests (M4)](#insight-layer-knn-graph-clustering--labeled-interests-m4) (revised)
- 2026-07-06 — [LLM generation client (`generator.Generator`)](#llm-generation-client-generatorgenerator) (revised)
- 2026-07-06 — [Retrieval eval harness](#retrieval-eval-harness)
- 2026-07-06 — [M6 (planned): RAG / Q&A synthesis + SOTA natural-language search](#m6-planned-rag--qa-synthesis--sota-natural-language-search)
- 2026-07-06 — [nomic-embed-text task prefixes (`search_document:` / `search_query:`)](#nomic-embed-text-task-prefixes-search_document--search_query) (revised)
- 2026-07-06 — [Insight clustering quality: the "general-reading" mega-cluster (known limitation)](#insight-clustering-quality-the-general-reading-mega-cluster-known-limitation)
- 2026-09-09 — [Host-cache hits are permanent failures](#host-cache-hits-are-permanent-failures) (revised)
- 2026-09-24 — [Local API: loopback only, no token, browsers shut out](#local-api-loopback-only-no-token-browsers-shut-out) (revised)
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
- 2026-09-24 — [Folder and host filters: literal input, segment-boundary folders](#folder-and-host-filters-literal-input-segment-boundary-folders) (revised)
- 2026-09-24 — [API: handler edge cases found by coverage](#api-handler-edge-cases-found-by-coverage) (revised)
- 2026-09-25 — [updated_at: written by each statement, not by triggers](#updated_at-written-by-each-statement-not-by-triggers)
- 2026-09-25 — [Jobs reference their document through a column](#jobs-reference-their-document-through-a-column)
- 2026-09-25 — [Indexes follow the queries; plans are pinned by tests](#indexes-follow-the-queries-plans-are-pinned-by-tests) (revised)
- 2026-09-25 — [Chunks: external-content FTS, derived rows kept by triggers](#chunks-external-content-fts-derived-rows-kept-by-triggers) (revised)
- 2026-09-25 — [Worker wakeups: an in-process signal, and idle polls that back off](#worker-wakeups-an-in-process-signal-and-idle-polls-that-back-off) (revised)
- 2026-09-25 — [API: request IDs, one error mapping, logged server errors](#api-request-ids-one-error-mapping-logged-server-errors) (revised)
- 2026-09-25 — [API: tolerant responses, strict requests](#api-tolerant-responses-strict-requests)
- 2026-09-25 — [API: absolute content paths, and hydration errors fail the request](#api-absolute-content-paths-and-hydration-errors-fail-the-request) (revised)
- 2026-09-25 — [API: filters are validated, sizing knobs default](#api-filters-are-validated-sizing-knobs-default) (revised)
- 2026-09-25 — [Clients: one discovery, an explicit daemon environment, a signal context](#clients-one-discovery-an-explicit-daemon-environment-a-signal-context) (revised)
- 2026-09-25 — [Client errors: a typed APIError, and "unreachable" means never connected](#client-errors-a-typed-apierror-and-unreachable-means-never-connected)
- 2026-09-25 — [MCP sidecar: restart an unreachable daemon, retry once](#mcp-sidecar-restart-an-unreachable-daemon-retry-once) (revised)
- 2026-09-25 — [List pagination: keyset on (timestamp, id)](#list-pagination-keyset-on-timestamp-id) (revised)
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
- 2026-09-27 — [Daemon lifecycle: a per-user launchd agent](#daemon-lifecycle-a-per-user-launchd-agent) (revised)
- 2026-09-27 — [Keep-awake: caffeinate on AC power while the workers have queued work](#keep-awake-caffeinate-on-ac-power-while-the-workers-have-queued-work)
- 2026-09-27 — [curio up: a plan-first setup wizard](#curio-up-a-plan-first-setup-wizard) (revised)
- 2026-09-28 — [curio up: the import step](#curio-up-the-import-step)
- 2026-09-28 — [Dashboard: server-rendered pages in the daemon (phase 1)](#dashboard-server-rendered-pages-in-the-daemon-phase-1) (revised)
- 2026-09-28 — [Dashboard: formatting budgets for stored markdown](#dashboard-formatting-budgets-for-stored-markdown)
- 2026-09-28 — [Commands take a document's URL as well as its ID](#commands-take-a-documents-url-as-well-as-its-id)
- 2026-09-28 — [Doctor warns when GitHub requests carry no token](#doctor-warns-when-github-requests-carry-no-token)
- 2026-09-28 — [Failure causes: recorded when a document fails](#failure-causes-recorded-when-a-document-fails) (revised)
- 2026-09-29 — [Dashboard: a design language under the CSP](#dashboard-a-design-language-under-the-csp) (revised)
- 2026-09-29 — [Dashboard: search is home, the Overview becomes Status](#dashboard-search-is-home-the-overview-becomes-status) (revised)
- 2026-09-29 — [Dashboard: actions through /v1, sent by a first-party module](#dashboard-actions-through-v1-sent-by-a-first-party-module) (revised)
- 2026-09-29 — [Library: a Date saved order lists saves](#library-a-date-saved-order-lists-saves) (revised)
- 2026-09-29 — [Dashboard: the Failures tab](#dashboard-the-failures-tab)
- 2026-09-29 — [Interests page by offset within a run](#interests-page-by-offset-within-a-run)
- 2026-09-29 — [Search pages by offset within a fixed-depth pool](#search-pages-by-offset-within-a-fixed-depth-pool)
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

**Revised (2026-09-27):** the launchd half landed as `curio daemon
install` (and `uninstall`): a per-user agent that starts the daemon at
login and restarts it after a crash, and that clients start, stop and
restart the daemon through while it is loaded. Auto-start without an
agent is unchanged. See "Daemon lifecycle: a per-user launchd agent".
systemd stays deferred with Linux.

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

**Revised (2026-09-29):** the fanout no longer scales with k. Every search
reads one pool, 800 chunks from each retriever (8 × `store.MaxSearchK`),
ranks its documents, and returns the window `[offset, offset+k)` of that
ranking; see "Search pages by offset within a fixed-depth pool".
`find_related` keeps `max(50, 8·k)`.

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

**Revised (2026-09-29):** `GET /v1/interests` and `GET
/v1/interests/{id}` page by `offset` within a clustering run. A run's rows
are written once and never change, so an offset is exact there, and it
gives numbered pages the random access a cursor can't. `run_id` on every
interest shows a rebuild between two pages. See "Interests page by offset
within a run".

**Revised (2026-09-29):** `POST /v1/search` pages by offset too. A
search's order is a score recomputed on every request, so it has no
keyset, and within one ranking an offset is exact: every request for a
query ranks the same pool. See "Search pages by offset within a
fixed-depth pool".

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
`curio up` is to measure one to estimate an import, and the batch size and
the default timeout are to be re-checked against that number.

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

**Revised (2026-09-29):** a failed run is kept until the next run replaces
it. Pruning after a failure used to keep the latest done run alone, which
deleted the failed row at once whenever there was one, so a rebuild that
failed left no trace a reader could find. It now keeps the latest done run
and the run that just failed (`PruneRunsExcept` takes the runs to keep, at
least one): at most two rows, a later failure replacing the earlier, and a
success pruning both. `LatestRun` with no status, the newest attempt, is
then the failure, which the dashboard's Interests page reports (see
"Dashboard: actions through /v1, sent by a first-party module"); the
current interests are still the latest done run's. `LatestRun` breaks a tie
on `started_at`, kept to the millisecond, by insertion order.

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
`curio refetch <id>`, or one kind of failure with `curio refetch --all
--cause=anti_bot`; see "Failure causes: recorded when a document
fails") once the host is back: cheap and explicit, same posture as dead
links. The
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

**Revised (2026-09-28):** Two Fetch Metadata rules join these, and the
daemon now serves one browser client of its own.

- A change (any method but GET, HEAD and OPTIONS) that carries
  `Sec-Fetch-Site` gets 403 unless it carries it once, valued
  `same-origin`. Browsers send it on every request; the CLI and the MCP
  sidecar send none, and pass. The Origin rule already refuses other
  sites' changes; this one also holds where a browser leaves Origin off,
  and keeps the daemon's names apart: a page from `localhost:P` changing
  `127.0.0.1:P` sends an Origin the daemon accepts, but is cross-site. It
  runs after the Origin check, in the starting router too, so a refused
  change during startup is a 403, not a 503.
- The dashboard (see "Dashboard: server-rendered pages in the daemon
  (phase 1)") is admitted because it is same-origin: its pages come from
  the daemon's own port, their requests carry the daemon's own Origin,
  and they are GET-only. Another site's page loading one as a
  subresource (`Sec-Fetch-Site` cross-site or same-site without a
  top-level navigation) is refused.

Rejections are logged with the `Sec-Fetch-*` headers the request carried.

**Revised (2026-09-29):** the Origin allowlist also holds the bound
address's own origin, `http://<daemon.listen's address>:P` (IPv6 in
brackets). A page served from a `daemon.listen` of 127.0.0.2 is the daemon
itself, and the dashboard's changes carry that page's origin: without it
every change from such a page was refused, though its reads, which carry
no Origin, worked. The address is loopback by config validation, so this
admits nothing new. See "Dashboard: actions through /v1, sent by a
first-party module".

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

**Revised (2026-09-27):** with the home's launchd agent loaded, clients
start the daemon through it (`launchctl kickstart`) instead of spawning
it, under the same start lock, and wait on the PID launchd reports as on
a child; the flock stays the safety net, so a daemon launchd starts next
to a spawned one just loses the lock. Stop goes through `launchctl kill
SIGTERM` when launchd runs the lock holder, and signals directly
otherwise. The start lock is also held across installing, removing and
restarting the agent, not only while spawning. The daemon exits 0 when a
signal stopped it and when it finds the lock held (ErrAlreadyRunning),
which the agent's KeepAlive leaves alone. See "Daemon lifecycle: a
per-user launchd agent".

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

**Revised (2026-09-28):** `GET /v1/documents` filters by `host` and
`folder` with the same rules, built by the same code: `hostPredicate`,
which the search host filter uses too, and `folderPredicate`, which the
bookmark list uses too. Documents have no folder, so the folder filter is
an EXISTS over the document's bookmarks of the tenant: a document with
several bookmarks in the folder is listed once, and one without bookmarks
never matches a folder.

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

**Revised (2026-09-28):** `PreviewIngest`, the import's dry run, needs no
new index: it looks each URL up in the unique constraints' own indexes
(`sqlite_autoindex_documents_2`, `sqlite_autoindex_bookmarks_2`), and its
plan is pinned too.

**Revised (2026-09-28):** The documents list's `content_type`, `host` and
`folder` filters need no index either. Under each, alone or together, with
or without `state`, the list still walks `idx_documents_tenant_updated` or
`idx_documents_tenant_state_updated` in its order from the cursor, with no
temporary b-tree, checking the filters on each row it reads; the folder
filter's EXISTS seeks `idx_bookmarks_document`. At 50k documents and 50k
bookmarks the slowest selective filters took about 11 ms (a host with 5
documents), 30 ms (a folder with 5) and 7 ms (a content type with 10),
against 0.14 ms unfiltered: a page walks the list until it has its rows,
the trade the bookmark list's folder filter already makes.
`idx_documents_tenant_ctype` stays dropped. The Document page's two reads
are pinned too: `GetWithLastError` is a point search of the primary key,
and `ListByDocument` seeks `idx_bookmarks_document` and sorts its few rows
by `saved_at`. Ordered by `created_at` instead, SQLite walks
`idx_bookmarks_tenant_created` through every bookmark the tenant has to
skip that sort.

**Revised (2026-09-28):** migration 014 adds
`idx_documents_tenant_cause_updated`, a partial index of the documents
that failed, for the cause filters and the failure summary, and the jobs
list by document writes its tenant term `+j.tenant_id` so it seeks
`idx_jobs_document` instead of walking a tenant index. Both are measured
and pinned; see "Failure causes: recorded when a document fails".

**Revised (2026-09-29):** migration 015 adds `idx_bookmarks_tenant_saved
(tenant_id, saved_at, id)` for the bookmark list's saved order, and
rebuilds `idx_bookmarks_document` as `(document_id, saved_at, id)`. With
the new index alone, SQLite walked every bookmark the tenant has in saved
order, rather than seek one document's and sort them, for `ListByDocument`
and for an untitled document's bookmark title (1.2 ms instead of 9 µs,
and 4.5 ms instead of 0.16 ms for a Library page of untitled documents,
both growing with the library). With the rebuilt index both seek the
document and read its bookmarks in their order: `ListByDocument` no
longer sorts, and its pin now refuses a sort. `document_id` stays the
index's first column, so `TagsForDocument`, the EXISTS of the folder
filter and of search's source filter, and the foreign-key action keep
their seek; a BM25 search by source is pinned too now. `Count` is served
by either tenant index as a covering index, and SQLite now takes the
saved one, which its pin names. See "Library: a Date saved order lists
saves".

**Revised (2026-09-29):** the interests' reads are pinned too, and need no
index. `ListClusters` seeks `idx_clusters_run` and sorts only clusters of
the same size; `ClusterMembers` seeks the primary key's index and sorts the
cluster's members; both pins allow those sorts. `GetByIDsWithLastError`,
which hydrates every member of a page at once, seeks the documents primary
key for each ID of a JSON array, its tenant written `+d.tenant_id` so that
it never walks a tenant index; its pin refuses one. See "Interests page by
offset within a run".

**Revised (2026-09-29):** search's new read needs no index either.
`Snippets` reaches each chunk's FTS row by rowid, its `seq` found through
`sqlite_autoindex_chunks_1`, with the MATCH only marking terms (FTS5 plans
it `INDEX 0:=M`); its plan is pinned. Search names its untitled hits with
the members' `GetByIDsWithLastError` rather than a read of its own: a
bookmark-title query written `d.tenant_id = ? AND d.id IN (…)` fell into
the same trap, planned on the test database as a seek of the primary key
for three IDs, which its pin used, and as a walk of the tenant's documents
from four on. See "Search pages by offset within a fixed-depth pool".

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

**Revised (2026-09-29):** `POST /v1/search` validates its filters the same
way: a `filters.content_type` or `filters.source` value outside its set is a
400 naming the allowed values, refused before the query is embedded.
`{"query":"kafka","filters":{"content_type":["articles"]}}` answered 200 with
no items, so `curio search --type articles` printed "no results" and an MCP
client passing "articles" found nothing. `filters.host` stays free-form:
hosts are literal input.

**Revised (2026-09-29):** `offset` on `GET /v1/interests` and `GET
/v1/interests/{id}`, and the dashboard's `?page=`, are positions, not
sizes: read as the default, a wrong one would answer the first page for
another. So they are refused as a filter is, a 400 naming them when they
aren't a whole number in range (`positionParam`); `limit` and `members`
keep their defaults. See "Interests page by offset within a run".

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

**Revised (2026-09-28):** `daemonctl.BaseURL` drops a trailing slash from
`--daemon-url`. The client joins the API's paths onto the base, so
`http://127.0.0.1:8765/` asked for `//v1/healthz`, which is no route, and
every command failed to find the daemon.

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
  `created_at DESC, id DESC`, newest first by a key that never changes,
  or with `order=saved` by `saved_at DESC, id DESC`, newest saved first
  (the Library's Date saved order), which never changes either.
- `store.PageKey{At, ID}` is the last row of a page. `ListDocumentsOpts`
  and `ListJobsOpts` gained `After`, which replaced `ListBookmarksOpts`'
  `Cursor`; a non-zero key restricts the list to rows strictly after it
  with the row-value predicate `(updated_at, id) < (?, ?)`.
- The handlers ask the store for one row more than the page, so
  `next_cursor` is present exactly when another page follows. The cursor
  is base64url of `{"t": <RFC 3339>, "id": ...}`, plus `"o"` naming the
  order when it isn't the list's default (`"o":"saved"`). One that doesn't
  decode to a time and an ID, or that another order issued, is a 400
  "invalid cursor", never ignored.
- `BookmarkStore.List` returns `BookmarkWithDocument`, what the list shows
  of each bookmark's document (its state, title, type, cause and last
  error) read through a `LEFT JOIN` in the same query, instead of the
  handler loading each bookmark's document.
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
| `idx_jobs_document (document_id, status, updated_at)` | a document's last error (`curio docs`); `ListWithDoc` by document, with or without status or kind (its tenant term written `+j.tenant_id`); the FK action when a document is deleted |
| `idx_jobs_tenant_status_updated (tenant_id, status, updated_at, id)` | `ListWithDoc` by status, with or without kind, and no document; `CountByStatus`; `MetricsByKind`'s window; `PruneOlderThan`; `DeleteByStatus` |
| `idx_jobs_tenant_updated (tenant_id, updated_at, id)` | `ListWithDoc` unfiltered or by kind only |
| `idx_documents_tenant_state_updated (tenant_id, state, updated_at, id)` | `ListWithLastError` by state, and no cause; `CountByState`; `ListIDsWithContent`; `DocumentVectors` (now covering); `RequeueFetchByStates` without a cause |
| `idx_documents_tenant_cause_updated (tenant_id, failure_cause, updated_at, id) WHERE failure_cause IS NOT NULL` | `ListWithLastError` by cause, alone or with state, host or folder; `RequeueFetchByStates` by cause; `FailureSummary` |
| `idx_documents_tenant_updated (tenant_id, updated_at, id)` | `ListWithLastError` unfiltered |
| `idx_bookmarks_tenant_created (tenant_id, created_at, id)` | `Bookmarks.List` in created order, unfiltered or under any filter (checked per row) |
| `idx_bookmarks_tenant_saved (tenant_id, saved_at, id)` | `Bookmarks.List` in saved order, unfiltered or under any filter (checked per row); `Count` (covering) |
| `idx_bookmarks_document (document_id, saved_at, id)` | `ListByDocument`, read in order; an untitled document's bookmark title in `ListWithLastError` and `GetWithLastError`; `TagsForDocument`; the EXISTS of the documents list's folder filter and of search's source filter; the FK action when a document is deleted |

**Revised (2026-09-29):** the bookmark list gained the saved order, with
the Library's filters, migration 015's two index changes above, and
cursors that record a non-default order. Cursors of a default order encode
as before, so the ones already issued keep working, and one of another
order is refused rather than resuming that walk at an unrelated position.
See "Library: a Date saved order lists saves".

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

**Revised (2026-09-27), with `curio up`:** `Pull` reports each line of the
stream (status, digest, total, completed) to a callback instead of logging
it: `curio up` draws a progress line from it, and `KeepPulled` passes one
that logs about every 10%, as before. A line over 64 KiB fails the pull,
and so does a stream silent for 5 minutes, naming the model: read with an
unbounded decoder on a client with no timeout, a stalled stream blocked
until the daemon stopped, and `KeepPulled` never retried it. The base-URL
rule is exported (`ValidateBaseURL`) for `config.Validate`.

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

**Revised (2026-09-28):** a document Jina refused behind a page-level
origin failure (a thin, login or challenge page, an unreadable PDF) fails
for good on that attempt and records the cause `jina_refused`, so
`curio refetch --all --cause=jina_refused` retries those once the block
lifts. Behind a host-wide origin verdict the refusal isn't the
document's last word: its retry normally fails from the host cache, so
it records the cached verdict, `anti_bot` for an origin 403 or 503 (the
first document of the site included), `login_wall` for a redirect onto
the site's own login page, and `--cause=anti_bot` is what reaches it.
See "Failure causes: recorded when a document fails".

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
  `GET /v1/queue`; `keepawake.Pmset` is the power probe it can reuse (see
  "Keep-awake: caffeinate on AC power while the workers have queued
  work").
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

## Daemon lifecycle: a per-user launchd agent

**Decision:** on macOS, `curio daemon install` installs a per-user
launchd agent that keeps the home's daemon running; `curio daemon
uninstall` removes it. While the agent is loaded, clients start, stop and
restart the daemon through launchd; without it they spawn it, as before.

- **The seam** is `internal/service.Manager` (Status, Install, Uninstall,
  Start, Stop, Restart), not a `setup.ServiceManager`: daemonctl drives
  the daemon through it, and the `curio up` wizard (internal/setup)
  imports daemonctl, so a seam in setup would be an import cycle.
  `service.ForHome` picks the implementation at run time (`runtime.GOOS`):
  `Launchd` on darwin, `Unsupported` (manages nothing, every change fails
  with `ErrUnsupported`) elsewhere. No build tags, so the launchd code
  compiles and its tests run on the Linux CI job.
  `servicetest.Fake` is the in-process manager tests inject;
  `daemonctl.New` leaves the manager nil, so the e2e harness and
  curio-mcp's tests spawn as before.
- **The label** is `com.github.samsar.curio.daemon` for `~/.curio`, and
  for any other home that plus `.` and the first 8 hex digits of the
  SHA-256 of its canonical path (symlinks resolved). With one label for
  every home, `curio --curio-home B daemon start` would kickstart home
  A's agent and then wait on B's lock.
- **The plist**, at `~/Library/LaunchAgents/<label>.plist`, rendered from
  a template with every string XML-escaped, written atomically (temp
  file, fsync, 0644, rename; launchd refuses a group- or world-writable
  plist) and golden-tested:
  - `ProgramArguments`: the daemon's path as installed, never resolved
    through symlinks (`/opt/homebrew/bin/curio-daemon`, not the Cellar),
    so an upgrade needs no new plist. `CURIO_DAEMON_BIN` is made absolute.
  - `RunAtLoad` true, `KeepAlive {SuccessfulExit: false}`: launchd starts
    the daemon at load and login, and restarts it after a non-zero exit.
  - `ExitTimeOut` 25. `launchctl print` shows `exit timeout = 5` for
    agents that don't set it, and the daemon's shutdown takes up to 20s
    (5s HTTP, 15s drain): a bootout, logout or `kickstart -k` would
    SIGKILL a draining daemon, orphaning its jobs with the attempt kept.
    The chain is 20s (daemon) < 25s (launchd) < 30s (daemonctl's stop
    wait), asserted by a test.
  - `StandardOutPath` daemon.log, `StandardErrorPath` launchd.err. The
    daemon now logs to stdout, so daemon.log stays the one structured log
    (`curio daemon logs`, a failed start's quoted tail), and launchd.err
    gets only what the runtime writes as the process dies. Spawned
    daemons still send both streams to daemon.log.
  - `EnvironmentVariables`: `CURIO_HOME` for a non-default home, and a
    fixed `PATH` of Homebrew's directories then the system's. launchd's
    default is `/usr/bin:/bin:/usr/sbin:/sbin`, where yt-dlp (looked up
    once, at startup) and node aren't; copying the installing shell's
    PATH would bake in whatever that shell had. Nothing else of the
    environment is carried: `CURIO_GITHUB_TOKEN` and `CURIO_JINA_API_KEY`
    belong in config.yaml, `curio daemon install` warns about each it
    sees, and no token is ever written to the plist.
  - No `ProcessType`: launchd's default suits a service the user waits
    on.
- **The verbs**, all in `gui/<uid>`, never with sudo, each bounded (10s;
  40s for what waits on the daemon's exit), with capped output, failing
  as a `*LaunchctlError` that quotes the exit status and stderr:
  - Install: `print` first (bootstrapping a loaded label fails with an
    opaque "Bootstrap failed: 5"), nothing more when the loaded plist is
    byte-identical, otherwise `bootout` and poll `print` until the label
    is gone (bootout returns before the job has stopped, and `--wait` may
    not exist on every release), then write, `enable` (undoing an earlier
    `launchctl disable`, as `brew services` does) and `bootstrap`.
  - Uninstall: `bootout`, the same poll, then remove the plist.
  - Start: `kickstart -p`; Restart: `kickstart -k -p`, whose termination
    signal is launchd's to choose (ExitTimeOut covers a SIGTERM); the PID
    is the last number printed, since the output's form isn't documented,
    or `print`'s when there is none. Stop: `kill SIGTERM`.
  - Status: no launchctl at all without a plist, so homes without an
    agent never fork it; otherwise `print`, reading only the one-tab-deep
    `state`, `pid`, `last exit code` and `last terminating signal` lines
    (nested blocks have `state = active` lines of their own). Exit 113
    (not loaded) and 112 (no GUI domain) are statuses, not errors.
- **Clients** (`internal/daemonctl`): starting takes `daemon.start.lock`
  as before, then kickstarts when the manager reports the agent loaded,
  and spawns otherwise: no agent, one installed but not loaded, or no GUI
  session (over ssh), where an agent can't run. A manager that can't say
  fails the start. The launched daemon is waited on like a child:
  StartTimeout of silence, the start lock released once its PID holds
  daemon.pid, and, while it doesn't hold the lock, the manager says
  whether it still runs, so one that dies before serving is reported at
  once with launchd's last exit, the daemon.log tail and launchd.err's
  when the launch wrote it. Stop re-probes the lock and asks the manager
  when it runs the holder; a daemon started outside launchd is signalled
  directly. Install, Uninstall and a restart through launchd run under
  the start lock, bootout and `kickstart -k` waits included (up to about
  40s), so no auto-starter starts a daemon in the middle of one: Install
  stops a daemon running outside the agent (it would keep the lock from
  the agent's), and refuses an agent whose daemon couldn't bind because
  another home's daemon serves the port (launchd would relaunch it every
  10s); Uninstall returns only once the agent's daemon has released
  daemon.pid, so `curio up --fresh` can move the home after it.
- **Exit status:** the daemon exits 0 when a signal stopped it, even if
  its shutdown failed part way (a request outliving the API's 5s grace),
  and when another daemon holds the home's lock; otherwise 1. Before, a
  stop during a slow request exited 1 and launchd restarted the daemon
  the user had stopped, and a second daemon exited 1 every 10s for ever.
  A bad config.yaml or a taken port still exits 1, and launchd's
  throttled retries pick up the fix.
- **Versions:** `Controller.EnsureVersion` restarts, once, a daemon that
  reports another version than the caller's (`curio up` after an
  upgrade), through `kickstart -k` when launchd runs it and Stop plus
  EnsureRunning otherwise, never taking the old daemon's lock or answers
  for the new one's; still mismatched, it fails with
  `ErrVersionMismatch` naming what runs. `curio daemon start` warns when
  the daemon runs another build than the CLI, the case of rebuilt
  binaries next to a daemon that kept running.
- `curio daemon status` and `curio doctor` report the agent: doctor warns
  about a missing program, an agent installed but not loaded, and one
  running another curio-daemon than this curio's.

**Why an agent, not a login item or a LaunchDaemon:** an agent is the
per-user, no-sudo way to run at login and after crashes, in the user's
GUI session where the home and Ollama are. macOS 13+ shows it in Login
Items & Extensions, where the user can disallow it; the install says so.

**Rejected:**

- The daemon opening daemon.log itself: foreground `curio-daemon` runs
  would print nothing. Both streams to daemon.log: the plist's decided
  launchd.err would be empty for good.
- Build tags for the launchd code: its tests wouldn't run on CI.
- `bootout --wait`: not verified on every macOS release curio supports;
  polling `print` works on all of them.

**Not done:** systemd, with Linux. An end-to-end test of `curio daemon
install` against a built binary: it would need env overrides for the
launchctl path and the agents directory, which nothing else needs.

**Revised (2026-09-27), with `curio up`:**

- **Exit status:** the daemon exits 0 when a restart would change
  nothing, and 1 when one might help:
  - 0: a signal stopped it (even if its shutdown failed part way);
    another daemon holds the home's lock; or it refused to start for a
    cause that stays until someone fixes it: a home it can't open or
    create (not ours, not a directory), a config.yaml it can't read,
    parse or validate, a home `CheckEmbedding` refuses (unreadable
    marker, legacy, newer, a mismatch), or a vector index of another
    width than the home's (`*VectorWidthError`). `run` marks these
    refusals where it knows them, with an unexported `refusal` type that
    `exitCode` finds with `errors.As`, never by matching messages, and
    `finish` logs one once, at ERROR, saying the daemon stays down until
    the cause is fixed and a curio command (or `curio up`) starts it.
  - 1: everything else: a port it can't bind, a database it can't open
    or migrate, queue settings it can't read, a crash. These may clear by
    themselves, or are the crash recovery launchd is for.

  **Why:** a refused home or config.yaml exited 1, and under `KeepAlive
  {SuccessfulExit: false}` launchd relaunched it every 10s for ever,
  logging an ERROR each time, when only an edit clears it. For the same
  reason `config.Validate` now rejects what the daemon refused only after
  binding its port and migrating: an `embedding.base_url` or
  `generation.base_url` that isn't an http(s) URL with a host, by the rule
  `ollama.New` uses (`ollama.ValidateBaseURL`), and `fetcher.default:
  web2md` without `fetcher.web2md.bin`. No `ThrottleInterval`: launchd
  only delays the restart of a process that exits fast, and with the
  refusals exiting 0, what is left should retry at the default 10s. The
  "already running" line now says to run `curio daemon stop` first to
  replace the running build with the new one.
- **Install asks the manager first** (`Manager.Preflight`, which returns
  what `Install` would fail with, changing nothing: root, a program
  launchd couldn't run, no GUI session as `service.ErrNoGUISession`) and
  stops nothing for an install that can't happen. Over ssh, `curio daemon
  install` used to stop the running daemon and then fail. `Launchd.Install`
  calls `Preflight`, so the refusals are defined once.
- **No GUI session** for an installed agent (`launchctl print` exit 112)
  is `Status.NoGUISession`. `curio daemon status` and `curio doctor` say
  the agent is installed with no login session to run in and that curio
  commands start the daemon meanwhile, rather than suggesting an install
  that fails there.
- **`Controller.WithDaemonStopped(ctx, fn)`** holds `daemon.start.lock`
  from before it boots the agent out and stops a daemon running outside
  it until `fn` has returned, and runs `fn` only once nothing holds
  daemon.pid; `curio up --fresh` moves the home in `fn`. `Uninstall` and
  it share the part under the lock.

---

## Keep-awake: caffeinate on AC power while the workers have queued work

**Decision:** with keep-awake on (`curio keep-awake on`), the daemon
holds the Mac out of idle sleep with `caffeinate -i -w <daemon pid>`
while all of these hold: keep-awake is on, the queue isn't paused, the
Mac draws from AC power, and jobs of the pools' kinds (fetch, index,
cluster) are pending or running (`internal/keepawake`).

- **A queue setting, not config.yaml:** `queue_settings.keep_awake`
  (migration 013), `PUT /v1/queue {"keep_awake": …}`. It must be
  opt-in, switchable while an import runs (the wizard offers it once the
  daemon is up, and `off` must take effect at once), and survive
  restarts, launchd's included. A config.yaml key needs a restart and the
  daemon never writes the user's file; queue_settings is persisted,
  changed at runtime, daemon-wide, written by one writer and published to
  waiters already. It never gates a claim.
- **The rule's edges:** a pause releases the hold at once (the user said
  "not now"). A queue closed only by its schedule keeps it: releasing
  would let an idle Mac sleep before an overnight window opens, and the
  "only overnight" import the wizard offers would never start. The cost
  is a Mac awake while it waits for the window. Only the pools' kinds
  count: the store allows import and summarize jobs no worker claims,
  which would hold the Mac awake for ever.
- **Power:** `/usr/bin/pmset -g ps`, bounded to 5s with capped output:
  "Now drawing from 'AC Power'" is AC, 'Battery Power' and 'UPS Power'
  battery, anything else (no pmset, a failure, an answer it can't read)
  unknown, which holds nothing. While there is work it is read once an
  interval (60s), and at once when the settings change, so unplugging
  releases within a minute. A step stamps its reading, and any retry it
  schedules, with the time it began, before it arms its interval timer:
  stamped after the probe returned, a reading would fall due a few
  milliseconds after that timer's wake, and be skipped until the next
  one. While there is work, enqueues don't wake the keeper, so an
  import's hundreds of enqueues a minute cost no pmset runs; while there
  is none, an enqueue holds at once.
- **The hold:** `/usr/bin/caffeinate` with exactly `-i -w <pid>`, started
  in the daemon's process group (launchd's group kill at the end of
  ExitTimeOut reaches it) and reaped by one goroutine. `-w` ends it with
  the daemon however the daemon ends. Release sends SIGTERM, then SIGKILL
  after 2s. One that can't start (no caffeinate) or exits by itself is
  retried at the next interval, never in a loop. The keeper runs with
  the workers and releases its hold as the daemon shuts down.
- **Wakeups:** the keeper takes the gate's change channel and settings
  from one snapshot (`QueueGate.Watch`), so turning keep-awake off or
  pausing releases at once, and waits on that, the interval, the hold's
  process exiting, and `Enqueued` only while it saw no work.
- **Reporting:** info on transitions only ("holding the Mac awake" with
  the caffeinate pid and counts; "released" with the reason). Warnings
  come once per failure streak: pmset failing, the queue count failing,
  caffeinate failing to start (a streak ends when one starts). A
  caffeinate that exits by itself is warned about each time, at most once
  an interval, next to the "holding" line of its restart. `GET
  /v1/queue` adds `keep_awake_active` and, while keep-awake is on,
  `power_source`, both from memory; `curio status`
  prints a keep-awake line saying whether the Mac is held, and why not.
  With keep-awake off, the default, the keeper runs no subprocess.

**What `-i` doesn't do:** it prevents idle sleep only. The display still
sleeps, and closing a laptop's lid still sleeps it; `-s` (system sleep)
applies on AC only anyway, and holding a closed laptop awake is not
curio's call.

**Rejected:** IOKit power assertions through cgo (caffeinate is the same
assertion, with no cgo), and holding on battery (an import would drain
it).

---

## curio up: a plan-first setup wizard

**Decision:** `curio up` sets curio up on a Mac, and is idempotent. It is
five steps in `internal/setup`, run in the design's order by one
function: machine, Ollama, models, home (with config.yaml), daemon. The
import step (group 5) goes after the daemon.

- **Steps check, then apply.** A `setup.Step` has a `Check(ctx)` that
  reads the world, bounded (2s per read, `Timeouts.Probe`) and changing
  nothing, and returns a `Result`: OK, Warn or Fail, a one-line detail, and
  a `Fix` (a summary and the exact argv of every command it runs) or a
  `Hint` (what to do by hand). `Apply(ctx, ui)` makes the fix.
- **The plan comes first.** The runner checks every step; the fixes are
  the plan. An empty plan changes nothing and prints `Nothing to do: curio
  is up.` and a status block. A blocker (a Fail with no fix: a directory
  that isn't a home, a config.yaml that doesn't load, a legacy, newer or
  mismatched home without `--fresh`, no curio-daemon, another home's or a
  legacy daemon on the port, too little disk for the missing models)
  stops the run with the plan before anything is applied: a legacy home
  with Ollama down runs no brew command. Then each step is checked again
  right before it applies, confirmed, applied, and checked after; a step
  that still fails fails the run, and nothing after a failed or declined
  step runs.
- **No wizard state.** A run cut short resumes by checking the world
  again: the steps before it passed, and the plan is what is left.
  `setup.json` in the home holds only the optional installs the user
  declined (group 5 records yt-dlp there), outside the strictly validated
  config.yaml; a corrupt one is a warning and an empty state.
- **`curio doctor` runs the same checks** (`Runner.Checks`: machine,
  home, config, ollama, models, daemon, launchd, embeddings) and adds its
  own (the Jina upstream, the fetcher, the content directory). A test runs
  both over the same fake worlds: up's plan is empty exactly when doctor
  shows no ✗ and no ! that curio up would fix. Doctor no longer sits
  behind `Discover`, so it reports a missing home instead of creating one,
  and an invalid config.yaml instead of dying before its report.
- **The root's hook skips `Discover`** for commands annotated as owning
  their environment (`up`, `doctor`, bare `curio`). `Discover` creates a
  missing home with the default embedding model, which would make
  `--embedding-model` a mismatch with the home up had just asked for, and
  a dry run create a home. An annotation rather than a `PersistentPreRunE`
  on `up`: that would shadow the root's hook, whose flag tells
  `cli.Run` a runtime error from a usage error. `daemonctl.Connect` builds
  the environment of a home that is already open with a config already
  loaded, or the defaults when it doesn't load.

**Consent:**

- A step with a fix asks one question, after showing the summary and
  every command in full, shell-quoted, before any runs. Output streams
  live to stderr; a failed command is named with its exit status.
- Creating a new home, and writing a config.yaml where there is none, are
  announced, not asked: they are curio's own files.
- The models step asks `Use these? [Y/n/choose]` when curio picked the
  models; `choose` lists every tier.
- `--fresh`'s move defaults to No.
- `--yes` answers every question yes, the move included, and takes every
  default. It never implies `--fresh`.
- Without a terminal (stdin and stderr both, checked before any prompt
  library runs) nothing is asked: a non-empty plan is printed and the run
  exits 1, unless `--yes`. `--dry-run` prints the plan and changes nothing,
  in any mode.
- A prompt left without an answer (end of input, ctrl-c, a cancelled
  context) is `setup.ErrAborted`, which `cli.Run` exits 130 for, like an
  interrupt; it is never the default.
- `--no-install` runs no `Installer` command (no brew, no open), leaving
  those to do by hand. Pulling models, the home, config.yaml and the
  launchd agent are curio's own work and still happen.
- Never sudo: `curio up` refuses to run as root.

**The UI:** `charm.land/huh/v2` for the full-screen prompts on a
terminal; a line prompter of curio's own in accessible mode (`TERM=dumb`
or `ACCESSIBLE` set); a flags-only UI without a terminal; setuptest's
scripted UI in tests. huh v2.0.3's accessible mode takes end of input on
a yes-by-default confirm as yes (a ctrl-d would answer yes to `brew
install`), ignores a cancelled context, panics on a select without a
value at end of input, and writes to stdout; the line prompter reads
input on one goroutine so a cancelled context returns at once, treats end
of input as an abort, and writes to stderr. Progress is curio's own
renderer: one line redrawn at most ten times a second on a terminal, a
line per 10% elsewhere, never going backwards, ending at 100%. depguard
confines `charm.land` to internal/setup; curio-daemon and curio-mcp don't
link it.

**The machine** (`setup.Probe`; sysctl, statfs and ioreg through
golang.org/x/sys, no gopsutil): Apple silicon with 16 GB or more is
supported; under 16 GB a warning (the smallest models swap in and out;
import overnight); under Rosetta a warning to install the arm64 build; an
Intel Mac degraded (CPU-only Ollama, no Homebrew build, a first import of
days), asked once whether to go on; not macOS, a warning, with no install
offered. The GPU core count is best effort. **Disk** is judged once the
missing models are known: the models volume must hold their sizes plus 2
GiB, and a home on another volume 2 GiB. That scales with the tier where
a flat 10 GB wouldn't; at 16 GB the two agree.

**Ollama:** the check is whether something answers `GET
/api/version` at `embedding.base_url`, never whether a binary exists.
Nothing answering: the formula's service (`brew services start ollama`)
before the app (`open -a Ollama`), then a Homebrew install on Apple
silicon, else the download page, opened only in a terminal, with up to
10 minutes to install it. Too old for the picked models (gemma4 needs
0.30.5, its QAT tags 0.30.6): `brew upgrade` and `brew services restart`
for the formula, by hand for the app. A base URL off this machine is its
owner's to start. Homebrew bottles Ollama only for the newest macOS and
builds it from source elsewhere, and has no Intel build, which is why the
app is the alternative.

**Models:** `setup.ModelAdvisor` is a static table: `qwen3-embedding:0.6b`
everywhere, and the writing model by unified memory (`qwen3:4b-instruct`
under 16 GB, `gemma4:12b` to 32, `gemma4:26b-a4b-it-qat` to 64,
`gemma4:26b` from 64), each with its size, a reason, its minimum Ollama
and the tier below as the smaller alternative. config.yaml's
`generation.model` is kept, and a `--generation-model` that contradicts
it is refused, naming the file and key; with no config.yaml the flag,
else the pick. No writing model is pulled when interest labels don't use
one. Pulls skip what `Ping` finds (the exact name Ollama runs), go one
progress line each, and a failed one names `ollama pull <model>`.

**Pull progress** comes from `ollama.Client.Pull`'s callback, one line
per model summed over its layers; a stalled or oversized stream fails the
pull (see "Ollama: one client", revised).

**A new home's width is measured,** not assumed or asked: after the pull,
one short `/api/embed` through internal/embedder (`MeasureWidth`, the
indexer's request: `truncate: false`, `keep_alive`, `num_ctx`) gives the
vector length config.yaml and the marker record. `/api/show` reports an
architecture-specific `<arch>.embedding_length`, the model's hidden size,
and a flag can be wrong; the embed gives the width the daemon will
receive. The dry-run plan shows 1024 for the default model and "measured
after the pull" for another. A default model measuring otherwise is
recorded as measured, with a warning. An override's prompt prefixes come
from a table of the models curio knows (qwen3-embedding: the Qwen query
instruction, no document prefix; nomic-embed-text: `search_document: `
and `search_query: `); any other gets none and a warning to set them
before importing. An untagged `--embedding-model` is refused: a home's
vectors are its model's for good, and `:latest` moves.

**config.yaml is written once, when absent, and never edited:** 0600,
with a header, the embedding model, width and prompts, the writing
model, and the addresses that differ from the built-in defaults; linked
into place from a synced temp file, which fails rather than replaces a
file written meanwhile. Machine-editing a YAML file a person wrote loses
its comments and its layout, and a value the user set is theirs. An
existing config.yaml without `generation.model` is left alone; the plan
says what curio would pick and which key to set. The home is created
first (`curiohome.Init` with the measured width): an interrupt between
the two leaves a valid home the next run gives a config.yaml, where the
other order would leave a directory the next run must refuse.

**`--fresh`** renames the directory the home resolves to (a symlinked
home is moved and made again at its target, so the link keeps working)
to `<dir>.bak-YYYYMMDD-HHMMSS`, then `-2`, `-3` when taken, with one
`os.Rename`: never a copy, never a delete, and a failed rename leaves the
home in place and fails the run. What isn't a curio home is never moved.
It runs inside `daemonctl.Controller.WithDaemonStopped`, which holds
`daemon.start.lock` from before it boots the agent out and stops a daemon
running outside it until the rename is done: a daemon started mid-move
would serve the moved directory and hold the port. A daemon from before
the lock protocol is refused, since it can't be verified. The move comes
at the home step, after the pulls, so a failed pull leaves the old home
untouched.

**The daemon step:** OK when this curio's curio-daemon serves the home
with config.yaml's writing model under a loaded agent that runs it.
Otherwise it validates the home again as the daemon will, installs the
agent (which waits until the daemon serves, a migration reported as it
goes), `EnsureVersion`s it, and restarts a daemon whose healthz
`generation_model` differs from config.yaml's; an older daemon that
reports none is never restarted for it. Over ssh (`ErrNoGUISession` from
`Preflight`) or with no service manager, the daemon is started on demand
with a warning, and the check passes with that warning.

**Bare `curio`** prints the help, creating and starting nothing, and adds
a line pointing at `curio up` when the home doesn't exist, or its daemon
already serves an empty library (a second at most).

**Rejected:**

- A wizard state file: it would disagree with the world the moment the
  user fixed something by hand.
- `setup.ServiceManager`: `internal/service.Manager` is the seam (see
  "Daemon lifecycle"), extended with `Preflight` and `NoGUISession`.
- An `--embedding-dim` flag, or `/api/show`: see the measured width.
- huh's accessible mode: the four defects above.

**Not done:** Linux.

**Revised (2026-09-28):**

- **The import step is done**, the sixth step: see "curio up: the import
  step", which also has the yt-dlp and keep-awake offers.
- **A new home's disk:** the home check judges any new home (a missing or
  empty path, or one `--fresh` replaces): a volume the probe measured with
  under 2 GiB free is a blocker, whether or not a model is missing. The
  models' rule is only theirs now, their sizes plus the 2 GiB margin on
  the models volume; on one volume that margin is the home's room, so
  the two never count it twice. Before, only a missing model brought the
  home's volume into question.
- **config.yaml without hard links:** on exFAT, FAT and some SMB mounts
  link(2) fails with ENOTSUP, EOPNOTSUPP or EPERM, and the file is then
  created exclusively (O_EXCL, so a file written meanwhile still wins) and
  written and synced in place. That isn't atomic: a crash can leave a
  partial config.yaml, which the config check then reports as not
  loading. Either way the directory is synced once the file is in it.
- **The line prompter reads only while a prompt waits:** a read starts
  when a prompt needs a line and none is pending. Reading for the UI's
  life took the lines typed for a command curio runs between prompts
  (brew, open) and gave them to the next prompt. A read a cancelled
  prompt leaves pending delivers to the next prompt, the one line that
  can still be taken from a command, and only as the run ends.
- **Blocked homes:** a file, a directory that isn't a curio home, or a
  home that can't be looked at leaves the config and daemon checks "not
  checked", pointing at the home check, with nothing for curio up to do,
  where they used to offer fixes up couldn't make. The daemon check's
  hint for a home the daemon refuses fits the reason: `curio up --fresh`
  for a legacy home, the marker's permissions (or --fresh) for an
  unreadable one, an upgrade for a newer one, config.yaml set back (or
  --fresh) for a mismatch, `curio up --fresh --embedding-model <m>` for a
  flag the home contradicts. A legacy or newer home also leaves the
  models "not checked", instead of offering to pull the model its marker
  records.

---

## curio up: the import step

**Decision:** `curio up`'s sixth step imports the user's bookmarks, and
only when there is something to do: the library has no bookmarks, or
`--import` names a source. A library with bookmarks is up, and no browser
file is even looked for; `curio import` adds more.

**When it has a fix:**

- The library is read from the daemon whose healthz names this home
  (bounded by the probe timeout). With no home yet, or `--fresh` pending,
  it is empty and no daemon is asked: the one answering serves the old
  home, which would count the new home's bookmarks as known. A daemon
  that doesn't serve the home (down, starting, another home's) leaves the
  step "not checked", checked again once it serves: in the same run, the
  daemon step before it starts one.
- Empty library, a terminal, no `--yes`: the fix is "choose which
  bookmarks to import", noted with each source's new count or its
  problem. Without a terminal, or with `--yes`, it is a warning without a
  fix naming the `--import` value of each source found, shell-quoted
  (`--import 'chrome:Profile 1'`), with its count: `--yes` never answers
  a menu. The status block prints a warning's notes under it, so a
  scripted run sees the values.
- `--import chrome|chrome:<profile>|safari|firefox|html:<path>` (split at
  the first ':'; `chrome` is the Default profile, as for `curio import
  chrome`) is parsed before any check; an unknown kind is an error. The
  source it names must be there, readable and hold bookmarks: an unknown
  profile (listing those found), a missing file, a browser that isn't
  installed, an unreadable source, one with no bookmarks, or one macOS
  withholds when nobody can be walked through Full Disk Access (no
  terminal, or `--yes`; see below), is a blocker. Nothing new in it is OK
  ("nothing new in X"), so running the same `--import` again is
  idempotent.
- The menu (the step's `Confirmer`) lists the readable sources with their
  new counts (the one with the most is the default), the ones that need
  permission, "An exported bookmarks file (HTML)…" and "Skip for now". A
  path is typed or dragged into the terminal, which quotes it or escapes
  its spaces, so it is taken as the shell takes one word; one that won't
  do says why and shows the menu again, and a file named again is read
  again. A skip returns yes, not a decline: Apply then does nothing and
  the check after it is a warning, so the run exits 0. With `--import` it
  is one yes-or-no, and a no is a decline like any step's.
- "New" counts bookmarks everywhere (the plan, the menu, the question,
  the hand-off): those the source hasn't saved, which the import creates.
  A bookmark whose page another source brought in is new and fetches
  nothing, so where the pages to fetch are fewer the menu says so ("3 new
  (2 pages to fetch)"); the estimates, the yt-dlp count and the host
  warning are of pages.
- The step keeps what it decided in the run's world (the parse of each
  source, the index rate, the choice, a skip, the report), so the check
  after Apply reads "import started" rather than planning another import.
  Nothing about a pending import is remembered between runs.

**Sources:** `importer.Source` (`Name`, the menu's label; `Label`, the
API source; `Spec`, the `--import` value; `Check`; `Parse`).
`importer.Discover` returns a source per Chrome profile (named from Local
State, the directory added when two names collide), Firefox's install
default and Safari, and never fails: a profile listing that fails is an
Unreadable Chrome source. `Check` tells a missing file (NotInstalled)
from a permission error (NeedsPermission, naming Full Disk Access) from
anything else (Unreadable), at stat and at open: macOS's TCC refuses
Safari's plist at either, and the old stat-only lookup read a refusal as
"not found", so `curio import safari` never reached its Full Disk Access
remedy. `Spec` is the fifth method the design didn't list: the warning
and the resume hint name each source by it, and a test double has to be
nameable too. The Chrome profile matcher (directory, then display name
ignoring case), `Send` (batches of 500, summed, the first failure named
by its range) and `CandidatesOf` moved from the CLI into
`internal/importer`, which may import `internal/client`; `curio import`
and `curio up` share them, and `curio import`'s output didn't change.

**Full Disk Access:** macOS grants it to the terminal app curio runs in,
not to curio, and applies it once that app restarts. The step says so
(naming Terminal or iTerm2 from `$TERM_PROGRAM`), shows `/usr/bin/open
'x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles'`
and runs it through the Installer on a yes (never with `--no-install`,
which says where the pane is), then checks again as often as asked,
saying a still-denied check needs the terminal restarted. A decline goes
back to the menu, or with `--import safari` fails the step with the
remedy. Only someone at the terminal can walk through it: they turn
access on, and macOS applies it only after a restart, so `--yes`, which
answers every "check again" itself, would check again for good. Without
a terminal or with `--yes`, a withheld `--import safari` is a blocker in
the plan with the remedy, and Apply, meeting a source withheld only
since the plan, fails with it rather than walking through. `--yes`
answers no prompt once the run is cancelled, as every UI does.

**Counting what is new: a dry run on the import endpoint.** `POST
/v1/bookmarks/import` takes `dry_run`: the same body, limit and
validation, nothing written, and the ImportResult the import would get
now, plus `new_urls`, the URLs whose documents it would create (so
`jobs_enqueued` counts the fetches it would enqueue). The handler
classifies each URL once, the same way in both modes
(`importer.Classify`: the filter, then `urlutil.Normalize`), asks the
store about the distinct URLs in one statement, and replays `Ingest`'s
rules in request order: a URL seen earlier in the request, or one the
source has a bookmark of, is skipped; any other is created, and fetched
when its document is new. A test holds a dry run equal to the import of
the same body. `BookmarkStore.PreviewIngest` is one `SELECT … FROM
json_each(?)` with correlated EXISTS on the unique constraints Ingest
conflicts on; its plan is pinned (`SCAN j`, then covering searches of
`sqlite_autoindex_documents_2` and `sqlite_autoindex_bookmarks_2`), about
10 ms for 12k URLs against 10k rows. `importer.CountNew` sends the
distinct candidates alone, up to 10,000 a request (about 1 MB), and sums
the answers, exact since no URL is in two requests. `new_urls` is what
the estimates, the yt-dlp count and the host warning are made of. A
daemon that predates the field refuses it as unknown (400); the count is
then unknown, not an error. "Importers: CLI parses, daemon receives
lists" still holds: the daemon counts a list it is sent.

**yt-dlp:** offered only when the new URLs include YouTube videos
(`urlutil.YouTubeVideoID`), with their count. healthz gains
`youtube_fetcher`, the yt-dlp the daemon's LookPath found at startup,
since the route is fixed then: it tells an install that is needed from
one the running daemon predates, and a restart that worked from one on a
PATH launchd doesn't search. The daemon reporting it: said, nothing
asked. Installed (formula or binary): restart the daemon, then warn if it
still doesn't find it (link it into Homebrew's bin, or set
`fetcher.youtube.bin`). Otherwise: a decline in `setup.json` is said and
not asked; `--no-install` and no Homebrew get hints; else `brew install
yt-dlp` is shown and asked, then the restart, then healthz. A no is
saved in `setup.json` (a failed save is a warning). All of it happens
before the queue is set and a bookmark sent, so the new videos are
routed by a daemon that has yt-dlp.

**Estimates** (`setup/estimate.go`, pure but for the measurement):
fetching takes N/45 to N/25 minutes at 16 fetches at once, scaled by
min(W, 16)/16 for a fetch limit W (`daemon.fetch_workers`, or
`store.ThrottleGentle.Limit`, which moved into the store from jobs so
setup needn't link the fetcher stack); indexing takes N × 23 chunks / the
measured rate, × 0.7 to × 1.5; both round up to the minute. The rate is
one warm-up batch and one timed batch of `indexer.EmbedBatchSize` (32)
synthetic chunks of 2,300 to 2,500 characters with the document prefix,
through the home's model, width and `embedding.timeout_seconds`; a
failure makes indexing "unknown", never an error. A host is named when it
holds more than 2/W of the new URLs (12.5% at 16) and at least 100: curio
fetches at most 2 pages at a time from one site, so those take longer;
the check-back time isn't stretched for them, since their per-page time
is unknown. GitHub pages are mentioned only when `config.yaml` sets no
`fetcher.github.token` (the launchd daemon doesn't see a token exported
in the shell): 60 API requests an hour, 2 a repository, and `curio
refetch --all --state=failed` for the ones that fail. The check-back time
walks the pace's schedule window (`DailyWindow.Contains` and
`NextStart`): work only inside it. Estimates show in Apply, before the
pace question, and as fetch ranges in the plan's notes. **The one embed
a check makes:** the plan measures the index rate only for `--dry-run`,
whose plan is all it prints; every other check stays embed-free.

**Pace and keep-awake:** "Now, at full speed", "Now, gently (at most 4
fetches and 1 embedding at a time…)", "Only overnight, 22:00 to 07:00",
each with its check-back time; the default is full speed, or overnight
under 16 GiB. Keep-awake is asked after with `Ask`, not `Confirm`, its
default the queue's current setting (off on a new home): `--yes`'s
Confirm answers yes, which would turn it on. One `PUT /v1/queue` carries
both, `{"paused":false,"throttle":…,"schedule":…,"keep_awake":…}`,
before the import is posted, so the workers claim the import's jobs under
the chosen pace. Neither question is asked when nothing new is fetched.

**The hand-off:** "Import started. Check back after HH:MM." ("tomorrow
HH:MM", or a date), what was imported, then `curio status --follow`
(which follows the queue until it drains), `curio pause | resume`, `curio
throttle gentle`, `curio search "..."` and the MCP line. Other runs that
changed something end with the generic card, which names `curio import`.
A test resolves every command either card names with `root.Find` and
parses its flags. An import cut short (an error, ctrl-c) warns how many
bookmarks were sent and names `curio up --import <spec>`, which finishes
it, skipping those saved, before the error ends the run: `cli.Run`
prints nothing for an interrupt, and the next run would pass, the
library no longer empty.

**Tests never read real bookmarks:** `setup.Deps.Sources` (nil means
`importer.Discover`) is set by every test that builds Deps, and the
TestMain of internal/cli, internal/setup and test/e2e runs under
`setuptest.WithoutBrowsers`, which points CURIO_CHROME_DIR,
CURIO_SAFARI_DIR and CURIO_FIREFOX_DIR at an empty directory; tests in
internal/cli, internal/setup and internal/importer assert Discover finds
nothing available there. setuptest's
scriptable `Source` stands in for a browser, and its fake daemon answers
imports (every valid URL new), keeps queue settings, and reports
`youtube_fetcher` from a marker file a fake install creates, never from
the host's PATH.

**Rejected:**

- A separate preview endpoint: it would duplicate the import's
  validation and limits, and drift from the rules it previews; a flag
  runs the same classification.
- Counting locally: the CLI can't know which URLs the library has, and
  dedup is the daemon's (documents per tenant and URL, bookmarks per
  source).
- Remembering a pending import in setup.json: it would disagree with the
  library the moment an import finished elsewhere; an interrupted import
  is finished by running it again, which skips what was saved.
- Stretching the check-back time for dominant hosts: their per-page time
  is unknown; they are named instead.
- Parsing in the daemon: see "Importers: CLI parses, daemon receives
  lists".

---

## Dashboard: server-rendered pages in the daemon (phase 1)

**Decision:** curio-daemon serves a read-only dashboard under `/ui/` on
its own port, and `/` redirects there: an Overview (counts, the queue and
its progress, health, the newest bookmarks), Search, a Library of
documents with filters and paging, a page per document (metadata, its
rendered text, related documents, its bookmarks, its last error) and the
Interests. config.yaml's `daemon.ui` (default true) turns the pages off;
`curio ui` opens them. Phase 1 changes nothing: refetch, reindex, rebuild
and the queue controls are phase 2 (docs/ui.md).

**Where it is served, and no token.** From the daemon's own origin, so
the pages are same-origin with `/v1`: the Origin rule ("Local API")
already admits their requests and refuses every other site's, and no
CORS is ever needed. A second port or binary would be another origin,
needing either CORS or a token the pages would have to be handed; a
desktop wrapper is a product of its own. The API trusts local processes
already, and the pages add no one it didn't trust.

**Stack.** Go's `html/template`, not templ: no generator in the build, and
the standard library's contextual escaping. Every page is a full
server-rendered page. htmx does two things, search as you type and "load
more" in the Library, and every htmx request gets the same full page a
plain GET gets and selects its region (`hx-select`): one rendering path,
no partial endpoints, no `Vary: HX-Request`, and every page works with
JavaScript off. No Node toolchain, no SPA, no chart library.
`TestEveryTemplateRenders` executes every template of every page's set
(pages, blocks and partials) with a typed sample whose strings are
hostile, and a template without a sample fails the test, so a broken
template fails `make test` rather than a page.

**One code path per resource.** The page handlers (`internal/api/ui*.go`)
get their data from the Deps functions the JSON handlers call (`health`,
`stats`, `queueState`, `metrics`, `listDocuments`, `document`, `search`,
`related`, `interests`, `interest`, `listBookmarks`, and `openMarkdown`,
which `openContent` opens a document's markdown with), plus two store
reads, and map it into `internal/ui`'s
typed view models. They never build SQL: depguard denies `database/sql`
and the SQLite store to `internal/ui` and `internal/api/ui*.go`. A view
can then be rebuilt client-side from `/v1` without its answers
disagreeing with the page's.

**Security model.** Everything a page shows came from a web page or from
Jina and is hostile.

- Every response under `/` and `/ui` carries exactly this
  Content-Security-Policy,

  ```
  default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:;
  connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'
  ```

  and `X-Content-Type-Options: nosniff` and `Referrer-Policy:
  no-referrer`, set by middleware before
  the access checks, so their 403s, the 404 page, the 405s, the redirect,
  the starting page and the assets carry them too.
  `TestDashboard_SecurityHeaders` takes the routes from the router and
  needs a sample request for each.
- No template holds inline script, inline style, an event handler,
  `hx-on`, `hx-vars` or a `javascript:` URL (`TestTemplatesHaveNoInlineCode`),
  and every page test checks the HTML inert with `internal/ui/uitest`.
- Stored markdown goes through goldmark with three of GFM's extensions,
  tables, strikethrough and task lists, and without `html.WithUnsafe`,
  so raw HTML is left out. The fourth, Linkify, is left out: a web or
  email address written out without link markup stays text, since its
  regexps scan from every space at a cost the formatting budgets can't
  charge (see "Dashboard: formatting budgets for stored markdown"). An
  AST transformer then resolves every link and image against the
  document's URL (`url_canonical`, else `url`), after resolving
  character references as the renderer will (`&#106;avascript:` is
  `javascript:`): a relative link would otherwise resolve against the
  daemon. Links, images and autolinks keep only http and https URLs with
  a host, and mailto ones; any other becomes its text. What an image
  holds, a link or another image, is written only as its text. An http
  URL without a host (`[x](http:/ui/search)`, `<http:/v1/stats>`) is a
  relative one to a browser on an http page: it would open the daemon's
  own `/ui/search`. bluemonday's UGC allow-list then sanitizes the HTML,
  allowing those schemes, requiring a host for http and https again, and
  no relative URL, and giving links `rel="nofollow noreferrer noopener"`
  and `target="_blank"`. Its output is the only conversion to one of
  html/template's trusted types (`template.HTML`, `JS`, `URL` and the
  rest) outside tests: `TestTrustedHTMLOnlyFromTheSanitizer` parses every
  non-test Go file in the repository to keep it that way. Search snippets
  are split at their `<em>` markers into plain-text segments, so a chunk's
  markup is text.
- Links the templates make are built in Go (`url.Values`,
  `url.PathEscape`): a query string pieced together in a template is
  escaped a second time, and html/template leaves a path segment
  unescaped. Outbound links carry `rel="noopener noreferrer"`.
- htmx 2.0.11 is vendored: `dist/htmx.min.js` of the npm package
  htmx.org@2.0.11 (0BSD), 52,182 bytes, SHA-256
  `d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717`,
  identical from jsDelivr, pinned by `TestHTMXIsPinned`. Its config (a
  meta tag) sets `allowEval: false` (which also rules out `hx-trigger`
  filters, so none are used), `allowScriptTags: false`,
  `includeIndicatorStyles: false` (its injected `<style>` would break the
  CSP; `app.css` has the indicator), `historyCacheSize: 0` (no page
  snapshots in localStorage), `selfRequestsOnly: true`, and swaps 4xx and
  5xx answers too, so a failed search replaces the results rather than
  leaving stale ones.
- Every page route is a GET (`TestDashboard_GETOnly`): a page must never
  change anything, since another site can send a browser to one. The
  assets' route is a `Get`, not a `Mount`: chi reports a mount under every
  method.
- A change must come from the daemon's own pages when a browser says
  where it came from (`Sec-Fetch-Site: same-origin`; see "Local API").
- A page request another site's page makes as a subresource is refused:
  `Sec-Fetch-Site` cross-site or same-site gets 403 unless the request is
  a top-level navigation (`Sec-Fetch-Mode: navigate`, `Sec-Fetch-Dest:
  document`), which following a link to the dashboard is. `GET
  /ui/search?q=` runs the search engine and has Ollama embed the query,
  and `<img src="http://127.0.0.1:8765/ui/search?q=x">` on any page passed
  the Host check (the daemon's own Host) and the Origin rule (a no-cors
  GET carries no Origin). The answer is opaque and nothing is written,
  but a hostile page left open could keep Ollama busy, starving the index
  jobs that share it, and add a line per request to `daemon.log`, which is
  never rotated. `/v1/search` is a POST, so the Origin and JSON rules
  covered it already; this rule adds to them for the pages.

**Remote images: off by default.** Loading an image tells its host that
the page was read, and when. Each image is its alt text, linking to it;
an image that isn't http(s), or already sits inside a link, is its text.
A document page whose text has remote images offers a "Load images" link
to itself with `?images=1`, which stores nothing, and
`ui.load_remote_images: true` shows them on every document page. Only
https images are shown, lazily, and only those answers' CSP adds `https:`
to `img-src`; http and `data:` images stay links or text.

**At most 1 MiB of markdown is rendered,** cut after the last whole line
(`ui.MaxRenderedMarkdown`), or before the character the limit falls in
when the first line is longer. A cut page says so and names the markdown
file; `GET /v1/documents/{id}/content` has the whole text. The cap alone
doesn't bound the cost: goldmark does far more than linear work on some
shapes a stored page can hold. A text over the formatting budgets, or
whose render passes two seconds or a gigabyte allocated, is shown as
stored instead (see "Dashboard: formatting budgets for stored
markdown").

**Pages degrade by panel.** The Overview's panels (counts, queue,
progress, health, newest bookmarks) and the Document page's text, related
documents and bookmarks each read on their own. A read that fails shows
its message and request ID in its panel, logged once as `writeError` logs
a request's (`reportError`), and the page still answers 200. A failed
search is the Search page's answer, with the status `reportError` gives
it: a 499 when the client has gone. The JSON API
keeps "hydration errors fail the request": a client wants a whole answer
or an error it can act on, a person reading a page wants what could be
read. A page renders into a buffer first: a template that fails is a 500
error page, never a truncated 200.

**Progress estimate.** The Overview estimates when the queued fetch and
index jobs will be done at the pace of the last 10 minutes
(`ui.ProgressWindow`): rate = done and failed jobs of those kinds in the
window / 10 minutes, ETA = (pending + running) / rate. It shows an ETA
only with work queued, the queue open and a rate above 0; with nothing
finished in the window it says so, and a closed queue says why instead.
It is labeled an estimate at the window's pace: the index job each fetch
will enqueue isn't counted until it is.

**While starting,** a page GET answers 503 with a page that reloads
itself every 2 seconds (a meta refresh, not script) and shows the phase
and the migrations applied; the assets are served, `/` redirects, and
changes and `/v1` get the starting problem. `curio ui` opens the
dashboard as soon as the daemon answers (`EnsureStarted`, as `curio-mcp`
starts), rather than waiting out a migration with nothing to show.

**New API and store reads.** `GET /v1/documents` gained `content_type`,
`host` and `folder` filters, for the Library (see "Folder and host
filters" and "Indexes follow the queries"). The Document page reads its
bookmarks (`BookmarkStore.ListByDocument`, newest saved first: ordered
by `saved_at`, since `created_at` would walk every bookmark's index
entry) and its last job error (`DocumentStore.GetWithLastError`, built
from `ListWithLastError`'s SQL) straight from the store, shown only while
the document is failed or dead: `/v1` doesn't expose either yet. Rebuilding the page client-side would add
`last_error` to `GET /v1/documents/{id}` and implement the decided `GET
/v1/documents/{id}/references` (`{bookmarks: [...]}`).

**Phase 2's rule for changes.** Actions go through `/v1` as JSON sent with
fetch or XHR (htmx's json-enc, or a small module under `/ui/static/`),
never as a plain HTML form post: with `Referrer-Policy: no-referrer`, a
browser sends `Origin: null` on a POST that isn't in CORS mode (the Fetch
standard's Origin rules), which the Origin rule refuses, and the
`Sec-Fetch-Site: same-origin` rule applies to them. The document page
leaves an empty template region, `document-actions`, where its buttons
go.

**Dependencies.** github.com/yuin/goldmark v1.8.6 (MIT);
github.com/microcosm-cc/bluemonday v1.0.27 (BSD-3-Clause), with
github.com/aymerick/douceur v0.2.0 (MIT) and github.com/gorilla/css
v1.0.1 (BSD-3-Clause); htmx 2.0.11 (0BSD), vendored. golang.org/x/net
stays at v0.58.0: bluemonday's floor, v0.26.0, carries advisories fixed
in v0.56.0. govulncheck finds nothing in the new modules.

**Revised (2026-09-29):** search is the home, at `/ui/`, and the Overview
is Status, at `/ui/status`, without the newest bookmarks; `/ui/search`
answers 302 to `/ui/` with its query. The page handlers no longer read
`listBookmarks` and read `failures` for Status. The request
`isolateDashboard` guards is `GET /ui/?q=`, and it refuses another site's
subresource request for `/ui/search` before the redirect. See "Dashboard:
search is home, the Overview becomes Status".

**Revised (2026-09-29):** the dashboard changes things now: a document's
refetch and reindex, the interests' rebuild and the queue's controls,
sent to `/v1` as JSON by the pages' own script, and its live parts refresh
by themselves. "Phase 2's rule for changes" above stands, but not its
choice of json-enc, and the empty `document-actions` region holds the
document's buttons. The pages are still GET-only. See "Dashboard: actions
through /v1, sent by a first-party module".

**Revised (2026-09-29):** phase 2 is complete. Its last piece is the
Library's Failures tab, `/ui/failures`: the failed and dead documents
grouped by cause, each group refetched through `POST
/v1/documents/refetch-all`. See "Dashboard: the Failures tab".

---

## Dashboard: formatting budgets for stored markdown

**Decision:** Before goldmark sees a document's markdown, one linear pass
over its lines (`ui.checkShape`, `internal/ui/budget.go`) charges it for
what makes goldmark's work or memory grow faster than the text. The link
transformer charges every link and image before it resolves it, and the
HTML goldmark writes is capped. Whatever the text, every render is also
held to two seconds and a gigabyte allocated (`maxFormatTime`,
`maxFormatAlloc`), checked from goldmark's extension points as it runs.
A text over any budget or limit is shown as it is stored, escaped in a
`<pre>`, under a banner naming it; everything else is formatted as
before. GFM's Linkify extension is left out, so an address written out
without link markup stays text.

**Why.** goldmark v1.8.6 has corners where its work grows with the square
of the input, or multiplies it, and a stored page's markdown is whatever
a web page, a GitHub README (stored verbatim), a PDF or Jina made it.
goldmark has no way to cancel a render: unless something inside it stops
it, a render runs to the end after its client and the 2-minute write
timeout are gone, on a core of its own, and every reload starts another.
Measured through `ui.Renderer.RenderMarkdown` on the development Mac:

- Container markers stacked on one line are quadratic in the markers: a
  1 MiB line of `>` took 4 min 30 s, allocated 607 MB and wrote 28 MB of
  HTML. At 64 KiB, `>\t` took 2.1 s, `- ` ending in `x` 1.6 s, `>`
  1.1 s, `+ ` and `> - ` 0.55 s, `1. ` 0.37 s.
- The block parse visits the open blocks on every line, in order, up to
  the first the line doesn't continue, and keeps a record of each visit
  until the top-level block ends. A blank line continues every list and
  list item, so it visits all of them, and lists stay open over any
  number of blank lines. An item nested 32 deep, then 1 MiB of blank
  lines, allocated 9.2 GB through the document page and took the daemon
  to 4.8 GB; one `- a` over the same blank lines, 258 MB. Nesting also
  builds up line by line through indentation: a 16 KiB ramp of lines
  indented with tabs, 32 markers each, reached about 1,400 levels, and
  32,000 blank lines after it (48 KB in all) allocated 11.5 GB. Each
  visit also reads the line's indentation again, and copies the rest of
  the line when a tab is split between two blocks: that ramp filling
  1 MiB took 26 s, cmark's deeply nested lists indented with tabs 4.8 s.
- Inline markup is quadratic in its paragraph. In one 256 KiB paragraph,
  `[a](` repeated took 21.7 s, `[a](b` and `![a](` 18 s, `[a]` lines
  0.8 s, `[a]: b` lines 0.6 s, and `[a](< ` 0.8 s at 128 KiB. The same
  256 KiB as paragraphs of 1,024 `[a](` took 0.37 s: the cost is per
  paragraph. An unclosed opener scans to the end of its line or
  paragraph, and each `]` walks its paragraph's lines. `*a`, `_a`, `~a`
  and `a*` took 4.3 s each, `**a` 2.9 s and `` `a `` 2.2 s, but that was
  Linkify's (below); without it they take 24 to 54 ms.
- Emphasis runs that pair with nothing can stay to be compared again:
  goldmark has no cut-off for openers that can never match (cmark's
  `openers_bottom`), so after `a**b`, every `c* ` (a closer the rule of
  three keeps from pairing with the `**`) is compared with every one
  before it. One 80 KB paragraph of them took 2.4 s.
- Two shapes multiply what they are given. goldmark pads every table row
  to its header's width: a 1,024-column header over 128K one-character
  lines (256 KiB) made 1.3 GB of HTML in 69 s. Every link to a reference
  definition repeats its URL: one 16 KiB definition used by 32K links
  (147 KiB) made 540 MB in 7 s.

The earlier assumption, that a 2 MiB table (half a second) was the worst
case the 1 MiB cap had to cover, was wrong.

Within those budgets, four more shapes cost seconds or gigabytes, and
they are why every render has limits too:

- Every link and image is resolved: its character references (two more
  copies of its destination), then `url.Parse` and `ResolveReference`.
  Only kept links counted against the link budget, and every link to a
  reference definition shares its destination, so a definition to a
  16 KiB `ftp:` URL used by 1 MiB of `[x]` links took 11.7 s and
  allocated 11.2 GiB, formatted; to a `javascript:` URL, 3.3 s and
  3.9 GiB; as `![x]` images, 9.9 s and 9.4 GiB; a 128 KiB destination,
  1 min 26 s and 73 GiB.
- Linkify, which GFM includes, runs regexps (20 to 35 ns a byte) from
  every space, line start, `*`, `_`, `~` and `(`, a cost the inline
  budget, calibrated on byte loops at about 1 ns a byte, doesn't see.
  One 1 MiB line of `x`, 680 `~~~a`, an `@`, a domain of 62-letter labels
  and `a_` took 24 s: a run of three `~` isn't strikethrough, so its last
  `~` reaches Linkify, which scans the address and the whole domain and
  then drops it for the `_` after it, and the next `~` scans it again.
  256 units of `(http://a.b/` and 4,000 `)` took 2.4 s. The email shape
  at 256 KiB still took 22.4 s: the budget is a product, so a shorter
  text admits more scans.
- The link budget counts URLs as stored, and goldmark escapes them as it
  writes: `&` is `&amp;`, `"` and `<` are `%22` and `%3C`. A 19 KB page,
  a definition to `https://a.example/?` and 16 KiB of `&` used by 500
  links, is within every budget, and wrote 41 MB of HTML, allocating
  676 MiB in 0.37 s; sanitizing, the page template and the response then
  cost what that HTML does.
- The transformer labelled every image, those inside other images too,
  each over its whole subtree: `![` n times, `a`, then
  `](https://i.example/x.png)` n times took 29 ms and 160 MiB at
  n = 4,000 (112 KB).

**The budgets.**

- `maxLineNesting`: 32 blockquote and list markers on one line. A
  thematic break (`- - -`, `* * *`) isn't markers: goldmark tries it
  before a list at every level.
- `maxContainers`: 131,072 such markers in the text, each a container
  with its own tags.
- `maxBlockVisits`: 2^21 (2,097,152), the sum over the text's lines of
  the open blocks goldmark can visit at each.
- `maxVisitBytes`: 2^28, the sum over the text's lines of those visits
  times the line's length.
- `maxInlineWork`: 2^31, the sum over the text's paragraphs of their
  markup characters times their length in bytes. Markup is every
  character goldmark's inline parsers start at, `!`, `[`, `]`, `` ` ``,
  `*`, `_`, `~` and `<`, and `(`, where an inline link's destination
  starts, which the `]` before it scans to the end of the line.
  `TestMarkup_CoversInlineTriggers` checks the list against goldmark's
  default inline parsers and the extensions'.
- `maxEmphasisWork`: 2^27, the sum over the text's paragraphs of the
  square of their runs of `*`, `_` and `~`.
- `maxTableCells`: 262,144 cells. Every line after a line shaped like a
  delimiter row is charged the widest such row's cells, until the
  paragraph ends, and each such row its header's.
- `maxLinkBytes`: 8 MiB of destinations and titles, as stored, over
  every link and image in the text, kept, unwrapped, labelled or turned
  into a link, each counted once and before it is resolved: the
  transformer stops at the first past it, before resolving it and before
  goldmark writes a thing. Autolinks aren't counted: each covers its own
  span of the text.
- `maxHTMLBytes`: 8 MiB of HTML from goldmark, 8 times
  `MaxRenderedMarkdown`. The writer goldmark renders into stops the
  render at the write that would pass it.

Each of `checkShape`'s charges is an overestimate, never an
underestimate. The pass's paragraphs are runs of lines between blank
lines (spaces, tabs and line ends only, as goldmark counts them), split
only before a line that starts a bullet item with content or an ATX
heading at most three spaces in after any blockquote markers: either
interrupts a paragraph inside any container, so every paragraph, heading
and table cell goldmark parses inline lies within one of them.
Everything else a paragraph may or may not end at (a fence, an HTML
block, a table row) is left inside it. Markers are counted on every
line, continued containers too, a run of `*`, `_` or `~` counts whatever
it turns out to be, and a table is assumed wherever a line could be a
delimiter row.

The block charges follow from how goldmark continues a block. A line
continues a blockquote only with a `>` of its own, a list only with a
marker of its own or as much indentation as its item's content (at
least two columns), and the item with that same indentation; once what
is left of a line is blank, every list and list item beneath continues.
So the pass keeps a bound on the blocks open after each line and charges
each line: a blank line, or one of only spaces, tabs and markers, can
visit every open block; any other line at most one block per marker and
per column of indentation (a tab counts four), and one more. Each marker
opens at most two blocks, a list and its item, and a nonblank line one
more, the paragraph or code it holds. The blocks a line doesn't continue
stay open only when it continues a paragraph lazily, which a line after
a blank one can't. The rule was checked against a copy of goldmark
instrumented to count its visits and the bytes they read or copy, over
9 million generated documents (indentation of spaces and tabs, every
marker, lazy and blank lines, fences, thematic breaks, ramps hundreds of
levels deep) and over 400 million fuzzed ones: the visits charged were
never fewer than goldmark's, and the bytes it read or copied were at
most three times the bytes charged.

**The limits.** The budgets stay the decision: fast, and the same answer
on every machine. But each covers shapes someone found, and the four
above got past them, so every render is also held to `maxFormatTime`,
2 s, and `maxFormatAlloc`, 1 GiB allocated since it began. goldmark
calls curio's code often enough to be stopped from there:

- at every inline trigger: `stopParser`, an inline parser whose
  triggers are all the others' (`` ![]`<*_~ ``), runs first at each and
  parses nothing. Its priority is -1: goldmark tries inline parsers in
  ascending priority, and the task list's checkbox parser is at 0.
  `TestMarkup_CoversInlineTriggers` keeps its triggers equal to theirs;
- at every line the block parse reads, once for each open block it
  visits there: the `text.Reader` handed to `Parse` checks in
  `PeekLine`;
- at every link, autolink and image in the link transformer;
- at every write of HTML (goldmark buffers through bufio, so every
  4 KiB), and once more after the render, before sanitizing.

A check is an atomic load of a flag a `time.AfterFunc` sets; every 256th
also reads the process's heap allocations from runtime/metrics
(`/gc/heap/allocs:bytes`, 0.2 µs a read, about a hundred checks' worth).
A check that fails panics with its `*budgetError`, `errFormatTime` or
`errFormatAlloc`, and the HTML writer with `errHTMLBytes`; `format`
recovers that and only that, and the page shows the text as stored with
the reason. Any other panic goes on. Unwinding is safe because goldmark
v1.8.6 keeps nothing between renders that a render changes: it has no
recover, no `sync.Pool` and no mutex, its `sync.Once` initializations
finish before any hook runs, and every render gets a fresh
`parser.Context`. `TestMarkdown_FormatsAfterAStop` pins that across
upgrades: after a render stopped by the time limit, and one stopped by
the HTML cap, the same Renderer formats a 1 MiB article byte for byte as
a fresh one does. It is the pattern `http.ErrAbortHandler` uses to stop
a handler.

A render stops at the first check past a limit: it takes at most the
limit plus the costliest step between two checks, and allocates at most
the limit plus what one step allocates. The steps no check interrupts
are each bounded by a budget:

- emphasis matching of one paragraph (goldmark's `ProcessDelimiters`),
  by `maxEmphasisWork`: `a**b` over lines of `c*` just under it takes
  0.46 s, and ran 0.42 s past a 10 ms limit;
- the table extension's escaped-pipe pass, by `maxInlineWork`: up to
  0.1 s past the limit in the tables tried here, 0.16 to 0.27 s in
  review;
- table padding, by `maxTableCells`;
- sanitizing at most `maxHTMLBytes`: 24 to 200 ms, and 40 to 285 MiB
  allocated, for 8 MiB of links, list items, images or text.

The allocation limit overshoots by what is allocated between two reads,
and by one step's allocation: about 0.2 GiB where goldmark doubles a
slice. The count is the whole process's, so other requests can only stop
a render sooner, never let it run on, and renders running at once share
the limit: four renders of 'text under deep lists' (about 320 MiB each
alone) at once were all stopped, while 16 of the 1 MiB article at once
all formatted. Whether a text is formatted therefore depends on the
machine's load, and on what else renders at the same time, only for
texts near a limit: ones that take over 2 s, twice the costliest text
the budgets accept (about 1 s) and over 15 times the slowest ordinary
1 MiB document (a loose list, 120 ms), or that allocate hundreds of MiB. Tests of the
budgets relax the time limit (`newBudgetRenderer`): under the race
detector, the costliest texts within the budgets take longer than 2 s.

Each limit needs the other. With the budgets bypassed, the time limit
alone stops a 1 MiB line of `>`, 1 MiB of cmark's deeply nested lists
indented with tabs, the tab ramp filling 1 MiB and a 256 KiB paragraph
of `[a](` at 2,000 to 2,013 ms, but the item nested 32 deep over blank
lines, and the ramp over blank lines, allocate 5.7 GiB and reach a
2.1 GiB heap within one second. The allocation limit stops both at
1.2 GiB allocated after 190 to 230 ms, with at most 454 MiB of heap at
once. An allocation limit alone lets 64 KiB of `>` on one line run
1.1 s on 38 MiB. In review, without the fixes for the four shapes
above, the limits stopped each of them within 5 to 20 ms of a 200 ms
limit.

**Numbers.** Just under the budgets, the slowest shape is still `[a](`:
0.95 s for one 52 KiB paragraph, and 1.02 s for 1 MiB in paragraphs of
2.7 KiB; `[a](b` takes 0.95 s. Next are lists nested with tabs, cut just
under `maxVisitBytes` (213 KiB), 0.47 s, and `a**b` over lines of `c*`
just under `maxEmphasisWork` (34 KiB), 0.46 s; then text under 128
levels of tab-indented lists (1 MiB), 0.27 s. Without Linkify the other
inline shapes take milliseconds: `` `a `` 8 ms and `*a` 4 ms, each as
one paragraph just under the budget. An item 32 deep over blank lines
takes 43 ms. The most memory is that text under deep lists, 322 MiB
allocated and 119 MiB of heap at once, then the deep item over blank
lines, 257 and 142 MiB: about what goldmark takes for 1 MiB of
one-character lines, which no budget stops (263 and 139 MiB).

A text over the link budget costs goldmark's parse of its links and
nothing more: 1 MiB of `[x]` links to a 16 KiB definition takes 120 to
170 ms and 200 to 250 MiB, whether the URL is https, ftp or javascript,
16 KiB or 128, links or images: the transformer stops within the first
thousand links, and the rest is goldmark parsing some 200,000 of them.
Links a page keeps reach the HTML cap before the link budget: 510 links
to a 16 KiB URL write 8.4 MB, and 1 MiB of `[x]` links to a short one
8.6 MB (0.24 s, 318 MiB). The link budget bounds resolving, the HTML cap
writing. The page of escaped `&` stops at the cap in 30 ms and 82 MiB.
The largest HTML of the ordinary documents below is 2.5 MiB: the 1 MiB
article writes 1.9 MB, the awesome list 2.0 MB and the loose nested
lists 2.6 MB. A text over a shape budget costs what copying and escaping
it does.

`TestMarkdown_OverBudget` renders every shape above, at 1 MiB where it
has one, and checks it takes that path, fast and within 16 MiB of
allocation (512 MiB for the link budget, whose text costs what the parse
does). `TestMarkdown_CostsCheckShapeDoesntSee` holds the four shapes
above to their outcome and to a time and an allocation bound per render.
`TestMarkdown_JustUnderBudget` cuts the block and emphasis shapes to the
most of them within the budgets and holds their render to 10 s and
1 GiB. `TestMarkdown_TimeLimit` stops a block-heavy and an inline-heavy
text at a 10 ms limit within 250 ms, and `TestMarkdown_AllocLimit` stops
text under deep lists at a 32 MiB limit within 64 MiB, race detector
included.

The checks cost 3 to 4% on the ordinary documents below (the 1 MiB
article 50.0 → 52.1 ms, the awesome list 60.5 → 62.5 ms, nested lists
80.8 → 83.8 ms, loose nested lists 78.9 → 82.1 ms) and 6% on text under
deep lists (257 → 273 ms), whose block parse checks at every one of its
two million visits. Leaving Linkify out pays for them on pages of links:
against the build before both, the article went from 53.7 to 52.1 ms and
the awesome list from 65.5 to 62.5 ms, while nested lists cost 1% more
and text under deep lists 5%.

On a real library of 4,498 stored documents, rendering all of them takes
2.1 s, and the slowest, 583 KiB, 48 ms. The largest block charges are
165,244 visits and 6.7 × 10^6 visit bytes, 12 and 40 times under their
budgets; the largest emphasis work within the inline budget is 3.6 ×
10^6, 37 times under. One document is over a budget: a Wikipedia list
flattened by Jina into one 325 KiB paragraph of links and citations
(6.1 × 10^9 of inline work). The next are a Wikipedia article with
11 KiB paragraphs (1.2 × 10^9) and a PDF extraction without paragraph
breaks (6.4 × 10^8). What the budgets will catch in practice is that
shape, and PDF text of several hundred KiB with no blank line, which is
plain text anyway, and tables of a few thousand rows of links: a table
is charged as one paragraph. `TestMarkdown_WithinBudget` formats a 1 MiB
article, a 1 MiB awesome-list README, a 100 KiB flattened wiki table,
lists nested ten deep, lists nested five deep with a blank line after
every item, a 1 MiB loose list, a 1,000-row table and a thematic break
of 64 spaced dashes, which counted as markers before and was the other
real document shown unformatted.

**Considered.**

- A render deadline outside the render, a goroutine with a response
  deadline or a semaphore: it bounds latency or concurrency, not the
  render, which keeps burning its core. The limits stop the render
  itself, from inside goldmark.
- Only the limits, without budgets: the outcome would depend on the
  machine and its load for every costly text, not only for those over
  2 s, and the budgets turn most costly texts away in a linear pass.
- Only a size-limited writer as the outer bound: the unkept links and
  Linkify's scans wrote 1.9 MB and 1 MB of HTML.
- A work counter in the transformer and the renderer only: Linkify's
  cost was inside goldmark's inline parse.
- A lower input cap: the Linkify email shape still took 22.4 s at
  256 KiB.
- Hooking emphasis matching: it means replacing goldmark's emphasis and
  strikethrough parsers, for a step `maxEmphasisWork` bounds at about
  half a second.
- Keeping Linkify with bounded regexps (`WithLinkifyURLRegexp`,
  `WithLinkifyWWWRegexp`, `WithLinkifyEmailRegexp`): each trigger would
  still scan at regexp speed, and Linkify triggers at every space, so it
  would need a charge of its own and regexps to maintain. Dropping it
  removes the one inline parser the markup budget misprices. Stored
  pages rarely need it: readability and Jina write `[text](url)`. GitHub
  READMEs, stored verbatim, lose click-through on bare URLs; docs/ui.md
  says so.
- A cap on a destination's length: with every link charged before it is
  resolved, a 128 KiB destination costs 0.15 s and 203 MiB, all of it
  goldmark's parse. A cap would bound nothing the charge doesn't, and
  change how legitimate long URLs render.
- A second charge after resolving: a resolved URL is at most about three
  times the stored one plus the base URL, and what the renderer writes
  for kept links is the HTML cap's.
- Failing the writes past the HTML cap instead of unwinding: goldmark's
  node renderers ignore write errors, and bufio then swallows every
  later write, so goldmark keeps escaping every URL left. On the page of
  escaped `&` that took 79 ms and 211 MiB in review, against 30 ms and
  82 MiB.
- Relying on the HTML cap for tables: table padding happens as goldmark
  parses, before it writes anything, so `maxTableCells` bounds it.
- Formatting all but the costly paragraphs: which lines make a paragraph
  is known only once goldmark has parsed the blocks, and the block parse
  is where the nesting and the table padding cost.
- Charging each scan goldmark makes (how far each unclosed opener looks):
  tighter, so fewer documents would be shown plain, but it would mirror
  goldmark's internals scan by scan and miss the next corner. The budgets
  charge what any inline parser could do.
- A cap on nesting depth alone: the block parse's cost is its depth
  times its lines, blank ones included, so only a sum over the lines
  bounds it.
- Charging a blank line once, since goldmark reads it in full only at
  its outermost list: closer to goldmark's count, but it leans on that
  detail; charging every visit the whole line costs the real library
  nothing.
- Rows of a detected table as paragraphs of their own: a table goldmark
  doesn't make (a header in a fenced block, or the heading before a
  delimiter row) would leave its lines one paragraph charged as many.

---

## Commands take a document's URL as well as its ID

**Decision:** `curio refetch`, `reindex`, `docs show` and `related` accept
an `http(s)` URL where they take a document ID. The CLI resolves it
through `GET /v1/documents/lookup?url=`, which normalizes the URL with
`urlutil.Normalize`, the function ingest stores URLs with, and looks it up
on the `UNIQUE (tenant_id, url)` index (`DocumentStore.GetByURL`, its plan
pinned in `plans_test.go`). Anything that isn't an absolute `http` or
`https` URL is taken as an ID as it is.

**Why:** a user debugging one page knows its URL, not its ID, and `curio
add` of a URL already bookmarked is refused, so there was no short path
from a URL to a refetch. Normalizing on the daemon keeps one definition of
"the same URL". A URL with no document says so and suggests `curio add`.
A scheme-less argument (`github.com/…`) stays an ID: guessing would make
an ID that happens to contain a dot ambiguous.

---

## Doctor warns when GitHub requests carry no token

**Decision:** `/v1/healthz` reports `github_token`: whether the daemon's
GitHub fetcher sends a token, from `fetcher.github.token` or
`CURIO_GITHUB_TOKEN` in the daemon's own environment. `curio doctor`
adds a `github` check: a warning when there is none, with the hint that
any token works, even one with no scopes or permissions, and where to put
it. When the daemon doesn't say (it is starting, stopped, or predates the
field), config.yaml decides.

**Why:** without a token GitHub allows 60 API requests an hour, and an
import with a hundred-odd github.com bookmarks fails many of them
rate-limited; people don't expect that a token with no access at all is
enough to lift it to 5,000. The daemon, not the CLI, knows what it sends:
under launchd it doesn't see the shell's environment, so a token exported
in a terminal isn't one the daemon has.

---

## Failure causes: recorded when a document fails

**Decision:** every failed or dead document records why it failed, in
`documents.failure_cause`, one of `store.FailureCauses`. The
permanent-failure hook (`markDocFailed`) writes it through
`DocumentStore.MarkFailed`: `fetcher.FailureCause` of the error a fetch
job gave up with, or `index` for an index job, whatever its error. The
cause is what the dashboard's Failures view groups by, what
`GET /v1/failures` counts, and what `GET /v1/documents?cause=`,
`POST /v1/documents/refetch-all?cause=` and `curio refetch --all --cause`
filter on.

**Why stored, not parsed:** until now the only record of a failure was
`jobs.last_error`, text, and reading causes back from it fails three ways:

- The text has lost the typed chain, and after a Jina fallback it carries
  both paths' verdicts: `jina: answer is not the page: target answered
  HTTP 401 Unauthorized (after native: HTTP 403 Forbidden: origin blocked
  the request (likely anti-bot))` is an HTTP error, not an anti-bot block.
  Even the live error matches both sentinels with `errors.Is`. Only the
  fetcher knows which path spoke for the target.
- The wording changes with the fetchers.
- `curio jobs prune` deletes failed jobs, and their documents' causes
  would go with them.

So the fetcher classifies the typed error once, when the job gives up,
and the cause is stored with the state.

**The causes**, with the count on the author's library after the backfill
(7,467 documents, 2,969 failed or dead):

| Cause | Means | Documents |
|---|---|---|
| `dead_link` | The content is gone: a 404 or 410, a soft 404, a redirect onto a homepage or another site's landing page. The one cause of a `dead` document. | 819 |
| `anti_bot` | The site blocked the request: a 403 or 503, a challenge or block page. What Jina found after it can make it another cause (below). | 926 |
| `login_wall` | A login page, a redirect onto one, or too little text to be the article. | 188 |
| `jina_refused` | The Jina fallback refused the target (a domain block, a publisher's opt-out, a deterministic 4xx), normally after the site served a page curio can't use (below). | 185 |
| `tls` | The site's certificate failed verification. | 43 |
| `unreachable` | The host doesn't resolve, or refuses connections. | 244 |
| `timeout` | The site, or the tool fetching it, took too long. | 130 |
| `network` | Any other transport failure: a reset, a TLS alert, a redirect loop, a connection closed before the answer, our own network down. | 26 |
| `rate_limited` | The site, GitHub or YouTube rate-limited curio. | 222 |
| `http_error` | Any other status, or an error page naming one. | 143 |
| `unsupported` | A URL or content curio can't read: a channel page, a GitHub profile, a file that isn't HTML, a PDF it can't extract. | 28 |
| `too_large` | The response was over the 32 MiB body cap. | 0 |
| `index` | The fetch worked, then indexing gave up. | 0 |
| `other` | Anything else. | 15 |

**Precedence** (`fetcher.FailureCause`, `internal/fetcher/cause.go`):

1. A dead link anywhere in the chain is `dead_link`: the rule that made a
   document dead before, so `dead_link` and the `dead` state always
   agree.
2. After a Jina fallback, one path speaks for the target: Jina, when its
   failure is a verdict about the target (`jinaAnswered`: an answer that
   isn't the page, a refusal; `errJinaTargetTrouble`: a status the target
   gave Jina; `ErrTooLarge`), and the origin otherwise. Jina's own trouble
   (its 429, a 5xx, its CDN's challenge, 401/402, a 403 naming no target,
   a network error, the cooldown's fail-fast) says nothing about the site,
   so a document is grouped by what the site did, as the fallback policy
   judges it. To tell the two paths apart the composite error is now a
   named type, `jinaFallbackError`, whose text is byte-identical to the
   old `%w (after %w)` and whose `Unwrap` returns Jina's error first, so
   `errors.Is`/`As`, `last_error` and the host cache's text are unchanged.
3. The first of these the chosen error matches: `errJinaRefused`
   (`jina_refused`), `ErrTLSCertificate`, `ErrHostUnreachable`,
   `ErrTooLarge`, `ErrAntiBot`, `ErrLoginWall`, then `unsupported`
   (`ErrUnsupported`, `ErrFetcherNotFound`, `errPDFUnreadable`), a rate
   limit (`errRateLimited`, or a 429 status), any other status
   (`http_error`), a deadline or network timeout, any other transport
   failure (`network`), and `other`.

A host-cache hit is classified by the verdict it cached. A document
records the error its last attempt ended with, so what it records after a
host-wide origin verdict (the site answered 403 or 503, or redirected
onto its own login page) depends on what Jina then did: `Native.Fetch`
caches the origin's verdict (`settle`) only when Jina gave one of its own
about the target.

- Jina refused the target, or answered with something that isn't the
  page: the origin's verdict is cached and the failure stays retryable,
  so the retry fails from the cache and the document records `anti_bot`
  (`login_wall`), the site's first document included. Normally: the
  cache lives 15 minutes, in memory, so when a daemon restart or a paused
  queue lets it lapse before a document's last attempt, that attempt asks
  the origin and Jina again and records Jina's verdict, `jina_refused`
  for a refusal.
- Jina's own trouble caches nothing and is retried, but the origin speaks
  for the target, so the document records `anti_bot` (`login_wall`) once
  its attempts run out.
- Jina found the target's 404 or 410 or a not-found page (dead-link
  detection on), or answered over the body cap: the document fails at
  once, `dead_link` or `too_large`, and nothing is cached.
- Jina reported a status the target has for now (a 429, a 5xx other than
  503, a 404 or 410 with dead-link detection off): retried without
  caching, so the document records `rate_limited` or `http_error` when
  its last attempt ends that way.

Behind a verdict about one page (a thin, login or challenge page, a PDF
curio can't read), nothing is cached and Jina's refusal, or an answer
that isn't the page, fails the document for good at once under Jina's
verdict; the other outcomes go as above, with the origin's page-level
cause where the origin speaks. On the author's library all 185
`jina_refused` documents came after a login-wall or thin page, none after
a 403 or 503, and the 33 whose cached verdict quotes Jina's
`AbuseAlleviationError` are `anti_bot`. Of the documents whose own
fallback followed the origin's 403, 33 are `dead_link` (Jina found the
target's 404 or 410, or a not-found page) and 1 is `http_error` (the
target's 500, reported by Jina). So `--cause=anti_bot` reaches a site
that blocked curio, and `--cause=jina_refused` the pages Jina would have
read but for its refusal.

`fetcher.ErrUnsupported` is new, wrapped where a YouTube URL names no
video, a GitHub URL isn't one the GitHub fetcher reads, and a response is
neither HTML nor a PDF; those were bare `PermanentError`s. Their errors
read as before, but for "(unsupported URL)" at the end of YouTube's and
of GitHub's unrecognized-URL one. A new sentinel or failure path belongs
in the list.

**The invariant:** `failure_cause` is set exactly when the document is
`failed` or `dead`, and `dead` goes with `dead_link` and nothing else.
`MarkFailed(id, cause)` derives the state from the cause
(`FailureCause.State()`), so no caller can pair `dead` with `anti_bot`;
`UpdateState` is gone, since it couldn't keep the two in step.
`MarkFetched`, `ApplyFetch` (a duplicate or stale fetch job that succeeds
for a failed document), `RequeueFetch` and `RequeueFetchByStates` clear the
cause in the statement that moves the document on, and `Create` refuses a
pair that breaks the invariant. The store is the only writer, so no trigger
guards it; a test drives every writer and runs the invariant query after
each.

**Migration 014** adds the column, fills it and creates the index inside
goose's transaction. The backfill takes the `last_error` of each failed or
dead document's most recent failed job and reads it with LIKE rules frozen
for the wording errors had until now, mirroring the classifier: a dead
document is `dead_link`; no failed job, or one without an error, is
`other`; an index job's failure is `index`; a Jina-led error by Jina's
verdict, or else by its `(after native: …)` part; a host-cache hit by the
verdict it cached; then the native, youtube, github and dispatcher shapes,
timeouts and transport failures. From here on the classifier works on
typed errors, so the rules never need to follow a new wording. The
backfill leaves `updated_at` alone, unlike every UPDATE the store runs:
the failures happened already, and bumping the column would reorder the
Library around the migration. On a copy of the author's library it took
about 160 ms, gave the counts above, and left no document breaking the
invariant. `TestMigration014_FailureCause` pins the rules over the real
error shapes.

**No CHECK constraint**, unlike every other enum column: causes will grow
(the Failures view will want finer ones), and changing a CHECK on
`documents` means rebuilding the table most others reference, with the
recipe in `migrations/README.md`. The store validates every write
instead; `TestOpenAPI_FailureCauseEnum` holds the spec's enum to
`store.FailureCauses`, and a CLI test holds `--cause`'s help to it.

**Indexes**, following "Indexes follow the queries":

- `idx_documents_tenant_cause_updated (tenant_id, failure_cause,
  updated_at, id) WHERE failure_cause IS NOT NULL`. Without it a cause
  filter walks `idx_documents_tenant_updated` through every document to
  fill a page of a rare cause: SQLite has no statistics to prefer anything
  else. It is partial, so the documents that never failed, most of a
  library, cost it nothing. It serves a page of one cause and the pages
  after it, alone or with state, host or folder; refetch-all by cause;
  and the failure summary, for which `failure_cause IS NOT NULL` is a
  range on it. Every earlier plan is unchanged.
- A document's jobs (`GET /v1/jobs?document_id=`, which the Document page
  will poll every 2 s). Written plainly, `AND j.document_id = ?` is
  planned through `idx_jobs_tenant_updated`, the index that serves the
  ORDER BY, walking every tenant job to find a handful: 3 ms warm and up
  to 16 ms on the author's 12k jobs, growing with a table only manual
  pruning shrinks, against 0.03 ms through `idx_jobs_document`. The tenant
  term is written `+j.tenant_id`: the unary plus keeps SQLite off the
  tenant indexes, and it seeks `idx_jobs_document` and sorts the few rows.
  No new index: one on `(document_id, updated_at, id)` would still need
  the plus under a status filter, and would churn the pinned last-error
  plan. The plans are pinned, with a check that no tenant index appears.

**API:**

- `GET /v1/failures` answers `{total, causes: [{cause, count, hosts:
  [{host, count}]}]}`: the causes by count, then name, each with at most
  5 hosts by count, then host. A host is the URL's authority as the
  documents list's host filter matches it (lowercased, port kept, `www.`
  a host of its own), so `GET /v1/documents?cause=C&host=H` lists exactly
  what the pair counts, which a test checks for every pair. The store
  reads each failed document's cause and URL over the partial index and
  counts in Go: about 1 ms for 3,000 rows, where SQL would need to cut the
  host out of each URL and rank the hosts within each cause. The arrays
  are never null.
- `GET /v1/documents?cause=` composes with every other filter; documents
  carry `failure_cause`, omitted when there is none.
  `POST /v1/documents/refetch-all?cause=` composes with `state` and its
  default, and clears the causes it resets. `cause=dead_link` without
  `state=dead` is a 400: dead links' documents are dead, which the default
  leaves out, so it would enqueue nothing while reading like a refetch of
  every dead link. Other pairs no document can match (`state=fetched&
  cause=tls`) truthfully enqueue 0.
- The Library page parses its query as `GET /v1/documents` does, so its
  next-page link and filter form carry `cause=`; it has no control for it
  yet.
- `curio refetch --all --cause <cause>` (with or without `--state`), and a
  cause line in `curio docs` and `curio docs show`. `--state` or `--cause`
  without `--all` is now an error, where `--state` used to be ignored.

**Not done:**

- **Any change to fetching.** The cause is recorded, never acted on: holding
  a host-cache anti-bot failure back to retry later, and pacing Jina per
  site, are the import-failure fixes, separate changes.
- **Causes for yt-dlp's refusals.** Private, removed and unavailable
  videos stay `other` (all 15 of the author's `other` documents); yt-dlp's
  wording is the only signal, and no view needs them apart yet.
- **Folding transport failures into `timeout` or `unreachable`.** The
  first taxonomy had no `network`; the author's library has 26 documents
  of them (9 TLS alerts, 8 `ENETUNREACH`, 5 resets, 3 redirect loops, a
  connection closed before the answer), which mean neither a slow site
  nor a missing one to someone deciding what to retry.
- **An HTML body that fails to read.** go-readability reads the body
  itself and flattens its read error into text (`native: readability:
  failed to parse input: …`, no `%w`), so a timeout or a connection lost
  while reading a page's body is `other`, where a PDF's or GitHub's is
  `timeout` or `network`. The backfill, which reads only the words, would
  call such a timeout `timeout`; none of the 3,156 errors the author's
  jobs record has the shape. Keeping the read error means capturing it
  around the body in `tryReadability`, a change to the fetch path's
  errors rather than to their classification.
- **A cause filter on `curio docs`.** It shows the cause; the flag waits
  for a need.

**Revised (2026-09-29):** the Library shows its cause filter now: a line
under the toolbar names the cause, with a Clear that drops it alone. The
Library's Failures tab groups the failed and dead documents by cause and
refetches a group by it. See "Dashboard: the Failures tab".

---

## Dashboard: a design language under the CSP

**Decision:** the dashboard's pages share one design language: one
stylesheet (`internal/ui/static/app.css`) of tokens and components,
served from `/ui/static/` under the pages' `style-src 'self'`, and a
page frame (`templates/layout.html`) with a skip link, a sticky header
(the brand, the navigation with icons, a search box), and a footer
naming the daemon's version and the address it listens on. Every
template is built from its components, and the rules below hold for any
page added later.

**One stylesheet, no inline style.** Tokens first (neutrals, one indigo
accent, four status tones with soft fills, a type scale on a 14 px UI
base, 4 px spacing steps, radii, shadows), then base, layout,
components, pages, the responsive rules at 64 rem and 48 rem, and
utilities. System fonts only; nothing is imported, and the one image is a
`data:` URL (the select's chevron). Dark mode follows
`prefers-color-scheme`, guarded by `:root:not([data-theme="light"])`, and
the same values sit under `:root[data-theme="dark"]` so a host can force
a theme; the product never sets `data-theme`. `color-scheme: light dark`
makes native controls follow. `prefers-reduced-motion` turns off every
animation and transition. `TestStylesheet` pins what no page test can
see: the rules below, the two dark blocks equal, and the sheet loading
nothing.

**`overflow-wrap: break-word` on the body, never `anywhere`.** The old
sheet set `body { overflow-wrap: anywhere }`. Unlike `break-word`,
`anywhere` counts a break chance between any two characters when a box's
min-content width is worked out, so every table cell's minimum was about
one character, and the Library's automatic table layout shared its width
by max-content: stored URLs up to 705 characters and errors over 400 won,
and State, Type and Updated read "Sta/te", "fet/ch/ed", one letter a line
on a phone. `anywhere` is left only where a string is read rather than
scanned and has no column to squeeze: `code`, the full error's `<pre>`,
the unformatted text's `pre.source`, IDs and paths (`dl.facts .mono`), a
document's `h1`, search result titles and passages, related titles,
bookmark titles and interest labels. `TestStylesheet` holds that list.

**Tables have fixed layouts.** `table.data` is `table-layout: fixed`
and its columns' widths come from `<col>` classes (state 7 rem, type
6.5 rem, time 7.5 rem, similarity 9.5 rem, rank 3 rem), never from
content, and a cell clips what it holds, so no stored value spills into
the next column. Below 48 rem the Library folds its Type and Updated
columns into the line under each title (`.narrow`), and two columns
remain.

**Proportions are attributes, never `style=`.** The CSP refuses inline
style, so the state bar and the coverage bar are SVGs drawn by the server
in a 0-100 viewBox, each segment an `x` and `width` attribute
(`stateBar`, `coverageBar`, worked in thousandths so segments meet and
the bar ends at 100.000) and its colour a class; similarity and cohesion
are native `<meter>`s, a migration's progress a `<progress>`.

**Times.** Lists show relative times ("13 min ago", "in 3 h", a date past
a week) in `<time datetime>`, RFC 3339 in UTC, with the exact
daemon-local time in `title`. A document's page shows its own dates in
full; its bookmarks, a list, are relative. A
published date is a day, stored as its midnight UTC (347 of the author's
2,048 are), so it is formatted in UTC (`day`): on the daemon's clock west
of UTC, 2014-03-14 read as "2014-03-13 20:00".

**Hostile content, by place.** Every stored string is hostile and can be
any length:

| Where | Title | URL | Error |
|---|---|---|---|
| Lists (Library, Recently saved, Interest members, card members) | 1 line, ellipsis, `title=` | host and path, 1 line | cause and short error, 1 line, `title=` |
| Search results | 2-line clamp | host › path, 1 line, `title=` | none |
| Document page | wraps (`anywhere`) | 1 line, ellipsis | the cause's sentence, and `<details>` "Full error" in a `<pre>` that wraps anywhere and scrolls past 16 rem |
| Facts: IDs, paths | | monospace, wraps anywhere | |

**Display transforms are plain strings.** `host`, `shortURL`, `urlTrail`,
`shortError`, `causeLabel`, `num`, `pct` and the rest (`format.go`)
return strings or structs of them, which `html/template` escapes; none
returns a trusted type, and `TestTrustedHTMLOnlyFromTheSanitizer` is
unchanged.

- `shortURL` names an untitled document: host, escaped path without the
  trailing slash, and the query. It keeps the query because it is often
  what tells two pages apart: of the author's 3,019 untitled documents,
  401 have one, and without it 155 of them collapse into 26 identical
  names, 53 reading `www.youtube.com/watch`. It drops the scheme,
  userinfo and fragment, and never decodes a percent-escape: `%E2%80%AE`
  stays text, not a right-to-left override.
- `shortError` drops what every stored error starts with and says nothing
  about the document: at most one worker wrapper (`permanent failure: `,
  `fetch failed: `), then one fetcher name (`native: `, `github: `,
  `youtube: `, `web2md: `; `TestFetcherNames` holds the list to the
  fetchers' `Name()`s), then native's `fetch: `. `jina: ` stays, since it
  says Jina gave the verdict, and so does `index: `. It collapses
  whitespace and cuts at 300 characters: stored errors reach 1,657 on the
  author's library, and a subprocess's stderr 64 KiB. The whole error is
  in `title=` and on the document's page.
- A failed or dead document's last error and cause show only while it is
  failed or dead, in the Library as on its page (`failureCurrent`): the
  Library printed the last failed job's error whatever the state, and two
  fetched documents in the author's library showed one.
- A document's page offers the commands that work: `curio refetch
  --force` for a dead document, whose plain refetch the API refuses, and
  `curio reindex` only when it has an extraction to reindex.

**Class and icon names come from fixed sets.** A class or icon name is a
template literal or the output of a Go func over a fixed set:
`stateClass` (`state-pending|fetched|failed|dead`, or nothing),
`stateTone`, `typeIcon`, `causeLabel` over `store.FailureCauses()`
(`TestCauses`: each has a label and a sentence, the labels distinct), an
upstream's `Tone`. A stored value never picks a class, as `state-{{.State}}`
did, escaped but still chosen by the page. The Overview's tile reads
"Fetched", not "Searchable": `stats` counts fetched documents, not
indexed ones, and while the queue works fetched documents wait for their
index jobs.

**Icons are an inline partial.** `templates/icons.html` defines `icon`,
one switch on its name whose branches are whole `<svg>`s of inline
shapes (`aria-hidden`, sized by where they sit), and `logo`. No sprite:
a `<use href="#…">` is a fragment reference to keep inert and a request
to allow, and an icon font is a font to load. The shapes are Lucide's,
whose ISC license asks for its notice in every copy; the file's opening
comment carries it, and the file is embedded as is. `TestIcons` checks
that every icon a template or a func names is defined and every one
defined is used, and that the file references and styles nothing.

**The frame.** A skip link (`href="#main"`) comes first. `uitest` refused
every fragment link, so it now accepts one exactly when the page has an
element with that id; `#ZgotmplZ`, html/template's refusal, never passes.
The navigation is built in Go (`navItems`, over `navHrefs`, which an
error page's Retry uses too) and stays `<nav aria-label="Main">`. The
header's search box is a plain GET form to `/ui/search`, hidden below
64 rem; the Search page (which is one) and the starting page (which
can't search) override its block with an HTML comment, since text/template
keeps a block's default over an empty define. The footer names the
listener's address (`ln.Addr()`, handed to the dashboard by `NewServer`),
so a page says which daemon, on which port, it came from.

**Nothing grows past its box.** Besides the clipping cells, the search
results column (`.results`) is `width: 100%`: a flex item with auto
margins shrinks to its min-content width, which a result's one-line
address makes wider than a phone. No test sees layout, so a change to
the sheet or a template is checked in a browser, at 1440 and 390 px, in
both themes, with long and hostile data, under the real CSP (headless
Chrome logs a CSP violation to its console).

**Revised (2026-09-29):** the header's search box is a GET to `/ui/`, the
search home, and the "Fetched" tile is Status's, the Overview's
successor. The Recently saved list is gone from the table of hostile
content by place, with the Overview. See "Dashboard: search is home, the
Overview becomes Status".

---

## Dashboard: search is home, the Overview becomes Status

**Decision:** the dashboard opens on search. `/ui/` is the search page,
and the navigation is Search, Library, Interests, Status. The Overview
moved to `/ui/status` as Status: what needs attention first, as callouts
(Ollama not ready, the embeddings drifted, the Jina Reader fallback
failing or degraded), then a 2:1 board of the library, the queue and "Why
documents failed" on the left, health, progress and the jobs on the
right. `/ui/search` answers 302 to `/ui/` with its query as sent. The
Library's state filter became tabs with counts, and the search page
gained a type filter. `curio ui` still opens `/ui/`.

**Why search is home.** Search is what people open the dashboard for;
Status answers "is curio working?", which you ask now and then, or when
something looks off. A status strip on the home was considered and
rejected: a line that reads the same on every visit becomes noise, the
box's placeholder already says how much there is to search ("Search your
4,498 documents"), and what does affect a search, semantic search being
down, shows with the results as the keyword-only warning.

**The home reads nothing while a query is typed.** Search as you type
renders the whole page on every keystroke, so with a query the page reads
the search and nothing else. Without one it reads `stats` (the fetched
count, for the placeholder) and `interests` with limit 6 and no members
(the latest run and its six largest clusters), for one quiet line of
interests at the foot of the screen. Each read that fails is logged once
(`reportError`, a 499 at info when the client has gone) and left out: no
panel error on a page that shows no panels. On the author's library
(7,467 documents, about 12,000 jobs) each of stats' three counts and the
interests read take under a millisecond on covering indexes, so nothing
is cached; they still run only without a query.
`TestUI_SearchHomeReadsOnlyWithoutAQuery` counts the reads: none with a
query, htmx's request or not, and one each without.

**#results carries no class.** htmx swaps the children of `#results`
(`hx-select="#results > *"`, `innerHTML`), never the element, so a class
on it would stay as the first render set it: a page that opened on the
home would show full-width results after typing, and one that opened on
results would squeeze the interests line into the 48 rem column. Its
child carries the width: `.landing` on the home, `div.results` with a
query. A plain GET of either state renders the DOM a swap leaves.

**The old address redirects, after the cross-site check.** `/ui/search`
answers 302 with `/ui/?` and its raw query, unparsed, so any parameter a
later page takes survives, and the fixed path keeps the Location on the
daemon. `isolateDashboard` runs before routing, so another site's
subresource request for `/ui/search?q=` is refused with a 403, never
redirected into a search.

**The type filter.** `content_type` is validated as the Library validates
it (`contentTypeParam`, one helper): a value that isn't a content type is
a 400 page, on the home too, and no read runs. A valid type the page
doesn't offer (thread, unknown) still limits the search and marks no
tab. The tabs are links built in Go (`searchHref`), so a type is a plain
navigation that works without JavaScript; the chosen type is a hidden
input in the form, so a plain submit keeps it. htmx sends a GET with the
triggering input's value alone, so the box carries `hx-include="closest
form"` (htmx sends `q` once), and the tabs sit outside `#results`: typing
"kafka" on the home left the PDFs tab pointing at
`/ui/?content_type=pdf`, and choosing it dropped the query. The box
therefore swaps `#search-scope` out of band (`hx-select-oob`) with every
answer, as the Library's load more swaps `#more`. `POST /v1/search`
validates its filters the same way (see "API: filters are validated,
sizing knobs default").

**Library tabs count only what they list.** The counts come from `stats`
and cover the whole library, so a tab shows one only when state is the
page's one filter: with a host, folder, type or cause chosen, "Failed
2,150" would sit over the failed documents of one host. The form keeps
the current state as a hidden input, since Apply would otherwise drop
the tab the state select used to hold. "Showing N of M documents" under
the table needs N, the rows the page shows, which a keyset cursor can't
know on the next page: htmx's load more asks for it with `shown`, the
rows shown so far, and swaps `#showing` out of band with `#more`; the
plain link, for JavaScript off, shows only its own rows and carries no
`shown`. `shown` is display only (`intQuery`, 0 when absent or out of
range), never a filter, and no tab or form carries it. M is the current
state's count, left out when the counts don't apply or when N passes it,
as it does when the library changed between pages. A CSS counter over
the rows was rejected: it prints ungrouped numbers, puts content in CSS,
and no test can see it.

**Interest names are cut only when two words remain.** A line of six
names fits one line at 1440 px when each is cut at its first " and ", but
cutting always turned 30 of the author's 91 labels holding " and " into
one word ("Identity and Access Management" read "Identity"). `interestName`
cuts only when the words before it are at least two. The full label is
in `title`, and a name longer than 18 rem ends in an ellipsis: labels
come from an LLM or from page words and can be any length.

**Status's callouts say what the tracker says.** They come from the
health read alone, in a fixed order, and only while it succeeded. An
upstream gets one while failing (danger) or degraded (warn), from
`Upstream.Alert` over a fixed set; a pause is the upstream's own request
and gets none. The copy counts only the tracker's failure classes as
failures: a refusal of a target is a healthy answer
("Fetch upstream health"), so the degraded callout gives the reported
counts and window ("6 of its 20 calls in the last 15m failed") rather
than a share or a window of its own, and both point to `curio doctor`
for the cause.

**Why documents failed.** Status draws the five commonest causes of
`failures` (the function `GET /v1/failures` runs) as bars scaled to the
largest, each linking to the Library of that cause. The card is hidden
when nothing failed. `FailureSummary` reads the cause and URL of every
failed and dead document, about a millisecond per 3,000: fine once per
page load, but a region of the page that is polled must leave the card
out.

**What moved elsewhere.** Recently saved left the dashboard with the
Overview; the Library's "Date saved" order will list saves. A Failures
view of the library, with refetch by cause, will be a Library tab, not a
fifth navigation item: a phone's navigation fits four.

**Revised (2026-09-29):** the failures card leads to the Library's
Failures tab: "All failures →" to the tab, where "Failed documents →"
left out the dead links the card counts, and each cause to its card
there (`/ui/failures#cause-<code>`), rather than to the Library of that
cause, which answers 400 for a cause this build doesn't know. See
"Dashboard: the Failures tab".

---

## Dashboard: actions through /v1, sent by a first-party module

**Decision:** the dashboard changes things. A document's page refetches it
(a dead link only after a confirm, with `force=1`) and reindexes it,
Interests rebuilds the interests, and Status pauses and resumes the queue
and sets its throttle, keep-awake and schedule. Each change goes to the
`/v1` route the CLI uses, as JSON, sent by one first-party script,
`internal/ui/static/actions.js`; the pages stay GET-only, and nothing new
is routed. What changes on a page refreshes by itself: Status's queue,
counts, progress and jobs every 2 seconds and its health every 15, a
document's jobs and the Interests' rebuild while they are in flight.

**Why a module, not htmx's json-enc.** json-enc sends every form value as
a string, so `{"paused":"true"}`, which `PUT /v1/queue` refuses, and it
has no way to show a problem's `detail`. actions.js is under 200 lines:
one strict IIFE, no dependency, no global, and none of `eval`,
`innerHTML`, a timer, `mode` or `credentials` (`TestActionsScript` reads
it for them). It writes only `textContent` and creates no element.

**Every change is declared in Go.** A control is a button, a checkbox or
a form carrying an `Action` (`internal/ui/actions.go`) as data attributes:
`data-method` (POST or PUT), `data-path` (under `/v1/`, query included,
its document ID one escaped segment), a button's `data-body` (json.Marshal
of fixed keys), a checkbox's `data-field`, a form's `data-join`, and
`data-status`, the id of the `.action-status` it reports to, with
`data-done` (and a checkbox's `data-done-off`). actions.js sends exactly
that: the body, `{field: checked}`, or a form's fields with the values of
those sharing a name joined (the schedule's two times make
`{"schedule":"22:00-07:00"}`); it parses no 2xx body. A template writes
none of it: `TestTemplatesHaveNoInlineCode` requires each of those values,
and every `hx-get`, to be one template action. `TestDashboard_ActionsMatchTheAPI`
renders the pages in each state and holds every control to the API: the
method and path route (`methodIndex`), the route is in `api/openapi.yaml`,
the query is one the route takes (refetch's `force=1`), the body, built
by actions.js's rules, decodes strictly into the request and validates,
and the status is on the page; every `ActionKind` must appear. uitest
refuses a `data-method` other than POST or PUT, a `data-path` that isn't
a clean path under `/v1/` (no scheme, host, empty or dot segment, escaped
or not), an `hx-get` off `/ui/`, and any `hx-post`, `hx-put`, `hx-patch`
or `hx-delete`.

**How it sends.** `fetch(url, {method, headers, body, signal:
AbortSignal.timeout(15000)})` in its default mode, with no `mode` or
`credentials`. Checked in headless Chrome 154 under the pages'
`Referrer-Policy: no-referrer`: a POST and a PUT carry `Origin:
http://127.0.0.1:P`, `Sec-Fetch-Site: same-origin` and `Sec-Fetch-Mode:
cors`, so the Origin rule and the same-origin rule ("Local API") admit
them, where a plain form post would send `Origin: null`. `Content-Type:
application/json` goes exactly with a body; refetch, reindex and rebuild
send none. It refuses, sending nothing, a method other than POST or PUT
and a path not under `/v1/` of its own origin. One change runs at a time
per page, and none is retried: a click while one is in flight does
nothing, and a checkbox toggled then is set back. The status shows
`data-state` busy, then ok with the done text, or error with the problem's
`detail` (else its `title`, else `HTTP <status>`), or, on a network error
or the timeout, that the daemon didn't answer; a checkbox whose change
wasn't taken is set back. Settled, it dispatches `curio:changed` on the
body, which the pollers listen for.

**Controls that need JavaScript are `.js-only`, not `hidden`.** actions.js
first marks `<html>` with `data-js`; app.css hides `.js-only` until then
and `.no-js` after (`!important`), and gives `[hidden]` `display: none
!important`. The attribute alone doesn't hide this design's controls:
`.btn`, `.segmented` and `.switch` set `display`, which beats the
browser's `[hidden]` rule (a `hidden` `.btn` computed `inline-flex` in
Chrome), and a poll re-renders them, so a script that revealed them once
would have to reveal them after every swap. Without JavaScript the queue
card shows its settings and the commands (`curio pause`, …) instead, and
a document page its "From the terminal" commands, as before. Only the
pollers carry `hidden` (`TestTemplates_HiddenOnlyOnPollers`).

**A poll reads only what it refreshes (`?poll=`).** htmx's `hx-select`
selects in the browser; the server would still read the whole page. So a
poller asks the same route for its regions (`ui.PollParam`): Status's
`?poll=live` reads the stats, the queue and its metrics, `?poll=health`
the health alone; a document's `?poll=jobs&updated=…&extraction=…` reads
the document, its jobs and, while one waits, the queue, never its text,
related documents or bookmarks; Interests' `?poll=rebuild&run=…` reads the
queue and the newest run, never the interests. Same route, handler and
template, the URL alone decides the answer, and a panel the answer didn't
read is left out, never shown empty. A value the page doesn't take, or a
document poll without a time, is a 400 that reads nothing. Measured on a
`sqlite3 -readonly` `.backup` of the author's library (7,467 documents,
11,969 jobs), served by a throwaway daemon with Ollama unreachable, median
of 20 curl timings:

| Request | Time | Size |
|---|---|---|
| `/ui/status` | 4.3 ms | 15.6 KB |
| `/ui/status?poll=live` | 1.9 ms | 10.9 KB |
| `/ui/status?poll=health` | 0.5 ms | 5.1 KB |
| a document's page (9 KB of markdown) | 41.8 ms | 19 KB |
| its `?poll=jobs` | 0.45 ms | 3.4 KB |
| the largest document's page (2.3 MB, 1 MiB rendered) | 263 ms | 1.0 MB |
| its `?poll=jobs` | 0.48 ms | 3.4 KB |
| `/ui/interests` | 5.8 ms | 63 KB |
| its `?poll=rebuild` | 0.43 ms | 3.7 KB |

Polling a document's whole page every 2 seconds would have cost 2 to 13%
of a core per open tab. Why documents failed reads the cause and URL of
every failed document (about 2 ms of the Status page's 4.3) and health pings Ollama
(up to 500 ms when it hangs), so neither is in the 2-second poll: the
failures are read with the page alone, health every 15 seconds.

**Pollers.** A poller is a dedicated, empty, hidden element (`data-poll`,
the poller partial, built from `ui.Poller`) with `hx-get` built in Go,
`hx-swap="none"`, `hx-select-oob` naming its regions and
`hx-sync="this:replace"`. No content region carries hx- attributes. Two
cases, reproduced in headless Chrome against a scratch server with the
vendored htmx and the pages' config and CSP, decided the shape. A region
that swapped itself (`hx-select` of itself, outerHTML) with the default
sync lost a refresh triggered while its own request was in flight: the
queued request belongs to the element the answer replaces, and htmx drops
requests of detached elements; with `this:replace` the newest trigger is
the one answered. And the pages' htmx config swaps 4xx and 5xx answers,
so an answer without the region (the 503 starting page while the daemon
restarts, an error page) replaced it with nothing, poller included. A
poller that swaps nothing in its own place can remove nothing: the ids
aren't in such an answer. Status has two pollers, `every 2s, curio:changed
from:body` and `every 15s`; a document's and the Interests' poller lists
itself among its regions and is rendered with `every 2s` exactly while
something is in flight (or the document is pending), so the server's
render decides whether polling goes on. Each region a poller names is on
its page, and in its answer, exactly once, whatever the state (tests take
the ids from the pollers). actions.js keeps them quiet: a poll is
cancelled while the tab is hidden (`every` has no filter under
`allowEval: false`, so `htmx:beforeRequest` is), or while a change is in
flight, whose answer could land before the change's; the tab becoming
visible triggers one.

**An unchanged region keeps its element.** htmx restores focus by id after
a swap, but a screen reader announces a refocused control again, so
replacing an unchanged Pause button every 2 seconds would announce it
every 2 seconds. On `htmx:oobBeforeSwap` actions.js skips a region whose
new element `isEqualNode` the one on the page. Regions that hold a
control hold nothing that changes with time, and each of Status's
controls has a region of its own (`queue-toggle`, `queue-throttle`,
`queue-keep-awake`), apart from the text that does change (`queue-state`,
`keep-awake-hint`, `schedule-state`), so a control is replaced only when
its setting changes; every focusable element in a region has an id. The
schedule's form is in no region, so a poll never replaces a time being
typed. Checked in Chrome: focus on Pause stays through polls, and the
unchanged button is the same node.

**Stale, not live.** An answer that isn't the page (`detail.isError` in
`htmx:beforeSwap`) or no answer (`htmx:sendError`) marks `<html>`
`data-stale` and swaps nothing; the next page answer clears it. A note
("Not updating: the daemon didn't answer. This page keeps trying.") shows
while it is set, and Progress's live badge hides. It is checked before
the swap, not after the request: a poller that lists itself is replaced
by its answer, and a replaced element's later events never reach the
document.

**No live roles in polled regions.** A polled region announces nothing:
Status's callouts are notes, not alerts, and the panel error has no role,
since a live role there would speak on every poll that changed it. Only
the `.action-status` elements announce (`role="status"`,
`aria-live="polite"`), and none is in a region. A test walks every region
of every polled page's samples for `aria-live` and the alert and status
roles.

**What came of the work is a reload offer, not a swap.** A document's
region lists its jobs in flight (kind, queued or running, the attempt out
of the queue's `AttemptLimit`, a retry's time, and why a closed queue
holds them) and, against the baseline the page was rendered with (its
`updated_at` and current extraction, in the poller's URL, which a poll's
answer keeps), what came of them: a new text (offered as soon as it is
current), and once nothing is in flight, a failure or another change.
Every job outcome moves `updated_at`, so a refetch that fails is reported
rather than going silent. Interests compares run ids: a newer done run is
"New interests are ready: reload"; a newer failed run is its error, once
nothing is in flight, and on page load too, since the interests shown
are then older. Swapping the list in would re-read the page's costliest
read (50 interests with their members, 5.8 ms and 63 KB) every time and
move the grid under the reader; the document's text, the same with its
render. In-flight comes from the queue's per-kind counts; the running
rebuild's start is read only while one runs, and no read walks the queued
jobs, which an import makes thousands of.

**Polls are logged at debug.** An access line is about 200 bytes, and a
visible Status tab polls 1,800 times an hour and its health 240: about
400 KB an hour, 10 MB a day per open tab, into a `daemon.log` that is
never rotated. A GET to a dashboard path with `poll` that answers below
400 is logged at debug (`daemon.log_level: debug` shows it); a failed
poll, and every page load and API request, still at info. Checked on the
throwaway daemon: ten polls added no line at info.

**New reads.** The page handlers read two more things straight from the
store, beside `GetWithLastError` and `ListByDocument`: `JobStore.AttemptLimit`
(the queue's `MaxAttempts`, for "attempt 2 of 5") and
`InsightStore.LatestRun` with no status, the newest run. The engine now
keeps a failed run until the next run replaces it (see "Insight layer",
revised), without which a failed rebuild left nothing to read. The
document's jobs are `listJobs` by document with a page of 10, and
`onePage` now refuses a limit below 1 rather than indexing `rows[-1]`.

**The Origin rule admits the bound address.** A dashboard served from
another loopback address sends its changes with that origin (see "Local
API", revised).

**Checked in a browser.** Headless Chrome 154 against a throwaway daemon
on a small seeded home, with a local test site and a fake Ollama: the
fast poll ran every 2 seconds; Pause and Resume, Normal and Gentle,
keep-awake on and off, and a schedule of 22:00 to 07:00 then Turn off each
changed `GET /v1/queue` and the regions within one refresh; 07:00 to 07:00
showed the API's 400 detail in the card's foot, and an empty time sent
nothing; stopping the daemon showed the stale note and hid the live
badge, and restarting it cleared both with every region in place. A
refetch of a fetched document, held by a paused queue, showed the fetch
and why it waited, then "The text changed: reload", and polling stopped;
"Refetch anyway…" on a dead link opened its confirm with Cancel focused,
Esc and Cancel closed it without a request, and its confirm sent
`?force=1` and reported the link failed again. A rebuild showed queued
(with the queue paused) with its button disabled and focused, then "New
interests are ready: reload", and polling stopped. With JavaScript off
the controls were hidden and the settings and commands shown, and at
390 px no page scrolled sideways. The review then checked the two that
headless Chrome doesn't show on its own. With `document.hidden` overridden,
no poll ran for 4.5 seconds and one ran as soon as `visibilitychange`
fired. With a queue change held open through the DevTools protocol, the
status read that curio-daemon didn't answer after 14.9 seconds, and the
keep-awake switch was set back.

**Revised (2026-09-29):** the Failures tab's poller is the one poller
that reads the failure summary: once per change (`?poll=causes`, after a
change made there and when the tab comes back into view), never on a
timer; Status's still leave it out. A group's refetch adds two kinds of
Action, and `TestDashboard_ActionsMatchTheAPI` now allows a set of
values for each query key and runs refetch-all's own checks
(`refetchAllFilter`) on each such action, so one the API would refuse,
a dead-link refetch without `state=dead` or a cause it doesn't know,
fails the walk. See "Dashboard: the Failures tab".

**Revised (2026-09-29):** the Interests page now shows 24 interests a page,
and reads their members' documents at once: 1.8 ms and 34 KB where the
table above has 5.8 ms and 63 KB for 50 cards. See "Interests page by
offset within a run".

---

## Library: a Date saved order lists saves

**Decision:** the Library's toolbar gains an Order field. Last updated,
the default, lists documents as before. Date saved lists saves, newest
saved first, one row per bookmark: `GET /v1/bookmarks?order=saved` under
the Library's filters (state, type, host, folder, cause), which the page
reads through the same Deps function. In that order the time column reads
Saved, the line under a title names the browser it came from on a wide
screen (the folder on hover), and a note under the table explains the
duplicates and Safari's dates. An untitled document is named by its
bookmark's title, in italics, in both orders and on its own page.

**Why an order of the table.** The redesign weighed three places for the
recent saves the Overview used to list:

- (A) an order of the table, chosen. It adds no element: the tabs,
  filters, Load more and the phone fold all apply, it pages through every
  save rather than a handful, and it is invisible until chosen.
- (B) a collapsed "Recently saved" row at the top of the table card,
  opening onto the 8 newest. The cheapest, with no store or API change,
  but capped at 8, hidden yet there on every visit, and in `created_at`
  order, which follows imports rather than saves.
- (D) a denormalized `documents.saved_at`, the newest bookmark's, with
  its own indexes: one row per document and every filter indexed as
  `updated_at` is, for a backfill, two indexes and the import path writing
  the column. It is worth that only if Date saved becomes the default
  order.

**A row is a save.** A page saved in two browsers is listed twice: 30 of
the author's 7,467 documents are. Safari's bookmarks keep no save date,
so they carry the time curio imported them: all 2,169 of the author's are
stamped within one second, and an import lands as one batch at the top of
the order. The note says both. Reading the Reading List's `DateAdded`,
the one date Safari keeps, is left to the importer, outside the dashboard
work.

**Storage.** Migration 015 changes two indexes inside goose's
transaction, rebuilding no table:

- `idx_bookmarks_tenant_saved (tenant_id, saved_at, id)`. The saved order
  walks it from the cursor and stops at its limit, as the created order
  walks `idx_bookmarks_tenant_created`. Every filter is checked on the
  rows it reads, the document's through its primary key, so a filter that
  matches few saves reads more of the list: the trade the folder filter
  already makes.
- `idx_bookmarks_document`, rebuilt as `(document_id, saved_at, id)`.
  With the saved index alone SQLite, which has no statistics to go on,
  walked every bookmark in saved order for each read of one document's
  bookmarks newest saved first, where it used to seek the document and
  sort its few: `ListByDocument` took 1.2 ms instead of 9 µs, and a
  Library page of 51 untitled failed documents with their bookmark titles
  4.5 ms instead of 0.16 ms, both growing with the library. The rebuilt
  index serves the order too, so both seek and read in order.
- The host is checked on the bookmark's URL. Ingest keys the document by
  the bookmark's normalized URL, so the two are equal (0 of the author's
  7,497 bookmarks differ), and checking the bookmark's skips a document
  lookup for each row the host rejects: a host with no saves reads the
  list in 1.8 ms, against 4.7 ms on the document's URL.

**Paging.** The keyset is `(saved_at, id)` through `keysetAfter`, and
`store.BookmarkOrder.Key` is both the store's `After` and the API's
cursor, so the two can't drift apart. A cursor records its order when it
isn't the list's default (`"o":"saved"`), so the cursors issued before
decode as they did, and one from the other order is a 400 "invalid
cursor" rather than a walk resumed at an unrelated position (see "List
pagination: keyset on (timestamp, id)").

**Measured** on a `sqlite3 -readonly` `.backup` of the author's library
(7,497 bookmarks, 7,467 documents) migrated through 015, SQLite 3.53.4
on an Apple M4 Max, median of 21 reads of a page of 51:

| Read | Time |
|---|---|
| Date saved, the first page | 0.12 ms |
| Date saved, after 5,000 saves | 0.12 ms |
| state=fetched / host=github.com / state=dead | 0.13 / 0.15 / 0.45 ms |
| state=pending (no saves) | 4.2 ms |
| content_type=thread (7 saves) | 4.2 ms |
| cause=tls (43 saves) | 4.3 ms |
| a host with no saves | 1.8 ms |
| a folder with one save | 1.5 ms |
| source=manual (5 saves) | 1.3 ms |
| `ListByDocument` | 8 µs |
| the documents list, first page, with bookmark titles | 0.13 ms (0.11 without) |
| a page of 51 untitled failed documents, with them | 0.14 ms (0.10 without) |

Migration 015 took 15 ms when a throwaway daemon started on a copy, and
23 ms under the probe.

**The tabs keep document counts** in both orders. A tab counts the
documents in a state, the same whichever order lists them, and the lede
names both totals. Exact counts of saves by state would take another
join on every render, for a difference the note explains: on the
author's library All is 7,467 documents and 7,497 saves, Fetched 4,498
and 4,522, Failed 2,150 and 2,152, Dead 819 and 823. The Showing line
never mixes them: "Showing N of M saves", M the bookmarks total, only
with no filter at all, state included; "Showing N saves" otherwise.

**An untitled document is named by its bookmark.** 3,019 of the author's
documents have no title, every failed or dead one (2,969) and 50 fetched
ones, and 3,015 of them have a bookmark with one. A document with no
title of its own takes the title of its most recently saved bookmark
whose title isn't blank: `bookmarkTitleSQL`, in the select list
`ListWithLastError` and `GetWithLastError` share, runs only for untitled
rows and seeks `idx_bookmarks_document`. `GET /v1/documents` returns it
as `bookmark_title`. In the Date saved order a row is a save, so it takes
that save's own title: no subquery, and the same name unless a
document's bookmarks carry different titles (none of the author's
untitled ones do) or the save has none while an older one of the same
document has one (1 save). The Document page names
an untitled document the same way, in its heading and tab, from the
`GetWithLastError` it already runs. The fallback is italic, weight 500,
`--text-2`, the unlabeled interest's treatment, in the title's own font:
it reads as a name, never as the monospace address an unnamed row shows.
Bookmark titles that are addresses are shown as they are (15 of the
3,015 start with `http://` or `https://`, and 46 more are an address
without its scheme): the italic already says it is the bookmark's name,
and a heuristic for what looks like a URL isn't worth its misses.

**Still named by their address:** Interests, an interest's members and
search results. They list fetched documents only (50 untitled, 21 of them
in the current interests), read their documents through their own
queries, and their pages are changing in their own work (interest
paging, search paging). The doc-title partial takes the fallback, so
they can pass one later.

**Revised (2026-09-29):** interests' members are named by their bookmark
now too. `GET /v1/interests` and `GET /v1/interests/{id}` read members'
documents with `GetByIDsWithLastError`, which carries the bookmark title,
and return it as `bookmark_title`; the cards and an interest's member table
show it in italics. See "Interests page by offset within a run".

**Revised (2026-09-29):** search results and the Document page's related
documents take the fallback too: `/v1/search` and `/related` carry
`bookmark_title` for their untitled hits, read once per response with the
same `GetByIDsWithLastError`, and the result's title and the related link
style it as the Library does. Every list that names a document now names
an untitled one by its bookmark. See "Search pages by offset within a
fixed-depth pool".

---

## Dashboard: the Failures tab

**Decision:** the Library gains a second view, Failures, at
`/ui/failures`: the failed and dead documents grouped by
`documents.failure_cause`, most first. A line counts them, failed and
dead links apart. Each cause has a card: its icon, label and code, the
sentence on what it means, the five hosts most of its documents are on
(each a tag leading to the Library of that cause on that host), how many
documents and their share, **Refetch N** and **View in Library →**.
Under the cards, the causes without documents. The tab shares the
Library's head: its title, its lede and a Documents / Failures subnav,
whose Failures tab counts the failed and dead documents. Status's "Why
documents failed" leads there. With it, phase 2 of the dashboard is
complete.

**A Library tab, at a route of its own.** It is a view of the library,
and a phone's navigation fits four items, so the page's frame marks
Library current and the navigation keeps its four (`TestNavItems`). It
has its own route rather than `/ui/library?view=failures`:

- A Library URL's query is a valid `GET /v1/documents` query, parsed by
  the same function. A `view=` would break that, and every Library link
  would have to carry or drop it.
- The tab reads other data, the summary rather than a list, and has a
  poll of its own.
- Every page is one handler, one template and one view model.

**What it reads.** The page reads `failures` (the function `GET
/v1/failures` runs) once, and `stats` once, for the lede. The subnav
counts the summary's total, so the two numbers agree. Its poll reads
`failures` once, and nothing else (`TestUI_FailuresReads`). The Library
page's subnav counts failed and dead documents from the `stats` it
already reads. Measured on a `sqlite3 -readonly` `.backup` of the
author's library (7,467 documents, 2,969 failed or dead, 12 causes),
served by a throwaway daemon, median of 21 curl timings:

| Request | Time | Size |
|---|---|---|
| `/ui/failures` | 4.5 ms | 33.4 KB |
| `/ui/failures?poll=causes` | 3.0 ms | 32.9 KB |
| `GET /v1/failures` | 2.3 ms | 2.7 KB |

**No timed poll.** The summary reads the cause and URL of every failed
and dead document, a scan that grows with the failures, and what changes
the groups is mostly a refetch made there. So the tab refreshes on
`curio:changed`: after a change it makes, and when it comes back into
view (actions.js dispatches the event on `visibilitychange`). Its poller
(`hx-trigger="curio:changed from:body"`, no `every`) asks for
`?poll=causes` and swaps two regions from the one read: the subnav
(`library-subnav`), whose count is the summary's total, and the groups
(`failures-live`): the totals, the cards and the footnote; the empty
state; or the summary's error, which the next refresh can clear. Every
focusable element in them has an id made from its cause
(`refetch-<code>`, `view-<code>`, `host-<code>-<n>`, the confirm's),
never from its card's place, so focus stays on a control whose card
survives a refresh.

**One status for the page.** A refetch the daemon takes requeues every
document of its cause, so the refresh right after it always removes the
card that held the button, and a status of the card's own with it. A
status inside a refreshed region would also break the rule that nothing
polled announces. So one `.action-status`, `failures-status`, sits
between the subnav and the groups, outside both regions, over the totals
its refetches change. It takes no room while empty (`.page-status`).
Status's queue card does the same, with one status for all its controls.

**Dead links behind a confirm, with `state=dead`.** refetch-all's default
states leave dead documents out, and it answers 400 for
`cause=dead_link` without `state=dead` ("Failure causes"). So the dead
links' action sends both (`refetchDeadLinksAction`), and
`FailureGroup.Refetch` is the one place that picks it: no template
decides a query. As on a dead document's page, a refetch of links found
gone asks first. "Refetch N anyway…" opens a declarative popover
(`popovertarget`, no script) with Cancel autofocused, and its button
sends. The confirm sits in the dead links' card, so the N it names is
refreshed with the card. htmx focuses an `[autofocus]` element in
content it swaps in, which would reach the closed confirm's Cancel after
every refresh. The browser check showed that it doesn't: a closed
popover isn't rendered, so `focus()` does nothing, and after a group's
refetch the page's active element was the body.

**N is the page's.** actions.js parses no 2xx body and stays as it was.
The done text is built in Go from the group's count ("Blocked by bot
protection: 926 refetches queued"), and the refresh right after it shows
what is really left. A repeat from a stale page enqueues 0, since a
requeued document is pending with its cause cleared, while saying it
queued N, which the refresh corrects. Reading `jobs_enqueued` would take
a new branch in actions.js for a number the page already shows.

**A cause this build doesn't know** (one a newer daemon wrote; the
column has no CHECK) still gets its card: the code as its label, no
sentence, the neutral alert icon. Its hosts are plain tags, and it has
no View link, Refetch or command, since `/ui/library?cause=` and
refetch-all both answer 400 for it.

**Host tags lead to exactly what they count.** A tag leads to
`/ui/library?cause=C&host=H`, and `failures` counts hosts as the
documents list's host filter matches them, so the Library lists what the
tag counts (`TestUI_Failures` follows one). A stored host can be any
length. In the mockup's own 390 px screenshot a real 48-character host
overflowed its card and clipped its count. So a tag is at most its
column's width, the name gives way with an ellipsis while the count
stays, and the whole host is in `title` (`TestStylesheet`). The hosts are
one group (`role="group"`, "Top hosts"), and the count reads "926
documents, 31% of failures" to a screen reader, with visually hidden
words.

**View in Library** is `causeHref` for every cause, dead links too. The
list's cause filter matches dead documents (`failure_cause = ?`);
`state=dead` is refetch-all's rule alone.

**Status leads to the cards.** "All failures →" leads to the tab. It
replaces "Failed documents →", which left out the dead links the card
counts. Each cause leads to its card, `/ui/failures#cause-<code>`, built
from the `causeCardID` that gives the card its id. The page's `html` has
a `scroll-padding-top` of the header's height and a step, so a jump
lands the card below the sticky header, about 15 px under it in Chrome.

**The Library's cause line.** The Library had no control for `cause=`;
the tab's links set it. A line under the toolbar names it ("Why they
failed: **Blocked by bot protection** `anti_bot`"), with a Clear
(`clearCauseHref`) that keeps every other filter, the order and the page
size. The form still carries the cause as a hidden input, so Apply keeps
it. The subnav's count is the whole library's, shown whatever the
filters. The state tabs still count only a list with no other filter;
the tests that said no count shows on a filtered page now look at the
tabs alone.

**Tones.** A cause's icon and tone come from one table
(`internal/ui/causes.go`, through `causeIcon` and `causeTone`):

- warn where its sentence says a refetch later usually works (`timeout`,
  `network`, `rate_limited`);
- neutral for dead links, drawn as the dead state is, and where a
  refetch can't change the verdict or it isn't the site refusing
  (`unsupported`, `too_large`, `other`);
- danger, the failed state's, for the rest.

That matches the mockup for the 12 causes it shows. Ten new icons are
Lucide's shapes. Status's bars keep their colours.

**Checked in a browser.** Headless Chrome 154 against a throwaway daemon
on the `.backup` above, with its queue paused before anything was clicked
and Ollama unreachable:

- At 1440 and 390 px, light and dark, Failures, Status and the Library
  with `cause=anti_bot` (with and without a host, in both orders) had no
  CSP violation or script error and no sideways scroll at 390. Every
  host tag stayed inside its card with its count shown, the 48-character
  host included. The same held with hostile rows seeded: a 300-character
  host, markup and quotes in a host, and a cause this build doesn't know.
  The only console entry was Chrome's own request for `/favicon.ico` on a
  new origin, a 404.
- Refetch 15 on Other showed "Other: 15 refetches queued". One poll
  followed, the card went, and the totals and the subnav fell from 2,969
  to 2,954. Focus was on the body, and the confirm stayed closed.
- "Refetch 819 anyway…" opened the confirm with Cancel focused. Esc and
  Cancel closed it without a request. Its button sent
  `?cause=dead_link&state=dead`, and the dead links' card went.
- 834 fetches waited in the paused queue, and none ran.
- With JavaScript off, no Refetch showed, and each group showed its
  command.
- A Status row's link landed its card about 15 px below the header at both
  widths.
- Nothing failed, on a fresh home, is the empty state, and the subnav
  counts 0.

---

## Interests page by offset within a run

**Decision:** `GET /v1/interests` and `GET /v1/interests/{id}` take an
`offset`, and the dashboard pages through them by number: 24 interests a
page on Interests, 50 members a page on an interest's page. 24 cards fill
rows of 3, 2 and 1, the grid's columns at each width. The numbered pager is
generic (`internal/ui/pager.go`, the `pager` partial, links built in
`links.go`), for the search pages to reuse.

**Why offset, here.** "API: cursor pagination, not offset" refuses offset
because rows land between two page reads, so a client skips or repeats
them. A run's clusters and memberships are written once, in one
transaction (`ReplaceClusters`), and never change; a rebuild writes a new
run and prunes the old one once it finishes. Within a run, an offset names
the same rows on every read, so offset is exact. Numbered pages need random
access ("page 7 of 10"), which a keyset cursor can't give without walking
the pages before. The live lists (documents, jobs, bookmarks) stay on
cursors.

**Total orders.** An offset over an order with ties can repeat or skip a
row at a page boundary, and the ties are real. In the author's run (222
clusters, 1,951 memberships), 209 of the 222 clusters share their size
with another: there are only 30 distinct sizes. Cohesion separates them
today, with no exact tie on both. 40 groups of members, 96 members in all,
tie on similarity within their cluster. So `ListClusters` orders by `size
DESC, cohesion DESC, id` and `ClusterMembers` by `similarity DESC,
document_id`, and both take a limit and an offset. Each is one SQL
constant ending in `LIMIT ? OFFSET ?`, a limit of 0 or less bound as -1
(no limit); a negative offset is an error before any query.

**An offset is refused; past the end is an empty page.** An offset is a
position: read as 0, a malformed one would answer the first page for
another. So anything but a whole number of 0 or more is a 400 naming it,
as a filter is (see "API: filters are validated, sizing knobs default");
`limit` and `members` keep their defaults. An offset at or past the end is
an empty page, not a 400: the end depends on the run, which can change
between two requests. The empty page still carries `run_id` and
`num_clusters`, so a client can tell "finished" from "rebuilt". Every
interest carries `run_id`, list items and the single interest alike;
`num_clusters` and `size` are the totals across pages.

**Paging through a rebuild.**

- Links between the Interests' pages carry the run they show (`run=`). A
  page asked for from another run shows the newest run's page of that
  number, with a note that the interests were rebuilt since and, past page
  1, a link to the first page. The run it came from is pruned the moment
  the rebuild finishes, so its page N can't be served, and the new run's
  page N keeps the reader's place in the size order. The run asked for is
  compared, never shown. The rebuild's poller is unchanged: it carries the
  run shown and no page, and reads only the queue and the newest run.
- An interest gets a new ID with every run, so after a rebuild the next
  page of an interest's members is a 404. It says so ("interests get new
  IDs each time they are rebuilt"), and its Start over leads to Interests.
- The API reads the latest done run, then its page of clusters: two
  autocommit reads, since a read never opens a transaction (see "SQLite
  DSN: per-connection pragmas via mattn's query params"). A rebuild that
  finishes between them prunes the run, and a page the run held reads no
  clusters. When a page comes back empty for an offset below the run's
  `num_clusters`, it is read once more, from the newer run; a second miss
  is answered as read, rather than chasing rebuilds. `FinishRun` writes
  `num_clusters` as the run's cluster count, so the test can't misfire,
  and an offset past the end never reads again. A card that reads its
  members just after its cluster was pruned shows fewer of them; that is
  accepted, and the rebuild's line offers the reload.

**Members in one read.** Each member used to read its document, then its
current extraction for the markdown path: about 354 statements for
`/ui/interests` (50 cards of 3), 552 for `GET /v1/interests` with its
defaults, 170 for an interest's page of 84 members, and 4,126 for
`?limit=500&members=100` on the author's library. Now a page reads each
interest's page of members (`ClusterMembers`), then all their documents at
once, with `DocumentStore.GetByIDsWithLastError`: the Library's own select,
with the last error, the markdown path and an untitled document's bookmark
title. That is at most 30 statements for `/ui/interests` (the run, the
page, 24 cards' members, the documents, the queue's counts and the newest
run), 4 for an interest's page (the interest, its members, their
documents, its run), 53 for `GET /v1/interests` and 225 for
`?limit=500&members=100`. A member whose document is missing from the read
is a 500, never a 404 naming the interest: memberships cascade with their
document.

The read has a planner trap. With `d.tenant_id = ?` and ten IDs or more,
SQLite, with no statistics, walks `idx_documents_tenant_state_updated`
through every document the tenant has instead of seeking the primary key:
453 µs instead of 116 µs for 50 IDs, 3.9 ms instead of 2.8 ms for 1,000,
and growing with the library. With three IDs it still seeks, so a plan pin
written with a few placeholders passes while production walks the tenant.
So the IDs go in as one JSON array (`d.id IN (SELECT value FROM
json_each(?))`), and the tenant is checked as `+d.tenant_id`, on the rows
found. The SQL is one constant, whatever the number of IDs, so one plan
and one pin cover it, and it has no limit on bound parameters (32,766; the
API's largest request is 500 × 100 IDs, and a store test passes 40,001).
On the empty test database the same SQL without the `+` plans the tenant
walk, so the pin fails on a regression. Measured with the `+`: 116 µs for
50 IDs, 172 µs for 72, 2.8 ms for 1,000, where the reads one by one took
708 µs for 50 and 1.01 ms for 72; the 24 cards' reads take 1.21 ms, the
50 cards' took 3.05 ms. Rejected:

- one window-function query for every card's members: slower (494 µs
  against 357 µs for the 24 reads), since it sorts every member of the
  page's clusters;
- one query joining members to their documents: about as fast (228 µs for
  50), but across the store boundary, where the insight store stays about
  clusters.

Members now carry their `state` and, when untitled, a `bookmark_title`,
and the cards and the member table name an untitled member by its bookmark
in italics, as the Library does. All 21 untitled members of the author's
run have a titled bookmark, and 8 of them are among a card's three.

**No new index.** `ListClusters` seeks `idx_clusters_run (run_id, size
DESC)`, which gives the size order, and sorts only the ties;
`ClusterMembers` seeks the primary key's index and sorts the cluster's
members. Both are pinned with their sorts. Measured on the author's
library, median of 31: `ListClusters`, 24 a page at offsets 0, 96 and 216,
36, 61 and 75 µs; the largest interest's members (84), 50 a page at offsets
0 and 50, 42 and 37 µs. Synthetic worst cases in the same copy: one
cluster holding all 4,498 fetched documents, 287 µs for its first page,
1.6 ms for its last and 237 µs for a card's three; a run of 2,001
clusters, 37 µs for the first page and 628 µs for the last. Covering
indexes, `(cluster_id, similarity DESC, document_id)` and `(run_id, size
DESC, cohesion DESC, id)`, bring these to 20–74 µs and 24–48 µs:
microseconds, for a migration and one more index write per membership on
every rebuild. Revisit when clusters reach tens of thousands of members.

**The pages.** Interests' lede counts the run's interests and, from page 2,
says which page it is ("Page 2 of 10."). The pager sits under the cards
when there is more than one page: which interests the page shows,
Previous, the first and last pages, the current one and its neighbours (a
run of two or more pages left out is one gap, a single one is shown),
Next, and on a phone "Page N of M" between Previous and Next. A disabled
Previous or Next is a `<span>`: a link with `aria-disabled` still takes
focus and still navigates. A page past the last is a 404 that keeps the
page's frame (the head, the coverage, the Rebuild and its poller), with a
card saying how many pages there are and leading to the first and the
last; it reads no members. A page that isn't a whole number of 1 or more
is a 400 that reads nothing, and a poll ignores `page`. An interest's page
lists 50 members, ranked across the pages (ranks computed in Go), under a
pager of its own, with the run it comes from in its meta row ("run of
2026-09-28 10:34"). That run is one more direct store read,
`InsightStore.GetRun`, beside `LatestRun` and `AttemptLimit`; a failed read
is logged once and the line left out. The rank column grew from 3 rem to
3.5 rem: members were capped at 100 before, and in Chrome at 1440 px a
four-digit rank ran 2.7 px past its cell's clip and "1,234" 6.7 px.

**Measured** on a throwaway daemon over a `sqlite3 -readonly` `.backup` of
the author's library (222 interests, the largest of 84 members), Ollama
unreachable, median of 21 curl timings, before (origin/main) and after on
the same machine:

| Request | Before | After |
|---|---|---|
| `/ui/interests` | 5.0 ms, 62.7 KB (50 cards) | 1.8 ms, 34.0 KB (24 cards) |
| `/ui/interests?page=10` | | 0.95 ms, 12.7 KB (6 cards) |
| the largest interest's page | 2.8 ms, 47.7 KB | 1.35 ms, 33.1 KB (50 members) |
| its page 2 | | 0.97 ms, 23.7 KB (34 members) |
| `GET /v1/interests` | 7.1 ms, 125 KB | 2.9 ms, 133 KB |
| `GET /v1/interests?limit=500&members=100` | 49.0 ms, 930 KB | 14.6 ms, 981 KB |

The JSON grew by each interest's `run_id` and each member's `state`. The
Interests' row supersedes the 5.8 ms and 63 KB that "Dashboard: actions
through /v1, sent by a first-party module" measured for 50 cards.

**Checked in a browser.** Headless Chrome 154 against a throwaway daemon
on that `.backup`, its queue paused before it started and Ollama
unreachable, at 1440 and 390 px, light and dark. The pages: Interests
pages 1, 5 and 10, page 11 (the 404), page 2 asked for from an old run
(the note), the largest interest's pages 1, 2 and 3 (the 404), and a
hostile sample seeded in the copy (a 300-character label with markup, an
untitled member whose bookmark title is 300 characters of markup, and a
700-character member URL). On every one:

- no CSP violation or script error; the only console entry was Chrome's
  own request for `/favicon.ico` on a new origin, a 404;
- no sideways scroll at 390, and the phone's pager read Previous · "Page N
  of M" · Next with the numbers hidden;
- the current page carried `aria-current`, and page 1's Previous and the
  last page's Next were spans;
- no rank was clipped, and the cards' fallback names were italic, their
  addresses in the mono face, all inside their cards.

A rebuild started from page 2 was queued behind the paused queue,
"Rebuilding" once the queue opened, then "New interests are ready:
reload", and the poller stopped polling; page 2 of the old run then showed
the new run's, with the note.

---

## Search pages by offset within a fixed-depth pool

**Decision:** every search ranks one pool of fixed depth, whatever it asks
for. Each retriever returns `chunkFanout(store.MaxSearchK)` chunks, 800,
the fused chunks collapse to documents, and the documents sort by score,
then ID. A request is a window of that ranking, `[offset, offset+k)`:
`POST /v1/search` takes `offset`, and answers `total`, how many documents
the ranking holds (at most 100), and `capped`, whether more matched than
it holds. The dashboard shows the ranking 10 results a page, `?page=` 1 to
10, under a numbered pager. `curio search`, MCP `search_bookmarks` and
`curio eval` still send k alone, and get the first k of the same ranking.
`find_related` keeps `max(50, 8·k)`. The pages reuse the Interests'
numbered pager ("Interests page by offset within a run").

**Why a fixed pool.** The pool used to grow with k (`max(50, 8·k)`), so a
page asked as "k=20, keep 11–20" was ranked from more chunks than page 1.
RRF sums reciprocal ranks, and a chunk ranked about 90 in both lists
(2/150 = 0.0133) outranks one ranked 30 in one list alone (1/90 = 0.0111),
so a document could cross page 1's edge, and a reader paging forward would
see it twice or never. With one pool, every window of a query is a slice
of one ranking: pages at offsets 0–90 concatenate to the k=100 answer.

**Why offset here, and cursors elsewhere.** The live lists page by keyset
because rows land between their pages ("API: cursor pagination, not
offset"); the interests page by offset because a run's rows never change.
A search has no keyset: its order is a score recomputed on every
request. Within one ranking an offset is exact; a cursor would only wrap
one, and couldn't jump to page 7. A document indexed between two pages
can shift the ranking under them, as it would shift a reload of the same
page.

**Total and capped** are counted before hydration: `total` is
`min(documents in the pool, 100)`, and `capped` is set when the pool held
more. An offset at or past `total` is an empty window, a 200. With
semantic search on, nearly every query is capped: the vector leg's 800
nearest chunks always come from more than 100 documents (44 of the 44
queries below), and a keyword-only search of common words can be too (the
browser check's). So the results' head says "100+ documents match ·
showing the best 100", and the last page says "curio ranks the best 100
matches; refine the query to see others."

**Validation** is `offset < 0 || offset > MaxSearchK - k`, with k as the
request gets it (`search.default_k` when omitted), a 400 naming the bounds
before the query is embedded; the engine checks the same. It is written as
a difference because `offset + k` overflows: the decoder takes an offset of
9223372036854775807, and the sum would wrap negative and pass.

**The page size is 10, not `search.default_k`.** Pages must tile the
100-document ranking exactly, and the page count must not change with the
config: `ui.SearchPageSize` is 10, `ui.MaxSearchPages` 10.

**Measured**, to decide whether the CLI and MCP rank the pool too. Setup:
a `sqlite3 -readonly` `.backup` of the author's library (4,498 fetched
documents, 41,489 chunks) served by throwaway daemons built from the tree,
with the queue paused and no pulls; 44 queries drawn from interest labels,
document titles and questions asked in natural language, each searched 5
times at k=10 and twice at k=100, and for this build each of its pages at
offsets 0–90; the k-scaled build (cebb708) run before and after this one;
an Apple M4 Max, SQLite 3.53.4, Ollama 0.34.4, `qwen3-embedding:0.6b`. No
relevance judgments exist for the library, so the pool's ranking is
measured against the k-scaled one, not against qrels.

| Ranking: the pool's first 10 against the k-scaled k=10 | |
|---|---|
| Identical, in order | 28/44 |
| The same documents | 30/44 |
| overlap@10 / @5 / @3, mean | 9.41 (min 5) / 4.84 / 2.95 |
| The same top result | 44/44 |
| Results replaced | 26 of 440 (5.9%); they land at ranks 11–23 of the pool |
| Pages at offsets 0–90 concatenated = the pool's k=100 | 44/44 |
| The pool's first 10 = the k-scaled k=100's first 10 | 44/44 |

By title the swaps are mixed: some better ("How to speak so that people
want to listen" for "how to speak patrick winston"), some worse.

| took_ms, 220 searches at k=10 | p50 | p95 |
|---|---|---|
| k-scaled pool (two runs) | 69 | 81–83 |
| fixed pool, snippets made in the BM25 query (prototype) | 82 | 115–147 |
| fixed pool, snippets for the window (this build) | 77 | 86 |
| each page of this build, offsets 0–90 | 76 | 82 |

At k=100, this build took 94 / 103 ms against 83–85 / 123–130. Rebased
onto the interests' paging, which changed only how untitled hits are
read, it returned the same rankings, pages and snippets for 44 of 44
queries, at 73 / 84 ms over 3 repetitions.

**Snippets for the window.** BM25's query made FTS5's `snippet()` for every
row it returned, 800 of them at the pool's LIMIT for a page that shows at
most 30, and that made BM25 the search's critical path. Stages, median of
7 per query on the copy:

| Stage | p50 | p95 |
|---|---|---|
| BM25 with `snippet()`, LIMIT 80 / 800 | 27 / 85 ms | 62 / 157 ms |
| BM25 without, LIMIT 80 / 800 | 14 / 16 ms | 46 / 49 ms |
| vector, k=800 / 1,000 (limits 80 / 800) | 48 / 55 ms | |
| `Snippets`, 30 chunks | 2.3 ms | 3.2 ms |

So BM25's query makes no snippet now. After hydration the engine reads
the snippets of the window's BM25 chunks, at most 3 a result, in one query
(`ChunkStore.Snippets`: `MATCH ? AND rowid IN (…)`, each chunk's FTS row
by rowid, its plan pinned), with the FTS query the BM25 leg ran. They are
the snippets the old query made: 44 of 44 queries alike in the
measurement, and a store test holds `Snippets` to the old query's
`snippet()`. `matches[].snippet` is unchanged on the wire. A failed read
logs one WARN and keeps the hits without snippets; the caller's context
ending is an error.

**The CLI and MCP rank the pool too.** The alternative kept the k-scaled
fanout for a request without an offset. With no regression to detect, one
ranking everywhere wins: the dashboard's first page, `curio search`,
`search_bookmarks` and `curio eval` agree (with the conditional variant
they would differ for 16 of the 44 queries, and eval would measure a
ranking the dashboard never shows), and the API keeps one meaning: an
omitted offset is offset 0, and `total` and `capped` mean the same to
every caller. The cost is 8 ms at p50 and 3–5 ms at p95. `curio search`
now prints BM25 and vector counts up to 800 where it printed 80.

**How to revisit it.** Once relevance judgments exist for a library, run
`curio eval --queries` against both rankings. If recall@10 or nDCG@10
regress, a `search.Request` field can put requests without an offset back
on the k-scaled fanout, keeping the dashboard on the pool.

**Ties rank the same on every page.** The pool is ranked again for every
page, and RRF's ranks come from the retrievers' output order, so a tie's
order must not change between requests. BM25 scores tie exactly for
duplicate text (the author's library has 948 chunk texts duplicated across
302 searchable documents), so BM25's query orders by `bm25_score, c.seq`;
fusion and the document sort already break ties by ID. The vector leg
can't take a second key, since sqlite-vec refuses one ("Only a single
'ORDER BY distance' clause is allowed on vec0 KNN queries"), and its
distances tie only for bit-identical embeddings.

**The dashboard's pages:**

- `?page=` is read only with a query: absent or empty is page 1, and
  anything but a whole number from 1 to 10 is a 400 page that runs no
  search (`searchPageParam`: the Interests' rule, capped at the last page
  any search can have).
- The pager is the Interests' (`ui.Pager` from `newPager(pageSpan)`, the
  `pager` partial): a summary ("Results 11–20 of 37"), Previous, the first
  and last pages and those beside the current one, a gap for two or more
  pages left out, and Next. Its links are `searchHref(q, type, page)`, the
  first page naming none, and the type tabs lead to page 1.
- A page past the last (1 ≤ page ≤ 10 with offset ≥ total > 0) is the
  Interests' out-of-range card ("No page 5", how many pages there are, and
  the first and last) under the head that counts the matches, and no
  pager. It answers 200, where the Interests' answers 404: a search page's
  status is its search's (a failed search answers the search's error), and
  this search answered, as `POST /v1/search` answers an offset past
  `total` with an empty window. Nothing matched is still "Nothing in your
  library matches".
- htmx boosts the pager: a wrapper swaps `#results` with the box's
  `hx-target` and `hx-select`, scrolls the results' top into view below
  the sticky header (`show:#results:top`), and pushes the page's URL; the
  links work as they are without JavaScript. The box and the pager share
  one `hx-sync` on `.search-page`: a keystroke `replace`s (it aborts a
  page in flight), a page `drop`s (asked for during a keystroke's search,
  it is dropped), so an old query's page never lands under a new one.
  Previous and Next have ids in the shared partial (`pager-prev`,
  `pager-next`), so htmx gives focus back to the one used; the Interests'
  pages, which aren't boosted, carry them unused. The form holds no page:
  typing starts at page 1.
- Show scores: each passage's bm25 and vector scores and each result's
  fused score sit in `.score` spans, which show only while the
  `#show-scores` checkbox is checked (`body:has(#show-scores:checked)`,
  no script). The checkbox is inside `#results`, which every keystroke and
  page replaces; `hx-preserve` keeps it, and its state, across both swaps
  (htmx 2.0.11 applies it after `hx-select`). A full load starts it
  unchecked. Each result says how it matched without the toggle: "keyword
  + meaning", "keyword only" or "meaning only" (`SearchHit.MatchKind`).
- Passages without markdown. Chunks are stored markdown with no newlines,
  so even block syntax is inline: of 1,160 real snippets from the queries
  above, 436 hold a link's `](`, 210 a heading, 153 bold, 79 an image, and
  62 start inside a link's destination. `ui.Passage` drops the markup and
  keeps the text it wraps: a link's or image's text without its
  destination (any scheme, balanced parentheses, cut anywhere by the
  snippet), emphasis, code and strikethrough runs (not between two letters
  or digits: `snake_case`, `5*3`), flattened heading and quote markers
  (not `x > 5` or `->`), table rules, and the tags of a fixed list of HTML
  elements (other angle brackets stay text), escapes taken literally.
  Whitespace collapses, FTS5's ellipses stay at the ends, and two marks
  apart by at most 3 runes of spaces and hyphens merge ("single-table
  design" is one mark). A passage without a snippet, or whose snippet is
  markup alone, is the start of its chunk's cleaned text, 300 runes. Each
  rule is one pass that never goes back, so cleaning is linear in the
  text, tested on adversarial texts 100 times a chunk's size. The result
  is plain strings the template escapes, never a trusted type; the
  document's page shows the whole text formatted.
- An untitled result, and an untitled related document on a document's
  page, is named as the Library names it: its bookmark's title in italics
  (`.result-title a.from-bookmark`, in the title's own font), or its short
  address in monospace, with the address above it as before. The API
  reads a response's untitled hits, only when there are any, with the
  interests' batched `GetByIDsWithLastError` (the IDs as one JSON array,
  each document by its key, `+d.tenant_id`), and a failed read fails the
  request, as a failed markdown path does ("API: absolute content paths,
  and hydration errors fail the request"). A lighter read of the bookmark
  title alone was tried first and dropped: written `d.tenant_id = ? AND
  d.id IN (…)`, SQLite planned it on the test database as a seek of each
  document's key for three IDs, which its pin used, and from four on as a
  walk of `idx_documents_tenant_state_updated` through every document the
  tenant has, the trap "Interests page by offset within a run" describes.
  A page has at most 10 hits, `k` at most 100, and most hits are titled
  (50 of the 4,498 fetched documents aren't), so the Library row's extra
  columns cost next to nothing.

**Checked in a browser.** Headless Chrome 154 against this build on the
copy above (the queue paused), and against the real templates rendered
with hostile view models (a 300-character title, a 700-character
unbroken address as an untitled result, markup and quotes in titles,
bookmark titles and snippets), under the real CSP:

- Page 1, a middle page (5), the capped last page with its note, a result
  named by its bookmark, a keyword-only page (a daemon whose
  `embedding.base_url` is a closed port), a page past the last there (a
  200 with the out-of-range card under "20 documents match") and the
  hostile pages, at 1440 and 390 px, light and dark: no CSP violation, no script error, and no
  sideways scroll at 390, where the pager reads "Previous · Page 5 of 10
  · Next". The only console entry was Chrome's own request for
  `/favicon.ico` on an origin's first load, a 404.
- Next swapped `#results` without a page load, pushed `?page=2`, and left
  the results' top 15 px below the sticky header. Back loaded page 1
  afresh. Enter on a focused Next paged, and focus stayed on Next.
- A new query from page 2 went to page 1, with no page in its URL, and
  Show scores stayed checked across the keystroke and across paging; a
  type tab loaded page 1 with it unchecked.
- A page clicked while a keystroke's search was in flight was dropped,
  and a keystroke right after a page click won: the query typed and its
  first page.

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
