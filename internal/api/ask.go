package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/samsar/curio/internal/ask"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/ui"
)

// Asker answers questions: the daemon's ask.Asker, which it always
// builds, off with its reason when its role isn't in use. Timeout is how
// long an ask may take (ask.timeout_seconds), which the stream's write
// deadline outlasts. State is what it knows of its model, read from its
// memory alone, for healthz.
type Asker interface {
	Ask(ctx context.Context, req ask.Request, emit func(ask.Event) error) error
	Timeout() time.Duration
	State() ask.State
}

// AskRequest mirrors the openapi AskRequest schema. Understand is on
// unless the request turns it off, and Rerank left out is ask.rerank; a
// request that supplies a plan understands nothing, and may not ask to.
// Sources 0 is ask.sources, and Mode "" routes the question
// automatically.
type AskRequest struct {
	Question   string    `json:"question"`
	Filters    Filters   `json:"filters,omitzero"`
	Plan       *PlanJSON `json:"plan,omitempty"`
	Understand *bool     `json:"understand,omitempty"`
	Rerank     *bool     `json:"rerank,omitempty"`
	Sources    int       `json:"sources,omitempty"`
	Mode       string    `json:"mode,omitempty"`
}

// request is r as an ask takes it.
func (r AskRequest) request() ask.Request {
	out := ask.Request{Question: r.Question, Filters: r.Filters.request(), Sources: r.Sources,
		Mode: ask.Route(r.Mode), Understand: r.Plan == nil, Rerank: r.Rerank}
	if r.Plan != nil {
		p := r.Plan.plan()
		out.Plan = &p
	}
	if r.Understand != nil {
		out.Understand = *r.Understand
	}
	return out
}

// The stream's bounds. writeGrace is what the write deadline allows past
// the ask's own timeout, for its last events. askHeartbeat is the longest
// the stream goes without a line: a load, a check or a search writes
// nothing for seconds, and a client bounds its waits between lines (the
// CLI at 30 s), so the last progress event is written again.
const (
	writeGrace   = 10 * time.Second
	askHeartbeat = 10 * time.Second
)

// maxAsks bounds the asks in flight or waiting. One answer is written at
// a time (the writer's slot), and an ask waiting for it has planned its
// question already and holds a connection and a goroutine: past a few, a
// new one would only wait behind the others toward its own timeout, its
// plan adding to the model's queue meanwhile. Four is one answer being
// written and three waiting, about 20 s of answers at the measured p50.
// askRetryAfter, in seconds, is about one answer's time.
const (
	maxAsks       = 4
	askRetryAfter = "5"
)

// handleAsk answers POST /v1/ask: a problem for a request refused before
// anything is streamed (503 with a Retry-After while maxAsks are in flight
// or waiting), else 200 and the ask's events, one JSON line each
// (application/x-ndjson), each flushed as it is written. A failure of the
// daemon's after the stream began is one error event, logged at ERROR
// with the request's ID. A client that leaves cancels the ask; one that
// stops reading, or reads too slowly for the write deadline, breaks the
// stream, which ends the ask with a DEBUG line: nothing of the daemon's
// failed.
func (d Deps) handleAsk(w http.ResponseWriter, r *http.Request) {
	var body AskRequest
	if err := decodeJSON(w, r, maxJSONBody, &body); err != nil {
		d.writeError(w, r, err)
		return
	}
	req := body.request()
	if err := req.Validate(); err != nil {
		if re, ok := errors.AsType[*ask.RequestError](err); ok {
			err = &requestError{err: re.Err}
		}
		d.writeError(w, r, err)
		return
	}
	release, admitted := d.admitAsk()
	if !admitted {
		w.Header().Set("Retry-After", askRetryAfter)
		writeProblem(w, r, http.StatusServiceUnavailable, "too many asks",
			fmt.Sprintf("%d questions are being answered or waiting to be; ask again once one has ended", maxAsks))
		return
	}
	defer release()

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	// The server's write timeout protects every other route from a stuck
	// client; an ask may outlast it, so its deadline is its own.
	if err := rc.SetWriteDeadline(time.Now().Add(d.askTimeout() + writeGrace)); err != nil {
		d.Log.Debug("ask: the stream keeps the server's write deadline", "err", err)
	}
	s := &eventStream{w: w, rc: rc}
	if err := s.flush(); err != nil {
		return
	}
	// Deferred too: an ask that panics unwinds past the explicit stop, and
	// net/http closes the connection once the handler is gone, so a
	// heartbeat left running would write to it from a goroutine no
	// handler's recover reaches, and stop the daemon.
	stop := sync.OnceFunc(s.heartbeat(d.heartbeat()))
	defer stop()
	err := d.ask(r.Context(), req, s.write)
	stop()
	if err == nil || r.Context().Err() != nil {
		return
	}
	id := middleware.GetReqID(r.Context())
	if errors.Is(err, errClientGone) {
		d.Log.Debug("ask: the client stopped reading the stream", "request_id", id, "err", err)
		return
	}
	d.Log.Error("ask failed", "request_id", id, "err", err)
	_ = s.write(askErrorEvent{Type: "error", Error: err.Error(), RequestID: id}) // the stream ends either way
}

// admitAsk takes one of the maxAsks places of asks in flight or waiting,
// reporting whether it got one, and returns the func that gives it back.
// A Deps no router built (a test's handler alone) bounds nothing.
func (d Deps) admitAsk() (release func(), admitted bool) {
	if d.asks == nil {
		return func() {}, true
	}
	select {
	case d.asks <- struct{}{}:
		return func() { <-d.asks }, true
	default:
		return nil, false
	}
}

// ask runs req on the asker, or answers off without one.
func (d Deps) ask(ctx context.Context, req ask.Request, emit func(any) error) error {
	wire := func(e ask.Event) error { return emit(askEventJSON(e)) }
	if d.Asker == nil {
		return wire(ask.Done{Status: ask.StatusOff, Reason: ask.OffReason, PromptVersion: ask.PromptVersion,
			Route: ask.RouteAnswer})
	}
	return d.Asker.Ask(ctx, req, wire)
}

// askTimeout is how long an ask may take.
func (d Deps) askTimeout() time.Duration {
	if d.Asker == nil {
		return 0
	}
	return d.Asker.Timeout()
}

// heartbeat is how long the stream goes without a line before the last
// progress event is written again.
func (d Deps) heartbeat() time.Duration {
	if d.askHeartbeat > 0 {
		return d.askHeartbeat
	}
	return askHeartbeat
}

// eventStream writes an ask's events as JSON lines, each flushed, from
// the ask's goroutine and the heartbeat's alike. The done or error event
// ends it, and so does the first write or flush that fails: nothing is
// written after either.
type eventStream struct {
	w  http.ResponseWriter
	rc *http.ResponseController

	mu       sync.Mutex
	last     time.Time // when the last line was written
	progress []byte    // the last progress event's line, written again by the heartbeat
	ended    bool      // the done or error event was written
	broken   error     // the first write or flush that failed
}

// errStreamEnded is a line offered after the stream's last.
var errStreamEnded = errors.New("the ask's stream has ended")

// errClientGone marks a write or flush of the stream that failed: the
// client closed the connection, or read too slowly for the write
// deadline.
var errClientGone = errors.New("the client stopped reading the stream")

// write writes v as one line and flushes it.
func (s *eventStream) write(v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode an ask event: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch v.(type) {
	case askProgressEvent:
		s.progress = line
	case askDoneEvent, askErrorEvent:
		defer func() { s.ended = true }()
	}
	return s.writeLocked(line)
}

// writeLocked writes line and flushes it, unless the stream has ended or
// broken. s.mu is held.
func (s *eventStream) writeLocked(line []byte) error {
	switch {
	case s.broken != nil:
		return s.broken
	case s.ended:
		return errStreamEnded
	}
	if _, err := s.w.Write(append(line, '\n')); err != nil {
		return s.breakLocked(fmt.Errorf("write an ask event: %w: %w", errClientGone, err))
	}
	s.last = time.Now()
	return s.flushLocked()
}

func (s *eventStream) flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushLocked()
}

func (s *eventStream) flushLocked() error {
	if err := s.rc.Flush(); err != nil {
		return s.breakLocked(fmt.Errorf("flush the ask's stream: %w: %w", errClientGone, err))
	}
	return nil
}

// breakLocked ends the stream on err, a write or flush that failed, and
// returns it: the connection is gone or stalled past its deadline, and
// writing again would only fail again. s.mu is held.
func (s *eventStream) breakLocked(err error) error {
	s.broken = err
	return err
}

// heartbeat writes the last progress event again once every has passed
// since the last line, until the returned stop, which waits for it to
// end: nothing is written after the handler returns. It decides and
// writes under one lock, so a done written meanwhile is the last line.
func (s *eventStream) heartbeat(every time.Duration) (stop func()) {
	done := make(chan struct{})
	s.mu.Lock()
	s.last = time.Now()
	s.mu.Unlock()
	var wg sync.WaitGroup
	wg.Go(func() {
		timer := time.NewTimer(every)
		defer timer.Stop()
		for {
			select {
			case <-done:
				return
			case <-timer.C:
			}
			timer.Reset(s.beat(every))
		}
	})
	return func() {
		close(done)
		wg.Wait()
	}
}

// beat writes the last progress event again when every has passed since
// the last line and the stream hasn't ended, and returns how long until
// the next beat is due.
func (s *eventStream) beat(every time.Duration) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if quiet := time.Since(s.last); quiet < every {
		// A line went out meanwhile: the silence is counted from it.
		return every - quiet
	}
	if s.progress != nil && !s.ended && s.broken == nil {
		// A failed write breaks the stream, so the ask's next event
		// fails with it and the ask ends; no beat tries again.
		_ = s.writeLocked(s.progress)
	}
	return every
}

// The ask's events on the wire, mirroring the openapi AskEvent schemas.
type (
	askProgressEvent struct {
		Type  string `json:"type"`
		Stage string `json:"stage"`
		Model string `json:"model,omitempty"`
		Pages int    `json:"pages,omitempty"`
	}
	askRetrievalEvent struct {
		Type          string          `json:"type"`
		Route         string          `json:"route"`
		Understanding *Understanding  `json:"understanding,omitempty"`
		Reranking     *Reranking      `json:"reranking,omitempty"`
		Degraded      bool            `json:"degraded,omitempty"`
		Warnings      []string        `json:"warnings,omitempty"`
		Sources       []AskSource     `json:"sources"`
		Unreadable    []AskUnreadable `json:"unreadable,omitempty"`
	}
	askDeltaEvent struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	askEvidenceEvent struct {
		Type          string `json:"type"`
		Status        string `json:"status"`
		Reason        string `json:"reason,omitempty"`
		Judged        int    `json:"judged"`
		Supports      []int  `json:"supports"`
		Contradicts   []int  `json:"contradicts"`
		DroppedQuotes int    `json:"dropped_quotes"`
		TookMS        int64  `json:"took_ms"`
	}
	askTableEvent struct {
		Type string `json:"type"`
		InterestsTimeline
		UndatedMarker string `json:"undated_marker"`
		Shown         int    `json:"shown"`
	}
	askGroupsEvent struct {
		Type     string     `json:"type"`
		Groups   []AskGroup `json:"groups"`
		Listed   int        `json:"listed"`
		Ranked   int        `json:"ranked"`
		Wordless bool       `json:"wordless,omitempty"`
		Shown    int        `json:"shown"`
	}
	askDoneEvent struct {
		Type             string     `json:"type"`
		Status           string     `json:"status"`
		Reason           string     `json:"reason,omitempty"`
		Model            string     `json:"model,omitempty"`
		PromptVersion    string     `json:"prompt_version"`
		Route            string     `json:"route"`
		Citations        []int      `json:"citations"`
		Markers          []string   `json:"markers,omitempty"`
		CitedAt          []AskMark  `json:"cited_at"`
		InvalidCitations []string   `json:"invalid_citations,omitempty"`
		Abstained        bool       `json:"abstained,omitempty"`
		Uncited          bool       `json:"uncited,omitempty"`
		Partial          bool       `json:"partial,omitempty"`
		Timings          AskTimings `json:"timings"`
	}
	askErrorEvent struct {
		Type      string `json:"type"`
		Error     string `json:"error"`
		RequestID string `json:"request_id,omitempty"`
	}
)

// AskMark mirrors the openapi AskMark schema: where a citation is
// written in the answer (ask.CitedAt), in code points, and the source (N)
// or the marker it cites.
type AskMark struct {
	Start  int    `json:"start"`
	End    int    `json:"end"`
	N      int    `json:"n,omitempty"`
	Marker string `json:"marker,omitempty"`
}

// AskNames are the fields the openapi AskSource, AskUnreadable and
// AskGroupDocument schemas each inline (name, full_name, untitled): how
// the dashboard's lists name a page an ask reports (ui.DocRef), so that
// the answer's page names it as Search and the Library do: the name a list
// shows, the whole of it, and whether the page has no title of its own.
type AskNames struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Untitled bool   `json:"untitled,omitempty"`
}

// askNames are the names of document id at url, named n.
func askNames(id, url string, n ask.Names) AskNames {
	ref := ui.DocRef{ID: id, Title: n.OwnTitle, Fallback: n.SavedTitle, URL: url}
	return AskNames{Name: ref.Name(), FullName: ref.Hover(), Untitled: ref.Untitled()}
}

// AskSource mirrors the openapi AskSource schema: a page an answer is
// written from, numbered as it cites it, with an evidence source's quote
// and the passages it was shown.
type AskSource struct {
	N        int          `json:"n"`
	DocID    string       `json:"doc_id"`
	URL      string       `json:"url"`
	Title    string       `json:"title"`
	SavedAs  []string     `json:"saved_as,omitempty"`
	Site     string       `json:"site,omitempty"`
	SavedOn  string       `json:"saved_on,omitempty"`
	About    string       `json:"about,omitempty"`
	Quote    string       `json:"quote,omitempty"`
	Passages []AskPassage `json:"passages,omitempty"`
	AskNames
}

// AskPassage mirrors the openapi AskPassage schema: a part of a page's
// text as the prompt showed it, and the section of the page it is from.
type AskPassage struct {
	Text    string `json:"text"`
	Section string `json:"section,omitempty"`
}

// AskGroup mirrors the openapi AskGroup schema: an interest's documents
// among those the list route listed.
type AskGroup struct {
	Marker     string             `json:"marker"`
	InterestID string             `json:"interest_id,omitempty"`
	Label      string             `json:"label"`
	AreaLabel  string             `json:"area_label,omitempty"`
	Count      int                `json:"count"`
	Documents  []AskGroupDocument `json:"documents"`
}

// AskGroupDocument mirrors the openapi AskGroupDocument schema.
type AskGroupDocument struct {
	DocID string         `json:"doc_id"`
	URL   string         `json:"url"`
	Title string         `json:"title"`
	State store.DocState `json:"state"`
	About string         `json:"about,omitempty"`
	AskNames
}

// AskUnreadable mirrors the openapi AskUnreadable schema: a page the
// search found that curio couldn't read, and why.
type AskUnreadable struct {
	DocID  string         `json:"doc_id"`
	URL    string         `json:"url"`
	Title  string         `json:"title"`
	State  store.DocState `json:"state"`
	Reason string         `json:"reason"`
	AskNames
}

// AskTimings mirrors the openapi AskTimings schema, in milliseconds: each
// step's, the first token and the whole from the ask's start.
type AskTimings struct {
	WaitMS       int64 `json:"wait_ms"`
	PlanMS       int64 `json:"plan_ms"`
	SearchMS     int64 `json:"search_ms"`
	RerankMS     int64 `json:"rerank_ms"`
	SourcesMS    int64 `json:"sources_ms"`
	JudgeMS      int64 `json:"judge_ms"`
	PromptEvalMS int64 `json:"prompt_eval_ms"`
	FirstTokenMS int64 `json:"first_token_ms"`
	TotalMS      int64 `json:"total_ms"`
}

// askEventJSON is e on the wire.
func askEventJSON(e ask.Event) any {
	switch e := e.(type) {
	case ask.Progress:
		return askProgressEvent{Type: "progress", Stage: string(e.Stage), Model: e.Model, Pages: e.Pages}
	case ask.Retrieved:
		return retrievalEventJSON(e)
	case ask.Evidence:
		return askEvidenceEvent{Type: "evidence", Status: string(e.Status), Reason: e.Reason, Judged: e.Judged,
			Supports: orEmpty(e.Supports), Contradicts: orEmpty(e.Contradicts), DroppedQuotes: e.DroppedQuotes,
			TookMS: e.Took.Milliseconds()}
	case ask.Table:
		return tableEventJSON(e)
	case ask.Groups:
		return groupsEventJSON(e)
	case ask.Delta:
		return askDeltaEvent{Type: "delta", Text: e.Text}
	case ask.Done:
		t := e.Timings
		return askDoneEvent{Type: "done", Status: string(e.Status), Reason: e.Reason, Model: e.Model,
			PromptVersion: e.PromptVersion, Route: string(e.Route), Citations: orEmpty(e.Citations),
			Markers: e.Markers, CitedAt: marksOf(e.CitedAt), InvalidCitations: e.InvalidCitations,
			Abstained: e.Abstained, Uncited: e.Uncited, Partial: e.Partial,
			Timings: AskTimings{WaitMS: t.Wait.Milliseconds(), PlanMS: t.Plan.Milliseconds(),
				SearchMS: t.Search.Milliseconds(), RerankMS: t.Rerank.Milliseconds(), SourcesMS: t.Sources.Milliseconds(),
				JudgeMS: t.Judge.Milliseconds(), PromptEvalMS: t.PromptEval.Milliseconds(),
				FirstTokenMS: t.FirstToken.Milliseconds(), TotalMS: t.Total.Milliseconds()}}
	}
	panic(fmt.Sprintf("api: an ask event of type %T", e)) // the event types are ask's closed set
}

// orEmpty is xs, or an empty list for none: the wire's lists are always
// arrays.
func orEmpty(xs []int) []int {
	if xs == nil {
		return []int{}
	}
	return xs
}

// marksOf are cited on the wire: an array, empty for none, as the wire's
// lists always are.
func marksOf(cited []ask.CitedAt) []AskMark {
	out := make([]AskMark, len(cited))
	for i, c := range cited {
		out[i] = AskMark{Start: c.Start, End: c.End, N: c.N, Marker: c.Marker}
	}
	return out
}

// retrievalEventJSON is e on the wire.
func retrievalEventJSON(e ask.Retrieved) askRetrievalEvent {
	out := askRetrievalEvent{Type: "retrieval", Route: string(e.Route), Understanding: understandingOf(e.Understanding),
		Reranking: rerankingOf(e.Reranking), Degraded: e.Degraded, Warnings: e.Warnings,
		Sources: make([]AskSource, 0, len(e.Sources))}
	for _, s := range e.Sources {
		src := AskSource{N: s.N, DocID: s.DocID, URL: s.URL, Title: s.Title, SavedAs: s.SavedAs, Site: s.Site,
			SavedOn: s.SavedOn, About: s.About, Quote: s.Quote, AskNames: askNames(s.DocID, s.URL, s.Names)}
		for _, p := range s.Passages {
			src.Passages = append(src.Passages, AskPassage{Text: p.Text, Section: p.Section})
		}
		out.Sources = append(out.Sources, src)
	}
	for _, u := range e.Unreadable {
		out.Unreadable = append(out.Unreadable, AskUnreadable{DocID: u.DocID, URL: u.URL, Title: u.Title,
			State: u.State, Reason: u.Reason, AskNames: askNames(u.DocID, u.URL, u.Names)})
	}
	return out
}

// tableEventJSON is e on the wire: the timeline GET /v1/interests/timeline
// answers, each row the prompt showed marked.
func tableEventJSON(e ask.Table) askTableEvent {
	timeline := timelineView(e.RunID, e.FinishedAt, e.Shape, e.Timeline, e.Window)
	for i := range timeline.Rows {
		timeline.Rows[i].Marker = e.Markers[i]
	}
	return askTableEvent{Type: "table", InterestsTimeline: timeline, UndatedMarker: e.UndatedMarker, Shown: e.Shown}
}

// groupsEventJSON is e on the wire.
func groupsEventJSON(e ask.Groups) askGroupsEvent {
	out := askGroupsEvent{Type: "groups", Groups: make([]AskGroup, 0, len(e.Groups)), Listed: e.Listed,
		Ranked: e.Ranked, Wordless: e.Wordless, Shown: e.Shown}
	for _, g := range e.Groups {
		group := AskGroup{Marker: g.Marker, InterestID: g.InterestID, Label: g.Label, AreaLabel: g.AreaLabel,
			Count: g.Count, Documents: make([]AskGroupDocument, 0, len(g.Documents))}
		for _, d := range g.Documents {
			group.Documents = append(group.Documents, AskGroupDocument{DocID: d.DocID, URL: d.URL, Title: d.Title,
				State: d.State, About: d.About, AskNames: askNames(d.DocID, d.URL, d.Names)})
		}
		out.Groups = append(out.Groups, group)
	}
	return out
}

// AskingHealth mirrors the openapi AskingHealth schema: what the asker
// knows of its model, read from its memory alone, never by asking the
// model. On is whether questions are answered (ask.enabled, or left out,
// a model measured as the answer writer), OnByDefault whether it is on
// for the latter alone. Reason says why no question is answered at all:
// Ask is off, or off by default for an unmeasured model, its provider
// can't stream, or questions and pages would leave this machine for a
// server the answers role doesn't name. The rest is as
// UnderstandingHealth's, for answers.
type AskingHealth struct {
	On                bool                  `json:"on"`
	OnByDefault       bool                  `json:"on_by_default,omitempty"`
	Model             string                `json:"model,omitempty"`
	Reason            string                `json:"reason,omitempty"`
	Check             string                `json:"check,omitempty"`
	CheckedAt         time.Time             `json:"checked_at,omitzero"`
	Unfit             string                `json:"unfit,omitempty"`
	LastStatus        string                `json:"last_status,omitempty"`
	LastAt            time.Time             `json:"last_at,omitzero"`
	TimeoutHoldUntil  time.Time             `json:"timeout_hold_until,omitzero"`
	EvictionHoldUntil time.Time             `json:"eviction_hold_until,omitzero"`
	LastFailure       *UnderstandingFailure `json:"last_failure,omitempty"`
}

// asking is the asker's state on healthz; with no asker, off. Times are
// in UTC.
func (d Deps) asking() AskingHealth {
	if d.Asker == nil {
		return AskingHealth{Reason: ask.OffReason}
	}
	s := d.Asker.State()
	h := AskingHealth{On: s.On, OnByDefault: s.ByDefault, Model: s.Model, Reason: s.Reason, Check: s.Check,
		Unfit:      s.Unfit,
		LastStatus: string(s.Last), CheckedAt: utc(s.CheckedAt), LastAt: utc(s.LastAt),
		TimeoutHoldUntil: utc(s.TimeoutHoldUntil), EvictionHoldUntil: utc(s.EvictionHoldUntil)}
	if f := s.Failure; f.Cause != "" {
		h.LastFailure = &UnderstandingFailure{Cause: f.Cause, At: f.At.UTC(), Error: f.Err}
	}
	return h
}
