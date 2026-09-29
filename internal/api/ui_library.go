package api

import (
	"net/http"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/ui"
)

// library answers GET /ui/library: a page of documents under the filters
// GET /v1/documents takes, parsed and checked the same way, so a Library
// URL's query is a valid /v1/documents query, each failed or dead one with
// why it failed. A filter or cursor the list can't take is a 400 page that
// offers the first page.
func (h pageHandlers) library(w http.ResponseWriter, r *http.Request) {
	opts, err := listDocumentsOpts(r)
	if err != nil {
		h.writePageError(w, r, err, ui.NavLibrary)
		return
	}
	resp, err := h.d.listDocuments(r.Context(), opts)
	if err != nil {
		h.writePageError(w, r, err, ui.NavLibrary)
		return
	}
	vm := ui.Library{Layout: h.pages.layout("Library", ui.NavLibrary), Filters: libraryFilters(opts),
		NextCursor: resp.NextCursor, PageSize: opts.Limit}
	for _, doc := range resp.Items {
		row := ui.LibraryRow{DocumentID: doc.ID, Title: deref(doc.Title), URL: doc.URL, State: doc.State,
			ContentType: doc.ContentType, UpdatedAt: doc.UpdatedAt}
		if failureCurrent(store.DocState(doc.State)) {
			row.LastError, row.FailureCause = doc.LastError, doc.FailureCause
		}
		vm.Rows = append(vm.Rows, row)
	}
	h.page(w, r, http.StatusOK, ui.PageLibrary, vm)
}

// libraryFilters are opts as the page's links carry them on: the page size
// only when it isn't the default.
func libraryFilters(opts store.ListDocumentsOpts) ui.LibraryFilters {
	f := ui.LibraryFilters{State: string(opts.State), ContentType: string(opts.ContentType), Host: opts.Host,
		Folder: opts.Folder, Cause: string(opts.Cause)}
	if opts.Limit != defaultListLimit {
		f.Limit = opts.Limit
	}
	return f
}
