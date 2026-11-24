package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// streamServer answers POST /v1/ask with handle.
func streamServer(t *testing.T, handle http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handle)
	t.Cleanup(srv.Close)
	return New(srv.URL)
}

// TestAsk_Events: each line is an event, decoded and kept as sent, past
// the client's request timeout; the request carries what it sets.
func TestAsk_Events(t *testing.T) {
	var body map[string]any
	c := streamServer(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/v1/ask", r.URL.Path)
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.Header().Set("Content-Type", "application/x-ndjson")
		for _, line := range []string{`{"type":"progress","stage":"loading","model":"m"}`,
			`{"type":"retrieval","route":"answer","sources":[{"n":1,"doc_id":"d","url":"https://example.com/","title":"T"}],"new":1}`,
			`{"type":"delta","text":"It [1]."}`,
			`{"type":"done","status":"answered","citations":[1],"timings":{"total_ms":5}}`} {
			fmt.Fprintln(w, line)
			w.(http.Flusher).Flush()
		}
	})
	c.http.Timeout = time.Nanosecond // the stream ignores it
	var events []AskEvent
	off := false
	require.NoError(t, c.Ask(context.Background(), AskRequest{Question: "q", Rerank: &off, Sources: 3},
		func(e AskEvent) error {
			events = append(events, e)
			return nil
		}))
	assert.Equal(t, map[string]any{"question": "q", "rerank": false, "sources": float64(3)}, body)
	require.Len(t, events, 4)
	assert.Equal(t, "loading", events[0].Stage)
	assert.Equal(t, "T", events[1].Sources[0].Title)
	assert.Contains(t, string(events[1].Raw), `"new":1`, "kept as sent")
	assert.Equal(t, "It [1].", events[2].Text)
	assert.Equal(t, []int{1}, events[3].Citations)
	assert.Equal(t, int64(5), events[3].Timings.TotalMS)
}

// TestAsk_RouteEvents: the fixed plans' events decode: a judging
// progress, an evidence event, a table event (its timeline read back as
// the timeline endpoint's), a groups event, and done's markers and
// timings.
func TestAsk_RouteEvents(t *testing.T) {
	c := streamServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		for _, line := range []string{
			`{"type":"progress","stage":"judging","model":"m","pages":12}`,
			`{"type":"retrieval","route":"evidence","sources":[{"n":1,"doc_id":"d","url":"https://example.com/",` +
				`"title":"T","quote":"q","passages":[{"text":"p","section":"S"}]}]}`,
			`{"type":"evidence","status":"judged","judged":12,"supports":[1],"contradicts":[],"dropped_quotes":2,` +
				`"took_ms":900}`,
			`{"type":"table","run":{"id":"r","finished_at":"2025-04-01T09:00:00Z","shape":"areas"},"period":"year",` +
				`"periods":["2024"],"rows":[{"kind":"area","id":"a","label":"Cooking","counts":[3],"total":3,` +
				`"marker":"T1"}],"dated":3,"undated":1,"undated_marker":"T2","shown":2}`,
			`{"type":"groups","groups":[{"marker":"G1","label":"Bread","count":1,"documents":[{"doc_id":"d",` +
				`"url":"https://example.com/","title":"T","state":"dead"}]}],"listed":1,"ranked":9,"wordless":true,` +
				`"shown":1}`,
			`{"type":"done","status":"answered","route":"interests","citations":[],"markers":["T1","T2"],` +
				`"timings":{"sources_ms":4,"judge_ms":900,"prompt_eval_ms":700,"total_ms":5}}`} {
			fmt.Fprintln(w, line)
		}
	})
	var events []AskEvent
	require.NoError(t, c.Ask(context.Background(), AskRequest{Question: "q"}, func(e AskEvent) error {
		events = append(events, e)
		return nil
	}))
	require.Len(t, events, 6)
	assert.Equal(t, 12, events[0].Pages)
	assert.Equal(t, "q", events[1].Sources[0].Quote)
	assert.Equal(t, []AskPassage{{Text: "p", Section: "S"}}, events[1].Sources[0].Passages)
	ev := events[2]
	assert.Equal(t, "judged", ev.Status)
	assert.Equal(t, 12, ev.Judged)
	assert.Equal(t, []int{1}, ev.Supports)
	assert.Equal(t, 2, ev.DroppedQuotes)
	assert.Equal(t, int64(900), ev.TookMS)
	table := events[3].Table()
	require.NotNil(t, table)
	assert.Equal(t, "r", table.Run.ID)
	assert.Equal(t, []TimelineRow{{Kind: "area", ID: "a", Label: "Cooking", Counts: []int{3}, Total: 3,
		Marker: "T1"}}, table.Rows)
	assert.Equal(t, 1, table.Undated)
	assert.Equal(t, "T2", events[3].UndatedMarker)
	assert.Equal(t, 2, events[3].Shown)
	assert.Nil(t, events[2].Table(), "only a table event has one")
	g := events[4]
	require.Len(t, g.Groups, 1)
	assert.Equal(t, "G1", g.Groups[0].Marker)
	assert.Equal(t, "dead", g.Groups[0].Documents[0].State)
	assert.True(t, g.Wordless)
	assert.Equal(t, 9, g.Ranked)
	done := events[5]
	assert.Equal(t, []string{"T1", "T2"}, done.Markers)
	assert.Equal(t, AskTimings{SourcesMS: 4, JudgeMS: 900, PromptEvalMS: 700, TotalMS: 5}, done.Timings)
}

// TestAsk_Refused: a problem is an *APIError, a 404 among them.
func TestAsk_Refused(t *testing.T) {
	c := streamServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"title":"Not Found","status":404,"detail":"no route"}`)
	})
	err := c.Ask(context.Background(), AskRequest{Question: "q"}, func(AskEvent) error { return nil })
	assert.True(t, IsNotFound(err), "%v", err)
}

// TestAsk_Stalled: a stream quiet past its idle bound is cut, saying so.
func TestAsk_Stalled(t *testing.T) {
	c := streamServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"type":"progress","stage":"loading"}`)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	c.askIdle = 50 * time.Millisecond
	var events int
	err := c.Ask(context.Background(), AskRequest{Question: "q"}, func(AskEvent) error {
		events++
		return nil
	})
	require.ErrorIs(t, err, ErrAskStalled)
	assert.Equal(t, "curio-daemon sent nothing for 30 s", err.Error())
	assert.Equal(t, 1, events)
}

// TestAsk_LineTooLong: a line over the cap is an error, not memory.
func TestAsk_LineTooLong(t *testing.T) {
	c := streamServer(t, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, `{"type":"delta","text":"`+strings.Repeat("x", maxAskLine)+`"}`)
	})
	err := c.Ask(context.Background(), AskRequest{Question: "q"}, func(AskEvent) error { return nil })
	require.ErrorContains(t, err, "token too long")
}

// TestAsk_Cancelled: cancelling the context, as Ctrl-C does, closes the
// request, which the daemon sees at once.
func TestAsk_Cancelled(t *testing.T) {
	gone := make(chan struct{})
	c := streamServer(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"type":"progress","stage":"writing"}`)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(gone)
	})
	ctx, cancel := context.WithCancel(context.Background())
	err := c.Ask(ctx, AskRequest{Question: "q"}, func(AskEvent) error {
		cancel()
		return nil
	})
	require.ErrorIs(t, err, context.Canceled)
	select {
	case <-gone:
	case <-time.After(time.Second):
		t.Fatal("the daemon never saw the request close")
	}
}
