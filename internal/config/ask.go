package config

import (
	"fmt"

	"github.com/samsar/curio/internal/ollama"
)

// Ask configures curio ask: a question searched as a search is, and
// answered by a writing model (the answers role) from the pages found,
// every claim citing the page it comes from. See docs/decisions.md "Ask: a
// cited, streamed answer over the search path".
type Ask struct {
	// Enabled answers questions. True is on with any model, its fitness
	// check guarding it; false is off; left out (nil) it is on only for a
	// model measured as the answer writer (AskOn): the check's three
	// probes are a floor, not the bars a measured model met, and an
	// unmeasured one it refused would warn in every curio status. The
	// daemon never pulls the role's model for it alone: curio up does,
	// with consent, or another role's pull of the same model.
	Enabled *bool `yaml:"enabled"`
	// Sources is how many pages an answer is written from. Default 6, in
	// [1, MaxAskSources]: the ask measurement's pick (docs/decisions.md
	// "Choosing Ask's prompt and K"), more facts stated than at 8, the
	// first two sources' larger allowances the larger with fewer beside
	// them.
	Sources int `yaml:"sources"`
	// MaxTokens caps an answer's length in tokens. Default 768, in
	// [MinAskTokens, MaxAskTokens].
	MaxTokens int `yaml:"max_tokens"`
	// TimeoutSeconds bounds a whole ask: waiting for the model to load
	// and pass its check, the search and the answer. Default 90, in
	// [15, 300].
	TimeoutSeconds int `yaml:"timeout_seconds"`
	// Rerank reorders the head of an ask's search with the reranker role
	// before its sources are read, as a search's own rerank does
	// (search.rerank governs searches alone). A request may say otherwise.
	// Default false, the ask measurement's pick (docs/decisions.md
	// "Choosing Ask's prompt and K"): the writer reads its sources itself,
	// and without the rerank's 2.9 s (p50) the first token met its bar,
	// every other bar held and fact coverage rose.
	Rerank bool `yaml:"rerank"`
}

// The bounds of the ask keys. At most 12 sources, so each still gets a
// useful share of the writer's 8,192-token context beside the instructions
// and the answer; 128 answer tokens hold two cited sentences, and 2,048 is
// about a quarter of the context. 15 s is a warm answer's search and
// writing; 300 s covers a cold load and the fitness check on a slow Mac.
const (
	MaxAskSources = 12
	MinAskTokens  = 128
	MaxAskTokens  = 2048
	minAskSeconds = 15
	maxAskSeconds = 300
)

// defaultAsk is Ask's defaults: on as AskOn says. The timeout covers the
// first ask after a restart: gemma4:26b's cold load took 10.7 s on Ollama
// 0.40.16, then the fitness check's three answers and the answer itself.
func defaultAsk() Ask {
	return Ask{Sources: 6, MaxTokens: 768, TimeoutSeconds: 90}
}

// askMeasured are the models measured as the answer writer against Ask's
// bars, by name as ollama.NormalizeModel writes it (docs/decisions.md
// "Choosing Ask's prompt and K"): gemma4:26b on the operator's library,
// 40 questions, every bar met at ask.sources 6 without Ask's rerank. A
// model not listed is unmeasured: with ask.enabled left out it answers
// nothing, and true turns it on, the fitness check guarding it. The tiers
// curio up picks below it (qwen3:4b-instruct, gemma4:12b,
// gemma4:26b-a4b-it-qat) pass the check, but none was measured on the
// questions.
var askMeasured = map[string]bool{"gemma4:26b": true}

// AskMeasured reports whether model was measured as the answer writer.
// Names match as Ollama's do: case aside, and an untagged name being its
// :latest.
func AskMeasured(model string) bool { return askMeasured[ollama.NormalizeModel(model)] }

// AskOn reports whether questions are answered: as ask.enabled says when
// config.yaml sets it, and left out, only with a model measured as the
// answer writer (AskMeasured).
func (c Config) AskOn() bool {
	if c.Ask.Enabled != nil {
		return *c.Ask.Enabled
	}
	return AskMeasured(c.Generation.Role(RoleAnswers).Model)
}

// AskOffByDefault reports whether Ask is off because config.yaml leaves
// ask.enabled out and the answers role's model isn't measured.
func (c Config) AskOffByDefault() bool { return c.Ask.Enabled == nil && !c.AskOn() }

// validateAsk holds the ask keys to their bounds, on or off, so turning
// Ask on never finds a config.yaml the daemon would then refuse.
func validateAsk(a Ask) error {
	for _, k := range []struct {
		key       string
		v, lo, hi int
	}{
		{"ask.sources", a.Sources, 1, MaxAskSources},
		{"ask.max_tokens", a.MaxTokens, MinAskTokens, MaxAskTokens},
		{"ask.timeout_seconds", a.TimeoutSeconds, minAskSeconds, maxAskSeconds},
	} {
		if k.v < k.lo || k.v > k.hi {
			return fmt.Errorf("%s must be in [%d, %d], got %d", k.key, k.lo, k.hi, k.v)
		}
	}
	return nil
}
