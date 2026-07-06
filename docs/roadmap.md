# Roadmap

A staged plan. Each milestone is shippable on its own — the system does
something useful at every step.

> **Status (current):** M0–M4 complete. M4 shipped the insight layer:
> documents are clustered by embedding similarity (a kNN-graph +
> label-propagation clusterer with a noise bucket) and surfaced as labeled
> interests (`GET /v1/interests`, `curio interests`, `list_interests`),
> driven by an async `cluster` job. Cluster labels are term-frequency by
> default with optional LLM labels behind a `generator.Generator` interface.
> A retrieval eval harness (`curio eval`) is the measurement rig that
> de-risks M6. **Next: M5.** See `docs/status.md` and `docs/mcp.md`.

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

## M3 — MCP sidecar

**Goal:** Claude (and other LLM clients) can query the corpus.

- `cmd/curio-mcp` binary, stdin/stdout MCP protocol
- Tools: `search_bookmarks`, `get_document`, `find_related`
- Documentation: how to register the MCP server with Claude Code
- Auto-start daemon if MCP sidecar is invoked and daemon isn't running

**Done when:** Claude Code can search the corpus and pull context into its
responses without any manual paste.

## M4 — Insight layer

**Goal:** surface patterns, not just search.

- Clustering job over document embeddings — a kNN-graph + label-propagation
  clusterer with a noise/unclustered bucket, behind a pluggable
  `insight.Clusterer` interface (`KNNGraphClusterer`; HDBSCAN can drop in
  later behind the same interface)
- Cluster labels: deterministic term-frequency labels by default
  (`TermLabeler`), optional LLM labels (`LLMLabeler`) via the
  provider-agnostic `generator.Generator` interface (local Ollama impl); the
  engine tries LLM, falls back to terms
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

## M5 — Suggestions and the digest

**Goal:** proactive value, not just queries.

- Per-cluster suggestion generation (papers, repos, people, follow-up reads)
- Weekly digest job — writes a markdown file the user can open
- Dismissal mechanism (suggestions the user said "not interested" to)

**Done when:** the user opens `~/.curio/digest/<week>.md` and finds it worth
reading.

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

## M7 — Hosted mode

**Goal:** make curio runnable as a service.

- Postgres + pgvector implementations behind existing interfaces
- Authentication middleware (real, not stub)
- Multi-tenant deployment configs
- Public API documentation

**Not on the v1 critical path. Listed for completeness.**

## Stretch / later

- Browser history importer (data model already supports it)
- Read-later importers (Pocket, Instapaper, Raindrop)
- Highlight importers (Readwise)
- Web UI
- Snapshot to WARC for dead-link insurance
- Cross-source signal weighting ("read this thing, bookmarked this thing,
  highlighted this thing → strong interest")
