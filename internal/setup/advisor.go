package setup

import (
	"strings"

	"github.com/samsar/curio/internal/config"
)

// Model is a model curio may pull.
type Model struct {
	Name string // a pinned Ollama tag
	Size uint64 // the download, in bytes; 0 when unknown
	// Reason says in a line why curio picks it.
	Reason string
	// MinOllama is the oldest Ollama that runs it; empty when any does.
	MinOllama string
}

// EmbeddingModel is the model a new home embeds with.
var EmbeddingModel = Model{
	Name:   "qwen3-embedding:0.6b",
	Size:   639e6,
	Reason: "embeds pages and searches: small, multilingual, a 32K-token context",
}

// Tier is the writing model for Macs with at least MinMemory of memory.
type Tier struct {
	MinMemory uint64
	Model     Model
}

// generationTiers are the writing models by unified memory, smallest
// first. gemma4's QAT tags came with Ollama 0.30.6; gemma4:12b needs
// 0.30.5, which fixed its crash.
var generationTiers = []Tier{
	{0, Model{Name: "qwen3:4b-instruct", Size: 2.5e9,
		Reason: "names your interests; fits next to the embedder in 8 GB"}},
	{16 * GiB, Model{Name: "gemma4:12b", Size: 7.6e9, MinOllama: "0.30.5",
		Reason: "names your interests; a stronger writer that fits 16 GB"}},
	{32 * GiB, Model{Name: "gemma4:26b-a4b-it-qat", Size: 16e9, MinOllama: "0.30.6",
		Reason: "names your interests; the 26B mixture of experts, quantization-aware, for 32 GB"}},
	{64 * GiB, Model{Name: "gemma4:26b", Size: 19e9, MinOllama: "0.30.5",
		Reason: "names your interests; the full 26B model, for 64 GB or more"}},
}

// Advice is the models curio picks for a machine.
type Advice struct {
	Embedding  Model
	Generation Model
	// Smaller is the writing model of the tier below, offered in its
	// place; nil at the smallest tier.
	Smaller *Model
}

// ModelAdvisor picks curio's models for a machine from a static table:
// the embedding model is the same everywhere, and the writing model goes
// by unified memory.
type ModelAdvisor struct{}

// Advise picks the models for a machine with memory bytes of unified
// memory.
func (ModelAdvisor) Advise(memory uint64) Advice {
	i := 0
	for j, t := range generationTiers {
		if memory >= t.MinMemory {
			i = j
		}
	}
	a := Advice{Embedding: EmbeddingModel, Generation: generationTiers[i].Model}
	if i > 0 {
		smaller := generationTiers[i-1].Model
		a.Smaller = &smaller
	}
	return a
}

// Tiers lists every tier, smallest first, for the user to choose from.
func (ModelAdvisor) Tiers() []Tier { return append([]Tier(nil), generationTiers...) }

// knownModel is what curio knows of a model it may be told to use: its
// size and the Ollama it needs, when it is one of curio's picks.
func knownModel(name string) Model {
	if name == EmbeddingModel.Name {
		return EmbeddingModel
	}
	for _, t := range generationTiers {
		if t.Model.Name == name {
			return t.Model
		}
	}
	return Model{Name: name}
}

// prompts are an embedding model's prompt prefixes, for documents and for
// queries.
type prompts struct {
	document, query string
}

// knownPrompts are the prompt prefixes of the embedding models curio
// knows, by family: Qwen3-Embedding instructs queries only;
// nomic-embed-text prefixes both.
var knownPrompts = map[string]prompts{
	"qwen3-embedding":  {document: "", query: config.QwenQueryPrefix},
	"nomic-embed-text": {document: "search_document: ", query: "search_query: "},
}

// modelPrompts returns the prompt prefixes for an embedding model, and
// whether curio knows them. An unknown model gets none.
func modelPrompts(model string) (prompts, bool) {
	p, ok := knownPrompts[modelFamily(model)]
	return p, ok
}

// modelFamily is a model's name without its tag or registry path:
// "qwen3-embedding" for "qwen3-embedding:0.6b".
func modelFamily(model string) string {
	name := model[strings.LastIndex(model, "/")+1:]
	if i := strings.Index(name, ":"); i >= 0 {
		name = name[:i]
	}
	return strings.ToLower(name)
}

// hasTag reports whether a model name pins a tag. The tag follows a ':' in
// the last path segment; a ':' before the last '/' belongs to a
// registry's port.
func hasTag(model string) bool {
	return strings.Contains(model[strings.LastIndex(model, "/")+1:], ":")
}
