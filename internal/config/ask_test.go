package config

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDefault_Ask: ask.enabled is left out, so Ask is on as AskOn says,
// from 6 sources not reranked, an answer of at most 768 tokens within 90 s.
func TestDefault_Ask(t *testing.T) {
	assert.Equal(t, Ask{Sources: 6, MaxTokens: 768, TimeoutSeconds: 90}, Default().Ask)
	require.NoError(t, Default().Validate())
}

// TestAskOn: ask.enabled set is on or off as it says, with any model; left
// out, Ask is on only for a model measured as the answer writer, its name
// matched as Ollama's, whether generation.model or the answers role's own
// names it; off by default otherwise.
func TestAskOn(t *testing.T) {
	for _, tc := range []struct {
		name, model, answers string
		enabled              *bool
		on, offByDefault     bool
	}{
		{name: "left out, measured", model: "gemma4:26b", on: true},
		{name: "left out, measured, named as Ollama", model: "Gemma4:26B", on: true},
		{name: "left out, the role's own model measured", model: "qwen3:4b-instruct", answers: "gemma4:26b",
			on: true},
		{name: "left out, unmeasured", model: "qwen3:4b-instruct", offByDefault: true},
		{name: "left out, another tier unmeasured", model: "gemma4:12b", offByDefault: true},
		{name: "true, unmeasured", model: "qwen3:4b-instruct", enabled: new(true), on: true},
		{name: "false, measured", model: "gemma4:26b", enabled: new(false)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			c.Generation.Model, c.Ask.Enabled = tc.model, tc.enabled
			if tc.answers != "" {
				c.Generation.Roles = map[Role]ModelSpec{RoleAnswers: {Model: tc.answers}}
			}
			assert.Equal(t, tc.on, c.AskOn())
			assert.Equal(t, tc.offByDefault, c.AskOffByDefault())
			assert.Equal(t, tc.on, slices.Contains(c.RolesInUse(), RoleAnswers), "in use exactly while on")
		})
	}
}

// TestLoad_Ask: the keys load strictly under their own section.
func TestLoad_Ask(t *testing.T) {
	got, err := Load(writeConfig(t, "ask:\n  enabled: false\n  sources: 4\n  max_tokens: 1024\n"+
		"  timeout_seconds: 120\n  rerank: true\n"))
	require.NoError(t, err)
	assert.Equal(t, Ask{Enabled: new(false), Sources: 4, MaxTokens: 1024, TimeoutSeconds: 120, Rerank: true},
		got.Ask)
	assert.NotContains(t, got.RolesInUse(), RoleAnswers, "off, the role isn't in use")

	got, err = Load(writeConfig(t, "ask:\n  sources: 3\n"))
	require.NoError(t, err)
	assert.Equal(t, Ask{Sources: 3, MaxTokens: 768, TimeoutSeconds: 90}, got.Ask, "the rest default")

	_, err = Load(writeConfig(t, "ask:\n  source: 3\n"))
	require.ErrorContains(t, err, "field source not found")
}

// TestValidate_AskBounds: each key is refused one past either bound and
// taken at it, the error naming the key, the bounds and the value.
func TestValidate_AskBounds(t *testing.T) {
	for _, tc := range []struct {
		key    string
		set    func(*Ask, int)
		lo, hi int
	}{
		{"ask.sources", func(a *Ask, v int) { a.Sources = v }, 1, 12},
		{"ask.max_tokens", func(a *Ask, v int) { a.MaxTokens = v }, 128, 2048},
		{"ask.timeout_seconds", func(a *Ask, v int) { a.TimeoutSeconds = v }, 15, 300},
	} {
		for _, v := range []int{tc.lo, tc.hi} {
			c := Default()
			tc.set(&c.Ask, v)
			require.NoError(t, c.Validate(), "%s %d", tc.key, v)
		}
		for _, v := range []int{tc.lo - 1, tc.hi + 1} {
			c := Default()
			tc.set(&c.Ask, v)
			assert.EqualError(t, c.Validate(), fmt.Sprintf("%s must be in [%d, %d], got %d", tc.key, tc.lo, tc.hi, v))
		}
	}
	c := Default()
	c.Ask.Enabled = new(false)
	c.Ask.Sources = 0
	require.Error(t, c.Validate(), "bounded on or off")
}

// TestAnswers_Role: generation.roles.answers names the answers' own
// model; the role is in use exactly while Ask is on, never pulls alone.
func TestAnswers_Role(t *testing.T) {
	got, err := Load(writeConfig(t, "generation:\n  roles:\n    answers:\n      model: gemma4:26b\n"))
	require.NoError(t, err)
	assert.Equal(t, "gemma4:26b", got.Generation.Role(RoleAnswers).Model)
	assert.Equal(t, "qwen3:4b-instruct", got.Generation.Role(RolePlanner).Model)
	assert.Contains(t, got.RolesInUse(), RoleAnswers)
	assert.False(t, got.SharesModel(RoleAnswers), "a model of its own")
	assert.False(t, RoleAnswers.PullsAlone())
}

// TestAnswers_KeepAlive: the answers send search's keep-alive. Beside the
// labels, which send none, they are a short sharer's; beside the planner
// alone, which sends the same, they aren't. A summaries pass on their
// model sends search's keep-alive too.
func TestAnswers_KeepAlive(t *testing.T) {
	cfg := Default()
	cfg.Ask.Enabled = new(true)
	assert.Equal(t, 30*time.Minute, cfg.KeepAliveOf(RoleAnswers))
	assert.True(t, cfg.SharesModelWith(RoleAnswers, RoleLabels))
	assert.True(t, cfg.SharesModelShorter(RoleAnswers), "the labels send none")

	cfg.Insight.Labeling = "terms"
	assert.True(t, cfg.SharesModelWith(RoleAnswers, RolePlanner))
	assert.True(t, cfg.SharesModel(RoleAnswers))
	assert.False(t, cfg.SharesModelShorter(RoleAnswers), "the planner sends the same")
	assert.False(t, cfg.SharesModelShorter(RolePlanner), "so do the answers")

	cfg.Search.Understand = false
	assert.True(t, cfg.SharesModelWith(RoleSummaries, RoleAnswers), "whether or not the summaries are in use")
	assert.Equal(t, 30*time.Minute, cfg.KeepAliveOf(RoleSummaries), "the answers on the summaries' model")
	cfg.Ask.Enabled = new(false)
	assert.Zero(t, cfg.KeepAliveOf(RoleSummaries), "nothing of search's on their model")
}
