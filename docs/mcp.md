# MCP server (`curio-mcp`)

`curio-mcp` exposes your saved-bookmark corpus to MCP clients (Claude Code,
Claude Desktop, …) over stdio. It's a thin sidecar: it forwards tool calls to
the curio daemon over the local HTTP API and **auto-starts the daemon** if it
isn't already running, at startup and again if it stops mid-session:
through the home's launchd agent when one is installed and loaded (`curio
daemon install`), as a child process otherwise.

Build it with the others:

```sh
make build      # produces ./bin/curio, ./bin/curio-daemon, ./bin/curio-mcp
```

## Tools

| Tool | Arguments | Returns |
|---|---|---|
| `search_bookmarks` | `query`, `k?` (1-100; default the daemon's `search.default_k`), `content_type?[]`, `source?[]`, `host?[]`, `site?[]`, `folder?[]`, `saved_after?`, `saved_before?`, `published_after?`, `published_before?`, `include_undated?` | top matching documents, by their text and by what they were saved as (title, url, doc_id, score, `state`, and a snippet; a result without a snippet matched on its bookmark, named by `saved_title` and `folder` (without the browser's root, such as Bookmarks Bar), and one whose state is dead is a page no longer available, one whose state is failed a page curio couldn't read); `degraded`/`warnings` when keyword-only; with a date filter, `undated`, how many documents it couldn't judge, and a note saying whether they were kept |
| `get_document` | `id` (doc_id) | the document's metadata + full extracted markdown; a note instead of the markdown when nothing is extracted yet. An unknown id, or content that fails to load, is a tool error |
| `find_related` | `id` (doc_id), `k?` | documents similar to the given one (vector similarity over its indexed content), excluding itself |
| `list_interests` | `limit?` (default 20), `interests?` (default 8), `members?` (default 2), `id?` | an outline of the user's interests from the latest rebuild: in a large library each area with its largest interests (label, size, id) and a few documents of each (doc_ids); in a small one the interests alone; then how many documents are unsorted and what the latest rebuild changed. With `id`, an area's interests or an interest's documents and loose fits. IDs last across rebuilds; a retired `id` is answered with when it was retired and the interests that took its documents, an unknown one is a tool error. Before the first rebuild finishes it says why there are none yet: waiting for 20 indexed documents, about to be grouped, being grouped, held by an embedding drift, or the last rebuild failed |

`content_type` ∈ `article|repo|video|pdf|thread|unknown`, `source` ∈
`chrome|safari|firefox|html|manual`, `host` is a URL host like
`github.com`, matched exactly. `site` is a domain like `example.com`,
matching it and every subdomain (`www.example.com` and
`blog.example.com` are `example.com`). `folder` is a bookmark folder as
stored, the browser's root included (`/Bookmarks Bar/Tech`), matching its
subfolders too. Each list holds at most 32 values; filters AND, a list's
values OR.

The date filters bound when a page was saved (`saved_*`) or published
(`published_*`): a date `YYYY-MM-DD` is midnight UTC, any other value an
RFC 3339 date-time with a zone (a local day is
`2024-03-01T00:00:00+01:00`), and a window is `[after, before)`: "in 2020"
is `saved_after: 2020-01-01`, `saved_before: 2021-01-01`. Only a known
date counts: a bookmark a browser's bulk import stamped, or whose source
kept no date, is undated, and so is a published date more than a day
after the page was first saved. Undated documents are left out unless
`include_undated`, which needs a date filter. A filter the daemon refuses
is a tool error naming it (`filters.site: …`).

## Register with Claude Code

```sh
claude mcp add curio -- curio-mcp                    # curio-mcp on PATH (Homebrew)
claude mcp add curio -- /absolute/path/to/bin/curio-mcp
```

A `curio up` that set something up ends by printing the command for your
install: by name when the `curio-mcp` on PATH is the one next to `curio`,
and by its absolute path otherwise.

Or add it to a project's `.mcp.json` (or your user MCP config):

```json
{
  "mcpServers": {
    "curio": {
      "command": "/absolute/path/to/bin/curio-mcp"
    }
  }
}
```

Then, in a session: *"search my bookmarks for X"*, *"what have I saved about
Y?"*, *"open doc &lt;id&gt; and summarize it"*.

## Register with Claude Desktop

Edit `claude_desktop_config.json` (macOS:
`~/Library/Application Support/Claude/claude_desktop_config.json`) and add the
same `mcpServers` block as above, then restart Claude Desktop.

## Notes

- **stdout is the MCP channel.** All diagnostics go to stderr; never print to
  stdout from this binary.
- **Daemon discovery.** `curio-mcp` resolves `$CURIO_HOME` (default `~/.curio`),
  reads `config.yaml` for `daemon.listen`, and looks for `curio-daemon` next to
  itself (override with `CURIO_DAEMON_BIN`), exactly as the CLI does. The daemon
  must be reachable or startable for tools to work: startup fails if it can't
  be started, or if the port is served by another home's daemon.
- **Startup and migrations.** `curio-mcp` doesn't wait for the daemon to
  finish starting before it answers the MCP handshake: a first start after
  an upgrade can migrate the database for longer than a client allows a
  server to connect (30s by default in Claude Code). It logs to stderr
  that the daemon is still starting, and tool calls wait for it instead:
  a call that finds the daemon starting waits up to 30s for it to be
  ready, then sends the request again. One still starting after that is a
  tool error that says how far along it is and to try again in a minute;
  the daemon carries on regardless.
- **Daemon restarts.** The sidecar outlives the daemon when the daemon stops
  mid-session (`curio daemon stop` after a config edit, an upgrade, a crash).
  A tool call that finds the daemon unreachable starts it again and retries
  once; if that start fails, the tool error says why. A daemon that answers
  with an error is not restarted.
- **Embeddings.** Semantic search needs Ollama running (the daemon embeds the
  query). If it's down or doesn't answer within `search.embed_timeout_seconds`,
  `search_bookmarks` still returns keyword-only results: the structured output
  carries `degraded: true` and `warnings`, and the text starts with a note
  saying so. `find_related` never needs Ollama (it uses stored vectors).
- **Lifecycle.** The client spawns and stops `curio-mcp` per session; a normal
  disconnect (stdin closed) is a clean exit, not a crash. The daemon keeps
  running across sessions.
