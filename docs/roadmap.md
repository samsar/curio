# Roadmap

A staged plan. Each milestone is shippable on its own — the system does
something useful at every step.

> **Status (current):** M0 and M1 complete; M2 substantially complete —
> Go-native default fetcher + Chrome fingerprint backend, YouTube, GitHub,
> and a two-tier PDF fetcher are shipped. Remaining M2: `fetcher_rules.yaml`,
> dead-link detection, GitHub issues/PRs/wiki. **M3 (MCP sidecar) is the
> recommended next milestone.** See `docs/status.md` for the detailed
> checklist.

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

## M4 — Insight layer (first cut)

**Goal:** surface patterns, not just search.

- Periodic clustering job (HDBSCAN over document embeddings)
- LLM-generated cluster labels (via Claude API or local Ollama LLM)
- `curio interests` CLI subcommand
- MCP tool: `list_interests`
- Trajectory analysis: cluster growth over time, "new this month" detection

**Done when:** running `curio interests` returns a labeled, browsable list of
topic clusters that feel like an accurate picture of what the user reads.

## M5 — Suggestions and the digest

**Goal:** proactive value, not just queries.

- Per-cluster suggestion generation (papers, repos, people, follow-up reads)
- Weekly digest job — writes a markdown file the user can open
- Dismissal mechanism (suggestions the user said "not interested" to)

**Done when:** the user opens `~/.curio/digest/<week>.md` and finds it worth
reading.

## M6 — Hosted mode

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
