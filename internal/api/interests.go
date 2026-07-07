package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/samsar/curio/internal/store"
)

// InterestMember is one document belonging to an interest (a labeled cluster).
type InterestMember struct {
	DocID        string  `json:"doc_id"`
	Title        string  `json:"title,omitempty"`
	URL          string  `json:"url"`
	MarkdownPath string  `json:"markdown_path,omitempty"`
	Similarity   float64 `json:"similarity"`
}

// InterestResponse is a labeled cluster surfaced to clients as an "interest".
type InterestResponse struct {
	ID       string           `json:"id"`
	Label    string           `json:"label,omitempty"`
	Summary  string           `json:"summary,omitempty"`
	Size     int              `json:"size"`
	Cohesion float64          `json:"cohesion"`
	Members  []InterestMember `json:"members,omitempty"`
}

// InterestListResponse is the body of GET /v1/interests: the current interests
// plus metadata about the run that produced them.
type InterestListResponse struct {
	RunID        string             `json:"run_id,omitempty"`
	ComputedAt   *time.Time         `json:"computed_at,omitempty"`
	Algo         string             `json:"algo,omitempty"`
	NumDocuments int                `json:"num_documents"`
	NumClusters  int                `json:"num_clusters"`
	NumNoise     int                `json:"num_noise"`
	Items        []InterestResponse `json:"items"`
}

const (
	defaultInterestLimit   = 50
	defaultInterestMembers = 5
	maxInterestMembers     = 100
)

// handleListInterests returns the current interests — the labeled clusters of
// the latest completed clustering run. Returns 200 with an empty list when no
// clustering has run yet.
func (d Deps) handleListInterests(w http.ResponseWriter, r *http.Request) {
	limit := intParam(r, "limit", defaultInterestLimit, 1, 500)
	members := intParam(r, "members", defaultInterestMembers, 0, maxInterestMembers)

	run, err := d.Insights.LatestRun(r.Context(), d.TenantID, store.ClusterRunDone)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusOK, InterestListResponse{Items: []InterestResponse{}})
			return
		}
		writeError(w, err)
		return
	}

	clusters, err := d.Insights.ListClusters(r.Context(), run.ID, limit)
	if err != nil {
		writeError(w, err)
		return
	}

	resp := InterestListResponse{
		RunID:        run.ID,
		ComputedAt:   run.FinishedAt,
		Algo:         run.Algo,
		NumDocuments: run.NumDocuments,
		NumClusters:  run.NumClusters,
		NumNoise:     run.NumNoise,
		Items:        make([]InterestResponse, 0, len(clusters)),
	}
	for _, c := range clusters {
		resp.Items = append(resp.Items, d.interestToResponse(r.Context(), c, members))
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleGetInterest returns one interest (cluster) with its member documents.
func (d Deps) handleGetInterest(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	members := intParam(r, "members", maxInterestMembers, 0, 1000)

	c, err := d.Insights.GetCluster(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	if c.TenantID != d.TenantID {
		writeProblem(w, http.StatusNotFound, "not found", "interest not found")
		return
	}
	writeJSON(w, http.StatusOK, d.interestToResponse(r.Context(), c, members))
}

// handleRebuildInterests enqueues a clustering job and returns 202 + job_id.
// Refused with 409 when the insight layer is disabled in config.
func (d Deps) handleRebuildInterests(w http.ResponseWriter, r *http.Request) {
	if !d.InsightEnabled {
		writeProblem(w, http.StatusConflict, "insight disabled",
			"the insight layer is disabled; set insight.enabled: true in config.yaml")
		return
	}
	job := &store.Job{
		TenantID: d.TenantID,
		Kind:     store.JobKindCluster,
		Payload:  json.RawMessage(`{}`),
	}
	if err := d.Queue.Enqueue(r.Context(), job); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job_id": job.ID})
}

// interestToResponse maps a stored cluster + its top members to the wire shape,
// hydrating each member with title / url / on-disk markdown path (one extra DB
// hit per member, same pattern as search hits — fine for small member limits).
func (d Deps) interestToResponse(ctx context.Context, c *store.Cluster, membersLimit int) InterestResponse {
	out := InterestResponse{ID: c.ID, Size: c.Size, Cohesion: c.Cohesion}
	if c.Label != nil {
		out.Label = *c.Label
	}
	if c.Summary != nil {
		out.Summary = *c.Summary
	}
	if membersLimit <= 0 {
		return out
	}

	members, err := d.Insights.ClusterMembers(ctx, c.ID, membersLimit)
	if err != nil {
		return out
	}
	out.Members = make([]InterestMember, 0, len(members))
	for _, m := range members {
		im := InterestMember{DocID: m.DocumentID, Similarity: m.Similarity}
		if doc, derr := d.Documents.GetByID(ctx, m.DocumentID); derr == nil {
			im.URL = doc.URL
			if doc.Title != nil {
				im.Title = *doc.Title
			}
			if doc.CurrentExtractionID != nil {
				if ext, eerr := d.Extractions.GetByID(ctx, *doc.CurrentExtractionID); eerr == nil && ext.MarkdownPath != nil {
					im.MarkdownPath = d.Home.ContentDir() + "/" + *ext.MarkdownPath
				}
			}
		}
		out.Members = append(out.Members, im)
	}
	return out
}

// intParam reads an int query param with a default and clamping to [lo, hi].
func intParam(r *http.Request, name string, def, lo, hi int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
