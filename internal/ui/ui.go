// Package ui is the dashboard's view layer: the pages' templates and
// static assets, embedded in the binary, the view models they render, and
// the markdown renderer and snippet highlighting their content goes
// through. It renders; internal/api routes and gathers each page's data.
//
// Everything a page shows from the library came from a web page (or from
// Jina) and is treated as hostile: html/template escapes it, stored
// markdown goes through markdown's sanitizer, and every response carries a
// CSP that allows no inline script or style (see docs/ui.md).
package ui

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/samsar/curio/internal/store"
)

//go:embed templates/*.html static/*
var files embed.FS

// pageNames are the pages: each is templates/<name>.html on top of the
// layout, the partials and the icons.
var pageNames = []string{
	PageSearch, PageStatus, PageLibrary, PageFailures, PageDocument, PageInterests, PageInterest, PageUnsorted,
	PageChanges, PageRetired, PageError, PageStarting,
}

// Renderer renders the dashboard's pages and serves its assets. It is
// immutable once built and safe for concurrent use.
type Renderer struct {
	pages    map[string]*template.Template
	assets   assetSet
	markdown *markdown
}

// New parses every page's templates and reads the assets. An error means a
// template or asset doesn't build, which the render tests catch first.
func New() (*Renderer, error) {
	assets, err := loadAssets(files, "static")
	if err != nil {
		return nil, fmt.Errorf("ui: %w", err)
	}
	base, err := template.New("layout.html").Funcs(funcs(assets)).ParseFS(files, "templates/layout.html",
		"templates/icons.html")
	if err != nil {
		return nil, fmt.Errorf("ui: parse the layout: %w", err)
	}
	pages := make(map[string]*template.Template, len(pageNames))
	for _, name := range pageNames {
		t, err := base.Clone()
		if err != nil {
			return nil, fmt.Errorf("ui: clone the layout for %s: %w", name, err)
		}
		if pages[name], err = t.ParseFS(files, "templates/"+name+".html"); err != nil {
			return nil, fmt.Errorf("ui: parse the %s page: %w", name, err)
		}
	}
	return &Renderer{pages: pages, assets: assets, markdown: newMarkdown()}, nil
}

// Page writes the page name rendered from data, its view model, with
// status and csp as its Content-Security-Policy. It renders into a buffer
// first: a template that fails part way writes nothing and returns its
// error, for the caller to answer with an error page rather than half a
// page that looks complete.
func (r *Renderer) Page(w http.ResponseWriter, status int, name string, data any, csp string) error {
	t, ok := r.pages[name]
	if !ok {
		return fmt.Errorf("ui: no page %q", name)
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		return fmt.Errorf("ui: render the %s page: %w", name, err)
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", csp)
	h.Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes()) // fails only when the client has gone
	return nil
}

// RenderMarkdown renders stored markdown for a document page: base is the
// document's URL, which relative links and images resolve against, and
// images shows its https images. See markdown for what it lets through.
func (r *Renderer) RenderMarkdown(src []byte, base string, images bool) (Text, error) {
	return r.markdown.render(src, base, images)
}

// funcs are the templates' functions. Every link is built by one of them
// (links.go) or a view model's method, never pieced together in a
// template, and so is every stored value's display form, class and icon
// name (format.go, causes.go). Actions and pollers come from the view
// models' methods (actions.go, poll.go).
func funcs(assets assetSet) template.FuncMap {
	return template.FuncMap{
		"asset":              assets.url,
		"documentHref":       documentHref,
		"documentImagesHref": documentImagesHref,
		"interestHref":       interestHref,
		"unsortedHref":       unsortedHref,
		"changesHref":        changesHref,
		"libraryHref":        libraryHref,
		"libraryMoreHref":    libraryMoreHref,
		"clearFiltersHref":   clearFiltersHref,
		"clearCauseHref":     clearCauseHref,
		"stateHref":          stateHref,
		"causeHref":          causeHref,
		"causeHostHref":      causeHostHref,
		"failuresHref":       failuresHref,
		"failureCauseHref":   failureCauseHref,
		"navHref":            navHref,
		"navItems":           navItems,
		"outbound":           outbound,
		"when":               when,
		"ago":                ago,
		"datetime":           datetime,
		"day":                day,
		"localDay":           localDay,
		"duration":           duration,
		"score":              score,
		"host":               Host,
		"shortURL":           shortURL,
		"urlTrail":           urlTrail,
		"shortError":         shortError,
		"causeLabel":         causeLabel,
		"causeWhy":           causeWhy,
		"causeIcon":          causeIcon,
		"causeTone":          causeTone,
		"interestName":       interestName,
		"stateClass":         stateClass,
		"stateTone":          stateTone,
		"typeIcon":           typeIcon,
		"num":                num,
		"count":              count,
		"pct":                pct,
		"stateBar":           stateBar,
		"childBar":           childBar,
		"causeBar":           causeBar,
		"fitClass":           fitClass,
		"eventLabel":         eventLabel,
		"triggerLabel":       triggerLabel,
		"freshReason":        freshReason,
		"queueReason":        queueReason,
		"gentleHint":         gentleHint,
		"jobKindLabel":       jobKindLabel,
		"viaLabel":           viaLabel,
		"contentTypes":       func() []string { return contentTypes },
	}
}

// Every document state, in lifecycle order, as the state bar draws them,
// and every content type, as the Library's form offers them.
var (
	docStates = []string{string(store.DocStatePending), string(store.DocStateFetched),
		string(store.DocStateFailed), string(store.DocStateDead)}
	contentTypes = []string{string(store.ContentTypeArticle), string(store.ContentTypeRepo),
		string(store.ContentTypeVideo), string(store.ContentTypePDF), string(store.ContentTypeThread),
		string(store.ContentTypeUnknown)}
)

// when formats t on the daemon's clock, to the minute, or "never" for the
// zero time.
func when(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.Local().Format("2006-01-02 15:04")
}

// score formats a retriever's score, or "" when it didn't return the
// chunk.
func score(s *float64) string {
	if s == nil {
		return ""
	}
	return fmt.Sprintf("%.3f", *s)
}

// duration formats d roughly, as a person would say it: "3h 20m", "12m",
// "under a minute".
func duration(d time.Duration) string {
	d = d.Round(time.Minute)
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d/time.Hour), int(d%time.Hour/time.Minute))
	default:
		return fmt.Sprintf("%dd %dh", int(d/(24*time.Hour)), int(d%(24*time.Hour)/time.Hour))
	}
}
