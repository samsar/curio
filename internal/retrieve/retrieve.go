// Package retrieve is a search's path from a question to its ranked hits:
// understanding it (a plan supplied, or the planner's), laying the
// request's filters over the plan's, searching, and ordering the head of
// the ranking (an order supplied, or the reranker's). POST /v1/search,
// curio ask and the developer's ask evaluation all retrieve through it, so
// a fix to one is a fix to every one. Checking a request as its caller
// receives it stays the caller's.
package retrieve

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/samsar/curio/internal/rerank"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/understand"
)

// Planner understands search questions: the daemon's understand.Planner,
// nil while search.understand is off. Timeout is how long a plan may take
// (search.understand_timeout_seconds), which the dashboard's note for a
// plan that took longer names. State is what it knows of its model, read
// from its memory alone, for healthz.
type Planner interface {
	Plan(ctx context.Context, question string) (understand.Outcome, error)
	Timeout() time.Duration
	State() understand.State
}

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

// Retriever retrieves for one tenant. A nil Planner understands nothing
// (status off) and a nil Reranker reranks nothing (status off); a nil
// Summaries reads no summary for the reranker's candidates.
type Retriever struct {
	Engine    *search.Engine
	Planner   Planner
	Reranker  Reranker
	Fields    rerank.FieldsReader
	Summaries rerank.SummaryReader
	TenantID  string
	Log       *slog.Logger
}

// Query is what to retrieve: the question; the window [Offset, Offset+K)
// of its ranking (K 0 is the engine's default); the request's own filters,
// which win over a plan's dimension by dimension; Understand to ask the
// planner, or Plan to search by a plan supplied, not both; and Rerank to
// ask the reranker, or RerankOrder to order the head by an order supplied,
// not both. The caller has checked K, Offset, the filters alone and the
// order.
type Query struct {
	Question    string
	K           int
	Offset      int
	Filters     search.FilterRequest
	Understand  bool
	Plan        *understand.Plan
	Rerank      bool
	RerankOrder []string
}

// Understanding is how the question was understood, when the query asked
// for understanding or supplied a plan. Reason says why there is no plan,
// for any status but planned and supplied; Plan is the plan as applied,
// and Dropped its filters left out, by the planner's guards or because
// the query's filters set their dimension.
type Understanding struct {
	Status  understand.Status
	Reason  string
	Model   string
	Took    time.Duration
	Plan    *understand.Plan
	Dropped []understand.Dropped
}

// Reranking is how the head of the ranking was ordered, when the query
// asked for a rerank or supplied an order. Reason says why it kept the
// first ranking, for any status but reranked and supplied; Order is the
// head's document IDs as applied, for those two; Stale, for supplied, how
// many of the order's IDs weren't among the ranking's head.
type Reranking struct {
	Status rerank.Status
	Reason string
	Model  string
	Method rerank.Method
	Depth  int
	Took   time.Duration
	Order  []string
	Stale  int
}

// Timings are how long each step took.
type Timings struct {
	Plan   time.Duration
	Search time.Duration
	Rerank time.Duration
}

// Retrieval is what a question retrieved: the engine's result with its
// hits in the order applied and cut to the query's window; the plan it was
// searched by (nil for its words alone), the kind of which routes a
// rerank; how it was understood and reranked (each nil when the query
// asked neither); and the timings.
type Retrieval struct {
	Result        search.Result
	Plan          *understand.Plan
	Understanding *Understanding
	Reranking     *Reranking
	Timings       Timings
}

// QueryError is a query Retrieve refuses as made: understanding asked and
// a plan supplied, a plan understand.Validate refuses, or filters that
// won't resolve once a plan's are laid under them. Its message is what to
// fix.
type QueryError struct{ Err error }

func (e *QueryError) Error() string { return e.Err.Error() }
func (e *QueryError) Unwrap() error { return e.Err }

// maxOrderIDBytes is the longest document ID an order takes: a document
// ID is a UUID, and a longer one is no ID of curio's.
const maxOrderIDBytes = 64

// CheckOrder refuses a query's rerank fields the search can't take,
// before anything is planned or embedded: both of rerank and an order, an
// order that isn't 1 to rerank.MaxDepth distinct IDs of at most
// maxOrderIDBytes, with no control character, or an order with
// understand. An order is a later page of one ranking, which goes by the
// plan its first page returned: asking the planner again could plan
// another ranking, whose head the order wasn't made for.
func CheckOrder(rerankAsked, understandAsked bool, order []string) error {
	switch {
	case order == nil:
		return nil
	case rerankAsked:
		return errors.New("send rerank or rerank_order, not both")
	case len(order) < 1 || len(order) > rerank.MaxDepth:
		return fmt.Errorf("rerank_order must hold 1 to %d document IDs, got %d", rerank.MaxDepth, len(order))
	}
	seen := make(map[string]bool, len(order))
	for i, id := range order {
		switch {
		case id == "":
			return fmt.Errorf("rerank_order[%d] is empty", i)
		case len(id) > maxOrderIDBytes:
			return fmt.Errorf("rerank_order[%d] is over %d bytes", i, maxOrderIDBytes)
		case strings.ContainsFunc(id, unicode.IsControl):
			return fmt.Errorf("rerank_order[%d] holds a control character", i)
		case seen[id]:
			return fmt.Errorf("rerank_order[%d] repeats an earlier ID", i)
		}
		seen[id] = true
	}
	if understandAsked {
		return errors.New("send rerank_order with the plan its first page returned, not understand")
	}
	return nil
}

// CheckPlan refuses what Retrieve would refuse of q's plan before
// searching, asking no model: understanding asked with a plan supplied, a
// plan understand.Validate refuses, or a supplied plan's filters that
// won't resolve under q's own. The planner's own plans are grounded, so a
// caller that must refuse a query before it begins answering checks it
// here. A refusal is a *QueryError.
func CheckPlan(q Query) error {
	switch {
	case q.Plan != nil && q.Understand:
		return &QueryError{Err: errors.New("send understand or plan, not both")}
	case q.Plan == nil:
		return nil
	}
	if err := understand.Validate(*q.Plan); err != nil {
		return &QueryError{Err: err}
	}
	merged, _ := mergeFilters(q.Filters, q.Plan.Filters)
	if _, err := merged.Resolve(); err != nil {
		return &QueryError{Err: err}
	}
	return nil
}

// Retrieve runs q through understanding, the search and the rerank.
// Understanding failing is no error, nor is reranking failing: the
// question is searched by its words, or the first ranking kept whole, the
// outcome saying why. Only a refused query (*QueryError), the caller's
// context ending and the engine's failures are errors.
func (r Retriever) Retrieve(ctx context.Context, q Query) (Retrieval, error) {
	start := time.Now()
	u, err := r.Understand(ctx, q)
	if err != nil {
		return Retrieval{}, err
	}
	plan, understanding := u.Plan, u.Understanding
	planned := time.Now()
	sreq := search.Request{TenantID: r.TenantID, Query: q.Question, K: q.K, Offset: q.Offset}
	if plan != nil {
		sreq.Rewrites, sreq.Names = plan.Rewrites, plan.Names
	}
	if sreq.Filters, err = u.Filters.Resolve(); err != nil {
		return Retrieval{}, &QueryError{Err: err}
	}
	// A reranked head is read from the top whatever the window: the
	// engine is asked for both, and the window cut from the head reordered
	// and the first ranking's rest.
	k := cmp.Or(q.K, r.Engine.DefaultK())
	rp := r.planRerank(q, plan)
	res, err := r.Engine.Search(ctx, rerankWindow(sreq, rp, k))
	if err != nil {
		return Retrieval{}, err
	}
	searched := time.Now()
	hits, reranking, err := r.applyRerank(ctx, q, plan, rp, res.Items)
	if err != nil {
		return Retrieval{}, err
	}
	if rp.depth > 0 {
		hits = hitWindow(hits, q.Offset, k)
	}
	res.Items = hits
	return Retrieval{Result: *res, Plan: plan, Understanding: understanding, Reranking: reranking,
		Timings: Timings{Plan: planned.Sub(start), Search: searched.Sub(planned), Rerank: time.Since(searched)}}, nil
}

// Understood is a query understood, before anything is searched: the
// plan it is searched by (nil for its words alone), how it was understood
// (nil when it asked for neither understanding nor a plan; its Plan as
// applied, and Dropped what of it the query's own filters left out), and
// the filters a search by it applies, the query's laid over the plan's.
type Understood struct {
	Plan          *understand.Plan
	Understanding *Understanding
	Filters       search.FilterRequest
}

// Understand is Retrieve's first step alone: q's plan, supplied or the
// planner's, and its filters laid under q's own. The interests route of
// an ask takes it without a search. Only a refused plan (*QueryError) and
// the caller's context ending are errors.
func (r Retriever) Understand(ctx context.Context, q Query) (Understood, error) {
	plan, understanding, err := r.understand(ctx, q)
	if err != nil {
		return Understood{}, err
	}
	out := Understood{Plan: plan, Understanding: understanding, Filters: q.Filters}
	if plan != nil {
		var dropped []understand.Dropped
		out.Filters, dropped = mergeFilters(q.Filters, plan.Filters)
		applied := *plan
		applied.Filters = withoutDropped(plan.Filters, dropped)
		understanding.Plan = &applied
		understanding.Dropped = append(understanding.Dropped, dropped...)
	}
	return out, nil
}

// understand is q's plan and how it came to be: the plan it supplied,
// checked by understand.Validate; the planner's, when it asked for
// understanding and the planner gave one; or none, with the status that
// says why. A query that asked for neither has no Understanding. Only the
// caller's context ending is an error besides a refused plan.
func (r Retriever) understand(ctx context.Context, q Query) (*understand.Plan, *Understanding, error) {
	switch {
	case q.Plan != nil && q.Understand:
		return nil, nil, &QueryError{Err: errors.New("send understand or plan, not both")}
	case q.Plan != nil:
		p := *q.Plan
		if err := understand.Validate(p); err != nil {
			return nil, nil, &QueryError{Err: err}
		}
		return &p, &Understanding{Status: understand.StatusSupplied}, nil
	case !q.Understand:
		return nil, nil, nil
	case r.Planner == nil:
		return nil, &Understanding{Status: understand.StatusOff, Reason: understand.OffReason}, nil
	}
	out, err := r.Planner.Plan(ctx, q.Question)
	if err != nil {
		return nil, nil, err
	}
	u := &Understanding{Status: out.Status, Model: out.Model, Took: out.Took, Dropped: out.Dropped}
	if out.Status != understand.StatusPlanned {
		u.Reason = out.Reason
	}
	return out.Plan, u, nil
}

// mergeFilters lays a plan's filters under the request's: each dimension
// the request sets is the request's, and the plan's of that dimension, if
// it set one, is dropped. A window (saved, published) is one dimension,
// its two bounds together; include_undated goes with the request's dates
// when it sets any, and with the plan's otherwise.
func mergeFilters(req, plan search.FilterRequest) (search.FilterRequest, []understand.Dropped) {
	out := req
	var dropped []understand.Dropped
	lists := []struct {
		field     string
		req, plan []string
		to        *[]string
	}{
		{"content_type", req.ContentType, plan.ContentType, &out.ContentType},
		{"host", req.Host, plan.Host, &out.Host},
		{"source", req.Source, plan.Source, &out.Source},
		{"site", req.Site, plan.Site, &out.Site},
		{"folder", req.Folder, plan.Folder, &out.Folder},
	}
	for _, l := range lists {
		switch {
		case len(l.req) == 0:
			*l.to = l.plan
		case len(l.plan) > 0:
			dropped = append(dropped, understand.Dropped{Field: l.field, Reason: "the request sets " + l.field})
		}
	}
	windows := []struct {
		field                 string
		reqSet, planSet       bool
		planAfter, planBefore string
		toAfter, toBefore     *string
	}{
		{"saved", req.SavedAfter != "" || req.SavedBefore != "", plan.SavedAfter != "" || plan.SavedBefore != "",
			plan.SavedAfter, plan.SavedBefore, &out.SavedAfter, &out.SavedBefore},
		{"published", req.PublishedAfter != "" || req.PublishedBefore != "",
			plan.PublishedAfter != "" || plan.PublishedBefore != "", plan.PublishedAfter, plan.PublishedBefore,
			&out.PublishedAfter, &out.PublishedBefore},
	}
	reqDates := false
	for _, w := range windows {
		reqDates = reqDates || w.reqSet
		switch {
		case !w.reqSet:
			*w.toAfter, *w.toBefore = w.planAfter, w.planBefore
		case w.planSet:
			dropped = append(dropped, understand.Dropped{Field: w.field,
				Reason: fmt.Sprintf("the request sets %s_after or %s_before", w.field, w.field)})
		}
	}
	switch {
	case !reqDates:
		out.IncludeUndated = plan.IncludeUndated
	case plan.IncludeUndated && !req.IncludeUndated:
		dropped = append(dropped, understand.Dropped{Field: "include_undated",
			Reason: "the request sets its own dates, without include_undated"})
	}
	return out, dropped
}

// withoutDropped is f less the dimensions dropped names: what of a plan's
// filters a search applied.
func withoutDropped(f search.FilterRequest, dropped []understand.Dropped) search.FilterRequest {
	for _, d := range dropped {
		f = WithoutDimension(f, d.Field)
	}
	return f
}

// WithoutDimension is f less the dimension field names: content_type,
// host, source, site, folder, saved or published (a window, both bounds),
// or include_undated. Taking out its last date filter takes
// include_undated with it, which keeps nothing alone.
func WithoutDimension(f search.FilterRequest, field string) search.FilterRequest {
	switch field {
	case "content_type":
		f.ContentType = nil
	case "host":
		f.Host = nil
	case "source":
		f.Source = nil
	case "site":
		f.Site = nil
	case "folder":
		f.Folder = nil
	case "saved":
		f.SavedAfter, f.SavedBefore = "", ""
	case "published":
		f.PublishedAfter, f.PublishedBefore = "", ""
	case "include_undated":
		f.IncludeUndated = false
	}
	if f.SavedAfter == "" && f.SavedBefore == "" && f.PublishedAfter == "" && f.PublishedBefore == "" {
		f.IncludeUndated = false
	}
	return f
}
