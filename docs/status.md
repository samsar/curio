# Status

**Current position:** M0 ✅ · M1 ✅ (all importers, incl. Firefox) · M2
substantially complete — Go-native default fetcher + Chrome TLS/HTTP2
fingerprint backend, YouTube, GitHub, and a two-tier PDF fetcher all
shipped. Remaining M2: `fetcher_rules.yaml`, dead-link detection, GitHub
issues/PRs/wiki. **Next milestone: M3 — MCP sidecar** (`cmd/curio-mcp`):
expose `search_bookmarks` / `get_document` / `find_related` so Claude and
other LLM clients can query the corpus. That's the highest-leverage step
now that ingestion is solid — the payoff for everything built so far.

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

# Fetching works out of the box: the default fetcher is Go-native (no
# web2md/Node setup). PDFs extract locally (pure-Go) with a Jina fallback.

./bin/curio add https://martinfowler.com/articles/feature-toggles.html --wait
./bin/curio search "feature flag rollout"
```

`docs/setup.md` has more detail and troubleshooting.

## Known M0 gaps to address later

These are not bugs — they're scope-trimmed pieces deferred from M0 to M1+:

- **`curio init`** — there's no explicit init command; the CLI auto-inits
  `~/.curio` on first run. If a future workflow needs explicit init
  (e.g., to choose a different embedder upfront), adding it is trivial.
- **Tags from bookmarks not fully wired.** Tags are denormalized into
  `chunks_fts` at index time, but there's no `BookmarkStore.ListByDocument`
  on the store interface — so the indexer can't look up a document's
  bookmark tags to feed them in. Lands when M1 importers do.
- **No reindex CLI yet.** Documented in `docs/decisions.md`; needed when
  someone first wants to swap embedding models.
- **Search filters** (`content_type`, `host`, `source`, etc.) are
  accepted by the API but not yet applied. Engine work needed.

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

## M1 — Bookmark importers

### Completed

| Feature | Package / file | Notes |
|---|---|---|
| Chrome parser | `internal/importer/chrome.go` | Profile discovery, multi-profile, `--file` override |
| HTML export parser | `internal/importer/html.go` | Generic Netscape format; works for any browser |
| `curio import chrome` | `internal/cli/import.go` | `--profile`, `--all-profiles`, `--list-profiles`, `--file` |
| `curio import html` | `internal/cli/import.go` | Takes any exported bookmarks HTML file |
| URL dedup | daemon import endpoint | Reports `skipped` count for existing URLs |
| URL filtering | `internal/importer/importer.go` | Drops `javascript:`, `file://`, browser-internal schemes |
| Progress reporting | `internal/cli/import.go` | `--follow` polls `/v1/stats`, prints live rate + ETA |
| Batched import | `internal/cli/import.go` | 500-bookmark batches with running totals |
| Safari parser | `internal/importer/safari.go` | Reads binary/XML plist, skips Reading List, `--file` override |
| `curio import safari` | `internal/cli/import_safari.go` | Auto-discovers `~/Library/Safari/Bookmarks.plist`, Full Disk Access guidance |
| Firefox parser | `internal/importer/firefox.go` | Reads `places.sqlite` via a temp copy (incl. `-wal`, so a just-added bookmark is seen while Firefox runs); walks `moz_bookmarks`, skips Tags/separators; per-install profile selection |
| `curio import firefox` | `internal/cli/import_firefox.go` | Auto-discovers the default profile via `profiles.ini`, `--file` override |
| Worker concurrency | daemon config | Split fetch + index pools, tunable via config |
| `/v1/stats` | `internal/api/system.go` | Doc/job/bookmark totals + breakdowns by state |
| `/v1/documents` list | `internal/api/documents.go` | `?state` and `?limit` filtering |
| `curio refetch` | `internal/cli/refetch.go` | Single-doc, `--all`, `--state` filtering |

| `curio status` | `internal/cli/status.go` | CLI + daemon version, embed info, counts, disk usage |
| Jobs timestamp + sort | `internal/cli/jobs.go`, `internal/store/sqlite/jobs.go` | Shows `updated_at`, sorts most-recent-first |

### Deferred

_None — Firefox landed (see table above). **M1 is complete.**_

## M2 — Multiple fetchers + rules engine

### Completed

| Feature | Package / file | Notes |
|---|---|---|
| Native fetcher (v1 default) | `internal/fetcher/native.go` | Go-native: net/http + Readability + html-to-markdown; replaced the Node `web2md` as the default. Jina Reader fallback for anti-bot / login-wall cases |
| Chrome fingerprint backend | `internal/fetcher/transport.go` | uTLS + HTTP/2 via `bogdanfinn/tls-client` to defeat JA3/Akamai bot detection; `fetcher.native.backend` = `chrome` (default) \| `stock`. h3 (QUIC) responses decompressed defensively; live integration test under `make test-integration` |
| PDF fetcher (two-tier) | `internal/fetcher/pdf.go`, `native.go` | `application/pdf` (or a `.pdf` URL) → pure-Go local extraction (`ledongthuc/pdf`), falling back to Jina. Other non-HTML binary (images, octet-stream) rejected as a permanent failure. Content type stored as `pdf` |
| PatternDispatcher | `internal/fetcher/fetcher.go` | Host-based routing; first match wins, fallback to Native |
| YouTube fetcher | `internal/fetcher/youtube.go` | yt-dlp for metadata + captions; VTT parser; auto/manual subs |
| YouTube URL normalization | `internal/urlutil/normalize.go` | `youtu.be`, shorts, mobile, embed → canonical `watch?v=ID` |
| YouTube config | `internal/config/config.go` | `bin`, `timeout_seconds`, `sub_langs` |
| GitHub fetcher | `internal/fetcher/github.go` | REST API for repo metadata + README; file URLs fetch specific files |
| GitHub URL parsing | `internal/urlutil/normalize.go` | `ParseGitHubURL` extracts owner/repo/type/ref/path |
| GitHub config | `internal/config/config.go` | `token` (optional, also `CURIO_GITHUB_TOKEN` env), `timeout_seconds` |
| Per-fetcher rate limiting | `internal/fetcher/fetcher.go` | `RateLimited` wrapper using `golang.org/x/time/rate` token bucket |
| GitHub internal rate limiting | `internal/fetcher/github.go` | 1.5 API calls/s at `apiGet` level; inline retry with `Retry-After` support |
| yt-dlp stderr fix | `internal/fetcher/youtube.go` | Extract ERROR lines only; ignore WARNING lines on failure |

### Remaining

- **`fetcher_rules.yaml`** — user-configurable fetcher routing (deferred until 3+ fetchers justify the config complexity)
- **Dead-link detection** — soft 404s that return HTTP 200 with junk content
- **GitHub issues/PRs/wiki** — currently unsupported URL types; fall through to Native or add dedicated handling
