# Roadmap

A staged plan. Each milestone is shippable on its own — the system does
something useful at every step.

This is the one place that tracks milestone status. Each milestone keeps its
original goal and plan; the **Status** notes under it say what shipped (with
package pointers), where the result deviates from the plan, and what was
deferred. Design reasoning lives in `docs/decisions.md`.

**Where things stand:** M0–M4 are shipped. **Next: M5.**

## M0 — Walking skeleton

**Goal:** one URL goes in, searchable text comes out.

- Project scaffold: `cmd/curio`, `cmd/curio-daemon`, `internal/...`
- SQLite schema + migrations
- Daemon HTTP server with: `POST /v1/bookmarks`, `POST /v1/search`,
  `GET /v1/healthz`
- CLI with: `curio add <url>`, `curio search <query>`, `curio daemon
  {start|stop|status}`
- One fetcher: `web2md` (shells out to the existing Node tool)
- One embedder: Ollama / `nomic-embed-text`
- BM25 (FTS5) + vector (sqlite-vec) + RRF hybrid search
- Single-worker job loop in the daemon

**Done when:** `curio add <url>` → wait → `curio search "topic"` returns the
doc with a relevant snippet.

**Status: shipped.**

- Scaffold and three binaries: `cmd/curio`, `cmd/curio-daemon`,
  `cmd/curio-mcp`. SQLite schema as goose migrations (`migrations/`),
  store interfaces in `internal/store` with the SQLite implementation in
  `internal/store/sqlite`.
- Hybrid search: FTS5 BM25 plus sqlite-vec vectors fused with RRF
  (`internal/search`), chunked and embedded by `internal/indexer` with
  Ollama's `nomic-embed-text` (`internal/embedder`, `internal/ollama`).
- Daemon lifecycle for the CLI: auto-start, one daemon per home held by a
  lock (`internal/daemonctl`). The daemon answers as starting, with its
  migration progress, from the moment it binds, and clients wait on that
  progress rather than a fixed timeout.
- Deviations: the default fetcher is Go-native (`internal/fetcher/native.go`),
  not the Node `web2md`, which stays as an optional backend. The job loop
  runs per-kind worker pools (`jobs.NewPools`: fetch, index, cluster), not a
  single worker.

## M1 — Bookmark importers

**Goal:** ingest the user's actual bookmark corpus.

- Chrome bookmarks parser (`Bookmarks` JSON)
- Safari bookmarks parser (`Bookmarks.plist`)
- Firefox parser (`places.sqlite`)
- `curio import chrome [path]`, `curio import safari`, etc.
- URL normalization + dedup across sources
- Progress reporting (`curio status` shows queue depth, recent failures)
- Worker pool with concurrency tunable in config

**Done when:** running `curio import chrome` ingests a real Chrome bookmark
file, fetches everything reachable, and `curio status` shows accurate counts.

**Status: shipped.**

- Importers in `internal/importer`: Chrome (profile discovery), Safari
  (skips the Reading List; needs Full Disk Access), Firefox (reads a copy of
  `places.sqlite` and its WAL, so a bookmark added seconds ago counts), and
  a Netscape HTML export parser that works for any browser. `curio import
  {chrome,safari,firefox,html}` sends 500-bookmark batches, with
  `--dry-run`, `--limit` and `--follow`.
- URL normalization (`internal/urlutil`) and dedup in the import endpoint;
  non-web schemes are filtered.
- `curio status` (versions, counts, disk usage), `curio docs` and
  `curio jobs` views, `curio refetch`.
- Fetch and index pools sized by `daemon.fetch_workers` and
  `daemon.index_workers`.

## M2 — Multiple fetchers + rules engine

**Goal:** handle the long tail of content types.

- `fetcher_rules.yaml` config, hot-reloadable
- Fetchers: `github` (API), `youtube` (yt-dlp + transcript), `pdf`,
  `jina` (HTTP to self-hosted Jina Reader)
- Retry policy with exponential backoff
- Dead-link detection + archive.org fallback
- Per-fetcher rate limiting

**Done when:** mixed corpus (articles, repos, videos, PDFs) all index
correctly and search returns useful results across content types.

**Status: shipped, with deviations.**

- `$CURIO_HOME/fetcher_rules.yaml` routes URLs to fetchers, hot-reloaded
  and keeping the last good rules (`internal/fetcher/rules.go`).
- GitHub through its REST API: repos, files, issues, pull requests and
  wikis (`github.go`). YouTube through yt-dlp, captions preferred over
  descriptions (`youtube.go`). PDFs extracted locally in pure Go
  (`pdf.go`).
- Retries with exponential backoff in the job queue; dead-link detection
  (hard 404/410 and soft 404s) moves a document to `dead`.
- Rate limiting per fetcher, a shared Jina pace and cooldown, and at most
  two origin requests in flight per host (`throttle.go`). The native
  fetcher sends a Chrome TLS fingerprint (`transport.go`).
- Deviations: Jina Reader is not a self-hosted fetcher but the native
  fetcher's fallback through `r.jina.ai` (`fetcher.native.jina_base_url`),
  used only for anti-bot and login-wall pages and PDFs the local extractor
  can't read. There is no archive.org fallback: dead links are marked
  `dead` (decisions.md "Dead-link detection"). web2md is optional, not a
  default.

## M3 — MCP sidecar

**Goal:** Claude (and other LLM clients) can query the corpus.

- `cmd/curio-mcp` binary, stdin/stdout MCP protocol
- Tools: `search_bookmarks`, `get_document`, `find_related`
- Documentation: how to register the MCP server with Claude Code
- Auto-start daemon if MCP sidecar is invoked and daemon isn't running

**Done when:** Claude Code can search the corpus and pull context into its
responses without any manual paste.

**Status: shipped.**

- `cmd/curio-mcp` speaks MCP over stdio (the official Go SDK) and forwards
  to the daemon's HTTP API: `search_bookmarks` (with content type, source
  and host filters), `get_document`, `find_related`, and `list_interests`
  since M4. It starts the daemon when needed, restarts an unreachable one
  once per call, and waits up to 30s for one still starting. Registration
  is in `docs/mcp.md`.
- Deviation: `find_related` ranks by the document's stored chunk vectors,
  mean-pooled into one query (`GET /v1/documents/{id}/related`,
  `curio related`), not by title similarity.

## M4 — Insight layer

**Goal:** surface patterns, not just search.

- Clustering job over document embeddings — a kNN-graph + label-propagation
  clusterer with a noise/unclustered bucket, behind a pluggable
  `insight.Clusterer` interface (`KNNGraphClusterer`; HDBSCAN can drop in
  later behind the same interface)
- Cluster labels: LLM labels by default (`LLMLabeler`) via the
  provider-agnostic `generator.Generator` interface (local Ollama impl, model
  auto-pulled on startup), falling back to deterministic term labels
  (`TermLabeler`) when no generation model is available
- Corpus-wide recompute as an async `cluster` job; each run is a snapshot,
  only the latest run is kept
- API: `GET /v1/interests`, `GET /v1/interests/{id}`,
  `POST /v1/interests/rebuild`
- `curio interests` (and `curio interests rebuild`) CLI subcommands
- MCP tool: `list_interests`
- Eval harness: `curio eval --queries <qrels.yaml>` scores recall@k /
  precision@k / NDCG@k / MRR over a labeled query set — the measurement rig
  that de-risks M6

**Done when:** running `curio interests` returns a labeled, browsable list of
topic clusters that feel like an accurate picture of what the user reads.

**Status: shipped.**

- `internal/insight`: `KNNGraphClusterer` behind `insight.Clusterer`, LLM
  labels through `generator.Generator` with term labels as the fallback,
  and the `Engine` that clusters, labels and persists (`cluster_runs`,
  `clusters`, `cluster_documents`, migration 004). Document vectors come
  from `ChunkStore.DocumentVectors`, mean-centered by default.
- The `cluster` job runs on its own single-worker pool; `POST
  /v1/interests/rebuild` (or `curio interests rebuild`) enqueues it.
- The daemon keeps the embedding and generation models pulled, retrying
  until Ollama serves them (`ollama.Client.KeepPulled`).
- `curio eval` (`internal/eval`) scores recall@k, precision@k, NDCG@k and
  MRR over a labeled query set.
- Deferred: trajectory analysis ("new this month"), a standalone
  interests table with cross-cluster merging, and `/v1/suggestions` (M5).
- Known limitations: one large "general-reading" cluster takes about 60%
  of a real corpus next to good niche interests. It comes from mean-pooled
  document vectors, not a tunable knob (decisions.md "Insight clustering
  quality"). Label quality is bounded by the local generation model
  (`llama3.2` by default).

## M5 — Suggestions and the digest

**Goal:** proactive value, not just queries.

- Per-cluster suggestion generation (papers, repos, people, follow-up reads)
- Weekly digest job — writes a markdown file the user can open
- Dismissal mechanism (suggestions the user said "not interested" to)

**Done when:** the user opens `~/.curio/digest/<week>.md` and finds it worth
reading.

**Status: not started.** This is next.

## M6 — RAG / Q&A synthesis + SOTA search

**Goal:** answer questions over saved content and make natural-language
search state-of-the-art.

- True RAG / Q&A synthesis: retrieve → LLM → cited answer, reusing the
  `generator.Generator` interface from M4
- Upgrade NL search beyond the current OR+stopword BM25 — options logged in
  `docs/decisions.md` (stemming + `minimum_should_match`, LLM query-rewriting
  via Ollama, SPLADE/ColBERT) — measured with the M4 eval harness
- **Decide: build vs. adopt (e.g. [tobi/qmd](https://github.com/tobi/qmd))
  before implementing RAG.**

**Done when:** `curio ask "..."` returns a synthesized, cited answer and the
eval harness shows measurably better retrieval than the v1 baseline.

**Status: not started.** The build-vs-adopt decision comes first.

## M7 — Hosted mode

**Goal:** make curio runnable as a service.

- Postgres + pgvector implementations behind existing interfaces
- Authentication (scheme still open) for deployments beyond loopback; the
  local daemon deliberately has none (see decisions.md "Local API: loopback
  only, no token, browsers shut out")
- Multi-tenant deployment configs
- Public API documentation

**Not on the v1 critical path. Listed for completeness.**

**Status: not started.**

## Stretch / later

- Browser history importer (data model already supports it)
- Read-later importers (Pocket, Instapaper, Raindrop)
- Highlight importers (Readwise)
- Web UI
- Snapshot to WARC for dead-link insurance
- Cross-source signal weighting ("read this thing, bookmarked this thing,
  highlighted this thing → strong interest")
- Insight clustering quality: split the ~60% "general-reading" mega-cluster
  (recursive split of oversized clusters → Leiden → better doc representation).
  See `docs/decisions.md` → "Insight clustering quality" for the diagnosis.
- Embedding model swap: update the marker, rebuild `chunks_vec` at the new
  dimension and reindex, behind the startup guard that today refuses any
  model change (see `docs/decisions.md` → "Embedding model swap").
