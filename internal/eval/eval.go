package eval

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Query is one labeled evaluation query: the text and the identifiers
// (document URLs) considered relevant to it.
type Query struct {
	Query    string   `yaml:"query"`
	Relevant []string `yaml:"relevant"`
}

// QuerySet is a collection of labeled queries loaded from a qrels YAML file.
type QuerySet struct {
	Queries []Query `yaml:"queries"`
}

// LoadQuerySet reads and validates a qrels YAML file.
func LoadQuerySet(path string) (*QuerySet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read qrels: %w", err)
	}
	var qs QuerySet
	if err := yaml.Unmarshal(data, &qs); err != nil {
		return nil, fmt.Errorf("parse qrels %q: %w", path, err)
	}
	if len(qs.Queries) == 0 {
		return nil, fmt.Errorf("qrels %q has no queries", path)
	}
	for i, q := range qs.Queries {
		if q.Query == "" {
			return nil, fmt.Errorf("qrels %q: query %d has empty text", path, i)
		}
		// A query with no relevant docs would score 0 on every metric yet
		// still count toward the mean, silently deflating the aggregate — by
		// IR convention (and trec_eval) it doesn't belong in the set.
		if len(q.Relevant) == 0 {
			return nil, fmt.Errorf("qrels %q: query %d (%q) has no relevant documents", path, i, q.Query)
		}
	}
	return &qs, nil
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
		rel := make(map[string]bool, len(q.Relevant))
		for _, r := range q.Relevant {
			rel[r] = true
		}
		qr := QueryResult{
			Query:        q.Query,
			NumRelevant:  len(rel),
			NumRetrieved: len(ranked),
			RecallAtK:    RecallAtK(ranked, rel, k),
			PrecisionAtK: PrecisionAtK(ranked, rel, k),
			NDCGAtK:      NDCGAtK(ranked, rel, k),
			RR:           ReciprocalRank(ranked, rel),
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
