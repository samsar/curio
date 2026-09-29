package store_test

import (
	"encoding/json"
	"testing"
	"time"
	_ "time/tzdata" // the daylight-saving tests need zones on any machine

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

func TestDocState_Valid(t *testing.T) {
	for _, s := range []store.DocState{store.DocStatePending, store.DocStateFetched, store.DocStateFailed, store.DocStateDead} {
		assert.True(t, s.Valid(), s)
	}
	for _, s := range []store.DocState{"", "bogus", "Fetched", "running"} {
		assert.False(t, s.Valid(), s)
	}
}

// TestFailureCauses: the list holds every cause once, in the order of the
// constants, and a caller can't change it for the next.
func TestFailureCauses(t *testing.T) {
	all := []store.FailureCause{
		store.FailureCauseDeadLink, store.FailureCauseAntiBot, store.FailureCauseLoginWall,
		store.FailureCauseJinaRefused, store.FailureCauseTLS, store.FailureCauseUnreachable,
		store.FailureCauseTimeout, store.FailureCauseNetwork, store.FailureCauseRateLimited,
		store.FailureCauseHTTPError, store.FailureCauseUnsupported, store.FailureCauseTooLarge,
		store.FailureCauseIndex, store.FailureCauseOther,
	}
	assert.Equal(t, all, store.FailureCauses())

	got := store.FailureCauses()
	got[0] = "bogus"
	assert.Equal(t, all, store.FailureCauses(), "a fresh slice each call")
}

func TestFailureCause_Valid(t *testing.T) {
	for _, c := range store.FailureCauses() {
		assert.True(t, c.Valid(), c)
	}
	for _, c := range []store.FailureCause{"", "bogus", "Anti_Bot", "failed"} {
		assert.False(t, c.Valid(), c)
	}
}

// TestFailureCause_State: a dead link, and nothing else, makes a document
// dead.
func TestFailureCause_State(t *testing.T) {
	for _, c := range store.FailureCauses() {
		want := store.DocStateFailed
		if c == store.FailureCauseDeadLink {
			want = store.DocStateDead
		}
		assert.Equal(t, want, c.State(), c)
	}
}

func TestContentType_Valid(t *testing.T) {
	for _, c := range []store.ContentType{store.ContentTypeArticle, store.ContentTypeRepo, store.ContentTypeVideo,
		store.ContentTypePDF, store.ContentTypeThread, store.ContentTypeUnknown} {
		assert.True(t, c.Valid(), c)
	}
	for _, c := range []store.ContentType{"", "bogus", "Article", "fetched"} {
		assert.False(t, c.Valid(), c)
	}
}

// The valid sets are the jobs table's CHECK constraints (migrations/001).
func TestJobStatus_Valid(t *testing.T) {
	for _, s := range []store.JobStatus{store.JobStatusPending, store.JobStatusRunning, store.JobStatusDone,
		store.JobStatusFailed} {
		assert.True(t, s.Valid(), s)
	}
	for _, s := range []store.JobStatus{"", "bogus", "Done", "fetched"} {
		assert.False(t, s.Valid(), s)
	}
}

func TestJobKind_Valid(t *testing.T) {
	for _, k := range []store.JobKind{store.JobKindFetch, store.JobKindIndex, store.JobKindImport,
		store.JobKindCluster, store.JobKindSummarize} {
		assert.True(t, k.Valid(), k)
	}
	for _, k := range []store.JobKind{"", "bogus", "Fetch", "done"} {
		assert.False(t, k.Valid(), k)
	}
}

func TestJobStatus_IsFinished(t *testing.T) {
	cases := map[store.JobStatus]bool{
		store.JobStatusPending: false,
		store.JobStatusRunning: false,
		store.JobStatusDone:    true,
		store.JobStatusFailed:  true,
		"":                     false,
		"bogus":                false,
	}
	for status, want := range cases {
		assert.Equal(t, want, status.IsFinished(), status)
	}
}

func TestClusterRunStatus_IsFinished(t *testing.T) {
	cases := map[store.ClusterRunStatus]bool{
		store.ClusterRunRunning: false,
		store.ClusterRunDone:    true,
		store.ClusterRunFailed:  true,
		"":                      false,
	}
	for status, want := range cases {
		assert.Equal(t, want, status.IsFinished(), status)
	}
}

func TestNewDocumentJob(t *testing.T) {
	job, err := store.NewDocumentJob("local", store.JobKindIndex, "doc-1")
	require.NoError(t, err)
	assert.Equal(t, "local", job.TenantID)
	assert.Equal(t, store.JobKindIndex, job.Kind)
	assert.Empty(t, job.ID, "the queue assigns it")
	assert.JSONEq(t, `{"document_id":"doc-1"}`, string(job.Payload))

	var p store.DocumentJobPayload
	require.NoError(t, json.Unmarshal(job.Payload, &p))
	assert.Equal(t, "doc-1", p.DocumentID)
}

func TestBookmarkOrder_Valid(t *testing.T) {
	for _, o := range []store.BookmarkOrder{store.BookmarkOrderCreated, store.BookmarkOrderSaved} {
		assert.True(t, o.Valid(), o)
	}
	for _, o := range []store.BookmarkOrder{"", "updated", "Saved", "saved_at"} {
		assert.False(t, o.Valid(), o)
	}
}

// TestBookmarkOrder_Key: a bookmark's position is its save time in the
// saved order, and when curio added it otherwise, the default included.
func TestBookmarkOrder_Key(t *testing.T) {
	saved := time.Date(2019, 5, 1, 12, 0, 0, 0, time.UTC)
	created := time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)
	b := &store.Bookmark{ID: "b1", SavedAt: saved, CreatedAt: created}
	assert.Equal(t, store.PageKey{At: saved, ID: "b1"}, store.BookmarkOrderSaved.Key(b))
	assert.Equal(t, store.PageKey{At: created, ID: "b1"}, store.BookmarkOrderCreated.Key(b))
	assert.Equal(t, store.PageKey{At: created, ID: "b1"}, store.BookmarkOrder("").Key(b), "the default order")
}

func TestSearchFilters_IsEmpty(t *testing.T) {
	assert.True(t, store.SearchFilters{}.IsEmpty())
	assert.True(t, store.SearchFilters{ContentType: []string{}, Host: []string{}}.IsEmpty(), "empty slices filter nothing")
	for name, f := range map[string]store.SearchFilters{
		"content type": {ContentType: []string{"pdf"}},
		"host":         {Host: []string{"example.com"}},
		"source":       {Source: []string{"chrome"}},
		"exclude":      {ExcludeDocumentID: "doc-1"},
	} {
		assert.False(t, f.IsEmpty(), name)
	}
}

func TestNullableString(t *testing.T) {
	assert.Nil(t, store.NullableString(""), "empty is NULL")
	got := store.NullableString("x")
	require.NotNil(t, got)
	assert.Equal(t, "x", *got)
}

// The valid set is queue_settings' CHECK constraint (migrations/011).
func TestThrottle_Valid(t *testing.T) {
	for _, th := range []store.Throttle{store.ThrottleNormal, store.ThrottleGentle} {
		assert.True(t, th.Valid(), th)
	}
	for _, th := range []store.Throttle{"", "Gentle", "fast"} {
		assert.False(t, th.Valid(), th)
	}
}

// TestThrottle_Limit: the gentle throttle caps fetches at 4 and index jobs
// at 1, never above the pool; the normal one, and every other kind, run
// the whole pool.
func TestThrottle_Limit(t *testing.T) {
	cases := []struct {
		throttle store.Throttle
		kind     store.JobKind
		pool     int
		want     int
	}{
		{store.ThrottleGentle, store.JobKindFetch, 16, 4},
		{store.ThrottleGentle, store.JobKindFetch, 2, 2},
		{store.ThrottleGentle, store.JobKindIndex, 4, 1},
		{store.ThrottleGentle, store.JobKindCluster, 1, 1},
		{store.ThrottleGentle, store.JobKindImport, 8, 8},
		{store.ThrottleNormal, store.JobKindFetch, 16, 16},
		{store.ThrottleNormal, store.JobKindIndex, 4, 4},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, tc.throttle.Limit(tc.kind, tc.pool), "%s %s of %d", tc.throttle, tc.kind, tc.pool)
	}
	_, capped := store.ThrottleNormal.Cap(store.JobKindFetch)
	assert.False(t, capped)
}

func TestParseDailyWindow(t *testing.T) {
	accepted := map[string]store.DailyWindow{
		"22:00-07:00": {Start: 22 * 60, End: 7 * 60},
		"7:00-9:30":   {Start: 7 * 60, End: 9*60 + 30},
		"23:59-00:00": {Start: 23*60 + 59, End: 0},
		"00:00-23:59": {Start: 0, End: 23*60 + 59},
	}
	for s, want := range accepted {
		got, err := store.ParseDailyWindow(s)
		require.NoError(t, err, s)
		assert.Equal(t, want, got, s)
		assert.True(t, got.Valid(), s)
		again, err := store.ParseDailyWindow(got.String())
		require.NoError(t, err, "String reads back: %s", got)
		assert.Equal(t, got, again, s)
	}
	w, err := store.ParseDailyWindow("7:00-9:30")
	require.NoError(t, err)
	assert.Equal(t, "07:00-09:30", w.String())

	for _, s := range []string{"", "off", "22:00", "22-07", "22:00-22:00", "24:00-07:00", "22:60-07:00",
		"22:00-07:00:00", " 22:00-07:00", "22:00-7:0", "22:00--07:00"} {
		_, err := store.ParseDailyWindow(s)
		assert.Error(t, err, "%q", s)
	}
}

func TestDailyWindow_Valid(t *testing.T) {
	for _, w := range []store.DailyWindow{{}, {Start: 0, End: 1}, {Start: 1439, End: 0}} {
		assert.True(t, w.Valid(), w)
	}
	for _, w := range []store.DailyWindow{{Start: 60, End: 60}, {Start: -1, End: 60}, {Start: 60, End: 1440}} {
		assert.False(t, w.Valid(), w)
	}
	assert.True(t, store.DailyWindow{}.IsZero())
	assert.False(t, store.DailyWindow{Start: 0, End: 1}.IsZero(), "a window that starts at midnight is set")
	assert.True(t, store.DailyWindow{}.Contains(time.Now()), "no schedule excludes nothing")
	assert.True(t, store.DailyWindow{}.NextStart(time.Now()).IsZero())
	assert.True(t, store.DailyWindow{Start: 60, End: 60}.NextStart(time.Now()).IsZero())
}

func mustWindow(t *testing.T, s string) store.DailyWindow {
	t.Helper()
	w, err := store.ParseDailyWindow(s)
	require.NoError(t, err)
	return w
}

// TestDailyWindow_Edges: a window holds from its start up to, not
// including, its end, overnight or within the day.
func TestDailyWindow_Edges(t *testing.T) {
	zone := time.FixedZone("UTC-5", -5*60*60)
	at := func(day, hour, minute int) time.Time { return time.Date(2026, 6, day, hour, minute, 0, 0, zone) }

	overnight := mustWindow(t, "22:00-07:00")
	for _, tm := range []time.Time{at(10, 22, 0), at(10, 23, 59), at(11, 0, 0), at(11, 6, 59)} {
		assert.True(t, overnight.Contains(tm), tm)
	}
	for _, tm := range []time.Time{at(11, 7, 0), at(10, 21, 59)} {
		assert.False(t, overnight.Contains(tm), tm)
	}
	assert.Equal(t, at(10, 22, 0), overnight.NextStart(at(10, 21, 59)))
	assert.Equal(t, at(11, 22, 0), overnight.NextStart(at(11, 7, 0)))

	daytime := mustWindow(t, "09:00-17:00")
	for _, tm := range []time.Time{at(10, 9, 0), at(10, 16, 59)} {
		assert.True(t, daytime.Contains(tm), tm)
	}
	assert.False(t, daytime.Contains(at(10, 17, 0)))
	assert.Equal(t, at(11, 9, 0), daytime.NextStart(at(10, 17, 0)))
	assert.Equal(t, at(10, 9, 0), daytime.NextStart(at(10, 8, 59)))
	assert.Equal(t, zone, daytime.NextStart(at(10, 8, 59)).Location(), "in t's location")
}

func loadLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	require.NoError(t, err)
	return loc
}

// TestDailyWindow_DaylightSavingGap: on 2026-03-08 New York's clocks jump
// from 02:00 EST to 03:00 EDT. A window starting in the gap opens as it
// ends; one wholly inside it opens the next day. Lord Howe's gap (02:00 to
// 02:30 on 2026-10-04) is one time.Date resolves forward rather than back.
func TestDailyWindow_DaylightSavingGap(t *testing.T) {
	ny := loadLocation(t, "America/New_York")
	before := time.Date(2026, 3, 8, 1, 45, 0, 0, ny)

	straddles := mustWindow(t, "02:30-04:00")
	opens := straddles.NextStart(before)
	assert.Equal(t, time.Date(2026, 3, 8, 3, 0, 0, 0, ny), opens)
	assert.Equal(t, "EDT", opens.Format("MST"))
	assert.True(t, straddles.Contains(opens))

	inside := mustWindow(t, "02:30-02:45")
	assert.Equal(t, time.Date(2026, 3, 9, 2, 30, 0, 0, ny), inside.NextStart(before))

	lordHowe := loadLocation(t, "Australia/Lord_Howe")
	w := mustWindow(t, "02:15-03:00")
	assert.Equal(t, time.Date(2026, 10, 4, 2, 30, 0, 0, lordHowe),
		w.NextStart(time.Date(2026, 10, 4, 1, 45, 0, 0, lordHowe)))
}

// TestDailyWindow_NextStartAcrossDaylightSaving: every minute of New
// York's spring-forward and fall-back days, NextStart is a later moment,
// less than 49 hours on, when the window holds.
func TestDailyWindow_NextStartAcrossDaylightSaving(t *testing.T) {
	ny := loadLocation(t, "America/New_York")
	windows := []store.DailyWindow{
		mustWindow(t, "22:00-07:00"), mustWindow(t, "02:30-04:00"),
		mustWindow(t, "02:30-02:45"), mustWindow(t, "01:00-01:30"),
	}
	for _, day := range []time.Time{time.Date(2026, 3, 8, 0, 0, 0, 0, ny), time.Date(2026, 11, 1, 0, 0, 0, 0, ny)} {
		next := day.AddDate(0, 0, 1)
		for tm := day; tm.Before(next); tm = tm.Add(time.Minute) {
			for _, w := range windows {
				got := w.NextStart(tm)
				if !got.After(tm) || got.Sub(tm) >= 49*time.Hour || !w.Contains(got) {
					t.Errorf("%s.NextStart(%s) = %s", w, tm.Format(time.RFC3339), got.Format(time.RFC3339))
				}
			}
		}
	}
}
