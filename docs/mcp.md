# MCP server (`curio-mcp`)

`curio-mcp` exposes your saved-bookmark corpus to MCP clients (Claude Code,
Claude Desktop, …) over stdio. It's a thin sidecar: it forwards tool calls to
the curio daemon over the local HTTP API and **auto-starts the daemon** if it
isn't already running, at startup and again if it stops mid-session.

Build it with the others:

```sh
make build      # produces ./bin/curio, ./bin/curio-daemon, ./bin/curio-mcp
```

## Tools

| Tool | Arguments | Returns |
|---|---|---|
| `search_bookmarks` | `query`, `k?` (1-100; default the daemon's `search.default_k`), `content_type?[]`, `source?[]`, `host?[]` | top matching documents (title, url, doc_id, score, snippet); `degraded`/`warnings` when keyword-only |
| `get_document` | `id` (doc_id) | the document's metadata + full extracted markdown; a note instead of the markdown when nothing is extracted yet. An unknown id, or content that fails to load, is a tool error |
| `find_related` | `id` (doc_id), `k?` | documents similar to the given one (vector similarity over its indexed content), excluding itself |
| `list_interests` | `limit?` (default 20), `members?` (default 5) | the labeled topic clusters from the latest clustering run, each with a summary, size and representative documents (doc_ids); empty until `curio interests rebuild` has run |

`content_type` ∈ `article|repo|video|pdf|thread|unknown`, `source` ∈
`chrome|safari|firefox|html|manual`, `host` is a URL host like `github.com`.

## Register with Claude Code

```sh
claude mcp add curio /absolute/path/to/bin/curio-mcp
```

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
  be started.
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
