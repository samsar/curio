package rerank

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/samsar/curio/internal/generator"
)

// pointwise asks once a candidate, in first-stage order, whether it is
// wanted, and scores it by the likelihood of yes against no. Ollama runs a
// model's requests one at a time, so the calls are sequential.
type pointwise struct {
	gen       generator.LogprobGenerator
	family    Family
	keepAlive time.Duration
}

// judge scores each of shown for question: a candidate whose answer names
// neither yes nor no among the provider's likeliest first tokens is
// unscored, and keeps its place; fewer than two scored is no judgement.
func (p pointwise) judge(ctx context.Context, question string, shown []Candidate, s Settings) (judgement, error) {
	j := judgement{score: make([]float64, len(shown)), scored: make([]bool, len(shown))}
	scored := 0
	for i, c := range shown {
		odds, ok, bounded, err := p.logOdds(ctx, question, c, s)
		if err != nil {
			return judgement{}, err
		}
		if !ok {
			continue
		}
		j.score[i], j.scored[i] = sigmoid(odds), true
		scored++
		if bounded {
			j.bounded++
		}
	}
	if scored < 2 {
		return judgement{}, &invalidAnswer{"unscored", fmt.Sprintf("%d of %d candidates could be scored",
			scored, len(shown))}
	}
	j.place = placesByScore(j.score, j.scored)
	return j, nil
}

// logOdds asks whether c is wanted for question, and is the log odds of
// yes against no its first token gives (logOdds of the top tokens).
func (p pointwise) logOdds(ctx context.Context, question string, c Candidate, s Settings) (odds float64, ok,
	bounded bool, err error) {
	prompt, raw := pointwisePrompt(question, c, p.family, s)
	top, err := p.gen.FirstTokens(ctx, prompt, raw, generator.MaxTopTokens, generator.Options{KeepAlive: p.keepAlive})
	if err != nil {
		return 0, false, false, err
	}
	odds, ok, bounded = logOdds(top)
	return odds, ok, bounded, nil
}

// logOdds is log Σ p(yes) − log Σ p(no) over top, the likeliest first
// tokens, each token counted whose text, trimmed and in lower case, is
// exactly the word: case and a leading space are a tokenizer's variants of
// one answer. A side missing from top is less likely than every token in
// it, so it is bounded by the least likely (bounded). With neither side in
// top there is no score (ok false), never an even one.
func logOdds(top []generator.TokenChoice) (odds float64, ok, bounded bool) {
	var yes, no []float64
	least := math.Inf(1)
	for _, t := range top {
		least = min(least, t.Logprob)
		switch strings.ToLower(strings.TrimSpace(t.Token)) {
		case "yes":
			yes = append(yes, t.Logprob)
		case "no":
			no = append(no, t.Logprob)
		}
	}
	switch {
	case len(yes) == 0 && len(no) == 0:
		return 0, false, false
	case len(yes) == 0:
		return least - logSumExp(no), true, true
	case len(no) == 0:
		return logSumExp(yes) - least, true, true
	}
	return logSumExp(yes) - logSumExp(no), true, false
}

// sigmoid is the probability log odds x stand for.
func sigmoid(x float64) float64 { return 1 / (1 + math.Exp(-x)) }

// logSumExp is log Σ e^x over xs, none empty, without overflow.
func logSumExp(xs []float64) float64 {
	top := xs[0]
	for _, x := range xs[1:] {
		top = max(top, x)
	}
	sum := 0.0
	for _, x := range xs {
		sum += math.Exp(x - top)
	}
	return top + math.Log(sum)
}
