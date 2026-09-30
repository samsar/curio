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
  failure_cause         TEXT                   -- why a failed or dead document failed; NULL otherwise
  created_at, updated_at
  UNIQUE (tenant_id, url)
```

Why `tenant_id` is denormalized here: every search/list query filters by tenant,
and joining through a reference table on every search hurts. The constraint is
enforced at write time by the importer/crawler.

`failure_cause` is one of `store.FailureCauses` (`dead_link`, `anti_bot`,
`login_wall`, `jina_refused`, `tls`, `unreachable`, `timeout`, `network`,
`rate_limited`, `http_error`, `unsupported`, `too_large`, `index`,
`other`). It is set exactly when the document is `failed` or `dead`, and a
`dead` document's is `dead_link`: the permanent-failure hook records it with
the state (`MarkFailed` derives the state from the cause), and every write
that moves the document on clears it (a refetch, a successful fetch or
index). No CHECK constraint holds the values, unlike the other enums: the
store validates them, and a new cause needs no table rebuild. The partial
index `idx_documents_tenant_cause_updated (tenant_id, failure_cause,
updated_at, id) WHERE failure_cause IS NOT NULL` serves the cause filters
and the failure summary, and holds only the documents that failed. See
[Failure causes: recorded when a document fails](./decisions.md#failure-causes-recorded-when-a-document-fails).

### `document_extractions`

Each fetch attempt produces a new extraction row. Lets us keep history of how a
document has changed and which fetcher produced it.

```
document_extractions
  id                UUID PK
  document_id       UUID NOT NULL FK
  fetched_at        TIMESTAMP NOT NULL
  fetcher           TEXT NOT NULL              -- 'native' | 'web2md' | 'github' | 'youtube'
  status            TEXT NOT NULL              -- 'ok' | 'partial' | 'paywalled' | 'error'
  markdown_path     TEXT                       -- relative to ~/.curio/content/
  raw_path          TEXT                       -- unused: no fetcher keeps the raw response
  extraction_meta   JSON                       -- fetcher-specific (repo stars, video duration, ...)
  error_message     TEXT                       -- why the status isn't 'ok' (a partial's missing content)
```

`documents.current_extraction_id` points at the latest successful row. Older
rows are kept for diff/history (could be GC'd by a future retention job).

### `chunks`

```
chunks
  seq               INTEGER PK                 -- rowid of the chunk's chunks_fts entry
  id                UUID NOT NULL UNIQUE       -- the chunk's ID everywhere else
  document_id       UUID NOT NULL FK
  extraction_id     UUID NOT NULL FK           -- chunks belong to a specific extraction
  ord               INTEGER NOT NULL
  text              TEXT NOT NULL
  token_count       INTEGER
  title             TEXT NOT NULL              -- the document title, as indexed for this chunk
  tags              TEXT NOT NULL              -- bookmark tags as indexed (a JSON array), '' for none
```

Two virtual tables sit alongside:

- `chunks_fts` — FTS5 index over `chunks.text`, `title` and `tags` (title
  and tags are denormalized at index time so they can be matched without a
  JOIN). `chunks` is its external content (`content_rowid = seq`): the
  index holds no copy of the text and reads it back from `chunks` for
  snippets.
- `chunks_vec` — sqlite-vec, keyed on `chunks.id`, holds the embedding.

Triggers on `chunks` mirror every insert, update and delete into
`chunks_fts`, and the delete trigger removes the chunk's vector too, so a
chunk deleted any way, a foreign-key cascade from its document or
extraction included, takes its derived rows with it. `title` and `tags`
are stored on the chunk because an external-content delete must supply
the values that were indexed. `seq` is an explicit INTEGER PRIMARY KEY
because SQLite keeps those across VACUUM, where an implicit rowid could be
renumbered and detach the index from its rows.

A document that fails or goes dead keeps the chunks of its last successful
fetch; search and find-related leave failed and dead documents out, and a
refetch that succeeds re-indexes them.

Only `chunks_vec` depends on the embedding model: it holds that model's
vectors, and its width is the home's, recorded in `.curio-meta.json` when
the home is created (1024 for the default `qwen3-embedding:0.6b`). No
migration can know that width, so migration 001 creates the table at
FLOAT[768] and the daemon's `sqlite.EnsureVectorIndex`, after migrating,
recreates it at the marker's width while it is empty; it never rebuilds a
table that holds vectors. The daemon refuses to start when `config.yaml`'s
embedding model or width differs from the marker's, or the marker predates
home format 2 (see
[embedding model and per-home width](./decisions.md#embedding-model-and-per-home-width)).
The database records neither: migration 012 dropped `schema_meta`, whose
row claimed nomic-embed-text at 768 for every home.

### `bookmarks` (reference table)

```
bookmarks
  id                UUID PK
  tenant_id         TEXT NOT NULL
  document_id       UUID FK                    -- linked at ingest; NULL only if the document is deleted
  url               TEXT NOT NULL              -- denormalized for fast lookup
  title             TEXT                       -- title at save-time (from the browser)
  saved_at          TIMESTAMP NOT NULL         -- when the browser saved it; see below
  source            TEXT NOT NULL              -- 'chrome' | 'safari' | 'firefox' | 'html' | 'manual'
  folder_path       TEXT                       -- '/Tech/AI/Agents'
  tags              JSON                       -- string array
  created_at, updated_at
  UNIQUE (tenant_id, url, source)              -- one bookmark per (tenant, url, source)
```

A bookmark is saved together with its document in one transaction
(`BookmarkStore.Ingest`): the document is found by `(tenant_id, url)` or
created `pending`, the bookmark is linked to it, and a fetch job is enqueued
only when the document is new. The same URL bookmarked in several browsers
is one document, fetched once.

`saved_at` is when the browser saved the bookmark, as its importer reads
it. Safari keeps no save date, so its bookmarks carry the time curio
imported them; a manual bookmark (`curio add`), or an imported one that
names no date, carries the time it was added. `created_at` is when curio
added the row. `GET /v1/bookmarks` lists by `created_at` by default and by
`saved_at` with `order=saved`, the Library's Date saved order, each
through its own index (`idx_bookmarks_tenant_created`,
`idx_bookmarks_tenant_saved`); `idx_bookmarks_document (document_id,
saved_at, id)` reads one document's bookmarks newest saved first.

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
  kind          TEXT NOT NULL                  -- 'fetch' | 'index' | 'cluster'; 'import' and 'summarize'
                                               --   are reserved: allowed by the CHECK, never enqueued
  payload       JSON NOT NULL
  document_id   UUID FK                        -- → documents(id), ON DELETE SET NULL
  status        TEXT NOT NULL                  -- 'pending' | 'running' | 'done' | 'failed'
  attempts      INTEGER NOT NULL DEFAULT 0
  run_after     TIMESTAMP NOT NULL DEFAULT now -- when a pending job may run: a retry's backoff, a deferral's hold
  last_error    TEXT                           -- a failed job's error; a pending job's retry error or its
                                               --   "waiting for …" reason; cleared when done
  started_at    TIMESTAMP                      -- set when claimed
  created_at, updated_at
```

`document_id` is the document a fetch or index job works on. It is the
payload's `document_id`, copied into a column when the job is inserted;
the payload keeps it, since the API returns payloads verbatim. It is NULL
for a job that names no document (cluster), and for a job whose document
has been deleted: the foreign key is `ON DELETE SET NULL`, so the job's
history (its `last_error`, its durations) outlives the document. A job
whose payload names a document that doesn't exist can't be enqueued.

Workers claim with a single `UPDATE jobs SET status = 'running' ... WHERE
id = (SELECT id FROM jobs WHERE status = 'pending' AND kind = ? AND
run_after <= now ORDER BY run_after, created_at LIMIT 1) RETURNING ...`, so
jobs are claimed in the order they became runnable. `idx_jobs_claim
(status, kind, run_after, created_at)` turns a one-kind claim into an index
seek and the first row, however many jobs are queued. An attempt that
failed and may be retried gets exponential backoff via `run_after`, and
keeps its error in `last_error`. A job deferred because curio held its
fetch back itself (an upstream's rate-limit cooldown) goes back to
`pending` with `run_after` at the hold's end, the attempt refunded, and
`waiting for …` in `last_error`, for up to a day from `created_at`. So a
pending job whose `run_after` is still ahead waits, and its `last_error`
says why. `MarkDone` clears `last_error`.

Idle workers don't poll on a fixed tick. The store signals, per kind,
when a job is enqueued or put back to pending in this process
(`JobQueue.Enqueued`), and a worker wakes on that; between signals it
polls, starting at 500 ms and backing off to 5 s, which is how it finds
retries coming due and jobs other processes enqueued.

Every claim first waits on the queue gate (`queue_settings`, below): while
the queue is paused, outside its daily schedule, or at the throttle's cap
for a kind, workers make no claim, and a pending job stays pending however
long it has been runnable. Running jobs are never interrupted.

### `queue_settings`

The queue gate's settings, and keep-awake: one row, daemon-wide, with no
`tenant_id`, since workers claim across tenants. No row means the
defaults: not paused, `normal`, no schedule, keep-awake off.

```
queue_settings
  id              INTEGER PK                   -- always 1
  paused          INTEGER NOT NULL DEFAULT 0   -- 0 | 1
  throttle        TEXT NOT NULL DEFAULT 'normal'   -- 'normal' | 'gentle'
  schedule_start  INTEGER                      -- minutes after local midnight, 0..1439; NULL: no schedule
  schedule_end    INTEGER                      -- set with schedule_start, never equal to it
  updated_at
  keep_awake      INTEGER NOT NULL DEFAULT 0   -- 0 | 1: hold the Mac awake while there is work, on AC power (013)
```

`keep_awake` never gates a claim: the daemon's keep-awake keeper reads it
to decide whether to run `caffeinate` while the workers have work.

The daemon reads the row once at startup and refuses to start if it
can't; it writes it only when `PUT /v1/queue` changes a setting. The
schedule is a window of the daemon's local wall clock, from
`schedule_start` up to `schedule_end`, wrapping midnight when the end is
the smaller.

### `cluster_runs`

One row per clustering execution. The clusters of the latest `done` run are
what surface as interests.

```
cluster_runs
  id                UUID PK
  tenant_id         TEXT NOT NULL
  status            TEXT                       -- 'running' | 'done' | 'failed'
  algo              TEXT                       -- clusterer name, e.g. 'knn-graph'
  params            JSON                       -- clusterer parameters + the engine's center flag
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
cluster centroid (the normalized mean of its members' vectors).

```
clusters
  id                UUID PK
  tenant_id         TEXT NOT NULL
  run_id            UUID NOT NULL FK           -- → cluster_runs(id), ON DELETE CASCADE
  label             TEXT                       -- nullable; topic name
  summary           TEXT                       -- nullable
  size              INTEGER
  cohesion          REAL                       -- mean member cosine to centroid, 0..1
  created_at, updated_at
```

### `cluster_documents`

Cluster membership, one row per (cluster, document). Noise docs simply have no
row.

```
cluster_documents
  cluster_id        UUID NOT NULL FK           -- → clusters(id), ON DELETE CASCADE
  document_id       UUID NOT NULL FK           -- → documents(id), ON DELETE CASCADE
  similarity        REAL                       -- cosine to centroid, 0..1
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

Critical for dedup. The normalized URL is both the dedup key and the URL
that gets fetched, so normalization never changes which resource it names,
and applying it twice changes nothing. It accepts only http(s) URLs with a
host, lowercases scheme and host, drops default ports and fragments, turns
an empty path into `/`, removes common tracking params (`utm_*`, `fbclid`,
`gclid`, ...) and sorts the remaining query params; query pairs it can't
decode are kept rather than dropped. Implementation lives in
`internal/urlutil/normalize.go` (one place, one impl, table-tested and
fuzzed). See `docs/decisions.md` "URL normalization: fetch-equivalent and
idempotent".

## On-disk content layout

```
~/.curio/content/
  <document_id>/
    <extraction_id>.md            # one per extraction; the document points at its current one
```

Keeping content on disk rather than in SQLite:

- Database stays small and fast to back up
- `grep`/`ripgrep` works directly against the corpus
- Easy to delete a document's content without DB surgery

## Schema versioning and migrations

Migrations live in `migrations/` and run via `pressly/goose` at daemon startup,
one at a time: while they run, the daemon answers as starting and
`/v1/healthz` reports how many are applied.
The schema version is the highest version in goose's `goose_db_version`
table, the one source of truth for it. The daemon copies it into
`.curio-meta.json` after migrating, as a cache for `/v1/healthz`,
`curio version` and `curio doctor`. Downgrade is not supported — backup
before major version bumps.
