package summarize

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/store"
)

var update = flag.Bool("update", false,
	"record testdata/prompt.golden, testdata/schema.golden and testdata/input_rules.golden")

// goldens are the recorded texts PromptVersion is the hash of, in its
// order.
var goldens = []string{"prompt.golden", "schema.golden", "input_rules.golden"}

// TestPromptGolden: the prompt, the answer's schema and the input rules
// (the scan window, the reduction, the cut and the token estimate) are the
// ones recorded. A change to any changes what every summary is written
// from, and marks those written before outdated: the sample and the
// fitness check are measured again before it is recorded with -update.
func TestPromptGolden(t *testing.T) {
	texts := map[string]string{goldens[0]: promptTemplate, goldens[1]: answerSchema, goldens[2]: inputRules}
	for file, got := range texts {
		path := filepath.Join("testdata", file)
		if *update {
			require.NoError(t, os.WriteFile(path, []byte(got), 0o600))
		}
		want, err := os.ReadFile(path)
		require.NoError(t, err, "record %s with -update", path)
		assert.Equal(t, string(want), got, "%s changed: measure the sample again, then -update", file)
	}
}

// TestPromptVersion is the SHA-256 of the recorded prompt, schema and
// input rules, in hex, so no change of any of them, the cut's included,
// keeps the version stored summaries were written under.
func TestPromptVersion(t *testing.T) {
	var recorded []byte
	for _, file := range goldens {
		b, err := os.ReadFile(filepath.Join("testdata", file))
		require.NoError(t, err)
		recorded = append(recorded, b...)
	}
	sum := sha256.Sum256(recorded)
	assert.Equal(t, hex.EncodeToString(sum[:]), PromptVersion)
}

// TestSchema: the schema is JSON whose kinds are the store's, in their
// order, every field required, no other key allowed, and its caps the
// decoding's.
func TestSchema(t *testing.T) {
	var s struct {
		Properties map[string]struct {
			Enum      []store.SummaryKind
			MaxLength int `json:"maxLength"`
			MaxItems  int `json:"maxItems"`
			Items     struct {
				MaxLength int `json:"maxLength"`
			}
		}
		Required             []string
		AdditionalProperties bool `json:"additionalProperties"`
	}
	require.NoError(t, json.Unmarshal([]byte(answerSchema), &s))
	assert.Equal(t, store.SummaryKinds(), s.Properties["kind"].Enum)
	assert.Equal(t, []string{"kind", "site", "author", "about", "entities", "claims", "descriptions"}, s.Required)
	assert.False(t, s.AdditionalProperties)
	assert.Equal(t, maxAboutChars, s.Properties["about"].MaxLength)
	assert.Equal(t, maxEntities, s.Properties["entities"].MaxItems)
	assert.Equal(t, maxEntityChars, s.Properties["entities"].Items.MaxLength)
	for _, k := range store.SummaryKinds() {
		assert.Contains(t, promptTemplate, "- "+string(k)+": ", "the prompt says what %s is", k)
	}
}

// TestRenderPrompt: the fixed instructions first, then the page's fields,
// then its text between the delimiters, last, a placeholder in it kept as
// written.
func TestRenderPrompt(t *testing.T) {
	in := Input{Title: "A {url} title", URL: "https://example.com/a", Site: "Example", ContentType: "article",
		Text: "text with {title}"}
	got := renderPrompt(in)
	fixed, _, ok := strings.Cut(promptTemplate, "Title: {title}")
	require.True(t, ok)
	assert.True(t, strings.HasPrefix(got, fixed), "the instructions come first, as fixed")
	assert.True(t, strings.HasSuffix(got, "Title: A {url} title\nURL: https://example.com/a\nSite: Example\n"+
		"Type: article\n<page>\ntext with {title}\n</page>\n"), got[len(fixed):])
}

// TestInstructionTokens: the prompt without a page costs at most
// instructionTokens, with a quarter to spare for other tokenizers; it was
// measured at instructionsMeasured tokens on the larger of the two models
// measured, and the test holds the text to the length measured, so a
// longer prompt is measured again.
func TestInstructionTokens(t *testing.T) {
	const (
		instructionsMeasured      = 481 // gemma4:26b, Ollama 0.40.14; qwen3:4b-instruct: 444
		instructionsMeasuredBytes = 1826
	)
	assert.LessOrEqual(t, len(instructions()), instructionsMeasuredBytes,
		"the prompt grew: measure its tokens again (prompt_eval_count with an empty page)")
	assert.GreaterOrEqual(t, instructionTokens, instructionsMeasured*5/4)
}
