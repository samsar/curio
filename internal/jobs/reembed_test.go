package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
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

// fetchedBeforeContext is a document fetched and indexed through deps, its
// index job done, whose chunks are then made as a home upgraded from
// before chunk context holds them: embedded from their text alone, at
// context version 0.
func fetchedBeforeContext(t *testing.T, deps Deps, db *sqlitestore.DB, url string) *store.Document {
	t.Helper()
	ctx := context.Background()
	doc := &store.Document{TenantID: "local", URL: url, ContentType: store.ContentTypeArticle}
	require.NoError(t, deps.Documents.Create(ctx, doc))
	require.NoError(t, fetchHandler(deps)(ctx, docJob(t, store.JobKindFetch, doc.ID)))
	require.NoError(t, indexHandler(deps)(ctx, docJob(t, store.JobKindIndex, doc.ID)))
	_, err := db.Exec(`UPDATE jobs SET status = 'done' WHERE document_id = ? AND kind = 'index';
		UPDATE chunks SET context = '', context_version = 0 WHERE document_id = ?`, doc.ID, doc.ID)
	require.NoError(t, err)
	got, err := deps.Documents.GetByID(ctx, doc.ID)
	require.NoError(t, err)
	return got
}

// reembedJob is a re-embed index job for a document, as the start queues
// it.
func reembedJob(t *testing.T, docID string) *store.Job {
	t.Helper()
	payload, err := json.Marshal(store.DocumentJobPayload{DocumentID: docID, Reembed: true})
	require.NoError(t, err)
	return &store.Job{TenantID: "local", Kind: store.JobKindIndex, Payload: payload}
}

// logsTo is a logger writing JSON lines to buf.
func logsTo(buf *bytes.Buffer) *slog.Logger { return slog.New(slog.NewJSONHandler(buf, nil)) }

// TestReembed_Start: a start that finds documents to re-embed owes the
// interests a fresh rebuild, queues the first one an index job doesn't
// already re-embed, and no more, logs one INFO, and reports it; one that
// finds none does nothing.
func TestReembed_Start(t *testing.T) {
	deps, db, _ := newTestDeps(t)
	ctx := context.Background()
	insights := sqlitestore.NewInsights(db)
	queued := fetchedBeforeContext(t, deps, db, "https://example.com/queued")
	stale := fetchedBeforeContext(t, deps, db, "https://example.com/stale")
	require.NoError(t, deps.Queue.Enqueue(ctx, docJob(t, store.JobKindIndex, queued.ID)))
	current := &store.Document{TenantID: "local", URL: "https://example.com/current"}
	require.NoError(t, deps.Documents.Create(ctx, current))
	require.NoError(t, fetchHandler(deps)(ctx, docJob(t, store.JobKindFetch, current.ID)))
	require.NoError(t, indexHandler(deps)(ctx, docJob(t, store.JobKindIndex, current.ID)))
	_, err := db.Exec(`UPDATE jobs SET status = 'done' WHERE document_id = ? AND kind = 'index'`, current.ID)
	require.NoError(t, err)

	var logs bytes.Buffer
	r := NewReembed(ReembedOptions{Documents: deps.Documents, Insights: insights, TenantID: "local",
		Log: logsTo(&logs)})
	began, err := r.Start(ctx)
	require.NoError(t, err)
	assert.True(t, began)
	state, err := insights.State(ctx, "local")
	require.NoError(t, err)
	assert.Equal(t, store.FreshReindex, state.FreshOwed)
	var payload string
	require.NoError(t, db.QueryRow(`SELECT payload FROM jobs WHERE kind = 'index' AND status = 'pending'
		AND json_extract(payload, '$.reembed')`).Scan(&payload))
	assert.JSONEq(t, `{"document_id":"`+stale.ID+`","reembed":true}`, payload, "the one no index job re-embeds")
	p := r.Progress()
	assert.Equal(t, ReembedProgress{Active: true, Began: p.Began, Of: store.ReembedCount{Documents: 2, Chunks: 2},
		Left: store.ReembedCount{Documents: 2, Chunks: 2}}, p)
	lines := watchLines(t, logs.String())
	require.Len(t, lines, 1)
	assert.Equal(t, "chunk context: re-embedding the library's chunks", lines[0].Msg)

	logs.Reset()
	none := NewReembed(ReembedOptions{Documents: emptyReembed{}, Insights: failingOwer{}, TenantID: "local",
		Log: logsTo(&logs)})
	began, err = none.Start(ctx)
	require.NoError(t, err)
	assert.False(t, began, "nothing to re-embed: nothing owed, nothing queued")
	assert.Empty(t, logs.String())
	assert.False(t, none.Progress().Active)
}

// emptyReembed is a library with nothing to re-embed.
type emptyReembed struct{}

func (emptyReembed) CountToReembed(context.Context, string, int) (store.ReembedCount, error) {
	return store.ReembedCount{}, nil
}

func (emptyReembed) EnqueueReembed(context.Context, string, int, store.ReembedLimits) (int, error) {
	return 0, errors.New("nothing to queue")
}

// failingOwer can't owe a fresh rebuild.
type failingOwer struct{}

func (failingOwer) OweFresh(context.Context, string, store.FreshReason) error {
	return errors.New("owed")
}

// TestIndexHandler_Reembed: a re-embed job re-embeds a document still
// fetched with stale chunks, with their context, recording when without
// touching the document's state or updated_at; leaves one no longer
// fetched, or whose chunks are current, as it is, embedding nothing; and
// keeps the earlier vectors of one whose markdown is gone, or with a chunk
// the model can't take even without its context, recorded as current so
// that the re-embed doesn't queue it again, with one WARN.
func TestIndexHandler_Reembed(t *testing.T) {
	ctx := context.Background()
	chunks := func(t *testing.T, db *sqlitestore.DB, docID string) [][2]any {
		t.Helper()
		return chunkContexts(t, db, docID)
	}
	times := func(t *testing.T, db *sqlitestore.DB, docID string) [2]any {
		t.Helper()
		row := dumpRowsOf(t, db, `SELECT indexed_at, updated_at FROM documents WHERE id = ?`, docID)
		return [2]any{row[0], row[1]}
	}
	backdate := func(t *testing.T, db *sqlitestore.DB, docID string) {
		t.Helper()
		_, err := db.Exec(`UPDATE documents SET indexed_at = '2026-01-01T00:00:00.000Z',
			updated_at = '2026-01-01T00:00:00.000Z' WHERE id = ?`, docID)
		require.NoError(t, err)
	}

	t.Run("a document still fetched", func(t *testing.T) {
		deps, db, _ := newTestDeps(t)
		doc := fetchedBeforeContext(t, deps, db, "https://example.com/a")
		backdate(t, db, doc.ID)
		require.NoError(t, indexHandler(deps)(ctx, reembedJob(t, doc.ID)))
		assert.Equal(t, [][2]any{{"Article Title — example.com — Title", indexer.ContextVersion}}, chunks(t, db, doc.ID))
		got, err := deps.Documents.GetByID(ctx, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, store.DocStateFetched, got.State)
		at := times(t, db, doc.ID)
		assert.NotEqual(t, "2026-01-01T00:00:00.000Z", at[0], "indexed_at records the new vectors")
		assert.Equal(t, "2026-01-01T00:00:00.000Z", at[1], "updated_at, the Library's order, is left alone")
	})

	t.Run("a document no longer fetched", func(t *testing.T) {
		for _, gone := range []func(Deps, string) error{
			func(d Deps, id string) error { // a refetch in flight
				_, err := d.Documents.RequeueFetch(ctx, "local", id)
				return err
			},
			func(d Deps, id string) error { return d.Documents.MarkFailed(ctx, id, store.FailureCauseAntiBot) },
		} {
			deps, db, _ := newTestDeps(t)
			doc := fetchedBeforeContext(t, deps, db, "https://example.com/b")
			require.NoError(t, gone(deps, doc.ID))
			before, err := deps.Documents.GetByID(ctx, doc.ID)
			require.NoError(t, err)

			require.NoError(t, indexHandler(deps)(ctx, reembedJob(t, doc.ID)))
			assert.Equal(t, [][2]any{{"", 0}}, chunks(t, db, doc.ID), "nothing re-embedded")
			after, err := deps.Documents.GetByID(ctx, doc.ID)
			require.NoError(t, err)
			assert.Equal(t, before.State, after.State)
			assert.Equal(t, before.UpdatedAt, after.UpdatedAt, "nothing written")
		}
	})

	t.Run("a document whose chunks are current", func(t *testing.T) {
		deps, db, _ := newTestDeps(t)
		doc := fetchedBeforeContext(t, deps, db, "https://example.com/c")
		require.NoError(t, indexHandler(deps)(ctx, docJob(t, store.JobKindIndex, doc.ID)), "a reindex since")
		backdate(t, db, doc.ID)
		before := chunks(t, db, doc.ID)
		emb := &countingEmbedder{fakeEmbedder: fakeEmbedder{dim: sqlitetest.Width(t, db)}}
		deps.Indexer = indexer.New(deps.Chunks, emb, indexer.Options{})

		require.NoError(t, indexHandler(deps)(ctx, reembedJob(t, doc.ID)))
		assert.Zero(t, emb.calls.Load(), "nothing embedded")
		assert.Equal(t, before, chunks(t, db, doc.ID))
		assert.Equal(t, [2]any{"2026-01-01T00:00:00.000Z", "2026-01-01T00:00:00.000Z"}, times(t, db, doc.ID),
			"nothing written")
	})

	for name, lose := range map[string]func(t *testing.T, db *sqlitestore.DB, deps Deps, ext *store.DocumentExtraction){
		"a document whose markdown is gone": func(t *testing.T, _ *sqlitestore.DB, deps Deps,
			ext *store.DocumentExtraction) {
			require.NoError(t, os.Remove(filepath.Join(deps.ContentDir, *ext.MarkdownPath)))
		},
		"an extraction that names no markdown": func(t *testing.T, db *sqlitestore.DB, _ Deps,
			ext *store.DocumentExtraction) {
			_, err := db.Exec(`UPDATE document_extractions SET markdown_path = NULL WHERE id = ?`, ext.ID)
			require.NoError(t, err)
		},
	} {
		t.Run(name, func(t *testing.T) {
			deps, db, _ := newTestDeps(t)
			var logs bytes.Buffer
			deps.Log = logsTo(&logs)
			doc := fetchedBeforeContext(t, deps, db, "https://example.com/d")
			ext, err := deps.Extractions.GetByID(ctx, *doc.CurrentExtractionID)
			require.NoError(t, err)
			lose(t, db, deps, ext)
			vectors, err := deps.Chunks.EmbeddingsForDocument(ctx, doc.ID)
			require.NoError(t, err)

			require.NoError(t, indexHandler(deps)(ctx, reembedJob(t, doc.ID)))
			assert.Equal(t, [][2]any{{"", indexer.ContextVersion}}, chunks(t, db, doc.ID),
				"kept as it was, recorded current")
			kept, err := deps.Chunks.EmbeddingsForDocument(ctx, doc.ID)
			require.NoError(t, err)
			assert.Equal(t, vectors, kept)
			got, err := deps.Documents.GetByID(ctx, doc.ID)
			require.NoError(t, err)
			assert.Equal(t, store.DocStateFetched, got.State)
			assert.Contains(t, logs.String(), `"level":"WARN","msg":"chunk context: a document's markdown is gone`)
			assert.Contains(t, logs.String(), doc.ID)
		})
	}

	t.Run("a chunk the model can take only without its context", func(t *testing.T) {
		deps, db, _ := newTestDeps(t)
		doc := fetchedBeforeContext(t, deps, db, "https://example.com/e")
		text := dumpRowsOf(t, db, `SELECT text FROM chunks WHERE document_id = ?`, doc.ID)[0].(string)
		emb := &shortEmbedder{fakeEmbedder: fakeEmbedder{dim: sqlitetest.Width(t, db)}, max: len(text)}
		deps.Indexer = indexer.New(deps.Chunks, emb, indexer.Options{})

		require.NoError(t, indexHandler(deps)(ctx, reembedJob(t, doc.ID)))
		assert.Equal(t, []string{text}, emb.sent(), "embedded from its text alone")
		assert.Equal(t, [][2]any{{"", indexer.ContextVersion}}, chunks(t, db, doc.ID),
			"which is what its context records")
	})

	t.Run("a chunk the model can't take even without its context", func(t *testing.T) {
		deps, db, _ := newTestDeps(t)
		var logs bytes.Buffer
		deps.Log = logsTo(&logs)
		doc := fetchedBeforeContext(t, deps, db, "https://example.com/f")
		vectors, err := deps.Chunks.EmbeddingsForDocument(ctx, doc.ID)
		require.NoError(t, err)
		deps.Indexer = indexer.New(deps.Chunks, refusingEmbedder{fmt.Errorf("ollama embed: %w: HTTP 400: "+
			"the input length exceeds the context length", embedder.ErrInputTooLong)}, indexer.Options{})

		require.NoError(t, indexHandler(deps)(ctx, reembedJob(t, doc.ID)))
		assert.Equal(t, [][2]any{{"", indexer.ContextVersion}}, chunks(t, db, doc.ID), "kept as it was, recorded current")
		kept, err := deps.Chunks.EmbeddingsForDocument(ctx, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, vectors, kept)
		got, err := deps.Documents.GetByID(ctx, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, store.DocStateFetched, got.State)
		assert.Contains(t, logs.String(), `"level":"WARN","msg":"chunk context: the embedding model can't take `+
			`a document's chunk even without its context`)

		err = indexHandler(deps)(ctx, docJob(t, store.JobKindIndex, doc.ID))
		require.ErrorIs(t, err, ErrPermanent, "a regular index job still fails")
		require.ErrorIs(t, err, embedder.ErrInputTooLong)
	})
}

// dumpRowsOf is the first row query reads, its columns as the driver
// gives them.
func dumpRowsOf(t *testing.T, db *sqlitestore.DB, query string, args ...any) []any {
	t.Helper()
	rows, err := db.Query(query, args...)
	require.NoError(t, err)
	defer rows.Close()
	cols, err := rows.Columns()
	require.NoError(t, err)
	require.True(t, rows.Next(), "no row")
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	require.NoError(t, rows.Scan(ptrs...))
	require.NoError(t, rows.Err())
	return vals
}

// countingEmbedder is a fakeEmbedder that counts its calls.
type countingEmbedder struct {
	fakeEmbedder
	calls atomic.Int64
}

func (e *countingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	e.calls.Add(1)
	return e.fakeEmbedder.Embed(ctx, texts)
}

// shortEmbedder is a fakeEmbedder with a context of max bytes: a batch
// holding a longer text is refused as too long, as Ollama refuses it. It
// records each text it embedded.
type shortEmbedder struct {
	fakeEmbedder
	max int

	mu       sync.Mutex
	embedded []string
}

func (e *shortEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	for _, text := range texts {
		if len(text) > e.max {
			return nil, fmt.Errorf("ollama embed: %w: %d bytes", embedder.ErrInputTooLong, len(text))
		}
	}
	e.mu.Lock()
	e.embedded = append(e.embedded, texts...)
	e.mu.Unlock()
	return e.fakeEmbedder.Embed(ctx, texts)
}

func (e *shortEmbedder) sent() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return slices.Clone(e.embedded)
}

// racingEmbedder is a fakeEmbedder that runs race once, while it embeds
// the first batch: what a refetch does while an index job embeds.
type racingEmbedder struct {
	fakeEmbedder
	race func()
	once sync.Once
}

func (e *racingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	e.once.Do(e.race)
	return e.fakeEmbedder.Embed(ctx, texts)
}

// raceAtRecord is a DocumentStore that runs race once, before an index is
// recorded: what a refetch does after an index job wrote its chunks.
type raceAtRecord struct {
	store.DocumentStore
	race func()
	once sync.Once
}

func (r *raceAtRecord) MarkIndexed(ctx context.Context, id string, guard store.IndexGuard, words int) error {
	r.once.Do(r.race)
	return r.DocumentStore.MarkIndexed(ctx, id, guard, words)
}

// TestIndexHandler_LosesTheRaceToARefetch: an index job, regular or
// re-embed, that a refetch overtakes while it embeds writes nothing and is
// done: a new extraction made current keeps its own chunks and state, and
// a re-embed's document that failed meanwhile stays failed, its cause
// kept.
func TestIndexHandler_LosesTheRaceToARefetch(t *testing.T) {
	ctx := context.Background()
	for _, reembed := range []bool{false, true} {
		t.Run(fmt.Sprintf("a new extraction, reembed %v", reembed), func(t *testing.T) {
			deps, db, ff := newTestDeps(t)
			var logs bytes.Buffer
			deps.Log = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			doc := fetchedBeforeContext(t, deps, db, "https://example.com/a")
			deps.Indexer = indexer.New(deps.Chunks, &racingEmbedder{fakeEmbedder: fakeEmbedder{dim: sqlitetest.Width(t,
				db)}, race: func() {
				// The refetch's fetch and index, run whole.
				ff.res.Markdown = "# Title\n\nThe refetched body."
				_, err := deps.Documents.RequeueFetch(ctx, "local", doc.ID)
				require.NoError(t, err)
				refetch := deps
				refetch.Indexer = indexer.New(deps.Chunks, &fakeEmbedder{dim: sqlitetest.Width(t, db)}, indexer.Options{})
				require.NoError(t, fetchHandler(refetch)(ctx, docJob(t, store.JobKindFetch, doc.ID)))
				require.NoError(t, indexHandler(refetch)(ctx, docJob(t, store.JobKindIndex, doc.ID)))
			}}, indexer.Options{})
			job := docJob(t, store.JobKindIndex, doc.ID)
			if reembed {
				job = reembedJob(t, doc.ID)
			}

			require.NoError(t, indexHandler(deps)(ctx, job))
			got, err := deps.Documents.GetByID(ctx, doc.ID)
			require.NoError(t, err)
			assert.NotEqual(t, *doc.CurrentExtractionID, *got.CurrentExtractionID)
			assert.Equal(t, store.DocStateFetched, got.State)
			texts := dumpRowsOf(t, db, `SELECT group_concat(text, ' | '), group_concat(extraction_id) FROM chunks
				WHERE document_id = ?`, doc.ID)
			assert.Equal(t, []any{"# Title The refetched body.", *got.CurrentExtractionID}, texts,
				"the refetch's chunks stand")
			assert.Contains(t, logs.String(), `"msg":"index: the document moved on while it was indexed; nothing written"`)
		})
	}

	t.Run("after its chunks were written", func(t *testing.T) {
		deps, db, ff := newTestDeps(t)
		var logs bytes.Buffer
		deps.Log = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
		doc := fetchedBeforeContext(t, deps, db, "https://example.com/c")
		refetch := deps
		deps.Documents = &raceAtRecord{DocumentStore: deps.Documents, race: func() {
			// The refetch's fetch, between the chunks and the record.
			ff.res.Markdown = "# Title\n\nThe refetched body."
			_, err := refetch.Documents.RequeueFetch(ctx, "local", doc.ID)
			require.NoError(t, err)
			require.NoError(t, fetchHandler(refetch)(ctx, docJob(t, store.JobKindFetch, doc.ID)))
		}}

		require.NoError(t, indexHandler(deps)(ctx, docJob(t, store.JobKindIndex, doc.ID)))
		assert.Contains(t, logs.String(), `"msg":"index: the document moved on after its chunks were written, so `+
			`its record wasn't; its next index rewrites the chunks"`)
		got, err := refetch.Documents.GetByID(ctx, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, store.DocStatePending, got.State, "the refetch's to record")
		require.NoError(t, indexHandler(refetch)(ctx, docJob(t, store.JobKindIndex, doc.ID)))
		assert.Equal(t, []any{"# Title The refetched body."}, dumpRowsOf(t, db,
			`SELECT group_concat(text, ' | ') FROM chunks WHERE document_id = ?`, doc.ID), "and its index rewrites them")
	})

	t.Run("a re-embed's document that failed meanwhile", func(t *testing.T) {
		deps, db, _ := newTestDeps(t)
		doc := fetchedBeforeContext(t, deps, db, "https://example.com/b")
		deps.Indexer = indexer.New(deps.Chunks, &racingEmbedder{fakeEmbedder: fakeEmbedder{dim: sqlitetest.Width(t,
			db)}, race: func() {
			_, err := deps.Documents.RequeueFetch(ctx, "local", doc.ID)
			require.NoError(t, err)
			require.NoError(t, deps.Documents.MarkFailed(ctx, doc.ID, store.FailureCauseAntiBot))
		}}, indexer.Options{})

		require.NoError(t, indexHandler(deps)(ctx, reembedJob(t, doc.ID)))
		got, err := deps.Documents.GetByID(ctx, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, store.DocStateFailed, got.State, "never brought back to fetched")
		assert.Equal(t, store.FailureCauseAntiBot, got.FailureCause)
		assert.Equal(t, [][2]any{{"", 0}}, chunkContexts(t, db, doc.ID), "nothing re-embedded")
	})
}

// TestMarkDocFailed_Reembed: a re-embed job that fails for good, a
// verdict, its attempts spent or an orphan out of attempts at start,
// leaves its document fetched and searchable, with one WARN, to be tried
// again. The document keeps its earlier vectors for good, recorded as
// current, at its third failure once another document was indexed since
// the first, whatever the cause, and never while none was. A regular index
// job still fails its document.
func TestMarkDocFailed_Reembed(t *testing.T) {
	ctx := context.Background()
	// fail records a re-embed of doc failed for good with cause, through
	// the hook, and returns the hook's log.
	fail := func(t *testing.T, deps Deps, logs *bytes.Buffer, doc *store.Document, cause error) string {
		t.Helper()
		job := reembedJob(t, doc.ID)
		job.Status = store.JobStatusFailed
		require.NoError(t, deps.Queue.Enqueue(ctx, job))
		logs.Reset()
		require.NoError(t, markDocFailed(deps)(ctx, job, cause))
		got, err := deps.Documents.GetByID(ctx, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, store.DocStateFetched, got.State)
		assert.Empty(t, got.FailureCause)
		assert.Equal(t, 1, strings.Count(logs.String(), `"level":"WARN"`), logs.String())
		return logs.String()
	}
	stale := func(t *testing.T, deps Deps, doc *store.Document) bool {
		t.Helper()
		stale, err := deps.Chunks.StaleContext(ctx, doc.ID, indexer.ContextVersion)
		require.NoError(t, err)
		return stale
	}
	indexedSince := func(t *testing.T, db *sqlitestore.DB, doc *store.Document) {
		t.Helper()
		_, err := db.Exec(`UPDATE documents SET indexed_at = '2999-01-01T00:00:00.000Z' WHERE id = ?`, doc.ID)
		require.NoError(t, err)
	}

	t.Run("a failure of the document's own", func(t *testing.T) {
		deps, db, _ := newTestDeps(t)
		var logs bytes.Buffer
		deps.Log = logsTo(&logs)
		doc := fetchedBeforeContext(t, deps, db, "https://example.com/a")
		other := fetchedBeforeContext(t, deps, db, "https://example.com/other")
		own := errors.New("index: decode response: unexpected end of JSON input")
		for failures := 1; failures <= reembedMaxFailures; failures++ {
			out := fail(t, deps, &logs, doc, own)
			assert.True(t, stale(t, deps, doc), "tried again: failure %d", failures)
			assert.Contains(t, out, "the re-embed tries it again")
		}
		assert.Contains(t, logs.String(), `"others_indexed":false`, "nothing indexed since its first failure")

		indexedSince(t, db, other)
		out := fail(t, deps, &logs, doc, own)
		assert.False(t, stale(t, deps, doc), "kept for good, others indexed since")
		assert.Contains(t, out, "a refetch re-indexes it")
		assert.Equal(t, [][2]any{{"", indexer.ContextVersion}}, chunkContexts(t, db, doc.ID))

		require.NoError(t, markDocFailed(deps)(ctx, docJob(t, store.JobKindIndex, doc.ID), errors.New("boom")))
		got, err := deps.Documents.GetByID(ctx, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, store.DocStateFailed, got.State, "a regular index job fails its document")
	})

	t.Run("whatever the cause", func(t *testing.T) {
		for name, cause := range map[string]error{
			"a width that doesn't fit": fmt.Errorf("%w: index: ollama embed: %w: embedding[0] has dim 768, "+
				"expected 1024", ErrPermanent, embedder.ErrWrongDimension),
			"the model missing": fmt.Errorf("index: ollama embed: %w: %w", ollama.ErrModelNotLoaded,
				&ollama.StatusError{Code: 404, Body: `{"error":"model \"m\" not found"}`}),
			"an error status the document draws": fmt.Errorf("index: ollama embed: %w",
				&ollama.StatusError{Code: 500, Body: "failed to encode response: json: unsupported value: NaN"}),
			"no answer, past the deferral budget": errDown,
			"a timeout":                           fmt.Errorf("index: ollama embed: %w", context.DeadlineExceeded),
		} {
			t.Run(name, func(t *testing.T) {
				deps, db, _ := newTestDeps(t)
				var logs bytes.Buffer
				deps.Log = logsTo(&logs)
				doc := fetchedBeforeContext(t, deps, db, "https://example.com/a")
				other := fetchedBeforeContext(t, deps, db, "https://example.com/other")
				for failures := 1; failures <= reembedMaxFailures; failures++ {
					fail(t, deps, &logs, doc, cause)
					assert.True(t, stale(t, deps, doc), "nothing indexed since: failure %d", failures)
				}
				indexedSince(t, db, other)
				out := fail(t, deps, &logs, doc, cause)
				assert.False(t, stale(t, deps, doc), "kept, another document indexed since its first failure")
				assert.Contains(t, out, "a refetch re-indexes it")
			})
		}
	})

	t.Run("an orphan out of attempts", func(t *testing.T) {
		deps, db, _ := newTestDeps(t)
		doc := fetchedBeforeContext(t, deps, db, "https://example.com/b")
		job := reembedJob(t, doc.ID)
		job.Status, job.Attempts = store.JobStatusRunning, 5
		require.NoError(t, deps.Queue.Enqueue(ctx, job))
		var index Pool
		for _, p := range NewPools(deps, PoolSizes{Fetch: 1, Index: 1}, WorkerOptions{Log: quietLog}) {
			if p.Kind == store.JobKindIndex {
				index = p
			}
		}
		require.NoError(t, index.Worker.RecoverOrphans(ctx))
		assert.Equal(t, store.JobStatusFailed, getJob(t, deps.Queue, job.ID).Status)
		got, err := deps.Documents.GetByID(ctx, doc.ID)
		require.NoError(t, err)
		assert.Equal(t, store.DocStateFetched, got.State)
		assert.True(t, stale(t, deps, doc))
	})
}

// scriptedReembed is a ReembedStore whose counts follow a script, the
// last answer standing, and which counts its top-ups and says on reads
// that it was read.
type scriptedReembed struct {
	mu      sync.Mutex
	answers []countAnswer
	topUps  int
	topErr  error
	reads   chan struct{}
}

type countAnswer struct {
	left store.ReembedCount
	err  error
}

func (s *scriptedReembed) CountToReembed(_ context.Context, tenantID string, version int) (store.ReembedCount,
	error) {
	if tenantID != "local" || version != indexer.ContextVersion {
		return store.ReembedCount{}, errors.New("asked for another tenant or version")
	}
	s.mu.Lock()
	a := s.answers[0]
	if len(s.answers) > 1 {
		s.answers = s.answers[1:]
	}
	s.mu.Unlock()
	select {
	case s.reads <- struct{}{}:
	default:
	}
	return a.left, a.err
}

func (s *scriptedReembed) EnqueueReembed(context.Context, string, int, store.ReembedLimits) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.topUps++
	return 0, s.topErr
}

// watchLine is a re-embed's log line.
type watchLine struct {
	Level         string   `json:"level"`
	Msg           string   `json:"msg"`
	DocumentsLeft *int     `json:"documents_left"`
	ChunksLeft    *int     `json:"chunks_left"`
	Documents     *int     `json:"documents"`
	ChunksPerS    *float64 `json:"chunks_per_s"`
	ETA           *string  `json:"eta"`
}

func watchLines(t *testing.T, logs string) []watchLine {
	t.Helper()
	var out []watchLine
	for raw := range strings.Lines(logs) {
		var l watchLine
		require.NoError(t, json.Unmarshal([]byte(raw), &l))
		out = append(out, l)
	}
	return out
}

// runScripted runs a re-embed, begun with 10 documents of 10 chunks left,
// over scripted until it returns, and returns its log lines.
func runScripted(t *testing.T, scripted *scriptedReembed, progressEvery time.Duration) []watchLine {
	t.Helper()
	var logs bytes.Buffer
	r := NewReembed(ReembedOptions{Documents: scripted, TenantID: "local", Log: logsTo(&logs),
		CheckEvery: time.Millisecond, ProgressEvery: progressEvery})
	r.progress = ReembedProgress{Active: true, Began: time.Now(), Of: countOf(10), Left: countOf(10)}
	r.samples = []reembedSample{{at: time.Now(), chunks: 100}}
	done := make(chan struct{})
	go func() { defer close(done); r.Run(context.Background()) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the re-embed didn't finish")
	}
	return watchLines(t, logs.String())
}

// countOf is n documents of 10 chunks each.
func countOf(n int) store.ReembedCount { return store.ReembedCount{Documents: n, Chunks: 10 * n} }

// TestReembed_Run: the re-embed tops itself up at each check, logs its
// progress when the chunks left change, at most every ProgressEvery,
// nothing while they don't, and a line once none is left; a top-up or a
// read that fails is a WARN at most every ProgressEvery; it stops when its
// context ends.
func TestReembed_Run(t *testing.T) {
	t.Run("progress, then done", func(t *testing.T) {
		s := &scriptedReembed{answers: []countAnswer{{left: countOf(10)}, {left: countOf(6)}, {left: countOf(6)},
			{left: countOf(2)}, {left: countOf(0)}}, reads: make(chan struct{}, 100)}
		got := runScripted(t, s, time.Nanosecond)
		require.Len(t, got, 3)
		assert.Equal(t, "chunk context: re-embedding", got[0].Msg)
		assert.Equal(t, []int{6, 60}, []int{*got[0].DocumentsLeft, *got[0].ChunksLeft})
		assert.Positive(t, *got[0].ChunksPerS, "a rate measured from the start")
		assert.NotEmpty(t, *got[0].ETA)
		assert.Equal(t, []int{2, 20}, []int{*got[1].DocumentsLeft, *got[1].ChunksLeft})
		assert.Equal(t, "chunk context: re-embedded the library's chunks", got[2].Msg)
		assert.Equal(t, 10, *got[2].Documents)
		assert.GreaterOrEqual(t, s.topUps, 5, "topped up at each check")
	})
	t.Run("at most every ProgressEvery", func(t *testing.T) {
		s := &scriptedReembed{answers: []countAnswer{{left: countOf(9)}, {left: countOf(8)}, {left: countOf(0)}},
			reads: make(chan struct{}, 100)}
		got := runScripted(t, s, time.Hour)
		require.Len(t, got, 1)
		assert.Equal(t, "chunk context: re-embedded the library's chunks", got[0].Msg)
	})
	t.Run("reads and top-ups that fail", func(t *testing.T) {
		s := &scriptedReembed{answers: []countAnswer{{err: errors.New("database is locked")},
			{err: errors.New("database is locked")}, {left: countOf(0)}}, topErr: errors.New("database is locked"),
			reads: make(chan struct{}, 100)}
		got := runScripted(t, s, time.Hour)
		require.Len(t, got, 2, "one WARN for the failures, then done")
		assert.Equal(t, "WARN", got[0].Level)
		assert.Equal(t, "chunk context: re-embedded the library's chunks", got[1].Msg)
	})

	t.Run("nothing while nothing changes, until the context ends", func(t *testing.T) {
		var logs bytes.Buffer
		s := &scriptedReembed{answers: []countAnswer{{left: countOf(10)}}, reads: make(chan struct{}, 100)}
		r := NewReembed(ReembedOptions{Documents: s, TenantID: "local", Log: logsTo(&logs),
			CheckEvery: time.Millisecond, ProgressEvery: time.Nanosecond})
		r.progress = ReembedProgress{Active: true, Began: time.Now(), Of: countOf(10), Left: countOf(10)}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); r.Run(ctx) }()
		for range 5 {
			<-s.reads // a paused queue: checked, and the same each time
		}
		cancel()
		<-done
		assert.Empty(t, logs.String())
		assert.Zero(t, r.Progress().ChunksPerSecond, "nothing moved")
	})
}

// TestReembed_Rate: the rate is the chunks that left over the samples in
// the window, the last before it included, and the time left is the chunks
// left at it.
func TestReembed_Rate(t *testing.T) {
	r := NewReembed(ReembedOptions{RateWindow: 10 * time.Minute})
	t0 := time.Date(2026, 10, 14, 9, 0, 0, 0, time.UTC)
	r.samples = []reembedSample{{at: t0, chunks: 10_000}}
	r.progress = ReembedProgress{Active: true, Left: store.ReembedCount{Documents: 100, Chunks: 10_000}}
	p, moved := r.record(t0.Add(time.Minute), store.ReembedCount{Documents: 94, Chunks: 9_400})
	assert.True(t, moved)
	assert.InDelta(t, 10, p.ChunksPerSecond, 1e-9)
	assert.Equal(t, 940*time.Second, p.ETA(9_400))
	for m := 2; m <= 20; m++ {
		p, _ = r.record(t0.Add(time.Duration(m)*time.Minute), store.ReembedCount{Documents: 90,
			Chunks: 9_400 - (m-1)*300})
	}
	assert.InDelta(t, 5, p.ChunksPerSecond, 0.01, "the last 10 minutes' 300 a minute, not the first minute's")
	assert.LessOrEqual(t, len(r.samples), 12)
	_, moved = r.record(t0.Add(21*time.Minute), p.Left)
	assert.False(t, moved)
	p, _ = r.record(t0.Add(40*time.Minute), p.Left)
	assert.Zero(t, p.ChunksPerSecond, "nothing moved in the window")
	assert.Zero(t, p.ETA(100), "no time left without a rate")
	assert.Zero(t, ReembedETA(0, 3))
}

// feedLog records the order documents were embedded in, and how many
// re-embed jobs were running or pending at each embed.
type feedLog struct {
	fakeEmbedder
	db *sqlitestore.DB

	mu       sync.Mutex
	inFlight []int
	err      error
}

func (f *feedLog) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	var n int
	err := f.db.QueryRow(`SELECT count(*) FROM jobs WHERE kind = 'index' AND status IN ('pending', 'running')
		AND json_extract(payload, '$.reembed')`).Scan(&n)
	f.mu.Lock()
	f.inFlight = append(f.inFlight, n)
	f.err = errors.Join(f.err, err)
	f.mu.Unlock()
	return f.fakeEmbedder.Embed(ctx, texts)
}

// TestReembed_FeedsOneAtATime: through the daemon's pools, the re-embed
// keeps one job in flight, topped up as each finishes, until every
// document is re-embedded; a page saved meanwhile is indexed beside it, not
// behind the backlog; nothing is left stale and no document changes state.
func TestReembed_FeedsOneAtATime(t *testing.T) {
	deps, db, _ := newTestDeps(t)
	ctx := context.Background()
	docs := make([]*store.Document, 0, 6)
	for i := range 6 {
		docs = append(docs, fetchedBeforeContext(t, deps, db, fmt.Sprintf("https://example.com/%d", i)))
	}
	emb := &feedLog{fakeEmbedder: fakeEmbedder{dim: sqlitetest.Width(t, db)}, db: db}
	deps.Indexer = indexer.New(deps.Chunks, emb, indexer.Options{})
	r := NewReembed(ReembedOptions{Documents: deps.Documents, Insights: sqlitestore.NewInsights(db), TenantID: "local",
		Log: quietLog, CheckEvery: 20 * time.Millisecond})
	deps.Reembed = r
	began, err := r.Start(ctx)
	require.NoError(t, err)
	require.True(t, began)
	saved := &store.Document{TenantID: "local", URL: "https://example.com/saved"}
	require.NoError(t, deps.Documents.Create(ctx, saved))
	require.NoError(t, fetchHandler(deps)(ctx, docJob(t, store.JobKindFetch, saved.ID)))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Go(func() { r.Run(runCtx) })
	for _, p := range NewPools(deps, PoolSizes{Fetch: 1, Index: 2},
		WorkerOptions{PollInterval: 5 * time.Millisecond, MaxPollInterval: 20 * time.Millisecond, Log: quietLog}) {
		if p.Kind == store.JobKindIndex {
			for range p.Size {
				wg.Go(func() { _ = p.Worker.Run(runCtx) })
			}
		}
	}
	require.Eventually(t, func() bool {
		left, err := deps.Documents.CountToReembed(ctx, "local", indexer.ContextVersion)
		return err == nil && left.Documents == 0 && !r.Progress().Active
	}, 10*time.Second, 10*time.Millisecond)
	cancel()
	wg.Wait()

	emb.mu.Lock()
	defer emb.mu.Unlock()
	require.NoError(t, emb.err)
	assert.Equal(t, 1, slices.Max(emb.inFlight), "one re-embed job in flight at every embed")
	var reembeds, failed int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM jobs WHERE json_extract(payload, '$.reembed')`).Scan(&reembeds))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM jobs WHERE status = 'failed'`).Scan(&failed))
	assert.Equal(t, len(docs), reembeds, "each document queued once")
	assert.Zero(t, failed)
	var savedStarted, lastReembedStarted string
	require.NoError(t, db.QueryRow(`SELECT started_at FROM jobs WHERE kind = 'index' AND document_id = ?`,
		saved.ID).Scan(&savedStarted))
	require.NoError(t, db.QueryRow(`SELECT max(started_at) FROM jobs WHERE json_extract(payload, '$.reembed')`).
		Scan(&lastReembedStarted))
	assert.Less(t, savedStarted, lastReembedStarted, "the saved page is indexed beside the re-embed, not after it")
	for _, d := range append(docs, saved) {
		got, err := deps.Documents.GetByID(ctx, d.ID)
		require.NoError(t, err)
		assert.Equal(t, store.DocStateFetched, got.State, d.URL)
		assert.Equal(t, [][2]any{{"Article Title — example.com — Title", indexer.ContextVersion}},
			chunkContexts(t, db, d.ID), d.URL)
	}
}

// brokenModel is an embedding model that fails every embed with err while
// it is set, and embeds as a fakeEmbedder once it is cleared.
type brokenModel struct {
	fakeEmbedder
	err atomic.Pointer[error]
}

func (m *brokenModel) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if err := m.err.Load(); err != nil {
		return nil, *err
	}
	return m.fakeEmbedder.Embed(ctx, texts)
}

// TestReembed_FailureOfEveryDocument: through the daemon's index pool and
// the feed, an embedding model that fails every document's embed (an error
// status, a model missing, a width that doesn't fit) queues no more than
// the feed's in flight and waiting, keeps no document's earlier vectors for
// good, and puts the brake on once, with one WARN, after which nothing more
// is queued; once the model is fixed the feed goes on, and every document
// is re-embedded.
func TestReembed_FailureOfEveryDocument(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"an error status", fmt.Errorf("ollama embed: %w",
			&ollama.StatusError{Code: 500, Body: `{"error":"llama runner process has terminated"}`})},
		{"the model missing", fmt.Errorf("ollama embed: %w: %w", ollama.ErrModelNotLoaded,
			&ollama.StatusError{Code: 404, Body: `{"error":"model \"m\" not found, try pulling it first"}`})},
		{"a width that doesn't fit", fmt.Errorf("ollama embed: %w: embedding[0] has dim 768, expected 1024",
			embedder.ErrWrongDimension)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const docs = 20
			deps, db, _ := newTestDeps(t)
			ctx := context.Background()
			for i := range docs {
				fetchedBeforeContext(t, deps, db, fmt.Sprintf("https://example.com/%d", i))
			}
			model := &brokenModel{fakeEmbedder: fakeEmbedder{dim: sqlitetest.Width(t, db)}}
			model.err.Store(&tc.err)
			deps.Indexer = indexer.New(deps.Chunks, model, indexer.Options{})
			var logs syncBuffer
			log := slog.New(slog.NewJSONHandler(&logs, nil))
			deps.Log = log
			r := NewReembed(ReembedOptions{Documents: deps.Documents, Insights: sqlitestore.NewInsights(db),
				TenantID: "local", Log: log, CheckEvery: 10 * time.Millisecond, FailedWait: time.Second,
				BrakeMin: 2 * time.Second})
			deps.Reembed = r
			began, err := r.Start(ctx)
			require.NoError(t, err)
			require.True(t, began)

			runCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			var wg sync.WaitGroup
			wg.Go(func() { r.Run(runCtx) })
			for _, p := range NewPools(deps, PoolSizes{Fetch: 1, Index: 4},
				WorkerOptions{PollInterval: 5 * time.Millisecond, MaxPollInterval: 20 * time.Millisecond, Log: log}) {
				if p.Kind == store.JobKindIndex {
					for range p.Size {
						wg.Go(func() { _ = p.Worker.Run(runCtx) })
					}
				}
			}
			require.Eventually(t, func() bool { return r.Progress().Brake.On() }, 5*time.Second, time.Millisecond)
			time.Sleep(200 * time.Millisecond) // the brake on: nothing more is queued
			brake := r.Progress().Brake
			assert.Equal(t, reembedBrakeAfter, brake.Failures)
			assert.Contains(t, brake.Err, tc.err.Error())
			var queued, current int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM jobs WHERE json_extract(payload, '$.reembed')`).
				Scan(&queued))
			assert.LessOrEqual(t, queued, reembedInFlight+reembedWaiting, "at most the feed's in flight and waiting")
			assert.Equal(t, reembedBrakeAfter, queued, "one job each for the documents that failed, then the brake")
			left, err := deps.Documents.CountToReembed(ctx, "local", indexer.ContextVersion)
			require.NoError(t, err)
			assert.Equal(t, store.ReembedCount{Documents: docs, Chunks: docs}, left, "nothing recorded re-embedded")
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM chunks WHERE context_version = ?`,
				indexer.ContextVersion).Scan(&current))
			assert.Zero(t, current, "no document keeps its earlier vectors as current")

			model.err.Store(nil)
			// The minute the retries wait, passed.
			_, err = db.Exec(`UPDATE jobs SET run_after = '2000-01-01T00:00:00.000Z' WHERE status = 'pending'`)
			require.NoError(t, err)
			require.Eventually(t, func() bool { return !r.Progress().Active }, 10*time.Second, 5*time.Millisecond,
				"every document re-embedded once the model is fixed")
			cancel()
			wg.Wait()

			left, err = deps.Documents.CountToReembed(ctx, "local", indexer.ContextVersion)
			require.NoError(t, err)
			assert.Zero(t, left.Documents)
			out := logs.String()
			assert.Equal(t, 1, strings.Count(out, `"msg":"chunk context: documents' re-embeds keep failing`), out)
			assert.Equal(t, 1, strings.Count(out, `"level":"INFO","msg":"chunk context: a document was re-embedded; `+
				`the re-embed resumes"`)+strings.Count(out, `"msg":"chunk context: the re-embed tries again after `+
				`its pause"`), out)
			assert.NotContains(t, out, "a refetch re-indexes it")
			var failedDocs int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM documents WHERE state != 'fetched'`).Scan(&failedDocs))
			assert.Zero(t, failedDocs)
		})
	}
}

// TestReembed_Brake: the brake goes on once BrakeAfter documents' re-embeds
// failed for the first time with none re-embedded since, never for one
// document failing again and again, with one WARN, naming the first
// reembedBrakeNamed documents; it stops the feed until its time passes,
// which turns it off with one INFO, also when read, and a first failure
// after that puts it straight back on for twice as long, up to BrakeMax,
// where a document that failed before doesn't. A document re-embedded
// turns it off with one INFO, and its next stop is BrakeMin again.
func TestReembed_Brake(t *testing.T) {
	var logs bytes.Buffer
	r := NewReembed(ReembedOptions{Log: logsTo(&logs), BrakeAfter: 3, BrakeMin: time.Hour, BrakeMax: 3 * time.Hour})
	boom := errors.New("HTTP 500: boom")
	for range 5 {
		r.Failed("a", boom)
	}
	assert.False(t, r.Progress().Brake.On(), "one document failing again and again")
	r.Failed("b", boom)
	before := time.Now()
	r.Failed("c", errors.New("HTTP 500: the last"))
	b := r.Progress().Brake
	require.True(t, b.On())
	assert.Equal(t, 3, b.Failures)
	assert.Equal(t, []string{"a", "b", "c"}, b.Documents)
	assert.Equal(t, "HTTP 500: the last", b.Err)
	assert.WithinRange(t, b.Until, before.Add(time.Hour), time.Now().Add(time.Hour))
	for _, doc := range []string{"d", "e", "f"} {
		r.Failed(doc, boom)
	}
	assert.Equal(t, b.Until, r.Progress().Brake.Until, "on already")
	assert.Equal(t, 6, r.Progress().Brake.Failures)
	assert.Equal(t, []string{"a", "b", "c", "d", "e"}, r.Progress().Brake.Documents, "the first five named")
	assert.True(t, r.braked(b.Until.Add(-time.Second)))

	for i, pause := range []time.Duration{2 * time.Hour, 3 * time.Hour, 3 * time.Hour} {
		until := r.Progress().Brake.Until
		assert.False(t, r.braked(until), "off once its time passed")
		assert.False(t, r.Progress().Brake.On())
		r.Failed("a", boom)
		assert.False(t, r.Progress().Brake.On(), "a document that failed before says nothing of the library")
		before := time.Now()
		r.Failed(fmt.Sprint("new ", i), boom)
		assert.WithinRange(t, r.Progress().Brake.Until, before.Add(pause), time.Now().Add(pause),
			"back on at once, for longer")
	}

	r.Reembedded()
	assert.False(t, r.Progress().Brake.On())
	assert.False(t, r.braked(time.Now()))
	for _, doc := range []string{"a", "b", "c", "d"} {
		r.Failed(doc, boom)
	}
	assert.False(t, r.Progress().Brake.On(), "documents that failed before count no more after one re-embedded")
	r.Failed("x", boom)
	r.Failed("y", boom)
	before = time.Now()
	r.Failed("z", boom)
	assert.Equal(t, []string{"x", "y", "z"}, r.Progress().Brake.Documents)
	assert.WithinRange(t, r.Progress().Brake.Until, before.Add(time.Hour), time.Now().Add(time.Hour),
		"its first stop again")

	// A brake whose time has passed is off when read, as the next check
	// finds it.
	r.mu.Lock()
	r.brake.until = time.Now().Add(-time.Second)
	r.mu.Unlock()
	assert.False(t, r.Progress().Brake.On())
	assert.False(t, r.braked(time.Now()), "and stays off")

	lines := watchLines(t, logs.String())
	msgs := make([]string, 0, len(lines))
	for _, l := range lines {
		msgs = append(msgs, l.Level+" "+l.Msg)
	}
	warn := "WARN chunk context: documents' re-embeds keep failing; the re-embed stops queueing them for a while, " +
		"and they keep their earlier vectors"
	pause := "INFO chunk context: the re-embed tries again after its pause"
	assert.Equal(t, []string{warn, pause, warn, pause, warn, pause, warn,
		"INFO chunk context: a document was re-embedded; the re-embed resumes", warn, pause}, msgs)

	var none *Reembed
	none.Failed("a", boom)
	none.Reembedded()
	assert.False(t, none.Progress().Brake.On())
}

// badDocuments is an embedding model that fails every batch holding a
// chunk of a document titled BAD-DOC with err, and embeds the rest.
type badDocuments struct {
	fakeEmbedder
	err error
}

func (e *badDocuments) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	for _, text := range texts {
		if strings.Contains(text, "BAD-DOC") {
			return nil, e.err
		}
	}
	return e.fakeEmbedder.Embed(ctx, texts)
}

// TestReembed_FewDocumentsFailingOnTheirOwn: through the daemon's index
// pool and the feed, a library whose first three documents in the walk's
// order fail every embed for their own reasons (a 400, or a 500 a document
// alone draws, Ollama failing to encode its NaN vector), each attempt a
// failure for good: the brake goes on once, the nine others are all
// re-embedded once it lifts, ahead of the three, which are then tried again
// and kept on their earlier vectors at their third failure, whatever its
// status; the re-embed ends with the brake off. Whether the three's wait
// is over when the brake lifts or not.
func TestReembed_FewDocumentsFailingOnTheirOwn(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		err                  error
		failedWait, brakeMin time.Duration
	}{
		{"a 400, its wait over at the lift", fmt.Errorf("ollama embed: %w",
			&ollama.StatusError{Code: 400, Body: `{"error":"bad input"}`}), 50 * time.Millisecond, 200 * time.Millisecond},
		{"a 500 of its own, its wait over at the lift", fmt.Errorf("ollama embed: %w", &ollama.StatusError{Code: 500,
			Body: `{"error":"failed to encode response: json: unsupported value: NaN"}`}), 50 * time.Millisecond,
			200 * time.Millisecond},
		{"a 400, its wait still running at the lift", fmt.Errorf("ollama embed: %w",
			&ollama.StatusError{Code: 400, Body: `{"error":"bad input"}`}), 400 * time.Millisecond,
			200 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, db, _ := newTestDeps(t)
			deps.Queue.(*sqlitestore.Jobs).MaxAttempts = 1 // each failure is for good: the retries compressed
			ctx := context.Background()
			ids := make([]string, 0, 12)
			for i := range 12 {
				ids = append(ids, fetchedBeforeContext(t, deps, db, fmt.Sprintf("https://example.com/%d", i)).ID)
			}
			slices.Sort(ids) // the walk's order
			bad, healthy := ids[:3], ids[3:]
			for _, id := range bad {
				_, err := db.Exec(`UPDATE documents SET title = 'BAD-DOC' WHERE id = ?`, id)
				require.NoError(t, err)
			}
			deps.Indexer = indexer.New(deps.Chunks, &badDocuments{fakeEmbedder: fakeEmbedder{dim: sqlitetest.Width(t, db)},
				err: tc.err}, indexer.Options{})
			var logs syncBuffer
			log := slog.New(slog.NewJSONHandler(&logs, nil))
			deps.Log = log
			r := NewReembed(ReembedOptions{Documents: deps.Documents, Insights: sqlitestore.NewInsights(db),
				TenantID: "local", Log: log, CheckEvery: 10 * time.Millisecond, FailedWait: tc.failedWait,
				BrakeMin: tc.brakeMin, BrakeMax: 4 * tc.brakeMin})
			deps.Reembed = r
			began, err := r.Start(ctx)
			require.NoError(t, err)
			require.True(t, began)

			runCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			var wg sync.WaitGroup
			wg.Go(func() { r.Run(runCtx) })
			for _, p := range NewPools(deps, PoolSizes{Fetch: 1, Index: 4},
				WorkerOptions{PollInterval: 5 * time.Millisecond, MaxPollInterval: 20 * time.Millisecond, Log: log}) {
				if p.Kind == store.JobKindIndex {
					for range p.Size {
						wg.Go(func() { _ = p.Worker.Run(runCtx) })
					}
				}
			}
			require.Eventually(t, func() bool { return !r.Progress().Active }, 10*time.Second, 5*time.Millisecond,
				"the re-embed ends")
			cancel()
			wg.Wait()

			assert.False(t, r.Progress().Brake.On(), "the brake off at the end")
			jobsOf := func(id string) int {
				t.Helper()
				var n int
				require.NoError(t, db.QueryRow(`SELECT count(*) FROM jobs WHERE document_id = ?
					AND json_extract(payload, '$.reembed')`, id).Scan(&n))
				return n
			}
			for _, id := range healthy {
				assert.Equal(t, [][2]any{{"Article Title — example.com — Title", indexer.ContextVersion}},
					chunkContexts(t, db, id), "re-embedded")
				assert.Equal(t, 1, jobsOf(id), "once")
			}
			for _, id := range bad {
				assert.Equal(t, [][2]any{{"", indexer.ContextVersion}}, chunkContexts(t, db, id),
					"kept on its earlier vectors")
				assert.Equal(t, reembedMaxFailures, jobsOf(id), "kept at its third failure")
			}
			var notFetched int
			require.NoError(t, db.QueryRow(`SELECT count(*) FROM documents WHERE state != 'fetched'`).Scan(&notFetched))
			assert.Zero(t, notFetched)
			out := logs.String()
			assert.Equal(t, 1, strings.Count(out, `"msg":"chunk context: documents' re-embeds keep failing`), out)
			assert.Equal(t, len(bad), strings.Count(out, "a refetch re-indexes it"), out)
		})
	}
}
