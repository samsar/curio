# Curio

A personal context layer built from your bookmarks. Hybrid BM25 + vector search
over the full text of every page you've ever bookmarked, with an MCP server so
your LLM tools can pull that context automatically.

The problem it solves: your accumulated curiosity is invisible to the tools you
think with. When you ask an LLM a question, it has no idea what you've been
reading. Curio makes that context queryable.

## Quickstart

```sh
brew install samsar/tap/curio          # or `make build` from a clone
curio up
```

`curio up` checks the Mac, installs or starts Ollama (with Homebrew, or the
app from ollama.com), picks and pulls the models for your Mac's memory,
creates `~/.curio` with its `config.yaml`, and installs a launchd agent that
keeps the daemon running. It shows the plan first and asks before each
step; every command it runs is shown in full, and it never uses sudo. Run
it again any time: with everything in place it says `Nothing to do: curio
is up.` Then:

```sh
curio import html ~/Downloads/bookmarks.html --follow
curio search "feature flag rollout"
```

Export your bookmarks from any browser as HTML (Chrome → Bookmark Manager →
⋮ → Export bookmarks). The HTML export works across all browsers and is the
fastest way to load your corpus.

The writing model is yours to change (`generation.model`); the embedding
model is fixed when a home is created (`curio up --fresh` starts a new one).
See [docs/setup.md](./docs/setup.md), which also covers setting up by hand.

Time budget: with the default pools (16 fetch workers, 4 index workers; set
`daemon.fetch_workers` / `daemon.index_workers` in `~/.curio/config.yaml`) and
the native fetcher, expect roughly 1–2 seconds per bookmark — so 1000
bookmarks ≈ 4–8 minutes. The older single `daemon.workers` setting is still
read, split 75/25 between the two pools, but can't be combined with the new
ones.

## More commands

```sh
curio up                            # set up, or check the setup; --dry-run shows the plan only
curio doctor                        # what curio up checks, plus the fetcher and the Jina fallback
curio status                        # daemon health + corpus counts + queue depth and state

# Pacing the work (stored, so it holds across restarts; running jobs finish)
curio pause                         # start no new jobs until resumed
curio resume                        # start them again (a schedule still applies)
curio throttle gentle|normal        # gentle: fewer at once, to keep the machine cool
curio schedule HH:MM-HH:MM|off      # start jobs only in a daily window, e.g. 22:00-07:00
curio keep-awake on|off             # keep the Mac from idle sleep while jobs are queued, on AC power

# Inspecting the corpus
curio docs                          # successfully-fetched documents (the happy path)
curio docs --failed                 # docs whose fetch or index gave up
curio docs --all                    # every state
curio docs --all --limit 100        # a page at a time; the last line is the next page's command
curio docs --all --cursor <token>   # that next page (curio jobs pages the same way)
curio docs show <doc-id>            # full metadata + on-disk path
curio docs show <doc-id> --content  # also streams the extracted markdown

# Inspecting work history
curio jobs                          # done jobs (default; the audit view)
curio jobs --failed                 # failures with full error + retry count
curio jobs --all                    # every status
curio jobs --kind index             # filter by job kind
curio jobs show <job-id>            # one job, e.g. the one refetch or reindex just enqueued

# Recovery
curio refetch <doc-id>              # try one URL again
curio refetch --all --state failed  # retry every failed doc

# Maintenance
curio jobs prune --older-than 30d   # trim the audit table
curio jobs delete --status failed   # purge a specific status

# Daemon lifecycle
curio daemon {start|stop|status|logs|install|uninstall}  # install: a launchd agent keeps it running

# Import variations
curio import chrome [--profile X | --all-profiles | --list-profiles]
curio import safari                 # reads ~/Library/Safari/Bookmarks.plist (needs Full Disk Access)
curio import firefox                # reads the default profile's places.sqlite (Firefox can stay open)
curio import html --dry-run         # parse + filter without sending
curio import html --limit 200       # try a slice first
curio import html --follow          # poll progress until queue drains
```

Both `curio docs` and `curio jobs` print URL + doc_id + on-disk path under
each row, so the three usual follow-ups are copy/paste-ready:
`cat <path>`, `curio docs show <doc_id>`, `curio refetch <doc_id>`.

`curio --help` lists everything.

## Use with Claude (MCP)

`curio-mcp` exposes your corpus to Claude Code / Claude Desktop over MCP —
search and pull saved pages straight into a conversation. It auto-starts the
daemon.

```sh
claude mcp add curio -- curio-mcp       # curio up prints this, with the path when curio-mcp isn't on PATH
```

Tools: `search_bookmarks` (with `content_type`/`source`/`host` filters),
`get_document`, `find_related`, `list_interests`. If the daemon stops during
a session, the next tool call starts it again. See [docs/mcp.md](./docs/mcp.md).

## High-level architecture

```text
┌──────────────┐  ┌─────────────┐  ┌──────────────┐
│   curio CLI  │  │ curio-mcp   │  │  Future Web  │
│  (cobra)     │  │ (sidecar)   │  │     UI / API │
└──────┬───────┘  └──────┬──────┘  └──────┬───────┘
       └─────────────────┼────────────────┘
                   HTTP + JSON
                         │
                  ┌──────▼────────────┐
                  │   curio-daemon    │
                  │  importer/crawler │
                  │  indexer/search   │
                  │  insight          │
                  └──┬───────────┬────┘
                     │           │
              ┌──────▼────┐  ┌───▼────────┐
              │ SQLite    │  │ Ollama     │
              │ FTS5 +    │  │ (embed +   │
              │ sqlite-vec│  │ local LLM) │
              └───────────┘  └────────────┘
```

## Documentation

- [Setup](./docs/setup.md) — full install + troubleshooting
- [Architecture](./docs/architecture.md) — components, transports, data flow
- [MCP server](./docs/mcp.md) — register `curio-mcp` with Claude, available tools
- [Data model](./docs/data-model.md) — schemas and storage layout
- [Decisions](./docs/decisions.md) — running log of design choices and why
- [Roadmap](./docs/roadmap.md) — milestones, what shipped, and what's next
- [API](./api/openapi.yaml) — daemon HTTP contract
- [Migrations](./migrations) — SQLite schema

## Building from source

Requires Go 1.26.8 or newer, the `go` line in `go.mod`: the Makefile builds
with exactly that toolchain, and the go command downloads it once if your
default differs. cgo is required (sqlite + sqlite-vec), so you also need a C
toolchain: on macOS, the Xcode Command Line Tools (`xcode-select --install`).
Node isn't needed; the default fetcher is Go-native.

```sh
git clone https://github.com/samsar/curio
cd curio
make build      # produces bin/curio, bin/curio-daemon and bin/curio-mcp
make test       # unit tests
```

The Makefile forces `CGO_ENABLED=1`, and on arm64 compiles sqlite-vec's
NEON distance kernels (`CGO_CFLAGS` gains `-DSQLITE_VEC_ENABLE_NEON`), so
build and test through `make`. `make tools` installs the pinned
golangci-lint and goose, and `make help` lists every target.

## Naming

Curio: a rare or interesting object you've collected. Also: curiosity.

## License

Apache License 2.0; see [LICENSE](./LICENSE).
