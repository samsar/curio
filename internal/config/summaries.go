package config

import (
	"fmt"
	"math"
	"time"
)

// SummaryCost is what a writing model costs as the summaries' writer, as
// cmd/searcheval measured it on a sample of a real library's pages: the
// median seconds a page takes at the queue's normal throttle, whether it
// passed the writer's fitness check, and the share of the summaries
// checked by hand that state something their page doesn't.
type SummaryCost struct {
	SecondsPerPage float64
	Fit            bool
	Unfaithful     float64
}

// The bars a model should meet to write the summaries: a pass over a
// library of summaryPassPages taking at most summaryPassLimit at normal
// throttle, and at most maxUnfaithful of its summaries stating what the
// page doesn't.
const (
	summaryPassPages = 5000
	summaryPassLimit = 24 * time.Hour
	maxUnfaithful    = 0.02
)

// summaryCosts are the setup tiers' models as the summaries' writer,
// measured with cmd/searcheval on one 100-page sample of a real library,
// stratified across articles, home pages, profiles, posts, repositories,
// videos, PDFs and thin pages, on a 64 GB M4 Max with Ollama 0.40.14 at the
// shared num_ctx 8192: the median call, the check, and every statement of
// 50 summaries read by hand against what the model was sent
// (docs/decisions.md "Summaries: one structured call per document, stored
// for the reranker"). A summary is unfaithful when a statement says what
// the page doesn't: a relation garbled, a detail given to the wrong thing,
// or a name the page never gives. qwen3:4b-instruct's 30 summaries were
// read only where a word screen flagged them, which already found an
// invented fact, and it called 70 of the 100 pages articles. A model not
// listed is unmeasured: doctor says nothing of it, and an import says no
// time for its summaries.
var summaryCosts = map[string]SummaryCost{
	"qwen3:4b-instruct":     {SecondsPerPage: 4.0, Fit: true, Unfaithful: 1.0 / 30},
	"gemma4:12b":            {SecondsPerPage: 5.9, Fit: true, Unfaithful: 3.0 / 50},
	"gemma4:26b-a4b-it-qat": {SecondsPerPage: 3.6, Fit: true, Unfaithful: 1.0 / 50},
	"gemma4:26b":            {SecondsPerPage: 3.6, Fit: true, Unfaithful: 0},
}

// SummaryCostOf is model's measured cost as the summaries' writer, and
// whether it was measured.
func SummaryCostOf(model string) (SummaryCost, bool) {
	c, ok := summaryCosts[model]
	return c, ok
}

// SummariesBelowBars is why model, as the summaries' writer, misses the
// measured bars: it failed the writer's check, more than maxUnfaithful of
// its summaries were unfaithful, or a pass over summaryPassPages would take
// more than summaryPassLimit at normal throttle. "" for a model that meets
// every bar, and for one unmeasured. curio doctor warns by it while
// summaries.enabled is true.
func SummariesBelowBars(model string) string {
	c, ok := SummaryCostOf(model)
	switch {
	case !ok:
		return ""
	case !c.Fit:
		return model + " failed the summaries' check"
	case c.Unfaithful > maxUnfaithful:
		return fmt.Sprintf("%.0f%% of %s's summaries stated what their page doesn't", 100*c.Unfaithful, model)
	}
	if pass := time.Duration(float64(summaryPassPages) * c.SecondsPerPage * float64(time.Second)); pass >
		summaryPassLimit {
		return fmt.Sprintf("%s takes about %.0f h for a library of %d pages", model, math.Ceil(pass.Hours()),
			summaryPassPages)
	}
	return ""
}

// SummariesOn reports whether document summaries are written: only when
// config.yaml sets summaries.enabled true. Left out, they are off for
// every model.
func (c Config) SummariesOn() bool {
	return c.Summaries.Enabled != nil && *c.Summaries.Enabled
}
