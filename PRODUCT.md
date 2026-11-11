# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

The person running it first: one technical person on their own Mac (Apple silicon) who
saves pages every day in their browsers and forgets most of them. They live
between an editor, a terminal and a browser, and use curio's CLI and MCP
server as much as the dashboard. Other people who install curio from the
Homebrew tap get the same dashboard; nothing may assume knowledge of
curio's internals that the page doesn't explain.

## Product Purpose

curio reads every page the operator bookmarked, keeps a copy on their Mac and
makes all of it searchable by meaning and by words, groups the library into
named interests, and lets their AI tools search it through MCP. Bookmarks
are mostly saved as reference, not as reading lists: the dashboard is for
taking stock of what has been saved and finding it again. It exists so the
the operator finds a saved thing fast, sees what else they saved about it and what
they've been collecting lately, and keeps the library healthy without the
terminal. Success: a search finds the page they half remember, and the
library reads as their collection rather than as a job queue.

## Positioning

Everything is local: the library, the index, the models and the dashboard
run on this Mac only, served by the daemon on 127.0.0.1. It keeps the text
of pages that have since died. It is the operator's own collection, not the web.

## Operating Context

- Opened with `curio ui` (or a bookmark) on http://127.0.0.1:8765/ui/, in
  a desktop browser beside an editor and terminal; used in daytime and in
  the evening, so light and dark get equal care.
- The main job is finding a saved thing: search is the front door. Then
  reading a document, browsing interests and the interest map, and
  tending the library (failures, the queue, rebuilds) now and then.
- Libraries run to thousands of documents (the operator's: about 7,600
  bookmarks, 5,300 fetched, 32 areas, 200 interests), with a long tail of
  pages that can't be fetched (dead links, login walls, bot blocks).

## Capabilities and Constraints

- Server-rendered pages under /ui/ (Go html/template, htmx), GET only;
  every change goes through /v1 via the first-party actions.js. A strict
  CSP: no inline script or style, no remote fonts or images by default.
  The rules are pinned by tests; see CLAUDE.md "Dashboard".
- Terminology the product uses: library, bookmark, document, interest,
  area, Unsorted, map, refetch, reindex, rebuild, failure cause.
- Pages must stay fast and dense: search renders the whole page on every
  keystroke.

## Brand Commitments

- Name: curio, lowercase. Its icon is the bookmark mark in the header.
- Keep: the terminal tie-ins (IDs, markdown paths, copy-ready curio
  commands on documents); density and speed, with no decorative chrome;
  the Failures taxonomy (cause groups, host chips, one action per cause);
  the interest map's drawing and controls as they are (its placement may
  change).
- Visual direction: the category standard, played straight. No themed
  world; the craft bar is the best developer tools (Linear, Raycast):
  precise type scale, aligned grid, quiet colour that carries state.

## Evidence on Hand

A real library, served read-only by a preview daemon for
checking pages; no testimonials, customers or metrics exist and none may
be invented.

## Product Principles

1. Finding comes first: every page offers search, and the home leads with it.
2. The library is the operator's reference collection, not a pipeline: show
   what they saved before how curio processed it.
3. Be honest: say when nothing matches, why a page couldn't be read, and
   what an action will cost.
4. Plain words over internal ones; the commands stay one click away.
5. Local and quiet: nothing on the page implies a service beyond this Mac.

## Accessibility & Inclusion

WCAG 2.2 AA: keyboard-complete, visible focus at 3:1, contrast at 4.5:1
for text, both colour schemes, reduced motion honoured.
