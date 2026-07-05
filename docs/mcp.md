# MCP server (`curio-mcp`)

`curio-mcp` exposes your saved-bookmark corpus to MCP clients (Claude Code,
Claude Desktop, …) over stdio. It's a thin sidecar: it forwards tool calls to
the curio daemon over the local HTTP API and **auto-starts the daemon** if it
isn't already running.

Build it with the others:

```sh
make build      # produces ./bin/curio, ./bin/curio-daemon, ./bin/curio-mcp
```

## Tools

| Tool | Arguments | Returns |
|---|---|---|
| `search_bookmarks` | `query`, `k?`, `content_type?[]`, `source?[]`, `host?[]` | top matching documents (title, url, doc_id, score, snippet) |
| `get_document` | `id` (doc_id) | the document's metadata + full extracted markdown |
| `find_related` | `id` (doc_id), `k?` | documents similar to the given one (vector similarity over its indexed content), excluding itself |

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
  itself (override with `CURIO_DAEMON_BIN`). The daemon must be reachable or
  startable for tools to work.
- **Embeddings.** Search needs Ollama running (the daemon embeds the query);
  if it's down, search returns an error the client will surface.
- **Lifecycle.** The client spawns and stops `curio-mcp` per session; a normal
  disconnect (stdin closed) is a clean exit, not a crash. The daemon keeps
  running across sessions.
