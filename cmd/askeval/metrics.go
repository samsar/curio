package main

import (
	"slices"
	"strconv"
	"time"
)

// answered is one question's ask as the evaluation scores it.
type answered struct {
	q        question
	route    string
	status   string
	evidence string // the evidence route's status, "" on the others
	cited    []int
	// markers are the table rows or groups the answer cites, on the
	// interests and list routes.
	markers   []string
	invalid   int
	abstained bool
	// citedURLs are the normalized URLs of the pages the answer cites: its
	// sources, or on the list route every page of each group it cites.
	citedURLs []string
	sents     []string
	// sentCited says, by sentence, whether it carries a valid citation,
	// and sentCitations its citations as written.
	sentCited     []bool
	sentCitations [][]string
	// verdicts are the judge's, and verdicts2 the second judge's, each
	// when judged.
	verdicts, verdicts2 judgment
	judged, judged2     bool
	// sources are what the answer was written from (their URLs), and
	// answer the answer, for the private report; material is what its
	// writer read (ask.Material of its prompt), which the judge reads.
	sources  []string
	answer   string
	material string
	// stages are every stage's timing, and promptTokens and outputTokens
	// the answer call's, 0 when unknown.
	stages                     stageMS
	promptTokens, outputTokens int
	// timingsReplayed says the stages are an answer book's or a recorded
	// report's, recorded when the ask was made, not this run's.
	timingsReplayed bool
}

// metrics are what a run measured over some questions; a rate is -1
// when nothing could be counted for it.
type metrics struct {
	Questions int `json:"questions"`
	// Unjudged are the answers the judge's verdict couldn't be used for,
	// left out of support, claims cited and fact coverage.
	Unjudged         int     `json:"unjudged"`
	CitationValidity float64 `json:"citation_validity"`
	CitationSupport  float64 `json:"citation_support"`
	ClaimsCited      float64 `json:"claims_cited"`
	SourceRecall     float64 `json:"source_recall"`
	FactCoverage     float64 `json:"fact_coverage"`
	Abstention       float64 `json:"abstention"`
	FalseAbstention  float64 `json:"false_abstention"`
	Routing          float64 `json:"routing"`
	FirstTokenP50MS  int64   `json:"first_token_p50_ms"`
	FirstTokenP95MS  int64   `json:"first_token_p95_ms"`
	TotalP50MS       int64   `json:"total_p50_ms"`
	TotalP95MS       int64   `json:"total_p95_ms"`
	// Stages are each stage's p50 and p95, over the asks it ran in.
	Stages []stageStat `json:"stages"`
	// PromptTokens and OutputTokens are the answer call's, over the asks
	// whose call reported them.
	PromptTokens tokenStat `json:"prompt_tokens"`
	OutputTokens tokenStat `json:"output_tokens"`
	// TimingsReplayed are the questions whose timings are an answer
	// book's or a recorded report's, recorded when the ask was made.
	TimingsReplayed int `json:"timings_replayed"`
}

// stageStat is one stage's p50 and p95 over the n asks it ran in (took
// more than 0 ms).
type stageStat struct {
	Stage string `json:"stage"`
	N     int    `json:"n"`
	P50MS int64  `json:"p50_ms"`
	P95MS int64  `json:"p95_ms"`
}

// tokenStat is a token count's p50 and p95 over n asks.
type tokenStat struct {
	N   int `json:"n"`
	P50 int `json:"p50"`
	P95 int `json:"p95"`
}

// stageNames are the stages a report gives, in their order.
var stageNames = []string{"wait", "plan", "search", "rerank", "sources", "judge", "prompt_eval", "first_token", "total"}

// stage is s's timing of the stage named name.
func (s stageMS) stage(name string) int64 {
	return map[string]int64{"wait": s.Wait, "plan": s.Plan, "search": s.Search, "rerank": s.Rerank,
		"sources": s.Sources, "judge": s.Judge, "prompt_eval": s.PromptEval, "first_token": s.FirstToken,
		"total": s.Total}[name]
}

// stageStats are each stage's p50 and p95 over the asks it ran in.
func stageStats(as []answered) []stageStat {
	out := make([]stageStat, 0, len(stageNames))
	for _, name := range stageNames {
		var ds []time.Duration
		for _, a := range as {
			if ms := a.stages.stage(name); ms > 0 {
				ds = append(ds, time.Duration(ms)*time.Millisecond)
			}
		}
		out = append(out, stageStat{Stage: name, N: len(ds), P50MS: percentile(ds, 50), P95MS: percentile(ds, 95)})
	}
	return out
}

// tokens is the p50 and p95 of the counts above 0.
func tokens(counts []int) tokenStat {
	var known []int
	for _, c := range counts {
		if c > 0 {
			known = append(known, c)
		}
	}
	if len(known) == 0 {
		return tokenStat{}
	}
	slices.Sort(known)
	at := func(p int) int { return known[max((p*len(known)+99)/100, 1)-1] }
	return tokenStat{N: len(known), P50: at(50), P95: at(95)}
}

// ratio is n/d, -1 when d is 0.
func ratio(n, d float64) float64 {
	if d == 0 {
		return -1
	}
	return n / d
}

// measure computes the metrics of as, by the definitions written before
// the measurement (docs/decisions.md "Choosing Ask's prompt and K").
func measure(as []answered) metrics {
	var (
		valid, all                 float64
		supported, judgedClaims    float64
		cited, claims              float64
		recalled, withSources      float64
		stated, facts              float64
		abstained, unanswerable    float64
		falseAbstained, answerable float64
		routed                     float64
		unjudged, replayed         int
		firsts, totals             []time.Duration
		prompts, outputs           []int
	)
	for _, a := range as {
		if a.timingsReplayed {
			replayed++
		}
		valid += float64(len(a.cited) + len(a.markers))
		all += float64(len(a.cited) + len(a.markers) + a.invalid)
		if a.route == a.q.wantRoute() {
			routed++
		}
		if a.stages.FirstToken > 0 {
			firsts = append(firsts, time.Duration(a.stages.FirstToken)*time.Millisecond)
		}
		totals = append(totals, time.Duration(a.stages.Total)*time.Millisecond)
		prompts, outputs = append(prompts, a.promptTokens), append(outputs, a.outputTokens)
		switch {
		case !a.q.answerable():
			unanswerable++
			if a.abstained {
				abstained++
			}
		default:
			answerable++
			if a.abstained {
				falseAbstained++
			}
			if len(a.q.Sources) > 0 {
				withSources++
				if slices.ContainsFunc(a.citedURLs, func(u string) bool { return slices.Contains(a.q.Sources, u) }) {
					recalled++
				}
			}
		}
		if !a.judged {
			if a.status != "no_sources" && len(a.sents) > 0 {
				unjudged++
			}
			continue
		}
		for _, v := range a.verdicts.Sentences {
			if !v.Claim {
				continue
			}
			claims++
			if !a.sentCited[v.N-1] {
				continue
			}
			cited++
			switch v.Support {
			case "supported":
				judgedClaims++
				supported++
			case "partly":
				judgedClaims++
				supported += 0.5
			case "not":
				judgedClaims++
			}
		}
		for _, v := range a.verdicts.Facts {
			facts++
			if v.Stated {
				stated++
			}
		}
	}
	return metrics{Questions: len(as), Unjudged: unjudged, CitationValidity: ratio(valid, all),
		CitationSupport: ratio(supported, judgedClaims), ClaimsCited: ratio(cited, claims),
		SourceRecall: ratio(recalled, withSources), FactCoverage: ratio(stated, facts),
		Abstention: ratio(abstained, unanswerable), FalseAbstention: ratio(falseAbstained, answerable),
		Routing: ratio(routed, float64(len(as))), FirstTokenP50MS: percentile(firsts, 50),
		FirstTokenP95MS: percentile(firsts, 95), TotalP50MS: percentile(totals, 50),
		TotalP95MS: percentile(totals, 95), Stages: stageStats(as), PromptTokens: tokens(prompts),
		OutputTokens: tokens(outputs), TimingsReplayed: replayed}
}

// byJudge2 is as as the second judge judged it.
func byJudge2(as []answered) []answered {
	out := slices.Clone(as)
	for i := range out {
		out[i].verdicts, out[i].judged = out[i].verdicts2, out[i].judged2
	}
	return out
}

// agreement is how two judges' labels of the same items agree: how many
// items both labelled, the share they labelled alike (-1 for none), and
// Cohen's kappa, that agreement beyond what chance gives with each
// judge's own mix of labels; nil when it is undefined, every item given
// one same label by both.
type agreement struct {
	Items int      `json:"items"`
	Raw   float64  `json:"raw"`
	Kappa *float64 `json:"kappa"`
}

// agree is the agreement of pairs, each an item's two labels.
func agree(pairs [][2]string) agreement {
	n := float64(len(pairs))
	if n == 0 {
		return agreement{Raw: -1}
	}
	var same float64
	first, second := map[string]float64{}, map[string]float64{}
	for _, p := range pairs {
		if p[0] == p[1] {
			same++
		}
		first[p[0]]++
		second[p[1]]++
	}
	var chance float64
	for label, c := range first {
		chance += (c / n) * (second[label] / n)
	}
	out := agreement{Items: len(pairs), Raw: same / n}
	if chance < 1 {
		kappa := (out.Raw - chance) / (1 - chance)
		out.Kappa = &kappa
	}
	return out
}

// judgeAgreement is how the second judge's verdicts agree with the
// first's, over the answers both judged: each sentence's label (its
// support, or none for a sentence that makes no claim); the support of
// the cited sentences both call a claim, which citation support counts;
// and whether each fact is stated.
type judgeAgreement struct {
	Sentences agreement `json:"sentences"`
	Support   agreement `json:"support"`
	Facts     agreement `json:"facts"`
}

// agreeJudges is the judges' agreement over as.
func agreeJudges(as []answered) judgeAgreement {
	var sents, support, facts [][2]string
	label := func(v sentenceVerdict) string {
		if !v.Claim {
			return "none"
		}
		return v.Support
	}
	for _, a := range as {
		if !a.judged || !a.judged2 {
			continue
		}
		one, two := numbered(a.verdicts.Sentences), numbered(a.verdicts2.Sentences)
		for n := 1; n <= len(a.sents); n++ {
			v1, v2 := one[n], two[n]
			sents = append(sents, [2]string{label(v1), label(v2)})
			if a.sentCited[n-1] && v1.Claim && v2.Claim {
				support = append(support, [2]string{v1.Support, v2.Support})
			}
		}
		f1, f2 := numbered(a.verdicts.Facts), numbered(a.verdicts2.Facts)
		for n := 1; n <= len(a.q.Facts); n++ {
			facts = append(facts, [2]string{strconv.FormatBool(f1[n].Stated), strconv.FormatBool(f2[n].Stated)})
		}
	}
	return judgeAgreement{Sentences: agree(sents), Support: agree(support), Facts: agree(facts)}
}

// numbered is vs by their numbers: a judgment names each sentence and
// fact once, in any order (decodeJudgment).
func numbered[V interface{ number() int }](vs []V) map[int]V {
	out := make(map[int]V, len(vs))
	for _, v := range vs {
		out[v.number()] = v
	}
	return out
}

func (v sentenceVerdict) number() int { return v.N }
func (v factVerdict) number() int     { return v.N }

// percentile is the nearest-rank pth percentile of ds in milliseconds, 0
// for none.
func percentile(ds []time.Duration, p int) int64 {
	if len(ds) == 0 {
		return 0
	}
	sorted := slices.Sorted(slices.Values(ds))
	rank := (p*len(sorted) + 99) / 100
	return sorted[max(rank, 1)-1].Milliseconds()
}
