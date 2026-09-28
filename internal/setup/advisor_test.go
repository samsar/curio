package setup

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/samsar/curio/internal/config"
)

// TestAdvise: the writing model goes by unified memory, at the tiers'
// boundaries, and the smaller alternative is the tier below.
func TestAdvise(t *testing.T) {
	cases := []struct {
		memory  uint64
		want    string
		smaller string
	}{
		{8 * GiB, "qwen3:4b-instruct", ""},
		{16*GiB - 1, "qwen3:4b-instruct", ""},
		{16 * GiB, "gemma4:12b", "qwen3:4b-instruct"},
		{24 * GiB, "gemma4:12b", "qwen3:4b-instruct"},
		{32*GiB - 1, "gemma4:12b", "qwen3:4b-instruct"},
		{32 * GiB, "gemma4:26b-a4b-it-qat", "gemma4:12b"},
		{48 * GiB, "gemma4:26b-a4b-it-qat", "gemma4:12b"},
		{64*GiB - 1, "gemma4:26b-a4b-it-qat", "gemma4:12b"},
		{64 * GiB, "gemma4:26b", "gemma4:26b-a4b-it-qat"},
		{128 * GiB, "gemma4:26b", "gemma4:26b-a4b-it-qat"},
	}
	for _, tc := range cases {
		a := ModelAdvisor{}.Advise(tc.memory)
		assert.Equal(t, EmbeddingModel, a.Embedding, "%d", tc.memory)
		assert.Equal(t, tc.want, a.Generation.Name, "%d bytes", tc.memory)
		if tc.smaller == "" {
			assert.Nil(t, a.Smaller, "%d bytes", tc.memory)
			continue
		}
		require.NotNil(t, a.Smaller, "%d bytes", tc.memory)
		assert.Equal(t, tc.smaller, a.Smaller.Name, "%d bytes", tc.memory)
	}
}

// TestTiers: every tier has a pinned tag, a size and a reason, and gemma4
// names the Ollama it needs.
func TestTiers(t *testing.T) {
	tiers := ModelAdvisor{}.Tiers()
	require.Len(t, tiers, 4)
	for _, tier := range append(tiers, Tier{Model: EmbeddingModel}) {
		m := tier.Model
		assert.True(t, hasTag(m.Name), m.Name)
		assert.Positive(t, m.Size, m.Name)
		assert.NotEmpty(t, m.Reason, m.Name)
		if modelFamily(m.Name) == "gemma4" {
			assert.NotEmpty(t, m.MinOllama, m.Name)
		}
	}
	assert.Equal(t, config.Default().Embedding.Model, EmbeddingModel.Name, "the default home's model")
	assert.Equal(t, uint64(639e6), EmbeddingModel.Size)
}

func TestKnownModel(t *testing.T) {
	assert.Equal(t, uint64(19e9), knownModel("gemma4:26b").Size)
	assert.Equal(t, "0.30.5", knownModel("gemma4:26b").MinOllama)
	assert.Equal(t, Model{Name: "mistral-small:24b"}, knownModel("mistral-small:24b"), "size and minimum unknown")
}

func TestModelPrompts(t *testing.T) {
	p, ok := modelPrompts("qwen3-embedding:0.6b")
	assert.True(t, ok)
	assert.Equal(t, prompts{document: "", query: config.QwenQueryPrefix}, p)
	p, ok = modelPrompts("registry.example:5000/library/nomic-embed-text:v1.5")
	assert.True(t, ok)
	assert.Equal(t, prompts{document: "search_document: ", query: "search_query: "}, p)
	_, ok = modelPrompts("mxbai-embed-large:335m")
	assert.False(t, ok)
}

func TestHasTag(t *testing.T) {
	assert.True(t, hasTag("qwen3-embedding:0.6b"))
	assert.False(t, hasTag("qwen3-embedding"))
	assert.False(t, hasTag("registry.example:5000/qwen3-embedding"), "a registry's port is no tag")
	assert.True(t, hasTag("registry.example:5000/qwen3-embedding:0.6b"))
}
