package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/store/sqlite/sqlitetest"
)

// countingRebuilder is the insight engine, counting the rebuilds it is
// asked for.
type countingRebuilder struct {
	*insight.Engine
	rebuilds atomic.Int32
}

func (c *countingRebuilder) Rebuild(ctx context.Context, tenantID string, trigger store.RunTrigger) (string, error) {
	c.rebuilds.Add(1)
	return c.Engine.Rebuild(ctx, tenantID, trigger)
}

// clusterWorld is what a daemon that died during a rebuild leaves behind:
// the cluster job running with attempts counted, and the interest run it
// created still running; with the engine and the cluster pool over them.
type clusterWorld struct {
	deps     Deps
	insights *sqlitestore.Insights
	engine   *countingRebuilder
	job      *store.Job
	run      *store.InterestRun
	pool     Pool
}

func newClusterWorld(t *testing.T, attempts int) *clusterWorld {
	t.Helper()
	deps, db, _ := newTestDeps(t)
	ctx := context.Background()
	w := &clusterWorld{deps: deps, insights: sqlitestore.NewInsights(db)}
	w.engine = &countingRebuilder{Engine: insight.New(deps.Documents, sqlitestore.NewChunks(db, sqlitetest.Width(t, db)),
		w.insights, insight.NewLouvainGrouper(quietLog), nil, insight.Config{}, quietLog)}
	w.deps.Insight = w.engine
	w.job = &store.Job{TenantID: "local", Kind: store.JobKindCluster, Payload: []byte(`{"trigger":"auto"}`),
		Status: store.JobStatusRunning, Attempts: attempts}
	require.NoError(t, deps.Queue.Enqueue(ctx, w.job))
	w.run = &store.InterestRun{TenantID: "local", Trigger: store.RunTriggerAuto, Grouper: "louvain",
		RunOutcome: store.RunOutcome{Kind: store.RunKindWarm, Shape: store.InterestShapeAreas}}
	require.NoError(t, w.insights.CreateRun(ctx, w.run))
	w.pool = NewPools(w.deps, PoolSizes{Fetch: 1, Index: 1}, WorkerOptions{Log: quietLog})[2]
	return w
}

// assertAbandoned checks that the rebuild was recorded as failed once, and
// never run again.
func (w *clusterWorld) assertAbandoned(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	assert.Equal(t, store.JobStatusFailed, getJob(t, w.deps.Queue, w.job.ID).Status)
	assert.Zero(t, w.engine.rebuilds.Load(), "no rebuild ran")
	run, err := w.insights.GetRun(ctx, w.run.ID)
	require.NoError(t, err)
	assert.Equal(t, store.InterestRunFailed, run.Status)
	require.NotNil(t, run.Error)
	assert.Contains(t, *run.Error, "the daemon stopped during this rebuild")
	st, err := w.insights.State(ctx, "local")
	require.NoError(t, err)
	assert.Equal(t, 1, st.Failures, "one failure, for the scheduler's backoff")
}

// TestWorker_AnOrphanedRebuildIsRecordedNotRerun: the rebuild a daemon
// died during is requeued by the next one, which claims it, records it as
// one failed rebuild and runs nothing: what killed the daemon would
// otherwise kill it again at every start.
func TestWorker_AnOrphanedRebuildIsRecordedNotRerun(t *testing.T) {
	w := newClusterWorld(t, 1)
	require.NoError(t, w.pool.Worker.RecoverOrphans(context.Background()))
	require.Equal(t, store.JobStatusPending, getJob(t, w.deps.Queue, w.job.ID).Status, "requeued, its attempt kept")

	stop := startWorker(t, w.pool.Worker)
	waitForJob(t, w.deps.Queue, w.job.ID, statusIs(store.JobStatusFailed))
	stop()
	w.assertAbandoned(t)
}

// TestWorker_AnExhaustedOrphanedRebuildIsRecorded: an orphaned rebuild
// with no attempts left, which RecoverOrphans fails itself, is recorded
// the same way, by the cluster pool's hook.
func TestWorker_AnExhaustedOrphanedRebuildIsRecorded(t *testing.T) {
	w := newClusterWorld(t, 5)
	require.NoError(t, w.pool.Worker.RecoverOrphans(context.Background()))
	w.assertAbandoned(t)
}

// blockingGrouper groups nothing: it says it started, then waits for its
// context to end.
type blockingGrouper struct{ started chan struct{} }

func (g blockingGrouper) Group(ctx context.Context, _ insight.GroupInput) (insight.Grouping, error) {
	close(g.started)
	<-ctx.Done()
	return insight.Grouping{}, ctx.Err()
}
func (blockingGrouper) Name() string           { return "blocking" }
func (blockingGrouper) Params() map[string]any { return nil }

// TestWorker_AnInterruptedRebuildIsRequeued: a rebuild the shutdown cut
// short goes back to the queue with its attempt refunded, though its
// error is permanent, and counts no failure: the next start runs it.
func TestWorker_AnInterruptedRebuildIsRequeued(t *testing.T) {
	deps, db, _ := newTestDeps(t)
	ctx := context.Background()
	insights := sqlitestore.NewInsights(db)
	started := make(chan struct{})
	deps.Insight = insight.New(deps.Documents, sqlitestore.NewChunks(db, sqlitetest.Width(t, db)), insights,
		blockingGrouper{started: started}, nil, insight.Config{}, quietLog)
	job, _, err := EnqueueRebuild(ctx, deps.Queue, "local", store.RunTriggerAuto)
	require.NoError(t, err)
	pool := NewPools(deps, PoolSizes{Fetch: 1, Index: 1}, WorkerOptions{Log: quietLog})[2]

	stop := startWorker(t, pool.Worker)
	<-started
	stop()
	got := getJob(t, deps.Queue, job.ID)
	assert.Equal(t, store.JobStatusPending, got.Status)
	assert.Zero(t, got.Attempts, "the attempt is refunded")
	st, err := insights.State(ctx, "local")
	require.NoError(t, err)
	assert.Zero(t, st.Failures, "a cancelled rebuild counts no failure")
	run, err := insights.LatestRun(ctx, "local", "")
	require.NoError(t, err)
	assert.Equal(t, store.InterestRunFailed, run.Status, "its run is failed, not left running")
}

// TestWorker_OnFinishedSeesTheOutcome: the finished callback runs once the
// job's outcome is recorded, so a watcher reading the queue then sees it.
func TestWorker_OnFinishedSeesTheOutcome(t *testing.T) {
	deps, _, _ := newTestDeps(t)
	ctx := context.Background()
	job, _, err := EnqueueRebuild(ctx, deps.Queue, "local", store.RunTriggerAuto)
	require.NoError(t, err)
	seen := make(chan store.JobStatus, 1)
	w := NewWorker(deps.Queue, WorkerOptions{Log: quietLog})
	w.Register(store.JobKindCluster, func(context.Context, *store.Job) error { return nil })
	w.OnFinished(store.JobKindCluster, func() { seen <- getJob(t, deps.Queue, job.ID).Status })

	stop := startWorker(t, w)
	defer stop()
	assert.Equal(t, store.JobStatusDone, <-seen)
}

// TestPools_NothingToGroupBacksOff: a rebuild that finds no vector to group
// keeps the done run and changes nothing the scheduler reads but the
// failures it counts, so the check the end of the job kicks queues no
// other until the backoff passes. Were it to succeed changing nothing, the
// rebuild would still be due, and each would queue the next at once. The
// library's documents were indexed without a chunk; the scheduler, on a
// clock past the settle window, checks through Run, at the kicks the pools
// and the test give it.
func TestPools_NothingToGroupBacksOff(t *testing.T) {
	deps, db, _ := newTestDeps(t)
	ctx := context.Background()
	ins := sqlitestore.NewInsights(db)
	engine := insight.New(deps.Documents, sqlitestore.NewChunks(db, sqlitetest.Width(t, db)), ins,
		insight.NewLouvainGrouper(quietLog), nil, insight.Config{}, quietLog)
	deps.Insight = engine
	start := time.Now()
	var clock atomic.Pointer[time.Time]
	at := func(d time.Duration) { now := start.Add(d); clock.Store(&now) }
	at(5 * time.Minute)
	sched := insight.NewScheduler(insight.SchedulerOptions{
		TenantID: "local",
		Library:  insight.NewLibrary(ins, deps.Documents, sqlitestore.NewJobs(db)),
		Enqueue: func(ctx context.Context, tenantID string, trigger store.RunTrigger) (*store.Job, bool, error) {
			return EnqueueRebuild(ctx, deps.Queue, tenantID, trigger)
		},
		ParamsChanged: engine.ParamsChanged,
		Config:        insight.SchedulerConfig{Settle: time.Minute, Interval: time.Hour},
		Now:           func() time.Time { return *clock.Load() },
		Log:           quietLog,
	})
	deps.KickInterests = sched.Kick
	added := 0
	chunkless := func(n int) {
		for range n {
			added++
			doc := &store.Document{TenantID: "local", URL: fmt.Sprintf("https://example.com/%d", added)}
			require.NoError(t, deps.Documents.Create(ctx, doc))
			require.NoError(t, deps.Documents.MarkFetched(ctx, doc.ID))
		}
	}
	count := func(c require.TestingT, query string) int {
		var n int
		require.NoError(c, db.QueryRow(query).Scan(&n))
		return n
	}
	settlesAt := func(state insight.RebuildState, jobs int) {
		t.Helper()
		require.EventuallyWithT(t, func(c *assert.CollectT) {
			assert.Equal(c, state, sched.Snapshot().State)
			assert.Zero(c, count(c, `SELECT count(*) FROM jobs WHERE kind = 'cluster' AND status IN ('pending', 'running')`))
		}, 5*time.Second, 10*time.Millisecond)
		assert.Equal(t, jobs, count(t, `SELECT count(*) FROM jobs WHERE kind = 'cluster'`))
	}

	chunkless(insight.FirstRebuildAt)
	pool := NewPools(deps, PoolSizes{Fetch: 1, Index: 1}, WorkerOptions{PollInterval: 10 * time.Millisecond, Log: quietLog})[2]
	stopWorker := startWorker(t, pool.Worker)
	defer stopWorker()
	runCtx, cancel := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() { sched.Run(runCtx); close(stopped) }()
	defer func() { cancel(); <-stopped }()
	settlesAt(insight.StateCurrent, 1)
	assert.Equal(t, 1, count(t, `SELECT count(*) FROM interest_runs WHERE status = 'done' AND num_documents = 0`),
		"the first rebuild records an empty grouping")

	chunkless(insight.MinChanges)
	sched.Kick()
	settlesAt(insight.StateFailing, 2)
	first := sched.Snapshot()
	assert.Contains(t, first.LastError, "no fetched document has a usable vector")
	assert.WithinDuration(t, start.Add(insight.RetryAfter), first.RetryAt, time.Minute)

	at(insight.RetryAfter + time.Minute)
	sched.Kick()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.True(c, sched.Snapshot().RetryAt.After(first.RetryAt), "the retry failed too")
	}, 5*time.Second, 10*time.Millisecond)
	settlesAt(insight.StateFailing, 3)
	assert.WithinDuration(t, start.Add(insight.RetryDelay(2)), sched.Snapshot().RetryAt, time.Minute)
	assert.Equal(t, 1, count(t, `SELECT count(*) FROM interest_runs`), "the empty grouping stands alone")
}

// doneRunWith commits a run of one interest whose centroid is centroid,
// the tenant's latest done run, and returns it and the interest.
func doneRunWith(t *testing.T, ins *sqlitestore.Insights, centroid []float32) (*store.InterestRun, string) {
	t.Helper()
	ctx := context.Background()
	run := &store.InterestRun{TenantID: "local", Trigger: store.RunTriggerFirst, Grouper: "test",
		RunOutcome: store.RunOutcome{Kind: store.RunKindFresh, Shape: store.InterestShapeFlat}}
	require.NoError(t, ins.CreateRun(ctx, run))
	interest := "interest-" + run.ID
	require.NoError(t, ins.CommitRun(ctx, store.RunCommit{RunID: run.ID, TenantID: "local",
		Outcome:       store.RunOutcome{Kind: store.RunKindFresh, Shape: store.InterestShapeFlat, NumInterests: 1},
		NewIdentities: []store.Interest{{ID: interest, Level: store.InterestLevelInterest}},
		Groups: []store.InterestGroup{{Interest: store.Interest{ID: interest, Level: store.InterestLevelInterest}, Size: 1,
			Cohesion: 1, Centroid: centroid}},
	}))
	return run, interest
}

// unit is the unit vector along signs: each component +1 or -1 by its
// index's parity when alternate, all +1 otherwise.
func unit(dim int, alternate bool) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = float32(1 / math.Sqrt(float64(dim)))
		if alternate && i%2 == 1 {
			v[i] = -v[i]
		}
	}
	return v
}

// TestIndexHandler_PlacesTheDocument: a document indexed while a done run
// is current has its placement when the index job returns: in the
// interest its vector is near (the fake embedder's vectors are all equal
// components), in Unsorted when none is.
func TestIndexHandler_PlacesTheDocument(t *testing.T) {
	for name, alternate := range map[string]bool{"joined": false, "unsorted": true} {
		t.Run(name, func(t *testing.T) {
			deps, db, _ := newTestDeps(t)
			ctx := context.Background()
			dim := sqlitetest.Width(t, db)
			ins := sqlitestore.NewInsights(db)
			run, interest := doneRunWith(t, ins, unit(dim, alternate))
			deps.Placer = insight.NewPlacer(ins, sqlitestore.NewChunks(db, dim), nil, quietLog)
			doc := &store.Document{TenantID: "local", URL: "https://example.com/new", ContentType: store.ContentTypeArticle}
			require.NoError(t, deps.Documents.Create(ctx, doc))
			require.NoError(t, fetchHandler(deps)(ctx, docJob(t, store.JobKindFetch, doc.ID)))
			require.NoError(t, indexHandler(deps)(ctx, docJob(t, store.JobKindIndex, doc.ID)))

			into := interest
			if alternate {
				into = ""
			}
			placed, err := ins.Placements(ctx, run.ID, into, 0)
			require.NoError(t, err)
			require.Len(t, placed, 1)
			assert.Equal(t, doc.ID, placed[0].DocumentID)
			want := 1.0
			if alternate {
				want = 0
			}
			assert.InDelta(t, want, placed[0].Similarity, 1e-5)
		})
	}
}

// failingPlacements fails, or panics on, every placement write.
type failingPlacements struct {
	store.InsightStore
	panic bool
}

func (f failingPlacements) PlaceDocument(context.Context, string, store.Placement) (bool, error) {
	if f.panic {
		panic("placement write")
	}
	return false, errors.New("database is locked")
}

// TestIndexHandler_PlacementNeverFailsTheJob: a placement that fails, or
// panics, leaves the index job done and its document fetched, with one
// warning for the run however many documents it fails for.
func TestIndexHandler_PlacementNeverFailsTheJob(t *testing.T) {
	for name, panics := range map[string]bool{"a store error": false, "a panic": true} {
		t.Run(name, func(t *testing.T) {
			deps, db, _ := newTestDeps(t)
			ctx := context.Background()
			dim := sqlitetest.Width(t, db)
			ins := sqlitestore.NewInsights(db)
			doneRunWith(t, ins, unit(dim, false))
			var logs bytes.Buffer
			deps.Placer = insight.NewPlacer(failingPlacements{InsightStore: ins, panic: panics},
				sqlitestore.NewChunks(db, dim), nil, slog.New(slog.NewTextHandler(&logs, nil)))
			docs := make([]string, 0, 2)
			for i := range 2 {
				doc := &store.Document{TenantID: "local", URL: fmt.Sprintf("https://example.com/%d", i),
					ContentType: store.ContentTypeArticle}
				require.NoError(t, deps.Documents.Create(ctx, doc))
				require.NoError(t, deps.Queue.Enqueue(ctx, docJob(t, store.JobKindFetch, doc.ID)))
				docs = append(docs, doc.ID)
			}

			runPoolsUntil(t, deps, func(c *assert.CollectT) {
				var done int
				require.NoError(c, db.QueryRow(`SELECT count(*) FROM jobs WHERE kind = 'index' AND status = 'done'`).Scan(&done))
				assert.Equal(c, 2, done, "the index jobs are done")
			})
			for _, id := range docs {
				got, err := deps.Documents.GetByID(ctx, id)
				require.NoError(t, err)
				assert.Equal(t, store.DocStateFetched, got.State)
			}
			assert.Equal(t, 1, strings.Count(logs.String(), "level=WARN"), logs.String())
			assert.Contains(t, logs.String(), "interests: placement failed")
		})
	}
}
