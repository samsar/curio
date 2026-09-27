package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/jobs"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/store/sqlite/sqlitetest"
)

func putQueue(t *testing.T, s *testServer, body string) response {
	t.Helper()
	return s.do(t, request{method: http.MethodPut, path: "/v1/queue", contentType: "application/json", body: body})
}

func decodeQueue(t *testing.T, resp response) QueueResponse {
	t.Helper()
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	var q QueueResponse
	require.NoError(t, json.Unmarshal([]byte(resp.body), &q))
	return q
}

func TestQueue_Get(t *testing.T) {
	s := newTestServer(t)
	ctx := context.Background()
	for _, j := range []*store.Job{
		{TenantID: "local", Kind: store.JobKindFetch},
		{TenantID: "local", Kind: store.JobKindFetch, Status: store.JobStatusRunning},
		{TenantID: "other", Kind: store.JobKindFetch},
		{TenantID: "local", Kind: store.JobKindFetch, Status: store.JobStatusDone},
		{TenantID: "local", Kind: store.JobKindIndex, Status: store.JobStatusFailed},
		{TenantID: "local", Kind: store.JobKindCluster},
	} {
		require.NoError(t, s.deps.Queue.Enqueue(ctx, j))
	}

	q := decodeQueue(t, s.do(t, request{method: http.MethodGet, path: "/v1/queue"}))
	assert.Equal(t, QueueResponse{
		Throttle: "normal",
		State:    "open",
		Kinds: []QueueKindResponse{
			{Kind: "fetch", Limit: testPools.Fetch, Running: 1, Pending: 2},
			{Kind: "index", Limit: testPools.Index},
			{Kind: "cluster", Limit: 1, Pending: 1},
		},
	}, q)
}

// TestQueue_PauseIsStored: a pause set through the API is what the next
// daemon's gate loads.
func TestQueue_PauseIsStored(t *testing.T) {
	s := newTestServer(t)
	q := decodeQueue(t, putQueue(t, s, `{"paused":true}`))
	assert.True(t, q.Paused)
	assert.Equal(t, "closed", q.State)
	assert.Equal(t, "paused", q.Reason)
	assert.True(t, q.OpensAt.IsZero(), "a pause has no end")

	next, err := jobs.NewQueueGate(context.Background(), sqlite.NewQueueSettings(s.db), testPools, s.deps.Log)
	require.NoError(t, err)
	assert.Equal(t, jobs.ReasonPaused, next.State(time.Now()).Closed)
}

func TestQueue_Schedule(t *testing.T) {
	s := newTestServer(t)
	q := decodeQueue(t, putQueue(t, s, `{"schedule":"7:00-9:30"}`))
	assert.Equal(t, "07:00-09:30", q.Schedule)

	window := windowExcludingNow()
	q = decodeQueue(t, putQueue(t, s, `{"schedule":"`+window+`"}`))
	assert.Equal(t, window, q.Schedule)
	assert.Equal(t, "closed", q.State)
	assert.Equal(t, "outside_schedule", q.Reason)
	assert.Equal(t, time.UTC, q.OpensAt.Location())
	w, err := store.ParseDailyWindow(window)
	require.NoError(t, err)
	// NextStart, not now plus two hours: on a daylight-saving day the two differ.
	assert.WithinDuration(t, w.NextStart(time.Now()), q.OpensAt, time.Minute)

	q = decodeQueue(t, putQueue(t, s, `{"schedule":"off"}`))
	assert.Empty(t, q.Schedule)
	assert.Equal(t, "open", q.State)
	assert.Empty(t, q.Reason)
	assert.True(t, q.OpensAt.IsZero())
}

// countingSettings counts Puts through to the store.
type countingSettings struct {
	store.QueueSettingsStore
	puts atomic.Int32
}

func (c *countingSettings) Put(ctx context.Context, s store.QueueSettings) error {
	c.puts.Add(1)
	return c.QueueSettingsStore.Put(ctx, s)
}

// TestQueue_UpdateSeveralFields: every field given changes, in one update.
func TestQueue_UpdateSeveralFields(t *testing.T) {
	var settings *countingSettings
	s := newTestServer(t, func(d *Deps) {
		settings = &countingSettings{QueueSettingsStore: sqlite.NewQueueSettings(sqlitetest.NewDB(t))}
		d.Gate = newGate(t, settings)
	})

	q := decodeQueue(t, putQueue(t, s, `{"paused":true,"throttle":"gentle","schedule":"22:00-07:00"}`))
	assert.True(t, q.Paused)
	assert.Equal(t, "gentle", q.Throttle)
	assert.Equal(t, "22:00-07:00", q.Schedule)
	assert.Equal(t, []QueueKindResponse{{Kind: "fetch", Limit: 4}, {Kind: "index", Limit: 1}, {Kind: "cluster", Limit: 1}},
		q.Kinds)
	assert.EqualValues(t, 1, settings.puts.Load())
}

func newGate(t *testing.T, settings store.QueueSettingsStore) *jobs.QueueGate {
	t.Helper()
	g, err := jobs.NewQueueGate(context.Background(), settings, testPools, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	return g
}

func TestQueue_UpdateRefusals(t *testing.T) {
	s := newTestServer(t)
	cases := []struct {
		name string
		body string
		want []string // in the detail
	}{
		{"nothing to change", `{}`, []string{"paused", "throttle", "schedule"}},
		{"an unknown throttle", `{"throttle":"fast"}`, []string{`"fast"`, "normal", "gentle"}},
		{"a malformed schedule", `{"schedule":"22:00"}`, []string{`"22:00"`, "HH:MM-HH:MM", "22:00-07:00", "off"}},
		{"an hour past the day", `{"schedule":"25:00-07:00"}`, []string{"25:00", "22:00-07:00", "off"}},
		{"equal ends", `{"schedule":"22:00-22:00"}`, []string{"same time", "22:00-07:00", "off"}},
		{"a wrong type", `{"paused":"yes"}`, []string{"malformed JSON body"}},
		{"an unknown field", `{"paused":true,"speed":"slow"}`, []string{`"speed"`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := assertProblem(t, putQueue(t, s, tc.body), http.StatusBadRequest)
			for _, want := range tc.want {
				assert.Contains(t, p.Detail, want)
			}
		})
	}
	q := decodeQueue(t, s.do(t, request{method: http.MethodGet, path: "/v1/queue"}))
	assert.False(t, q.Paused, "no refused request changed anything")
	assert.Equal(t, "normal", q.Throttle)
}

// TestQueue_ForeignOriginChangesNothing: a web page can't pause the queue.
func TestQueue_ForeignOriginChangesNothing(t *testing.T) {
	s := newTestServer(t)
	resp := s.do(t, request{method: http.MethodPut, path: "/v1/queue", origin: "https://attacker.example",
		contentType: "application/json", body: `{"paused":true}`})
	assertProblem(t, resp, http.StatusForbidden)
	assert.False(t, s.deps.Gate.State(time.Now()).Settings.Paused)
}

// failingSettings loads the defaults and fails every Put.
type failingSettings struct{ store.QueueSettingsStore }

func (failingSettings) Put(context.Context, store.QueueSettings) error {
	return errors.New("disk I/O error")
}

// failingCounts fails the queue counts.
type failingCounts struct{ store.JobStore }

func (failingCounts) QueueCounts(context.Context) (map[store.JobKind]store.QueueCount, error) {
	return nil, errors.New("database is locked")
}

func TestQueue_ServerErrors(t *testing.T) {
	s := newTestServer(t, func(d *Deps) {
		d.Gate = newGate(t, failingSettings{sqlite.NewQueueSettings(sqlitetest.NewDB(t))})
	})
	p := assertProblem(t, putQueue(t, s, `{"paused":true}`), http.StatusInternalServerError)
	assert.Contains(t, p.Detail, "disk I/O error")

	s = newTestServer(t, func(d *Deps) { d.Queue = failingCounts{d.Queue} })
	p = assertProblem(t, s.do(t, request{method: http.MethodGet, path: "/v1/queue"}), http.StatusInternalServerError)
	assert.Contains(t, p.Detail, "database is locked")
}
