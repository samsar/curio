// Package rerank reorders the head of a search's ranking with a writing
// model, the reranker role's: it reads each of the first N documents the
// engine ranked (its title, what it was saved as, its site, its summary
// and its best passage) and asks the model which the question wants, in
// one listwise call or one yes-or-no call a candidate, routed by the
// plan's kind. Deterministic code decides what of an answer to trust: a
// strict decoding, scores from the provider's token probabilities, blends
// with the first ranking, and a fitness check the model must pass before
// any answer is used. Every failure keeps the first ranking, with a
// status that says why. See docs/decisions.md "Reranking: a reranker role
// over the ranking's head, routed by the plan's kind".
//
// rerank reads search's hits and never the reverse: the engine stays free
// of language models.
package rerank

import (
	"context"
	"fmt"
	"strings"

	"github.com/samsar/curio/internal/docmeta"
	"github.com/samsar/curio/internal/search"
	"github.com/samsar/curio/internal/store"
)

// Candidate is one of a ranking's first documents as the reranker reads
// it: what it is, what it was saved as, its summary when one reads its
// current extraction, and the passage the engine ranked best for it.
type Candidate struct {
	ID string
	// Rank is its first-stage rank, from 1.
	Rank  int
	State store.DocState
	// Title is its own title, else the title it was saved as.
	Title string
	// SavedAs are the titles it was saved as that differ from Title.
	SavedAs []string
	// Site is its site's name, else its host; Author its author, else
	// its summary's.
	Site   string
	Author string
	// Folders are the folders it was saved in.
	Folders []string
	Summary *Summary
	// Passage is the text of the chunk the engine ranked best for it; ""
	// for a document only the document lists found.
	Passage string
}

// Summary is what of a document's summary a candidate shows.
type Summary struct {
	Kind         string
	About        string
	Descriptions []string
	Entities     []string
	Claims       []string
}

// Textless reports whether c has nothing of its page to read: no summary
// and no passage, as a failed, dead or pending page has, or a fetched one
// with neither.
func (c Candidate) Textless() bool { return c.Summary == nil && c.Passage == "" }

// note says why c's page can't be read: dead, failed (docmeta.Unavailable),
// or not read yet; "" for a page curio has read.
func (c Candidate) note() string {
	if c.State == store.DocStatePending {
		return "not read yet"
	}
	return docmeta.Unavailable(string(c.State))
}

// FieldsReader reads documents' fields in one read (store.DocumentIndex).
type FieldsReader interface {
	FieldsOf(ctx context.Context, tenantID string, ids []string) (map[string]store.DocumentFields, error)
}

// SummaryReader reads documents' current summaries in one read
// (store.SummaryStore).
type SummaryReader interface {
	CurrentSummaries(ctx context.Context, tenantID string, ids []string) (map[string]*store.Summary, error)
}

// Readers are the stores ReadCandidates reads.
type Readers struct {
	Fields    FieldsReader
	Summaries SummaryReader
}

// ReadCandidates builds the candidates of hits, the first of a ranking in
// its order, with one read of their fields and one of their summaries,
// whatever their number: what the API and cmd/searcheval rerank alike.
func ReadCandidates(ctx context.Context, r Readers, tenantID string, hits []search.Hit) ([]Candidate, error) {
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.Document.ID
	}
	fields, err := r.Fields.FieldsOf(ctx, tenantID, ids)
	if err != nil {
		return nil, fmt.Errorf("read the candidates' fields: %w", err)
	}
	summaries, err := r.Summaries.CurrentSummaries(ctx, tenantID, ids)
	if err != nil {
		return nil, fmt.Errorf("read the candidates' summaries: %w", err)
	}
	out := make([]Candidate, len(hits))
	for i, h := range hits {
		out[i] = candidate(i+1, h, fields[h.Document.ID], summaries[h.Document.ID])
	}
	return out, nil
}

// candidate is hit, ranked rank, as its fields f and its summary s (nil
// for none) show it. A document whose fields aren't derived yet is shown
// by its own title and host.
func candidate(rank int, hit search.Hit, f store.DocumentFields, s *store.Summary) Candidate {
	doc := hit.Document
	if f.Host == "" {
		f.Host = docmeta.Host(doc.URL)
	}
	if f.Title == "" && doc.Title != nil {
		f.Title = *doc.Title
	}
	saved := docmeta.SavedTitles(f.Title, f.BookmarkTitles)
	title := f.Title
	if title == "" && len(saved) > 0 {
		title, saved = saved[0], saved[1:]
	}
	c := Candidate{ID: doc.ID, Rank: rank, State: doc.State, Title: title, SavedAs: saved,
		Site: docmeta.SiteOrHost(f.SiteName, f.Host), Author: f.Author, Folders: lines(f.Folders)}
	if s != nil {
		c.Summary = &Summary{Kind: string(s.Kind), About: s.About, Descriptions: s.Descriptions,
			Entities: s.Entities, Claims: s.Claims}
		if c.Author == "" {
			c.Author = s.Author
		}
	}
	if len(hit.Chunks) > 0 {
		c.Passage = hit.Chunks[0].Text
	}
	return c
}

// lines are the entries of a one-a-line list, none for an empty one.
func lines(list string) []string {
	if list == "" {
		return nil
	}
	return strings.Split(list, "\n")
}
