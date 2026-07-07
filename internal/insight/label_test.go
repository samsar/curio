package insight

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTermLabeler_NamesByFrequentTerms(t *testing.T) {
	l := NewTermLabeler()
	lab, err := l.Label(context.Background(), ClusterInfo{
		Titles: []string{
			"Machine Learning Basics",
			"Deep Learning and Neural Networks",
			"Learning Rate Schedules",
		},
		Size: 3,
	})
	require.NoError(t, err)
	// "learning" appears in all three titles → should lead the label.
	assert.Contains(t, strings.ToLower(lab.Name), "learning")
	// Stopwords ("and") must not appear.
	assert.NotContains(t, strings.ToLower(lab.Name), "and")
}

func TestTermLabeler_EmptyOnNoSalientTerms(t *testing.T) {
	l := NewTermLabeler()
	lab, err := l.Label(context.Background(), ClusterInfo{Titles: []string{"a", "of", "the"}})
	require.NoError(t, err)
	assert.Empty(t, lab.Name)
}

func TestParseLabel(t *testing.T) {
	got := parseLabel("NAME: Distributed Systems\nSUMMARY: Articles about consensus and replication.")
	assert.Equal(t, "Distributed Systems", got.Name)
	assert.Equal(t, "Articles about consensus and replication.", got.Summary)

	// Falls back to the first non-empty line when unformatted.
	got = parseLabel("  Rust Programming\n")
	assert.Equal(t, "Rust Programming", got.Name)

	// Strips wrapping quotes/markdown.
	got = parseLabel(`NAME: "Web Security"`)
	assert.Equal(t, "Web Security", got.Name)

	// Tolerates a dash separator without leaking the "NAME" token.
	assert.Equal(t, "DNS Fundamentals", parseLabel("NAME - DNS Fundamentals").Name)

	// Tolerates markdown bold around the field.
	assert.Equal(t, "DNS Fundamentals", parseLabel("**NAME:** DNS Fundamentals\n**SUMMARY:** about DNS").Name)

	// Rejects a sentence-like preamble (no NAME field) so the engine falls
	// back to deterministic term labels instead of persisting junk.
	assert.Empty(t, parseLabel("Sure, here is the topic name you asked me for").Name)
}
