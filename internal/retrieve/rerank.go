package retrieve

import (
	"context"
	"log/slog"
	"time"

	"github.com/samsar/curio/internal/rerank"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/understand"
)

// rerankPlan is what a search does about its head, decided before it
// runs: the depth of the ranking it reads for it (0 for none), and the
// reranking it reports so far.
type rerankPlan struct {
	depth     int
	reranking *Reranking
}

// planRerank decides q's rerank before the search, asking no model: an
// order supplied is applied at its own length; rerank asks the reranker
// how deep to read, or why not; neither reads nothing.
func (r Retriever) planRerank(q Query, plan *understand.Plan) rerankPlan {
	switch {
	case q.RerankOrder != nil:
		return rerankPlan{depth: len(q.RerankOrder), reranking: &Reranking{Status: rerank.StatusSupplied}}
	case !q.Rerank:
		return rerankPlan{}
	case r.Reranker == nil:
		return rerankPlan{reranking: &Reranking{Status: rerank.StatusOff, Reason: rerank.OffReason}}
	}
	var kind understand.Kind
	if plan != nil {
		kind = plan.Kind
	}
	depth, early := r.Reranker.Decide(q.Question, kind, plan != nil)
	if depth == 0 {
		return rerankPlan{reranking: rerankingOf(early)}
	}
	return rerankPlan{depth: depth}
}

// rerankingOf is out as a Retrieval reports it.
func rerankingOf(out rerank.Outcome) *Reranking {
	r := &Reranking{Status: out.Status, Model: out.Model, Method: out.Method, Depth: out.Depth, Took: out.Took,
		Order: out.Order}
	if out.Status != rerank.StatusReranked {
		r.Reason = out.Reason
	}
	return r
}

// applyRerank orders hits, the first of the ranking (at least rp.depth of
// them, when the ranking holds that many), as rp and q say: by the order
// q supplied, asking no model, or by the reranker's over the first
// rp.depth; every other outcome keeps the first ranking whole. Only the
// caller's context ending is an error; a failed read of the candidates is
// status unavailable, logged at WARN.
func (r Retriever) applyRerank(ctx context.Context, q Query, plan *understand.Plan, rp rerankPlan,
	hits []search.Hit) ([]search.Hit, *Reranking, error) {
	if rp.depth == 0 {
		return hits, rp.reranking, nil
	}
	if q.RerankOrder != nil {
		out, stale := reorderHits(hits, q.RerankOrder)
		rp.reranking.Order, rp.reranking.Stale = headIDs(out, len(q.RerankOrder)), stale
		return out, rp.reranking, nil
	}
	began := time.Now()
	head := hits[:min(rp.depth, len(hits))]
	cands, err := rerank.ReadCandidates(ctx, rerank.Readers{Fields: r.Fields, Summaries: r.summaryReader()},
		r.TenantID, head)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, nil, cerr
		}
		r.log().Warn("search reranking: couldn't read the candidates; the search keeps its first ranking", "err", err)
		return hits, &Reranking{Status: rerank.StatusUnavailable, Reason: "couldn't read the candidates",
			Took: time.Since(began)}, nil
	}
	var kind understand.Kind
	if plan != nil {
		kind = plan.Kind
	}
	out, err := r.Reranker.Rerank(ctx, rerank.Request{Question: q.Question, Kind: kind, Planned: plan != nil,
		Candidates: cands})
	if err != nil {
		return nil, nil, err
	}
	reranking := rerankingOf(out)
	reranking.Took = time.Since(began)
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
func (r Retriever) summaryReader() rerank.SummaryReader {
	if r.Summaries == nil {
		return noSummaries{}
	}
	return r.Summaries
}

// noSummaries reads no summary.
type noSummaries struct{}

func (noSummaries) CurrentSummaries(context.Context, string, []string) (map[string]*store.Summary, error) {
	return map[string]*store.Summary{}, nil
}

// log is the retriever's logger; slog.Default() when unset.
func (r Retriever) log() *slog.Logger {
	if r.Log == nil {
		return slog.Default()
	}
	return r.Log
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
