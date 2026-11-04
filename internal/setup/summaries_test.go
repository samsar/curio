package setup

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
)

// TestSummaryTiers_Measured: every tier's model has a measured cost as the
// summaries' writer, so an import with summaries turned on says how long
// they take, and doctor whether the model meets the bars.
func TestSummaryTiers_Measured(t *testing.T) {
	for _, tier := range generationTiers {
		_, measured := config.SummaryCostOf(tier.Model.Name)
		assert.True(t, measured, "%s has a measured summary cost", tier.Model.Name)
	}
}

// TestRenderConfig_LeavesSummariesOut: curio up writes no summaries
// section, whatever the tier: summaries are off by default.
func TestRenderConfig_LeavesSummariesOut(t *testing.T) {
	for _, model := range []string{"qwen3:4b-instruct", "gemma4:26b"} {
		cfg := config.Default()
		cfg.Generation.Model = model
		data, err := renderConfig(cfg)
		require.NoError(t, err)
		assert.NotContains(t, string(data), "summaries", model)
	}
}

// TestImportNotes_Summaries: with summaries turned on, the import's notes
// add the summary pass on a line of its own, the new pages at the
// summaries' model's measured time a page, the role's model when it names
// one, and no time for a model unmeasured; no line while they are off, by
// default or by config.yaml.
func TestImportNotes_Summaries(t *testing.T) {
	cfg := config.Default()
	cfg.Generation.Model = "gemma4:26b"
	e := estimateImport(1000, cfg.Daemon.FetchWorkers, 40, "")
	notes := func() string {
		return strings.Join(importNotes(e, 40, nil, cfg, "/Users/x/.curio/config.yaml", true), "\n")
	}
	assert.NotContains(t, notes(), "summaries", "off by default")

	cfg.Summaries.Enabled = new(true)
	assert.Contains(t, notes(), "\n  summaries: about 1h after, a page at a time with gemma4:26b (about 4 s a "+
		"page at normal throttle, as measured on a 64 GB M4 Max; searches pause it)")

	cfg.Generation.Roles = map[config.Role]config.ModelSpec{config.RoleSummaries: {Model: "gemma4:12b"}}
	assert.Contains(t, notes(), "  summaries: about 1h39m after, a page at a time with gemma4:12b")

	cfg.Generation.Roles = nil
	cfg.Generation.Model = "elsewhere:7b"
	assert.Contains(t, notes(), "  summaries: written after, a page at a time with elsewhere:7b (its time a page "+
		"isn't measured)")

	cfg.Summaries.Enabled = new(false)
	assert.NotContains(t, notes(), "summaries")
}
