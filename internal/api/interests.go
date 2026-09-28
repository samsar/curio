package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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

// Sizes for the interest endpoints. The list previews a few members of each
// interest; the single interest shows many more.
const (
	defaultInterestLimit      = 50
	maxInterestLimit          = 500
	defaultInterestMembers    = 5
	maxInterestMembers        = 100
	defaultOneInterestMembers = 100
	maxOneInterestMembers     = 1000
)

func (d Deps) handleListInterests(w http.ResponseWriter, r *http.Request) {
	limit := intQuery(r, "limit", defaultInterestLimit, 1, maxInterestLimit)
	members := intQuery(r, "members", defaultInterestMembers, 0, maxInterestMembers)
	resp, err := d.interests(r.Context(), limit, members)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	d.writeJSON(w, r, http.StatusOK, resp)
}

// interests returns the current interests, the labeled clusters of the
// latest completed clustering run, up to limit of them with up to members
// members each. With no completed run yet it returns none, and no error.
func (d Deps) interests(ctx context.Context, limit, members int) (InterestListResponse, error) {
	run, err := d.Insights.LatestRun(ctx, d.TenantID, store.ClusterRunDone)
	if errors.Is(err, store.ErrNotFound) {
		return InterestListResponse{Items: []InterestResponse{}}, nil
	}
	if err != nil {
		return InterestListResponse{}, err
	}

	clusters, err := d.Insights.ListClusters(ctx, run.ID, limit)
	if err != nil {
		return InterestListResponse{}, err
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
		in, err := d.interestToResponse(ctx, c, members)
		if err != nil {
			return InterestListResponse{}, err
		}
		resp.Items = append(resp.Items, in)
	}
	return resp, nil
}

// handleGetInterest returns one interest (cluster) with its member documents.
func (d Deps) handleGetInterest(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	members := intQuery(r, "members", defaultOneInterestMembers, 0, maxOneInterestMembers)
	resp, err := d.interest(r.Context(), id, members)
	if err != nil {
		d.writeLookupError(w, r, "interest", id, err)
		return
	}
	d.writeJSON(w, r, http.StatusOK, resp)
}

// interest returns the tenant's interest id with up to members members.
// Another tenant's interest is an error wrapping store.ErrNotFound, as an
// unknown one is.
func (d Deps) interest(ctx context.Context, id string, members int) (InterestResponse, error) {
	c, err := d.Insights.GetCluster(ctx, id)
	if err != nil {
		return InterestResponse{}, err
	}
	if c.TenantID != d.TenantID {
		return InterestResponse{}, fmt.Errorf("interest %s: %w", id, store.ErrNotFound)
	}
	return d.interestToResponse(ctx, c, members)
}

// handleRebuildInterests enqueues a clustering job and returns 202 + job_id.
// Refused with 409 when the insight layer is disabled in config.
func (d Deps) handleRebuildInterests(w http.ResponseWriter, r *http.Request) {
	if !d.InsightEnabled {
		writeProblem(w, r, http.StatusConflict, "insight disabled",
			"the insight layer is disabled; set insight.enabled: true in config.yaml")
		return
	}
	job := &store.Job{
		TenantID: d.TenantID,
		Kind:     store.JobKindCluster,
		Payload:  json.RawMessage(`{}`),
	}
	if err := d.Queue.Enqueue(r.Context(), job); err != nil {
		d.writeError(w, r, err)
		return
	}
	d.writeJSON(w, r, http.StatusAccepted, map[string]string{"job_id": job.ID})
}

// interestToResponse maps a stored cluster + its top members to the wire shape,
// hydrating each member with title / url / on-disk markdown path (one extra DB
// hit per member, same pattern as search hits — fine for small member limits).
func (d Deps) interestToResponse(ctx context.Context, c *store.Cluster, membersLimit int) (InterestResponse, error) {
	out := InterestResponse{ID: c.ID, Size: c.Size, Cohesion: c.Cohesion}
	if c.Label != nil {
		out.Label = *c.Label
	}
	if c.Summary != nil {
		out.Summary = *c.Summary
	}
	if membersLimit <= 0 {
		return out, nil
	}

	members, err := d.Insights.ClusterMembers(ctx, c.ID, membersLimit)
	if err != nil {
		return InterestResponse{}, fmt.Errorf("interest %s: load members: %w", c.ID, err)
	}
	out.Members = make([]InterestMember, 0, len(members))
	for _, m := range members {
		im, err := d.interestMember(ctx, m)
		if err != nil {
			return InterestResponse{}, fmt.Errorf("interest %s: %w", c.ID, err)
		}
		out.Members = append(out.Members, im)
	}
	return out, nil
}

// interestMember hydrates one member with its document's title, URL and
// markdown path. Memberships cascade with their document, so a member
// whose document is missing is an inconsistency, not a missing resource.
func (d Deps) interestMember(ctx context.Context, m store.ClusterMember) (InterestMember, error) {
	doc, err := d.Documents.GetByID(ctx, m.DocumentID)
	if errors.Is(err, store.ErrNotFound) {
		return InterestMember{}, fmt.Errorf("member document %s doesn't exist", m.DocumentID)
	}
	if err != nil {
		return InterestMember{}, fmt.Errorf("load member document %s: %w", m.DocumentID, err)
	}
	path, err := d.documentMarkdownPath(ctx, doc)
	if err != nil {
		return InterestMember{}, err
	}
	im := InterestMember{DocID: doc.ID, URL: doc.URL, MarkdownPath: path, Similarity: m.Similarity}
	if doc.Title != nil {
		im.Title = *doc.Title
	}
	return im, nil
}
