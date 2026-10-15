package api

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/ui"
	"github.com/samsar/curio/internal/understand"
)

// search answers GET /ui/?q=&content_type=&page=, the dashboard's home:
// the form, and for a query, a page of the results of the same search POST
// /v1/search runs, ui.SearchPageSize of them from the page's offset,
// limited to the content type when one is given. A search that fails is
// shown where its results would be, with its status. A type that isn't
// one is a 400 page, as the Library answers it, and so is a page no search
// can have, before anything is searched; without a query the page is
// ignored. A page past this search's last is its answer, a 200 with the
// out-of-range card: the page's status is its search's, and the search
// answered, as POST /v1/search answers an offset past its total.
//
// Search as you type renders the whole page on every keystroke, so with a
// query the page reads nothing but the search; without one it reads what
// the home shows instead (home). A keystroke never asks for understanding:
// only ?understand=1, which Enter sends, asks the planner, and ?plan=, a
// token of the plan an understood page searched by, searches by it again
// without asking (planParams). Asked from htmx, an understood page's
// answer says which address to push: its plan's, or its words' when it
// went without one, so that reload, back and forward never plan again.
func (h pageHandlers) search(w http.ResponseWriter, r *http.Request) {
	contentType, err := contentTypeParam(r)
	if err != nil {
		h.writePageError(w, r, err, ui.NavSearch)
		return
	}
	q := r.URL.Query().Get("q")
	vm := ui.Search{Layout: h.pages.layout("Search", ui.NavSearch), Query: q, Type: string(contentType)}
	status := http.StatusOK
	if strings.TrimSpace(q) == "" {
		vm.Home = h.home(r)
		h.page(w, r, status, ui.PageSearch, vm)
		return
	}
	if vm.Page, err = searchPageParam(r); err != nil {
		h.writePageError(w, r, err, ui.NavSearch)
		return
	}
	req := SearchRequest{Query: q, K: ui.SearchPageSize, Offset: ui.PageOffset(vm.Page, ui.SearchPageSize)}
	if contentType != "" {
		req.Filters.ContentType = []string{string(contentType)}
	}
	if req.Understand, req.Plan, err = planParams(r); err != nil {
		h.writePageError(w, r, err, ui.NavSearch)
		return
	}
	resp, err := h.d.search(r.Context(), req)
	if err == nil {
		vm.Results = searchResults(resp)
		vm.Plan, vm.Understood, err = understoodView(resp)
	}
	if vm.Understood != nil && h.d.Planner != nil {
		vm.Understood.Timeout = h.d.Planner.Timeout()
	}
	if err != nil {
		status, vm.Err = h.reportPanel(r, err)
	}
	if (req.Understand || req.Plan != nil) && r.Header.Get("Hx-Request") == "true" {
		w.Header().Set("HX-Push-Url", vm.Address())
	}
	h.page(w, r, status, ui.PageSearch, vm)
}

// maxPlanParam is the longest ?plan the search page takes. A planner's plan
// at its caps, three rewrites and four names each of characters JSON
// writes twice as long (a quote, a backslash; understand refuses control
// characters, which it writes six times as long), with every filter the
// planner sets and include_undated, is 2,079 bytes of JSON (encodePlan
// leaves &, < and > as they are), 2,772 as base64.
const maxPlanParam = 4 << 10

// planParams reads how the search page's query is to be understood:
// ?understand=1, or ?plan=, a token encodePlan made; one or neither. A
// token that doesn't decode to a plan understand.Validate takes, a value
// of understand but 1, or both, is a requestError, refused before
// anything is searched. ?understand=1 asks the planner only from the
// dashboard itself or the user's own navigation (selfInitiated): another
// site's link to the page searches its words, so no page elsewhere can
// load a writing model of gigabytes for its keep-alive. A plan asks no
// model, wherever it comes from.
func planParams(r *http.Request) (bool, *PlanJSON, error) {
	query := r.URL.Query()
	asked, token := query.Get("understand"), query.Get(ui.PlanParam)
	switch {
	case asked != "" && asked != "1":
		return false, nil, badRequest("understand %q must be 1", asked)
	case asked != "" && token != "":
		return false, nil, badRequest("send understand or %s, not both", ui.PlanParam)
	case token != "":
		p, err := decodePlan(token)
		return false, p, err
	}
	return asked == "1" && selfInitiated(r), nil, nil
}

// selfInitiated reports whether r came from the dashboard's own pages or
// from the user, by what the browser says in Sec-Fetch-Site: same-origin;
// none, a navigation no page made (an address typed, a bookmark, a link
// opened from another app such as mail, chat or a terminal); or nothing,
// a client that isn't a browser, a local process the API trusts, or a
// Safari before 16.4, which sends no Sec-Fetch headers. No website can
// produce none or leave the header out of a modern browser's request. A
// cross-site or same-site request, which isolateDashboard lets through
// when it is a top-level navigation, didn't come from either.
func selfInitiated(r *http.Request) bool {
	sites := r.Header.Values("Sec-Fetch-Site")
	return len(sites) == 0 || (len(sites) == 1 && (sites[0] == "same-origin" || sites[0] == "none"))
}

// encodePlan is p as a search page's ?plan: its JSON, base64url without
// padding, opaque to the page. The JSON leaves &, < and > as they are:
// escaped for HTML, each is six bytes, and a plan of them outgrows
// maxPlanParam.
func encodePlan(p PlanJSON) (string, error) {
	var raw bytes.Buffer
	enc := json.NewEncoder(&raw)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(p); err != nil {
		return "", fmt.Errorf("encode the plan: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes.TrimSuffix(raw.Bytes(), []byte("\n"))), nil
}

// decodePlan reads a ?plan token, strictly: at most maxPlanParam bytes of
// base64url, one JSON plan of the API's shape and no unknown key, and a
// plan understand.Validate takes. Anything else is a requestError.
func decodePlan(token string) (*PlanJSON, error) {
	if len(token) > maxPlanParam {
		return nil, badRequest("%s is %d bytes, at most %d", ui.PlanParam, len(token), maxPlanParam)
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, badRequest("%s isn't a plan this page made: %w", ui.PlanParam, err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var p PlanJSON
	if err := dec.Decode(&p); err != nil {
		return nil, badRequest("%s isn't a plan this page made: %w", ui.PlanParam, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, badRequest("%s isn't a plan this page made: more than one value", ui.PlanParam)
	}
	if err := understand.Validate(p.plan()); err != nil {
		return nil, &requestError{err: err}
	}
	return &p, nil
}

// understoodView is how resp's query was understood, as the search page
// shows it, and the token of the plan it was searched by (empty without
// one): each of the plan's filters with the token of the plan without it,
// and the token of the plan with its undated documents switched. Nil for
// a search that didn't ask.
func understoodView(resp SearchResponse) (string, *ui.Understood, error) {
	u := resp.Understanding
	if u == nil {
		return "", nil, nil
	}
	view := &ui.Understood{Status: u.Status, Model: u.Model}
	if u.Plan == nil {
		return "", view, nil
	}
	p := *u.Plan
	token, err := encodePlan(p)
	if err != nil {
		return "", nil, err
	}
	view.Rewrites, view.Names = p.Rewrites, p.Names
	view.Undated, view.UndatedKept = resp.Undated, p.Filters.IncludeUndated
	for _, f := range planFilters(p.Filters) {
		without := p
		without.Filters = filtersOf(withoutDimension(p.Filters.request(), f.Field))
		if f.Token, err = encodePlan(without); err != nil {
			return "", nil, err
		}
		view.Filters = append(view.Filters, f)
	}
	if view.Undated != nil {
		switched := p
		switched.Filters.IncludeUndated = !p.Filters.IncludeUndated
		if view.UndatedToken, err = encodePlan(switched); err != nil {
			return "", nil, err
		}
	}
	return token, view, nil
}

// planFilters are the dimensions f sets, in a fixed order, as the search
// page's chips show them.
func planFilters(f Filters) []ui.PlanFilter {
	var out []ui.PlanFilter
	for _, l := range []struct {
		field  string
		values []string
	}{
		{"content_type", f.ContentType}, {"source", f.Source}, {"site", f.Site}, {"host", f.Host},
		{"folder", f.Folder},
	} {
		if len(l.values) > 0 {
			out = append(out, ui.PlanFilter{Field: l.field, Values: l.values})
		}
	}
	if f.SavedAfter != "" || f.SavedBefore != "" {
		out = append(out, ui.PlanFilter{Field: "saved", After: f.SavedAfter, Before: f.SavedBefore})
	}
	if f.PublishedAfter != "" || f.PublishedBefore != "" {
		out = append(out, ui.PlanFilter{Field: "published", After: f.PublishedAfter, Before: f.PublishedBefore})
	}
	return out
}

// searchPageParam reads the search page's ?page (ui.PageParam) as pageParam
// reads a numbered list's, and refuses a page past ui.MaxSearchPages as
// well: a search ranks store.MaxSearchK documents at most, so no search has
// one, and the page's offset would be one POST /v1/search refuses.
func searchPageParam(r *http.Request) (int, error) {
	s := r.URL.Query().Get(ui.PageParam)
	if s == "" {
		return 1, nil
	}
	page, err := strconv.Atoi(s)
	if err != nil || page < 1 || page > ui.MaxSearchPages {
		return 0, badRequest("%s %q must be a whole number from 1 to %d", ui.PageParam, s, ui.MaxSearchPages)
	}
	return page, nil
}

// home is what the search page shows without a query: how many documents
// there are to search, for the box's placeholder, and the latest
// rebuild's largest top-level groups (areas, or interests in the flat
// shape), without their interests or members. The home does without a read
// that fails: it is logged, and what it was for is left out.
func (h pageHandlers) home(r *http.Request) *ui.SearchHome {
	ctx := r.Context()
	home := &ui.SearchHome{}
	if st, err := h.d.stats(ctx); err != nil {
		h.quietError(r, err)
	} else {
		home.Searchable = st.DocumentsByState[string(store.DocStateFetched)]
	}
	interests, err := h.d.interests(ctx, interestsOpts{Limit: homeInterests, Bare: true})
	if err != nil {
		h.quietError(r, err)
		return home
	}
	home.AllInterests = interests.Total
	for _, in := range interests.Items {
		home.Interests = append(home.Interests, interestView(in))
	}
	return home
}

func searchResults(resp SearchResponse) *ui.SearchResults {
	res := &ui.SearchResults{Degraded: resp.Degraded, Warnings: resp.Warnings, TookMS: resp.TookMS,
		Total: resp.Total, Capped: resp.Capped}
	for _, item := range resp.Items {
		hit := ui.SearchHit{DocumentID: item.Document.ID, Title: deref(item.Document.Title),
			BookmarkTitle: strings.TrimSpace(item.BookmarkTitle), URL: item.Document.URL,
			ContentType: item.Document.ContentType, State: item.Document.State, Score: item.Score}
		for _, m := range item.Matches {
			hit.Matches = append(hit.Matches, ui.Match{Segments: ui.Passage(m.Snippet, m.Text), BM25: m.BM25Score,
				Vector: m.VectorScore})
		}
		for _, b := range item.Bookmarks {
			hit.Bookmarks = append(hit.Bookmarks, ui.SavedBookmark{Title: b.Title, FolderPath: b.FolderPath,
				Source: b.Source})
		}
		res.Hits = append(res.Hits, hit)
	}
	return res
}
