package config

import "time"

// SearchBudgetSeconds is what an understood search may spend before and
// between its reads: asking whether the planner's model is loaded (1 s),
// the plan, the query's embed, and for a reranked search asking whether
// the reranker's model is loaded (1 s) and the rerank stay under it, so
// `curio search`, which gives up after 30 s, keeps 5 s for the reads and
// gets its results rather than a client timeout when Ollama is slow. The
// planner and the reranker each hold their share to it.
const SearchBudgetSeconds = 25

// PlanTimeout is how long one plan may take under c: the search's
// understand_timeout_seconds, shortened to what embed_timeout_seconds
// leaves of SearchBudgetSeconds when the two and a second for the loaded
// check reach it, and 0 when that leaves no whole second. Validate leaves
// the sum alone: embed_timeout_seconds by itself, valid before
// understanding existed, can reach it, and a refused config would stop the
// daemon on an upgrade.
func (c Config) PlanTimeout() time.Duration {
	asked, embed := c.Search.UnderstandTimeoutSeconds, c.Search.EmbedTimeoutSeconds
	if asked+embed+1 < SearchBudgetSeconds {
		return time.Duration(asked) * time.Second
	}
	if left := SearchBudgetSeconds - 1 - embed - 1; left >= 1 {
		return time.Duration(left) * time.Second
	}
	return 0
}

// Understands reports whether a search's question may be understood under
// c: search.understand is on, the planner role's requests stay where the
// role itself names (Generation.Unnamed), and the budget leaves a plan at
// least a second (PlanTimeout). The planner (understand.OptionsFrom) and
// the reranker's default (RerankOn) both read it.
func (c Config) Understands() bool {
	key, _ := c.Generation.Unnamed(RolePlanner)
	return c.Search.Understand && key == "" && c.PlanTimeout() > 0
}
