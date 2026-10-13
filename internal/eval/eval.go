package eval

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/samsar/curio/internal/urlutil"
)

// MaxGrade is the highest grade a judgment takes. Grade 1 is a relevant
// document, grade 2 what the query is after. The scale stops there: a
// grade the format doesn't define is more likely a typo than a meaning,
// and a gain of 2^g-1 for an unbounded g overflows to +Inf, which makes
// nDCG NaN.
const MaxGrade = 2

// Judgment is a document judged relevant to a query: its URL, normalized
// as stored document URLs are, and its grade, 1 to MaxGrade.
type Judgment struct {
	URL   string
	Grade int
}

// Query is one judged evaluation query: its text and the documents judged
// relevant to it. ID, Kind and Tags are optional: an ID names the query in
// reports and matches it across edits of the file, and the kind and tags
// group it, each group scored on its own. Note is the file's comment on
// the query; nothing scores it.
type Query struct {
	ID       string
	Kind     string
	Tags     []string
	Note     string
	Query    string
	Relevant []Judgment
}

// QuerySet is a collection of judged queries loaded from a qrels YAML file.
type QuerySet struct {
	Queries []Query
}

// LoadQuerySet reads and parses a qrels YAML file (see ParseQuerySet).
func LoadQuerySet(path string) (*QuerySet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read qrels: %w", err)
	}
	return ParseQuerySet(data, path)
}

// ParseQuerySet parses and validates a qrels file's contents; name, the
// file's path, prefixes every error. The keys are strict, as config.yaml's
// are: one the format doesn't define (a `tag:` for `tags:`, say) would
// otherwise drop silently what it meant to say. Each relevant entry is a
// URL, at grade 1, or a {url, grade} mapping, and URLs are normalized the
// way stored document URLs are, so they compare equal.
func ParseQuerySet(data []byte, name string) (*QuerySet, error) {
	nodes, err := queryNodes(data)
	if err != nil {
		return nil, fmt.Errorf("parse qrels %q: %w", name, err)
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("qrels %q has no queries", name)
	}
	qs := &QuerySet{Queries: make([]Query, 0, len(nodes))}
	byID := make(map[string]int, len(nodes))
	for i, node := range nodes {
		q, err := parseQuery(node)
		if err != nil {
			return nil, fmt.Errorf("qrels %q: query %d%s: %w", name, i, q.ident(), err)
		}
		if q.ID != "" {
			if first, dup := byID[q.ID]; dup {
				return nil, fmt.Errorf("qrels %q: queries %d and %d have the same id %q", name, first, i, q.ID)
			}
			byID[q.ID] = i
		}
		qs.Queries = append(qs.Queries, q)
	}
	return qs, nil
}

// queryNodes are the file's queries, unparsed: none for an empty file.
func queryNodes(data []byte) ([]*yaml.Node, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	root := doc.Content[0]
	if err := checkKeys(root, "the file", "queries"); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 {
		return nil, nil
	}
	switch queries := root.Content[1]; {
	case queries.ShortTag() == "!!null":
		return nil, nil
	case queries.Kind != yaml.SequenceNode:
		return nil, fmt.Errorf("line %d: queries is a list", queries.Line)
	default:
		return queries.Content, nil
	}
}

// checkKeys fails unless node is a mapping whose keys are all among keys;
// what names it in the error.
func checkKeys(node *yaml.Node, what string, keys ...string) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: %s is a mapping of %s", node.Line, what, strings.Join(keys, ", "))
	}
	for i := 0; i < len(node.Content); i += 2 {
		if key := node.Content[i]; !slices.Contains(keys, key.Value) {
			return fmt.Errorf("line %d: unknown key %q (%s has %s)", key.Line, key.Value, what, strings.Join(keys, ", "))
		}
	}
	return nil
}

// ident names q in an error: its id, or its text when it has none; empty
// when it has neither.
func (q Query) ident() string {
	switch {
	case q.ID != "":
		return fmt.Sprintf(" (id %q)", q.ID)
	case q.Query != "":
		return fmt.Sprintf(" (%q)", q.Query)
	}
	return ""
}

// parseQuery reads one query of the file. On an error it still returns
// the id and text it read, which name the query in the error.
func parseQuery(node *yaml.Node) (Query, error) {
	var raw struct {
		ID       string      `yaml:"id"`
		Kind     string      `yaml:"kind"`
		Tags     []string    `yaml:"tags"`
		Note     string      `yaml:"note"`
		Query    string      `yaml:"query"`
		Relevant []yaml.Node `yaml:"relevant"`
	}
	// Decoded before the keys are checked, which an unknown one fails, so
	// that error can name the query too.
	decodeErr := node.Decode(&raw)
	if err := checkKeys(node, "a query", "id", "kind", "tags", "note", "query", "relevant"); err != nil {
		return Query{ID: raw.ID, Query: raw.Query}, err
	}
	if decodeErr != nil {
		return Query{ID: raw.ID, Query: raw.Query}, decodeErr
	}
	q := Query{ID: raw.ID, Kind: raw.Kind, Note: raw.Note, Query: raw.Query}
	if q.Query == "" {
		return q, errors.New("empty text")
	}
	tags, err := uniqueTags(raw.Tags)
	if err != nil {
		return q, err
	}
	q.Tags = tags
	// A query with no relevant docs would score 0 on every metric yet
	// still count toward the mean, silently deflating the aggregate — by
	// IR convention (and trec_eval) it doesn't belong in the set.
	if len(raw.Relevant) == 0 {
		return q, errors.New("no relevant documents")
	}
	q.Relevant, err = parseJudgments(raw.Relevant)
	return q, err
}

// uniqueTags refuses an empty tag, which would name a group of nothing,
// and keeps a repeated one once, so a query counts once in its group.
func uniqueTags(tags []string) ([]string, error) {
	var out []string
	for _, t := range tags {
		if t == "" {
			return nil, errors.New("an empty tag")
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out, nil
}

// parseJudgments reads a query's relevant entries. Duplicates that
// normalization reveals are one judgment, at the first one's place, with
// the higher grade, so they can't inflate a query's relevant count.
func parseJudgments(nodes []yaml.Node) ([]Judgment, error) {
	out := make([]Judgment, 0, len(nodes))
	at := make(map[string]int, len(nodes))
	for i := range nodes {
		j, err := parseJudgment(&nodes[i])
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", nodes[i].Line, err)
		}
		if k, dup := at[j.URL]; dup {
			out[k].Grade = max(out[k].Grade, j.Grade)
			continue
		}
		at[j.URL] = len(out)
		out = append(out, j)
	}
	return out, nil
}

// parseJudgment reads one relevant entry: a URL, at grade 1, or a mapping
// of exactly url and grade. The URL is canonicalized with
// urlutil.Normalize, as the daemon does before storing a document, so a
// URL pasted from a browser — with a fragment, tracking parameters, or a
// youtu.be link — still matches.
func parseJudgment(node *yaml.Node) (Judgment, error) {
	var j Judgment
	switch node.Kind {
	case yaml.ScalarNode:
		j.Grade = 1
		if err := node.Decode(&j.URL); err != nil {
			return Judgment{}, err
		}
	case yaml.MappingNode:
		var hasURL, hasGrade bool
		for i := 0; i < len(node.Content); i += 2 {
			key, val := node.Content[i], node.Content[i+1]
			var err error
			switch key.Value {
			case "url":
				hasURL, err = true, val.Decode(&j.URL)
			case "grade":
				hasGrade, err = true, decodeGrade(val, &j.Grade)
			default:
				err = fmt.Errorf("unknown key %q (an entry's keys are url and grade)", key.Value)
			}
			if err != nil {
				return Judgment{}, err
			}
		}
		switch {
		case !hasURL:
			return Judgment{}, errors.New("an entry without a url")
		case !hasGrade:
			return Judgment{}, errors.New("an entry without a grade (a URL alone is grade 1)")
		}
	case yaml.SequenceNode, yaml.DocumentNode, yaml.AliasNode:
		return Judgment{}, errors.New("an entry is a URL or a {url, grade} mapping")
	}
	u, err := urlutil.Normalize(j.URL)
	if err != nil {
		return Judgment{}, fmt.Errorf("relevant URL %q: %w", j.URL, err)
	}
	j.URL = u
	return j, nil
}

// decodeGrade reads a grade: a whole number from 1 to MaxGrade.
func decodeGrade(node *yaml.Node, grade *int) error {
	if node.Kind != yaml.ScalarNode || node.ShortTag() != "!!int" {
		return fmt.Errorf("grade %q: a whole number from 1 to %d", node.Value, MaxGrade)
	}
	if err := node.Decode(grade); err != nil {
		return fmt.Errorf("grade %q: %w", node.Value, err)
	}
	if *grade < 1 || *grade > MaxGrade {
		return fmt.Errorf("grade %d: a whole number from 1 to %d", *grade, MaxGrade)
	}
	return nil
}

// Grades are the query's judgments by URL.
func (q Query) Grades() Grades {
	g := make(Grades, len(q.Relevant))
	for _, j := range q.Relevant {
		g[j.URL] = j.Grade
	}
	return g
}

// QueryResult holds the metrics for a single query.
type QueryResult struct {
	Query        string
	NumRelevant  int
	NumRetrieved int
	RecallAtK    float64
	PrecisionAtK float64
	NDCGAtK      float64
	RR           float64
}

// Report is the aggregate outcome of an evaluation.
type Report struct {
	K             int
	Results       []QueryResult
	MeanRecall    float64
	MeanPrecision float64
	MeanNDCG      float64
	MRR           float64
}

// Evaluate scores retrieval results against a query set at cutoff k.
// rankedByQuery[i] is the ordered list of retrieved document identifiers for
// qs.Queries[i]; the caller performs retrieval so this package stays free of
// HTTP/search concerns. Aggregate fields are means across all queries.
// NumRelevant counts a query's judgments, whatever their grades.
func Evaluate(qs *QuerySet, rankedByQuery [][]string, k int) Report {
	rep := Report{K: k}
	if len(qs.Queries) == 0 {
		return rep
	}
	for i, q := range qs.Queries {
		var ranked []string
		if i < len(rankedByQuery) {
			ranked = rankedByQuery[i]
		}
		grades := q.Grades()
		qr := QueryResult{
			Query:        q.Query,
			NumRelevant:  len(grades),
			NumRetrieved: len(ranked),
			RecallAtK:    RecallAtK(ranked, grades, k),
			PrecisionAtK: PrecisionAtK(ranked, grades, k),
			NDCGAtK:      NDCGAtK(ranked, grades, k),
			RR:           ReciprocalRank(ranked, grades),
		}
		rep.Results = append(rep.Results, qr)
		rep.MeanRecall += qr.RecallAtK
		rep.MeanPrecision += qr.PrecisionAtK
		rep.MeanNDCG += qr.NDCGAtK
		rep.MRR += qr.RR
	}
	n := float64(len(qs.Queries))
	rep.MeanRecall /= n
	rep.MeanPrecision /= n
	rep.MeanNDCG /= n
	rep.MRR /= n
	return rep
}
