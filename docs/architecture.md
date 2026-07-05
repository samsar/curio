# Architecture

## Goals

1. **Personal search** over the full text of every page the user has bookmarked.
2. **Interest modeling** — surface patterns, clusters, and trajectory across the
   corpus.
3. **LLM context layer** — make all of the above queryable by external LLMs via
   an MCP server, so they can pull personal context automatically.
4. **Local-first** by default; nothing about the design should preclude running
   curio as a hosted multi-tenant service later.

## Non-goals (for now)

- Multi-user collaboration in a single installation.
- Browser extension UI (CLI + MCP is enough for v1).
- Read-later, highlights, and history sources (the data model accommodates them
  but v1 ingests bookmarks only).

## Components

```
┌──────────────┐  ┌─────────────┐  ┌──────────────┐
│   curio CLI  │  │ curio-mcp   │  │  Future Web  │
│  (cobra)     │  │ (sidecar)   │  │   UI / API   │
└──────┬───────┘  └──────┬──────┘  └──────┬───────┘
       └─────────────────┼────────────────┘
                   HTTP + JSON
                         │
                  ┌──────▼────────────┐
                  │   curio-daemon    │
                  │                   │
                  │  Importer ──┐     │
                  │  Crawler  ──┤     │
                  │  Indexer  ──┼──► Job queue (SQLite-backed)
                  │  Insight  ──┘     │
                  │  Search           │
                  └──┬──────────┬──┬──┘
                     │          │  │
              ┌──────▼───┐  ┌───▼──▼─────┐  ┌──────────┐
              │ SQLite   │  │ Ollama     │  │ Fetchers │
              │ FTS5 +   │  │ (embed +   │  │ web2md / │
              │ sqlite-vec│  │  LLM)     │  │ Jina /   │
              └──────────┘  └────────────┘  │ GitHub / │
                                            │ yt-dlp   │
                                            └──────────┘
```

### `curio` (CLI)

A Cobra-based CLI, thin client over the daemon's HTTP API. Subcommands:

- `curio add <url>` — manually add a bookmark
- `curio import <source> [path]` — bulk import from Chrome / Safari / Firefox
- `curio search <query>` — hybrid search
- `curio status` — daemon health, doc counts, job queue depth
- `curio daemon {start|stop|status|logs}` — lifecycle management via PID file
- `curio refetch <id|all>` — force re-extract
- `curio reindex` — re-embed (after model swap)

If a CLI command needs the daemon and it isn't running, the CLI auto-starts it.

### `curio-daemon`

Long-running background process. Owns the SQLite database, the job queue, and
all fetch/index/search/insight workflows.

- HTTP+JSON API on `127.0.0.1:8765` (port configurable)
- OpenAPI spec is the source of truth — clients codegen from it
- Internal worker pool processes jobs from the SQLite-backed queue

### `curio-mcp` (sidecar)

A separate process that speaks the MCP protocol on stdin/stdout (per MCP
convention) and forwards calls to the daemon over HTTP. Why a sidecar:

- MCP servers are spawned per-session by Claude Code; lifecycle differs from the
  always-on daemon
- Clean process boundary; can be restarted independently
- Future-proofs against MCP protocol changes

MCP tools (implemented):

- `search_bookmarks(query, k, content_type?, source?, host?)` — hybrid search
  with optional filters
- `get_document(id)` — fetch a document's metadata + extracted markdown
- `find_related(id, k)` — find documents similar to a given one (by its title)

Later (insight layer, M4):

- `list_interests()` — inferred interest clusters

Registration and usage: see `docs/mcp.md`.

## Transport: HTTP + JSON

Chose HTTP+JSON over gRPC for:

- Trivial debugging with `curl`
- MCP protocol speaks JSON anyway
- Docker uses HTTP and it scales fine
- No protobuf toolchain dependency

OpenAPI spec lives at `api/openapi.yaml`. All clients (CLI, MCP sidecar, future
web UI) generate types from it.

## Daemon lifecycle

V0: PID-file-based, managed via `curio daemon start|stop|status`. CLI commands
that need the daemon will auto-start it if not running.

V1+: optional `curio service install` that drops a `launchd` plist (macOS) or
systemd unit (Linux) for auto-start at login.

## Storage layout

Everything under `$CURIO_HOME` (defaults to `~/.curio`).

```
~/.curio/
  .curio-meta.json       # marker file: schema_version, embedding_model, dim
  config.yaml            # user config
  curio.db               # SQLite database (metadata, jobs, FTS5, vectors)
  content/               # extracted markdown, on disk
    <bookmark_id>/
      <document_id>.md
      <document_id>.raw.html
  logs/
    daemon.log
  daemon.pid
```

If `~/.curio` exists without `.curio-meta.json`, the daemon refuses to start and
suggests setting `CURIO_HOME` to a different path.

## Data flow

```
bookmark file ──► importer ──► bookmarks table ──► enqueue fetch jobs
                                                          │
                                                          ▼
                              ┌──► fetcher (per-domain strategy)
                              │           │
                              │           ▼
                              │       documents table
                              │           │
                              │           ▼
                              │       enqueue index job
                              │           │
                              │           ▼
                              │       chunker ──► embedder (Ollama)
                              │                       │
                              │                       ▼
                              │                  FTS5 + sqlite-vec
                              │
                              └── (periodically) ──► insight jobs
                                                       │
                                                       ▼
                                                clusters / interests
```

## Fetcher strategy selection

Data-driven, not hardcoded. `$CURIO_HOME/fetcher_rules.yaml` lists rules
top-to-bottom, first match wins (`internal/fetcher/rules.go`):

```yaml
rules:
  - match: { host: "github.com" }
    fetcher: github
  - match: { host_suffix: ".youtube.com" }
    fetcher: youtube
  - match: { host_in: ["news.ycombinator.com", "lobste.rs"] }
    fetcher: native
  - match: {}             # catch-all
    fetcher: native
```

Matchers are URL-based: `host` (exact), `host_suffix` (label-boundary
suffix), `host_in` (list), `{}` (catch-all). Fetcher names bind against
what the daemon constructed at startup — `native` (always available),
`web2md` (only when it is the configured default), `github`, and
`youtube` (when yt-dlp is present); a rule naming an unavailable fetcher
is skipped with a logged warning. Parsing is strict: an unknown key
(e.g. a typo'd `host_sufix`) is a validation error — otherwise it would
decode as an empty match, i.e. a catch-all. PDFs are handled inside the
Native fetcher, so there is no `content_type` matcher — dispatch happens
before the response exists.

Hot-reloadable (stat-on-dispatch, throttled to 2s): edit the file and the
next fetch uses the new rules — no restart. Invalid edits keep the last
good rules; deleting the file reverts to the built-in defaults. Lets the
user tune per-domain without recompiling.

## Search: hybrid BM25 + vector

```
query ──► BM25 (FTS5)                ──► top 50 chunks
   │
   └────► embed (Ollama)
              │
              └──► vector ANN (sqlite-vec) ──► top 50 chunks
                              │
                              ▼
                          RRF fusion (k=60)
                              │
                              ▼
                  collapse chunks → documents
                              │
                              ▼
                  apply metadata filters
                              │
                              ▼
                          top k results
```

Two knobs in config: BM25/vector weights in RRF, and chunk-to-doc collapse
strategy (max vs sum vs top-3-avg).

## Pluggability: where interfaces live

Three interfaces with explicit swap paths:

1. **`DocumentStore`** — primary metadata storage. SQLite impl in v1; Postgres
   impl when hosted-mode is wanted.
2. **`BM25Index`** — keyword index. FTS5 impl in v1; Tantivy or Elasticsearch
   later if scale requires.
3. **`VectorIndex`** — embedding index. sqlite-vec impl in v1; pgvector,
   Qdrant, or Pinecone later.
4. **`Embedder`** — embedding model client. Ollama impl in v1; Voyage / OpenAI
   for cloud. Switching embedding models requires re-indexing
   (see [decisions](./decisions.md#embedding-model-swap)).
5. **`Fetcher`** — content fetcher. Multiple impls (web2md, jina, github, ...),
   selected per-URL by the rules engine.

Do not abstract until you have two impls. The interfaces above are commitments
because we already know we want hosted mode, model swaps, and multiple fetchers.

## Multi-tenancy stance

Single-tenant in deployment; multi-tenant in the schema. Every top-level entity
carries a `tenant_id` (hardcoded to `"local"` for personal installs). Hosted
mode is a deployment change, not a schema change. See
[data model](./data-model.md#multi-tenancy).

## Dependencies

External processes the daemon expects:

- **Ollama** — for embeddings (and optional local LLM). Daemon talks to it on
  `http://localhost:11434`. Fails loudly if absent; documents how to install.
- **Node + web2md** (the user's existing tool) — invoked as a subprocess by the
  `web2md` fetcher. Optional if other fetchers cover all URLs.
- **Jina Reader (optional)** — self-hostable via Docker, used by the `jina`
  fetcher.
- **Claude API (optional)** — for heavier synthesis in the insight layer.

## What's deliberately not in v1

- Browser history ingestion (data model supports it; importer doesn't yet)
- Read-later / highlights (Pocket, Instapaper, Readwise)
- Interest clustering and trajectory analysis (insight layer)
- Web UI
- Authentication (single-tenant local; auth middleware stub for future)
