package config

import "github.com/samsar/curio/internal/ollama"

// RerankPoint is a writing model as the reranker, as cmd/searcheval
// measured it on a real library against the pick rule (docs/decisions.md
// "Choosing the reranker (written before the grid runs)"): whether the
// point it passed at needs the candidates' summaries, so reranking is on
// by default only while summaries are written. Every point is measured
// on understood searches, so on by default needs search.understand too.
type RerankPoint struct {
	NeedsSummaries bool
}

// rerankMeasured are the models that passed the pick rule as the
// reranker, by name as ollama.NormalizeModel writes it. A model not listed
// is unmeasured: with search.rerank left out it doesn't rerank, and true
// turns it on, the fitness check guarding it. On the owner's 2.7 copy
// gemma4:26b passed the rule as amended on 2026-11-04, after the grid ran
// (the known items' R@10 held to what planning alone keeps, as S@1
// already was): the topical kinds reranked listwise, at all queries'
// nDCG@10 0.796 against 0.762 planned. Without the summaries the same
// point falls to 0.787, under the bar of 0.792, so it needs them. As
// written before the grid, no point passed (docs/decisions.md "Reranking:
// a reranker role over the ranking's head, routed by the plan's kind").
var rerankMeasured = map[string]RerankPoint{
	"gemma4:26b": {NeedsSummaries: true},
}

// RerankMeasured is model's measured point as the reranker, and whether it
// was measured. Names match as Ollama's do: case aside, and an untagged
// name being its :latest.
func RerankMeasured(model string) (RerankPoint, bool) {
	p, ok := rerankMeasured[ollama.NormalizeModel(model)]
	return p, ok
}

// RerankOn reports whether searches may be reranked: as search.rerank
// says when config.yaml sets it, and left out, only with a model measured
// as the reranker, while search understanding is on (every point is
// measured on understood searches), and, when its measured point needs
// the candidates' summaries, only while they are written (SummariesOn).
func (c Config) RerankOn() bool {
	if c.Search.Rerank != nil {
		return *c.Search.Rerank
	}
	p, ok := RerankMeasured(c.Generation.Role(RoleReranker).Model)
	return ok && c.Search.Understand && (!p.NeedsSummaries || c.SummariesOn())
}

// RerankOffByDefault reports whether reranking is off because config.yaml
// leaves search.rerank out: its model isn't measured, or its measured point
// needs the summaries, which aren't written.
func (c Config) RerankOffByDefault() bool {
	return c.Search.Rerank == nil && !c.RerankOn()
}
