package eval

import (
	"bytes"
	"errors"
	"fmt"
	"io"
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
// the query; nothing scores it. Filters, nil when the query has none, are
// the search filters it is run with.
type Query struct {
	ID       string
	Kind     string
	Tags     []string
	Note     string
	Query    string
	Filters  *Filters
	Relevant []Judgment
}

// Filters are a query's search filters, under the API's names, as the file
// writes them: the search checks and resolves them, so this package stays
// free of it. Dates are their text, as written.
type Filters struct {
	SavedAfter      string   `json:"saved_after,omitempty"`
	SavedBefore     string   `json:"saved_before,omitempty"`
	PublishedAfter  string   `json:"published_after,omitempty"`
	PublishedBefore string   `json:"published_before,omitempty"`
	Site            []string `json:"site,omitempty"`
	Folder          []string `json:"folder,omitempty"`
	IncludeUndated  bool     `json:"include_undated,omitempty"`
}

// The keys of a query's filters mapping.
const (
	filterSavedAfter      = "saved_after"
	filterSavedBefore     = "saved_before"
	filterPublishedAfter  = "published_after"
	filterPublishedBefore = "published_before"
	filterSite            = "site"
	filterFolder          = "folder"
	filterIncludeUndated  = "include_undated"
)

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

// queryNodes are the file's queries, unparsed: none for an empty file. The
// file is one YAML document: a second, as two files concatenated give,
// is refused rather than dropped.
func queryNodes(data []byte) ([]*yaml.Node, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	switch err := dec.Decode(&doc); {
	case errors.Is(err, io.EOF):
		return nil, nil
	case err != nil:
		return nil, err
	}
	var next yaml.Node
	switch err := dec.Decode(&next); {
	case err == nil:
		return nil, fmt.Errorf("line %d: a second YAML document (a query file is one)", next.Line)
	case !errors.Is(err, io.EOF):
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	root := resolve(doc.Content[0])
	if err := checkKeys(root, "the file", "queries"); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 {
		return nil, nil
	}
	switch queries := resolve(root.Content[1]); {
	case queries.ShortTag() == "!!null":
		return nil, nil
	case queries.Kind != yaml.SequenceNode:
		return nil, fmt.Errorf("line %d: queries is a list", queries.Line)
	default:
		return queries.Content, nil
	}
}

// resolve is the node an alias names, or node itself: an anchored query
// or entry reused by alias reads as the original.
func resolve(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.AliasNode {
		return node.Alias
	}
	return node
}

// checkKeys fails unless node is a mapping whose keys are all among keys,
// each at most once; what names it in the error. A repeated key is
// refused, not overwritten: a file concatenated from two would otherwise
// lose the second's queries without a word.
func checkKeys(node *yaml.Node, what string, keys ...string) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: %s is a mapping of %s", node.Line, what, strings.Join(keys, ", "))
	}
	firstAt := make(map[string]int, len(keys))
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		if !slices.Contains(keys, key.Value) {
			return fmt.Errorf("line %d: unknown key %q (%s has %s)", key.Line, key.Value, what, strings.Join(keys, ", "))
		}
		if line, seen := firstAt[key.Value]; seen {
			return fmt.Errorf("line %d: key %q repeated (first at line %d)", key.Line, key.Value, line)
		}
		firstAt[key.Value] = key.Line
	}
	return nil
}

// scalarField is the first value of a mapping's key when it is a
// non-null scalar, "" otherwise: what names a query whose keys are wrong,
// read without decoding it.
func scalarField(node *yaml.Node, key string) string {
	if node.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		k, v := node.Content[i], node.Content[i+1]
		if k.Value == key {
			if v.Kind != yaml.ScalarNode || v.ShortTag() == "!!null" {
				return ""
			}
			return v.Value
		}
	}
	return ""
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
// the id and text it found, which name the query in the error.
func parseQuery(node *yaml.Node) (Query, error) {
	node = resolve(node)
	named := Query{ID: scalarField(node, "id"), Query: scalarField(node, "query")}
	if err := checkKeys(node, "a query", "id", "kind", "tags", "note", "query", "filters", "relevant"); err != nil {
		return named, err
	}
	var raw struct {
		ID       string      `yaml:"id"`
		Kind     string      `yaml:"kind"`
		Tags     []yaml.Node `yaml:"tags"`
		Note     string      `yaml:"note"`
		Query    string      `yaml:"query"`
		Filters  yaml.Node   `yaml:"filters"`
		Relevant []yaml.Node `yaml:"relevant"`
	}
	if err := node.Decode(&raw); err != nil {
		return named, err
	}
	q := Query{ID: raw.ID, Kind: raw.Kind, Note: raw.Note, Query: raw.Query}
	if q.Query == "" {
		return q, errors.New("empty text")
	}
	tags, err := parseTags(raw.Tags)
	if err != nil {
		return q, err
	}
	q.Tags = tags
	if raw.Filters.Kind != 0 {
		if q.Filters, err = parseFilters(&raw.Filters); err != nil {
			return q, err
		}
	}
	// A query with no relevant docs would score 0 on every metric yet
	// still count toward the mean, silently deflating the aggregate — by
	// IR convention (and trec_eval) it doesn't belong in the set.
	if len(raw.Relevant) == 0 {
		return q, errors.New("no relevant documents")
	}
	q.Relevant, err = parseJudgments(raw.Relevant)
	return q, err
}

// parseTags reads a query's tags. An empty or null one is refused, since
// it would name a group of nothing (decoded straight into strings, a null
// would vanish instead), and a repeated one is kept once, so a query
// counts once in its group.
func parseTags(nodes []yaml.Node) ([]string, error) {
	var out []string
	for i := range nodes {
		var t string
		if err := nodes[i].Decode(&t); err != nil {
			return nil, err
		}
		if t == "" {
			return nil, fmt.Errorf("line %d: an empty tag", nodes[i].Line)
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out, nil
}

// parseFilters reads a query's filters: a mapping of the API's filter
// names, each at most once, the dates text, site and folder lists of text,
// and include_undated a boolean. Filters that filter nothing are refused,
// an empty mapping, an empty value or list, or include_undated false
// alone: the query would be reported as filtered, and searched without a
// filter.
func parseFilters(node *yaml.Node) (*Filters, error) {
	node = resolve(node)
	if err := checkKeys(node, "filters", filterSavedAfter, filterSavedBefore, filterPublishedAfter,
		filterPublishedBefore, filterSite, filterFolder, filterIncludeUndated); err != nil {
		return nil, err
	}
	if len(node.Content) == 0 {
		return nil, fmt.Errorf("line %d: an empty filters mapping (leave filters out for none)", node.Line)
	}
	f := &Filters{}
	for i := 0; i < len(node.Content); i += 2 {
		key, val := node.Content[i], resolve(node.Content[i+1])
		var err error
		switch key.Value {
		case filterSavedAfter:
			f.SavedAfter, err = filterText(key.Value, val)
		case filterSavedBefore:
			f.SavedBefore, err = filterText(key.Value, val)
		case filterPublishedAfter:
			f.PublishedAfter, err = filterText(key.Value, val)
		case filterPublishedBefore:
			f.PublishedBefore, err = filterText(key.Value, val)
		case filterSite:
			f.Site, err = filterList(key.Value, val)
		case filterFolder:
			f.Folder, err = filterList(key.Value, val)
		case filterIncludeUndated:
			if val.Kind != yaml.ScalarNode || val.ShortTag() != "!!bool" {
				return nil, fmt.Errorf("line %d: %s is true or false", val.Line, key.Value)
			}
			err = val.Decode(&f.IncludeUndated)
		}
		if err != nil {
			return nil, err
		}
	}
	if f.filtersNothing() {
		return nil, fmt.Errorf("line %d: filters that filter nothing (leave filters out for none)", node.Line)
	}
	return f, nil
}

// filtersNothing reports whether f sets no filter: include_undated false
// alone.
func (f *Filters) filtersNothing() bool {
	return f.SavedAfter == "" && f.SavedBefore == "" && f.PublishedAfter == "" && f.PublishedBefore == "" &&
		len(f.Site) == 0 && len(f.Folder) == 0 && !f.IncludeUndated
}

// filterText reads a filter written as one value: a scalar's text as
// written, so an unquoted date stays the date the file says. An empty one
// would filter nothing.
func filterText(name string, node *yaml.Node) (string, error) {
	if node.Kind != yaml.ScalarNode || node.ShortTag() == "!!null" {
		return "", fmt.Errorf("line %d: %s is one value, such as a date", node.Line, name)
	}
	if strings.TrimSpace(node.Value) == "" {
		return "", fmt.Errorf("line %d: %s is empty (leave it out for none)", node.Line, name)
	}
	return node.Value, nil
}

// filterList reads a filter written as a list of values, each its text.
// An empty list would filter nothing.
func filterList(name string, node *yaml.Node) ([]string, error) {
	if node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("line %d: %s is a list", node.Line, name)
	}
	if len(node.Content) == 0 {
		return nil, fmt.Errorf("line %d: %s is an empty list (leave it out for none)", node.Line, name)
	}
	out := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		v, err := filterText(name, resolve(item))
		if err != nil {
			return nil, err
		}
		out = append(out, v)
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
// of exactly url and grade, each once. The URL is canonicalized with
// urlutil.Normalize, as the daemon does before storing a document, so a
// URL pasted from a browser — with a fragment, tracking parameters, or a
// youtu.be link — still matches.
func parseJudgment(node *yaml.Node) (Judgment, error) {
	node = resolve(node)
	var j Judgment
	switch node.Kind {
	case yaml.ScalarNode:
		j.Grade = 1
		if err := node.Decode(&j.URL); err != nil {
			return Judgment{}, err
		}
	case yaml.MappingNode:
		seen := make(map[string]bool, 2)
		for i := 0; i < len(node.Content); i += 2 {
			key, val := node.Content[i], node.Content[i+1]
			if seen[key.Value] {
				return Judgment{}, fmt.Errorf("key %q repeated", key.Value)
			}
			seen[key.Value] = true
			var err error
			switch key.Value {
			case "url":
				err = val.Decode(&j.URL)
			case "grade":
				err = decodeGrade(val, &j.Grade)
			default:
				err = fmt.Errorf("unknown key %q (an entry's keys are url and grade)", key.Value)
			}
			if err != nil {
				return Judgment{}, err
			}
		}
		switch {
		case !seen["url"]:
			return Judgment{}, errors.New("an entry without a url")
		case !seen["grade"]:
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
