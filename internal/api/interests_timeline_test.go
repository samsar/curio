package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// savedOn gives doc a bookmark saved at at, of saved date class.
func (s *testServer) savedOn(t *testing.T, doc *store.Document, at time.Time, class store.SavedDate) {
	t.Helper()
	_, err := s.db.Exec(`INSERT INTO bookmarks (id, tenant_id, document_id, url, saved_at, source, saved_date)
		VALUES (?, 'local', ?, ?, ?, ?, ?)`, uuid.NewString(), doc.ID, doc.URL,
		at.UTC().Format("2006-01-02T15:04:05.000Z"), store.SourceChrome, class)
	require.NoError(t, err)
}

// timeline answers GET path, expecting 200.
func (s *testServer) timeline(t *testing.T, path string) InterestsTimeline {
	t.Helper()
	resp := s.do(t, request{method: http.MethodGet, path: path})
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	var out InterestsTimeline
	require.NoError(t, json.Unmarshal([]byte(resp.body), &out))
	return out
}

// TestInterestsTimeline: the latest rebuild's saves by quarter, in the
// daemon's zone, each document once under its interest and area (a
// member, a loose fit, placed since) or Unsorted, undated ones apart; the
// window as applied.
func TestInterestsTimeline(t *testing.T) {
	zone := time.FixedZone("UTC-5", -5*60*60)
	s := newTestServer(t, func(d *Deps) { d.Location = zone })
	d := s.docs(t, "t", 6)
	f := s.newRun(t, store.InterestShapeAreas)
	area := f.area("Cooking")
	bread := f.interest("Bread", area, 0, d[0:2], d[2:3])
	f.unsorted(bread, d[3])
	run := f.commit(t)
	s.placeMapped(t, run, bread, d[4], mapPlace(1, 1, 1))
	s.savedOn(t, d[0], time.Date(2024, 1, 5, 12, 0, 0, 0, time.UTC), store.SavedDateExact)
	s.savedOn(t, d[1], time.Date(2024, 3, 31, 23, 30, 0, 0, zone), store.SavedDateApprox)
	s.savedOn(t, d[2], time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC), store.SavedDateExact)
	s.savedOn(t, d[3], time.Date(2024, 5, 2, 12, 0, 0, 0, time.UTC), store.SavedDateExact)
	s.savedOn(t, d[4], time.Date(2024, 2, 1, 12, 0, 0, 0, time.UTC), store.SavedDateUnknown)

	got := s.timeline(t, "/v1/interests/timeline")
	assert.Equal(t, run, got.Run.ID)
	assert.Equal(t, "areas", got.Run.Shape)
	assert.Equal(t, "quarter", got.Period)
	assert.Equal(t, []string{"2024 Q1", "2024 Q2"}, got.Periods)
	assert.Equal(t, []TimelineRow{
		{Kind: "area", ID: area, Label: "Cooking", Counts: []int{2, 1}, Total: 3},
		{Kind: "interest", ID: bread, AreaID: area, Label: "Bread", Counts: []int{2, 1}, Total: 3},
		{Kind: "unsorted", Label: "", Counts: []int{0, 1}, Total: 1},
	}, got.Rows, "the save late on 31 March in the daemon's zone is in Q1")
	assert.Equal(t, 4, got.Dated)
	assert.Equal(t, 1, got.Undated, "the placed one's date is unknown; the sixth is outside the run")
	assert.Nil(t, got.Window)

	got = s.timeline(t, "/v1/interests/timeline?saved_after=2024-04-01&period=year")
	assert.Equal(t, "year", got.Period)
	assert.Equal(t, []string{"2024"}, got.Periods)
	assert.Equal(t, 3, got.Dated, "the window's bound is midnight UTC, which the save late on 31 March passed")
	require.NotNil(t, got.Window)
	require.NotNil(t, got.Window.SavedAfter)
	assert.Equal(t, time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC), *got.Window.SavedAfter)
	assert.Nil(t, got.Window.SavedBefore)
}

// TestInterestsTimeline_Refused: a date that won't parse, a window that
// holds nothing and an unknown period are 400s naming the query
// parameter, never a search's filters.<field>.
func TestInterestsTimeline_Refused(t *testing.T) {
	s := newTestServer(t)
	for path, says := range map[string]string{
		"/v1/interests/timeline?saved_before=yesterday":                         "saved_before: ",
		"/v1/interests/timeline?saved_after=2024-02-01&saved_before=2024-01-01": "saved_after: must be before",
		"/v1/interests/timeline?period=week":                                    "period must be",
	} {
		p := assertProblem(t, s.do(t, request{method: http.MethodGet, path: path}), http.StatusBadRequest)
		assert.True(t, strings.HasPrefix(p.Detail, says), "%s: %s", path, p.Detail)
		assert.NotContains(t, p.Detail, "filters.", path)
	}
}

// TestInterestsTimeline_InsightOff: with insight.enabled false nothing is
// rebuilt, but the rebuild done before is read as ever.
func TestInterestsTimeline_InsightOff(t *testing.T) {
	s := newTestServer(t, func(d *Deps) { d.InsightEnabled, d.Interests = false, nil })
	d := s.docs(t, "t", 1)
	f := s.newRun(t, store.InterestShapeAreas)
	f.interest("Bread", f.area("Cooking"), 0, d, nil)
	run := f.commit(t)
	s.savedOn(t, d[0], time.Date(2024, 1, 5, 12, 0, 0, 0, time.UTC), store.SavedDateExact)
	got := s.timeline(t, "/v1/interests/timeline")
	assert.Equal(t, run, got.Run.ID)
	assert.Equal(t, 1, got.Dated)
}

// TestInterestsTimeline_NoRun: before any rebuild, a 404 of its own type,
// reason no_run.
func TestInterestsTimeline_NoRun(t *testing.T) {
	s := newTestServer(t)
	resp := s.do(t, request{method: http.MethodGet, path: "/v1/interests/timeline"})
	require.Equal(t, http.StatusNotFound, resp.status, resp.body)
	assert.Equal(t, "application/problem+json", resp.contentType)
	var p InterestsUnavailable
	require.NoError(t, json.Unmarshal([]byte(resp.body), &p))
	assert.Equal(t, InterestsProblemType, p.Type)
	assert.Equal(t, "no_run", p.Reason)
	assert.Equal(t, "interests unavailable", p.Title)
}

// TestInterestsTimeline_RereadsAcrossARebuild: a rebuild that commits
// between the run read and the rows read is answered from the newer run.
func TestInterestsTimeline_RereadsAcrossARebuild(t *testing.T) {
	s := newTestServer(t)
	d := s.docs(t, "r", 2)
	runs := make([]string, 0, 2)
	for i := range 2 {
		f := s.newRun(t, store.InterestShapeFlat)
		f.interest("Kafka", "", 0, d[i:i+1], nil)
		f.c.Outcome.NumDocuments = 1
		require.NoError(t, s.insights().CommitRun(context.Background(), f.c))
		runs = append(runs, f.c.RunID)
		s.savedOn(t, d[i], time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC), store.SavedDateExact)
	}
	s.deps.Insights = &rebuildMidRead{InsightStore: s.insights(), runs: []string{runs[0], runs[1]}}
	s.deps.TenantID = store.LocalTenantID
	got, err := s.deps.interestsTimeline(context.Background(), "", "", "")
	require.NoError(t, err)
	assert.Equal(t, runs[1], got.Run.ID)
	require.Len(t, got.Rows, 1)
	assert.Equal(t, 1, got.Rows[0].Total)
}
