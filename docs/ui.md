# The dashboard

curio's dashboard shows your library in a browser: search, your documents
with their text, your interests, and what the daemon is doing. It opens on
search. The daemon serves it on its own address, for this Mac only.

```sh
curio ui            # open it in your browser (starts the daemon if needed)
curio ui --print    # print its address instead: http://127.0.0.1:8765/ui/
```

While the daemon is starting (applying a database migration after an
upgrade, say), the page shows its progress and turns into the dashboard
once the daemon is ready.

`curio status` shows the address too. Besides showing what curio knows,
the dashboard does what these commands do:

| On the page | What it does | The command |
|---|---|---|
| a document's **Refetch** | fetches the page again | `curio refetch <id>` |
| a dead link's **Refetch anyway…**, after you confirm | fetches it though it was found gone | `curio refetch --force <id>` |
| a document's **Reindex** (once it has text) | re-chunks and re-embeds its text | `curio reindex <id>` |
| **Rebuild** on Interests | groups the library into interests again | `curio interests rebuild` |
| **Pause** / **Resume** on Status | stops starting jobs, or starts them again | `curio pause`, `curio resume` |
| **Throttle** on Status | runs fewer jobs at once (gentle), or all (normal) | `curio throttle gentle\|normal` |
| **Keep awake** on Status | keeps the Mac from idle sleep while jobs run on AC power | `curio keep-awake on\|off` |
| **Schedule** on Status | starts jobs only between two times, or at any hour (Turn off) | `curio schedule HH:MM-HH:MM\|off` |

Each says what came of it next to it: done, or the daemon's reason for
refusing it. One change runs at a time. The controls need JavaScript;
without it the pages show the commands instead, and Status the queue's
settings.

## What refreshes by itself

Status keeps itself current: the library's counts, the queue, its
progress and the jobs every 2 seconds and right after a change you make,
and health (which checks Ollama) every 15 seconds; why documents failed
is read when the page loads. A document's page shows its jobs while one
is queued or running (why, if the queue holds them, and each retry's
attempt), then offers a reload once there is something new to show: a
new text, a failure, or another change. Interests does the same for a
rebuild: queued or running, then "New interests are ready: reload", or
why the rebuild failed. Nothing refreshes while the tab is in the
background; it catches up when you come back. If the daemon stops
answering, a note says the page isn't updating, and it goes once the
daemon is back.

## The pages

The navigation is Search, Library, Interests and Status. The pages follow
your Mac's light or dark appearance. Every page but Search and the
starting page has a search box in its header (on a wide window), and the
footer names the address the daemon listens on.

- **Search** (`/ui/`, the home): a search box, with a type filter under
  it (All, Articles, Repos, Videos, PDFs). With nothing typed, the box
  says how many documents there are to search ("Search your 4,498
  documents"), and a quiet line at the foot of the screen names your six
  largest interests, each linking to its page, and links to all of them.
  Type, and the same hybrid search as `curio search` runs as you type,
  limited to the type chosen, which the address keeps
  (`/ui/?q=kafka&content_type=pdf`). Each result shows where the page
  lives, its title, the first two passages that matched with the matched
  words highlighted (the rest a click away), and its scores. When
  semantic search is unavailable (Ollama down), the keyword results come
  under a warning. The old address, `/ui/search?q=…`, leads here with its
  query.
- **Library**: every document, most recently updated first. Tabs choose
  the state (All, Fetched, Pending, Failed, Dead), each with its count
  when the state is the only filter; with a type, host, folder or cause
  chosen too, the counts are left out, since they count the whole
  library.
  Filters narrow it by type, host (`example.com`, exactly) and bookmark
  folder (the folder and the folders under it), and Apply keeps the tab
  chosen. Each row shows the title on one line with where the page lives
  under it, its state, type and when it was last updated. A page with no
  title of its own (every one that failed, and a few others) is named by
  its bookmark's title, in italics, with its address under it; one
  without a titled bookmark is named by its address, with its host under
  it. A failed or dead row adds why it failed, its cause and the start of
  its error, with the whole error on hover and on the document's page. On
  a phone the type and time move under the title. "Load more" pages
  through the rest, and the line under the table counts the documents
  shown ("Showing 100 of 2,150 documents"). A `cause=` in the address
  (`/ui/library?cause=anti_bot`) narrows it to the documents that failed
  for one cause, and paging, the tabs and filtering keep it; Status links
  there for each cause.

  **Order** switches to **Date saved** (`/ui/library?order=saved`): your
  saves, newest saved first, one row per bookmark, so a page saved in two
  browsers is listed twice. The time column reads Saved, the line under a
  title adds the browser it was saved in on a wide window (hover it for
  the folder), and a note under the table says why a page can appear
  twice and why Safari's bookmarks are dated when curio imported them
  (Safari keeps no save dates). An untitled page is named by that save's
  own title. The tabs, filters and Load more work as in Last updated, and
  the tabs still count documents; the line under the table counts saves,
  "Showing 50 of 7,497 saves" with no filter at all, and "Showing 50
  saves" with any.
- **Document**: the page's title (for an untitled page, its bookmark's
  title, in italics, as the Library names it) and address, its state,
  type, length, author, published date and language; for a failed page, a box naming
  why it failed and what that means, with its **Full error** a click
  away, and for a dead one, that the page is gone. Then its text, and
  beside it related documents, its bookmarks with their folders and tags,
  and its details: when it was added, updated and fetched (Jina Reader
  called out), its ID, where its markdown is, and the `curio` commands
  that work for it. Under its head, **Refetch** and **Reindex**, and how
  its text was fetched; for a dead link, **Refetch anyway…**, which asks
  first. The text shown is at most the first 1 MiB; the page
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
  documents. An interest's page lists its documents by similarity.
  **Rebuild**, at the top, groups the library again. With insight turned
  off in config.yaml (`insight.enabled: false`), there is no Rebuild, and
  the page says so.
- **Status**: what curio is doing, and whether what it needs works. What
  needs your attention comes first: Ollama not ready, the embeddings
  drifted (with the command that fixes it), or the Jina Reader fallback
  failing or degraded. Then cards for the library (how many documents,
  fetched and bookmarks, and a bar of the documents by state, each state
  linking to the Library of those), the queue (open or closed and why,
  with Pause or Resume, its throttle, keep-awake and schedule controls,
  and each pool's load and the jobs it finished in the last 10 minutes), and why
  documents failed (the five commonest causes as bars, each linking to
  the Library of the documents that failed for it); beside them, health
  (the daemon, Ollama, the models, embedding drift, the Jina Reader
  fallback and the YouTube fetcher, each with a status dot), an estimate
  of when the queued work will be done, at the pace of the last 10
  minutes, and the jobs by status.

Lists show times relative to now ("13 min ago"), with the exact time on
hover; a document's page shows its own dates in full. Anything a saved page
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
since opening a page changes nothing. A change is sent only by the
dashboard's own script, to the daemon's API as JSON, from the daemon's own
pages, which the daemon checks. The rules, and why, are in
[decisions.md](./decisions.md): "Dashboard: server-rendered pages in the
daemon (phase 1)", "Dashboard: formatting budgets for stored markdown",
"Dashboard: a design language under the CSP", "Dashboard: actions through
/v1, sent by a first-party module" and "Local API: loopback only, no
token, browsers shut out".
