package api

import (
	"time"

	"github.com/samsar/curio/internal/rerank"
	"github.com/samsar/curio/internal/retrieve"
)

// Reranker reorders a search's head: the daemon's rerank.Reranker, which
// it always builds, off with its reason when its role isn't in use
// (retrieve.Reranker).
type Reranker = retrieve.Reranker

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

// checkRerank refuses a request's rerank fields the search can't take,
// before anything is planned or embedded (retrieve.CheckOrder).
func checkRerank(req SearchRequest) error {
	if err := retrieve.CheckOrder(req.Rerank, req.Understand, req.RerankOrder); err != nil {
		return &requestError{err: err}
	}
	return nil
}

// rerankingOf is r on the wire; nil for a search that asked for no
// rerank.
func rerankingOf(r *retrieve.Reranking) *Reranking {
	if r == nil {
		return nil
	}
	return &Reranking{Status: string(r.Status), Reason: r.Reason, Model: r.Model, Method: string(r.Method),
		Depth: r.Depth, TookMS: r.Took.Milliseconds(), Order: r.Order, Stale: r.Stale}
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
