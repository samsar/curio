package understand

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

var update = flag.Bool("update", false, "record testdata/prompt.golden and testdata/schema.golden")

// TestPromptGolden: the prompt and the answer's schema are the ones
// recorded. A change to either changes what every plan is made from, and
// the planned search and the fitness check are measured again before it
// is recorded with -update.
func TestPromptGolden(t *testing.T) {
	for file, got := range map[string]string{"prompt.golden": promptTemplate, "schema.golden": answerSchema} {
		path := filepath.Join("testdata", file)
		if *update {
			require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
		}
		want, err := os.ReadFile(path)
		require.NoError(t, err, "record %s with -update", path)
		assert.Equal(t, string(want), got, "%s changed: measure the planned search again, then -update", file)
	}
}

// TestPromptVersion is the SHA-256 of the prompt and the schema, in hex.
func TestPromptVersion(t *testing.T) {
	sum := sha256.Sum256([]byte(promptTemplate + answerSchema))
	assert.Equal(t, hex.EncodeToString(sum[:]), PromptVersion)
	assert.Len(t, PromptVersion, 64)
}

// TestRenderPrompt: the fixed instructions, then today's weekday and date
// as given, then the question, last and as written, a placeholder in it
// included.
func TestRenderPrompt(t *testing.T) {
	thursday := time.Date(2025, 4, 17, 23, 30, 0, 0, time.UTC)
	const q = "which {today} talk about {question}?"
	got := renderPrompt(q, thursday)
	instructions, _, ok := strings.Cut(promptTemplate, "Today is {weekday}")
	require.True(t, ok)
	assert.True(t, strings.HasPrefix(got, instructions), "the instructions come first, as fixed")
	assert.True(t, strings.HasSuffix(got, instructions[len(instructions)-2:]+
		"Today is Thursday, 2025-04-17.\nQuestion: "+q+"\n"), "got the tail %q", got[len(instructions):])
}

// TestSchema: the schema is JSON whose kinds are Kinds, whose content types
// and sources are the store's, each with words that state it, and that
// lets the model set no site, folder, host, include_undated, article or
// unknown.
func TestSchema(t *testing.T) {
	var s struct {
		Properties struct {
			Kind    struct{ Enum []Kind }
			Filters struct {
				Properties           map[string]json.RawMessage
				AdditionalProperties bool `json:"additionalProperties"`
			}
		}
		Required             []string
		AdditionalProperties bool `json:"additionalProperties"`
	}
	require.NoError(t, json.Unmarshal([]byte(answerSchema), &s))
	assert.Equal(t, Kinds(), s.Properties.Kind.Enum)
	assert.Equal(t, []string{"kind", "names", "rewrites", "filters"}, s.Required)
	assert.False(t, s.AdditionalProperties)
	assert.False(t, s.Properties.Filters.AdditionalProperties)
	keys := make([]string, 0, len(s.Properties.Filters.Properties))
	for k := range s.Properties.Filters.Properties {
		keys = append(keys, k)
	}
	assert.ElementsMatch(t, []string{"saved_after", "saved_before", "published_after", "published_before",
		"content_type", "source"}, keys)

	enum := func(key string) []string {
		var p struct{ Items struct{ Enum []string } }
		require.NoError(t, json.Unmarshal(s.Properties.Filters.Properties[key], &p))
		return p.Items.Enum
	}
	for _, ct := range enum("content_type") {
		assert.True(t, store.ContentType(ct).Valid(), ct)
		assert.NotEmpty(t, typeWords[ct], "%s has words that state it", ct)
	}
	assert.NotContains(t, enum("content_type"), string(store.ContentTypeArticle))
	assert.NotContains(t, enum("content_type"), string(store.ContentTypeUnknown))
	for _, src := range enum("source") {
		assert.True(t, store.ValidSource(src), src)
		assert.NotEmpty(t, sourceWords[src], "%s has words that state it", src)
	}
	assert.Len(t, typeWords, len(enum("content_type")))
	assert.Len(t, sourceWords, len(enum("source")))
}

// TestProbes: each probe passes the answer a fit model gives, and fails
// the ones a weak model gave in review: filters on a question that states
// none, a month misread, a type left out, a paper not named.
func TestProbes(t *testing.T) {
	byName := func(name string) probe {
		i := slices.IndexFunc(probes, func(p probe) bool { return p.name == name })
		require.GreaterOrEqual(t, i, 0, name)
		return probes[i]
	}
	none, month, paper := byName("no filter"), byName("a month and a type"), byName("a paper by description")
	video := []string{"video"}

	assert.True(t, none.fits(answer{Kind: KindKeywords}))
	assert.False(t, none.fits(answer{Kind: KindKeywords, Filters: answerFilters{SavedAfter: "2025-04-01"}}))
	assert.False(t, none.fits(answer{Kind: KindKeywords, Filters: answerFilters{Source: []string{"chrome"}}}))

	assert.True(t, month.fits(answer{Filters: answerFilters{SavedAfter: "2017-10-01", SavedBefore: "2017-11-01",
		ContentType: video}}))
	assert.False(t, month.fits(answer{Filters: answerFilters{SavedAfter: "2017-10-01", SavedBefore: "2017-10-31",
		ContentType: video}}), "the end is the first day after")
	assert.False(t, month.fits(answer{Filters: answerFilters{SavedAfter: "2017-10-01", SavedBefore: "2017-11-01"}}))
	assert.False(t, month.fits(answer{Filters: answerFilters{SavedAfter: "2017-10-01", SavedBefore: "2017-11-01",
		ContentType: video, Source: []string{"safari"}}}))

	assert.True(t, paper.fits(answer{Names: []string{"Google", "Attention Is All You Need (Vaswani et al.)"}}))
	assert.False(t, paper.fits(answer{Names: []string{"Transformer"}}))
	assert.False(t, paper.fits(answer{Names: []string{"Attention Is All You Need"},
		Filters: answerFilters{PublishedAfter: "2017-01-01"}}))
}
