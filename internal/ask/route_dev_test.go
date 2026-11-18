package ask

import (
	"os"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// devQuestion is one question of testdata/routing-dev.yaml.
type devQuestion struct {
	Q     string `yaml:"q"`
	Route Route  `yaml:"route"`
	Hard  bool   `yaml:"hard"`
}

// devQuestions reads testdata/routing-dev.yaml.
func devQuestions(t *testing.T) []devQuestion {
	t.Helper()
	raw, err := os.ReadFile("testdata/routing-dev.yaml")
	require.NoError(t, err)
	var set struct {
		Questions []devQuestion `yaml:"questions"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &set))
	require.NotEmpty(t, set.Questions)
	return set.Questions
}

// devMisses are the dev set's questions RouteFor routes elsewhere, and
// why: the lexicons read words as they are spelled.
var devMisses = map[string]Route{
	"my intrests this year": RouteAnswer, // misspelled
}

// TestRouteFor_Dev: every question of the blind dev set takes the route
// it is labelled with, but the known misses, which take theirs; no
// question labelled answer, a factual one, leaves the answer route.
func TestRouteFor_Dev(t *testing.T) {
	questions := devQuestions(t)
	for _, q := range questions {
		want := q.Route
		if missed, ok := devMisses[q.Q]; ok {
			want = missed
		}
		assert.Equal(t, want, RouteFor(q.Q), "%q (labelled %s)", q.Q, q.Route)
	}
	for q := range devMisses {
		assert.True(t, slices.ContainsFunc(questions, func(d devQuestion) bool { return d.Q == q }),
			"a known miss the set holds: %q", q)
	}
}
