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

**`ENOENT: spawn node`** from a fetch — Node isn't on the daemon's PATH.
Either install Node into a directory in PATH or set
`fetcher.web2md.node_bin` in config.
