package api

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/understand"
)

// SearchRequest is the POST /v1/search body. K 0 (or omitted) means the
// configured search.default_k. The response is the documents ranked
// [Offset, Offset+K) of the one ranking every request for the query gets.
//
// Understand asks the planner what the query means and searches by its
// plan; Plan searches by a plan a client supplies, a response's
// understanding.plan as is or with a filter taken out, and asks no model,
// so an understood search's pages are windows of one ranking. A request
// sends one or neither. Filters win over a plan's, dimension by dimension.
type SearchRequest struct {
	Query      string    `json:"query"`
	K          int       `json:"k,omitempty"`
	Offset     int       `json:"offset,omitempty"`
	Filters    Filters   `json:"filters,omitzero"`
	Understand bool      `json:"understand,omitempty"`
	Plan       *PlanJSON `json:"plan,omitempty"`
}

// PlanJSON mirrors the openapi Plan schema: what a search does for a
// question besides its words. Filters are in the request's form; a saved
// bound the planner set is its local midnight in RFC 3339.
type PlanJSON struct {
	Kind     string   `json:"kind"`
	Filters  Filters  `json:"filters,omitzero"`
	Rewrites []string `json:"rewrites,omitempty"`
	Names    []string `json:"names,omitempty"`
}

// Understanding mirrors the openapi Understanding schema: how the query
// was understood, present exactly when the request sent understand or
// plan. Reason says why there is no plan, for any status but planned and
// supplied; Plan is the plan as applied, and Dropped the plan's filters
// left out, by the planner's guards or because the request sets their
// dimension.
type Understanding struct {
	Status  string          `json:"status"`
	Reason  string          `json:"reason,omitempty"`
	Model   string          `json:"model,omitempty"`
	TookMS  int64           `json:"took_ms"`
	Plan    *PlanJSON       `json:"plan,omitempty"`
	Dropped []DroppedFilter `json:"dropped,omitempty"`
}

// DroppedFilter is a filter of a plan the search left out, and why.
type DroppedFilter struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// Planner understands search questions: the daemon's understand.Planner,
// nil while search.understand is off. Timeout is how long a plan may take
// (search.understand_timeout_seconds), which the dashboard's note for a
// plan that took longer names. State is what it knows of its model, read
// from its memory alone, for healthz.
type Planner interface {
	Plan(ctx context.Context, question string) (understand.Outcome, error)
	Timeout() time.Duration
	State() understand.State
}

// Filters mirrors the openapi filters block; the search engine applies
// every dimension, as search.FilterRequest reads it.
type Filters struct {
	ContentType     []string `json:"content_type,omitempty"`
	Host            []string `json:"host,omitempty"`
	Source          []string `json:"source,omitempty"`
	Site            []string `json:"site,omitempty"`
	Folder          []string `json:"folder,omitempty"`
	SavedAfter      string   `json:"saved_after,omitempty"`
	SavedBefore     string   `json:"saved_before,omitempty"`
	PublishedAfter  string   `json:"published_after,omitempty"`
	PublishedBefore string   `json:"published_before,omitempty"`
	IncludeUndated  bool     `json:"include_undated,omitempty"`
}

// request is f as the search engine reads it, to resolve.
func (f Filters) request() search.FilterRequest {
	return search.FilterRequest{ContentType: f.ContentType, Host: f.Host, Source: f.Source, Site: f.Site,
		Folder: f.Folder, SavedAfter: f.SavedAfter, SavedBefore: f.SavedBefore, PublishedAfter: f.PublishedAfter,
		PublishedBefore: f.PublishedBefore, IncludeUndated: f.IncludeUndated}
}

// filtersOf is r on the wire.
func filtersOf(r search.FilterRequest) Filters {
	return Filters{ContentType: r.ContentType, Host: r.Host, Source: r.Source, Site: r.Site, Folder: r.Folder,
		SavedAfter: r.SavedAfter, SavedBefore: r.SavedBefore, PublishedAfter: r.PublishedAfter,
		PublishedBefore: r.PublishedBefore, IncludeUndated: r.IncludeUndated}
}

// plan is p as the planner's package reads it.
func (p PlanJSON) plan() understand.Plan {
	return understand.Plan{Kind: understand.Kind(p.Kind), Filters: p.Filters.request(), Rewrites: p.Rewrites,
		Names: p.Names}
}

// planJSON is p on the wire.
func planJSON(p understand.Plan) *PlanJSON {
	return &PlanJSON{Kind: string(p.Kind), Filters: filtersOf(p.Filters), Rewrites: p.Rewrites, Names: p.Names}
}

// SearchHitResponse mirrors the openapi SearchHit schema. MarkdownPath
// is the absolute on-disk path to the extracted markdown, populated from
// the doc's current extraction. Empty when there's no extraction yet.
// Surfaced here so CLI consumers can `cat` / open the file without a
// second round-trip to /v1/documents/{id}. BookmarkTitle names an untitled
// document: its newest titled bookmark's title, as the documents list has
// it. A hit without Matches matched on what it was saved as, and carries
// its Bookmarks, newest saved first, to say so; a hit with matches carries
// none.
type SearchHitResponse struct {
	Document      DocumentResponse    `json:"document"`
	Score         float64             `json:"score"`
	MarkdownPath  string              `json:"markdown_path,omitempty"`
	BookmarkTitle string              `json:"bookmark_title,omitempty"`
	Matches       []ChunkMatchJSON    `json:"matches,omitempty"`
	Bookmarks     []SearchHitBookmark `json:"bookmarks,omitempty"`
}

// SearchHitBookmark mirrors the openapi schema: one bookmark a hit without
// matches was saved under.
type SearchHitBookmark struct {
	Title      string    `json:"title,omitempty"`
	FolderPath string    `json:"folder_path,omitempty"`
	Source     string    `json:"source"`
	SavedAt    time.Time `json:"saved_at"`
	SavedDate  string    `json:"saved_date"`
}

// ChunkMatchJSON mirrors the openapi schema; pointer scores let us emit
// nil when the retriever didn't surface the chunk.
type ChunkMatchJSON struct {
	ChunkID     string   `json:"chunk_id"`
	Text        string   `json:"text"`
	Snippet     string   `json:"snippet,omitempty"`
	BM25Score   *float64 `json:"bm25_score,omitempty"`
	VectorScore *float64 `json:"vector_score,omitempty"`
}

// SearchResponse mirrors the openapi SearchResponse schema. Total is how
// many documents the query's ranking holds, at most store.MaxSearchK, and
// Capped whether more matched than it ranks. Degraded and Warnings report
// keyword-only results when semantic search was unavailable. Undated, with
// a date filter alone, is how many documents it couldn't judge.
// Understanding says how a request that sent understand or plan was
// understood; TookMS covers the planning too.
type SearchResponse struct {
	Query         string              `json:"query"`
	TookMS        int64               `json:"took_ms"`
	BM25Hits      int                 `json:"bm25_hits"`
	VectorHits    int                 `json:"vector_hits"`
	Total         int                 `json:"total"`
	Capped        bool                `json:"capped"`
	Degraded      bool                `json:"degraded,omitempty"`
	Warnings      []string            `json:"warnings,omitempty"`
	Undated       *int                `json:"undated,omitempty"`
	Understanding *Understanding      `json:"understanding,omitempty"`
	Items         []SearchHitResponse `json:"items"`
}

func (d Deps) handleSearch(w http.ResponseWriter, r *http.Request) {
	var req SearchRequest
	if err := decodeJSON(w, r, maxJSONBody, &req); err != nil {
		d.writeError(w, r, err)
		return
	}
	resp, err := d.search(r.Context(), req)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	d.writeJSON(w, r, http.StatusOK, resp)
}

// search runs req through the search engine and hydrates its hits. A query,
// k, offset or filter the engine can't take is a requestError, refused
// before the query is embedded or understood: a filter's names its field
// (search.FilterError), and a content_type or source outside its set is
// one, since the engine would match nothing and a typo would read as "no
// results". So is a request sending understand and a plan, or a plan
// understand.Validate refuses. Semantic search failing is not an error:
// the keyword results come back Degraded, with Warnings; nor is
// understanding failing, which searches the query's words, Understanding
// saying why.
func (d Deps) search(ctx context.Context, req SearchRequest) (SearchResponse, error) {
	if req.Query == "" {
		return SearchResponse{}, badRequest("query is required")
	}
	if req.K < 0 || req.K > store.MaxSearchK {
		return SearchResponse{}, badRequest("k must be between 1 and %d (or omitted for the default), got %d",
			store.MaxSearchK, req.K)
	}
	// Checked as a difference: offset+k overflows for an offset near the
	// int64 the decoder takes.
	k := cmp.Or(req.K, d.Search.DefaultK())
	if req.Offset < 0 || req.Offset > store.MaxSearchK-k {
		return SearchResponse{}, badRequest("offset must be between 0 and %d (offset + k at most %d), got %d",
			store.MaxSearchK-k, store.MaxSearchK, req.Offset)
	}
	// The request's own filters are checked alone, as without a plan:
	// include_undated without a date of its own is refused even when a
	// plan would add one.
	if _, err := req.Filters.request().Resolve(); err != nil {
		return SearchResponse{}, &requestError{err: err}
	}

	start := time.Now()
	plan, understanding, err := d.understand(ctx, req)
	if err != nil {
		return SearchResponse{}, err
	}
	sreq := search.Request{TenantID: d.TenantID, Query: req.Query, K: req.K, Offset: req.Offset}
	merged := req.Filters.request()
	if plan != nil {
		var dropped []understand.Dropped
		merged, dropped = mergeFilters(req.Filters.request(), plan.Filters)
		applied := *plan
		applied.Filters = withoutDropped(plan.Filters, dropped)
		understanding.Plan = planJSON(applied)
		understanding.Dropped = append(understanding.Dropped, droppedJSON(dropped)...)
		sreq.Rewrites, sreq.Names = plan.Rewrites, plan.Names
	}
	if sreq.Filters, err = merged.Resolve(); err != nil {
		return SearchResponse{}, &requestError{err: err}
	}
	res, err := d.Search.Search(ctx, sreq)
	if err != nil {
		return SearchResponse{}, err
	}

	items, err := d.searchHitsToResponse(ctx, res.Items)
	if err != nil {
		return SearchResponse{}, err
	}
	return SearchResponse{
		Query:         res.Query,
		TookMS:        time.Since(start).Milliseconds(),
		BM25Hits:      res.BM25Hits,
		VectorHits:    res.VectorHits,
		Total:         res.Total,
		Capped:        res.Capped,
		Degraded:      res.Degraded,
		Warnings:      res.Warnings,
		Undated:       res.Undated,
		Understanding: understanding,
		Items:         items,
	}, nil
}

// understand is req's plan and how it came to be: the plan it supplied,
// checked by understand.Validate; the planner's, when it asked for
// understanding and the planner gave one; or none, with the status that
// says why. A request that asked for neither has no Understanding. Only
// the caller's context ending is an error besides a refused plan.
func (d Deps) understand(ctx context.Context, req SearchRequest) (*understand.Plan, *Understanding, error) {
	switch {
	case req.Plan != nil && req.Understand:
		return nil, nil, badRequest("send understand or plan, not both")
	case req.Plan != nil:
		p := req.Plan.plan()
		if err := understand.Validate(p); err != nil {
			return nil, nil, &requestError{err: err}
		}
		return &p, &Understanding{Status: string(understand.StatusSupplied)}, nil
	case !req.Understand:
		return nil, nil, nil
	case d.Planner == nil:
		return nil, &Understanding{Status: string(understand.StatusOff), Reason: understand.OffReason}, nil
	}
	out, err := d.Planner.Plan(ctx, req.Query)
	if err != nil {
		return nil, nil, err
	}
	u := &Understanding{Status: string(out.Status), Model: out.Model, TookMS: out.Took.Milliseconds(),
		Dropped: droppedJSON(out.Dropped)}
	if out.Status != understand.StatusPlanned {
		u.Reason = out.Reason
	}
	return out.Plan, u, nil
}

// mergeFilters lays a plan's filters under the request's: each dimension
// the request sets is the request's, and the plan's of that dimension, if
// it set one, is dropped. A window (saved, published) is one dimension,
// its two bounds together; include_undated goes with the request's dates
// when it sets any, and with the plan's otherwise.
func mergeFilters(req, plan search.FilterRequest) (search.FilterRequest, []understand.Dropped) {
	out := req
	var dropped []understand.Dropped
	lists := []struct {
		field     string
		req, plan []string
		to        *[]string
	}{
		{"content_type", req.ContentType, plan.ContentType, &out.ContentType},
		{"host", req.Host, plan.Host, &out.Host},
		{"source", req.Source, plan.Source, &out.Source},
		{"site", req.Site, plan.Site, &out.Site},
		{"folder", req.Folder, plan.Folder, &out.Folder},
	}
	for _, l := range lists {
		switch {
		case len(l.req) == 0:
			*l.to = l.plan
		case len(l.plan) > 0:
			dropped = append(dropped, understand.Dropped{Field: l.field, Reason: "the request sets " + l.field})
		}
	}
	windows := []struct {
		field                 string
		reqSet, planSet       bool
		planAfter, planBefore string
		toAfter, toBefore     *string
	}{
		{"saved", req.SavedAfter != "" || req.SavedBefore != "", plan.SavedAfter != "" || plan.SavedBefore != "",
			plan.SavedAfter, plan.SavedBefore, &out.SavedAfter, &out.SavedBefore},
		{"published", req.PublishedAfter != "" || req.PublishedBefore != "",
			plan.PublishedAfter != "" || plan.PublishedBefore != "", plan.PublishedAfter, plan.PublishedBefore,
			&out.PublishedAfter, &out.PublishedBefore},
	}
	reqDates := false
	for _, w := range windows {
		reqDates = reqDates || w.reqSet
		switch {
		case !w.reqSet:
			*w.toAfter, *w.toBefore = w.planAfter, w.planBefore
		case w.planSet:
			dropped = append(dropped, understand.Dropped{Field: w.field,
				Reason: fmt.Sprintf("the request sets %s_after or %s_before", w.field, w.field)})
		}
	}
	switch {
	case !reqDates:
		out.IncludeUndated = plan.IncludeUndated
	case plan.IncludeUndated && !req.IncludeUndated:
		dropped = append(dropped, understand.Dropped{Field: "include_undated",
			Reason: "the request sets its own dates, without include_undated"})
	}
	return out, dropped
}

// withoutDropped is f less the dimensions dropped names: what of a plan's
// filters a search applied.
func withoutDropped(f search.FilterRequest, dropped []understand.Dropped) search.FilterRequest {
	for _, d := range dropped {
		f = withoutDimension(f, d.Field)
	}
	return f
}

// withoutDimension is f less the dimension field names: content_type,
// host, source, site, folder, saved or published (a window, both bounds),
// or include_undated. Taking out its last date filter takes
// include_undated with it, which keeps nothing alone.
func withoutDimension(f search.FilterRequest, field string) search.FilterRequest {
	switch field {
	case "content_type":
		f.ContentType = nil
	case "host":
		f.Host = nil
	case "source":
		f.Source = nil
	case "site":
		f.Site = nil
	case "folder":
		f.Folder = nil
	case "saved":
		f.SavedAfter, f.SavedBefore = "", ""
	case "published":
		f.PublishedAfter, f.PublishedBefore = "", ""
	case "include_undated":
		f.IncludeUndated = false
	}
	if f.SavedAfter == "" && f.SavedBefore == "" && f.PublishedAfter == "" && f.PublishedBefore == "" {
		f.IncludeUndated = false
	}
	return f
}

func droppedJSON(dropped []understand.Dropped) []DroppedFilter {
	if len(dropped) == 0 {
		return nil
	}
	out := make([]DroppedFilter, 0, len(dropped))
	for _, d := range dropped {
		out = append(out, DroppedFilter{Field: d.Field, Reason: d.Reason})
	}
	return out
}

// searchHitsToResponse maps engine hits to wire hits, populating each
// hit's markdown path from its current extraction, naming the untitled
// ones by their bookmarks (bookmarkTitles), and giving each hit without
// matches the bookmarks it was saved under (savedUnder).
//
// One extra DB hit per result to surface the markdown path. K is at most
// store.MaxSearchK (100), so this stays small; GetByIDsWithLastError, which
// the untitled hits' names already use, returns markdown_path too and could
// serve every hit in one read if it ever shows in latency.
func (d Deps) searchHitsToResponse(ctx context.Context, hits []search.Hit) ([]SearchHitResponse, error) {
	titles, err := d.bookmarkTitles(ctx, hits)
	if err != nil {
		return nil, err
	}
	saved, err := d.savedUnder(ctx, hits)
	if err != nil {
		return nil, err
	}
	out := make([]SearchHitResponse, 0, len(hits))
	for _, hit := range hits {
		matches := make([]ChunkMatchJSON, 0, len(hit.Chunks))
		for _, cm := range hit.Chunks {
			matches = append(matches, ChunkMatchJSON{
				ChunkID:     cm.ChunkID,
				Text:        cm.Text,
				Snippet:     cm.Snippet,
				BM25Score:   cm.BM25Score,
				VectorScore: cm.VectorScore,
			})
		}

		mdPath, err := d.documentMarkdownPath(ctx, hit.Document)
		if err != nil {
			return nil, err
		}
		out = append(out, SearchHitResponse{
			Document:      documentToResponse(hit.Document),
			Score:         hit.Score,
			MarkdownPath:  mdPath,
			BookmarkTitle: titles[hit.Document.ID],
			Matches:       matches,
			Bookmarks:     saved[hit.Document.ID],
		})
	}
	return out, nil
}

// savedUnder is, by document, the bookmarks each hit without matches was
// saved under, newest saved first: what it matched on, when no passage
// did. It reads them all at once, and nothing when every hit has matches.
func (d Deps) savedUnder(ctx context.Context, hits []search.Hit) (map[string][]SearchHitBookmark, error) {
	var bare []string
	for _, hit := range hits {
		if len(hit.Chunks) == 0 {
			bare = append(bare, hit.Document.ID)
		}
	}
	out := map[string][]SearchHitBookmark{}
	if len(bare) == 0 {
		return out, nil
	}
	byDoc, err := d.Bookmarks.ListByDocuments(ctx, d.TenantID, bare)
	if err != nil {
		return nil, fmt.Errorf("read the bookmarks of the hits without matches: %w", err)
	}
	for doc, bms := range byDoc {
		for _, b := range bms {
			out[doc] = append(out[doc], SearchHitBookmark{Title: strings.TrimSpace(deref(b.Title)),
				FolderPath: deref(b.FolderPath), Source: b.Source, SavedAt: b.SavedAt, SavedDate: string(b.SavedDate)})
		}
	}
	return out, nil
}

// bookmarkTitles names the hits' untitled documents, by ID, as the
// documents list names them: by their newest titled bookmark. It reads
// them all at once, with the batched read the interests' members take, and
// reads nothing when every hit has a title of its own.
func (d Deps) bookmarkTitles(ctx context.Context, hits []search.Hit) (map[string]string, error) {
	titles := map[string]string{}
	var untitled []string
	for _, hit := range hits {
		if deref(hit.Document.Title) == "" {
			untitled = append(untitled, hit.Document.ID)
		}
	}
	if len(untitled) == 0 {
		return titles, nil
	}
	docs, err := d.Documents.GetByIDsWithLastError(ctx, d.TenantID, untitled)
	if err != nil {
		return nil, fmt.Errorf("name untitled hits by their bookmarks: %w", err)
	}
	for _, doc := range docs {
		titles[doc.ID] = doc.BookmarkTitle
	}
	return titles, nil
}

// defaultRelatedK is how many related documents GET
// /v1/documents/{id}/related returns without ?k.
const defaultRelatedK = 10

// RelatedResponse is the body of GET /v1/documents/{id}/related. Scores
// are raw vector similarities (1/(1+L2 distance), 0..1) — not comparable
// with /v1/search's RRF-fused scores.
type RelatedResponse struct {
	DocID  string              `json:"doc_id"`
	TookMS int64               `json:"took_ms"`
	Items  []SearchHitResponse `json:"items"`
}

// handleRelatedDocuments finds documents similar to {id} by embedding
// similarity over its stored chunk vectors. 404 for an unknown document;
// 200 with empty items for a document that has no indexed chunks yet.
func (d Deps) handleRelatedDocuments(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	k := intQuery(r, "k", defaultRelatedK, 1, store.MaxSearchK)
	resp, err := d.related(r.Context(), id, k)
	if err != nil {
		d.writeLookupError(w, r, "document", id, err)
		return
	}
	d.writeJSON(w, r, http.StatusOK, resp)
}

// related finds up to k documents similar to document id, hydrated as
// search hits. An unknown document is an error wrapping store.ErrNotFound,
// and one with no indexed chunks has none.
func (d Deps) related(ctx context.Context, id string, k int) (RelatedResponse, error) {
	start := time.Now()
	res, err := d.Search.Related(ctx, search.RelatedRequest{
		TenantID:   d.TenantID,
		DocumentID: id,
		K:          k,
	})
	if err != nil {
		return RelatedResponse{}, err
	}
	items, err := d.searchHitsToResponse(ctx, res.Items)
	if err != nil {
		return RelatedResponse{}, err
	}
	return RelatedResponse{
		DocID:  id,
		TookMS: time.Since(start).Milliseconds(),
		Items:  items,
	}, nil
}
