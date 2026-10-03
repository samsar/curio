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
| a group's **Refetch N** on Failures | fetches again every document that failed for that cause | `curio refetch --all --cause <cause>` |
| dead links' **Refetch N anyway…** on Failures, after you confirm | fetches every dead link again | `curio refetch --all --state dead --cause dead_link` |
| **Rebuild** on Interests | rebuilds the interests now, rather than when the daemon would on its own; a second click while one waits queues nothing more | `curio interests rebuild` |
| **Pause** / **Resume** on Status | stops starting jobs, or starts them again | `curio pause`, `curio resume` |
| **Throttle** on Status | runs fewer jobs at once (gentle), or all (normal) | `curio throttle gentle\|normal` |
| **Keep awake** on Status | keeps the Mac from idle sleep while jobs run on AC power | `curio keep-awake on\|off` |
| **Schedule** on Status | starts jobs only between two times, or at any hour (Turn off) | `curio schedule HH:MM-HH:MM\|off` |

Each says what came of it next to it (on Failures, over the groups,
since a group that was refetched goes): done, or the daemon's reason for
refusing it. One change runs at a time. The controls need JavaScript;
without it the pages show the commands instead, and Status the queue's
settings.

## What refreshes by itself

Status keeps itself current: the library's counts, the queue, its
progress and the jobs every 2 seconds and right after a change you make,
and health (which checks Ollama) every 15 seconds; why documents failed
is read when the page loads. A document's page shows its jobs while one
is queued, waiting or running: why, if the queue holds them; the attempt
a job is on, once it has used one; and for a job that waits for a time,
when it is due and why on a line of its own, the error a retry follows
or the rate limit a fetch waits out, whole on hover. Then it offers a
reload once there is something new to show: a new text, a failure, or
another change. Interests says where its rebuilds stand: every 2 seconds
while one is queued or running (and right after you click Rebuild),
since when; then "New interests are ready: reload" once a newer rebuild
is done. Otherwise it checks every 30 seconds, so a rebuild the daemon
starts on its own shows up on an open page, and says what the daemon last
found: when the interests were rebuilt and how much has changed since,
that a rebuild is due and what it waits for, or that rebuilds are held
(the embeddings drifted) or failing, with the error and when it is tried
again. Failures refreshes its groups, and the count on its tab, after a
refetch you make there and when you come back to its tab, never on a
timer. Nothing refreshes while the tab is in the background; it catches
up when you come back. If the daemon stops answering, a note says the
page isn't updating, and it goes once the daemon is back.

## The pages

The navigation is Search, Library, Interests and Status. The pages follow
your Mac's light or dark appearance. Every page but Search and the
starting page has a search box in its header (on a wide window), and the
footer names the address the daemon listens on.

- **Search** (`/ui/`, the home): a search box, with a type filter under
  it (All, Articles, Repos, Videos, PDFs). With nothing typed, the box
  says how many documents there are to search ("Search your 4,498
  documents"), and a quiet line at the foot of the screen names your six
  largest areas (or interests, in a library too small for areas), each
  linking to its page, and links to all of them.
  Type, and the same hybrid search as `curio search` runs as you type,
  limited to the type chosen, which the address keeps
  (`/ui/?q=kafka&content_type=pdf`). The results come 10 a page, with
  numbered pages under them, up to the best 100 (`&page=2`); typing or
  choosing another type starts again at page 1, and a page past the last
  says how many pages there are. A line over them counts what matched
  ("37 documents match"), or, past 100, says "100+ documents match ·
  showing the best 100", and the last page then says to refine the query
  to see others. Each result shows where the page lives, its title, how
  it matched (keyword, meaning, or both), and the first two passages
  that matched with the matched words highlighted (the rest a click
  away). A passage is shown without its markdown (a link's text without
  its address, no heading or bold marks); the document's page has the
  whole text. **Show scores** adds each passage's bm25 and vector scores
  and each result's fused score, and stays on while you type and page. A
  page with no title of its own is named by its bookmark's title, in
  italics, as the Library names it, and so is a related document on a
  document's page. When semantic search is unavailable (Ollama down),
  the keyword results come under a warning. The old address,
  `/ui/search?q=…`, leads here with its query.
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
  (`/ui/library?cause=anti_bot`, where Failures' links lead) narrows it
  to the documents that failed for one cause: a line under the filters
  names it, with **Clear**, which drops the cause and keeps everything
  else, and paging, the tabs and filtering keep it. Above the filters,
  **Documents** and **Failures** switch between the Library's two views;
  Failures shows how many documents failed or are dead, whatever the
  filters.

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
- **Failures** (`/ui/failures`), the Library's second tab: the documents
  that couldn't be fetched, grouped by why. A line counts them, failed
  and dead links apart, then a card for each cause, the most first: what
  the cause means, the sites most of its documents are on (each leading
  to the Library of that cause on that site), how many documents and
  their share, **Refetch N**, and **View in Library →**; the dead links'
  refetch asks first. Under the cards, the causes no document failed for.
  A refetch queues a fresh fetch for each document of the group, so its
  card goes; while the queue is paused, or outside its schedule, the
  fetches wait there (Status says so), and the documents are pending
  meanwhile.
- **Document**: the page's title (for an untitled page, its bookmark's
  title, in italics, as the Library names it) and address, its state,
  type, length, author, published date and language; for a failed page,
  a box naming why it failed and what that means, with its **Full
  error** a click away, and for a dead one, that the page is gone. Then
  its text, and beside it related documents, its bookmarks with their
  folders and tags, and its details: when it was added, updated and
  fetched (Jina Reader called out), its ID, where its markdown is, and
  the `curio` commands that work for it. Under its facts, a line says
  where the last rebuild of the interests put it: "In Area › Interest", a
  loose fit of one, in Unsorted with the interest it is nearest, or "New
  since the last rebuild" with where it was placed; none before the first
  rebuild. Under its head, **Refetch** and
  **Reindex**, and how its text was fetched; for a dead link, **Refetch
  anyway…**, which asks first. The text shown is at most the first 1
  MiB; the page says when it is cut and where the whole file is. A text
  whose markdown would take too long or too much memory to format (a
  line nesting dozens of quotes, lists nested deep over thousands of
  blank lines, a paragraph thick with unclosed brackets, a table padded
  to thousands of columns: a few shapes a page can hold, by accident or
  on purpose) is shown as it is stored, unformatted, and the page says
  why. Whatever the text holds, formatting stops after two seconds or a
  gigabyte of memory, and the text is shown as stored. Web and email
  addresses written out in the text without link markup show as text,
  not links: a GitHub README's bare URLs, say.
- **Interests**: the topics the last rebuild found, largest first, 24 a
  page. A library of about 1,000 documents or more is grouped in two
  levels: areas, each holding interests ("29 areas holding 182 interests
  in your library"); a smaller one in interests alone. The head says how
  many there are, and beside it where rebuilds stand: when the interests
  were last rebuilt (fresh or warm) and how many documents changed since
  against how many make the next due; that a rebuild is due, and what it
  waits for (the library to settle, or a re-embedding to finish); queued,
  and why the queue holds it; running, and since when; held, while the
  embeddings may have drifted, with the fix (`curio reindex --all`); or
  failing, with the error (whole on hover) and when it is tried again.
  **Rebuild** groups the library again now, whatever that line says: a
  rebuild you ask for isn't held, and it retries a failure at once; it is
  disabled only while one is queued or running. For a week after a
  rebuild that split, merged or dissolved interests, a note says how many
  ("Rebuilt on Oct 9, 2026: 5 interests split, 1 merged, 9 new") and leads
  to what changed. A bar shows where the run's documents, and those added
  since, are: in an interest, a loose fit of one, new since the rebuild,
  or unsorted, with their counts and shares under it. An area's card
  counts its documents, its interests and those added since ("4 new"),
  and lists its five largest interests, each with its size and a bar
  against the largest, then "+ N more interests →" to the rest. An
  interest's card counts its documents, its loose fits and its new ones,
  and shows a few of its documents (an untitled one named by its
  bookmark's title, in italics). After the last page's cards, **Unsorted**
  counts the documents close to no interest and those placed there since.
  Numbered pages under the cards lead through the rest (a phone shows
  "Page 2 of 10" between Previous and Next). A rebuild that finishes while
  you page through leads to the new run's page of that number, which says
  the interests were rebuilt; a page past the last says how many pages
  there are. Until the first rebuild is done the page says why: the
  library is being grouped for the first time, the first grouping is due
  once the library settles, how many documents it waits for, or that it
  is held or failing.
  With insight turned off in config.yaml (`insight.enabled: false`),
  there is no Rebuild, and the page says how to turn it on.
- **An area's page**: the area's summary, its documents, interests,
  loose fits, new documents and cohesion, the run it comes from, and what
  the last rebuild did to it; then its interests as cards, 24 a page.
- **An interest's page**: Interests › its area › the interest above its
  name, each cut on one line; its documents, loose fits, new documents,
  cohesion and run. A note says what the last rebuild did to it, dated,
  each name linking to its page: split off from another, another split
  off from it, took in others, moved here from another area, or new; one
  it only kept says nothing. On the first page, "New since the last
  rebuild": the documents indexed since and placed into it by similarity,
  newest first, each tagged new (the next rebuild decides for good); then
  its documents by similarity, 50 a page, ranked across the pages; then,
  under their own heading, its loose fits: documents close to it but not
  grouped with it, which never name it.
- **Unsorted** (`/ui/interests/unsorted`): the documents close to no
  interest, nearest one first, 50 a page, each with the interest it is
  nearest (on a phone, under the document's name) and how close; on the
  first page, those placed there since.
- **What changed** (`/ui/interests/changes`): the last rebuild, when it
  ran, what started it and its counts, then what it split, merged, moved,
  dissolved and created, under a heading each, areas first; a first
  grouping, and a rebuild that changed nothing, say so in a line.
- An interest keeps its ID, and its page its address, across rebuilds
  while it keeps most of its documents. One that a rebuild split, merged
  into another or dissolved answers 410 in the Interests' frame, saying
  when and what became of it ("It split into these on Oct 9, 2026"), each
  successor linking to its page with the documents it took (one retired
  since is marked, and its page says what became of it). An ID nothing
  knows answers 404: it may be from before an upgrade regrouped the
  interests, or name one retired more than 180 days ago, which curio no
  longer remembers.
- **Status**: what curio is doing, and whether what it needs works. What
  needs your attention comes first: Ollama not ready, the embeddings
  drifted, or may have drifted, since the library was indexed (what
  changed, what a re-embedded sample of the library showed, and the
  command that fixes it), or the Jina Reader fallback failing or
  degraded. Then cards for the library (how many documents,
  fetched and bookmarks, and a bar of the documents by state, each state
  linking to the Library of those), the queue (open or closed and why,
  with Pause or Resume, its throttle, keep-awake and schedule controls,
  and each pool's load and the jobs it finished in the last 10 minutes),
  and why documents failed (the five commonest causes as bars, each
  leading to its group on Failures, and **All failures →** to the tab);
  beside them, health (the daemon, Ollama, the models, the interests,
  embedding drift, the Jina Reader fallback and the YouTube fetcher,
  each with a status dot; the interests' row says where their rebuilds
  stand, in the line `curio status` prints, its dot a warning while they
  are held or failing), an estimate of when the queued work will be done,
  at the pace of the last 10 minutes, and the jobs by status. The estimate counts only
  the jobs that can run now: jobs due later (a retry backing off, a fetch
  waiting for a rate limit) are said apart, how many and when the first is
  due, and when they are all that is queued, the card says so instead of
  that the queue stalled; so does the queue's state line.

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
/v1, sent by a first-party module", "Dashboard: the Failures tab",
"Dashboard: two-level interests" and "Local API: loopback only, no token,
browsers shut out".
