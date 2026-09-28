package importer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/samsar/curio/internal/client"
)

// Daemon is the daemon's bulk import, which *client.Client is.
type Daemon interface {
	ImportBookmarks(ctx context.Context, req client.ImportRequest) (*client.ImportResponse, error)
}

// BatchSize is how many bookmarks one import request carries: well under
// any proxy's limit even for thousand-bookmark folders, and often enough
// for progress that feels responsive.
const BatchSize = 500

// countBatchSize is how many URLs one counting request carries: about 1 MB
// of JSON, far under the daemon's 32 MiB limit.
const countBatchSize = 10_000

// Totals sum the daemon's answers to an import's batches.
type Totals struct {
	Created, Skipped, Filtered, JobsEnqueued int
	FilteredBy                               map[FilterReason]int
	Errors                                   []string
}

func (t *Totals) add(r *client.ImportResponse) {
	t.Created += r.Created
	t.Skipped += r.Skipped
	t.Filtered += r.Filtered
	t.JobsEnqueued += r.JobsEnqueued
	if t.FilteredBy == nil {
		t.FilteredBy = map[FilterReason]int{}
	}
	for reason, n := range r.FilteredBy {
		t.FilteredBy[FilterReason(reason)] += n
	}
	t.Errors = append(t.Errors, r.Errors...)
}

// Send imports bms into the daemon under source, BatchSize at a time,
// calling sent after each batch with how many have been sent and the
// totals so far, and returns the totals. The first batch that fails stops
// it: its error names the batch's range, and the totals are the batches'
// before it. A bookmark already saved is skipped, so sending again
// finishes an import cut short.
func Send(ctx context.Context, d Daemon, source string, bms []ParsedBookmark, sent func(n int, so Totals)) (Totals, error) {
	var totals Totals
	for i := 0; i < len(bms); i += BatchSize {
		end := min(i+BatchSize, len(bms))
		batch := make([]client.ImportBookmark, 0, end-i)
		for _, b := range bms[i:end] {
			batch = append(batch, client.ImportBookmark{URL: b.URL, Title: b.Title, FolderPath: b.FolderPath,
				Tags: b.Tags, SavedAt: b.SavedAt})
		}
		resp, err := d.ImportBookmarks(ctx, client.ImportRequest{Source: source, Bookmarks: batch})
		if err != nil {
			return totals, fmt.Errorf("batch %d-%d: %w", i, end, err)
		}
		totals.add(resp)
		if sent != nil {
			sent(end, totals)
		}
	}
	return totals, nil
}

// Candidates are what an import of some bookmarks asks the daemon to save.
type Candidates struct {
	// URLs are the distinct URLs that pass the filter, normalized as the
	// daemon saves them, in the order first seen.
	URLs []string
	// Filtered counts the bookmarks the filter drops, FilteredBy by
	// reason.
	Filtered   int
	FilteredBy map[FilterReason]int
}

// CandidatesOf classifies bms as the daemon does (Classify).
func CandidatesOf(bms []ParsedBookmark) Candidates {
	c := Candidates{FilteredBy: map[FilterReason]int{}}
	seen := make(map[string]bool, len(bms))
	for _, b := range bms {
		norm, why := Classify(b.URL)
		switch {
		case why != "":
			c.Filtered++
			c.FilteredBy[why]++
		case !seen[norm]:
			seen[norm] = true
			c.URLs = append(c.URLs, norm)
		}
	}
	return c
}

// Count is what importing a source's bookmarks would do to the library,
// as the daemon counts it without writing anything.
type Count struct {
	// Parsed is how many bookmarks the source has.
	Parsed int
	Candidates
	// Known: the daemon counted. False for a daemon that predates
	// counting, which leaves Created and NewURLs unknown.
	Known bool
	// Created is how many bookmarks the import would save: the candidates
	// the source hasn't saved before.
	Created int
	// NewURLs are the URLs new to the library, whose documents the import
	// would create and fetch, in order.
	NewURLs []string
}

// CountNew asks d what importing bms under source would do, writing
// nothing: it sends the distinct candidates alone, countBatchSize at a
// time, and sums the answers, which is exact because no URL is in two
// requests. A daemon that refuses the dry run as a field it doesn't know
// gives a Count that isn't Known, not an error.
func CountNew(ctx context.Context, d Daemon, source string, bms []ParsedBookmark) (Count, error) {
	c := Count{Parsed: len(bms), Candidates: CandidatesOf(bms), Known: true}
	for i := 0; i < len(c.URLs); i += countBatchSize {
		end := min(i+countBatchSize, len(c.URLs))
		batch := make([]client.ImportBookmark, 0, end-i)
		for _, u := range c.URLs[i:end] {
			batch = append(batch, client.ImportBookmark{URL: u})
		}
		resp, err := d.ImportBookmarks(ctx, client.ImportRequest{Source: source, Bookmarks: batch, DryRun: true})
		switch {
		case olderDaemon(err):
			return Count{Parsed: c.Parsed, Candidates: c.Candidates}, nil
		case err != nil:
			return c, fmt.Errorf("count the new bookmarks: %w", err)
		}
		c.Created += resp.Created
		c.NewURLs = append(c.NewURLs, resp.NewURLs...)
	}
	return c, nil
}

// olderDaemon reports whether err is a daemon refusing dry_run as a field
// it doesn't know: one from before it counted.
func olderDaemon(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusBadRequest &&
		strings.Contains(apiErr.Problem.Detail, `unknown field "dry_run"`)
}
