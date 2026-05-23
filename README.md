# Curio

A personal context layer built from your bookmarks.

Curio imports your browser bookmarks, fetches and indexes the content of every
page, and provides hybrid BM25 + vector search over the corpus. On top of that,
it builds a model of your interests — what you're learning, what's trending in
your reading, what you might want to follow up on — and exposes the whole thing
to LLMs (Claude, ChatGPT, etc.) via an MCP server.

The problem it solves: your accumulated curiosity is invisible to the tools you
think with. When you ask an LLM a question, it has no idea what you've been
reading. Curio makes that context queryable.

## Status

Early design phase. No code yet. See [`docs/`](./docs) for architecture,
decisions, and roadmap.

## High-level architecture

```
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
                  └──┬──────────┬─────┘
                     │          │
              ┌──────▼───┐  ┌───▼────────┐
              │ SQLite   │  │ Ollama     │
              │ FTS5 +   │  │ (embed +   │
              │ sqlite-vec│  │ local LLM)│
              └──────────┘  └────────────┘
```

See [`docs/architecture.md`](./docs/architecture.md) for the full breakdown.

## Documentation

- [Architecture](./docs/architecture.md) — components, transports, data flow
- [Data model](./docs/data-model.md) — schemas and storage layout
- [Decisions](./docs/decisions.md) — running log of design choices and why
- [Roadmap](./docs/roadmap.md) — milestones and what's next
- [M0 plan](./docs/m0-plan.md) — walking-skeleton implementation plan
- [API](./api/openapi.yaml) — daemon HTTP contract
- [Migrations](./migrations) — SQLite schema

## Naming

Curio: a rare or interesting object you've collected. Also: curiosity.
