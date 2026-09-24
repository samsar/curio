# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build, test, lint

```sh
make build               # produces ./bin/curio, ./bin/curio-daemon, ./bin/curio-mcp
make test                # unit tests under -race
make test-integration    # needs Ollama + web2md available
make test-e2e            # boots the daemon end-to-end
make lint                # golangci-lint v2 with the project's .golangci.yml
make fmt                 # gofmt + go mod tidy
make help                # full target list
```

Run a single test:
```sh
go test -race -count=1 -tags=sqlite_fts5,sqlite_json -run TestParseHTML_Basic ./internal/importer/...
```

The build tags are mandatory; without them SQLite FTS5 is missing and `chunks_fts` virtual table creation fails. `make` already passes them. Never invoke `go build` / `go test` directly without `-tags=sqlite_fts5,sqlite_json` — the Makefile is the source of truth.

Cgo is required (sqlite, sqlite-vec). `CGO_ENABLED=1` is forced in the Makefile.

## Tooling traps

- **Go version**: `go.mod` declares `go 1.25.7` because deps (`pressly/goose`, `html-to-markdown/v2`, others) require it. `go mod tidy` run with a different Go version produces a different `go.sum` — CI uses the version in `go.mod` via `go-version-file`. Always tidy with the matching toolchain: `GOTOOLCHAIN=go1.25.7 go mod tidy`.
- **No `toolchain` directive**: removed because golangci-lint v2 sees it as the targeted version. Don't add it back unless you also bump the linter to a version built with a newer Go.
- **golangci-lint v2.12.2** is the pinned version; older v2.0.x was built with go1.24 and rejected our modules. The action is `golangci/golangci-lint-action@v7` (v6 doesn't pull v2.x).

## Architecture in one screen

Three binaries:

```
curio     (CLI, cobra)   ──HTTP+JSON──►  curio-daemon  ──►  SQLite (~/.curio/curio.db)
curio-mcp (MCP sidecar)  ──HTTP+JSON──►       │             ├ FTS5 (chunks_fts)
                                              │             └ sqlite-vec (chunks_vec)
                                              │
                                              ├─► Ollama at http://localhost:11434  (embeddings)
                                              ├─► Native fetcher (Go-native HTTP + Readability)
                                              └─► Optional web2md (Node subprocess)
```

`curio-mcp` (`cmd/curio-mcp`) speaks MCP over stdio to clients like Claude Code and forwards tool calls to the daemon over the same HTTP API the CLI uses; it auto-starts the daemon. See `docs/mcp.md`.

- Storage state lives under `$CURIO_HOME` (default `~/.curio`): `curio.db`, `content/<doc_id>/<extraction_id>.md`, `daemon.pid`, `daemon.start.lock`, `logs/daemon.log`, `.curio-meta.json`.
- **One daemon per home.** The daemon holds an exclusive `flock` on `daemon.pid` for its lifetime and writes its own PID there (truncated on clean exit, never unlinked). It takes the lock before loading config or opening the DB, and binds its port before migrating or starting workers, so a second daemon, or one that can't bind, exits without touching the DB. Liveness is the lock, never `kill(pid, 0)`.
- The CLI and `curio-mcp` auto-start the daemon via `internal/daemonctl` (`EnsureRunning`), serialized by `daemon.start.lock`. They confirm identity through `/v1/healthz` (`pid`, `home`), and `curio daemon stop` only signals the PID the lock holder recorded. The auto-discovery picks `curio-daemon` from the same directory as the `curio` binary, override with `CURIO_DAEMON_BIN`.
- The daemon listens on loopback only (`127.0.0.1:8765`; `daemon.listen` must be a loopback host). The API has no auth: it trusts local processes, and its middleware shuts out browsers. Requests are rejected unless `Host` is a loopback name on the bound port (stops DNS rebinding), rejected if they carry a foreign `Origin`, and bodies must be `application/json` (1 MiB cap, 32 MiB for import). See `docs/decisions.md` "Local API".
- SIGINT, SIGTERM and SIGHUP all shut down gracefully (5s HTTP + 15s worker drain). There is no config reload: restart the daemon.

## Where the design lives

**Read these before making non-trivial changes:**

- `docs/decisions.md` — running log of design choices and *why*. The single most important doc; consult before second-guessing anything that looks weird (e.g., why the chunker has a 3500-char cap, why we don't use `toolchain` in go.mod, why `MarkFailed` returns `(permanent bool, error)`).
- `docs/architecture.md` — components, transports, data flow.
- `docs/data-model.md` — schema and the "documents vs references" split.
- `docs/setup.md` — Ollama + web2md installation flow.
- `docs/roadmap.md` and `docs/status.md` — what's done vs. deferred per milestone.
- `api/openapi.yaml` — HTTP contract.

## State machine, briefly

This catches people:

**Jobs**: `pending ↔ running → done | failed`. `failed` is terminal — won't retry. `ClaimNext` counts the attempt. `MarkDone`, `MarkFailed` and `Requeue` only move a `running` row (`store.ErrNotRunning` otherwise). The `run_after` column on a failed row is stale data from the last retry cycle (we update status + last_error but not run_after at terminal transition). The CLI hides `next_attempt` for failed/done rows for that reason. Only finished (done/failed) jobs can be pruned or deleted.

- **Interrupted** (the handler returned while the worker's context was cancelled, i.e. shutdown): requeued with the attempt *refunded*, whatever error the handler returned. Queue writes use a detached, 10s-bounded context, so outcomes still get recorded during shutdown. A handler that finishes during shutdown is `done`.
- **Orphaned** (left `running` by a daemon that died): at startup `Worker.RecoverOrphans` requeues it with the attempt *kept*. Once attempts are exhausted it goes `failed` and runs the permanent-failure hook. This is what stops a job that crashes the daemon from looping forever.
- A handler or hook panic is recovered and fails the job permanently (`last_error` starts with `panic:`).

**Documents**: `pending → fetched | failed | dead`. The `failed`/`dead` transition is driven by the `jobs.OnPermanentFailure` hook (`PermFailHook` — receives the cause error) — when a fetch or index job permanently fails, the worker calls the hook which sets `doc.state = failed`, or `dead` when the cause wraps `fetcher.ErrDeadLink` (hard 404/410 or a detected soft 404). Without this hook, docs would stay `pending` forever even after their jobs gave up. `refetch` flips the doc back to `pending` and enqueues a fresh job (new attempts counter) in one transaction (`DocumentStore.RequeueFetch*`), so a doc is never left pending without a job. Refetching a `dead` doc is refused with 409 unless `--force` (`?force=1`), and `refetch --all` skips dead docs unless `--state=dead` is explicit.

## Fetcher fallback policy

`internal/fetcher/native.go` falls back to Jina Reader (`r.jina.ai`) only when the original error wraps `ErrLoginWall` or `ErrAntiBot`. Hard errors (404, DNS failures, timeouts) return directly — Jina can't help and burning rate-limit budget there gets us 429'd on the calls that would actually benefit. If you're tempted to widen the fallback, read `docs/decisions.md` under "Fallback strategy" first.

`ErrAntiBot` wraps HTTP 403 and 503. The Native fetcher also sends Chrome-like headers (`Sec-Fetch-*`, `Sec-Ch-Ua-*`) to reduce false-positive bot blocks.

Status classification is shared by all HTTP fetchers (`internal/fetcher/errors.go`): every HTTP failure carries a `*HTTPStatusError` (status, the URL that answered, `Retry-After`). 408/421/425/429 and 5xx except 501/505 are retried; every other status is a `PermanentError`. Native's policy (403/503 → `ErrAntiBot`, 404/410 → `ErrDeadLink`) runs before that rule. Error text quotes at most 512 bytes of a response body.

Every response body is capped at 32 MiB after decompression (`maxResponseBytes`; Native through the `limitBodies` transport decorator, GitHub via `readLimited`). Overflow is a permanent `ErrTooLarge`: never Jina, never host-cached. A PDF over the cap still goes to Jina without being read further.

`ErrDeadLink` (404/410, soft-404 titles, redirect-to-homepage) is always wrapped in a `PermanentError`, never goes to Jina, and is deliberately NOT host-cached (a dead path says nothing about the host). The soft-404 check runs BEFORE the login-wall heuristics in `tryReadability` — order matters, thin tombstone pages would otherwise classify as login walls and leak to Jina.

A hit on the in-memory host-failure cache (`hostFailureCache`, 15-min TTL) returns a `PermanentError` wrapping the original sentinel — the verdict can't change inside the TTL, so retrying would only re-read the cache. The *first* failure for a host stays retryable; it's what populates the cache. Recovery is `curio refetch --all --state=failed` (or per-doc `curio refetch <id>`). See `docs/decisions.md` "Host-cache hits are permanent failures".

Only host-wide verdicts are cached (`hostVerdict`): unreachable (NXDOMAIN, `ECONNREFUSED`, `EHOSTUNREACH` — not DNS timeouts or `ENETUNREACH`), anti-bot (403/503), and a redirect onto the site's own login page. They are keyed by the host that *answered* (the redirect target), never cached when Jina failed for its own reasons (429/5xx/timeouts/401/402), only when Jina was off or gave a verdict about the target. Page-level login walls (thin text, no article, login title, cross-site redirect) are never cached; instead they fail permanently once every configured extraction path has answered. See "Host cache: only host-wide verdicts, under the host that gave them".

## Fetcher routing

`$CURIO_HOME/fetcher_rules.yaml` (optional) routes URLs to fetchers — `host` / `host_suffix` / `host_in` / `{}` catch-all matchers, first match wins, hot-reloaded via throttled stat-on-dispatch (`internal/fetcher/rules.go`). Missing file = built-in defaults (github.com → github, youtube hosts → youtube when yt-dlp exists, everything else → the config default). Invalid edits keep the last good rules; unknown fetcher names skip the rule with a warning.

## Code layout pointers

- `internal/store/` — interfaces (`store.go`) + sqlite impls (`sqlite/`). The interface boundary is real; other packages should never import `internal/store/sqlite` directly except `cmd/curio-daemon/main.go` and tests.
- `internal/jobs/` — Worker loop + handlers. `OnPermanentFailure` hooks are wired in `Register`.
- `internal/api/` — HTTP handlers, RFC 7807 errors, chi router.
- `internal/config/` — `config.yaml` loader. Decoding is strict: an unknown key fails the load. `daemon.workers` is a deprecated alias that `Load` folds into `fetch_workers`/`index_workers`; `Validate` never mutates. `embedding.dim` must equal `store.EmbeddingDim` (768).
- `internal/cli/` — Cobra commands. Pattern: each command file (`add.go`, `docs.go`, etc.) exports `newXxxCmd()` and `root.go` adds them.
- `internal/fetcher/` — Native (Go) and Web2MD (subprocess) backends behind the same `Fetcher` interface.
- `internal/indexer/` — Chunker (paragraph-aware with hard char cap) + orchestrator (chunk → embed → store).
- `internal/insight/` — M4 insight layer. Pluggable `Clusterer` (kNN-graph + label propagation), `Labeler` (term / LLM), and the `Engine` that clusters → labels → persists.
- `internal/generator/` — provider-agnostic LLM text generation (`Generator` interface + Ollama `/api/generate`). Separate from `internal/embedder`; used for cluster labels (M4) and RAG (M6).
- `internal/eval/` — retrieval eval harness (recall@k / NDCG@k / MRR) behind `curio eval --queries`.
- `migrations/` — Goose SQL migrations, embedded into the binary via `embed.go`. Rebuilding a table must follow the recipe in `migrations/README.md` (NO TRANSACTION, one StatementBegin block, enforcing FK guard). Doing it inside goose's transaction cascade-deletes child rows.

## Insight layer (M4)

Clusters documents into labeled "interests". The whole algorithm sits behind `insight.Clusterer` (points → per-point label, `-1` = noise) so the clusterer is swappable without touching storage/API/CLI/MCP — the shipped one is `KNNGraphClusterer` (kNN graph + deterministic label propagation). See `docs/decisions.md` "Insight layer: kNN-graph clustering" for why *not* HDBSCAN.

- Clustering runs as a corpus-wide **`cluster` job on its own single-worker pool** (`cmd/curio-daemon/main.go`). It fully recomputes each run: a `cluster_runs` row tracks the attempt, current interests are the `clusters` of the latest `done` run, and older runs are pruned. Unlike fetch/index it has **no doc-state `OnPermanentFailure` hook** (it's corpus-global, not per-doc).
- Cluster labels: **LLM by default** (`insight.labeling = "llm"`). Needs a generation model in Ollama — the daemon **auto-pulls it** on startup (`generation.auto_pull`, default true; the embedding model auto-pulls too via `embedding.auto_pull`). Until the model is ready (or if it's unavailable / auto_pull is off), labeling **falls back to deterministic term labels**, so it stays zero-setup-safe. Set `insight.labeling = "terms"` to force it. Pulling lives in `internal/ollama.PullModel`, shared by both Ollama clients.
- Doc vectors for clustering come from `ChunkStore.DocumentVectors` (bulk mean-pooled per doc, single query) — do not loop `EmbeddingsForDocument`.
- An interest *is* a labeled cluster: one API surface (`/v1/interests`, `/{id}`, `/rebuild`), not a separate `/v1/clusters`. `POST /v1/interests/rebuild` is gated by `insight.enabled` (409 when off). Config knobs: `insight.{enabled,knn,min_similarity,min_cluster_size,labeling}` + `generation.{provider,model,base_url,timeout_seconds,auto_pull}` + `embedding.auto_pull`. `min_similarity` is the granularity dial; tune with `curio eval`.

## Conventions to preserve

- The CLI never echoes `tenant_id` to clients; tenant scoping is server-side. Single-tenant local installs hardcode `"local"`.
- API list endpoints use cursor pagination (`?cursor=...`), not offset. Stable under concurrent writes.
- Queued work returns `202`: `{job_id}` for single-target ops (refetch, reindex, interests rebuild), `{jobs_enqueued}` for the bulk refetch-all and reindex-all, which have no parent job. Import is synchronous (`200`) and enqueues fetch jobs. Clients watch `GET /v1/jobs`.
- Errors over the wire are RFC 7807 (`application/problem+json`).
- `curio docs` and `curio jobs` default to the happy-path view (`state=fetched`, `status=done`). `--failed`, `--all`, and explicit `--state`/`--status` widen.
- Both list views include the on-disk markdown path under `doc_id` so `cat`, `curio docs show`, and `curio refetch` are copy/paste-ready.

## CI release flow

`v*` git tags trigger `.github/workflows/release.yml` → goreleaser → publishes a binary tarball to GitHub releases and writes `Formula/curio.rb` to `samsar/homebrew-tap`. cgo limits us to `darwin/arm64` for now (single macos-14 runner). Adding amd64 or linux means matrix runners + `goreleaser --split`/`--merge`; deliberately deferred.
