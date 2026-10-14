package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// reembedLibrary is a library whose documents' chunks were embedded under
// several context versions, for the re-embed's reads and writes.
type reembedLibrary struct {
	t      *testing.T
	ctx    context.Context
	db     *DB
	docs   *Documents
	chunks *Chunks
	jobs   *Jobs
	n      int
}

func newReembedLibrary(t *testing.T) *reembedLibrary {
	db := newTestDB(t)
	return &reembedLibrary{t: t, ctx: context.Background(), db: db, docs: NewDocuments(db),
		chunks: NewChunks(db, vecDim), jobs: NewJobs(db)}
}

// add writes a document of tenant in state with a chunk embedded under
// each of versions, and returns its ID.
func (l *reembedLibrary) add(tenant string, state store.DocState, versions ...int) string {
	l.t.Helper()
	l.n++
	id := seedDocs(l.t, l.db, tenant, fmt.Sprintf("https://example.com/%d", l.n))[0]
	inputs := make([]store.ChunkInput, len(versions))
	for i, v := range versions {
		inputs[i] = store.ChunkInput{Text: fmt.Sprintf("chunk %d", i), ContextVersion: v, Embedding: fillVec(0.1)}
	}
	require.NoError(l.t, l.chunks.ReplaceForDocument(l.ctx, id, latestExtractionID(l.t, l.db, id), "", nil, inputs))
	switch state {
	case store.DocStateFetched:
		require.NoError(l.t, l.docs.MarkFetched(l.ctx, id, 0))
	case store.DocStateFailed, store.DocStateDead:
		require.NoError(l.t, l.docs.MarkFailed(l.ctx, id, stateCause(state)))
	case store.DocStatePending:
	}
	return id
}

func (l *reembedLibrary) toReembed(version int) []string {
	l.t.Helper()
	got, err := l.docs.DocumentsToReembed(l.ctx, "local", version)
	require.NoError(l.t, err)
	n, err := l.docs.CountToReembed(l.ctx, "local", version)
	require.NoError(l.t, err)
	assert.Len(l.t, got, n.Documents, "the count counts what the list lists")
	return got
}

// indexJobs is each index job of id: its status and payload.
func (l *reembedLibrary) indexJobs(id string) [][]any {
	l.t.Helper()
	return dumpRows(l.t, l.db, `SELECT status, payload FROM jobs WHERE kind = ? AND document_id = ? ORDER BY rowid`,
		store.JobKindIndex, id)
}

// reembedJobs is the IDs of the documents with a re-embed job in status.
func (l *reembedLibrary) reembedJobs(status store.JobStatus) []string {
	l.t.Helper()
	rows := dumpRows(l.t, l.db, `SELECT document_id FROM jobs WHERE kind = ? AND status = ?
		AND json_extract(payload, '$.reembed') ORDER BY document_id`, store.JobKindIndex, status)
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row[0].(string))
	}
	return ids
}

// enqueue tops up the re-embed for version 1 with inFlight in flight and
// room for as many waiting as any test makes, and returns how many it
// queued.
func (l *reembedLibrary) enqueue(inFlight int) int {
	l.t.Helper()
	return l.enqueueWithin(store.ReembedLimits{InFlight: inFlight, Waiting: 100})
}

// enqueueWithin tops up the re-embed for version 1 within limits, and
// returns how many it queued.
func (l *reembedLibrary) enqueueWithin(limits store.ReembedLimits) int {
	l.t.Helper()
	n, err := l.docs.EnqueueReembed(l.ctx, "local", 1, limits)
	require.NoError(l.t, err)
	return n
}

// TestDocumentsToReembed: the documents to re-embed are the tenant's
// fetched documents with a current extraction holding a chunk of an older
// context version, whatever their jobs; the count counts them and their
// chunks of an older version.
func TestDocumentsToReembed(t *testing.T) {
	l := newReembedLibrary(t)
	old := l.add("local", store.DocStateFetched, 0, 0)
	mixed := l.add("local", store.DocStateFetched, 1, 0)
	l.add("local", store.DocStateFetched, 1, 1) // current
	l.add("local", store.DocStateFetched)       // no chunks
	l.add("local", store.DocStatePending, 0)
	l.add("local", store.DocStateFailed, 0)
	l.add("local", store.DocStateDead, 0)
	l.add("other", store.DocStateFetched, 0)
	noExtraction := l.add("local", store.DocStateFetched, 0)
	_, err := l.db.Exec(`UPDATE documents SET current_extraction_id = NULL WHERE id = ?`, noExtraction)
	require.NoError(t, err)

	assert.ElementsMatch(t, []string{old, mixed}, l.toReembed(1))
	n, err := l.docs.CountToReembed(l.ctx, "local", 1)
	require.NoError(t, err)
	assert.Equal(t, store.ReembedCount{Documents: 2, Chunks: 3}, n, "their chunks of an older version alone")
	assert.Empty(t, l.toReembed(0), "nothing is older than version 0")
	assert.Len(t, l.toReembed(2), 3, "every fetched document with chunks and an extraction, for a version after")
	n, err = l.docs.CountToReembed(l.ctx, "local", 2)
	require.NoError(t, err)
	assert.Equal(t, store.ReembedCount{Documents: 3, Chunks: 6}, n)

	job, err := store.NewDocumentJob("local", store.JobKindIndex, old)
	require.NoError(t, err)
	require.NoError(t, l.jobs.Enqueue(l.ctx, job))
	assert.ElementsMatch(t, []string{old, mixed}, l.toReembed(1), "an index job queued changes nothing")
}

// TestEnqueueReembed: a top-up queues re-embed jobs, their payload marked,
// for the next documents to re-embed with no index job pending or running,
// until inFlight are running or pending and due, waking the index workers;
// a re-embed job waiting out a retry or a deferral holds no place, and a
// regular index job none either, though its document is passed over.
func TestEnqueueReembed(t *testing.T) {
	l := newReembedLibrary(t)
	stale := make([]string, 0, 4)
	for range 4 {
		stale = append(stale, l.add("local", store.DocStateFetched, 0))
	}
	slices.Sort(stale)
	l.add("local", store.DocStateFetched, 1)
	l.add("local", store.DocStatePending, 0)
	l.add("other", store.DocStateFetched, 0)
	regular, err := store.NewDocumentJob("local", store.JobKindIndex, stale[0])
	require.NoError(t, err)
	require.NoError(t, l.jobs.Enqueue(l.ctx, regular))
	woken := l.jobs.Enqueued([]store.JobKind{store.JobKindIndex})

	assert.Equal(t, 2, l.enqueue(2))
	select {
	case <-woken:
	default:
		t.Fatal("the index workers weren't woken")
	}
	assert.Equal(t, stale[1:3], l.reembedJobs(store.JobStatusPending),
		"the next two in order, past the one an index job re-embeds")
	var payload store.DocumentJobPayload
	require.NoError(t, json.Unmarshal([]byte(l.indexJobs(stale[1])[0][1].(string)), &payload))
	assert.Equal(t, store.DocumentJobPayload{DocumentID: stale[1], Reembed: true}, payload)
	assert.Zero(t, l.enqueue(2), "two in flight")
	assert.Equal(t, 1, l.enqueue(3), "a place more, for the last")
	assert.Zero(t, l.enqueue(9), "each document is queued once")

	_, err = l.db.Exec(`UPDATE jobs SET status = 'running' WHERE document_id = ?`, stale[1])
	require.NoError(t, err)
	_, err = l.db.Exec(`UPDATE jobs SET status = 'done' WHERE document_id IN (?, ?)`, stale[2], stale[3])
	require.NoError(t, err)
	assert.Equal(t, 2, l.enqueue(3), "the two done documents, still to re-embed, queued again beside the running")
	_, err = l.db.Exec(`UPDATE jobs SET run_after = '2999-01-01T00:00:00.000Z', last_error = 'waiting for the `+
		`embedding model to answer' WHERE document_id = ? AND status = 'pending'`, stale[2])
	require.NoError(t, err)
	assert.Zero(t, l.enqueue(2), "the running one and the due one")
	_, err = l.db.Exec(`UPDATE jobs SET status = 'done' WHERE document_id = ?`, stale[3])
	require.NoError(t, err)
	assert.Equal(t, 1, l.enqueue(2), "the deferred one holds no place")
	assert.Equal(t, []string{stale[3]}, l.reembedJobs(store.JobStatusPending)[1:],
		"the deferred one stays, and the other is queued again")

	assert.Zero(t, l.enqueueWithin(store.ReembedLimits{Waiting: 100}), "no place at all")
}

// TestEnqueueReembed_Limits: a top-up stops at InFlight + Waiting re-embed
// jobs running or pending, due or not, so a failure every document meets
// parks that many, not the library; passes over a document whose re-embed
// failed for good within FailedWait, which would otherwise be queued again
// at once; and queues the documents none of whose re-embeds failed before
// any whose did, which it queues again, in order, once no other is left.
func TestEnqueueReembed_Limits(t *testing.T) {
	l := newReembedLibrary(t)
	stale := make([]string, 0, 6)
	for range 6 {
		stale = append(stale, l.add("local", store.DocStateFetched, 0))
	}
	slices.Sort(stale)
	limits := store.ReembedLimits{InFlight: 1, Waiting: 2, FailedWait: time.Hour}
	retry := func(id string) {
		t.Helper()
		_, err := l.db.Exec(`UPDATE jobs SET run_after = '2999-01-01T00:00:00.000Z', last_error = 'HTTP 500'
			WHERE document_id = ? AND status = 'pending'`, id)
		require.NoError(t, err)
	}

	for i := range 3 {
		require.Equal(t, 1, l.enqueueWithin(limits), "document %d", i)
		retry(stale[i])
	}
	assert.Zero(t, l.enqueueWithin(limits), "three waiting out a retry: the cap, though none is in flight")
	assert.Equal(t, stale[:3], l.reembedJobs(store.JobStatusPending))

	_, err := l.db.Exec(`UPDATE jobs SET status = 'failed' WHERE document_id = ?`, stale[0])
	require.NoError(t, err)
	require.Equal(t, 1, l.enqueueWithin(limits))
	assert.Equal(t, stale[1:4], l.reembedJobs(store.JobStatusPending),
		"the next, past the one that just failed for good")

	_, err = l.db.Exec(`UPDATE jobs SET status = 'done' WHERE document_id = ?;
		UPDATE chunks SET context_version = 1 WHERE document_id = ?;
		UPDATE jobs SET updated_at = '2000-01-01T00:00:00.000Z' WHERE document_id = ?`, stale[3], stale[3], stale[0])
	require.NoError(t, err)
	require.Equal(t, 1, l.enqueueWithin(limits))
	assert.Equal(t, []string{stale[1], stale[2], stale[4]}, l.reembedJobs(store.JobStatusPending),
		"its wait over, still after every document that never failed")

	// The two waiting fail for good too, long ago, and the last that never
	// failed is queued: those that failed come next, first in order first.
	_, err = l.db.Exec(`UPDATE jobs SET status = 'failed', updated_at = '2000-01-01T00:00:00.000Z'
		WHERE document_id IN (?, ?) AND status = 'pending'`, stale[1], stale[2])
	require.NoError(t, err)
	assert.Equal(t, 2, l.enqueueWithin(store.ReembedLimits{InFlight: 3, Waiting: 0, FailedWait: time.Hour}))
	assert.Equal(t, []string{stale[0], stale[4], stale[5]}, l.reembedJobs(store.JobStatusPending),
		"the last that never failed, then the first in order of those that did")
	_, err = l.db.Exec(`UPDATE jobs SET status = 'done' WHERE status = 'pending';
		UPDATE chunks SET context_version = 1 WHERE document_id IN (?, ?, ?)`, stale[0], stale[4], stale[5])
	require.NoError(t, err)
	assert.Equal(t, 2, l.enqueueWithin(store.ReembedLimits{InFlight: 9, Waiting: 0, FailedWait: time.Hour}),
		"the two left, each once")
	assert.Equal(t, []string{stale[1], stale[2]}, l.reembedJobs(store.JobStatusPending))
}

// TestReembedFailures: a document's re-embed jobs failed for good are
// counted, and no other job of its; and whether another document was
// indexed after the first of them failed: not the document itself, and
// not before.
func TestReembedFailures(t *testing.T) {
	l := newReembedLibrary(t)
	id := l.add("local", store.DocStateFetched, 0)
	other := l.add("local", store.DocStateFetched, 0)
	l.add("other", store.DocStateFetched, 0)
	read := func(id string) store.ReembedFailures {
		t.Helper()
		got, err := l.docs.ReembedFailures(l.ctx, "local", id)
		require.NoError(t, err)
		return got
	}
	_, err := l.db.Exec(`UPDATE documents SET indexed_at = '2026-01-01T00:00:00.000Z'`)
	require.NoError(t, err)
	assert.Equal(t, store.ReembedFailures{}, read(id))

	for i, status := range []store.JobStatus{store.JobStatusFailed, store.JobStatusFailed, store.JobStatusDone,
		store.JobStatusPending} {
		require.Equal(t, 1, l.enqueue(1))
		_, err := l.db.Exec(`UPDATE jobs SET status = ?, updated_at = ? WHERE status = 'pending'`, status,
			fmt.Sprintf("2026-01-0%dT00:00:00.000Z", i+2))
		require.NoError(t, err)
	}
	regular, err := store.NewDocumentJob("local", store.JobKindIndex, id)
	require.NoError(t, err)
	regular.Status = store.JobStatusFailed
	require.NoError(t, l.jobs.Enqueue(l.ctx, regular))
	got := read(id)
	got.Failed += read(other).Failed
	assert.Equal(t, store.ReembedFailures{Failed: 2}, got,
		"the two re-embeds failed, not the done or pending one, nor a regular index job; nothing indexed since")

	failing := id
	if read(id).Failed == 0 {
		failing = other
	}
	first := dumpRows(t, l.db, `SELECT min(updated_at) FROM jobs WHERE document_id = ? AND status = 'failed'
		AND json_extract(payload, '$.reembed')`, failing)[0][0].(string)
	index := func(doc, at string) {
		t.Helper()
		_, err := l.db.Exec(`UPDATE documents SET indexed_at = ? WHERE id = ?`, at, doc)
		require.NoError(t, err)
	}
	others := slices.DeleteFunc([]string{id, other}, func(d string) bool { return d == failing })
	index(failing, "2026-06-01T00:00:00.000Z")
	assert.False(t, read(failing).OthersIndexed, "the document's own index isn't another's")
	index(others[0], first)
	assert.False(t, read(failing).OthersIndexed, "indexed as it failed, not after")
	_, err = l.db.Exec(`UPDATE documents SET indexed_at = '2026-06-01T00:00:00.000Z' WHERE tenant_id = 'other'`)
	require.NoError(t, err)
	assert.False(t, read(failing).OthersIndexed, "another tenant's document says nothing")
	index(others[0], "2026-06-01T00:00:00.000Z")
	assert.True(t, read(failing).OthersIndexed)
}

// TestChunks_StaleContext: a document is stale while any of its chunks is
// of an older context version than the one asked about.
func TestChunks_StaleContext(t *testing.T) {
	l := newReembedLibrary(t)
	stale := func(id string, version int) bool {
		t.Helper()
		got, err := l.chunks.StaleContext(l.ctx, id, version)
		require.NoError(t, err)
		return got
	}
	mixed := l.add("local", store.DocStateFetched, 1, 0, 1)
	current := l.add("local", store.DocStateFetched, 1, 1)
	none := l.add("local", store.DocStateFetched)
	assert.True(t, stale(mixed, 1))
	assert.False(t, stale(current, 1))
	assert.True(t, stale(current, 2))
	assert.False(t, stale(none, 1), "no chunks, nothing to re-embed")
	assert.False(t, stale("no-such-document", 1))
}

// TestChunks_KeepEmbedded: a document's chunks of an older context version
// are recorded as embedded under the version given, their contexts and
// vectors as they were, nothing written to chunks_fts; it is no longer to
// re-embed.
func TestChunks_KeepEmbedded(t *testing.T) {
	l := newReembedLibrary(t)
	id := l.add("local", store.DocStateFetched, 0, 1, 0)
	other := l.add("local", store.DocStateFetched, 0)
	vectors, err := l.chunks.EmbeddingsForDocument(l.ctx, id)
	require.NoError(t, err)
	fts := ftsShadows(t, l.db)

	n, err := l.chunks.KeepEmbedded(l.ctx, id, 1)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "the chunks of an older version")
	assert.Equal(t, [][]any{{"", int64(1)}, {"", int64(1)}, {"", int64(1)}},
		dumpRows(t, l.db, `SELECT context, context_version FROM chunks WHERE document_id = ? ORDER BY ord`, id))
	assert.Equal(t, fts, ftsShadows(t, l.db), "chunks_fts is as it was")
	kept, err := l.chunks.EmbeddingsForDocument(l.ctx, id)
	require.NoError(t, err)
	assert.Equal(t, vectors, kept)
	assert.Equal(t, []string{other}, l.toReembed(1))
}

// TestChunks_ReplaceIndexed: an index's chunks are written while the
// extraction it read is the document's current one, and a re-embed's while
// the document is fetched too; otherwise nothing is written,
// ErrSuperseded, and a document that is gone is ErrNotFound.
func TestChunks_ReplaceIndexed(t *testing.T) {
	l := newReembedLibrary(t)
	id := l.add("local", store.DocStateFetched, 0)
	read := latestExtractionID(t, l.db, id)
	chunks := func() [][]any {
		t.Helper()
		return dumpRows(t, l.db, `SELECT text, context_version, extraction_id FROM chunks WHERE document_id = ?`, id)
	}
	replace := func(guard store.IndexGuard, text string) error {
		t.Helper()
		return l.chunks.ReplaceIndexed(l.ctx, id, guard, "", nil,
			[]store.ChunkInput{{Text: text, ContextVersion: 1, Embedding: fillVec(0.2)}})
	}

	require.NoError(t, replace(store.IndexGuard{ExtractionID: read, Reembed: true}, "re-embedded"))
	assert.Equal(t, [][]any{{"re-embedded", int64(1), read}}, chunks())

	// A refetch: a new extraction is current, and the document pending.
	refetched := &store.DocumentExtraction{DocumentID: id, Fetcher: "test", Status: store.ExtractionStatusOK}
	require.NoError(t, NewExtractions(l.db).Create(l.ctx, refetched))
	require.NoError(t, l.docs.SetCurrentExtraction(l.ctx, id, refetched.ID))
	for _, guard := range []store.IndexGuard{{ExtractionID: read}, {ExtractionID: read, Reembed: true}} {
		err := replace(guard, "the old extraction")
		require.ErrorIs(t, err, store.ErrSuperseded, "%+v", guard)
		assert.Equal(t, [][]any{{"re-embedded", int64(1), read}}, chunks(), "nothing written")
	}
	_, err := l.docs.RequeueFetch(l.ctx, "local", id)
	require.NoError(t, err)
	require.ErrorIs(t, replace(store.IndexGuard{ExtractionID: refetched.ID, Reembed: true}, "x"), store.ErrSuperseded,
		"a re-embed of a document no longer fetched")
	require.NoError(t, replace(store.IndexGuard{ExtractionID: refetched.ID}, "the new extraction"),
		"the refetch's own index")
	assert.Equal(t, [][]any{{"the new extraction", int64(1), refetched.ID}}, chunks())

	err = l.chunks.ReplaceIndexed(l.ctx, "no-such-document", store.IndexGuard{ExtractionID: read}, "", nil, nil)
	require.ErrorIs(t, err, store.ErrNotFound)
}

// TestDocuments_MarkIndexed: an index is recorded while the extraction it
// read is the document's current one: the document fetched, with no
// failure cause, its word count and indexed_at, updated_at with it; a
// re-embed's, while the document is fetched too, records the word count
// and indexed_at alone. Otherwise nothing is written, ErrSuperseded, and a
// document that is gone is ErrNotFound.
func TestDocuments_MarkIndexed(t *testing.T) {
	l := newReembedLibrary(t)
	id := l.add("local", store.DocStatePending, 0)
	ext := latestExtractionID(t, l.db, id)
	row := func() []any {
		t.Helper()
		return dumpRows(t, l.db, `SELECT state, failure_cause, word_count, indexed_at, updated_at FROM documents
			WHERE id = ?`, id)[0]
	}
	setTimes := func() {
		t.Helper()
		_, err := l.db.Exec(`UPDATE documents SET indexed_at = '2026-01-01T00:00:00.000Z',
			updated_at = '2026-01-01T00:00:00.000Z' WHERE id = ?`, id)
		require.NoError(t, err)
	}

	require.NoError(t, l.docs.MarkIndexed(l.ctx, id, store.IndexGuard{ExtractionID: ext}, 12))
	got := row()
	assert.Equal(t, []any{string(store.DocStateFetched), nil, int64(12)}, got[:3])
	assert.Equal(t, got[4], got[3], "indexed_at and updated_at, one now")

	setTimes()
	require.NoError(t, l.docs.MarkIndexed(l.ctx, id, store.IndexGuard{ExtractionID: ext, Reembed: true}, 14))
	got = row()
	assert.Equal(t, []any{string(store.DocStateFetched), nil, int64(14)}, got[:3])
	assert.NotEqual(t, "2026-01-01T00:00:00.000Z", got[3], "a re-embed writes indexed_at")
	assert.Equal(t, "2026-01-01T00:00:00.000Z", got[4], "and leaves updated_at, the lists' order, alone")

	require.NoError(t, l.docs.MarkFailed(l.ctx, id, store.FailureCauseAntiBot))
	setTimes()
	before := row()
	err := l.docs.MarkIndexed(l.ctx, id, store.IndexGuard{ExtractionID: ext, Reembed: true}, 1)
	require.ErrorIs(t, err, store.ErrSuperseded, "a re-embed never brings a failed document back")
	assert.Equal(t, before, row())
	err = l.docs.MarkIndexed(l.ctx, id, store.IndexGuard{ExtractionID: "another"}, 1)
	require.ErrorIs(t, err, store.ErrSuperseded, "nor an index of another extraction")
	assert.Equal(t, before, row())

	err = l.docs.MarkIndexed(l.ctx, "no-such-document", store.IndexGuard{ExtractionID: ext}, 1)
	require.ErrorIs(t, err, store.ErrNotFound)
	require.Error(t, l.docs.MarkIndexed(l.ctx, id, store.IndexGuard{ExtractionID: ext}, -1))
}
