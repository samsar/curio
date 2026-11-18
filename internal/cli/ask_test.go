package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/api/apitest"
	"github.com/samsar/curio/internal/ask"
	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/modelgate"
	"github.com/samsar/curio/internal/rerank"
	"github.com/samsar/curio/internal/retrieve"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/understand"
)

// answerEvents are an ask's stream: the model loading, an understood
// search, two sources and an unreadable page, an answer citing one source
// and one it wasn't given, and done with status.
func answerEvents(status ask.Status, reason string) []ask.Event {
	return []ask.Event{
		ask.Progress{Stage: ask.StageLoading, Model: "gemma4:26b"},
		ask.Progress{Stage: ask.StageLoading, Model: "gemma4:26b"},
		ask.Progress{Stage: ask.StageRetrieving},
		ask.Retrieved{Route: ask.RouteAnswer,
			Understanding: &retrieve.Understanding{Status: understand.StatusPlanned, Model: "gemma4:26b",
				Took: 1200 * time.Millisecond, Plan: &understand.Plan{Kind: understand.KindEvidence}},
			Sources: []ask.Source{
				{N: 1, DocID: "11111111-1111-4111-8111-111111111111", URL: "https://example.com/a", Title: "Bread"},
				{N: 2, DocID: "22222222-2222-4222-8222-222222222222", URL: "https://example.org/b", Title: "Rye"},
			},
			Unreadable: []ask.Unreadable{{DocID: "33333333-3333-4333-8333-333333333333",
				URL: "https://example.net/c", Title: "Starter", State: store.DocStateDead,
				Reason: "the page is no longer available"}}},
		ask.Progress{Stage: ask.StageWriting, Model: "gemma4:26b"},
		ask.Delta{Text: "Let it cool "},
		ask.Delta{Text: "for an hour [1][9]."},
		ask.Done{Status: status, Reason: reason, Model: "gemma4:26b", PromptVersion: ask.PromptVersion,
			Route: ask.RouteAnswer, Citations: []int{1}, InvalidCitations: []string{"9"}},
	}
}

// TestAsk_Output: the header, the answer as it streamed, then the
// sources, the cited one marked, and the unreadable page, on stdout;
// what the ask waited on and the notes on stderr, each once. The
// request carries the flags.
func TestAsk_Output(t *testing.T) {
	asker := apitest.NewAsker(answerEvents(ask.StatusAnswered, "")...)
	w := upWorld(t, apitest.WithAsker(asker))
	code, out, errOut := w.exit(t, "ask", "--sources", "4", "--site", "example.com", "how", "long", "to", "cool")
	require.Equal(t, 0, code, errOut)
	assert.Equal(t, "understood (gemma4:26b, 1.2 s): evidence\n\n"+
		"Let it cool for an hour [1][9].\n\n"+
		"Sources:\n"+
		"[1] Bread (cited)\n    https://example.com/a\n    doc_id: 11111111-1111-4111-8111-111111111111\n"+
		"[2] Rye\n    https://example.org/b\n    doc_id: 22222222-2222-4222-8222-222222222222\n\n"+
		"Saved, but curio couldn't read:\n"+
		"- Starter (the page is no longer available)\n    https://example.net/c\n"+
		"    doc_id: 33333333-3333-4333-8333-333333333333\n", out)
	assert.Equal(t, "loading gemma4:26b…\n"+
		"note: the answer cites [9], which it wasn't given: nothing backs those claims\n", errOut)

	req := asker.Requests()[0]
	assert.Equal(t, "how long to cool", req.Question)
	assert.True(t, req.Understand)
	assert.Nil(t, req.Rerank, "left to the daemon's ask.rerank")
	assert.Equal(t, 4, req.Sources)
	assert.Equal(t, []string{"example.com"}, req.Filters.Site)

	_, _, _ = w.exit(t, "ask", "--literal", "q")
	assert.False(t, asker.Requests()[1].Understand)
	assert.Equal(t, new(false), asker.Requests()[1].Rerank)
	_, _, _ = w.exit(t, "ask", "--no-rerank", "q")
	assert.True(t, asker.Requests()[2].Understand)
	assert.Equal(t, new(false), asker.Requests()[2].Rerank)
	_, _, _ = w.exit(t, "ask", "--rerank", "--mode", "list", "q")
	assert.True(t, asker.Requests()[3].Understand)
	assert.Equal(t, new(true), asker.Requests()[3].Rerank)
	assert.Equal(t, ask.RouteList, asker.Requests()[3].Mode)
}

// TestAsk_RerankFlagValues: a rerank flag given a value says what the
// value says, false included; only none of them leaves the rerank to the
// daemon's ask.rerank.
func TestAsk_RerankFlagValues(t *testing.T) {
	asker := apitest.NewAsker(answerEvents(ask.StatusAnswered, "")...)
	w := upWorld(t, apitest.WithAsker(asker))
	for i, c := range []struct {
		flag       string
		understand bool
		rerank     *bool
	}{
		{"--rerank=true", true, new(true)},
		{"--rerank=false", true, new(false)},
		{"--no-rerank=true", true, new(false)},
		{"--no-rerank=false", true, new(true)},
		{"--literal=true", false, new(false)},
		{"--literal=false", true, nil},
	} {
		code, _, errOut := w.exit(t, "ask", c.flag, "q")
		require.Equal(t, 0, code, "%s: %s", c.flag, errOut)
		req := asker.Requests()[i]
		assert.Equal(t, c.understand, req.Understand, c.flag)
		assert.Equal(t, c.rerank, req.Rerank, c.flag)
	}
}

// TestAsk_RerankFlagsExclusive: --rerank, --no-rerank and --literal are
// one choice: two of them are a usage error naming them, and nothing is
// asked.
func TestAsk_RerankFlagsExclusive(t *testing.T) {
	asker := apitest.NewAsker(answerEvents(ask.StatusAnswered, "")...)
	w := upWorld(t, apitest.WithAsker(asker))
	for _, pair := range [][2]string{{"--rerank", "--no-rerank"}, {"--rerank", "--literal"},
		{"--no-rerank", "--literal"}} {
		code, _, errOut := w.exit(t, "ask", pair[0], pair[1], "q")
		assert.NotEqual(t, 0, code, pair)
		assert.Contains(t, errOut, strings.TrimPrefix(pair[0], "--"), pair)
		assert.Contains(t, errOut, strings.TrimPrefix(pair[1], "--"), pair)
	}
	assert.Empty(t, asker.Requests())
}

// TestAsk_FallbacksAreNotes: the planner's and the reranker's fallbacks
// are notes on stderr, never on stdout with the answer.
func TestAsk_FallbacksAreNotes(t *testing.T) {
	asker := apitest.NewAsker(
		ask.Retrieved{Route: ask.RouteAnswer,
			Understanding: &retrieve.Understanding{Status: understand.StatusTimeout,
				Reason: "the plan took longer than 8s\x1b[2J"},
			Reranking: &retrieve.Reranking{Status: rerank.StatusUnavailable, Reason: "the reranker is unavailable"},
			Sources: []ask.Source{{N: 1, DocID: "11111111-1111-4111-8111-111111111111",
				URL: "https://example.com/a", Title: "Bread"}}},
		ask.Delta{Text: "It cools [1]."},
		ask.Done{Status: ask.StatusAnswered, PromptVersion: ask.PromptVersion, Route: ask.RouteAnswer,
			Citations: []int{1}})
	w := upWorld(t, apitest.WithAsker(asker))
	code, out, errOut := w.exit(t, "ask", "q")
	require.Equal(t, 0, code, errOut)
	assert.True(t, strings.HasPrefix(out, "It cools [1]."), out)
	assert.NotContains(t, out, "searched your words")
	assert.NotContains(t, out, "kept the first ranking")
	assert.Contains(t, errOut, "note: searched your words: the plan took longer than 8s\\x1b[2J\n")
	assert.Contains(t, errOut, "note: kept the first ranking: the reranker is unavailable\n")
}

// TestAsk_Evidence: judging is said once on stderr; the sources are
// marked by the side they take, each with its quote.
func TestAsk_Evidence(t *testing.T) {
	asker := apitest.NewAsker(
		ask.Progress{Stage: ask.StageJudging, Model: "gemma4:26b", Pages: 12},
		ask.Progress{Stage: ask.StageJudging, Model: "gemma4:26b", Pages: 12},
		ask.Retrieved{Route: ask.RouteEvidence, Sources: []ask.Source{
			{N: 1, DocID: "11111111-1111-4111-8111-111111111111", URL: "https://example.com/a", Title: "For",
				Quote: "cool it\x1b[2J for an hour"},
			{N: 2, DocID: "22222222-2222-4222-8222-222222222222", URL: "https://example.org/b", Title: "Against",
				Quote: "slice it warm"}}},
		ask.Evidence{Status: ask.EvidenceJudged, Judged: 12, Supports: []int{1}, Contradicts: []int{2}},
		ask.Delta{Text: "One page says wait [1]; another says not [2]."},
		ask.Done{Status: ask.StatusAnswered, PromptVersion: ask.PromptVersion, Route: ask.RouteEvidence,
			Citations: []int{1, 2}})
	w := upWorld(t, apitest.WithAsker(asker))
	code, out, errOut := w.exit(t, "ask", "is it true that bread must cool")
	require.Equal(t, 0, code, errOut)
	assert.Equal(t, "judging 12 pages…\n", errOut)
	assert.Contains(t, out, "[1] For (cited) — supports\n    \u201ccool it\\x1b[2J for an hour\u201d\n")
	assert.Contains(t, out, "[2] Against (cited) — contradicts\n    \u201cslice it warm\u201d\n")
}

// TestAsk_JudgingNote: the judging note counts its pages, one in the
// singular.
func TestAsk_JudgingNote(t *testing.T) {
	for pages, want := range map[int]string{1: "judging 1 page…\n", 12: "judging 12 pages…\n"} {
		var errOut strings.Builder
		a := &textAsk{out: io.Discard, errOut: &errOut}
		a.progress(client.AskEvent{Type: "progress", Stage: "judging", Pages: pages})
		assert.Equal(t, want, errOut.String())
	}
}

// TestAsk_Table: an interests table is printed before the answer, a line
// a period with its total and its three largest areas, then the undated
// count; the rows the answer cites after it.
func TestAsk_Table(t *testing.T) {
	saves := []store.RunSave{
		{DocumentID: "d1", InterestID: "in-1", AreaID: "area-1", SavedAt: time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC)},
		{DocumentID: "d2", InterestID: "in-2", AreaID: "area-2", SavedAt: time.Date(2024, 1, 6, 0, 0, 0, 0, time.UTC)},
		{DocumentID: "d3", InterestID: "in-2", AreaID: "area-2", SavedAt: time.Date(2024, 5, 6, 0, 0, 0, 0, time.UTC)},
		{DocumentID: "d4"},
	}
	labels := map[string]string{"area-1": "Cooking\x1b[2J", "area-2": "Cycling"}
	timeline := insight.Timeline(saves, labels, insight.Window{}, insight.PeriodAuto, time.UTC)
	markers := []string{"T1", "T2", "T3", "T4"}
	asker := apitest.NewAsker(
		ask.Retrieved{Route: ask.RouteInterests},
		ask.Table{RunID: "44444444-4444-4444-8444-444444444444", FinishedAt: time.Now(),
			Shape: store.InterestShapeAreas, Timeline: timeline, Markers: markers, UndatedMarker: "T5", Shown: 5},
		ask.Delta{Text: "Cycling grew [T1]; one save is undated [T5]."},
		ask.Done{Status: ask.StatusAnswered, PromptVersion: ask.PromptVersion, Route: ask.RouteInterests,
			Markers: []string{"T1", "T5"}})
	w := upWorld(t, apitest.WithAsker(asker))
	code, out, errOut := w.exit(t, "ask", "how have my interests changed")
	require.Equal(t, 0, code, errOut)
	assert.Equal(t, "Saves by quarter:\n"+
		"  2024 Q1: 2 — Cycling 1, Cooking\\x1b[2J 1\n"+
		"  2024 Q2: 1 — Cycling 1\n"+
		"  undated: 1\n\n"+
		"Cycling grew [T1]; one save is undated [T5].\n\n"+
		"Rows cited:\n"+
		"[T1] Cycling: 2 saves\n"+
		"[T5] undated: 1 save\n", out)
}

// TestAsk_Groups: the list route's groups follow the answer, the cited
// ones marked, each document's title, URL and doc_id under it; an
// interest not named yet is unnamed, and only the pages in none are in no
// interest.
func TestAsk_Groups(t *testing.T) {
	asker := apitest.NewAsker(
		ask.Retrieved{Route: ask.RouteList},
		ask.Groups{Groups: []ask.Group{
			{Marker: "G1", InterestID: "in-1", Label: "Bread\x1b[2J", AreaLabel: "Cooking", Count: 1,
				Documents: []ask.GroupDocument{{DocID: "11111111-1111-4111-8111-111111111111",
					URL: "https://example.com/a", Title: "Sourdough", State: store.DocStateFetched}}},
			{Marker: "G2", InterestID: "in-2", Count: 1, Documents: []ask.GroupDocument{{
				DocID: "44444444-4444-4444-8444-444444444444", URL: "https://example.net/d", Title: "Rye",
				State: store.DocStateFetched}}},
			{Marker: "G3", Count: 1, Documents: []ask.GroupDocument{{DocID: "22222222-2222-4222-8222-222222222222",
				URL: "https://example.org/b", Title: "Stray", State: store.DocStateDead}}}},
			Listed: 3, Ranked: 9, Shown: 3},
		ask.Delta{Text: "Mostly bread [G1]."},
		ask.Done{Status: ask.StatusAnswered, PromptVersion: ask.PromptVersion, Route: ask.RouteList,
			Markers: []string{"G1"}})
	w := upWorld(t, apitest.WithAsker(asker))
	code, out, errOut := w.exit(t, "ask", "everything about bread")
	require.Equal(t, 0, code, errOut)
	assert.Equal(t, "Mostly bread [G1].\n\n"+
		"Groups:\n"+
		"[G1] Bread\\x1b[2J (in Cooking) (1) (cited)\n"+
		"  - Sourdough\n    https://example.com/a\n    doc_id: 11111111-1111-4111-8111-111111111111\n"+
		"[G2] (unnamed) (1)\n"+
		"  - Rye\n    https://example.net/d\n    doc_id: 44444444-4444-4444-8444-444444444444\n"+
		"[G3] in no interest (1)\n"+
		"  - Stray\n    https://example.org/b\n    doc_id: 22222222-2222-4222-8222-222222222222\n", out)
}

// TestAsk_ExitCodes: an answer, a truncated one and no sources exit 0;
// every other ending exits 1, its reason on stderr after the answer's
// pieces; an error event names its request.
func TestAsk_ExitCodes(t *testing.T) {
	for _, tc := range []struct {
		status ask.Status
		code   int
		err    string
	}{
		{ask.StatusAnswered, 0, ""},
		{ask.StatusTruncated, 0, "note: the answer ran past 768 tokens"},
		{ask.StatusNoSources, 0, "note: the answer ran past 768 tokens"},
		{ask.StatusOff, 1, "Error: the answer ran past 768 tokens"},
		{ask.StatusWithheld, 1, "Error: the answer ran past 768 tokens"},
		{ask.StatusUnfit, 1, "Error: the answer ran past 768 tokens"},
		{ask.StatusTimeout, 1, "Error: the answer ran past 768 tokens"},
		{ask.StatusUnavailable, 1, "Error: the answer ran past 768 tokens"},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			reason := ""
			if tc.status != ask.StatusAnswered {
				reason = "the answer ran past 768 tokens"
			}
			w := upWorld(t, apitest.WithAsker(apitest.NewAsker(answerEvents(tc.status, reason)...)))
			code, out, errOut := w.exit(t, "ask", "q")
			assert.Equal(t, tc.code, code)
			assert.Contains(t, out, "Let it cool for an hour [1][9].\n", "the answer is kept")
			assert.Contains(t, errOut, tc.err)
		})
	}

	asker := apitest.NewAsker(ask.Progress{Stage: ask.StageRetrieving})
	asker.Set(errors.New("read the sources' fields: disk I/O error"), ask.Progress{Stage: ask.StageRetrieving})
	w := upWorld(t, apitest.WithAsker(asker))
	for _, args := range [][]string{{"ask", "q"}, {"ask", "--json", "q"}} {
		code, _, errOut := w.exit(t, args...)
		assert.Equal(t, 1, code, args)
		assert.Contains(t, errOut, "Error: read the sources' fields: disk I/O error (request ", args)
	}
}

// TestAsk_Notes: a partial answer, an uncited one and a search that found
// nothing say so on stderr.
func TestAsk_Notes(t *testing.T) {
	asker := apitest.NewAsker(ask.Delta{Text: "It cools."}, ask.Done{Status: ask.StatusAnswered,
		PromptVersion: ask.PromptVersion, Route: ask.RouteAnswer, Uncited: true})
	w := upWorld(t, apitest.WithAsker(asker))
	_, _, errOut := w.exit(t, "ask", "q")
	assert.Contains(t, errOut, "note: nothing is cited; check the sources before relying on it\n")

	asker.Set(nil, ask.Delta{Text: "It co"}, ask.Done{Status: ask.StatusUnavailable, Reason: "the model stopped",
		PromptVersion: ask.PromptVersion, Route: ask.RouteAnswer, Partial: true})
	code, out, errOut := w.exit(t, "ask", "q")
	assert.Equal(t, 1, code)
	assert.Equal(t, "It co\n", out)
	assert.Contains(t, errOut, "note: the answer stopped part way\nError: the model stopped\n")
}

// TestAsk_JSON: --json writes each event as the daemon sent it, one a
// line, every rune a terminal would act on escaped inside its strings:
// each line valid JSON decoding to what was sent.
func TestAsk_JSON(t *testing.T) {
	title := "Bread\x1b[2J\x07\x7f\u009b\u202egnp.exe"
	delta := "\x1b[31mred"
	asker := apitest.NewAsker(
		ask.Retrieved{Route: ask.RouteAnswer, Sources: []ask.Source{{N: 1,
			DocID: "11111111-1111-4111-8111-111111111111", URL: "https://example.com/a", Title: title}}},
		ask.Delta{Text: delta},
		ask.Done{Status: ask.StatusAnswered, PromptVersion: ask.PromptVersion, Route: ask.RouteAnswer})
	w := upWorld(t, apitest.WithAsker(asker))
	code, out, errOut := w.exit(t, "ask", "--json", "q")
	require.Equal(t, 0, code, errOut)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	require.Len(t, lines, 3)
	got := make([]map[string]any, 0, len(lines))
	for _, l := range lines {
		require.True(t, json.Valid([]byte(l)), l)
		var v map[string]any
		require.NoError(t, json.Unmarshal([]byte(l), &v))
		got = append(got, v)
		for _, r := range l {
			assert.False(t, r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == '\u202e', "a raw %U", r)
		}
	}
	assert.Equal(t, title, got[0]["sources"].([]any)[0].(map[string]any)["title"])
	assert.Equal(t, delta, got[1]["text"])
	assert.Equal(t, "done", got[2]["type"])
}

// TestAsk_JSONEveryEvent: the fixed plans' events pass through --json as
// the daemon sent them, every rune a terminal would act on escaped: an
// evidence source's quote and the evidence's reason, a table's labels, a
// group's labels and its pages' titles.
func TestAsk_JSONEveryEvent(t *testing.T) {
	hostileText := "x\x1b[2J\x07\x7f\u009b\u202egnp.exe"
	timeline := insight.Timeline([]store.RunSave{{DocumentID: "d1", InterestID: "in-1", AreaID: "area-1",
		SavedAt: time.Date(2024, 1, 5, 0, 0, 0, 0, time.UTC)}},
		map[string]string{"area-1": hostileText, "in-1": hostileText}, insight.Window{}, insight.PeriodAuto,
		time.UTC)
	asker := apitest.NewAsker(
		ask.Retrieved{Route: ask.RouteEvidence, Sources: []ask.Source{{N: 1,
			DocID: "11111111-1111-4111-8111-111111111111", URL: "https://example.com/a", Title: "For",
			Quote: hostileText}}},
		ask.Evidence{Status: ask.EvidenceNone, Reason: hostileText},
		ask.Table{RunID: "44444444-4444-4444-8444-444444444444", FinishedAt: time.Now(),
			Shape: store.InterestShapeAreas, Timeline: timeline, Markers: []string{"T1", "T2"}, UndatedMarker: "T3",
			Shown: 3},
		ask.Groups{Groups: []ask.Group{{Marker: "G1", InterestID: "in-1", Label: hostileText, AreaLabel: hostileText,
			Count: 1, Documents: []ask.GroupDocument{{DocID: "22222222-2222-4222-8222-222222222222",
				URL: "https://example.org/b", Title: hostileText, State: store.DocStateFetched}}}},
			Listed: 1, Ranked: 1, Shown: 1},
		ask.Done{Status: ask.StatusNoSources, Reason: "none of the 1 pages read supports or contradicts it",
			PromptVersion: ask.PromptVersion, Route: ask.RouteEvidence})
	w := upWorld(t, apitest.WithAsker(asker))
	code, out, errOut := w.exit(t, "ask", "--json", "q")
	require.Equal(t, 0, code, errOut)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	require.Len(t, lines, 5)
	for _, l := range lines {
		require.True(t, json.Valid([]byte(l)), l)
		for _, r := range l {
			assert.False(t, r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == '\u202e', "a raw %U", r)
		}
	}
	events := make([]client.AskEvent, len(lines))
	for i, l := range lines {
		require.NoError(t, json.Unmarshal([]byte(l), &events[i]), l)
	}
	assert.Equal(t, hostileText, events[0].Sources[0].Quote)
	assert.Equal(t, "evidence", events[1].Type)
	assert.Equal(t, hostileText, events[1].Reason)
	require.NotNil(t, events[2].Table())
	assert.Equal(t, hostileText, events[2].Table().Rows[0].Label)
	require.Len(t, events[3].Groups, 1)
	assert.Equal(t, hostileText, events[3].Groups[0].Label)
	assert.Equal(t, hostileText, events[3].Groups[0].AreaLabel)
	assert.Equal(t, hostileText, events[3].Groups[0].Documents[0].Title)
	assert.Equal(t, "done", events[4].Type)
}

// TestAsk_TerminalSafe: the answer and a source's title can't drive the
// terminal: their controls are written escaped.
func TestAsk_TerminalSafe(t *testing.T) {
	asker := apitest.NewAsker(
		ask.Retrieved{Route: ask.RouteAnswer, Sources: []ask.Source{{N: 1,
			DocID: "11111111-1111-4111-8111-111111111111", URL: "https://example.com/a", Title: "Bread\x1b[2J\nforged"}}},
		ask.Delta{Text: "\x1b[31mred [1]"},
		ask.Done{Status: ask.StatusAnswered, PromptVersion: ask.PromptVersion, Route: ask.RouteAnswer,
			Citations: []int{1}})
	w := upWorld(t, apitest.WithAsker(asker))
	code, out, _ := w.exit(t, "ask", "q")
	require.Equal(t, 0, code)
	assert.NotContains(t, out, "\x1b")
	assert.Contains(t, out, `\x1b[31mred [1]`)
	assert.Contains(t, out, `[1] Bread\x1b[2J\nforged (cited)`, "one line")
}

// TestAsk_OlderDaemon: a daemon without /v1/ask gets one line saying to
// restart it, and exit 1.
func TestAsk_OlderDaemon(t *testing.T) {
	w := upWorld(t)
	daemon, err := url.Parse(w.daemonURL)
	require.NoError(t, err)
	proxy := httputil.NewSingleHostReverseProxy(daemon)
	old := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/ask" {
			rw.Header().Set("Content-Type", "application/problem+json")
			rw.WriteHeader(http.StatusNotFound)
			fmt.Fprint(rw, `{"title":"Not Found","status":404}`)
			return
		}
		r.Host = daemon.Host
		proxy.ServeHTTP(rw, r)
	}))
	t.Cleanup(old.Close)
	w.daemonURL = old.URL
	code, _, errOut := w.exit(t, "ask", "q")
	assert.Equal(t, 1, code)
	assert.Contains(t, errOut, "Error: "+askPredates+"\n")
}

// TestAskWords: the fixes doctor shows, and the reasons it reads, are the
// ones the asker writes.
func TestAskWords(t *testing.T) {
	assert.Equal(t, ask.UnfitFix, askUnfitFix)
	assert.Equal(t, ask.OffReason, askOffReason)
	assert.Equal(t, ask.OnFix, askOnFix)
}

// TestDoctorAndStatus_Asking: while Ask is on and answers nothing, status
// says why in one warning line, and doctor warns with the fix; off, off by
// default for an unmeasured model (doctor's line then says how to turn it
// on), fit or unchecked warns of nothing, and a line on by default says so.
func TestDoctorAndStatus_Asking(t *testing.T) {
	at := time.Date(2026, 10, 14, 14, 2, 0, 0, time.UTC)
	until := time.Now().Add(10 * time.Minute)
	const model = "gemma4:26b"
	for _, tc := range []struct {
		name         string
		state        ask.State
		status       string
		mark, doctor string
		hint         string
	}{
		{"off", ask.State{Reason: ask.OffReason}, "", "✓", "off (ask.enabled: false)", ""},
		{"off by default", ask.State{Model: "qwen3:4b-instruct", Reason: ask.OptionsFrom(config.Default()).OffReason},
			"", "✓", "off by default for qwen3:4b-instruct, which isn't measured as the answer writer", askOnFix},
		{"withheld", ask.State{On: true, Model: model, Check: modelgate.CheckUnchecked,
			Reason: "questions and pages would leave this machine for http://gpu-box.lan:11434"},
			"warning: curio ask answers nothing: questions and pages would leave this machine for " +
				"http://gpu-box.lan:11434; run `curio doctor`\n",
			"!", "no question is answered: questions and pages would leave this machine for http://gpu-box.lan:11434",
			""},
		{"unfit", ask.State{On: true, Model: model, Check: modelgate.CheckUnfit, CheckedAt: at,
			Unfit: model + ` didn't pass the check "none answers"; ` + ask.UnfitFix},
			"warning: curio ask answers nothing: " + model + " failed its check; run `curio doctor`\n",
			"!", model + ` didn't pass the check "none answers" (checked ` + localTime(at) + "); no question is " +
				"answered", askUnfitFix},
		{"evicted", ask.State{On: true, Model: model, Check: modelgate.CheckFit, CheckedAt: at,
			EvictionHoldUntil: until},
			"warning: curio ask answers nothing until " + localTime(until) + ": Ollama keeps unloading " + model +
				" for other models; run `curio doctor`\n",
			"!", "Ollama unloaded " + model + " within its keep-alive twice, for other models; curio doesn't load " +
				"it again until " + localTime(until) + ", and no question is answered meanwhile", askEvictFix},
		{"timing out", ask.State{On: true, Model: model, Check: modelgate.CheckFit, CheckedAt: at,
			TimeoutHoldUntil: until},
			"warning: curio ask answers nothing until " + localTime(until) + ": answers with " + model +
				" keep timing out; run `curio doctor`\n",
			"!", "answers with " + model + " kept running past ask.timeout_seconds; no question is answered until " +
				localTime(until), askSlowFix},
		{"a check that ran out of time", ask.State{On: true, Model: model, Check: modelgate.CheckUnchecked,
			TimeoutHoldUntil: until, Failure: modelgate.Failure{Cause: "timeout:check", At: at,
				Err: "checking " + model + `, the probe "none answers" took longer than 30s`}},
			"warning: curio ask answers nothing until " + localTime(until) + ": checking " + model +
				" ran out of time; run `curio doctor`\n",
			"!", "checking " + model + `, the probe "none answers" took longer than 30s; curio checks it again at ` +
				localTime(until) + ", and no question is answered meanwhile", askSlowFix},
		{"a check that keeps failing", ask.State{On: true, Model: model, Check: modelgate.CheckUnchecked,
			Failure: modelgate.Failure{Cause: "unavailable:check", At: at, Err: `probe "none answers": HTTP 500`}},
			"warning: curio ask answers nothing until " + model + " can be checked; run `curio doctor`\n",
			"!", model + ` couldn't be checked: probe "none answers": HTTP 500; no question is answered until it is`,
			askCheckFix},
		{"fit", ask.State{On: true, Model: model, Check: modelgate.CheckFit, CheckedAt: at}, "",
			"✓", model + " passed its check (" + localTime(at) + ")", ""},
		{"on by default, fit", ask.State{On: true, ByDefault: true, Model: model, Check: modelgate.CheckFit,
			CheckedAt: at}, "",
			"✓", "on by default (measured as the answer writer): " + model + " passed its check (" + localTime(at) +
				")", ""},
		{"unchecked", ask.State{On: true, Model: model, Check: modelgate.CheckUnchecked}, "",
			"✓", model + " not checked yet: the first question loads and checks it", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := apitest.NewAsker()
			a.SetState(tc.state)
			w := upWorld(t, apitest.WithAsker(a))
			out, err := w.run(t, "status")
			require.NoError(t, err)
			if tc.status == "" {
				assert.NotContains(t, out, "curio ask")
			} else {
				assert.Contains(t, out, tc.status)
			}
			out, err = w.run(t, "doctor")
			require.NoError(t, err, out)
			line, hint := doctorLine(t, out, "ask")
			assert.Equal(t, fmt.Sprintf("%s %-22s %s", tc.mark, "ask", tc.doctor), line)
			assert.Equal(t, tc.hint, hint)
			assert.Empty(t, a.Requests(), "status and doctor ask nothing")
		})
	}
}
