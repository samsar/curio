# Status

**Current position:** M0 ✅ · M1 ✅ · M2 ✅ · M3 ✅. The M2 tail landed
(`fetcher_rules.yaml` hot-reloadable routing, dead-link detection with the
`dead` doc state, GitHub issues/PRs/wiki) and the M3 tail did too:
`find_related` is now a true vector-neighbor lookup over stored chunk
embeddings (`GET /v1/documents/{id}/related`, `curio related`), replacing
the title-search stopgap. The corpus is queryable from inside the LLM
client via `curio-mcp` (`search_bookmarks` / `get_document` /
`find_related`). **Next up: M4** — the insight layer (clustering,
`curio interests`, `list_interests`).

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
- ~~**Tags from bookmarks not wired.**~~ ✅ Done — `BookmarkStore.TagsForDocument`
  unions a doc's bookmark tags (across sources); the index handler feeds them
  into `chunks_fts`, so a tag word is searchable even when absent from the body.
- ~~**No reindex CLI.**~~ ✅ Done — `curio reindex <id>` / `--all` re-chunks +
  re-embeds existing extractions (`POST /v1/documents/{id}/reindex`,
  `/reindex-all`). Covers same-dimension model swaps, chunker changes, and
  tag pickup; a *dimension-change* swap still needs a `chunks_vec` rebuild
  (see `decisions.md`).
- ~~**Search filters**~~ ✅ Done — `content_type`, `host`, and `source` are
  applied by the search engine (`store.SearchFilters` threaded through BM25 +
  vector) and exposed via `curio search --type/--source/--host`. `folder`/
  `tag` are accepted but not yet applied.

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

| `fetcher_rules.yaml` | `internal/fetcher/rules.go` | User-configurable routing under `$CURIO_HOME/fetcher_rules.yaml`: `host` / `host_suffix` / `host_in` / catch-all matchers, first match wins. Hot-reloaded via throttled stat-on-dispatch; invalid edits keep the last good rules; unknown fetcher names skip the rule with a warning. Missing file = built-in defaults |
| Dead-link detection | `internal/fetcher/native.go`, `internal/jobs` | Hard 404/410 → `PermanentError`+`ErrDeadLink` (fail on attempt 1, never Jina). Soft 404s (HTTP 200 + not-found title, or redirect-to-homepage) detected before the login-wall heuristics. Docs land in state `dead`; refetch of a dead doc needs `--force` (bulk `--all` skips dead unless `--state=dead`) |
| GitHub issues/PRs | `internal/fetcher/github.go`, `internal/urlutil` | `/issues/N` + `/pull/N` via REST (`pulls` API path), body + up to 100 conversation comments, content_type `thread`. Issue URLs that are really PRs delegate to the PR path. `ParseGitHubURL` gained `Number` |
| GitHub wiki | `internal/fetcher/github.go` | `/wiki[/Page]` via `raw.githubusercontent.com/wiki/o/r/Page.md` (no REST API exists for wikis); public wikis only; content_type `article` |

### Remaining

_None — **M2 is complete.**_

## M3 — MCP sidecar

### Completed

| Feature | Package / file | Notes |
|---|---|---|
| `curio-mcp` binary | `cmd/curio-mcp/main.go` | Speaks MCP over stdio (official `modelcontextprotocol/go-sdk`); forwards to the daemon HTTP API; auto-starts the daemon. Built by `make build`. |
| Tools | `cmd/curio-mcp/main.go` | `search_bookmarks` (query + content_type/source/host filters), `get_document` (metadata + markdown), `find_related` (by title similarity, excludes self) |
| Registration docs | `docs/mcp.md` | Claude Code (`claude mcp add`) + Claude Desktop config |
| Tests | `cmd/curio-mcp/main_test.go` | In-memory client/server round-trip over a fake daemon: tool listing, each tool, error path |

| Vector `find_related` | `internal/search/search.go`, `internal/api/search.go` | `GET /v1/documents/{id}/related?k=N`: mean-pools the doc's stored chunk vectors (read back from `chunks_vec`, no embedder call) into one ANN query, excludes self via `SearchFilters.ExcludeDocumentID` (rides the over-fetch path — vec0 applies predicates after the k cutoff). MCP tool + new `curio related` command use it |

### Remaining

- Richer tools (e.g. `list_interests`) arrive with the M4 insight layer.

_**M3 is complete.**_
