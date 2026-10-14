package sqlite

import (
	"strings"
	"time"

	"github.com/samsar/curio/internal/store"
)

// A search's filters are predicates on its documents, aliased d by every
// query that takes them, each dimension one indexed seek per document: a
// bookmark's of idx_bookmarks_document, a document's fields' by their
// document. Values within a dimension are OR'd, and dimensions AND'd.

// buildFilterClause builds the AND-prefixed WHERE conditions and bind args
// that scope a search to f. Returns ("", nil) for an empty filter, so an
// unfiltered search's SQL is the same with or without the filters.
func buildFilterClause(f store.SearchFilters) (string, []any) {
	if f.IsEmpty() {
		return "", nil
	}
	var c filterClause
	c.nonDates(f)
	if f.HasDates() {
		cond, args := datesPredicate(f, f.IncludeUndated)
		c.and(cond, args...)
	}
	if f.ExcludeDocumentID != "" {
		// Alone, it doesn't narrow (Narrows), so a KNN list applies it
		// after sqlite-vec's k cutoff, which the list's over-fetch covers.
		c.and("d.id != ?", f.ExcludeDocumentID)
	}
	return c.sql.String(), c.args
}

// documentTenantSQL is the tenant's predicate on d, its argument the
// tenant, for a document list that reaches d by primary key from what it
// searches (the fields' MATCH, the cards' KNN). Under a narrowing
// filter's predicates SQLite (curio never runs ANALYZE) takes the
// tenant's documents to be few enough to walk, searching the list's index
// once for each of them. So the tenant is then checked on the document
// found (+d.tenant_id), which keeps the list's own index driving; a filter
// that doesn't narrow keeps the SQL it had. The chunk lists' state
// predicate (searchedDocSQL) keeps them driven by their index already.
func documentTenantSQL(f store.SearchFilters) string {
	if f.Narrows() {
		return "+d.tenant_id = ?"
	}
	return "d.tenant_id = ?"
}

// withoutDates is f without its date dimensions.
func withoutDates(f store.SearchFilters) store.SearchFilters {
	f.SavedAfter, f.SavedBefore = time.Time{}, time.Time{}
	f.PublishedAfter, f.PublishedBefore = time.Time{}, time.Time{}
	f.IncludeUndated = false
	return f
}

// filterClause accumulates predicates, each ANDed onto the caller's
// WHERE, and their arguments.
type filterClause struct {
	sql  strings.Builder
	args []any
}

func (c *filterClause) and(cond string, args ...any) {
	c.sql.WriteString(" AND " + cond)
	c.args = append(c.args, args...)
}

// nonDates adds every dimension of f but its dates and the excluded
// document.
func (c *filterClause) nonDates(f store.SearchFilters) {
	if len(f.ContentType) > 0 {
		c.and("d.content_type IN ("+placeholders(len(f.ContentType))+")", appendArgs(nil, f.ContentType)...)
	}
	if len(f.Source) > 0 {
		c.and("EXISTS (SELECT 1 FROM bookmarks b"+
			" WHERE b.document_id = d.id AND b.tenant_id = d.tenant_id"+
			" AND b.source IN ("+placeholders(len(f.Source))+"))", appendArgs(nil, f.Source)...)
	}
	if len(f.Host) > 0 {
		conds := make([]string, 0, len(f.Host))
		var args []any
		for _, h := range f.Host {
			cond, condArgs := hostPredicate("d.url", h)
			conds = append(conds, cond)
			args = append(args, condArgs...)
		}
		c.and("("+strings.Join(conds, " OR ")+")", args...)
	}
	if len(f.Site) > 0 {
		cond, args := sitePredicate(f.Site)
		c.and(cond, args...)
	}
	if len(f.Folder) > 0 {
		cond, args := folderFilter(f.Folder)
		c.and(cond, args...)
	}
}

// sitePredicate is the condition that d's fields' host, lowercased
// without its port or "www." (docmeta.Host), is one of sites or a
// subdomain of one, and its arguments. A site's '%', '_' and '\' match
// only themselves.
func sitePredicate(sites []string) (string, []any) {
	conds := make([]string, 0, len(sites))
	args := make([]any, 0, 2*len(sites))
	for _, site := range sites {
		conds = append(conds, `sf.host = ? OR sf.host LIKE ? ESCAPE '\'`)
		args = append(args, site, "%."+escapeLike(site))
	}
	return "EXISTS (SELECT 1 FROM document_fields sf WHERE sf.document_id = d.id AND (" +
		strings.Join(conds, " OR ") + "))", args
}

// folderFilter is the condition that d has a bookmark in one of folders or
// under it (folderPredicate), and its arguments. Every folder names one
// (SearchFilters.Validate).
func folderFilter(folders []string) (string, []any) {
	conds := make([]string, 0, len(folders))
	var args []any
	for _, folder := range folders {
		cond, condArgs, ok := folderPredicate("fb.folder_path", folder)
		if !ok {
			continue
		}
		conds = append(conds, cond)
		args = append(args, condArgs...)
	}
	return "EXISTS (SELECT 1 FROM bookmarks fb WHERE fb.document_id = d.id AND fb.tenant_id = d.tenant_id AND (" +
		strings.Join(conds, " OR ") + "))", args
}

// datesPredicate is the condition that d passes every date dimension f
// sets, and its arguments: a saved or published date in the window, or,
// with includeUndated, no date the dimension can judge.
func datesPredicate(f store.SearchFilters, includeUndated bool) (string, []any) {
	var (
		conds []string
		args  []any
	)
	add := func(window string, windowArgs []any, undated string, undatedArgs []any) {
		if includeUndated {
			window = "(" + window + " OR " + undated + ")"
			windowArgs = append(windowArgs, undatedArgs...)
		}
		conds = append(conds, window)
		args = append(args, windowArgs...)
	}
	if f.HasSavedWindow() {
		window, windowArgs := savedWindow(f)
		add(window, windowArgs, savedUndatedSQL, []any{store.SavedDateUnknown})
	}
	if f.HasPublishedWindow() {
		window, windowArgs := publishedWindow(f)
		untrusted, untrustedArgs := untrustedPublished()
		add(window, windowArgs, "(d.published_at IS NULL OR "+untrusted+")", untrustedArgs)
	}
	return strings.Join(conds, " AND "), args
}

// savedWindow is the condition that d has a bookmark of a known date,
// exact or approx, in f's saved window, and its arguments: one seek of
// the document's bookmarks in idx_bookmarks_document.
func savedWindow(f store.SearchFilters) (string, []any) {
	cond := "EXISTS (SELECT 1 FROM bookmarks sb WHERE sb.document_id = d.id AND sb.tenant_id = d.tenant_id" +
		" AND sb.saved_date <> ?"
	args := []any{store.SavedDateUnknown}
	if !f.SavedAfter.IsZero() {
		cond += " AND sb.saved_at >= ?"
		args = append(args, formatTime(f.SavedAfter))
	}
	if !f.SavedBefore.IsZero() {
		cond += " AND sb.saved_at < ?"
		args = append(args, formatTime(f.SavedBefore))
	}
	return cond + ")", args
}

// savedUndatedSQL is the condition that d has no bookmark of a known
// date, which a saved window can't judge. Its argument is
// store.SavedDateUnknown.
const savedUndatedSQL = "NOT EXISTS (SELECT 1 FROM bookmarks ub WHERE ub.document_id = d.id" +
	" AND ub.tenant_id = d.tenant_id AND ub.saved_date <> ?)"

// publishedWindow is the condition that d's published date is in f's
// published window and trusted, and its arguments. It is false, never
// NULL, for a document with no published date, so CountUndated can negate
// it.
func publishedWindow(f store.SearchFilters) (string, []any) {
	conds := []string{"d.published_at IS NOT NULL"}
	var args []any
	if !f.PublishedAfter.IsZero() {
		conds = append(conds, "d.published_at >= ?")
		args = append(args, formatTime(f.PublishedAfter))
	}
	if !f.PublishedBefore.IsZero() {
		conds = append(conds, "d.published_at < ?")
		args = append(args, formatTime(f.PublishedBefore))
	}
	untrusted, untrustedArgs := untrustedPublished()
	conds = append(conds, "NOT "+untrusted)
	return "(" + strings.Join(conds, " AND ") + ")", append(args, untrustedArgs...)
}

// untrustedPublished is the trust rule of a published date, the only
// definition of it: d's published date is untrusted when it falls more
// than a day after its earliest exact save, since a page saved before it
// was published was modified then, not published. A date at most a day
// after the earliest exact save is trusted, and so is one of a document
// with no exact save. It returns the condition that d's is untrusted, and
// its arguments.
func untrustedPublished() (string, []any) {
	const untrusted = "EXISTS (SELECT 1 FROM bookmarks pb WHERE pb.document_id = d.id AND pb.tenant_id = d.tenant_id" +
		" AND pb.saved_date = ? AND pb.saved_at < strftime('%Y-%m-%dT%H:%M:%fZ', d.published_at, '-1 day'))"
	return untrusted, []any{store.SavedDateExact}
}
