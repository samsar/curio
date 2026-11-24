package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// AskRequest is the POST /v1/ask body. Understand is on unless set false,
// and Rerank left out is the daemon's ask.rerank; Sources 0 is the
// daemon's ask.sources, and Mode "" routes the question as the daemon
// decides.
type AskRequest struct {
	Question   string         `json:"question"`
	Filters    *SearchFilters `json:"filters,omitempty"`
	Understand *bool          `json:"understand,omitempty"`
	Rerank     *bool          `json:"rerank,omitempty"`
	Sources    int            `json:"sources,omitempty"`
	Mode       string         `json:"mode,omitempty"`
}

// AskEvent is one line of an ask's stream, as api's AskEvent schemas
// write it, told apart by Type: progress (Stage, Model, Pages), retrieval
// (Route, Understanding, Reranking, Degraded, Warnings, Sources,
// Unreadable), evidence (Status, Reason, Judged, Supports, Contradicts,
// DroppedQuotes, TookMS), table (the timeline's Run, Period, Periods,
// Rows, Dated, Undated, Window, with UndatedMarker and Shown: Table),
// groups (Groups, Listed, Ranked, Wordless, Shown), delta (Text), done
// (Status, Reason, Model, PromptVersion, Route, Citations, Markers,
// CitedAt, InvalidCitations, Abstained, Uncited, Partial, Timings) or
// error (Error, RequestID). Raw is the line as the daemon sent it, fields
// this client doesn't know included.
type AskEvent struct {
	Type string `json:"type"`

	Stage string `json:"stage,omitempty"`
	Model string `json:"model,omitempty"`
	Pages int    `json:"pages,omitempty"`

	Route         string          `json:"route,omitempty"`
	Understanding *Understanding  `json:"understanding,omitempty"`
	Reranking     *Reranking      `json:"reranking,omitempty"`
	Degraded      bool            `json:"degraded,omitempty"`
	Warnings      []string        `json:"warnings,omitempty"`
	Sources       []AskSource     `json:"sources,omitempty"`
	Unreadable    []AskUnreadable `json:"unreadable,omitempty"`

	Judged        int   `json:"judged,omitempty"`
	Supports      []int `json:"supports,omitempty"`
	Contradicts   []int `json:"contradicts,omitempty"`
	DroppedQuotes int   `json:"dropped_quotes,omitempty"`
	TookMS        int64 `json:"took_ms,omitempty"`

	Run           *TimelineRun    `json:"run,omitempty"`
	Period        string          `json:"period,omitempty"`
	Periods       []string        `json:"periods,omitempty"`
	Rows          []TimelineRow   `json:"rows,omitempty"`
	Dated         int             `json:"dated,omitempty"`
	Undated       int             `json:"undated,omitempty"`
	Window        *TimelineWindow `json:"window,omitempty"`
	UndatedMarker string          `json:"undated_marker,omitempty"`

	Groups   []AskGroup `json:"groups,omitempty"`
	Listed   int        `json:"listed,omitempty"`
	Ranked   int        `json:"ranked,omitempty"`
	Wordless bool       `json:"wordless,omitempty"`
	Shown    int        `json:"shown,omitempty"`

	Text string `json:"text,omitempty"`

	Status           string     `json:"status,omitempty"`
	Reason           string     `json:"reason,omitempty"`
	PromptVersion    string     `json:"prompt_version,omitempty"`
	Citations        []int      `json:"citations,omitempty"`
	Markers          []string   `json:"markers,omitempty"`
	CitedAt          []AskMark  `json:"cited_at,omitempty"`
	InvalidCitations []string   `json:"invalid_citations,omitempty"`
	Abstained        bool       `json:"abstained,omitempty"`
	Uncited          bool       `json:"uncited,omitempty"`
	Partial          bool       `json:"partial,omitempty"`
	Timings          AskTimings `json:"timings,omitzero"`

	Error     string `json:"error,omitempty"`
	RequestID string `json:"request_id,omitempty"`

	Raw []byte `json:"-"`
}

// Table is a table event's timeline, as GET /v1/interests/timeline
// answers it; nil for any other event.
func (e AskEvent) Table() *InterestsTimeline {
	if e.Type != "table" || e.Run == nil {
		return nil
	}
	return &InterestsTimeline{Run: *e.Run, Period: e.Period, Periods: e.Periods, Rows: e.Rows, Dated: e.Dated,
		Undated: e.Undated, Window: e.Window}
}

// AskMark mirrors api.AskMark.
type AskMark struct {
	Start  int    `json:"start"`
	End    int    `json:"end"`
	N      int    `json:"n,omitempty"`
	Marker string `json:"marker,omitempty"`
}

// AskNames mirrors api.AskNames.
type AskNames struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Untitled bool   `json:"untitled,omitempty"`
}

// AskSource mirrors api.AskSource.
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

// AskPassage mirrors api.AskPassage.
type AskPassage struct {
	Text    string `json:"text"`
	Section string `json:"section,omitempty"`
}

// AskGroup mirrors api.AskGroup.
type AskGroup struct {
	Marker     string             `json:"marker"`
	InterestID string             `json:"interest_id,omitempty"`
	Label      string             `json:"label"`
	AreaLabel  string             `json:"area_label,omitempty"`
	Count      int                `json:"count"`
	Documents  []AskGroupDocument `json:"documents"`
}

// AskGroupDocument mirrors api.AskGroupDocument.
type AskGroupDocument struct {
	DocID string `json:"doc_id"`
	URL   string `json:"url"`
	Title string `json:"title"`
	State string `json:"state"`
	About string `json:"about,omitempty"`
	AskNames
}

// AskUnreadable mirrors api.AskUnreadable.
type AskUnreadable struct {
	DocID  string `json:"doc_id"`
	URL    string `json:"url"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Reason string `json:"reason"`
	AskNames
}

// AskTimings mirrors api.AskTimings.
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

// The bounds of reading an ask's stream. Its first line may wait for the
// daemon's start of an answer, and the daemon writes a line at least
// every 10 s however long its model takes, so 30 s without one is a
// daemon that hung; the stream as a whole is the daemon's to bound
// (ask.timeout_seconds). A line is an event, the retrieval event the
// largest: its sources' passages, well under the cap.
const (
	askIdle     = 30 * time.Second
	maxAskLine  = 1 << 20
	askLineSize = 64 << 10
)

// ErrAskStalled is an ask whose stream went quiet past its idle bound.
var ErrAskStalled = errors.New("curio-daemon sent nothing for 30 s")

// Ask sends req and passes each event of the stream to fn as it arrives,
// until the stream ends or fn returns an error, which comes back as is.
// The stream ignores the client's request timeout: it is bounded by ctx,
// whose end aborts the request (and with it the daemon's ask), and by an
// idle bound between lines. A refused request is an *APIError.
func (c *Client) Ask(ctx context.Context, req AskRequest, fn func(AskEvent) error) error {
	idle := c.askIdle
	if idle <= 0 {
		idle = askIdle
	}
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	timer := time.AfterFunc(idle, func() { cancel(ErrAskStalled) })
	defer timer.Stop()

	untimed := &http.Client{Transport: c.http.Transport}
	resp, err := c.sendWith(ctx, untimed, http.MethodPost, "/v1/ask", req)
	if err != nil {
		return askError(ctx, err)
	}
	defer resp.Body.Close()
	lines := bufio.NewScanner(resp.Body)
	lines.Buffer(make([]byte, 0, askLineSize), maxAskLine)
	for lines.Scan() {
		timer.Reset(idle)
		raw := bytes.TrimSpace(lines.Bytes())
		if len(raw) == 0 {
			continue
		}
		var e AskEvent
		if err := json.Unmarshal(raw, &e); err != nil {
			return fmt.Errorf("POST /v1/ask: decode an event: %w", err)
		}
		e.Raw = append([]byte(nil), raw...)
		if err := fn(e); err != nil {
			return err
		}
	}
	if err := lines.Err(); err != nil {
		return askError(ctx, fmt.Errorf("POST /v1/ask: read the stream: %w", err))
	}
	return nil
}

// askError is err, an ask's failure, as the stall it was when its idle
// bound cut it.
func askError(ctx context.Context, err error) error {
	if errors.Is(context.Cause(ctx), ErrAskStalled) {
		return ErrAskStalled
	}
	return err
}
