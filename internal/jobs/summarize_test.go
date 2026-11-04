package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/generator"
	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/summarize"
)

// pageModel is a scripted summaries' model: it answers the fitness check's
// pages as a fit model does, and every other page with page; while hold is
// set, a page's call waits for hold to close, or its context to end.
type pageModel struct {
	mu      sync.Mutex
	page    func(prompt string) (string, error)
	hold    chan struct{}
	calling chan struct{} // receives once a held call has begun
	calls   int
	pingErr error
	// probeErr, when set, fails every probe of the fitness check, each
	// counted in probes.
	probeErr error
	probes   int
}

const pageSummary = `{"kind":"article","site":"Example Times","author":"","about":"Why queues back up.",` +
	`"entities":[],"claims":[],"descriptions":["a made-up piece on queues"]}`

func (m *pageModel) GenerateJSON(ctx context.Context, prompt string, _ json.RawMessage,
	_ generator.Options) (json.RawMessage, error) {
	m.mu.Lock()
	probeErr := m.probeErr
	isProbe := strings.Contains(prompt, "Kestrel K2") || strings.Contains(prompt, "Title: Sign in\n") ||
		strings.Contains(prompt, "Castellbo")
	if probeErr != nil && isProbe {
		m.probes++
	}
	m.mu.Unlock()
	if probeErr != nil && isProbe {
		return nil, probeErr
	}
	switch {
	case strings.Contains(prompt, "Kestrel K2"):
		return json.RawMessage(`{"kind":"article","site":"","author":"","about":"x","entities":["Kestrel K2"],` +
			`"claims":[],"descriptions":[]}`), nil
	case strings.Contains(prompt, "Title: Sign in\n"):
		return json.RawMessage(`{"kind":"not_the_page","site":"","author":"","about":"","entities":[],` +
			`"claims":[],"descriptions":[]}`), nil
	case strings.Contains(prompt, "Castellbo"):
		return json.RawMessage(`{"kind":"profile","site":"","author":"Oriane Castellbo-Ferrant","about":"x",` +
			`"entities":[],"claims":[],"descriptions":[]}`), nil
	}
	m.mu.Lock()
	m.calls++
	hold, answer, calling := m.hold, m.page, m.calling
	m.mu.Unlock()
	if hold != nil {
		calling <- struct{}{}
		select {
		case <-hold:
		case <-ctx.Done():
			return nil, fmt.Errorf("ollama generate: %w", ctx.Err())
		}
	}
	if answer == nil {
		return json.RawMessage(pageSummary), nil
	}
	reply, err := answer(prompt)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(reply), nil
}

func (*pageModel) Generate(context.Context, string, generator.Options) (string, error) {
	return "", errors.New("prose is never asked for")
}
func (*pageModel) Model() string { return "gemma4:26b" }
func (m *pageModel) Ping(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pingErr
}

func (m *pageModel) probesMade() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.probes
}

func (m *pageModel) callsMade() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// holdCalls makes each page's call wait until the returned release.
func (m *pageModel) holdCalls() (release func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hold, m.calling = make(chan struct{}), make(chan struct{}, 1)
	return func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		close(m.hold)
		m.hold = nil
	}
}

func (m *pageModel) set(page func(string) (string, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.page = page
}

// summaryEnv is the jobs' dependencies with the summary pass wired, over a
// scripted model.
type summaryEnv struct {
	t      *testing.T
	ctx    context.Context
	deps   Deps
	db     *sqlitestore.DB
	model  *pageModel
	writer *summarize.Writer
	sums   *sqlitestore.Summaries
}

func newSummaryEnv(t *testing.T, on bool) *summaryEnv {
	t.Helper()
	deps, db, _ := newTestDeps(t)
	model := &pageModel{}
	sums := sqlitestore.NewSummaries(db)
	writer := summarize.New(model, summarize.Options{On: on, Log: quietLog,
		Model: config.ModelSpec{Provider: config.ProviderOllama, Model: "gemma4:26b"}})
	deps.Summaries, deps.Writer = sums, writer
	deps.SummaryFeed = NewSummaries(SummariesOptions{Store: sums, Writer: writer, TenantID: "local", Log: quietLog})
	deps.Foreground = NewForeground(20 * time.Millisecond)
	return &summaryEnv{t: t, ctx: context.Background(), deps: deps, db: db, model: model, writer: writer, sums: sums}
}

// page saves a fetched document of url whose markdown is text, and returns
// it.
func (e *summaryEnv) page(url, text string) *store.Document {
	e.t.Helper()
	doc := &store.Document{TenantID: "local", URL: url, Title: store.NullableString("Queues"),
		ContentType: store.ContentTypeArticle}
	require.NoError(e.t, e.deps.Documents.Create(e.ctx, doc))
	e.refetch(doc, text)
	return doc
}

// refetch gives doc a new current extraction whose markdown is text, and
// marks it fetched.
func (e *summaryEnv) refetch(doc *store.Document, text string) {
	e.t.Helper()
	ext := &store.DocumentExtraction{DocumentID: doc.ID, Fetcher: "test", Status: store.ExtractionStatusOK}
	require.NoError(e.t, e.deps.Extractions.Create(e.ctx, ext))
	rel := filepath.Join(doc.ID, ext.ID+".md")
	require.NoError(e.t, os.MkdirAll(filepath.Join(e.deps.ContentDir, doc.ID), 0o700))
	require.NoError(e.t, os.WriteFile(filepath.Join(e.deps.ContentDir, rel), []byte(text), 0o600))
	_, err := e.db.Exec(`UPDATE document_extractions SET markdown_path = ? WHERE id = ?`, rel, ext.ID)
	require.NoError(e.t, err)
	require.NoError(e.t, e.deps.Documents.ApplyFetch(e.ctx, doc.ID, store.FetchedMetadata{ExtractionID: ext.ID,
		ContentType: store.ContentTypeArticle, Title: doc.Title}))
	require.NoError(e.t, sqlitestore.NewDocuments(e.db).MarkFetched(e.ctx, doc.ID, 10))
	doc.CurrentExtractionID = &ext.ID
}

// job is a summarize job for doc's current extraction, as the feed queues
// it.
func (e *summaryEnv) job(doc *store.Document) *store.Job {
	e.t.Helper()
	payload, err := json.Marshal(store.DocumentJobPayload{DocumentID: doc.ID, ExtractionID: *doc.CurrentExtractionID})
	require.NoError(e.t, err)
	return &store.Job{ID: "job-" + doc.ID, TenantID: "local", Kind: store.JobKindSummarize, Payload: payload,
		CreatedAt: time.Now()}
}

func (e *summaryEnv) run(job *store.Job) error {
	return summarizeHandler(e.deps)(e.ctx, job)
}

func (e *summaryEnv) summary(doc *store.Document) *store.StoredSummary {
	e.t.Helper()
	got, err := e.sums.GetSummary(e.ctx, "local", doc.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	require.NoError(e.t, err)
	return got
}

// TestSummarizeJob_Writes: a page's summary is written from its current
// extraction, and the feed is told; a refetch that finds the same page
// points the summary at the new extraction without asking the model.
func TestSummarizeJob_Writes(t *testing.T) {
	e := newSummaryEnv(t, true)
	doc := e.page("https://example.com/queues", "Queues back up when consumers fall behind.")
	require.NoError(t, e.run(e.job(doc)))
	got := e.summary(doc)
	require.NotNil(t, got)
	require.NotNil(t, got.Summary)
	assert.Equal(t, *doc.CurrentExtractionID, got.Summary.ExtractionID)
	assert.Equal(t, "Why queues back up.", got.Summary.About)
	assert.Equal(t, 1, e.model.callsMade())

	first := got.Summary.WrittenAt
	e.refetch(doc, "Queues back up when consumers fall behind.")
	require.NoError(t, e.run(e.job(doc)))
	assert.Equal(t, 1, e.model.callsMade(), "the same input: no call")
	got = e.summary(doc)
	assert.Equal(t, *doc.CurrentExtractionID, got.Summary.ExtractionID)
	assert.Equal(t, first, got.Summary.WrittenAt)

	e.refetch(doc, "Queues back up, and now the page says more.")
	require.NoError(t, e.run(e.job(doc)))
	assert.Equal(t, 2, e.model.callsMade(), "another input: written again")
}

// TestSummarizeJob_NothingToDo: a page gone, no longer fetched, refetched
// since its job was queued, or a writer that writes nothing, are done with
// nothing written and no call.
func TestSummarizeJob_NothingToDo(t *testing.T) {
	e := newSummaryEnv(t, true)
	gone := e.page("https://example.com/gone", "text")
	job := e.job(gone)
	_, err := e.db.Exec(`DELETE FROM documents WHERE id = ?`, gone.ID)
	require.NoError(t, err)
	require.NoError(t, e.run(job))

	failed := e.page("https://example.com/failed", "text")
	require.NoError(t, e.deps.Documents.MarkFailed(e.ctx, failed.ID, store.FailureCauseAntiBot))
	require.NoError(t, e.run(e.job(failed)))

	moved := e.page("https://example.com/moved", "text")
	stale := e.job(moved)
	e.refetch(moved, "other text")
	require.NoError(t, e.run(stale))
	assert.Nil(t, e.summary(moved))
	assert.Zero(t, e.model.callsMade())

	off := newSummaryEnv(t, false)
	doc := off.page("https://example.com/a", "text")
	require.NoError(t, off.run(off.job(doc)), "summaries off: the jobs left drain")
	assert.Nil(t, off.summary(doc))
}

// TestSummarizeJob_RefetchedDuringTheCall: a refetch that lands while the
// model writes the page's summary supersedes it: the job is done, nothing
// is stored, and the feed hears of no failure.
func TestSummarizeJob_RefetchedDuringTheCall(t *testing.T) {
	e := newSummaryEnv(t, true)
	e.deps.SummaryFeed = NewSummaries(SummariesOptions{Store: e.sums, Writer: e.deps.Writer, TenantID: "local",
		Log: quietLog, BrakeAfter: 1})
	doc := e.page("https://example.com/a", "Queues back up.")
	job := e.job(doc)
	release := e.model.holdCalls()
	result := make(chan error, 1)
	go func() { result <- e.run(job) }()
	<-e.model.calling
	e.refetch(doc, "Queues back up, and the page says more now.")
	release()
	require.NoError(t, <-result, "superseded: done")
	assert.Nil(t, e.summary(doc), "nothing stored for the extraction the call read")
	assert.False(t, e.deps.SummaryFeed.Progress().Brake.On(), "not a failure")
}

// TestSummarizeJob_Outcomes: what each failure makes of the job, and what
// the feed's brake hears of it.
func TestSummarizeJob_Outcomes(t *testing.T) {
	timeout := fmt.Errorf("ollama generate: %w: %w", generator.ErrUnreachable, context.DeadlineExceeded)
	for _, tc := range []struct {
		name      string
		err       error
		reply     string
		deferred  bool
		permanent bool
		told      bool
	}{
		{"no answer", generator.ErrUnreachable, "", true, false, false},
		{"a refused caller", generator.ErrUnauthorized, "", true, false, false},
		{"a timeout while it answers", timeout, "", false, false, true},
		{"a provider that can't serve now", generator.ErrUnavailable, "", false, false, true},
		{"an invalid answer", nil, `{"kind":"essay"}`, false, true, true},
		{"cut off", generator.ErrTruncated, "", false, true, true},
		{"a refused request", generator.ErrBadRequest, "", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSummaryEnv(t, true)
			e.deps.SummaryFeed = NewSummaries(SummariesOptions{Store: e.sums, Writer: e.deps.Writer, TenantID: "local",
				Log: quietLog, BrakeAfter: 1})
			e.model.set(func(string) (string, error) { return tc.reply, tc.err })
			doc := e.page("https://example.com/a", "text")
			err := e.run(e.job(doc))
			require.Error(t, err)
			de, isDeferred := errors.AsType[*DeferError](err)
			assert.Equal(t, tc.deferred, isDeferred, "%v", err)
			assert.Equal(t, tc.permanent, errors.Is(err, ErrPermanent), "%v", err)
			assert.Equal(t, tc.told, e.deps.SummaryFeed.Progress().Brake.On(), "the brake hears of the failure")
			if isDeferred {
				assert.Equal(t, summarize.FeedOne, e.deps.Writer.Feed(), "the writer records the outage")
				assert.Contains(t, de.Reason, "summaries' model")
				assert.True(t, de.Routine, "the writer logs the outage once, not each deferral")
			}
		})
	}
}

// heldWriter is the env's writer under an eviction hold: Ready says the
// pass is held.
type heldWriter struct{ *summarize.Writer }

func (heldWriter) Ready(context.Context) error {
	return fmt.Errorf("%w: Ollama unloaded the model twice", summarize.ErrHeld)
}

// TestSummarizeJob_Held: a job that meets an eviction hold is done with no
// call and nothing written, so no job waits out the hold holding the Mac
// awake, and the brake hears nothing of it.
func TestSummarizeJob_Held(t *testing.T) {
	e := newSummaryEnv(t, true)
	e.deps.Writer = heldWriter{e.writer}
	doc := e.page("https://example.com/a", "text")
	require.NoError(t, e.run(e.job(doc)))
	assert.Zero(t, e.model.callsMade())
	assert.Nil(t, e.summary(doc))
	assert.False(t, e.deps.SummaryFeed.Progress().Brake.On())
}

// TestSummarizeJob_ModelNotPulled: a job that finds the model not pulled,
// by its call or by the check, is done with nothing written: no job waits
// for a pull that may never come, holding keep-awake, and the brake hears
// nothing of it.
func TestSummarizeJob_ModelNotPulled(t *testing.T) {
	for name, probes := range map[string]bool{"the call": false, "the check": true} {
		t.Run(name, func(t *testing.T) {
			e := newSummaryEnv(t, true)
			e.model.set(func(string) (string, error) { return "", generator.ErrModelMissing })
			if probes {
				e.model.mu.Lock()
				e.model.probeErr = generator.ErrModelMissing
				e.model.mu.Unlock()
			}
			doc := e.page("https://example.com/a", "text")
			require.NoError(t, e.run(e.job(doc)))
			assert.Nil(t, e.summary(doc))
			assert.Equal(t, summarize.FeedNone, e.deps.Writer.Feed())
			assert.Equal(t, summarize.OutageMissing, e.writer.State().Outage.Cause)
			assert.False(t, e.deps.SummaryFeed.Progress().Brake.On())
		})
	}
}

// TestSummarizeJob_LoadFailsUnchecked: a load of an unchecked model that
// fails with a 5xx (not enough memory) is a check that reached no verdict:
// the job that asked waits for the next, the brake hears nothing, and a
// job within the wait is done with nothing asked; no page is charged.
func TestSummarizeJob_LoadFailsUnchecked(t *testing.T) {
	e := newSummaryEnv(t, true)
	model := &warmingModel{pageModel: e.model, warmErr: fmt.Errorf("ollama generate: %w: HTTP 500: "+
		"model requires more system memory", generator.ErrUnavailable)}
	writer := summarize.New(model, summarize.Options{On: true, Log: quietLog,
		Model: config.ModelSpec{Provider: config.ProviderOllama, Model: "gemma4:26b"}})
	e.deps.Writer, e.writer = writer, writer
	e.deps.SummaryFeed = NewSummaries(SummariesOptions{Store: e.sums, Writer: writer, TenantID: "local",
		Log: quietLog, BrakeAfter: 1})
	doc := e.page("https://example.com/a", "text")
	err := e.run(e.job(doc))
	de, ok := errors.AsType[*DeferError](err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, summaryCheckReason, de.Reason)
	assert.False(t, errors.Is(err, ErrPermanent))
	assert.False(t, e.deps.SummaryFeed.Progress().Brake.On(), "nothing of the page")
	assert.Equal(t, summarize.FeedOne, writer.Feed())
	require.NoError(t, e.run(e.job(e.page("https://example.com/b", "text"))), "within the wait: done")
	assert.Equal(t, 1, model.warms, "nothing asked within the wait")
	assert.Zero(t, e.model.callsMade())
	assert.Nil(t, e.summary(doc))
}

// TestSummaryPass_ModelNotPulled: through the daemon's pools and the feed,
// a summaries' model that isn't pulled leaves no summarize job queued once
// the first finds it so; the feed's check then asks the model listing, and
// queues the pass again once the model is pulled.
func TestSummaryPass_ModelNotPulled(t *testing.T) {
	e := newSummaryEnv(t, true)
	e.deps.SummaryFeed = NewSummaries(SummariesOptions{Store: e.sums, Writer: e.writer, TenantID: "local",
		Log: quietLog, CheckEvery: 20 * time.Millisecond})
	for i := range 3 {
		e.page(fmt.Sprintf("https://example.com/%d", i), fmt.Sprintf("page %d", i))
	}
	e.model.mu.Lock()
	e.model.probeErr, e.model.pingErr = generator.ErrModelMissing, generator.ErrModelMissing
	e.model.mu.Unlock()
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	feed := e.deps.SummaryFeed
	require.NoError(t, feed.Start(ctx))
	go feed.Run(ctx)
	stop := runPools(NewPools(e.deps, PoolSizes{Fetch: 1, Index: 1},
		WorkerOptions{Gate: e.deps.Foreground, PollInterval: 5 * time.Millisecond, Log: quietLog}))
	defer stop()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.Equal(c, summarize.FeedNone, e.writer.Feed())
		assert.Equal(c, 1, e.summarizeCount(store.JobStatusDone))
		assert.Zero(c, e.summarizeCount(store.JobStatusPending)+e.summarizeCount(store.JobStatusRunning))
	}, 5*time.Second, 5*time.Millisecond)
	assert.Never(t, func() bool {
		return e.summarizeCount(store.JobStatusPending)+e.summarizeCount(store.JobStatusRunning) > 0
	}, 200*time.Millisecond, 5*time.Millisecond, "no job is kept while the model isn't pulled")

	e.model.mu.Lock()
	e.model.probeErr, e.model.pingErr = nil, nil
	e.model.mu.Unlock()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		n, err := e.sums.CountSummaries(e.ctx, "local", e.writer.Identity())
		require.NoError(c, err)
		assert.Equal(c, 3, n.Written, "the pull seen, the pass goes on")
	}, 10*time.Second, 10*time.Millisecond)
}

// TestSummaryPass_CheckWaitOutlivesItsJob: through the daemon's pools and
// the feed, the one job kept while the fitness check waits can come back
// before the wait is over: its day of deferrals spent, so its deferral
// failed an attempt and is retried a minute later; left a day old by a
// restart, so the same; or pre-empted by a search right after its probe
// failed, deferred a second. Whichever, it is done with nothing asked, and
// the feed queues one job for the wait's end: never one due that would
// meet the wait too, and be done, and be followed by another, every few
// milliseconds until the wait ended.
func TestSummaryPass_CheckWaitOutlivesItsJob(t *testing.T) {
	for _, tc := range []struct {
		name string
		// keep brings the env to a check that waits, its one job pending
		// and due inside the wait; it returns the probes made.
		keep func(t *testing.T, e *summaryEnv, start func()) int
	}{
		{"its day of deferrals spent", func(t *testing.T, e *summaryEnv, start func()) int {
			clock := &testClock{at: time.Now()}
			writer := summarize.New(e.model, summarize.Options{On: true, Now: clock.now, Log: quietLog,
				Model: config.ModelSpec{Provider: config.ProviderOllama, Model: "gemma4:26b"}})
			e.deps.Writer, e.writer = writer, writer
			e.deps.SummaryFeed = NewSummaries(SummariesOptions{Store: e.sums, Writer: writer, TenantID: "local",
				Log: quietLog})
			start()
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				assert.Equal(c, 1, e.model.probesMade())
				assert.Equal(c, 1, e.summarizeCount(store.JobStatusPending), "the job whose check failed waits")
			}, 5*time.Second, 5*time.Millisecond)
			// A day of waits later, the wait over and the job due.
			clock.set(writer.State().CheckRetryAt)
			_, err := e.db.Exec(`UPDATE jobs SET created_at = `+sqlAgo(deferralBudget+time.Hour)+`, run_after = `+
				sqlAgo(0)+` WHERE kind = ? AND status = ?`, store.JobKindSummarize, store.JobStatusPending)
			require.NoError(t, err)
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				assert.Equal(c, 2, e.model.probesMade(), "the check ran again, and failed")
				assert.Equal(c, 1, e.pendingAttempts(), "its deferral, past the budget, failed the attempt")
			}, 5*time.Second, 5*time.Millisecond)
			return 2
		}},
		{"left a day old by a restart", func(t *testing.T, e *summaryEnv, start func()) int {
			n, err := e.sums.EnqueueSummarize(e.ctx, "local", e.writer.Identity(), store.SummarizeLimits{InFlight: 3})
			require.NoError(t, err)
			require.Equal(t, 3, n)
			_, err = e.db.Exec(`UPDATE jobs SET created_at = `+sqlAgo(deferralBudget+time.Hour)+` WHERE kind = ?`,
				store.JobKindSummarize)
			require.NoError(t, err)
			start()
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				assert.Equal(c, 1, e.model.probesMade())
				assert.Equal(c, 2, e.summarizeCount(store.JobStatusDone), "the others met the wait")
				assert.Equal(c, 1, e.pendingAttempts(), "the one that probed: its deferral failed an attempt")
			}, 5*time.Second, 5*time.Millisecond)
			return 1
		}},
		{"pre-empted right after its probe failed", func(t *testing.T, e *summaryEnv, start func()) int {
			e.deps.Writer = &preemptingWriter{Writer: e.writer, fg: e.deps.Foreground}
			start()
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				assert.Equal(c, 1, e.model.probesMade())
				var reason string
				assert.NoError(c, e.db.QueryRow(`SELECT last_error FROM jobs WHERE kind = ? AND status = ?`,
					store.JobKindSummarize, store.JobStatusPending).Scan(&reason))
				assert.Equal(c, "waiting for "+foregroundWaitReason, reason)
			}, 5*time.Second, 5*time.Millisecond)
			return 1
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSummaryEnv(t, true)
			for i := range 4 {
				e.page(fmt.Sprintf("https://example.com/%d", i), fmt.Sprintf("page %d", i))
			}
			e.model.mu.Lock()
			e.model.probeErr = generator.ErrUnavailable
			e.model.mu.Unlock()
			ctx, cancel := context.WithCancel(e.ctx)
			defer cancel()
			var stop func()
			defer func() {
				if stop != nil {
					stop()
				}
			}()
			probes := tc.keep(t, e, func() {
				require.NoError(t, e.deps.SummaryFeed.Start(ctx))
				go e.deps.SummaryFeed.Run(ctx)
				stop = runPools(NewPools(e.deps, PoolSizes{Fetch: 1, Index: 1},
					WorkerOptions{Gate: e.deps.Foreground, PollInterval: 5 * time.Millisecond, Log: quietLog}))
			})
			until := e.writer.State().CheckRetryAt
			require.False(t, until.IsZero(), "the check waits")
			jobs := e.summarizeJobs()

			// The kept job comes due inside the wait.
			_, err := e.db.Exec(`UPDATE jobs SET run_after = `+sqlAgo(0)+` WHERE kind = ? AND status = ?`,
				store.JobKindSummarize, store.JobStatusPending)
			require.NoError(t, err)
			assert.Never(t, func() bool { return e.summarizeJobs() > jobs+1 }, 500*time.Millisecond,
				5*time.Millisecond, "a job a few milliseconds while the check waits")
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				assert.Zero(c, e.summarizeCount(store.JobStatusRunning))
				var due, waiting int
				assert.NoError(c, e.db.QueryRow(`SELECT coalesce(sum(run_after < ?1), 0), coalesce(sum(run_after >= ?1), 0)
					FROM jobs WHERE kind = ?2 AND status = ?3`, until.UTC().Format("2006-01-02T15:04:05.000Z"),
					store.JobKindSummarize, store.JobStatusPending).Scan(&due, &waiting))
				assert.Zero(c, due, "none due before the wait's end")
				assert.Equal(c, 1, waiting, "the feed's one job, for the wait's end")
			}, 5*time.Second, 5*time.Millisecond)
			assert.Equal(t, jobs+1, e.summarizeJobs())
			assert.Equal(t, probes, e.model.probesMade(), "nothing asked within the wait")
			assert.Zero(t, e.model.callsMade(), "no page asked")
			assert.False(t, e.deps.SummaryFeed.Progress().Brake.On(), "a check's wait says nothing of the pages")
		})
	}
}

// TestSummaryPass_HeldOrNotPulled: through the daemon's pools and the
// feed, jobs left queued when the pass is held for a model Ollama keeps
// unloading, or finds its model not pulled, are each done once, and the
// feed queues none after them: nothing loops through a job that is done
// at once.
func TestSummaryPass_HeldOrNotPulled(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, e *summaryEnv)
	}{
		{"an eviction hold", func(t *testing.T, e *summaryEnv) {
			model := &warmingModel{pageModel: e.model}
			writer := summarize.New(model, summarize.Options{On: true, Log: quietLog,
				Model: config.ModelSpec{Provider: config.ProviderOllama, Model: "gemma4:26b"}})
			e.deps.Writer, e.writer = writer, writer
			e.deps.SummaryFeed = NewSummaries(SummariesOptions{Store: e.sums, Writer: writer, TenantID: "local",
				Log: quietLog})
			require.NoError(t, writer.Ready(e.ctx), "a first load")
			model.unload()
			require.NoError(t, writer.Ready(e.ctx), "one eviction loads it again")
			model.unload()
			require.ErrorIs(t, writer.Ready(e.ctx), summarize.ErrHeld, "the second within an hour holds the pass")
		}},
		{"a model not pulled", func(_ *testing.T, e *summaryEnv) {
			e.model.mu.Lock()
			e.model.probeErr, e.model.pingErr = generator.ErrModelMissing, generator.ErrModelMissing
			e.model.mu.Unlock()
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSummaryEnv(t, true)
			for i := range 4 {
				e.page(fmt.Sprintf("https://example.com/%d", i), fmt.Sprintf("page %d", i))
			}
			n, err := e.sums.EnqueueSummarize(e.ctx, "local", e.writer.Identity(), store.SummarizeLimits{InFlight: 3})
			require.NoError(t, err)
			require.Equal(t, 3, n, "left queued before")
			tc.setup(t, e)
			ctx, cancel := context.WithCancel(e.ctx)
			defer cancel()
			require.NoError(t, e.deps.SummaryFeed.Start(ctx))
			go e.deps.SummaryFeed.Run(ctx)
			stop := runPools(NewPools(e.deps, PoolSizes{Fetch: 1, Index: 1},
				WorkerOptions{Gate: e.deps.Foreground, PollInterval: 5 * time.Millisecond, Log: quietLog}))
			defer stop()
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				assert.Equal(c, 3, e.summarizeCount(store.JobStatusDone))
			}, 5*time.Second, 5*time.Millisecond)
			assert.Equal(t, summarize.FeedNone, e.writer.Feed())
			for range 3 {
				e.deps.SummaryFeed.Kick()
			}
			assert.Never(t, func() bool { return e.summarizeJobs() > 3 }, 300*time.Millisecond, 5*time.Millisecond,
				"no job queued after them")
			assert.Zero(t, e.model.callsMade(), "no page asked")
		})
	}
}

// preemptingWriter is the env's writer under a search that begins right
// after the first failed probe of its check, before the job hears of it:
// the job is pre-empted, deferred a second, well inside the check's wait.
type preemptingWriter struct {
	*summarize.Writer
	fg   *Foreground
	once sync.Once
}

func (p *preemptingWriter) Write(ctx context.Context, in summarize.Input) (summarize.Written, error) {
	w, err := p.Writer.Write(ctx, in)
	if we, ok := errors.AsType[*summarize.WaitError](err); ok && we.Probed {
		p.once.Do(func() {
			end := p.fg.Begin()
			<-ctx.Done() // the job's watcher cancels its call for the search
			end()
		})
	}
	return w, err
}

// warmingModel is a pageModel the writer can ask whether it is loaded,
// and load, which fails with warmErr when set.
type warmingModel struct {
	*pageModel
	loaded  bool
	warmErr error
	warms   int
}

func (m *warmingModel) Loaded(context.Context) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loaded, nil
}

func (m *warmingModel) Warm(context.Context, time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.warms++
	m.loaded = m.warmErr == nil
	return m.warmErr
}

func (m *warmingModel) Unload(context.Context) error {
	m.unload()
	return nil
}

func (m *warmingModel) unload() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loaded = false
}

// testClock is a clock a test sets.
type testClock struct {
	mu sync.Mutex
	at time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *testClock) set(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = at
}

// summarizeJobs counts the summarize jobs queued, in every status.
func (e *summaryEnv) summarizeJobs() int {
	e.t.Helper()
	var n int
	require.NoError(e.t, e.db.QueryRow(`SELECT count(*) FROM jobs WHERE kind = ?`, store.JobKindSummarize).Scan(&n))
	return n
}

// pendingAttempts is the attempts of the one summarize job pending, -1 for
// none or more.
func (e *summaryEnv) pendingAttempts() int {
	e.t.Helper()
	var n, attempts int
	require.NoError(e.t, e.db.QueryRow(`SELECT count(*), coalesce(max(attempts), 0) FROM jobs
		WHERE kind = ? AND status = ?`, store.JobKindSummarize, store.JobStatusPending).Scan(&n, &attempts))
	if n != 1 {
		return -1
	}
	return attempts
}

// TestSummarizeJob_NoMarkdown: a page whose markdown is gone fails for
// good.
func TestSummarizeJob_NoMarkdown(t *testing.T) {
	e := newSummaryEnv(t, true)
	doc := e.page("https://example.com/a", "text")
	require.NoError(t, os.RemoveAll(filepath.Join(e.deps.ContentDir, doc.ID)))
	err := e.run(e.job(doc))
	require.ErrorIs(t, err, ErrPermanent)
	require.ErrorIs(t, err, ErrNoMarkdown)
}

// TestSummarizeJob_Unfit: a model found unfit by the job's own check
// writes nothing, and the job is done.
func TestSummarizeJob_Unfit(t *testing.T) {
	e := newSummaryEnv(t, true)
	unfit := &unfitModel{pageModel: e.model}
	writer := summarize.New(unfit, summarize.Options{On: true, Log: quietLog,
		Model: config.ModelSpec{Provider: config.ProviderOllama, Model: "m"}})
	e.deps.Writer = writer
	doc := e.page("https://example.com/a", "text")
	require.NoError(t, e.run(e.job(doc)))
	assert.Nil(t, e.summary(doc))
	assert.NotEmpty(t, writer.Refusal())
}

// TestSummarizeJob_RefusedFirst: a writer that writes nothing leaves a job
// nothing to do before its payload is read, so a job whose payload doesn't
// decode is done, as every other is; a writer that writes fails it for
// good.
func TestSummarizeJob_RefusedFirst(t *testing.T) {
	bad := &store.Job{ID: "job-bad", TenantID: "local", Kind: store.JobKindSummarize, Payload: []byte("{"),
		CreatedAt: time.Now()}
	off := newSummaryEnv(t, false)
	require.NotEmpty(t, off.deps.Writer.Refusal())
	require.NoError(t, off.run(bad))
	require.ErrorIs(t, newSummaryEnv(t, true).run(bad), ErrPermanent)
}

// unfitModel reads a sign-in wall as an article.
type unfitModel struct{ *pageModel }

func (m *unfitModel) GenerateJSON(ctx context.Context, prompt string, s json.RawMessage,
	o generator.Options) (json.RawMessage, error) {
	if strings.Contains(prompt, "Title: Sign in\n") {
		return json.RawMessage(pageSummary), nil
	}
	return m.pageModel.GenerateJSON(ctx, prompt, s, o)
}

// TestSummarizeJob_Preempted: a use of a model that begins during a call
// cancels it within 50 ms, and the job waits for the use to pause, its
// attempt refunded; while a use is in flight, or within the quiet window
// after, no call is made at all.
func TestSummarizeJob_Preempted(t *testing.T) {
	e := newSummaryEnv(t, true)
	doc := e.page("https://example.com/a", "text")
	e.model.holdCalls()
	result := make(chan error, 1)
	go func() { result <- e.run(e.job(doc)) }()
	<-e.model.calling
	began := time.Now()
	end := e.deps.Foreground.Begin()
	var err error
	select {
	case err = <-result:
	case <-time.After(time.Second):
		t.Fatal("the call wasn't cancelled")
	}
	assert.Less(t, time.Since(began), 50*time.Millisecond)
	de, ok := errors.AsType[*DeferError](err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, foregroundWaitReason, de.Reason)
	assert.False(t, e.deps.SummaryFeed.Progress().Brake.On())

	calls := e.model.callsMade()
	err = e.run(e.job(doc))
	_, ok = errors.AsType[*DeferError](err)
	assert.True(t, ok, "a use in flight: deferred, %v", err)
	end()
	err = e.run(e.job(doc))
	_, ok = errors.AsType[*DeferError](err)
	assert.True(t, ok, "within the quiet window: deferred, %v", err)
	assert.Equal(t, calls, e.model.callsMade(), "no call while the person uses a model")
	assert.False(t, e.deps.Foreground.Admit(store.JobKindSummarize, 0, time.Now()).Open())
}

// TestPreemptible_JoinsItsWatcher: every call's watcher returns before
// stop does, pre-empted or not, and a stop deferred beside a call that
// panics joins it too; stop answers the same however often it is called.
func TestPreemptible_JoinsItsWatcher(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := range 300 {
		begun := make(chan struct{})
		ctx, stop := preemptible(context.Background(), begun)
		switch i % 3 {
		case 0:
			close(begun)
			<-ctx.Done()
			assert.True(t, stop())
			assert.True(t, stop(), "the same answer again")
		case 1:
			assert.False(t, stop())
			assert.Error(t, ctx.Err(), "released once stopped")
		case 2:
			assert.Panics(t, func() {
				defer stop()
				panic("a made-up call that panics")
			})
			assert.Error(t, ctx.Err(), "released by the deferred stop")
		}
	}
	assert.LessOrEqual(t, runtime.NumGoroutine(), before)
	_, stop := preemptible(context.Background(), nil)
	assert.False(t, stop(), "nothing pre-empts without a channel")
}

// TestSummaryFailed: the hook records a page's own failure against the
// payload's extraction, never one that got no answer from the model or a
// search pre-empted, a spent deferral included, and tolerates a deleted
// page.
func TestSummaryFailed(t *testing.T) {
	e := newSummaryEnv(t, true)
	hook := summaryFailed(e.deps)
	doc := e.page("https://example.com/a", "text")
	job := e.job(doc)
	for _, cause := range []error{generator.ErrUnreachable, generator.ErrModelMissing, generator.ErrUnauthorized,
		fmt.Errorf("check: %w", summarize.ErrNotChecked),
		&DeferError{Reason: foregroundWaitReason, Err: errPreempted}} {
		require.NoError(t, hook(e.ctx, job, fmt.Errorf("summarize: %w", cause)))
		assert.Nil(t, e.summary(doc), "%v records nothing", cause)
	}
	require.NoError(t, hook(e.ctx, job, errOrphanExhausted))
	got := e.summary(doc)
	require.NotNil(t, got)
	require.NotNil(t, got.Failure, "an orphan out of attempts is the page's")
	assert.Equal(t, *doc.CurrentExtractionID, got.Failure.ExtractionID)
	assert.Equal(t, e.deps.Writer.Identity(), got.Failure.SummaryWriter)

	_, err := e.db.Exec(`DELETE FROM documents WHERE id = ?`, doc.ID)
	require.NoError(t, err)
	require.NoError(t, hook(e.ctx, job, &summarize.InvalidAnswer{Rule: "kind"}), "a deleted page")
}

// TestSummaryPass_PreemptedPastTheBudget: through the daemon's pools, a
// summarize job past its day of deferrals that a search pre-empts on its
// last attempt fails, and its page's summary records no failure: the feed
// queues it again.
func TestSummaryPass_PreemptedPastTheBudget(t *testing.T) {
	e := newSummaryEnv(t, true)
	doc := e.page("https://example.com/a", "text")
	n, err := e.sums.EnqueueSummarize(e.ctx, "local", e.deps.Writer.Identity(),
		store.SummarizeLimits{InFlight: 1, Waiting: 0})
	require.NoError(t, err)
	require.Equal(t, 1, n)
	_, err = e.db.Exec(`UPDATE jobs SET attempts = 4, created_at = `+sqlAgo(deferralBudget+time.Hour)+
		` WHERE kind = ?`, store.JobKindSummarize)
	require.NoError(t, err)
	e.model.holdCalls()

	stop := runPools(NewPools(e.deps, PoolSizes{Fetch: 1, Index: 1},
		WorkerOptions{Gate: e.deps.Foreground, PollInterval: 5 * time.Millisecond, Log: quietLog}))
	defer stop()
	<-e.model.calling
	e.deps.Foreground.Begin()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.Equal(c, 1, e.summarizeCount(store.JobStatusFailed), "its last attempt spent")
	}, 5*time.Second, 5*time.Millisecond)
	assert.Nil(t, e.summary(doc), "a pre-emption says nothing of the page")
	targets, err := e.sums.ToSummarize(e.ctx, "local", e.deps.Writer.Identity(), SummariesFailedWait, 10)
	require.NoError(t, err)
	assert.Equal(t, []store.SummaryTarget{{DocumentID: doc.ID, ExtractionID: *doc.CurrentExtractionID}}, targets)
}

// summarizeCount counts the summarize jobs in status.
func (e *summaryEnv) summarizeCount(status store.JobStatus) int {
	e.t.Helper()
	var n int
	require.NoError(e.t, e.db.QueryRow(`SELECT count(*) FROM jobs WHERE kind = ? AND status = ?`,
		store.JobKindSummarize, status).Scan(&n))
	return n
}

// TestSummaryPass_HeldWithinOneDocument: through the daemon's pools, a
// pause or a schedule that closes takes effect after the summary in
// flight, and a resume or the schedule's end goes on.
func TestSummaryPass_HeldWithinOneDocument(t *testing.T) {
	// A window that opens an hour from now and closes an hour later.
	now := time.Now()
	minute := now.Hour()*60 + now.Minute()
	closed := store.DailyWindow{Start: (minute + 60) % (24 * 60), End: (minute + 120) % (24 * 60)}
	for _, tc := range []struct {
		name          string
		hold, release QueueUpdate
	}{
		{"a pause", QueueUpdate{Paused: new(true)}, QueueUpdate{Paused: new(false)}},
		{"a schedule", QueueUpdate{Schedule: &closed}, QueueUpdate{Schedule: &store.DailyWindow{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newSummaryEnv(t, true)
			for i := range 3 {
				e.page(fmt.Sprintf("https://example.com/%d", i), "text")
			}
			n, err := e.sums.EnqueueSummarize(e.ctx, "local", e.deps.Writer.Identity(),
				store.SummarizeLimits{InFlight: 3, Waiting: 0})
			require.NoError(t, err)
			require.Equal(t, 3, n)
			gate, err := NewQueueGate(e.ctx, newMemSettings(store.DefaultQueueSettings()), daemonPools, quietLog)
			require.NoError(t, err)
			release := e.model.holdCalls()

			stop := runPools(NewPools(e.deps, PoolSizes{Fetch: 1, Index: 1},
				WorkerOptions{Gate: All(gate, e.deps.Foreground), PollInterval: 5 * time.Millisecond, Log: quietLog}))
			defer stop()
			<-e.model.calling
			_, err = gate.Update(e.ctx, tc.hold)
			require.NoError(t, err)
			release()
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				assert.Equal(c, 1, e.summarizeCount(store.JobStatusDone), "the summary in flight is written")
			}, 5*time.Second, 5*time.Millisecond)
			assert.Never(t, func() bool { return e.summarizeCount(store.JobStatusRunning) > 0 },
				100*time.Millisecond, 5*time.Millisecond, "held: no job claimed after the one in flight")
			_, err = gate.Update(e.ctx, tc.release)
			require.NoError(t, err)
			require.EventuallyWithT(t, func(c *assert.CollectT) {
				assert.Equal(c, 3, e.summarizeCount(store.JobStatusDone))
			}, 5*time.Second, 5*time.Millisecond)
		})
	}
}

// TestSummaryPass_EveryPageFailing: a model that answers every page with
// a 500 costs the pass three documents' jobs, puts the brake on once, and
// writes nothing; a few pages failing for their own reasons don't stop
// the others.
func TestSummaryPass_EveryPageFailing(t *testing.T) {
	e := newSummaryEnv(t, true)
	e.deps.SummaryFeed = NewSummaries(SummariesOptions{Store: e.sums, Writer: e.deps.Writer, TenantID: "local",
		Log: quietLog})
	for i := range 6 {
		e.page(fmt.Sprintf("https://example.com/%d", i), fmt.Sprintf("page %d", i))
	}
	e.model.set(func(string) (string, error) { return "", generator.ErrUnavailable })
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	feed := e.deps.SummaryFeed
	require.NoError(t, feed.Start(ctx))
	go feed.Run(ctx)
	stop := runPools(NewPools(e.deps, PoolSizes{Fetch: 1, Index: 1},
		WorkerOptions{Gate: e.deps.Foreground, PollInterval: 5 * time.Millisecond, Log: quietLog}))
	defer stop()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.True(c, feed.Progress().Brake.On())
	}, 5*time.Second, 5*time.Millisecond)
	var jobs int
	require.NoError(t, e.db.QueryRow(`SELECT count(DISTINCT document_id) FROM jobs WHERE kind = ?`,
		store.JobKindSummarize).Scan(&jobs))
	assert.Equal(t, summariesInFlight+summariesWaiting, jobs, "the feed queued no more past the brake")
	assert.Equal(t, 3, feed.Progress().Brake.Failures)
	n, err := e.sums.CountSummaries(e.ctx, "local", e.deps.Writer.Identity())
	require.NoError(t, err)
	assert.Zero(t, n.Written)
}

// TestSummaryPass_CheckReachesNoVerdict: through the daemon's pools and
// the feed, a model that answers the fitness check's probes with a 500 is
// probed once a wait, not once a job: the job whose check failed waits for
// the next, until the wait's end healthz says, no other is queued beside
// it, and nothing is written.
func TestSummaryPass_CheckReachesNoVerdict(t *testing.T) {
	e := newSummaryEnv(t, true)
	for i := range 4 {
		e.page(fmt.Sprintf("https://example.com/%d", i), fmt.Sprintf("page %d", i))
	}
	e.model.mu.Lock()
	e.model.probeErr = generator.ErrUnavailable
	e.model.mu.Unlock()
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	feed := e.deps.SummaryFeed
	require.NoError(t, feed.Start(ctx))
	go feed.Run(ctx)
	stop := runPools(NewPools(e.deps, PoolSizes{Fetch: 1, Index: 1},
		WorkerOptions{Gate: e.deps.Foreground, PollInterval: 5 * time.Millisecond, Log: quietLog}))
	defer stop()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		assert.Equal(c, 1, e.model.probesMade())
		assert.Equal(c, 1, e.summarizeCount(store.JobStatusPending), "the job whose check failed waits")
	}, 5*time.Second, 5*time.Millisecond)
	for range 3 {
		feed.Kick()
	}
	assert.Never(t, func() bool {
		return e.model.probesMade() > 1 || e.summarizeCount(store.JobStatusPending) > 1
	}, 200*time.Millisecond, 5*time.Millisecond, "kicks queue nothing beside it, and nothing asks the model")
	assert.Equal(t, summarize.FeedOne, e.deps.Writer.Feed())
	assert.Equal(t, summarize.CheckUnchecked, e.writer.State().Check)
	assert.False(t, e.writer.State().CheckRetryAt.IsZero())
	assert.Zero(t, e.model.callsMade(), "no page asked")
	var reason, runAfter string
	require.NoError(t, e.db.QueryRow(`SELECT last_error, run_after FROM jobs WHERE kind = ? AND status = ?`,
		store.JobKindSummarize, store.JobStatusPending).Scan(&reason, &runAfter))
	assert.Equal(t, "waiting for "+summaryCheckReason, reason)
	assert.Equal(t, e.writer.State().CheckRetryAt.UTC().Format("2006-01-02T15:04:05.000Z"), runAfter,
		"deferred to the wait's end healthz says, on the wall clock the jobs table keeps")
	assert.False(t, feed.Progress().Brake.On(), "a check's wait says nothing of the pages")
}

// TestSummaryPass_AFewPagesFailing: pages failing for their own reasons,
// fewer than the brake's count, don't stop the others.
func TestSummaryPass_AFewPagesFailing(t *testing.T) {
	e := newSummaryEnv(t, true)
	for i := range 5 {
		e.page(fmt.Sprintf("https://example.com/%d", i), fmt.Sprintf("page %d", i))
	}
	e.model.set(func(prompt string) (string, error) {
		if strings.Contains(prompt, "page 1") || strings.Contains(prompt, "page 3") {
			return `{"kind":"essay"}`, nil
		}
		return pageSummary, nil
	})
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	feed := e.deps.SummaryFeed
	require.NoError(t, feed.Start(ctx))
	go feed.Run(ctx)
	stop := runPools(NewPools(e.deps, PoolSizes{Fetch: 1, Index: 1},
		WorkerOptions{Gate: e.deps.Foreground, PollInterval: 5 * time.Millisecond, Log: quietLog}))
	defer stop()
	require.EventuallyWithT(t, func(c *assert.CollectT) {
		n, err := e.sums.CountSummaries(e.ctx, "local", e.deps.Writer.Identity())
		require.NoError(c, err)
		assert.Equal(c, store.SummaryCounts{Written: 3, Failed: 2}, n)
	}, 10*time.Second, 10*time.Millisecond)
	assert.False(t, feed.Progress().Brake.On())
}

// TestQueueGate_GentleRest: under the gentle throttle the summary pass
// rests after each job as long as it took; under the normal one it
// doesn't, and no other kind rests.
func TestQueueGate_GentleRest(t *testing.T) {
	now := at(10, 12, 0)
	gate, err := NewQueueGate(context.Background(), newMemSettings(store.QueueSettings{
		Throttle: store.ThrottleGentle}), daemonPools, quietLog)
	require.NoError(t, err)
	gate.Rested(4*time.Second, now)
	v := gate.Admit(store.JobKindSummarize, 0, now.Add(time.Second))
	assert.Equal(t, ReasonThrottled, v.Closed)
	assert.True(t, now.Add(4*time.Second).Equal(v.Until), "%v", v.Until)
	assert.True(t, gate.Admit(store.JobKindSummarize, 0, now.Add(4*time.Second)).Open(), "rested")
	assert.True(t, gate.Admit(store.JobKindCards, 0, now.Add(time.Second)).Open())
	_, err = gate.Update(context.Background(), QueueUpdate{Throttle: new(store.ThrottleNormal)})
	require.NoError(t, err)
	assert.True(t, gate.Admit(store.JobKindSummarize, 0, now.Add(time.Second)).Open(), "no rest at normal")
}

// TestSummarizeHandler_Rests: each job tells the pacer how long it took.
func TestSummarizeHandler_Rests(t *testing.T) {
	e := newSummaryEnv(t, false)
	pacer := &recordingPacer{}
	e.deps.Pace = pacer
	doc := e.page("https://example.com/a", "text")
	require.NoError(t, e.run(e.job(doc)))
	assert.Equal(t, 1, pacer.calls)
}

type recordingPacer struct{ calls int }

func (p *recordingPacer) Rested(time.Duration, time.Time) { p.calls++ }
