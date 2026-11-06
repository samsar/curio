package rerank

import (
	"fmt"
	"strings"

	"github.com/samsar/curio/internal/understand"
)

// Method is how the model is asked about the candidates.
type Method string

// The methods.
const (
	// MethodListwise asks once for the candidates' numbers in order, under
	// a JSON schema (generator.JSONGenerator).
	MethodListwise Method = "listwise"
	// MethodPointwise asks once a candidate whether it is wanted, scored
	// by the likelihood of yes against no (generator.LogprobGenerator).
	MethodPointwise Method = "pointwise"
	// MethodSkip asks nothing: the first ranking stands.
	MethodSkip Method = "skip"
)

// Methods lists the methods that ask the model.
func Methods() []Method { return []Method{MethodListwise, MethodPointwise} }

// Group is a search's plan kind as it is routed: a plan's kinds, one, the
// topical kinds together (list, evidence, analysis) and keywords, or none
// for a search without a plan.
type Group string

// The groups.
const (
	GroupOne      Group = "one"
	GroupTopical  Group = "topical"
	GroupKeywords Group = "keywords"
	GroupNone     Group = "none"
)

// GroupOf is the group a search with a plan of kind is routed by; planned
// false is a search without a plan.
func GroupOf(kind understand.Kind, planned bool) Group {
	switch {
	case !planned:
		return GroupNone
	case kind == understand.KindOne:
		return GroupOne
	case kind == understand.KindKeywords:
		return GroupKeywords
	}
	return GroupTopical
}

// Blend is how the reranker's judgement and the first ranking make the
// head's order.
type Blend string

// The blends, over the candidates sent: r is a candidate's first-stage
// rank, ρ its place in the reranker's order, s its score (pointwise: the
// probability of yes against no; listwise: 1/ρ).
const (
	// BlendPure orders by the reranker alone, ties by r.
	BlendPure Blend = "pure"
	// BlendPosition weighs the first ranking more at its top:
	// w/r + (1-w)·s, w 0.75 for r 1 to 3, 0.60 for 4 to 10, 0.40 below.
	BlendPosition Blend = "position"
	// BlendRRF fuses the two orders: 1/(60+r) + 1/(60+ρ).
	BlendRRF Blend = "rrf"
)

// Blends lists the blends.
func Blends() []Blend { return []Blend{BlendPure, BlendPosition, BlendRRF} }

// Input is what of a candidate's page is sent besides what it is.
type Input string

// The inputs.
const (
	InputSummary Input = "summary"
	InputPassage Input = "passage"
	InputBoth    Input = "both"
)

// Textless is what is done with a candidate with no summary or passage.
type Textless string

// The textless modes.
const (
	// TextlessRead sends it, shown by what it was saved as, with a note
	// saying why its page can't be read.
	TextlessRead Textless = "read"
	// TextlessHeld sends it not at all, and keeps it at its first-stage
	// place.
	TextlessHeld Textless = "held"
)

// Inputs are what of each candidate is sent.
type Inputs struct {
	Input Input
	// Folders and Claims add the folders it was saved in, and its
	// summary's claims.
	Folders bool
	Claims  bool
}

// Settings are the reranker's choices: the measured ones, which the daemon
// fixes (Shipped) and cmd/searcheval varies, and the depth, which
// search.rerank_depth sets.
type Settings struct {
	// Routes is the method each group is reranked by; a group missing is
	// skipped, and keywords always is.
	Routes map[Group]Method
	// Force, when set, reranks every search by that method, keywords
	// included: for measuring a method alone.
	Force    Method
	Depth    int
	Blend    Blend
	Textless Textless
	Inputs
	// PointwiseBytes caps a pointwise candidate.
	PointwiseBytes int
}

// Shipped are the settings the daemon reranks by, but the depth, config's:
// the grid's pick by the rule's order, the known items' R@10 bar amended
// to what planning alone keeps (decisions.md "Choosing the reranker
// (written before the grid runs)"):
// the topical kinds listwise; a page remembered keeping its first
// ranking (reranking it too cost every search's median latency, 1.2 s to
// 3.9 s), a search without a plan too (on the hybrid base it lost
// known-item S@1, 0.973 to 0.946), and keywords always; the reranker's
// order alone, textless pages read with their note, summary and passage
// both, no folders or claims.
func Shipped() Settings {
	return Settings{
		Routes:         map[Group]Method{GroupTopical: MethodListwise},
		Blend:          BlendPure,
		Textless:       TextlessRead,
		Input:          InputBoth,
		PointwiseBytes: pointwiseBytes,
	}
}

// Validate checks s's values are of their sets and in range.
func (s Settings) Validate() error {
	for g, m := range s.Routes {
		switch {
		case g == GroupKeywords && m != MethodSkip:
			return fmt.Errorf("keywords are never reranked, got %s", m)
		case m != MethodListwise && m != MethodPointwise && m != MethodSkip:
			return fmt.Errorf("route %s: unknown method %q", g, m)
		}
	}
	switch {
	case s.Force != "" && s.Force != MethodListwise && s.Force != MethodPointwise:
		return fmt.Errorf("unknown method %q", s.Force)
	case s.Depth < 2 || s.Depth > MaxDepth:
		return fmt.Errorf("depth must be in [2, %d], got %d", MaxDepth, s.Depth)
	case s.Blend != BlendPure && s.Blend != BlendPosition && s.Blend != BlendRRF:
		return fmt.Errorf("unknown blend %q", s.Blend)
	case s.Input != InputSummary && s.Input != InputPassage && s.Input != InputBoth:
		return fmt.Errorf("unknown input %q", s.Input)
	case s.Textless != TextlessRead && s.Textless != TextlessHeld:
		return fmt.Errorf("unknown textless mode %q", s.Textless)
	case s.PointwiseBytes < 1:
		return fmt.Errorf("the pointwise cap must be positive, got %d", s.PointwiseBytes)
	}
	return nil
}

// MaxDepth is the most candidates a rerank reads, and the longest order a
// later page sends back: a listwise prompt of that many fits the context
// at the densest text (listwiseShare).
const MaxDepth = 30

// Family is what kind of model the reranker's is, by what it can be asked.
type Family string

// The families.
const (
	// FamilyInstruct is a general instruct model: listwise or pointwise,
	// through its own chat template.
	FamilyInstruct Family = "instruct"
	// FamilyQwen3Reranker is a Qwen3-Reranker build: a causal model
	// trained to answer yes or no in its own template, pointwise alone,
	// sent raw.
	FamilyQwen3Reranker Family = "qwen3-reranker"
)

// FamilyOf is model's family, by its name: a Qwen3-Reranker build's last
// path segment, its tag aside, starts with "qwen3-reranker", whatever its
// case and quantization.
func FamilyOf(model string) Family {
	name := model[strings.LastIndex(model, "/")+1:]
	if i := strings.IndexByte(name, ':'); i >= 0 {
		name = name[:i]
	}
	if strings.HasPrefix(strings.ToLower(name), "qwen3-reranker") {
		return FamilyQwen3Reranker
	}
	return FamilyInstruct
}

// needsPlan reports whether every route s takes for a model of family
// needs a plan: a search without one keeps its first ranking.
func (s Settings) needsPlan(f Family) bool { return s.route(GroupNone, f) == MethodSkip }

// route is the method a search of group is reranked by, under s, for a
// model of family: a Qwen3-Reranker build reranks every routed group
// pointwise, since it answers nothing else.
func (s Settings) route(g Group, f Family) Method {
	m := s.Force
	if m == "" {
		if g == GroupKeywords {
			return MethodSkip
		}
		var ok bool
		if m, ok = s.Routes[g]; !ok {
			return MethodSkip
		}
	}
	if m == MethodListwise && f == FamilyQwen3Reranker {
		return MethodPointwise
	}
	return m
}
