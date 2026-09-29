package api

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/samsar/curio/internal/ui"
)

// relatedOnDocumentPage is how many related documents a document's page
// lists.
const relatedOnDocumentPage = 5

// document answers GET /ui/documents/{id}: the document's metadata and
// current extraction, its rendered text, related documents and bookmarks,
// and, while it is failed or dead, why it failed and its last error. The
// text, related and bookmarks panels read on their own, and one that fails
// shows its error while the rest renders.
//
// Stored pages' remote images are off: each is its alt text, linking to
// it. ?images=1, or ui.load_remote_images, shows the https ones, and only
// that answer's CSP allows https images.
func (h pageHandlers) document(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := chi.URLParam(r, "id")
	doc, err := h.d.Documents.GetWithLastError(ctx, h.d.TenantID, id)
	if err != nil {
		h.lookupError(w, r, "document", id, err)
		return
	}
	resp, err := h.d.document(ctx, doc.Document)
	if err != nil {
		h.writePageError(w, r, err, ui.NavNone)
		return
	}
	images := h.pages.opts.LoadRemoteImages || boolParam(r, "images")
	vm := ui.Document{
		Layout:     h.pages.layout(cmp.Or(deref(doc.Title), doc.URL), ui.NavLibrary),
		Meta:       documentMeta(resp),
		Extraction: extractionView(resp.CurrentExtraction),
		Text:       h.documentText(r, resp, images),
		Related:    h.relatedPanel(r, id),
		Bookmarks:  h.bookmarksPanel(r, id),
	}
	if failureCurrent(doc.State) {
		vm.LastError, vm.FailureCause = doc.LastError, string(doc.FailureCause)
	}
	csp := ui.CSP
	if images {
		csp = ui.CSPWithImages
	}
	h.pageWithCSP(w, r, http.StatusOK, ui.PageDocument, vm, csp)
}

func documentMeta(d DocumentResponse) ui.DocumentMeta {
	m := ui.DocumentMeta{ID: d.ID, Title: deref(d.Title), URL: d.URL, CanonicalURL: deref(d.URLCanonical),
		ContentType: d.ContentType, State: d.State, Author: deref(d.Author), Language: deref(d.Language),
		CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt, MarkdownPath: currentMarkdownPath(d)}
	if d.PublishedAt != nil {
		m.PublishedAt = *d.PublishedAt
	}
	if d.WordCount != nil {
		m.WordCount = *d.WordCount
	}
	return m
}

// extractionView is how the current text was fetched, or nil before the
// first fetch. Via is extraction_meta's "via", what served the text.
func extractionView(e *ExtractionResponse) *ui.Extraction {
	if e == nil {
		return nil
	}
	via, _ := e.ExtractionMeta["via"].(string) // absent or not a string: unknown
	return &ui.Extraction{Fetcher: e.Fetcher, Via: via, Status: e.Status, ErrorMessage: deref(e.ErrorMessage),
		FetchedAt: e.FetchedAt}
}

// currentMarkdownPath is the absolute path of the document's current
// markdown, or "" when it has none.
func currentMarkdownPath(d DocumentResponse) string {
	if d.CurrentExtraction == nil {
		return ""
	}
	return d.CurrentExtraction.MarkdownPath
}

// documentText renders the document's markdown, the first
// ui.MaxRenderedMarkdown bytes of it, from the file GET
// /v1/documents/{id}/content streams.
func (h pageHandlers) documentText(r *http.Request, doc DocumentResponse, images bool) ui.TextPanel {
	content, err := openMarkdown(doc.ID, currentMarkdownPath(doc))
	if unavailable, ok := errors.AsType[*contentUnavailable](err); ok {
		if unavailable.missing {
			return ui.TextPanel{State: ui.TextMissing}
		}
		return ui.TextPanel{State: ui.TextNotFetched}
	}
	if err != nil {
		return ui.TextPanel{Err: h.panelError(r, err)}
	}
	defer content.Close()
	src, err := io.ReadAll(io.LimitReader(content, ui.MaxRenderedMarkdown+1))
	if err != nil {
		return ui.TextPanel{Err: h.panelError(r, fmt.Errorf("document %s: read its markdown: %w", doc.ID, err))}
	}
	src, truncated := ui.CutMarkdown(src)
	text, err := h.pages.render.RenderMarkdown(src, cmp.Or(deref(doc.URLCanonical), doc.URL), images)
	if err != nil {
		return ui.TextPanel{Err: h.panelError(r, fmt.Errorf("document %s: %w", doc.ID, err))}
	}
	return ui.TextPanel{State: ui.TextShown, Text: text, Truncated: truncated, MarkdownPath: content.path,
		OfferImages: !images && text.RemoteImages > 0}
}

// relatedPanel lists the documents most like document id, as GET
// /v1/documents/{id}/related finds them.
func (h pageHandlers) relatedPanel(r *http.Request, id string) ui.RelatedPanel {
	resp, err := h.d.related(r.Context(), id, relatedOnDocumentPage)
	if err != nil {
		return ui.RelatedPanel{Err: h.panelError(r, err)}
	}
	var p ui.RelatedPanel
	for _, hit := range resp.Items {
		p.Docs = append(p.Docs, ui.RelatedDoc{DocumentID: hit.Document.ID, Title: deref(hit.Document.Title),
			URL: hit.Document.URL, Score: hit.Score})
	}
	return p
}

// bookmarksPanel lists document id's bookmarks, newest saved first.
func (h pageHandlers) bookmarksPanel(r *http.Request, id string) ui.BookmarksPanel {
	bms, err := h.d.Bookmarks.ListByDocument(r.Context(), h.d.TenantID, id)
	if err != nil {
		return ui.BookmarksPanel{Err: h.panelError(r, err)}
	}
	var p ui.BookmarksPanel
	for _, b := range bms {
		p.Bookmarks = append(p.Bookmarks, ui.DocumentBookmark{Source: b.Source, Folder: deref(b.FolderPath),
			Title: deref(b.Title), Tags: b.Tags, SavedAt: b.SavedAt})
	}
	return p
}
