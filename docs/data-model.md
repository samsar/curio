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
- `interest_runs`, `interests` (insight layer: runs and the identities
  that outlive them), and `insight_state`

Child tables (`documents`, `chunks`, `document_extractions`, and the
run-scoped `interest_groups`, `interest_assignments` and
`interest_placements`, which inherit their tenant through `interest_runs`)
**do not** carry `tenant_id`. `interest_lineage` is reached through the
identities it names. They are reached only
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
  indexed_at            TIMESTAMP              -- when its vectors were last written; NULL until it is fetched
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

`indexed_at` (migration 017) is when the document's vectors were last
written: the index step's `MarkFetched` sets it, in the statement that
marks the document fetched and to the same instant as `updated_at`, and no
other write touches it (a refetch leaves it, so it says when the vectors
the document still has were written). The interests read it: what changed
since a rebuild is counted from it, and the scheduler waits until nothing
was indexed for a while. `idx_documents_tenant_indexed (tenant_id,
indexed_at)` serves both: the documents indexed since a time, and the
latest time. 017 set it to `updated_at` for every document fetched then,
which `MarkFetched` alone leaves fetched.

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
retries and deferred jobs coming due and jobs other processes enqueued.
`Defer` signals nothing: its job isn't runnable before its `run_after`.

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

### Interests (migrations 016 to 018)

Interests come in two levels: areas, each holding interests, in a library
large enough to have them, and interests alone in a smaller one (the run's
`shape`, `areas` or `flat`). A rebuild is a run; the groups it found are
identities that outlive it, so an interest keeps its ID, label and links
across rebuilds while it keeps most of its documents. Migration 016
replaced 004's `cluster_runs`, `clusters` and `cluster_documents` (one
flat partition per run, under IDs that lasted that run alone) and dropped
their rows: nothing in them could seed the new grouping, and the daemon
regroups the library on its own once it holds 20 fetched documents and
nothing has been indexed for 10 minutes (at once on a library already
quiet). The reasoning and measurements are in `decisions.md` "Interests:
two levels, stable identities, automatic rebuilds".

Vectors (`mean`, `centroid`) are float32 little-endian BLOBs, NULL when
absent.

Migration 018 keeps each run's interest map on the same rows: two views
of the grouping, the document map (every document placed so that similar
documents sit together) and the zoom view (each interest a circle of its
documents inside its area's), every position in a square of side 1000.
The map's status and its view-wide values are on `interest_runs`, each
group's circle and label anchor on `interest_groups`, each document's two
positions on `interest_assignments` and `interest_placements`. All are
nullable columns, NULL for a run from before 018 and for a map that
failed; CHECKs keep a run from being half a map and a row from being half
placed. See `decisions.md` "Interest map: two views of each regrouping,
drawn when it is built".

#### `interest_runs`

One row per rebuild attempt, `running → done | failed`. The tenant's
latest `done` run is the current grouping; a done run never changes.

```
interest_runs
  id                  UUID PK
  tenant_id           TEXT NOT NULL
  status              TEXT     -- 'running' (default) | 'done' | 'failed'
  trigger             TEXT     -- what queued it: 'first' | 'manual' | 'auto' | 'reindex' | 'params' | 'shape'
  kind                TEXT     -- 'fresh' | 'warm' (started from the previous run's seeds)
  split_check         INTEGER  -- 0 | 1: it ran the split check
  shape               TEXT     -- 'flat' | 'areas'
  grouper             TEXT     -- the grouper's name, e.g. 'louvain'
  params              JSON     -- the grouper's constants + the engine's center flag; a change makes the next run fresh
  vectors_read_at     TIMESTAMP  -- when it read the document vectors
  mean                BLOB     -- the centering mean; NULL when not centered
  num_documents, num_areas, num_interests, num_loose, num_unsorted   INTEGER
  changed_documents   INTEGER  -- documents added, left, deleted or reindexed since the previous run read its vectors
  changes_since_split INTEGER  -- changes absorbed since the last split check
  kept, created, split, merged, moved, dissolved   INTEGER  -- what it did to interest identities
  error               TEXT     -- set when failed
  started_at, finished_at, created_at, updated_at
  -- the map (018):
  map_status          TEXT     -- 'built' | 'failed'; NULL: none drawn (a run from before 018, or one not done)
  map_kind            TEXT     -- a built map's: 'fresh' | 'warm' (its document map started from the previous run's)
  map_error           TEXT     -- a failed map's, one line
  map_ms              INTEGER  -- how long drawing it took
  map_params          JSON     -- every layout constant, the seed and center; another value starts the next map cold
  map_dot_radius      REAL     -- a built map's dot radius in the zoom view
  map_unsorted_x, map_unsorted_y, map_unsorted_r  REAL  -- a built map's Unsorted disc
```

A CHECK on `map_unsorted_r` ties the map's columns together: a NULL status
leaves every one NULL; `failed` sets the error, time and params and no
dot radius or disc; `built` sets the kind, time, params, a dot radius
above 0 and the disc with a radius above 0, and no error.

`idx_interest_runs_tenant_status (tenant_id, status, started_at DESC)`
finds the latest done run, which every read starts from. A rebuild that
commits prunes the runs before it (their groups, assignments and
placements cascade); one that fails is kept beside the current run until
the next rebuild, so a reader can see why.

#### `interests`

The identities, areas and interests alike. An identity carries the label;
a run's groups reference it.

```
interests
  id              UUID PK
  tenant_id       TEXT NOT NULL
  level           TEXT     -- 'area' | 'interest'
  label, summary  TEXT     -- NULL until labeled
  label_source    TEXT     -- 'llm' | 'terms' | 'user'
  created_run_id  UUID NOT NULL   -- the run that minted it (no FK: runs are pruned)
  labeled_at      TIMESTAMP
  retired_at      TIMESTAMP       -- set by the run that no longer holds it
  retired_run_id  UUID            -- that run (no FK)
  created_at, updated_at
```

A run that doesn't hold a live identity retires it. A retired identity is
kept 180 days with its lineage, so an old link can say what became of it
(`GET /v1/interests/{id}` answers 410 with its successors), then deleted.
`idx_interests_retired (tenant_id, retired_run_id) WHERE retired_at IS NOT
NULL` serves both "what did this run retire" and the retention sweep.

Refinements over the design's sketch, and why:

- `retired_run_id` (and its index): the changes a run made, and the 410,
  name the run that retired an identity, which `retired_at` alone doesn't
  say.
- `created_run_id` is NOT NULL: every identity is minted by a run, and a
  run's new identities are read through it.
- `label_source` accepts `'user'` before anything writes it (renames come
  later): four tables reference `interests`, so widening the CHECK later
  would mean rebuilding the table.

#### `interest_groups`

An identity as one run found it.

```
interest_groups
  run_id       UUID NOT NULL FK   -- → interest_runs(id), ON DELETE CASCADE
  interest_id  UUID NOT NULL FK   -- → interests(id)
  parent_id    UUID FK            -- an interest's area; NULL for areas and flat interests
  size         INTEGER            -- members (an area: its interests' members)
  loose        INTEGER            -- loose fits
  cohesion     REAL               -- an interest: mean member cosine to its centroid
  centroid     BLOB               -- an interest's members' unit mean; NULL for areas
  zoom_x, zoom_y, zoom_r  REAL    -- its circle in the zoom view (018)
  anchor_x, anchor_y      REAL    -- its label's anchor on the document map (018)
  similar      JSON               -- an interest's 3 most similar interests, [{"id", "cosine"}] (018)
  PRIMARY KEY (run_id, interest_id)
```

The five map columns are all set (with `zoom_r` above 0) or all NULL;
`similar` is set for every interest of a run from 018 on, map or no map,
and is valid JSON when set.

`idx_interest_groups_list (run_id, parent_id, size DESC, cohesion DESC,
interest_id)` serves the top-level page, an area's interests and every
interest of a run in the order the API pages them.

#### `interest_assignments`

Every document a run grouped, once.

```
interest_assignments
  run_id         UUID NOT NULL FK   -- → interest_runs(id), ON DELETE CASCADE
  document_id    UUID NOT NULL FK   -- → documents(id), ON DELETE CASCADE
  interest_id    UUID FK            -- NULL: unsorted
  area_id        UUID FK            -- the area whose community holds it; NULL in the flat shape and outside every area
  fit            TEXT               -- 'member' | 'loose' | 'unsorted'
  similarity     REAL               -- to its interest's centroid; unsorted: to the nearest interest
  nearest_id     UUID FK            -- unsorted only; NULL when there is no interest
  area_seed, interest_seed  INTEGER -- the next warm start's seeds; -1 for none
  map_x, map_y, zoom_x, zoom_y  REAL -- its place on the document map and in the zoom view (018); all or none
  PRIMARY KEY (run_id, document_id),
  CHECK ((fit = 'unsorted') = (interest_id IS NULL)),
  CHECK (fit = 'unsorted' OR nearest_id IS NULL)
```

A member names its interest; a loose fit sits close to an interest it
wasn't grouped with (shown after its members, never used to name it);
unsorted documents are in no interest. Refinements: `area_id`, because
carry-over matches areas by their communities, which `interest_id` and the
groups' `parent_id` can't rebuild for a document in an area but in no
interest (8 of them in a fresh grouping of the author's library); the
CHECKs, so a row can't be half one fit; and
`idx_interest_assignments_document (document_id)`, so deleting a document
seeks its rows instead of scanning every kept run's.
`idx_interest_assignments_list (run_id, interest_id, fit, similarity DESC,
document_id)` serves members, loose fits and the unsorted, each most
similar first.

#### `interest_placements`

Documents placed into the current grouping between rebuilds, newest first
per interest (`interest_id` NULL: unsorted, `similarity` then to the
nearest interest). The index step writes one when it marks a document
fetched, and a sweep after each rebuild, and at the daemon's start, those
it missed; every write checks its run is still the latest done one and
didn't group the document. They go with their run.

```
interest_placements
  run_id       UUID NOT NULL FK   -- → interest_runs(id), ON DELETE CASCADE
  document_id  UUID NOT NULL FK   -- → documents(id), ON DELETE CASCADE
  interest_id  UUID FK            -- NULL: unsorted
  similarity   REAL
  placed_at    TIMESTAMP
  map_x, map_y, zoom_x, zoom_y  REAL  -- its place on the run's map, near its most similar mapped documents (018); all or none
  PRIMARY KEY (run_id, document_id)
```

Indexed by `(run_id, interest_id, placed_at DESC)` and, for document
deletes, `(document_id)`.

#### `interest_lineage`

What each run did to an old identity, toward each new one. It outlives
runs: an old link's 410 names its successors from it.

```
interest_lineage
  run_id  UUID NOT NULL           -- no FK: runs are pruned
  old_id  UUID NOT NULL FK        -- → interests(id), ON DELETE CASCADE
  new_id  UUID NOT NULL FK        -- → interests(id), ON DELETE CASCADE
  event   TEXT                    -- 'kept' | 'split' | 'merged' | 'moved'
  shared  INTEGER                 -- the old identity's members the new one holds
  PRIMARY KEY (run_id, old_id, new_id)
```

Each rebuild trims the rows of earlier runs whose old identity is still
live, which would otherwise add a "kept" row per surviving group forever;
a retired identity's rows stay until its retention ends.

#### `insight_state`

Per tenant, what a rebuild can't derive from its runs (rebuilt by
migration 017):

```
insight_state
  tenant_id        TEXT PK
  fresh_owed       TEXT     -- 'reindex' | 'manual': the next rebuild must be fresh; NULL when it needn't be
  fresh_owed_at    TIMESTAMP  -- when it was last owed; NULL exactly when fresh_owed is
  failures         INTEGER  -- rebuilds failed since the last done one
  last_failure_at  TIMESTAMP
  last_error       TEXT
  updated_at       TIMESTAMP
```

`curio reindex --all` owes `reindex` (the scheduler waits for the
re-embedding to drain, and placement holds meanwhile); `curio interests
rebuild --fresh` owes `manual`, which never replaces `reindex`. A fresh
run clears it in its commit only when it was owed at or before the run
read its vectors, so one owed again during a rebuild survives it, and a
`reindex` one only when no index job was left before or after the run read
them, so a rebuild asked for mid-drain leaves it owed. A change of the grouper's
params needs no row: each run records them. Every failed rebuild but a
cancelled one adds a failure; the scheduler waits 15 minutes after the
first, doubling to 4 hours; a done rebuild clears them. There is no shape
column: the current done run's `shape` is the state the grouper reads, and
two copies could disagree.

### Deferred insight tables (not in v1)

Sketched for completeness; not yet built. Suggestions arrive with M5.

```
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
