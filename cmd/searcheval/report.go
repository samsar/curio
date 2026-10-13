package main

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/samsar/curio/internal/config"
	"github.com/samsar/curio/internal/devtool"
	"github.com/samsar/curio/internal/eval"
	"github.com/samsar/curio/internal/store"
	"github.com/samsar/curio/internal/version"
)

// reportFormat is the JSON report's format, which a baseline must have.
const reportFormat = 1

// report is every number a run measured, as the JSON report holds it. The
// timings and generated_at are the only values two runs on the same copy,
// config and query file can differ in. It never holds a comparison with
// its baseline, so any report can be the next run's baseline.
type report struct {
	Format      int             `json:"format"`
	GeneratedAt time.Time       `json:"generated_at"`
	Provenance  provenance      `json:"provenance"`
	Library     libraryReport   `json:"library"`
	Variants    []variantReport `json:"variants"`
}

// provenance is what the run measured: which curio, copy, query file and
// settings.
type provenance struct {
	Version       string              `json:"version"`
	Commit        string              `json:"commit"`
	Database      string              `json:"database"`
	Schema        int64               `json:"schema"`
	Width         int                 `json:"width"`
	Queries       string              `json:"queries"`
	QueriesSHA256 string              `json:"queries_sha256"`
	Config        string              `json:"config"` // "" for the defaults
	Embedding     embeddingProvenance `json:"embedding"`
	Search        searchProvenance    `json:"search"`
	K             int                 `json:"k"`
	Variants      []string            `json:"variants"`
	Baseline      string              `json:"baseline"`
}

type embeddingProvenance struct {
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Dim         int    `json:"dim"`
	BaseURL     string `json:"base_url"`
	QueryPrefix string `json:"query_prefix"`
}

type searchProvenance struct {
	BM25Weight          float64 `json:"bm25_weight"`
	VectorWeight        float64 `json:"vector_weight"`
	RRFK                int     `json:"rrf_k"`
	Collapse            string  `json:"collapse"`
	EmbedTimeoutSeconds int     `json:"embed_timeout_seconds"`
}

// libraryReport counts the judgments, over every query, and those search
// can't return.
type libraryReport struct {
	Judgments    int `json:"judgments"`
	Missing      int `json:"missing"`
	Unsearchable int `json:"unsearchable"`
}

// variantReport is one variant's run.
type variantReport struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	WallMS      float64                `json:"wall_ms"`
	LatencyMS   map[string]percentiles `json:"latency_ms"`
	Groups      []groupReport          `json:"groups"`
	Queries     []queryReport          `json:"queries"`

	// stages are LatencyMS's keys in the order the stages run.
	stages []string
}

type percentiles struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
}

// groupReport is a group's mean scores, and how many of its judgments
// search can't return.
type groupReport struct {
	eval.GroupScore
	Missing      int `json:"missing"`
	Unsearchable int `json:"unsearchable"`
}

// queryReport is one query's ranking and scores.
type queryReport struct {
	Index int      `json:"index"`
	ID    string   `json:"id"`
	Kind  string   `json:"kind"`
	Tags  []string `json:"tags"`
	Query string   `json:"query"`
	eval.QueryScore
	Judgments []judgmentReport   `json:"judgments"`
	Ranking   []rankedReport     `json:"ranking"`
	TimingsMS map[string]float64 `json:"timings_ms"`
}

// judgmentReport is a judgment, its document in the copy and its rank in
// the query's ranking, nil when it isn't there.
type judgmentReport struct {
	URL        string         `json:"url"`
	DocumentID string         `json:"document_id"`
	Grade      int            `json:"grade"`
	Status     judgmentStatus `json:"status"`
	Rank       *int           `json:"rank"`
}

type rankedReport struct {
	Rank       int    `json:"rank"`
	DocumentID string `json:"document_id"`
	URL        string `json:"url"`
}

// newReport is the report of a run, its variants still to run.
func newReport(opts options, cfg config.Config, lib *devtool.Copy, qf queryFile, judged map[string]resolved) *report {
	names := make([]string, len(opts.variants))
	for i, v := range opts.variants {
		names[i] = v.name
	}
	rep := &report{Format: reportFormat, Variants: []variantReport{}, Provenance: provenance{
		Version: version.Version, Commit: version.Commit,
		Database: opts.db, Schema: lib.Schema, Width: lib.Width,
		Queries: qf.path, QueriesSHA256: qf.sha256, Config: opts.config,
		Embedding: embeddingProvenance{Provider: cfg.Embedding.Provider, Model: cfg.Embedding.Model,
			Dim: cfg.Embedding.Dim, BaseURL: cfg.Embedding.BaseURL, QueryPrefix: cfg.Embedding.QueryPrefix},
		Search: searchProvenance{BM25Weight: cfg.Search.BM25Weight, VectorWeight: cfg.Search.VectorWeight,
			RRFK: cfg.Search.RRFK, Collapse: cfg.Search.Collapse, EmbedTimeoutSeconds: cfg.Search.EmbedTimeoutSeconds},
		K: store.MaxSearchK, Variants: names, Baseline: opts.baseline,
	}}
	for _, q := range qf.set.Queries {
		for _, j := range q.Relevant {
			rep.Library.Judgments++
			switch judged[j.URL].status {
			case missing:
				rep.Library.Missing++
			case unsearchable:
				rep.Library.Unsearchable++
			case searchable:
			}
		}
	}
	return rep
}

// newQueryReport is query i's report: its scores, its judgments with their
// documents and ranks, and the ranking that got them.
func newQueryReport(i int, q eval.Query, score eval.QueryScore, r ranking, judged map[string]resolved) queryReport {
	tags := q.Tags
	if tags == nil {
		tags = []string{}
	}
	qr := queryReport{Index: i, ID: q.ID, Kind: q.Kind, Tags: tags, Query: q.Query, QueryScore: score,
		Judgments: make([]judgmentReport, 0, len(q.Relevant)), Ranking: make([]rankedReport, 0, len(r.docs)),
		TimingsMS: make(map[string]float64, len(r.stages))}
	rankOf := make(map[string]int, len(r.docs))
	for pos, d := range r.docs {
		rankOf[d.url] = pos + 1
		qr.Ranking = append(qr.Ranking, rankedReport{Rank: pos + 1, DocumentID: d.id, URL: d.url})
	}
	for _, j := range q.Relevant {
		res := judged[j.URL]
		jr := judgmentReport{URL: j.URL, DocumentID: res.documentID, Grade: j.Grade, Status: res.status}
		if rank, ok := rankOf[j.URL]; ok {
			jr.Rank = &rank
		}
		qr.Judgments = append(qr.Judgments, jr)
	}
	for _, s := range r.stages {
		qr.TimingsMS[s.name] = ms(s.took)
	}
	return qr
}

// newGroupReport is g's mean scores and its judgments search can't
// return, counted over its queries.
func newGroupReport(g eval.Group, scores []eval.QueryScore, queries []queryReport) groupReport {
	gr := groupReport{GroupScore: eval.Summarize(g, scores)}
	for _, i := range g.Queries {
		for _, j := range queries[i].Judgments {
			switch j.Status {
			case missing:
				gr.Missing++
			case unsearchable:
				gr.Unsearchable++
			case searchable:
			}
		}
	}
	return gr
}

// writeText renders the report, compared with base when there is one, and
// writes it to w in one write.
func (r *report) writeText(w io.Writer, base *baseline) error {
	var b bytes.Buffer
	r.text(&b, base)
	_, err := w.Write(b.Bytes())
	return err
}

func (r *report) text(b *bytes.Buffer, base *baseline) {
	p := r.Provenance
	fmt.Fprintf(b, "Search evaluation of %s (schema %d, %d-wide vectors), curio %s\n", p.Database, p.Schema, p.Width,
		p.Version)
	fmt.Fprintf(b, "Queries: %s (sha256 %s), %d queries, %d judgments: %d missing, %d unsearchable\n",
		p.Queries, shortHash(p.QueriesSHA256), r.queryCount(), r.Library.Judgments, r.Library.Missing,
		r.Library.Unsearchable)
	fmt.Fprintf(b, "Searched with %s: %s; k %d; variants %s\n", configName(p.Config), searchSettings(p),
		p.K, strings.Join(p.Variants, ", "))
	if base != nil {
		base.header(b, r)
	}
	for _, v := range r.Variants {
		fmt.Fprintf(b, "\n%s: %s\n\n", v.Name, v.Description)
		writeGroups(b, v.Groups)
		b.WriteString("\n")
		writeQueries(b, v.Queries)
		b.WriteString("\n")
		writeLatencies(b, v)
		fmt.Fprintf(b, "\nwall time %.1f s\n", v.WallMS/1000)
		if base != nil {
			base.compareVariant(b, v)
		}
	}
}

// queryCount is how many queries the run scored.
func (r *report) queryCount() int {
	if len(r.Variants) == 0 {
		return 0
	}
	return len(r.Variants[0].Queries)
}

// searchSettings are the embedding model and search keys a run used.
func searchSettings(p provenance) string {
	s := p.Search
	return fmt.Sprintf("embedding %s; bm25_weight %g, vector_weight %g, rrf_k %d, collapse %s, embed_timeout_seconds %d",
		p.Embedding.Model, s.BM25Weight, s.VectorWeight, s.RRFK, s.Collapse, s.EmbedTimeoutSeconds)
}

// shortHash is a SHA-256's first 12 hex digits.
func shortHash(h string) string { return h[:min(12, len(h))] }

// groupLabel names a group: "all", "kind topic", "tag owner".
func groupLabel(typ eval.GroupType, name string) string {
	if typ == eval.GroupAll {
		return string(typ)
	}
	return string(typ) + " " + name
}

func writeGroups(b *bytes.Buffer, groups []groupReport) {
	rows := make([][]string, 0, 1+len(groups))
	rows = append(rows, []string{"group", "n", "R@10", "R@100", "R@10hi", "n_hi", "MRR", "nDCG@10", "S@1", "S@5",
		"S@10", "missing", "unsearchable"})
	for _, g := range groups {
		rows = append(rows, []string{groupLabel(g.Type, g.Name), strconv.Itoa(g.N), score(g.RecallAt10),
			score(g.RecallAt100), optional(g.HighRecallAt10), strconv.Itoa(g.NHigh), score(g.MRR), score(g.NDCGAt10),
			score(g.SuccessAt1), score(g.SuccessAt5), score(g.SuccessAt10), strconv.Itoa(g.Missing),
			strconv.Itoa(g.Unsearchable)})
	}
	table(b, rows)
}

func writeQueries(b *bytes.Buffer, queries []queryReport) {
	rows := make([][]string, 0, 1+len(queries))
	rows = append(rows, []string{"query", "kind", "R@10", "RR", "nDCG@10", "grade-2 ranks"})
	for _, q := range queries {
		kind := q.Kind
		if kind == "" {
			kind = "-"
		}
		rows = append(rows, []string{queryLabel(q.ID, q.Index), kind, score(q.RecallAt10), score(q.RR),
			score(q.NDCGAt10), highRanks(q.Judgments)})
	}
	table(b, rows)
}

// maxRanksShown caps the grade-2 ranks a query's line lists.
const maxRanksShown = 12

// highRanks lists where the query's grade-2 judgments are: their ranks,
// ascending, then "-" for each one search could return that isn't in the
// ranking, then "×" for each missing or unsearchable one; past
// maxRanksShown entries, how many more.
func highRanks(judgments []judgmentReport) string {
	var ranks []int
	var absent, unreachable int
	for _, j := range judgments {
		switch {
		case j.Grade < eval.MaxGrade:
		case j.Rank != nil:
			ranks = append(ranks, *j.Rank)
		case j.Status == searchable:
			absent++
		default:
			unreachable++
		}
	}
	slices.Sort(ranks)
	entries := make([]string, 0, len(ranks)+absent+unreachable)
	for _, r := range ranks {
		entries = append(entries, strconv.Itoa(r))
	}
	for range absent {
		entries = append(entries, "-")
	}
	for range unreachable {
		entries = append(entries, "×")
	}
	if len(entries) > maxRanksShown {
		more := len(entries) - maxRanksShown
		entries = append(entries[:maxRanksShown], fmt.Sprintf("(+%d)", more))
	}
	return strings.Join(entries, " ")
}

func writeLatencies(b *bytes.Buffer, v variantReport) {
	rows := make([][]string, 0, 1+len(v.stages))
	rows = append(rows, []string{"stage", "p50 ms", "p95 ms"})
	for _, name := range v.stages {
		l := v.LatencyMS[name]
		rows = append(rows, []string{name, fmt.Sprintf("%.1f", l.P50), fmt.Sprintf("%.1f", l.P95)})
	}
	table(b, rows)
}

// table writes rows in columns, left-aligned, two spaces apart. A row's
// last cell is written as it is and widens no column, so a short row's
// note ("only in the baseline") doesn't push the others apart.
func table(b *bytes.Buffer, rows [][]string) {
	var widths []int
	for _, row := range rows {
		for i, cell := range row[:len(row)-1] {
			if i == len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], utf8.RuneCountInString(cell))
		}
	}
	for _, row := range rows {
		for i, cell := range row[:len(row)-1] {
			b.WriteString(cell)
			b.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell)+2))
		}
		b.WriteString(row[len(row)-1])
		b.WriteByte('\n')
	}
}

// score is a score to 3 decimals.
func score(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

// optional is a score that may not apply, "-" when it doesn't.
func optional(v *float64) string {
	if v == nil {
		return "-"
	}
	return score(*v)
}
