package setup

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/client"
	"github.com/samsar/curio/internal/config"
)

// TestWritingChanges: a role config.yaml uses is a reason to restart when
// the daemon writes it with another model or with none; a daemon from
// before roles, or one using a role config.yaml no longer does, is not.
func TestWritingChanges(t *testing.T) {
	cfg := config.Default()
	cfg.Summaries.Enabled = new(true) // every role in use, with generation.model
	model := cfg.Generation.Model
	off := config.Default()
	off.Insight.Enabled, off.Search.Understand, off.Summaries.Enabled = false, false, new(false)
	all := func(m string) map[string]string {
		return map[string]string{"labels": m, "planner": m, "summaries": m}
	}
	for _, tc := range []struct {
		name    string
		cfg     config.Config
		roles   map[string]string
		want    []writingChange
		problem string
	}{
		{name: "a daemon from before roles", cfg: cfg},
		{name: "the same model", cfg: cfg, roles: all(model)},
		{name: "another model, once for every role", cfg: cfg, roles: all("gemma4:12b"),
			want:    []writingChange{{running: "gemma4:12b", configured: model}},
			problem: "it writes with gemma4:12b, not config.yaml's " + model},
		{name: "a daemon from before the planner", cfg: cfg, roles: map[string]string{"labels": model},
			want: []writingChange{{configured: model, added: config.RolePlanner},
				{configured: model, added: config.RoleSummaries}},
			problem: "it writes search understanding with no model, not config.yaml's " + model},
		{name: "a daemon from before the summaries", cfg: cfg,
			roles:   map[string]string{"labels": model, "planner": model},
			want:    []writingChange{{configured: model, added: config.RoleSummaries}},
			problem: "it writes document summaries with no model, not config.yaml's " + model},
		{name: "no role the daemon uses", cfg: cfg, roles: map[string]string{},
			want: []writingChange{{configured: model, added: config.RoleLabels},
				{configured: model, added: config.RolePlanner}, {configured: model, added: config.RoleSummaries}},
			problem: "it writes interest labels with no model, not config.yaml's " + model},
		{name: "roles config.yaml no longer uses", cfg: off, roles: all(model)},
		{name: "no role on either side", cfg: off, roles: map[string]string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := writingChanges(&client.Health{GenerationRoles: tc.roles}, tc.cfg)
			require.Equal(t, tc.want, got)
			if tc.problem != "" {
				assert.Equal(t, tc.problem, got[0].problem())
			}
		})
	}
}
