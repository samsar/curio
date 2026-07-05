package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
)

// SearchRequest is the POST /v1/search body.
type SearchRequest struct {
	Query   string  `json:"query"`
	K       int     `json:"k,omitempty"`
	Filters Filters `json:"filters,omitempty"`
}

// Filters mirrors the openapi filters block. content_type/host/source are
// applied by the search engine; folder/tag are accepted but not yet applied.
type Filters struct {
	ContentType []string `json:"content_type,omitempty"`
	Host        []string `json:"host,omitempty"`
	Source      []string `json:"source,omitempty"`
	Folder      string   `json:"folder,omitempty"`
	Tag         []string `json:"tag,omitempty"`
}

// SearchHitResponse mirrors the openapi SearchHit schema. MarkdownPath
// is the absolute on-disk path to the extracted markdown, populated from
// the doc's current extraction. Empty when there's no extraction yet.
// Surfaced here so CLI consumers can `cat` / open the file without a
// second round-trip to /v1/documents/{id}.
type SearchHitResponse struct {
	Document     DocumentResponse `json:"document"`
	Score        float64          `json:"score"`
	MarkdownPath string           `json:"markdown_path,omitempty"`
	Matches      []ChunkMatchJSON `json:"matches,omitempty"`
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

// SearchResponse mirrors the openapi SearchResponse schema.
type SearchResponse struct {
	Query      string              `json:"query"`
	TookMS     int64               `json:"took_ms"`
	BM25Hits   int                 `json:"bm25_hits"`
	VectorHits int                 `json:"vector_hits"`
	Items      []SearchHitResponse `json:"items"`
}

func (d Deps) handleSearch(w http.ResponseWriter, r *http.Request) {
	var req SearchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeProblem(w, http.StatusBadRequest, "bad request", err.Error())
		return
	}
	if req.Query == "" {
		writeProblem(w, http.StatusBadRequest, "bad request", "query is required")
		return
	}

	res, err := d.Search.Search(r.Context(), search.Request{
		TenantID: d.TenantID,
		Query:    req.Query,
		K:        req.K,
		Filters: store.SearchFilters{
			ContentType: req.Filters.ContentType,
			Host:        req.Filters.Host,
			Source:      req.Filters.Source,
		},
	})
	if err != nil {
		writeError(w, err)
		return
	}

	resp := SearchResponse{
		Query:      res.Query,
		BM25Hits:   res.BM25Hits,
		VectorHits: res.VectorHits,
		Items:      d.searchHitsToResponse(r.Context(), res.Items),
	}
	writeJSON(w, http.StatusOK, resp)
}

// searchHitsToResponse maps engine hits to wire hits, populating each
// hit's markdown path from its current extraction.
//
// One extra DB hit per result to surface the markdown path. For typical K
// (10–50) this is negligible; if it ever shows up in latency, batch via a
// single SELECT IN (...) instead.
func (d Deps) searchHitsToResponse(ctx context.Context, hits []search.Hit) []SearchHitResponse {
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

		var mdPath string
		if hit.Document.CurrentExtractionID != nil {
			if ext, err := d.Extractions.GetByID(ctx, *hit.Document.CurrentExtractionID); err == nil &&
				ext.MarkdownPath != nil {
				mdPath = d.Home.ContentDir() + "/" + *ext.MarkdownPath
			}
		}

		out = append(out, SearchHitResponse{
			Document:     documentToResponse(hit.Document),
			Score:        hit.Score,
			MarkdownPath: mdPath,
			Matches:      matches,
		})
	}
	return out
}

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
	k := 10
	if v := r.URL.Query().Get("k"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			k = n
		}
	}

	start := time.Now()
	res, err := d.Search.Related(r.Context(), search.RelatedRequest{
		TenantID:   d.TenantID,
		DocumentID: id,
		K:          k,
	})
	if err != nil {
		writeError(w, err)
		return
	}

	writeJSON(w, http.StatusOK, RelatedResponse{
		DocID:  id,
		TookMS: time.Since(start).Milliseconds(),
		Items:  d.searchHitsToResponse(r.Context(), res.Items),
	})
}

// decodeMetaJSON parses extraction_meta back into a map; tolerant of
// missing/invalid data.
func decodeMetaJSON(raw []byte, out *map[string]any) error {
	return json.Unmarshal(raw, out)
}

// copyAll wraps io.Copy and returns just the error.
func copyAll(w io.Writer, r io.Reader) (int64, error) {
	return io.Copy(w, r)
}
