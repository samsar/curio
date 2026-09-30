package api

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
)

// SearchRequest is the POST /v1/search body. K 0 (or omitted) means the
// configured search.default_k. The response is the documents ranked
// [Offset, Offset+K) of the one ranking every request for the query gets.
type SearchRequest struct {
	Query   string  `json:"query"`
	K       int     `json:"k,omitempty"`
	Offset  int     `json:"offset,omitempty"`
	Filters Filters `json:"filters,omitzero"`
}

// Filters mirrors the openapi filters block; the search engine applies
// every dimension.
type Filters struct {
	ContentType []string `json:"content_type,omitempty"`
	Host        []string `json:"host,omitempty"`
	Source      []string `json:"source,omitempty"`
}

// SearchHitResponse mirrors the openapi SearchHit schema. MarkdownPath
// is the absolute on-disk path to the extracted markdown, populated from
// the doc's current extraction. Empty when there's no extraction yet.
// Surfaced here so CLI consumers can `cat` / open the file without a
// second round-trip to /v1/documents/{id}. BookmarkTitle names an untitled
// document: its newest titled bookmark's title, as the documents list has
// it.
type SearchHitResponse struct {
	Document      DocumentResponse `json:"document"`
	Score         float64          `json:"score"`
	MarkdownPath  string           `json:"markdown_path,omitempty"`
	BookmarkTitle string           `json:"bookmark_title,omitempty"`
	Matches       []ChunkMatchJSON `json:"matches,omitempty"`
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
// keyword-only results when semantic search was unavailable.
type SearchResponse struct {
	Query      string              `json:"query"`
	TookMS     int64               `json:"took_ms"`
	BM25Hits   int                 `json:"bm25_hits"`
	VectorHits int                 `json:"vector_hits"`
	Total      int                 `json:"total"`
	Capped     bool                `json:"capped"`
	Degraded   bool                `json:"degraded,omitempty"`
	Warnings   []string            `json:"warnings,omitempty"`
	Items      []SearchHitResponse `json:"items"`
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
// before the query is embedded. Semantic search failing is not an error:
// the keyword results come back Degraded, with Warnings.
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
	if err := validateSearchFilters(req.Filters); err != nil {
		return SearchResponse{}, err
	}

	start := time.Now()
	res, err := d.Search.Search(ctx, search.Request{
		TenantID: d.TenantID,
		Query:    req.Query,
		K:        req.K,
		Offset:   req.Offset,
		Filters: store.SearchFilters{
			ContentType: req.Filters.ContentType,
			Host:        req.Filters.Host,
			Source:      req.Filters.Source,
		},
	})
	if err != nil {
		return SearchResponse{}, err
	}

	items, err := d.searchHitsToResponse(ctx, res.Items)
	if err != nil {
		return SearchResponse{}, err
	}
	return SearchResponse{
		Query:      res.Query,
		TookMS:     time.Since(start).Milliseconds(),
		BM25Hits:   res.BM25Hits,
		VectorHits: res.VectorHits,
		Total:      res.Total,
		Capped:     res.Capped,
		Degraded:   res.Degraded,
		Warnings:   res.Warnings,
		Items:      items,
	}, nil
}

// validateSearchFilters refuses a content_type or source outside its set, as
// the lists refuse their filters: the engine would match nothing, and a typo
// would read as "no results". Hosts are literal input and match as given.
func validateSearchFilters(f Filters) error {
	for _, t := range f.ContentType {
		if !store.ContentType(t).Valid() {
			return badRequest("filters.content_type %q must be one of: %s", t, contentTypeList)
		}
	}
	for _, s := range f.Source {
		if !validSource(s) {
			return badRequest("filters.source %q must be one of: %s", s, sourceList)
		}
	}
	return nil
}

// searchHitsToResponse maps engine hits to wire hits, populating each
// hit's markdown path from its current extraction and naming the untitled
// ones by their bookmarks (bookmarkTitles).
//
// One extra DB hit per result to surface the markdown path. K is at most
// store.MaxSearchK (100), so this stays small; if it ever shows up in latency,
// batch via a single SELECT IN (...) instead.
func (d Deps) searchHitsToResponse(ctx context.Context, hits []search.Hit) ([]SearchHitResponse, error) {
	titles, err := d.bookmarkTitles(ctx, hits)
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
		})
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
