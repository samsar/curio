package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// seedInterest records a finished run with one cluster over docs, for tenant.
func (s *testServer) seedInterest(t *testing.T, tenant, label string, docs ...*store.Document) *store.Cluster {
	t.Helper()
	ctx := context.Background()
	run := &store.ClusterRun{TenantID: tenant, Algo: "knn-graph"}
	require.NoError(t, s.deps.Insights.CreateRun(ctx, run))
	c := store.Cluster{ID: uuid.NewString(), TenantID: tenant, RunID: run.ID, Label: &label, Size: len(docs), Cohesion: 0.8}
	members := make([]store.ClusterMember, len(docs))
	for i, d := range docs {
		members[i] = store.ClusterMember{DocumentID: d.ID, Similarity: 0.9 - 0.1*float64(i)}
	}
	require.NoError(t, s.deps.Insights.ReplaceClusters(ctx, run.ID, []store.ClusterWithMembers{{Cluster: c, Members: members}}))
	require.NoError(t, s.deps.Insights.FinishRun(ctx, run.ID,
		store.RunResult{Status: store.ClusterRunDone, NumDocuments: len(docs), NumClusters: 1}))
	return &c
}

func TestListInterests(t *testing.T) {
	s := newTestServer(t)

	resp := s.do(t, request{method: http.MethodGet, path: "/v1/interests"})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	assert.JSONEq(t, `{"num_documents":0,"num_clusters":0,"num_noise":0,"items":[]}`, resp.body,
		"no clustering yet: an empty list, not an error")

	a := s.seedDocument(t, "https://example.com/a", store.DocStateFetched)
	title := "Post A"
	_, err := s.db.Exec(`UPDATE documents SET title = ? WHERE id = ?`, title, a.ID)
	require.NoError(t, err)
	ext := s.seedContent(t, a, "# A")
	b := s.seedDocument(t, "https://example.com/b", store.DocStateFetched)
	s.seedInterest(t, "local", "Go", a, b)

	resp = s.do(t, request{method: http.MethodGet, path: "/v1/interests?members=5"})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	var got InterestListResponse
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	assert.Equal(t, "knn-graph", got.Algo)
	assert.NotNil(t, got.ComputedAt)
	assert.Equal(t, 2, got.NumDocuments)
	require.Len(t, got.Items, 1)
	in := got.Items[0]
	assert.Equal(t, "Go", in.Label)
	assert.Equal(t, 2, in.Size)
	require.Len(t, in.Members, 2)
	assert.Equal(t, a.ID, in.Members[0].DocID, "most similar first")
	assert.Equal(t, "Post A", in.Members[0].Title)
	assert.Equal(t, "https://example.com/a", in.Members[0].URL)
	assert.Equal(t, filepath.Join(s.deps.Home.ContentDir(), *ext.MarkdownPath), in.Members[0].MarkdownPath)
	assert.Empty(t, in.Members[1].MarkdownPath, "b has no content")

	resp = s.do(t, request{method: http.MethodGet, path: "/v1/interests?members=0"})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	assert.NotContains(t, resp.body, `"members"`)

	// Sizing parameters out of range mean their default, as for ?limit.
	for _, members := range []string{"101", "-1", "many"} {
		resp = s.do(t, request{method: http.MethodGet, path: "/v1/interests?members=" + members})
		require.Equal(t, http.StatusOK, resp.status, resp.body)
		require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
		assert.Len(t, got.Items[0].Members, 2, "members=%s: the default 5 covers both", members)
	}
}

func TestGetInterest(t *testing.T) {
	s := newTestServer(t)
	a := s.seedDocument(t, "https://example.com/a", store.DocStateFetched)
	mine := s.seedInterest(t, "local", "Go", a)
	theirs := s.seedInterest(t, "other", "Rust", a)

	resp := s.do(t, request{method: http.MethodGet, path: "/v1/interests/" + mine.ID})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	var got InterestResponse
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	assert.Equal(t, "Go", got.Label)
	require.Len(t, got.Members, 1)

	p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/interests/no-such-interest"}),
		http.StatusNotFound)
	assert.Equal(t, `interest "no-such-interest" not found`, p.Detail)
	p = assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/interests/" + theirs.ID}),
		http.StatusNotFound)
	assert.Equal(t, `interest "`+theirs.ID+`" not found`, p.Detail, "another tenant's interest doesn't exist here")
}

// failingMembers fails every cluster-member lookup.
type failingMembers struct{ store.InsightStore }

func (failingMembers) ClusterMembers(context.Context, string, int) ([]store.ClusterMember, error) {
	return nil, errInjected
}

// TestInterests_MemberLookupFailure: an interest whose members can't be read
// is a server error, not an interest without members.
func TestInterests_MemberLookupFailure(t *testing.T) {
	s := newTestServer(t, func(d *Deps) { d.Insights = failingMembers{d.Insights} })
	c := s.seedInterest(t, "local", "Go", s.seedDocument(t, "https://example.com/a", store.DocStateFetched))

	for _, path := range []string{"/v1/interests", "/v1/interests/" + c.ID} {
		p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: path}), http.StatusInternalServerError)
		assert.Contains(t, p.Detail, errInjected.Error(), path)
	}
	resp := s.do(t, request{method: http.MethodGet, path: "/v1/interests?members=0"})
	assert.Equal(t, http.StatusOK, resp.status, "no members asked for, none looked up: %s", resp.body)
}

// nanCohesion reports a cluster whose cohesion JSON can't represent.
type nanCohesion struct{ store.InsightStore }

func (n nanCohesion) GetCluster(ctx context.Context, id string) (*store.Cluster, error) {
	c, err := n.InsightStore.GetCluster(ctx, id)
	if err != nil {
		return nil, err
	}
	c.Cohesion = math.NaN()
	return c, nil
}

// TestInterests_UnencodableResponse: a response that can't be encoded is a
// 500 problem, not a 200 with an empty body.
func TestInterests_UnencodableResponse(t *testing.T) {
	s := newTestServer(t, func(d *Deps) { d.Insights = nanCohesion{d.Insights} })
	c := s.seedInterest(t, "local", "Go", s.seedDocument(t, "https://example.com/a", store.DocStateFetched))

	p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/interests/" + c.ID}),
		http.StatusInternalServerError)
	assert.Contains(t, p.Detail, "NaN")
}

func TestRebuildInterests(t *testing.T) {
	s := newTestServer(t)
	resp := s.do(t, request{method: http.MethodPost, path: "/v1/interests/rebuild"})
	require.Equal(t, http.StatusAccepted, resp.status, resp.body)
	assert.Equal(t, 1, s.count(t, "jobs"))

	disabled := newTestServer(t, func(d *Deps) { d.InsightEnabled = false })
	assertProblem(t, disabled.do(t, request{method: http.MethodPost, path: "/v1/interests/rebuild"}), http.StatusConflict)
	assert.Zero(t, disabled.count(t, "jobs"))
}
