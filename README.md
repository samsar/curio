# Curio

A personal context layer built from your bookmarks. Hybrid BM25 + vector search
over the full text of every page you've ever bookmarked, with an MCP server so
your LLM tools can pull that context automatically.

The problem it solves: your accumulated curiosity is invisible to the tools you
think with. When you ask an LLM a question, it has no idea what you've been
reading. Curio makes that context queryable.

## Quickstart

```sh
# 1. Install
brew install samsar/tap/curio          # or `make build` from a clone
brew install ollama                    # required for embeddings
brew services start ollama
ollama pull nomic-embed-text

# 2. Verify
curio doctor                            # six green checks = ready

# 3. Use
curio import html ~/Downloads/bookmarks.html --follow
curio search "feature flag rollout"
```

Export your bookmarks from any browser as HTML (Chrome → Bookmark Manager →
⋮ → Export bookmarks). The HTML export works across all browsers and is the
fastest way to load your corpus.

Time budget: with 4 workers (default) and the native fetcher, expect roughly
1–2 seconds per bookmark — so 1000 bookmarks ≈ 4–8 minutes.

## More commands

```sh
curio doctor                        # verify Ollama + DB + config + paths
curio status                        # daemon health + corpus counts + queue depth

# Inspecting the corpus
curio docs                          # successfully-fetched documents (the happy path)
curio docs --failed                 # docs whose fetch or index gave up
curio docs --all                    # every state
curio docs show <doc-id>            # full metadata + on-disk path
curio docs show <doc-id> --content  # also streams the extracted markdown

# Inspecting work history
curio jobs                          # done jobs (default; the audit view)
curio jobs --failed                 # failures with full error + retry count
curio jobs --all                    # every status
curio jobs --kind index             # filter by job kind

# Recovery
curio refetch <doc-id>              # try one URL again
curio refetch --all --state failed  # retry every failed doc

# Maintenance
curio jobs prune --older-than 30d   # trim the audit table
curio jobs delete --status failed   # purge a specific status

# Daemon lifecycle
curio daemon {start|stop|status|logs}

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
- [Data model](./docs/data-model.md) — schemas and storage layout
- [Decisions](./docs/decisions.md) — running log of design choices and why
- [Roadmap](./docs/roadmap.md) — milestones and what's next
- [M0 plan](./docs/m0-plan.md) — walking-skeleton implementation plan
- [API](./api/openapi.yaml) — daemon HTTP contract
- [Migrations](./migrations) — SQLite schema

## Building from source

Requires Go 1.23+ (will use 1.26 toolchain via go.mod), Node not required (the
default fetcher is Go-native).

```sh
git clone https://github.com/samsar/curio
cd curio
make build      # produces bin/curio and bin/curio-daemon
make test       # unit tests
```

cgo is required (sqlite + sqlite-vec). The Makefile forces `CGO_ENABLED=1`.

## Naming

Curio: a rare or interesting object you've collected. Also: curiosity.
