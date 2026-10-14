package indexer

import (
	"strings"

	"github.com/samsar/curio/internal/docmeta"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/textutil"
)

// ContextVersion names the rules Context and the chunker's headings
// (Chunk.Headings) build a chunk's context by. A change to any output is a
// new version, stored with every chunk embedded under it, so the daemon's
// start re-embeds every document holding chunks of an older one;
// TestContextGolden fails until it is bumped.
const ContextVersion = 1

// A context's parts are each bounded, so a context is at most
// maxContextBytes: with a chunk of at most the chunker's 3,500-byte cap, a
// byte-level tokenizer like Qwen3-Embedding's takes under 4,000 tokens,
// inside the embedder's num_ctx of 8,192.
const (
	maxContextTitleBytes = 200
	maxContextSiteBytes  = 120
	maxContextPathBytes  = 120
	maxContextBytes      = maxContextTitleBytes + maxContextSiteBytes + maxContextPathBytes + 2*len(contextSeparator)
	contextSeparator     = " — "
	pathSeparator        = " › "
)

// titleSeparators are what a title puts between a heading it repeats and
// the rest of it: "Why queues fail | Example Times".
var titleSeparators = []string{" | ", " – ", " — ", " - ", " · ", ": "}

// DocumentContext is what a document's chunks' context is built from.
type DocumentContext struct {
	URL string
	// Title is the document's own title as stored, "" when it has none.
	Title string
	// Bookmarks are its bookmarks, newest saved first: what names an
	// untitled document.
	Bookmarks []docmeta.Bookmark
	// Site is its site as its search fields hold it; nil when it has no
	// fields row.
	Site *store.DocumentSite
}

// Context is a chunk's context, what it is embedded with before its text,
// so its vector knows what the chunk is part of: the document's title,
// its site and the headings the chunk sits under (Chunk.Headings), each
// trimmed, those that are empty dropped, the rest joined by " — ":
//
//   - the title is the document's, whitespace collapsed, else the title
//     it was saved as (docmeta.SavedAs: its newest titled bookmark), cut
//     to 200 bytes on a rune boundary;
//   - the site is its fields' site name, else their host (docmeta.
//     SiteOrHost), else, without a fields row, its URL's host
//     (docmeta.Host), cut to 120 bytes;
//   - the heading path is the nearest of the headings that don't repeat
//     the title or the site (repeats), preceded by its parent and " › "
//     when both fit in 120 bytes; a nearest heading longer is cut to 120
//     bytes.
func Context(doc DocumentContext, headings []string) string {
	title := collapse(doc.Title)
	if title == "" {
		saved, _ := docmeta.SavedAs(doc.Bookmarks)
		title = saved.Title
	}
	site := docmeta.Host(doc.URL)
	if doc.Site != nil {
		site = docmeta.SiteOrHost(doc.Site.Name, doc.Site.Host)
	}
	parts := make([]string, 0, 3)
	for _, p := range []string{cut(title, maxContextTitleBytes), cut(site, maxContextSiteBytes),
		headingPath(headings, title, site)} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, contextSeparator)
}

// headingPath is the path of the headings a chunk sits under, whitespace
// collapsed, leaving out those that are empty or repeat title or site: the
// nearest, after its parent when both fit in maxContextPathBytes.
func headingPath(headings []string, title, site string) string {
	var kept []string
	for _, h := range headings {
		if h = collapse(h); h != "" && !repeats(h, title, site) {
			kept = append(kept, h)
		}
	}
	if len(kept) == 0 {
		return ""
	}
	nearest := kept[len(kept)-1]
	if len(kept) > 1 {
		if path := kept[len(kept)-2] + pathSeparator + nearest; len(path) <= maxContextPathBytes {
			return path
		}
	}
	return cut(nearest, maxContextPathBytes)
}

// repeats reports whether heading says again what the title or the site
// says, compared case-insensitively with whitespace collapsed: it equals
// either, or the title begins with it and a separator (titleSeparators).
// Half the documents with an H1 repeat their title in it.
func repeats(heading, title, site string) bool {
	h, t := fold(heading), fold(title)
	if h == t || h == fold(site) {
		return true
	}
	for _, sep := range titleSeparators {
		if strings.HasPrefix(t, h+sep) {
			return true
		}
	}
	return false
}

// fold is s lowercased with its whitespace collapsed.
func fold(s string) string { return strings.ToLower(collapse(s)) }

// collapse is s with every run of whitespace one space, and none at either
// end.
func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// cut is s cut to n bytes on a rune boundary, trimmed.
func cut(s string, n int) string { return strings.TrimSpace(textutil.TruncateBytes(s, n)) }
