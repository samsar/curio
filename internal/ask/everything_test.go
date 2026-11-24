package ask

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
)

// goldenListPrompt is the list prompt for made-up groups.
func goldenListPrompt() string {
	groups := []Group{
		{Marker: "G1", InterestID: "in-bread", Label: "Bread", AreaLabel: "Cooking", Count: 2, Documents: []GroupDocument{
			{DocID: "a", Title: "Sourdough starters", About: "How to keep a starter alive."},
			{DocID: "b", Title: "Rye loaves"}}},
		{Marker: "G2", Label: "", Count: 1, Documents: []GroupDocument{{DocID: "c", Title: "A stray page"}}},
	}
	material, _ := renderGroups(groups, budget("everything I saved about bread", listInstructionTokens, 768))
	return fill(listTemplate, material, "everything I saved about bread", goldenDay, time.UTC)
}

// groupsOf is the Groups event.
func groupsOf(t *testing.T, events []Event) Groups {
	t.Helper()
	for _, e := range events {
		if g, ok := e.(Groups); ok {
			return g
		}
	}
	t.Fatal("no Groups event")
	return Groups{}
}

// listed gives r a ranking of seven pages, words matched, and a rebuild
// that put them in two interests, Unsorted, one placed since, one outside
// it.
func (r *rig) listed() {
	r.ret.got.Result = search.Result{BM25Hits: 3, Total: 40, Items: []search.Hit{
		hit("u1", "https://example.com/u1", "Loose end", store.DocStateFetched, "x"),
		hit("s1", "https://example.com/s1", "Soup one", store.DocStateFetched, "x"),
		hit("b1", "https://example.com/b1", "Bread one", store.DocStateFetched, "x"),
		hit("b2", "https://example.com/b2", "Bread two", store.DocStateDead),
		hit("s2", "https://example.com/s2", "Soup two", store.DocStateFetched, "x"),
		hit("out", "https://example.com/out", "Outside", store.DocStateFetched, "x"),
		hit("new", "https://example.com/new", "Placed since", store.DocStateFetched, "x"),
	}}
	r.interests.runs = []*store.InterestRun{{ID: "run-1", Shape: store.InterestShapeAreas, FinishedAt: &finishedAt}}
	r.interests.places = map[string]map[string]store.DocumentInterest{"run-1": {
		"s1":  {InterestID: "in-soup", AreaID: "area-cook"},
		"b1":  {InterestID: "in-bread", AreaID: "area-cook"},
		"b2":  {InterestID: "in-bread", AreaID: "area-cook"},
		"s2":  {InterestID: "in-soup", AreaID: "area-cook"},
		"new": {InterestID: "in-bread", AreaID: "area-cook"},
	}}
	r.interests.labels = map[string]string{"area-cook": "Cooking", "in-bread": "Bread", "in-soup": "Soup"}
	r.stores.summaries = map[string]*store.Summary{"b1": {About: "A loaf."}}
	untitled := ""
	r.ret.got.Result.Items[0].Document.Title = &untitled
	r.stores.bookmarks = map[string][]*store.Bookmark{"u1": {
		{ID: "bm-1", Title: new("Older title"), SavedAt: finishedAt.AddDate(-1, 0, 0)},
		{ID: "bm-2", Title: new("Newer title"), SavedAt: finishedAt}}}
}

// TestList: the ranking's pages grouped by interest, the most first (ties
// by the best rank), Unsorted and pages outside the rebuild last, a page
// placed since under its placement's interest, dead pages listed by state;
// the answer cites groups; the events in their order.
func TestList(t *testing.T) {
	r := newRig(t, nil)
	r.listed()
	r.writer.answer = answers("Bread and soup [G1][G2]; a stray [G9].")
	events := r.ask(t, Request{Question: "everything I saved about cooking"})
	assert.Equal(t, []string{"progress retrieving", "retrieval", "groups", "progress writing", "delta", "done"},
		order(events))
	require.Len(t, r.ret.queries, 1)
	assert.Equal(t, listMax, r.ret.queries[0].K)
	assert.True(t, r.ret.queries[0].Rerank, "ask.rerank")
	assert.Empty(t, retrievedOf(t, events).Sources)

	g := groupsOf(t, events)
	assert.Equal(t, 7, g.Listed)
	assert.Equal(t, 40, g.Ranked)
	assert.False(t, g.Wordless)
	require.Len(t, g.Groups, 3)
	bread, soup, rest := g.Groups[0], g.Groups[1], g.Groups[2]
	assert.Equal(t, "G1", bread.Marker)
	assert.Equal(t, "Bread", bread.Label)
	assert.Equal(t, "Cooking", bread.AreaLabel)
	assert.Equal(t, 3, bread.Count, "one of them placed since")
	assert.Equal(t, []string{"b1", "b2", "new"}, docIDs(bread))
	assert.Equal(t, store.DocStateDead, bread.Documents[1].State)
	assert.Equal(t, "A loaf.", bread.Documents[0].About)
	assert.Equal(t, "G2", soup.Marker)
	assert.Equal(t, []string{"s1", "s2"}, docIDs(soup))
	assert.Equal(t, "G3", rest.Marker)
	assert.Empty(t, rest.InterestID)
	assert.Equal(t, []string{"u1", "out"}, docIDs(rest), "Unsorted and outside the rebuild, last")
	assert.Equal(t, 3, g.Shown)
	assert.Equal(t, Names{OwnTitle: "Bread one"}, bread.Documents[0].Names)
	assert.Equal(t, Names{SavedTitle: "Newer title"}, rest.Documents[0].Names, "named by its newest save")
	assert.Equal(t, 1, r.stores.bookmarkReads, "the listed pages' bookmarks read once")

	asks := r.writer.asks()
	require.Len(t, asks, 1)
	assert.Contains(t, asks[0], "[G1] Bread (in Cooking): 3 pages\n- Bread one — A loaf.\n- Bread two\n- Placed since")
	d := doneOf(t, events)
	assert.Equal(t, RouteList, d.Route)
	assert.Equal(t, []string{"G1", "G2"}, d.Markers)
	assert.Equal(t, []CitedAt{{Start: 16, End: 18, Marker: "G1"}, {Start: 20, End: 22, Marker: "G2"}}, d.CitedAt)
	assert.Equal(t, []string{"G9"}, d.InvalidCitations)
	assert.Positive(t, d.Timings.Sources, "reading the groups")
}

// docIDs are a group's documents' IDs.
func docIDs(g Group) []string {
	out := make([]string, len(g.Documents))
	for i, d := range g.Documents {
		out[i] = d.DocID
	}
	return out
}

// TestList_Wordless: a ranking by meaning alone lists its first
// listWordless pages, and says so.
func TestList_Wordless(t *testing.T) {
	r := newRig(t, nil)
	hits := make([]search.Hit, 0, 30)
	for i := range 30 {
		hits = append(hits, hit(fmt.Sprint("d", i), fmt.Sprintf("https://example.com/%d", i), "x",
			store.DocStateFetched, "x"))
	}
	r.ret.got.Result = search.Result{Total: 100, Items: hits}
	events := r.ask(t, Request{Question: "q", Mode: RouteList})
	g := groupsOf(t, events)
	assert.True(t, g.Wordless)
	assert.Equal(t, listWordless, g.Listed)
}

// TestList_NoRun: before any rebuild every page is one group, not
// grouped yet.
func TestList_NoRun(t *testing.T) {
	r := newRig(t, nil)
	r.listed()
	r.interests.runs = nil
	g := groupsOf(t, r.ask(t, Request{Question: "q", Mode: RouteList}))
	require.Len(t, g.Groups, 1)
	assert.Equal(t, "not grouped yet", g.Groups[0].Label)
	assert.Equal(t, 7, g.Groups[0].Count)
}

// TestList_RunChangesMidRead: a rebuild that commits between the run
// read and the places read is caught, the newer run's places used.
func TestList_RunChangesMidRead(t *testing.T) {
	r := newRig(t, nil)
	r.listed()
	newer := &store.InterestRun{ID: "run-2", Shape: store.InterestShapeFlat, FinishedAt: &finishedAt}
	r.interests.runs = []*store.InterestRun{r.interests.runs[0], newer}
	r.interests.places["run-2"] = map[string]store.DocumentInterest{"out": {InterestID: "in-soup"}}
	g := groupsOf(t, r.ask(t, Request{Question: "q", Mode: RouteList}))
	require.Len(t, g.Groups, 2)
	assert.Equal(t, []string{"out"}, docIDs(g.Groups[0]), "the newer run's places")
}

// TestList_Fits: fifty groups of 200-byte titles and 300-byte about
// lines fit the context at the longest answer and question, whole groups
// in order, and the groups shown are counted.
func TestList_Fits(t *testing.T) {
	groups := make([]Group, 0, listMax)
	for i := range listMax {
		g := Group{Marker: fmt.Sprint("G", i+1), InterestID: fmt.Sprint("i", i), Label: hostile(200),
			AreaLabel: hostile(200), Count: 1}
		for range listTitles + 1 {
			g.Documents = append(g.Documents, GroupDocument{Title: hostile(200), About: hostile(300)})
		}
		groups = append(groups, g)
	}
	question := strings.Repeat("9", MaxQuestionBytes)
	material, shown := renderGroups(groups, budget(question, listInstructionTokens, config.MaxAskTokens))
	prompt := fill(listTemplate, material, question, goldenDay, time.UTC)
	fits(t, listTemplate, prompt, listInstructionTokens, config.MaxAskTokens, "list")
	assert.Positive(t, shown)
	assert.Less(t, shown, listMax)
	assert.Equal(t, shown, strings.Count(material, "\n\n")+1)
}

// TestList_LogsNoContent: no record of a list ask carries the question, a
// label or the answer.
func TestList_LogsNoContent(t *testing.T) {
	r := newRig(t, nil)
	r.listed()
	r.writer.answer = answers("Bread [G1].")
	r.ask(t, Request{Question: "everything I saved about cooking"})
	for _, secret := range []string{"saved about", "Bread", "Cooking", "Loose end"} {
		assert.NotContains(t, r.logs.String(), secret)
	}
}
