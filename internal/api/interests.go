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

// InterestMember is one document belonging to an interest (a labeled
// cluster). BookmarkTitle names an untitled one, as DocumentListItem's
// does.
type InterestMember struct {
	DocID         string  `json:"doc_id"`
	Title         string  `json:"title,omitempty"`
	BookmarkTitle string  `json:"bookmark_title,omitempty"`
	URL           string  `json:"url"`
	State         string  `json:"state"`
	MarkdownPath  string  `json:"markdown_path,omitempty"`
	Similarity    float64 `json:"similarity"`
}

// InterestResponse is a labeled cluster surfaced to clients as an
// "interest", with a page of its members. Size counts all of them.
type InterestResponse struct {
	ID       string           `json:"id"`
	RunID    string           `json:"run_id"`
	Label    string           `json:"label,omitempty"`
	Summary  string           `json:"summary,omitempty"`
	Size     int              `json:"size"`
	Cohesion float64          `json:"cohesion"`
	Members  []InterestMember `json:"members,omitempty"`
}

// InterestListResponse is the body of GET /v1/interests: a page of the
// current interests plus metadata about the run that produced them.
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
	offset, err := offsetParam(r)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	resp, err := d.interests(r.Context(), interestsOpts{
		Limit:   intQuery(r, "limit", defaultInterestLimit, 1, maxInterestLimit),
		Offset:  offset,
		Members: intQuery(r, "members", defaultInterestMembers, 0, maxInterestMembers),
	})
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	d.writeJSON(w, r, http.StatusOK, resp)
}

// interestsOpts are a page of the current interests.
type interestsOpts struct {
	Limit   int // interests on the page, at least 1
	Offset  int // interests before it, in their order
	Members int // members of each interest; 0 for none
}

// interests returns a page of the current interests, the labeled clusters
// of the latest completed clustering run, largest first, each with its
// most similar members. With no completed run yet it returns none, and no
// error. An offset past the run's interests is an empty page of that run.
func (d Deps) interests(ctx context.Context, opts interestsOpts) (InterestListResponse, error) {
	run, clusters, err := d.clusterPage(ctx, opts)
	if err == nil && len(clusters) == 0 && opts.Offset < run.NumClusters {
		// The run and its clusters are two reads. A rebuild that finished
		// between them has pruned the run, which left the page empty:
		// read it once more, from the newer run. A second miss is answered
		// as read, rather than chasing rebuilds.
		run, clusters, err = d.clusterPage(ctx, opts)
	}
	if errors.Is(err, store.ErrNotFound) {
		return InterestListResponse{Items: []InterestResponse{}}, nil
	}
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
		resp.Items = append(resp.Items, interestResponse(c))
	}
	if err := d.withMembers(ctx, resp.Items, opts.Members, 0); err != nil {
		return InterestListResponse{}, err
	}
	return resp, nil
}

// clusterPage reads the latest done run and the page of its clusters opts
// names. No run is an error wrapping store.ErrNotFound.
func (d Deps) clusterPage(ctx context.Context, opts interestsOpts) (*store.ClusterRun, []*store.Cluster, error) {
	run, err := d.Insights.LatestRun(ctx, d.TenantID, store.ClusterRunDone)
	if err != nil {
		return nil, nil, err
	}
	clusters, err := d.Insights.ListClusters(ctx, run.ID, opts.Limit, opts.Offset)
	if err != nil {
		return nil, nil, err
	}
	return run, clusters, nil
}

// handleGetInterest returns one interest (cluster) with a page of its
// member documents.
func (d Deps) handleGetInterest(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	offset, err := offsetParam(r)
	if err != nil {
		d.writeError(w, r, err)
		return
	}
	members := intQuery(r, "members", defaultOneInterestMembers, 0, maxOneInterestMembers)
	resp, err := d.interest(r.Context(), id, members, offset)
	if err != nil {
		d.writeLookupError(w, r, "interest", id, err)
		return
	}
	d.writeJSON(w, r, http.StatusOK, resp)
}

// interest returns the tenant's interest id with up to members of its
// members from offset, most similar first. Another tenant's interest is an
// error wrapping store.ErrNotFound, as an unknown one is.
func (d Deps) interest(ctx context.Context, id string, members, offset int) (InterestResponse, error) {
	c, err := d.Insights.GetCluster(ctx, id)
	if err != nil {
		return InterestResponse{}, err
	}
	if c.TenantID != d.TenantID {
		return InterestResponse{}, fmt.Errorf("interest %s: %w", id, store.ErrNotFound)
	}
	out := []InterestResponse{interestResponse(c)}
	// Size counts the members, so an offset at or past it has none to read.
	if offset < c.Size {
		if err := d.withMembers(ctx, out, members, offset); err != nil {
			return InterestResponse{}, err
		}
	}
	return out[0], nil
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

// interestResponse is a stored cluster in the wire shape, without its
// members.
func interestResponse(c *store.Cluster) InterestResponse {
	return InterestResponse{ID: c.ID, RunID: c.RunID, Label: deref(c.Label), Summary: deref(c.Summary),
		Size: c.Size, Cohesion: c.Cohesion}
}

// withMembers gives each of interests up to limit of its members from
// offset, most similar first: a read of each interest's members, then one
// read of all their documents, however many interests and members there
// are. With limit 0, or no members to show, it reads no document.
func (d Deps) withMembers(ctx context.Context, interests []InterestResponse, limit, offset int) error {
	if limit <= 0 {
		return nil
	}
	pages := make([][]store.ClusterMember, len(interests))
	var ids []string
	for i, in := range interests {
		members, err := d.Insights.ClusterMembers(ctx, in.ID, limit, offset)
		if err != nil {
			return fmt.Errorf("interest %s: load members: %w", in.ID, err)
		}
		pages[i] = members
		for _, m := range members {
			ids = append(ids, m.DocumentID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	docs, err := d.memberDocuments(ctx, ids)
	if err != nil {
		return err
	}
	for i := range interests {
		if interests[i].Members, err = d.interestMembers(pages[i], docs); err != nil {
			return fmt.Errorf("interest %s: %w", interests[i].ID, err)
		}
	}
	return nil
}

// memberDocuments reads the documents of members ids in one read, by ID.
func (d Deps) memberDocuments(ctx context.Context, ids []string) (map[string]store.DocumentWithError, error) {
	docs, err := d.Documents.GetByIDsWithLastError(ctx, d.TenantID, ids)
	if err != nil {
		return nil, fmt.Errorf("load member documents: %w", err)
	}
	byID := make(map[string]store.DocumentWithError, len(docs))
	for _, doc := range docs {
		byID[doc.ID] = doc
	}
	return byID, nil
}

// interestMembers are members in the wire shape, hydrated from docs, their
// documents by ID. Memberships cascade with their document, so a member
// whose document is missing is an inconsistency, not a missing resource.
func (d Deps) interestMembers(members []store.ClusterMember, docs map[string]store.DocumentWithError) ([]InterestMember, error) {
	out := make([]InterestMember, 0, len(members))
	for _, m := range members {
		doc, ok := docs[m.DocumentID]
		if !ok {
			return nil, fmt.Errorf("member document %s doesn't exist", m.DocumentID)
		}
		out = append(out, InterestMember{DocID: doc.ID, Title: deref(doc.Title), BookmarkTitle: doc.BookmarkTitle,
			URL: doc.URL, State: string(doc.State), MarkdownPath: d.contentPath(doc.MarkdownPath),
			Similarity: m.Similarity})
	}
	return out, nil
}
