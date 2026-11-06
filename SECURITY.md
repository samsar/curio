# Security

curio runs a background service on your Mac, fetches pages from the web,
and shows their text in a dashboard in your browser. If you find a way to
abuse any of that, please tell me privately before telling anyone else.

## Reporting a vulnerability

Report it through GitHub's private vulnerability reporting:
**[Report a vulnerability](https://github.com/samsar/curio/security/advisories/new)**
(the Security tab, then "Report a vulnerability"). Please don't open a
public issue, pull request or discussion for it.

Include what you can of:

- what an attacker can do, and what they need first (a page you bookmark,
  a site you visit while curio runs, a process on your Mac, …);
- the steps to reproduce it, or a proof of concept;
- `curio version` and your macOS version.

curio is maintained by one person, in their spare time. I'll acknowledge
your report as soon as I can, keep you posted while I fix it, and credit
you in the advisory and release notes unless you'd rather not be named.

## Supported versions

Fixes go into the latest release only. Upgrade with
`brew upgrade curio`, then `curio up`.

## What's in scope

- **The local API and dashboard** on `127.0.0.1:8765`: it has no login and
  trusts processes on your Mac, but it must refuse other websites (DNS
  rebinding, cross-origin requests, a page driving the dashboard's
  actions). See `docs/decisions.md`, "Local API".
- **The dashboard's rendering of stored pages**: page text is markdown from
  the web, sanitized before it is shown. Script running in the dashboard,
  or a stored page that hangs or exhausts it, is in scope.
- **Fetching**: a bookmarked page or a redirect that makes curio reach
  something it shouldn't (another machine on your network, a local file),
  or that crashes or wedges the daemon.
- **The importers**: a crafted bookmarks file (HTML, Chrome, Safari,
  Firefox) that does more than add bookmarks.
- **`curio-mcp`**: a tool call that reaches beyond the library.
- **The release**: the Homebrew formula and the binaries it installs.

## What isn't

- Anything that needs your user account already (it can read `~/.curio`
  directly).
- Bugs in Ollama, Jina Reader, yt-dlp or the sites curio fetches; report
  those upstream. How curio *uses* them is in scope.
- What a page says. curio stores what sites serve and doesn't vouch for it.
