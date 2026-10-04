package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/ui"
	"github.com/samsar/curio/internal/version"
)

// The dashboard: pages under /ui/ on the daemon's own port and origin
// (docs/ui.md). The handlers in ui_*.go get their data through the same
// Deps functions as the JSON handlers, and never from SQL, then map it
// into internal/ui's view models, which the templates render. Every route
// is a GET: pages never change anything, since another site can make a
// browser navigate to one. What a page changes, its own script sends to
// /v1 as JSON (internal/ui/static/actions.js).
//
// A page's pollers ask for its live regions alone with ?poll= (ui.PollParam):
// the same route, handler and template, which then read only what those
// regions show.

// UIOptions configure the dashboard (config.yaml's daemon.ui and ui).
type UIOptions struct {
	// Enabled serves the pages under /ui/, and redirects / there.
	Enabled bool
	// LoadRemoteImages shows stored pages' https images on every document
	// page, without the per-page ?images=1.
	LoadRemoteImages bool
}

// dashboard is what a server serves the pages with: none while render is
// nil.
type dashboard struct {
	opts   UIOptions
	render *ui.Renderer
	listen string // the address the daemon listens on, which the pages name
}

// newDashboard builds the dashboard opts asks for, parsing its templates,
// for a daemon listening on listen ("" when unknown).
func newDashboard(opts UIOptions, listen string) (dashboard, error) {
	if !opts.Enabled {
		return dashboard{}, nil
	}
	render, err := ui.New()
	if err != nil {
		return dashboard{}, err
	}
	return dashboard{opts: opts, render: render, listen: listen}, nil
}

func (d dashboard) enabled() bool { return d.render != nil }

// layout is the frame of a page titled title, under nav.
func (d dashboard) layout(title string, nav ui.Nav) ui.Layout {
	return ui.Layout{Title: title, Nav: nav, Version: version.String(), Listen: d.listen}
}

// redirectToDashboard answers / with the dashboard.
func redirectToDashboard(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/ui/", http.StatusFound)
}

// redirectToSearch answers GET /ui/search, the search page's address before
// search became the home, with the home and the same query, so that links
// and bookmarks to it keep working. The query passes through as sent,
// whatever parameters it holds; the path is fixed, so the answer never
// leads off the daemon.
func redirectToSearch(w http.ResponseWriter, r *http.Request) {
	target := "/ui/"
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusFound) //nolint:gosec // G710: the path is fixed; only the query is the request's
}

// failureCurrent reports whether a document in state is failing now, and
// so whether a page shows its last error and cause: a refetch that
// recovered from an older failure leaves that job's error behind.
func failureCurrent(state store.DocState) bool {
	switch state {
	case store.DocStateFailed, store.DocStateDead:
		return true
	case store.DocStatePending, store.DocStateFetched:
	}
	return false
}

// pageHandlers serve the dashboard's pages over d.
type pageHandlers struct {
	d     Deps
	pages dashboard
}

// routes adds the pages' routes to r: GETs only (TestDashboard_GETOnly).
// The assets' route is a Get, not a Mount, which would take every method.
func (h pageHandlers) routes(r chi.Router) {
	r.Get("/", redirectToDashboard)
	r.Route("/ui", func(r chi.Router) {
		r.NotFound(h.notFound)
		r.Get("/", h.search)
		r.Get("/search", redirectToSearch)
		r.Get("/status", h.status)
		r.Get("/library", h.library)
		r.Get("/failures", h.failures)
		r.Get("/documents/{id}", h.document)
		r.Get("/interests", h.interests)
		r.Get("/interests/unsorted", h.unsorted)
		r.Get("/interests/changes", h.changes)
		r.Get("/interests/map", h.interestMap)
		r.Get("/interests/{id}", h.interest)
		r.Get("/static/{file}", h.asset)
	})
}

// page answers the page name, rendered from data, with status and the
// strict CSP.
func (h pageHandlers) page(w http.ResponseWriter, r *http.Request, status int, name string, data any) {
	h.pageWithCSP(w, r, status, name, data, ui.CSP)
}

// pageWithCSP is page with csp as the page's Content-Security-Policy. A
// page that fails to render is answered as a 500 error page, never half
// written.
func (h pageHandlers) pageWithCSP(w http.ResponseWriter, r *http.Request, status int, name string, data any, csp string) {
	if err := h.pages.render.Page(w, status, name, data, csp); err != nil {
		h.writePageError(w, r, err, ui.NavNone)
	}
}

// writePageError answers err as an error page: classified, and a server
// error logged once, as writeError answers it as a problem. retry is the
// page the error page suggests starting over from.
func (h pageHandlers) writePageError(w http.ResponseWriter, r *http.Request, err error, retry ui.Nav) {
	status, title := h.d.reportError(r, err)
	h.errorPage(w, r, status, title, err.Error(), retry)
}

// lookupError answers a failure to load the kind of resource named id:
// a 404 page naming it when there is none, writePageError's otherwise.
func (h pageHandlers) lookupError(w http.ResponseWriter, r *http.Request, kind, id string, err error) {
	if errors.Is(err, store.ErrNotFound) {
		h.errorPage(w, r, http.StatusNotFound, "not found", fmt.Sprintf("%s %q not found", kind, id), ui.NavSearch)
		return
	}
	h.writePageError(w, r, err, ui.NavNone)
}

// notFound answers a path under /ui/ that no page has.
func (h pageHandlers) notFound(w http.ResponseWriter, r *http.Request) {
	h.errorPage(w, r, http.StatusNotFound, "not found", "no page at "+routingPath(r), ui.NavSearch)
}

// errorPage answers the error page. Should it fail to render too, nothing
// has been written yet, and a plain-text answer still can be.
func (h pageHandlers) errorPage(w http.ResponseWriter, r *http.Request, status int, title, message string, retry ui.Nav) {
	id := middleware.GetReqID(r.Context())
	vm := ui.ErrorPage{Layout: h.pages.layout(title, ui.NavNone), Status: status, Title: title, Message: message,
		RequestID: id, Retry: retry}
	if err := h.pages.render.Page(w, status, ui.PageError, vm, ui.CSP); err != nil {
		h.d.Log.Error("render the error page", "request_id", id, "err", err)
		http.Error(w, fmt.Sprintf("%s: %s (request %s)", title, message, id), status)
	}
}

// panelError reports a panel's failed read, logged as writeError logs it,
// for the panel to show while the rest of the page renders: the page
// degrades by panel, where a JSON answer fails whole.
func (h pageHandlers) panelError(r *http.Request, err error) *ui.PanelError {
	if err == nil {
		return nil
	}
	_, p := h.reportPanel(r, err)
	return p
}

// quietError reports a failed read the page does without, logged as
// writeError logs it: the page leaves out what the read was for, rather
// than showing a panel's error where nothing would have shown.
func (h pageHandlers) quietError(r *http.Request, err error) {
	h.d.reportError(r, err)
}

// reportPanel is panelError for a read whose failure is the page's answer,
// a search's: it also returns the status to answer with, reportError's,
// which is a 499 when the client has gone.
func (h pageHandlers) reportPanel(r *http.Request, err error) (int, *ui.PanelError) {
	status, _ := h.d.reportError(r, err)
	return status, &ui.PanelError{Message: err.Error(), RequestID: middleware.GetReqID(r.Context())}
}

// asset serves the pages' asset named by the path, or the 404 page.
func (h pageHandlers) asset(w http.ResponseWriter, r *http.Request) {
	if !h.pages.render.ServeAsset(w, chi.URLParam(r, "file")) {
		h.notFound(w, r)
	}
}

// pollParam reads a page's ?poll: "" for the whole page, or one of kinds,
// the live regions it takes. Any other value is a requestError, answered
// before the page reads anything.
func pollParam(r *http.Request, kinds ...string) (string, error) {
	poll := r.URL.Query().Get(ui.PollParam)
	if poll == "" || slices.Contains(kinds, poll) {
		return poll, nil
	}
	return "", badRequest("%s %q must be one of: %s", ui.PollParam, poll, strings.Join(kinds, ", "))
}

// pageParam reads a numbered list's ?page (ui.PageParam), from 1: absent
// or empty is the first, and anything but a whole number of 1 or more is a
// requestError, answered before the page reads anything.
func pageParam(r *http.Request) (int, error) { return positionParam(r, ui.PageParam, 1) }

// isPoll reports whether r asks a page for its live regions.
func isPoll(r *http.Request) bool { return r.URL.Query().Get(ui.PollParam) != "" }

// deref is *s, or "" for nil: an optional field as a page shows it.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
