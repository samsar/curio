package ask

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/textutil"
)

// chunksOf gives r's stores doc's chunks, by ID, each at its position,
// embedded with context, and ranks them by the question in rank's order.
func (r *rig) chunksOf(doc string, texts []string, contexts []string, rank ...int) {
	if r.stores.chunks == nil {
		r.stores.chunks, r.stores.ranked = map[string]*store.Chunk{}, map[string][]string{}
	}
	for i, text := range texts {
		id := fmt.Sprintf("%s-%d", doc, i)
		r.stores.chunks[id] = &store.Chunk{ID: id, DocumentID: doc, Ord: i, Text: text, Context: contexts[i]}
	}
	for _, i := range rank {
		r.stores.ranked[doc] = append(r.stores.ranked[doc], fmt.Sprintf("%s-%d", doc, i))
	}
}

// TestPassages_ByTheQuestion: a source's passages are its chunks nearest
// the question's vector, not the search's, shown in their page's order,
// each under its section when its context names one; a hit the document
// lists alone found gets passages too.
func TestPassages_ByTheQuestion(t *testing.T) {
	r := newRig(t, nil)
	title := "Kafka consumer groups"
	ctx := title + " — example.com"
	r.ret.got.Result = search.Result{Vector: []float32{0.1, 0.2}, Items: []search.Hit{
		hit("d1", "https://example.com/kafka", title, store.DocStateFetched, "the search's own chunk"),
		{Document: &store.Document{ID: "d2", URL: "https://example.org/listed", Title: new("Found by its title"),
			State: store.DocStateFetched}},
	}}
	r.chunksOf("d1", []string{"Intro text.", "Heartbeats keep members alive.", "Rebalances move partitions."},
		[]string{ctx, ctx + " — Setup › Heartbeats", ctx}, 2, 1)
	r.chunksOf("d2", []string{"A page the cards found."}, []string{""}, 0)

	got := retrievedOf(t, r.ask(t, Request{Question: "how do rebalances move partitions"}))
	require.Len(t, got.Sources, 2)
	assert.Equal(t, []Passage{{Text: "Heartbeats keep members alive.", Section: "Setup › Heartbeats"},
		{Text: "Rebalances move partitions."}}, got.Sources[0].Passages, "the page's order, the search's chunk left")
	assert.Equal(t, []Passage{{Text: "A page the cards found."}}, got.Sources[1].Passages)
	require.Len(t, r.stores.rankAsked, 1)
	assert.Equal(t, []float32{0.1, 0.2}, r.stores.rankAsked[0], "the search's own vector, no second embed")
}

// TestPassages_Fallback: without a vector, or when the ranking fails,
// the passages are the search's chunks, in its order, the failure warned
// of once without what was asked.
func TestPassages_Fallback(t *testing.T) {
	r := newRig(t, nil)
	r.chunksOf("d1", []string{"ranked first"}, []string{""}, 0)
	got := retrievedOf(t, r.ask(t, Request{Question: "q"}))
	assert.Equal(t, []Passage{{Text: "A consumer group shares a topic's partitions among its members."}},
		got.Sources[0].Passages, "no vector: the search's chunk")
	assert.Empty(t, r.stores.rankAsked)

	r.ret.got.Result.Vector = []float32{1}
	r.stores.rankErr = fmt.Errorf("rank: %w", store.ErrVectorIndexInconsistent)
	for range 2 {
		got = retrievedOf(t, r.ask(t, Request{Question: "how do consumer groups share partitions"}))
		assert.Equal(t, []Passage{{Text: "A consumer group shares a topic's partitions among its members."}},
			got.Sources[0].Passages)
	}
	warns := 0
	for _, rec := range r.records(t) {
		if rec["level"] == "WARN" {
			warns++
			assert.NotContains(t, fmt.Sprint(rec), "partitions")
		}
	}
	assert.Equal(t, 1, warns)
}

// TestPassages_Bound: documents holding more chunks than one ranking
// takes are ranked by nobody (RankChunks answers nil), and keep the
// search's chunks.
func TestPassages_Bound(t *testing.T) {
	r := newRig(t, nil)
	r.ret.got.Result.Vector = []float32{1}
	r.stores.chunks, r.stores.ranked = map[string]*store.Chunk{}, map[string][]string{}
	got := retrievedOf(t, r.ask(t, Request{Question: "q"}))
	assert.Equal(t, []Passage{{Text: "A consumer group shares a topic's partitions among its members."}},
		got.Sources[0].Passages)
	assert.Len(t, r.stores.rankAsked, 1)
}

// TestFitParagraphs: a chunk too long for what is left is fitted by its
// paragraphs that hold the most of the question's words, then by their
// place, joined in their order with gaps marked; a paragraph too long is
// cut from its start.
func TestFitParagraphs(t *testing.T) {
	text := "Carried over from the chunk before, about something else.\n\n" +
		"Raft elects a leader by votes.\n\n" +
		"Unrelated filler that goes on.\n  \n" +
		"A leader steps down after missed heartbeats; Raft then elects another."
	words := questionWords([]string{"how does raft elect a leader"})
	assert.Equal(t, map[string]bool{"raft": true, "elect": true, "leader": true}, words)

	got := fitParagraphs(text, 4*90, words)
	assert.Equal(t, "Raft elects a leader by votes."+gapMark+
		"A leader steps down after missed heartbeats; Raft then elects another.", got)

	got = fitParagraphs(text, 4*40, words)
	first, cut, ok := strings.Cut(got, gapMark)
	require.True(t, ok, got)
	assert.Equal(t, "Raft elects a leader by votes.", first, "the best paragraph whole")
	next := "A leader steps down after missed heartbeats; Raft then elects another."
	assert.Equal(t, next[:len(cut)], cut, "the next best cut from its start")
	assert.LessOrEqual(t, textutil.TokenQuarters(got), 4*40)

	got = fitParagraphs(text, 4*60, nil)
	first, cut, ok = strings.Cut(got, ". ")
	require.True(t, ok, got)
	assert.Equal(t, "Carried over from the chunk before, about something else", first, "no words: by place")
	neighbour := "Raft elects a leader by votes."
	assert.Equal(t, neighbour[:len(cut)], cut, "a neighbour joined by a space, cut from its start")
	assert.Empty(t, fitParagraphs(text, 0, words))
}

// TestPassages_BracketsWhole: a passage whose brackets grow as they are
// neutralized ('<' to '‹', three bytes) is shown whole while it fits, and
// so is each paragraph fitted from one.
func TestPassages_BracketsWhole(t *testing.T) {
	text := "<p>" + strings.Repeat("tide ", 43) + "</p>"
	require.Len(t, text, 222)
	_, shown, _ := fitPassages([]candidate{{text: text}}, 1<<20, nil)
	require.Len(t, shown, 1)
	assert.Equal(t, textutil.NeutralizeWhole(text), shown[0].Text)
	assert.True(t, strings.HasSuffix(shown[0].Text, "tide ‹/p›"))

	para := "<b>" + strings.Repeat("moon ", 30) + "</b>"
	got := fitParagraphs("an opening line\n\n"+para, 1<<20, map[string]bool{"moon": true})
	assert.True(t, strings.HasSuffix(got, "moon ‹/b›"), got)
}

// TestQuestionWords: words of three runes or more, or holding a digit,
// lowercased, less the stopwords, from the question and the plan's
// rewrites and names.
func TestQuestionWords(t *testing.T) {
	assert.Equal(t, map[string]bool{"read": true, "raft": true, "2021": true, "x9": true, "kafka": true,
		"élection": true},
		questionWords([]string{"What did I read on Raft in 2021, x9?", "Kafka", "an ÉLECTION"}))
}
