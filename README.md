# Curio

**Search everything you've ever bookmarked, and let your AI tools use it too.**

Your accumulated curiosity is invisible to the tools you think with. You save pages every day and forget most of them. Curio reads every page you've bookmarked, keeps a copy on your Mac, and makes all of it searchable. Further, through its MCP server, it lets your agent look through your library so your questions get answers grounded in what you've actually read.

Everything runs on your Mac: your library, the search, and the AI models.

## What you can do with it

- **Find that article again.** `curio search "rolling out feature flags"` searches the full text of every saved page, by meaning, not just keywords.
- **Ask your agent about your reading.** Connect Curio to your favorite agent then ask things like *"what have I saved about pricing experiments?"*
- **See what you're into.** `curio interests` groups your library into topics and names them.
- **Find related reads.** `curio related <id>` shows other pages about the same thing.
- **Keep old links useful.** Pages are saved when Curio fetches them, so you still have the text of bookmarks that have since gone dead.

## Get started

You need a Mac with Apple silicon (M1 or later), macOS 14 or newer, and [Homebrew](https://brew.sh). 16 GB of memory or more is best, though 8 GB works. You'll also need about 5–25 GB of free disk, depending on your Mac.

```sh
brew install samsar/tap/curio
curio up
```

That's it. `curio up` walks you through the rest and asks before each step:

1. It checks that your Mac can run AI models locally.
2. It installs [Ollama](https://ollama.com), the app that runs those models, if you don't have it yet.
3. It picks the right models for your Mac, tells you why, and downloads them.
4. It sets Curio up to keep running in the background, even after a restart.
5. It asks where to import your bookmarks from: Chrome, Safari, Firefox, or an exported file. It shows how many are new and how long they'll take.

A few thousand bookmarks takes a couple of hours to fetch and index. Curio tells you when to check back, and you can search while it works.

Run `curio up` again whenever you like. If everything's fine, it says **"Nothing to do: curio is up."**

## Everyday commands

```sh
curio search "what you remember about it"   # search your library
curio status --follow                       # watch an import until it's done
curio ui                                    # open the dashboard in your browser
curio interests                             # the topics in your library
curio add https://example.com/article       # save one page
curio import safari                         # import more bookmarks later (also: chrome, firefox, html <file>)
```

To keep your Mac comfortable during a big import:

```sh
curio pause | resume            # stop starting new work, then pick up again
curio throttle gentle           # do less at once, so the fans stay quiet
curio schedule 22:00-07:00      # only work overnight (curio schedule off to undo)
curio keep-awake on             # stay awake while importing, when plugged in
```

If something seems off, run `curio doctor`: it checks every part and says what to fix.

## Use it with Claude

```sh
claude mcp add curio -- curio-mcp
```

Then ask Claude Code about anything you've saved. Claude can search your library, open a saved page, find related pages, and see your interests. For Claude Desktop, see [docs/mcp.md](./docs/mcp.md).

## Good to know

- **Privacy.**
  - Your library, the search, and the AI models all live on your Mac.
  - Curio goes online to download the pages you bookmarked.
  - For pages that block it, Curio can ask [Jina Reader](https://jina.ai/reader) to fetch the page; that service sees the page's address. To turn this off, set `fetcher.native.jina_fallback: false` in `~/.curio/config.yaml`. Create your own Jina account if you get rate limited and use that.
- **Changing the writing model.** Curio uses a local model to name your interests. To use a different one, set `generation.model` in `~/.curio/config.yaml` and run `curio up`.
- **GitHub pages.** Without a GitHub token, GitHub allows only 60 requests an hour, about 30 repositories, so github.com bookmarks wait for its hourly limit and a library with many of them takes hours. Any token works, even one with no permissions at all: set `fetcher.github.token` in the same file. `curio doctor` warns when there isn't one.
- **Upgrading.** Run `brew upgrade curio`, then `curio up`.
- **Starting over.** `curio up --fresh` moves your current library aside to `~/.curio.bak-<date>` and starts a new one. Nothing is deleted.
- **Uninstalling.** Run `curio daemon uninstall`, then `brew uninstall curio`. Your library stays in `~/.curio` until you delete it.

## More

- [Setup and troubleshooting](./docs/setup.md): every detail of `curio up`, and setting up by hand
- [Using Curio with Claude (MCP)](./docs/mcp.md)
- [How it works](./docs/architecture.md), [design decisions](./docs/decisions.md), and the [roadmap](./docs/roadmap.md)

To build from source, clone the repo and run `make build` (Go and the Xcode Command Line Tools required; `make help` lists everything).

## License

Apache License 2.0; see [LICENSE](./LICENSE).
