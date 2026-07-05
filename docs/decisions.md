# Decisions

A running log of design decisions, what we picked, and why. New entries go at
the bottom. When a decision is revisited, add a new entry rather than rewriting
the old one — the history is useful.

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

**Forward compatibility:** All access goes through `DocumentStore`, `BM25Index`,
`VectorIndex` interfaces. Postgres + pgvector impls land when hosted-mode
demands them.

---

## Job queue: SQLite-backed

**Decision:** A `jobs` table with worker pool polling, not a dedicated queue
(asynq, NATS, Redis, ...).

**Why:** One less moving part. At single-user scale, a few thousand jobs/day is
trivial for SQLite. Polling interval ~1s is fine.

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

---

## Daemon lifecycle: PID file + auto-start

**Decision:** `curio daemon {start|stop|status|logs}` manages the daemon via a
PID file in `~/.curio/daemon.pid`. CLI commands that need the daemon auto-start
it if not running.

**Why:** Most ergonomic for a single-user tool — user never has to think about
it. Skip `launchd`/`systemd` complexity for v0.

**Later:** `curio service install` drops a `launchd` plist (macOS) or
`systemd` unit (Linux) for boot-time auto-start.

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

**Decision:** BM25 and vector run in parallel, results merged via Reciprocal
Rank Fusion (RRF, k=60), then chunks collapse to documents.

**Why:** BM25 wins on rare terms and proper nouns; vector wins on conceptual
matches; RRF is the standard, simple, parameter-light fusion method.

**Knobs exposed in config:** BM25/vector weights in RRF, chunk-to-doc collapse
strategy.

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

**Decision:** List endpoints use opaque cursors (`?cursor=...` + `next_cursor`
in the response), not offset/limit.

**Why:** Offset is buggy under concurrent writes — rows land between page
fetches and clients silently skip data. Cursors are stable on SQLite via
`WHERE id > :cursor ORDER BY id LIMIT N`. Cost is the same.

---

## API: all long-running operations are async with job IDs

**Decision:** Imports, refetches, and (later) reindex operations return
`202 Accepted` with `{ job_id }`. Clients poll `/v1/jobs/{id}` for status.

**Why:** Imports can take minutes (10k+ bookmarks → 10k+ fetches). Refetches
can take seconds-to-minutes per URL. Sync responses tie up HTTP connections
and force timeout tuning. Polling is simple and observable.

The CLI hides the polling from the user (`curio import chrome` blocks with a
progress bar). The MCP sidecar can either poll or hand the `job_id` back and
let the next turn check.

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
`file:/path/curio.db?_fk=true&_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000`.

**Why:** SQLite's `foreign_keys` PRAGMA is *per-connection* and defaults to
OFF — every connection in `database/sql`'s pool must turn it on, or FK
constraints are silently unenforced. Running `PRAGMA foreign_keys = ON` in
migrations only affects that one connection. The DSN-level params apply to
every new pooled connection.

Note: `_pragma=foreign_keys(1)` syntax is for `modernc.org/sqlite`, NOT
`mattn/go-sqlite3`. They look similar; mixing them silently no-ops.

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

**Why:** The transaction approach deadlocks under concurrent workers. The
initial SELECT takes a SHARED lock; when each worker tries to upgrade to a
RESERVED lock for the UPDATE, they all block on each other and SQLite's
busy_timeout doesn't save us under load. The single-statement form acquires
the write lock immediately, serializes cleanly across workers, and is also
shorter code.

Tested with 20 jobs / 8 workers / `-race`: every job claimed exactly once,
no duplicates, no errors. The test lives in jobs_test.go specifically so the
multi-worker semantics are locked in before the M1 worker-pool expansion.

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

---

## Marker file's schema_version is synced from the DB after migrations

**Decision:** On daemon startup, after running migrations, we read the
current `schema_meta.schema_version` and write it back to
`.curio-meta.json` so `/v1/healthz` reflects the actual schema state.

**Why:** The marker file is set at `curiohome.Init` time using the
"current" schema version (a constant). Migrations bump
`schema_meta.schema_version` but the marker doesn't update on its own,
so after migration 002 ran, `curio status` still reported "schema: v1".
The DB is authoritative; the marker mirrors it.

---

## Chunker enforces a 3500-char hard cap (not just 384 words)

**Decision:** `ChunkOptions.SizeChars` (default 3500) is a hard upper bound
on chunk byte length applied AFTER the word-count chunking pass. Chunks
that exceed the limit are split at word boundaries with a small overlap.

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
  `user_agent` together.**
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
  any job whose `updated_at` is older than the duration. Accepts Go
  duration syntax plus `Nd` for days.
- `curio jobs delete --status failed` — exact-status delete; status is
  required.

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
"Bookmarks Menu" rather than their internal identifiers.

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
pseudo-bookmarks, not real folders — its subtree is skipped so tagged URLs
don't double-count. Root GUIDs map to friendly labels ("Bookmarks Menu",
"Bookmarks Toolbar", "Other Bookmarks", "Mobile Bookmarks").

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

- **Insight layer endpoints** (`/v1/clusters`, `/v1/interests`,
  `/v1/suggestions`) — added in milestone M4.
- **Config endpoints** (`GET/PUT /v1/config`) — for now, edit
  `~/.curio/config.yaml` and `SIGHUP` the daemon.
- **Admin reindex endpoint** (`POST /v1/admin/reindex`) — added when an
  embedding model swap is an actual need. Until then, run `curio reindex` CLI.
- **Server-Sent Events for job progress** — polling is fine for v1. If the
  CLI's progress UX gets ugly, add `/v1/jobs/{id}/stream`.
- **Generic batch endpoints** for bookmarks, documents, or jobs. Add only
  when a real non-file source needs them.
- **Authentication scheme** (API keys, OAuth, SSO). Middleware hook is in
  place; the actual mechanism is deferred to hosted-mode work.
- **WebSocket or streaming search** — current `POST /v1/search` is fine.

---

## What's not decided yet

- **Insight layer specifics:** clustering algorithm, summarization prompts,
  trajectory analysis. Deferred until the corpus layer is working and we have
  real data to look at.
- **Authentication for hosted mode:** middleware stub goes in early but the
  actual auth scheme (API keys vs OAuth vs SSO) is deferred.
- **Re-crawl policy:** how often to refetch a given URL. Likely
  domain-rule-driven (news daily, docs monthly, static essays never).
- **Highlight / read-later importers:** schema is ready; importer code is not
  in v1.
- ~~**"Page Not Found" detection**~~ ✅ implemented — see the
  "Dead-link detection: hard 404/410 + soft-404 heuristics" entry
  below (title patterns + redirect-to-homepage; dead docs go to
  state `dead`). The embedding-based "this isn't really an article"
  classifier remains a possible future refinement.

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

  **What's NOT under consideration:** building our own tokenizer,
  custom synonym dictionaries, query-classification pipelines. The
  hybrid retriever + RRF was chosen specifically to keep retrieval
  simple; any "smartness" should live in the query-rewriting layer
  above the retriever, not inside it.

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

**yt-dlp stderr handling:** On failure (`cmd.Run` returns error),
extract only `ERROR:` lines from stderr. Ignore `WARNING:` lines
(e.g., "ffmpeg not found", impersonation warnings) that are noisy
but harmless.

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

**No archive.org fallback (yet):** the roadmap sketched a Wayback
fallback for dead links; scoped out of this pass to keep detection
observable on its own. If added later, it belongs in `Native.Fetch`
next to the Jina gate, modeled on `tryJina`.

---

## fetcher_rules.yaml: mtime-polled hot reload, keep-last-good

**Decision:** the data-driven fetcher routing designed in
architecture.md is now real. `$CURIO_HOME/fetcher_rules.yaml` lists
rules top-to-bottom, first match wins; matchers are `host` (exact),
`host_suffix` (label-boundary suffix: "youtube.com" matches
"m.youtube.com" and "youtube.com" but not "evilyoutube.com"),
`host_in` (list of exact hosts), or `{}` (catch-all). Rules bind to
fetchers by `Fetcher.Name()` against a registry the daemon builds at
startup (default fetcher, github, youtube-if-yt-dlp-present).

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

**Contract edges:** unknown doc → 404; known doc with no indexed
chunks (pending/failed/dead) → 200 with empty items (the document
exists; it just has no vectors yet — kinder to MCP callers than a
409). Scores are raw vector similarities (1/(1+L2), 0..1) and NOT
comparable with /v1/search's RRF-fused scores; the openapi
description says so.
