package api

import (
	"context"
	"strings"
	"time"
	"unicode"

	"github.com/samsar/curio/internal/rerank"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/understand"
)

// Reranker reorders a search's head: the daemon's rerank.Reranker, which
// it always builds, off with its reason when its role isn't in use. Decide
// says before the search whether its head is reranked and how deep;
// Rerank reorders it. State is what it knows of its model, read from its
// memory alone, for healthz.
type Reranker interface {
	Decide(question string, kind understand.Kind, planned bool) (int, rerank.Outcome)
	Rerank(ctx context.Context, req rerank.Request) (rerank.Outcome, error)
	State() rerank.State
}

// Reranking mirrors the openapi Reranking schema: how the head of the
// ranking was ordered, present exactly when the request sent rerank or
// rerank_order. Reason says why it kept the first ranking, for any status
// but reranked and supplied; Order is the head's document IDs as applied,
// for those two, to send back as rerank_order for another page; Stale, for
// supplied, how many of rerank_order's IDs weren't among the ranking's
// first len(rerank_order).
type Reranking struct {
	Status string   `json:"status"`
	Reason string   `json:"reason,omitempty"`
	Model  string   `json:"model,omitempty"`
	Method string   `json:"method,omitempty"`
	Depth  int      `json:"depth,omitempty"`
	TookMS int64    `json:"took_ms"`
	Order  []string `json:"order,omitempty"`
	Stale  int      `json:"stale,omitempty"`
}

// maxOrderIDBytes is the longest document ID rerank_order takes: a
// document ID is a UUID, and a longer one is no ID of curio's.
const maxOrderIDBytes = 64

// checkRerank refuses a request's rerank fields the search can't take,
// before anything is planned or embedded: both of rerank and
// rerank_order, an order that isn't 1 to rerank.MaxDepth distinct IDs of
// at most maxOrderIDBytes, with no control character, or an order with
// understand. An order is a later page of one ranking, which goes by the
// plan its first page returned: asking the planner again could plan
// another ranking, whose head the order wasn't made for.
func checkRerank(req SearchRequest) error {
	switch {
	case req.RerankOrder == nil:
		return nil
	case req.Rerank:
		return badRequest("send rerank or rerank_order, not both")
	case len(req.RerankOrder) < 1 || len(req.RerankOrder) > rerank.MaxDepth:
		return badRequest("rerank_order must hold 1 to %d document IDs, got %d", rerank.MaxDepth, len(req.RerankOrder))
	}
	seen := make(map[string]bool, len(req.RerankOrder))
	for i, id := range req.RerankOrder {
		switch {
		case id == "":
			return badRequest("rerank_order[%d] is empty", i)
		case len(id) > maxOrderIDBytes:
			return badRequest("rerank_order[%d] is over %d bytes", i, maxOrderIDBytes)
		case strings.ContainsFunc(id, unicode.IsControl):
			return badRequest("rerank_order[%d] holds a control character", i)
		case seen[id]:
			return badRequest("rerank_order[%d] repeats an earlier ID", i)
		}
		seen[id] = true
	}
	if req.Understand {
		return badRequest("send rerank_order with the plan its first page returned, not understand")
	}
	return nil
}

// rerankPlan is what a search does about its head, decided before it
// runs: the depth of the ranking it reads for it (0 for none), and the
// reranking it reports so far.
type rerankPlan struct {
	depth     int
	reranking *Reranking
}

// planRerank decides req's rerank before the search, asking no model: an
// order supplied is applied at its own length; rerank asks the reranker
// how deep to read, or why not; neither reads nothing.
func (d Deps) planRerank(req SearchRequest, plan *understand.Plan) rerankPlan {
	switch {
	case req.RerankOrder != nil:
		return rerankPlan{depth: len(req.RerankOrder), reranking: &Reranking{Status: string(rerank.StatusSupplied)}}
	case !req.Rerank:
		return rerankPlan{}
	case d.Reranker == nil:
		return rerankPlan{reranking: &Reranking{Status: string(rerank.StatusOff), Reason: rerank.OffReason}}
	}
	var kind understand.Kind
	if plan != nil {
		kind = plan.Kind
	}
	depth, early := d.Reranker.Decide(req.Query, kind, plan != nil)
	if depth == 0 {
		return rerankPlan{reranking: rerankingOf(early)}
	}
	return rerankPlan{depth: depth}
}

// rerankingOf is out on the wire.
func rerankingOf(out rerank.Outcome) *Reranking {
	r := &Reranking{Status: string(out.Status), Model: out.Model, Method: string(out.Method), Depth: out.Depth,
		TookMS: out.Took.Milliseconds(), Order: out.Order}
	if out.Status != rerank.StatusReranked {
		r.Reason = out.Reason
	}
	return r
}

// applyRerank orders hits, the first of the ranking (at least rp.depth of
// them, when the ranking holds that many), as rp and req say: by the order
// req supplied, asking no model, or by the reranker's over the first
// rp.depth; every other outcome keeps the first ranking whole. Only the
// caller's context ending is an error; a failed read of the candidates is
// status unavailable, logged at WARN.
func (d Deps) applyRerank(ctx context.Context, req SearchRequest, plan *understand.Plan, rp rerankPlan,
	hits []search.Hit) ([]search.Hit, *Reranking, error) {
	if rp.depth == 0 {
		return hits, rp.reranking, nil
	}
	if req.RerankOrder != nil {
		out, stale := reorderHits(hits, req.RerankOrder)
		rp.reranking.Order, rp.reranking.Stale = headIDs(out, len(req.RerankOrder)), stale
		return out, rp.reranking, nil
	}
	began := time.Now()
	head := hits[:min(rp.depth, len(hits))]
	cands, err := rerank.ReadCandidates(ctx, rerank.Readers{Fields: d.Index, Summaries: d.summaryReader()},
		d.TenantID, head)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, nil, cerr
		}
		d.Log.Warn("search reranking: couldn't read the candidates; the search keeps its first ranking", "err", err)
		return hits, &Reranking{Status: string(rerank.StatusUnavailable), Reason: "couldn't read the candidates",
			TookMS: time.Since(began).Milliseconds()}, nil
	}
	var kind understand.Kind
	if plan != nil {
		kind = plan.Kind
	}
	out, err := d.Reranker.Rerank(ctx, rerank.Request{Question: req.Query, Kind: kind, Planned: plan != nil,
		Candidates: cands})
	if err != nil {
		return nil, nil, err
	}
	reranking := rerankingOf(out)
	reranking.TookMS = time.Since(began).Milliseconds()
	if out.Status != rerank.StatusReranked {
		return hits, reranking, nil
	}
	reordered, _ := reorderHits(hits, out.Order)
	return reordered, reranking, nil
}

// reorderHits is hits laid out by order (rerank.Apply), and how many of
// order's IDs weren't among its head.
func reorderHits(hits []search.Hit, order []string) ([]search.Hit, int) {
	ids := make([]string, len(hits))
	byID := make(map[string]search.Hit, len(hits))
	for i, h := range hits {
		ids[i] = h.Document.ID
		byID[h.Document.ID] = h
	}
	applied, stale := rerank.Apply(ids, order)
	out := make([]search.Hit, len(applied))
	for i, id := range applied {
		out[i] = byID[id]
	}
	return out, stale
}

// headIDs are the IDs of hits' first n.
func headIDs(hits []search.Hit, n int) []string {
	n = min(n, len(hits))
	out := make([]string, n)
	for i, h := range hits[:n] {
		out[i] = h.Document.ID
	}
	return out
}

// summaryReader is the store the candidates' summaries are read from;
// without one, none is read.
func (d Deps) summaryReader() rerank.SummaryReader {
	if d.Summaries == nil {
		return noSummaries{}
	}
	return d.Summaries
}

// noSummaries reads no summary.
type noSummaries struct{}

func (noSummaries) CurrentSummaries(context.Context, string, []string) (map[string]*store.Summary, error) {
	return map[string]*store.Summary{}, nil
}

// RerankingHealth mirrors the openapi RerankingHealth schema: what the
// reranker knows of its model, read from its memory alone, never by
// asking the model. On is whether searches may be reranked
// (search.rerank, or a measured model while it is left out), and
// OnByDefault whether it is on for the latter alone. Reason says
// why no search is reranked at all: reranking is off, off by default, its
// provider can't be asked as its routes need, or questions and pages would
// leave this machine for a server the reranker role doesn't name. The
// rest is as UnderstandingHealth's, for reranks.
type RerankingHealth struct {
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

// reranking is the reranker's state on healthz; with no reranker, off.
// Times are in UTC.
func (d Deps) reranking() RerankingHealth {
	if d.Reranker == nil {
		return RerankingHealth{Reason: rerank.OffReason}
	}
	s := d.Reranker.State()
	h := RerankingHealth{On: s.On, OnByDefault: s.ByDefault, Model: s.Model, Reason: s.Reason, Check: s.Check, Unfit: s.Unfit,
		LastStatus: string(s.Last), CheckedAt: utc(s.CheckedAt), LastAt: utc(s.LastAt),
		TimeoutHoldUntil: utc(s.TimeoutHoldUntil), EvictionHoldUntil: utc(s.EvictionHoldUntil)}
	if f := s.Failure; f.Cause != "" {
		h.LastFailure = &UnderstandingFailure{Cause: f.Cause, At: f.At.UTC(), Error: f.Err}
	}
	return h
}

// rerankWindow is the engine's window for a search whose head is read
// rp.depth deep: from the top, to past the response's window and the
// head both.
func rerankWindow(sreq search.Request, rp rerankPlan, k int) search.Request {
	if rp.depth > 0 {
		sreq.K, sreq.Offset = max(rp.depth, sreq.Offset+k), 0
	}
	return sreq
}

// hitWindow is hits' [offset, offset+k).
func hitWindow(hits []search.Hit, offset, k int) []search.Hit {
	if offset >= len(hits) {
		return nil
	}
	return hits[offset:min(offset+k, len(hits))]
}
