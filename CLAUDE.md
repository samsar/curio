# CLAUDE.md

Guidance for Claude Code in this repository. It holds the commands, the traps
and the rules that cut across packages. The reasons, and every subsystem's
details, are in `docs/decisions.md`.

## Build and test

```sh
make build             # ./bin/curio, ./bin/curio-daemon, ./bin/curio-mcp
make test              # unit tests under -race; no network, no Ollama
make test-e2e          # builds curio-daemon (tag e2e), drives it against a fake Ollama
make test-integration  # needs network: fetches live sites
make vet lint vulncheck tidy-check
make fmt               # go fmt + go mod tidy under the go.mod toolchain
make tools             # installs the pinned golangci-lint and goose
make help              # every target, including the developer's eval tools and their arguments
```

Run a single test:

```sh
go test -race -count=1 -tags=sqlite_fts5,sqlite_json -run TestParseHTML_Basic ./internal/importer/...
# on arm64, also compile sqlite-vec's NEON kernels, as make does (x86 can't build them):
CGO_CFLAGS="-O2 -g -DSQLITE_VEC_ENABLE_NEON" go test -race -count=1 -tags=sqlite_fts5,sqlite_json -run TestParseHTML_Basic ./internal/importer/...
```

- **The build tags are mandatory.** Without `sqlite_fts5,sqlite_json`, FTS5 is missing and migrations fail. `make` passes them, and the Makefile is the source of truth for every flag.
- Cgo is required (sqlite, sqlite-vec). On arm64, a `go test` outside make needs the `CGO_CFLAGS` above, or `TestVectorExtension_NEONOnArm64` fails. Keep `-O2 -g` in it: setting `CGO_CFLAGS` replaces Go's default.
- `make test` runs `internal/insight/...` a second time without -race, because its grouping property tests skip under it.

## Tooling traps

- **Go version.** The `go` directive in `go.mod` is the exact toolchain that CI and releases build with, and the Makefile exports it as `GOTOOLCHAIN`. `go mod tidy` under another Go version writes a different `go.sum`, so use `make fmt` / `make tidy-check`. Bump the patch when `make vulncheck` flags the standard library.
- **No `toolchain` directive.** golangci-lint would take it as the target version, and modernize would then suggest APIs that vet rejects. Move the `go` line instead.
- **golangci-lint** is pinned once, in the Makefile (`GOLANGCI_LINT_VERSION`, which CI reads). `make lint` refuses any other version, and the binary must be built with a Go minor at least the directive's.

## Architecture

```
curio     (CLI, cobra)   ──HTTP+JSON──►  curio-daemon  ──►  SQLite (~/.curio/curio.db)
curio-mcp (MCP sidecar)  ──HTTP+JSON──►       │             ├ FTS5 (chunks_fts, document_fields_fts)
browser   (curio ui)     ──HTML /ui/──►       │             └ sqlite-vec (chunks_vec, document_cards_vec)
                                              ├─► Ollama (embeddings, writing models)
                                              ├─► Native fetcher (Go HTTP + Readability, Jina Reader fallback)
                                              └─► GitHub, yt-dlp, optional web2md
```

- **The home** is `$CURIO_HOME` (default `~/.curio`): `curio.db`, `content/<doc_id>/<extraction_id>.md`, `daemon.pid`, `logs/daemon.log`, `config.yaml` and `.curio-meta.json`. The marker fixes the embedding model and width when the home is created. The daemon refuses a `config.yaml` that disagrees with it, and no migration converts an old home (`curio up --fresh` replaces one).
- **One daemon per home.** It holds an exclusive `flock` on `daemon.pid`. Liveness is the lock, never `kill(pid, 0)`. It binds its port before it migrates, and answers 503 "starting" until it is ready.
- **Auto-start.** The CLI and `curio-mcp` start the daemon through `internal/daemonctl`: through the launchd agent when one is loaded, otherwise by spawning it (`CURIO_DAEMON_BIN` overrides discovery).
- **Local API.** It listens on loopback only and has no auth. Its middleware refuses a foreign `Host` or `Origin`, a cross-site change and a non-JSON body. The dashboard is the one browser client. See decisions.md "Local API".
- **Exit codes.** The daemon exits 0 when a signal stopped it, when another daemon holds the lock, or when it refuses a cause only a fix clears. It exits 1 otherwise, and launchd restarts it. So `config.Validate` must reject anything the daemon would otherwise refuse after binding.
- **Writing models** are `config.Role`s (labels, planner, summaries, reranker, answers). Build a role's model with `generator.New(cfg.Generation.Role(r))`, never `NewOllama` directly. Both Ollama clients sit on one `internal/ollama.Client`.
- **`curio-mcp`** forwards MCP tool calls to the same HTTP API. Its search never asks curio's models (`TestMCP_SearchIsLiteral`), and it fences page content as untrusted. See `docs/mcp.md`.
- **`curio up`** (`internal/setup`) is a list of steps, each with a read-only `Check` and an `Apply`, and `curio doctor` renders the same checks. Commands annotated `ownsEnvironment` (`up`, `doctor`, bare `curio`) skip `Discover`, which would create a default home. Tests inject fakes through `setup.Deps` and `internal/setup/setuptest`. They never run brew, launchctl, open or ollama, and never read real browser files (`setuptest.WithoutBrowsers`).

## Read before non-trivial changes

- `docs/decisions.md` is the log of design choices and why they were made. It is long, so grep it for the function or rule you are about to change before you second-guess anything that looks odd.
- `docs/architecture.md`, `docs/data-model.md`, `docs/setup.md`, `docs/ui.md`, `docs/mcp.md` and `docs/roadmap.md` (milestones: what shipped, what's deferred).
- `api/openapi.yaml` is the HTTP contract. `internal/api/openapi_test.go` holds it to the router and to live responses, so update it in the same change as any route, field or status.

## Repo privacy

This repository is public. **Never commit a real library**: no bookmarks,
page titles, URLs, document IDs, folder names, interest labels, query text,
screenshots, logs or copies of `~/.curio`. Docs and tests use reserved hosts
(`example.com`, `example.org`, `example.net`, `*.example`) and synthetic
pages that keep the rule's trigger text. The search-eval and ask-eval query
files, and every report made with them, stay outside the repo. `test/privacy`
refuses a personal mailbox and a real user home path. Treat anything else that
names a real page as a bug for the same reason.

## Store and migrations

- **Interface boundary.** `internal/store` holds the interfaces and `internal/store/sqlite` the implementations. depguard lets only `cmd/curio-daemon`, the test helpers `sqlitetest` (`NewDB`) and `apitest` (`Start`), and `internal/devtool` import the sqlite package.
- **Pinned query plans.** Store SQL lives in constants and builders that `internal/store/sqlite/plans_test.go` also runs, pinning each hot query's index (curio never runs ANALYZE). A new list query gets a pinned plan. A query or index change keeps its plan, or updates the pin and decisions.md "Indexes follow the queries".
- **Never `INSERT OR REPLACE`** into `chunks`, `document_fields` or `document_cards`. Triggers keep their FTS, vector and vote tables in step, so upsert with `ON CONFLICT … DO UPDATE` and an unchanged-row guard.
- **Vectors are updated in place.** INSERT a vector only when the row has none, because sqlite-vec never reuses a deleted slot. A re-index rewrites a document's chunks by position (`ReplaceForDocument`, `ReplaceIndexed`). Never delete and insert them again.
- **`updated_at`.** Every UPDATE sets it itself, since no trigger does. The one exception is a re-embed's `MarkIndexed`.
- **Rebuilding a table** follows `migrations/README.md` (NO TRANSACTION, one StatementBegin block, the FK guard). Done inside goose's transaction, it cascade-deletes child rows.

## Jobs

- **States.** `pending ↔ running → done | failed`, and `failed` is terminal. `MarkDone`, `MarkFailed`, `Requeue` and `Defer` only move a `running` row (`store.ErrNotRunning`). A failed row's `run_after` is stale data, which the CLI hides.
- **Interrupted** (the handler returned during shutdown): requeued with the attempt refunded.
- **Orphaned** (left `running` by a daemon that died): `RecoverOrphans` requeues it at startup with the attempt kept, so a job that crashes the daemon can't loop forever. A `cluster` job gets one attempt.
- **Deferred** (a `jobs.DeferError`: waiting on a hold, not a verdict): back to `pending` until its `run_after`, with the attempt refunded. This holds only while the job is under a day old. See decisions.md "Waiting is not failing".
- **Permanent failures.** A fetch or index job that fails for good runs `OnPermanentFailure`, which records the cause through `DocumentStore.MarkFailed` (`dead` exactly for `dead_link`, `failed` otherwise). Without the hook, documents would stay `pending` forever. A handler panic fails its job permanently, with `last_error` starting `panic:`.
- **Queue gate.** A worker claims only what its `jobs.Gate` admits (pause, schedule, throttle, embedder hold, foreground searches). Running jobs are never interrupted.

## Fetcher

- **Failure causes.** Why a fetch failed is `fetcher.FailureCause(err)`. A new sentinel or failure path must be placed in its list.
- **Jina fallback.** Jina Reader is the fallback only for `ErrLoginWall`, `ErrAntiBot` and a PDF the local extractor couldn't read, never for 404s, DNS failures or timeouts. Read decisions.md "Fallback strategy" before widening it. Jina requests identify as curio, never as Chrome. Don't replace the per-site pacer with a `rate.Limiter` per site (decisions.md "A site's block is not its pages' verdict").
- **One judge.** `judgePage` judges every page, from the origin or from Jina. Its dead-link checks run before every login-wall check, and the order matters.
- **Host-failure cache.** It holds only host-wide verdicts, keyed by the host that answered. Dead links, certificate failures and page-level verdicts are never cached (decisions.md "Host cache").
- **Status rules.** HTTP status classification is shared by every fetcher (`internal/fetcher/errors.go`), and every body is capped at 32 MiB.
- **Routing.** `$CURIO_HOME/fetcher_rules.yaml` routes URLs to fetchers (`internal/fetcher/rules.go`).
- **Fake tools.** Subprocess fetchers' tests re-exec the test binary as the fake tool (`TestMain` + `CURIO_FAKE_TOOL`). Don't write shell scripts.

## Dashboard

Server-rendered, GET-only pages under `/ui/` on the daemon's port. `internal/ui` renders them, and `internal/api`'s `ui*.go` handlers gather the data through the same `Deps` functions the JSON handlers use, with no SQL. Tests enforce these rules; knowing them up front saves a round trip:

- **Routes and headers.** `/ui` routes are GET only (`TestDashboard_GETOnly`). Every response carries the strict CSP, nosniff and no-referrer. A new route needs a sample request in `TestDashboard_SecurityHeaders`.
- **Templates.** No inline script, style, event handler, `hx-on`, `hx-vars` or `javascript:` (`TestTemplatesHaveNoInlineCode`). Links, `hx-get` and actions are built in Go (`links.go`, `actions.go`), never written as literals in a template.
- **Trusted HTML.** `template.HTML` comes only from the markdown sanitizer's output (`TestTrustedHTMLOnlyFromTheSanitizer`). Stored markdown is formatted only within `internal/ui/budget.go`'s budgets.
- **Changes.** A change goes through `/v1` as a JSON `fetch` from `static/actions.js`, never as a form post. A control carries its `Action` as data attributes, and `TestDashboard_ActionsMatchTheAPI` holds each one to the router and the spec.
- **Scripts.** Each script is held to its line cap and to what it may send (`TestActionsScript`, `TestSearchScript`, `TestThemeScript`, `TestMapScript`). Vendored htmx and d3 are pinned by SHA-256.
- **Search.** A keystroke never asks the planner (`TestUI_KeystrokesNeverUnderstand`), and another site's link never loads a model (`TestUI_UnderstandsOnlyItsOwnRequests`).
- **Pollers.** Polled regions follow `TestLiveRegions`. JavaScript-only controls carry `.js-only` and their stand-ins `.no-js`. Only pollers use the `hidden` attribute.
- **Template tests.** Every template renders with a hostile sample in `TestEveryTemplateRenders`, so a new template needs one. Page tests check inertness with `internal/ui/uitest`.
- **Styles.** Styling comes from `app.css`'s tokens (`TestStylesheet`) and icons from `templates/icons.html` (`TestIcons`). Never use `style=`.
- **Checking a layout.** Check a layout change in a browser at 1440 and 390 px, in both themes, with long and hostile data.

The design is in `docs/ui.md` and decisions.md's "Dashboard: …" entries.

## Config, search and the model features

- **Config loading.** `config.yaml` decoding is strict, so an unknown key fails the load. `Validate` never mutates. Pin model tags, since an untagged name is `:latest`.
- **Mapping keys.** Search's settings reach the engine only through `search.ConfigFrom`: search imports config, never the reverse. The planner's and the reranker's keys go through `understand.OptionsFrom` and `rerank.OptionsFrom`. Reflection tests hold each function to its share, so a new key must be mapped by one of them.
- **No models in search.** depguard keeps `generator`, `understand`, `ollama`, `rerank` and `modelgate` out of `internal/search`.
- **Prompts are pinned.** The planner, reranker, summaries and ask prompts are pinned by `testdata/*.golden`, with `PromptVersion` as their SHA-256. `docmeta.RulesVersion` and `indexer.ContextVersion` work the same way: change an output, then bump the version and its golden.

## API and CLI conventions

- **Tenants.** Never echo `tenant_id` to clients. Single-tenant installs use `store.LocalTenantID` (`"local"`).
- **Pagination.** Lists page by keyset (`?cursor=` / `next_cursor`, ordered by time then `id`, descending). Two lists page by `offset`: the interests, within a run, and `POST /v1/search`, within its fixed-depth ranking. A search's later pages send back the plan and rerank order that its first page returned, and never ask a model again.
- **Queued work** returns `202`: `{job_id}` for a single target, `{jobs_enqueued}` for the bulk refetch-all and reindex-all. Import is synchronous (`200`).
- **Errors** over the wire are RFC 7807 (`application/problem+json`).
- **CLI views.** `curio docs` and `curio jobs` default to the happy path (`state=fetched`, `status=done`), and both list views show each document's markdown path.
- **CLI commands.** Each command file exports `newXxxCmd()`, and `root.go` adds it. Tests build the root with `newRootCmdWith(testDeps)`.

## Code map

- `cmd/curio`, `cmd/curio-daemon` and `cmd/curio-mcp` are the binaries.
- `cmd/searcheval`, `cmd/askeval`, `cmd/clusterreport` and `cmd/plannercheck` are the developer's tools. They run through `make` on a `.backup` copy of a home (`internal/devtool`), and are never built into `./bin` or shipped.
- `internal/api`: HTTP handlers (chi). `internal/ui`: the dashboard's view layer. `internal/client`: the API client.
- `internal/jobs`: the worker loop, five pools (fetch, index, cluster, cards, summarize), handlers, the queue gate and the re-embed feed.
- `internal/fetcher`: the Native, GitHub, YouTube and Web2MD backends. `internal/importer`: browser and HTML bookmark import.
- `internal/indexer`: the chunker, and chunk → embed → store. `internal/search`: the ranking engine (BM25, vectors, fields, cards, titles; filters; plans). `internal/docmeta`: a pure derivation of each document's search fields and card.
- `internal/understand` (planner), `internal/rerank`, `internal/retrieve` (question → ranked hits), `internal/ask` (`curio ask`), `internal/summarize` and `internal/modelgate`: the features that use writing models.
- `internal/insight`: the interests: grouping, stable identities, the scheduler, placement and the map. Its `louvain` and `layout` subpackages use the standard library only.
- `internal/generator`, `internal/embedder` and `internal/ollama`: the model clients.
- `internal/config`, `curiohome`, `daemonctl`, `service` (launchd), `setup`, `keepawake`, `drift` and `cli`: config, the home, lifecycle and setup.
- `migrations/`: goose SQL, embedded in the binary.

## CI and releases

`ci.yml` runs on pushes to main and on PRs: `test` on ubuntu (tidy-check, build, vet, test, test-e2e, vulncheck), `test-macos` (macos-15: `make test`), and `lint` (golangci-lint, actionlint). A `v*` tag runs `release.yml`, which is gated on `ci.yml`, and goreleaser publishes a GitHub release and the Homebrew tap formula. Actions are pinned by commit SHA. Builds are darwin/arm64 only, because of cgo. See decisions.md "Releases: gated on CI, pinned, least privilege".
