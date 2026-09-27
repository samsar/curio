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

## Ollama (native)

Install via Homebrew on macOS. The native install uses Metal acceleration on
Apple Silicon, which is noticeably faster than a containerized build.

```sh
brew install ollama
ollama serve &                    # background; or use the menu-bar app
ollama pull nomic-embed-text      # 274 MB; embedding model (optional — see below)
ollama list                       # verify
```

You can skip the `ollama pull` step: the daemon **auto-pulls** the models it
needs — the embedding model (`nomic-embed-text`) and, when insight labeling
is on, the generation model (`llama3.2`, ~2 GB). It pulls in the background
and keeps retrying, 5 s after a failure and doubling up to every 5 minutes,
until Ollama answers and the pull completes, so Ollama can start before or
after the daemon. The first failure is logged at WARN in
`~/.curio/logs/daemon.log`; retries, and the pulls they start, only at
debug. Until the model is ready, index jobs retry with backoff and cluster
labels fall back to term labels. Disable with `embedding.auto_pull: false`
/ `generation.auto_pull: false` in `config.yaml` (e.g. on a metered
connection), and pull manually instead.

Alternative: install via the macOS app from ollama.com — same result, runs
as a launchd service, less terminal management. Either way the daemon
listens on `http://localhost:11434`.

### Verify Ollama works

```sh
curl -s http://localhost:11434/api/tags | jq
# Should list nomic-embed-text under "models"

curl -s http://localhost:11434/api/embed \
  -d '{"model":"nomic-embed-text","input":["hello"]}' | jq '.embeddings[0] | length'
# Should print 768
```

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

# 2. Start the curio daemon (auto-creates ~/.curio on first run).
curio daemon start

# 3. Dry-run first to see what'd happen without actually importing.
curio import html --dry-run ~/Downloads/bookmarks.html

# 4. Import a slice incrementally to make sure your setup works.
curio import html --limit 50 --follow ~/Downloads/bookmarks.html

# 5. If happy, import the full file.
curio import html --follow ~/Downloads/bookmarks.html

# 6. Search the corpus.
curio search "feature flag rollout"

# 7. See failures (404s, paywalls, embedding errors).
curio jobs --failed

# 8. Retry failures after fixing whatever caused them.
curio refetch --all --state failed

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

## Curio itself

Once Ollama works, build curio (web2md is optional; see "Fetcher options").
The README's "Building from source" lists the Go and C toolchains it needs.

```sh
cd ~/projects/curio
make build
./bin/curio version
./bin/curio daemon start  # listens on 127.0.0.1:8765; JSON logs in ~/.curio/logs/daemon.log
curl -s http://localhost:8765/v1/healthz | jq
```

On first run the CLI (or the daemon) creates the home, `~/.curio` or
`$CURIO_HOME`: the `.curio-meta.json` marker, `content/` and `logs/`; the
daemon creates and migrates `curio.db`. A missing `config.yaml` means the
defaults. A directory that already exists without `.curio-meta.json` is
refused rather than adopted, so pointing `CURIO_HOME` at the wrong
directory can't write into it: use a path that doesn't exist yet.

The first start after an upgrade may migrate the database, which can take
a minute on a large library. `curio daemon start`, or whichever command
started the daemon, prints one line to stderr saying so and waits;
`curio daemon logs -f` shows each migration as it runs. Meanwhile
`/v1/healthz` answers 503 with a `Retry-After` header, the daemon's pid
and how many migrations are applied, and every other request gets 503
until the daemon is ready. `curio daemon status` shows the same progress.

## Config: time budgets for Ollama calls

Each bounds how long one kind of work waits on Ollama; all are validated as
positive. When a search or labeling budget runs out, the work degrades
(keyword-only results, term labels) rather than failing. An embed timeout
while indexing fails that index job, and the job queue retries it.

| Key | Default | Bounds |
|---|---|---|
| `embedding.timeout_seconds` | 60 | one embed request (the indexer sends at most 32 chunks per request) |
| `search.embed_timeout_seconds` | 10 | embedding a search query; past it, search returns keyword-only results marked `degraded`. Keep it well under 30: the CLI and MCP give up on a request after 30 s, so a hung Ollama would surface as a client timeout instead |
| `generation.timeout_seconds` | 120 | one LLM request; timeouts aren't retried |
| `insight.labeling_timeout_seconds` | 900 | all LLM labeling in one clustering run; the rest get term labels |

## Troubleshooting

**`fts5` not available** — you built without the required tags. Always use
`make build` (or pass `-tags=sqlite_fts5,sqlite_json` to `go build`).

**`vec_version()` not found** — sqlite-vec failed to load. This means cgo
wasn't enabled. Ensure `CGO_ENABLED=1`; `make` forces this.

**"model not loaded" from `/api/embed`** — Ollama answered 404: the
embedding model isn't pulled. The daemon keeps retrying the pull in the
background (see above), or run `ollama pull nomic-embed-text`. On a very old
Ollama (below 0.1.30 or so) the batched embed endpoint doesn't exist and
answers 404 too: upgrade it.

**`invalid TLS certificate: … x509: …`** on a document — the site's
certificate failed verification (expired, for another name, or from an
untrusted authority). curio won't fetch past that, not even through Jina,
and doesn't retry it. Once the site fixes its certificate, `curio refetch
<id>`. If every https document fails this way at once, something is
intercepting TLS (a captive portal, a corporate proxy) or the system clock
is badly off; fix that, then `curio refetch --all --state=failed`. A
`jina: invalid TLS certificate` error is about the certificate of
`r.jina.ai`, the fallback reader, not the site's, and is retried like any
other Jina failure.

**`dead link (redirected to another site's landing page: …)`** on a
document — the bookmark redirects to another site's homepage or section
page, which kept nothing of what the bookmark named: the usual fate of a
retired site's pages. When that page refused curio, the message starts with
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
`Retry-After`), with a warning in the daemon log for each challenged answer.
The job queue doesn't wait for the pause: it retries a document about 1, 3,
7 and 15 minutes after its first failure, five attempts in all. A retry
that comes due inside the pause fetches the page again, sends no request to
Jina, and fails at once with `jina: not sent, cooldown has … left: HTTP 429
Too Many Requests`, a 429 curio reports for its own pause, not an answer
from Jina. Each such retry uses up an attempt: with the default pause, only
the fifth attempt of the document that met the challenge can reach Jina. A
document that runs out of attempts ends `failed`; once Jina answers again,
`curio refetch --all --state=failed`.

**`ENOENT: spawn node`** from a fetch — Node isn't on the daemon's PATH.
Either install Node into a directory in PATH or set
`fetcher.web2md.node_bin` in config.
