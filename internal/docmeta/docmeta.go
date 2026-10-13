// Package docmeta derives what curio searches a document by besides its
// text: its fields (its title, the titles and folders it was saved under,
// its site and its author), the site name its host agrees on, and its
// card, the short text it is embedded by.
//
// Every function is pure: no I/O, and the same inputs give the same
// outputs under the rules RulesVersion names. The store
// (internal/store/sqlite) derives every document's rows through it, for
// every write and for the refresh at start that applies a change of the
// rules, so those rows have one implementation. The dashboard, curio
// search and curio-mcp reuse FolderText alone, to show a bookmark's folder
// as search reads it.
package docmeta

import (
	"strings"

	"github.com/samsar/curio/internal/textutil"
)

// RulesVersion names the rules every function here derives by. A change
// to any output is a new version, recorded with each derived row, so the
// store's refresh at start re-derives the rows of an older one;
// TestRulesGolden fails until it is bumped.
const RulesVersion = 1

// MaxFieldBytes caps each of a document's fields: a title, a list of
// bookmark titles or folders, a site or an author is read in full, and
// longer is noise or abuse.
const MaxFieldBytes = 2 << 10

// Bookmark is what a document was saved as: one bookmark's title and
// folder path, as stored ("/Bookmarks Bar/Tech/AI").
type Bookmark struct {
	Title      string
	FolderPath string
}

// Document is what a document's fields are derived from.
type Document struct {
	URL    string
	Title  string
	Author string
	// SiteName is the site name its current extraction's page declares
	// (og:site_name), as the fetcher read it, or empty.
	SiteName string
	// Bookmarks are its bookmarks, oldest saved first.
	Bookmarks []Bookmark
}

// Own is what a document says of itself: its fields but the site name its
// host agrees on, which takes the host's other documents. Lists are one
// entry a line, in their documents' order.
type Own struct {
	// Host is its URL's host (Host): what the host's documents agree a
	// site name across.
	Host string
	// DeclaredSite is the site name its page declares, unless it says
	// nothing (DeclaredSite).
	DeclaredSite string
	// TitleSuffixes are the distinct site names its title and its
	// bookmarks' titles end with (TitleSuffix), the title's first.
	TitleSuffixes string
	// Title is its title, whitespace collapsed.
	Title string
	// BookmarkTitles are its bookmarks' distinct titles, whitespace
	// collapsed.
	BookmarkTitles string
	// Folders are the distinct folders it was saved in (FolderText).
	Folders string
	// Author is its author, whitespace collapsed.
	Author string
}

// Fields are a document's searchable metadata, as the store keeps them.
type Fields struct {
	Own
	// SiteName is its site's name for display: the name its page
	// declares, else the one its host agrees on, else empty.
	SiteName string
	// Site is what its site is searched by: the site name and the host's
	// words (HostWords).
	Site string
}

// Derive is what d says of itself. Each field is at most MaxFieldBytes,
// cut on a rune boundary.
func Derive(d Document) Own {
	host := Host(d.URL)
	titles := make([]string, 0, len(d.Bookmarks))
	paths := make([]string, 0, len(d.Bookmarks))
	for _, b := range d.Bookmarks {
		titles = append(titles, collapse(b.Title))
		paths = append(paths, b.FolderPath)
	}
	suffixes := make([]string, 0, 1+len(titles))
	suffixes = append(suffixes, TitleSuffix(d.Title, host))
	for _, t := range titles {
		suffixes = append(suffixes, TitleSuffix(t, host))
	}
	return Own{
		Host:           host,
		DeclaredSite:   DeclaredSite(d.SiteName, host),
		TitleSuffixes:  joinDistinct(suffixes),
		Title:          field(d.Title),
		BookmarkTitles: textutil.TruncateBytes(joinDistinct(titles), MaxFieldBytes),
		Folders:        textutil.TruncateBytes(FolderText(paths), MaxFieldBytes),
		Author:         field(d.Author),
	}
}

// Fields are o's fields once its host has agreed on agreed, the site name
// AgreedName gives the host's documents: o's own declared name wins.
func (o Own) Fields(agreed string) Fields {
	name := o.DeclaredSite
	if name == "" {
		name = agreed
	}
	return Fields{Own: o, SiteName: name, Site: field(name + " " + HostWords(o.Host))}
}

// rootLabels are the names browsers give their bookmark trees' roots,
// which curio's importers start a folder path with: Chrome's and
// Firefox's, Safari's, and the reading list's identifier. A root says
// nothing about what is saved in it.
var rootLabels = []string{
	"Bookmarks Bar", "Other Bookmarks", "Mobile Bookmarks", "Favorites", "Bookmarks Menu", "Bookmarks Toolbar",
	"com.apple.ReadingList",
}

// FolderText is the folders paths name, as words: each path's segments
// joined by " / ", without its first segment when that is a browser's
// root (compared case-insensitively; a deeper folder of that name is
// kept), and the distinct paths one a line, in paths' order. A path that
// is only a root, or none, gives nothing.
func FolderText(paths []string) string {
	folders := make([]string, 0, len(paths))
	for _, p := range paths {
		var segments []string
		for s := range strings.SplitSeq(p, "/") {
			if s = collapse(s); s != "" {
				segments = append(segments, s)
			}
		}
		if len(segments) > 0 && isRootLabel(segments[0]) {
			segments = segments[1:]
		}
		folders = append(folders, strings.Join(segments, " / "))
	}
	return joinDistinct(folders)
}

func isRootLabel(segment string) bool {
	for _, root := range rootLabels {
		if strings.EqualFold(segment, root) {
			return true
		}
	}
	return false
}

// field is s with its whitespace collapsed, cut to MaxFieldBytes on a rune
// boundary.
func field(s string) string {
	return strings.TrimSpace(textutil.TruncateBytes(collapse(s), MaxFieldBytes))
}

// collapse is s with every run of whitespace one space, and none at
// either end.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// joinDistinct joins the distinct non-empty values, one a line, in order.
func joinDistinct(values []string) string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return strings.Join(out, "\n")
}

// lines are the entries of a one-a-line list, none for an empty one.
func lines(list string) []string {
	if list == "" {
		return nil
	}
	return strings.Split(list, "\n")
}
