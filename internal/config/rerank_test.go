package config

import (
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/ollama"
)

// measure lists model as measured at p for the test.
func measure(t *testing.T, model string, p RerankPoint) {
	t.Helper()
	key := ollama.NormalizeModel(model)
	was, had := rerankMeasured[key]
	rerankMeasured[key] = p
	t.Cleanup(func() {
		if had {
			rerankMeasured[key] = was
		} else {
			delete(rerankMeasured, key)
		}
	})
}

// TestDefault_Rerank: search.rerank is left out, so the reranker is on
// only for a measured model; the default writing model isn't one, so the
// role isn't in use. The depth and timeout are the design's. gemma4:26b
// is measured, needing the summaries: the amended pick rule's outcome.
func TestDefault_Rerank(t *testing.T) {
	d := Default()
	assert.Nil(t, d.Search.Rerank)
	assert.Equal(t, 30, d.Search.RerankDepth)
	assert.Equal(t, 7, d.Search.RerankTimeoutSeconds)
	assert.False(t, d.RerankOn())
	assert.True(t, d.RerankOffByDefault())
	assert.NotContains(t, d.RolesInUse(), RoleReranker)
	assert.Equal(t, d.Generation.Role(RolePlanner), d.Generation.Role(RoleReranker), "generation's model")

	p, ok := RerankMeasured("gemma4:26b")
	assert.True(t, ok)
	assert.Equal(t, RerankPoint{NeedsSummaries: true}, p)
	assert.Len(t, rerankMeasured, 1, "the one model the grid measured to pass")
	d.Generation.Model = "gemma4:26b"
	assert.False(t, d.RerankOn(), "no summaries")
	d.Summaries.Enabled = new(true)
	assert.True(t, d.RerankOn(), "with the summaries")
	assert.False(t, d.RerankOffByDefault())
	d.Search.Understand = false
	assert.False(t, d.RerankOn(), "measured on understood searches")
	d.Search.Understand = true
	d.Generation.Model = "gemma4:12b"
	assert.False(t, d.RerankOn(), "another model")
}

// TestRerankOn: true is on with any model, false off with any; left out,
// on only for a measured model, matched as Ollama matches names, and,
// when its point needs the summaries, only while they are written.
func TestRerankOn(t *testing.T) {
	measure(t, "gemma4:26b", RerankPoint{})
	measure(t, "Example/Reranker", RerankPoint{})
	measure(t, "needs:summaries", RerankPoint{NeedsSummaries: true})
	for _, tc := range []struct {
		name      string
		rerank    *bool
		model     string
		summaries bool
		on        bool
	}{
		{"true, unmeasured", new(true), "elsewhere:7b", false, true},
		{"false, measured", new(false), "gemma4:26b", false, false},
		{"left out, unmeasured", nil, "qwen3:4b-instruct", false, false},
		{"left out, measured", nil, "gemma4:26b", false, true},
		{"left out, measured, case aside", nil, "Gemma4:26B", false, true},
		{"left out, untagged is latest", nil, "example/reranker:latest", false, true},
		{"left out, another tag", nil, "gemma4:12b", false, false},
		{"left out, needs summaries, none", nil, "needs:summaries", false, false},
		{"left out, needs summaries, written", nil, "needs:summaries", true, true},
		{"true, needs summaries, none", new(true), "needs:summaries", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			c.Search.Rerank = tc.rerank
			c.Generation.Model = tc.model
			c.Summaries.Enabled = new(tc.summaries)
			assert.Equal(t, tc.on, c.RerankOn())
			assert.Equal(t, tc.on, slices.Contains(c.RolesInUse(), RoleReranker))
			assert.Equal(t, tc.rerank == nil && !tc.on, c.RerankOffByDefault())
		})
	}
}

// TestRerank_OwnModel: generation.roles.reranker names the reranker's own
// model, which RerankOn judges, and which SharesModel sees.
func TestRerank_OwnModel(t *testing.T) {
	measure(t, "example/qwen3-reranker-4b:q8_0", RerankPoint{})
	got, err := Load(writeConfig(t, "generation:\n  model: gemma4:26b\n  roles:\n    reranker:\n"+
		"      model: example/Qwen3-Reranker-4B:Q8_0\n"))
	require.NoError(t, err)
	assert.Equal(t, "example/Qwen3-Reranker-4B:Q8_0", got.Generation.Role(RoleReranker).Model)
	assert.True(t, got.RerankOn())
	assert.Equal(t, []Role{RoleLabels, RolePlanner, RoleReranker}, got.RolesInUse())
	assert.False(t, got.SharesModel(RoleReranker), "a model of its own")
	assert.True(t, got.SharesModel(RolePlanner), "the planner still shares the labels'")

	measure(t, "gemma4:26b", RerankPoint{})
	got.Generation.Roles = nil
	assert.True(t, got.SharesModel(RoleReranker), "on generation.model, with the labels and the planner")
	assert.True(t, got.SharesModelWith(RolePlanner, RoleReranker))
	got.Insight.Labeling, got.Search.Understand = "terms", false
	assert.False(t, got.SharesModel(RoleReranker), "no other role in use")
}

// TestLoad_Rerank: the keys load strictly, and Validate bounds the depth
// and the timeout.
func TestLoad_Rerank(t *testing.T) {
	for _, tc := range []struct {
		yaml string
		want *bool
	}{
		{"search:\n  rerank: true\n", new(true)},
		{"search:\n  rerank: false\n", new(false)},
		{"search:\n  default_k: 10\n", nil},
	} {
		got, err := Load(writeConfig(t, tc.yaml))
		require.NoError(t, err, tc.yaml)
		assert.Equal(t, tc.want, got.Search.Rerank, tc.yaml)
	}
	_, err := Load(writeConfig(t, "search:\n  rerank: sometimes\n"))
	require.Error(t, err, "a boolean or nothing")
	_, err = Load(writeConfig(t, "search:\n  rerank_model: x\n"))
	require.ErrorContains(t, err, "field rerank_model not found")

	got, err := Load(writeConfig(t, "search:\n  rerank_depth: 30\n  rerank_timeout_seconds: 23\n"))
	require.NoError(t, err)
	assert.Equal(t, 30, got.Search.RerankDepth)
	assert.Equal(t, 23, got.Search.RerankTimeoutSeconds)
	for _, tc := range []struct{ key, want string }{
		{"rerank_depth: 1", "search.rerank_depth must be in [2, 30], got 1"},
		{"rerank_depth: 31", "search.rerank_depth must be in [2, 30], got 31"},
		{"rerank_timeout_seconds: 0", "search.rerank_timeout_seconds must be in [1, 23], got 0"},
		{"rerank_timeout_seconds: 24", "search.rerank_timeout_seconds must be in [1, 23], got 24"},
	} {
		_, err := Load(writeConfig(t, fmt.Sprintf("search:\n  %s\n", tc.key)))
		require.ErrorContains(t, err, ": "+tc.want)
	}
}

// TestLoad_RerankBudget: a timeout whose sum with the plan's and the
// embed's passes the 25 s budget is still taken: holding the sum is the
// reranker's (rerank.OptionsFrom), never a refused daemon start.
func TestLoad_RerankBudget(t *testing.T) {
	got, err := Load(writeConfig(t, "search:\n  rerank: true\n  rerank_timeout_seconds: 23\n"+
		"  understand_timeout_seconds: 23\n  embed_timeout_seconds: 29\n"))
	require.NoError(t, err)
	assert.True(t, got.RerankOn())
}
