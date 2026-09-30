package api

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

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

// seedRun records a finished run of tenant local whose clusters are
// clusters, as the engine does: the older runs are pruned.
func (s *testServer) seedRun(t *testing.T, clusters ...store.ClusterWithMembers) *store.ClusterRun {
	t.Helper()
	ctx := context.Background()
	run := &store.ClusterRun{TenantID: "local", Algo: "knn-graph"}
	require.NoError(t, s.deps.Insights.CreateRun(ctx, run))
	for i := range clusters {
		clusters[i].Cluster.TenantID = "local"
	}
	require.NoError(t, s.deps.Insights.ReplaceClusters(ctx, run.ID, clusters))
	require.NoError(t, s.deps.Insights.FinishRun(ctx, run.ID,
		store.RunResult{Status: store.ClusterRunDone, NumDocuments: 10, NumClusters: len(clusters), NumNoise: 1}))
	require.NoError(t, s.deps.Insights.PruneRunsExcept(ctx, "local", run.ID))
	return run
}

// getJSON gets path, which must answer 200, into v.
func (s *testServer) getJSON(t *testing.T, path string, v any) {
	t.Helper()
	resp := s.do(t, request{method: http.MethodGet, path: path})
	require.Equal(t, http.StatusOK, resp.status, "%s: %s", path, resp.body)
	require.NoError(t, json.Unmarshal([]byte(resp.body), v), path)
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
	_, err = s.deps.Bookmarks.Ingest(context.Background(), &store.Bookmark{TenantID: "local", URL: b.URL,
		Title: new("Saved as B"), Source: store.SourceChrome, SavedAt: time.Now().UTC()})
	require.NoError(t, err)
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
	assert.Equal(t, got.RunID, in.RunID, "the run it belongs to")
	assert.Equal(t, "Post A", in.Members[0].Title)
	assert.Empty(t, in.Members[0].BookmarkTitle, "a titled document needs no bookmark's")
	assert.Equal(t, "https://example.com/a", in.Members[0].URL)
	assert.Equal(t, "fetched", in.Members[0].State)
	assert.Equal(t, filepath.Join(s.deps.Home.ContentDir(), *ext.MarkdownPath), in.Members[0].MarkdownPath)
	assert.Empty(t, in.Members[1].MarkdownPath, "b has no content")
	assert.Equal(t, "Saved as B", in.Members[1].BookmarkTitle, "an untitled document is named by its bookmark")
	assert.Empty(t, in.Members[1].Title)

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
	assert.Equal(t, mine.RunID, got.RunID)
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

func (failingMembers) ClusterMembers(context.Context, string, int, int) ([]store.ClusterMember, error) {
	return nil, errInjected
}

// failingMemberDocuments fails the read of members' documents.
type failingMemberDocuments struct{ store.DocumentStore }

func (failingMemberDocuments) GetByIDsWithLastError(context.Context, string, []string) ([]store.DocumentWithError, error) {
	return nil, fmt.Errorf("get documents with error: %w", errInjected)
}

// missingMemberDocuments reads none of the members' documents, as if they
// were gone.
type missingMemberDocuments struct{ store.DocumentStore }

func (missingMemberDocuments) GetByIDsWithLastError(context.Context, string, []string) ([]store.DocumentWithError, error) {
	return nil, nil
}

// TestInterests_MemberLookupFailure: an interest whose members can't be
// read, or whose members' documents can't, is a server error, not an
// interest without members; a member whose document is missing is an
// inconsistency, never a 404 naming the interest.
func TestInterests_MemberLookupFailure(t *testing.T) {
	for name, tc := range map[string]struct {
		fail   func(*Deps)
		detail string
	}{
		"members":   {func(d *Deps) { d.Insights = failingMembers{d.Insights} }, errInjected.Error()},
		"documents": {func(d *Deps) { d.Documents = failingMemberDocuments{d.Documents} }, errInjected.Error()},
		"missing":   {func(d *Deps) { d.Documents = missingMemberDocuments{d.Documents} }, "doesn't exist"},
	} {
		t.Run(name, func(t *testing.T) {
			s := newTestServer(t, tc.fail)
			a := s.seedDocument(t, "https://example.com/a", store.DocStateFetched)
			c := s.seedInterest(t, "local", "Go", a)
			for _, path := range []string{"/v1/interests", "/v1/interests/" + c.ID} {
				p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: path}), http.StatusInternalServerError)
				assert.Contains(t, p.Detail, tc.detail, path)
				if name == "missing" {
					assert.Contains(t, p.Detail, "member document "+a.ID+" doesn't exist", path)
				}
			}
			resp := s.do(t, request{method: http.MethodGet, path: "/v1/interests?members=0"})
			assert.Equal(t, http.StatusOK, resp.status, "no members asked for, none looked up: %s", resp.body)
		})
	}
}

// TestInterests_Pages: pages of interests read from successive offsets
// add up to the whole list, and pages of an interest's members to all its
// members, ties on size, cohesion and similarity broken the same way on
// every read.
func TestInterests_Pages(t *testing.T) {
	s := newTestServer(t)
	docs := make([]*store.Document, 0, 5)
	for _, u := range []string{"a", "b", "c", "d", "e"} {
		docs = append(docs, s.seedDocument(t, "https://example.com/"+u, store.DocStateFetched))
	}
	members := make([]store.ClusterMember, 0, len(docs))
	for i, d := range docs {
		members = append(members, store.ClusterMember{DocumentID: d.ID, Similarity: []float64{0.9, 0.5, 0.5, 0.7, 0.5}[i]})
	}
	cluster := func(id string, size int, cohesion float64) store.ClusterWithMembers {
		return store.ClusterWithMembers{Cluster: store.Cluster{ID: id, Size: size, Cohesion: cohesion}}
	}
	big := cluster("big", len(members), 0.5)
	big.Members = members
	run := s.seedRun(t, cluster("tie-c", 2, 0.5), big, cluster("tie-a", 2, 0.5), cluster("small", 1, 0.9),
		cluster("tie-b", 2, 0.5), cluster("cohesive", 2, 0.8))

	ids := func(items []InterestResponse) []string {
		out := make([]string, 0, len(items))
		for _, in := range items {
			out = append(out, in.ID)
		}
		return out
	}
	var all InterestListResponse
	s.getJSON(t, "/v1/interests?limit=500&members=0", &all)
	assert.Equal(t, []string{"big", "cohesive", "tie-a", "tie-b", "tie-c", "small"}, ids(all.Items))
	var paged []InterestResponse
	for offset := 0; offset <= len(all.Items); offset += 2 {
		var page InterestListResponse
		s.getJSON(t, "/v1/interests?members=0&limit=2&offset="+strconv.Itoa(offset), &page)
		assert.Equal(t, run.ID, page.RunID)
		paged = append(paged, page.Items...)
	}
	assert.Equal(t, ids(all.Items), ids(paged), "the pages add up to the list")

	memberIDs := func(in InterestResponse) []string {
		out := make([]string, 0, len(in.Members))
		for _, m := range in.Members {
			out = append(out, m.DocID)
		}
		return out
	}
	var whole InterestResponse
	s.getJSON(t, "/v1/interests/big", &whole)
	ties := slices.Sorted(slices.Values([]string{docs[1].ID, docs[2].ID, docs[4].ID}))
	assert.Equal(t, append([]string{docs[0].ID, docs[3].ID}, ties...), memberIDs(whole))
	var pagedMembers []string
	for offset := 0; offset <= len(members); offset += 2 {
		var page InterestResponse
		s.getJSON(t, "/v1/interests/big?members=2&offset="+strconv.Itoa(offset), &page)
		assert.Equal(t, len(members), page.Size, "the size counts every member")
		pagedMembers = append(pagedMembers, memberIDs(page)...)
	}
	assert.Equal(t, memberIDs(whole), pagedMembers, "the pages add up to the members")

	// Past the end: an empty page of the run, which says what the run holds.
	resp := s.do(t, request{method: http.MethodGet, path: "/v1/interests?offset=6"})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	var past map[string]any
	require.NoError(t, json.Unmarshal([]byte(resp.body), &past))
	assert.Equal(t, []any{}, past["items"])
	assert.Equal(t, run.ID, past["run_id"])
	for _, field := range []string{"computed_at", "algo"} {
		assert.NotEmpty(t, past[field], field)
	}
	assert.InDelta(t, 10, past["num_documents"], 0)
	assert.InDelta(t, 6, past["num_clusters"], 0)
	assert.InDelta(t, 1, past["num_noise"], 0)
	resp = s.do(t, request{method: http.MethodGet, path: "/v1/interests/big?offset=5"})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	assert.NotContains(t, resp.body, `"members"`)
	assert.Contains(t, resp.body, `"size":5`)
}

// TestInterests_BadOffset: an offset that isn't a whole number of 0 or
// more is a 400 naming it, answered before anything is read.
func TestInterests_BadOffset(t *testing.T) {
	runs, interests := new(atomic.Int32), new(atomic.Int32)
	s := newTestServer(t, func(d *Deps) { d.Insights = countingReads{d.Insights, runs, interests} })
	c := s.seedInterest(t, "local", "Go", s.seedDocument(t, "https://example.com/a", store.DocStateFetched))
	for _, offset := range []string{"-1", "1.5", "x", "99999999999999999999", "0x10"} {
		for _, path := range []string{"/v1/interests", "/v1/interests/" + c.ID} {
			p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: path + "?offset=" + offset}),
				http.StatusBadRequest)
			assert.Contains(t, p.Detail, `offset "`+offset+`"`, path)
		}
	}
	assert.Zero(t, runs.Load(), "no run read")
	assert.Zero(t, interests.Load(), "no interest read")

	for _, offset := range []string{"", "0"} {
		var got InterestListResponse
		s.getJSON(t, "/v1/interests?offset="+offset, &got)
		assert.Len(t, got.Items, 1, "offset %q is the start", offset)
	}
}

// countingReads counts the latest run's reads and the interest reads, a
// cluster's and a page of clusters.
type countingReads struct {
	store.InsightStore
	runs, interests *atomic.Int32
}

func (c countingReads) LatestRun(ctx context.Context, tenantID string, status store.ClusterRunStatus) (*store.ClusterRun, error) {
	c.runs.Add(1)
	return c.InsightStore.LatestRun(ctx, tenantID, status)
}

func (c countingReads) GetCluster(ctx context.Context, id string) (*store.Cluster, error) {
	c.interests.Add(1)
	return c.InsightStore.GetCluster(ctx, id)
}

func (c countingReads) ListClusters(ctx context.Context, runID string, limit, offset int) ([]*store.Cluster, error) {
	c.interests.Add(1)
	return c.InsightStore.ListClusters(ctx, runID, limit, offset)
}

// TestInterests_RunID: every interest names the run it belongs to, so a
// client paging through the list sees a rebuild between two pages.
func TestInterests_RunID(t *testing.T) {
	s := newTestServer(t)
	first := s.seedRun(t, store.ClusterWithMembers{Cluster: store.Cluster{ID: "a1", Size: 2}},
		store.ClusterWithMembers{Cluster: store.Cluster{ID: "a2", Size: 1}})
	var page InterestListResponse
	s.getJSON(t, "/v1/interests?limit=1", &page)
	assert.Equal(t, first.ID, page.RunID)
	require.Len(t, page.Items, 1)
	assert.Equal(t, first.ID, page.Items[0].RunID)

	rebuilt := s.seedRun(t, store.ClusterWithMembers{Cluster: store.Cluster{ID: "b1", Size: 2}},
		store.ClusterWithMembers{Cluster: store.Cluster{ID: "b2", Size: 1}})
	s.getJSON(t, "/v1/interests?limit=1&offset=1", &page)
	assert.Equal(t, rebuilt.ID, page.RunID, "the second page is the new run's")
	require.Len(t, page.Items, 1)
	assert.Equal(t, rebuilt.ID, page.Items[0].RunID)
	assert.NotEqual(t, first.ID, page.RunID)

	var one InterestResponse
	s.getJSON(t, "/v1/interests/b2", &one)
	assert.Equal(t, rebuilt.ID, one.RunID)
}

// prunedMidRead is the insight store as a read sees it while a rebuild
// finishes: the first latest done run it answers, pruned, has no clusters
// left by the time they are read; the reads after it see the rebuild's,
// unless the rebuilds go on.
type prunedMidRead struct {
	store.InsightStore
	pruned      *store.ClusterRun
	ongoing     bool // every read of the latest run answers a pruned one
	latestReads *atomic.Int32
}

func (p prunedMidRead) LatestRun(ctx context.Context, tenantID string, status store.ClusterRunStatus) (*store.ClusterRun, error) {
	if p.latestReads.Add(1) == 1 || p.ongoing {
		return p.pruned, nil
	}
	return p.InsightStore.LatestRun(ctx, tenantID, status)
}

func (p prunedMidRead) ListClusters(ctx context.Context, runID string, limit, offset int) ([]*store.Cluster, error) {
	if runID == p.pruned.ID {
		return nil, nil
	}
	return p.InsightStore.ListClusters(ctx, runID, limit, offset)
}

// TestInterests_PrunedMidRead: a page the latest run holds, read while a
// rebuild prunes that run, is read once more, from the rebuild's run; a
// second miss is answered as read, and a page past the run's end, which
// has no clusters anyway, is never read again.
func TestInterests_PrunedMidRead(t *testing.T) {
	pruned := &store.ClusterRun{ID: "run-a", Algo: "knn-graph", NumClusters: 3}
	for _, tc := range []struct {
		name    string
		path    string
		ongoing bool
		run     func(rebuilt *store.ClusterRun) string
		items   int
		latest  int32
	}{
		{"rebuilt", "/v1/interests?limit=1&offset=1", false, func(r *store.ClusterRun) string { return r.ID }, 1, 2},
		{"rebuilding", "/v1/interests?limit=1&offset=1", true, func(*store.ClusterRun) string { return "run-a" }, 0, 2},
		{"past the end", "/v1/interests?offset=3", false, func(*store.ClusterRun) string { return "run-a" }, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			latest := new(atomic.Int32)
			s := newTestServer(t, func(d *Deps) {
				d.Insights = prunedMidRead{InsightStore: d.Insights, pruned: pruned, ongoing: tc.ongoing, latestReads: latest}
			})
			rebuilt := s.seedRun(t, store.ClusterWithMembers{Cluster: store.Cluster{ID: "b1", Size: 2}},
				store.ClusterWithMembers{Cluster: store.Cluster{ID: "b2", Size: 1}})
			var got InterestListResponse
			s.getJSON(t, tc.path, &got)
			assert.Equal(t, tc.run(rebuilt), got.RunID)
			assert.Len(t, got.Items, tc.items)
			for _, in := range got.Items {
				assert.Equal(t, rebuilt.ID, in.RunID)
			}
			assert.Equal(t, tc.latest, latest.Load(), "reads of the latest run")
		})
	}
}

// memberReads counts the reads of interests' members and of documents:
// the batched read of members' documents, and the one-by-one reads of a
// document and of an extraction that the members must never need.
type memberReads struct {
	members, batches, documents, extractions atomic.Int32
}

type memberReadInsights struct {
	store.InsightStore
	n *memberReads
}

func (i memberReadInsights) ClusterMembers(ctx context.Context, clusterID string, limit, offset int) ([]store.ClusterMember, error) {
	i.n.members.Add(1)
	return i.InsightStore.ClusterMembers(ctx, clusterID, limit, offset)
}

type memberReadDocuments struct {
	store.DocumentStore
	n *memberReads
}

func (d memberReadDocuments) GetByIDsWithLastError(ctx context.Context, tenantID string, ids []string) ([]store.DocumentWithError, error) {
	d.n.batches.Add(1)
	return d.DocumentStore.GetByIDsWithLastError(ctx, tenantID, ids)
}

func (d memberReadDocuments) GetByID(ctx context.Context, id string) (*store.Document, error) {
	d.n.documents.Add(1)
	return d.DocumentStore.GetByID(ctx, id)
}

type memberReadExtractions struct {
	store.ExtractionStore
	n *memberReads
}

func (e memberReadExtractions) GetByID(ctx context.Context, id string) (*store.DocumentExtraction, error) {
	e.n.extractions.Add(1)
	return e.ExtractionStore.GetByID(ctx, id)
}

// TestInterests_MemberReads: a page of interests reads each interest's
// members, then all their documents at once, however many interests and
// members; never a document or an extraction one by one. With no members
// asked for, or none to show, it reads no document.
func TestInterests_MemberReads(t *testing.T) {
	n := &memberReads{}
	s := newTestServer(t, func(d *Deps) {
		d.Insights = memberReadInsights{d.Insights, n}
		d.Documents = memberReadDocuments{d.Documents, n}
		d.Extractions = memberReadExtractions{d.Extractions, n}
	})
	clusters := make([]store.ClusterWithMembers, 0, 3)
	for i := range 3 {
		c := store.ClusterWithMembers{Cluster: store.Cluster{ID: "c" + strconv.Itoa(i), Size: 2}}
		for j := range 2 {
			doc := s.seedDocument(t, fmt.Sprintf("https://example.com/%d/%d", i, j), store.DocStateFetched)
			s.seedContent(t, doc, "# text")
			c.Members = append(c.Members, store.ClusterMember{DocumentID: doc.ID, Similarity: 0.5})
		}
		clusters = append(clusters, c)
	}
	s.seedRun(t, append(clusters, store.ClusterWithMembers{Cluster: store.Cluster{ID: "empty", Size: 1}})...)

	for _, tc := range []struct {
		path             string
		members, batches int32
	}{
		{"/v1/interests?limit=3&members=2", 3, 1},
		{"/v1/interests?limit=4&members=100", 4, 1},
		{"/v1/interests/c1", 1, 1},
		{"/v1/interests?members=0", 0, 0},
		{"/v1/interests/c1?members=0", 0, 0},
		{"/v1/interests?limit=1&offset=3", 1, 0},
		{"/v1/interests/c1?offset=2", 0, 0},
	} {
		*n = memberReads{}
		resp := s.do(t, request{method: http.MethodGet, path: tc.path})
		require.Equal(t, http.StatusOK, resp.status, "%s: %s", tc.path, resp.body)
		assert.Equal(t, tc.members, n.members.Load(), "%s: members' reads", tc.path)
		assert.Equal(t, tc.batches, n.batches.Load(), "%s: documents' reads", tc.path)
		assert.Zero(t, n.documents.Load(), "%s: no document read one by one", tc.path)
		assert.Zero(t, n.extractions.Load(), "%s: no extraction read", tc.path)
	}
	var page InterestListResponse
	s.getJSON(t, "/v1/interests?limit=3&members=2", &page)
	for _, in := range page.Items {
		require.Len(t, in.Members, 2, in.ID)
		for _, m := range in.Members {
			assert.NotEmpty(t, m.MarkdownPath, "hydrated from the batch")
		}
	}
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
