package main

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
)

// report is everything a run measured. Its JSON is private: it holds the
// questions, and each answer's sentences and verdicts, for a person to
// spot-check the judge. The text names questions by id alone.
type report struct {
	GeneratedAt     time.Time `json:"generated_at"`
	Questions       string    `json:"questions"`
	QuestionsSHA256 string    `json:"questions_sha256"`
	Writer          string    `json:"writer"`
	PromptVersion   string    `json:"prompt_version"`
	Judge           string    `json:"judge"`
	Judge2          string    `json:"judge2,omitempty"`
	JudgeVersion    string    `json:"judge_version"`
	Today           string    `json:"today"`
	Sources         int       `json:"sources"`
	MaxTokens       int       `json:"max_tokens"`
	Rerank          bool      `json:"rerank"`
	// RecordedPromptVersion is the prompt version of the report the
	// recorded variant takes its answer-route questions from.
	RecordedPromptVersion string            `json:"recorded_prompt_version,omitempty"`
	Checks                map[string]string `json:"checks"`
	Variants              []variantResult   `json:"variants"`
	// Evicted are the models Ollama held before the run and didn't after.
	Evicted []string `json:"evicted,omitempty"`
}

// variantResult is one variant's metrics, overall and by question kind;
// with a second judge, the metrics as it judged them and how its verdicts
// agree with the first's; and each question's answer.
type variantResult struct {
	Name      string             `json:"name"`
	Overall   metrics            `json:"overall"`
	ByKind    map[string]metrics `json:"by_kind"`
	Judge2    *metrics           `json:"judge2,omitempty"`
	Agreement *judgeAgreement    `json:"agreement,omitempty"`
	Questions []questionResult   `json:"questions"`
}

// questionResult is one question's answer and how it was judged.
type questionResult struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Question  string   `json:"question"`
	Route     string   `json:"route"`
	Evidence  string   `json:"evidence,omitempty"`
	Status    string   `json:"status"`
	Citations []int    `json:"citations,omitempty"`
	Markers   []string `json:"markers,omitempty"`
	Invalid   int      `json:"invalid_citations,omitempty"`
	Abstained bool     `json:"abstained,omitempty"`
	Sources   []string `json:"sources"`
	FirstMS   int64    `json:"first_token_ms"`
	TotalMS   int64    `json:"total_ms"`
	Stages    *stageMS `json:"stages,omitempty"`
	// PromptTokens and OutputTokens are the answer call's.
	PromptTokens int `json:"prompt_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
	// TimingsReplayed says the timings are the answer book's.
	TimingsReplayed bool            `json:"timings_replayed,omitempty"`
	Sentences       []sentenceEntry `json:"sentences,omitempty"`
	Facts           []factEntry     `json:"facts,omitempty"`
}

// sentenceEntry is a sentence of an answer, its citations as written, and
// its verdicts: the judge's, and the second judge's (Claim2, Support2)
// when it judged the answer.
type sentenceEntry struct {
	Text      string   `json:"text"`
	Citations []string `json:"citations,omitempty"`
	Cited     bool     `json:"cited"`
	Claim     bool     `json:"claim"`
	Support   string   `json:"support"`
	Claim2    *bool    `json:"claim2,omitempty"`
	Support2  string   `json:"support2,omitempty"`
}

// factEntry is a fact and whether the answer states it, by the judge and
// by the second judge.
type factEntry struct {
	Fact    string `json:"fact"`
	Stated  bool   `json:"stated"`
	Stated2 *bool  `json:"stated2,omitempty"`
}

// variantReport is as's metrics and records, for variant name; judged2
// says a second judge judged them.
func variantReport(name string, as []answered, judged2 bool) variantResult {
	out := variantResult{Name: name, Overall: measure(as), ByKind: map[string]metrics{}}
	if judged2 {
		m, agreed := measure(byJudge2(as)), agreeJudges(as)
		out.Judge2, out.Agreement = &m, &agreed
	}
	for _, kind := range kinds {
		var of []answered
		for _, a := range as {
			if a.q.Kind == kind {
				of = append(of, a)
			}
		}
		if len(of) > 0 {
			out.ByKind[kind] = measure(of)
		}
	}
	for _, a := range as {
		stages := a.stages
		qr := questionResult{ID: a.q.ID, Kind: a.q.Kind, Question: a.q.Question, Route: a.route,
			Evidence: a.evidence, Status: a.status, Citations: a.cited, Markers: a.markers, Invalid: a.invalid,
			Abstained: a.abstained, Sources: a.sources, FirstMS: stages.FirstToken, TotalMS: stages.Total,
			Stages: &stages, PromptTokens: a.promptTokens, OutputTokens: a.outputTokens,
			TimingsReplayed: a.timingsReplayed}
		verdicts, verdicts2 := numbered(a.verdicts.Sentences), numbered(a.verdicts2.Sentences)
		for i, sent := range a.sents {
			v := verdicts[i+1]
			e := sentenceEntry{Text: sent, Cited: a.sentCited[i], Claim: v.Claim, Support: v.Support}
			if i < len(a.sentCitations) {
				e.Citations = a.sentCitations[i]
			}
			if v2, ok := verdicts2[i+1]; ok && a.judged2 {
				e.Claim2, e.Support2 = &v2.Claim, v2.Support
			}
			qr.Sentences = append(qr.Sentences, e)
		}
		stated, stated2 := numbered(a.verdicts.Facts), numbered(a.verdicts2.Facts)
		for i, f := range a.q.Facts {
			e := factEntry{Fact: f, Stated: stated[i+1].Stated}
			if v2, ok := stated2[i+1]; ok && a.judged2 {
				e.Stated2 = &v2.Stated
			}
			qr.Facts = append(qr.Facts, e)
		}
		out.Questions = append(out.Questions, qr)
	}
	return out
}

// writeText writes the report for a person: numbers, and questions by id.
func (r report) writeText(w io.Writer) error {
	var b strings.Builder
	judges := r.Judge
	if r.Judge2 != "" {
		judges += " and " + r.Judge2
	}
	fmt.Fprintf(&b, "askeval %s: writer %s (prompt %.8s), judge %s, %d sources, %d tokens, rerank %v, today %s\n",
		r.GeneratedAt.Format(time.RFC3339), r.Writer, r.PromptVersion, judges, r.Sources, r.MaxTokens, r.Rerank,
		r.Today)
	if r.RecordedPromptVersion != "" {
		fmt.Fprintf(&b, "recorded: answer-route questions from a report of prompt %.8s\n", r.RecordedPromptVersion)
	}
	for _, role := range slices.Sorted(func(yield func(string) bool) {
		for k := range r.Checks {
			if !yield(k) {
				return
			}
		}
	}) {
		fmt.Fprintf(&b, "check %s: %s\n", role, r.Checks[role])
	}
	for _, v := range r.Variants {
		fmt.Fprintf(&b, "\n%s\n", v.Name)
		writeMetrics(&b, "all", v.Overall)
		for _, kind := range kinds {
			if m, ok := v.ByKind[kind]; ok {
				writeMetrics(&b, kind, m)
			}
		}
		if v.Judge2 != nil {
			writeMetrics(&b, "judge2", *v.Judge2)
			writeAgreement(&b, *v.Agreement)
		}
		writeStages(&b, v.Overall)
		if n := v.Overall.TimingsReplayed; n > 0 {
			fmt.Fprintf(&b, "  timings: %d of %d recorded when the answer book was, not by this run\n", n,
				v.Overall.Questions)
		}
		b.WriteString("  per question: id route status cited invalid abstained first_ms total_ms\n")
		for _, q := range v.Questions {
			cited := fmt.Sprint(q.Citations)
			if len(q.Markers) > 0 {
				cited = fmt.Sprint(q.Markers)
			}
			fmt.Fprintf(&b, "    %s %s %s %s %d %v %d %d\n", q.ID, q.Route, q.Status, cited, q.Invalid,
				q.Abstained, q.FirstMS, q.TotalMS)
		}
	}
	if len(r.Evicted) > 0 {
		fmt.Fprintf(&b, "\nevicted while it ran: %s\n", strings.Join(r.Evicted, ", "))
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// rate is v to three places, a rate that counted nothing as "-".
func rate(v float64) string {
	if v < 0 {
		return "-"
	}
	return fmt.Sprintf("%.3f", v)
}

// writeAgreement writes how the judges agree on one line: raw agreement
// and Cohen's kappa ("-" undefined), and the items each counts.
func writeAgreement(b *strings.Builder, a judgeAgreement) {
	one := func(x agreement) string {
		kappa := "-"
		if x.Kappa != nil {
			kappa = fmt.Sprintf("%.3f", *x.Kappa)
		}
		return fmt.Sprintf("raw %s kappa %s (n=%d)", rate(x.Raw), kappa, x.Items)
	}
	fmt.Fprintf(b, "  %-12s sentences %s; support of cited claims %s; facts %s\n", "agreement", one(a.Sentences),
		one(a.Support), one(a.Facts))
}

// writeStages writes each stage's p50/p95 and n on one line, and the
// answer call's tokens on another.
func writeStages(b *strings.Builder, m metrics) {
	parts := make([]string, 0, len(m.Stages))
	for _, s := range m.Stages {
		parts = append(parts, fmt.Sprintf("%s %d/%d (n=%d)", s.Stage, s.P50MS, s.P95MS, s.N))
	}
	fmt.Fprintf(b, "  %-12s %s ms\n", "stages", strings.Join(parts, ", "))
	fmt.Fprintf(b, "  %-12s prompt p50/p95 %d/%d (n=%d), output p50/p95 %d/%d (n=%d)\n", "tokens",
		m.PromptTokens.P50, m.PromptTokens.P95, m.PromptTokens.N, m.OutputTokens.P50, m.OutputTokens.P95,
		m.OutputTokens.N)
}

// writeMetrics writes m on one line.
func writeMetrics(b *strings.Builder, label string, m metrics) {
	fmt.Fprintf(b, "  %-12s n=%-3d unjudged %d validity %s support %s cited %s recall %s facts %s abstain %s false-abstain %s "+
		"routing %s first p50/p95 %d/%d ms total p50/p95 %d/%d ms\n", label, m.Questions, m.Unjudged,
		rate(m.CitationValidity),
		rate(m.CitationSupport), rate(m.ClaimsCited), rate(m.SourceRecall), rate(m.FactCoverage), rate(m.Abstention),
		rate(m.FalseAbstention), rate(m.Routing), m.FirstTokenP50MS, m.FirstTokenP95MS, m.TotalP50MS, m.TotalP95MS)
}
