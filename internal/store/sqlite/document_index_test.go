package sqlite

import (
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

func hitIDs(hits []store.DocumentHit) []string {
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.DocumentID
	}
	return ids
}

// TestDocumentIndex_SearchFields: the fields find the tenant's documents
// of every state by what they were saved as, scoped by the filters, best
// first, equal matches in the order their fields were first written.
func TestDocumentIndex_SearchFields(t *testing.T) {
	e := newFieldsEnv(t)
	ix := NewDocumentIndex(e.db, vecDim)
	byState := map[store.DocState]string{}
	for _, st := range []store.DocState{store.DocStateFetched, store.DocStatePending, store.DocStateFailed,
		store.DocStateDead} {
		b := e.ingest("https://example.com/"+string(st), store.SourceChrome, "Kafka consumer groups", "")
		id := *b.DocumentID
		if cause := stateCause(st); cause != "" {
			require.NoError(t, e.docs.MarkFailed(e.ctx, id, cause))
		} else if st == store.DocStateFetched {
			require.NoError(t, e.docs.MarkFetched(e.ctx, id, 0))
		}
		byState[st] = id
	}
	other := &store.Document{TenantID: "other", URL: "https://example.com/theirs", Title: store.NullableString("Kafka")}
	require.NoError(t, e.docs.Create(e.ctx, other))

	hits, err := ix.SearchFields(e.ctx, "local", `"kafka"`, 10, store.SearchFilters{})
	require.NoError(t, err)
	assert.Equal(t, []string{byState[store.DocStateFetched], byState[store.DocStatePending],
		byState[store.DocStateFailed], byState[store.DocStateDead]}, hitIDs(hits),
		"every state, equal matches in the order their fields were written, never another tenant's")
	for _, h := range hits {
		assert.Positive(t, h.Score)
	}

	folder := e.ingest("https://other.example/x", store.SourceSafari, "Unrelated", "/Favorites/Kafka")
	titled := &store.Document{TenantID: "local", URL: "https://example.net/kafka", Title: store.NullableString("Kafka")}
	require.NoError(t, e.docs.Create(e.ctx, titled))
	hits, err = ix.SearchFields(e.ctx, "local", `"kafka"`, 10, store.SearchFilters{})
	require.NoError(t, err)
	require.Len(t, hits, 6)
	assert.Equal(t, titled.ID, hits[0].DocumentID, "a one-word title matches best")
	assert.Equal(t, *folder.DocumentID, hits[5].DocumentID, "a folder counts half")

	for name, tc := range map[string]struct {
		f    store.SearchFilters
		want []string
	}{
		"host":   {store.SearchFilters{Host: []string{"other.example"}}, []string{*folder.DocumentID}},
		"source": {store.SearchFilters{Source: []string{store.SourceSafari}}, []string{*folder.DocumentID}},
		"type":   {store.SearchFilters{ContentType: []string{string(store.ContentTypeVideo)}}, nil},
		"excluded": {store.SearchFilters{Host: []string{"example.net", "other.example"},
			ExcludeDocumentID: titled.ID}, []string{*folder.DocumentID}},
	} {
		hits, err := ix.SearchFields(e.ctx, "local", `"kafka"`, 10, tc.f)
		require.NoError(t, err, name)
		assert.Equal(t, tc.want, nilIfEmpty(hitIDs(hits)), name)
	}

	hits, err = ix.SearchFields(e.ctx, "local", `"kafka"`, 2, store.SearchFilters{})
	require.NoError(t, err)
	assert.Len(t, hits, 2, "at most the limit")
	hits, err = ix.SearchFields(e.ctx, "local", "  ", 10, store.SearchFilters{})
	require.NoError(t, err)
	assert.Empty(t, hits, "a blank query matches nothing")
}

// TestDocumentIndex_SearchTitles: the title list finds the tenant's
// documents of every state whose title and bookmark titles hold every
// phrase of the query between them, never by a word only their folders,
// site or author hold; scoped by the filters, best first, equal matches
// in the order their fields were first written.
func TestDocumentIndex_SearchTitles(t *testing.T) {
	e := newFieldsEnv(t)
	ix := NewDocumentIndex(e.db, vecDim)
	const query = `"kafka" AND "retention"`
	search := func(t *testing.T, query string, limit int, f store.SearchFilters) []string {
		t.Helper()
		hits, err := ix.SearchTitles(e.ctx, "local", query, limit, f)
		require.NoError(t, err)
		for _, h := range hits {
			assert.Positive(t, h.Score)
		}
		return nilIfEmpty(hitIDs(hits))
	}

	states := []store.DocState{store.DocStateFetched, store.DocStatePending, store.DocStateFailed, store.DocStateDead}
	byState := make([]string, 0, len(states))
	for _, st := range states {
		id := *e.ingest("https://example.com/"+string(st), store.SourceChrome, "Kafka retention tuning", "").DocumentID
		if cause := stateCause(st); cause != "" {
			require.NoError(t, e.docs.MarkFailed(e.ctx, id, cause))
		} else if st == store.DocStateFetched {
			require.NoError(t, e.docs.MarkFetched(e.ctx, id, 0))
		}
		byState = append(byState, id)
	}
	split := &store.Document{TenantID: "local", URL: "https://example.com/split", Title: store.NullableString("Kafka")}
	require.NoError(t, e.docs.Create(e.ctx, split))
	e.ingest(split.URL, store.SourceSafari, "Retention", "")
	for _, url := range []string{"https://example.com/partial", "https://example.com/folder",
		"https://retention.example/site", "https://example.com/author"} {
		e.ingest(url, store.SourceChrome, "Kafka streams", "")
	}
	e.ingest("https://example.com/folder", store.SourceFirefox, "Kafka streams", "/Bookmarks Bar/Retention")
	authored := *e.ingest("https://example.com/author", store.SourceFirefox, "Kafka streams", "").DocumentID
	e.fetch(authored, "Kafka streams", "Retention Smith", "")
	theirs := &store.Document{TenantID: "other", URL: "https://example.com/theirs",
		Title: store.NullableString("Kafka retention")}
	require.NoError(t, e.docs.Create(e.ctx, theirs))

	assert.Equal(t, append([]string{split.ID}, byState...), search(t, query, 10, store.SearchFilters{}),
		"the words split between the title and a bookmark's best, as the shorter row; then every state, in the "+
			"order their fields were written; never a document missing a word in its titles, nor another tenant's")
	assert.Equal(t, []string{split.ID, byState[0]}, search(t, query, 2, store.SearchFilters{}), "at most the limit")

	video := &store.Document{TenantID: "local", URL: "https://other.example/talk", ContentType: store.ContentTypeVideo,
		Title: store.NullableString("Kafka retention, the talk")}
	require.NoError(t, e.docs.Create(e.ctx, video))
	for name, tc := range map[string]struct {
		f    store.SearchFilters
		want []string
	}{
		"host":   {store.SearchFilters{Host: []string{"other.example"}}, []string{video.ID}},
		"type":   {store.SearchFilters{ContentType: []string{string(store.ContentTypeVideo)}}, []string{video.ID}},
		"source": {store.SearchFilters{Source: []string{store.SourceSafari}}, []string{split.ID}},
	} {
		assert.Equal(t, tc.want, search(t, query, 10, tc.f), name)
	}

	retitled := &store.Document{TenantID: "local", URL: "https://example.org/retitled",
		Title: store.NullableString("Kafka retention basics")}
	require.NoError(t, e.docs.Create(e.ctx, retitled))
	assert.Contains(t, search(t, query, 10, store.SearchFilters{}), retitled.ID)
	e.fetch(retitled.ID, "Stream compaction guide", "", "")
	assert.NotContains(t, search(t, query, 10, store.SearchFilters{}), retitled.ID,
		"its old title, which no bookmark holds, is gone with the fetch")
	assert.Equal(t, []string{retitled.ID}, search(t, `"stream" AND "compaction"`, 10, store.SearchFilters{}))

	assert.Nil(t, search(t, "  ", 10, store.SearchFilters{}), "a blank query reads nothing")
	assert.Nil(t, search(t, query, 0, store.SearchFilters{}), "nor does a limit of 0")
}

func nilIfEmpty(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	return ids
}

// TestDocumentIndex_SearchCards: the nearest card vectors' documents, of
// every state and the tenant's alone, nearest first and equal distances
// by document ID; filters apply past the over-fetch.
func TestDocumentIndex_SearchCards(t *testing.T) {
	e := newFieldsEnv(t)
	ix := NewDocumentIndex(e.db, vecDim)
	var cards []store.CardVector
	put := func(url string, v float32, source string) string {
		t.Helper()
		id := *e.ingest(url, source, "Card of "+url, "").DocumentID
		cards = append(cards, store.CardVector{DocumentID: id, Text: e.card(id), Embedding: fillVec(v)})
		return id
	}
	near := put("https://example.com/near", 0.10, store.SourceChrome)
	twinA := put("https://example.com/twin-a", 0.20, store.SourceChrome)
	twinB := put("https://example.com/twin-b", 0.20, store.SourceChrome)
	far := put("https://example.org/far", 0.50, store.SourceSafari)
	require.NoError(t, e.docs.MarkFailed(e.ctx, far, store.FailureCauseDeadLink))
	written, err := ix.PutCardVectors(e.ctx, cards)
	require.NoError(t, err)
	require.Equal(t, 4, written)
	theirs := &store.Document{TenantID: "other", URL: "https://example.com/theirs"}
	require.NoError(t, e.docs.Create(e.ctx, theirs))
	_, err = ix.PutCardVectors(e.ctx, []store.CardVector{{DocumentID: theirs.ID, Text: e.card(theirs.ID),
		Embedding: fillVec(0.10)}})
	require.NoError(t, err)

	twins := []string{twinA, twinB}
	slices.Sort(twins)
	for range 3 {
		hits, err := ix.SearchCards(e.ctx, "local", fillVec(0.1), 10, store.SearchFilters{})
		require.NoError(t, err)
		assert.Equal(t, []string{near, twins[0], twins[1], far}, hitIDs(hits),
			"nearest first, twins by document ID, the dead one too, never another tenant's")
		assert.InDelta(t, 1.0, hits[0].Score, 1e-6)
		assert.Greater(t, hits[1].Score, hits[3].Score)
	}
	hits, err := ix.SearchCards(e.ctx, "local", fillVec(0.1), 2, store.SearchFilters{})
	require.NoError(t, err)
	assert.Equal(t, []string{near, twins[0]}, hitIDs(hits))
	hits, err = ix.SearchCards(e.ctx, "local", fillVec(0.1), 1, store.SearchFilters{Host: []string{"example.org"}})
	require.NoError(t, err)
	assert.Equal(t, []string{far}, hitIDs(hits), "the farthest, past three nearer ones the filter drops")
	hits, err = ix.SearchCards(e.ctx, "local", fillVec(0.1), 10, store.SearchFilters{Source: []string{store.SourceSafari}})
	require.NoError(t, err)
	assert.Equal(t, []string{far}, hitIDs(hits))

	_, err = ix.SearchCards(e.ctx, "local", make([]float32, 3), 10, store.SearchFilters{})
	assert.ErrorContains(t, err, "embedding length 3")
}

// TestDocumentIndex_CardVectors: StaleCards pages through the stale cards
// by document; PutCardVectors stores a vector only for a card still
// holding the text embedded, so one that changed meanwhile stays stale;
// ResetCards makes every card stale and queues the job, keeping the old
// vectors searchable.
func TestDocumentIndex_CardVectors(t *testing.T) {
	e := newFieldsEnv(t)
	ix := NewDocumentIndex(e.db, vecDim)
	ids := make([]string, 0, 5)
	for i := range 5 {
		ids = append(ids, *e.ingest(fmt.Sprintf("https://example.com/%d", i), store.SourceChrome,
			fmt.Sprint("Title ", i), "").DocumentID)
	}
	slices.Sort(ids)
	theirs := &store.Document{TenantID: "other", URL: "https://example.com/theirs"}
	require.NoError(t, e.docs.Create(e.ctx, theirs))

	var read []store.Card
	for after := ""; ; {
		page, err := ix.StaleCards(e.ctx, "local", after, 2)
		require.NoError(t, err)
		if len(page) == 0 {
			break
		}
		read = append(read, page...)
		after = page[len(page)-1].DocumentID
	}
	require.Len(t, read, 5, "the tenant's cards alone")
	vectors := make([]store.CardVector, len(read))
	for i, c := range read {
		assert.Equal(t, ids[i], c.DocumentID, "in document order")
		assert.Equal(t, e.card(c.DocumentID), c.Text)
		vectors[i] = store.CardVector{DocumentID: c.DocumentID, Text: c.Text, Embedding: fillVec(float32(i) / 10)}
	}

	// A card that changes between the read and the write keeps no vector
	// of its old text.
	changed := read[1].DocumentID
	e.fetch(changed, "A new title", "", "")
	written, err := ix.PutCardVectors(e.ctx, vectors)
	require.NoError(t, err)
	assert.Equal(t, 4, written)
	stale, err := ix.StaleCards(e.ctx, "local", "", 10)
	require.NoError(t, err)
	assert.Equal(t, []store.Card{{DocumentID: changed, Text: e.card(changed)}}, stale)
	assert.Equal(t, 4, countRows(t, e.db, "document_cards_vec"))

	_, err = ix.PutCardVectors(e.ctx, []store.CardVector{{DocumentID: changed, Text: e.card(changed),
		Embedding: make([]float32, 3)}})
	assert.ErrorContains(t, err, "embedding length 3")
	written, err = ix.PutCardVectors(e.ctx, nil)
	require.NoError(t, err)
	assert.Zero(t, written)

	_, err = e.db.Exec(`DELETE FROM jobs WHERE kind = ?`, store.JobKindCards)
	require.NoError(t, err)
	n, err := ix.ResetCards(e.ctx, "local")
	require.NoError(t, err)
	assert.Equal(t, 4, n, "the embedded cards; the stale one was already")
	stale, err = ix.StaleCards(e.ctx, "local", "", 10)
	require.NoError(t, err)
	assert.Len(t, stale, 5)
	assert.Equal(t, 1, e.pendingCardsJobs())
	hits, err := ix.SearchCards(e.ctx, "local", fillVec(0), 10, store.SearchFilters{})
	require.NoError(t, err)
	assert.Len(t, hits, 4, "the old vectors stay searchable until embedded again")
	theirStale, err := ix.StaleCards(e.ctx, "other", "", 10)
	require.NoError(t, err)
	assert.Len(t, theirStale, 1, "another tenant's cards are theirs")
}
