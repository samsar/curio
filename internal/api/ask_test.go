package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/ask"
	"github.com/samsar/curio/internal/insight"
	"github.com/samsar/curio/internal/modelgate"
	"github.com/samsar/curio/internal/rerank"
	"github.com/samsar/curio/internal/retrieve"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/ui"
	"github.com/samsar/curio/internal/understand"
)

// fakeAsker answers as answer says, recording each request.
type fakeAsker struct {
	mu      sync.Mutex
	asked   []ask.Request
	answer  func(ctx context.Context, req ask.Request, emit func(ask.Event) error) error
	timeout time.Duration
	state   ask.State
}

func (a *fakeAsker) Ask(ctx context.Context, req ask.Request, emit func(ask.Event) error) error {
	a.mu.Lock()
	a.asked = append(a.asked, req)
	a.mu.Unlock()
	return a.answer(ctx, req, emit)
}

func (a *fakeAsker) Timeout() time.Duration {
	if a.timeout == 0 {
		return time.Minute
	}
	return a.timeout
}

func (a *fakeAsker) State() ask.State { return a.state }

func (a *fakeAsker) requests() []ask.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]ask.Request(nil), a.asked...)
}

// emitAll emits events in order, stopping at the first error.
func emitAll(emit func(ask.Event) error, events ...ask.Event) error {
	for _, e := range events {
		if err := emit(e); err != nil {
			return err
		}
	}
	return nil
}

// contractAsks answers each contract question with every event and field
// its stream can carry: "kafka" an answer from two sources with an
// unreadable page beside them, "timeout" one cut by its deadline,
// "nothing" an abstention, "broken" a failure after the stream began.
func contractAsks(_ context.Context, req ask.Request, emit func(ask.Event) error) error {
	timings := ask.Timings{Wait: time.Second, Plan: time.Second, Search: time.Millisecond, Rerank: time.Second,
		Sources: time.Millisecond, Judge: time.Second, PromptEval: time.Second, FirstToken: 3 * time.Second,
		Total: 5 * time.Second}
	done := ask.Done{Model: "gemma4:26b", PromptVersion: ask.PromptVersion, Route: ask.RouteAnswer, Timings: timings}
	switch req.Question {
	case "kafka":
		plan := kafkaPlan()
		done.Status, done.Citations, done.InvalidCitations = ask.StatusAnswered, []int{1, 2}, []string{"9"}
		done.CitedAt = []ask.CitedAt{{Start: 25, End: 26, N: 1}, {Start: 28, End: 29, N: 2}}
		return emitAll(emit,
			ask.Progress{Stage: ask.StageLoading, Model: "gemma4:26b"},
			ask.Progress{Stage: ask.StageRetrieving},
			ask.Retrieved{Route: ask.RouteAnswer,
				Understanding: &retrieve.Understanding{Status: understand.StatusPlanned, Model: "gemma4:26b",
					Took: time.Second, Plan: plan,
					Dropped: []understand.Dropped{{Field: "source", Reason: "the request sets source"}}},
				Reranking: &retrieve.Reranking{Status: rerank.StatusReranked, Model: "gemma4:26b",
					Method: rerank.MethodListwise, Depth: 30, Took: time.Second, Order: []string{uuid.NewString()}},
				Degraded: true, Warnings: []string{"semantic search unavailable"},
				Sources: []ask.Source{{N: 1, DocID: uuid.NewString(), URL: "https://example.com/a", Title: "A",
					SavedAs: []string{"a saved"}, Site: "example.com", SavedOn: "2024-03-05", About: "About a.",
					Passages:   []ask.Passage{{Text: "A passage.", Section: "Setup › Groups"}},
					SavedTitle: "A saved"},
					{N: 2, DocID: uuid.NewString(), URL: "https://example.org/b", Title: "B",
						OwnTitle: "B"}},
				Unreadable: []ask.Unreadable{{DocID: uuid.NewString(), URL: "https://example.net/c", Title: "C",
					State: store.DocStateDead, Reason: "the page is no longer available",
					SavedTitle: "C saved"}}},
			ask.Progress{Stage: ask.StageWriting, Model: "gemma4:26b"},
			ask.Delta{Text: "Kafka shares partitions [1][2] "},
			ask.Delta{Text: "and moves them [9]."},
			done)
	case "timeout":
		done.Status, done.Reason, done.Uncited, done.Partial = ask.StatusTimeout, "the ask took longer than 1m0s",
			true, true
		return emitAll(emit, ask.Progress{Stage: ask.StageWaiting, Model: "gemma4:26b"},
			ask.Retrieved{Route: ask.RouteAnswer, Sources: []ask.Source{{N: 1, DocID: uuid.NewString(),
				URL: "https://example.com/a", Title: "A"}}},
			ask.Delta{Text: "Kafka"}, done)
	case "nothing":
		done.Status, done.Abstained = ask.StatusAnswered, true
		return emitAll(emit, ask.Progress{Stage: ask.StageChecking, Model: "gemma4:26b"},
			ask.Retrieved{Route: ask.RouteAnswer, Sources: []ask.Source{{N: 1, DocID: uuid.NewString(),
				URL: "https://example.com/a", Title: "A"}}},
			ask.Delta{Text: ask.Abstention}, done)
	case "is it true that kafka never loses data":
		done.Status, done.Route, done.Citations = ask.StatusAnswered, ask.RouteEvidence, []int{1, 2}
		return emitAll(emit,
			ask.Progress{Stage: ask.StageRetrieving},
			ask.Progress{Stage: ask.StageJudging, Model: "gemma4:26b", Pages: 2},
			ask.Retrieved{Route: ask.RouteEvidence, Sources: []ask.Source{
				{N: 1, DocID: uuid.NewString(), URL: "https://example.com/a", Title: "A", Quote: "acks=all",
					Passages: []ask.Passage{{Text: "With acks=all no write is lost."}}},
				{N: 2, DocID: uuid.NewString(), URL: "https://example.org/b", Title: "B", Quote: "can lose",
					Passages: []ask.Passage{{Text: "An unclean election can lose writes."}}}}},
			ask.Evidence{Status: ask.EvidenceJudged, Judged: 2, Supports: []int{1}, Contradicts: []int{2},
				DroppedQuotes: 1, Took: time.Second},
			ask.Delta{Text: "It holds with acks=all [1], not after an unclean election [2]."},
			done)
	case "evidence that fell back":
		done.Status, done.Route = ask.StatusAnswered, ask.RouteEvidence
		return emitAll(emit, ask.Retrieved{Route: ask.RouteEvidence},
			ask.Evidence{Status: ask.EvidenceTimeout, Reason: "the judgment took too long"}, done)
	case "how have my interests changed":
		done.Status, done.Route, done.Markers = ask.StatusAnswered, ask.RouteInterests, []string{"T1"}
		area := uuid.NewString()
		interests := []string{uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()}
		jan, may := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC), time.Date(2024, 5, 2, 0, 0, 0, 0, time.UTC)
		saves := []store.RunSave{{DocumentID: "d5", SavedAt: may}, {DocumentID: "d6"}}
		for i, interest := range interests {
			at := may
			if i == 0 {
				at = jan
			}
			saves = append(saves, store.RunSave{DocumentID: fmt.Sprint("d", i), InterestID: interest, AreaID: area,
				SavedAt: at})
		}
		window := insight.Window{After: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
			Before: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)}
		timeline := insight.Timeline(saves, map[string]string{area: "Cooking", interests[0]: "Bread"}, window,
			insight.PeriodAuto, time.UTC)
		markers := make([]string, len(timeline.Rows))
		for i := range markers {
			markers[i] = fmt.Sprint("T", i+1)
		}
		return emitAll(emit, ask.Progress{Stage: ask.StageRetrieving},
			ask.Retrieved{Route: ask.RouteInterests, Understanding: &retrieve.Understanding{
				Status: understand.StatusPlanned, Plan: &understand.Plan{Kind: understand.KindAnalysis}}},
			ask.Table{RunID: uuid.NewString(), FinishedAt: time.Now(), Shape: store.InterestShapeAreas, Window: window,
				Timeline: timeline, Markers: markers, UndatedMarker: fmt.Sprint("T", len(markers)+1),
				Shown: len(markers) + 1},
			ask.Delta{Text: "Bread grew [T1]."}, done)
	case "everything about kafka":
		done.Status, done.Route, done.Markers = ask.StatusAnswered, ask.RouteList, []string{"G1"}
		done.CitedAt = []ask.CitedAt{{Start: 13, End: 15, Marker: "G1"}}
		return emitAll(emit, ask.Retrieved{Route: ask.RouteList},
			ask.Groups{Groups: []ask.Group{{Marker: "G1", InterestID: uuid.NewString(), Label: "Kafka",
				AreaLabel: "Systems", Count: 1, Documents: []ask.GroupDocument{{DocID: uuid.NewString(),
					URL: "https://example.com/a", Title: "example.com", State: store.DocStateFetched,
					About: "About a."}}}},
				Listed: 1, Ranked: 40, Wordless: true, Shown: 1},
			ask.Delta{Text: "Kafka pages [G1]."}, done)
	case "broken":
		if err := emit(ask.Progress{Stage: ask.StageRetrieving}); err != nil {
			return err
		}
		return errors.New("read the sources' fields: disk I/O error")
	}
	done.Status, done.Reason = ask.StatusNoSources, "the search found no saved page to answer from"
	return emit(done)
}

// contractAsker is the contract test's asker: contractAsks, its state with
// every field set.
func contractAsker() *fakeAsker {
	now := time.Now()
	return &fakeAsker{answer: contractAsks, state: ask.State{On: true, ByDefault: true, Model: "gemma4:26b",
		Reason: "questions and pages would leave this machine", Check: modelgate.CheckUnfit, CheckedAt: now,
		Unfit: `gemma4:26b didn't pass the check "none answers"`, Last: ask.StatusTimeout, LastAt: now,
		TimeoutHoldUntil: now.Add(2 * time.Minute), EvictionHoldUntil: now.Add(15 * time.Minute),
		Failure: modelgate.Failure{Cause: "timeout:held", At: now, Err: "3 answers in a row took longer than 1m30s"}}}
}

// askLines posts body to /v1/ask and decodes the stream's lines.
func askLines(t *testing.T, s *testServer, body string) (response, []map[string]any) {
	t.Helper()
	resp := s.do(t, request{method: http.MethodPost, path: "/v1/ask", contentType: "application/json", body: body})
	var lines []map[string]any
	if resp.status != http.StatusOK {
		return resp, nil
	}
	for line := range strings.Lines(resp.body) {
		var v map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &v), line)
		lines = append(lines, v)
	}
	return resp, lines
}

// TestAsk_Stream: a request is asked as sent, understanding and
// reranking on unless turned off, and its events stream one a line, as
// application/x-ndjson that no cache keeps.
func TestAsk_Stream(t *testing.T) {
	asker := contractAsker()
	s := newTestServer(t, func(d *Deps) { d.Asker = asker })
	resp, lines := askLines(t, s, `{"question":"kafka","filters":{"site":["example.com"]},"sources":3}`)
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	assert.Equal(t, "application/x-ndjson", resp.contentType)
	assert.Equal(t, "no-store", resp.header.Get("Cache-Control"))
	types := make([]string, 0, len(lines))
	for _, l := range lines {
		types = append(types, l["type"].(string))
	}
	assert.Equal(t, []string{"progress", "progress", "retrieval", "progress", "delta", "delta", "done"}, types)
	assert.Equal(t, "answered", lines[len(lines)-1]["status"])
	assert.Equal(t, []any{float64(1), float64(2)}, lines[len(lines)-1]["citations"])

	req := asker.requests()[0]
	assert.True(t, req.Understand)
	assert.Nil(t, req.Rerank, "left to ask.rerank")
	assert.Equal(t, 3, req.Sources)
	assert.Equal(t, []string{"example.com"}, req.Filters.Site)

	_, _ = askLines(t, s, `{"question":"q","understand":false,"rerank":false}`)
	req = asker.requests()[1]
	assert.False(t, req.Understand)
	assert.Equal(t, new(false), req.Rerank)

	_, _ = askLines(t, s, `{"question":"q","plan":{"kind":"list","rewrites":["a b"]}}`)
	req = asker.requests()[2]
	assert.False(t, req.Understand, "a plan understands nothing")
	require.NotNil(t, req.Plan)
	assert.Equal(t, []string{"a b"}, req.Plan.Rewrites)
}

// TestAsk_CitedAt: an answered ask's done line says where each citation
// is written, in code points of the delta texts joined; one that found
// nothing to answer from says nowhere, an empty array.
func TestAsk_CitedAt(t *testing.T) {
	s := newTestServer(t, func(d *Deps) { d.Asker = contractAsker() })
	for _, q := range []string{"kafka", "everything about kafka"} {
		_, lines := askLines(t, s, `{"question":"`+q+`"}`)
		var answer strings.Builder
		for _, l := range lines {
			if l["type"] == "delta" {
				answer.WriteString(l["text"].(string))
			}
		}
		runes := []rune(answer.String())
		done := lines[len(lines)-1]
		marks := done["cited_at"].([]any)
		require.NotEmpty(t, marks, q)
		for _, m := range marks {
			mark := m.(map[string]any)
			written := string(runes[int(mark["start"].(float64)):int(mark["end"].(float64))])
			if marker, ok := mark["marker"]; ok {
				assert.Equal(t, marker, written, q)
				continue
			}
			assert.Equal(t, fmt.Sprint(mark["n"]), written, q)
		}
	}
	_, lines := askLines(t, s, `{"question":"anything else"}`)
	done := lines[len(lines)-1]
	assert.Equal(t, "no_sources", done["status"])
	assert.Equal(t, []any{}, done["cited_at"])
}

// TestAsk_Names: every page an ask reports, a source, an unreadable page
// or a group's document, is named as the dashboard's lists name it
// (ui.DocRef): untitled, by the title it was last saved under, or by its
// short URL when that title is an address, marked untitled either way; by
// its own title otherwise.
func TestAsk_Names(t *testing.T) {
	docs := []struct {
		id, url string
		names   ask.Names
		name    string
	}{
		{uuid.NewString(), "https://example.com/saved", ask.Names{SavedTitle: "Newer title"}, "Newer title"},
		{uuid.NewString(), "https://example.com/a/b", ask.Names{SavedTitle: "https://example.com/a/b"},
			ui.DocRef{URL: "https://example.com/a/b"}.Name()},
		{uuid.NewString(), "https://example.com/titled", ask.Names{OwnTitle: "Its own"}, "Its own"},
	}
	sources := make([]ask.Source, 0, len(docs))
	unread := make([]ask.Unreadable, 0, len(docs))
	grouped := make([]ask.GroupDocument, 0, len(docs))
	for i, d := range docs {
		sources = append(sources, ask.Source{N: i + 1, DocID: d.id, URL: d.url, Title: "prompt's", Names: d.names})
		unread = append(unread, ask.Unreadable{DocID: d.id, URL: d.url, Title: "prompt's", State: store.DocStateDead,
			Reason: "gone", Names: d.names})
		grouped = append(grouped, ask.GroupDocument{DocID: d.id, URL: d.url, Title: "prompt's",
			State: store.DocStateFetched, Names: d.names})
	}
	asker := &fakeAsker{answer: func(_ context.Context, _ ask.Request, emit func(ask.Event) error) error {
		return emitAll(emit, ask.Retrieved{Route: ask.RouteList, Sources: sources, Unreadable: unread},
			ask.Groups{Groups: []ask.Group{{Marker: "G1", Label: "L", Count: len(grouped), Documents: grouped}}},
			ask.Done{Status: ask.StatusAnswered, PromptVersion: ask.PromptVersion, Route: ask.RouteList})
	}}
	s := newTestServer(t, func(d *Deps) { d.Asker = asker })
	_, lines := askLines(t, s, `{"question":"q"}`)
	require.Len(t, lines, 3)
	retrieval, groups := lines[0], lines[1]["groups"].([]any)[0].(map[string]any)
	for what, pages := range map[string][]any{"source": retrieval["sources"].([]any),
		"unreadable": retrieval["unreadable"].([]any), "group document": groups["documents"].([]any)} {
		require.Len(t, pages, len(docs), what)
		for i, d := range docs {
			page := pages[i].(map[string]any)
			ref := ui.DocRef{ID: d.id, Title: d.names.OwnTitle, Fallback: d.names.SavedTitle, URL: d.url}
			assert.Equal(t, d.name, page["name"], what)
			assert.Equal(t, ref.Name(), page["name"], what)
			assert.Equal(t, ref.Hover(), page["full_name"], what)
			untitled, _ := page["untitled"].(bool)
			assert.Equal(t, ref.Untitled(), untitled, what)
			assert.Equal(t, d.names.OwnTitle == "", untitled, what)
			assert.Equal(t, "prompt's", page["title"], "%s: the prompt's title is kept", what)
		}
	}
	assert.Equal(t, "https://example.com/a/b", retrieval["sources"].([]any)[1].(map[string]any)["full_name"])
}

// TestAsk_Refused: what is refused before a byte of the stream, each a
// problem with its status.
func TestAsk_Refused(t *testing.T) {
	asker := contractAsker()
	s := newTestServer(t, func(d *Deps) { d.Asker = asker })
	for _, tc := range []struct {
		name, body, detail string
		status             int
	}{
		{"malformed", `{"question":`, "malformed JSON body", http.StatusBadRequest},
		{"an unknown field", `{"question":"q","k":3}`, `unknown field "k"`, http.StatusBadRequest},
		{"empty", `{"question":"  "}`, "question is required", http.StatusBadRequest},
		{"too long", `{"question":"` + strings.Repeat("x", 513) + `"}`, "at most 512", http.StatusBadRequest},
		{"too many sources", `{"question":"q","sources":13}`, "sources must be between 1 and 12",
			http.StatusBadRequest},
		{"an unknown mode", `{"question":"q","mode":"essay"}`, "isn't a route", http.StatusBadRequest},
		{"the interests mode with a host", `{"question":"q","mode":"interests","filters":{"host":["example.com"]}}`,
			"mode interests counts saves by date alone: filters.host isn't taken", http.StatusBadRequest},
		{"a body over 1 MiB", `{"question":"` + strings.Repeat("x", maxJSONBody+1-len(`{"question":""}`)) + `"}`, "",
			http.StatusRequestEntityTooLarge},
		{"a bad filter", `{"question":"q","filters":{"saved_after":"soon"}}`, "filters.saved_after",
			http.StatusBadRequest},
		{"a plan and understand", `{"question":"q","understand":true,"plan":{"kind":"list"}}`,
			"send understand or plan, not both", http.StatusBadRequest},
		{"a plan refused", `{"question":"q","plan":{"kind":"summary"}}`, "plan.kind", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := assertProblem(t, s.do(t, request{method: http.MethodPost, path: "/v1/ask",
				contentType: "application/json", body: tc.body}), tc.status)
			assert.Contains(t, p.Detail, tc.detail)
		})
	}
	resp := s.do(t, request{method: http.MethodPost, path: "/v1/ask", contentType: "text/plain",
		body: `{"question":"q"}`})
	assert.Equal(t, http.StatusUnsupportedMediaType, resp.status)
	assert.Empty(t, asker.requests(), "nothing asked")
}

// TestAsk_Modes: every route is a mode a request may name, the interests
// with their saved window.
func TestAsk_Modes(t *testing.T) {
	asker := contractAsker()
	s := newTestServer(t, func(d *Deps) { d.Asker = asker })
	for _, body := range []string{`{"question":"q","mode":"answer"}`, `{"question":"q","mode":"evidence"}`,
		`{"question":"q","mode":"list"}`,
		`{"question":"q","mode":"interests","filters":{"saved_after":"2024-01-01"}}`} {
		resp, _ := askLines(t, s, body)
		assert.Equal(t, http.StatusOK, resp.status, body)
	}
	asked := asker.requests()
	modes := make([]ask.Route, 0, len(asked))
	for _, r := range asked {
		modes = append(modes, r.Mode)
	}
	assert.Equal(t, ask.Routes(), slices.Sorted(slices.Values(modes)))
}

// TestAsk_ErrorAfterTheStreamBegan: a failure inside the daemon once the
// stream began is one error event, with the request's ID, logged at ERROR.
func TestAsk_ErrorAfterTheStreamBegan(t *testing.T) {
	logs := &strings.Builder{}
	var mu sync.Mutex
	s := newTestServer(t, func(d *Deps) {
		d.Asker = contractAsker()
		d.Log = slog.New(slog.NewJSONHandler(lockedBuilder{&mu, logs}, nil))
	})
	resp, lines := askLines(t, s, `{"question":"broken"}`)
	require.Equal(t, http.StatusOK, resp.status)
	require.Len(t, lines, 2)
	assert.Equal(t, map[string]any{"type": "error", "error": "read the sources' fields: disk I/O error",
		"request_id": resp.requestID}, lines[1])
	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, logs.String(), `"level":"ERROR","msg":"ask failed","request_id":"`+resp.requestID+`"`)
}

// TestAsk_ClientStopsReading: a stream whose write fails, the client gone
// or too slow for the write deadline, ends the ask with a DEBUG line and
// no ERROR: nothing of the daemon's failed.
func TestAsk_ClientStopsReading(t *testing.T) {
	logs := &strings.Builder{}
	var mu sync.Mutex
	d := Deps{Log: slog.New(slog.NewJSONHandler(lockedBuilder{&mu, logs},
		&slog.HandlerOptions{Level: slog.LevelDebug})),
		Asker: &fakeAsker{answer: func(_ context.Context, _ ask.Request, emit func(ask.Event) error) error {
			return emit(ask.Progress{Stage: ask.StageRetrieving})
		}}}
	rec := &failingRecorder{ResponseRecorder: httptest.NewRecorder()}
	r := httptest.NewRequest(http.MethodPost, "/v1/ask", strings.NewReader(`{"question":"q"}`))
	r.Header.Set("Content-Type", "application/json")
	d.handleAsk(rec, r)

	assert.Equal(t, 1, rec.writes, "the one line, which failed; no error event after it")
	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, logs.String(), `"level":"DEBUG","msg":"ask: the client stopped reading the stream"`)
	assert.NotContains(t, logs.String(), `"level":"ERROR"`)
}

// lockedBuilder serializes writes to a builder.
type lockedBuilder struct {
	mu *sync.Mutex
	b  *strings.Builder
}

func (l lockedBuilder) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// TestAsk_NoAsker: without an asker, the stream is one done event, off.
func TestAsk_NoAsker(t *testing.T) {
	s := newTestServer(t)
	resp, lines := askLines(t, s, `{"question":"q"}`)
	require.Equal(t, http.StatusOK, resp.status)
	require.Len(t, lines, 1)
	assert.Equal(t, "off", lines[0]["status"])
	assert.Equal(t, ask.OffReason, lines[0]["reason"])
}

// slowAsks answers after a silence of quiet, then streams for long in
// steps, done at the end.
func slowAsks(quiet, long time.Duration) func(context.Context, ask.Request, func(ask.Event) error) error {
	return func(ctx context.Context, _ ask.Request, emit func(ask.Event) error) error {
		if err := emit(ask.Progress{Stage: ask.StageLoading, Model: "m"}); err != nil {
			return err
		}
		select {
		case <-time.After(quiet):
		case <-ctx.Done():
			return ctx.Err()
		}
		for range 4 {
			if err := emit(ask.Delta{Text: "x"}); err != nil {
				return err
			}
			select {
			case <-time.After(long / 4):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return emit(ask.Done{Status: ask.StatusAnswered, PromptVersion: ask.PromptVersion, Route: ask.RouteAnswer})
	}
}

// TestAsk_OutlastsTheWriteTimeout: an ask's stream extends its own write
// deadline past the server's, so a server whose write timeout is 300 ms
// still sends the done event of an ask that streams for a second.
func TestAsk_OutlastsTheWriteTimeout(t *testing.T) {
	deps := Deps{Asker: &fakeAsker{answer: slowAsks(0, time.Second)}, Log: slog.New(slog.DiscardHandler),
		TenantID: "local"}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	origin, err := newLocalOrigin(ln.Addr())
	require.NoError(t, err)
	router, err := newRouter(deps, origin, testDashboard(t, pagesOn))
	require.NoError(t, err)
	srv := &http.Server{Handler: router, WriteTimeout: 300 * time.Millisecond, ReadHeaderTimeout: time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	req, err := http.NewRequest(http.MethodPost, "http://"+ln.Addr().String()+"/v1/ask",
		strings.NewReader(`{"question":"q"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var last map[string]any
	lines := bufio.NewScanner(resp.Body)
	for lines.Scan() {
		require.NoError(t, json.Unmarshal(lines.Bytes(), &last))
	}
	require.NoError(t, lines.Err())
	assert.Equal(t, "done", last["type"])
}

// TestAsk_Heartbeat: a silence longer than the heartbeat writes the last
// progress event again, and nothing is written after done.
func TestAsk_Heartbeat(t *testing.T) {
	s := newTestServer(t, func(d *Deps) {
		d.Asker = &fakeAsker{answer: slowAsks(200*time.Millisecond, 0)}
		d.askHeartbeat = 30 * time.Millisecond
	})
	resp, lines := askLines(t, s, `{"question":"q"}`)
	require.Equal(t, http.StatusOK, resp.status)
	progress := 0
	for _, l := range lines {
		if l["type"] == "progress" {
			progress++
			assert.Equal(t, "loading", l["stage"])
		}
	}
	assert.Greater(t, progress, 2, "written again in the silence")
	assert.Equal(t, "done", lines[len(lines)-1]["type"], "nothing after done")
}

// TestEventStream_HeartbeatCountsFromTheLastLine: a heartbeat comes once
// the interval has passed since the last line, whenever that was
// written, not at the next tick of a clock of its own: a line written
// just after a tick would otherwise leave nearly two intervals of
// silence.
func TestEventStream_HeartbeatCountsFromTheLastLine(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const every = 10 * time.Second
		rec := &stampedRecorder{ResponseRecorder: httptest.NewRecorder()}
		s := &eventStream{w: rec, rc: http.NewResponseController(rec)}
		stop := s.heartbeat(every)
		require.NoError(t, s.write(askProgressEvent{Type: "progress", Stage: "loading"}))
		time.Sleep(every / 2)
		require.NoError(t, s.write(askProgressEvent{Type: "progress", Stage: "checking"}))
		written := time.Now()
		time.Sleep(2*every - time.Second)
		stop()

		require.Len(t, rec.lines, 3, "one heartbeat in the silence after the second line")
		assert.Equal(t, every, rec.at[2].Sub(written), "an interval after the last line")
		assert.Contains(t, rec.lines[2], `"stage":"checking"`, "the last progress event, again")
	})
}

// TestEventStream_NoLineAfterDone: an ask that writes its done event at
// the heartbeat's very instant is never followed by a heartbeat's line:
// the heartbeat decides and writes under the lock the done is written
// under.
func TestEventStream_NoLineAfterDone(t *testing.T) {
	const every = 10 * time.Second
	for range 300 {
		synctest.Test(t, func(t *testing.T) {
			rec := &stampedRecorder{ResponseRecorder: httptest.NewRecorder()}
			s := &eventStream{w: rec, rc: http.NewResponseController(rec)}
			stop := s.heartbeat(every)
			require.NoError(t, s.write(askProgressEvent{Type: "progress", Stage: "loading"}))
			time.Sleep(every)
			require.NoError(t, s.write(askDoneEvent{Type: "done", Status: "answered"}))
			time.Sleep(2 * every)
			stop()
			require.NotEmpty(t, rec.lines)
			assert.Contains(t, rec.lines[len(rec.lines)-1], `"type":"done"`, "nothing after done")
			assert.ErrorIs(t, s.write(askErrorEvent{Type: "error", Error: "late"}), errStreamEnded)
		})
	}
}

// stampedRecorder records each line written and when.
type stampedRecorder struct {
	*httptest.ResponseRecorder
	lines []string
	at    []time.Time
}

func (r *stampedRecorder) Write(p []byte) (int, error) {
	r.lines = append(r.lines, string(p))
	r.at = append(r.at, time.Now())
	return r.ResponseRecorder.Write(p)
}

// TestAsk_PanicAfterTheStreamBegan: an ask that panics once its stream
// began, through the router and a real http.Server, cuts the connection
// and writes nothing after its handler returned: the heartbeat stops with
// the handler. net/http closes the connection once the handler is gone, so
// a heartbeat still beating would write to it from a goroutine no
// handler's recover reaches, and stop the daemon.
func TestAsk_PanicAfterTheStreamBegan(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const every = time.Second
		deps := Deps{Log: slog.New(slog.DiscardHandler), TenantID: "local", askHeartbeat: every,
			Asker: &fakeAsker{answer: func(_ context.Context, _ ask.Request, emit func(ask.Event) error) error {
				if err := emit(ask.Progress{Stage: ask.StageLoading, Model: "m"}); err != nil {
					return err
				}
				panic("the asker broke")
			}}}
		ln := newPipeListener()
		origin, err := newLocalOrigin(ln.Addr())
		require.NoError(t, err)
		router, err := newRouter(deps, origin, testDashboard(t, pagesOn))
		require.NoError(t, err)
		var late atomic.Int32
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			after := &afterReturn{ResponseWriter: w, late: &late}
			defer after.returned.Store(true)
			router.ServeHTTP(after, r)
		})}
		served := make(chan error, 1)
		go func() { served <- srv.Serve(ln) }()
		transport := &http.Transport{DialContext: ln.dial}
		client := &http.Client{Transport: transport}

		resp, err := client.Post("http://"+ln.Addr().String()+"/v1/ask", "application/json",
			strings.NewReader(`{"question":"q"}`))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		_, err = io.ReadAll(resp.Body)
		assert.Error(t, err, "the connection is cut, not the response finished")
		require.NoError(t, resp.Body.Close())
		time.Sleep(10 * every) // a heartbeat still beating would beat ten times
		assert.Zero(t, late.Load(), "nothing is written after the handler returned")

		require.NoError(t, srv.Close())
		transport.CloseIdleConnections()
		assert.ErrorIs(t, <-served, http.ErrServerClosed)
	})
}

// afterReturn passes writes and flushes through to the response until
// returned is set, once the handler has returned; after it, each is
// counted in late and ends the goroutine that made it, which can only be
// one the handler left running: its write would reach a connection
// net/http has closed, and the goroutine would beat on, keeping the test
// from ending.
type afterReturn struct {
	http.ResponseWriter
	returned atomic.Bool
	late     *atomic.Int32
}

func (a *afterReturn) Write(p []byte) (int, error) {
	a.refuseLate()
	return a.ResponseWriter.Write(p)
}

func (a *afterReturn) FlushError() error {
	a.refuseLate()
	return http.NewResponseController(a.ResponseWriter).Flush()
}

func (a *afterReturn) Unwrap() http.ResponseWriter { return a.ResponseWriter }

func (a *afterReturn) refuseLate() {
	if a.returned.Load() {
		a.late.Add(1)
		runtime.Goexit()
	}
}

// pipeListener is an in-memory listener whose connections are net.Pipe
// pairs, so a server and its clients run inside a synctest bubble. Its
// address is the daemon's own, which the Host check admits.
type pipeListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{conns: make(chan net.Conn), closed: make(chan struct{})}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (*pipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8765} }

// dial is a Transport's DialContext: a new pipe, its other end accepted.
func (l *pipeListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.conns <- server:
		return client, nil
	case <-l.closed:
		return nil, net.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// TestEventStream_FailedWriteEndsIt: the first write that fails breaks the
// stream: no heartbeat writes again, and the ask's next event fails with
// that write's error, so the ask ends.
func TestEventStream_FailedWriteEndsIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		const every = 10 * time.Second
		rec := &failingRecorder{ResponseRecorder: httptest.NewRecorder(), failAfter: 1}
		s := &eventStream{w: rec, rc: http.NewResponseController(rec)}
		stop := s.heartbeat(every)
		require.NoError(t, s.write(askProgressEvent{Type: "progress", Stage: "loading"}))
		time.Sleep(5 * every)
		stop()

		assert.Equal(t, 2, rec.writes, "the line, then one heartbeat that failed")
		err := s.write(askDeltaEvent{Type: "delta", Text: "x"})
		require.ErrorIs(t, err, errWriteFailed)
		assert.Equal(t, 2, rec.writes, "nothing written after the failure")
	})
}

// errWriteFailed is a write to a client that has gone.
var errWriteFailed = errors.New("write: broken pipe")

// failingRecorder records writes, failing every one after the first
// failAfter.
type failingRecorder struct {
	*httptest.ResponseRecorder
	failAfter int
	writes    int
}

func (r *failingRecorder) Write(p []byte) (int, error) {
	r.writes++
	if r.writes > r.failAfter {
		return 0, errWriteFailed
	}
	return r.ResponseRecorder.Write(p)
}

// TestAsk_Bounded: with maxAsks asks in flight or waiting, another is
// refused before its stream begins, 503 with a Retry-After, asking
// nothing; once they have ended, the next is asked.
func TestAsk_Bounded(t *testing.T) {
	release := make(chan struct{})
	asker := &fakeAsker{answer: func(ctx context.Context, req ask.Request, emit func(ask.Event) error) error {
		if req.Question == "held" {
			if err := emit(ask.Progress{Stage: ask.StageWaiting, Model: "m"}); err != nil {
				return err
			}
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return emit(ask.Done{Status: ask.StatusAnswered, PromptVersion: ask.PromptVersion, Route: ask.RouteAnswer})
	}}
	s := newTestServer(t, func(d *Deps) { d.Asker = asker })
	var held sync.WaitGroup
	for range maxAsks {
		req, err := http.NewRequest(http.MethodPost, s.base+"/v1/ask", strings.NewReader(`{"question":"held"}`))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		lines := bufio.NewScanner(resp.Body)
		require.True(t, lines.Scan(), "the held ask's first line")
		held.Go(func() {
			defer resp.Body.Close()
			for lines.Scan() {
			}
		})
	}

	resp := s.do(t, request{method: http.MethodPost, path: "/v1/ask", contentType: "application/json",
		body: `{"question":"another"}`})
	p := assertProblem(t, resp, http.StatusServiceUnavailable)
	assert.Equal(t, "5", resp.header.Get("Retry-After"))
	assert.Equal(t, "4 questions are being answered or waiting to be; ask again once one has ended", p.Detail)
	assert.Len(t, asker.requests(), maxAsks, "the refused ask asked nothing")

	close(release)
	held.Wait()
	resp, lines := askLines(t, s, `{"question":"another"}`)
	require.Equal(t, http.StatusOK, resp.status, resp.body)
	assert.Equal(t, "done", lines[len(lines)-1]["type"])
}

// TestAsk_ClientLeaves: a client that closes the stream cancels the ask
// within a second, and the handler writes nothing more.
func TestAsk_ClientLeaves(t *testing.T) {
	cancelled := make(chan struct{})
	started := make(chan struct{})
	s := newTestServer(t, func(d *Deps) {
		d.Asker = &fakeAsker{answer: func(ctx context.Context, _ ask.Request, emit func(ask.Event) error) error {
			if err := emit(ask.Progress{Stage: ask.StageWriting}); err != nil {
				return err
			}
			close(started)
			<-ctx.Done()
			close(cancelled)
			return ctx.Err()
		}}
	})
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+"/v1/ask", strings.NewReader(`{"question":"q"}`))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	<-started
	cancel()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("the ask wasn't cancelled within a second of the client leaving")
	}
}

// TestHealth_Asking: healthz reports the asker's state from its memory,
// and off without one.
func TestHealth_Asking(t *testing.T) {
	s := newTestServer(t)
	var h Health
	require.NoError(t, json.Unmarshal([]byte(s.do(t, request{method: http.MethodGet, path: "/v1/healthz"}).body), &h))
	assert.Equal(t, AskingHealth{Reason: ask.OffReason}, h.Asking)

	asker := contractAsker()
	s = newTestServer(t, func(d *Deps) { d.Asker = asker })
	require.NoError(t, json.Unmarshal([]byte(s.do(t, request{method: http.MethodGet, path: "/v1/healthz"}).body), &h))
	assert.True(t, h.Asking.On)
	assert.Equal(t, modelgate.CheckUnfit, h.Asking.Check)
	assert.Equal(t, "timeout", h.Asking.LastStatus)
	require.NotNil(t, h.Asking.LastFailure)
	assert.Equal(t, "timeout:held", h.Asking.LastFailure.Cause)
	assert.Equal(t, "ok", h.Status, "never changes status")
}
