# Setup

Curio depends on two external pieces of infrastructure at runtime:

1. **Ollama** — for embeddings (`nomic-embed-text`) and, later, local LLM
   calls. Required.
2. **web2md** — the Node.js extractor at `~/projects/experiments/web-to-markdown/`.
   Required for the v1 fetcher.

This document covers installing both on macOS. Linux / Windows users follow
the same shape but with platform-appropriate package managers.

## Ollama (native)

Install via Homebrew on macOS. The native install uses Metal acceleration on
Apple Silicon, which is noticeably faster than a containerized build.

```sh
brew install ollama
ollama serve &                    # background; or use the menu-bar app
ollama pull nomic-embed-text      # 274 MB; one-time
ollama list                       # verify
```

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

## web2md (Node)

The tool already lives in your repo at `~/projects/experiments/web-to-markdown/`.
One-time setup:

```sh
cd ~/projects/experiments/web-to-markdown
node --version            # 18+ recommended; the tool uses native fetch
npm install               # installs JSDOM, Readability, Turndown
./web2md.js https://example.com --stdout | head -20
```

The curio daemon will invoke `node /path/to/web2md.js <url> --stdout` via the
`Web2MD` fetcher. Set `fetcher.web2md.bin` in `~/.curio/config.yaml` to the
absolute path of `web2md.js`. If you ever publish a `web2md` shim to your
PATH, you can set `bin` to just `"web2md"` instead.

Example config snippet:

```yaml
fetcher:
  web2md:
    bin: "/Users/you/projects/experiments/web-to-markdown/web2md.js"
    timeout_seconds: 30
```

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
