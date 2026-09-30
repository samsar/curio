# Setup

Curio has **one** runtime dependency: Ollama, for embeddings. The fetcher
that extracts article content from URLs is Go-native (a port of
[samsar/web-to-markdown](https://github.com/samsar/web-to-markdown)) and
needs nothing external.

Optionally, you can switch to the Node-based `web2md` fetcher (set
`fetcher.default: web2md` in config). It produces slightly different
extraction results — the Mozilla Readability source-of-truth — and is
useful as a fallback / comparison backend.

This document covers macOS. Linux / Windows users follow the same shape
but with platform-appropriate package managers.

## First run: `curio up`

```sh
curio up
```

`curio up` sets curio up on this Mac, and keeps it set up. It checks six
things, in order, shows what it would do about each (the plan), and then
does it, asking before each step:

1. **The Mac.** Apple silicon with 16 GB of memory or more is what curio is
   built for. With 8 GB it runs with the smallest models, which swap in and
   out of memory: import overnight. Under Rosetta it says to install the
   arm64 build. An Intel Mac runs Ollama on its CPU only (a first import
   takes days), and curio up asks once whether to go on. Elsewhere than
   macOS, curio up installs nothing: install and start Ollama yourself.
2. **Ollama**, answering at `embedding.base_url`
   (`http://localhost:11434`). When nothing answers, curio up starts the
   Ollama that is installed (`brew services start ollama` for the Homebrew
   formula, `open -a Ollama` for the app) or installs it (`brew install
   ollama`, then `brew services start ollama`), and waits up to a minute
   for it to answer. Without Homebrew, or on an Intel Mac, it opens
   <https://ollama.com/download> and waits up to 10 minutes for you to
   install and open the app. An Ollama too old for the models (gemma4 needs
   0.30.5, its QAT tags 0.30.6) is upgraded with `brew upgrade ollama` and `brew services restart
   ollama` when Homebrew installed it; the app you update yourself.
   Homebrew has a prebuilt Ollama only for the newest macOS (on older
   releases `brew install` builds it from source, which takes a while) and
   none for Intel, which is why the app from ollama.com is the
   alternative. An Ollama elsewhere than this Mac is yours to start.
3. **The models.** curio picks them for your Mac's memory, says why, and
   offers the tier below: `Use these? [Y/n/choose]`, where `choose` lists
   every tier. It pulls the ones Ollama doesn't have, with progress, after
   checking the disk has room for them and 2 GiB more.

   | Memory | Writing model | |
   |---|---|---|
   | under 16 GB | `qwen3:4b-instruct` | 2.5 GB |
   | 16 GB | `gemma4:12b` | 7.6 GB |
   | 32 GB | `gemma4:26b-a4b-it-qat` | 16 GB |
   | 64 GB or more | `gemma4:26b` | 19 GB |

   The embedding model is `qwen3-embedding:0.6b` (639 MB) everywhere. A
   `generation.model` already in `config.yaml` is kept, and nothing is
   asked; with interest labels off (`insight.labeling: terms`) no writing
   model is pulled.
4. **The home**, `~/.curio` or `$CURIO_HOME`, and its `config.yaml`. A new
   home records the embedding model and the width of its vectors, measured
   with one embed after the pull. curio up writes `config.yaml` once, when
   there is none, with those and the writing model, and never edits it
   again: it is yours. A new home needs 2 GiB free on its volume, whether
   or not a model is pulled there. An existing home is checked, never
   converted (see `--fresh`). A directory that isn't a curio home is never
   written into.
5. **The daemon**, kept running by its launchd agent (see "Keep the daemon
   running"): installed, running this curio's `curio-daemon`, and
   restarted when it runs another build or another writing model than
   `config.yaml`'s. Over ssh, with no desktop session for the agent, the
   daemon is started on demand instead, with a warning.
6. **Your bookmarks**, when the library has none (a new home's hasn't).
   curio up offers what it finds, each with how many of its bookmarks are
   new to the library (and how many pages that fetches, when some are
   there already from another browser), counted by the daemon without
   saving anything: each Chrome profile, Firefox's default profile,
   Safari, or an exported HTML file whose path you type or drag into the
   terminal. `Skip for now` imports nothing, and the next run offers
   again. A library with bookmarks is left alone: `curio import` adds
   more. `--import` names the source up front (see below); run again with
   the same `--import`, it imports only what is new.
   Without a terminal, or with `--yes`, nothing is imported unless
   `--import` says what: the status lists the `--import` values for the
   sources found.
   - **Safari** needs Full Disk Access: macOS grants it to the terminal
     app curio runs in (Terminal, iTerm2), not to curio. curio up explains
     it, offers to open the setting (`/usr/bin/open
     'x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles'`)
     and checks again when you say so. macOS applies it only once the
     terminal app restarts: quit it, reopen it, and run `curio up` again.
     Only you can turn it on, so without a terminal, or with `--yes`,
     `--import safari` stops at the plan with these steps instead.
   - **YouTube videos** among the new bookmarks: curio indexes a video's
     transcript through yt-dlp. curio up offers `brew install yt-dlp`,
     then restarts the daemon, which finds yt-dlp only as it starts, and
     checks that it does. When yt-dlp is installed already it only
     restarts the daemon; a no is remembered in `setup.json`, and not
     asked again.
   - **How long it takes**, as ranges: fetching is network-bound, 25 to 45
     pages a minute with 16 fetch workers, at most 2 at a time from one
     site, so a site that holds many of the new pages is named as taking
     longer; indexing is Ollama-bound, measured on this Mac (one batch of
     32 sample chunks, at about 23 chunks a page). GitHub pages without a
     `fetcher.github.token` in `config.yaml` are mentioned: GitHub allows
     60 API requests an hour without one, about 30 repositories, so they
     wait for its hourly limit, and the note says for about how long. Any
     token lifts that to 5,000, even one that can access nothing: a
     classic token with no scopes ticked, or a fine-grained token with
     public repositories (read-only) and no permissions. `curio doctor`
     warns while the daemon's GitHub requests carry no token.
   - **When to work through them**: now at full speed, gently (`curio
     throttle gentle`), or only overnight, 22:00 to 07:00 (`curio
     schedule`), the default on a Mac with under 16 GB, each with when to
     check back. Then whether to keep the Mac awake (see "Keeping the Mac
     awake during an import"), as it is by default, off on a new home, so
     `--yes` never turns it on. Both are set on the queue before any
     bookmark is sent.

When every check passes, curio up changes nothing and says `Nothing to do:
curio is up.`, then the status: the daemon and whether launchd keeps it
running, Ollama and the models, the library's size, the queue, and
anything worth knowing (an embedding drift, a warning about the Mac). A
run that started an import ends with `Import started. Check back after
05:14.` (or `tomorrow 05:14`), what it imports, and what to do meanwhile:
`curio status --follow`, which follows the queue until it drains, `curio
pause | resume`, `curio throttle gentle`, `curio search "..."`, and
registering curio with Claude Code (`claude mcp add curio --
curio-mcp`). Any other run that did something ends with what to do next.
Run it again whenever you like: nothing records where a run stopped, so a
run cut short (ctrl-c, a no) picks up where it left off; an import cut
short says how many bookmarks were sent, and `curio up --import <source>`
finishes it, skipping those already saved. `curio doctor` runs the same
checks (all but the import) and reports them, changing nothing.

What curio up asks, and never does unasked:

- Every command it runs is shown in full before the question, and its
  output streams as it runs. A command that fails is named, with how it
  exited, so you can run it yourself.
- One question per step, yes by default, except moving a home aside,
  which is no by default. Creating a new home and writing its `config.yaml`
  are only announced: those files are curio's.
- It never uses sudo, and refuses to run as root.
- A question left without an answer (ctrl-c, ctrl-d) stops the run; it is
  never taken for yes.

| Flag | Does |
|---|---|
| `--yes` | Answer every question: yes to each step, moving the home aside with `--fresh` included, and the default to each choice. It never implies `--fresh`. |
| `--no-install` | Install and start nothing with Homebrew or the Ollama app: the plan shows the commands to run yourself. Pulling models, the home, `config.yaml` and the launchd agent are curio's own work, and still happen. |
| `--dry-run` | Show the plan and change nothing. |
| `--fresh` | Move the existing home aside to `<home>.bak-<YYYYMMDD-HHMMSS>` and start a new one; see below. |
| `--embedding-model <tag>` | Embed a new home with this model instead of `qwen3-embedding:0.6b`. Name a tag: a home embeds with one model for good. |
| `--generation-model <tag>` | Write interest labels with this model instead of curio's pick. It can't override a `generation.model` in `config.yaml`: edit that instead. |
| `--import <source>` | Import from this source without the menu, whatever the library holds: `chrome` (the Default profile), `chrome:<profile>` (its directory, `Profile 1`, or its name), `safari`, `firefox`, or `html:<file>`. One that isn't there, can't be read or holds no bookmarks stops the run before anything is done. |

Without a terminal (in a script, with stdin or stderr redirected), curio
up never asks: it prints the plan and exits 1, unless `--yes` answers for
you. It exits 0 when there was nothing to do, when it did everything, and
after a dry run with no blocker; 1 when a blocker stopped it, a dry run's
included (a home it won't touch, a
config.yaml that doesn't load, too little disk, an `--import` with
nothing to import; the plan says what to do),
when you said no, or when a step failed; and 130 when a question was left
unanswered.

### `--fresh`: start a new home

A home made by an older curio, or with another embedding model, can't be
converted: its vectors came from another model. `curio up --fresh` sets
it aside and starts over. It stops the daemon (removing its launchd agent
first), moves the whole home to `~/.curio.bak-<YYYYMMDD-HHMMSS>` (`-2` and
so on when that name is taken) with one rename, deleting nothing, and
carries on as a first run; nothing starts a daemon in the home while it
moves. A symlinked home is moved, and made again, where the link points.
It asks first, no by default, naming both paths. Then import your
bookmarks again. The old home stays whole at its new name until you
delete it.

## Ollama by hand

`curio up` installs and starts Ollama for you. To do it yourself (or with
`curio up --no-install`), install via Homebrew on macOS. The native install
uses Metal acceleration on Apple Silicon, which is noticeably faster than a
containerized build.

```sh
brew install ollama
ollama serve &                        # background; or use the menu-bar app
ollama pull qwen3-embedding:0.6b      # 639 MB; the embedding model (optional — see below)
ollama pull qwen3:4b-instruct         # 2.5 GB; the writing model, for interest labels
ollama list                           # verify
```

curio uses two models (`curio up` picks the writing model for your Mac):

| Model | Size | Used for |
|---|---|---|
| `qwen3-embedding:0.6b` | 639 MB (q8_0) | embedding chunks and queries: 1024-dimensional vectors, a 32K-token context |
| `qwen3:4b-instruct` | 2.5 GB | naming and summarizing interests (`generation.model`; see "Change the writing model") |

Always pull a tag. An untagged name means `:latest`, which moves when the
library does, and for `qwen3-embedding` it is the 8B model, not the 0.6B
one curio uses. curio compares names the way Ollama resolves them, so
`qwen3-embedding` counts as missing when only `qwen3-embedding:0.6b` is
pulled.

You can skip the `ollama pull` steps: the daemon **auto-pulls** the models
it needs — the embedding model and, when insight labeling is on, the
writing model. It pulls in the background and keeps retrying, 5 s after a
failure and doubling up to every 5 minutes, until Ollama answers and the
pull completes, so Ollama can start before or after the daemon. The first
failure is logged at WARN in `~/.curio/logs/daemon.log`; retries, and the
pulls they start, only at debug. Until the model is ready, index jobs retry
with backoff and cluster labels fall back to term labels. Disable with
`embedding.auto_pull: false` / `generation.auto_pull: false` in
`config.yaml` (e.g. on a metered connection), and pull manually instead.

Alternative: install via the macOS app from ollama.com — same result, runs
as a launchd service, less terminal management. Either way the daemon
listens on `http://localhost:11434`.

### Verify Ollama works

```sh
curl -s http://localhost:11434/api/tags | jq '.models[].name'
# Should list "qwen3-embedding:0.6b"

curl -s http://localhost:11434/api/embed \
  -d '{"model":"qwen3-embedding:0.6b","input":["hello"],"truncate":false}' | jq '.embeddings[0] | length'
# Should print 1024
```

### Ollama serves one request at a time

`OLLAMA_NUM_PARALLEL`, how many requests Ollama runs at once per model,
defaults to 1. The daemon's index workers (`daemon.index_workers`, 4 by
default) then queue inside Ollama, and the time an embed request waits
there counts against `embedding.timeout_seconds`. On a slow machine, where
index jobs time out, lower `daemon.index_workers` or run `curio throttle
gentle` (one index job at a time). Raise `OLLAMA_NUM_PARALLEL` only with
memory to spare: each parallel slot holds its own context. curio doesn't
manage Ollama's environment; set it where Ollama starts (`launchctl setenv
OLLAMA_NUM_PARALLEL 2` for the app or `brew services`, then restart Ollama;
or in the shell that runs `ollama serve`).

### Change the writing model

`generation.model` in `~/.curio/config.yaml` names the model that writes
interest labels; curio sends it `think: false` and an explicit `num_ctx`
on every request. Edit it and run `curio up`, which pulls the new model and
restarts the daemon. (Or restart the daemon yourself, `curio daemon stop`,
and the next command starts it: with `generation.auto_pull` on, it pulls
the new model in the background, and interests get term labels until it is
ready.) Nothing needs reindexing: the home records only the embedding
model.

Pick by the Mac's unified memory (`curio up` does, when `config.yaml`
names none):

| Memory | `generation.model` |
|---|---|
| 8 GB | `qwen3:4b-instruct` (the default) |
| 16 GB | `gemma4:12b` |
| 32 GB | `gemma4:26b-a4b-it-qat` |
| 64 GB or more | `gemma4:26b` |

A model that can't turn thinking off answers the request with an error,
and its interests get term labels; pick another.

### The embedding model is the home's

The embedding model and the width of its vectors are fixed when a home is
created and recorded in its marker, `.curio-meta.json`; the vector index
takes that width. `embedding.model` and `embedding.dim` in `config.yaml`
must match the marker, or the daemon refuses to start and `curio doctor`
fails its `curio home` check, naming both files and both values. To use
another embedding model, start a new home with `curio up --fresh` (or move
`~/.curio` aside yourself) and import your bookmarks again.

### Embedding drift

The same model name can make different vectors after an Ollama upgrade or
a pull that brings a new build of the model: Ollama 0.30 made
nomic-embed-text lowercase its input, for one. New queries then stop
matching the stored vectors, and search quietly gets worse. Most upgrades
change nothing, though. The daemon records the model's digest (what
`ollama list` shows) and the Ollama version when it first reaches them,
and checks every minute. When either changes, it re-embeds a sample of
your library, up to 64 chunks (about 6 seconds on an M4 Max), and compares
each with its stored vector:

- **They match:** the daemon takes the new build as the baseline, logs
  it, and there is nothing to do.
- **They don't:** `curio status` prints a warning line, `curio doctor`
  warns and lists what changed, with the evidence (`64 of 64 sampled
  chunks changed (worst cosine 0.9713)`), the dashboard's Status page
  shows it, `/v1/healthz` reports `embedding_drift`, and the log has one
  warning.
- **The sample can't be re-embedded:** at once when the new build can't
  embed it at all (a reply of the wrong width, say), otherwise after three
  failed attempts about 45 minutes apart, the same places say the
  embeddings *may* have drifted, and why. The daemon keeps trying.

The fix is

```sh
curio reindex --all
```

which re-embeds every fetched document and takes the build serving now as
the new baseline. Search mixes old and new vectors until the index jobs
finish. The daemon never reindexes by itself.

## Fetcher options

The default fetcher (`fetcher.default: native`) needs no setup — it's
built into the curio binary. You can switch to `web2md` if you want the
Mozilla Readability source-of-truth extraction:

```sh
git clone https://github.com/samsar/web-to-markdown ~/code/web-to-markdown
cd ~/code/web-to-markdown
node --version            # 18+ required; uses native fetch
npm install               # installs JSDOM, Readability, Turndown
```

Then in `~/.curio/config.yaml`:

```yaml
fetcher:
  default: "web2md"
  web2md:
    bin: "/Users/you/code/web-to-markdown/web2md.js"
    timeout_seconds: 30
```

### Jina Reader

The native fetcher asks [Jina Reader](https://jina.ai/reader)
(`r.jina.ai`) for the pages it can't read itself: pages the site blocks
curio from, login pages and pages too thin to be the article, and PDFs
the local extractor can't read. `fetcher.native.jina_fallback: false`
turns that off.

```yaml
fetcher:
  native:
    jina_fallback: true
    jina_api_key: ""                    # or CURIO_JINA_API_KEY, for a daemon started from your shell
    jina_site_requests_per_minute: 6
```

Without a key Jina allows 20 requests a minute, and curio sends no more;
with a free key (`jina_api_key`) curio sends up to 200. On top of that,
`jina_site_requests_per_minute` caps the requests for the pages of any one
site, key or not: 6 a minute by default, one every 10 seconds. A site is
its registrable domain, so `mobile.twitter.com` and `twitter.com` count as
one. A page whose site's turn is more than 30 seconds off waits in the
queue for it, without using up an attempt; the other sites' pages go
meanwhile. Jina blocks keyless reads of a site for about an hour after a
run of them. 6 a minute is under the 10 in one minute that one site took
without a block, but another was blocked after about 80 reads in 19
minutes, some 4 a minute, so a long run of one site's pages may still be
blocked. curio then waits the block out (see Troubleshooting); lower the
value if that keeps happening. Raise it to speed up an import dominated by
one site only with a key, since Jina's blocks name anonymous access.
Restart the daemon after a change (`curio daemon stop`; the next command
starts it).

When a site answers curio's own request with 403 or 503, or sends it to
its login page, and Jina turns that page down too (or the fallback is
off), curio stops asking the site for 15 minutes: its other pages go
straight to Jina, and each gets Jina's verdict of its own. When Jina
serves the page, or can't answer for now, nothing is cached, and the
site's next pages ask the site again. With the fallback off, the pages
wait for the 15 minutes to pass, and a site that keeps refusing curio
holds them for up to a day: each time the 15 minutes end, about two of
them ask the site again, each spending one of its attempts, and the rest
wait once more. They fail, as `anti_bot` or `login_wall`, when their
attempts or their day run out.

### YouTube

YouTube URLs go to the YouTube fetcher when yt-dlp is installed
(`fetcher.youtube.bin`, by default `yt-dlp` on the daemon's PATH). It
stores the video's metadata, its description and an English transcript.

```yaml
fetcher:
  youtube:
    bin: "yt-dlp"
    timeout_seconds: 60
    # sub_langs: leave unset for the default, en,en-(?-i:[A-Z]{2})
```

`sub_langs` is passed to yt-dlp's `--sub-langs`. yt-dlp matches each
comma-separated item, as a case-insensitive regular expression, against the
whole language key of every caption track, uploaded and automatic, and
downloads each track that matches. The default picks:

- `en`: the uploaded English track, or else YouTube's automatic one. For a
  video in another language that is its captions machine-translated into
  English.
- `en-(?-i:[A-Z]{2})`: uploaded regional English tracks such as `en-GB` and
  `en-US`. The region is case-sensitive so that YouTube's automatic
  translations, keyed `en-<source language>` (`en-zh`, `en-ca`), don't
  match.

That is one or two caption requests per video. A broader pattern costs one
request per track it matches, and YouTube answers too many with HTTP 429:
`en.*` also matches one machine translation for every caption language the
video was uploaded with, and popular videos have dozens. What the default
gives up is named English tracks (`en-<id>`) and, on a video without
automatic captions, the English translation of an uploaded track in
another language. Such a video is stored with its description only unless
you widen `sub_langs`.

If your config sets `sub_langs: "en.*,en"`, the old default, delete the key
to get the new one.

A video whose captions couldn't be downloaded (YouTube answering 429, say)
is still stored, with its description only: `curio docs show <id>`
reports the extraction as `partial` with yt-dlp's reason under `err:`.
`curio refetch <id>` tries the transcript again. After a 429, curio holds
every YouTube fetch for two minutes, so one throttle doesn't cost a whole
import its transcripts. A refetch replaces what a video had, so one that
meets a 429 while refetching a video that already has its transcript
leaves it description-only until a later refetch gets the transcript;
`curio refetch --all` includes fetched videos.

## Demo: import and search your bookmarks

End-to-end flow using a Chrome HTML export. Substitute your own browser/path.

```sh
# 1. Export your bookmarks: Chrome → Bookmark Manager → ⋮ → Export bookmarks
#    Saves a .html file (Netscape Bookmark format).

# 2. Set curio up: Ollama, the models, ~/.curio, the daemon. It offers to
#    import the export too (--import html:~/Downloads/bookmarks.html picks
#    it up front); skip that to import it by hand, step by step, below.
curio up

# 3. Dry-run first to see what'd happen without actually importing.
curio import html --dry-run ~/Downloads/bookmarks.html

# 4. Import a slice incrementally to make sure your setup works.
curio import html --limit 50 --follow ~/Downloads/bookmarks.html

# 5. If happy, import the full file.
curio import html --follow ~/Downloads/bookmarks.html

# 6. Search the corpus.
curio search "feature flag rollout"

# 7. See failures (404s, paywalls, embedding errors). `curio docs --failed`
#    shows each failed document's cause (anti_bot, login_wall, tls, ...).
curio jobs --failed

# 8. Retry failures after fixing whatever caused them: all of them, or
#    those of one cause.
curio refetch --all --state failed
curio refetch --all --cause anti_bot

# 9. Check overall state.
curio status

# 10. Stop the daemon when done.
curio daemon stop
```

Time budget: with the default pools (16 fetch workers, 4 index workers) and
the Native fetcher, expect roughly 1-2 seconds per bookmark — so 1000
bookmarks ≈ 4-8 minutes wall-clock. Larger files scale linearly. Use `--limit`
to test in chunks. Pool sizes are `daemon.fetch_workers` and
`daemon.index_workers` in `~/.curio/config.yaml`. The deprecated
`daemon.workers` is split 75/25 between them and can't be combined with
either.

## Pausing, throttling or scheduling an import

A large import keeps the daemon, and Ollama, busy for a while. Three
settings pace the work. Each is stored in the database, so it holds across
daemon restarts until changed, and none interrupts a job already running:
those finish.

```sh
curio pause                   # start no new jobs; running ones finish
curio resume                  # start them again
curio throttle gentle         # at most 4 fetches and 1 index job at once
curio throttle normal         # every worker again
curio schedule 22:00-07:00    # start jobs only overnight, on the daemon's clock
curio schedule off            # at any time again
```

`gentle` exists to keep the machine cool and quiet: embedding runs in
Ollama's own process, so what spares the machine is fewer embed requests at
once, one index job instead of four; lowering curio-daemon's priority
wouldn't reach Ollama. Clustering runs one job at a time either way.

A pause and a schedule must both allow work. `curio resume` outside the
window leaves the queue closed until the window opens; `curio schedule
off` runs it now. The window runs from its start up to its end and may
wrap midnight; when daylight saving skips its start, it opens as the
clock resumes.

`curio status` shows the queue's state (open, paused, or closed outside
the schedule and when it opens) and each pool's running jobs against its
limit, with what is pending. `curio add --wait` and `curio import
--follow` say when the queue is closed and what opens it. `curio pause`
starts the daemon if it isn't running.

## Keeping the Mac awake during an import

An import stops whenever the Mac idle-sleeps. Keep-awake holds it awake
while there is work, on AC power only:

```sh
curio keep-awake on    # hold the Mac awake while jobs are queued, on AC power
curio keep-awake off   # let it sleep as usual
```

With keep-awake on, the daemon runs `caffeinate -i -w <its pid>` while its
workers have jobs queued or running, the queue isn't paused, and the Mac
draws from AC power (`pmset -g ps`, read once a minute while there is
work). It lets go when the queue drains, within a minute of the Mac
going on battery, and at once when you pause the queue or turn keep-awake
off. A schedule keeps the hold while the queue waits for its window, so an
overnight import starts: the Mac stays awake until then. The hold prevents
idle sleep only: the display still sleeps, and closing a laptop's lid
still puts it to sleep. The setting is stored like the queue settings and
survives restarts; it is off until you turn it on.

`curio status` says whether keep-awake is on and, if so, whether the Mac
is being held awake, or why not (on battery, the queue paused, nothing
queued).

## Curio itself

To build curio from source (web2md is optional; see "Fetcher options"),
the README's "Building from source" lists the Go and C toolchains it needs.

```sh
cd ~/projects/curio
make build
./bin/curio version
./bin/curio up            # the daemon listens on 127.0.0.1:8765; JSON logs in ~/.curio/logs/daemon.log
curl -s http://localhost:8765/v1/healthz | jq
```

`curio up` creates the home, `~/.curio` or `$CURIO_HOME`: the
`.curio-meta.json` marker, `content/`, `logs/` and `config.yaml`; the
daemon creates and migrates `curio.db`. Any other command (or the daemon)
run first creates a home with the default embedding model and no
`config.yaml`, which means the defaults; `curio up` then writes one. A
directory that already exists without `.curio-meta.json` is refused rather
than adopted, so pointing `CURIO_HOME` at the wrong directory can't write
into it: use a path that doesn't exist yet.

The first start after an upgrade may migrate the database, which can take
a minute on a large library. `curio daemon start`, or whichever command
started the daemon, prints one line to stderr saying so and waits;
`curio daemon logs -f` shows each migration as it runs. Meanwhile
`/v1/healthz` answers 503 with a `Retry-After` header, the daemon's pid
and how many migrations are applied, and every other request gets 503
until the daemon is ready. `curio daemon status` shows the same progress.

After you rebuild or upgrade curio, the daemon already running is still
the old build. `curio up` restarts it; `curio daemon start` says so,
naming both versions: run `curio daemon stop`, and the next command starts
the new daemon. If the launchd agent (below) runs a curio-daemon other than this curio's, say an
older install elsewhere, a stop would only start that one again, so the
warning says to run `curio daemon install` instead, which repoints the
agent and restarts the daemon.

## Keep the daemon running (launchd)

By default the CLI and the MCP sidecar start the daemon when they need it,
and it runs until it is stopped, the Mac restarts, or it crashes. A
launchd agent keeps it running instead; `curio up` installs it, and so
does:

```sh
curio daemon install     # start the daemon at login, restart it after a crash
curio daemon uninstall   # back to starting it on demand
```

`curio daemon install` writes a per-user agent,
`~/Library/LaunchAgents/com.github.samsar.curio.daemon.plist` for
`~/.curio` (a home elsewhere gets its own agent, the label ending in a
hash of its path), loads it and waits for its daemon to serve. It runs the
`curio-daemon` next to the `curio` you ran, by the path it was run by, so
a `brew upgrade` needs no new agent; run it again after moving curio.
It never uses sudo. macOS may announce a background item from
curio-daemon: keep it allowed in System Settings > General > Login Items
& Extensions, or launchd won't run it. A daemon a command started before
the install is stopped first, so the agent's can take over.

With the agent installed:

- `curio daemon start`, and every command that needs the daemon, start it
  through launchd (`launchctl kickstart`) rather than as a child process.
- `curio daemon stop` stops it through launchd (`launchctl kill
  SIGTERM`); launchd doesn't restart a daemon that stopped cleanly, and
  starts it again at the next login or when a command needs it. A daemon
  that crashes, or can't bind its port, is restarted, every 10 seconds at
  most, until it runs. One that refuses its `config.yaml` or its home
  (invalid, or for another embedding model) stays down, having said why
  once in `logs/daemon.log`: restarting changes nothing until you fix it,
  after which the next curio command, or `curio up`, starts it.
- `curio daemon status` says whether launchd runs the daemon, and `curio
  doctor` checks the agent: loaded, and running this curio's daemon.

The daemon's log is still `logs/daemon.log`: launchd sends the daemon's
output there. Its stderr goes to `logs/launchd.err`, which gets only what
the Go runtime prints when the daemon dies (a panic's stack trace); a
failed start quotes it.

launchd doesn't run the agent's daemon in your shell, so it doesn't see
your shell's environment:

- Its `PATH` is Homebrew's directories and the system's
  (`/opt/homebrew/bin:/opt/homebrew/sbin:/usr/local/bin:/usr/local/sbin:/usr/bin:/bin:/usr/sbin:/sbin`).
  A tool installed elsewhere (yt-dlp, node for web2md) needs its absolute
  path in `config.yaml` (`fetcher.youtube.bin`, `fetcher.web2md.node_bin`).
  The daemon logs `youtube fetcher disabled: yt-dlp not found`, with the
  PATH it searched, when it can't find yt-dlp.
- Tokens given only in the environment (`CURIO_GITHUB_TOKEN`,
  `CURIO_JINA_API_KEY`) don't reach it: put them in `config.yaml`
  (`fetcher.github.token`, `fetcher.native.jina_api_key`). `curio daemon
  install` warns about each one it sees. Nothing of the environment is
  written into the agent.

An agent runs only in a desktop login session. Over ssh with nobody
logged in at the Mac there is none: `curio daemon install` says so, and
stops nothing; `curio daemon status` and `curio doctor` say the agent has
no session to run in; and the CLI starts the daemon itself, as without an
agent.

## Config: time budgets for Ollama calls

Each bounds how long one kind of work waits on Ollama; all are validated as
positive. When a search or labeling budget runs out, the work degrades
(keyword-only results, term labels) rather than failing. An embed timeout
while indexing fails that index job, and the job queue retries it.

| Key | Default | Bounds |
|---|---|---|
| `embedding.timeout_seconds` | 60 | one embed request (the indexer sends at most 32 chunks per request), including the time it waits behind other requests inside Ollama |
| `search.embed_timeout_seconds` | 10 | embedding a search query; past it, search returns keyword-only results marked `degraded`. Keep it well under 30: the CLI and MCP give up on a request after 30 s, so a hung Ollama would surface as a client timeout instead |
| `generation.timeout_seconds` | 120 | one LLM request; timeouts aren't retried |
| `insight.labeling_timeout_seconds` | 900 | all LLM labeling in one clustering run; the rest get term labels |

## Troubleshooting

**Checking or retrying one page** — `curio docs show`, `curio refetch`,
`curio reindex` and `curio related` take the page's URL as well as its
document ID: `curio refetch https://example.com/article` finds the
document the URL was bookmarked under (as typed or pasted: case, a
`#fragment` and tracking parameters don't matter) and refetches it. A URL
must start with `http://` or `https://`; anything else is taken as an ID.
`curio jobs --all --limit 500 | grep -B1 -A4 <part of the url>` shows
every fetch and index attempt for it.

**`fts5` not available** — you built without the required tags. Always use
`make build` (or pass `-tags=sqlite_fts5,sqlite_json` to `go build`).

**`vec_version()` not found** — sqlite-vec failed to load. This means cgo
wasn't enabled. Ensure `CGO_ENABLED=1`; `make` forces this.

**"model not loaded" from `/api/embed`** — Ollama answered 404: the
embedding model isn't pulled. The daemon keeps retrying the pull in the
background (see above), or run `ollama pull qwen3-embedding:0.6b`. On a
very old Ollama (below 0.1.30 or so) the batched embed endpoint doesn't
exist and answers 404 too: upgrade it.

**`input longer than the embedding model's context`** on an index job —
Ollama refused a chunk longer than the context (`num_ctx` 8192, or the
model's own if smaller). curio asks Ollama never to truncate, since a
truncated chunk gets a vector for text it doesn't hold, so the job fails at
once and the document goes `failed`; its error names the chunks and the
longest one's size. Chunks are capped at 3500 bytes, well inside the
context, so this means the home's embedding model has a small context.
Lowering `chunking.size_tokens` and restarting the daemon helps only
chunks bounded by word count: the 3500-byte cap is fixed, so for a model
whose context is under about 3500 tokens, start a new home with a model
that has a larger one (see "The embedding model is the
home's"). Then bring the documents back: `curio reindex <id>` re-embeds
one from the content it already has and returns it to `fetched`, and
`curio refetch --all --state=failed` retries every failed document. A
search query that long falls back to keyword results with the same reason
in its warning.

**`curio home from an older curio`** — the home was made by a curio from
before home formats: its vectors came from another embedding model, under
other rules, and nothing converts them. The daemon refuses to start and
`curio doctor` fails its `curio home` check. Start a new home with `curio
up --fresh`, which moves the old one to `~/.curio.bak-<YYYYMMDD-HHMMSS>`
and deletes nothing (or move it aside yourself), then import your
bookmarks again.

**`embedding model mismatch`** — `config.yaml` sets an `embedding.model`
or `embedding.dim` other than the one the home was created with. Set them
back to what the error says the marker records, or start a new home for
the new model (see "The embedding model is the home's").

**`invalid TLS certificate: … x509: …`** on a document — the site's
certificate failed verification (expired, for another name, or from an
untrusted authority). curio won't fetch past that, not even through Jina,
and doesn't retry it. Once the site fixes its certificate, `curio refetch
<id>`, or `curio refetch --all --cause=tls` for every document that failed
this way. If every https document fails this way at once, something is
intercepting TLS (a captive portal, a corporate proxy) or the system clock
is badly off; fix that, then `curio refetch --all --cause=tls`. A
`jina: invalid TLS certificate` error is about the certificate of
`r.jina.ai`, the fallback reader, not the site's, and is retried like any
other Jina failure.

**`dead link (redirected to another site's landing page: …)`** on a
document — the bookmark redirects to another site's homepage or section
page, which kept nothing of what the bookmark named: the usual fate of a
retired site's pages. When that page refused curio, the reason follows
`HTTP 403 Forbidden:` or `HTTP 503 Service Unavailable:`; the verdict is
the same. A forced refetch (`curio refetch <id> --force`) applies the same
rule to the same redirect, so it only helps once the redirect changes. If
the page did move there, bookmark its new address (`curio add <url>`). If
the rules misjudge a whole corpus, set `fetcher.native.dead_link_detection:
false` in `~/.curio/config.yaml`, which turns off every dead-link rule
daemon-wide (404 and 410 are retried, soft 404s stored), restart the daemon
(`curio daemon stop`; the next command starts it), and refetch the dead
documents (`curio refetch --all --state=dead`, or `curio refetch <id>
--force`).

**`jina: r.jina.ai's CDN challenged the request`** — Jina Reader's
Cloudflare refused curio. Jina calls pause for 10 minutes (or the answer's
`Retry-After`), with one warning in the daemon log when a challenge starts
a pause (`r.jina.ai's CDN challenged curio, pausing Jina calls`); the
challenged answers of calls already in flight only extend it. `curio
doctor` shows the `jina` check paused until then, and failing once the
challenges have gone on for 30 minutes across documents (see the next
entry). The document whose request was challenged fails that attempt.
Documents that need Jina during the pause wait for it: the fetch sends no
request to Jina, and the job goes back to the queue until the pause ends,
without using up an attempt, then fetches the page again, whole (see
"Documents waiting for a rate limit" below). A document still held after a
day fails with `jina: not sent, cooldown has … left: HTTP 429 Too Many
Requests`, a 429 curio reports for its own pause, not an answer from Jina.
It is recorded under the cause of the site's own answer (`anti_bot`,
`login_wall`, or `unsupported` for a PDF curio couldn't read), not
`rate_limited`; once Jina answers again, `curio refetch <id>`.

**`curio doctor` reports `jina` degraded, paused or failing** — the daemon
counts how each request to Jina Reader, the fallback for pages the native
fetcher can't read, went over the last 15 minutes. Degraded means at least
a quarter of them failed, paused that Jina asked curio to wait (a rate
limit or a challenge), and failing that nothing but failures came back,
for documents on two or more sites, 5 in a row or for 30 minutes; the
daemon log then has one `upstream failing` warning, and `curio status` a
warning line. The hint names the kind of failure seen most:

- `challenged`: r.jina.ai's Cloudflare took curio for a bot; see the entry
  above.
- `forbidden`: r.jina.ai answered HTTP 403 without naming a site, which
  may mean it blocks curio itself. `curio jobs --failed` quotes what it
  said, if anything.
- `auth`: 401 is an invalid `fetcher.native.jina_api_key` (or
  `CURIO_JINA_API_KEY`), 402 a key with no balance left. Fix or remove the
  key, then restart the daemon (`curio daemon stop`; the next command
  starts it).
- `rate_limited`: Jina is rate-limiting curio, or blocking keyless reads
  of a busy site (an `AbuseAlleviationError`), which curio waits out. The
  check names the sites Jina blocks and until when, as `/v1/healthz` does
  under `upstreams[].site_pauses`; a block alone doesn't change the
  check's state. A key raises the limit from 20 requests a minute to 200,
  and Jina's blocks name anonymous access only.
- `server_error`: r.jina.ai is failing on its side. Fetches keep
  retrying; nothing to do but wait.
- `network`: curio can't reach r.jina.ai, or it doesn't answer in time.
  Check the machine's connectivity (a VPN, a proxy, DNS).

Failing clears on the next answer Jina gives, whatever it says about the
page; `curio refetch <id>` of a failed document makes a call. The state is
kept in memory: a restarted daemon starts over. Documents that ran out of
attempts meanwhile are `failed`: `curio refetch --all --state=failed` once
Jina answers again.

**Documents waiting for a rate limit** — when an upstream's rate limit
holds curio back (GitHub's API, YouTube after an `HTTP Error 429`, a pause
on Jina Reader calls, a site's turn at Jina Reader, Jina's block of a
site), a fetch that would meet it sends nothing and waits instead of
failing: its job goes back to the queue until the limit lifts, without
using up an attempt. A document's page then says its fetch is
waiting, when it is due, and why (`waiting for GitHub's API rate limit to
reset`); `curio jobs --status pending` shows the same reason as `last:`,
beside `next attempt:`; and Status's estimate counts the jobs due later
apart from the work it can do now, or says how many wait and when the
first is due when nothing else is queued (`curio status` adds `(N due
later)` to a pool's load). Without a token, GitHub serves about 30
repositories an hour, so a library with many github.com pages waits hours:
set `fetcher.github.token` (any token, even one with no scopes, lifts the
limit from 60 requests an hour to 5,000) and restart the daemon (`curio
daemon stop`; the next command starts it). A job waits for up to a day from
when it was queued; one still held after that fails. A GitHub or YouTube
page fails as `rate_limited`, which `curio refetch --all
--cause=rate_limited` retries, and so does a page waiting for Jina's block
of its site. A page waiting for a pause on Jina calls or for its site's
turn at Jina fails under the cause of its site's own answer (`anti_bot`,
`login_wall`, or `unsupported` for a PDF curio couldn't read), which
`--cause` with that name retries, along with the pages that failed that
way for reasons of their own. The
same commands recover documents that failed on these limits before curio
waited for them: set the token first, then `curio refetch --all
--cause=rate_limited`, or open Failures on the dashboard and refetch the
Rate limited group.

**`waiting for Jina Reader's block of <site> to lift`**, or `jina:
blocks the site for now: HTTP 403 Forbidden: AbuseAlleviationError:
Anonymous access to domain … blocked until …` — Jina Reader blocks keyless
reads of that site until the time it names, usually about an hour off,
after a burst of requests for it (curio's own, typically: a bulk import
from one site). curio waits it out instead of failing the pages: the page
that met the block and the site's other pages go back to the queue until
it ends, without using up an attempt, and send Jina nothing meanwhile.
When it ends they come back one at a time, at the site's pace (see "Jina
Reader" under Fetcher options). The daemon log warns once per block
(`Jina Reader blocks keyless reads of a site, holding its pages`), `curio
doctor`'s `jina` check names the blocked sites and until when, and
`/v1/healthz` lists them under `upstreams[].site_pauses`. A page still
held a day after it was queued (Jina has named 2039) fails as
`rate_limited`. A free Jina API key raises Jina's limit to 200 requests a
minute, and the blocks name anonymous access only: set
`fetcher.native.jina_api_key` in `~/.curio/config.yaml`, which a daemon
run by launchd needs (`CURIO_JINA_API_KEY` reaches only a daemon started
from your shell), and restart the daemon.

Documents that failed on such a block before curio waited for them read
`jina: refused the target: HTTP 403 Forbidden: AbuseAlleviationError: …`,
or, when the site had answered 403 itself, `(cached: jina: refused the
target: …)`. They are in the "Refused by Jina Reader" and "Blocked by bot
protection" groups: Refetch both on the dashboard's Failures tab, or run
`curio refetch --all --cause=jina_refused` and `curio refetch --all
--cause=anti_bot`. The groups hold pages that failed for reasons of their
own too, which fail again the same way. How long the refetch takes follows
curio's paces: pages that need Jina go at 20 a minute in all without a key
(about an hour for a thousand) and at 6 a minute for any one site.

**`jina: refused the target: HTTP 451 Unavailable For Legal Reasons: This
domain is excluded from Jina Reader at the request of its owner, …`** is
permanent: the site's owner opted out of Jina, and only the site itself
can serve curio its pages.

**`ENOENT: spawn node`** from a fetch — Node isn't on the daemon's PATH.
Either install Node into a directory in PATH or set
`fetcher.web2md.node_bin` in config.
