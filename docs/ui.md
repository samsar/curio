# The dashboard

curio's dashboard shows your library in a browser: what the daemon is
doing, search, your documents with their text, and your interests. The
daemon serves it on its own address, for this Mac only.

```sh
curio ui            # open it in your browser (starts the daemon if needed)
curio ui --print    # print its address instead: http://127.0.0.1:8765/ui/
```

`curio status` shows the address too. The dashboard is read-only for now:
it shows what curio knows and changes nothing. Refetching, reindexing,
rebuilding interests and the queue controls are still `curio` commands
(the document page lists the ones for that document).

## The pages

- **Overview**: how many documents are in each state (each links to the
  Library of those), bookmarks and jobs; the queue, open or closed and
  why, its throttle, schedule and keep-awake, and each pool's load; an
  estimate of when the queued work will be done, at the pace of the last
  10 minutes; health: the daemon, Ollama, the models, embedding drift and
  its fix, the Jina Reader fallback and the YouTube fetcher; and your 10
  newest bookmarks.
- **Search**: the same hybrid search as `curio search`, as you type. Each
  result shows its score and the passages that matched, with the matched
  words highlighted. When semantic search is unavailable (Ollama down),
  the keyword results come with a warning.
- **Library**: every document, most recently updated first, filtered by
  state, type, host (`example.com`, exactly) and bookmark folder (the
  folder and the folders under it). "Load more" pages through the rest.
- **Document**: what curio knows about one page (title, addresses, type,
  state, author, dates, where its markdown is), how it was fetched (Jina
  Reader called out), its text, related documents, its bookmarks with
  their folders and tags, and, for a failed or dead page, its last error.
  The text shown is at most the first 1 MiB; the page says when it is cut
  and where the whole file is.
- **Interests**: the topics the last clustering run found, each with a few
  of its documents; an interest's page lists its documents by similarity.
  With no run yet, `curio interests rebuild` makes one.

## Images from other sites

A saved page's images are not loaded: each shows as its description,
linking to the image. Loading an image tells the site hosting it that you
read that page, and when. A page with such images offers **Load images**,
which shows them for that visit only. To always show them, set this in
`~/.curio/config.yaml` and restart the daemon (`curio daemon stop`; the
next command starts it):

```yaml
ui:
  load_remote_images: true
```

Only https images are ever shown.

## Turning it off

```yaml
daemon:
  ui: false
```

With that, the daemon serves only its API, and `curio ui` says the
dashboard is off.

## Security

Every saved page is treated as hostile: it came from the web. The
dashboard renders it without any of its scripts, styles or forms, keeps
only its http, https and mailto links (pointing at the original site), and
sends a strict Content-Security-Policy with every response, so nothing it
shows can run code or load from anywhere but the daemon. Other websites
can't make your browser use the dashboard or the API: the daemon refuses
their requests. The rules, and why, are in
[decisions.md](./decisions.md): "Dashboard: server-rendered pages in the
daemon (phase 1)" and "Local API: loopback only, no token, browsers shut
out".
