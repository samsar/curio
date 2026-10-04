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
- 2026-07-06 — [Insight clustering quality: the "general-reading" mega-cluster (known limitation)](#insight-clustering-quality-the-general-reading-mega-cluster-known-limitation) (revised)
- 2026-09-09 — [Host-cache hits are permanent failures](#host-cache-hits-are-permanent-failures) (revised)
- 2026-09-24 — [Local API: loopback only, no token, browsers shut out](#local-api-loopback-only-no-token-browsers-shut-out) (revised)
- 2026-09-24 — [Single daemon per home: flock on daemon.pid, bind before touching the DB](#single-daemon-per-home-flock-on-daemonpid-bind-before-touching-the-db) (revised)
- 2026-09-24 — [Interrupted vs. orphaned jobs](#interrupted-vs-orphaned-jobs) (revised)
- 2026-09-24 — [Config: strict keys, legacy `workers` folded in at load](#config-strict-keys-legacy-workers-folded-in-at-load) (revised)
- 2026-09-24 — [Refetch: state reset and fetch job in one transaction](#refetch-state-reset-and-fetch-job-in-one-transaction)
- 2026-09-24 — [Migrations: rebuilding a table other tables reference](#migrations-rebuilding-a-table-other-tables-reference)
- 2026-09-24 — [Fetcher errors: one typed status model](#fetcher-errors-one-typed-status-model)
- 2026-09-24 — [GitHub: secondary rate limits and a shared cooldown](#github-secondary-rate-limits-and-a-shared-cooldown) (revised)
- 2026-09-24 — [Fetchers: one cap on every response body](#fetchers-one-cap-on-every-response-body)
- 2026-09-24 — [Host cache: only host-wide verdicts, under the host that gave them](#host-cache-only-host-wide-verdicts-under-the-host-that-gave-them) (revised)
- 2026-09-24 — [Login-wall heuristic: www and apex are the same site](#login-wall-heuristic-www-and-apex-are-the-same-site) (revised)
- 2026-09-24 — [Fetch politeness: shared Jina pacing, per-host origin gate](#fetch-politeness-shared-jina-pacing-per-host-origin-gate) (revised)
- 2026-09-24 — [URL normalization: fetch-equivalent and idempotent](#url-normalization-fetch-equivalent-and-idempotent)
- 2026-09-24 — [Subprocess fetchers: kill the process group, cap the output](#subprocess-fetchers-kill-the-process-group-cap-the-output)
- 2026-09-24 — [Chrome profiles carry their own User-Agent and sec-ch-ua](#chrome-profiles-carry-their-own-user-agent-and-sec-ch-ua) (revised)
- 2026-09-24 — [Store boundary: consumers see interfaces, depguard enforces it](#store-boundary-consumers-see-interfaces-depguard-enforces-it) (revised)
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
- 2026-09-26 — [YouTube: a shared cooldown after a 429](#youtube-a-shared-cooldown-after-a-429) (revised)
- 2026-09-26 — [Page verdicts: one judge for every page, bot challenges included](#page-verdicts-one-judge-for-every-page-bot-challenges-included) (revised)
- 2026-09-26 — [Jina answers are judged like the origin's pages](#jina-answers-are-judged-like-the-origins-pages) (revised)
- 2026-09-26 — [Search leaves out failed and dead documents](#search-leaves-out-failed-and-dead-documents)
- 2026-09-27 — [Cross-site redirects: judged where they land](#cross-site-redirects-judged-where-they-land)
- 2026-09-27 — [Error pages whose status is hidden](#error-pages-whose-status-is-hidden)
- 2026-09-27 — [Jina requests identify as curio](#jina-requests-identify-as-curio) (revised)
- 2026-09-27 — [Fetch upstream health: Jina's calls are tracked and reported](#fetch-upstream-health-jinas-calls-are-tracked-and-reported) (revised)
- 2026-09-27 — [Jina refusing a target is a verdict](#jina-refusing-a-target-is-a-verdict) (revised)
- 2026-09-27 — [Queue gate: pause, throttle and schedule, persisted in SQLite](#queue-gate-pause-throttle-and-schedule-persisted-in-sqlite)
- 2026-09-27 — [Embedding model and per-home width](#embedding-model-and-per-home-width)
- 2026-09-27 — [Embedding drift: the marker records the build, healthz reports a change](#embedding-drift-the-marker-records-the-build-healthz-reports-a-change) (revised)
- 2026-09-27 — [Embeddings never truncate; an over-long chunk fails at once](#embeddings-never-truncate-an-over-long-chunk-fails-at-once)
- 2026-09-27 — [sqlite-vec: NEON distance kernels on arm64](#sqlite-vec-neon-distance-kernels-on-arm64)
- 2026-09-27 — [Daemon lifecycle: a per-user launchd agent](#daemon-lifecycle-a-per-user-launchd-agent) (revised)
- 2026-09-27 — [Keep-awake: caffeinate on AC power while the workers have queued work](#keep-awake-caffeinate-on-ac-power-while-the-workers-have-queued-work)
- 2026-09-27 — [curio up: a plan-first setup wizard](#curio-up-a-plan-first-setup-wizard) (revised)
- 2026-09-28 — [curio up: the import step](#curio-up-the-import-step)
- 2026-09-28 — [Dashboard: server-rendered pages in the daemon (phase 1)](#dashboard-server-rendered-pages-in-the-daemon-phase-1) (revised)
- 2026-09-28 — [Dashboard: formatting budgets for stored markdown](#dashboard-formatting-budgets-for-stored-markdown)
- 2026-09-28 — [Commands take a document's URL as well as its ID](#commands-take-a-documents-url-as-well-as-its-id)
- 2026-09-28 — [Doctor warns when GitHub requests carry no token](#doctor-warns-when-github-requests-carry-no-token) (revised)
- 2026-09-28 — [Failure causes: recorded when a document fails](#failure-causes-recorded-when-a-document-fails) (revised)
- 2026-09-29 — [Dashboard: a design language under the CSP](#dashboard-a-design-language-under-the-csp) (revised)
- 2026-09-29 — [Dashboard: search is home, the Overview becomes Status](#dashboard-search-is-home-the-overview-becomes-status) (revised)
- 2026-09-29 — [Dashboard: actions through /v1, sent by a first-party module](#dashboard-actions-through-v1-sent-by-a-first-party-module) (revised)
- 2026-09-29 — [Library: a Date saved order lists saves](#library-a-date-saved-order-lists-saves) (revised)
- 2026-09-29 — [Dashboard: the Failures tab](#dashboard-the-failures-tab)
- 2026-09-29 — [Interests page by offset within a run](#interests-page-by-offset-within-a-run) (revised)
- 2026-09-29 — [Search pages by offset within a fixed-depth pool](#search-pages-by-offset-within-a-fixed-depth-pool)
- 2026-09-30 — [Waiting is not failing: jobs curio didn't send are deferred](#waiting-is-not-failing-jobs-curio-didnt-send-are-deferred) (revised)
- 2026-09-30 — [A site's block is not its pages' verdict](#a-sites-block-is-not-its-pages-verdict)
- 2026-09-30 — [Embedding drift: verified by re-embedding a sample](#embedding-drift-verified-by-re-embedding-a-sample) (revised)
- 2026-10-02 — [Interests: corrections that teach the grouping (deferred)](#interests-corrections-that-teach-the-grouping-deferred) (revised)
- 2026-10-02 — [Soft-404 titles: whole templates, not phrases](#soft-404-titles-whole-templates-not-phrases)
- 2026-10-03 — [Louvain: ours, warm-started; gonum as a test oracle](#louvain-ours-warm-started-gonum-as-a-test-oracle) (revised)
- 2026-10-03 — [Interests: two levels, stable identities, automatic rebuilds](#interests-two-levels-stable-identities-automatic-rebuilds) (revised)
- 2026-10-03 — [Dashboard: two-level interests](#dashboard-two-level-interests)
- 2026-10-03 — [Page text: not-found notices, parked domains and sign-in forms](#page-text-not-found-notices-parked-domains-and-sign-in-forms)
- 2026-10-04 — [Interest map: two views of each regrouping, drawn when it is built](#interest-map-two-views-of-each-regrouping-drawn-when-it-is-built)
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

**Revised (2026-10-02):** the not-found title rule is a whole template,
anchored at both ends with explicit word lists, no longer a phrase found
anywhere in the title, and it reads the title whether or not an article
was found. See "Soft-404 titles: whole templates, not phrases".

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


**Revised (2026-10-03):** the engine no longer runs `KNNGraphClusterer`.
It groups with `LouvainGrouper` behind `insight.Grouper`, in two levels
when the library is large enough, warm-started from the previous run, and
carries identities across rebuilds (see "Interests: two levels, stable
identities, automatic rebuilds"). `KNNGraphClusterer` stays as the
quality harness's baseline, usable through `FlatGrouper`, and its knobs
(`insight.knn`, `min_similarity`, `min_cluster_size`) are ignored. The
noise bucket is now the unsorted (`num_unsorted`, `GET
/v1/interests/unsorted`), and one surface remains, `/v1/interests`, with
`/unsorted` and `/changes` beside it.

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

**Revised (2026-10-03):** by the interests rework the problem had become
fragmentation, not one blob. On a copy of the owner's library embedded
with qwen3-embedding (5,254 documents), `KNNGraphClusterer` at k 10, 0.5
and minimum 3 made 325 interests, a third of them (34.5%) with 4 members
or fewer, and left 30% of the documents in none. Two levels resolved it:
30 areas holding 187 interests, 92% of the documents in an interest and
8% unsorted (402, besides 3 loose fits). Both sides come from `make
cluster-report` on one copy, the old clusterer as its baseline reproducing
the stored run's 325 interests and 70.2% coverage (see "Interests: two
levels, stable identities, automatic rebuilds", "Acceptance on a copy of
the owner's library (PR 5)").

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

**Revised (2026-09-30):** an anti-bot or login-wall hit no longer fails
the page. It skips the origin only: with Jina on, the page goes to Jina,
whose verdict on that page decides it; with Jina off, it waits for the
entry to expire (a `DeferError`), without using up an attempt. Only an
unreachable hit is still a `PermanentError` from the cache. The entry now
quotes the origin's own answer, not the fallback's. See "A site's block is
not its pages' verdict".

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

**Revised (2026-09-30):** a third way back to the queue. A handler that
didn't run its job, held back by a cooldown curio keeps itself, returns a
`jobs.DeferError`, and the worker puts the job back with `JobQueue.Defer`:
pending until the hold ends, the attempt refunded, what it waits for as its
`last_error`, for up to a day from the job's creation. A deferral during
shutdown is an interruption like any other (`Requeue`), and `ErrPermanent`
in its chain wins over it. `Defer` moves only a `running` row, like the
other transitions, and `MarkDone` now clears `last_error`. See "Waiting is
not failing: jobs curio didn't send are deferred".

**Revised (2026-10-03):** a `cluster` job, a rebuild of the interests, gets
one attempt. Every rebuild error is permanent, and the engine counts it as
a failed rebuild in `insight_state`, which the interest scheduler retries
after its own backoff (15 minutes doubling to 4 hours); the queue's five
attempts 30 seconds apart would re-read every vector four more times
within minutes. An interrupted rebuild is still requeued with its attempt
refunded, so a cluster job claimed a second time can only be an orphan:
its handler records the rebuild as failed (`Rebuilder.Abandoned`: the runs
it left running fail, and one failure counts) and runs nothing, since what
killed the daemon would kill it again at every launchd restart, each one
costing every running fetch and index job an attempt. The cluster pool's
permanent-failure hook does the same for an orphan `RecoverOrphans` fails
for good. See "Interests: two levels, stable identities, automatic
rebuilds".

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


**Revised (2026-10-03):** `insight.knn`, `insight.min_similarity` and
`insight.min_cluster_size` no longer do anything. The strict decoder still
accepts them with any value of their type, `Default()` and `Validate`
ignore them, and `Load` records which the file sets, by presence as for
`daemon.workers` (`Config.DeprecatedKeys`); the daemon logs one WARN at
start naming them, with the fix: remove them. See "Interests: two levels,
stable identities, automatic rebuilds".

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

**Revised (2026-09-30):** a call the cooldown holds past the 2-minute cap
no longer fails the fetch: `awaitTurn` returns a `fetcher.DeferError`
until the cooldown ends, around the same retryable 429, and the job waits
for the reset with its attempt refunded, for up to a day. The call whose
answer carried the long `Retry-After` still fails its attempt, since GitHub
answered it. A delay from `X-RateLimit-Reset` is clamped at 24 hours, as a
`Retry-After` is. See "Waiting is not failing: jobs curio didn't send are
deferred".

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

**Revised (2026-09-30):** which verdicts are cached, and under which host,
is unchanged; what a hit does changed. A page on a host with a fresh
anti-bot or login-wall entry goes to Jina without asking the origin, and
Jina's verdict about it is final; with Jina off, it waits for the entry
to expire. A redirect onto such a host does the same, carrying the page's
own origin error; an unreachable entry still fails it from the cache.
Neither ever writes or refreshes an entry. The entry stores `originErr`'s
text, so `(cached: …)` quotes what the origin answered. The cost: while an
entry is fresh, a routed page's dead link that redirects to a homepage or
a landing page is caught only by its title. See "A site's block is not its
pages' verdict".

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

**Revised (2026-09-30):** the queue can take a delay now. A Jina call the
cooldown holds past 30 s (a long 429, a CDN challenge's pause) is a
`fetcher.DeferError` until the cooldown ends; the fetch is deferred with
its attempt refunded and runs again, whole, when the pause is over. The
origin's error still leads the cause, and the host cache is still not
written. See "Waiting is not failing: jobs curio didn't send are
deferred".

**Revised (2026-09-30):** Jina requests are also paced per site, the
registrable domain: 6 a minute by default
(`fetcher.native.jina_site_requests_per_minute`), turns taken before the
shared limiter, waited for up to 30 s and deferred beyond, and a
send-time guard that spaces a site's sends again after the limiter's
queue. The host cache checked once the slot is acquired now sends the
page to Jina, releasing the slot first. See "A site's block is not its
pages' verdict".

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

**Revised (2026-10-03):** `cmd/clusterreport` imports
`internal/store/sqlite` outside tests too, through its own exception in
`store-boundary` (`!**/cmd/clusterreport/**`): a developer's report that
migrates and reads a copy of a home's database, which `make build` and
goreleaser never build (see "Interests: two levels, stable identities,
automatic rebuilds", "The cluster report (PR 5)").

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

**Revised (2026-10-04):** migration 018's map columns add no index.
`RunGroups` now seeks `idx_interest_groups_list` on `run_id` where it took
the primary key's index: the old statement plans the same way on 018's
table and as before on 017's, so it is the wider table, not the query,
that moved SQLite's choice. Either seeks the run's groups alone, and the
pin names the new one. `MapDocuments` (a run's assignments, then its
placements, each from its primary key in document ID order, each document
by its primary key, an untitled one's bookmark title by
`idx_bookmarks_document`) and `MapPositions` (a JSON array of IDs against
both primary keys) are pinned, and refuse a walk of the tenant's documents.
A document's delete still seeks `idx_interest_assignments_document` and
`idx_interest_placements_document`. See "Interest map: two views of each
regrouping, drawn when it is built".

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

**Revised (2026-09-30):** the attempt arithmetic above is gone. A fetch
that meets more than 30 s of cooldown, before its yt-dlp slot or after, is
a `fetcher.DeferError` until the cooldown ends: the job waits for it with
the attempt refunded, and runs once it has passed. Only a run that met a
429 itself spends an attempt. See "Waiting is not failing: jobs curio
didn't send are deferred".

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

**Revised (2026-10-03):** the page's text is read too (see "Page text:
not-found notices, parked domains and sign-in forms"). The order is now:

1. dead link, with detection on: the URL rules, a not-found title, then a
   not-found notice or a parked domain's in the page's opening;
2. bot challenge, whose phrases now include Google's unusual-traffic page
   and Fastly's client challenge;
3. error page, as before;
4. login wall by redirect, as before;
5. no article, as before;
6. thin, as before;
7. a login-page title, as before;
8. a sign-in form: a short page whose text opens with a password field
   and a sign-in line.

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

**Revised (2026-10-03):** a parked domain's page is a dead link by its
text (`text reads like a parked domain: …`), final and uncached like any
dead link a Jina answer gives, whatever its size; before, it was rejected
only when under 500 bytes, and then as a thin page. A not-found notice
under an ordinary title, Google's unusual-traffic page and a page that is
only a sign-in form are judged by their text too, which Jina's link URLs
had carried past the thin floor. See "Page text: not-found notices,
parked domains and sign-in forms".

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

**Revised (2026-09-30):** the pause holds the queue's retries back now. A
Jina-bound fetch inside it still makes its origin request, then gets a
`fetcher.DeferError` from `awaitJina` instead of a retryable failure: the
job waits for the pause to end with its attempt refunded, for up to a day
from its creation, and runs again whole. So a document no longer ends
`failed` before Jina answers again, unless the pauses outlast its day; the
one whose request was challenged still fails that attempt. See "Waiting is
not failing: jobs curio didn't send are deferred".

**Revised (2026-09-30):** the narrowed 403 bullet's example is no
refusal any more: an `AbuseAlleviationError` is Jina's block of a site for
now (`errJinaSiteBlocked`), whatever its status, recognized before the
refusal rule, and it holds the site's pages until it ends. A 403 whose
reason names the target's host and is no abuse block is still a refusal.
See "A site's block is not its pages' verdict".

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

**Revised (2026-09-30):** a call the cooldown holds past 30 s no longer
fails fast: `awaitJina` returns a `fetcher.DeferError` and the job waits
for the pause (see "Waiting is not failing: jobs curio didn't send are
deferred"). It is still a call never sent, and records nothing.

**Revised (2026-09-30):** `rate_limited` also counts Jina's block of a
site (an `AbuseAlleviationError`, whatever its status), which was a
healthy `refused`; a page held for a site's turn or block records
nothing. Healthz's upstreams carry `site_pauses`, the blocks in effect,
which `curio doctor`'s `jina` check lists; a block leaves the state alone.
See "A site's block is not its pages' verdict".

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

**Revised (2026-09-30):** the abuse block is no refusal any more. An
`AbuseAlleviationError` is `errJinaSiteBlocked`, recognized first: the
site's pages wait for the end it names instead of failing on it, it caches
nothing, and it is counted `rate_limited` (as is a page still held when
its day of waiting runs out). Both items under "Not done" are done:
Jina requests are paced per site, and a site's block is remembered until
it ends. And behind a host-wide origin verdict, the retry of a refused
document no longer fails from the cache: it asks Jina without the origin,
and a refusal of that page is final, `jina_refused`. See "A site's block
is not its pages' verdict".

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

**Revised (2026-09-30):** a changed fingerprint is no longer reported as it
is. The monitor first re-embeds a sample of the library under the build
serving now: a sample that matches the stored vectors records the new
build with no report, and only one that doesn't, or that can't be checked,
reaches `embedding_drift`, which now carries that evidence
(`verification`). Upgrading Ollama from 0.34.4 to 0.35.0 was reported
while every sampled chunk came back bit for bit. See "Embedding drift:
verified by re-embedding a sample".

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

**Revised (2026-09-30):** without a token, github.com pages now wait for
GitHub's hourly limit instead of failing rate-limited (see "Waiting is not
failing: jobs curio didn't send are deferred"). The check's detail says so,
about 30 repositories an hour, and `curio up`'s note says how long its
github.com pages may wait, and that the ones still waiting after a day fail
as `rate_limited`, which `curio refetch --all --cause=rate_limited`
retries.

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

**Revised (2026-09-30):** a call a shared cooldown holds back no longer
fails its job: the job waits for the hold, for up to a day from its
creation (see "Waiting is not failing: jobs curio didn't send are
deferred"). A job still held after that fails with the causes above: a
GitHub or YouTube fetch `rate_limited`, from the 429 its error carries; a
Jina-bound one the origin's cause, since a held Jina call is Jina's own
trouble.

**Revised (2026-09-30):** after a host-wide origin verdict, a document no
longer records the cached verdict: its retry, and every other page of the
host while the entry is fresh, asks Jina without the origin and records
Jina's verdict about that page (the cached verdict's cause only when Jina
has trouble of its own). Jina's `AbuseAlleviationError` is no longer
`jina_refused`: it defers the site's pages until the block ends, and a
document still held after its day records `rate_limited`, which now
covers Jina Reader too; `jina_refused` is left with opt-outs and
deterministic 4xx. See "A site's block is not its pages' verdict".

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

**Revised (2026-10-03):** the Interests' poll reads the queue (whether a
rebuild is queued or running), the latest done run (newer interests to
offer as a reload) and the interest scheduler's snapshot (held, failing,
due, current, none), never the newest run of any status: a failure is the
one `insight_state` counts, so a rebuild that failed before creating a run
is reported, and a run a shutdown cancelled isn't. The full page no longer
reads a run for the rebuild at all. The poller polls every 2 seconds while
a rebuild is in flight and every 30 seconds otherwise, so a rebuild the
scheduler queues reaches an open page. See "Dashboard: two-level
interests".

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


**Revised (2026-10-03):** interests now have identities that outlive
runs (see "Interests: two levels, stable identities, automatic rebuilds").
Offset paging stays exact for the same reason as before: a run's groups
and assignments are written once, in one transaction (`CommitRun`), and
never change. What changed:

- An interest keeps its ID across rebuilds while it keeps most of its
  documents, so the next page of an interest's members after a rebuild is
  the same interest's. The 404 that said "interests get new IDs each time
  they are rebuilt" is gone. A retired ID (split, merged or dissolved) is
  a 410, `urn:curio:problem:interest-retired`, naming its successors (in
  the dashboard, a 410 page saying what became of it); an ID nothing knows,
  such as one from before migration 016, is a 404 saying interests were
  regrouped when curio was upgraded.
- The pages are of the run's top-level groups (areas, or interests in a
  library too small for areas) by `size DESC, cohesion DESC, interest_id`,
  and an interest's members, then its loose fits, by `similarity DESC,
  document_id`. `num_clusters` is now `total`. Migration 016's covering
  indexes (`idx_interest_groups_list`, `idx_interest_assignments_list`)
  serve both orders without a sort, so "No new index" no longer holds.
- Documents placed between rebuilds are listed apart (`new`,
  `new_members`), never inside the offset-paged lists, which they would
  shift; the index step and the sweep after each rebuild write them.
- The re-read rules are unchanged, plus one: a group missing from the run
  that was read is looked up in the newest done run, then as an identity,
  a 410 if it was retired meanwhile.

**Revised (2026-10-03):** two more of the dashboard's lists page by offset
within a run: an area's interests, 24 a page, cut from the one read of
them in Go (`GET /v1/interests/{area}` still lists every one), and
Unsorted, 50 a page nearest first, whose links name the run as the
Interests' do; past the last, each is a 404 that keeps the head. A retired
ID's page is a 410 in the Interests' frame naming its successors, and an
unknown ID's 404 no longer says the interests were regrouped by an
upgrade: it may as well name one retired more than 180 days ago, which
`PruneRetired` deleted. See "Dashboard: two-level interests".

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

- Page 1, a middle page (5), the capped last page with its note, a
  result named by its bookmark, a keyword-only page (a daemon whose
  `embedding.base_url` is a closed port), a page past the last there (a
  200 with the out-of-range card under "20 documents match") and the
  hostile pages, at 1440 and 390 px, light and dark: no CSP violation,
  no script error, and no sideways scroll at 390, where the pager reads
  "Previous · Page 5 of 10 · Next". The only console entry was Chrome's
  own request for `/favicon.ico` on an origin's first load, a 404.
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

## Waiting is not failing: jobs curio didn't send are deferred

**Decision:** a fetch that curio held back itself doesn't fail. The job
waits for the hold, with its attempt refunded, for up to a day.

- **Three sources.** Each shared cooldown is sat out inline up to a cap,
  and a longer one used to fail the fetch with a retryable 429 and no
  request. Now each returns a `fetcher.DeferError{Until, Reason, Err}`:
  `Until` is the cooldown's end, `Reason` what the fetch waits for, and
  `Err` the error it used to fail with, text and chain unchanged
  (`heldBack`, `internal/fetcher/errors.go`).
  - GitHub's `awaitTurn` (2-minute cap), for any call of a fetch: a README
    call held after the repository's answered defers the whole fetch.
  - YouTube's `awaitCooldown` (30 s), before queueing for a yt-dlp slot
    and again once it holds one; the slot is released.
  - Native's `awaitJina` (30 s), whether or not the fetch has called Jina
    already. `Native.Fetch` returns it inside `jinaFallbackError`
    unchanged, so the origin's sentinel and cause still speak, the host
    cache isn't written, and Jina's health counts no call.

  A `DeferError` is never a `PermanentError`, and no fetcher wraps it in
  one. The limiter's wait stays inline: it is a steady pace, and its
  callers carry no deadline. An answer to a request that was sent is never
  itself a deferral: a GitHub 429 or rate-limit 403 with a long
  `Retry-After`, a yt-dlp run that met `HTTP Error 429` and a Jina CDN
  challenge each fail their attempt. Jina's own 429 differs only because
  `tryJina` retries it inside the fetch, after the cooldown it set: when
  that cooldown outlasts 30 s, the retry is held and the fetch defers
  (unless the 429 answered the fetch's last Jina try, which fails).
  GitHub's `X-RateLimit-Reset` delay is clamped at 24 hours, as
  `Retry-After` already was: it becomes the cooldown, and so the time a
  deferred fetch waits for.
- **The fetch handler** turns a `fetcher.DeferError` anywhere in `Fetch`'s
  error into a `jobs.DeferError` around `fetch failed: …`, keeping `Until`
  and `Reason`, after checking for a `PermanentError` first. Nothing is
  written: no markdown, extraction, document change or index job.
- **The worker** records a handled job's outcome by the first of:
  1. no error: `MarkDone`;
  2. the worker's context is done: `Requeue`, a deferral included (runnable
     now, attempt refunded, the earlier `last_error` kept);
  3. `ErrPermanent` in the chain: failed for good, even around a deferral;
  4. a `jobs.DeferError` while `now - created_at` is under 24 hours
     (`deferralBudget`): `JobQueue.Defer`, with `run_after` its `Until`
     clamped to [now + 1 s, now + 24 h], `last_error` `waiting for
     <Reason>`, one `job deferred` log line, and no permanent-failure hook;
  5. anything else, a deferral past the budget included: `MarkFailed` as
     before. The attempt counts, `last_error` is `Err`'s text, and at the
     last attempt the hook runs with it. A held GitHub or YouTube fetch
     fails `rate_limited`, from the 429 in the chain; a held Jina-bound one
     keeps the origin's cause (`anti_bot`, `login_wall`, or `unsupported`
     for a PDF curio couldn't read), since Jina's own trouble never speaks
     for the target (`speaker`, `internal/fetcher/cause.go`).

  `Until` is not clamped to the budget's end, so a job deferred at 23
  hours still waits for its hold and then gets one real try.
- **`JobQueue.Defer`** is one status-guarded UPDATE, like `Requeue`: a
  `running` row goes `pending` with `run_after` set, `attempts` less one
  (never below 0), `last_error` the reason, `started_at` cleared. It
  signals nothing: the job isn't runnable before its time, and idle
  workers poll at least every 5 s, as they do for a retry's backoff.
  `MarkDone` now clears `last_error`: a done job has nothing left to
  explain, and every GitHub fetch that waited once would otherwise read
  "waiting for …" in `curio jobs`' default view of done jobs.
- **Native redoes the whole fetch** at `Until`, origin request included.
  Keeping the origin's answer would be per-URL state that a restart loses
  and that goes stale; the request is cheap and gated at 2 per host, and a
  block that has lifted may let the origin serve the page.
- **Surfaces.** The rule is "a pending job whose `run_after` is still ahead
  waits, and its `last_error` says why": the error a retry retries after,
  or what a deferral waits for. No marker says which.
  - A document's page says the job is waiting, when it is due (relative,
    the exact time on hover), its attempt once it has used one, and why, on
    a line of its own, whole on hover. A due pending job reads "queued".
  - `curio jobs` labels a failed job's `last_error` `err:`, any other's
    `last:`.
  - `QueueCounts` also counts, per kind, the pending jobs due later and
    the earliest `run_after` among them, in the same covering walk of
    `idx_jobs_claim`. `/v1/queue`'s kinds carry them as `due_later` and
    `next_due`; `pending` still counts every pending job.
  - Status's estimate divides only the work due now by the window's
    finishes. A new `waiting` state, the queue open with nothing due or
    running but jobs due later, says how many and when the first is due,
    where it used to say `stalled`; the running and stalled states add how
    many more are due later. The queue card's state line says the same
    instead of "Working" when every pending job is due later and none
    runs.
  - `curio status` adds `(N due later)` to a pool's load; `--follow`
    computes its ETA over the jobs due now, and prints how many are due
    later, and when the first is, instead of `rate≈0.0/s eta≈0s` when
    nothing else is queued.
  - `curio doctor`'s `github` check and `curio up`'s GitHub note say pages
    wait for GitHub's hourly limit, not that they fail.

**Why:** an import of the author's library ended with 2,150 failed
documents, 222 of them `rate_limited`, and 211 of those never sent a
request: 171 GitHub fetches and 40 YouTube fetches that met curio's own
cooldown. The queue retries a failed attempt after 60, 120, 240 and 480 s
(`retryBackoff` of the attempts the claim already counted), so five
attempts span about 15 minutes, while GitHub's primary limit resets up to
an hour after it trips: the last attempts still read `cooldown has 24m30s
left`. Jina's held calls failed the same way under the origin's cause, so
they hid in the `anti_bot` and `login_wall` counts. Every one of those
failures was curio's own hold, which only waiting could clear.

**What counts as an attempt:** a claim whose outcome an upstream decided.
A claim that ended on curio's own hold is refunded, whatever it sent
before it met the hold: a Jina 429 whose `Retry-After` outlasts the cap is
an answer, and the next Jina call of the same fetch is then held. An
interrupted claim is refunded; an orphaned one is kept ("Interrupted vs.
orphaned jobs").

**Why a day, counted from `created_at`:** 24 of GitHub's hourly resets,
time for about 700 repositories at 2 calls each without a token, and the
longest `Retry-After` the fetchers honor. An upstream that holds curio off
longer won't serve it: a visible failure that `curio refetch` retries
beats a document pending for days. `created_at` is already in the table,
so there is no migration, and a refetch enqueues a new job with a new
budget.

**Why `last_error` for the reason:** the owner's choice, and no migration.
A pending job's `last_error` already explained a retry; a deferral's
reads the same way, and `MarkDone` clears it once there is nothing left to
explain. A column would have said "deferred" apart from "retrying", which
no surface needs: both wait for their `run_after`.

**Tradeoffs:**

- The budget counts from creation, so a job that sat a day in a paused
  queue, or outside its schedule, gets no deferrals: its first held claim
  fails the attempt.
- Each held Jina-bound document requests its origin again once per hold,
  30 s at the least and 10 minutes for a CDN challenge, for up to its day,
  until anti-bot host-cache hits go straight to Jina.
- The jobs a hold releases come due together. The upstream's own pacing
  spreads them: GitHub's limiter (1.5 calls a second), yt-dlp's 2 slots,
  Jina's 20 a minute. At each GitHub reset the call that meets the limit
  again spends its job's attempt, since GitHub answered it, and the rest
  are deferred to the next reset.
- Keep-awake counts every pending job, those due later included: they are
  work the queue will run, so a Mac on AC power stays awake while GitHub
  pages wait for resets.
- `MarkFailed` still ignores `Retry-After` on answers that were sent. The
  cooldown those answers extend turns the next claim that meets it into a
  deferral, so the attempt it spends is only the one that got the answer.
- A GitHub fetch whose second call is held spends its first call again on
  its next run.
- The reason lives in `last_error` rather than a column of its own, so
  only its text tells a deferral from a retry. A surface that must tell
  them apart, rather than show why a job waits, would need the column.

**Recovering old failures:** documents that failed `rate_limited` before
this change stay failed. Set `fetcher.github.token` first, then `curio
refetch --all --cause=rate_limited`, or Failures → Rate limited → Refetch
on the dashboard. Jina-bound documents that failed on a pause are among
the `anti_bot` and `login_wall` failures, with pages that failed for
reasons of their own; `--cause=anti_bot` or `--cause=login_wall` refetches
them with the rest of their group.

**Not done here:** the host cache (a cached verdict is still a permanent
failure), per-site Jina pacing, `AbuseAlleviationError`, and `MarkFailed`
honoring `Retry-After`. Those paths can return a `fetcher.DeferError` when
they come.

**Revised (2026-09-30):** three more holds return a `fetcher.DeferError`:
a host-cache entry waited out with Jina off, a site's turn at Jina, and
Jina's block of a site. The last is an answer that was sent, and still a
deferral: it names when Jina will read the site again and decides nothing
about the page, which is the case the rule above is for. See "A site's
block is not its pages' verdict".

---

## A site's block is not its pages' verdict

**Decision:** a verdict about one site no longer fails the site's other
pages. Three changes to `Native` (`internal/fetcher`), which only work
together:

- **A cached host's pages skip the origin, not Jina** (`pastCachedHost`).
  A fresh `hostFailureCache` entry of kind anti-bot or login-wall no longer
  fails a page from the cache:
  - With Jina on, the page goes to Jina without an origin request or an
    origin slot: the entry is checked before `hostGate.acquire` and again
    after it, and the slot is released before any Jina wait or call.
    Jina's answer decides that page alone, judged as any fallback answer
    (`judgeJinaAnswer`). A good one is stored, with `host_cache`
    (`anti-bot`, `login-wall`) in its meta. A verdict about the target
    (`jinaAnswered`, `ErrDeadLink`, `ErrTooLarge`) is a `PermanentError`
    around `jinaFallbackError{jina, origin}`, whose origin side is the
    entry's error. Jina's own trouble, the target's for now, or a hold
    stays retryable or deferred.
  - With Jina off, the page is a `DeferError` until the entry expires
    (`waiting for www.nytimes.com to be tried again: it blocked curio's
    last request`), never a `PermanentError`; its error keeps the
    `(cached: …)` text and the entry's cause.
  - A page that skipped the origin never writes or refreshes the entry,
    which lives 15 minutes from the origin's answer.
  - A redirect onto a host with a fresh entry is held the same way, except
    that its `jinaFallbackError` carries the page's own origin error.
  - An unreachable entry still fails the page for good, from the cache.
  - The entry quotes the origin's own answer: `settle` stores
    `originErr.Error()`, not the fallback's composite text, so every later
    page's `(cached: …)` reads `native: HTTP 403 Forbidden: origin blocked
    the request (likely anti-bot)`. The cache reads time from the fetcher's
    injectable clock: `Get` and `Put` take `now`, and an entry carries its
    expiry.
- **Jina requests are paced per site** (`sitePacer`,
  `internal/fetcher/sitepace.go`). A site is the registrable domain
  (`siteOf`, `publicsuffix.EffectiveTLDPlusOne`: mobile.twitter.com and
  twitter.com are one site), or the host itself for an IP address, a name
  under no public suffix, or a public suffix. A site gets
  `fetcher.native.jina_site_requests_per_minute` requests a minute (default
  6, `DefaultJinaSiteRequestsPerMinute`: turns 10 s apart), key or not.
  `awaitJina` clears each request in order: the shared cooldown's
  pre-check (a held request spends no turn), the site's turn, the shared
  limiter and cooldown (`pace`, unchanged), a send-time guard, and the
  site's block once more. A turn within 30 s (`maxInlineJinaWait`) is
  waited for. A later one is a `DeferError` (`waiting for a turn at Jina
  Reader for twitter.com`), with no request, no limiter token and no
  health record. Deferred pages get Untils one interval apart, after the
  ones already handed out, so they come back spread out and find their
  turns free. In-fetch retries take turns too. The guard spaces a site's
  sends an interval apart, whatever the shared limiter's queue did, and
  its wait is sat out like the limiter's.
- **Jina's block of a site holds the site's pages instead of failing
  them.** A non-2xx Jina answer whose reason is an `AbuseAlleviationError`
  (`Anonymous access to domain mobile.twitter.com blocked until Mon Sep 28
  2026 18:48:50 GMT+0000 (Coordinated Universal Time) due to …`), with a
  403 or a 429, is `errJinaSiteBlocked`, recognized before the refusal
  rule. Its end is read from Jina's JavaScript date (or an HTTP or ISO
  one), taken as 1 hour when it can't be read (`jinaSiteBlockDefault`),
  and clamped to [1 minute, 24 hours] (`minJinaSiteBlock`,
  `maxJinaSiteBlock`, the deferral budget). The block is recorded under
  the site of the domain it names, or the target's when it names none, and
  is only ever extended. The page that received it and every later page of
  the site are `DeferError`s until it ends (`waiting for Jina Reader's
  block of twitter.com to lift`), spread one interval apart from its end.
  Later pages send nothing (`jina: not sent, Jina Reader blocks
  twitter.com until …: <Jina's reason>`); the block is checked before the
  turn and again before sending. It is never `jinaAnswered`, never cached,
  never extends the shared cooldown, and is never retried within the
  fetch. One WARN per block, when it starts. Jina's health counts the
  answer `rate_limited`; `/v1/healthz` reports the blocks in effect as
  `upstreams[].site_pauses`, and `curio doctor`'s `jina` check lists them,
  neither changing the upstream's state. A document still held when its
  day of waiting runs out records `rate_limited`.

**Why:** an import of the owner's library ended with 2,150 failed
documents. 866 of them were anti-bot host-cache hits: 810 quote Jina's
rejection of another page (its CAPTCHA warning, a status the target gave
it), 33 an `AbuseAlleviationError`, 23 another refusal. 160 of the 169
failed stackoverflow.com documents failed on their first attempt, from the
cache, without a request of their own; medium.com lost 134 documents this
way and www.nytimes.com 48. One page's Jina verdict failed the whole host
for 15 minutes, although Jina served other pages of those sites: bloomberg
49 fetched (all through Jina) against 20 failed, medium 21 against 134,
quora 11 against 32, theatlantic 8 against 28, stackoverflow 3 against 169.
And because the entry stored the composite text, 810 documents'
`last_error` names a CAPTCHA or a target status they never received.

170 more documents failed `jina_refused` on an abuse block directly
(mobile.twitter.com 117, twitter.com 35, …): 203 failed jobs on
2026-09-28 name blocks of mobile.twitter.com, twitter.com, x.com,
www.forbes.com, www.instagram.com, www.alibaba.com, stocktwits.com,
www.smithsonianmag.com, www.postman.com, www.benzinga.com and
www.investing.com. The blocks are temporary and name their end, about an
hour on: www.alibaba.com answered at 07:51:49 "until 08:51:48" and at
07:52:13 "until 08:52:12", mobile.twitter.com at 17:50:10 "until
18:48:50", www.instagram.com at 07:41:37 "until 08:38:29"; one named 2039
(www.investing.com). Taken for refusals, they failed pages for good,
cached their site's origin 403s, counted as healthy answers, and cost
every later page of the site a request to learn the block again. "Jina
refusing a target is a verdict" listed pacing per domain and remembering
blocks as not done. The cache change makes pacing necessary:
stackoverflow.com's 169 pages each need a Jina request now, where the cache
failed them with none.

**Why 6 a minute:** bloomberg took 10 Jina requests in one minute without
a block, and 6 a minute leaves the keyless 20 a minute to three sites at
once. It is not below every run that tripped a block: mobile.twitter.com
was blocked after about 80 keyless reads between 10:10 and 10:29, some 4 a
minute, so a long run of one site at 6 a minute may still trip one. The
owner's database can't narrow it down: it keeps each job's last attempt
only, and almost no Jina extractions for the blocked sites. A block now
costs the site's pages about an hour's wait, not their documents: curio
waits it out, and the value can be lowered. Jina blocks hostnames
separately (mobile.twitter.com until 18:48:50 and twitter.com until
18:46:03 in the same run), but the two were blocked within a minute of
each other both times, and a block can name www.X for a target on X, so
turns and blocks are per registrable domain.

**Why not a `rate.Limiter` per site:** a discrete-event simulation, 169
pages of one site on 16 workers with claims ordered by `run_after` as
`JobQueue` orders them. With a limiter per site, cancelling a deferred
page's reservation sends every deferred page back at the same next slot:
21.6 claims a page, each two SQLite writes and, on an uncached host, an
origin request. Keeping the reservation starves the site: the page that
comes back can't claim the slot it holds. The pacer keeps four times per
site (its last turn, the last deferral handed out, its last send and its
block's end) and the block's reason, nothing per page, and hands deferrals
distinct Untils: 1.6 to 2.0 claims a page. **Why the send-time guard:**
the shared limiter's queue can bunch one site's requests. With a 1,500-job
backlog the smallest gap fell to 3 s, and 7 or 8 requests landed in one
minute. With the guard the smallest gap is exactly 10 s, at most 6 land in
a minute, and its waits stayed under about 18 s with a 3,000-job backlog.

**Why an unreachable hit stays final:** the 243 documents that failed from
a cached unreachable entry cover 179 hosts, 172 with no such name and 7
refusing connections. A read-only DNS check on 2026-09-30 found 122
NXDOMAIN, 24 SERVFAIL, 28 names without an address record and 2 aliases to
names without one: none of the 172 resolves. Of the 7 refusing hosts, 4
are `localhost:NNNN` dev servers and 1 resolves to 127.0.0.1. Jina can't
reach them either, and deferring would only churn the queue for a day.

**Costs:**

- About one more Jina request per page of a cached site: some 800 for the
  owner's library, around 40 minutes of the keyless 20 a minute. Pages of
  a site Jina never passes still spend one each.
- A routed page's Jina answer carries no final URL, so while its host's
  entry is fresh, a dead link that redirects to the site's homepage or to
  another site's landing page is caught only by the not-found title rule.
  27 of the owner's 819 dead documents were redirect verdicts behind a 403
  or 503.
- With Jina off, a cached host's pages all come due when the entry
  expires, and a host that keeps answering 403 holds them for up to their
  day of waiting, where the cache used to fail them at once. Each time the
  entry expires, about 2 of them (the host's origin slots) ask the origin
  again, each spending an attempt, and the rest wait for the next expiry.
  They fail `anti_bot` (`login_wall` behind a login redirect) when their
  attempts or their day run out.
- Turns and blocks live in memory: a restart relearns a block with one
  request per blocked site.
- An import's pages that need Jina go at the keyless 20 a minute in all
  (about an hour for a thousand) and 6 a minute for any one site.

**Recovering old failures:** documents that failed before this change stay
failed. Refetch them from the dashboard's Failures tab ("Blocked by bot
protection", "Refused by Jina Reader"), or with `curio refetch --all
--cause=anti_bot` and `--cause=jina_refused`. The groups hold pages that
failed for reasons of their own too; those fail again, the same way.

**Not done:**

- **The dashboard.** Its Status page doesn't show the blocked sites;
  healthz and `curio doctor` do, and each held document's page says what
  it waits for.
- **Caching Jina's verdicts per site.** A site Jina never passes costs a
  request per page, as above.

---

## Embedding drift: verified by re-embedding a sample

**Decision:** a changed embedding fingerprint (model digest or Ollama
version) is reported as a drift only once a sample of the library,
re-embedded by the build serving now, shows the vectors changed, or can't
be re-embedded at all:

- **The sample** (`drift.Sampler`, over `ChunkStore.SampleChunks`): one
  chunk from each of up to 64 fetched documents. 16 are the documents
  whose longest chunk, in bytes, is longest, represented by that chunk,
  ties going to the lower `seq`; 48 are documents chosen at random among
  the rest, one random chunk each. A smaller library gives a smaller
  sample, an empty one none.
- **The requests are indexing's own.** `Indexer.EmbedChunks` is the path
  every index embed takes (the configured document prefix, batches of 32,
  in order), and the sampler re-embeds the stored texts through it, with
  the daemon's embedder: model, `truncate`, `keep_alive` and `num_ctx` are
  the index path's by construction.
- **A chunk matches** when its vector comes back bit for bit, or when its
  cosine with the stored vector is at least 0.9999 and its length is
  within sqrt(2(1 - 0.9999)) = 1.4% of the stored one's. A zero vector, a
  NaN or Inf component and another width never match, and the worst
  cosine is never NaN (it reaches healthz's JSON).
- **Verdicts.** Every chunk matches: the new build is recorded in the
  marker, with one INFO naming the change and the evidence, and nothing is
  reported. Some don't: the change is reported, with `sampled`, `changed`
  and `min_cosine`, until the build changes again or `curio reindex --all`
  rebaselines. The build can't embed the sample (a reply of another width,
  an input too long, a 4xx other than 404, 408 and 429: failures the same
  build repeats): the change is reported at once as unverified, with the
  error as its reason. Each distinct drift, verified or not, is one WARN.
- **Other failures** (Ollama down, the model not pulled, 5xx, timeouts, a
  store error) are retried at the first check 15 minutes later, then 30
  minutes, 1, 2 and 4 hours, and every 4 hours after. From the third
  failure in a row, about 45 minutes after the first, the change is
  reported unverified, naming the attempts and the last error; retries go
  on, and a verdict replaces that report.
- **While a verdict is pending,** the last report stands.
- **At most 4 verifications start in an hour,** whatever change each
  verifies. A build that keeps changing (a `base_url` balancing Ollamas of
  two versions, say) outdates every verification, which asks for the next
  at once, or flips back to the recorded build between checks, which
  starts a new change's verification at the next one: without the cap it
  re-embeds the sample back to back for as long as it flips. The first
  check the cap holds back is one WARN. A single change, even retried
  after failures (starts 15 and 45 minutes after the first), never meets
  it. Such a build's report never settles, since each flip outdates or
  resets its verification; that is outside what a local Ollama does, and
  the WARN names it.
- **The evidence has one wording,** `drift.Evidence.Detail` (`64 of 64
  sampled chunks changed (worst cosine 0.9713)`, or `not verified: ` and
  why, the cosine rounded down at four decimals so one under 0.9999
  never prints as 0.9999). The daemon logs it and sends it as healthz's
  `verification.detail`; `curio doctor`, `curio status`, `curio up` and
  the Status page print it as sent, saying "drifted" or "may have
  drifted".

**Why:** after the owner upgraded Ollama from 0.34.4 to 0.35.0 (model
digest `ac6da0df…` unchanged), healthz, doctor, status, the dashboard and
the log all reported a drift and asked for a reindex, hours of the
machine. Re-embedding 320 stored chunks under 0.35.0 gave 320 vectors bit
for bit the same as those stored under 0.34.4, and embedding the same
chunks twice showed no noise at all. A fingerprint says what might have
changed the vectors; only re-embedding says whether it did. The drift the
check exists for still shows: Ollama 0.30.0 lowercasing nomic-embed-text's
input moves the vector of every chunk with a capital letter.

**Why batching doesn't matter, and the request still is indexing's:** on
the owner's Ollama, stored chunks re-embedded four ways all came back bit
for bit: in the indexer's own batches, in one batch of 32 chunks from 32
documents, in that batch reversed, and one per request. So the sample may
mix documents in a batch. The request is still indexing's by
construction: one path can't diverge from itself, so the parity test only
has to prove `Index` uses it. The sample uses the configured document
prefix, so a prefix changed in config.yaml without a reindex also shows as
a difference, when a change of build next triggers a check.

**Why 64, and why these:**

- **48 random chunks, one per document:** a change that touches 1 chunk in
  10 slips past all 48 with probability 0.9^48 = 0.6%, 1 in 20 with 8.5%.
  A change that touches every input, like the lowercasing, shows in any
  one. One per document spreads the sample over the library rather than
  over its longest documents.
- **16 longest:** the long-sequence path (attention over 3,500 bytes,
  batching of long inputs) is where a change to the runtime shows first.
  Lengths saturate at the chunker's cap: on the owner's library 376 of
  41,489 chunks are exactly 3,500 bytes, in 248 documents, and taken
  plainly the 16 longest come from 13 of them. So each document is
  represented once, by its longest chunk, and ties go to the lower `seq`:
  `seq` has no AUTOINCREMENT, a reindex writes a document's chunks at the
  top, and the lower one is the chunk written first, the least likely to
  have been re-embedded since the change.
- **The cost:** 77-89 ms per 2,400-character chunk and 130 ms per
  3,500-character chunk on the owner's M4 Max (the review measured 52-65 ms
  for ~2 KB and 157 ms for 3,500 bytes), about 6 s for the sample; an 8 GB
  M1 takes 4-5 times as long. Once per change of build.

**Why 0.9999, and the length guard:** between unit vectors the L2
distance search ranks by is sqrt(2(1 - cos)), so a chunk at cosine 0.9999
has moved at most 0.014 of it from its stored self. The same build
re-embeds bit for bit, so the bound only has to absorb noise a build that
changes nothing might add. A cosine can't see a change of
length, and L2 orders like cosine only between unit vectors (the owner's
stored vectors measure 1 ± 5e-7), so a length that strays by more than the
same 1.4% is a change too. With unit vectors, as Ollama returns them
today, the guard never changes a verdict.

**Why the sample's bound is its batches times `embedding.timeout_seconds`**
(2 × 60 s by default, covering the store read too): each request is
already bounded by that timeout, which includes waiting in Ollama behind
the index workers' batches. A shorter bound would fail samples a slow
machine's config allows (CPU-only Ollama with a raised timeout); a longer
one bounds nothing more. Doubling the retry up to 4 hours keeps a machine
that can't finish the sample from spending that bound every 15 minutes.

**Why the last report stands while a verdict is pending:** the verdict
normally lands within seconds. Reporting the pending change would flash
the very false alarm this entry removes, on healthz, doctor, status and
the dashboard, and transient failures are capped at three attempts, so a
flaky Ollama hides a drift for an hour at most.

**Why a verification running across a rebaseline, a record or another
change is discarded:** it compared vectors against a baseline that no
longer holds, or verified a build no longer serving. After the sample the
monitor reads the fingerprint again and applies the result only if it is
still the one verified, the change is still the monitor's, and no
rebaseline or record happened meanwhile (an epoch both bump). Otherwise it
asks for a check at once, which records the build after a rebaseline, or
verifies the newer build; the build in between is never recorded. At most
one verification runs, and the monitor's mutex is never held across it:
`Report`, which healthz and the dashboard's 2-second poll call, answers
while it runs.

**Why verdicts live in memory:** a restart verifies again, once, about 6
s; persisting verdicts would add marker fields for a once-per-upgrade
cost.

**Why not a job, and not gated by the queue:** the verification runs on
the monitor's goroutine, on the daemon's context, and ignores pause,
schedule and throttle. It is one bounded burst per change of build, and
holding it for a schedule would leave a drift unreported for up to a day.
Shutdown cuts it short without counting an attempt.

**The store read** is one autocommit statement (in WAL a read never waits
on writers): each fetched document's chunks ranked by length in one
window, then the random documents shuffled before a chunk is picked from
each, then each sampled chunk's vector by a point lookup on `chunks_vec`'s
`chunk_id` (the table has no rowid tied to `seq`, and `chunk_id IN
(subquery)` scans every vector). Its plan is pinned. Measured: 0.26-0.28 s
warm on the owner's 41,489 chunks (0.9 s cold), 3.1-3.9 s warm on a
synthetic 497,868 (8-10 s cold). The cost is one pass over the chunks
table, 143 MiB on the owner's library; an index on the length would add a
migration and a write to every chunk insert, for a read that runs once per
change of build.

**What it can miss:** a "same" verdict needs every sampled chunk to
match, and a change that moves every vector shows in any chunk still
holding an old vector. So it is missed only if every sampled chunk was
re-embedded after the change. With a fraction f of documents re-indexed
since, that is about f^48 for the random set (f = 0.5: 4e-15), and the
longest set prefers the chunks written first. As f approaches 1 the stored
vectors are the new build's, and recording it is right. For a change that
touches a fraction p of chunks, re-indexed documents dilute detection to a
miss probability of (1 - p(1 - f))^48. The check runs within a minute of
an upgrade while the daemon runs, or at its start (no index job runs while
it is down), so f is small.

**Revised (2026-10-03):** a reported drift, verified or not, holds the
interests' automatic rebuilds and the placement of new documents: the
library then mixes vectors of two builds, and the current grouping's
centroids are the old build's. The interests stay as they were, and their
state is `held`, with the drift and the fix (`curio reindex --all`). A
rebuild asked for still runs, and its log line says it was built while the
embeddings drifted. `reindex-all` also owes the interests a fresh rebuild,
which waits for the re-embedding to drain. A change the sample shows to
be the same build changes nothing. See "Interests: two levels, stable
identities, automatic rebuilds".

---

## Interests: corrections that teach the grouping (deferred)

**Decision:** not built yet. The owner wants a way to correct the
interests, and for curio to learn from the corrections: move a document
to another interest or area, say a document doesn't belong where it is,
rename an interest. This entry records the feature and what it needs, so
the clustering rework (areas holding sub-interests, strays joined as loose
fits or left Unsorted) leaves room for it. Nothing here is built.

**What "learn" can mean, from cheapest to most ambitious:**

1. **Corrections hold.** A document the owner moved stays where they put
   it across rebuilds, and one marked "not here" stays out of that
   interest. A rename stays too. This is the minimum: without it, the next
   rebuild undoes the owner's work.
2. **Neighbours follow.** When a stray or a new document is placed, the
   corrected documents near it vote: if its nearest corrected neighbours
   were moved to X, it leans to X, weighted by similarity. No model is
   trained, and removing a correction removes its effect.
3. **Groups take the corrections in.** Corrections become constraints on
   the similarity graph before it is grouped: a moved document's edges to
   its new interest's members are strengthened ("must link") and its edges
   to the old one's weakened ("cannot link"). The grouping itself then
   keeps corrected documents together, and their neighbours with them.
4. **The space learns.** A projection of the embedding space, learned from
   the accumulated corrections, so that documents like the corrected ones
   land right from the start. This needs many corrections to learn
   anything, more than one person's library usually gives.

When this is built, 1 and 2 come first, 3 once corrections accumulate and
1–2 visibly fall short, and 4 only if a measurement shows the first three
aren't enough.

**What it needs first:**

- **Interests that keep their identity across rebuilds.** A correction
  names an interest, and today every rebuild makes new interests with new
  IDs. The rework's carry-over is that identity: a new interest that mostly
  overlaps an old one (more than 70% of its members) takes the old one's ID
  and label. A correction whose interest dissolves in a rebuild has to go
  somewhere visible (kept with the documents corrected alongside it, or
  back to Unsorted with a note), never silently dropped.
- **Corrections stored apart from runs.** A rebuild replaces every run's
  rows; corrections must outlive them. That means a table of (tenant,
  document, interest, kind: moved | not here | renamed) that a rebuild
  reads and never deletes.
- **A change path.** The dashboard changes things only through `/v1`, as
  JSON sent by `actions.js` (POST or PUT). The CLI and MCP get the same
  endpoints, and the OpenAPI spec and its test cover them.
- **A measurement.** The cluster-quality harness (branch
  `research/cluster-quality`, `cmd/clusterreport`) should show that
  corrections don't cost stability. That means replaying a set of
  corrections, rebuilding, and checking that they hold and that the
  untouched interests survive as often as without them.

**Why deferred:** the rework comes first. It decides the shape (two
levels) and the strays (loose fits plus Unsorted), and it brings the
stable identities that corrections need. Building corrections on today's
interests would mean building them twice. The measurements behind the
rework are in the cluster-quality report of 2026-10-01: 325 interests for
5,254 documents, a third with 4 or fewer members, 30% of documents in
none.

**Revised (2026-10-03):** the interests rework is in, and with it what
corrections need first, short of the corrections themselves (see
"Interests: two levels, stable identities, automatic rebuilds"):

- **In place:** identities that outlive runs (`interests`), carried over
  by the 70% rule, with `interest_lineage` saying what became of each; a
  retired identity kept 180 days with its successors, so a correction
  naming one can find where its documents went; `label_source` accepting
  `'user'`, with no writer yet, so renames need no table rebuild; and the
  engine's steps in an order with room for the hooks: corrections that
  hold and neighbours that follow (1 and 2 above) after the grouping and
  before carry-over, and in `Placer` for the documents placed between
  rebuilds, and groups that take corrections in (3) as a constraint on
  `GroupInput`. The measurement is on main: `make cluster-report`
  (`cmd/clusterreport`) runs the production pieces on a copy of a library
  and reports names kept by warm rebuilds and along the chain, which a
  replay of corrections would be measured against.
- **Still missing:** the corrections table, the change path (`/v1`
  endpoints, `actions.js` controls, the CLI and MCP) and the hooks
  themselves.

---

## Soft-404 titles: whole templates, not phrases

**Decision:** a title is a not-found verdict only when the whole title is
a not-found template (`soft404TitleRE`, `internal/fetcher/native.go`):

- **Site names around it.** Up to two before the template, each followed
  by a spaced separator, or by a colon when the name is one word
  ("Palantir | Careers | Page Not Found", "reddit.com: page not found"),
  and up to two after it, each after a spaced separator ("Page not found |
  Free local classifieds - Kijiji"). A spaced separator is one of
  `| / · • – — -` with whitespace on both sides; a site's name is up to 60
  characters other than `|`. Otherwise a colon opens a headline, after the
  template ("Not Found: The Search for Amelia Earhart") or after several
  words before it ("Lessons from a failed launch: Product not found").
- **The template**, optionally opened by an interjection (oops, whoops,
  sorry, uh oh) and closed by "(404)", "(410)" or "(Error 404)":
  - a status: 404 or 410, after an optional "HTTP" and "Error", then up to
    two of the status words not found, (not found), error, gone, page not
    found, deleted, deleted by (the) author, each after an optional colon,
    dash or pipe ("410 Deleted by author", "404. Page Not Found", "404
    Error: Page Not Found");
  - something gone: an optional this, that or the and "requested", one of
    13 things (page, content, post, story, article, video, track, product,
    group, profile, user, item, listing), an optional "you're looking for",
    "you requested" or "you tried to access", then how it is gone (not
    found, could not be found, doesn't exist, no longer exists, is no
    longer available, is missing, has been removed, was deleted by its
    author, and their variants);
  - a bare "Not found";
  - "We can't find the page" (couldn't, could not, cannot; the same 13
    things), with an optional "you're looking for".
- **One sentence anchored at the start only:** "The page you're looking
  for" (this page; you are, you were) and how it is gone, whatever follows.
  A Readability title can carry the page's whole first sentence.
- **Explicit lists.** No `\w+`, `.*` or free-length gap stands for a word
  inside the template; a word added to a list needs a row in
  `TestSoft404TitleRE`. Every word and mark of the lists has one: the test
  fails on each of 118 variants of the rule that drop one of them, or an
  optional piece of the grammar. Straight and curly apostrophes both.
- **Read whether or not an article was found** (`looksLikeSoft404`):
  Readability takes the title from the page's metadata whatever it finds
  in the body.
- **Plain spaces.** The title is matched with its whitespace made plain
  (`strings.Fields`), since Go's `\s` is ASCII and some sites pad their
  separators with no-break spaces (Google Cloud's docs put a space and a
  no-break space on each side of the `|` in "App Engine documentation |
  Google Cloud Documentation"; 16 stored titles carry one). The reason
  quotes the title as served.

Unchanged: `judgePage`'s order (dead link first), the kill switch, which
turns this rule off with the others, `ErrDeadLink` being permanent, never
host-cached and never sent to Jina from the origin, the reason text
(`title looks like a not-found page: <title>`), and `judgeJinaAnswer`'s
reading of Jina's target-status warning.

**Why:** 17 documents of the owner's library were stored `fetched`
although they are not-found or deleted pages, and the old rule knew none
of their titles: it knew a leading 404, a bare "Not Found", phrases
starting with "page", and "couldn't find this page".

- 11 × "410 Deleted by author — Medium" (medium.com ×9,
  onezero.medium.com, levelup.gitconnected.com): 5 through Jina after the
  origin's 403, 6 through Jina past an anti-bot host-cache entry (meta
  `host_cache: anti-bot`). They made an interest of their own, "Deleted
  Content".
- 2 × "Product Not Found | The Home Depot Canada", "Meetup | Group not
  found" and "This track was not found" (SoundCloud): through Jina after a
  thin origin page.
- "Content has been deleted - Quora": through Jina after the origin's 403.
- aqr.com's "The page you are looking for does not exist or has been
  moved. To find what you’re looking for, try one of the following:":
  straight from the origin, through Readability.

Jina's target-status warning would have made the Jina ones dead, and
where it comes it does: java.dzone.com's and vimeo.com's pages died on
"dead link (target answered HTTP 410 Gone)". Medium's answers for its 410
and 404 pages carry none (no Medium document in the library is dead), and
neither did LSAC's 403 page. There the title is the only signal: run on
the old code, Medium's tombstone answer was stored on all three Jina
routes.

The old rule's phrases were unanchored, and its leading-404 alternative
had no end. Over 51 realistic article titles it matched 16, among them
"How to fix 404 Not Found errors in Nginx", "Fix the 404 error on your
WordPress site", "404 Media", "404 Error Pages: 30 Creative Examples",
"Creating a custom page not found handler in Express" and "Your page has
been removed from Google's index: what now?". An origin article with such
a title was judged dead through Readability, and a dead verdict is
sticky: a refetch is refused without `--force`, and `--force` judges the
same title again. So the rule is held to whole titles, as
`errorPageTitleRE` and `challengeTitleRE` are.

The article gate: a not-found page whose body Readability can't extract
(an app shell) was "no article extracted", a page-level login wall sent to
Jina. 4 dead documents were judged dead only by Jina after that, each
spending a Jina request that a dead link never should: "404 - Not Found"
(`f7b423fe`), "Page Not Found - Clarity Design System" (`9493db96`),
"Palantir | Page Not Found" (`50676971`) and "RxJS - PAGE NOT FOUND"
(`fff881fa`). With Jina off, or in trouble, they would have ended
`failed`/`login_wall`. The anchored rule is the guard against false
positives; the gate added none.

**Corpus check** (read-only, 2026-10-02, through `looksLikeSoft404`): of
the library's 5,203 stored titles (every `fetched` document), the new rule
matches exactly the 17 tombstones and the old rule none. The old rule's 14
dead verdicts (12 distinct titles, each a genuine not-found page; all 833
dead documents still have their failed jobs) all still match. Of the 51
article titles the new rule matches none, and it matches all 42 template
variants tried.

**Existing documents:** the rule judges fetches, not what is stored. The
17 stay `fetched`, in search and in the latest clustering run, until they
are refetched:

| ID | Host | Title |
|---|---|---|
| `7f6fa07b`, `7c63ac42`, `88ebd4c0`, `453de608`, `8c6112ca`, `438e38a6`, `d5cd9688`, `db4436e7`, `55e21e00` | medium.com | 410 Deleted by author — Medium |
| `5247c16c` | onezero.medium.com | 410 Deleted by author — Medium |
| `1bdfb327` | levelup.gitconnected.com | 410 Deleted by author — Medium |
| `11175b32`, `714ced09` | www.homedepot.ca | Product Not Found \| The Home Depot Canada |
| `1e29e209` | www.meetup.com | Meetup \| Group not found |
| `4bcccb00` | soundcloud.com | This track was not found |
| `466d4d9e` | consultantsmind.quora.com | Content has been deleted - Quora |
| `b8993401` | www.aqr.com | The page you are looking for does not exist or has been moved. … |

The remedy is `curio refetch <id>` for each, with its full ID or its URL,
once the daemon runs this fix. A refetch asks the site, judges the answer
like any fetch, and records the state, cause and last error through the
permanent-failure hook. Medium's origin answers curio with 403, and Jina
currently meets Medium's Cloudflare challenge (one keyless request on
2026-10-02 got "Just a moment..." and the CAPTCHA warning), so the 11
Medium documents will most likely end `failed` (`anti_bot`) rather than
`dead`; the other 6 should end `dead` (`dead_link`). Either state leaves
search at once and leaves the interests at the next clustering run
(`curio interests rebuild`, or the planned full rebuild). Their extraction
files stay on disk: search and clustering filter by state when they read.

There is no sweep at startup. SQL can't run the rule, so it can't be a
migration; as startup work it would need a one-shot marker, a store write
marking documents dead with no fetch or job behind it (bypassing the hook
that records why), a verdict on a stale title without asking the site, and
time before the daemon is ready, all for 17 documents.

**Not done:**

- **Tombstones only their body gives away.** 7 more stored documents are
  not-found pages under a title no rule can read as one: Medium's 404 page,
  titled just "Medium" (`7ad607e2`, `bd70c172`, `70233b2a`, `e0251c27`,
  `2b976ea6`, `fddead8b`; body "PAGE NOT FOUND", "## 404"), and Bespoke's,
  titled "Bespoke Interactive" (`abd7cbb3`; "# 404 / Sorry, but we can't
  find the page you're looking for."). All 7 came through Jina without a
  warning, and a refetch won't change them. A body rule needs its own
  design and false-positive measurement: an article about status codes can
  open with a 404 heading, and the origin path's text carries no heading
  markers.
- **LSAC's sign-in page** (`570bb74c`, "403 (access denied) error | The
  Law School Admission Council", "Sorry, you must sign in to view this
  content."): no tombstone, since the content exists behind a sign-in, so
  this rule leaves it alone and `TestSoft404TitleRE` pins that. Reading it
  as an error page is `errorPageTitleRE`'s business, a title shape that
  rule doesn't know yet.
- **Titles in other languages.** The lists are English, so javalobby.org's
  404 page, titled "หน้าไม่พบ | JavaLobby" (Thai for "Page not found";
  `3219e2f1`, `fa2c61a1`, through Jina without a warning), stays stored.
- **A template beside site names, in an article's title.** A page about a
  status, or a thread named after an error, can be titled with a template
  and site names alone: Wikipedia's "HTTP 404 - Wikipedia" and "HTTP 410 -
  Wikipedia", MDN's "404 Not Found - HTTP | MDN" (which the old
  rule matched too) and "410 Gone - HTTP | MDN", "php - Error 404 - Stack
  Overflow", "User not found - Auth0 Community", "Item not found -
  Microsoft Q&A", or a one-word name before a colon ("Kubernetes: Error
  404"). So can two kinds of headline: one whose subtitle follows a spaced
  dash, which reads as a site's name ("Error 404 - How to Fix It", "404 Not
  Found - What It Means and How to Fix It", the second matched by the old
  rule too), and one that opens with the sentence ("The page you're looking
  for doesn't exist: designing better 404 pages"). Such a page is judged
  dead. None is among the 5,203 stored titles; the title alone can't tell
  them from a site's not-found page.

**Revised (2026-10-03):** the first "Not done" item is done; see "Page
text: not-found notices, parked domains and sign-in forms". A page's text
is now read for a not-found notice: a line of its opening (its own lines,
links and code left out, up to its first line of prose within its first
2 KiB) that is a not-found template as a whole, or that opens with a
not-found sentence. The body rule's worry, that an article can open with
a 404 heading and the origin's text carries no heading markers, was
measured rather than removed. Only the opening and whole lines are read:
the line structure survives on both paths (go-readability's `RenderText`
puts each block on its own line), and no stored article has a notice in
its opening. An article that does open with a not-found heading ("## 404
Not Found") is judged dead, a risk the new entry accepts. A status code
alone counts only as a markdown heading, which the origin's text never
has, because on a line of its own "404" is as often a question's score.
Medium's 404 ×6 and Bespoke's are dead by their text.
Titles in other languages are still not read: javalobby.org's two pages
are dead only because their text opens with "## 404 - หน้าไม่พบ", a status
heading beside a site's name. The template's words (`notFoundLead`,
`notFoundThing`, `notFoundYouWanted`, `notFoundGone`, `notFoundCantFind`)
moved out of `soft404TitleRE` into one definition that the text rules
share; the title rule matches exactly what it did.

---

## Louvain: ours, warm-started; gonum as a test oracle

**Decision:** the interests rework groups with its own Louvain,
`internal/insight/louvain`, for fresh and warm passes alike, and gonum's
`community.Modularize` is only a test oracle, imported from `_test.go`
files and denied to the binaries by depguard. The grouping library around
it (`internal/insight`: `Grouper`, `LouvainGrouper`, `FlatGrouper`, the
merge, the strays, carry-over, placement; `internal/insight/quality`) is
built and tested, and nothing in the daemon calls it yet: the engine still
runs `KNNGraphClusterer`.

**Why ours:**

- **The warm start.** A rebuild starts from the previous grouping's
  communities, which is what keeps names across rebuilds; gonum takes no
  starting partition.
- **Speed.** On the owner's library (5,254 documents) both levels take
  9.8 ms fresh and 9.6 ms warm with the split check, against gonum's
  263 ms.
- **No dependency in the binary.** The package imports the standard
  library only (depguard `louvain-stdlib-only`), and `go list -deps
  ./cmd/...` names no gonum package.

**The algorithm** (package doc of `louvain`):

- **Objective:** Newman-Girvan modularity with resolution γ; local moving
  in a seeded permutation per level, then aggregation, until a level merges
  nothing.
- **Fixpoint.** The top level of a multi-level result is not a local
  optimum of the first level, so a warm start from it moves nodes. In the
  review of this change, a warm rebuild of an unchanged 2,000-document
  two-level fixture gave interest ARI 0.86 to 0.90 and kept 95 to 97% of
  names, on 4 fixtures of 4. `Run` restarts from its own result until a
  restart changes nothing, so `Run(g, r) = r` for every result r: an
  unchanged library rebuilds into the identical grouping, and names kept
  after 5% added rose from 94.9% (worst 83.8%) to 97.6% (worst 94.7%) in
  that review. Every restart that changes the partition raises modularity,
  so the loop ends; it cost about 0.6 ms at 5,000 nodes there.
- **Ties:** a node moves only when its best community beats staying by
  more than `GainTolerance` (1e-12, in edge-weight units: the modularity
  gain times half the total degree); candidates are compared in ascending
  community number, so the smallest wins.
- **Start rule:** with a starting partition, a node without one starts in
  the community it is most strongly connected to among those already
  assigned (node order, ties to the smallest), or alone; a node without
  edges is always alone, since no move changes modularity for it.
- **Caps:** 100 local-moving passes per level and 20 restarts. Each is
  reported in `Stats`; a Group call that hits one logs one WARN naming the
  pass (area, interest or flat), its node count and the cap.
- **The split check** (`RefineSplit`) runs Louvain inside each community
  alone, each node keeping its full degree and the graph its total degree,
  and keeps a split only when it raises modularity by more than the
  tolerance; the grouper runs Louvain again from its result. The research
  code's "largest part keeps the community's number" had no effect (its
  output was renumbered by first member), so it is gone.
- **Validation:** `NewGraph` rejects a neighbour out of range, a
  self-loop, a row not strictly ascending, a weight that isn't positive
  and finite, and an edge without the same weight back (`ErrInvalidGraph`);
  `Run` rejects a starting partition of the wrong length and a resolution
  that isn't finite and positive, and returns ctx's error, no partial
  result, when ctx ends.

**The oracle.** On the graphs a grouping cuts at the shipped settings (the
area graph and each area's subgraph at γ = 1, the flat graph at r(n) for
35, 300, 1,000 and 2,000 documents; 27 graphs over two fixtures),
`TestLouvain_MatchesGonum` holds |Q_ours − Q_gonum| ≤ 0.01. Measured: at
most 0.0087, ours above gonum's (the restarts polish a partition), and at
most 0.0058 below. `quality`'s modularity delegates to
`louvain.Modularity`, which matches gonum's `community.Q` to 1e-9.

**One neighbour pass serves both shapes.** A grouping makes one O(n²·d)
pass for every point's 20 nearest neighbours of positive similarity; the
flat graph is the union of those lists, the area graph the union of each
list's first 10 at cosine 0.40 or above, which is exactly the top-10 graph
(tested edge for edge against two separate builds). The gate between the
shapes needs the area pass's coverage on that graph, so it lives in the
grouper: `GroupInput.Shape` is the shape the previous grouping had (the
hysteresis state) and `Grouping.Shape` the shape produced; a grouping
whose shape changed was computed fresh. So is one whose `Prior` holds a
seed for none of its points (nil, empty, or about other documents): a
warm start from it would start every point beside its first neighbours,
which on the fixture gave 28 interests where a fresh pass gives 36.

**The merge repeats until nothing joins.** One pass over centroids can
leave a pair at the threshold: A and B at 0.86 join, and their joined
centroid reaches D at 0.871, which neither did at 0.84. `MergeNearDuplicates`
repeats with the joined centroids until a round joins nothing. It changes
nothing on the owner's library, where one pass already takes the fresh
grouping's 3 strict pairs to none (190 interests to 187). Seeds are the
communities before the merge, so a warm start re-derives the merge.

**Carry-over** (`Carry`) passes an identity only by the 70% rule (integer
comparisons, 10·shared > 7·size; successors at 4·shared ≥ size). A new
group that took no old group above 70% is a new identity, whatever merged
into it; a merge needs no input of its own, since it shows as one new group
taking 25% or more of two old groups. Per old group the flags (kept, moved,
split, merged, dissolved) aren't exclusive. "Moved" needs areas on both
sides, so a change of shape never moves anything. `quality.Inherit` is an
adapter over it.

**Measured on a copy of the owner's library** (5,254 documents, schema
15, the copy read with `DocumentVectors` and nothing else, never `~/.curio`
or the daemon):

- **`KNNGraphClusterer` unchanged.** At k 10, 0.5 and minimum 3 it
  reproduces the stored run `e6020c71` exactly (325 interests).
- **(a) A fresh grouping**, ours against gonum on the same graphs: 30
  areas against 29; 190 interests against 188, 187 against 185 after the
  merge; mean interest cohesion 0.645 against 0.655 (0.641 and 0.651 after
  the merge); area modularity 0.833 against 0.830; 92.3% of documents in an
  interest, 3 loose fits, 402 unsorted. The research measured ours at 31
  and 193 with cohesion 0.646. Times: the neighbour pass 1.98 s, Group
  without it 24 ms (23 ms warm with the split check), the merge and strays
  0.10 s: a grouping takes about 2.1 s.
- **(b) Names kept by a warm rebuild** through the whole pipeline, 3 draws
  each (the research's draws): 5% added, 96.2% of interests and 97.8% of
  areas (worst 95.4% · 96.6%); 5% mixed, 94.3% · 95.6% (worst 93.1% ·
  93.3%). The research measured 93% · 97% and 93% · 94%.
- **(c) A topic arriving all at once.** Held out by a fixed rule (the fresh
  grouping's interests at the 25th, 50th and 75th size percentile of those
  with 20 members or more: 25, 34 and 46 documents; the area nearest the
  median size: 152), grouped without it, then added back in one change.
  Recovery is the share of the topic in interests where it is the majority;
  the fresh grouping recovers each fully, since each is one of its
  interests:

  | Held out | Beside neighbours | + split check | Alone | + split check |
  |---|---|---|---|---|
  | 25 documents | 0.00 | 0.00 | 0.00 | 0.00 |
  | 34 documents | 0.00 | 0.97 | 0.97 | 0.97 |
  | 46 documents | 0.00 | 0.80 | 0.80 | 0.80 |
  | 152 documents (an area) | 0.82 | 0.90 | 0.92 | 0.92 |

  Names kept in these cases were the same or higher with "alone", and on
  (b)'s draws starting new documents alone kept 96.8% · 99.0% (added) and
  94.1% · 95.6% (mixed). The rule fixed before measuring adopts "alone" only if it
  reaches 0.9 of the fresh recovery on every hold-out; it doesn't (0.00 and
  0.80), so the strongest-neighbour start stays. Neither start recovers the
  25-document topic: on the warm start its documents settle in another area
  than the fresh grouping gives them and 23 of 25 join an interest of 87
  there, and the split check doesn't part them. The split check does
  recover the 34- and 46-document topics under either start, so a split
  check triggered by growth (any group grown by more than half since its
  last check) would bring that forward; it is not built.
- **(d) Visiting order:** a seeded hash of the document ID (FNV-1a,
  aggregated nodes taking their smallest key) against the seeded
  permutation. Fresh: 32 areas and 198 interests, cohesion 0.006 higher
  (beyond the 0.005 the rule allows), area modularity 0.0002 lower. Names
  kept, hash against permutation: fresh after 5% added 74.9% · 80.4%
  against 73.8% · 82.4%; warm added 95.8% · 97.8% against 96.2% · 97.8%;
  warm mixed 95.0% · 94.9% against 94.3% · 95.6%. Lower in three of six,
  so the permutation stays.
- **(e) r(n)** with ours on the research's draws (10 per size, each
  prepared alone), interests mean (range), gonum on the same graphs after:
  35 documents 4.5 (3–6) against 4.1 (3–6); 100, 5.0 (4–6) against 5.1
  (4–7); 300, 11.2 (9–13) against 11.3 (10–13); 1,000, 26.5 (25–31)
  against 26.2 (21–30). Coverage 100% throughout, every draw flat (1,000
  random documents stay under the area gate's 80%), and no draw of 35
  gives none.

**The fixture** (`fixture_test.go`): 2,000 seeded documents in 8 areas of
3 to 8 topics of skewed sizes, a planted near-duplicate pair of topics
(centroid cosine about 0.94), 8% generalists in a separate subspace that
end up unsorted, and a common offset so centering matters. The stability
properties run on it through the whole pipeline, each threshold the
design's target, with its measured value:

| Property | Threshold | Measured |
|---|---|---|
| Unchanged library, warm (also from a split run) | identical, every name | identical, 100% · 100% |
| 5% added, names kept (mean of 3) | ≥ 90% · ≥ 90% | 96.3% · 100% |
| 5% mixed, names kept (mean of 3) | ≥ 90% · ≥ 88% | 93.2% · 96.7% |
| Chain 60% → 100%, split every 4th: interests, cohesion against fresh | within 10%, within 0.03 | 33 against 36 (−8.3%), −0.002 |
| The same chain without the split check | fewer interests | 31 |
| No pair at 0.85 within a scope after the merge | none | none (7 joined fresh, 7 warm with the split check) |
| Placement keeps every name | 100% | 100% |
| 35 documents of 4 topics, 10 seeds | ≥ 2 interests each | 4 each |
| Shuffled input, fresh, warm, warm with the split check | identical per document | identical |

On fixture 1 Louvain itself keeps the planted pair in one interest; the
merge joins the five pieces it cuts one topic into and three pairs of the
fresh grouping, and in the warm one with the split check it also puts a
piece of the pair's first topic back with the interest holding the pair.

Each mechanism was disabled once to see its property fail: ignoring the
prior (names kept fall to 86.8% and 83.2%), skipping the restarts (the
unchanged library moves), never splitting (the chain ends at 31 against
31), one merge pass (a pair at 0.85 is left).

**The property tests run without -race.** Each runs the whole pipeline
many times over the fixture, where the race detector costs about ten times
the CPU and finds nothing the concurrency tests don't already run under
it. Under -race the package took 178 s on CI's 4-core Linux runner and
pushed `internal/ui`'s time-bounded markdown test over its limit. So six
of them (`skipUnderRace`: names kept, the chain, no near-duplicates left,
idempotence, order invariance, the gonum oracle) skip under -race, and
`make test` runs `./internal/insight/...` a second time without it. At
`-cpu 4` on an M4 Max: 31 s of CPU under -race (from about 280 s), 18 s
without.

**Below the gate the split check carries the growth.** The chain property
is pinned in the areas shape. In the flat shape r(n) rises as the library
grows, and warm starts never split a community, so a growing library falls
behind the interests r(n) asks for unless the split check runs. On three
900-document flat fixtures, the chain from 60% to 100% ends at 10.0, 10.7
and 8.3 interests (means of 3 draws) without it, against 16, 20 and 16
fresh, and at 16.0, 18.3 and 15.0 with it every 4th step. The rebuilds
that wire the grouper in have to run the split check regularly in both
shapes.

**One big step needs the split check too.** A warm pass whose Prior knows
only part of the library is coarse without it: on a 900-document flat
fixture, a Prior from 10%, 30% or 50% of the documents gave 7, 6 and 9
interests against 13 fresh (13 with the split check), and on the areas
fixture a Prior from 50% or 55% gave 32 and 28 interests against 36 (31
and 32 with it). Only a Prior that knows none of the points makes the pass
fresh. So the scheduler has to run the split check whenever one rebuild
absorbs a large change, not only by cadence; counting the split check in
changes (four times the rebuild threshold) does this, since an import of
that size crosses it in one rebuild.

**The shape gate reads two partitions.** The flip from areas to flat reads
coverage on the warm area partition, the flip from flat to areas on a
fresh one, so a library whose warm coverage is under 70% while its fresh
coverage is 80% or more would bounce areas → flat → areas on unchanged
rebuilds, each a fresh pass. A fuzzer found it with the gate's constants
lowered; at the shipped ones, 240 synthetic libraries with coverage
between 0.70 and 0.85 bounced none, and warm and fresh coverage differed by
0.007 at most. Left as is; if it ever shows, the flip to flat can read a
fresh area partition too (about 4 ms at 5,000 nodes, no second graph).

**Benchmarks** (Apple M4 Max, 16 cores; `bench_test.go` in both
packages): on a synthetic 5,000 × 1,024 library (132 areas and about 450
interests, more than the owner's library makes), the neighbour pass
1.70 s; Group with the merge and strays 1.80 s fresh and 1.81 s warm with
the split check (under the 3 s budget); Group without the pass 17 ms
fresh, 18 ms warm with the split check (both Louvain levels and the graph
cuts, under the 50 ms budget for Louvain); the merge and strays 69 ms.
`louvain.Run` on a 5,000-node area graph: 4.3 ms fresh, 1.1 ms warm,
4.7 ms warm with the split check and the run after it.


**Revised (2026-10-03):** the daemon now calls it. The engine groups with
`insight.NewLouvainGrouper` and runs the merge, the strays and
carry-over in that order, warm from the previous run's seeds when its
grouper and params match, with the split check by absorbed changes (see
"Interests: two levels, stable identities, automatic rebuilds").

**Revised (2026-10-03):** fresh-rebuild stability, re-measured with ours,
as Q1 promised: on the owner's copy, with the research's draws, `make
cluster-report` finds that a fresh rebuild of the whole library after 5%
added keeps 73.8% of interest names and 82.4% of area names (worst 70.7%
· 79.3%), against gonum's 75.9% · 85.6% (worst 73.8% · 83.9%) in the
research: ours is about as stable fresh as gonum's (the number is (d)'s
above). A warm rebuild of the same draws keeps 96.2% · 97.8%. So a fresh
rebuild, which follows `curio reindex --all`, a change of the grouper's
params or `curio interests rebuild --fresh`, renames about a quarter of
the interests, and the warm start is what keeps names (see "Interests:
two levels, stable identities, automatic rebuilds", "Acceptance on a copy
of the owner's library (PR 5)").

---

## Interests: two levels, stable identities, automatic rebuilds

**Decision:** interests are stored as runs of a grouping whose groups are
identities that outlive the runs. Migration 016 replaces 004's
`cluster_runs`, `clusters` and `cluster_documents` with `interest_runs`,
`interests` (the identities), `interest_groups`, `interest_assignments`,
`interest_placements`, `interest_lineage` and `insight_state`
(`docs/data-model.md` describes each column). The engine groups with
`LouvainGrouper` in two levels, areas holding interests, from the previous
run's seeds when it can, and carries identities over, so an interest keeps
its ID, label and links across rebuilds while it keeps most of its
documents. One that a rebuild split, merged or dissolved is retired, and
its ID answers 410 naming what took its documents. Rebuilds are
automatic: the daemon's scheduler queues one once enough of the library
changed and it settled, holds them while the embeddings drifted and after
a failure, and each document indexed between rebuilds is placed into its
nearest interest at once (see "Automatic rebuilds" and "Placement" below,
and migration 017).

**The schema follows the design's sketch, with these refinements** (each
also in the migration's header comment):

- `interest_assignments.area_id`: carry-over matches areas by their
  community's members, and `interest_id` plus the groups' `parent_id`
  can't rebuild that for a document in an area but in no interest (8 in a
  fresh grouping of the author's library).
- `interests.retired_run_id`, and `idx_interests_retired (tenant_id,
  retired_run_id) WHERE retired_at IS NOT NULL`: the changes a run made and
  the 410 name the run that retired an identity, which `retired_at` alone
  doesn't say. No foreign key: runs are pruned.
- `interests.created_run_id` is NOT NULL: every identity is minted by a
  run.
- `label_source` accepts `'user'` now, with no writer until renames: four
  tables reference `interests`, so widening the CHECK later would need the
  rebuild recipe (`migrations/README.md`).
- `idx_interest_assignments_document` and
  `idx_interest_placements_document`: without them EXPLAIN shows a
  document delete scanning both tables (`SCAN interest_assignments`),
  where `cluster_documents` sought `idx_cluster_documents_document`.
- CHECKs tie `fit` to `interest_id` (unsorted exactly when NULL) and keep
  `nearest_id` to unsorted rows.
- `insight_state` has no `shape`: the current done run's `shape` is the
  hysteresis state the grouper reads, and nothing wrote the two together.

Run-scoped tables inherit their tenant through `interest_runs`; runs and
identities carry `tenant_id`. Today's run is dropped, not converted (Q6):
its IDs lasted one run and nothing in it could seed a warm start. Down
drops the seven tables and recreates 004's with 004's own CREATE text, so
the schema is 015's byte for byte (tested, and checked on a copy of the
owner's library below).

**`CommitRun` is one `BEGIN IMMEDIATE` transaction,** so a run is
committed whole or not at all (`ReplaceClusters` and `FinishRun` were two,
and a crash between them left a run with clusters still `running`). In
order: the tenant's latest done run must be `RunCommit.PriorRunID` (else
`ErrConflict`, nothing written), so two rebuilds can't both commit from
one prior; the new identities; relabels (label, summary, label_source,
labeled_at); groups, assignments and lineage through statements prepared
once per commit; the retirement of every live identity of the tenant the
run's groups don't hold (`retired_at` and `retired_run_id`, one commit
timestamp); and the run's move from `running` to `done` with its outcome
(a run that isn't running is `ErrConflict`). A done run never changes.
After the commit, best effort, each failure one WARN: `PruneRunsExcept`
the new run (groups, assignments and placements cascade; identities and
lineage stay), `TrimLineage` (rows of earlier runs whose old identity is
still live: without it every rebuild adds a "kept" row per surviving
group, about 200, forever), and `PruneRetired` of identities retired
before now − 180 days (`insight.RetiredRetention`), their lineage
cascading. 180 days keeps an old link's answer useful for half a year.
Measured at C speed on the review's synthetic run over a copy of the
owner's library: the commit of 5,254 assignments and 223 groups about
31 ms, pruning one run 14 ms, deleting 50 retired identities 64 ms (the FK
checks scan the child tables once per identity; acceptable once per
rebuild, so no further index). On the acceptance run below the whole
commit and prune took 32 to 48 ms.

**The engine's order** (`insight.Engine.Rebuild`): read the latest done
run (the prior; no run is written when this or the vector read fails) →
read the vectors, dropping non-finite ones → with no vectors and a prior,
keep the prior, write nothing and fail (see "Nothing to group is a failed
rebuild" below), so a library momentarily without vectors retires nothing
→ read the prior's assignments and groups → plan → create the run →
prepare the points (center, normalize) → `Group`, warm from the prior's
seeds when eligible → `MergeNearDuplicates` → `Centroids` → `AssignStrays`
→ `Carry`, areas then interests → label → `CommitRun` → prune. Any error
after the run is created marks it failed on a 10 s context detached from
the job's and keeps the previous run current. One INFO line, "interests
rebuilt", carries the run, trigger, kind, split_check, shape, counts, what
carry-over did at both levels, and read_ms, group_ms, label_ms and
persist_ms.

- **Fresh or warm.** Warm-eligible means a prior exists, made by this
  grouper (`prior.Grouper == Grouper.Name()`) with byte-equal params (the
  grouper's constants plus `center`, canonical JSON); a change of either
  makes the next run fresh. Carry-over applies to fresh runs too, so names
  survive a change of params by overlap. The recorded kind is fresh unless
  the run was warm-eligible, the grouping kept the input's shape, and the
  prior held a seed for at least one point.
- **Changes.** What changed since the prior read its vectors, counted
  from the data (`InsightStore.Changes`, see "The change count is
  derived" below): added, left (failed, dead or pending again), deleted
  and reindexed documents. The engine reads it right after the vectors, and
  the one count is the run's `changed_documents` and feeds the split
  check's cadence. (PR 2 counted |N \ P| + (prior.num_documents − |P ∩ N|)
  over the vectors read, which missed reindexed documents and which no
  scheduler could read.)
- **The split check.** Q3's rule: with T = max(5, ⌈5% of the
  prior's documents⌉), a warm-eligible run splits when the changes since
  the last split check plus this run's reach 4·T; a fresh or split run
  resets the count, any other adds to it. At automatic rebuilds every T
  changes that is every fourth rebuild, and a manual rebuild moves it
  closer only by the changes it absorbs. Rejected, on the owner's library
  copy (PR 1's pipeline, in-process): **always splitting on a manual
  rebuild**: the first split check after a fresh grouping of an unchanged
  library splits 9 communities and renames 2 of 30 areas, and after a 5%
  addition a warm rebuild with the split check keeps 88.9–94.3% of interest
  names and 90.0–96.7% of area names, against 95.0–98.3% and 96.7–100%
  without it (87.4–89.9% against 93.1–96.4% on a mixed 5% change): every
  click would visibly rename. **Never splitting**: warm starts drift (one
  5% step ends at 168–178 interests against 187 fresh), and one big step
  needs the split check (see "Louvain: ours, warm-started").
- **What is written.** An interest's size is its members, its loose its
  loose fits, its cohesion the mean member similarity, its centroid the
  `Centroids` row; an area's size and loose are its interests' sums, its
  cohesion the mean cosine of those members to their unit mean, with no
  centroid. One assignment per point: a member or loose fit with its
  cosine to the interest's centroid, or unsorted with its nearest interest
  and the cosine to it; `area_id` from the merged grouping; seeds from
  `Grouping.Seeds`. A new group inherits its predecessor's ID, label,
  summary and label_source; the others get new UUIDs.

**Labels.** Only these groups are named: new ones; carried ones without a
label (labeling was off); carried term labels when the LLM is wanted,
keeping the term label when it doesn't answer; and of two carried labels
with one key in one scope (an area's interests, the areas, or every
interest in the flat shape) the smaller group (size, then ID). A carried
LLM label is never regenerated otherwise, so a warm rebuild of an
unchanged library makes no labeler call. In the areas shape the areas go
largest first, and within each its interests to name, largest first, each
seeing its area's name when it has one and every sibling name so far;
then the areas to name, largest first, each from its interests' names and
sizes and up to 12 representative titles taken round-robin (each
interest's most central, then each one's second), seeing the other areas'
names. `ClusterInfo` gains `Siblings`, `Area`, `Children` and `Taken`; the
prompts ask for a name different from the siblings'. A reply whose key
(`insight.LabelKey`: lowercased runs of letters and digits, the quality
harness's comparison, now one function) is empty or taken is asked once
more with `Taken`, then falls back to a term label that skips every token
a sibling name uses (and stays empty when nothing is left). The fallback
and budget rules are unchanged. This fixes the two "AI Agent Engineering"
interests of 86 and 45 documents one area held. A flat run's interest
prompt says "Other topics are already named:" (it has no area to say
"Its other topics").

**One pending job.** The cluster job's payload is
`store.ClusterJobPayload{Trigger}`; a missing or unknown trigger (`{}`
from an older job) is `manual`. Every rebuild goes through
`JobQueue.EnqueueOnce`, which inserts a job only when no pending job of
its kind exists for its tenant and otherwise returns that one, in one
`BEGIN IMMEDIATE` transaction; `POST /v1/interests/rebuild` answers the
new or the pending job's ID, and a running job doesn't count, so a click
during a rebuild queues the next one. The pending check is written
`+tenant_id = ?`: written plainly, EXPLAIN on the owner's library shows
the NOT EXISTS seeking `idx_jobs_tenant_status_updated (tenant_id=? AND
status=?)` and walking every pending job of the tenant, thousands during
an import; with the `+` it seeks `idx_jobs_claim (status=? AND kind=?)`,
which holds only pending cluster jobs. Both statements are pinned. The
daemon no longer reads `insight.knn`, `min_similarity` or
`min_cluster_size`. (PR 2 queued a `first` rebuild at every start without
a done run, whatever the library held: a new home committed an empty run
before `curio up` imported anything, and nothing backed off. The
scheduler replaced it.)

**The API** (`api/openapi.yaml`): `GET /v1/interests` (limit 1–500, 50;
offset; children 0–100, 5; members 0–100, 3; `level=interest` for every
interest with its area) lists the run's top-level groups, areas or flat
interests, by size, cohesion, then ID, with `total`, the counts
(`num_areas`, `num_interests`, `num_loose`, `num_unsorted`), the run's
`rebuild` (trigger, kind, split_check, changed_documents and what it did)
and `next`, where automatic rebuilds stand: the scheduler's snapshot,
the same as healthz's `interests` (see "healthz and the snapshot" below);
clients read an unknown state as current. `GET /v1/interests/{id}` answers an area
with all its interests, or an interest with its area, a page of members
then loose fits (each with `fit`), its newest placements and its events in
the latest rebuild. A retired ID is a 410 `urn:curio:problem:interest-retired`
with `id`, `level`, `label`, `retired_at`, `run_id` (the run that retired
it) and `successors` from that run's lineage (split or merged, with the
documents shared), and a detail sentence naming them; an unknown ID is a
404. `GET /v1/interests/unsorted` lists the unsorted nearest first with
their nearest interest, and `GET /v1/interests/changes` the latest
rebuild's split, merged, moved, dissolved and new events. Placements are
listed apart (`new`, `new_members`, and Unsorted's `new`).
Renamed fields have no aliases: `num_noise` is `num_unsorted`,
`num_clusters` is `total`. The consistency rules keep today's: a page
that reads no groups below `total` is read once more from the newest run,
and a group gone from the run read is looked up again, a 410 if it was
retired meanwhile; a live identity no done run holds is a logged 500.
`internal/client`, `curio interests` (an outline of areas with their
largest interests, `--flat`, `show`, `unsorted`, `changes`, `rebuild`),
the MCP tool and the dashboard (area cards, an area's page, loose fits on
an interest's page, a 410 page in the dashboard's frame) read through
them.

**Deprecated keys.** `insight.knn`, `insight.min_similarity` and
`insight.min_cluster_size` stay in the strict decoder, with any value of
their type (0, −1, 1.5 and `.nan` load), are neither defaulted nor
validated, and are recorded by presence, as `daemon.workers` is; the
daemon logs one WARN at start naming them. The new grouping's constants
are recorded in each run's params and changed in code (Q5). Rejected:
refusing the keys (an upgrade would refuse to start over a key nothing
reads) and keeping them meaningful (they tuned a clusterer the engine no
longer runs).

**Departures from the implementation spec.** `PreparePoints` runs after
the run is created, not before: a corpus whose vectors can't be prepared
(mixed widths) is then a failed run with its error, which the `failing`
state and the Interests page report, instead of a job error alone. The
410 page in the dashboard says what became of the identity in its detail
sentence, without links to the successors (the JSON and the CLI give
their IDs); PR 4 redesigns the pages.

**Acceptance on a copy of the owner's library.** Every run used a fresh
`sqlite3 -readonly ~/.curio/curio.db ".backup …"` copy (5,237 fetched
documents, schema 15), a scratch `CURIO_HOME` with the owner's marker, a
config with a free loopback port, `insight.labeling: terms` and auto-pull
off, and binaries built by `make build BIN_DIR=<scratch>`; never
`~/.curio`, port 8765 or a checkout's `./bin`. The numbers are from the
"interests rebuilt" log line.

- **Migration and the first rebuild.** 016 applied at start in 9 ms; the
  daemon queued the first rebuild itself ("interests: rebuild enqueued",
  trigger first) and it ran fresh in the areas shape: 29 areas, 182
  interests, 2 loose fits, 400 unsorted, 211 term labels; read_ms 9,284 +
  group_ms 2,004 = 11.3 s (under 20 s), label_ms 59, persist_ms 32. The
  vector read is most of it, as the review measured (9.2 s).
- **A 5% hold-out, then warm.** 262 of the 5,237 fetched documents
  (Python `random.sample`, seed 1) were set to `pending` in a second copy
  before the daemon's first start. Its first rebuild, fresh: 28 areas and
  168 interests over 4,975 documents (read 8,722 ms, group 1,791 ms).
  With the documents set back to `fetched`, `POST /v1/interests/rebuild`
  ran warm with `split_check` false and 262 changed: 28 areas and 168
  interests, 167 interests kept, 1 merged into another (27 documents
  shared), 1 new, every area kept; persist_ms 48. `GET
  /v1/interests/{id}` for each of the first run's identities: 28 of 28
  areas and 167 of 168 interests answered 200 (100% and 99.4%), the merged
  one 410 naming its successor.
- **Down and up.** `goose -dir migrations sqlite3 <copy> down` on a copy
  migrated to 16 left `sqlite_master` (type, name, tbl_name, sql)
  identical to the original copy's, and `up` again gave the same 016
  schema as the daemon's migration.
- **In a browser.** Headless Chrome through the DevTools protocol at 1440
  and 390 px, light and dark, against the hold-out copy's daemon with its
  queue paused and Ollama unreachable, a hostile area and interest label
  seeded (markup, 270 characters without a break, an RTL override): the
  Interests page, an area's page, an interest's page with a loose fit, a
  retired ID's 410, an unknown ID's 404 and the search home. No CSP
  violation, no sideways scroll, no element outside its card, and no
  inline script. The check caught the crumb naming a long area label
  running 1,200 px past a phone's width; it is now cut on one line with
  the whole label in `title=`.

**Test times** under `-race` on an M4 Max, before and after:
`internal/insight` 7.5 s and 7.8 s, `internal/store/sqlite` 7.0 s and
7.5 s, `internal/api` 13.6 s and 14.6 s, `internal/ui` 51.3 s and 51.2 s.
The engine's fixture-library test (2,000 documents, several rebuilds)
skips under -race and runs in `make test`'s second, race-free pass of
`./internal/insight/...`. gonum stays test-only.

### Automatic rebuilds

**The scheduler** (`insight.Scheduler`) runs in the daemon beside the
drift monitor, and only with `insight.enabled`. It checks at start, once
synchronously before the API is up and any worker claims (so the first
healthz has its state, and a rebuild that is due, such as an upgraded
library's first, is queued before anything else runs), then every minute
and whenever kicked: by a rebuild requested through the API, by
reindex-all, by a cluster job starting, and by the cluster pool once a
job's outcome is recorded (`Worker.OnFinished`: a kick from the end of the
handler would read the job still running). Each check reads, through the
narrow `insight.Library` (`NewLibrary` over the stores): the queue's
cluster and index counts, the latest done run R and `Changes(R)`, the
fetched documents, `LastIndexedAt`, and `insight_state`; the drift report
and `Engine.ParamsChanged(R)` come from injected functions. The reads are
separate statements, and the queue goes first: a rebuild that commits
during the read was then queued or running, so the check is busy. In the
other order a check could read the done run from before a rebuild
committed and the queue after its job was done, and queue a second
rebuild from changes the first had absorbed; the cluster job's start kick
made that check likely whenever a rebuild took milliseconds. A pure `decide`
turns that into the snapshot and whether to queue, so the rules are tested
over a simulated library and clock. A check runs under its own 30 s
timeout; one that can't read keeps the last snapshot and warns once a
streak. It queues a rebuild (`jobs.EnqueueRebuild`) when all hold:

- no rebuild is queued or running;
- due: no done run and 20 documents fetched; a fresh rebuild owed against
  a done run; failures past their backoff; `Changes(R).Total()` ≥
  `Threshold(R.num_documents)`; or, with the map on, R without a map (the
  interest map, below: a run from before maps, one curio 2.5.x committed,
  or one made with the map off); and in every case at least one fetched
  document;
- not held: no embedding drift reported, and the backoff passed;
- settled: nothing indexed for 10 minutes, or due for 2 hours already (30
  minutes with no done run); a re-embedding's fresh rebuild only once no
  index job is pending or running and nothing was indexed for 10
  minutes, with no cap.

The trigger is `first` (no done run), then `reindex`, then `params`, then
`auto` (the library's changes, a retry, a map owed, or a fresh rebuild
asked for whose own job failed). A map owed has no trigger of its own:
016's `CHECK` on `interest_runs.trigger` would need the table rebuilt, and
curio 2.5.x would read a run it doesn't know; it owes no fresh rebuild
either, so the rebuild is planned warm whenever any would be, and on an
unchanged library keeps every identity, assignment and label and asks no
labeler. A map that failed owes nothing: what fails one (a library too
large for the 2-minute bound, a view that fails its own check, a panic)
fails it again on the same library, each retry costs a whole rebuild, and
a backoff would need state `CommitRun` doesn't keep (`insight_state`
counts failed rebuilds, and a done commit clears them); the failure shows
on healthz and in doctor with its fix, and every later rebuild draws the
map again. A request through the API queues a rebuild whatever the
scheduler says (threshold, settle window, drift and backoff don't apply;
the queue's gate does), and the scheduler queues none while one is queued
or running. States, the first that holds: `rebuilding`, `queued`, `held`,
`failing`, `due`, `current`, `none`; `unknown` only before the first check
succeeds, and `off` (from the API) with insight off. Logs: "interests:
rebuild due" (changed, rebuild_at, fresh_owed, waiting_for) and the WARN
"interests: rebuilds held" (reason, fix `curio reindex --all`) once an
episode (the hold's when a check first finds the state `held`: a rebuild
queued before the drift was reported still runs), never a line per check;
"interests: rebuild enqueued" (trigger, changed, waited, job, queued) each
time. Both of a rebuild the map owes add `map_owed=true`.

The values, exported constants in `internal/insight/schedule.go` and
shared with the engine where both use them (`Threshold`, `RetryDelay`):

| Value | | Basis |
|---|---|---|
| First rebuild | 20 fetched documents (`FirstRebuildAt`) | 35 is the smallest library measured; unmeasured |
| Threshold | max(5, ⌈5% of R's documents⌉) (`Threshold`) | owner; the floor unmeasured |
| Settle window | 10 minutes with nothing indexed (`SettleWindow`) | unmeasured |
| Longest wait once due | 2 h; 30 min with no done run (`MaxWait`, `MaxWaitFirst`) | unmeasured |
| Check | every minute (`CheckInterval`), and on kicks | |
| Backoff | 15 min, doubling to 4 h (`RetryDelay`) | mirrors the drift check |

Pinned on a fake clock by simulations (`schedule_test.go`: pure Go, no
SQLite, no sleeps; a rebuild queued runs at once, reading a second after
its check): 2,000 documents indexed over 30 minutes into the owner's
5,254 give one rebuild, at 40 minutes (due at 4, the last indexed at
29.5); a 5-hour import at the owner's index rate (50 a minute) is rebuilt
exactly MaxWait after each rebuild became due, to the check (at 2:06 and
4:18, never two within 2 hours), and once more 10 minutes after it ends; a
drift reported through the import queues nothing, says held, and warns
once; 19 documents never, 20 once settled or after 30 minutes; a library
of 30 after 5 changes, not 2; a queued rebuild blocks; a done run with no
fetched document left stays current; retries come 16, 31, 61, 121, 241 and
241 minutes apart (each failure a second after its check), a new scheduler
over the same state keeps the backoff, and a success ends it; a
re-embedding drained over 3 hours is rebuilt once, 10 minutes after the
last index job, and never while the queue is paused; a change of params at
once. A done run without a map is rebuilt once, at once on a quiet library
(trigger `auto`, `map_owed=true`) and 10 minutes after the last document
indexed otherwise, and the check after its commit finds it current; held
while the embeddings drifted, before the first drift verdict, during a
failure's backoff and while a rebuild is queued; never with nothing
fetched or the map off; and a map that fails on that rebuild queues no
second one in a day of checks (the sim's runs draw a built map unless a
test says otherwise).

**When a rebuild became due is kept in memory**, so a restart starts its
`MaxWait` again; the change count, the fresh rebuild owed and the backoff
are stored, so a restart changes nothing else. Storing it would add a
writer for a cap that only matters during an import of hours, where a
restart costs at most one more wait.

**The change count is derived** (`InsightStore.Changes(R)`), with since =
R's `vectors_read_at` (its `started_at` when unknown):

- added: fetched, `indexed_at` ≥ since, not assigned in R (placed or not);
- reindexed: fetched, `indexed_at` ≥ since, assigned in R;
- left: assigned in R, now pending (a refetch in flight), failed or dead;
- deleted: R's `num_documents` less R's assignment rows (they cascade).

One statement of four scalar subqueries, so the counts come from one
snapshot. A refetch counts as left while pending, then as reindexed. The
design said "added = fetched now, with no assignment in R", which counts
forever every document R couldn't group: one indexed without a chunk
(`indexer.Index` succeeds with none, and the document is marked fetched)
or one R dropped as non-finite. With T of them, 5 in any library under
100 documents, every check would be due and a rebuild run at every check.
Counting only documents indexed since R read its vectors ends that: such
a document was indexed before. So migration 017 adds `documents.indexed_at`,
written by `MarkFetched` alone, in its statement and to the same instant as
`updated_at`, backfilled exactly from `updated_at` for the fetched
documents (only `MarkFetched` leaves a document fetched), and indexed
`(tenant_id, indexed_at)` for the range and for `LastIndexedAt`'s seek. The
comparison is `>=`, so a document indexed in the millisecond R read is
never missed: at worst it is a phantom change of one. "Left" is driven from
the documents in those states on `idx_documents_tenant_state_updated`, each
checked against R's assignment by its primary key: driven from R's
assignments with `d.tenant_id = ?`, EXPLAIN on the owner's copy shows
SQLite walking `idx_documents_tenant_state_updated (tenant_id=?)` through
every document of the tenant. The count needs no counter kept by the index
handler, the failure hook, refetch and delete, and survives a restart (on
the copy below: 262 changed before a restart and after it).

**Nothing to group is a failed rebuild.** With no vector to group and a
prior, the engine keeps the prior and writes nothing: an empty grouping
would retire every interest and its name, and a library can lack vectors
for a while (a refetch of every document). Succeeding that way left
everything that made the rebuild due as it was (the change count, a fresh
rebuild owed, the failures), so the kick at the end of the job had the
scheduler queue the same rebuild at once, and again: the review drove the
real pools and scheduler over 25 fetched documents without a chunk (a
library whose every fetched document was indexed with no chunk, or whose
every vector is NaN or infinite) to 4,527 cluster jobs in 2 seconds. So it
fails (`errNothingToGroup`, "nothing to group: no fetched document has a
usable vector"), counted like any failure: the backoff spaces the retries
15 minutes to 4 hours apart until a document with a vector arrives, and
`curio status` and doctor say why. Remembering in the scheduler which
inputs it last queued for would also end the loop, but in memory (a
restart queues it again) and with the state saying `due` while nothing
happens. A rebuild is also due only with a fetched document, so a library
with none (a reindex-all while the embedding model is missing fails every
index job) stays `current` rather than failing.
`TestPools_NothingToGroupBacksOff` pins it through the real pools, engine
and scheduler.

**A re-embedding drains first.** `curio reindex --all` (state fetched, at
least one document) owes a fresh rebuild before it enqueues anything (a
failure there is a 500 with nothing enqueued; one later leaves a fresh
rebuild owed, which costs at most one rebuild), beside `Drift.Rebaseline`.
The owner's library indexed at most 3,017 documents in an hour (its
busiest, on 2026-09-28, with `index_workers` 4), so re-embedding its 5,254
takes about 1.75 hours, plus the settle window: the 2-hour cap would fire
mid-drain on a larger library or a slower machine and build the "fresh"
grouping from vectors of two builds, which is what the drift hold exists
to prevent. So that rebuild settles only once no index job is pending or
running and nothing was indexed for 10 minutes, with no cap: a queue
paused mid-drain holds it, since its index jobs stay pending. The old
interests keep showing meanwhile, and placement holds. It is owed with the
insight layer off too: the grouping the layer finds when it is turned on
again is of the old vectors. Only the CLI's note that the interests will
be regrouped waits for `insight.enabled` (config.yaml, as `curio ui` reads
`daemon.ui`).

**After a start, the interests wait for the drift check's first verdict.**
The drift monitor keeps its report in memory, so a daemon restarted during
an unresolved drift reports none until the monitor concludes its first
check, a few seconds later, or up to a sample verification's bound when the
build changed. A rebuild due through the drift (changes accumulated while
held) would be queued at start in that window and group vectors of two
builds, and placements would go into the old build's space. So until the
monitor's first verdict (`drift.Report.CheckedAt` set, clean or drifted),
the scheduler queues nothing (`SchedulerOptions.DriftChecked`; the snapshot
says due, the due line waits for "the first embedding check since the
daemon started", and nothing is asked of the user), `Run` holds its sweep,
and placement is held. When the check can't conclude (Ollama down: grouping
needs no Ollama), the wait ends 10 minutes after the start
(`driftCheckGrace`), and rebuilds and placement go ahead.

**Fresh rebuilds owed** (`insight_state.fresh_owed`, `fresh_owed_at`):

- `reindex`, written by reindex-all, always wins and is owed from now;
- `manual`, written by `POST /v1/interests/rebuild?fresh=1` (`curio
  interests rebuild --fresh`, Q10), only when nothing or `manual` is owed:
  it never replaces `reindex`. It is stored, not put in the job's payload,
  because `EnqueueOnce` may answer a pending job, whose payload can't
  change;
- `params` is derived: every done run records its grouper and canonical
  params, and `Engine.ParamsChanged(R)` is the comparison warm eligibility
  makes, so it stops being owed when a run with the new params commits,
  and nothing could go stale. The snapshot shows it as `fresh_owed:
  params`;
- `shape` has nothing to schedule: the gate between the shapes needs the
  area pass's coverage of the graph, so it is decided inside every
  rebuild, and the rebuild that crosses it is the next ordinary one (a
  library growing past 1,000 documents accumulates 5% changes). 017
  narrows the CHECK to the two reasons with a writer.

The engine reads the state after the vectors: owed, the plan is fresh. The
commit clears it, in its transaction, only for a fresh run whose
`vectors_read_at` is at or after `fresh_owed_at`: a warm run never
consumes it, and one owed again while a run grouped (a second reindex-all,
or a warm run whose plan read the state before the owe) survives. A fresh
rebuild owed with no done run makes nothing due: the first is fresh
anyway, and waits for its 20 documents. A rebuild asked for during a
re-embedding plans fresh, but its vectors are of both builds: consuming
the owe would make the next automatic rebuild warm, from seeds grouped on
that mix, and resume placement against its centroids while the drain
goes on. So the engine asks the queue whether index jobs are pending or
running (`Config.Indexing`, the scheduler's drain signal) both before and
after it reads the vectors: a drain that ends during the read (about 10 s
on the owner's library) has jobs left before it, and a re-embedding owed
before the read but enqueued during it has jobs left after. A run that
finds some either side commits with `ReadMidReindex`, which leaves a
`reindex` owe in place
(a `manual` one is consumed as usual: it asked for this rebuild). The
check can't hold an owe for good: the scheduler queues a re-embedding's
rebuild only with no index job left, so the run that finds one is one an
import started in between, and the next quiet spell queues another.

**Rebuild failures: one attempt, the scheduler's backoff.** PR 2's handler
let the queue retry a failed rebuild (5 attempts, 30 seconds doubling),
which re-read every vector, about 10 s on the owner's library, four more
times within 15 minutes, against the design's backoff; and counting each
attempt as a failure would have pushed the first scheduled retry to 4
hours. Now every rebuild error is permanent (`ErrPermanent`), and the
engine counts every failure but a cancellation in `insight_state`
(`RecordFailure`: one more failure, `last_failure_at`, `last_error`,
failing the run in the same transaction, or alone when the rebuild failed
before creating one), with one WARN "interests: rebuild failed" (err,
failures, retry_at). A cancelled rebuild (shutdown) fails its run, counts
nothing, and its job is requeued with the attempt refunded. A done commit
clears the failures. retry_at = `last_failure_at` + `RetryDelay(failures)`,
stored, so a restart keeps it; failures make a rebuild due once it passes.

A rebuild that kills the daemon (an OOM or jetsam kill, SIGKILL, a fatal
runtime error; a handler panic is already recovered) looped: `RecoverOrphans`
requeued its job with the attempt kept, the cluster worker claimed it at
the next start, and after five crashes PR 2 queued a fresh `first` at the
next start: a crash per launchd restart (10 s) for good, each one costing
every running fetch and index job an attempt. Now a cluster job claimed
with `Attempts > 1` is that orphan (nothing else claims one twice: errors
are permanent, interruptions refund), and its handler records it as
abandoned (`Rebuilder.Abandoned`, `RecordAbandoned`: every running run of
the tenant failed with "the daemon stopped during this rebuild (it
crashed, was killed, or outran the shutdown grace)", and one failure, in
one transaction) without running it; the cluster pool's hook does the same
for an orphan `RecoverOrphans` fails for good, and counts nothing for the
handler's own failures. Accepted: a crash between `CommitRun` and
`MarkDone` counts a spurious failure, which costs one rebuild 15 minutes
later. That window holds the post-commit prune (32 to 48 ms on the
owner's library) and placement sweep (0.39 s for 262 documents, bounded
at 2 minutes).

### Placement

`insight.Placer` places each document indexed between rebuilds into the
latest done run, where the run grouped (`RunSpace`: centered on the run's
stored mean, normalized, nearest centroid): its interest at a cosine of
0.45 (`LooseFitThreshold`) or more, else Unsorted with its cosine to the
nearest interest. The document's vector is the mean of all its chunk
vectors in float64 (`store.MeanVector`, which `DocumentVectors` pools
with too, so placement sees what the next rebuild will; not find-related's
first-64 cap). The run's space, its interests' centroids by ID, is cached
per run and read again once a newer run is done.

- **The fast path**: the index handler calls `Place` right after
  `MarkFetched`, before the job is marked done. In order, it does nothing
  while the embeddings drifted, before a done run, while a re-embedding's
  fresh rebuild is owed, or for a document the run assigned (a refetched
  or reindexed one keeps its assignment until the next rebuild, and counts
  as a change). It writes one row with one guarded statement
  (`PlaceDocument`: only while the run is still the tenant's latest done
  run, the run didn't assign the document and the document exists; ON
  CONFLICT it places it anew, so a document indexed again moves), and a
  run replaced meanwhile or a document deleted writes nothing. It never
  fails the job: it has its own 10 s timeout, recovers its own panics
  (the worker's recovery would fail the index job and mark the document
  failed), and logs a failure once a run at WARN ("interests: placement
  failed", run, document, err), the rest at DEBUG. A cancellation and a
  document without chunks log at DEBUG and write nothing.
- **The sweep** (`Sweep`): after each commit and its prune, and once when
  the scheduler starts, it places every fetched document indexed since the
  run read its vectors that the run neither assigned nor placed (`Unplaced`:
  a range of `idx_documents_tenant_indexed`, both primary keys): those
  indexed while a rebuild ran, and any whose fast path failed. It writes
  them as it goes, 64 at a time (`sweepBatch`), each batch one transaction
  with the guard checked once, keeping a placement made already (unless
  it is off the interest map: "Interest map", Placement), so it never
  overwrites a newer fast-path placement, and a run replaced mid-sweep
  takes none of the batches after; a document whose vector can't be
  placed (another width, a non-finite value) is left out with one WARN
  rather than stalling the rest. A sweep that fails, or runs out of its 2
  minutes, keeps what it wrote and returns it with the error; the next
  sweep places the rest. The "interests rebuilt" line counts them
  (`placed_after`).

Measured on the owner's library copy below: one `Place` takes 2.1 ms
(median; p95 10 ms, mean 3.0 ms over 261), the first of a run 11 ms as it
reads the run's space (196 groups); a sweep of 262 documents 0.39 s in the
daemon (0.78 s on a cold copy); a scheduler check's reads 1.3 ms (median,
40 ms cold).

### healthz and the snapshot

`/v1/healthz` gains `interests` and `GET /v1/interests` serves the same as
`next` (`InterestsState`, one schema): the scheduler's last snapshot
(`Scheduler.Snapshot`, an atomic pointer), never a query per request:
state; the done run's `last_rebuild_at`, `last_kind`, `last_trigger`;
`changed_documents` and `rebuild_at` (with no done run, the fetched
documents and 20); `due_since`, `fresh_owed`, `held_reason`, `retry_at`,
`last_error`, each only when it applies: `held_reason` with state `held`,
`retry_at` and `last_error` with `failing`, so a rebuild queued or running
while the embeddings drifted, or past failures, reads as just that (it
runs); and `map`, R's map as the check read it with R (`insight.MapState`,
no read of its own): `status` built (with `kind` and `took_ms`), failed
(`took_ms` and `error`), none (R drew no map: with the map on, a rebuild
is due to draw it) or off (`insight.map: false`, whatever R drew), absent
with no done run unless the map is off. `took_ms` is a pointer on the wire,
so a reused map's 0 is said. `off` with insight off (no
scheduler is built, nor a placer), `unknown` only before the first check
succeeds; clients read a state they don't know as current. Interests
aren't health: `status` stays ok. `curio status` prints one line from it
("interests: rebuilt 2 h ago (warm) · 37 documents changed, next at 276 ·
map built (warm, in 2.8s)", the due, held or failing sentence, then the
map built, failed with its error, or off; none while unknown), `curio
doctor` an interests check (! held, with `curio reindex --all`; ! failing,
with the error and the retry time; ! the map failed, with its error,
`curio interests rebuild`, the logs, and `insight.map: false` if it keeps
failing; ✓ otherwise, naming the map), and `curio interests` and its
empty states say rebuilds are automatic and `rebuild` means now. A due
rebuild is told as one to draw the map when that is all it waits for
(map none, no fresh rebuild owed, fewer than `rebuild_at` changed), never
as "0 changed, threshold 263": clients infer it from the state, which
carries no reason of its own. The
"interests rebuilt" line adds `placed_after`, and `embeddings_drifted=true`
for a rebuild asked for while the embeddings drifted.

### Tests and the e2e knob

The fake-clock simulations above, the store's counts and writes on
`sqlitetest` (each component of the count, its restart, the owe rules,
the guarded writes, the commit's clears), the engine through the store
(reindexed members raise the count and the split cadence; failures counted
but cancellations; fresh owed and consumed; documents indexed while a
rebuild groups are placed after its commit and the replaced run's
placements pruned), the placer (holds, warnings once per run, the cache
across runs under -race), the jobs (one attempt; an orphan recorded, not
rerun, with a real queue and engine; placement never failing an index
job), the API (the snapshot on both surfaces with stores that fail every
call; reindex-all's owe before its jobs), the CLI and the MCP text. The
interests' end-to-end tests need minutes of settling with the design's
values, so
the daemon reads its scheduler timing from `CURIO_E2E_INTERESTS`
(`interval`, `settle`, `max_wait`, `max_wait_first`; an unknown key
refuses to start) only when built with the `e2e` tag
(`cmd/curio-daemon/schedule_e2e.go`, with a `!e2e` twin that returns the
defaults): a release has no knob, config key or environment read. It
leaves the backoff out: the engine and the scheduler share `RetryDelay`,
and no end-to-end test fails a rebuild. `make test-e2e` also runs the
daemon's unit tests in its e2e build, which test the knob's parser. An
import of 20 pages is grouped unasked (trigger `first`), and 5 more pages
regroup it (trigger `auto`); a rebuild queued while the queue is paused
stays queued, then runs once it resumes. `make test-e2e` took 11.2 s
before and 18.4 s after (the two tests 2.6 s and 4.5 s; the daemon's
e2e-built unit tests 5.6 s more).

### Acceptance on a copy of the owner's library (PR 3)

A fresh `sqlite3 -readonly ~/.curio/curio.db ".backup …"` copy (schema 15,
5,237 fetched documents), a scratch `CURIO_HOME` with the owner's marker,
a config with a free loopback port, `insight.labeling: terms`, auto-pull
off and both Ollama URLs on a dead loopback port, and binaries built by
`make build BIN_DIR=<scratch> GOTAGS=sqlite_fts5,sqlite_json,e2e`, run
with `CURIO_E2E_INTERESTS='interval=5s,settle=60s'`; never `~/.curio`,
port 8765 or a checkout's `./bin`. The hold-out is PR 2's: Python
`random.Random(1).sample` of the sorted fetched IDs, 262, the same 262.

- **The first rebuild.** With the 262 set to `pending`, the daemon's
  start applied 016 (7 ms) and 017 (10 ms), and its synchronous check
  queued the first rebuild at once ("interests: rebuild enqueued",
  trigger first, changed 4,975, a check of about 2 ms): fresh, areas, 28
  areas and 168 interests over 4,975 documents, read 8,794 ms, group 1,797
  ms, persist 30 ms. healthz then said `current`, `rebuild_at` 249.
- **The re-add.** Stopped, the 262 set back to `fetched` with `indexed_at`
  now, and started: the start's sweep placed all 262 into the first run in
  about 0.39 s, 188 into an interest (71.8%, the design measured 65-72%)
  and 74 into Unsorted; healthz said `due`, 262 changed against 249, and
  the same 262 after a restart within the settle window. Exactly one
  "interests: rebuild enqueued" followed, trigger auto, 60 s after the
  re-add was indexed: warm, `split_check` false, 262 changed, 28 areas and
  168 interests, 167 kept, 1 merged, 1 new, every area kept (read 7,789
  ms, group 1,987 ms, persist 49 ms), PR 2's warm result again.
- **Names kept.** `GET /v1/interests/{id}` for each of the first run's 196
  identities: 28 of 28 areas and 167 of 168 interests answered 200 (100%
  and 99.4%), the merged one 410.
- **Placements.** The first run's 262 placements went with it when the
  warm run pruned it, and the warm run, which grouped them, has none.

**Test times (PR 3)** under `-race` on an M4 Max, before and after, the
packages run together: `internal/insight` 8.3 s and 9.2 s (the heavier
simulations run in parallel), `internal/jobs` 6.8 s and 7.4 s,
`internal/store/sqlite` 7.7 s and 8.8 s, `internal/api` 14.8 s and 16.2 s,
`cmd/curio-daemon` 6.4 s and 6.5 s, `internal/cli` 12.2 s and 13.2 s.

### The cluster report (PR 5)

**`make cluster-report DB=<copy> [JSON=<file>] [DRAWS=n] [SEED=n]`** runs
`cmd/clusterreport` with `go run`: a developer's report on the grouping,
on a `.backup` copy of a home's database. It opens the copy through the
SQLite store and migrates it, its only write (a 2.4 home's copy is at
schema 15). Before opening anything it refuses a database whose directory
holds `daemon.pid`, naming the `sqlite3 -readonly <db> ".backup <copy>"`
to take, and a path that doesn't exist, which opening would create. A
read-only open needs the database's `-shm` file, which a daemon that
stopped cleanly removed with its WAL, so for a stopped home the refusal
also names `sqlite3 'file:<db>?immutable=1' ".backup <copy>"`: the main
file then holds everything. It reads the local tenant's
`DocumentVectors`, drops non-finite vectors as the engine does, and needs
`insight.FirstRebuildAt` (20) left.

**The production pieces, in the engine's order.** `regroup` is
`PreparePoints` (centered, the daemon's default) → `Group` →
`MergeNearDuplicates` → `Centroids` → `AssignStrays`, with the inputs the
engine passes: a first grouping starts from `ShapeFlat` without a prior, a
fresh rebuild from the previous grouping's shape without its seeds, a warm
rebuild from its shape and seeds. Names kept are `insight.Carry`'s, its
input built as `previous.oldGroups` and `grouped.carry` build it (areas
matched over area membership, interests over their members, a new
interest's parent its area), as `Counts.Kept` over the old groups, and
n/a for a level with none. Rather than refactor the engine to share the
composition, `TestReport_MatchesTheEngine` holds the tool to
`Engine.Rebuild`: on a seeded synthetic library of 1,100 documents in
areas, plus one NaN vector, in a test database, the engine's fresh run of
a draw's 95% (6 areas, 24 interests, 8 loose fits) is identical to the
tool's fresh grouping through the stored-run comparison (the same
documents, ARI 1 at both levels, every fit the same), and once the last
5% arrive the engine's warm run keeps the interests and areas the tool's
warm rebuild of that draw says (23 of 24 interests, 6 of 6 areas). It
fails with the merge or the centering left out of `regroup`, every stray
left unsorted, the split check forced on, a warm pass started from the
flat shape, or the previous library passed as the new one; a unit case
holds names kept to members alone, as the engine's carry-over counts
them. It takes 0.9 s under -race, the package 2.8 s, with
`TestRun_FlatLibrary` running the whole report on a 300-document library
in the flat shape whose run the engine made.

**The draws** are the research's, so its numbers, PR 1's and these compare.
Draw d of a 5% change is a permutation seeded 5001+d (added: its first
round(5% of n) documents are held out of the previous library) or 5008+d
(mixed: of half = round(2.5% of n), the first half are held out of the
previous library and the next half removed from the new one). The chain's
is seeded 8080+1010·d: 8 steps of round(5% of n) documents from 60% to
100%, a first grouping at step 0 and a warm rebuild from the step before
at each other, with the split check at steps 4 and 8, each step set
against a first grouping of the same library. `-seed` adds to every seed.

**What it reports**, as text and in the `-json` file (snake_case keys, null
where nothing applies, written atomically at mode 0600 once every
measurement has succeeded): the fresh grouping's shape and, per level,
groups, coverage, sizes, cohesion (`quality.CohesionOf` over the
grouping's own prepared vectors) and silhouette; interests before and
after the merge, loose fits, unsorted, and near-duplicate interest pairs
(at 0.85 within an area and anywhere, at the research's lenient 0.74
anywhere); the baseline; names kept by a warm rebuild after 5% added and
after a mixed 5%, and by a fresh rebuild after 5% added, per draw, mean
and minimum; the chain's steps and summary (each draw's end against
fresh, the gap furthest from 0, names kept at the split-check steps and
at the others); and, when the copy holds a done run, that run: its row,
label sources, the label table, exact (`insight.LabelKey`) and near
(`quality.DuplicateLabels`) label pairs in each scope and among all
interests, and its agreement with the fresh grouping. SIGINT or SIGTERM
cancels every read and grouping, and the run then writes no JSON.

**Labels come from a stored run.** The engine's sibling-aware labeling is
unexported, reads titles through the document store and needs a model, so
the tool labels none of its groupings. It reads a run the daemon built
instead: a throwaway daemon's first rebuild of the same documents, which
it checks is its own fresh grouping.

**Not shipped** (Q9): `curio eval --clusters` would need an endpoint that
hands every vector to the CLI, for a measurement only a developer runs.
So the tool lives in `cmd/clusterreport` with its own exception in
depguard's `store-boundary` rule (`!**/cmd/clusterreport/**`: it opens the
SQLite store itself), and its non-test imports are the standard library,
`internal/insight`, `internal/insight/quality`, `internal/store` and
`internal/store/sqlite`; `go list -deps` names no gonum package. `make
build` names the three commands it builds, where `./cmd/...` would have
made the tool a fourth binary in `./bin`, and `.goreleaser.yaml` is
unchanged: its builds, archives and formula list curio, curio-daemon and
curio-mcp. `KNNGraphClusterer` stays one release as the report's baseline
(k 10, 0.5, minimum 3, its clusters as the old engine stored them, no
merge and no strays), then goes with that section.

### Acceptance on a copy of the owner's library (PR 5)

A fresh `.backup` of the copy the research and PR 1 measured (taken on
2026-10-01 after run `e6020c71`: schema 15, 5,254 fetched documents),
read with `sqlite3 'file:…?immutable=1'`; an Apple M4 Max (12
performance and 4 efficiency cores, 64 GB); never `~/.curio`, port 8765
or a checkout's `./bin`. `make cluster-report` on it took 1 min 44 s of
wall time, 8 s of it the vector read and 67 s the chain's 51 groupings
(1,389 s of CPU in all), at a peak RSS of 293 MB, and reproduced PR 1's
in-process measurement number for number. Names kept are interests · areas, the mean
of 3 draws with the worst in brackets.

- **Fresh:** the areas shape, 30 areas and 187 interests (190 before the
  merge; PR 2's ranges are 29 ± 3 and 188 ± 10), 92.3% of the documents
  in an interest, 3 loose fits, 402 unsorted; interest cohesion 0.6414
  and silhouette 0.094, area cohesion 0.501. No near-duplicate pair at
  0.85, within an area or anywhere; 10 at 0.74.
- **Baseline** (`KNNGraphClusterer` at 10, 0.5, 3): 325 interests, 70.2%
  of the documents in one, 34.5% of the interests with 4 members or fewer,
  cohesion 0.776 and silhouette 0.163: the stored run `e6020c71` again.
- **Warm rebuild:** after 5% added, 96.2% · 97.8% (95.4% · 96.6%); after
  a mixed 5%, 94.3% · 95.6% (93.1% · 93.3%). The design's bar, 90% at
  both levels, holds.
- **Fresh rebuild after 5% added:** 73.8% · 82.4% (70.7% · 79.3%),
  against gonum's 75.9% · 85.6% in the research (see the Revised note on
  "Louvain: ours, warm-started; gonum as a test oracle").
- **Chain, 60% to 100%:** it ends at 186, 185 and 180 interests (mean
  183.7, 1.8% fewer) and 33, 31 and 29 areas, against 187 and 30 fresh,
  with cohesion 0.6501, 0.6508 and 0.6495 (mean 0.6501) against 0.6414:
  every draw within the design's 0.03 (+0.0087 on the mean). The gap
  furthest from 0 at any step is −0.0246 (the first draw at 75%, the step
  before a split check): warm steps drift below fresh between split
  checks, and each check brings cohesion back above it.
- **Names kept along the chain:** at the split-check steps 80.3% · 92.1%
  (75.0% · 87.0%), against the research's 84% · 92% (worst 80%); at the
  other steps 96.1% · 97.2% (91.0% · 92.3%). Our split check renames a
  few more interests than the research's measured; a split rebuild stays
  visible, as the design's risks say, and the bar it set, the chain's
  cohesion, holds.

**A fresh rebuild with gemma4:26b**, the owner's writing model, through a
throwaway daemon: binaries from `make build BIN_DIR=<scratch>`, a scratch
`CURIO_HOME` holding another backup of the copy with its 20 pending fetch
jobs failed first (nothing was fetched), the marker of PR 4's scratch
home, a config with a free loopback port, both Ollama URLs on the local
Ollama, `generation.model: gemma4:26b`, auto-pull off for both models,
`insight.labeling: llm` and `labeling_timeout_seconds: 600`. The start
applied 016 (2 ms) and 017 (35 ms), the scheduler queued the first
rebuild 1 s later, once the drift monitor's first check was in, and the
log has one "interests rebuilt" line: trigger first, fresh, areas, 30
areas and 187 interests, 3 loose, 402 unsorted; read_ms 10,040,
group_ms 2,020, label_ms 138,047, labels_llm 217, labels_terms 0,
persist_ms 45. Nothing fell back to term labels, and no model was pulled.
The labels took 138 s against the design's estimate of 118 s, 0.64 s a
name against 0.55 s: the sibling-aware prompts are longer. `curio
interests` printed 30 areas and 187 interests, and `curio doctor`'s
interests check passed. The report on a backup of that home found the
run identical to its own fresh grouping (ARI 1 at both levels, all 5,254
fits the same) and gave the copy's fresh, warm and chain numbers again.

**The labels.** No exact duplicate (`LabelKey`) among an area's interests,
among the areas, or among all 187 interests. Three near pairs within a
scope (token overlap of half or more): "Distributed Systems and
Algorithms" and "Distributed System Design" in one area, "Game Engine
Development" and "Puzzle Game Development" in another, and the areas
"Digital Platform Economy" and "Digital Influencers and Platforms"; three
more across areas. Every label has 2 to 6 words in title case, none reads
as a preamble, a quote or a cut. Most area names cover their interests
("AWS Serverless Ecosystem", "Investment Theory and Psychology"). Areas
that join unrelated topics are named after two of them glued together, or
after one, which leaves the others out ("Financial Technology and Markets"
holds a "Wiki and Information Systems"). A few interests got catch-all
names ("Miscellaneous Search Queries", "Unrelated Web Content"). An area
of one interest repeats that interest's name ("Andreessen Horowitz
Partners"), so the outline prints it twice; they are in different scopes,
which the uniqueness rule allows. An area named "Deleted Content" (11
documents) is this copy's: it predates the refetch of the 17 stored
tombstones on 2026-10-02. The full label table stays outside the repo: it
is the owner's reading.

**Going back to 2.4.** 2.4.x runs goose v3.28.0, which ignores database
versions it doesn't know, so a 2.4 daemon starts on a schema-17 database
and its interests then fail on the tables 016 dropped. The down
migrations are right: run through curio's own driver, which loads
sqlite-vec, `DownTo(15)` on a migrated copy took 54 ms and left
`sqlite_master` (type, name, tbl_name, sql) identical to the original
copy's. goose's CLI can't run them on a real library, though: 017's down
drops `documents.indexed_at`, SQLite checks the whole schema after `ALTER
TABLE … DROP COLUMN`, triggers included, and the chunk index's triggers
need the vec0 module, which the CLI's SQLite lacks ("error in trigger
trg_chunks_delete: no such module: vec0"). PR 2's `goose down` from 16
worked because 016's down only drops and creates tables. So the release
note's way back is a copy taken before the upgrade, `curio daemon stop &&
sqlite3 ~/.curio/curio.db ".backup $HOME/curio-2.4.db"` (a `~` inside the
quoted argument is expanded by neither the shell nor sqlite3), put back
once 2.4 is installed and nothing that uses curio's MCP tools runs, which
would start the daemon again: `curio daemon stop && rm -f
~/.curio/curio.db-wal ~/.curio/curio.db-shm && cp ~/curio-2.4.db
~/.curio/curio.db`. The stop and the swap share a line after the install,
so a daemon started after the swap is 2.4's, and `cp` leaves the copy in
place, so a slip can be redone. Both lines ran as printed, under zsh with
`HOME` at a scratch directory, between v2.4.1 built from its tag and this
release's build (the `curio` in them ran with `CURIO_HOME` at the scratch
home and the real `HOME`: with `HOME` moved, curio takes the scratch home
for the default one, whose launchd label is the real agent's). The
database came back byte for byte, and 2.4.1 started on it at schema 15
with its 325 interests; 2.4.1 started on a schema-17 copy too, and
answered `curio interests` with "no such table: cluster_runs".

**Test times** under -race on an M4 Max, each package alone, before and
after: `internal/insight/quality` 1.3 s and 1.4 s
(`TestCohesion_MatchesTheEngine` now checks against `Centroids` and
`AssignStrays`), `internal/api` 15.9 s and 16.4 s (one assertion more,
no server), and `cmd/clusterreport`, new, 2.8 s. `make test`'s race-free
pass is unchanged.

**Revised (2026-10-04):** each rebuild also draws the interest map of its
grouping, between carry-over and labelling, and commits it with the run
(migration 018); a map that fails is no failure of the rebuild, and
placement gives each placed document a place on it. `Grouping.Neighbours`
carries the grouper's neighbour pass out for it. See "Interest map: two
views of each regrouping, drawn when it is built".

---

## Dashboard: two-level interests

**Decision:** the dashboard shows the two-level interests the engine
builds (see "Interests: two levels, stable identities, automatic
rebuilds"), on pages that follow the grouping's places: the Interests
page (areas, or interests below the area gate), an area's page, an
interest's page, Unsorted (`/ui/interests/unsorted`), what the latest
rebuild changed (`/ui/interests/changes`), and a retired or unknown ID's
page; a document's page says where the latest rebuild put it, and
Status's health card where automatic rebuilds stand. Rebuild stays the
only change the pages make, sent by actions.js to `POST
/v1/interests/rebuild` (no fresh rebuild, Q10).

**In flight from the queue, the rest from the scheduler.** The Interests'
rebuild line says, first match: the queue's read failed; a rebuild runs,
since its claim; one is queued, and why a closed queue holds it; then the
scheduler's snapshot (`Deps.interestsState`, a memory read): held, with
its reason and `curio reindex --all`; failing, with the error (cut, whole
on hover) and when it retries; due, with what it waits for; current, when
it was rebuilt, its kind and the changes against the threshold; none, the
documents so far against the 20 the first waits for; and nothing for
unknown, off, or a snapshot of a rebuild queued or running that the queue
no longer holds. In flight comes from the queue because a click's rebuild
is in the queue the moment its 202 answers, and the page's poll follows
that answer, where the snapshot is refreshed by an asynchronous Kick and
can lag the click by a check or two: a snapshot-only line would answer
"current" to the click (`TestUI_InterestsClickRace` holds the race: a
rebuild queued through the API while the fake scheduler still says current
is "Rebuild queued" on the next poll, its button disabled, polling every
2 seconds). Each state says what `curio status` says for it
(`interestsText`): one partial (`interests-state`) words it for the
Interests' head and for Status's row, in lower case as the rows read, and
the head capitalizes it in CSS. Rebuild stays enabled while rebuilds are
held or failing: a rebuild asked for isn't held by drift, and retries a
failure now.

**Failures are the scheduler's.** The line used to report the newest run
of any status when it wasn't the one shown. A rebuild that fails before
creating a run (nothing to group) or that a dead daemon left
(`RecordAbandoned`) left no failed run to read, so it went unsaid, while a
run a shutdown cancelled (`FailRun`, which counts no failure) read as "The
rebuild failed". The snapshot's failing state comes from `insight_state`,
which counts exactly the failures, so the page reads no run of any status
any more, and `ui.Rebuild` no longer carries one.

**Two cadences.** The poller polls every 2 seconds while a rebuild is in
flight by the queue's word or the snapshot's, or a read failed, and every
30 seconds otherwise (`idlePollEvery`) with insight on: the scheduler
checks once a minute and may queue a rebuild at any check, which an open
page would otherwise never learn of, since its poller stopped once nothing
was in flight. With insight off it polls only after a change. The 30-second
poll reads the queue and the latest done run: two index reads, about half a
millisecond (below).

**Reads.** The full page reads the latest done run, its page of groups,
the placement counts, each card's members (or the areas' interests in
one read) and their documents at once, and the queue: the newest run is
no longer read, since the page has just read the latest done one. Its poll
reads the queue and the latest done run, to offer newer interests as a
reload, plus the running rebuild's job while one runs; never the
interests, members, counts or documents. An interest's page reads the
done run, the group, its run, the counts, its members (and loose fits
when they are on the page), on its first page alone the documents placed
into it, their documents in one read, and the lineage and its identities;
an area's, the same with its interests in one read and their members for
the page alone. Unsorted reads the done run, its page, the placement
counts, on its first page alone the placements into Unsorted, and every
document at once; the changes page the done run, the lineage, the
identities the run created and retired and those its events name, and
the nested groups only when it moved an interest; a retired ID the done
run, the miss, the identity, its successors and theirs.
`TestUI_InterestsReads`, `TestUI_UnsortedReads`, `TestUI_ChangesReads`,
`TestUI_GoneReads` and `TestUI_DocumentReads` pin each.

**An area's interests are paged in Go.** An area's page shows 24 of its
interests a page, as the Interests page shows groups, under the shared
pager; a page past the last is a 404 that keeps the head and reads no
member. `describe` reads every interest of the area in one read, which the
count and the area's new documents need anyway, then lists members for
the page's window alone (`interestOpts.Window`, which only the page
sets): `GET /v1/interests/{area}` still lists every one, and the spec is
unchanged. An area of the owner's library holds at most 12 interests, so
paging costs nothing measurable there; it bounds the page for a library
that grows an area of hundreds.

**The coverage bar.** Four parts, over the run's documents and those
placed since (`NumDocuments + NumNew`): members (`NumDocuments −
NumLoose − NumUnsorted`, never below 0) in the accent, loose fits in the
accent's border colour, new since the rebuild in the neutral dot, and
unsorted as the track's remainder; the legend follows in the bar's order,
the loose fits and new documents only when there are any. The tones are a
fixed Go set (`CoveragePart`), each a `fill-`/`swatch-` class over
app.css's tokens. `GET /v1/interests` gains `num_new_unsorted`, the
placements into Unsorted, from the `PlacementCounts` read the list makes
already, for Unsorted's card; no endpoint reads more.

**Areas, Unsorted and what changed.** An area's card names its five
largest interests, each cut on one line with its size and a bar scaled to
the largest (a largest of 0 draws only the track), and ends "+ N more
interests →", or "All N interests →" when it lists them all. Unsorted's
card closes the last page of groups (or stands alone on a run without
any), leading to Unsorted's first page of the run shown. For a week after
a rebuild that split, merged or dissolved interests (the handler compares
its finish with now: templates do no time math), a note counts them and
leads to the changes. An interest's page carries a lineage note, a line
an event, dated by the run's finish, each identity linked; one the
rebuild only kept says nothing, since the run before is pruned and what
it gained and lost can't be told. The changes page lists the run's events
under a heading a kind, areas first; it links retired identities too,
whose page says what became of them.

**A retired or unknown ID is no fault.** A retired identity's 410 and an
unknown one's 404 are pages in the Interests' frame, Interests current,
with no request ID or "see `curio daemon logs`", which read as a fault for
an expected answer. The 410 names the identity, says in one sentence,
chosen by its successors' events and dated by its retirement in local
time, whether it split into them, merged into one or more, went to both,
or dissolved (its documents going to other interests or to Unsorted), and
lists the successors, each linked, a successor retired since marked. The
404 claimed interests "were regrouped when curio was upgraded", which is
false for an identity `PruneRetired` deleted after
`insight.RetiredRetention` (180 days): it now names both possibilities
without asserting either, the days computed from the constant. A 500 from
the read stays the error page, and the JSON 410 and 404 problems are
unchanged.

**A document's place: one read, the page's alone.** The design scopes the
line to the dashboard ("A document's place in the current run | Document
page | Both primary keys"). `InsightStore.DocumentPlace` reads it in one
statement: from the run's row, the document's assignment and placement,
each interest's group in the run for its area, and the labels, every
table sought by its primary key (pinned, no SCAN). The page reads the
latest done run, then the place; no run or no place is no line, and a
failed read is logged once and left out. The document's JSON, which the
CLI and MCP share, leaves it out; a later change can add it to `/v1`.

**Tests.** The rule suites cover every new route, page, partial, link
builder and state: routes, security headers (with an area's page past the
last, Unsorted past the last and not a number, changes, a retired and an
unknown ID), GET-only, the crawl that writes nothing (the new pages, every
line kind, their polls), every template with hostile samples (labels with
markup, 300 characters without a break and a right-to-left override),
navigation, live regions in every rebuild state, the stylesheet's new
columns, folds and cuts, icons and links. `apitest` gains merges,
dissolutions, moves and a rebuild that changes nothing beside splits. The
e2e test that imports 20 pages now serves them on five topics, four pages
each, so the first grouping finds interests, and GETs the Interests, an
interest's page, Unsorted and the changes from the real daemon.

**Measured** on a throwaway daemon over a copy of the owner's library
(`sqlite3 'file:…?immutable=1' ".backup …"`, schema 15, 5,254 fetched
documents), in the state the browser check below left it (a warm rebuild:
30 areas, 166 interests, 406 unsorted, the queue paused), Ollama
unreachable, median of 21 curl timings after one warm-up, before (cb81804)
and after on the same machine and copy:

| Request | Before | After |
|---|---|---|
| `/ui/interests` | 1.61 ms, 36.7 KB | 1.94 ms, 66.5 KB |
| its page 2 | 0.74 ms, 10.1 KB | 0.81 ms, 14.6 KB |
| the largest area's page | 1.09 ms, 12.2 KB | 1.10 ms, 13.2 KB |
| the largest interest's page (115 members) | 1.42 ms, 28.4 KB | 1.40 ms, 29.3 KB |
| its page 2 | 1.42 ms, 29.4 KB | 1.36 ms, 30.4 KB |
| `/ui/interests/unsorted` | (no page) | 1.45 ms, 45.9 KB |
| `/ui/interests/changes` | (no page) | 1.35 ms, 11.5 KB |
| a document's page | 42.8 ms, 9.1 KB | 43.7 ms, 9.8 KB |
| `/ui/interests?poll=rebuild` | 0.42 ms, 3.7 KB | 0.43 ms, 3.9 KB |
| `/ui/status?poll=health` | 0.47 ms, 5.1 KB | 0.48 ms, 5.4 KB |
| `GET /v1/interests` | 4.85 ms, 217 KB | 4.63 ms, 217 KB |

Both targets hold: the Interests under 3 ms, an interest's page under 2
ms. The Interests' 30 KB more are the area cards' bars, 120 SVGs of about
230 bytes; a document's page is its related documents' vector search, as
before, and its place one primary-key read.

**Checked in a browser.** Headless Chrome through the DevTools protocol
at 1440 and 390 px, light and dark, against binaries built by `make build
BIN_DIR=<scratch>` from the branch, on a scratch `CURIO_HOME` (the owner's
marker, a free loopback port, `insight.labeling: terms`, auto-pull off,
both Ollama URLs on a dead loopback port) over that copy, its 20 pending
jobs failed first so nothing was fetched; never `~/.curio`, port 8765 or a
checkout's `./bin`. 262 fetched documents (Python `random.Random(1).sample`
of this copy's sorted fetched IDs) were held out as pending before the
first start, which applied 016 and 017 and said the first grouping was
due; `curio interests rebuild` grouped the rest fresh (31 areas, 177
interests, 4,992 documents). A hostile area and interest label were then
seeded (markup, 270 characters without a break, a U+202E override), the
262 set back to fetched with `indexed_at` now, and the queue paused: once
the drift check's grace passed (Ollama down, ten minutes), the start's
sweep placed all 262 (176 into interests, 86 into Unsorted), and the
scheduler queued a rebuild behind the paused queue. Resumed, it ran warm
(trigger auto, 262 changed): 30 areas, 166 interests, 164 kept, 13 merged,
5 moved, 2 new. The pages: the Interests with the first grouping due,
due with a run, queued ("Rebuild queued · the queue is closed: Paused",
the button disabled), rebuilding and ready (one page left open through
the rebuild, in each width: "Rebuilding · started just now" after 2.5 s,
then "New interests are ready: reload" with the current line, from its
own polls), and current with the week's note; its pages 1, 2 and past
the last; the largest area's page; an interest with its new band, its
page 2, one with a loose fit under its heading, one that took in three
others and moved; Unsorted's pages 1, 2 and past the last, with its new
arrivals; the changes after the first grouping and after the warm
rebuild; a merged interest's and a merged area's 410; an unknown ID's
404; a document's page for a member, a loose fit, an unsorted document
and two placed since; Status; and the search home. The held and failing
lines, with a 300-character hostile reason and error, were checked on the
Interests and Status through an uncommitted overlay build that faked the
snapshot, on a copy of the home. On every page: no CSP violation or
script error, no sideways scroll at 390, no element outside its card, no
inline script; long labels cut on one line with the whole on hover; the
phone's pager read Previous · "Page N of M" · Next. The check caught one
defect, fixed here: a name cut on one line is an atomic inline, so a
line that held it in running text with `white-space: nowrap` (Unsorted's
nearest interest folded under a phone's document name, and a document's
place line) dropped the whole name for an ellipsis; both now lay their
words and names out as flex items, which shrink and cut the name.

**Test times** under `-race` on an M4 Max, each package alone, before and
after: `internal/api` 16.2 s and 18.1 s (the new pages' tests start about
25 more servers), `internal/ui` 49.5 s and 52.5 s.

---

## Page text: not-found notices, parked domains and sign-in forms

**Decision:** `judgePage` reads a page's text, not only its URL and
title, for four kinds of page that aren't the page asked for
(`internal/fetcher/pagetext.go`):

- **The text view** (`readPageText`, once per `judgePage` call, pure). It
  reads at most the first 64 KiB of the text (`pageTextScanBytes`), up to
  the last line break within them, as lines: markdown images dropped,
  links reduced to their text, heading, blockquote, list and task-box
  markers, `**`, `__`, backslash escapes, thematic breaks and setext
  underlines dropped, whitespace (no-break spaces included) made plain,
  empty lines dropped. A line that holds only links, after an optional
  list, heading or emphasis marker (Jina's menus, story cards, footers), is
  a link line: its words are no part of the page's own text, which counts
  the bytes of every other line with its line break. A line is recorded as
  a heading when it had an ATX marker ("## 404") or stands over a setext
  underline, and as quoted when it had a blockquote marker ("> 404 Not
  Found"): a quoted line is own text, but the notice rules never read it,
  since a page quoting an error (a Stack Overflow question showing what
  its server answered) is about it, not it. No library verdict rests on a
  quoted line. A fenced code block (three or more backticks or tildes, to
  its closing fence or the end of the text) is the page going on: its
  lines count as own text, but no rule reads them, since code is never
  the page's notice. From the origin the text is go-readability's
  `RenderText`, which puts each block on its own line and has no link
  markup, heading markers or code fences, so every line is own text, none
  a heading, and passes through unchanged but for a line that opens like a
  marker ("1. ", "- ").
- **The opening:** the own lines that begin within the first 2 KiB of own
  text (`openingBytes`), up to and including the first line longer than
  200 bytes (`proseLineBytes`), the first line of prose.
- **A not-found notice** (dead link, with detection on): a line of the
  opening that is a not-found template as a whole (`notFoundLineRE`:
  `soft404TitleRE`'s templates, anchored at both ends; the title's one
  sentence anchored at its start only is left out, since a line can go on
  after it: "The page you're looking for was not found on our old server,
  so we moved it."), or that opens with a not-found sentence
  (`notFoundSentenceRE`): "we can't find (this|that|the) <thing> [you're
  looking for]" or "(this|that|the) [requested] <thing> [you requested]
  <gone>", ended by "." or "!", in `soft404TitleRE`'s words. Those words
  (`notFoundLead`, `notFoundThing`, `notFoundYouWanted`, `notFoundGone`,
  `notFoundCantFind`, `notFoundApos`) now have one definition, which
  every rule is built from, and one function (`notFoundTemplates`) builds
  `soft404TitleRE`, unchanged, beside `notFoundLineRE` and
  `statusCodeLineRE`. A template that is a status code alone, site names
  around it (`statusCodeLineRE`: "404", "Votes: 404", "410 · Followers"),
  counts only as a heading ("# 404", or "404" over a setext underline): on
  a line of its own, a number is as often a count. 157 documents have a
  number alone on a line of their opening (list ordinals such as "3."
  aside): 24 of the 147 Stack Exchange questions whose text shows their
  score give it that way (two of those scores between 300 and 999: 307
  and 444), and 7 X profiles their counts. A bookmarked question whose
  score passed through 404 would otherwise go dead for good, since a
  refetch judges the same text. Medium's tombstone, whose text gives "410"
  on a plain line, is dead by its title alone. Every status-code line
  among the flagged pages is a heading; Bespoke's "# 404" and
  javalobby.org's "## 404 - หน้าไม่พบ" are the verdicts that rest on one.
  Reason: `text reads like a not-found page: "PAGE NOT FOUND"`.
- **A parked domain** (dead link, with detection on): a line of the
  opening that is an optional subject and one predicate
  (`parkedDomainRE`). Subjects: "this domain", "the domain (name)",
  "domain", or a domain name, which counts only when it is the requested
  host or its site (`siteOf`), "www." and case ignored. Predicates: is or
  may be for sale, is parked, (registration) has expired, has been
  (recently) registered with or at a domain, Hover's "is a totally awesome
  idea still being worked on", easyDNS's "is yet another domain managed by
  …". With a subject, the predicate ends the line or is followed by ".",
  "!" or ":"; without one, it is the whole line (GoDaddy's "is for sale!"
  under the domain's name). An expiry needs a subject, as Namecheap's
  "Domain registration has expired." has: "Registration has expired."
  alone is as likely an event's sign-up page. A search-ads parking page's
  heading, "Related searches" or "Related search topics" alone on its line
  (`parkingHeadingRE`), counts too. Only the opening's lines that begin
  within its first 256 bytes of own text are read (`parkedNoticeBytes`):
  a parking page says what it is first, and a search results page, which
  Jina does get, puts its "Related searches" after its results. Reason:
  `text reads like a parked domain: "is for sale!"`. The content is gone,
  so it is a dead link like a not-found page: permanent, never sent to
  Jina from the origin, never host-cached (each page of a parked host gets
  its own request and the same verdict), under the kill switch.
- **Two bot checks' phrases** join `challengePhrases`: Google's
  unusual-traffic page ("our systems have detected unusual traffic from
  your computer network") and Fastly's client challenge ("a required part
  of this site couldn't load"). The 2 KiB bound on raw text
  (`maxChallengeBytes`) is unchanged.
- **A sign-in form** (`ErrLoginWall`, page-level, the last check): a page
  read whole, with at most 1 KiB of own text (`signInPageBytes`), whose
  own lines beginning within its first 256 bytes of own text
  (`signInFormBytes`) include a password field ("Password", optionally
  followed by ":" or "*", any case) and a sign-in line (`loginTitleRE`:
  "Log in", "Sign in", "Join now"). Reason: `page is a sign-in form`.

`judgePage`'s order, pinned by `TestJudgePage_Order`: dead link (URL
rules, not-found title, then the opening's not-found notice and parked
domain) → bot challenge → error page → login wall by redirect → no article
→ thin → login title → sign-in form. No sentinel is new: the verdicts are
`ErrDeadLink`, `ErrAntiBot` and `ErrLoginWall`, which `FailureCause`
already classifies. The fallback policy, `settle`, `hostVerdict`, the host
cache and `judgeRedirect` are unchanged: a text verdict is page-level and
never cached; a dead link from the origin never goes to Jina; an anti-bot
or login-wall verdict from the origin goes to Jina; a text verdict on a
Jina answer is a dead link (final, uncached) or `errJinaRejected`. The
not-found and parked rules obey `fetcher.native.dead_link_detection`; the
challenge phrases and the sign-in form, like their title rules, have no
switch. Reasons quote page text only through `strconv.Quote`, cut on a
rune boundary to 120 bytes (`quoteLine`).

**Why text rules:** 73 of the 4,654 documents stored through Readability
or Jina on the 2026-10-03 backup copy are such pages, and nothing but
their text gives them away. Their titles are a site's name ("Medium",
"Quartz", "Instagram"), the URL asked for (Google's), empty, or not
English ("หน้าไม่พบ | JavaLobby"). Jina's answers keep link and image URLs,
so a page of a few words clears the 500-byte thin floor: wetwalls.ca's
parking page is 9,150 bytes around 199 of words, the sign-in walls 1.7 to
4.7 KB around 99 to 506. And Jina's answers carried neither its
target-status warning nor its CAPTCHA warning. They made interests of
their own: "Miscellaneous Search Queries" (21 documents, 19 of them
Google's page), "Domain Name Listings" (21 documents, 18 of them parked
domains) and "Meta Platforms Documentation" (11 documents, 9 of them
Instagram and Facebook walls).

- **Not-found pages (15):** Medium's 404 ×6, titled "Medium" ("PAGE NOT
  FOUND", "## 404"); The Week's, untitled; Bespoke's ("Bespoke
  Interactive", "# 404"); javalobby.org's ×2 under a Thai title; LinkedIn's
  ×2 ("Top Content on LinkedIn"); Quartz's Next.js 404; Advisor
  Perspectives' ("404 Error: Not Found", 19.3 KB into the answer); and
  bomatoronto.org's, through Readability under the site's name ("Page Not
  Found", "Sorry! The page you requested was not found."). 14 came
  through Jina, stored 2026-09-28 to 30 by builds that already judged Jina
  answers. The "Soft-404 titles" entry put a body rule off because an
  article can open with a 404 heading and the origin's text has no heading
  markers; measured, the line structure survives on both paths, the
  opening, own text only, keeps articles out, and a status code alone
  counts only as a heading.
- **Parked domains (21):** GoDaddy's and Afternic's sale pages ×12, the
  domain's name dropped, leaving "is for sale!"; HugeDomains ×2 and
  omegacoder.com through Readability; Namecheap's expired and
  just-registered pages; Hover's; easyDNS's; a search-ads parking page;
  and flappyroyale.io's news portal ("This domain name may be for
  sale."). No rule read "for sale": only a parked page under 500 bytes was
  ever caught, as a thin login wall rather than a dead link.
- **Bot checks (26):** Google's unusual-traffic page ×25, 530 to 1,580
  bytes, under the 2 KiB bound but in words no phrase matched, titled with
  the search URL; and PerlMonks' Fastly "Client Challenge", a title
  `challengeTitleRE` doesn't know.
- **Sign-in walls (11):** Instagram ×7, Facebook ×2 and LinkedIn ×2, under
  titles `loginTitleRE` doesn't match ("Instagram", "Facebook", "LinkedIn
  Login, Sign in | LinkedIn").

**Anchoring.** A rule reads whole lines, or a sentence that opens one, and
only in the opening, and only own text. A notice is what a page is when it
opens the page; further in, a page discusses or quotes one. A link's text
is never read: landingfolio's menu holds "[404](…)", and a story card's
title can say anything. Nor is a code block's, and a status code alone
counts only as a heading. Each bound, measured on the copy:

| Bound | Value | Flagged pages | Nearest page it keeps stored |
|---|---|---|---|
| `pageTextScanBytes` | 64 KiB | notices at most 19.3 KB into the text | none further in |
| `openingBytes` | 2 KiB of own text | notices at most 857 bytes in (bomatoronto; Quartz 495) | a Home Depot category's "Related Searches" at 6.9 KB, after a 591-byte line; HTTP Made Really Easy's "404 Not Found" at 7.9 KB, after a 693-byte paragraph; a business listing's "Related Searches" at 39 KB |
| `parkedNoticeBytes` | 256 bytes of own text | parking notices at most 54 bytes in (flappyroyale.io's, under its sign-in box) | none in the library; a search results page's "Related searches" after ten results, about 1.8 KB in |
| `proseLineBytes` | 200 | at most 86 bytes in the longest line before a notice; LinkedIn's notice is itself the first long line (217 bytes) | no matching line within 2 KiB after a long line |
| `signInPageBytes` | 1 KiB of own text | walls of 99, 171 and 506 bytes | a sign-up form (577, no sign-in line), a parking page (628, a dead link first), then pages of 7.7 KB and more with a password field near their top (Letterboxd, Stack Overflow's questions under their sign-up dialog) |
| `signInFormBytes` | 256 bytes of own text | password fields at 70, 98 and 209 | Pinterest's sign-in modal over a deleted pin (331), an image page with a portfolio sidebar (511), IGDA Toronto's public Facebook page (541), all under 1 KiB |
| `maxChallengeBytes` | 2 KiB, unchanged | Google's pages 530 to 1,580 bytes | — |

Google's page quotes the search URL twice, so a search URL longer than
about 870 characters would push Jina's answer past 2 KiB and out of the
phrase rule. The library's longest is 642, a 1,580-byte answer. Measuring
the bound on own text would change nothing in the library, so it stays.

**Precision, with the shipped code** on the 2026-10-03 backup copy (5,237
fetched documents; read-only). 4,654 reach `judgePage`: 3,370 through
Readability, whose text was rebuilt from the stored markdown (goldmark,
then go-readability's `render.InnerText`, with `url_canonical` as the
final URL), and 1,284 through Jina, whose stored markdown is the body
`judgeJinaAnswer` saw, judged through it with no warnings. The other 583
(310 GitHub API, 235 yt-dlp, 38 local PDF) never do. The old code flags
none of the 4,654; the new code flags 73 and changes nothing else: 15
not-found notices and 21 parked domains (`dead_link`), 26 bot checks
(`anti_bot`), 11 sign-in forms (`login_wall`). They cover 67 of the 69
documents a review of the library's junk had listed (the 2 left are
Remodelista's paywall, below), and 6 it hadn't, each reviewed and each
junk: flappyroyale.io's parking page (`9225309d`), PerlMonks' Fastly
challenge (`134358fc`), Advisor Perspectives' 404 (`d932fad2`), Quartz's
404 (`3ef6449a`) and LinkedIn's sign-in wall ×2 (`9dffe504`, `e1e79ffd`).
No article is flagged. `judgePage` costs at most about 8 ms on a
pathological 64 KiB text. The worst is many short lines (64 KiB of 2-byte
lines), each run through the line's regexps; one long line of brackets or
escapes costs up to 7 ms, and 64 KiB of menu lines about 2 ms.

**Accepted risks.** A page whose opening holds a not-found template line
is judged dead, as such a title is: an article that prints a notice on a
line of its own before its first paragraph, or a status-code cheat sheet
that opens with its list. A notice quoted as a blockquote ("> 404 Not
Found", as a Stack Overflow question quotes the error it got) is never
read. A page with a heading that is a status code alone is judged dead
too, a count given as a heading among them: LessWrong gives a post's karma as a heading
(`25e40719`'s "# 480"), so a post there whose karma is 404 or 410 when it
is fetched is judged dead, and stays so until a forced refetch finds
another score. From the origin, code is plain text, so an article that
shows a not-found page in a code block before its first paragraph is
judged dead there, though not through Jina. A page that says in its first
256 bytes of own text that this domain is for sale is a parked domain, an
article headlined "This Domain Is For Sale" among them. None of the 4,654
is misjudged this way (`25e40719`'s karma was 480). A short page led by a
sign-in form is a login wall, and goes to Jina like any; `loginTitleRE`
also takes "Join now" and "Join LinkedIn" as its sign-in line, so a
sign-up form of 1 KiB or less with a password field is one too.

**Not done** (what the rules still miss):

- Remodelista's metered paywall ×2 (`62b3ba53`, `d6ec70f0`, "You have
  reached your limit of three (3) free posts…", 2.3 KB of own text through
  Readability): a paywall phrase list would be one site's wording, and
  paywalled pages elsewhere often carry a real excerpt.
- Notices outside the grammar or in another language without a status
  line: "can't seem to find", "We can’t find that idea!",
  battle.net's "Profile Unavailable" ×4, X's "Account suspended",
  Substack's "This post didn't load". Several are temporary, and a dead
  verdict is sticky.
- Notices after a line of prose or past 2 KiB of own text.
- A not-found page from the origin whose only notice is a status code
  heading (`<h1>404</h1>`): the origin's text has no heading markers.
- Sign-in walls with a longer preamble, another password label, or an
  email-first flow (Notion, X).
- Parking pages in other words, and domains taken over for spam
  (framelessgrid.com's SIP777 casino portal, `80029943`).
- A bot check over 2 KiB.

**Existing documents:** the rules judge fetches, not what is stored. The
73 stay `fetched` until refetched; they are fetched, so no `--force` is
needed. With the daemon running this change:

```sh
for id in <ids>; do curio refetch "$id"; done
```

- Not-found notices, expected `dead` (`dead_link`):
  2b976ea6-0c21-4589-ab97-23ed74afeb56 2b977a4b-665e-438e-9d4a-f5585d633a01
  3219e2f1-4b64-4735-b1ee-c43a1c01825c 3ef4dab9-78f7-484f-bba1-1e91892641f7
  3ef6449a-da28-4677-8720-48aa14145f48 40303d6f-8998-4980-b128-639e628f139d
  70233b2a-896a-4022-98ad-e8b5bfa42f71 7ad607e2-b500-490f-9d3b-12aa6aa34e69
  abd7cbb3-0eac-42c3-a454-0d872c6dc509 b94ed7b8-186b-4ff9-8b21-13fabb111e41
  bd70c172-6209-49df-b2e5-660c7a685599 d932fad2-60ab-4b63-b932-621566608933
  e0251c27-6579-46b4-8997-9008f31b1caf fa2c61a1-2ddf-4b84-b462-2f4e69662dec
  fddead8b-a3d5-4dbb-997e-d82eb01b394b
- Parked domains, expected `dead` (`dead_link`):
  0fce584d-7ebb-49e5-8acc-84a00a959657 1e76c977-22a2-4218-81be-f9c048edf4cd
  397f7090-58da-48fa-863d-89515183f04e 433eee32-d2a2-41cc-aaff-ac2eac2a5db9
  4e100057-3231-4cd1-af99-04b646bd42e5 4e50ec79-9a7a-4d41-9040-6e3282da5c70
  5b84d76b-ab37-43e5-abb5-719b5287b463 64748bb6-f61c-4ca2-8dc5-4a83f88d4548
  7808c035-24fc-4860-aec4-2e5bb2fb2945 890a18f8-300f-45da-8658-cf1a9651b5a9
  9225309d-e421-4c83-b7f1-08724dcfd30b 94a6dad7-0406-493b-8561-0fe0faf5d3ec
  9a0e2012-e670-444e-bcd5-7f1578518990 9d57ab40-7fbc-4fd8-ae2a-2bcbb70e25e6
  b576dab4-565d-47d8-993a-ad1b5a40c55c b767c66d-0e10-496e-a900-2f70e563da07
  c57c0457-bb9c-48d8-84f0-a769fcb927c1 cd8c2a4e-28ae-4e85-bb12-6fbf3a445810
  d173f130-6192-4e98-a039-f5304779dadb d4dc7287-2d5e-4591-a836-bba2abb0dc83
  fcb281ad-5239-4197-a90e-9bb06b4a7fb5
- Bot checks, expected `failed` (`anti_bot`):
  0ea6643c-002c-4437-9045-2d4986bfecd2 11d6697f-3dc0-4224-abaa-349bb7d71aef
  134358fc-fdcc-40c7-b6af-db64fc003e3b 16cc372f-3440-4251-ac0c-6d8a9de4bc11
  1f0dcd53-53f2-48a7-903e-659b4070913a 2d6f2339-47fd-4f83-b653-b43cd1a98cb5
  2ee8f08a-fc7e-44ee-997a-907b8061b5fd 2fbb7da1-0d65-4f89-95a7-d8125042b048
  3969830b-25c1-49de-9cda-61079028ce36 3c4f78e9-b725-4a29-bc66-2551ebc802e8
  5153994a-abad-4054-ba94-976d1f8cd119 5b240d4c-9f14-40a9-940c-1233435ce65f
  5e7acd77-de98-4189-b939-a7d8bb224695 602d34de-269a-4fdf-8c11-312148a92ee4
  63848523-c6e0-4b71-bd96-cc164f4c5eb1 7be2d137-c5b0-4850-bd72-1a92f6a5f86b
  7eea839b-1774-44d7-b5a0-caee07890861 8aff06a0-fe38-449a-9b45-9712c37cecba
  af9dc68c-b44c-41d0-b051-4b72d9ce6103 c5e4a490-26f3-49f0-9983-6683ba71ab3c
  d6cb1653-dfdd-4574-b2c7-0e56941836a7 e0d420ce-0e8b-44a6-b618-766a8c91e77d
  e7cc72ef-90e0-4599-9526-475668728e9d f12e42ac-f2f6-4ae2-91aa-2300b9e1b46e
  f314b37f-4d49-4f49-8684-0a81689320b3 f4e4af63-72c8-4f68-9753-72dc650aef5d
- Sign-in forms, expected `failed` (`login_wall`):
  0866b543-a601-4fba-aa30-bd4c2a7b9792 2992618b-de69-4abd-ab7c-b71b9c8f153a
  2a8f7621-9949-4d80-b6f9-6783ec42438f 4ff5da11-353d-406a-88de-7b7066f222fa
  56b91dcd-cc06-48f2-afdd-c51bdd7d8289 685992c4-46dc-42d7-94b8-304f5f9690a3
  748c75b7-94c4-4e5f-9cd0-543649baf595 9dffe504-1bd5-41b0-9b85-eafc553d0f4d
  bd2f791a-823c-4b14-9e07-767ac77b3247 daa45ce8-bf38-473b-91c0-c6b7b2045a94
  e1e79ffd-6146-4fe9-aa44-37c0ae2210bb

A refetch asks the site again and judges what comes back, so a site that
changed decides otherwise. Not-found and parked pages should end `dead`.
Google's pages should end `failed` (`anti_bot`) unless Google serves its
results, and the walls `failed` (`login_wall`). Each leaves search once
its refetch ends it failed or dead. The interests change only at the next
rebuild, `curio interests rebuild`: 73 changed documents are below the
automatic rebuild's threshold (5% of the library, 262 documents).

---

## Interest map: two views of each regrouping, drawn when it is built

**Decision:** every rebuild draws an interest map of the grouping it
built, and commits it with the run: the **document map** (every document a
point, similar documents together, each area and interest a label anchor)
and the **zoom view** (each interest a circle just large enough for its
documents, packed by similarity inside its area's circle, plus a disc for
Unsorted, every document a dot inside its circle). Both are in a square of
side 1000 (`store.MapExtent`, `layout.Extent`). The layouts live in
`internal/insight/layout`, standard library only (depguard
`layout-stdlib-only`) and blind to curio's types; `insight.BuildMap` feeds
them a grouping and a previous map and keys the result for the commit.
Migration 018 keeps the map on rows a run already has, the placer gives
documents placed between rebuilds a place on it, and `GET
/v1/interests/map` serves it whole. This is dashboard phase 3's back end;
the pages that draw it come next.

### The document map

UMAP (McInnes, Healy and Melville, 2018), as umap-learn does it, on the
grouping's own neighbour lists:

- **Graph.** `LouvainGrouper` already finds each point's top 20 neighbours
  by cosine in the centred space; `Grouping.Neighbours` now carries them
  out, remapped to input order into fresh slices (the fixture tests share
  one set of lists between groupings, so nothing edits them in place), and
  `MergeNearDuplicates` passes them on. `FlatGrouper` leaves them nil and
  `BuildMap` makes the same pass itself, inside the map's deadline. Each
  point's fuzzy set reads its first 15 (`n_neighbors`): distance 1 −
  cosine, ρ the nearest positive distance, σ by bisection so the
  memberships sum to log2 k (at least 1e-3 of the row's mean distance), a
  row of one neighbour or none weighing 1; the union w = a + b − ab; edges
  below max w / epochs dropped. Edges are built from sorted slices, never
  a Go map.
- **Cold start: PCA, not spectral.** The points' first two principal
  components, by block subspace iteration (4 vectors, Rayleigh–Ritz, at
  most 300 iterations) on the float32 rows with the mean subtracted on the
  fly: no float64 copy of the matrix, which is 43 MB at 5,237 × 1,024.
  Each component's sign is fixed by its third moment, each axis scaled into
  [0, 10], plus 1e-4 of seeded noise (umap-learn's convention). The
  scatter sums fixed chunks of 256 rows in parallel and adds them in
  order, so the result doesn't depend on GOMAXPROCS. The design review
  re-ran the prototype on the owner's 5,237 documents: PCA and spectral
  starts give the same NP5 (0.294) and area purity@5 (0.863 and 0.862),
  but PCA moves less between seeds (1.64% against 2.14%) and in a cold
  re-layout after 5% dropped (2.65% against 5.40%), about as much warm
  (1.34% against 1.29%), starts in 0.68 s against 1.13 s, and needs nothing
  for a disconnected graph, where deflating only the trivial eigenvector
  collapses each component.
- **Descent:** umap-learn's `optimize_layout_euclidean`: a = 1.577, b =
  0.895 (min_dist 0.1, spread 1), 5 negative samples per positive one,
  gradients clipped to ±4, the rate decaying linearly. Cold: 500 epochs up
  to 10,000 points and 200 above, from rate 1. The prototype's 200 epochs
  on the owner's library gave NP5 0.278 in 3.2 s of descent against 0.294
  in 7.1 s; at 6.1 s for the whole cold map, 500 stay. One goroutine, one
  seeded PCG, edges in key order, the context checked every epoch.
- **Warm start** (`warmEpochs` 200 from rate `warmAlpha` 0.1): the prior's
  places, scaled so the median length of the graph's edges between points
  that have one is 0.3, the median a cold descent ends at (measured 0.31
  to 0.38 for 300 to 5,000 points). The spec's start, the prior scaled
  into the cold start's [0, 10] box, squeezed the map: the descent spent
  its epochs expanding it again and moved the shared points about ten
  times as far. A point without a place starts at the similarity-weighted
  mean of its neighbours that have one, propagated in passes; any left
  start at seeded spots near the centre. The rate: the prototype's 0.25
  moved the owner's documents 2.60% served · 1.83% aligned after 5% added
  (2.57% · 1.55% mixed) at NP5 0.302; 0.1 moves them 1.81% · 1.35% (1.31%
  · 1.31%) at NP5 0.309.
- **After the descent:** points beyond the 97th-percentile radius from
  the coordinate-wise median are pulled in softly, r′ = r97 + s(1 −
  e^−(r−r97)/s) with s = r97/4, so a few outliers don't shrink everything
  else. With a prior sharing at least 3 points, the least-squares
  similarity transform (rotation or reflection, uniform scale, translation)
  maps the shared points onto their previous places. Without one, the
  principal axis is turned horizontal, each axis's sign set by its third
  moment.
- **The box.** A view is fitted to the square with one uniform scale,
  centred, its longer side spanning 95%. An aligned map is left in the
  previous map's frame instead while it lies inside the square and its
  longer side spans at least 90% of it: refitting it scales and moves every
  shared point, which the "served" displacement counts and the owner sees.
  It brought the served displacement down to the aligned one in five of
  the report's six draws below; the sixth outgrew the square and was
  refitted (2.79% served, 1.41% aligned).
- **When it starts warm, aligns, or is reused.** Warm only when the
  prior's map is built, its params (`MapParams`: every layout constant,
  `MapSeed`, the map's version and `center`) equal this build's, no
  re-embedding owes a fresh rebuild and no drift is reported; aligned
  whenever the prior's map is built, warm or cold, so a re-embedding keeps
  the orientation the owner knows. Reused verbatim when it may start warm,
  nothing changed (`Changes(prior).Total() == 0`) and the points are the
  prior's assigned documents: a warm descent restarted from a settled map
  still moves it, and repeated no-op rebuilds would blur it (and
  `TestEngine_FixtureLibrary` holds an unchanged warm rebuild's rows
  equal). A reused or warm-started map is kind `warm`. Map params stay out
  of `runParams`: changing the layout never makes a grouping fresh or owes
  a rebuild. `layout.Params` names every constant that changes what the
  views draw, streams and tolerances included (a test parses the package
  and refuses one it leaves out), since a missed one would let a prior
  drawn with the old value start, or be reused as, the new map. A map
  allowed to start warm from a prior that shares none of its documents
  (the library replaced) starts cold, and `DocMap` says so: its kind is
  `fresh`.
- **Anchors:** an interest's label anchor is the coordinate-wise median of
  its members' places, snapped to the member nearest it (ties to the lower
  document ID), so a label sits on its documents even when the median
  falls between two islands; an area's, the same over its interests'
  members.

### The zoom view

Laid out in dot radii (a dot is a circle of radius 1) and scaled at the
end, with the dot radius at most 1% of the square, which keeps a tiny
library small and centred.

- **Sizes from contents.** An interest's circle holds its members and
  loose fits; its radius is the smallest whose hexagonal lattice of
  spacing 2.2 (a 10% gap) has ⌈1.15·m⌉ slots within r − 1. Unsorted's disc
  is sized the same way for at least 24 documents, so documents placed
  later have room.
- **Placing circles.** Distances are 1 − cosine of the centroids (an
  area's: the unit mean of its interests' members). Inside an area its
  interests are placed by classical MDS; at the top, the areas (the
  interests in the flat shape) by metric MDS, stress majorization seeded
  by classical MDS. Classical MDS of unit vectors' chord distances is
  their first two principal components (the double-centred squared
  chords are the centred vectors' Gram matrix), so it is found by the
  document map's subspace iteration through the centroids, linear in
  their number an iteration and checking the context, never by
  decomposing the n×n matrix: the Jacobi solver that first did was cubic
  and blind to the deadline (28.7 s for a flat view of 800 interests, 23 s
  to notice a 2 s deadline). Distances are scaled so the median pair of
  circles about touches. Then they are packed (interests 3 apart, areas 8, an
  area's rim 3 off its interests) and, cold, compacted: each circle pulled
  toward the centroid, a move taken only when it overlaps nothing, for up
  to 40 rounds. Unsorted's disc goes to the right of the content, 12
  apart, vertically centred.
- **Packing ends overlap-free, provably.** Overlapping pairs are pushed
  apart for up to 100 sweeps; if any overlap remains, the starting
  positions are spread by 1.25 about their centroid and it starts again.
  Coincident centres are spread apart first, so the starting centres are
  distinct, each spread multiplies every distance between them, and a
  start with no overlap at all comes in a bounded number of tries. Each
  push moves a pair 1e-3 past touching, so sweeps end rather than creep
  toward it. Packing is the last step that moves circles at every level,
  cold or warm: compaction only takes moves that overlap nothing, an
  area's interior and the fit to the map are translations and one
  uniform scale, and the warm view packs once more after its alignment
  (below), so no overlap rests on a transform being exact. The view then
  checks its output before returning it: the circles of each level
  apart, every interest inside its area, every dot inside its circle, up
  to what rounding to 0.01 can take (0.035). A view that fails the check
  fails its map, which the rebuild commits as failed rather than drawing
  overlapping circles.
- **Dots on lattice slots.** An interest's documents aim at their own
  first two principal components, turned or reflected (weighted Procrustes)
  toward the circles of its 6 most similar interests, with radii replaced
  by rank so the circle fills evenly; in rank order, each takes the free
  slot nearest its aim (ties to the lower slot). No two dots can overlap,
  at O(m × slots). Unsorted's documents aim toward their nearest
  interest's circle, the more similar nearer the rim; one with no nearest
  interest goes by the golden angle of its rank. This replaces the
  prototype's all-pairs relaxation, which had no guarantee of ending
  overlap-free and cost O(m² × 300), and its disc packing around label
  footprints from a made-up font metric: labels are the page's to place.
- **Warm** (the prior's map built, with the same params and shape): a
  carried group starts at its previous place, in the prior's dot radii; a
  new one at its lineage predecessor's place (the old identity it shares
  the most members with), else beside its 3 most similar placed groups and
  then by stress majorization against the placed ones, which stay put. The
  distances it majorizes are scaled by the least-squares fit of the placed
  groups' distances to their places, held within 4 times the touching
  scale either way (`maxFitScale`): two placed groups at nearly one place
  but drawn apart inflated the fit without bound (a review stress test
  found an area's two interests 0.00018 apart in cosine and 39 dot radii
  apart on the map, a fit of about 222,000 and an area 239,027 dot radii
  wide), and a warm view never compacts. On the owner's library the fit is
  1.1 to 1.9 times the touching scale (16 warm levels of the cluster
  report), so the bound changes nothing there. The
  circles are packed without compaction; the whole is then turned (or
  reflected) and moved back onto the previous places, never scaled (radii
  are absolute), and packed again. The turn is the plane's closed-form
  best rotation or reflection, exact at any rank. Two placed groups at a
  level, or collinear ones, give a rank-one fit, for which the factor
  first used (through mᵀm's eigenvectors, dividing by the smaller
  singular value, there rounding noise) was far from orthogonal in 494 of
  1,000 random cases: it squeezed the new groups toward the line through
  the placed ones, and the review found overlapping circles in 13 of
  17,696 random warm hierarchies. A rotation wins unless a reflection
  fits better by 1e-9 of the fit, since a rank-one fit can't tell mirror
  images apart and rounding alone would pick one. `FuzzZoomWarm` draws
  hierarchies warm from another's view, groups renamed with and without
  starts. The spec's short refinement of the carried places toward
  MDS's distances (5 iterations of stress majorization) moved the engine
  fixture's interest centres about eight times as far (4.5% to 6.0% of
  their diameter, aligned, against 0.3% to 0.9%), so there is none. An
  interior group starts from its previous place only when it lay inside
  its area's previous circle.
- **Reused** with the document map when the grouping is unchanged too:
  the same identities under the same parents, every document in the same
  interest and area with the same fit.

**Determinism.** A layout depends only on its input: documents are worked
on in key order, groups in the order of their contents (more documents
first, then the smallest document key), never by their keys, since a new
group's key is a fresh UUID each run. Parallel sums use a fixed partition
and add in order; the descent is single-threaded. Two engines over one
library in two databases commit identical positions though their new
identities differ (`TestRebuild_MapIsDeterministic`), and the layouts are
the same under shuffled input, renamed keys and `GOMAXPROCS(1)`. Tests
never assert golden coordinates: arm64 fuses multiply-adds, so another
architecture may differ in the last bits.

### Storage (migration 018)

Nullable columns on the rows a run already has, not the design's separate
`interest_map_points` table: that would need a second insert per document
in `CommitRun`, would turn `PlaceDocument`'s single guarded upsert into a
two-statement transaction (every `BeginTx` here is `BEGIN IMMEDIATE`, so
it would hold the write lock across both), and would need its own index
for document deletes. Columns go wherever their row goes, the runs'
cascade included. The map's status lives on `interest_runs`, so
`LatestRun` answers whether there is a map without another read; NULL is
"none drawn" (a run from before 018, or one not done), `failed` a map that
was attempted, with its error, time and params (a failed map has no kind:
nothing was laid out). A CHECK on the last run column ties them together,
another keeps a group's five map columns and a document's four all set or
all NULL. `similar` (an interest's 3 most similar interests, `[{id,
cosine}]`) is stored at commit, map or no map: computing it per request
costs O(k²·d), about 34 M multiply-adds for the owner's 182 interests, and
a done run never changes. 018 only adds columns, so it runs in goose's
transaction (10 ms on the owner's copy); its down drops them in reverse
order, since SQLite refuses to drop a column a later column's CHECK names,
and leaves `sqlite_master` as 017 had it (a test compares them). `CommitRun`
writes it all in its one transaction; `checkCommit` refuses, before
anything is written, a map (built or failed) without valid JSON params,
a built map missing a place, a place off the map or not finite, a circle
or dot radius not above 0, places without a built map, and a similar
list on an area, longer than 3, naming anything but another interest of
the commit, or with a cosine not finite. The map's
reads index a similar interest among the run's interests, so an area in a
list would be a run they answer 500 for. A group says which it is: a
commit's groups carry their identity's `Level` (the store refuses one
without, and one that isn't its new identity's), since nothing else in
a commit tells an area that holds no document from an interest.

### Failure, and the bound

The map runs between carry-over and labelling (`group → map → label →
commit`), so the points it needs are let go before labelling, which may
take 15 minutes. It runs under its own 2-minute deadline. An error, the
deadline, a panic (recovered, its stack logged), a zoom view that fails
its own check (circles apart, contents inside) or a map `Map.Validate`
refuses commits the run without one: `RunMap{Status: failed}` with one
line of at most 512 characters, one WARN "interests: map failed", and
`map=failed` on the "interests rebuilt" line. A map is the grouping's
picture; losing one is no reason to lose the grouping, and the next
rebuild draws it again. Only the rebuild's own cancellation fails the run,
as before. A failed map makes no rebuild due on its own ("Automatic
rebuilds" says why); it shows instead, as `interests.map` on healthz
(status failed, with its error) and as a `!` in `curio doctor`, whose fix
is `curio interests rebuild`, the logs, and `insight.map: false` if it
keeps failing. A run that drew no map, by contrast, owes one: the
scheduler makes a rebuild due to draw it.

### Placement

A document placed between rebuilds needs both places: the API sends no
nulls, and computing them per request would need every placed document's
vector. With the run's map built, the placer searches the document's
neighbours (`VectorSearch` of its mean vector, 50 chunks, itself excluded,
each document at its nearest chunk), reads their places (`MapPositions`,
one statement on both primary keys), and takes the 5 nearest that have
one. Each weighs e^((n − n₁)/0.02), where n is minus half its squared
distance (cosine − 1 for unit vectors) and n₁ the nearest's: a document
identical to a mapped one sits on it, and one between two sits between
them. The document map's place blends all five; the zoom view's only those
in the same circle (its interest's, or Unsorted's), so the dot stays
inside, the circle being convex. Each is then moved 0.4% of the map (one
dot, in the zoom view) at an angle from FNV-1a of the document's ID, so it
never lands exactly on another, kept inside its circle and the map, and
rounded as the layouts round. Without neighbours (none mapped, the search
or read failed, or the sweep's budget spent): the anchor of the interest
it joined (its nearest, in Unsorted), and in the zoom view a point halfway
out at its hash angle in its interest's circle, or seven tenths out toward
its nearest interest in Unsorted's. A failure there never fails the
placement: one WARN per run, then DEBUG, and DEBUG alone when the
placement's own context ended (the daemon stopping mid-index), which the
placement reports itself. The search and read have 2 s of their own
(`neighbourTimeout`, 50 times the measured search), so one that stalls
falls back and leaves the rest of the placement's 10 s to its write. A
sweep searches for at most 60 s (`sweepNeighbourBudget`), the rest taking
the fallback, so a large sweep still fits its 2 minutes. It writes as it
goes, 64 placements a batch (`sweepBatch`: about 2.8 s of searches at 44
ms each, against a write of a few milliseconds), each batch a short
transaction of its own, so the write lock is never held across a search,
and a sweep that fails or runs out of time keeps all but its last batch:
it returns what it wrote with the error, and the next sweep, at the next
start or rebuild, places the rest. It doesn't write its last batch on a
detached context after its own ended: that batch is redone, and stopping
the daemon stays as quick as it was.

On a copy of the owner's library (50,278 chunks), through the store:
`Place` took 44.0 ms median and 49.6 ms p95 with the map, against 0.74 ms
and 6.3 ms without one (the design measured 2.1 ms before maps; the
search is the cost, 40 ms median, 43 ms p95). A sweep of the 262
held-out documents placed them all in 11.8 s, every one with places, 186
into interests.

**Placements off the map.** curio 2.5.x starts on a database 018
migrated, since goose ignores versions it doesn't know, and knows nothing
of the map: its index jobs and its sweep place documents with no place,
and its `PlaceDocument` re-places one (`ON CONFLICT DO UPDATE`) without
touching the place columns, so a placement it moves into another interest,
or into Unsorted, keeps a dot drawn in the circle it left. A placement is
*off the map* when its run's map is built and it has no place, or its zoom
dot's centre lies outside its circle, its interest's or Unsorted's disc
(dx² + dy² > r²). One SQL predicate (`offMapSQL`) says so for both
`Unplaced`, which lists such placements with the documents the run neither
assigned nor placed, and `PlaceMany`, whose upsert replaces such a row
with a placement that has a place and keeps any other. So the sweep, at
the daemon's start and after each rebuild, repairs them; with no built
map nothing is off it, and both answer as before. The placer never draws
a dot outside its circle (its room is r − dot − 0.01, and rounding moves a
dot at most 0.0071), so nothing it placed itself is placed again
(`TestPlacer_NoChurn`). `Unplaced` stays a range of
`idx_documents_tenant_indexed` with primary-key seeks (the assignment, the
placement, its run and its group): 54 ms on a fresh copy of the owner's
library, and 4 ms after, with all 5,237 fetched documents in range.
`PlaceMany`'s conflict reads the run and the group by their primary keys.
`PlaceDocument`, the fast path, places anew whatever it finds, as before.
Tests write 2.5.0's two statements verbatim (`sqlitetest/curio250`) and
hold the store, the endpoint and the sweep to each shape they leave; on a
copy of the owner's library a start's sweep repaired 100 placements 2.5.0
made in 5.1 s, and 5 it moved in 1.1 s ("Going back to 2.5.x", below).
Placing with the map off leaves placements with no place too, which the
sweep repairs the same way once the map is on again.

### The API

`GET /v1/interests/map` answers the latest done run's map whole, unpaged
(the third exception to cursor paging): one run per response, about 1 MB
for 5,000 documents. `map` holds the kind, `took_ms`, `extent`,
`dot_radius` and Unsorted's disc; `areas` in `TopGroups` order and
`interests` area by area in `ChildGroups` order, each with its circle,
anchor and (an interest) up to 3 `similar` as indexes into `interests`;
`documents` as parallel columns, so the keys aren't repeated per
document: `id`, `title` (title, else the newest bookmark title, else the
host, else the URL; whitespace collapsed, at most 200 runes), `host`,
`interest` (the interest it is in, −1 for Unsorted), `nearest` (an
unsorted document's nearest interest, −1 otherwise, placements into
Unsorted included: they record none), `area` (its interest's area, −1
with none or in the flat shape), `fit` (`member`, `loose`, `unsorted`, or
`new` for placed since), `similarity`, `mx`, `my`, `zx`, `zy`. The
prototype's `interest`, meaning the nearest interest for an unsorted
document, made one column's meaning depend on another's; `nearest` costs
about 15 KB on the owner's library. Assigned documents come first, then
placed ones, each by ID; documents now failed or dead are left out, and so
is a placement off the map (above) until the sweep repairs it. That was a
500 for the whole map at first, as an inconsistency, and 2.5.x writes
them. Computing a place per request instead was turned down: a placement
into Unsorted records no nearest interest, so its fallback would sit at
the map's centre, and a place stored nowhere would differ from the one
the repaired row comes to hold, and hide that it needs repair. Assigned
documents aren't checked: `CommitRun` refuses a built map that leaves one
without a place, and the zoom view checks its dots. No map
is a 404 `urn:curio:problem:interest-map-unavailable` with `reason`
`no_run`, `no_map` (a run from before maps) or `map_failed` (with
`map_error`), and a detail the page can show. It and the retired
interest's 410 now share one mechanism, an error that carries its own
problem body (`problemError`). No read path can open a transaction (each
would be `BEGIN IMMEDIATE`), so a rebuild that commits mid-read is caught
by reading `LatestRun` again: a different run is read once more, and a
second change is answered as read, as `GET /v1/interests` does.

### The switch (`insight.map`)

`insight.map: false` turns the map off and leaves the grouping on, which
`insight.enabled: false` couldn't: a rebuild draws no map (no `BuildMap`,
none of its 2 minutes) and commits its run without one, as a run from
before 018, saying `map=off` on its line; a placement gets no place,
searching for no neighbours and reading no places; `GET
/v1/interests/map` answers 404 `map_off` without reading anything, its
detail naming the setting; and the scheduler owes no map, its snapshot's
map saying `off`, which doctor passes. On the owner's library that saves
the map's 5.6 to 6.1 s cold and 2.8 to 3.0 s warm a rebuild, up to the
2-minute bound on a library too large for it, and `Place`'s neighbour
search, 44 ms median a document against 0.74 ms without. `similar` is
still written: it is the grouping's, a few milliseconds, and keeping it
makes a run the same whether the map is on or off.

Turned on again, a run committed with the map off has no map, which makes
a rebuild due to draw it, and placements made meanwhile into a run whose
map was built before have no place: they are off the map, and the sweep
places them again. Every field that carries the switch is a negative
(`insight.Config.MapOff`, `PlacerOptions.MapOff`,
`SchedulerOptions.MapOff`, `api.Deps.MapOff`), so a zero value keeps the
map on, and every test, `cmd/clusterreport` and the engine fixtures draw
it as before. The reason is `map_off` of its own rather than `no_map` with
a detail: a client acts on `reason`, and `no_map` says a rebuild to draw
the map is due and `curio interests rebuild` draws one now, neither true
while it is off. A `RunMap` is built, failed (always with an error: one
that says nothing is "the map failed without saying why"), or nil for
none drawn, so a map that was never drawn can't be recorded as failed.
`TestDaemon_TheMapSwitch` runs it end to end: a home with the map off
groups an import unasked with no map (404 `map_off`, healthz map `off`);
started again without the key, its daemon queues a rebuild unasked
(trigger `auto`, nothing changed), and the map answers with every
document. It adds 2.6 s to `make test-e2e`.

### Measurements on a copy of the owner's library

A fresh `.backup` of the copy the design measured (5,237 fetched
documents, a run of 29 areas and 182 interests, schema 17), on an Apple
M4 Max; never `~/.curio`, port 8765 or a checkout's `./bin`; labels by
terms, auto-pull off, both Ollama URLs on a dead port.

`make cluster-report` (3 min 12 s of wall time, 87 s of it the maps),
whose maps `TestReport_MatchesTheEngine` holds to the engine's (checked
once to fail when the tool draws with another seed):

| | measured | target |
|---|---|---|
| cold map, 5,237 documents | 6.1 s, 809 MB allocated, peak RSS 327 MB | ≤ 15 s |
| document map NP5 · NP15 | 0.299 · 0.364 | NP5 ≥ 0.27 |
| area purity@5, map · space | 0.869 · 0.869 | ≥ 0.83 |
| another seed, aligned | 2.36% | ≤ 3.0% |
| warm after 5% added: documents, served · aligned | 1.81% · 1.35% (worst 2.79% · 1.44%) | aligned ≤ 2.0% |
| warm after a mixed 5%: documents | 1.31% · 1.31% (worst 1.41% · 1.41%) | aligned ≤ 2.0% |
| warm after 5% added: interests' centres | 1.30% · 1.09% (worst 2.00% · 1.55%) | ≤ 1.5% |
| warm after a mixed 5%: interests' centres | 1.57% · 1.98% (worst 2.02% · 2.50%) | ≤ 1.5% |
| warm map | 2.9 to 3.0 s; NP5 0.309 (added), 0.311 (mixed) against a cold map's 0.299 | ≤ 6 s |

Displacement is the mean shift of the shared documents (or carried
interests' centres) over the previous map's diameter, as served and after
a least-squares similarity alignment; three draws each. The mixed draws'
interest centres miss the 1.5% by 0.07 points served. A mixed change
also removes documents: an interest that lost some gets a smaller circle,
and as radii are absolute its area repacks around it, which a rigid
alignment can't absorb (the similarity alignment, which minimizes squared
shift, reads 1.98%). The one variant measured that pulls carried
interests toward their similarities, the warm refinement above, moved
them eight times as far. A 5% addition, the common change, moves them
1.30%. These are the numbers with the centroids' principal plane as
classical MDS; with the Jacobi solver it replaced (the same layout up to
each axis's sign) they were 1.31% and 1.56%, the cold map 6.2 s.

A throwaway daemon on another backup: binaries from `make build
BIN_DIR=<scratch>`, a scratch home with the marker of PR 3's and a config
on a free loopback port, and the 262 documents PR 3 held out set pending
before the start. It applied 018 in 9 ms. `GET /v1/interests/map` on the
copy's run from before 018 answered 404 `no_map`. A requested rebuild
(4,975 documents, 28 areas, 174 interests) logged `map=built
map_kind=fresh map_ms=5951`, and the map answered 200 with its 4,975
documents (4,578 members, 7 loose, 390 unsorted) in 912,225 bytes, 39 ms
the first time and 14 ms after (the handler's time in the access log). A
second rebuild of the unchanged library logged `map_kind=warm map_ms=0`,
every position and circle the same. The 262, made fetched again, were
placed by the start's sweep once the drift monitor's 10 minutes were up,
in about 12 s; read from a second daemon run the same way, right after
its sweep, the map served all 262 as `new`, each inside its circle, 186
in interests and 76 in Unsorted. The scheduler's automatic rebuild
(`changed` 262) drew its map warm in 2,756 ms: 5,237 documents and 175
interests in 956,295 bytes, served in 32 ms; the 4,975 documents it
shares with the previous map moved 1.34% served and aligned, the 174
interests' centres 0.45% served and 0.28% aligned. Neither log has a
WARN or an ERROR.

Both were run again, each on a fresh backup, once the warm zoom view's
alignment was exactly rigid and packing came last (the final binaries).
Every map value in the report's JSON, timings aside, is the same as
above (the cold map 6.0 s, peak RSS 324 MB). The daemon's run is the same
too: 404 `no_map`, a fresh map in 5,853 ms (912,227 bytes, 52 ms the
first time), its reuse (`map_ms=0`, every position the same), the sweep
placing the 262 in about 12 s (every one inside its circle, 186 in
interests), and the warm rebuild in 2,757 ms (956,295 bytes) with the
same displacement, every position equal to the earlier run's: the fix
changed nothing on this library. No WARN or ERROR.

### The fixture tables

Floors are 0.03 under what was measured and bounds 1.5 times it.

| test | measured | floor or bound |
|---|---|---|
| `TestDocMap_KeepsNeighbourhoods` (1,600 points, 12 planted clusters of 6 subtopics in 4 areas, 48 dimensions, the clusters overlapping): NP5 | 0.182 | ≥ 0.152 |
| … cluster purity@5 (the space's 0.816) | 0.896 | ≥ 0.866 |
| … NP5 over the points' own PCA projection (0.032) | +0.150 | ≥ +0.10 |
| `TestDocMap_WarmIsStable`, 5% added, mean shift | 0.29%, 0.35%, 0.33% | ≤ 0.53%, and ≤ 3% |
| `TestZoom_WarmIsStable`, 5% added, interests' centres | 0.46%, 0.46%, 0.39% | ≤ 0.69% |
| `TestEngine_FixtureLibrary` (fixture library 1, through the engine, 5% added): warm NP5 | 0.223, 0.222, 0.224 (a cold map's 0.200) | ≥ 0.192, and ≥ the cold map's − 0.02 |
| … area purity@5 on the map | 0.995, 0.997, 0.996 (the space's 0.986) | ≥ the space's − 0.05 |
| … documents' mean shift | 1.20%, 1.46%, 1.23% | ≤ 2.19% |
| … interests' centres | 0.50%, 0.90%, 0.65% | ≤ 1.35% |

The engine's warm NP5 beats the cold one's (it starts from a settled
map), so that check is one-sided: no worse than 0.02 under it. The
neighbourhood test's library has its points spread twice as wide as the
stability tests' (`overlapping`): on those, whose clusters lie apart,
the map's cluster purity is 1.000 and so is the space's, and a floor at
0.97 would hardly ever catch the map losing its clusters.

### Tests and their times

`FuzzZoom` holds the zoom view's invariants over random small hierarchies
and `FuzzZoomWarm` over hierarchies drawn warm from another's view, their
seed corpora in `go test` (the first warm seed is the review's
reproduction of the rank-one alignment); gonum checks the PCA, classical
MDS, the Rayleigh–Ritz steps' eigensolver and the alignment's polar
factor (on full, rank-one and zero matrices) to 1e-6 or better
(test-only: no binary imports it or `quality`). The heavy property tests
skip under -race and run in `make test`'s race-free pass.
`TestReport_MatchesTheEngine` holds both of the report's maps to the
engine's, the warm one's zoom view included: it renames the engine's
first run's groups to the report's keys by their members, so the
comparison costs no further map.

Times under -race on the M4 Max, in `make test`, before and after:
`internal/insight` 14.8 s and 21.1 s, `internal/insight/layout` (new)
12.5 s (10.3 s alone), `cmd/clusterreport` 3.4 s and 8.3 s (7.3 s alone,
against the 2.8 s PR 5 recorded: it draws and compares two maps; its
tests now run in parallel), `internal/store/sqlite` 9.4 s and 10.0 s,
`internal/api` 20.5 s and 22.0 s. The race-free pass: `internal/insight`
5.6 s and 14.8 s (the fixture's maps), `layout` 9.8 s. `make test` took
1 min 14 s and 1 min 21 s.

### Going back to 2.5.x, and the upgrade

**The upgrade, measured.** A fresh `.backup` of the schema-17 copy (one
done run of 5,237 documents, 29 areas and 182 interests; no fetch or index
job pending or running; the queue unpaused) under this branch's release
build (`make build BIN_DIR=<scratch>`), in a scratch home on a free
loopback port, both Ollama URLs on a dead port, auto-pull off, labels by
terms. The daemon applied 018 in 10 ms. Its first healthz, 2 s after the
start, said `due` with `map` `none`, and the map answered 404 `no_map`;
"interests: rebuild due" said `changed=0`, `map_owed=true`, waiting for
the first embedding check. With Ollama down that check never concludes,
so the scheduler waited out its 10-minute grace and queued the rebuild
then (`waited=10m0s`, `trigger=auto changed=0 map_owed=true`); the library
had been quiet for a day, so it was settled. The rebuild committed 17 s
later: `trigger=auto kind=warm changed=0`, every area and interest kept
(29 and 182, none created), `labels_llm=0 labels_terms=0`, `read_ms=9114
group_ms=1985`, `map=built map_kind=fresh map_ms=5604`. The map then
answered 200 with all 5,237 documents (4,835 members, 2 loose, 400
unsorted) in 958,869 bytes, 52 ms the first time and 17 ms after, and
healthz said `current` with the map built (fresh, 5,604 ms). No WARN or
ERROR in the log.

**Going back to 2.5.x.** curio 2.5.x starts on a schema-18 database
without migrating: its goose ignores versions it doesn't know. It reads
and writes none of 018's columns, which their CHECKs allow all NULL, so it
commits runs with no map, places documents with no place, and its
re-placement of a document (the `DO UPDATE` of its `PlaceDocument`) moves
it to another interest, or into Unsorted, keeping the place it had. Back
on 2.6 none of that needs a hand: a run 2.5.x committed has no map, so the
map answers 404 `no_map` until the rebuild it owes draws one (Automatic
rebuilds), and placements without a place, or moved outside their circle,
are left out of the map until the start sweep places them again
(Placement). No database restore is needed. Before going back, remove
`insight.map` from `config.yaml`: 2.5.x decodes it strictly, so its daemon
refuses to start (one ERROR, "field map not found in type config.Insight",
and exit 0, which launchd's `KeepAlive {SuccessfulExit: false}` doesn't
restart) and its CLI's `Discover` fails on the same parse; both checked
with v2.5.0's binaries.

018's down needn't run, and neither goose's CLI nor `sqlite3` can run it
on a real library: SQLite checks the whole schema after `ALTER TABLE …
DROP COLUMN`, triggers included, and the chunk index's triggers need the
vec0 module neither loads (goose v3.27.0's CLI: "error in trigger
trg_chunks_delete: no such module: vec0"; `sqlite3`: "SQL logic error").
Through curio's own driver, which loads sqlite-vec, `DownTo(17)` on a
migrated copy of the owner's library took 41 ms (0.4 s for the process),
`PRAGMA integrity_check` said ok, and `sqlite_master` (type, name,
tbl_name, sql: 251 rows) equalled 017's.

**The round trip, measured** after these fixes, on another fresh
`.backup` of the schema-17 copy in a scratch home, this branch's daemon
and a copy of v2.5.0's (`/opt/homebrew/Cellar/curio/2.5.0`) taking turns.
Ollama was a stub answering `/api/tags` (the marker's model) and
`/api/version` alone, so each start's drift check concluded at once with
nothing to re-embed, rather than after the 10-minute grace; nothing asked
it for an embedding. Before the first start 100 fetched documents were
set pending, as if refetching.

1. This branch applied 018; healthz said `due` with `map` `none`, the map
   404 `no_map`. The rebuild the map owed was queued a second after the
   start (`trigger=auto changed=100 map_owed=true`: 100 is under the
   threshold of 262) and committed 17 s later, warm, with
   `map=built map_kind=fresh map_ms=5527`; the map answered 200 with
   5,137 documents.
2. The 100 made fetched again (indexed after that run read its vectors),
   2.5.0 started on schema 18 without migrating and its start sweep placed
   all 100 within a second: 100 rows, none with a place. (Its
   `/v1/interests/map` is a 404 for an interest named "map".)
3. This branch again: the map answered 200 at once with the 100 left out
   (5,137 documents); its start sweep placed the 100 again within 5.1 s,
   and the map then served all 5,237, the 100 as `new`, each dot inside
   its circle.
4. 2.5.0's re-placement needs an index job, so embeddings; it ran instead
   as 2.5.0's `PlaceDocument` statement verbatim, as the tests run it,
   moving 5 of the 100: 3 into the interest farthest from their own, 2
   into Unsorted. This branch, started again, answered the map at once
   without those 5 (5,232 documents); 1.1 s later its sweep had placed the
   5 again (`placed=5`), each inside its circle.
5. 2.5.0 again committed a rebuild on request in 11 s (`trigger=manual
   kind=warm`, all 5,237 documents), its run with no map (`map_status`
   NULL).
6. Back on this branch, healthz said `due` with `map` `none` and the map
   404 `no_map`. The 100 had been indexed 20 s before, so the rebuild the
   map owed waited for the library to settle, and was queued unasked once
   nothing had been indexed for 10 minutes (`trigger=auto changed=0
   waited=10m1s map_owed=true`). It committed 16 s later, warm, every area
   and interest kept (27 and 171), no label asked for, `map=built
   map_kind=fresh map_ms=5511`, and the map answered 200 with all 5,237
   documents (954,384 bytes, 39 ms).

No step needed a database restore, and no log has a WARN or an ERROR.

### Known limits

- A library above about 100,000 documents may not draw its map in 2
  minutes (cold, 5,237 take 6.1 s): the descent is linear in the edges,
  but the zoom view is quadratic in the circles of one level (stress
  majorization, the distances and the packing). A flat view of 200, 400,
  800 and 1,200 interests of 1,024 dimensions takes 0.5, 1.8, 6.4 and
  14.3 s, and notices a 2 s deadline within 10 ms. Past the bound it
  rebuilds without a map, and the endpoint says so.
- A run from before 018, one curio 2.5.x committed, or one made with the
  map off has no map until the rebuild it owes (`no_map` meanwhile): due
  at once, queued once the library is quiet and the drift check has its
  first verdict, 10 minutes after a start whose Ollama is down.
- A placed document's place is an approximation, never a prior for the
  next map: the next rebuild lays it out with the rest.
- Positions are the same on one platform, not across architectures.
- The response grows with the library, about 185 bytes a document.

---

## Open questions

Choices still open. Those settled since this list was started are at its
end, pointing to the entries that decided them.

- **Insight layer specifics:** trajectory analysis ("new this month"),
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
- A standalone `interests` table and the merging of near-duplicate
  interests: "Interests: two levels, stable identities, automatic
  rebuilds" (identities that outlive runs, and `MergeNearDuplicates`
  within each area).
