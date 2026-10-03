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
	"github.com/samsar/curio/internal/store/sqlite"
)

// runFixture builds a run's commit on the test server's store: the
// identities it mints or carries, its groups and assignments, its lineage.
type runFixture struct {
	s       *testServer
	tenant  string
	c       store.RunCommit
	grouped int
}

// newRun starts a run of the local tenant in shape, built on its latest
// done run.
func (s *testServer) newRun(t *testing.T, shape store.InterestShape) *runFixture {
	t.Helper()
	return s.newTenantRun(t, "local", shape)
}

func (s *testServer) newTenantRun(t *testing.T, tenant string, shape store.InterestShape) *runFixture {
	t.Helper()
	ctx := context.Background()
	run := &store.InterestRun{TenantID: tenant, Trigger: store.RunTriggerManual, Grouper: "test",
		RunOutcome: store.RunOutcome{Kind: store.RunKindFresh, Shape: shape}}
	require.NoError(t, s.insights().CreateRun(ctx, run))
	f := &runFixture{s: s, tenant: tenant, c: store.RunCommit{RunID: run.ID, TenantID: tenant,
		Outcome: store.RunOutcome{Kind: store.RunKindFresh, Shape: shape}}}
	if prior, err := s.insights().LatestRun(ctx, tenant, store.InterestRunDone); err == nil {
		f.c.PriorRunID, f.c.Outcome.Kind = prior.ID, store.RunKindWarm
	} else {
		require.ErrorIs(t, err, store.ErrNotFound)
	}
	return f
}

// area adds an area, minted with label unless carried, and returns its
// ID; its size and loose fits are its interests', given later.
func (f *runFixture) area(label string, carried ...string) string {
	id := f.identity(store.InterestLevelArea, label, carried...)
	f.c.Groups = append(f.c.Groups, store.InterestGroup{Interest: store.Interest{ID: id}, Cohesion: 0.4})
	f.c.Outcome.NumAreas++
	return id
}

// interest adds an interest of size members (at least len(members)), in
// area ("" for none), its members each less similar than the one before,
// and its loose fits; minted with label unless carried. It returns its ID.
func (f *runFixture) interest(label, area string, size int, members, loose []*store.Document, carried ...string) string {
	id := f.identity(store.InterestLevelInterest, label, carried...)
	size = max(size, len(members))
	f.c.Groups = append(f.c.Groups, store.InterestGroup{Interest: store.Interest{ID: id}, ParentID: area,
		Size: size, Loose: len(loose), Cohesion: 0.8})
	for i := range f.c.Groups {
		if f.c.Groups[i].ID == area {
			f.c.Groups[i].Size += size
			f.c.Groups[i].Loose += len(loose)
		}
	}
	for i, d := range members {
		f.c.Assignments = append(f.c.Assignments, store.InterestAssignment{DocumentID: d.ID, InterestID: id, AreaID: area,
			Fit: store.InterestFitMember, Similarity: 0.9 - 0.1*float64(i), AreaSeed: -1, InterestSeed: -1})
	}
	for i, d := range loose {
		f.c.Assignments = append(f.c.Assignments, store.InterestAssignment{DocumentID: d.ID, InterestID: id,
			Fit: store.InterestFitLoose, Similarity: 0.5 - 0.01*float64(i), AreaSeed: -1, InterestSeed: -1})
	}
	f.grouped += size
	f.c.Outcome.NumInterests++
	f.c.Outcome.NumLoose += len(loose)
	return id
}

// unsorted adds documents in no interest, nearest is nearest, each less
// similar to it than the one before.
func (f *runFixture) unsorted(nearest string, docs ...*store.Document) {
	for i, d := range docs {
		f.c.Assignments = append(f.c.Assignments, store.InterestAssignment{DocumentID: d.ID, Fit: store.InterestFitUnsorted,
			Similarity: 0.3 - 0.01*float64(i), NearestID: nearest, AreaSeed: -1, InterestSeed: -1})
	}
	f.c.Outcome.NumUnsorted += len(docs)
}

// lineage records what the run did to old toward next.
func (f *runFixture) lineage(old, next string, event store.LineageEvent, shared int) {
	f.c.Lineage = append(f.c.Lineage, store.LineageRow{OldID: old, NewID: next, Event: event, Shared: shared})
}

// identity mints a labeled identity, or carries one when carried names it.
func (f *runFixture) identity(level store.InterestLevel, label string, carried ...string) string {
	if len(carried) > 0 {
		return carried[0]
	}
	in := store.Interest{ID: uuid.NewString(), Level: level, Label: label}
	if label != "" {
		at := time.Now().UTC()
		in.Summary, in.LabelSource, in.LabeledAt = label+", in a sentence.", store.LabelSourceLLM, &at
	}
	f.c.NewIdentities = append(f.c.NewIdentities, in)
	return in.ID
}

// commit commits the run, prunes the runs before it as the engine does,
// and returns its ID.
func (f *runFixture) commit(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	f.c.Outcome.NumDocuments = f.grouped + f.c.Outcome.NumLoose + f.c.Outcome.NumUnsorted
	f.c.Outcome.Created = len(f.c.NewIdentities)
	require.NoError(t, f.s.insights().CommitRun(ctx, f.c))
	require.NoError(t, f.s.insights().PruneRunsExcept(ctx, f.tenant, f.c.RunID))
	return f.c.RunID
}

// seedInterest commits a flat run of tenant with one interest over docs,
// and returns the interest's ID.
func (s *testServer) seedInterest(t *testing.T, tenant, label string, docs ...*store.Document) string {
	t.Helper()
	f := s.newTenantRun(t, tenant, store.InterestShapeFlat)
	id := f.interest(label, "", 0, docs, nil)
	f.commit(t)
	return id
}

// seedFailedRun records a rebuild that failed with msg, the newest run.
func (s *testServer) seedFailedRun(t *testing.T, msg string) string {
	t.Helper()
	ctx := context.Background()
	run := &store.InterestRun{TenantID: "local", Trigger: store.RunTriggerManual, Grouper: "test",
		RunOutcome: store.RunOutcome{Kind: store.RunKindWarm, Shape: store.InterestShapeFlat}}
	require.NoError(t, s.insights().CreateRun(ctx, run))
	require.NoError(t, s.insights().FailRun(ctx, run.ID, 0, msg))
	return run.ID
}

// insights is the test server's insight store, unwrapped: fixtures are
// written as the engine writes them, whatever a test wraps the server's
// store in.
func (s *testServer) insights() store.InsightStore { return sqlite.NewInsights(s.db) }

// placeDocument records doc placed into run's interest ("" for Unsorted)
// since the run.
func (s *testServer) placeDocument(t *testing.T, run, interest string, doc *store.Document) {
	t.Helper()
	var into any
	if interest != "" {
		into = interest
	}
	_, err := s.db.Exec(`INSERT INTO interest_placements (run_id, document_id, interest_id, similarity, placed_at)
		VALUES (?, ?, ?, 0.6, strftime('%Y-%m-%dT%H:%M:%fZ','now'))`, run, doc.ID, into)
	require.NoError(t, err)
}

// docs seeds n fetched documents under prefix.
func (s *testServer) docs(t *testing.T, prefix string, n int) []*store.Document {
	t.Helper()
	out := make([]*store.Document, n)
	for i := range out {
		out[i] = s.seedDocument(t, fmt.Sprintf("https://example.com/%s/%d", prefix, i), store.DocStateFetched)
	}
	return out
}

// eventsFixture is two rebuilds of an areas-shaped library, the second of
// which split, merged, moved, dissolved and created interests, and the
// identities they name.
type eventsFixture struct {
	first, second string // the runs
	// The areas: kept, kept, and new in the second run.
	a1, a2, a3 string
	// The interests: i1 split into itself and i6; i2 and i3 merged into
	// i7; i4 moved from a2 to a1; i5 dissolved; i8 new.
	i1, i2, i3, i4, i5, i6, i7, i8 string
	// Documents: i1's members, loose fit and the unsorted, as the second
	// run has them.
	members []*store.Document
	loose   *store.Document
	unsort  []*store.Document
}

// seedEvents commits the eventsFixture's two runs over members given (at
// least two, the first ones listed first) and new documents.
func (s *testServer) seedEvents(t *testing.T, members []*store.Document, loose *store.Document, unsorted ...*store.Document) eventsFixture {
	t.Helper()
	d := s.docs(t, "events", 7)
	var e eventsFixture
	e.members, e.loose, e.unsort = members, loose, unsorted

	f := s.newRun(t, store.InterestShapeAreas)
	e.a1, e.a2 = f.area("Engineering"), f.area("Investing")
	e.i1 = f.interest("Kafka", e.a1, 0, append(slices.Clone(members), d[0]), []*store.Document{loose})
	e.i2 = f.interest("Stream Joins", e.a1, 0, d[1:2], nil)
	e.i3 = f.interest("Windowing", e.a1, 0, d[2:3], nil)
	e.i4 = f.interest("Index Funds", e.a2, 0, d[3:4], nil)
	e.i5 = f.interest("Bonds", e.a2, 0, d[4:5], nil)
	f.unsorted(e.i1, unsorted...)
	e.first = f.commit(t)

	f = s.newRun(t, store.InterestShapeAreas)
	f.area("", e.a1)
	f.area("", e.a2)
	e.a3 = f.area("Cooking")
	f.interest("", e.a1, 0, members, []*store.Document{loose}, e.i1)
	e.i6 = f.interest("Kafka Connect", e.a1, 0, d[0:1], nil)
	e.i7 = f.interest("Stream Processing", e.a1, 0, d[1:3], nil)
	f.interest("", e.a1, 0, d[3:4], nil, e.i4)
	e.i8 = f.interest("Recipes", e.a3, 0, d[4:6], nil)
	f.unsorted(e.i1, unsorted...)
	f.lineage(e.a1, e.a1, store.LineageKept, 4)
	f.lineage(e.a2, e.a2, store.LineageKept, 1)
	f.lineage(e.i1, e.i1, store.LineageKept, len(members))
	f.lineage(e.i1, e.i6, store.LineageSplit, 1)
	f.lineage(e.i2, e.i7, store.LineageMerged, 1)
	f.lineage(e.i3, e.i7, store.LineageMerged, 1)
	f.lineage(e.i4, e.i4, store.LineageMoved, 1)
	f.c.Outcome.Kept, f.c.Outcome.Split, f.c.Outcome.Merged = 2, 1, 2
	f.c.Outcome.Moved, f.c.Outcome.Dissolved, f.c.Outcome.ChangedDocuments = 1, 1, 2
	e.second = f.commit(t)
	return e
}

// getJSON gets path, which must answer 200, into v.
func (s *testServer) getJSON(t *testing.T, path string, v any) {
	t.Helper()
	resp := s.do(t, request{method: http.MethodGet, path: path})
	require.Equal(t, http.StatusOK, resp.status, "%s: %s", path, resp.body)
	require.NoError(t, json.Unmarshal([]byte(resp.body), v), path)
}

// getAs is getJSON into a new T: decoding into a value read before would
// keep what an answer omits.
func getAs[T any](t *testing.T, s *testServer, path string) T {
	t.Helper()
	var v T
	s.getJSON(t, path, &v)
	return v
}

func ids(items []InterestResponse) []string {
	out := make([]string, 0, len(items))
	for _, in := range items {
		out = append(out, in.ID)
	}
	return out
}

func memberIDs(members []InterestMember) []string {
	out := make([]string, 0, len(members))
	for _, m := range members {
		out = append(out, m.DocID)
	}
	return out
}

func TestListInterests(t *testing.T) {
	s := newTestServer(t)

	resp := s.do(t, request{method: http.MethodGet, path: "/v1/interests"})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	assert.JSONEq(t, `{"num_documents":0,"num_areas":0,"num_interests":0,"num_loose":0,"num_unsorted":0,
		"num_new":0,"total":0,"next":{"state":"none"},"items":[]}`, resp.body,
		"no rebuild yet: an empty list, not an error")

	a := s.seedDocument(t, "https://example.com/a", store.DocStateFetched)
	_, err := s.db.Exec(`UPDATE documents SET title = 'Post A' WHERE id = ?`, a.ID)
	require.NoError(t, err)
	ext := s.seedContent(t, a, "# A")
	b := s.seedDocument(t, "https://example.com/b", store.DocStateFetched)
	_, err = s.deps.Bookmarks.Ingest(context.Background(), &store.Bookmark{TenantID: "local", URL: b.URL,
		Title: new("Saved as B"), Source: store.SourceChrome, SavedAt: time.Now().UTC()})
	require.NoError(t, err)
	id := s.seedInterest(t, "local", "Go", a, b)

	var got InterestListResponse
	s.getJSON(t, "/v1/interests?members=5", &got)
	assert.Equal(t, "test", got.Algo)
	assert.Equal(t, "flat", got.Shape)
	assert.NotNil(t, got.ComputedAt)
	assert.Equal(t, 2, got.NumDocuments)
	assert.Equal(t, 1, got.NumInterests)
	assert.Equal(t, 1, got.Total, "the flat shape's top-level groups are its interests")
	assert.Equal(t, InterestsState{State: stateCurrent}, got.Next)
	require.NotNil(t, got.Rebuild)
	assert.Equal(t, InterestRebuild{Trigger: "manual", Kind: "fresh", Created: 1}, *got.Rebuild)
	require.Len(t, got.Items, 1)
	in := got.Items[0]
	assert.Equal(t, id, in.ID)
	assert.Equal(t, "interest", in.Level)
	assert.Equal(t, "Go", in.Label)
	assert.Equal(t, 2, in.Size)
	assert.Equal(t, got.RunID, in.RunID, "the run it belongs to")
	require.Len(t, in.Members, 2)
	assert.Equal(t, a.ID, in.Members[0].DocID, "most similar first")
	assert.Equal(t, "member", in.Members[0].Fit)
	assert.Equal(t, "Post A", in.Members[0].Title)
	assert.Empty(t, in.Members[0].BookmarkTitle, "a titled document needs no bookmark's")
	assert.Equal(t, "https://example.com/a", in.Members[0].URL)
	assert.Equal(t, "fetched", in.Members[0].State)
	assert.Equal(t, filepath.Join(s.deps.Home.ContentDir(), *ext.MarkdownPath), in.Members[0].MarkdownPath)
	assert.Empty(t, in.Members[1].MarkdownPath, "b has no content")
	assert.Equal(t, "Saved as B", in.Members[1].BookmarkTitle, "an untitled document is named by its bookmark")

	resp = s.do(t, request{method: http.MethodGet, path: "/v1/interests?members=0"})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	assert.NotContains(t, resp.body, `"members"`)

	// Sizing parameters out of range mean their default, as for ?limit.
	for _, members := range []string{"101", "-1", "many"} {
		got := getAs[InterestListResponse](t, s, "/v1/interests?members="+members)
		assert.Len(t, got.Items[0].Members, 2, "members=%s: the default 3 covers both", members)
	}
}

// TestListInterests_Areas: in the areas shape the list is the areas, each
// with its interests' count and its largest interests, each of those with
// its members; level=interest lists every interest, each with its area.
func TestListInterests_Areas(t *testing.T) {
	s := newTestServer(t)
	d := s.docs(t, "doc", 6)
	f := s.newRun(t, store.InterestShapeAreas)
	tech := f.area("Tech")
	agents := f.interest("Agents", tech, 0, d[0:3], nil)
	rust := f.interest("Rust", tech, 2, d[3:4], nil)
	money := f.area("Money")
	bonds := f.interest("Bonds", money, 0, d[4:5], []*store.Document{d[5]})
	run := f.commit(t)

	var got InterestListResponse
	s.getJSON(t, "/v1/interests?children=1&members=2", &got)
	assert.Equal(t, "areas", got.Shape)
	assert.Equal(t, 2, got.NumAreas)
	assert.Equal(t, 3, got.NumInterests)
	assert.Equal(t, 1, got.NumLoose)
	assert.Equal(t, 2, got.Total, "the areas")
	require.Equal(t, []string{tech, money}, ids(got.Items), "largest first")
	area := got.Items[0]
	assert.Equal(t, "area", area.Level)
	assert.Equal(t, 5, area.Size)
	assert.Equal(t, 2, area.NumChildren, "every interest counts, listed or not")
	require.Equal(t, []string{agents}, ids(area.Children), "children=1")
	child := area.Children[0]
	assert.Equal(t, tech, child.ParentID)
	assert.Equal(t, run, child.RunID)
	assert.Equal(t, memberIDs(child.Members), []string{d[0].ID, d[1].ID}, "members=2")
	assert.Empty(t, area.Members, "an area lists interests, not documents")
	assert.Equal(t, 1, got.Items[1].Loose, "an area's loose fits are its interests'")

	got = getAs[InterestListResponse](t, s, "/v1/interests?level=interest&members=0")
	assert.Equal(t, 3, got.Total, "every interest")
	assert.Equal(t, []string{agents, rust, bonds}, ids(got.Items))
	for _, in := range got.Items {
		assert.NotEmpty(t, in.ParentID)
		assert.NotEmpty(t, in.ParentLabel)
		assert.Empty(t, in.Children)
	}
	assert.Equal(t, "Money", got.Items[2].ParentLabel)

	p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/interests?level=area"}), http.StatusBadRequest)
	assert.Contains(t, p.Detail, `level "area"`)
}

// TestInterests_State: the list says where the next rebuild stands.
func TestInterests_State(t *testing.T) {
	ctx := context.Background()
	state := func(s *testServer) InterestsState {
		t.Helper()
		var got InterestListResponse
		s.getJSON(t, "/v1/interests", &got)
		return got.Next
	}
	off := newTestServer(t, func(d *Deps) { d.InsightEnabled = false })
	assert.Equal(t, InterestsState{State: stateOff}, state(off))

	s := newTestServer(t)
	assert.Equal(t, InterestsState{State: stateNone}, state(s))
	job, _, err := queueRebuild(ctx, s)
	require.NoError(t, err)
	assert.Equal(t, InterestsState{State: stateQueued}, state(s))
	claimed, err := s.deps.Queue.ClaimNext(ctx, []store.JobKind{store.JobKindCluster})
	require.NoError(t, err)
	require.Equal(t, job.ID, claimed.ID)
	assert.Equal(t, InterestsState{State: stateRebuilding}, state(s))
	require.NoError(t, s.deps.Queue.MarkDone(ctx, job.ID))

	s.seedFailedRun(t, "ollama unreachable")
	assert.Equal(t, InterestsState{State: stateFailing, LastError: "ollama unreachable"}, state(s))
	s.seedInterest(t, "local", "Go", s.seedDocument(t, "https://example.com/a", store.DocStateFetched))
	assert.Equal(t, InterestsState{State: stateCurrent}, state(s))
	s.seedFailedRun(t, "boom")
	var got InterestListResponse
	s.getJSON(t, "/v1/interests", &got)
	assert.Equal(t, InterestsState{State: stateFailing, LastError: "boom"}, got.Next)
	assert.Len(t, got.Items, 1, "a failed rebuild leaves the last one's interests")
}

// queueRebuild queues a rebuild, as the API does.
func queueRebuild(ctx context.Context, s *testServer) (*store.Job, bool, error) {
	job := &store.Job{TenantID: "local", Kind: store.JobKindCluster, Payload: []byte(`{"trigger":"manual"}`)}
	queued, err := s.deps.Queue.EnqueueOnce(ctx, job)
	return job, queued, err
}

func TestGetInterest(t *testing.T) {
	s := newTestServer(t)
	members := s.docs(t, "member", 3)
	loose := s.seedDocument(t, "https://example.com/loose", store.DocStateFetched)
	e := s.seedEvents(t, members, loose)
	placed := s.seedDocument(t, "https://example.com/placed", store.DocStateFetched)
	s.placeDocument(t, e.second, e.i1, placed)

	var area InterestResponse
	s.getJSON(t, "/v1/interests/"+e.a1, &area)
	assert.Equal(t, "area", area.Level)
	assert.Equal(t, "Engineering", area.Label)
	assert.Equal(t, 4, area.NumChildren)
	require.Len(t, area.Children, 4, "every interest of the area")
	assert.Equal(t, e.i1, area.Children[0].ID)
	assert.Len(t, area.Children[0].Members, 3, "the default 3 a child")
	assert.Equal(t, 1, area.New, "its interests' placements")
	assert.Equal(t, []InterestEvent{{Event: "kept", Level: "area", From: &InterestRef{ID: e.a1, Label: "Engineering"},
		To: &InterestRef{ID: e.a1, Label: "Engineering"}, Shared: 4}}, area.Events)

	var in InterestResponse
	s.getJSON(t, "/v1/interests/"+e.i1+"?members=10", &in)
	assert.Equal(t, e.a1, in.ParentID)
	assert.Equal(t, "Engineering", in.ParentLabel)
	assert.Equal(t, 3, in.Size)
	assert.Equal(t, 1, in.Loose)
	assert.Equal(t, 1, in.New)
	assert.Equal(t, append(memberIDs(nil), members[0].ID, members[1].ID, members[2].ID, loose.ID), memberIDs(in.Members),
		"members, then loose fits")
	assert.Equal(t, "loose", in.Members[3].Fit)
	require.Len(t, in.NewMembers, 1)
	assert.Equal(t, InterestMember{DocID: placed.ID, URL: placed.URL, State: "fetched", Similarity: 0.6, Fit: "new"},
		in.NewMembers[0])
	kafka := &InterestRef{ID: e.i1, Label: "Kafka"}
	assert.Equal(t, []InterestEvent{
		{Event: "kept", Level: "interest", From: kafka, To: kafka, Shared: 3},
		{Event: "split", Level: "interest", From: kafka, To: &InterestRef{ID: e.i6, Label: "Kafka Connect"}, Shared: 1},
	}, in.Events)

	// The page: members, then loose fits, from the offset.
	in = getAs[InterestResponse](t, s, "/v1/interests/"+e.i1+"?members=2&offset=2")
	assert.Equal(t, []string{members[2].ID, loose.ID}, memberIDs(in.Members))
	in = getAs[InterestResponse](t, s, "/v1/interests/"+e.i1+"?offset=3")
	assert.Equal(t, []string{loose.ID}, memberIDs(in.Members))
	in = getAs[InterestResponse](t, s, "/v1/interests/"+e.i1+"?offset=4")
	assert.Empty(t, in.Members, "past members and loose fits")

	var moved InterestResponse
	s.getJSON(t, "/v1/interests/"+e.i4, &moved)
	assert.Equal(t, []InterestEvent{{Event: "moved", Level: "interest", From: &InterestRef{ID: e.i4, Label: "Index Funds"},
		To: &InterestRef{ID: e.i4, Label: "Index Funds"}, Area: &InterestRef{ID: e.a1, Label: "Engineering"}, Shared: 1}},
		moved.Events)
	var fresh InterestResponse
	s.getJSON(t, "/v1/interests/"+e.i8, &fresh)
	assert.Equal(t, []InterestEvent{{Event: "new", Level: "interest", To: &InterestRef{ID: e.i8, Label: "Recipes"}}},
		fresh.Events)

	p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/interests/no-such-interest"}),
		http.StatusNotFound)
	assert.Equal(t, `interest "no-such-interest" not found`, p.Detail)
	theirs := s.seedInterest(t, "other", "Rust", members[0])
	p = assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/interests/" + theirs}), http.StatusNotFound)
	assert.Equal(t, `interest "`+theirs+`" not found`, p.Detail, "another tenant's interest doesn't exist here")
}

// TestGetInterest_Retired: a retired identity answers 410 with when and
// which rebuild retired it, and the identities that took its documents.
func TestGetInterest_Retired(t *testing.T) {
	s := newTestServer(t)
	e := s.seedEvents(t, s.docs(t, "member", 2), s.seedDocument(t, "https://example.com/loose", store.DocStateFetched))

	resp := s.do(t, request{method: http.MethodGet, path: "/v1/interests/" + e.i2})
	require.Equal(t, http.StatusGone, resp.status, resp.body)
	assert.Equal(t, "application/problem+json", resp.contentType)
	var got RetiredInterest
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	assert.Equal(t, InterestRetiredProblemType, got.Type)
	assert.Equal(t, http.StatusGone, got.Status)
	assert.Equal(t, e.i2, got.ID)
	assert.Equal(t, "interest", got.Level)
	assert.Equal(t, "Stream Joins", got.Label)
	assert.Equal(t, e.second, got.RunID)
	assert.WithinDuration(t, time.Now(), got.RetiredAt, time.Minute)
	assert.Equal(t, []InterestSuccessor{{ID: e.i7, Level: "interest", Label: "Stream Processing", Event: "merged",
		Shared: 1}}, got.Successors)
	assert.Equal(t, `interest "Stream Joins" was retired by the rebuild of `+got.RetiredAt.Format(time.DateOnly)+
		`: it merged into "Stream Processing"`, got.Detail)
	assert.Equal(t, "/v1/interests/"+e.i2, got.Instance)

	resp = s.do(t, request{method: http.MethodGet, path: "/v1/interests/" + e.i5})
	require.Equal(t, http.StatusGone, resp.status, resp.body)
	require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
	assert.Empty(t, got.Successors)
	assert.Contains(t, got.Detail, `interest "Bonds" was retired`)
	assert.Contains(t, got.Detail, "it dissolved")
}

// TestUnsorted: the documents in no interest, nearest first, each naming
// the interest it is nearest; and those placed in Unsorted since.
func TestUnsorted(t *testing.T) {
	s := newTestServer(t)
	var empty UnsortedPage
	s.getJSON(t, "/v1/interests/unsorted", &empty)
	assert.Equal(t, UnsortedPage{Items: []UnsortedMember{}, New: []UnsortedMember{}}, empty)

	d := s.docs(t, "doc", 6)
	f := s.newRun(t, store.InterestShapeFlat)
	kafka := f.interest("Kafka", "", 0, d[0:2], nil)
	f.unsorted(kafka, d[2], d[3], d[4])
	f.c.Assignments[len(f.c.Assignments)-1].NearestID = "" // nearest none
	run := f.commit(t)
	s.placeDocument(t, run, "", d[5])

	var got UnsortedPage
	s.getJSON(t, "/v1/interests/unsorted", &got)
	assert.Equal(t, run, got.RunID)
	assert.Equal(t, 3, got.Total)
	assert.Equal(t, 1, got.NumNew)
	require.Len(t, got.Items, 3)
	assert.Equal(t, []string{d[2].ID, d[3].ID, d[4].ID}, []string{got.Items[0].DocID, got.Items[1].DocID, got.Items[2].DocID},
		"nearest first")
	assert.Equal(t, kafka, got.Items[0].NearestID)
	assert.Equal(t, "Kafka", got.Items[0].NearestLabel)
	assert.Empty(t, got.Items[2].NearestID)
	require.Len(t, got.New, 1)
	assert.Equal(t, d[5].ID, got.New[0].DocID)

	got = getAs[UnsortedPage](t, s, "/v1/interests/unsorted?limit=1&offset=1")
	require.Len(t, got.Items, 1)
	assert.Equal(t, d[3].ID, got.Items[0].DocID)
	got = getAs[UnsortedPage](t, s, "/v1/interests/unsorted?offset=3")
	assert.Empty(t, got.Items)
	assert.Equal(t, 3, got.Total, "past the end, the run's total")
	assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/interests/unsorted?offset=-2"}), http.StatusBadRequest)
}

// TestInterestChanges: what the latest rebuild did, without what it kept,
// by event, then level, then label.
func TestInterestChanges(t *testing.T) {
	s := newTestServer(t)
	var none InterestChanges
	s.getJSON(t, "/v1/interests/changes", &none)
	assert.Equal(t, InterestChanges{Events: []InterestEvent{}}, none)

	e := s.seedEvents(t, s.docs(t, "member", 2), s.seedDocument(t, "https://example.com/loose", store.DocStateFetched))
	var got InterestChanges
	s.getJSON(t, "/v1/interests/changes", &got)
	assert.Equal(t, e.second, got.RunID)
	require.NotNil(t, got.Rebuild)
	assert.Equal(t, 1, got.Rebuild.Split)
	ref := func(id, label string, retired bool) *InterestRef {
		return &InterestRef{ID: id, Label: label, Retired: retired}
	}
	assert.Equal(t, []InterestEvent{
		{Event: "split", Level: "interest", From: ref(e.i1, "Kafka", false), To: ref(e.i6, "Kafka Connect", false), Shared: 1},
		{Event: "merged", Level: "interest", From: ref(e.i2, "Stream Joins", true), To: ref(e.i7, "Stream Processing", false), Shared: 1},
		{Event: "merged", Level: "interest", From: ref(e.i3, "Windowing", true), To: ref(e.i7, "Stream Processing", false), Shared: 1},
		{Event: "moved", Level: "interest", From: ref(e.i4, "Index Funds", false), To: ref(e.i4, "Index Funds", false),
			Area: ref(e.a1, "Engineering", false), Shared: 1},
		{Event: "dissolved", Level: "interest", From: ref(e.i5, "Bonds", true)},
		{Event: "new", Level: "area", To: ref(e.a3, "Cooking", false)},
		{Event: "new", Level: "interest", To: ref(e.i8, "Recipes", false)},
	}, got.Events)
}

// failingMembers fails every interest-member lookup.
type failingMembers struct{ store.InsightStore }

func (failingMembers) Members(context.Context, string, string, store.InterestFit, int, int) ([]store.InterestAssignment, error) {
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
			id := s.seedInterest(t, "local", "Go", a)
			for _, path := range []string{"/v1/interests", "/v1/interests/" + id} {
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
// every read; a page past the end still names the run and its total.
func TestInterests_Pages(t *testing.T) {
	s := newTestServer(t)
	d := s.docs(t, "doc", 5)
	f := s.newRun(t, store.InterestShapeFlat)
	big := f.interest("Big", "", 0, d, nil)
	sims := []float64{0.9, 0.5, 0.5, 0.7, 0.5}
	for i := range f.c.Assignments {
		f.c.Assignments[i].Similarity = sims[i]
	}
	for _, size := range []int{2, 2, 1, 2} {
		f.interest("", "", size, nil, nil)
	}
	run := f.commit(t)

	var all InterestListResponse
	s.getJSON(t, "/v1/interests?limit=500&members=0", &all)
	require.Len(t, all.Items, 5)
	assert.Equal(t, big, all.Items[0].ID)
	var paged []InterestResponse
	for offset := 0; offset <= len(all.Items); offset += 2 {
		var page InterestListResponse
		s.getJSON(t, "/v1/interests?members=0&limit=2&offset="+strconv.Itoa(offset), &page)
		assert.Equal(t, run, page.RunID)
		paged = append(paged, page.Items...)
	}
	assert.Equal(t, ids(all.Items), ids(paged), "the pages add up to the list")

	var whole InterestResponse
	s.getJSON(t, "/v1/interests/"+big, &whole)
	ties := slices.Sorted(slices.Values([]string{d[1].ID, d[2].ID, d[4].ID}))
	assert.Equal(t, append([]string{d[0].ID, d[3].ID}, ties...), memberIDs(whole.Members))
	var pagedMembers []string
	for offset := 0; offset <= len(d); offset += 2 {
		var page InterestResponse
		s.getJSON(t, "/v1/interests/"+big+"?members=2&offset="+strconv.Itoa(offset), &page)
		assert.Equal(t, len(d), page.Size, "the size counts every member")
		pagedMembers = append(pagedMembers, memberIDs(page.Members)...)
	}
	assert.Equal(t, memberIDs(whole.Members), pagedMembers, "the pages add up to the members")

	resp := s.do(t, request{method: http.MethodGet, path: "/v1/interests?offset=5"})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	var past map[string]any
	require.NoError(t, json.Unmarshal([]byte(resp.body), &past))
	assert.Equal(t, []any{}, past["items"])
	assert.Equal(t, run, past["run_id"])
	for _, field := range []string{"computed_at", "algo", "shape"} {
		assert.NotEmpty(t, past[field], field)
	}
	assert.InDelta(t, 5, past["total"], 0)
	assert.InDelta(t, 12, past["num_documents"], 0)
}

// TestInterests_BadOffset: an offset that isn't a whole number of 0 or
// more is a 400 naming it, answered before anything is read.
func TestInterests_BadOffset(t *testing.T) {
	n := &insightReads{}
	s := newTestServer(t, func(d *Deps) { d.Insights = countingInsights{d.Insights, n} })
	id := s.seedInterest(t, "local", "Go", s.seedDocument(t, "https://example.com/a", store.DocStateFetched))
	*n = insightReads{}
	for _, offset := range []string{"-1", "1.5", "x", "99999999999999999999", "0x10"} {
		for _, path := range []string{"/v1/interests", "/v1/interests/" + id, "/v1/interests/unsorted"} {
			p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: path + "?offset=" + offset}),
				http.StatusBadRequest)
			assert.Contains(t, p.Detail, `offset "`+offset+`"`, path)
		}
	}
	assert.Zero(t, n.total(), "nothing read")

	for _, offset := range []string{"", "0"} {
		var got InterestListResponse
		s.getJSON(t, "/v1/interests?offset="+offset, &got)
		assert.Len(t, got.Items, 1, "offset %q is the start", offset)
	}
}

// insightReads counts the insight store's reads a response makes.
type insightReads struct {
	latest, newest, pages, groups, children, members, counts, placements, lineage, identities atomic.Int32
}

// total is every read counted.
func (n *insightReads) total() int32 {
	return n.latest.Load() + n.newest.Load() + n.pages.Load() + n.groups.Load() + n.children.Load() +
		n.members.Load() + n.counts.Load() + n.placements.Load() + n.lineage.Load() + n.identities.Load()
}

// countingInsights counts reads into n.
type countingInsights struct {
	store.InsightStore
	n *insightReads
}

func (c countingInsights) LatestRun(ctx context.Context, tenantID string, status store.InterestRunStatus) (*store.InterestRun, error) {
	if status == "" {
		c.n.newest.Add(1)
	} else {
		c.n.latest.Add(1)
	}
	return c.InsightStore.LatestRun(ctx, tenantID, status)
}

func (c countingInsights) TopGroups(ctx context.Context, runID string, limit, offset int) ([]store.InterestGroup, error) {
	c.n.pages.Add(1)
	return c.InsightStore.TopGroups(ctx, runID, limit, offset)
}

func (c countingInsights) NestedGroups(ctx context.Context, runID string, limit, offset int) ([]store.InterestGroup, error) {
	c.n.pages.Add(1)
	return c.InsightStore.NestedGroups(ctx, runID, limit, offset)
}

func (c countingInsights) GetGroup(ctx context.Context, runID, id string) (*store.InterestGroup, error) {
	c.n.groups.Add(1)
	return c.InsightStore.GetGroup(ctx, runID, id)
}

func (c countingInsights) ChildGroups(ctx context.Context, runID string, areaIDs []string) ([]store.InterestGroup, error) {
	c.n.children.Add(1)
	return c.InsightStore.ChildGroups(ctx, runID, areaIDs)
}

func (c countingInsights) Members(ctx context.Context, runID, interestID string, fit store.InterestFit, limit, offset int) ([]store.InterestAssignment, error) {
	c.n.members.Add(1)
	return c.InsightStore.Members(ctx, runID, interestID, fit, limit, offset)
}

func (c countingInsights) PlacementCounts(ctx context.Context, runID string) (map[string]int, error) {
	c.n.counts.Add(1)
	return c.InsightStore.PlacementCounts(ctx, runID)
}

func (c countingInsights) Placements(ctx context.Context, runID, interestID string, limit int) ([]store.Placement, error) {
	c.n.placements.Add(1)
	return c.InsightStore.Placements(ctx, runID, interestID, limit)
}

func (c countingInsights) RunLineage(ctx context.Context, runID string) ([]store.LineageRow, error) {
	c.n.lineage.Add(1)
	return c.InsightStore.RunLineage(ctx, runID)
}

func (c countingInsights) GetInterests(ctx context.Context, tenantID string, ids []string) ([]store.Interest, error) {
	c.n.identities.Add(1)
	return c.InsightStore.GetInterests(ctx, tenantID, ids)
}

func (c countingInsights) GetInterest(ctx context.Context, id string) (*store.Interest, error) {
	c.n.identities.Add(1)
	return c.InsightStore.GetInterest(ctx, id)
}

// countingQueue counts the queue's reads of its counts.
type countingQueue struct {
	store.JobStore
	counts *atomic.Int32
}

func (q countingQueue) QueueCounts(ctx context.Context) (map[store.JobKind]store.QueueCount, error) {
	q.counts.Add(1)
	return q.JobStore.QueueCounts(ctx)
}

// documentReads counts the batched read of members' documents, and the
// one-by-one reads of a document and of an extraction that the members
// must never need.
type documentReads struct {
	batches, documents, extractions atomic.Int32
}

type countingDocuments struct {
	store.DocumentStore
	n *documentReads
}

func (d countingDocuments) GetByIDsWithLastError(ctx context.Context, tenantID string, ids []string) ([]store.DocumentWithError, error) {
	d.n.batches.Add(1)
	return d.DocumentStore.GetByIDsWithLastError(ctx, tenantID, ids)
}

func (d countingDocuments) GetByID(ctx context.Context, id string) (*store.Document, error) {
	d.n.documents.Add(1)
	return d.DocumentStore.GetByID(ctx, id)
}

type countingExtractions struct {
	store.ExtractionStore
	n *documentReads
}

func (e countingExtractions) GetByID(ctx context.Context, id string) (*store.DocumentExtraction, error) {
	e.n.extractions.Add(1)
	return e.ExtractionStore.GetByID(ctx, id)
}

// TestInterests_Reads: a page of groups reads the latest run, the page,
// the placement counts, its areas' interests in one read, each listed
// interest's members, then all their documents at once, however many
// groups and members; its state is one read of the queue and one of the
// newest run. One interest reads its group, its members and loose fits,
// its placements and its run's lineage, and its documents in one read.
// Never a document or an extraction one by one.
func TestInterests_Reads(t *testing.T) {
	n, docs, queue := &insightReads{}, &documentReads{}, new(atomic.Int32)
	s := newTestServer(t, func(d *Deps) {
		d.Insights = countingInsights{d.Insights, n}
		d.Documents = countingDocuments{d.Documents, docs}
		d.Extractions = countingExtractions{d.Extractions, docs}
		d.Queue = countingQueue{d.Queue, queue}
	})
	d := s.docs(t, "doc", 7)
	for _, doc := range d {
		s.seedContent(t, doc, "# text")
	}
	f := s.newRun(t, store.InterestShapeAreas)
	tech, money := f.area("Tech"), f.area("Money")
	agents := f.interest("Agents", tech, 0, d[0:2], []*store.Document{d[6]})
	f.interest("Rust", tech, 0, d[2:4], nil)
	f.interest("Bonds", money, 0, d[4:6], nil)
	f.commit(t)

	for _, tc := range []struct {
		path                                      string
		latest, pages, children, members, batches int32
		queue, newest                             int32
	}{
		{"/v1/interests?children=5&members=2", 1, 1, 1, 3, 1, 1, 1},
		{"/v1/interests?children=1&members=2", 1, 1, 1, 2, 1, 1, 1},
		{"/v1/interests?members=0", 1, 1, 1, 0, 0, 1, 1},
		{"/v1/interests?level=interest&members=2", 1, 1, 0, 3, 1, 1, 1},
		{"/v1/interests?offset=2", 1, 1, 0, 0, 0, 1, 1},
	} {
		*n, *docs = insightReads{}, documentReads{}
		queue.Store(0)
		s.getJSON(t, tc.path, &InterestListResponse{})
		assert.Equal(t, tc.latest, n.latest.Load(), "%s: latest done run", tc.path)
		assert.Equal(t, tc.pages, n.pages.Load(), "%s: pages", tc.path)
		assert.Equal(t, tc.children, n.children.Load(), "%s: the areas' interests", tc.path)
		assert.Equal(t, tc.members, n.members.Load(), "%s: members' reads", tc.path)
		assert.Equal(t, int32(1), n.counts.Load(), "%s: placement counts", tc.path)
		assert.Equal(t, tc.batches, docs.batches.Load(), "%s: documents' reads", tc.path)
		assert.Equal(t, tc.queue, queue.Load(), "%s: the queue's counts", tc.path)
		assert.Equal(t, tc.newest, n.newest.Load(), "%s: the newest run", tc.path)
		assert.Zero(t, docs.documents.Load()+docs.extractions.Load(), "%s: no document read one by one", tc.path)
	}

	*n, *docs = insightReads{}, documentReads{}
	s.getJSON(t, "/v1/interests/"+agents, &InterestResponse{})
	assert.Equal(t, int32(1), n.latest.Load())
	assert.Equal(t, int32(1), n.groups.Load())
	assert.Equal(t, int32(2), n.members.Load(), "members, then loose fits")
	assert.Equal(t, int32(1), n.placements.Load())
	assert.Equal(t, int32(1), n.lineage.Load())
	assert.Equal(t, int32(1), docs.batches.Load())
	assert.Zero(t, docs.documents.Load()+docs.extractions.Load())

	*n, *docs = insightReads{}, documentReads{}
	s.getJSON(t, "/v1/interests/"+tech, &InterestResponse{})
	assert.Equal(t, int32(1), n.children.Load(), "an area's interests in one read")
	assert.Equal(t, int32(2), n.members.Load(), "each interest's members")
	assert.Equal(t, int32(1), docs.batches.Load())
}

// prunedMidRead is the insight store as a read sees it while a rebuild
// finishes: the first latest done run it answers, pruned, has no groups
// left by the time they are read; the reads after it see the rebuild's,
// unless the rebuilds go on.
type prunedMidRead struct {
	store.InsightStore
	pruned      *store.InterestRun
	ongoing     bool // every read of the latest run answers a pruned one
	latestReads *atomic.Int32
}

func (p prunedMidRead) LatestRun(ctx context.Context, tenantID string, status store.InterestRunStatus) (*store.InterestRun, error) {
	if status == "" {
		return p.InsightStore.LatestRun(ctx, tenantID, status)
	}
	if p.latestReads.Add(1) == 1 || p.ongoing {
		return p.pruned, nil
	}
	return p.InsightStore.LatestRun(ctx, tenantID, status)
}

func (p prunedMidRead) TopGroups(ctx context.Context, runID string, limit, offset int) ([]store.InterestGroup, error) {
	if runID == p.pruned.ID {
		return nil, nil
	}
	return p.InsightStore.TopGroups(ctx, runID, limit, offset)
}

func (p prunedMidRead) GetGroup(ctx context.Context, runID, id string) (*store.InterestGroup, error) {
	if runID == p.pruned.ID {
		return nil, fmt.Errorf("group %s: %w", id, store.ErrNotFound)
	}
	return p.InsightStore.GetGroup(ctx, runID, id)
}

// TestInterests_PrunedMidRead: a page or an interest the latest run holds,
// read while a rebuild prunes that run, is read once more, from the
// rebuild's run; a second miss is answered as read, and a page past the
// run's end, which has no groups anyway, is never read again. An interest
// no done run holds is a 410 when retired meanwhile, and an inconsistency
// when live.
func TestInterests_PrunedMidRead(t *testing.T) {
	pruned := &store.InterestRun{ID: "run-a", Grouper: "test",
		RunOutcome: store.RunOutcome{Shape: store.InterestShapeFlat, NumInterests: 3}}
	for _, tc := range []struct {
		name    string
		path    string
		ongoing bool
		run     func(rebuilt string) string
		items   int
		latest  int32
	}{
		{"rebuilt", "/v1/interests?limit=1&offset=1", false, func(r string) string { return r }, 1, 2},
		{"rebuilding", "/v1/interests?limit=1&offset=1", true, func(string) string { return "run-a" }, 0, 2},
		{"past the end", "/v1/interests?offset=3", false, func(string) string { return "run-a" }, 0, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			latest := new(atomic.Int32)
			s := newTestServer(t, func(d *Deps) {
				d.Insights = prunedMidRead{InsightStore: d.Insights, pruned: pruned, ongoing: tc.ongoing, latestReads: latest}
			})
			f := s.newRun(t, store.InterestShapeFlat)
			f.interest("A", "", 2, nil, nil)
			f.interest("B", "", 1, nil, nil)
			rebuilt := f.commit(t)
			latest.Store(0)
			var got InterestListResponse
			s.getJSON(t, tc.path, &got)
			assert.Equal(t, tc.run(rebuilt), got.RunID)
			assert.Len(t, got.Items, tc.items)
			for _, in := range got.Items {
				assert.Equal(t, rebuilt, in.RunID)
			}
			assert.Equal(t, tc.latest, latest.Load(), "reads of the latest run")
		})
	}

	t.Run("one interest", func(t *testing.T) {
		latest := new(atomic.Int32)
		s := newTestServer(t, func(d *Deps) {
			d.Insights = prunedMidRead{InsightStore: d.Insights, pruned: pruned, latestReads: latest}
		})
		id := s.seedInterest(t, "local", "Go", s.seedDocument(t, "https://example.com/a", store.DocStateFetched))
		latest.Store(0)
		var got InterestResponse
		s.getJSON(t, "/v1/interests/"+id, &got)
		assert.Equal(t, "Go", got.Label, "read again from the newest run")
		assert.Equal(t, int32(2), latest.Load())
	})
	t.Run("retired meanwhile", func(t *testing.T) {
		latest := new(atomic.Int32)
		s := newTestServer(t, func(d *Deps) {
			d.Insights = prunedMidRead{InsightStore: d.Insights, pruned: pruned, latestReads: latest}
		})
		e := s.seedEvents(t, s.docs(t, "member", 2), s.seedDocument(t, "https://example.com/loose", store.DocStateFetched))
		latest.Store(0)
		assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/interests/" + e.i2}), http.StatusGone)
		assert.Equal(t, int32(1), latest.Load(), "the identity says it is retired: nothing to read again")
	})
	t.Run("live, held by no run", func(t *testing.T) {
		latest := new(atomic.Int32)
		s := newTestServer(t, func(d *Deps) {
			d.Insights = prunedMidRead{InsightStore: d.Insights, pruned: pruned, ongoing: true, latestReads: latest}
		})
		id := s.seedInterest(t, "local", "Go", s.seedDocument(t, "https://example.com/a", store.DocStateFetched))
		p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/interests/" + id}),
			http.StatusInternalServerError)
		assert.Contains(t, p.Detail, "interest "+id+" is live, but no done run holds it")
	})
}

// nanCohesion reports a group whose cohesion JSON can't represent.
type nanCohesion struct{ store.InsightStore }

func (n nanCohesion) GetGroup(ctx context.Context, runID, id string) (*store.InterestGroup, error) {
	g, err := n.InsightStore.GetGroup(ctx, runID, id)
	if err != nil {
		return nil, err
	}
	g.Cohesion = math.NaN()
	return g, nil
}

// TestInterests_UnencodableResponse: a response that can't be encoded is a
// 500 problem, not a 200 with an empty body.
func TestInterests_UnencodableResponse(t *testing.T) {
	s := newTestServer(t, func(d *Deps) { d.Insights = nanCohesion{d.Insights} })
	id := s.seedInterest(t, "local", "Go", s.seedDocument(t, "https://example.com/a", store.DocStateFetched))

	p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/interests/" + id}),
		http.StatusInternalServerError)
	assert.Contains(t, p.Detail, "NaN")
}

// TestRebuildInterests: a rebuild is queued once while it is pending, so
// a second request answers the first's job; one asked for while a
// rebuild runs queues the next.
func TestRebuildInterests(t *testing.T) {
	ctx := context.Background()
	s := newTestServer(t)
	rebuild := func() string {
		t.Helper()
		resp := s.do(t, request{method: http.MethodPost, path: "/v1/interests/rebuild"})
		require.Equal(t, http.StatusAccepted, resp.status, resp.body)
		var got map[string]string
		require.NoError(t, json.Unmarshal([]byte(resp.body), &got))
		return got["job_id"]
	}
	first := rebuild()
	assert.Equal(t, first, rebuild(), "the pending one")
	assert.Equal(t, 1, s.count(t, "jobs"))
	job, err := s.deps.Queue.GetByID(ctx, first)
	require.NoError(t, err)
	assert.JSONEq(t, `{"trigger":"manual"}`, string(job.Payload))

	claimed, err := s.deps.Queue.ClaimNext(ctx, []store.JobKind{store.JobKindCluster})
	require.NoError(t, err)
	require.Equal(t, first, claimed.ID)
	second := rebuild()
	assert.NotEqual(t, first, second, "a running rebuild doesn't count")
	assert.Equal(t, 2, s.count(t, "jobs"))

	disabled := newTestServer(t, func(d *Deps) { d.InsightEnabled = false })
	assertProblem(t, disabled.do(t, request{method: http.MethodPost, path: "/v1/interests/rebuild"}), http.StatusConflict)
	assert.Zero(t, disabled.count(t, "jobs"))
}
