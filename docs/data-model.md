# Data Model

## Core idea: separate references from content

A URL the user cares about can show up in multiple places — bookmarks, browser
history, a read-later queue, a highlight. The *extracted content* of that URL
is the same regardless of where the reference came from. So:

- **`documents`** is the universal, deduplicated content table, keyed by
  normalized URL.
- **Reference tables** (`bookmarks`, `history_entries`, `highlights`, ...) point
  *at* documents. The same document can be referenced multiple times from
  multiple sources.

In v1 only the `bookmarks` reference table exists, but the schema is shaped so
that adding `history_entries` later is purely additive.

## When to add a new reference table vs. a new `content_type`

A common point of confusion: if curio starts handling PDFs, YouTube videos, or
GitHub repos, do those get new reference tables?

**Almost always no.** The rule:

> A new **reference table** when the *way the user encountered* the content is
> meaningfully different. A new **`content_type`** when only the *format* is
> different.

Reference tables capture the **origin story** — how the user came to care
about this thing, and what metadata that origin carries.

- Bookmarks have a folder path and tags.
- Browser history entries have visit count and dwell time.
- Highlights have a quoted passage and an optional note.
- Read-later items have a read/unread state.
- Local files have a file path and mtime.

`content_type` captures the **shape of the content** — what fetcher to use
and what extraction metadata to expect.

- `article`, `pdf`, `video`, `repo`, `thread`, ...

Applied to common cases:

| The user encountered... | Reference table | `content_type` |
|---|---|---|
| A bookmarked arxiv PDF | `bookmarks` | `pdf` |
| A bookmarked YouTube video | `bookmarks` | `video` |
| A bookmarked GitHub repo | `bookmarks` | `repo` |
| A page in browser history | `history_entries` (future) | `article` (or whatever fits) |
| A PDF on local disk dragged in | `local_files` (future) | `pdf` |
| A Pocket-saved article | `read_later` (future) | `article` |
| A Readwise highlight | `highlights` (future) | inherited from document |

The PDF case is the clearest illustration: a bookmarked PDF and a local PDF
share the same `documents` row shape and `content_type='pdf'`, but they enter
the system through different reference tables because the metadata about
*how the user got there* is fundamentally different.

## Multi-tenancy

`tenant_id` lives on every **top-level** entity:

- `bookmarks`, `history_entries`, `highlights` (reference tables)
- `jobs`
- `cluster_runs`, `clusters` (insight layer)

Child tables (`documents`, `chunks`, `document_extractions`,
`cluster_documents`) **do not** carry `tenant_id`. They are reached only
through a parent reference, so the JOIN implicitly enforces tenant scoping. This keeps row size sane and avoids the
redundancy of marking every chunk with a tenant when its document already
belongs (transitively) to a tenant via its references.

For single-user local installs, `tenant_id` is always `"local"`. For hosted
deployments, it'll be a UUID per customer.

**User IDs** are deliberately not added in v1. When hosted-mode lands and we
support team plans, we'll add `created_by_user_id` as an audit column on
reference tables — distinct from `tenant_id` (which is the customer/org), not a
replacement for it.

## Documents are shared, references are not

If the same URL appears in your bookmarks *and* later in your browser history,
you get **one** document and **two** reference rows. This:

- Saves re-fetching and re-embedding
- Lets the insight layer reason about "I encountered this from multiple
  sources" as a strength signal
- Makes `documents.url` a deduplication key

A document's `tenant_id` is implicitly the tenant of any reference that points
at it. In hosted mode, if two tenants bookmark the same URL, they get
*separate* document rows — we don't share extracted content across tenants
(privacy + extraction can be tenant-specific later, e.g., authenticated
fetches).

## Schema (v1)

### `documents`

Universal content table. One row per (tenant, URL).

```
documents
  id                    UUID PK
  tenant_id             TEXT NOT NULL          -- implicit via references, denormalized for query convenience
  url                   TEXT NOT NULL          -- normalized: lowercase host, no fragment, sorted query params
  url_canonical         TEXT                   -- post-redirect, if different
  content_type          TEXT                   -- 'article' | 'repo' | 'video' | 'pdf' | 'thread' | 'unknown'
  title                 TEXT                   -- extracted; may differ from any reference's title
  author                TEXT
  published_at          TIMESTAMP
  language              TEXT
  word_count            INTEGER
  current_extraction_id UUID                   -- FK to latest successful extraction
  state                 TEXT NOT NULL          -- 'pending' | 'fetched' | 'failed' | 'dead'
  created_at, updated_at
  UNIQUE (tenant_id, url)
```

Why `tenant_id` is denormalized here: every search/list query filters by tenant,
and joining through a reference table on every search hurts. The constraint is
enforced at write time by the importer/crawler.

### `document_extractions`

Each fetch attempt produces a new extraction row. Lets us keep history of how a
document has changed and which fetcher produced it.

```
document_extractions
  id                UUID PK
  document_id       UUID NOT NULL FK
  fetched_at        TIMESTAMP NOT NULL
  fetcher           TEXT NOT NULL              -- 'web2md' | 'jina' | 'github' | 'youtube' | 'pdf'
  status            TEXT NOT NULL              -- 'ok' | 'partial' | 'paywalled' | 'error'
  markdown_path     TEXT                       -- relative to ~/.curio/content/
  raw_path          TEXT                       -- original HTML/JSON, optional
  extraction_meta   JSON                       -- fetcher-specific (repo stars, video duration, ...)
  error_message     TEXT
```

`documents.current_extraction_id` points at the latest successful row. Older
rows are kept for diff/history (could be GC'd by a future retention job).

### `chunks`

```
chunks
  id                UUID PK
  document_id       UUID NOT NULL FK
  extraction_id     UUID NOT NULL FK           -- chunks belong to a specific extraction
  ord               INTEGER NOT NULL
  text              TEXT NOT NULL
  token_count       INTEGER
```

Two virtual tables sit alongside:

- `chunks_fts` — FTS5 over `chunks.text`, plus boostable columns for
  `documents.title` and `references.tags` (denormalized at index time).
- `chunks_vec` — sqlite-vec, keyed on `chunks.id`, holds the embedding.

When the embedding model changes, both virtual tables are rebuilt (see
[embedding model swap](./decisions.md#embedding-model-swap)).

### `bookmarks` (reference table)

```
bookmarks
  id                UUID PK
  tenant_id         TEXT NOT NULL
  document_id       UUID FK                    -- NULL until first successful fetch
  url               TEXT NOT NULL              -- denormalized for fast lookup
  title             TEXT                       -- title at save-time (from the browser)
  saved_at          TIMESTAMP NOT NULL
  source            TEXT NOT NULL              -- 'chrome' | 'safari' | 'firefox' | 'manual'
  folder_path       TEXT                       -- '/Tech/AI/Agents'
  tags              JSON                       -- string array
  created_at, updated_at
  UNIQUE (tenant_id, url, source)              -- one bookmark per (tenant, url, source)
```

### Future reference tables (not in v1)

These are sketched here to validate the schema's extensibility. Don't
implement until needed.

```
history_entries (tenant_id, document_id, visited_at, dwell_seconds, visit_count, source)
highlights      (tenant_id, document_id, text, note, highlighted_at, source)
```

Both follow the same pattern: their own table, FK to `documents`, own
source-specific fields.

### `jobs`

```
jobs
  id            UUID PK
  tenant_id     TEXT NOT NULL
  kind          TEXT NOT NULL                  -- 'fetch' | 'index' | 'cluster' | 'summarize'
  payload       JSON NOT NULL
  status        TEXT NOT NULL                  -- 'pending' | 'running' | 'done' | 'failed'
  attempts      INTEGER NOT NULL DEFAULT 0
  run_after     TIMESTAMP NOT NULL DEFAULT now
  last_error    TEXT
  created_at, updated_at
```

Worker pool polls `WHERE status='pending' AND run_after <= now ORDER BY
created_at LIMIT N`. Failed jobs get exponential backoff via `run_after`.

### `cluster_runs`

One row per clustering execution. The clusters of the latest `done` run are
what surface as interests.

```
cluster_runs
  id                UUID PK
  tenant_id         TEXT NOT NULL
  status            TEXT                       -- 'running' | 'done' | 'failed'
  algo              TEXT                       -- clusterer name, e.g. 'knn-graph'
  params            JSON                       -- clusterer parameters
  num_documents     INTEGER
  num_clusters      INTEGER
  num_noise         INTEGER
  error             TEXT                       -- nullable
  started_at        TIMESTAMP
  finished_at       TIMESTAMP                  -- nullable
  created_at, updated_at
```

### `clusters`

One row per cluster within a run. `cohesion` is the mean member cosine to the
cluster medoid.

```
clusters
  id                UUID PK
  tenant_id         TEXT NOT NULL
  run_id            UUID NOT NULL FK           -- → cluster_runs(id), ON DELETE CASCADE
  label             TEXT                       -- nullable; topic name
  summary           TEXT                       -- nullable
  size              INTEGER
  cohesion          REAL                       -- mean member cosine to medoid, 0..1
  created_at, updated_at
```

### `cluster_documents`

Cluster membership, one row per (cluster, document). Noise docs simply have no
row.

```
cluster_documents
  cluster_id        UUID NOT NULL FK           -- → clusters(id), ON DELETE CASCADE
  document_id       UUID NOT NULL FK           -- → documents(id), ON DELETE CASCADE
  similarity        REAL                       -- cosine to medoid, 0..1
  PRIMARY KEY (cluster_id, document_id)
```

### Deferred insight tables (not in v1)

Sketched for completeness; not yet built. In M4, interests are surfaced
directly from labeled clusters (no standalone `interests` table), and
suggestions arrive with M5.

```
interests    (tenant_id, name, summary, evidence_cluster_ids JSON, confidence)
suggestions  (tenant_id, kind, payload JSON, created_at, dismissed_at)
```

## URL normalization

Critical for dedup. The normalization function lowercases the host, strips
fragments, removes common tracking params (`utm_*`, `fbclid`, `gclid`, ...),
and sorts remaining query params. Implementation lives in
`internal/url/normalize.go` (one place, one impl, tested exhaustively).

## On-disk content layout

```
~/.curio/content/
  <document_id>/
    <extraction_id>.md
    <extraction_id>.raw.html      # optional
    <extraction_id>.meta.json     # mirror of extraction_meta, for grep-ability
```

Keeping content on disk rather than in SQLite:

- Database stays small and fast to back up
- `grep`/`ripgrep` works directly against the corpus
- Easy to delete a document's content without DB surgery

## Schema versioning and migrations

Migrations live in `migrations/` and run via `pressly/goose` at daemon startup.
`.curio-meta.json` records the current schema version. Downgrade is not
supported — backup before major version bumps.
