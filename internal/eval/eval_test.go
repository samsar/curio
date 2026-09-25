package eval

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeQrels(t *testing.T, yaml string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "qrels.yaml")
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o600))
	return path
}

func TestLoadQuerySet_NormalizesRelevantURLs(t *testing.T) {
	qs, err := LoadQuerySet(writeQrels(t, `
queries:
  - query: "videos"
    relevant:
      - "https://youtu.be/dQw4w9WgXcQ"
  - query: "origins and tracking"
    relevant:
      - "https://Example.com"
      - "https://example.com/post?utm_source=feed&id=7#comments"
      - "https://example.com/post?id=7"
`))
	require.NoError(t, err)
	assert.Equal(t, []string{"https://www.youtube.com/watch?v=dQw4w9WgXcQ"}, qs.Queries[0].Relevant)
	assert.Equal(t, []string{"https://example.com/", "https://example.com/post?id=7"}, qs.Queries[1].Relevant,
		"normalized duplicates are dropped")
}

func TestLoadQuerySet_InvalidRelevantURL(t *testing.T) {
	_, err := LoadQuerySet(writeQrels(t, `
queries:
  - query: "fine"
    relevant: ["https://example.com/a"]
  - query: "broken"
    relevant: ["ftp://example.com/file"]
`))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query 1")
	assert.Contains(t, err.Error(), "ftp://example.com/file")
}

func TestEvaluate_MatchesNormalizedURLs(t *testing.T) {
	qs, err := LoadQuerySet(writeQrels(t, `
queries:
  - query: "q"
    relevant: ["https://Example.com/a#section"]
`))
	require.NoError(t, err)
	rep := Evaluate(qs, [][]string{{"https://example.com/a"}}, 10)
	assert.InDelta(t, 1.0, rep.MeanRecall, 1e-9, "a stored URL matches the browser-pasted one")
}
