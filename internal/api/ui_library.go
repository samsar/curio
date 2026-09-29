package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/ui"
)

// maxShown bounds the Library's shown, the rows "load more" says the page
// already holds: more than any library has.
const maxShown = 1_000_000

// library answers GET /ui/library: a page of the library in its order.
// Last updated, the default, lists documents under the filters GET
// /v1/documents takes, parsed and checked the same way, so a Library URL's
// query is a valid /v1/documents query. Date saved (order=saved) lists
// saves under the same filters, through GET /v1/bookmarks' order=saved
// and its parsing, source aside: the Library has no source filter. Each
// failed or dead row says why it failed. An order, filter or cursor the
// list can't take, a cursor of the other order among them, is a 400 page
// that offers the first page. The library's counts label the state tabs,
// the lede and the subnav's Failures tab; the page does without them when
// they can't be read.
//
// shown, which htmx's "load more" sends, is how many rows the page it
// appends to holds, for the Showing line alone: a keyset cursor can't tell
// how many rows came before it. It is display only, never a filter, and
// anything but a count in range is 0.
func (h pageHandlers) library(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	order, err := libraryOrder(r)
	if err != nil {
		h.writePageError(w, r, err, ui.NavLibrary)
		return
	}
	var vm ui.Library
	if order == ui.OrderSaved {
		vm, err = h.librarySaves(ctx, r)
	} else {
		vm, err = h.libraryDocuments(ctx, r)
	}
	if err != nil {
		h.writePageError(w, r, err, ui.NavLibrary)
		return
	}
	vm.Layout = h.pages.layout("Library", ui.NavLibrary)
	vm.Shown = intQuery(r, "shown", 0, 0, maxShown)
	vm.Counts = h.libraryCounts(r)
	h.page(w, r, http.StatusOK, ui.PageLibrary, vm)
}

// libraryCounts reads the whole library's counts, which head both of the
// Library's views: the lede, the Failures tab's count in the subnav, and
// on the Documents view the state tabs. nil when they can't be read,
// which the pages do without.
func (h pageHandlers) libraryCounts(r *http.Request) *ui.LibraryCounts {
	st, err := h.d.stats(r.Context())
	if err != nil {
		h.quietError(r, err)
		return nil
	}
	return &ui.LibraryCounts{Documents: st.DocumentsTotal, Bookmarks: st.BookmarksTotal,
		ByState: st.DocumentsByState}
}

// libraryOrder reads the Library's ?order: "" for Last updated (absent,
// empty or "updated", which the form sends), or ui.OrderSaved. Anything
// else is a requestError.
func libraryOrder(r *http.Request) (string, error) {
	switch order := r.URL.Query().Get("order"); order {
	case "", ui.OrderUpdated:
		return "", nil
	case ui.OrderSaved:
		return order, nil
	default:
		return "", badRequest("order %q must be one of: %s, %s", order, ui.OrderUpdated, ui.OrderSaved)
	}
}

// libraryDocuments is the Library in the Last updated order: a page of
// documents, most recently updated first, as GET /v1/documents lists
// them. An untitled one is named by its bookmark's title.
func (h pageHandlers) libraryDocuments(ctx context.Context, r *http.Request) (ui.Library, error) {
	opts, err := listDocumentsOpts(r)
	if err != nil {
		return ui.Library{}, err
	}
	resp, err := h.d.listDocuments(ctx, opts)
	if err != nil {
		return ui.Library{}, err
	}
	vm := ui.Library{Filters: ui.LibraryFilters{State: string(opts.State), ContentType: string(opts.ContentType),
		Host: opts.Host, Folder: opts.Folder, Cause: string(opts.Cause), Limit: linkLimit(opts.Limit)},
		NextCursor: resp.NextCursor, PageSize: opts.Limit}
	for _, doc := range resp.Items {
		row := ui.LibraryRow{DocumentID: doc.ID, Title: deref(doc.Title),
			BookmarkTitle: strings.TrimSpace(doc.BookmarkTitle), URL: doc.URL, State: doc.State,
			ContentType: doc.ContentType, When: doc.UpdatedAt}
		if failureCurrent(store.DocState(doc.State)) {
			row.LastError, row.FailureCause = doc.LastError, doc.FailureCause
		}
		vm.Rows = append(vm.Rows, row)
	}
	return vm, nil
}

// librarySaves is the Library in the Date saved order: a page of saves,
// newest saved first, as GET /v1/bookmarks?order=saved lists them, each
// with its document. A row is a save, so an untitled document is named
// by that bookmark's own title.
func (h pageHandlers) librarySaves(ctx context.Context, r *http.Request) (ui.Library, error) {
	opts, err := bookmarksPageOpts(r, store.BookmarkOrderSaved)
	if err != nil {
		return ui.Library{}, err
	}
	resp, err := h.d.listBookmarks(ctx, opts)
	if err != nil {
		return ui.Library{}, err
	}
	vm := ui.Library{Filters: ui.LibraryFilters{Order: ui.OrderSaved, State: string(opts.State),
		ContentType: string(opts.ContentType), Host: opts.Host, Folder: opts.FolderPath, Cause: string(opts.Cause),
		Limit: linkLimit(opts.Limit)}, NextCursor: resp.NextCursor, PageSize: opts.Limit}
	for _, b := range resp.Items {
		row := ui.LibraryRow{DocumentID: deref(b.DocumentID), Title: deref(b.DocumentTitle),
			BookmarkTitle: strings.TrimSpace(deref(b.Title)), URL: b.URL, State: b.DocumentState,
			ContentType: b.DocumentContentType, When: b.SavedAt, Source: b.Source, Folder: deref(b.FolderPath)}
		if failureCurrent(store.DocState(b.DocumentState)) {
			row.LastError, row.FailureCause = b.DocumentLastError, b.DocumentFailureCause
		}
		vm.Rows = append(vm.Rows, row)
	}
	return vm, nil
}

// linkLimit is a page size as the Library's links carry it on: 0, which
// they leave out, for the default.
func linkLimit(limit int) int {
	if limit == defaultListLimit {
		return 0
	}
	return limit
}
