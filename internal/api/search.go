package api

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/samsar/curio/internal/search"
)

// SearchRequest is the POST /v1/search body.
type SearchRequest struct {
	Query   string   `json:"query"`
	K       int      `json:"k,omitempty"`
	Filters Filters  `json:"filters,omitempty"`
}

// Filters mirrors the openapi filters block. M0 honors none of these yet —
// they're accepted so clients can be written against the final shape.
type Filters struct {
	ContentType []string `json:"content_type,omitempty"`
	Host        []string `json:"host,omitempty"`
	Source      []string `json:"source,omitempty"`
	Folder      string   `json:"folder,omitempty"`
	Tag         []string `json:"tag,omitempty"`
}

// SearchHitResponse mirrors the openapi SearchHit schema.
type SearchHitResponse struct {
	Document DocumentResponse  `json:"document"`
	Score    float64           `json:"score"`
	Matches  []ChunkMatchJSON  `json:"matches,omitempty"`
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
	})
	if err != nil {
		writeError(w, err)
		return
	}

	resp := SearchResponse{
		Query:      res.Query,
		BM25Hits:   res.BM25Hits,
		VectorHits: res.VectorHits,
	}
	for _, hit := range res.Items {
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
		resp.Items = append(resp.Items, SearchHitResponse{
			Document: documentToResponse(hit.Document),
			Score:    hit.Score,
			Matches:  matches,
		})
	}
	writeJSON(w, http.StatusOK, resp)
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
