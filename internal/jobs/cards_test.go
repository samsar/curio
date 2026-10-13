package jobs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
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

// cardsEmbedder embeds every text as a zero vector, or fails with err; it
// records the batches it was sent.
type cardsEmbedder struct {
	dim int

	mu      sync.Mutex
	err     error
	batches [][]string
}

func (e *cardsEmbedder) Dimensions() int { return e.dim }
func (*cardsEmbedder) Model() string     { return "fake" }
func (e *cardsEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.batches = append(e.batches, slices.Clone(texts))
	if e.err != nil {
		return nil, e.err
	}
	return slices.Repeat([][]float32{make([]float32, e.dim)}, len(texts)), nil
}

func (e *cardsEmbedder) sent() [][]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.batches)
}

// newCardsDeps is newTestDeps with n documents, their cards stale and a
// cards job pending, embedded through emb with the document prefix.
func newCardsDeps(t *testing.T, n int, emb *cardsEmbedder) (Deps, *sqlitestore.DB) {
	t.Helper()
	deps, db, _ := newTestDeps(t)
	emb.dim = sqlitetest.Width(t, db)
	deps.Indexer = indexer.New(sqlitestore.NewChunks(db, emb.dim), emb, indexer.Options{DocumentPrefix: "doc: "})
	for i := range n {
		require.NoError(t, deps.Documents.Create(context.Background(), &store.Document{TenantID: "local",
			URL: fmt.Sprintf("https://example.com/%03d", i), Title: new(fmt.Sprint("Page ", i))}))
	}
	return deps, db
}

// cardsJob is the tenant's pending cards job.
func cardsJob(t *testing.T, db *sqlitestore.DB) *store.Job {
	t.Helper()
	var id string
	require.NoError(t, db.QueryRow(`SELECT id FROM jobs WHERE kind = ? AND status = ?`, store.JobKindCards,
		store.JobStatusPending).Scan(&id))
	return getJob(t, sqlitestore.NewJobs(db), id)
}

func staleCards(t *testing.T, deps Deps) int {
	t.Helper()
	stale, err := deps.Cards.StaleCards(context.Background(), "local", "", 1000)
	require.NoError(t, err)
	return len(stale)
}

// TestCardsJob_EmbedsTheStaleCards: the cards pool runs the queued job to
// done, every stale card embedded with the document prefix, 32 to a
// request; a job with nothing stale is done without calling the embedder.
func TestCardsJob_EmbedsTheStaleCards(t *testing.T) {
	emb := &cardsEmbedder{}
	deps, db := newCardsDeps(t, 70, emb)
	job := cardsJob(t, db)
	runPoolsUntil(t, deps, func(c *assert.CollectT) {
		got, err := deps.Queue.GetByID(context.Background(), job.ID)
		require.NoError(c, err)
		assert.Equal(c, store.JobStatusDone, got.Status)
	})
	sent := emb.sent()
	require.Len(t, sent, 3, "ceil(70/32) requests")
	assert.Equal(t, []int{32, 32, 6}, []int{len(sent[0]), len(sent[1]), len(sent[2])})
	for _, b := range sent {
		for _, text := range b {
			assert.Regexp(t, `^doc: Page \d+ — example\.com$`, text)
		}
	}
	assert.Zero(t, staleCards(t, deps))

	again := &store.Job{TenantID: "local", Kind: store.JobKindCards}
	require.NoError(t, deps.Queue.Enqueue(context.Background(), again))
	runPoolsUntil(t, deps, func(c *assert.CollectT) {
		got, err := deps.Queue.GetByID(context.Background(), again.ID)
		require.NoError(c, err)
		assert.Equal(c, store.JobStatusDone, got.Status)
	})
	assert.Len(t, emb.sent(), 3, "nothing stale, nothing sent")
}

// TestCardsJob_Failures: an embedding model that doesn't answer defers the
// job without spending its attempt; another error is retried after its
// backoff; one the model can't take fails it for good. The cards stay
// stale, and no document is marked.
func TestCardsJob_Failures(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		status  store.JobStatus
		attempt int
		lastErr string
	}{
		{"unreachable", fmt.Errorf("%w: dial tcp: connection refused", ollama.ErrUnreachable),
			store.JobStatusPending, 0, "waiting for the embedding model to answer"},
		{"other", errors.New("ollama: HTTP 500: oops"), store.JobStatusPending, 1, "ollama: HTTP 500: oops"},
		{"permanent", embedder.ErrInputTooLong, store.JobStatusFailed, 1, "permanent failure: cards: "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emb := &cardsEmbedder{err: tc.err}
			deps, db := newCardsDeps(t, 3, emb)
			job := cardsJob(t, db)
			begun := time.Now()
			runPoolsUntil(t, deps, func(c *assert.CollectT) {
				got, err := deps.Queue.GetByID(context.Background(), job.ID)
				require.NoError(c, err)
				assert.NotEqual(c, store.JobStatusRunning, got.Status)
				assert.NotNil(c, got.LastError, "its outcome is recorded")
			})
			got := getJob(t, deps.Queue, job.ID)
			assert.Equal(t, tc.status, got.Status)
			assert.Equal(t, tc.attempt, got.Attempts)
			require.NotNil(t, got.LastError)
			assert.Contains(t, *got.LastError, tc.lastErr)
			if tc.status == store.JobStatusPending {
				assert.True(t, got.RunAfter.After(begun.Add(30*time.Second)), "run_after %s is ahead", got.RunAfter)
			}
			assert.Equal(t, 3, staleCards(t, deps))
			counts, err := deps.Documents.CountByState(context.Background(), "local")
			require.NoError(t, err)
			assert.Equal(t, map[store.DocState]int{store.DocStatePending: 3}, counts, "no document is marked")
		})
	}
}

// TestCardsJob_DeferralGrowsWithAge: a cards job waits half its age for
// the embedding model, at least a minute and at most half an hour.
func TestCardsJob_DeferralGrowsWithAge(t *testing.T) {
	for age, want := range map[time.Duration]time.Duration{
		0:                minCardsWait,
		90 * time.Second: minCardsWait,
		10 * time.Minute: 5 * time.Minute,
		2 * time.Hour:    maxCardsWait,
		23 * time.Hour:   maxCardsWait,
	} {
		assert.Equal(t, want, deferral(age), age)
	}

	emb := &cardsEmbedder{err: fmt.Errorf("%w: timed out", ollama.ErrUnreachable)}
	deps, db := newCardsDeps(t, 1, emb)
	job := cardsJob(t, db)
	job.CreatedAt = time.Now().Add(-10 * time.Minute)
	err := cardsHandler(deps)(context.Background(), job)
	de, ok := errors.AsType[*DeferError](err)
	require.True(t, ok, "%v", err)
	assert.Equal(t, cardsWaitReason, de.Reason)
	assert.WithinDuration(t, time.Now().Add(5*time.Minute), de.Until, 5*time.Second)
	assert.ErrorIs(t, de, ollama.ErrUnreachable)
}

// TestCardsJob_InterruptedKeepsWhatItEmbedded: a shutdown between two
// batches requeues the job with its attempt refunded, and the cards
// embedded before it stay embedded.
func TestCardsJob_InterruptedKeepsWhatItEmbedded(t *testing.T) {
	emb := &cardsEmbedder{}
	deps, db := newCardsDeps(t, 40, emb)
	job := cardsJob(t, db)
	ctx, cancel := context.WithCancel(context.Background())
	deps.Cards = cancelAfterPut{DocumentIndex: deps.Cards, cancel: cancel}
	w := NewWorker(deps.Queue, WorkerOptions{PollInterval: 10 * time.Millisecond, Log: quietLog})
	w.Register(store.JobKindCards, cardsHandler(deps))
	require.ErrorIs(t, w.Run(ctx), context.Canceled)

	got := getJob(t, deps.Queue, job.ID)
	assert.Equal(t, store.JobStatusPending, got.Status)
	assert.Zero(t, got.Attempts, "the attempt is refunded")
	assert.Len(t, emb.sent(), 1, "no batch is sent after the shutdown")
	assert.Equal(t, 8, staleCards(t, deps), "the first batch stays embedded")
}

// cancelAfterPut is a card index that cancels a run once it stores a
// batch, as a shutdown between two batches would.
type cancelAfterPut struct {
	store.DocumentIndex
	cancel context.CancelFunc
}

func (c cancelAfterPut) PutCardVectors(ctx context.Context, cards []store.CardVector) (int, error) {
	defer c.cancel()
	return c.DocumentIndex.PutCardVectors(ctx, cards)
}
