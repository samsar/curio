package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/embedder"
	"github.com/samsar/curio/internal/indexer"
	"github.com/samsar/curio/internal/ollama"
	"github.com/samsar/curio/internal/store"
	sqlitestore "github.com/samsar/curio/internal/store/sqlite"
	"github.com/samsar/curio/internal/store/sqlite/sqlitetest"
)

// errDown is an embed that got no answer, as the Ollama client reports it.
var errDown = fmt.Errorf("ollama embed: %w: connection refused", ollama.ErrUnreachable)

// outage is an embedding model that answers or doesn't: a fakeEmbedder
// whose embeds fail with errDown while it is down, counted.
type outage struct {
	fakeEmbedder
	down   atomic.Bool
	missed atomic.Int64 // embeds that got no answer
}

func (o *outage) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if o.down.Load() {
		o.missed.Add(1)
		return nil, errDown
	}
	return o.fakeEmbedder.Embed(ctx, texts)
}

// probe is the hold's probe of o, an embed of its own, uncounted.
func (o *outage) probe(context.Context) error {
	if o.down.Load() {
		return errDown
	}
	return nil
}

// syncBuffer is a bytes.Buffer safe for the log handlers of several
// goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestEmbedderHold: open, it admits every kind; the first embed that gets
// no answer closes it to the kinds that embed, index and cards, with one
// WARN, a later one changing nothing; Run probes until the model answers,
// then opens it, waking the workers it held, with one INFO; any answer but
// none opens it. A nil hold holds nothing.
func TestEmbedderHold(t *testing.T) {
	var down atomic.Bool
	probes := make(chan struct{}, 100)
	var logs syncBuffer
	h := NewEmbedderHold(func(context.Context) error {
		probes <- struct{}{}
		if down.Load() {
			return errDown
		}
		return errors.New("HTTP 500: an answer, if not a vector")
	}, slog.New(slog.NewJSONHandler(&logs, nil)))
	h.minWait, h.maxWait = time.Millisecond, 4*time.Millisecond
	now := time.Now()
	for _, kind := range []store.JobKind{store.JobKindFetch, store.JobKindIndex, store.JobKindCards,
		store.JobKindCluster} {
		assert.True(t, h.Admit(kind, 0, now).Open(), kind)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ran := make(chan struct{})
	go func() { defer close(ran); h.Run(ctx) }()

	down.Store(true)
	h.Unreachable(errDown)
	h.Unreachable(errors.New("a second outage report"))
	assert.True(t, h.Held())
	held := h.Admit(store.JobKindIndex, 0, now)
	assert.Equal(t, ReasonEmbedderUnreachable, held.Closed)
	assert.Equal(t, held, h.Admit(store.JobKindCards, 3, now))
	assert.True(t, h.Admit(store.JobKindFetch, 0, now).Open(), "a fetch embeds nothing")
	assert.True(t, h.Admit(store.JobKindCluster, 0, now).Open())
	for range 3 {
		<-probes // probed, and probed again, while nothing answers
	}
	assert.True(t, h.Held())
	select {
	case <-held.Changed:
		t.Fatal("opened while the model doesn't answer")
	default:
	}

	down.Store(false)
	select {
	case <-held.Changed:
	case <-time.After(5 * time.Second):
		t.Fatal("never opened")
	}
	assert.False(t, h.Held())
	assert.True(t, h.Admit(store.JobKindIndex, 0, now).Open())
	out := logs.String()
	assert.Equal(t, 1, strings.Count(out, `"level":"WARN","msg":"embedding model not answering`), out)
	assert.Equal(t, 1, strings.Count(out, `"level":"INFO","msg":"embedding model answering again`), out)

	down.Store(true)
	h.Unreachable(errDown)
	assert.True(t, h.Held(), "closed again by another outage")
	down.Store(false)
	require.Eventually(t, func() bool { return !h.Held() }, 5*time.Second, time.Millisecond)
	cancel()
	<-ran

	var none *EmbedderHold
	none.Unreachable(errDown)
	assert.False(t, none.Held())
}

// TestIndexJobs_RideOutAnOllamaOutage: through the daemon's index pool,
// an Ollama that stops answering defers the jobs it caught running, their
// attempts refunded, and holds every other index job back, unclaimed, for
// as long as it lasts, past the retry window or not: no index or re-embed
// job fails, no document changes state, and the log holds a line a job
// caught and one each for the outage's start and end, however many jobs
// wait. Once Ollama answers, every job runs and every document is indexed.
func TestIndexJobs_RideOutAnOllamaOutage(t *testing.T) {
	deps, db, _ := newTestDeps(t)
	ctx := context.Background()
	var logs syncBuffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	deps.Log = log
	reembedding := make([]*store.Document, 0, 2)
	for i := range 2 {
		reembedding = append(reembedding, fetchedBeforeContext(t, deps, db, fmt.Sprintf("https://example.com/r%d", i)))
	}
	emb := &outage{fakeEmbedder: fakeEmbedder{dim: sqlitetest.Width(t, db)}}
	emb.down.Store(true)
	deps.Indexer = indexer.New(deps.Chunks, emb, indexer.Options{})
	saved := make([]*store.Document, 0, 6)
	for i := range 6 {
		doc := &store.Document{TenantID: "local", URL: fmt.Sprintf("https://example.com/s%d", i)}
		require.NoError(t, deps.Documents.Create(ctx, doc))
		require.NoError(t, fetchHandler(deps)(ctx, docJob(t, store.JobKindFetch, doc.ID)))
		saved = append(saved, doc)
	}
	hold := NewEmbedderHold(emb.probe, log)
	hold.minWait, hold.maxWait = time.Millisecond, 5*time.Millisecond
	deps.Hold = hold
	r := NewReembed(ReembedOptions{Documents: deps.Documents, Insights: sqlitestore.NewInsights(db), TenantID: "local",
		Log: log, CheckEvery: 10 * time.Millisecond})
	deps.Reembed = r
	began, err := r.Start(ctx)
	require.NoError(t, err)
	require.True(t, began)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Go(func() { hold.Run(runCtx) })
	wg.Go(func() { r.Run(runCtx) })
	const workers = 2
	for _, p := range NewPools(deps, PoolSizes{Fetch: 1, Index: workers}, WorkerOptions{Gate: All(hold),
		PollInterval: 5 * time.Millisecond, MaxPollInterval: 20 * time.Millisecond, Log: log}) {
		if p.Kind == store.JobKindIndex {
			for range p.Size {
				wg.Go(func() { _ = p.Worker.Run(runCtx) })
			}
		}
	}

	deferred := func() int {
		var n int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM jobs WHERE kind = 'index' AND status = 'pending'
			AND last_error = 'waiting for the embedding model to answer'`).Scan(&n))
		return n
	}
	require.Eventually(t, func() bool { return hold.Held() && deferred() > 0 }, 5*time.Second, time.Millisecond)
	time.Sleep(300 * time.Millisecond) // an outage, the hold probing all the while
	assert.True(t, hold.Held())
	assert.LessOrEqual(t, emb.missed.Load(), int64(workers), "only the jobs it caught running asked")
	rows := dumpRowsOf(t, db, `SELECT sum(status = 'pending' AND json_extract(payload, '$.reembed') IS NULL),
		sum(json_extract(payload, '$.reembed')), sum(status = 'failed'), sum(status = 'running'), max(attempts)
		FROM jobs WHERE kind = 'index' AND status != 'done'`)
	assert.Equal(t, int64(len(saved)), rows[0], "every saved page's index job waits")
	assert.LessOrEqual(t, rows[1], int64(2), "a re-embed job deferred, and at most one more queued in its place")
	assert.Equal(t, []any{int64(0), int64(0), int64(0)}, rows[2:], "none failed, none running, no attempt spent")

	emb.down.Store(false)
	require.Eventually(t, func() bool { return !hold.Held() }, 5*time.Second, time.Millisecond)
	// The minute the deferred jobs wait, passed.
	_, err = db.Exec(`UPDATE jobs SET run_after = '2000-01-01T00:00:00.000Z' WHERE status = 'pending'`)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		left, err := deps.Documents.CountToReembed(ctx, "local", indexer.ContextVersion)
		var unfinished int
		require.NoError(t, db.QueryRow(`SELECT count(*) FROM jobs WHERE kind = 'index'
			AND status IN ('pending', 'running')`).Scan(&unfinished))
		return err == nil && left.Documents == 0 && unfinished == 0
	}, 10*time.Second, 5*time.Millisecond)
	cancel()
	wg.Wait()

	var failed int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM jobs WHERE status = 'failed'`).Scan(&failed))
	assert.Zero(t, failed)
	for _, d := range append(saved, reembedding...) {
		got, err := deps.Documents.GetByID(ctx, d.ID)
		require.NoError(t, err)
		assert.Equal(t, store.DocStateFetched, got.State, d.URL)
	}
	out := logs.String()
	assert.LessOrEqual(t, strings.Count(out, `"msg":"job deferred"`), workers, out)
	assert.Equal(t, 1, strings.Count(out, `"msg":"embedding model not answering`), out)
	assert.Equal(t, 1, strings.Count(out, `"msg":"embedding model answering again`), out)
	assert.NotContains(t, out, `"msg":"job failed"`)
}

// TestEmbedderHold_State: the hold says whether it holds, since when and
// why, the last probe that got no answer replacing the error that closed
// it; Answers is one probe, an answer of any kind counting.
func TestEmbedderHold_State(t *testing.T) {
	var probeErr atomic.Pointer[error]
	probes := make(chan struct{}, 100)
	h := NewEmbedderHold(func(context.Context) error {
		probes <- struct{}{}
		if err := probeErr.Load(); err != nil {
			return *err
		}
		return nil
	}, quietLog)
	h.minWait, h.maxWait = time.Millisecond, time.Millisecond
	assert.Equal(t, HoldState{}, h.State())
	assert.True(t, h.Answers(context.Background()))
	for _, err := range []error{&ollama.StatusError{Code: 500, Body: "an answer"}, errors.New("decode response")} {
		probeErr.Store(&err)
		assert.True(t, h.Answers(context.Background()), err)
	}
	probeErr.Store(&errDown)
	assert.False(t, h.Answers(context.Background()))

	before := time.Now()
	h.Unreachable(errDown)
	st := h.State()
	assert.True(t, st.Held)
	assert.WithinRange(t, st.Since, before, time.Now())
	assert.Equal(t, errDown.Error(), st.Err)

	later := fmt.Errorf("ollama embed: %w: %w", ollama.ErrUnreachable, context.DeadlineExceeded)
	probeErr.Store(&later)
	for len(probes) > 0 {
		<-probes
	}
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() { defer close(ran); h.Run(ctx) }()
	<-probes
	<-probes // the first probe's error recorded before the second is sent
	assert.Equal(t, later.Error(), h.State().Err, "the last probe's")
	assert.Equal(t, st.Since, h.State().Since)
	probeErr.Store(nil)
	require.Eventually(t, func() bool { return !h.Held() }, 5*time.Second, time.Millisecond)
	assert.Equal(t, HoldState{}, h.State())
	cancel()
	<-ran

	var none *EmbedderHold
	assert.Equal(t, HoldState{}, none.State())
	assert.False(t, none.Answers(context.Background()))
}

// fakeOllama is an Ollama whose /api/embed answers a probe (one input,
// "curio") at once with a vector of dim, and a document's batch only after
// batchWait, or never while batchWait is 0; it says on entered when a
// batch arrives.
type fakeOllama struct {
	dim       int
	batchWait atomic.Int64 // a time.Duration
	probeDown atomic.Bool  // the probe waits as a batch does
	entered   chan struct{}
}

func (f *fakeOllama) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Input []string `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	probe := len(req.Input) == 1 && req.Input[0] == "curio"
	if !probe || f.probeDown.Load() {
		if !probe {
			select {
			case f.entered <- struct{}{}:
			default:
			}
		}
		wait := time.Duration(f.batchWait.Load())
		if wait == 0 {
			wait = time.Hour
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(wait):
		}
	}
	vectors := make([][]float32, len(req.Input))
	for i := range vectors {
		vectors[i] = make([]float32, f.dim)
		vectors[i][0] = 1
	}
	reply := struct {
		Model      string      `json:"model"`
		Embeddings [][]float32 `json:"embeddings"`
	}{"m:1", vectors}
	if err := json.NewEncoder(w).Encode(reply); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// ollamaDeps is newTestDeps embedding through the Ollama client against a
// fakeOllama, with timeout, its hold probing that model as the daemon's
// does and logging to logs.
func ollamaDeps(t *testing.T, timeout time.Duration, logs *syncBuffer) (Deps, *sqlitestore.DB, *fakeOllama) {
	t.Helper()
	deps, db, _ := newTestDeps(t)
	fake := &fakeOllama{dim: sqlitetest.Width(t, db), entered: make(chan struct{}, 1)}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	emb, err := embedder.NewOllama(embedder.OllamaOptions{BaseURL: srv.URL, Model: "m:1", Dim: fake.dim,
		Timeout: timeout})
	require.NoError(t, err)
	deps.Indexer = indexer.New(deps.Chunks, emb, indexer.Options{})
	log := slog.New(slog.NewJSONHandler(logs, nil))
	deps.Log = log
	deps.Hold = NewEmbedderHold(func(ctx context.Context) error {
		_, err := emb.Embed(ctx, []string{"curio"})
		return err
	}, log)
	return deps, db, fake
}

// TestEmbedJobs_ShutdownMidEmbed: a shutdown that cancels an index or cards
// job while Ollama embeds its batch leaves the embedder hold open, with no
// WARN that the model isn't answering: the worker puts the job back.
func TestEmbedJobs_ShutdownMidEmbed(t *testing.T) {
	ctx := context.Background()
	for _, kind := range []store.JobKind{store.JobKindIndex, store.JobKindCards} {
		t.Run(string(kind), func(t *testing.T) {
			var logs syncBuffer
			deps, db, fake := ollamaDeps(t, time.Minute, &logs)
			var (
				job     *store.Job
				handler HandlerFunc
			)
			doc := &store.Document{TenantID: "local", URL: "https://example.com/a"}
			require.NoError(t, deps.Documents.Create(ctx, doc))
			if kind == store.JobKindIndex {
				require.NoError(t, fetchHandler(deps)(ctx, docJob(t, store.JobKindFetch, doc.ID)))
				job, handler = docJob(t, store.JobKindIndex, doc.ID), indexHandler(deps)
			} else {
				job, handler = cardsJob(t, db), cardsHandler(deps)
			}

			jobCtx, cancel := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() { done <- handler(jobCtx, job) }()
			<-fake.entered
			cancel() // the daemon shuts down
			var err error
			select {
			case err = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("the handler never returned")
			}
			require.ErrorIs(t, err, context.Canceled)
			assert.False(t, deps.Hold.Held(), "a cancelled embed says nothing of the model")
			assert.NotContains(t, logs.String(), "embedding model not answering")
		})
	}
}

// TestIndexHandler_EmbedTimesOut: an index job whose embed times out while
// the model answers a probe is an attempt that failed, retried like any
// other, the hold left open: the document is too slow to embed, and a
// deferral would hide it for a day. One that times out with the probe
// unanswered too is no answer at all: the hold closes and the job waits.
func TestIndexHandler_EmbedTimesOut(t *testing.T) {
	ctx := context.Background()
	var logs syncBuffer
	deps, _, fake := ollamaDeps(t, 100*time.Millisecond, &logs)
	doc := &store.Document{TenantID: "local", URL: "https://example.com/a"}
	require.NoError(t, deps.Documents.Create(ctx, doc))
	require.NoError(t, fetchHandler(deps)(ctx, docJob(t, store.JobKindFetch, doc.ID)))
	job := docJob(t, store.JobKindIndex, doc.ID)

	err := indexHandler(deps)(ctx, job)
	require.ErrorIs(t, err, errSlowEmbed)
	assert.NotErrorIs(t, err, ErrPermanent, "retried")
	assert.NotErrorIs(t, err, ollama.ErrUnreachable)
	assert.False(t, deferred(err))
	assert.Contains(t, err.Error(), "Client.Timeout exceeded")
	assert.False(t, deps.Hold.Held())

	fake.probeDown.Store(true)
	err = indexHandler(deps)(ctx, job)
	assert.True(t, deferred(err), "%v", err)
	require.ErrorIs(t, err, ollama.ErrUnreachable)
	st := deps.Hold.State()
	assert.True(t, st.Held)
	assert.Contains(t, st.Err, "Client.Timeout exceeded")
	assert.Equal(t, 1, strings.Count(logs.String(), "embedding model not answering"))

	fake.batchWait.Store(int64(time.Millisecond))
	fake.probeDown.Store(false)
	require.NoError(t, indexHandler(deps)(ctx, job), "indexed once the model keeps up")
}

// TestTimedOut: a deadline passed or a network operation that timed out
// is a timeout; an answer, even an error, isn't.
func TestTimedOut(t *testing.T) {
	timeout := &net.OpError{Op: "read", Err: os.ErrDeadlineExceeded}
	for _, err := range []error{
		fmt.Errorf("embed: %w", context.DeadlineExceeded),
		fmt.Errorf("read response: %w", timeout),
		fmt.Errorf("ollama embed: %w: %w", ollama.ErrUnreachable, context.DeadlineExceeded),
	} {
		assert.True(t, timedOut(err), "%v", err)
	}
	for _, err := range []error{
		errDown,
		fmt.Errorf("embed: %w", &ollama.StatusError{Code: 500}),
		fmt.Errorf("%w: %s", errSlowEmbed, "ollama unreachable: context deadline exceeded"),
		context.Canceled,
	} {
		assert.False(t, timedOut(err), "%v", err)
	}
}
