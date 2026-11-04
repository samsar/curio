package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

// writer is the summaries' writer the tests write and count under.
var writer = store.SummaryWriter{Model: "gemma4:26b", PromptVersion: "p1"}

// summaryEnv is a fields environment with the summary store over it.
type summaryEnv struct {
	*fieldsEnv
	sums *Summaries
}

func newSummaryEnv(t *testing.T) *summaryEnv {
	t.Helper()
	e := newFieldsEnv(t)
	return &summaryEnv{fieldsEnv: e, sums: NewSummaries(e.db)}
}

// fetched saves url as title and marks its document fetched with an
// extraction, returning the document and its extraction.
func (e *summaryEnv) fetched(url, title string) (string, string) {
	e.t.Helper()
	id := *e.ingest(url, store.SourceChrome, title, "").DocumentID
	e.fetch(id, title, "", "")
	require.NoError(e.t, e.docs.MarkFetched(e.ctx, id, 10))
	return id, e.current(id)
}

// current is document id's current extraction.
func (e *summaryEnv) current(id string) string {
	e.t.Helper()
	doc, err := e.docs.GetByID(e.ctx, id)
	require.NoError(e.t, err)
	require.NotNil(e.t, doc.CurrentExtractionID)
	return *doc.CurrentExtractionID
}

// summary is a made-up summary of document id's extraction ext.
func summary(id, ext string) store.Summary {
	return store.Summary{DocumentID: id, ExtractionID: ext, Kind: store.SummaryKindArticle,
		Site: "Example Times, a made-up newspaper", Author: "Bo Example, its editor",
		About: "Why queues back up under load.", Entities: []string{"Bo Example", "Zephyrine"},
		Claims: []string{"Backpressure beats buffering."}, Descriptions: []string{"that piece on overloaded queues"},
		SummaryWriter: writer, InputHash: "h1", WrittenAt: time.Date(2026, 11, 3, 1, 2, 3, 0, time.UTC)}
}

// summariesIntegrity checks the summaries' index against its rows.
func (e *summaryEnv) summariesIntegrity() {
	e.t.Helper()
	require.NoError(e.t, checkIndex(e.ctx, e.db, "document_summaries_fts"))
}

func (e *summaryEnv) search(query string) []string {
	e.t.Helper()
	hits, err := NewDocumentIndex(e.db, vecDim).SearchSummaries(e.ctx, "local", query, 10, store.SearchFilters{})
	require.NoError(e.t, err)
	return hitIDs(hits)
}

// TestSummaries_PutAndGet: a summary is written and read back whole, its
// words searched by the summary list; the card never reads it, so it stays
// as it was, embedded, with no cards job queued.
func TestSummaries_PutAndGet(t *testing.T) {
	e := newSummaryEnv(t)
	id, ext := e.fetched("https://example.com/queues", "Queues")
	ix := NewDocumentIndex(e.db, vecDim)
	e.embedStaleCards(ix, 0.2)
	_, err := e.db.Exec(`UPDATE jobs SET status = 'done' WHERE kind = 'cards'`)
	require.NoError(t, err)
	before := e.card(id)
	require.NoError(t, e.sums.PutSummary(e.ctx, "local", summary(id, ext)))

	got, err := e.sums.GetSummary(e.ctx, "local", id)
	require.NoError(t, err)
	want := summary(id, ext)
	assert.Equal(t, &want, got.Summary)
	assert.False(t, got.Owed)
	assert.Nil(t, got.Failure)
	assert.Equal(t, before, e.card(id), "the card never reads the summary")
	stale, err := ix.CountStaleCards(e.ctx, "local")
	require.NoError(t, err)
	assert.Zero(t, stale, "the card stays embedded")
	assert.Zero(t, e.pendingCardsJobs())
	assert.Equal(t, []string{id}, e.search(`"zephyrine"`))
	assert.Equal(t, []string{id}, e.search(`"backpressure"`), "the list searches the claims")
	e.summariesIntegrity()

	_, err = e.sums.GetSummary(e.ctx, "other", id)
	require.ErrorIs(t, err, store.ErrNotFound, "never another tenant's")
	other, _ := e.fetched("https://example.com/none", "None")
	_, err = e.sums.GetSummary(e.ctx, "local", other)
	require.ErrorIs(t, err, store.ErrNotFound)

	bad := summary(id, ext)
	bad.Kind = "essay"
	require.ErrorContains(t, e.sums.PutSummary(e.ctx, "local", bad), `kind "essay"`)
	bad = summary(id, ext)
	bad.Entities = []string{"two\nlines"}
	require.ErrorContains(t, e.sums.PutSummary(e.ctx, "local", bad), "line break")
}

// TestSummaries_PutSupersededWritesNothing: a refetch between the read and
// the write, a document that failed meanwhile, and one deleted, each make
// the write ErrSuperseded, with nothing written.
func TestSummaries_PutSupersededWritesNothing(t *testing.T) {
	e := newSummaryEnv(t)
	refetched, ext := e.fetched("https://example.com/a", "A")
	e.fetch(refetched, "A", "", "")
	require.NoError(t, e.docs.MarkFetched(e.ctx, refetched, 10))
	require.ErrorIs(t, e.sums.PutSummary(e.ctx, "local", summary(refetched, ext)), store.ErrSuperseded)

	failed, ext2 := e.fetched("https://example.com/b", "B")
	require.NoError(t, e.docs.MarkFailed(e.ctx, failed, store.FailureCauseIndex))
	require.ErrorIs(t, e.sums.PutSummary(e.ctx, "local", summary(failed, ext2)), store.ErrSuperseded)
	require.ErrorIs(t, e.sums.PutSummary(e.ctx, "other", summary(failed, ext2)), store.ErrSuperseded,
		"another tenant's write")

	assert.Zero(t, countRows(t, e.db, "document_summaries"))
	require.ErrorIs(t, e.sums.PutSummary(e.ctx, "local", summary("gone", "x")), store.ErrSuperseded)
}

// TestSummaries_IdenticalRewriteWritesNothing: writing the summary stored
// writes no row, so no trigger fires and the index is untouched; pointing
// it at a refetch's extraction re-tokenizes nothing either.
func TestSummaries_IdenticalRewriteWritesNothing(t *testing.T) {
	e := newSummaryEnv(t)
	id, ext := e.fetched("https://example.com/a", "A")
	require.NoError(t, e.sums.PutSummary(e.ctx, "local", summary(id, ext)))
	const ftsData = `SELECT id, block FROM document_summaries_fts_data ORDER BY id`
	index := dumpRows(t, e.db, ftsData)
	row := dumpRows(t, e.db, `SELECT * FROM document_summaries`)

	require.NoError(t, e.sums.PutSummary(e.ctx, "local", summary(id, ext)))
	assert.Equal(t, row, dumpRows(t, e.db, `SELECT * FROM document_summaries`))
	assert.Equal(t, index, dumpRows(t, e.db, ftsData), "no write reached the index")

	e.fetch(id, "A", "", "")
	require.NoError(t, e.docs.MarkFetched(e.ctx, id, 10))
	moved := summary(id, e.current(id))
	require.NoError(t, e.sums.PutSummary(e.ctx, "local", moved))
	assert.Equal(t, index, dumpRows(t, e.db, ftsData), "a new extraction alone re-tokenizes nothing")
	e.summariesIntegrity()
}

// TestSummaries_RewritesLeaveTheCard: rewriting a summary over and over
// never makes its document's card stale, nor touches the card's vector.
func TestSummaries_RewritesLeaveTheCard(t *testing.T) {
	e := newSummaryEnv(t)
	ix := NewDocumentIndex(e.db, vecDim)
	id, ext := e.fetched("https://example.com/a", "A")
	e.embedStaleCards(ix, 0.1)
	cards := dumpRows(t, e.db, `SELECT * FROM document_cards`)
	for round := range 3 {
		s := summary(id, ext)
		s.About = fmt.Sprintf("Round %d of a made-up rewrite.", round)
		require.NoError(t, e.sums.PutSummary(e.ctx, "local", s))
		stale, err := ix.CountStaleCards(e.ctx, "local")
		require.NoError(t, err)
		assert.Zero(t, stale, "round %d", round)
		assert.Equal(t, cards, dumpRows(t, e.db, `SELECT * FROM document_cards`), "round %d", round)
	}
}

// TestSummaries_NeverChunks: writing a summary writes no row of the chunks
// or their indexes.
func TestSummaries_NeverChunks(t *testing.T) {
	e := newSummaryEnv(t)
	id, ext := e.fetched("https://example.com/a", "A")
	require.NoError(t, NewChunks(e.db, vecDim).ReplaceForDocument(e.ctx, id, ext, "A", nil,
		[]store.ChunkInput{{Text: "queues back up", Embedding: fillVec(0.3)}}))
	tables := []string{"chunks", "chunks_vec", "chunks_fts_data"}
	before := map[string][][]any{}
	for _, table := range tables {
		before[table] = dumpRows(t, e.db, "SELECT * FROM "+table)
	}
	require.NoError(t, e.sums.PutSummary(e.ctx, "local", summary(id, ext)))
	for _, table := range tables {
		assert.Equal(t, before[table], dumpRows(t, e.db, "SELECT * FROM "+table), table)
	}
}

// TestSummaries_DeleteCascades: deleting a document removes its summary
// and the summary's index entry.
func TestSummaries_DeleteCascades(t *testing.T) {
	e := newSummaryEnv(t)
	id, ext := e.fetched("https://example.com/a", "A")
	require.NoError(t, e.sums.PutSummary(e.ctx, "local", summary(id, ext)))
	require.Equal(t, []string{id}, e.search(`"zephyrine"`))
	_, err := e.db.Exec(deleteDocumentSQL, id)
	require.NoError(t, err)
	assert.Zero(t, countRows(t, e.db, "document_summaries"))
	assert.Empty(t, e.search(`"zephyrine"`))
	e.summariesIntegrity()
}

// TestSummaries_FailedAndDeadKeepTheirs: a document that fails, dies or
// is refetched keeps its summary, in the summary list too.
func TestSummaries_FailedAndDeadKeepTheirs(t *testing.T) {
	e := newSummaryEnv(t)
	ids := make([]string, 0, 3)
	for i, next := range []func(id string){
		func(id string) { require.NoError(t, e.docs.MarkFailed(e.ctx, id, store.FailureCauseAntiBot)) },
		func(id string) { require.NoError(t, e.docs.MarkFailed(e.ctx, id, store.FailureCauseDeadLink)) },
		func(id string) { e.fetch(id, "Refetched", "", "") },
	} {
		id, ext := e.fetched(fmt.Sprintf("https://example.com/%d", i), "Doc")
		require.NoError(t, e.sums.PutSummary(e.ctx, "local", summary(id, ext)))
		next(id)
		got, err := e.sums.GetSummary(e.ctx, "local", id)
		require.NoError(t, err)
		require.NotNil(t, got.Summary, "case %d", i)
		ids = append(ids, id)
	}
	assert.ElementsMatch(t, ids, e.search(`"zephyrine"`))
}

// TestSummaries_OwedAndFailures: a failure for good is recorded beside the
// summary written before, and a write clears it and the owed mark; owing
// marks the outdated summaries of fetched pages, or all of them and their
// failures, which it clears, never a failed or dead page's.
func TestSummaries_OwedAndFailures(t *testing.T) {
	e := newSummaryEnv(t)
	id, ext := e.fetched("https://example.com/a", "A")
	at := time.Date(2026, 11, 3, 4, 5, 6, 0, time.UTC)
	failure := store.SummaryFailure{ExtractionID: ext, SummaryWriter: writer, At: at, Error: "made-up error"}
	require.NoError(t, e.sums.RecordSummaryFailure(e.ctx, "local", id, failure))
	got, err := e.sums.GetSummary(e.ctx, "local", id)
	require.NoError(t, err)
	assert.Nil(t, got.Summary, "a failure alone is no summary")
	assert.Equal(t, &failure, got.Failure)
	require.NoError(t, e.sums.RecordSummaryFailure(e.ctx, "local", "gone", failure), "a deleted document")
	require.NoError(t, e.sums.RecordSummaryFailure(e.ctx, "other", id, failure), "another tenant's: nothing")
	e.summariesIntegrity()

	require.NoError(t, e.sums.PutSummary(e.ctx, "local", summary(id, ext)))
	got, err = e.sums.GetSummary(e.ctx, "local", id)
	require.NoError(t, err)
	assert.Nil(t, got.Failure, "a write clears the failure")
	require.NoError(t, e.sums.RecordSummaryFailure(e.ctx, "local", id, failure))
	got, err = e.sums.GetSummary(e.ctx, "local", id)
	require.NoError(t, err)
	assert.NotNil(t, got.Summary, "a failure keeps the summary written")

	old, oldExt := e.fetched("https://example.com/old", "Old")
	s := summary(old, oldExt)
	s.Model = "qwen3:4b-instruct"
	require.NoError(t, e.sums.PutSummary(e.ctx, "local", s))
	gone, goneExt := e.fetched("https://example.com/gone", "Gone")
	s = summary(gone, goneExt)
	s.Model = "qwen3:4b-instruct"
	require.NoError(t, e.sums.PutSummary(e.ctx, "local", s))
	require.NoError(t, e.docs.MarkFailed(e.ctx, gone, store.FailureCauseDeadLink))
	never, neverExt := e.fetched("https://example.com/never", "Never")
	require.NoError(t, e.sums.RecordSummaryFailure(e.ctx, "local", never,
		store.SummaryFailure{ExtractionID: neverExt, SummaryWriter: writer, At: at, Error: "made-up error"}))

	n, err := e.sums.OweSummaries(e.ctx, "local", store.SummaryScopeOutdated, writer)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "the other model's alone, of a fetched page")
	n, err = e.sums.OweSummaries(e.ctx, "local", store.SummaryScopeOutdated, writer)
	require.NoError(t, err)
	assert.Zero(t, n, "owed already")
	counts, err := e.sums.CountSummaries(e.ctx, "local", writer)
	require.NoError(t, err)
	assert.Equal(t, store.SummaryCounts{Written: 1, ToWrite: 1, Failed: 1}, counts,
		"A written, Old owed, Never failed")

	n, err = e.sums.OweSummaries(e.ctx, "local", store.SummaryScopeAll, writer)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "A, and Never's failure")
	counts, err = e.sums.CountSummaries(e.ctx, "local", writer)
	require.NoError(t, err)
	assert.Equal(t, store.SummaryCounts{ToWrite: 3}, counts, "every fetched page, the failed one too")
	got, err = e.sums.GetSummary(e.ctx, "local", id)
	require.NoError(t, err)
	assert.True(t, got.Owed)
	assert.Nil(t, got.Failure, "cleared, so the page is tried again")
	got, err = e.sums.GetSummary(e.ctx, "local", gone)
	require.NoError(t, err)
	assert.False(t, got.Owed, "a dead page's summary is never owed: the pass doesn't write it")
	_, err = e.sums.OweSummaries(e.ctx, "local", "some", writer)
	require.ErrorContains(t, err, `scope "some"`)
	e.summariesIntegrity()
	got, err = e.sums.GetSummary(e.ctx, "local", old)
	require.NoError(t, err)
	assert.True(t, got.Owed)
	require.NoError(t, e.sums.PutSummary(e.ctx, "local", summary(old, oldExt)))
	got, err = e.sums.GetSummary(e.ctx, "local", old)
	require.NoError(t, err)
	assert.False(t, got.Owed, "a write clears owed")
}

// TestSummaries_CountsMatchARecount: over random libraries, the counts sum
// to the fetched documents with a current extraction, each class as a
// brute-force reading of the rules finds it.
func TestSummaries_CountsMatchARecount(t *testing.T) {
	r := rand.New(rand.NewPCG(3, 5))
	for round := range 5 {
		e := newSummaryEnv(t)
		want := store.SummaryCounts{}
		fetched := 0
		for i := range 40 {
			id, ext := e.fetched(fmt.Sprintf("https://example.com/%d/%d", round, i), "Doc")
			fetched++
			s := summary(id, ext)
			class := r.IntN(7)
			switch class {
			case 0: // none
				want.ToWrite++
				continue
			case 1:
				want.Written++
			case 2:
				s.Kind = store.SummaryKindNotThePage
				want.NotThePage++
			case 3:
				s.PromptVersion = "p0"
				want.Outdated++
			case 4, 5:
				// owed, or of an older extraction
				want.ToWrite++
			case 6:
				want.Failed++
			}
			require.NoError(t, e.sums.PutSummary(e.ctx, "local", s))
			switch class {
			case 4:
				_, err := e.db.Exec(`UPDATE document_summaries SET owed = 1 WHERE document_id = ?`, id)
				require.NoError(t, err)
			case 5:
				e.fetch(id, "Doc", "", "")
				require.NoError(t, e.docs.MarkFetched(e.ctx, id, 10))
			case 6:
				e.fetch(id, "Doc", "", "")
				require.NoError(t, e.docs.MarkFetched(e.ctx, id, 10))
				require.NoError(t, e.sums.RecordSummaryFailure(e.ctx, "local", id, store.SummaryFailure{
					ExtractionID: e.current(id), SummaryWriter: writer, Error: "made up"}))
			}
		}
		// Neither fetched nor with an extraction: counted nowhere.
		id, ext := e.fetched("https://example.com/failed", "Doc")
		require.NoError(t, e.sums.PutSummary(e.ctx, "local", summary(id, ext)))
		require.NoError(t, e.docs.MarkFailed(e.ctx, id, store.FailureCauseAntiBot))
		got, err := e.sums.CountSummaries(e.ctx, "local", writer)
		require.NoError(t, err)
		assert.Equal(t, want, got, "round %d", round)
		assert.Equal(t, fetched, got.Written+got.ToWrite+got.Outdated+got.Failed+got.NotThePage)
	}
}

// TestSummaries_EnqueueSummarize: the feed queues the documents to write,
// those indexed most recently first, within the limits, each with the
// extraction it is to read, never one with a job, never one failed for
// good under its extraction and the writer within the wait.
func TestSummaries_EnqueueSummarize(t *testing.T) {
	e := newSummaryEnv(t)
	ids := make([]string, 0, 5)
	for i := range 5 {
		id, _ := e.fetched(fmt.Sprintf("https://example.com/%d", i), "Doc")
		_, err := e.db.Exec(`UPDATE documents SET indexed_at = ? WHERE id = ?`,
			formatTime(time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC)), id)
		require.NoError(t, err)
		ids = append(ids, id)
	}
	done, doneExt := e.fetched("https://example.com/done", "Done")
	require.NoError(t, e.sums.PutSummary(e.ctx, "local", summary(done, doneExt)))
	require.NoError(t, e.sums.RecordSummaryFailure(e.ctx, "local", ids[4], store.SummaryFailure{
		ExtractionID: e.current(ids[4]), SummaryWriter: writer, At: time.Now(), Error: "made up"}))
	pending, _ := e.fetched("https://example.com/pending", "Pending")
	_, err := e.db.Exec(`UPDATE documents SET state = 'pending' WHERE id = ?`, pending)
	require.NoError(t, err)

	limits := store.SummarizeLimits{InFlight: 1, Waiting: 1, FailedWait: time.Hour}
	targets, err := e.sums.ToSummarize(e.ctx, "local", writer, time.Hour, 10)
	require.NoError(t, err)
	want := make([]store.SummaryTarget, 0, 4)
	for _, i := range []int{3, 2, 1, 0} {
		want = append(want, store.SummaryTarget{DocumentID: ids[i], ExtractionID: e.current(ids[i])})
	}
	assert.Equal(t, want, targets,
		"the developer's list: the feed's rule and order, nothing queued")
	queued := func() [][]any {
		return dumpRows(t, e.db, `SELECT document_id, json_extract(payload, '$.extraction_id'), status FROM jobs
			WHERE kind = 'summarize' ORDER BY rowid`)
	}
	assert.Empty(t, queued(), "listing queues nothing")
	n, err := e.sums.EnqueueSummarize(e.ctx, "local", writer, limits)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "one in flight")
	assert.Equal(t, [][]any{{ids[3], e.current(ids[3]), "pending"}}, queued(),
		"the newest indexed, past the one failed within the wait")
	n, err = e.sums.EnqueueSummarize(e.ctx, "local", writer, limits)
	require.NoError(t, err)
	assert.Zero(t, n, "one pending and due fills the flight")

	_, err = e.db.Exec(`UPDATE jobs SET run_after = ? WHERE kind = 'summarize'`,
		formatTime(time.Now().Add(time.Hour)))
	require.NoError(t, err)
	n, err = e.sums.EnqueueSummarize(e.ctx, "local", writer, limits)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "a job waiting out a retry leaves its place in flight")
	assert.Equal(t, ids[2], queued()[1][0])
	n, err = e.sums.EnqueueSummarize(e.ctx, "local", writer, limits)
	require.NoError(t, err)
	assert.Zero(t, n, "in flight and waiting, full")

	_, err = e.db.Exec(`UPDATE jobs SET status = 'done' WHERE kind = 'summarize'`)
	require.NoError(t, err)
	n, err = e.sums.EnqueueSummarize(e.ctx, "local", writer, store.SummarizeLimits{InFlight: 5, Waiting: 5})
	require.NoError(t, err)
	assert.Equal(t, 5, n, "every document to write but the one written, the failure's wait over")
	rows := queued()
	order := make([]any, 0, len(rows)-2)
	for _, r := range rows[2:] {
		order = append(order, r[0])
	}
	assert.Equal(t, []any{ids[4], ids[3], ids[2], ids[1], ids[0]}, order)
}

// TestSummaries_EnqueueNotBefore: jobs queued to run no sooner than a
// time ahead wait for it, rounded up to the millisecond the jobs table
// keeps, and hold the flight as any job waiting does; a time past is now.
func TestSummaries_EnqueueNotBefore(t *testing.T) {
	e := newSummaryEnv(t)
	for i := range 3 {
		e.fetched(fmt.Sprintf("https://example.com/%d", i), "Doc")
	}
	at := time.Now().Add(time.Minute).Truncate(time.Millisecond).Add(400 * time.Microsecond)
	limits := store.SummarizeLimits{InFlight: 1, Waiting: 0, NotBefore: at}
	n, err := e.sums.EnqueueSummarize(e.ctx, "local", writer, limits)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	runAfter := func() []any {
		rows := dumpRows(t, e.db, `SELECT run_after FROM jobs WHERE kind = 'summarize' ORDER BY rowid`)
		out := make([]any, 0, len(rows))
		for _, r := range rows {
			out = append(out, r[0])
		}
		return out
	}
	assert.Equal(t, []any{formatTime(at.Truncate(time.Millisecond).Add(time.Millisecond))}, runAfter(),
		"never claimable before the time it waits for")
	n, err = e.sums.EnqueueSummarize(e.ctx, "local", writer, limits)
	require.NoError(t, err)
	assert.Zero(t, n, "the one job, waiting, fills the flight of one")

	_, err = e.db.Exec(`UPDATE jobs SET status = 'done' WHERE kind = 'summarize'`)
	require.NoError(t, err)
	before := time.Now()
	limits.NotBefore = before.Add(-time.Hour)
	n, err = e.sums.EnqueueSummarize(e.ctx, "local", writer, limits)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	got, err := parseTime(runAfter()[1].(string))
	require.NoError(t, err)
	assert.False(t, got.Before(before.Truncate(time.Millisecond)), "a time past: due now")
	assert.True(t, runNoSooner(time.Time{}, before).IsZero())
	exact := before.Add(time.Minute).Truncate(time.Millisecond)
	assert.Equal(t, exact, runNoSooner(exact, before), "a whole millisecond is kept as it is")
}

// TestSummaries_EnqueueSkipsADeletedDocument: a document deleted after the
// walk read it, outside the write lock, gets no job, and the documents
// read beside it still do: the insert queues nothing for it rather than
// failing the top-up on the jobs' foreign key.
func TestSummaries_EnqueueSkipsADeletedDocument(t *testing.T) {
	e := newSummaryEnv(t)
	gone, goneExt := e.fetched("https://example.com/gone", "Gone")
	kept, keptExt := e.fetched("https://example.com/kept", "Kept")
	_, err := e.db.Exec(`DELETE FROM documents WHERE id = ?`, gone)
	require.NoError(t, err)

	tx, err := e.db.BeginTx(e.ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	for _, d := range []struct {
		doc, ext string
		want     error
	}{{gone, goneExt, sql.ErrNoRows}, {kept, keptExt, nil}} {
		payload, err := json.Marshal(store.DocumentJobPayload{DocumentID: d.doc, ExtractionID: d.ext})
		require.NoError(t, err)
		err = insertJobWith(e.ctx, tx, insertSummarizeJobSQL,
			&store.Job{TenantID: "local", Kind: store.JobKindSummarize, Payload: payload})
		if d.want == nil {
			require.NoError(t, err)
			continue
		}
		require.ErrorIs(t, err, d.want)
	}
}

// TestSummaries_EnsureRebuildsTheIndex: a summaries index that disagrees
// with its rows fails its check, and EnsureDocumentFields rebuilds it,
// naming it.
func TestSummaries_EnsureRebuildsTheIndex(t *testing.T) {
	e := newSummaryEnv(t)
	id, ext := e.fetched("https://example.com/a", "A")
	require.NoError(t, e.sums.PutSummary(e.ctx, "local", summary(id, ext)))
	var trigger string
	require.NoError(t, e.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'trg_document_summaries_update'`).
		Scan(&trigger))
	for _, q := range []string{`DROP TRIGGER trg_document_summaries_update`,
		`UPDATE document_summaries SET about = 'Quorum loss' WHERE document_id = '` + id + `'`, trigger} {
		_, err := e.db.Exec(q)
		require.NoError(t, err)
	}
	require.ErrorIs(t, checkIndex(e.ctx, e.db, "document_summaries_fts"), ErrSearchIndexInconsistent)
	done, err := EnsureDocumentFields(e.ctx, e.db)
	require.NoError(t, err)
	assert.True(t, done.Rebuilt)
	assert.Equal(t, []string{"document_summaries_fts"}, done.RebuiltIndexes)
	e.summariesIntegrity()
	assert.Equal(t, []string{id}, e.search(`"quorum"`))
}

// TestSummaries_SearchBlankAndWeighted: a blank query reads nothing, and
// every column of the summary list is weighted.
func TestSummaries_SearchBlankAndWeighted(t *testing.T) {
	e := newSummaryEnv(t)
	id, ext := e.fetched("https://example.com/a", "A")
	require.NoError(t, e.sums.PutSummary(e.ctx, "local", summary(id, ext)))
	ix := NewDocumentIndex(e.db, vecDim)
	hits, err := ix.SearchSummaries(e.ctx, "local", "  ", 10, store.SearchFilters{})
	require.NoError(t, err)
	assert.Empty(t, hits, "a blank query reads nothing")
	assert.True(t, strings.Contains(summariesBM25, "0.5"), "every column is weighted: %s", summariesBM25)
}
