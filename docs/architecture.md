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
│   curio CLI  │  │ curio-mcp   │  │  Dashboard   │
│  (cobra)     │  │ (sidecar)   │  │ browser /ui/ │
└──────┬───────┘  └──────┬──────┘  └──────┬───────┘
       └─────────────────┼────────────────┘
        HTTP + JSON (/v1), HTML pages (/ui/)
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
              │ FTS5 +   │  │ (embed +   │  │ native / │
              │ sqlite-vec│  │  LLM)     │  │ GitHub / │
              └──────────┘  └────────────┘  │ yt-dlp / │
                                            │ web2md   │
                                            └──────────┘
```

### `curio` (CLI)

A Cobra-based CLI, thin client over the daemon's HTTP API. Subcommands:

- `curio up` — set curio up, or check that it is (see "Setup: `curio up`")
- `curio doctor` — check everything `curio up` checks, and the fetcher,
  the Jina fallback and the content directory, changing nothing
- `curio add <url>` — manually add a bookmark
- `curio import <source> [path]` — bulk import from Chrome / Safari / Firefox
- `curio search <query>` — hybrid search
- `curio ui [--print]` — open the dashboard in the browser (see
  `docs/ui.md`), or print its address
- `curio status [--follow]` — daemon health, doc counts, job queue depth
  and state, and a warning while the Jina fallback is failing; `--follow`
  then follows the queue until it drains
- `curio pause` / `curio resume` — stop starting jobs, and start again
  (running jobs finish; the pause survives restarts)
- `curio throttle gentle|normal` — fewer jobs at once to spare the
  machine, or every worker
- `curio schedule HH:MM-HH:MM|off` — start jobs only in a daily window
- `curio keep-awake on|off` — keep the Mac from idle sleep while jobs are
  queued and it runs on AC power
- `curio daemon {start|stop|status|logs|install|uninstall}` — lifecycle
  management, and the launchd agent (see "Daemon lifecycle")
- `curio refetch <id|--all>` — force re-extract; `--all` narrows with
  `--state`, and with `--cause` to one cause of failures (`anti_bot`,
  `tls`, ...)
- `curio reindex <id|--all>` — re-chunk and re-embed existing extractions
  (after chunker or embedding-prefix changes, or to pick up new tags)

If a CLI command needs the daemon and it isn't running, the CLI auto-starts it.
Every command but `up`, `doctor` and bare `curio` finds the home and its
daemon first (`daemonctl.Discover`), which creates a missing home with
the default embedding model; those three build their own environment and
create nothing to look at it. Bare `curio` prints its help, and points at
`curio up` when there is no home, or its daemon serves an empty library.

### Setup: `curio up`

`internal/setup` is the setup wizard, and `curio up` a thin command over
it. Six steps, in order: the machine (chip, memory, cores, free disk,
judged before anything is installed), Ollama (something answering
`embedding.base_url`, started or installed with Homebrew or the app when
not), the models (picked by unified memory from a static table, pulled
with progress), the home and its config.yaml (created where there is
none, at the width the embedding model measures; `--fresh` moves an old
one aside first), the daemon (its launchd agent installed, the daemon of
this build, with config.yaml's writing model), and the import (for an
empty library, or `--import`: the sources `importer.Discover` finds, each
counted by the daemon's dry run, then yt-dlp for new YouTube videos, the
estimates, the pace and keep-awake set on the queue, and the bookmarks
sent through the same batched import `curio import` uses). Each step
checks the world, read-only and bounded, and may apply a fix; the runner
checks every step first and shows the fixes as the plan, stops before
applying anything when the plan has a blocker, then applies step by step,
asking before each, and checks each again after. A run with nothing to do
changes nothing and shows the status; a run cut short resumes by checking
again. Its prompts, progress and the output of what it runs go to stderr,
the plan and the status to stdout. `curio doctor` runs the same checks,
so the two agree on what healthy means. Installing goes through
`setup.Installer` (Homebrew), the machine through `setup.Probe`, the
prompts through `setup.UI` (huh on a terminal, a line prompter in
accessible mode, none without a terminal), the daemon's agent through
`internal/service.Manager`, and the bookmarks through `importer.Source`
(`setup.Deps.Sources`), so the tests fake each and never read the
browsers of the machine they run on. See `docs/setup.md` and decisions.md
"curio up: a plan-first setup wizard" and "curio up: the import step".

### `curio-daemon`

Long-running background process. Owns the SQLite database, the job queue, and
all fetch/index/search/insight workflows.

- HTTP+JSON API on `127.0.0.1:8765` (port configurable; the host must be
  loopback). No authentication: it trusts local processes and refuses
  browser-originated requests (Host allowlist, Origin rejection, changes
  only from its own pages by `Sec-Fetch-Site`, JSON-only bodies), except
  its own dashboard's. See `docs/decisions.md` "Local API: loopback only,
  no token, browsers shut out".
- `api/openapi.yaml` is the contract, verified against the router and live
  responses by tests in `internal/api`; the clients (`internal/client`) are
  hand-written, and codegen is deferred
- The dashboard: GET-only HTML pages under `/ui/` on the same port and
  origin (`/` redirects there; `daemon.ui: false` turns them off): search
  is the home at `/ui/`, then the Library (documents most recently
  updated first, or in its Date saved order the saves, newest saved
  first, and its Failures tab at `/ui/failures`, the failed and dead
  documents grouped by cause), the Interests, and Status at `/ui/status`
  (the queue, health, progress and why documents failed).
  Page handlers in `internal/api/ui*.go` read through the same functions
  as the JSON handlers, and `internal/ui` renders them with
  `html/template`, a sanitized render of each document's markdown, one
  stylesheet of design tokens and components with inline SVG icons, and
  a vendored htmx for search-as-you-type and live regions, which pollers
  refresh with `?poll=` (the page's live regions alone, and their reads
  alone). What a page changes (refetch, reindex, rebuild, the queue's
  controls, a failure group's refetch) its own script, `actions.js`,
  sends to `/v1` as JSON, from
  actions built in Go (`internal/ui/actions.go`). Every response carries
  a strict CSP. See `docs/ui.md`, and decisions.md "Dashboard:
  server-rendered pages in the daemon (phase 1)", "Dashboard: a design
  language under the CSP", "Dashboard: search is home, the Overview
  becomes Status", "Dashboard: actions through /v1, sent by a
  first-party module", "Library: a Date saved order lists saves" and
  "Dashboard: the Failures tab"
- Internal worker pools process jobs from the SQLite-backed queue. They
  claim through the queue gate (`jobs.QueueGate`), which holds claims back
  while the queue is paused, outside its daily schedule, or at the
  throttle's cap; its settings live in SQLite (`queue_settings`) and are
  read and changed at `GET`/`PUT /v1/queue`

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
- `find_related(id, k)` — find documents similar to a given one (by embedding similarity over its indexed content)
- `list_interests(limit?, members?)` — labeled interest clusters from the latest clustering run

The sidecar starts the daemon when it starts, without waiting for it to
finish starting, and again when a tool call finds it unreachable
mid-session. A tool call that finds it unreachable or still starting
waits up to 30s for it and retries once.

Registration and usage: see `docs/mcp.md`.

## Transport: HTTP + JSON

Chose HTTP+JSON over gRPC for:

- Trivial debugging with `curl`
- MCP protocol speaks JSON anyway
- Docker uses HTTP and it scales fine
- No protobuf toolchain dependency

OpenAPI spec lives at `api/openapi.yaml`. It is the contract: tests in
`internal/api` check it against the router and validate live responses
against it. The CLI and MCP sidecar share the hand-written client in
`internal/client`; generating clients from the spec is deferred.

## Daemon lifecycle

One daemon per `$CURIO_HOME`, managed via `curio daemon start|stop|status`.
CLI commands and the MCP sidecar auto-start it when it isn't running:
through the home's launchd agent when one is loaded, as a child process
otherwise.

- **Single instance.** The daemon holds an exclusive `flock` on
  `daemon.pid` for as long as it runs and records its PID there. The kernel
  drops the lock however the daemon dies, so "the lock is held" means "a
  daemon is running", with no PID guesswork.
- **Startup order.** Lock, then config, then the home check (a home from
  before format 2, or a `config.yaml` whose embedding model or width isn't
  the marker's, is refused here), then bind the API port and answer as a
  starting daemon, and only then open and migrate the DB, size the vector
  index from the marker, recover orphaned jobs, swap in the full API and
  start workers. A second daemon, one whose port is taken, or one the home
  check refuses exits before touching the DB.
- **Starting API.** Until it is ready the daemon answers every request 503
  with `Retry-After` and a `urn:curio:problem:daemon-starting` problem;
  on `/v1/healthz` the problem also names the daemon (`pid`, `home`) and
  its progress (`phase`, and migrations applied of total while it
  migrates). Migrations run one at a time, and each is logged as it
  starts and finishes. Nothing a starting daemon refused has run, so
  clients resend it once the daemon is ready.
- **Clients** probe the lock for liveness and `/v1/healthz` (which reports
  `pid` and `home`, starting or serving) for identity, allowing for
  healthz's own bounded wait on Ollama. They wait while their daemon
  reports progress, failing after 15s of silence or at a 30 min ceiling
  (which leaves the daemon running), and print one line when it is
  migrating. `daemon.start.lock` serializes starting and is held until the
  new daemon holds `daemon.pid`; installing, removing or restarting the
  launchd agent holds it throughout, bootout waits included, and so does
  `curio up --fresh` from stopping the daemon until the home is moved
  aside (`Controller.WithDaemonStopped`). They only signal the PID the
  lock holder recorded. A daemon they started that crashes during startup
  is reported immediately, with its exit status and the tail of
  `daemon.log` (and of `launchd.err`, for one launchd started).
- **Shutdown** on SIGINT, SIGTERM or SIGHUP: stop accepting work, give
  in-flight HTTP requests 5s and running jobs 15s, record every outcome
  (interrupted jobs are requeued with their attempt refunded), then release
  the lock. Jobs abandoned after the grace period are recovered as orphans
  on the next start. A daemon told to stop exits 0, even when its
  shutdown ran over, and so does one that finds another daemon serving
  its home, or refuses to start for a cause only a fix clears (a
  config.yaml or a home it can't serve, logged once as it stays down);
  anything else it can't run with, a port it can't bind or a crash, exits
  1, which the launchd agent retries.
- **launchd agent** (macOS, `curio daemon install`): a per-user agent,
  `~/Library/LaunchAgents/com.github.samsar.curio.daemon.plist` for
  `~/.curio` (another home's label adds a hash of its path), that starts
  the daemon at login and restarts it when it exits non-zero
  (`KeepAlive {SuccessfulExit: false}`), giving it 25s to exit after
  SIGTERM. With the agent loaded, clients start the daemon with `launchctl
  kickstart`, stop it with `launchctl kill SIGTERM` and restart it with
  `kickstart -k`, all behind `internal/service.Manager`; without it (not
  installed, not loaded, or no GUI login session over ssh) they spawn it.
  The lock stays the safety net either way. Installing asks the manager
  first (`Preflight`: not root, a program launchd can run, a GUI session)
  and stops nothing for an install that can't happen. `curio daemon
  uninstall` boots the agent out and waits until the daemon has let go of
  the home.
- **Keep-awake** (`curio keep-awake on`): while keep-awake is on, the
  queue isn't paused, the Mac runs on AC power (`pmset -g ps`) and the
  workers have jobs queued or running, the daemon runs `caffeinate -i -w
  <its pid>`; it lets go when the queue drains, on battery, when paused
  or turned off, and as it stops (`internal/keepawake`).

## Storage layout

Everything under `$CURIO_HOME` (defaults to `~/.curio`).

```
~/.curio/
  .curio-meta.json       # marker file: format, schema_version, embedding model + dim,
                         # and the model digest + Ollama version that made the vectors
  config.yaml            # user config
  curio.db               # SQLite database (metadata, jobs, FTS5, vectors)
  content/               # extracted markdown, on disk
    <document_id>/
      <extraction_id>.md   # one file per extraction; the document points at its current one
  logs/
    daemon.log           # the daemon's JSON log (its stdout)
    launchd.err          # a launchd-run daemon's stderr: only a dying runtime's output
  daemon.pid             # single-instance lock (flock) + the running daemon's PID
  daemon.start.lock      # serializes clients auto-starting the daemon
  setup.json             # what curio up remembers: optional installs declined
```

`curio up` writes `config.yaml` once, when there is none, and never edits
it after; `setup.json` is its own.

If `~/.curio` exists without `.curio-meta.json`, the daemon refuses to start and
suggests setting `CURIO_HOME` to a different path.

## Data flow

The CLI parses a bookmark file (`importer.Source`) and posts it in
batches; `curio up` first posts the distinct URLs with `dry_run` to count
what is new, which writes nothing.

```
bookmark file ──► importer ──► bookmark + document ──► fetch job (new documents only)
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
                              └── curio interests rebuild ──► cluster job
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
        ┌──► BM25 (FTS5) ─────────────────────────────────► top N chunks ─┐
query ──┤                                                                 │
        └──► embed (Ollama) ──► vector ANN (sqlite-vec) ──► top N chunks ─┤
                                                                          ▼
                                                                 RRF fusion (k=60)
                                                                          │
                                                                          ▼
                                                            collapse chunks → documents
                                                                          │
                                                                          ▼
                                                                    top k results

N = max(50, 8·k)
```

The two legs run concurrently, and metadata filters (content type, host,
source) apply inside both. Neither leg returns failed or dead documents: they
keep the chunks of an earlier fetch, which no longer describe what the URL
serves. A BM25 failure fails the search. A vector-leg failure — Ollama
down, or no answer within `search.embed_timeout_seconds` — returns the BM25
results marked `degraded` with a warning. Knobs in config:
BM25/vector weights in RRF, chunk-to-doc collapse strategy (max vs sum vs
top-3-avg), `default_k`, and `embed_timeout_seconds`.

## Pluggability: where interfaces live

The interfaces with explicit swap paths:

1. **`store.*Store`** (`internal/store`) — `DocumentStore`,
   `ExtractionStore`, `BookmarkStore`, `ChunkStore` (FTS5 keyword and
   sqlite-vec vector search), `JobQueue`/`JobStore` and `InsightStore`. One
   SQLite implementation (`internal/store/sqlite`); Postgres + pgvector
   when hosted mode is wanted. depguard keeps every other package on the
   interfaces.
2. **`embedder.Embedder`** — embedding model client. Ollama impl; Voyage /
   OpenAI for cloud. A home's embedding model and vector width are fixed
   when it is created and recorded in its marker; another model means a
   new home (see [decisions](./decisions.md#embedding-model-and-per-home-width)).
3. **`generator.Generator`** — LLM text generation, used for cluster labels.
   Ollama impl.
4. **`fetcher.Fetcher`** — content fetcher: `native` (Go HTTP + Readability,
   with Jina Reader as its fallback), `web2md`, `github`, `youtube`,
   selected per URL by the rules engine.
5. **`insight.Clusterer`** and **`insight.Labeler`** — the clustering
   algorithm (kNN graph) and cluster naming (LLM or term labels).

Do not abstract until you have two impls. The interfaces above are commitments
because we already know we want hosted mode, model swaps, and multiple fetchers.

## Multi-tenancy stance

Single-tenant in deployment; multi-tenant in the schema. Every top-level entity
carries a `tenant_id` (hardcoded to `"local"` for personal installs). Hosted
mode is a deployment change, not a schema change. See
[data model](./data-model.md#multi-tenancy).

## Dependencies

External processes the daemon expects:

- **Ollama** — for embeddings and local text generation. Daemon talks to it on
  `http://localhost:11434`; `curio up` starts or installs it when nothing
  answers there, and pulls the models. The daemon runs without it and
  degrades: search returns keyword-only results marked `degraded`, index jobs fail and retry
  with backoff, cluster labels fall back to term labels, and `/v1/healthz`
  (and `curio doctor`) says what's wrong. It pulls the models it needs,
  retrying until Ollama answers. Generation is abstracted behind a
  `generator.Generator` interface (local Ollama `/api/generate` impl), used
  for LLM cluster labels in the insight layer. Embed requests never let
  Ollama truncate an input, and generate requests turn thinking off. The
  daemon records the embedding model's digest and Ollama's version in the
  marker, checks them every minute, and reports a change as
  `embedding_drift` on `/v1/healthz`, in `curio doctor` and in `curio
  status`, until `curio reindex --all` re-embeds the library.
- **Node + web2md** (the user's existing tool) — invoked as a subprocess by the
  optional `web2md` fetcher. Not needed: the default fetcher is Go-native.
- **Jina Reader** (`r.jina.ai`, `fetcher.native.jina_base_url`) — the native
  fetcher's fallback for anti-bot and login-wall pages and PDFs it can't
  read; off with `fetcher.native.jina_fallback: false`. Requests to it
  identify as curio, not as a browser. Its answers are judged like the
  origin's before they are stored: challenge, block, error, not-found and
  login pages are rejected. A redirect onto another site is judged where it
  lands, without Jina: a landing page is a dead link and a login page
  final, even when it answers 403 or 503 (then only a login path counts:
  no page is read). Its health (each request's outcome over the last 15
  minutes, and whether it is paused or failing) is reported on
  `/v1/healthz` (`upstreams`), in `curio doctor` and in `curio status`.
- **Claude API (optional)** — a future `generator.Generator` impl for heavier
  synthesis on the M6 RAG path (retrieve → LLM → cited answer).

## What's deliberately not in v1

- Browser history ingestion (data model supports it; importer doesn't yet)
- Read-later / highlights (Pocket, Instapaper, Readwise)
- Trajectory analysis / "new this month" detection (interest clustering itself
  landed in M4 — see the insight layer)
- A web UI beyond the dashboard (`docs/ui.md`): its pages refetch,
  reindex, rebuild, control the queue and group failures by cause;
  richer views are planned
- Authentication (single-tenant local: the API binds loopback only and
  refuses browser-originated requests; see decisions.md "Local API: loopback
  only, no token, browsers shut out")
