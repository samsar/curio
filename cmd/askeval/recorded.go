package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/samsar/curio/internal/ask"
	"github.com/samsar/curio/internal/urlutil"
)

// variantRecorded is the variant that takes the questions the answer route
// answers from an earlier report, as it recorded them, and asks the rest
// live as ask does: an earlier prompt's answers scored beside routes it
// never had. No answer book can replay them, since every prompt's version
// hashes every template.
const variantRecorded = "recorded"

// recordedVariant is the variant of a recorded report whose answers the
// recorded variant takes.
const recordedVariant = "ask"

// readRecorded reads the report at opts.recorded, refusing one of another
// question file, or judged by another judge than -judge, or, with -judge2
// set, by another second judge, whose verdicts wouldn't be this run's.
// Without -judge2 its second judge's labels go unused: a run asks the
// writer and the first judge, and a replay of its books adds the second,
// so that three models never contend for the memory one answer needs.
func readRecorded(opts options, qf questionFile) (*report, error) {
	data, err := os.ReadFile(opts.recorded)
	if err != nil {
		return nil, fmt.Errorf("the recorded report: %w", err)
	}
	var r report
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("the recorded report %s: %w", opts.recorded, err)
	}
	switch {
	case r.QuestionsSHA256 != qf.sha256:
		return nil, fmt.Errorf("the recorded report %s is of another question file", opts.recorded)
	case r.Judge != opts.judgeModel:
		return nil, fmt.Errorf("the recorded report %s was judged by %s, not -judge %s", opts.recorded, r.Judge,
			opts.judgeModel)
	case opts.judge2Model != "" && r.Judge2 != opts.judge2Model:
		return nil, fmt.Errorf("the recorded report %s was judged second by %q, not -judge2 %q", opts.recorded,
			r.Judge2, opts.judge2Model)
	}
	return &r, nil
}

// answeredOf is q as r's ask variant answered and judged it, its timings
// marked replayed: its answer's sentences, their citations as the answer
// route reads them, its sources and both judges' labels. False when r
// holds no answer to q.
func (r *report) answeredOf(q question) (answered, bool) {
	var found *questionResult
	for i := range r.Variants {
		if r.Variants[i].Name != recordedVariant {
			continue
		}
		for j := range r.Variants[i].Questions {
			if r.Variants[i].Questions[j].ID == q.ID {
				found = &r.Variants[i].Questions[j]
			}
		}
	}
	if found == nil {
		return answered{}, false
	}
	a := answered{q: q, route: found.Route, status: found.Status, evidence: found.Evidence, cited: found.Citations,
		markers: found.Markers, invalid: found.Invalid, abstained: found.Abstained, sources: found.Sources,
		stages: stageMS{FirstToken: found.FirstMS, Total: found.TotalMS}, timingsReplayed: true}
	if found.Stages != nil {
		a.stages = *found.Stages
	}
	a.promptTokens, a.outputTokens = found.PromptTokens, found.OutputTokens
	for _, n := range found.Citations {
		if n >= 1 && n <= len(found.Sources) {
			if u, err := urlutil.Normalize(found.Sources[n-1]); err == nil {
				a.citedURLs = append(a.citedURLs, u)
			}
		}
	}
	a.judged = found.Status != string(ask.StatusNoSources)
	a.judged2 = a.judged && r.Judge2 != ""
	for i, s := range found.Sentences {
		a.sents = append(a.sents, s.Text)
		a.answer += s.Text + " "
		a.sentCited = append(a.sentCited, s.Cited)
		a.sentCitations = append(a.sentCitations, written(ask.CitationsOf(s.Text, ask.Route(found.Route),
			len(found.Sources))))
		a.verdicts.Sentences = append(a.verdicts.Sentences, sentenceVerdict{N: i + 1, Claim: s.Claim,
			Support: s.Support})
		a.judged = a.judged && s.Support != ""
		if s.Claim2 == nil {
			a.judged2 = false
			continue
		}
		a.verdicts2.Sentences = append(a.verdicts2.Sentences, sentenceVerdict{N: i + 1, Claim: *s.Claim2,
			Support: s.Support2})
	}
	for i, f := range found.Facts {
		a.verdicts.Facts = append(a.verdicts.Facts, factVerdict{N: i + 1, Stated: f.Stated})
		if f.Stated2 == nil {
			a.judged2 = false
			continue
		}
		a.verdicts2.Facts = append(a.verdicts2.Facts, factVerdict{N: i + 1, Stated: *f.Stated2})
	}
	return a, true
}
