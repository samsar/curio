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
	cfg := config.Default() // the labels role in use, with generation.model
	model := cfg.Generation.Model
	off := config.Default()
	off.Insight.Enabled = false
	for _, tc := range []struct {
		name    string
		cfg     config.Config
		roles   map[string]string
		want    []writingChange
		problem string
	}{
		{name: "a daemon from before roles", cfg: cfg},
		{name: "the same model", cfg: cfg, roles: map[string]string{"labels": model}},
		{name: "another model", cfg: cfg, roles: map[string]string{"labels": "gemma4:12b"},
			want:    []writingChange{{running: "gemma4:12b", configured: model}},
			problem: "it writes with gemma4:12b, not config.yaml's " + model},
		{name: "a role the daemon doesn't use", cfg: cfg, roles: map[string]string{},
			want:    []writingChange{{configured: model, added: config.RoleLabels}},
			problem: "it writes interest labels with no model, not config.yaml's " + model},
		{name: "a role config.yaml no longer uses", cfg: off, roles: map[string]string{"labels": model}},
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
