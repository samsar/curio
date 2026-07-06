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

You can skip the `ollama pull` step: as long as Ollama itself is running, the
daemon **auto-pulls** the models it needs on startup — the embedding model
(`nomic-embed-text`) and, when insight labeling is on, the generation model
(`llama3.2`, ~2 GB). It fetches them in the background, so the first index /
clustering run may lag until the download finishes. Disable with
`embedding.auto_pull: false` / `generation.auto_pull: false` in `config.yaml`
(e.g. on a metered connection), and pull manually instead.

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

Time budget: with 4 workers (default) and the Native fetcher, expect roughly
1-2 seconds per bookmark — so 1000 bookmarks ≈ 4-8 minutes wall-clock. Larger
files scale linearly. Use `--limit` to test in chunks.

## Curio itself

Once Ollama and web2md work, build curio:

```sh
cd ~/projects/curio
make build
./bin/curio version
./bin/curio-daemon &      # starts on :8765; structured JSON logs to stderr
curl -s http://localhost:8765/v1/healthz | jq
```

The daemon will refuse to start if `~/.curio` exists without our marker
file. First-run initialization is handled by the CLI (forthcoming). For
now, an empty `~/.curio` works fine — the daemon's storage layer will
create the database on first connection.

## Troubleshooting

**`fts5` not available** — you built without the required tags. Always use
`make build` (or pass `-tags=sqlite_fts5,sqlite_json` to `go build`).

**`vec_version()` not found** — sqlite-vec failed to load. This means cgo
wasn't enabled. Ensure `CGO_ENABLED=1`; `make` forces this.

**Ollama 404 on `/api/embed`** — your Ollama is too old. The batched embed
endpoint was added in 2024. Run `ollama --version`; upgrade if below
0.1.30 or so.

**`ENOENT: spawn node`** from a fetch — Node isn't on the daemon's PATH.
Either install Node into a directory in PATH or set
`fetcher.web2md.node_bin` in config (planned, not yet wired).
