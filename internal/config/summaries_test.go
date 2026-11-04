package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// withSummaryCosts stands costs in for the measured ones for one test.
func withSummaryCosts(t *testing.T, costs map[string]SummaryCost) {
	t.Helper()
	saved := summaryCosts
	summaryCosts = costs
	t.Cleanup(func() { summaryCosts = saved })
}

// TestSummariesBelowBars: a model misses the bars when it failed the
// check, wrote more than 2% unfaithful summaries, or would take more than
// 24 h over 5,000 pages at normal throttle; an unmeasured one meets them.
func TestSummariesBelowBars(t *testing.T) {
	withSummaryCosts(t, map[string]SummaryCost{
		"fit:1":     {SecondsPerPage: 10, Fit: true, Unfaithful: 0.01},
		"unfit:1":   {SecondsPerPage: 5, Fit: false},
		"loose:1":   {SecondsPerPage: 5, Fit: true, Unfaithful: 0.05},
		"slow:1":    {SecondsPerPage: 18, Fit: true},
		"bar:1":     {SecondsPerPage: 17.28, Fit: true, Unfaithful: 0.02},
		"unknown:1": {},
	})
	assert.Empty(t, SummariesBelowBars("fit:1"))
	assert.Equal(t, "unfit:1 failed the summaries' check", SummariesBelowBars("unfit:1"))
	assert.Equal(t, "5% of loose:1's summaries stated what their page doesn't", SummariesBelowBars("loose:1"))
	assert.Equal(t, "slow:1 takes about 25 h for a library of 5000 pages", SummariesBelowBars("slow:1"))
	assert.Empty(t, SummariesBelowBars("bar:1"), "24 h and 2% exactly meet the bars")
	assert.Empty(t, SummariesBelowBars("elsewhere:7b"), "unmeasured: nothing said")
}

// TestSummariesBelowBars_Tiers: the two smallest tiers' models are over
// the 2% bar; gemma4:26b-a4b-it-qat, at 1 of 50, meets it exactly, and so
// does gemma4:26b, the operator-sized tier's.
func TestSummariesBelowBars_Tiers(t *testing.T) {
	for model, want := range map[string]string{
		"qwen3:4b-instruct":     "3% of qwen3:4b-instruct's summaries stated what their page doesn't",
		"gemma4:12b":            "6% of gemma4:12b's summaries stated what their page doesn't",
		"gemma4:26b-a4b-it-qat": "",
		"gemma4:26b":            "",
	} {
		_, measured := SummaryCostOf(model)
		assert.True(t, measured, "%s has a measured summary cost", model)
		assert.Equal(t, want, SummariesBelowBars(model), model)
	}
}

// TestSummariesOn: summaries are written only when summaries.enabled is
// true; left out or false, they are off, whatever the model.
func TestSummariesOn(t *testing.T) {
	for _, model := range []string{"qwen3:4b-instruct", "gemma4:26b", "elsewhere:7b"} {
		c := Default()
		c.Generation.Model = model
		assert.False(t, c.SummariesOn(), "%s: left out, off", model)
		c.Summaries.Enabled = new(false)
		assert.False(t, c.SummariesOn(), model)
		c.Summaries.Enabled = new(true)
		assert.True(t, c.SummariesOn(), model)
	}
}
