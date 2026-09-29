# The dashboard

curio's dashboard shows your library in a browser: what the daemon is
doing, search, your documents with their text, and your interests. The
daemon serves it on its own address, for this Mac only.

```sh
curio ui            # open it in your browser (starts the daemon if needed)
curio ui --print    # print its address instead: http://127.0.0.1:8765/ui/
```

While the daemon is starting (applying a database migration after an
upgrade, say), the page shows its progress and turns into the dashboard
once the daemon is ready.

`curio status` shows the address too. The dashboard is read-only for now:
it shows what curio knows and changes nothing. Refetching, reindexing,
rebuilding interests and the queue controls are still `curio` commands
(the document page lists the ones for that document).

## The pages

The pages follow your Mac's light or dark appearance. Every page but
Search and the starting page has a search box in its header (on a wide
window), and the footer names the address the daemon listens on.

- **Overview**: cards for the library (how many documents, fetched and
  bookmarks, and a bar of the documents by state, each state linking to
  the Library of those), the queue (open or closed and why, its throttle,
  schedule and keep-awake, and each pool's load), your newest bookmarks,
  health (the daemon, Ollama, the models, embedding drift and its fix,
  the Jina Reader fallback and the YouTube fetcher, each with a status
  dot), an estimate of when the queued work will be done, at the pace of
  the last 10 minutes, and the jobs by status.
- **Search**: the same hybrid search as `curio search`, as you type. Each
  result shows where the page lives, its title, the first two passages
  that matched with the matched words highlighted (the rest a click
  away), and its scores. When semantic search is unavailable (Ollama
  down), the keyword results come under a warning.
- **Library**: every document, most recently updated first, filtered by
  state, type, host (`example.com`, exactly) and bookmark folder (the
  folder and the folders under it). Each row shows the title on one line,
  or where the page lives for an untitled one, its state, type and when
  it was last updated; a failed or dead row adds why it failed, its cause
  and the start of its error, with the whole error on hover and on the
  document's page. On a phone the type and time move under the title.
  "Load more" pages through the rest. A `cause=` in the address
  (`/ui/library?cause=anti_bot`) narrows it to the documents that failed
  for one cause, and paging and filtering keep it; the page has no
  control for it yet.
- **Document**: the page's title and address, its state, type, length,
  author, published date and language; for a failed page, a box naming
  why it failed and what that means, with its **Full error** a click
  away, and for a dead one, that the page is gone. Then its text, and
  beside it related documents, its bookmarks with their folders and tags,
  and its details: when it was added, updated and fetched (Jina Reader
  called out), its ID, where its markdown is, and the `curio` commands
  that work for it. The text shown is at most the first 1 MiB; the page
  says when it is cut and where the whole file is. A text whose markdown
  would take too long or too much memory to format (a line nesting dozens
  of quotes, lists nested deep over thousands of blank lines, a paragraph
  thick with unclosed brackets, a table padded to thousands of columns: a
  few shapes a page can hold, by accident or on purpose) is shown as it is
  stored, unformatted, and the page says why. Whatever the text holds,
  formatting stops after two seconds or a gigabyte of memory, and the text
  is shown as stored. Web and email addresses written out in the text
  without link markup show as text, not links: a GitHub README's bare
  URLs, say.
- **Interests**: the topics the last clustering run found, largest first:
  how many there are (the page shows the 50 largest), a bar of how many
  documents are in one and how many in none, and a card for each with
  its size, how alike its documents are, its summary and a few of its
  documents. An interest's page lists its documents by similarity. With
  no run yet, `curio interests rebuild` makes one.

Lists show times relative to now ("13 min ago"), with the exact time on
hover; a document's page shows them in full. Anything a saved page
brought with it, a long title, address or error, takes one line in a
list, cut short, with the whole of it on hover.

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
only its links to web addresses (pointing at the original site) and email
addresses, and sends a strict Content-Security-Policy with every
response, so nothing it shows can run code or load from anywhere but the
daemon. Other websites can't use the dashboard or the API through your
browser: they can't read what the daemon answers, it refuses their
requests to change anything, and their pages can't load a dashboard page
as an image, a frame or a script. A page can still send your tab to a
dashboard page, as a link you follow does; that only shows you the page,
since pages change nothing. The rules, and why, are in
[decisions.md](./decisions.md): "Dashboard: server-rendered pages in the
daemon (phase 1)", "Dashboard: formatting budgets for stored markdown",
"Dashboard: a design language under the CSP" and "Local API: loopback
only, no token, browsers shut out".
