package ask

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/samsar/curio/internal/docmeta"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
)

// readDepth is how many of the ranking's first documents an answer's
// sources are found among: the reranker's whole head, so a reranked
// search's order is read as reranked.
const readDepth = 30

// maxUnreadable is how many saved pages curio couldn't read an answer
// lists beside its sources.
const maxUnreadable = 3

// Source is a page an answer is written from, numbered as the answer cites
// it: its document, URL and title (its own, else the first it was saved
// as, else its host), the other titles it was saved as, its site, the day
// it was first saved (YYYY-MM-DD, "about" before it when the day is
// approximate, empty when unknown), its summary's about line, for an
// evidence source the quote that shows its side, and its passages, as the
// prompt showed them. OwnTitle and SavedTitle are what a list elsewhere
// names it by (Names), which no prompt reads.
type Source struct {
	N        int
	DocID    string
	URL      string
	Title    string
	SavedAs  []string
	Site     string
	SavedOn  string
	About    string
	Quote    string
	Passages []Passage
	Names

	// side is an evidence source's: supports or contradicts.
	side string
	// candidates are the chunks its passages are chosen from, best first.
	candidates []candidate
}

// shown is s as an event reports it: what the prompt showed, without what
// it was chosen from.
func (s Source) shown() Source {
	s.candidates = nil
	return s
}

// Unreadable is a page the search found that curio couldn't read, so no
// answer is written from it: often the very page a person means, worth
// naming beside the sources. Reason says why (docmeta.Unavailable).
type Unreadable struct {
	DocID  string
	URL    string
	Title  string
	State  store.DocState
	Reason string
	Names
}

// Names are what the dashboard's lists name a document by, besides its
// URL: its own title (documents.title, "" for none), and, while it has
// none, the title it was last saved under (savedTitle). A Title, which
// falls back to the first title it was saved as, then its host, is the
// prompt's; these are the pages', so an answer's sources read as Search
// and the Library name them.
type Names struct {
	OwnTitle   string
	SavedTitle string
}

// namesOf are doc's Names, its saved title read from its bookmarks.
func namesOf(doc *store.Document, bookmarks []*store.Bookmark) Names {
	own := deref(doc.Title)
	return Names{OwnTitle: own, SavedTitle: savedTitle(own, bookmarks)}
}

// asciiSpace is what a bookmark's title may hold and still be blank, as
// the store reads it for an untitled document's name: ASCII whitespace.
const asciiSpace = " \t\n\v\f\r"

// savedTitle is the title an untitled document is named by: its most
// recently saved bookmark's (the latest saved_at, then the highest ID, as
// the store's ListByDocuments orders them) whose title isn't blank of
// ASCII whitespace, trimmed as the search page trims it; "" when own, its
// own title, names it, or no bookmark has a title. A title of other
// spaces alone is chosen, as the store chooses it, and trims to "".
func savedTitle(own string, bookmarks []*store.Bookmark) string {
	if own != "" {
		return ""
	}
	var newest *store.Bookmark
	for _, b := range bookmarks {
		if strings.Trim(deref(b.Title), asciiSpace) == "" {
			continue
		}
		if newest == nil || b.SavedAt.After(newest.SavedAt) || b.SavedAt.Equal(newest.SavedAt) && b.ID > newest.ID {
			newest = b
		}
	}
	if newest == nil {
		return ""
	}
	return strings.TrimSpace(*newest.Title)
}

// reading is what an ask read of the ranking's first hits besides the
// search: their fields, summaries and bookmarks, and each one's passage
// candidates.
type reading struct {
	hits       []search.Hit
	fields     map[string]store.DocumentFields
	summaries  map[string]*store.Summary
	bookmarks  map[string][]*store.Bookmark
	candidates map[string][]candidate
}

// read reads hits, the ranking's first readDepth, once each: their
// fields, summaries and bookmarks, and the passage candidates of those
// curio could read, ranked by vector, the question's.
func (a *Asker) read(ctx context.Context, vector []float32, hits []search.Hit) (reading, error) {
	r := reading{hits: hits[:min(len(hits), readDepth)]}
	if len(r.hits) == 0 {
		return r, nil
	}
	ids := make([]string, len(r.hits))
	readable := make([]search.Hit, 0, len(r.hits))
	for i, h := range r.hits {
		ids[i] = h.Document.ID
		if !unreadable(h.Document) {
			readable = append(readable, h)
		}
	}
	tenant := a.stores.TenantID
	var err error
	if r.fields, err = a.stores.Fields.FieldsOf(ctx, tenant, ids); err != nil {
		return reading{}, fmt.Errorf("read the sources' fields: %w", err)
	}
	if r.summaries, err = a.stores.summaries().CurrentSummaries(ctx, tenant, ids); err != nil {
		return reading{}, fmt.Errorf("read the sources' summaries: %w", err)
	}
	if r.bookmarks, err = a.stores.Bookmarks.ListByDocuments(ctx, tenant, ids); err != nil {
		return reading{}, fmt.Errorf("read the sources' bookmarks: %w", err)
	}
	r.candidates, err = a.candidates(ctx, vector, readable, documentContexts(readable, r.fields, r.bookmarks))
	if err != nil {
		return reading{}, err
	}
	return r, nil
}

// unreadable reports whether curio couldn't read doc: no text of it backs
// a claim.
func unreadable(doc *store.Document) bool {
	return doc.State == store.DocStateFailed || doc.State == store.DocStateDead
}

// sources picks up to k sources from what r read, the ranking's first in
// their order: a failed or dead page goes among the unreadable (at most
// maxUnreadable), never a source; a page with neither a passage candidate
// nor a summary is passed over; any other is the next source, until k.
func (a *Asker) sources(r reading, k int) ([]Source, []Unreadable) {
	var out []Source
	var unread []Unreadable
	for _, h := range r.hits {
		if len(out) == k {
			break
		}
		doc := h.Document
		switch summary := r.summaries[doc.ID]; {
		case unreadable(doc):
			if len(unread) < maxUnreadable {
				title, _ := titles(doc, r.fields[doc.ID])
				unread = append(unread, Unreadable{DocID: doc.ID, URL: doc.URL, Title: title, State: doc.State,
					Reason: docmeta.Unavailable(string(doc.State)), Names: namesOf(doc, r.bookmarks[doc.ID])})
			}
		case len(r.candidates[doc.ID]) == 0 && summary == nil:
		default:
			out = append(out, a.source(r, doc, len(out)+1))
		}
	}
	return out, unread
}

// source is doc as source n, from what r read.
func (a *Asker) source(r reading, doc *store.Document, n int) Source {
	f := r.fields[doc.ID]
	title, saved := titles(doc, f)
	s := Source{N: n, DocID: doc.ID, URL: doc.URL, Title: title, SavedAs: saved,
		Site: docmeta.SiteOrHost(f.SiteName, hostOf(doc, f)), SavedOn: a.savedOn(r.bookmarks[doc.ID]),
		Names: namesOf(doc, r.bookmarks[doc.ID]), candidates: r.candidates[doc.ID]}
	if summary := r.summaries[doc.ID]; summary != nil {
		s.About = summary.About
	}
	return s
}

// titles are doc's title as a source names it, its own, else the first it
// was saved as, else its host, and the other titles it was saved as.
func titles(doc *store.Document, f store.DocumentFields) (string, []string) {
	title := f.Title
	if title == "" && doc.Title != nil {
		title = *doc.Title
	}
	saved := docmeta.SavedTitles(title, f.BookmarkTitles)
	switch {
	case title != "":
	case len(saved) > 0:
		title, saved = saved[0], saved[1:]
	default:
		title = hostOf(doc, f)
	}
	return title, saved
}

// hostOf is doc's host, from its fields or, not derived yet, its URL.
func hostOf(doc *store.Document, f store.DocumentFields) string {
	if f.Host != "" {
		return f.Host
	}
	return docmeta.Host(doc.URL)
}

// savedOn is the day a document was first saved, among its bookmarks
// whose saved date is exact or approximate, in the asker's location:
// "about" before it when that bookmark's is approximate; empty when no
// bookmark's date is known.
func (a *Asker) savedOn(bookmarks []*store.Bookmark) string {
	var first *store.Bookmark
	for _, b := range bookmarks {
		known := b.SavedDate == store.SavedDateExact || b.SavedDate == store.SavedDateApprox
		if known && (first == nil || b.SavedAt.Before(first.SavedAt)) {
			first = b
		}
	}
	if first == nil {
		return ""
	}
	day := first.SavedAt.In(a.loc).Format(time.DateOnly)
	if first.SavedDate == store.SavedDateApprox {
		return "about " + day
	}
	return day
}
