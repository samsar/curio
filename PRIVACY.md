# Privacy

Curio is local-first. Your library, the search index, the extracted pages and
the models that run on them live on your Mac. This page says what curio
stores, what leaves the machine, and what it does not do.

## What curio stores

Everything is under `~/.curio` (or `$CURIO_HOME` if you set it), except
the launchd agent that starts the daemon at login. The home directory is
created with mode `0700`, so only your user can open anything in it; curio
writes config, content, log and marker files with mode `0600`. SQLite creates
`curio.db` and its `-wal`/`-shm` files with the process umask (commonly
`0644`), protected by the `0700` directory. Curio does not encrypt the
library itself; use FileVault (or another full-disk encryption) if you want
encryption at rest.

| Path | What it holds |
|---|---|
| `curio.db` | SQLite database: bookmarks, documents, extracted chunks, embeddings, summaries, interests, jobs and the state needed to resume work |
| `content/` | the markdown curio extracted from each page; the database points at the current extraction per document |
| `config.yaml` | your settings, including fetcher options, model names and any GitHub or Jina credentials |
| `fetcher_rules.yaml` | optional per-host fetcher rules you write |
| `setup.json` | what `curio up` remembers between runs, such as a declined optional install |
| `logs/` | daemon logs, which can include URLs, page titles, error text and provider responses |
| `.curio-meta.json`, daemon state files | the home's format and embedding marker, process lock and start lock |

The launchd agent is a plist under `~/Library/LaunchAgents/` with mode
`0644`, because launchd refuses a group- or world-writable plist. It holds
the daemon's paths and labels, not library content.

A page fetched again can add a new extraction. Earlier extractions are kept,
so a failed refetch does not lose the last text curio could read.

## What leaves your Mac

Curio is not a cloud service and has no account, sync or telemetry. It does
make network requests to do its job:

- **The sites you bookmark.** Fetching a page sends the request, its URL and
  headers to that site, as a browser would.
- **Jina Reader.** With `fetcher.native.jina_fallback` on (the default),
  curio asks `r.jina.ai` to read a page the site blocks, a login wall, or a
  PDF the local extractor cannot read. Jina receives the page's URL and
  returns its text; Jina sees both. Turn it off with
  `fetcher.native.jina_fallback: false`.
- **GitHub.** Pages on `github.com` are fetched through `api.github.com`.
  Without a token GitHub sees the request; with `fetcher.github.token` the
  token is sent to GitHub as well.
- **YouTube.** Videos are fetched with `yt-dlp`, which contacts YouTube for
  the video's metadata and captions.
- **Ollama.** Embeddings and local generation go to the Ollama server at
  `embedding.base_url` / `generation.base_url`, `http://localhost:11434` by
  default. If you point either at another host, page text, summaries and
  queries go there.
- **MCP clients.** `curio-mcp` talks to the local daemon and returns library
  content to the client you connected (Claude Code, Claude Desktop, ...).
  That client decides what to send to its model provider; curio does not call
  it. See [the MCP trust model](./docs/mcp.md#trust-model).

The dashboard does not load images from stored pages unless you ask it to:
`ui.load_remote_images` is off by default, so a page's images are not
fetched when you read it.

## Deleting data

Curio does not yet have a command that deletes a document and purges its
content. The HTTP API's bookmark delete removes the bookmark only; the
document, its extracted markdown, chunks, embeddings and summary stay in the
library, because another bookmark may point at the same URL. A purge command
is planned.

To remove a library completely, stop the daemon and delete `~/.curio` (and
any backups such as `~/.curio.bak-*`). `curio up --fresh` moves the current
library aside to `~/.curio.bak-<date>`; it does not delete it.

## The local API

The daemon's HTTP API listens on loopback and has no authentication: it
trusts every process that can reach the socket. It refuses browser-origin
requests and DNS-rebinding names, so a web page cannot read your library, but
another program running as your user can. Run curio on a machine and account
you trust.

## Reporting a problem

Security issues: see [SECURITY.md](./SECURITY.md). Other privacy questions:
open an issue at <https://github.com/samsar/curio/issues>.
