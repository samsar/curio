package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlanTimeout: a plan keeps its timeout while it, the embed and the
// loaded check stay under the search budget, gets what the embed leaves
// past it, and nothing when that is under a second.
func TestPlanTimeout(t *testing.T) {
	for _, tc := range []struct {
		understand, embed int
		want              time.Duration
	}{
		{5, 10, 5 * time.Second},
		{13, 10, 13 * time.Second},
		{14, 10, 13 * time.Second},
		{23, 10, 13 * time.Second},
		{5, 22, time.Second},
		{5, 23, 0},
		{1, 60, 0},
	} {
		c := Default()
		c.Search.UnderstandTimeoutSeconds, c.Search.EmbedTimeoutSeconds = tc.understand, tc.embed
		assert.Equal(t, tc.want, c.PlanTimeout(), "understand %d, embed %d", tc.understand, tc.embed)
	}
}

// TestUnderstands: questions are understood only with search.understand
// on, a planner whose requests stay where it names, and time for a plan.
func TestUnderstands(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
		want   bool
	}{
		{"the defaults", func(*Config) {}, true},
		{"off", func(c *Config) { c.Search.Understand = false }, false},
		{"a remote server the planner doesn't name", func(c *Config) {
			c.Generation.BaseURL = "http://gpu.example:11434"
		}, false},
		{"a remote server the planner names", func(c *Config) {
			c.Generation.Roles = map[Role]ModelSpec{RolePlanner: {BaseURL: "http://gpu.example:11434"}}
		}, true},
		{"no time for a plan", func(c *Config) { c.Search.EmbedTimeoutSeconds = 23 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Default()
			tc.change(&c)
			require.NoError(t, c.Validate())
			assert.Equal(t, tc.want, c.Understands())
		})
	}
}
